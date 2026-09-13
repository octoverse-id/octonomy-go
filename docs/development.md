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
make test-integration # the full integration suite against a booted server (see below)
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
depends on the server's real response shape needs the smoke test below — and anything that depends
on the server's *authorization or persistence* needs the full suite after it.

### Webhook signature vectors

`webhook/` is the one package here that is not tested against `httptest`, because it has no wire
contract to assert — it verifies bytes. Its suite is driven by
[`webhook/testdata/signature_vectors.json`](../webhook/testdata/README.md): fixed secrets, fixed
bodies, correct digests, and the deliveries that must be refused with a reason for each.

**Never compute a known-good digest in Go.** A vector produced by the implementation under test
proves only that the implementation agrees with itself — the same structural blind spot as a fixture
written against the vendored spec. The vectors come from `webhook/testdata/generate_vectors.py`,
which mirrors the server's `_webhook_signature`, and every accept vector was confirmed independently
against `openssl dgst -sha256 -hmac`. Regenerate with:

```bash
cd webhook/testdata && python3 generate_vectors.py > signature_vectors.json
```

Output is deterministic. A regeneration that changes an existing digest means the signature contract
moved, which is worth stopping over rather than committing. A new refusal needs a vector and a row
in `rejectReasons` — the suite asserts every sentinel is reachable from the shared file, so a refusal
proved only by a Go test is one no other language's SDK can adopt.

### Integration smoke test

`integration_test.go` (build tag `integration`) is the shape check against a real server. It has
grown with each resource into a single ordered walk — `TestSmoke_RealServer` — covering what a unit
suite structurally cannot: both response envelopes on writes and reads, list pagination and an `Each`
walk, `DecodeMetadata` against metadata the server itself stored, real error envelopes including
`409 scope_immutable`, the namespace axis on every model that carries it, aliases and resolution,
assignments including both bulk composites, the resource-tag replace composite, audit rows written as
a side effect of the mutations above, and request-id correlation. The steps share state deliberately,
so read it top to bottom rather than treating any one as standalone.

It is deliberately a **smoke** test: one ordered walk that asks whether the payloads match, kept
small and fast because it is the blocking check. The semantic assertions live in the full suite
below. It gates on `OCTONOMY_TEST_BASE_URL` and skips when that is empty, so `go test ./...` stays
hermetic.

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
`test (1.25)`, `vuln`, and `integration suite`.

### Full integration suite

