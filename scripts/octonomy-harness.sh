#!/bin/sh
# Reusable Octonomy container harness.
#
# Boots a real Octonomy server (Postgres + the published GHCR image), applies
# migrations, mints a service token, and asserts the environment is genuinely
# usable -- then writes the resulting credentials to an env file that both SDK
# version lines source.
#
# `docker run` alone is not enough. Every step below exists because omitting it
# produces an environment that looks healthy and silently fails, or worse,
# passes tests vacuously. The comments name which one.
#
# Usage:
#   scripts/octonomy-harness.sh up      # boot, verify, write the env file
#   scripts/octonomy-harness.sh down    # tear down containers and the network
#   scripts/octonomy-harness.sh logs    # dump container logs
#   scripts/octonomy-harness.sh env     # print the env file path
#
# POSIX sh on purpose: the Go 1.13 compat line and the modern /v2 line both
# invoke this, and neither may grow a bootstrap of its own.

set -eu

# --- Configuration -------------------------------------------------------------
# Every value is overridable so a caller can run two harnesses side by side, but
# the defaults are what CI uses.

# Pinned to an exact release, never a moving tag. `:latest` and `:edge` both
# exist in this registry and both would make a green run unreproducible.
# For a fully immutable pin, set this to the digest:
#   ghcr.io/octoverse-id/octonomy@sha256:58cd50931e5014320d3aef716ddb8dc79cc1a1101159c97d19a16b00b010bac2
HARNESS_IMAGE="${OCTONOMY_HARNESS_IMAGE:-ghcr.io/octoverse-id/octonomy:3.1.0}"
POSTGRES_IMAGE="${OCTONOMY_HARNESS_POSTGRES_IMAGE:-postgres:16}"

PREFIX="${OCTONOMY_HARNESS_PREFIX:-octonomy-harness}"
NET="${PREFIX}-net"
DB="${PREFIX}-db"
APP="${PREFIX}-app"

PORT="${OCTONOMY_HARNESS_PORT:-8000}"
BASE_URL="http://127.0.0.1:${PORT}"

ENV_FILE="${OCTONOMY_HARNESS_ENV_FILE:-.octonomy-harness.env}"

# Bounded readiness, in two parts. Both are needed for the ceiling to be real.
#
# READY_TIMEOUT is a true wall-clock ceiling per gate: the loops below run
# against a deadline, not an attempt count, so a slow probe eats into the budget
# instead of silently extending it. (An attempt-based loop advertising
# ATTEMPTS*INTERVAL is wrong the moment a probe takes longer than the interval.)
# Each probe and each inter-probe sleep is clamped to the remaining budget, so
# the gate cannot overshoot by a trailing probe either.
#
# PROBE_TIMEOUT bounds each individual probe. Without it a single request can
# block forever and the deadline is never re-evaluated -- a server that accepts
# the TCP connection but never completes the response does exactly that (a
# wedged gunicorn worker, or a stalled cursor inside /health/ready, which is all
# that view does). Verified: an unbounded `curl` against such a socket was still
# waiting when killed, while `--max-time` returns exit 28 on schedule. Note the
# ordinary startup window is NOT this case -- Docker's published port resets the
# connection until the app binds, so the loop advances normally.
#
# The poll interval is a sleep *between* probes; no gate is ever a blind
# `sleep N && assume up`.
READY_TIMEOUT="${OCTONOMY_HARNESS_READY_TIMEOUT:-120}"
READY_INTERVAL="${OCTONOMY_HARNESS_READY_INTERVAL:-2}"
PROBE_TIMEOUT="${OCTONOMY_HARNESS_PROBE_TIMEOUT:-5}"

# Bound for the real API calls below. Larger than a probe: a namespaced create
# also writes an audit row and an outbox event.
REQUEST_TIMEOUT="${OCTONOMY_HARNESS_REQUEST_TIMEOUT:-30}"

# Real values, not placeholders. config/settings.py:49 and :352 refuse to boot
# when DJANGO_DEBUG=false (the image default) and either the secret key or the
# token pepper is empty or still the documented local-dev default.
#
# The pepper must be IDENTICAL in the mint container and the app container:
# it salts the token hash, so a mismatch yields a token that authenticates
# nowhere, surfacing as a blanket 401 that looks like a bad token rather than a
# bad harness.
SECRET_KEY="${OCTONOMY_HARNESS_SECRET_KEY:-harness-secret-not-a-real-secret}"
TOKEN_PEPPER="${OCTONOMY_HARNESS_TOKEN_PEPPER:-harness-pepper-not-a-real-pepper}"

