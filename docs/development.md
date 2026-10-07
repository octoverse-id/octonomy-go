# Development

## Setup

```bash
git clone https://github.com/octoverse-id/octonomy-go.git
cd octonomy-go
go build ./...
make test
```

**This branch is the Go 1.13 line** (`support/go1.13`, module
`github.com/octoverse-id/octonomy-go`, `v1.x`). It takes security fixes, bug fixes, and ports of what
`main` already has, and never a change that would need a major — see [versioning.md](versioning.md),
and [AGENTS.md](../AGENTS.md) for the porting rules. Day-to-day work uses whatever modern toolchain
you have; the section below is the part that is not optional.

There are **no runtime dependencies** — keep `go.mod` free of a runtime `require` block.

## Quality gates

```bash
make fmt-check      # gofmt -l . (no output = clean)
make vet            # go vet ./...
make lint           # golangci-lint (if installed)
make test           # go test -race -cover ./...
make cover          # prints total coverage
make examples       # compile-check examples/
make compat-guard      # assert go.mod still matches this release line
make compat-guard-test # the guard's own fixture tests (release-PR and tag paths)
make tools-check       # fail unless golangci-lint and govulncheck are installed
make check             # fmt-check + vet + build + guard + guard tests (pre-push)
make release-check     # the full modern-toolchain pre-release gate
make test-go113     # THE gate: build + vet + test -race on a real go1.13 toolchain
make smoke          # integration smoke test against a booted server
make test-integration # namespace isolation suite against a booted server
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
| `http.Header.Values` | 1.14 | `h[http.CanonicalHeaderKey(name)]` |
| `url.Values.Has` | 1.17 | `_, ok := q[key]` |
| `errors.Join`, `min`/`max`, `for range int`, `strings.CutPrefix` | 1.20+ | spell it out |
| two `%w` in one `fmt.Errorf` | 1.20 | a wrapper type with `Unwrap()` and `Is()` — this one **compiles and vets clean** on go1.13 and returns an error with no `Unwrap` at all |
| `//go:build` **alone** | 1.17 | keep a matching `// +build` line beneath it |

[`porting-checklist.md`](porting-checklist.md) is the full set of rewrites a port from `main` makes,
each marked by whether go1.13.15 catches a miss — and, for the ones it does not, what does.

`ioutil` is correct here and must not be "modernized". `staticcheck` stays silent (SA1019 keys off the
declared language version, and the deprecation postdates `go 1.13`), but golangci-lint's `govet`
`inline` analyzer does object — it is disabled in `.golangci.yml` with that reasoning recorded.

CI enforces the floor with a real-`go1.13` job — the only check that can, and the reason this line
has CI at all. `scripts/compat-guard.sh` blocks a `go.mod` whose `go` directive drifts
off `1.13` — the one mistake with no toolchain backstop, because
a `v1.x` tag cut from a drifted `go.mod` keeps the same module path and resolves fine.

Which of those checks mechanically blocks a merge is branch-protection state, and no file in this
repository can observe it — so none of them claims to. Ask the API:

```console
$ gh api 'repos/{owner}/{repo}/branches/support%2Fgo1.13/protection' \
    --jq '.required_status_checks.contexts'
```

Optional local tools (CI installs them automatically):

```bash
# linter
go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
# vulnerability scanner
GOTOOLCHAIN=auto go install golang.org/x/vuln/cmd/govulncheck@latest
```

`GOTOOLCHAIN=auto` on that second line is load-bearing whenever the scanner's own minimum Go has
moved ahead of yours — x/vuln v1.8.0 requires go 1.26, so on a go1.25 toolchain the plain form fails
with `requires go >= 1.26.0`. It lets the go command fetch the toolchain needed to **build** the
tool; the binary still analyses this module with your own Go, which is what you want, since the
standard-library advisories it reports are the ones affecting the version you build with.

## Testing approach

Tests use `net/http/httptest` to stand up a fake Octonomy and assert the wire contract:

- **Server side** (inside the handler, with `t.Errorf` — handlers run on another goroutine): request
  method, path, `Authorization` and `X-Tenant-ID` headers, query params, and request body.
- **Client side** (in the test goroutine): the decoded return value, the `{data, pagination}` envelope,
  and error decoding via `IsNotFound`/`IsConflict`/`IsValidation`.

`newTestClient(t, handler)` in `octonomy_test.go` is the shared helper. It returns
`(*Client, func())` and the caller **must** `defer cleanup()` — `t.Cleanup` needs Go 1.14, and a
`defer srv.Close()` inside the helper would close the server before the test ever used it. The full
replacement model, for a test body and for a teardown that has to outlive its helper, is in
[`compat-test-disposition.md`](compat-test-disposition.md), with what became of each of `main`'s test
files. Keep new code covered and run with `-race`.

