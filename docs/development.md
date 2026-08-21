# Development

## Setup

```bash
git clone https://github.com/octoverse-id/octonomy-go.git
cd octonomy-go
go build ./...
make test
```

**This branch is the frozen Go 1.13 line** (`support/go1.13`, module
`github.com/octoverse-id/octonomy-go`, `v1.x`). It takes security fixes only and nothing else — see
[versioning.md](versioning.md). Day-to-day work uses whatever modern toolchain you have; the section
below is the part that is not optional.

There are **no runtime dependencies** — keep `go.mod` free of a runtime `require` block.

## Quality gates

```bash
make fmt-check      # gofmt -l . (no output = clean)
make vet            # go vet ./...
make lint           # golangci-lint (if installed)
make test           # go test -race -cover ./...
make cover          # prints total coverage
make examples       # compile-check examples/
make compat-guard   # assert go.mod still matches this release line
make check          # fmt-check + vet + build + compat-guard (fast pre-push gate)
make release-check  # the full modern-toolchain pre-release gate
make test-go113     # THE gate: build + vet + test -race on a real go1.13 toolchain
make smoke          # integration smoke test against a booted server
```

## The Go 1.13 floor (read before touching code)

A modern toolchain enforces the **language** version from `go.mod` but **not** the **stdlib**
version. With `go 1.13` declared, Go 1.25 rejects generics — and happily compiles `io.ReadAll`, which
needs Go 1.16. Verified: `go build`, `go vet`, `go vet -stdversion`, and `staticcheck` all pass a
stdlib-floor violation. **`make test` passing means nothing about whether this line still works.**

So: run `make test-go113` before you push. Get a real toolchain either way:

```bash
# option 1 -- the official version wrapper (matches the Makefile's default)
go install golang.org/dl/go1.13.15@latest
go1.13.15 download
make test-go113

# option 2 -- an unpacked tarball, no wrapper
curl -sSLo /tmp/go1.13.15.tar.gz https://go.dev/dl/go1.13.15.linux-amd64.tar.gz
tar -C /tmp -xzf /tmp/go1.13.15.tar.gz
GO113=/tmp/go/bin/go make test-go113
```

What the floor rules out, and what to write instead:

| Do not use | Needs | Use instead |
| ---------- | ----- | ----------- |
| generics (`List[T]`, type params) | 1.18 | a concrete type per resource (`TagList`, `VocabularyList`) |
| `any` | 1.18 | `interface{}` |
| `io.ReadAll` | 1.16 | `ioutil.ReadAll` (`io/ioutil`) |
| `os.ReadFile`, `os.WriteFile` | 1.16 | `ioutil.ReadFile`, `ioutil.WriteFile` |
| `t.Cleanup` | 1.14 | return a cleanup func and `defer` it at the call site |
| `errors.Join`, `min`/`max`, `for range int`, `strings.CutPrefix` | 1.20+ | spell it out |
| `//go:build` **alone** | 1.17 | keep a matching `// +build` line beneath it |

`ioutil` is correct here and must not be "modernized". `staticcheck` stays silent (SA1019 keys off the
declared language version, and the deprecation postdates `go 1.13`), but golangci-lint's `govet`
`inline` analyzer does object — it is disabled in `.golangci.yml` with that reasoning recorded.

CI enforces the floor with a **required** real-`go1.13` job, and `scripts/compat-guard.sh` blocks a
`go.mod` whose `go` directive drifts off `1.13` — the one mistake with no toolchain backstop, because
a `v1.x` tag cut from a drifted `go.mod` keeps the same module path and resolves fine.

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

`newTestClient(t, handler)` in `octonomy_test.go` is the shared helper. It returns
`(*Client, func())` and the caller **must** `defer cleanup()` — `t.Cleanup` needs Go 1.14, and a
`defer srv.Close()` inside the helper would close the server before the test ever used it. Keep new
code covered and run with `-race`.

Canned single-resource responses go through `writeData`, which wraps the body in the server's
`{"data": {...}}` envelope. Handlers that returned the bare object matched the vendored spec rather
than the server, and that mismatch hid a real defect: every `Create`/`Get`/`Update` decoded to a
zero-valued struct with a nil error against a real server. Use `writeJSON` only for bodies you mean
to send verbatim — list envelopes and error envelopes.

### Integration smoke test

`integration_test.go` (build tag `integration`) is the only test that talks to a real server. It is
five assertions, deliberately: the `{data, pagination}` list envelope, the single-resource `{data}`
envelope via a create/read round-trip, both list endpoints, and one real error envelope. It gates on
`OCTONOMY_TEST_BASE_URL` and skips when that is empty, so `go test ./...` stays hermetic.

```bash
make dev-server   # boots a real Octonomy, writes .octonomy-harness.env
make smoke        # sources the env file and runs the smoke test
make dev-server-down
```

CI runs it on the **go1.13** toolchain against the pinned container image, as a required check. That
combination — the frozen client, on its own toolchain, against the current server — is the only one
that proves this line still works, and it is what caught the single-resource envelope defect.

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

The harness boots server **3.1.0**, which is newer than the `1.0.0` contract this line was written
against. That is deliberate: the frozen client's remaining job is to keep working against the server
people actually run.

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