# The identity the SDK test suites authenticate as.
TENANT_ID="${OCTONOMY_HARNESS_TENANT_ID:-harness-tenant}"
APPLICATION_ID="${OCTONOMY_HARNESS_APPLICATION_ID:-harness-app}"
NAMESPACE_TYPE="${OCTONOMY_HARNESS_NAMESPACE_TYPE:-merchant}"
NAMESPACE_ID="${OCTONOMY_HARNESS_NAMESPACE_ID:-harness-merchant}"

PG_DB=octonomy
PG_USER=octonomy
PG_PASSWORD=octonomy
DATABASE_URL="postgres://${PG_USER}:${PG_PASSWORD}@${DB}:5432/${PG_DB}"

# --- Output helpers ------------------------------------------------------------

log() { printf 'harness: %s\n' "$*" >&2; }

fail() {
    if [ -n "${GITHUB_ACTIONS:-}" ]; then
        printf '::error::harness: %s\n' "$*" >&2
    else
        printf 'harness: ERROR: %s\n' "$*" >&2
    fi
    exit 1
}

# --- Docker plumbing -----------------------------------------------------------

require_docker() {
    command -v docker >/dev/null 2>&1 || fail "docker is not installed or not on PATH"
    docker info >/dev/null 2>&1 || fail "the docker daemon is not reachable"
}

container_running() {
    [ -n "$(docker ps --filter "name=^${1}$" --filter status=running --format '{{.Names}}')" ]
}

# Every Octonomy container -- the two one-shots and the long-running app -- gets
# the same settings environment. Divergence here is how a `manage.py check` that
# passes in the mint container fails in the app container.
octonomy_env_args() {
    printf '%s\n' \
        "-e" "DJANGO_SECRET_KEY=${SECRET_KEY}" \
        "-e" "SERVICE_TOKEN_PEPPER=${TOKEN_PEPPER}" \
        "-e" "DATABASE_URL=${DATABASE_URL}" \
        "-e" "ALLOWED_HOSTS=localhost,127.0.0.1,0.0.0.0" \
        "-e" "OCTONOMY_NAMESPACE_WRITE_ENABLED=true" \
        "-e" "LOG_LEVEL=WARNING"
}

# Run a one-shot management command inside the harness network.
oneshot() {
    # shellcheck disable=SC2046 # deliberate word splitting of the env flag list
    docker run --rm --network "$NET" $(octonomy_env_args) "$HARNESS_IMAGE" "$@"
}

dump_logs() {
    for name in "$DB" "$APP"; do
        if docker ps -a --filter "name=^${name}$" --format '{{.Names}}' | grep -q .; then
            printf '\n===== docker logs %s =====\n' "$name" >&2
            docker logs --tail "${OCTONOMY_HARNESS_LOG_TAIL:-100}" "$name" >&2 2>&1 || true
        fi
    done
}

# --- Bounded gates -------------------------------------------------------------

# Seconds left before $1 (an epoch deadline). Negative once it has passed.
budget_until() { echo $(($1 - $(date +%s))); }

# The smaller of two integers.
min_int() {
    if [ "$1" -lt "$2" ]; then echo "$1"; else echo "$2"; fi
}

# Sleep at most $2 seconds, and never past the deadline in $1. Keeps the poll
# interval from being the thing that overshoots the ceiling.
sleep_within() {
    _left="$(budget_until "$1")"
    [ "$_left" -gt 0 ] || return 0
    sleep "$(min_int "$2" "$_left")"
}

wait_for_postgres() {
    log "waiting for Postgres (bound: ${READY_TIMEOUT}s)"
    deadline=$(($(date +%s) + READY_TIMEOUT))
    while :; do
        container_running "$DB" || fail "the Postgres container exited during startup"
        # Clamp the probe to the budget that is actually left. A fixed probe
        # timeout started just inside the deadline would overshoot the ceiling
        # this function just advertised. Also keeps the value >= 1: `pg_isready
        # -t 0` means wait forever, which is the bug being fixed.
        left="$(budget_until "$deadline")"
        [ "$left" -gt 0 ] || break
        if docker exec "$DB" pg_isready -U "$PG_USER" -d "$PG_DB" \
            -t "$(min_int "$PROBE_TIMEOUT" "$left")" >/dev/null 2>&1; then
            log "Postgres accepting connections"
            return 0
        fi
        sleep_within "$deadline" "$READY_INTERVAL"
    done
    fail "Postgres did not become ready within ${READY_TIMEOUT}s"
}

