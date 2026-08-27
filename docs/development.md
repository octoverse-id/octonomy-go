# Development

## Setup

```bash
git clone https://github.com/octoverse-id/octonomy-go.git
cd octonomy-go
go build ./...
make test
```

Requires Go 1.24+. There are **no runtime dependencies** — keep `go.mod` free of a runtime `require`
block.

## Quality gates

```bash
make fmt-check   # gofmt -l . (no output = clean)
make vet         # go vet ./...
make lint        # golangci-lint (if installed)
make test        # go test -race -cover ./...
make cover       # prints total coverage
make examples    # go build ./examples/...
make smoke       # integration smoke test against a booted server (see below)
make check       # fmt-check + vet + build (fast pre-push gate)
make release-check  # the full pre-release gate
```

Optional local tools (CI installs them automatically):

```bash
# linter
go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
# vulnerability scanner
go install golang.org/x/vuln/cmd/govulncheck@latest
```

## Testing approach

Tests use `net/http/httptest` to stand up a fake Octonomy and assert the wire contract:

- **Server side** (inside the handler, with `t.Errorf` — handlers run on another goroutine): request
  method, path, `Authorization` and `X-Tenant-ID` headers, query params, and request body.
- **Client side** (in the test goroutine): the decoded return value, the `{data, pagination}` envelope,
  and error decoding via `IsNotFound`/`IsConflict`/`IsValidation`.

`newTestClient(t, handler)` in `octonomy_test.go` is the shared helper. Keep new code covered and run
with `-race`.