`newVersionedTestClient(t, version, handler)` (`scope_test.go`) pins the API version, and
`newUnreachableClient` fails the test if a request is sent at all — use it for any guard that promises
to refuse before sending. The JSON nesting guards' unguarded cases hang or kill the process, so
`jsondepth_test.go` runs them in a **child process**: the test re-executes its own binary with
`OCTONOMY_JSONDEPTH_CHILD` set and a deadline, and the parent reads the outcome from the exit status
and output. Follow that pattern for any guard whose failure would take the test binary with it.

Canned single-resource responses go through `writeData`, which wraps the body in the server's
`{"data": {...}}` envelope. Handlers that returned the bare object matched the vendored spec rather
than the server, and that mismatch hid a real defect: every `Create`/`Get`/`Update` decoded to a
zero-valued struct with a nil error against a real server. Use `writeJSON` only for bodies you mean
to send verbatim — list envelopes and error envelopes.

### Integration smoke test

`integration_test.go` (build tag `integration`) is one of the two tests that talk to a real server
(the other is the isolation suite below). It is a smoke test, not a suite: its `TestSmoke_` functions check that the client still decodes what a real
server sends — both envelopes, every response type, a real error envelope, the health probes and the
namespace pair on `/api/v2`. It gates on `OCTONOMY_TEST_BASE_URL` and skips when that is empty, so
`go test ./...` stays hermetic.

Its coverage is checked without a server. `TestEveryResponseTypeHasASmokeProbe`
(`smokeprobes_test.go`) reads the smoke file as source and fails when a response type has no
`TestSmoke_` function calling a method that decodes it; add the call in the change that adds the
type. The two runners that execute it — `make smoke` and the CI smoke job — are pinned in the same
file, so a change to either is made deliberately, re-checked against a real server, and recorded in
the pin.

```bash
make dev-server   # boots a real Octonomy, writes .octonomy-harness.env
make smoke        # sources the env file and runs the smoke test
make dev-server-down
```

CI runs it on the **go1.13** toolchain against the pinned container image, with
`OCTONOMY_SMOKE_REQUIRED=1` so a missing base URL fails instead of skipping — a skip would be a green
job that asserted nothing. That combination — this line's client, on its own toolchain, against
the current server — is the only one that proves this line still works, and it is what caught the
single-resource envelope defect.

### Namespace isolation suite