wait_for_ready() {
    log "waiting for /health/ready (bound: ${READY_TIMEOUT}s)"
    deadline=$(($(date +%s) + READY_TIMEOUT))
    while :; do
        container_running "$APP" || fail "the Octonomy container exited during startup"
        # Same clamp as above, and for the same reason: `curl --max-time 0`
        # also means no limit at all.
        left="$(budget_until "$deadline")"
        [ "$left" -gt 0 ] || break
        if curl -fsS --max-time "$(min_int "$PROBE_TIMEOUT" "$left")" \
            "${BASE_URL}/health/ready" >/dev/null 2>&1; then
            log "Octonomy reports ready"
            return 0
        fi
        sleep_within "$deadline" "$READY_INTERVAL"
    done
    fail "/health/ready did not return 200 within ${READY_TIMEOUT}s"
}

# --- The assertion that makes the harness worth having -------------------------

# /health/ready only opens a database cursor (octonomy/core/views.py:22) -- it
# reports ready on an EMPTY, unmigrated database. It is a liveness gate, not a
# proof the environment works. So prove it: perform the single most
# failure-prone operation the SDK suites depend on and require a real 201.
#
# Two ways this silently degrades if the harness is wrong:
#
#   1. OCTONOMY_NAMESPACE_WRITE_ENABLED defaults to "false"
#      (config/settings.py:393) and is parsed strictly. Without it every
#      namespaced write returns 403 namespaced_writes_disabled -- and a test
#      suite that only asserts "no transport error" passes vacuously while
#      testing nothing about the namespace axis.
#   2. The service token needs a grant that REACHES the namespace partition.
#      A plain grant (the shape `manage.py seed_demo` mints) has
#      namespace_type=NULL and namespace_wildcard=False, and
#      grant_matches_namespace (octonomy/core/auth.py:206-213) then matches
#      global scope ONLY -- every namespaced request 403s. Hence
#      --namespace-wildcard when minting.
#
# Note the request must also carry application_id: octonomy/core/auth.py:197-198
# rejects a namespaced request that does not name its parent application, even
# when the grant itself is tenant-wide.
assert_namespaced_write() {
    log "asserting a namespaced write genuinely succeeds"
    body_file="$(mktemp)"
    code="$(
        curl -sS -o "$body_file" -w '%{http_code}' \
            --max-time "$REQUEST_TIMEOUT" \
            -X POST "${BASE_URL}/api/v2/vocabularies" \
            -H "Authorization: Bearer ${SERVICE_TOKEN}" \
            -H "X-Tenant-ID: ${TENANT_ID}" \
            -H "X-Namespace-Type: ${NAMESPACE_TYPE}" \
            -H "X-Namespace-ID: ${NAMESPACE_ID}" \
            -H "Content-Type: application/json" \
            -d "{\"application_id\":\"${APPLICATION_ID}\",\"name\":\"Harness Probe\",\"slug\":\"harness-probe\"}"
    )"
    payload="$(cat "$body_file")"
    rm -f "$body_file"

    if [ "$code" != "201" ]; then
        log "expected 201, got ${code}: ${payload}"
        if [ "$code" = "000" ]; then
            fail "the namespaced write probe got no response within ${REQUEST_TIMEOUT}s"
        fi
        case "$payload" in
            *namespaced_writes_disabled*)
                fail "namespaced writes are disabled -- OCTONOMY_NAMESPACE_WRITE_ENABLED did not reach the container"
                ;;
            *)
                fail "the namespaced write probe failed with HTTP ${code}"
                ;;
        esac
    fi

    # A 201 is necessary but not sufficient: the row must actually carry the
    # namespace. A server that accepted the write but persisted it globally
    # would return 201 with null namespace fields, and every namespace
    # assertion downstream would be testing global behaviour under a
    # namespaced name.
    case "$payload" in
        *"\"namespace_type\":\"${NAMESPACE_TYPE}\""*) ;;
        *) fail "the write returned 201 but did not persist namespace_type=${NAMESPACE_TYPE}: ${payload}" ;;
    esac
    case "$payload" in
        *"\"namespace_id\":\"${NAMESPACE_ID}\""*) ;;
        *) fail "the write returned 201 but did not persist namespace_id=${NAMESPACE_ID}: ${payload}" ;;
    esac

    log "namespaced write confirmed (201, namespace persisted)"
}

