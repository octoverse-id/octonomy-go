#!/bin/sh
# Fetch the Octonomy server's published REST contract for the drift gate.
#
# Pulls three files out of octoverse-id/octonomy into a directory that
# `tools/contractdrift -upstream DIR` then reads:
#
#   docs/openapi.yaml        -> openapi.yaml     the /api/v1 contract
#   docs/openapi-v2.yaml     -> openapi-v2.yaml  the /api/v2 contract
#   octonomy/core/errors.py  -> errors.py        the error-code registry
#
# The third file is not decoration. The contracts type the error envelope's
# `code` as a bare string, so every code the server can return is invisible to a
# schema comparison; core/errors.py is the only machine-readable list of them.
#
# MECHANISM, and its credential. This is option A of the three the issue weighed:
# a contents read of the server repository. Both repositories are public, so the
# fetch needs no credential at all -- `gh` uses GITHUB_TOKEN when it has one,
# purely for the higher rate limit, and the curl fallback is unauthenticated.
# The alternatives were rejected for cost, not correctness: pulling the GHCR
# image to run `manage.py spectacular` needs the container for a file that is
# already in the repository, and booting one to read /api/schema/ tests the image
# the harness pins rather than what the server publishes today -- which is the
# one thing this gate is for.
#
#   scripts/contract-fetch.sh <dest-dir>
#
# Environment:
#   OCTONOMY_CONTRACT_REPO   default octoverse-id/octonomy
#   OCTONOMY_CONTRACT_REF    default main. The server's default branch, not its
#                            latest tag: a contract that has landed but not
#                            shipped is exactly what this gate wants to see
#                            early, and the report names the resolved commit.
#
# POSIX sh, like scripts/octonomy-harness.sh, and for the same reason: both SDK
# version lines invoke these and neither may grow a bootstrap of its own.

set -eu

REPO="${OCTONOMY_CONTRACT_REPO:-octoverse-id/octonomy}"
REF="${OCTONOMY_CONTRACT_REF:-main}"

ATTEMPTS="${OCTONOMY_CONTRACT_ATTEMPTS:-3}"
RETRY_SLEEP="${OCTONOMY_CONTRACT_RETRY_SLEEP:-5}"
# Bounded like every request the harness makes. An unbounded fetch against a
# server that accepts and never answers would sit until the job timeout.
TIMEOUT="${OCTONOMY_CONTRACT_TIMEOUT:-30}"

log() { printf 'contract-fetch: %s\n' "$*" >&2; }

fail() {
    if [ -n "${GITHUB_ACTIONS:-}" ]; then
        printf '::error::contract-fetch: %s\n' "$*" >&2
    else
        printf 'contract-fetch: ERROR: %s\n' "$*" >&2
    fi
    exit 1
}

DEST="${1:-}"
[ -n "$DEST" ] || fail "usage: scripts/contract-fetch.sh <dest-dir>"
mkdir -p "$DEST"

# `gh` when it is installed AND authenticated, curl otherwise. The check is
# `gh auth status` rather than `command -v gh`, because an installed but
# unauthenticated gh fails every call -- and a fallback that never runs is a
# fallback that is not there.
if command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; then
    MODE=gh
elif command -v curl >/dev/null 2>&1; then
    MODE=curl
else
    fail "neither an authenticated gh nor curl is available"
fi
log "fetching $REPO@$REF via $MODE"

# resolve_sha_once prints the commit the ref currently points at.
resolve_sha_once() {
    case "$MODE" in
        gh)
            gh api "repos/$REPO/commits/$REF" --jq .sha
            ;;
        curl)
            # No jq dependency: the commit object's own sha is the first "sha"
            # key in the response, and the -m1 makes that explicit rather than
            # incidental.
            curl -fsS --max-time "$TIMEOUT" "https://api.github.com/repos/$REPO/commits/$REF" \
                | grep -m1 '"sha"' \
                | sed -E 's/.*"sha"[[:space:]]*:[[:space:]]*"([0-9a-f]{7,40})".*/\1/'
            ;;
    esac
}

