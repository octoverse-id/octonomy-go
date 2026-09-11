# Octonomy Go SDK — Agent Instructions

`octonomy-go` is the official Go client SDK for [Octonomy](https://github.com/octoverse-id/octonomy),
a multi-tenant, multi-application tag management / taxonomy service. The SDK is a hand-written,
dependency-free client for the REST API. `Config.APIVersion` selects the surface and defaults to
**v2** (`/api/v2`), the server's primary advertised one; `/api/v1` remains fully supported and is
selected with `APIV1`.

## Product Rules

These mirror server semantics the client must respect — business rules live on the server; the SDK
stays a faithful, ergonomic client.

- The SDK adds ergonomics, not behavior. Do not encode server-side validation or invariants here.
  **One recorded exemption: `checkScopeCoherence` in `transport.go`** (#7). It rejects a request
  whose own scoping options contradict each other or the client's configured API version — a
  namespace on a v1 client, a half-set or reserved namespace pair, a namespaced bodyless request
  (GET/HEAD/DELETE) with no application, `WithIncludeGlobal` on a write. None of those consults resource state or can disagree
  with the server about a row; they are about the coherence of the caller's own configuration, and
  each names an SDK symbol in its remediation. **A check that could only cite a server rule does not
  belong there.** The server rejects all of them by name too, so the guard buys a round trip and a
  better-targeted error — except `include_global` on a write, which the server silently ignores, and
  silence is the failure mode this SDK refuses.
- Every request **on the versioned API** is tenant-scoped via the `X-Tenant-ID` header;
  `Config.TenantID` is required. The health probes are the documented exception — see the health
  rules below.
- `application_id` is optional on tags and vocabularies (`nil` = shared across the tenant) and is
  required for assignments.
- Tag deletion is **deactivation** on the server, not hard delete. `Delete` methods call HTTP
  `DELETE` and must document the deactivation semantics rather than implying data loss.
- Tag aliases are alternate identifiers that resolve to canonical tags and follow tenant/application
  compatibility rules.
- Keep the SDK faithful to the bundled contract references: `docs/openapi-v2.yaml` (`/api/v2`, the
  default surface) and `docs/openapi.yaml` (`/api/v1`), both vendored at server **3.1.1**. Read the
  **v2** spec when adding a resource — v1 has no namespace axis, so its schemas omit the
  `namespace_type` / `namespace_id` fields every model needs. Where the live server
  diverges from the generated spec — notably the **two response envelopes** the spec omits:
  `{data, pagination}` on lists and `{data}` on single resources — trust the server's real behavior
  and document the divergence in a comment. Both were verified against a running server, not read
  off the spec; only one of the two was known before #32.

## API Client Rules

- One file per resource (`tags.go`, `vocabularies.go`, …). Each defines a `*Service` reached from a
  field on `Client`.
- Methods take `context.Context` first and accept variadic `...RequestOption` last.
- **Scoping is the transport's job, not each resource's.** `WithNamespace`, `WithGlobalNamespace`,
  `WithApplication`, and `WithIncludeGlobal` apply to any method and are enforced at the chokepoint,
  so a new resource inherits them by doing nothing. Do not add a namespace field to `Config`, a
  per-resource namespace parameter, or a duplicate of a guard that already lives in
  `checkScopeCoherence`. **Application scope follows the body:** on a bodyless request the query is
  authoritative (`WithApplication`), and on a `POST`/`PATCH` the body's `ApplicationID` is — the
  server drops the query value on a global create, so the option is refused there rather than
  silently producing a tenant-shared row. `include_global` is a **query** parameter and is
  meaningless on writes.
- **Request correlation is send-only, per call, and never minted here.** `WithRequestID` sets
  `X-Request-ID` at the same transport chokepoint the scope options use — so every method that takes
  options gets it, and the health probes, which take none, deliberately do not (`health.go` records
  why) — and the header goes out **only** when the caller supplied one — the server mints `req_<uuid>` otherwise and threads
  whichever id it holds into the audit row, the outbox/webhook event, its structured log, and the
  error envelope. Do **not** add a `Config.RequestID`: a request id names *one* request, so a
  client-level default would stamp every call the process makes with a single value and correlate
  nothing. Do **not** mint one client-side: that replaces an id the caller can at least read back off
  `APIError.RequestID` with one that was never surfaced anywhere. The success path deliberately does
  not return the server's id — `doRaw` discards `resp.Header` and every method returns `(*T, error)`
  — and a caller who wants correlation on a success supplies their own. The option validates wire
  grammar only (non-blank, printable ASCII, no outer whitespace — a control byte would surface as
  `ErrUnreachable`, a high byte as latin-1 mojibake in the audit row, and outer space is trimmed by
  `net/http` on the way out, so each one silently breaks the string equality the id exists for); it
  does **not** enforce the server's
  100-character column, which is a server rule and stays out of this package.
- **A scope option that contradicts one already on the request is an error, never last-wins.** This
  holds option-versus-params and option-versus-itself. Last-wins on a scope axis is a silent
  wrong-tenant read, and on `Get`/`Delete` — which have no params struct — option-versus-option is
  the only way the value can be set at all. `WithGlobalNamespace` remains the one explicit override.
- Response models for the seven v2 schemas that carry namespace identity get `NamespaceType` /
  `NamespaceID` as `*string`, **decode-only**. The server sets them from the `X-Namespace-*` headers
  and never from a request body, so they must not appear on `*Create` / `*Update` — see
  `docs/roadmap.md` for the list and which issue owns each.
- **On this line** list methods return `*List[T]` and decode the `{data, pagination}` envelope; embed
  `ListOptions` in each resource's `*ListParams`. That shape is line-specific: `support/go1.13` has no
  type parameters, so it declares a two-field `*TagList` / `*VocabularyList` per resource instead.
  `ListOptions` and `Pagination` are identical on both. Write the generic form here and do not
  reach for the compat spelling.
- **Pick the transport helper by response shape, and never by convenience.** `doData[T]` for a call
  returning a single resource (it unwraps the server's `{"data": {...}}`), `doList[T]` for a list
  envelope, `client.do` for a call with no payload to decode — DELETE's 204. `doRaw` is the shared
  request path; resource files must not call it directly.
- **Every method that decodes a payload goes through `doData[T]` or `doList[T]`.** Never a bare
  `json.Unmarshal` into the resource type: that is #32 exactly, and it does not fail loudly — it
  returns a zero-valued struct, or an empty-looking page, with a nil error. That is how the defect
  survived a complete unit suite. A canned fixture must carry the envelope (`writeData` in the test
  helpers), because a fixture written against the vendored spec passes against a client that is
  wrong.
- **The resource groups are not all CRUD, and `docs/openapi.yaml` will mislead you about two of
  them.** All are implemented; the table is now a reference for anyone editing them. Shapes verified
  live against server 3.1.0, not read off the spec:

  | Group | Endpoints | Helper |
  | ----- | --------- | ------ |
  | Tag aliases (#8) | full CRUD | `doData`/`doList`/`do` — the `tags.go` template applies as-is |
  | Tag resolution (#9) | `GET /tag-resolution` only | `doData` — one specialized read, no list, no writes |
  | Assignments (#10) | `POST`/`DELETE /tag-assignments`, plus `bulk-assign`/`bulk-remove` | `doData` + `do`; see the composite note below |
  | Resource tags (#11) | `GET` list + `POST` composite replace | `doList` + `doData` (composite) |
  | Audit logs (#12) | `GET` only, three list routes | `doList` — **list-only**, there is no `Get` |
  | Health (#13) | `/health/live`, `/health/ready` | neither — `doUnversioned` + its own decoder, see below |

  - **Bulk and replace return a composite object under `data`**, e.g.
    `{"data": {"created": 1, "existing": 0, "skipped": 0, "assignments": [...]}}`. The spec is no
    guide here: it claims a bare array (`type: array`) for `bulk-assign` and the resource-tag
    replace, and documents **no response schema at all** for `bulk-remove` (which really returns
    `{"data": {"removed": N}}`). The bare-array claim is a third divergence in the same family as
    the two above, and the one most likely to reproduce #32 — a bare-array decoder against this
    body yields an empty slice and a nil error. It still fits `doData[T]`; `T` is a composite result
    struct, not the resource. Do **not** relax `doList`'s pagination requirement to accept these:
    they carry no pagination block because they are not pages.
  - **Health is outside the API surface in three ways at once.** It is rooted outside `/api/<version>`
    (the prefix is unconditional in `doRaw`), its body is a bare `{"status": "ok"}` with **no `data`
    envelope**, and it is **unauthenticated** — while `New` requires both `Token` and `TenantID`, so
    the tenant-scoping rule above does not apply to it. It therefore has its own request path
    (`doUnversioned`), its own decoder (`decodeHealthStatus`), and its own credential-free
    constructor (`NewHealthClient`). `doData`'s envelope requirement and `New`'s validation were
    **not** loosened to make it fit, and must not be: that re-opens #32, and the tenant guarantee,
    for every other resource. Nor may the auth suppression move into `doRaw` as a flag — the design
    note on #13 refuses a `skipAuth bool` there, and `doRaw` has only grown since.
  - **Health's decoder enforces the same rule `doData` does.** A 2xx with no readable `status` is an
    error, never a zero-valued `HealthStatus`: `{}`, a renamed key, and a load balancer's splash page
    must not read as a healthy server.
  - **Unreachable and unready must stay distinguishable, on health and everywhere.** A request that
    got no HTTP response wraps `ErrUnreachable` and produces no `*APIError`; a probe the server
    *answered* with a non-2xx and its own `{"status": …}` body is an `*APIError` carrying
    `CodeNotReady`. They mean different things operationally and must never collapse into one error.
- **A new response model must implement `identityFields()`** (`transport.go`), naming the field that
  identifies its row — `id` for most, `assignment_id` on `ResourceTag`, `resource_id` on
  `TagResource`, matching what the vendored contracts mark required. `doData` and the list/composite
  array decoder call it on every decoded value and reject a blank one. Without it a model is silently
  skipped, and `{"data": {"id": null}}` or a renamed id decodes to a zero-valued resource with a nil
  error again — #40, which is the same silent-zero family as #32. Name only the ROW's identity, never
  every field the schema documents: re-running the server's validation here is out of bounds by the
  first rule in this file. A nested resource counts only where the contract marks it required and the
  route exists to deliver it — `ResourceTag.Tag` and `TagResolution.Tag` both qualify, and **read the
  schema's `required:` list rather than assuming**, which is where the first draft of this rule got
  `ResourceTag` wrong. A composite carries no identity of its own and requires its keys in
  `UnmarshalJSON` instead; `TagResolution` does both, since the tag it exists to deliver is a
  resource.
- Non-2xx responses become `*APIError` carrying the `{error:{code,message,details,request_id}}`
  envelope. Add `Is<Code>` helpers for common error codes.
- **Every non-2xx becomes an `*APIError`, including one whose body could not be read.** An
  oversized or truncated error body must not downgrade to a bare read error: that removes exactly
  the large failures from `AsAPIError` / `IsUnexpectedStatus` while identical smaller ones keep
  working. Wrap the cause so `errors.Is` still finds it.
- **A non-2xx with no envelope gets `CodeUnexpectedStatus`, never a semantic code**, and
  `CodeNotReady` is not an exception: that rule bans *inferring* a code from an HTTP status, while
  `CodeNotReady` is established by a server-authored body (the health view's `{"status": …}`), and a
  non-2xx on a probe route whose body is *not* that shape still gets `CodeUnexpectedStatus`. Do not
  reintroduce a status-to-code mapping: deriving `not_found` from a bare 404 is what made an unrouted
  `/api/v2` satisfy `IsNotFound`, so a caller's not-found branch read a missing route as an empty
  taxonomy with no error (#7). A code that arrives *in* an envelope is preserved verbatim, including
  one this SDK has no constant for.
- Server read-only fields are decode-only; write structs (`*Create`/`*Update`) use pointer fields
  with `omitempty` so PATCH sends only what the caller set.
- No new exported surface without doc comments and tests.

## Go Conventions

- **Two lines, two Go floors — check which one you are on before you write anything.** This branch
  (`main`, module `github.com/octoverse-id/octonomy-go/v2`) targets Go **1.24+**: generics, `any`,
  and the modern standard library are all in bounds. The frozen compat line (`support/go1.13`,
  module `github.com/octoverse-id/octonomy-go`) targets Go **1.13** — no type parameters, no `any`
  alias, no post-1.13 standard library — and takes **security fixes only**. A fix that must reach
  both lands here first and is cherry-picked, where it has to compile and test under a real
  `go1.13` toolchain. See [`docs/versioning.md`](docs/versioning.md) for the policy and the compat
  line's sunset date, and [`docs/release.md`](docs/release.md) for the backport step.
- **Standard library only** on both lines — no third-party runtime dependencies. Dev tools
  (`golangci-lint`, `govulncheck`) are not module dependencies.
- Keep the tree `gofmt`-clean, `go vet`-clean, and `golangci-lint`-clean.
- The library never panics, never calls `os.Exit`, and never logs. It returns errors.
- Wrap internal errors with `%w` under the `octonomy:` prefix; never swallow an error.
- When changing a non-obvious mapping or semantic, add a comment explaining why so future
  contributors understand the rule.

## Testing Expectations

- Table-driven tests using `net/http/httptest`. Assert the request method, path, auth headers
  (`Authorization`, `X-Tenant-ID`), query params, and body on the server side; assert decoded values
  on the client side. (Use `t.Errorf` inside handlers — they run on a separate goroutine.)
- Cover success paths, both response envelopes, and error decoding (404 → `IsNotFound`,
  409 → `IsConflict`, 400 → `IsValidation`).
- Single-resource fixtures go through `writeData`, which wraps the body in `{"data": {...}}`. Use
  `writeJSON` only for bodies meant to go out verbatim — list envelopes and error envelopes.
- A unit suite cannot see a fixture-versus-server divergence: it asserts the client against the
  fixtures it ships with. New response shapes need an assertion in `integration_test.go`
  (`make smoke`) as well.
- Run tests with `-race`. Keep new code covered.

## Local Development

- Run `make check` before pushing and `make release-check` before a release.
- Keep the README quickstart, `examples/`, and `Makefile` current with the public API.
- Refresh the vendored `docs/openapi.yaml` (v1) and `docs/openapi-v2.yaml` (v2) from the Octonomy
  server when targeting a new contract, and record the server version each tracks in
  `docs/versioning.md`. They are generated by `make openapi` on the server, one per `--api-version`.
- **A contract refresh is not done when the YAML lands.** `make contract-check` (CI job
  `contract inventory`, which fails the PR) asserts the vendored contracts against this repository,
  and a refresh that stops at the files fails it. Three things move together: the specs, the
  `<!-- contract-version: X.Y.Z -->` marker in `docs/versioning.md`, and `docs/contract-coverage.yaml`
  — which needs a row per operation, naming either the Go method that implements it or a written
  reason it is not implemented. The gate reads the SDK as Go rather than as text, so it also fails on
  a query parameter *that operation's* method does not send, a schema field the decoded model has no
  place for, and a row whose method now requests a different route — "refresh the spec and implement
  it later" is not a state this repository can be left in. The cross-repository half,
  `make contract-drift`, is scheduled-only and never gates a PR. See
  `docs/development.md#contract-drift`.

## Development Pipeline

- Branch names must follow Conventional Branch naming from
  https://conventional-branch.github.io/.
- Use `<type>/<description>` with lowercase alphanumerics, hyphens, and dots only where valid.
- Allowed branch types are `feature`, `feat`, `bugfix`, `fix`, `hotfix`, `release`, `support`,
  and `chore`.
- `support/<description>` names a **long-lived** maintenance line that outlives any single issue
  (for example `support/go1.13`, the frozen Go 1.13 client line). Because such a line closes no
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