# --- Commands ------------------------------------------------------------------

cmd_down() {
    docker rm -f "$APP" >/dev/null 2>&1 || true
    docker rm -f "$DB" >/dev/null 2>&1 || true
    docker network rm "$NET" >/dev/null 2>&1 || true
    rm -f "$ENV_FILE"
    log "torn down"
}

cmd_up() {
    require_docker
    command -v curl >/dev/null 2>&1 || fail "curl is not installed or not on PATH"

    # Any failure from here on dumps container logs before exiting -- a bare
    # "exit 1" from CI with the containers already reaped is undebuggable.
    trap 'dump_logs; cmd_down' EXIT

    # Idempotent: a previous interrupted run leaves containers behind, and
    # `docker run --name` fails on a name clash rather than reusing it.
    docker rm -f "$APP" >/dev/null 2>&1 || true
    docker rm -f "$DB" >/dev/null 2>&1 || true
    docker network rm "$NET" >/dev/null 2>&1 || true

    log "using image ${HARNESS_IMAGE}"
    docker network create "$NET" >/dev/null || fail "could not create the docker network ${NET}"

    log "starting Postgres"
    docker run -d --name "$DB" --network "$NET" \
        -e "POSTGRES_DB=${PG_DB}" \
        -e "POSTGRES_USER=${PG_USER}" \
        -e "POSTGRES_PASSWORD=${PG_PASSWORD}" \
        "$POSTGRES_IMAGE" >/dev/null || fail "could not start Postgres"
    wait_for_postgres

    # The image entrypoint runs `manage.py check` but NOT `migrate`
    # (docker-entrypoint.sh, and the Dockerfile says so explicitly: migrations
    # are a deploy step). Without this the app boots, /health/ready returns 200
    # because a cursor opens fine, and the first API call dies on a missing
    # relation.
    log "applying migrations"
    oneshot python manage.py migrate --noinput >/dev/null || fail "migrations failed"

    # --namespace-wildcard is load-bearing; see assert_namespaced_write.
    log "minting a service token"
    mint_output="$(
        oneshot python manage.py create_service_token \
            --name octonomy-go-harness \
            --tenant "$TENANT_ID" \
            --namespace-wildcard \
            --scope tags:read \
            --scope tags:write \
            --scope audit:read
    )" || fail "could not mint a service token"
    SERVICE_TOKEN="$(printf '%s\n' "$mint_output" | sed -n 's/^Token: //p')"
    [ -n "$SERVICE_TOKEN" ] || fail "could not parse a token out of create_service_token output: ${mint_output}"

    log "starting Octonomy on port ${PORT}"
    # shellcheck disable=SC2046 # deliberate word splitting of the env flag list
    docker run -d --name "$APP" --network "$NET" \
        $(octonomy_env_args) \
        -p "${PORT}:8000" \
        "$HARNESS_IMAGE" >/dev/null || fail "could not start the Octonomy container"
    wait_for_ready

    assert_namespaced_write

    # Remove first, then create under a restrictive umask: `>` truncates an
    # existing file but leaves its old mode, so a previously world-readable
    # env file would keep leaking the token it now holds.
    rm -f "$ENV_FILE"
    umask 077
    cat > "$ENV_FILE" <<ENV_EOF
# Generated by scripts/octonomy-harness.sh -- do not commit.
# Source this, or let the CI composite action load it, before running the
# integration suites. OCTONOMY_TEST_BASE_URL is the gate: when it is empty the
# suites skip rather than fail.
OCTONOMY_TEST_BASE_URL=${BASE_URL}
OCTONOMY_TEST_TOKEN=${SERVICE_TOKEN}
OCTONOMY_TEST_TENANT_ID=${TENANT_ID}
OCTONOMY_TEST_APPLICATION_ID=${APPLICATION_ID}
OCTONOMY_TEST_NAMESPACE_TYPE=${NAMESPACE_TYPE}
OCTONOMY_TEST_NAMESPACE_ID=${NAMESPACE_ID}
ENV_EOF

    trap - EXIT
    log "ready at ${BASE_URL}; credentials written to ${ENV_FILE}"
}

case "${1:-up}" in
    up) cmd_up ;;
    down) cmd_down ;;
    logs) dump_logs ;;
    env) printf '%s\n' "$ENV_FILE" ;;
    *) fail "unknown command '${1}' (expected: up, down, logs, env)" ;;
esac