# fetch_once PATH DEST-FILE
#
# Every file is fetched at the RESOLVED COMMIT, never at the ref. Three requests
# against a moving branch can straddle a push and produce a snapshot that never
# existed -- v1 from before it, errors.py from after -- which shows up as drift
# nobody can reproduce, or worse, hides real drift. It also makes source.txt
# true: the commit in the report is the commit the bytes came from.
fetch_once() {
    case "$MODE" in
        gh)
            # stderr is NOT suppressed: gh reports a 404, a rate limit, and a
            # renamed path in three different ways, and the retry loop below
            # would otherwise reduce all three to "attempt 1/3 failed".
            gh api -H "Accept: application/vnd.github.raw" \
                "repos/$REPO/contents/$1?ref=$SHA" >"$2"
            ;;
        curl)
            curl -fsS --max-time "$TIMEOUT" \
                "https://raw.githubusercontent.com/$REPO/$SHA/$1" -o "$2"
            ;;
    esac
}

# fetch PATH DEST-FILE EXPECTED-SUBSTRING
#
# Retried, because a network failure is not drift. The gate treats "could not
# compare" and "compared and found a difference" as different outcomes, and the
# retry is what keeps a single dropped connection out of the second bucket.
#
# The content assertion matters as much as the retry: a proxy error page, a
# redirect to a login form, and a renamed path all arrive as a perfectly
# successful download of the wrong bytes.
fetch() {
    path="$1"
    dest="$2"
    expect="$3"
    attempt=1
    while [ "$attempt" -le "$ATTEMPTS" ]; do
        if fetch_once "$path" "$dest" && [ -s "$dest" ] && grep -q "$expect" "$dest"; then
            log "fetched $path ($(wc -c <"$dest" | tr -d ' ') bytes)"
            return 0
        fi
        log "attempt $attempt/$ATTEMPTS failed for $path"
        attempt=$((attempt + 1))
        # An `if`, not `[ ... ] && sleep`: under `set -e` that list exits the
        # script the moment the test is false, which is the final attempt --
        # turning the last retry into a silent exit 1 with no error line.
        if [ "$attempt" -le "$ATTEMPTS" ]; then
            sleep "$RETRY_SLEEP"
        fi
    done
    rm -f "$dest"
    fail "could not fetch $path from $REPO@$SHA -- the comparison did not run"
}

# Resolve first, then fetch -- see fetch_once. An unresolvable ref fails the run
# rather than falling back to the moving branch: a snapshot this script cannot
# name is one nobody can reproduce, and "could not compare" is a different
# outcome from "compared and found a difference".
SHA=""
attempt=1
while [ "$attempt" -le "$ATTEMPTS" ]; do
    SHA=$(resolve_sha_once 2>/dev/null || true)
    case "$SHA" in
        *[!0-9a-f]* | "") SHA="" ;;
        *) break ;;
    esac
    log "attempt $attempt/$ATTEMPTS failed to resolve $REF"
    attempt=$((attempt + 1))
    if [ "$attempt" -le "$ATTEMPTS" ]; then
        sleep "$RETRY_SLEEP"
    fi
done
[ -n "$SHA" ] || fail "could not resolve $REPO@$REF to a commit -- the comparison did not run"
log "resolved $REF to $SHA"

fetch "docs/openapi.yaml" "$DEST/openapi.yaml" "^openapi:"
fetch "docs/openapi-v2.yaml" "$DEST/openapi-v2.yaml" "^openapi:"
fetch "octonomy/core/errors.py" "$DEST/errors.py" "class DomainError"

printf '%s@%s\n' "$REPO" "$SHA" >"$DEST/source.txt"
log "source: $(cat "$DEST/source.txt")"
