# Development

## Setup

```bash
git clone https://github.com/octoverse-id/octonomy-go.git
cd octonomy-go
go build ./...
make test
```

Requires **Go 1.24+** — the floor for `main`, which is the module
`github.com/octoverse-id/octonomy-go/v2`. The frozen compat line on `support/go1.13` is the module
`github.com/octoverse-id/octonomy-go` and targets **Go 1.13**: no generics, no `any`, no post-1.13
standard library, and it must compile *and test* under a real `go1.13` toolchain. This page describes
`main`; see [versioning.md](versioning.md) for the two-line policy and [release.md](release.md) for
the backport step.

There are **no runtime dependencies** on either line — keep `go.mod` free of a runtime `require`
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
make contract-check # vendored contract vs the SDK, offline (see Contract drift)
make contract-drift # also fetches the server's contract and compares (network)
make check       # fmt-check + vet + build (fast pre-push gate)
make release-check  # the full pre-release gate (requires the tools below)
```

Optional local tools (CI installs them automatically):

```bash
# linter
go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
# vulnerability scanner
GOTOOLCHAIN=auto go install golang.org/x/vuln/cmd/govulncheck@latest
```

They are optional for day-to-day work — `make lint` and `make vuln` skip with a notice when their
binary is absent — but **`make release-check` requires both** and fails naming whichever is missing,
so a green gate means every check in it actually ran (#53). A tool installed this way lands in
`$(go env GOPATH)/bin`, which is not on `PATH` by default; add it there.

`GOTOOLCHAIN=auto` on that second line is load-bearing whenever the scanner's own
minimum Go has moved ahead of yours — x/vuln v1.8.0 requires go 1.26, so on a go1.25
toolchain the plain form fails with `requires go >= 1.26.0`. It lets the go command fetch
the toolchain needed to **build** the tool; the binary still analyses this module with
your own Go, which is what you want, since the standard-library advisories it reports are
the ones affecting the version you build with.

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

`integration_test.go` (build tag `integration`) is the only test that talks to a real server. It has
grown with each resource into a single ordered walk — `TestSmoke_RealServer` — covering what a unit
suite structurally cannot: both response envelopes on writes and reads, list pagination and an `Each`
walk, `DecodeMetadata` against metadata the server itself stored, real error envelopes including
`409 scope_immutable`, the namespace axis on every model that carries it, aliases and resolution,
assignments including both bulk composites, the resource-tag replace composite, audit rows written as
a side effect of the mutations above, and request-id correlation. The steps share state deliberately,
so read it top to bottom rather than treating any one as standalone.

It is still a **smoke** test, not the full suite — that is
[#17](https://github.com/octoverse-id/octonomy-go/issues/17). It gates on `OCTONOMY_TEST_BASE_URL`
and skips when that is empty, so `go test ./...` stays hermetic.

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

`docs/openapi-v2.yaml` (`/api/v2`) and `docs/openapi.yaml` (`/api/v1`) are vendored from the Octonomy
server, both at release **3.1.1**. When targeting a new server contract, refresh **both** (the server
generates one per `--api-version` with `make openapi`; copy the files here), reconcile any type
changes, and update:

- the `<!-- contract-version: X.Y.Z -->` marker in [versioning.md](versioning.md), plus the prose
  around it;
- [`docs/contract-coverage.yaml`](contract-coverage.yaml) — a row per operation, each either naming
  the Go method that implements it or carrying a written reason it is not implemented.

`make contract-check` fails until those agree, so a half-finished refresh cannot land.

## Contract drift

Nothing told this SDK when the server moved. It sat on a server 1.0.0 contract while the server
shipped 3.1.0 and made an entire second API surface primary, and the gap was found by reading the
server's repository, not by any mechanism.
[`tools/contractdrift`](../tools/contractdrift) is that mechanism
([#18](https://github.com/octoverse-id/octonomy-go/issues/18)).

```bash
make contract-check   # offline: the vendored contracts against this repository
make contract-drift   # network: also fetches octoverse-id/octonomy and compares
make contract-test    # the gate's own tests -- proves it can still fail
```

### It compares more than paths

A path-to-method inventory would not have caught the drift that prompted it. What actually changed
was query parameters, error codes, response schemas, and the arrival of a second surface — a path
check reports every one of those as green. So the gate compares:

| | |
| --- | --- |
| **Contract version** | `info.version` on both surfaces, against the marker in `versioning.md` |
| **Operations** | path + method, both directions, against the inventory |
| **Query parameters** | per operation, by `in` + name, with `required` and the parameter's schema — *and*, in both directions, against what that operation's method actually puts on the wire |
| **Responses** | per operation, per status, including request bodies |
| **Schemas** | `components.schemas`, property by property, including the `required` set |
| **Response models** | the client decodes a body built **from** the vendored schema; the gate reports what did not survive the round trip |
| **Routes** | each inventory row against the request its method actually issued |
| **Error codes** | `server_error_codes` in the inventory against this SDK's `Code*` constants offline, and against the server's `octonomy/core/errors.py` on the schedule — both directions on both hops |

The error-code check needs that Python file because the contract cannot answer the question:
`ErrorResponse` types `code` as a bare string, so every code the envelope can carry is invisible to a
schema comparison. The registry is therefore **vendored into
[`contract-coverage.yaml`](contract-coverage.yaml)**, exactly as the two OpenAPI documents are, so
the SDK's constants can be checked against it without the network. Without that copy, error codes
would have been the one item on this list where "refresh now, implement later" was still a state the
repository could be left in.

**The SDK side is driven, not read.** For each operation, `tools/contractdrift/drivers.go` calls the
method with every parameter it offers populated, against a stub server that records the request and
answers with a body **synthesized from the vendored schema**. The request is the answer: the route,
the query parameters, no inference. The response is the other half: a property the Go model has no
field for is dropped on the way back out, and a property whose *type* changed fails to decode at all.

That is what caught `q` and `slug` missing from `VocabularyListParams`
([#36](https://github.com/octoverse-id/octonomy-go/issues/36)).

This replaced a 700-line static reader of the same package, and the reason is worth keeping: that
reader had to infer control flow — which call is the transport call, which struct builds the query,
which type argument decodes the body — and it grew a special case for every Go shape it met while
still answering **clean** for a method that stopped passing its query builder, a method that branched
between two private helpers, and a schema property whose type changed. None of those three can hide
from a client that actually issues the request.

The cost is the driver table: one call per operation, which someone has to write and keep compiling.
That is the deliberate trade — a driver that names the wrong method reports the wrong route on its
first run, a driver that stops compiling is a build failure, and an operation with no driver is a
finding.

**Everything the offline half compares is vendored, on purpose.** The two contracts, the error
registry, the recorded server version: each has a copy in this repository, so each can be checked
against the code on every pull request and against the server once a week. That is the shape that
makes "the refresh landed but the code did not follow" impossible to sit on — the cross-repository
comparison structurally cannot see it, because after a refresh both of its sides are the same file.

### Two modes, and why only one runs on a pull request

`make contract-check` is **offline**: vendored contracts, the inventory, the Go sources, the recorded
version. It runs on every pull request as the **`contract inventory`** job and is safe to block a
merge, because it can only fail on something in this repository. It does not block one *yet* — that
needs its check context added to main's branch protection, which today requires `lint`, `test (1.24)`,
`test (1.25)`, and `vuln`. Until then it fails the PR and nothing more, exactly as the
`integration smoke test` job does.

`make contract-drift` adds the cross-repository comparison and runs **weekly**, in
[`contract-drift.yml`](../.github/workflows/contract-drift.yml). It is deliberately not a pull-request
check: a job that reaches into another repository can go red for reasons that have nothing to do with
the change under review, and the first time a merge is blocked that way, the team learns to merge
past a red check. On drift it writes the report to the job summary and fails the job; the owner is
named in the workflow header.

Both halves are the same program. `scripts/contract-fetch.sh` does the fetching — the tool itself
reads plain files, which is what makes a synthetic upstream copy a one-line test instead of a network
fixture. Both repositories are public, so the fetch needs no credential beyond the workflow's own
read-only token.

### The inventory, and the divergence it records

[`docs/contract-coverage.yaml`](contract-coverage.yaml) lists every operation the vendored contracts
publish. An operation missing from it fails the gate whether or not anyone intends to implement it —
which is the point: an endpoint nobody implemented and an endpoint nobody noticed look identical from
the outside, and telling them apart is the whole job. Each row also records two response shapes:
`documented_response` (what the spec claims) and `actual_response` (what the running server really
returns).

Those two differ on most rows, and **the server wins** — the specs document list responses as bare
arrays while the server returns `{data, pagination}`, and the same holds for the single-resource
`{data}` envelope, the bulk composites, and the health probes. The generated spec cannot see any of
it, because the envelope is added by a renderer below the serializers. The gate does not flag this;
it asserts the spec still says what the row recorded, so the day the divergence *ends* is the day it
speaks up, and the workaround can come out.

### Adding a resource, or adding to the gate

**A new endpoint needs three things**: the method, a row in
[`contract-coverage.yaml`](contract-coverage.yaml), and a driver in
`tools/contractdrift/drivers.go` that calls it with every parameter populated. The gate fails until
all three exist — a row with no driver is an operation nobody exercises, and a driver with no row is
a call nobody declared.

**New checks** belong in the same shape as the existing ones: mutate a real copy of the vendored
contract and assert the finding. A miniature fixture proves a checker works on the shape its author
imagined, and this gate exists because the contract stopped being the shape its author imagined.
Note which half you are testing — the client under test is the one compiled into the test binary, so
a test that needs the *SDK* to be wrong synthesizes the observation instead of editing Go source.