Canned single-resource responses go through `writeData`, which wraps the body in the server's
`{"data": {...}}` envelope. Handlers that returned the bare object matched the vendored spec rather
than the server, and that mismatch hid a real defect for the life of the SDK: every
`Create`/`Get`/`Update` decoded to a zero-valued struct with a nil error against a real server
([#32](https://github.com/octoverse-id/octonomy-go/issues/32)). Use `writeJSON` only for bodies you
mean to send verbatim — list envelopes and error envelopes.

That is also the structural limit of this suite, and worth internalizing before adding a resource:
**a unit test cannot catch a fixture-versus-server divergence**, because it asserts the client
against the fixtures it ships with. Both sides can be wrong together and stay green. Anything that
depends on the server's real response shape needs the smoke test below.

### Integration smoke test

`integration_test.go` (build tag `integration`) is the only test that talks to a real server. It is
six assertions, deliberately — the full suite is
[#17](https://github.com/octoverse-id/octonomy-go/issues/17): the single-resource `{data}` envelope
on a write and on a read, an update, the `{data, pagination}` list envelope on both resources, and
one real error envelope. It gates on `OCTONOMY_TEST_BASE_URL` and skips when that is empty, so
`go test ./...` stays hermetic.

```bash
make dev-server   # boots a real Octonomy, writes .octonomy-harness.env
make smoke        # sources the env file and runs the smoke test
make dev-server-down
```

CI runs it in the `smoke` job against the pinned container image, with `OCTONOMY_SMOKE_REQUIRED=1` so
a missing base URL fails instead of skipping — a skip would be a green job that asserted nothing. The
job runs `make smoke`, so CI and your laptop execute identical logic, guard included.

The job is no longer advisory: it fails the PR. It does not yet *block the merge* — that needs its
check context, **`integration smoke test`** (the job's display name, not the `smoke` job id), added
to main's branch-protection required contexts, which currently list `lint`, `test (1.24)`,
`test (1.25)`, and `vuln`.

## Running against a real Octonomy

`make dev-server` boots a complete, verified Octonomy in one command. It needs Docker and `curl`,
and nothing else:

```bash
make dev-server        # boot, verify, write .octonomy-harness.env  (~40s)
make dev-server-logs   # dump container logs
make dev-server-down   # tear everything down
```

It starts Postgres 16 and the pinned `ghcr.io/octoverse-id/octonomy:3.1.0` image on a private Docker
network, applies migrations, mints a service token, waits for `/health/ready`, and then **proves the
environment actually works** before reporting success. Credentials land in `.octonomy-harness.env`
(git-ignored, mode 600):

| Variable | Meaning |
|---|---|
| `OCTONOMY_TEST_BASE_URL` | Server root. Integration suites gate on this — when it is empty they skip |
| `OCTONOMY_TEST_TOKEN` | Bearer token with `tags:read`, `tags:write`, `audit:read` |
| `OCTONOMY_TEST_TENANT_ID` | `X-Tenant-ID` for every request |
| `OCTONOMY_TEST_APPLICATION_ID` | Parent application. Required on namespaced requests |
| `OCTONOMY_TEST_NAMESPACE_TYPE` / `_ID` | The `X-Namespace-*` pair to scope v2 calls with |

```bash
make dev-server
set -a; . ./.octonomy-harness.env; set +a

OCTONOMY_BASE_URL="$OCTONOMY_TEST_BASE_URL" \
OCTONOMY_TOKEN="$OCTONOMY_TEST_TOKEN" \
OCTONOMY_TENANT_ID="$OCTONOMY_TEST_TENANT_ID" \
go run ./examples/quickstart
```

Everything is overridable — `OCTONOMY_HARNESS_PORT`, `OCTONOMY_HARNESS_PREFIX`,
`OCTONOMY_HARNESS_IMAGE`, `OCTONOMY_HARNESS_ENV_FILE` and friends — so two harnesses can run side by
side. See the header of [`scripts/octonomy-harness.sh`](../scripts/octonomy-harness.sh).

### Why it is a script and not `docker run`

`docker run` alone produces an environment that looks healthy and silently fails. Four things the
harness does that a naive bootstrap does not:

- **Migrations.** The image entrypoint runs `manage.py check`, never `migrate`. Without an explicit
  migrate step the server boots and `/health/ready` returns 200 — that probe only opens a database
  cursor — and the first real API call dies on a missing relation.
- **`OCTONOMY_NAMESPACE_WRITE_ENABLED=true`.** It defaults to `false` on the server and is parsed
  strictly. Left off, every namespaced write returns `403 namespaced_writes_disabled`, and a suite
  that only checks for transport errors passes while testing nothing about the namespace axis.
- **`--namespace-wildcard` on the token.** A token minted the ordinary way carries a global-only
  grant, and *every* namespaced request 403s — including reads. This is the shape `seed_demo` mints,
  so it is an easy trap to copy.
- **A real write, asserted.** After readiness the harness POSTs a namespaced vocabulary and requires
  a `201` whose response actually carries `namespace_type`/`namespace_id`. A 201 with null namespace
  fields would mean the row persisted globally, and every downstream namespace assertion would be
  testing global behaviour under a namespaced name.

Both version lines call the same script, so the Go 1.13 compat line and the modern `/v2` line cannot
drift apart on setup. CI reaches it through the `.github/actions/octonomy-harness` composite action.

### Troubleshooting

- `error getting credentials … docker-credential-desktop.exe: exec format error` — a Docker
  Desktop-on-WSL config problem, not a harness one. The image is public, so point Docker at a
  credential-free config for the run: `mkdir -p /tmp/dc && echo '{}' > /tmp/dc/config.json && export DOCKER_CONFIG=/tmp/dc`.
- Port 8000 already taken: `OCTONOMY_HARNESS_PORT=8100 make dev-server`.
- A failed boot prints container logs automatically and tears itself down. To inspect a *running*
  harness, use `make dev-server-logs`.

## Keeping the contract current

`docs/openapi.yaml` is vendored from the Octonomy server. When targeting a new server contract,
refresh it (regenerate on the server with `make openapi`, copy the file here), reconcile any type
changes, and note the server version in [versioning.md](versioning.md).