`integration_suite_test.go` and `integration_harness_test.go` (same `integration` build tag) are
[#17](https://github.com/octoverse-id/octonomy-go/issues/17). Where the smoke test asks *does the
payload match*, this asks *does the server behave the way our doc comments say* — properties of
authorization and persistence that no fixture can settle, because a fake answers whatever the fixture
says:

| Test | What it pins |
|---|---|
| `TestIntegration_NamespaceIsolation` | A merchant-A client never sees a merchant-B row, on **every** read method this SDK exposes |
| `TestIntegration_IncludeGlobalFailsClosed` | `WithIncludeGlobal` widens what is *asked for*; a token with no global authority still sees no global rows — on **every** read method, because the server threads the flag through each view separately |
| `TestIntegration_AssignmentIdempotence` | `201` once, then `200` returning the same row — the contract `AssignmentService.Create` documents |
| `TestIntegration_BulkPartialFailure` | A partial bulk assign writes nothing, and an out-of-scope tag is reported identically to a nonexistent one |
| `TestIntegration_DeactivationCascade` | `Delete` deactivates rather than deletes, and a tag's aliases go with it |
| `TestIntegration_DuplicateSlugScopedPerNamespace` | Slug uniqueness is per namespace, not per tenant |
| `TestIntegration_ErrorEnvelopes` | Every `Is*` helper against the error the server really sends, on the status it really uses |

```bash
make dev-server
make test-integration   # the whole tagged package, including the smoke walk
make dev-server-down
```

Three things are worth knowing before adding to it.

**The isolation test asserts in both directions, and both halves are load-bearing.** Each read method
is probed six times: three runs that must *find* the row (they prove the fixture exists and the
endpoint works) and three that must not. The negatives cover three distinct mechanisms:

| run | mechanism |
|---|---|
| merchant A reads its **own** namespace, B's row must be absent | the namespace filter, under a merchant token |
| a **wildcard** token scoped to A, B's row must be absent | the namespace filter alone — this token *is* authorized for B, so nothing refuses it |
| merchant A **asks for** merchant B | authorization: 403 before any queryset runs |

The third is the request an attacker actually makes, and it is not implied by the other two — a token
reading its own namespace exercises the filter no matter what the permission layer does. A suite with
only the filter runs stays green through a permission regression on any individual route; one with
only the authorization run stays green through a lost namespace filter.

**A new read method needs a probe.** `readProbes` in `integration_suite_test.go` lists every
authenticated read in the SDK and carries the reasoning for the one deliberate exclusion (the health
probes, which are unauthenticated and outside the namespace axis). A read endpoint nobody probed is
where a cross-merchant leak lives. Two tests share that table through `runProbeMatrix`, so one entry
buys coverage in both.

Each probe also declares **how its endpoint declines a row that is out of scope** — a 200 with the row
absent, a 404 `not_found`, or resolution's 400 `validation_error` — and the negatives assert that
exact status and code. "It errored" is not evidence of isolation: the SDK turns every non-2xx into an
`*APIError` by design, so a crashed container and a working namespace filter would otherwise look
identical.

**Four `Is*` helpers are out of reach here and are listed rather than omitted** — the doc comment on
`TestIntegration_ErrorEnvelopes` names each one and why: two are deployment kill-switches this
harness must have *on*, one needs a broken database under a live app, and one is unreachable through
the server's HTTP surface at all. `IsNamespaceNotSupported` looks like a fifth — `WithNamespace` on a
v1 client never leaves the process — but `Config.HTTPClient` is exported and an `http.RoundTripper`
is this SDK's sanctioned extension point, so a wrapper transport reaches the server's real envelope.
The suite does exactly that, with a comment saying it is not how anyone should call Octonomy.

CI runs it in the **`integration suite`** job, on its own container, with `OCTONOMY_SMOKE_REQUIRED=1`
for the same reason the smoke job sets it. It is a **required context**: `integration suite` sits in
main's branch protection alongside `lint`, `test (1.24)`, `test (1.25)` and `vuln`, so a failure here
blocks the merge.

It was promoted on introduction rather than after a soak. The property it guards — that a merchant-A
client can never read a merchant-B row — is the one whose regression is least likely to be caught
anywhere else and most expensive to ship, and it adds little flake risk over the already-blocking
smoke job: both boot the same container the same way, so a registry or Docker hiccup reddens that one
too.

**Renaming the job breaks the branch.** A required context that never reports blocks every PR, and
the `name:` field is what reports it. If the job name has to change, change the branch-protection
context in the same hour.

## Running against a real Octonomy

`make dev-server` boots a complete, verified Octonomy in one command. It needs Docker and `curl`,
and nothing else:

```bash
make dev-server        # boot, verify, write .octonomy-harness.env  (~40s)
make dev-server-logs   # dump container logs
make dev-server-down   # tear everything down
```

It starts Postgres 16 and the pinned `ghcr.io/octoverse-id/octonomy:3.1.0` image on a private Docker
network, applies migrations, mints three service tokens, waits for `/health/ready`, and then **proves
the environment actually works** before reporting success. Credentials land in
`.octonomy-harness.env` (git-ignored, mode 600):

| Variable | Meaning |
|---|---|
| `OCTONOMY_TEST_BASE_URL` | Server root. Integration suites gate on this — when it is empty they skip |
| `OCTONOMY_TEST_TOKEN` | Bearer token with `tags:read`, `tags:write`, `audit:read`, and a **wildcard** namespace grant |
| `OCTONOMY_TEST_TENANT_ID` | `X-Tenant-ID` for every request |
| `OCTONOMY_TEST_APPLICATION_ID` | Parent application. Required on namespaced requests |
| `OCTONOMY_TEST_NAMESPACE_TYPE` / `_ID` | The `X-Namespace-*` pair to scope v2 calls with |
| `OCTONOMY_TEST_NAMESPACE_A_ID` / `_A_TOKEN` | A second merchant namespace and a token holding an **exact** grant for it alone |
| `OCTONOMY_TEST_NAMESPACE_B_ID` / `_B_TOKEN` | A third, likewise — the other side of the isolation boundary |

The wildcard token and the exact grants are not interchangeable, and picking the wrong one is how an
isolation test comes to assert nothing. A wildcard grant matches every partition including global, so
for that token authorization never says no: it can prove what the server's namespace *filter* does
and nothing about what its *authorization* does. The exact grants are the only way to reach the
refusal path, and the only way `include_global`'s fail-closed branch executes at all.

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
- **The exact merchant grants, proved in both directions.** After minting them the harness requires a
  `201` from merchant A inside its own namespace and a `403` from the same token reaching for
  merchant B. The negative is the one worth the round trip: a grant that reached every namespace
  would still satisfy the positive probe, and the whole isolation suite rests on it not doing that.
  Unlike the bullet above, this is about diagnosis rather than vacuity — thirty 403s inside Go
  assertions read as an SDK defect when the fault is a token minted with the wrong grant shape.

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
server, both at release **3.2.0**. When targeting a new server contract, refresh **both** (the server
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
| **Response models** | the client decodes a body built **from** the vendored schema, on both surfaces; every documented property must survive with its value, and the list envelope's pagination block with it. The reverse direction — a field the model decodes that nothing documents — is v2-authoritative, since v1's schemas omit the namespace axis |
| **Model field names** | each response model's Go field against the property it decodes — the one defect a round trip cannot see, since the same tags decode and re-encode. It reaches the models no success schema names, too: `Pagination`, the three composite results, `HealthStatus` |
| **Routes** | each inventory row against the request its method actually issued, on both surfaces |
| **Error codes** | `server_error_codes` in the inventory against this SDK's `Code*` constants offline, and against the server's `octonomy/core/errors.py` on the schedule — both directions on both hops |

Error codes are compared as sets **and** as declarations. The set comparison runs both ways — every
code in the vendored registry needs a constant, every constant needs a code or a recorded reason —
but a set cannot see a *swap*: exchange the values of `CodeNotFound` and `CodeForbidden` and the
registry still holds exactly the same codes while `IsNotFound` answers true for a forbidden. So each
constant's **name** is checked against the value it carries (`CodeNotFound` carries `not_found`),
with the two constants that abbreviate — `CodeValidation`, `CodeAuthRequired` — recorded in
`contract-coverage.yaml` with their reason, and a row that exempts nothing refused by the loader.
Two constants carrying one code are reported for the same reason: every set stays intact through it.
That comparison runs Go name → wire code and not the reverse, so it does **not** see a rename that
spells the same code differently (`CodeNamespaceAPIDisabled` → `CodeNamespaceApiDisabled`): going
the other way needs a table of initialisms, and a code whose initialism is missing from it would
report a correctly named constant as wrong. An exported rename is a release-gate question; this
gate does not answer it.

**What the registry reader can and cannot see.** It is a set of patterns over Python, not a parser,
so it is built to leave no third outcome: every `code = …` and every `error_response(…)` it captures
is read, recognised as an alias the server's own handler uses, or **reported as a spelling the gate
cannot read**. Five review rounds' worth of shapes are pinned by tests — an escaped literal, two
adjacent literals, a space or a line continuation before the paren, an equality mistaken for an
assignment, an attribute on an unrelated object. One silent spot remains, deliberately: the handler
does `code = exc.code` and passes `code`, so a bare `code` argument must be suppressed — and a
`code` this reader cannot follow to a literal is suppressed with it. Telling those apart needs
dataflow. The suppressed spellings are an explicit list of three, and a test fails if it grows.

The error-code check needs that Python file because the contract cannot answer the question:
`ErrorResponse` types `code` as a bare string, so every code the envelope can carry is invisible to a
schema comparison. The registry is therefore **vendored into
[`contract-coverage.yaml`](contract-coverage.yaml)**, exactly as the two OpenAPI documents are, so
the SDK's constants can be checked against it without the network. Without that copy, error codes
would have been the one item on this list where "refresh now, implement later" was still a state the
repository could be left in.

**The SDK side is driven, not read.** For each operation, `tools/contractdrift/drivers.go` calls the
method with every documented input populated — on **both REST surfaces**, twice each — against a
recording transport that answers with a body **synthesized from the vendored schema**. The request is
the answer: route, query parameters, headers and request body, **names and values**, compared with
that surface's contract in **both** directions and with no inference in any of it.

Values are comparable because every scalar and array input has a canonical value per execution, in a
single table (`ExpectedValue` in `drivers.go`), and the gate requires each execution's wire to carry
exactly its own. Most are the
wire field's own name, which makes a misrouted value self-describing; the rest — numbers, booleans,
an enum, the credentials — are listed explicitly. Booleans get a **pair** of values, one per
execution: there are three boolean axes on a tag list and only two values, so one run can never tell
them all apart.

Free-form objects (`metadata`) and path arguments come from elsewhere — the former is compared
exactly anyway, since the gate controls both sides; the latter varies by execution on purpose. The response is the other half: a property the Go model
has no field for is dropped on the way back out, and one whose type the model cannot read fails to
decode.

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
finding. A driver that is merely *wrong* — nil params, a missing option, a swapped argument, a hard-coded
value, a duplicate entry, one that never reaches the wire or reaches it twice, or one whose two
executions disagree — fails the same way a broken client would, because each execution is held to its
own expected values rather than merely compared with the other. That distinction is the whole point:
comparing the two runs and *allowing* the declared difference is an exemption, and a client
hard-coding one pass's value satisfies it on both.

What it does **not** cover: options the contract does not document (`WithActor`, `WithRequestID`)
have no documented counterpart to compare against, and free-form values (`metadata`) constrain
nothing.

And the boundary that contains all of those: **exact comparison proves these executions, not that the
SDK propagates arbitrary values.** A method that hard-codes a witness exactly — `limit=1`,
`application_id="cd~application_id"` — emits every expected name and value on both surfaces and both
executions and is still wrong for a real caller. So does a decoder hard-coded to the populated
response, a one-element array handler that would break on two, behaviour conditional on a value other
than the sentinels, and two booleans whose relationship is inverted rather than crossed
(`include_shared = !is_active`). Distinguishing those needs the full input space; this is
representative coverage, deliberately, and the integration smoke test is what exercises real values
against a real server.

**What a passing decode does and does not prove.** Each operation is driven twice: once with every
property populated, once with every `nullable` property set to `null`. Together those prove the model
has a field for everything documented, that no documented type is one it cannot read, and that a
nullable property round-trips as `null` rather than as the zero value — an `int` where the contract
now permits absence turns `null` into `0`, which is the silent-zero family of
[#32](https://github.com/octoverse-id/octonomy-go/issues/32) arriving through the contract instead of
through a decoder.

Every witness is derived from the property's own name — strings, integers, uuids, dates, and the
free-form objects — so two properties of one schema never share one and crossing them is always
visible. That is asserted rather than assumed: a test walks both contracts and fails on any
same-schema collision, which is how `AuditLog.id` and `AuditLog.operation_id` were found sharing a
uuid inside the very change that introduced per-property witnesses.

It is still **representative-value coverage**, not a proof of type equivalence. A property documented
`integer` decoded into a `float64` passes, as does anything at all decoded into `any`; and where the
contract constrains nothing (`metadata: {}`, `changes: {readOnly: true}`) there is nothing to check —
the stub sends the shape the server really sends, a JSON object, which is enough to prove a field
exists and no more. That is the honest boundary; the integration smoke test above is what exercises
real server payloads.

**The error envelope is driven too.** `ErrorResponse` is the most referenced schema in either
contract — every operation documents it on every failure — and it was for a while the one schema
nothing offline compared, because the response stub only ever answered `200` or `204`. So one extra
drive per surface answers **409** with a body built from `ErrorResponse` the same way every success
body is built, and the returned `*APIError` is held to it: `code`, `message`, `request_id` and
`details` must each carry what was sent, and a property the envelope grows that `parseError` has
nowhere to put is reported. Renaming `error.code` in both vendored contracts used to report **no
drift at all**, while in the SDK it means `parseError` finds no code, falls through to its
envelope-less branch, and stamps `CodeUnexpectedStatus` on every error the server sends — so
`IsNotFound`, `IsConflict` and `IsValidation` each answer `false` for the error they are named after. The drive attests what it
claims: the `/api/<version>` it really went to, the 409 the returned `*APIError` reports, and
**every** semantic helper — a caller writes `IsNotFound`, not `err.(*APIError).Code == "not_found"`,
so each of the sixteen is a claim. Each is driven with its own code and must answer for itself and
nothing else, which is what catches `IsNotFound` pointed at `CodeForbidden`: every set stays intact
through that, and both helpers then answer true for one envelope. A test fails if `errors.go` grows
a helper the table does not drive. The envelope carries **real** codes rather than name-shaped
witnesses, since a helper answers `false` for `cd~code` quite correctly. The table's own pairing is
**derived, not declared**: the `Code*` constant carrying a helper's code has to be the one the helper
is named after, so swapping a row's code and function together — which would otherwise redefine what
the table asserts — is reported. Each drive also reports the `/api/<version>` it reached, because a
drive that returns a fabricated error without issuing a request answers every question correctly
about something no client produced.

Two further drives cover where `CodeUnexpectedStatus` is really manufactured, which is **not** an
envelope: a non-2xx carrying a body that is not the contract's shape, and one whose body cannot be
read to completion. Both must produce that code and no other — inventing a semantic code from a
status is the one thing that constant exists to forbid, and driving `IsUnexpectedStatus` through a
synthesized envelope carrying `unexpected_status` proved it for a response no server sends.

**And what the route check proves.** Each driver runs twice per surface with different path values,
so a route that varies with the value it is given is reported; each placeholder has its own sentinel,
so two arguments in the wrong order do not produce the expected path; and the `/api/<version>` prefix
is asserted separately, because the suffix normalization erases it. Two executions are not a proof of
invariance — nothing short of reading every branch would be — but they are two more than the one a
single fixed input gives.

**Known boundaries**, stated rather than implied: a repeated query parameter or header is compared by
its first value only; path escaping is not exercised, since every path witness is a safe alphanumeric;
`WithActor` / `WithRequestID` have no documented counterpart to compare against; and **`required` is
not exercised offline** — the stub populates every property, so a property the contract stops
requiring produces the same body it produced before. That one is compared where it can be: the
cross-repository half diffs `required` as a set like every other part of a schema, so a server that
withdraws one is reported there. A test pins that, because "covered elsewhere" is a claim like any
other.

**Everything the offline half compares is vendored, on purpose.** The two contracts, the error
registry, the recorded server version: each has a copy in this repository, so each can be checked
against the code on every pull request and against the server once a week. That is the shape that
makes "the refresh landed but the code did not follow" visible at all — the cross-repository
comparison structurally cannot see it, because after a refresh both of its sides are the same file.
What the offline half catches there is what the drivers exercise: a documented input the client does
not send, a documented property it does not decode, a value it puts in the wrong place. A change to a
part of the contract no driver touches is still a change nobody is asked about.

### Two modes, and why only one runs on a pull request

`make contract-check` is **offline**: vendored contracts, the inventory, the Go sources, the recorded
version. It runs on every pull request as the **`contract inventory`** job and is safe to block a
merge, because it can only fail on something in this repository. It does not block one *yet* — that
needs its check context added to main's branch protection, which today requires `lint`, `test (1.24)`,
`test (1.25)`, `vuln`, and `integration suite`. Until then it fails the PR and nothing more, exactly
as the `integration smoke test` job does.

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