`integration_suite_test.go` (build tag `integration`, #97) asks the question `/api/v2` exists to
answer: can a merchant-A client see a merchant-B row? Every authenticated read method is a probe in `readProbes` — the two unauthenticated health probes are argued exclusions —
and two tests ask each probe a matrix of questions against rows seeded in two merchant namespaces and
the global one — `TestIntegration_NamespaceIsolation` (the namespace filter, under an exact grant and
under the wildcard, and the 403 a merchant-A token gets asking for merchant B) and
`TestIntegration_IncludeGlobalFailsClosed` (an exact grant that opts into the global rows still sees
none). Each probe declares how its endpoint declines a row out of scope — an empty page, a 404, or
resolution's 400 — and a filtered run must decline exactly that way: "it errored" is not isolation
when every non-2xx is an `*APIError`.

Which token a run uses is the test. The wildcard grant matches every partition, so under it
authorization never refuses and `include_global` always succeeds; the harness mints an exact grant
for each of two merchants (`OCTONOMY_TEST_NAMESPACE_A_*` / `_B_*`), and those are the only way to
reach the refusal path or the fail-closed branch.

Two guards keep it honest without a server. `TestEveryReadMethodHasANamespaceProbe`
(`readprobes_test.go`) fails on a read method with no probe or argued exclusion, and on a probe that
names or calls the wrong method; add the probe in the change that adds the read.
`TestTheIsolationSuiteRunsItsProbes` holds the suite to what makes it run — the `integration` tag,
the `TestIntegration_` prefix, one skip behind `OCTONOMY_SMOKE_REQUIRED` — and each isolation test to
every run in `isolationTests`, read as its grant, namespace, fixture, option and outcome.

```bash
make dev-server        # boots a real Octonomy, mints the three grants
make test-integration  # sources the env file and runs the isolation suite
make dev-server-down
```

CI runs it as a step of the required go1.13 smoke job, against the same harness and with
`OCTONOMY_SMOKE_REQUIRED=1`. Both `make test-integration` and that step are pinned
(`isolationRecipePin`, `smokeJobPin`), like the smoke runners.

## Running against a real Octonomy

`make dev-server` boots a complete, verified Octonomy in one command. It needs Docker and `curl`,
and nothing else:

```bash
make dev-server        # boot, verify, write .octonomy-harness.env  (~40s)
make dev-server-logs   # dump container logs
make dev-server-down   # tear everything down
```

It starts Postgres 16 and the pinned `ghcr.io/octoverse-id/octonomy:3.1.0` image on a private Docker
network, applies migrations, mints three service tokens — a wildcard grant and an exact grant for
each of two merchant namespaces — waits for `/health/ready`, and then **proves the environment
actually works** before reporting success. Credentials land in `.octonomy-harness.env`
(git-ignored, mode 600):

| Variable | Meaning |
|---|---|
| `OCTONOMY_TEST_BASE_URL` | Server root. Integration suites gate on this — when it is empty they skip |
| `OCTONOMY_TEST_TOKEN` | Bearer token with `tags:read`, `tags:write`, `audit:read` |
| `OCTONOMY_TEST_TENANT_ID` | `X-Tenant-ID` for every request |
| `OCTONOMY_TEST_APPLICATION_ID` | Parent application. Required on namespaced requests |
| `OCTONOMY_TEST_NAMESPACE_TYPE` / `_ID` | The `X-Namespace-*` pair to scope v2 calls with |
| `OCTONOMY_TEST_NAMESPACE_A_ID` / `_A_TOKEN` | A merchant namespace, and a token with an EXACT grant for it alone |
| `OCTONOMY_TEST_NAMESPACE_B_ID` / `_B_TOKEN` | A second merchant, likewise. The isolation suite reads both pairs |

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

`docker run` alone produces an environment that looks healthy and silently fails. Five things the
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
- **Exact grants, each proved in both directions.** Each merchant token must write in its own
  namespace (`201`) and be refused in the other merchant's (`403`) — both tokens, since a second
  wildcard minted as the merchant-B grant would pass a check of A alone. A token minted with the wrong grant shape would
  otherwise surface as dozens of 403s inside Go assertions that read as an SDK defect.

CI reaches it through the `.github/actions/octonomy-harness` composite action. The script on this
branch is **this line's own copy**: it cannot pick up an edit made on the other line, and the two
have already diverged. Read this one for what this line does, and do not assume a harness change
made elsewhere reached it — porting one is the same hand-port as any other file.

The harness pin and the vendored contract are two different numbers, and neither follows the other.
The pin is a floor for the server the smoke test proves this client against; the contract is what the
SDK is written against, recorded by the marker in [versioning.md](versioning.md). #90 refreshed the
contract and left the pin alone, so read each from where it is written — the image reference in the
script above for the one, the marker for the other — never one off the other.

### Troubleshooting

- `error getting credentials … docker-credential-desktop.exe: exec format error` — a Docker
  Desktop-on-WSL config problem, not a harness one. The image is public, so point Docker at a
  credential-free config for the run: `mkdir -p /tmp/dc && echo '{}' > /tmp/dc/config.json && export DOCKER_CONFIG=/tmp/dc`.
- Port 8000 already taken: `OCTONOMY_HARNESS_PORT=8100 make dev-server`.
- A failed boot prints container logs automatically and tears itself down. To inspect a *running*
  harness, use `make dev-server-logs`.

## Keeping the contract current

`docs/openapi.yaml` (`/api/v1`) and `docs/openapi-v2.yaml` (`/api/v2`) are vendored from the Octonomy
server, both at release **3.2.1**. When targeting a new server contract, refresh **both** (the server
generates one per `--api-version` with `make openapi`; copy the files here), reconcile any type
changes, and update:

- the `<!-- contract-version: X.Y.Z -->` marker in [versioning.md](versioning.md), plus the prose
  around it;
- [`docs/contract-coverage.yaml`](contract-coverage.yaml) — a row per operation, each either naming
  the Go method that implements it or carrying a written reason it is not implemented;
- every other sentence that names the vendored contract.

`make test` fails until those agree. `contractbaseline_test.go` holds the specs to the marker and the
coverage file to the specs; `contractversion_test.go` holds the prose to the marker, and reports each
version it cannot classify by file and line. What neither can see is a sentence naming the contract
without a version number in it — "`openapi.yaml` is the contract" — so read for those by hand.

That is not a contract gate. Nothing on this branch calls a method and compares what it puts on the
wire, or what it decodes, with the vendored schema; porting [`main`'s](https://github.com/octoverse-id/octonomy-go/tree/main/tools/contractdrift) to do that is [#98](https://github.com/octoverse-id/octonomy-go/issues/98).
