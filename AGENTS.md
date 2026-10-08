# Octonomy Go SDK — Agent Instructions

`octonomy-go` is the official Go client SDK for [Octonomy](https://github.com/octoverse-id/octonomy),
a multi-tenant, multi-application tag management / taxonomy service. The SDK is a hand-written,
dependency-free client for the Octonomy REST API. `Config.APIVersion` selects the surface and
defaults to **v1** (`/api/v1`) on this line; `/api/v2`, which carries the namespace axis, is opt-in
with `APIV2`. That is the one *default* that differs from `main`'s, which was v2 when the selector was
ported from it (5e40964): `v1.0.0` sent every request to `/api/v1`, and this line can never change a
default under a caller (see *READ FIRST*).

## READ FIRST — this branch is the Go 1.13 line, and its freeze is reversed

You are on `support/go1.13`: module `github.com/octoverse-id/octonomy-go`, versions `v1.x`, Go
**1.13**. `main` is a different module (`/v2`, on a modern Go floor) and is where capabilities
originate.

- **This line takes capability parity with `main`**
  ([epic #88](https://github.com/octoverse-id/octonomy-go/issues/88)). Until #89 this file told
  agents to refuse features, new resources, `/api/v2` and namespaces here and send them to `main`.
  That policy is withdrawn. The line takes security fixes, bug fixes, and ports of what `main`
  already has — its resource groups, `/api/v2` with the namespace axis, and its transport and decode
  guards. Do not refuse that work, and do not route it to `main`: it is already there.
- **Features originate on `main`; this line ports them.** A task asking for something `main` does
  not have belongs on `main` first. What may originate here is a *fix* — a security fix, a bug fix,
  or a guard against a hazard only Go 1.13 has.
- **No webhook receiver, ever.** That is policy, not an observation about any server's default: a
  consumer needing one moves to `/v2`. Do not port `webhook/`.
- **No major, ever — so no breaking change, ever.** The module path is unsuffixed, Go accepts only
  `v0`/`v1` versions on it, and the suffixed path a major would need is `main`'s module. Every
  change here must therefore keep `v1.0.0` callers compiling — an *unkeyed* struct literal aside,
  which any Go minor that adds a field can break (`docs/versioning.md`, the MINOR rule) — and must
  not move behaviour they correctly rely on: a break has no version to ride. In practice — never
  change an exported field's type or a method's signature, never remove or rename a symbol, and
  never change a default (so `/api/v2` arrives opt-in: an existing caller's requests must not move
  to another surface on an in-range upgrade). A bug fix still changes behaviour; that is what makes
  it a fix, and it is admissible. `scripts/compat-guard.sh` refuses a `v2+` release PR into this
  branch, and a `v2+` tag on a tree that carries this line's module path.
- **Sunset 2027-08-31**, unchanged by the reversal. See `SECURITY.md` and `docs/versioning.md`.
- **A published `v1.x` cannot be recalled.** `retract` shipped in Go 1.16, so a Go 1.13 consumer's
  toolchain ignores it, and `GOPROXY` caches tags forever. Verify before tagging, not after.
- **`go.mod` must keep `go 1.13` and the unsuffixed module path.** `make compat-guard` blocks the
  first; Go itself catches the second. Never "modernize" either.
- **A modern `go build`/`go vet`/`staticcheck` pass proves nothing here.** The language version is
  enforced from `go.mod`; the stdlib version is not. Run `make test-go113` (real toolchain) before
  claiming anything compiles on this line.

## Porting from `main`

Parity work is a **hand-port**. An AST generator was scoped for it and cut once the transform
surface measured 7% of the code — do not build one; the reasoning is in
[the epic's design doc](https://github.com/octoverse-id/octonomy-go/blob/main/docs/designs/compat-line-api-v2-parity.md)
on `main`.

- Branch off `support/go1.13` with an issue-numbered branch and PR back into it (see *Development
  Pipeline*). Take the file from `main`, then rewrite it into this line's dialect.
- The rewrite rules are [`docs/porting-checklist.md`](docs/porting-checklist.md), one row per rule,
  each marked **LOUD** (a miss fails `go build` or `go vet` under a real go1.13.15, so
  `make test-go113` catches it) or **SILENT** (a miss compiles clean and is wrong), and each SILENT
  row naming what catches it or saying that nothing does. Add a row when a port meets a rule the
  table lacks.
- **The SILENT rules are the ones to hold in your head**, because each one compiles and vets clean on
  go1.13.15 as well as on a modern toolchain:
  - **A leftover `omitzero`.** Go 1.13's `encoding/json` does not know the option and ignores it, so
    a ported `*string` tagged `,omitzero` emits `"field": null` on every PATCH that does not touch
    it — a PATCH that clears columns the caller never named. Optional write fields are `omitempty`
    here. `TestNoStructTagCarriesOmitzero` (`updateguard_test.go`, #96) refuses the option in every
    struct tag the module ships, on any toolchain — the anonymous struct inside each `MarshalJSON`
    included, which `update_test.go`'s round-trip sees only under go1.13.15.
  - **More than one `%w`.** Before Go 1.20, `fmt.Errorf` with two `%w` verbs returns an error with
    no `Unwrap` at all, so `errors.Is` finds neither. `%w` plus `%v` keeps one and loses the other.
    Where a sentinel and a cause must both survive, write a wrapper type whose `Unwrap()` returns the
    cause and whose `Is()` matches the sentinel.
  - **Doc comments.** Strip `Optional[T]`, `omitzero`, a Go 1.24 floor, and post-1.13 standard
    library in examples (`slices.*`). Rewrite the **module** path `.../octonomy-go/v2` to the
    unsuffixed one — but never strip `/api/v2`, which is a REST route and the capability being
    ported.
  - **A default.** Copy a value from `main` and its default comes with it. `DefaultAPIVersion` was
    `APIV2` there at 5e40964 and is `APIV1` here, and it is not the last default the port will meet:
    a default on this line never changes, so read each one against `v1.0.0`, not against `main`.
- **Port a rule with the code it governs.** Several rules in this file describe the tree as it is —
  every request tenant-scoped, every method taking `...RequestOption` — and `main`'s `AGENTS.md`
  records exceptions to them that arrive with the code: the health probes are unversioned,
  unauthenticated and take no options, and have their own constructor and request path. When a
  port brings in code `main` governs with such an exception, bring the exception into this file in
  the same PR. Do not bend the ported code to fit a rule written before it existed.
- **Do not port `main`'s `Optional[T]`.** The `*Update` structs keep their pointer fields, because
  changing a published field's type breaks `v1.0.0` callers (the no-major rule above). A PATCH here
  therefore cannot clear a nullable field; that is a deliberate carve-out, not a porting gap.
- **A test port moves its row in [`docs/compat-test-disposition.md`](docs/compat-test-disposition.md).**
  That table holds every `main` test file at 5e40964 with a verdict — ported, rewritten, preserved,
  owned by another issue, or excluded with a reason — and `TestDispositionTableNamesEveryTestFile`
  fails on a test file here that it does not name. **A test that parses Go source is rewritten, never
  ported:** `main`'s, at 5e40964, read response types off `doData[T]` type arguments, which this line
  does not have, so a transformed one finds nothing to read. `sourceguard_test.go` holds the readers
  rewritten for this dialect; build on them.
- **The contract gate is ported under a pin (#98).** `tools/contractdrift` is `main`'s gate as of
  5e40964, and every Go file of it that imports no SDK package — `checks.go`, `coverage.go`,
  `gosdk.go`, `main.go`, `sdk.go`, `spec.go` — is `main`'s byte for byte at the commit
  `tools/contractdrift/main.pin` names. The identity check derives that set from `main`'s files
  rather than reading it off the pin file, so relabelling one does not exempt it. **Never edit one of them here**: `make
  contract-identity` (in the required `compat guard` job) fails on a single byte, and a fix lands on
  `main` first and arrives by advancing the pin, in a PR that copies `main`'s files at the new commit.
  Every other file in that directory is `advisory` (this line's version of a `main` file — above all
  `drivers.go`, written against this line's surface) or `own`, and a new file on either side fails the
  check until the pin file classifies it. `make contract-report` prints the advisory diffs; the
  release runbook has a human read them.

## Product Rules

These mirror server semantics the client must respect — business rules live on the server; the SDK
stays a faithful, ergonomic client.

- The SDK adds ergonomics, not behavior. Do not encode server-side validation or invariants here.
  **One recorded exemption, ported from `main`: `checkScopeCoherence` in `transport.go`.** It
  rejects a request whose own scoping options contradict each other or the client's API version — a
  namespace on a v1 client (which, here, is every client that did not opt in), a half-set or reserved
  namespace pair, a namespaced bodyless request with no application, `WithIncludeGlobal` on a write.
  None of those consults resource state or can disagree with the server about a row; each names an
  SDK symbol in its remediation. **A check that could only cite a server rule does not belong there.**
- Every request **on the versioned API** is tenant-scoped via the `X-Tenant-ID` header;
  `Config.TenantID` is required. The health probes are the documented exception — see the health
  rules below.
- `application_id` is optional on tags and vocabularies (`nil` = shared across the tenant) and is
  required for assignments.
- Tag deletion is **deactivation** on the server, not hard delete. `Delete` methods call HTTP
  `DELETE` and must document the deactivation semantics rather than implying data loss.
- Tag aliases are alternate identifiers that resolve to canonical tags and follow tenant/application
  compatibility rules.
- Keep the SDK faithful to the bundled contract references: `docs/openapi.yaml` (`/api/v1`, the
  default surface on this line) and `docs/openapi-v2.yaml` (`/api/v2`, opt-in), both vendored at
  server **3.2.1**. Read the **v2** spec when porting a resource — v1's schemas omit the
  `namespace_type` / `namespace_id` fields every model that carries namespace identity needs. Where the live server diverges from the generated spec —
  notably the **two response envelopes** the spec omits: `{data, pagination}` on lists and `{data}`
  on single resources — trust the server's real behavior and document the divergence in a comment.
  Both were verified against a running server, not read off the spec.

## API Client Rules

- One file per resource (`tags.go`, `vocabularies.go`, …). Each defines a `*Service` reached from a
  field on `Client`.
- Methods take `context.Context` first and accept variadic `...RequestOption` last. The health
  probes take no options at all, and `HealthService` records why.
- **Scoping is the transport's job, not each resource's.** `WithNamespace`, `WithGlobalNamespace`,
  `WithApplication` and `WithIncludeGlobal` apply to any method and are enforced at the chokepoint,
  so a new resource inherits them by doing nothing. Do not add a namespace field to `Config`, a
  per-resource namespace parameter, or a duplicate of a guard already in `checkScopeCoherence`.
  **Application scope follows the body:** on a bodyless request the query is authoritative
  (`WithApplication`), and on a `POST`/`PATCH` the body's `ApplicationID` is, so the option is
  refused there.
- **A scope option that contradicts one already on the request is an error, never last-wins** —
  option-versus-params and option-versus-option alike. `WithGlobalNamespace` is the one explicit
  override.
- **Request correlation is send-only, per call, and never minted here.** `WithRequestID` sets
  `X-Request-ID` only when the caller supplied one. Do not add a `Config.RequestID` — a request id
  names one request — and do not mint one client-side. The option validates wire grammar only; the
  server's 100-character column is a server rule and stays out of this package.
- Response models that carry namespace identity get `NamespaceType` / `NamespaceID` as `*string`,
  **decode-only**: the server sets them from the `X-Namespace-*` headers, never from a body, so they
  must not appear on a `*Create` / `*Update`.
- List methods return a **per-resource** envelope (`*TagList`, `*VocabularyList`) decoding
  `{data, pagination}` — this line has no `List[T]`, because type parameters need Go 1.18. Embed
  `ListOptions` in each resource's `*ListParams`.
- Pick the transport helper by response shape: `client.doData` for a single resource (unwraps the
  server's `{"data": {...}}`), `client.doList` for a list envelope, `client.do` for a call with no
  payload to decode (DELETE's 204, which it asserts). The wrong choice compiles, and is caught only
  at runtime by the envelope, identity, pagination and 204 assertions — keep every one of them, since
  a decoder without them returns a zero-valued struct or an empty-looking page with a nil error.
  `doRaw` is the shared request path; do not call it directly from a resource file.
  `TestEveryResponseTypeCanRefuseAnEmptyDecode` fails on a call from anything but `Client.do`,
  `doData` and `doList`, a new helper beside them in `transport.go` included: a response decoded
  beneath them is one no guard can see.
- **Bulk and replace return a composite object under `data`** — `bulk-assign`, `bulk-remove` and
  the resource-tag replace, e.g. `{"data": {"created": 1, "existing": 0, "skipped": 0,
  "assignments": [...]}}`. Both vendored specs are wrong about them: a bare array for `bulk-assign`
  and the replace, no response schema at all for `bulk-remove`. A bare-array decoder against these
  bodies yields an empty slice and a nil error. They go through `doData` with a composite result
  struct whose `UnmarshalJSON` requires its keys, since a zero count is an ordinary answer. Do
  **not** relax `doList`'s pagination requirement to accept them: they carry no pagination block
  because they are not pages. This line has no `decodeResourceArray[T]`, so a composite's row array
  is held to the resource standard by hand — `requireResourceArray`, `decodeJSON` into the typed
  slice, `requireIdentity` per row — and a present-but-null array normalizes to an empty non-nil
  slice, as on `main`. That normalization is the composites' rule only: `doList` keeps a null page
  nil, as `v1.0.0` did.
- **A new response model must implement `identityFields()`** (`transport.go`) on its **value**
  receiver, naming the field that identifies its row — what the contract's `required:` list marks,
  never every field: `id` for most, `assignment_id` on `ResourceTag`, `resource_id` on
  `TagResource`. `doData` and `doList` call it on every decoded value and reject a blank one, so
  `{"data": {"id": null}}` is an error rather than a zero-valued resource. A nested resource counts
  only where the contract marks it required and the route exists to deliver it — `ResourceTag.Tag`
  and `TagResolution.Tag` both qualify; read the schema's `required:` list rather than assuming. A
  list type must implement `rows()`, which `doList`'s parameter type requires, so forgetting it does
  not compile. A composite result carries no identity of its own and requires its keys in its own
  decoder instead; `TagResolution` does both, since the tag it exists to deliver is a resource.
  **`TestEveryResponseTypeCanRefuseAnEmptyDecode` (`identityfields_test.go`) enforces the choice
  between those two** on every type handed to `doData` or `doList` — read off the `&out` it decodes
  into — and a destination it cannot name, such as an `out interface{}` passed through a wrapper,
  fails rather than passing. `rows()` is checked by calling it: add a new model to
  `identityModels()` and a new list to `identityLists()` (`transport_test.go`), and
  `TestTheRuntimeIdentityTablesMatchTheSource` fails until you do. Neither checks that the field you
  named is the right one; that stays with the reader.
- **Every decode of response bytes goes through `decodeJSON`** (`jsondepth.go`), never a bare
  `json.Unmarshal` or `json.NewDecoder`. Go 1.13's `encoding/json` has no depth limit, so an
  unguarded decode lets a server exhaust the stack — a fatal runtime error, not a panic.
  `TestEveryResponseDecodeIsDepthBounded` fails on a bypass. The request side is `checkBodyDepth`,
  run in `doRaw` before `json.Marshal`, which on Go 1.13 has no cycle detection either. **This guard
  had no counterpart on `main` when it was written** — a modern `encoding/json` detects encoding
  cycles and bounds decoding depth itself — so it is a deliberate divergence, not a porting gap:
  never "port away" what `main` lacks here.
- Non-2xx responses become `*APIError` carrying the `{error:{code,message,details,request_id}}`
  envelope. Add `Is<Code>` helpers for common error codes.
- **Every non-2xx becomes an `*APIError`, including one whose body could not be read.** An
  oversized body must not downgrade to a bare read error: wrap the cause so `errors.Is` still finds
  `ErrResponseTooLarge`.
- **A non-2xx with no envelope gets `CodeUnexpectedStatus`, never a semantic code, on both API
  versions.** Do not reintroduce a status-to-code mapping: this line's first release had one,
  `codeFromStatus`, and it made a wrong `BaseURL`'s 404 satisfy `IsNotFound`, so a caller's
  not-found branch read an infrastructure failure as an empty taxonomy. A code that arrives *in* an envelope is preserved verbatim. `CodeNotReady` is
  not an exception: it is established by the health view's own `{"status": …}` body, not inferred
  from a status.
- **Health is outside the API surface in three ways at once** — rooted outside `/api/<version>`, a
  bare `{"status": "ok"}` with no `data` envelope, and unauthenticated. It therefore has its own
  request path (`doUnversioned`), decoder (`decodeHealthStatus`) and credential-free constructor
  (`NewHealthClient`). Do not loosen `doData`'s envelope requirement or `New`'s validation to make it
  fit, and do not move the auth suppression into `doRaw` as a `skipAuth` flag.
- **Unreachable and unready stay distinguishable.** A request that got no HTTP response wraps
  `ErrUnreachable` and is never an `*APIError`; a probe the server answered with a non-2xx and its
  own status body is an `*APIError` with `CodeNotReady`. Both `errors.Is(err, ErrUnreachable)` and
  `errors.Is(err, <cause>)` must hold, which on Go 1.13 takes the `unreachableError` type, not a
  two-`%w` `fmt.Errorf`.
- Server read-only fields are decode-only; write structs (`*Create`/`*Update`) use pointer fields
  with `omitempty` so PATCH sends only what the caller set. They stay pointers when `main`'s
  `*Update` structs are ported — see *Porting from `main`*.
- **A `*Update` carrying `Metadata` needs a value-receiver `MarshalJSON`** that sends a non-nil map,
  empty or not, so `Metadata{}` empties the stored object instead of being dropped by `omitempty`
  (#37). A pointer receiver is skipped by `encoding/json` without a word, because `Update` takes the
  struct by value. Keep the method's field list in declaration order and add the type to
  `updateBodies` in `update_test.go`, which checks every field against the struct-tag encoding.
  `updateguard_test.go` (#96) reads the source for what marshalling cannot see:
  `TestUpdateBodiesNamesEveryUpdateType` fails until the row exists; `TestEveryUpdateFieldCanBeLeftOut`
  refuses a pointer-receiver `MarshalJSON`, a `Metadata` field with no `MarshalJSON`, and a field that
  is not `omitempty` or is neither a pointer nor `Metadata`; and `TestEveryPatchBodyIsAnUpdateType` holds every PATCH
  sent through the transport to the `*Update` name those checks find their types by. A row in
  `updateBodies` also carries its wire body as literal JSON, spelled from the contract's PATCH schema.
- No new exported surface without doc comments and tests.

## Go Conventions

- Target Go **1.13**. **Standard library only** — no third-party runtime dependencies. Dev tools
  (`golangci-lint`, `govulncheck`) are not module dependencies.
- **`tools/contractdrift` is the one exception to both, and it is a separate module.** It declares
  `go 1.24` and requires `gopkg.in/yaml.v3`, behind its own `go.mod` with a `replace ../..` onto this
  checkout, so neither reaches a consumer's build: Go's `./...` stops at a nested `go.mod`, and so do
  the go1.13 job's build and vet. Never lower it to Go 1.13 or move its dependency into the root
  module. A root test that walks the tree must stop at a nested `go.mod` too — go1.13.15's parser
  cannot read the gate's generics (`shippedSourceIn` in `updateguard_test.go` does this) — or, if it
  walks into one on purpose, must not need this toolchain to parse what it finds there
  (`testFileProblems` in `internal/testmainguard` reads such a file lexically).
- No generics, no `any` (write `interface{}`), no `io.ReadAll` (write `ioutil.ReadAll`), no
  `t.Cleanup`, no `os.ReadFile`. `docs/development.md` has the full floor table. `ioutil` here is
  correct and must not be "modernized" — the `govet` `inline` analyzer that objects is disabled in
  `.golangci.yml`, with the reason recorded there.
- Build constraints need **both** `//go:build` and a matching `// +build` line; Go 1.13 reads only
  the latter.
- Keep the tree `gofmt`-clean, `go vet`-clean, and `golangci-lint`-clean.
- The library never panics, never calls `os.Exit`, and never logs. It returns errors.
- Wrap internal errors with `%w` under the `octonomy:` prefix; never swallow an error.
- When changing a non-obvious mapping or semantic, add a comment explaining why so future
  contributors understand the rule.

## Testing Expectations

- Table-driven tests using `net/http/httptest`. Assert the request method, path, auth headers
  (`Authorization`, `X-Tenant-ID`), query params, and body on the server side; assert decoded values
  on the client side. (Use `t.Errorf` inside handlers — they run on a separate goroutine.)
- `newTestClient` returns `(*Client, func())`; `defer cleanup()` at every call site. `t.Cleanup`
  needs Go 1.14, and the helper returns, so a `defer srv.Close()` inside it closes the server before
  the test runs. That is rule 1 of the `t.Cleanup` replacement model in
  `docs/compat-test-disposition.md`; its other two cover a test body and a teardown that must outlive
  the helper registering it.
- **Every coverage row needs a driver, and every driver sets every input.** `make contract-check`
  calls each method `docs/contract-coverage.yaml` names through its driver in
  `tools/contractdrift/drivers.go`, on both surfaces, and compares the request and the decoded value
  with the vendored specs — names and values. A new method needs a row and a driver; a new parameter
  or write field needs the driver to set it, or the gate reports it as documented and unsent. Where
  the client genuinely cannot send a documented input, that is the finding: an `unsent_inputs` row with
  the reason, never a driver that skips the field. Such a gap row is held to the field's absence
  (`TestRecordedGapsStillHaveNoField`, with the field named in `recordedGaps`), since while it stands
  it would hide that field being ported and never driven.
- **What runs the gate is pinned from outside it.** CI runs `make contract-test` and `make
  contract-check` in the `test` job and the identity check in `compat-guard`. `contractgate_test.go`
  pins both jobs and ci.yml's `on:` block as text, and the `contract-*` and `release-check` recipes
  as make's own rule database resolves them under each real goal (`make -pq <target>`) — each still
  phony, so a same-named file cannot make it up to date — so a step `env`, a checkout `ref`, and a
  recipe overridden literally or computed for a real goal all fail. The limits it does not reach
  (a Makefile branching on the inspector's own `-q`, an earlier step writing `$GITHUB_ENV`) are
  recorded in the file as adversarial-only.
  It is in the ROOT package on purpose: `go test ./...` runs it in the required jobs without going
  through a make target it guards, where a copy inside the gate's module stopped running the moment
  `contract-test` was overridden. Changing a pinned job or recipe means updating its pin in the same
  commit.
- **Every response type needs a smoke call**, and so does every list envelope. A `TestSmoke_`
  function in `integration_test.go` must call a method that decodes it, on a client that function
  built, or
  `TestEveryResponseTypeHasASmokeProbe` (`smokeprobes_test.go`, no build tag) fails. Only a real
  server sees a fixture-versus-server divergence (#32). A call inside a closure or a helper is not
  counted, and a smoke test may neither skip (only `newSmokeClient` does, behind the required gate)
  nor return early. **The two smoke runners are pinned** — the Makefile's `smoke:` rule and the CI
  smoke job equal `smokeRecipePin` and `smokeJobPin` in `smokeprobes_test.go`. Changing one means
  re-checking what the pins' comment lists against a real server, then updating the pin in the same
  commit.
- **A new read method needs a probe in `readProbes`** (`integration_suite_test.go`, #97). That table is
  what makes "a merchant-A client never sees a merchant-B row" a statement about the whole authenticated
  read surface rather than about whichever endpoints someone remembered. A read endpoint nobody probed is
  where a cross-merchant leak lives. **`TestEveryReadMethodHasANamespaceProbe` (`readprobes_test.go`,
  no build tag) enforces this.** It resolves each surface method's verb from the source —
  following a call into a helper, since `Health.Live` names no verb of its own — and fails on a read
  with no probe, a probe naming a method that no longer exists, **a probe whose `find` closure calls a
  different endpoint than its name claims**, a duplicate name, and a stale or unargued exclusion. A
  method whose verb it cannot resolve fails too — unless it is probed or excluded — rather than
  passing as "not a read": treating the unknown case as silence is the defect the guard exists to
  prevent. A read that genuinely cannot leak across namespaces goes in `readProbeExclusions` with its
  reason, never in silence. The surface it reads is the exported methods declared on `Client` and on
  each `*FooService` an exported `Client` field holds. Any shape that would hand a caller a method
  outside it — an embedded field, an aliased service, an exported `Client` field of another type, an
  exported field on a service — fails it, and so does an exported method or package function
  anywhere else in the package that issues a read, or reaches the transport with a verb it cannot
  resolve, whatever way a caller would reach it. On this line a new transport helper on `*Client`
  that takes the verb goes in `transportCalls` with the index of its `method` parameter;
  `TestTransportCallsMatchTheHelpersSignatures` holds the table to `transport.go`.
- **Which harness token a test uses IS the test.** The wildcard grant matches every partition,
  global included, so under it authorization never refuses anything — it can only demonstrate the
  server's namespace FILTER. The per-merchant exact grants
  (`OCTONOMY_TEST_NAMESPACE_A_TOKEN`/`_B_TOKEN`, minted by `scripts/octonomy-harness.sh`) are the only
  way to reach the refusal path, and the only way `include_global`'s fail-closed branch runs at all.
  Reaching for the wildcard token because it is the convenient one is how an isolation assertion
  comes to assert nothing. `TestTheIsolationSuiteRunsItsProbes` refuses an isolation test missing
  any run `isolationTests` lists, each read off its `probeRun` literal as a grant, a namespace, a
  fixture, an option and an outcome — so a run moved to the wildcard, a fail-closed read aimed at a
  merchant's row, or a deleted control fails it. A new run the suite needs goes in that table.
- **The isolation suite runs in the required go1.13 smoke job**, as a step with its own
  `^TestIntegration_` selector — a step and not a job, because a new job is advisory until branch
  protection names it, and an isolation test in an advisory job, or matched by no selector, asserts
  nothing. `TestTheIsolationSuiteRunsItsProbes` holds the suite to the `integration` tag, the
  `TestIntegration_` prefix, `loadHarness`'s single skip behind `OCTONOMY_SMOKE_REQUIRED` (the
  smoke run's knob, reused), and `runProbeMatrix` ranging over `readProbes`. The CI step is part of
  `smokeJobPin`, and `make test-integration` equals `isolationRecipePin` (`readprobes_test.go`).
  The client builder there sets `APIVersion: octonomy.APIV2` — `main`'s, at 5e40964, relies on its
  `/api/v2` default there, and on this line's `/api/v1` default every namespaced read is refused
  client-side.
- **A `TestMain` is exactly `os.Exit(m.Run())`.** Before Go 1.15 a `TestMain` that returns exits 0
  over failing tests, and `TestNoTestMainHidesAFailure` refuses any other shape anywhere in the
  repository: it walks for every test file, a nested module's included, rather than listing
  packages, so a new one is covered by adding it. It lives in `internal/testmainguard`, a test
  binary of its own, since a root-package `TestMain` that exits early would skip a guard beside it.
- Canned **single-resource** responses go through `writeData`, which adds the server's `{"data": ...}`
  wrapper. `writeJSON` sends the body verbatim — use it for list and error envelopes only. Handlers
  that returned bare objects matched the vendored spec instead of the server and hid a real defect.
- Cover success paths, the `{data, pagination}` list envelope, and error decoding
  (404 → `IsNotFound`, 409 → `IsConflict`, 400 → `IsValidation`) — each from an **enveloped** body;
  a bare status satisfies none of them.
- **A fixture that decodes into a field must not be marshalled from the struct under test.** A
  `Tag` run through `writeData` round-trips through its own JSON tags, so a misspelled tag passes.
  Write the wire spelling as raw JSON where the field name is what is being asserted.
- **A guard whose unguarded case kills the process is tested in a child process** — re-exec the test
  binary with an environment variable, as `jsondepth_test.go` does, with a deadline. A cyclic body or
  a stack-exhausting response cannot be observed from inside the process it hangs or kills.
- Run tests with `-race`. Keep new code covered.

## Local Development

- Run `make check` before pushing and `make release-check` before a release. On this line neither is
  the real gate: also run `make test-go113` (real go1.13 toolchain) and, for anything touching
  decoding or transport, `make dev-server && make smoke` against a real server — with
  `make test-integration` beside it for anything touching scoping or a read method. For anything
  touching a method's parameters, a write struct, a model or the coverage file, run `make
  contract-check`; for anything under `tools/contractdrift`, `make contract-test contract-identity`.
- Keep the README quickstart, `examples/`, and `Makefile` current with the public API.
- **Describe `main` with a link, or with the commit a comparison was made at — never by restating
  its current state.** A sentence about what `main` has *now* rots on `main`'s schedule, and nothing
  on this branch can catch it; a relative link resolves to this branch's own stale copy, so link to
  `https://github.com/octoverse-id/octonomy-go/blob/main/…`. "Ported from `main`'s `transport.go` at
  5e40964" cannot rot. This is the policy #69 applied to this branch's docs.
- **A contract refresh is not done when the YAML lands.** Refresh both vendored specs from the Octonomy
  server (`make openapi` there, one per `--api-version`), then move three things with them: the
  `<!-- contract-version: X.Y.Z -->` marker in `docs/versioning.md`, a row per operation in
  `docs/contract-coverage.yaml` — naming the Go method that implements it or a written reason it is
  not implemented — and every sentence naming the contract. `make test` fails until the specs, the
  marker and the rows agree (`contractbaseline_test.go`) and until every version token in the tree
  equals the marker or carries a registered reason (`contractversion_test.go`). A sentence naming the
  contract *without* a version in it is invisible to both; read for those by hand. `make
  contract-check` is what fails when the client does not follow the refresh — a parameter no method
  sends, a property no model decodes, a type the model cannot read — so a refresh is done when it
  passes too (`docs/development.md#contract-drift`).

## Development Pipeline

- Branch names must follow Conventional Branch naming from
  https://conventional-branch.github.io/.
- Use `<type>/<description>` with lowercase alphanumerics, hyphens, and dots only where valid.
- Allowed branch types are `feature`, `feat`, `bugfix`, `fix`, `hotfix`, `release`, `support`,
  and `chore`.
- `support/<description>` names a **long-lived** maintenance line that outlives any single issue
  (for example `support/go1.13`, the Go 1.13 client line). Because such a line closes no
  issue, it is **exempt from the issue-number requirement below**. Work targeting a support line
  still branches off it with a normal issue-numbered branch, and its version bumps and tags still
  happen in a dedicated `release/<version>` PR.
- Example branch names: `feature/tag-assignments`, `fix/pagination-decode`, and
  `chore/update-agent-rules`.
- When the user explicitly asks to implement an approved development plan, such as
  `PLEASE IMPLEMENT THIS PLAN`, create a GitHub issue before creating the development branch.
- If the user provides an existing issue number, use that issue instead of creating a duplicate.
- New plan-tracking issues must include the plan summary, key implementation tasks, and acceptance
  checks.
- If GitHub issue creation fails, stop and report the blocker instead of implementing untracked work.
- Planned-development branches must include the issue number using
  `<type>/<issue-number>-<short-description>`, for example `feature/12-tag-assignments`.
- PR bodies for planned development must include `Closes #<issue-number>` and summarize how the
  implementation maps back to the approved plan.
- Work targeting this line branches **off `support/go1.13`** and PRs back into it — never into `main`.
  A change that applies to both lines lands on `main` first and is then ported here: a cherry-pick
  where the hunk is dialect-neutral, a hand-port otherwise (`docs/release.md`).
- **`Closes #N` does not fire here.** GitHub closes an issue only on a merge into the default branch,
  so an issue a PR into this line resolves has to be closed by hand after the merge.
- Releases follow Semantic Versioning: cut them with the runbook in `docs/release.md` and the policy
  in `docs/versioning.md`. The SDK version lives in `version.go` and is published as a git tag
  `vX.Y.Z`. Version bumps and tags happen only in a dedicated `release/<version>` PR, never in
  feature or fix PRs.
- The `code-review/` directory is reserved for local code review pipeline artifacts.
- Review agents must write findings to `code-review/findings.md`.
- Patch agents must read `code-review/findings.md`, apply valid fixes, and write the patch summary to
  `code-review/patches.md`.
- Agents must never stage or commit `code-review/findings.md`, `code-review/patches.md`, or any other
  generated review artifact.
- After creating a PR, remove all local files under `code-review/` except the tracked
  `code-review/.gitkeep` placeholder.

## Web Browsing

- Use the `/browse` skill from gstack for all web browsing.
- Do not use `mcp__claude-in-chrome__*` tools.
