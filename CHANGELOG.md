# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### BREAKING
- **The default REST surface is now `/api/v2`.** `Config.APIVersion` selects it and defaults to
  `APIV2`, the server's primary advertised surface; the client previously targeted `/api/v1`
  unconditionally. **If your Octonomy server predates 2.0, set `Config.APIVersion = APIV1`** — such a
  deployment has no `/api/v2` route and answers every call with an unrouted 404. The SDK cannot
  detect this in advance (there is no version handshake), so this is a wire-level change that
  compiles clean. It does not fail silently: see the error-mapping entry below, which is what makes
  the misconfiguration loud, and which was a condition of making v2 the default at all.
- **An envelope-less non-2xx no longer gets a semantic error code.** A response that did not carry
  Octonomy's `{"error": {...}}` envelope now yields `CodeUnexpectedStatus` (`IsUnexpectedStatus`)
  instead of a code derived from its HTTP status. **`IsNotFound(err)` no longer reports true for a
  bare 404** from a proxy, a gateway, or a server with no route for the requested API version — only
  for a real Octonomy `not_found`. This changes behavior for existing v1 callers who branch on
  `IsNotFound` for a bare 404, independently of the version default above.

  The old mapping is what made the v2 default unsafe: an unrouted 404 became `CodeNotFound`, so a
  caller's ordinary "that tag doesn't exist" branch read a missing `/api/v2` as an empty taxonomy
  with no error at all. Codes that *do* arrive in an envelope are preserved verbatim, including ones
  this SDK has no constant for, so a `503 namespace_api_disabled` stays distinguishable from an
  infrastructure 503.

### Added
- **`/api/v2` and namespace scoping** ([#7](https://github.com/octoverse-id/octonomy-go/issues/7)).
  `APIVersion` (`APIV1`, `APIV2`), `Config.APIVersion`, and `Client.APIVersion()`. Namespace
  (merchant / sub-tenant) scoping is per-request via `WithNamespace(nsType, nsID)` and
  `WithGlobalNamespace()`, which set or clear the `X-Namespace-Type` / `X-Namespace-ID` pair. There
  is deliberately **no** `Config` namespace field: omitting the headers is a legal request that
  returns the *global* namespace with a 200, so a client-level default would silently mis-scope every
  read at call sites that still look correct.
- `WithApplication(applicationID)` contributes the `application_id` query parameter on **bodyless**
  requests (`GET`, `HEAD`, `DELETE`), which take their application scope from the query and must
  carry one when namespaced. Without it the SDK could not construct a valid namespaced detail read at
  all. It is refused on a `POST`/`PATCH`, where the body's `ApplicationID` is authoritative: the
  server drops the query value on a global create, so honoring the option there would silently create
  a tenant-shared row for a caller who asked for application scope.
- `WithIncludeGlobal()` asks a namespaced read to also return the global rows the caller is
  authorized for (`include_global`, a query parameter — fail-closed on the server). It is refused on
  writes, where the server ignores it, rather than being sent to do nothing.
- `NamespaceType` / `NamespaceID` on `Tag` and `Vocabulary` — decode-only, nil on a global row and on
  every `/api/v1` response. The five remaining v2 schemas that carry them arrive with their resources
  (see [`docs/roadmap.md`](docs/roadmap.md)).
- Error codes and helpers for the namespace surface: `namespace_not_supported`, `namespace_invalid`,
  `namespaced_writes_disabled`, `namespace_api_disabled`, `ambiguous_resolution`, each with an `Is*`
  helper. `namespaced_writes_disabled` and `namespace_api_disabled` are **operator** states — rollout
  flags, not caller errors — and their doc comments say so.
- `IsTenantMismatch`, `IsApplicationMismatch`, and `IsInactiveTag`: the constants shipped without
  helpers, and the latter two are what assignment writes raise.
- Response bodies are bounded at 32 MiB, reported as `ErrResponseTooLarge`. A caller cannot express a
  size ceiling through `*http.Client` — its `Timeout` bounds duration, not bytes — so the limit lives
  at the one chokepoint every method shares. A non-2xx that trips the ceiling still returns an
  `*APIError` with its status and `CodeUnexpectedStatus`, wrapping the cause so `errors.Is` reaches
  it; otherwise the large failures would silently fall out of `AsAPIError` while identical smaller
  ones kept working. `APIError` gained `Unwrap` for this.
- Contradictory scope options are refused rather than resolved by precedence — including a second
  `WithApplication` or `WithNamespace` naming a different value. Last-wins on a scope axis is a
  silent cross-merchant read, and on `Get`/`Delete` (no params struct) option-versus-option is the
  only way the value can be set. `WithGlobalNamespace` stays the one explicit override.
- The missing-application guard on a namespaced request keys on whether the request carries a **body**
  rather than on whether it is a read. A bodyless `DELETE` is exactly the case where the query string
  is the whole request and `WithApplication` is the only way to supply an application, so it is now
  checked locally instead of being sent to a certain `403`.
- **`WithRequestID(id)` sends `X-Request-ID`** ([#5](https://github.com/octoverse-id/octonomy-go/issues/5)),
  threading a caller's own correlation id through the four places the server records a request: the
  audit row (`AuditLog.RequestID`), the outbox / webhook event envelope (its `request_id` field and
  the delivered webhook's `X-Octonomy-Request-ID`), the structured request log, and the error
  envelope (`APIError.RequestID`). It composes with `WithActor` — actor is *who*, request id is
  *which call* — and applies to every method that takes options. The health probes are the
  exception: they take none, and `HealthService` now records why this option is excluded along with
  the scoping knobs.

  **The SDK never mints one, and there is no `Config` field.** No header is sent unless the option is
  used, which leaves the server's own `req_<uuid>` minting intact; a client-minted id would replace a
  value the caller can at least read back off an error envelope with one that was never surfaced
  anywhere. A client-level default is worse still: a request id names *one* request, so it would
  stamp every call the process makes with a single value and correlate nothing while looking like it
  worked. The server's id is still not surfaced on the **success** path — methods return
  `(*T, error)` — so callers who want correlation on a success generate the id themselves, which is
  the whole point of the option.

  An id that is blank, non-printable-ASCII, or surrounded by whitespace is refused locally, before
  the request is sent. That is wire grammar, not a server rule, and each case is a distinct way the
  id the caller logged and the id the server stores stop matching: a control byte is rejected by
  `net/http` inside `Do`, where the SDK would report it as `ErrUnreachable` ("nothing answered") for
  a request that was never sent; a byte above `0x7e` is *accepted* and then decoded `latin-1`
  server-side, landing in the audit row as mojibake; and outer whitespace is silently trimmed by
  `net/http` while writing the header, so `" req-abc "` would be recorded as `"req-abc"`.
- `docs/openapi-v2.yaml`, vendored from server 3.1.1.

### Changed
- `docs/roadmap.md` is re-derived from `openapi-v2.yaml` rather than edited. It had been written
  against server 1.0.0 and had drifted: `Tags.Resolve` was documented as taking `slug` +
  `application_id` when the endpoint takes four parameters including `scope`. Since #8–#13 delegate
  to that file, the drift would have been copied into six resources.
- Header assembly moved out of `doRaw` into `Client.headers`, which had grown past the point where
  the branches read clearly inline.

### Fixed
- **A nil `RequestOption` panicked instead of returning an error.** The transport now refuses one by
  name (`RequestOption 2 is nil`) before anything is sent, as `NewHealthClient` does for a nil
  `HealthOption`. Conditionally assembled option slices are where a nil comes from, and this library
  never panics. Found by Codex review of
  [#13](https://github.com/octoverse-id/octonomy-go/issues/13).
- **Single-resource responses decoded to zero-valued structs.** The server wraps every payload under
  `data` — single resources as `{"data": {...}}`, not only lists — so `Tags.Create`/`Get`/`Update`
  and the three `Vocabularies` equivalents returned an **empty struct with a nil error** against a
  real server. The vendored `docs/openapi.yaml` documents bare objects, every canned test body
  encoded the spec rather than the server, and so a complete unit suite stayed green throughout.
  Found by the compat line's smoke test on its first run against a container
  ([#32](https://github.com/octoverse-id/octonomy-go/issues/32)).
- **The `vuln` CI job stopped running govulncheck at all.** `golang/govulncheck-action` installs
  `golang.org/x/vuln/cmd/govulncheck@latest` and offers no version input, while `actions/setup-go`
  exports `GOTOOLCHAIN=local` so the pinned Go really is the Go used. When x/vuln v1.8.0
  (2026-09-08) raised its own minimum to go 1.26, the install began failing on
  `requires go >= 1.26.0 (running go 1.25.x; GOTOOLCHAIN=local)` — on `main` as well as on every
  PR — and the scan never ran. The job now installs with `GOTOOLCHAIN=auto` (which applies to
  *building* the tool; the scan still uses the pinned Go, so standard-library advisories stay
  reported against the version under test) and invokes `govulncheck` directly, so a failed install
  is a failed step rather than a skipped scan. No advisories against this module: all 18 findings
  seen while reproducing were artifacts of a local go1.25.4 and are fixed at the 1.25 patch CI
  resolves to. Same one-line fix applied to the install hints in `docs/development.md` and the
  `Makefile`, which reproduce the identical error on a go1.25 toolchain.

### Changed
- The transport is now one request path (`doRaw`) and three decoders chosen by response shape:
  `doData[T]` unwraps the single-resource envelope, `doList[T]` decodes the list envelope, and
  `Client.do` handles a call with no payload. Every shape that would previously have decoded to a
  zero value with a nil error is an error instead: a 2xx with no `data` key, a null `data` where a
  resource was expected, an empty body, a list response with no usable `pagination` block, and a
  non-204 answer to `Delete`. A present-but-null `"data"` on a list normalizes to an empty non-nil
  slice, identical to `"data": []`.

### Added
- `integration_test.go` (build tag `integration`, `make smoke`): a six-assertion smoke test against
  a real server, covering both response envelopes on both resources. Wired into CI as a
  non-advisory `smoke` job running `make smoke` with `OCTONOMY_SMOKE_REQUIRED=1`, so neither a
  harness that failed to export its credentials nor a test that no longer runs can report a vacuous
  green. It replaces the advisory bootstrap-only `harness` job, keeping that job's cross-step
  credential assertion as a step. Making it block the *merge* additionally needs its check context
  added to branch protection.
- Reusable Octonomy container harness (`scripts/octonomy-harness.sh`, `make dev-server`) that boots
  Postgres plus the pinned `ghcr.io/octoverse-id/octonomy:3.1.0` image, applies migrations, mints a
  namespace-capable service token, and writes `OCTONOMY_TEST_*` credentials to
  `.octonomy-harness.env`. Both SDK version lines invoke it, so neither carries a bootstrap of its
  own. Exposed to CI as the `.github/actions/octonomy-harness` composite action.

### Added
- **Tag aliases** ([#8](https://github.com/octoverse-id/octonomy-go/issues/8)). `client.Aliases`
  covers the full CRUD surface — `Create`, `Get`, `List`, `Update`, `Delete` on `/tag-aliases` — plus
  `client.Tags.ListAliases` for `GET /tags/{tag_id}/aliases`. `TagAlias` carries `NamespaceType` /
  `NamespaceID` (decode-only), which makes it the third of the seven v2 schemas that do.
- `TagAliasUpdate.TagID` re-points an alias at a different tag. That is a normal edit, not the scope
  change `PATCH` refuses: moving the alias itself between scopes is a `409` carrying the code
  `scope_immutable`, which reaches callers verbatim as `APIError.Code` and deliberately does **not**
  satisfy `IsConflict` — reading a fixed-scope refusal as a duplicate slug would send a caller down a
  retry path that cannot work.
- `TagAliasListParams` exposes the full documented filter set for the collection route
  (`application_id`, `include_shared`, `is_active`, `q` as `Query`, `slug`, `tag_id`, plus paging).
  `TagListAliasesParams` is a separate, narrower type for the nested route, which the contract
  documents with five parameters. One server function backs both routes, so `q` and `slug` would be
  honored on the nested one too; exposing them would put the SDK ahead of the published contract on a
  route the server is free to narrow.
- Note for callers filtering aliases: the server lists **active rows only** when `is_active` is
  absent. Since `Delete` is deactivation, `IsActive: octonomy.Bool(false)` is how deleted aliases are
  found.

### Added
- **Tag resolution** ([#9](https://github.com/octoverse-id/octonomy-go/issues/9)).
  `client.Tags.Resolve(ctx, slug, params, opts...)` resolves a slug to a tag, possibly by way of an
  alias, returning `*TagResolution` (`{MatchedType, MatchedAlias, Tag}`). One specialized read — the
  group has no list form and no writes. It works on both surfaces, with one caveat on `Scope` below;
  the namespace headers and `include_global` are v2-only.
- `ResolutionScope` (`ResolutionScopeGlobal`, `ResolutionScopeMerchant`) types the `scope` parameter
  the spec describes as a bare string. The server accepts exactly these two values.
  `ResolutionScopeMerchant` resolves within the request's own namespace, so the SDK refuses it
  locally on a request that has none.

  **`Scope` is not in the vendored v1 contract.** `docs/openapi.yaml` is still at server `1.0.0`,
  which predates the parameter; the running server adds it to *both* surfaces, verified against a
  3.1.0 container, where `/api/v1/tag-resolution` validates it by name — `scope=merchant` on a global
  request is rejected with "Merchant scope requires a namespaced request", and an unknown value with
  "Use 'global' or 'merchant'". So it is sent on v1 rather than gated to v2: the running server is
  the authority where the vendored spec is merely stale, and the SDK has no version handshake with
  which to gate it honestly. Against a v1 deployment older than the release that added it, the
  parameter is silently dropped like any unknown query parameter — the same exposure every other
  post-1.0.0 addition carries, and what #6 closes by re-vendoring the v1 contract at 3.1.1. `ResolutionScopeGlobal` is a legal explicit pin — the one place
  in this SDK where the literal `global` is accepted, as against the reserved `X-Namespace-Type` — and
  from a namespaced request it is *also* the authorization opt-in, so it does not need
  `WithIncludeGlobal` beside it: the server widens the authorized set for this route only when it
  sees `scope=global`.
- `MatchedType` (`MatchedTypeTag`, `MatchedTypeAlias`) types the response's `matched_type`.
  `MatchedAlias` is non-nil exactly when the match came through an alias, and `Tag` is the canonical
  tag either way.
- **An unmatched slug is a `400 validation_error`, not a `404`.** `IsValidation` is the branch that
  means "nothing is called that"; `IsNotFound` reports false. A `scope=global` resolution by a caller
  without the authority to see global rows returns that same error, indistinguishable on purpose, so
  the response cannot disclose the existence of rows the caller may not read.
- `WithIncludeGlobal` is now refused alongside `ResolutionScopeMerchant`, which is the second place
  the server silently discards that option rather than reporting it: merchant scope pins resolution
  to the request's namespace and `effective_resolution_scope` returns `include_global` false on that
  branch, whatever the query said. A caller who asked for global rows would have got merchant-only
  results with no sign the option did nothing — the same reason the option is already refused on
  writes. Pairing it with `ResolutionScopeGlobal` stays legal: redundant is not contradictory.
- **The two ambiguity axes arrive under different codes**, so handling only one misses half the
  cases: rows differing by *application* are `ambiguous_resolution` (`IsAmbiguousResolution`) with
  `Details["application_id"]`, while canonical tags differing by *type* are a plain
  `validation_error` (`IsValidation`) with `Details["type"]`. Both verified against a running 3.1.0
  server, not read off the spec.

### Added
- **Tag assignments, including bulk** ([#10](https://github.com/octoverse-id/octonomy-go/issues/10)).
  `client.Assignments` covers `Create`, `Remove`, `BulkAssign`, and `BulkRemove` — linking tags to
  external resources, which is the core operation of a tagging service. `Assignment` carries
  `NamespaceType` / `NamespaceID` (decode-only), making it the fourth of the seven v2 schemas that do,
  and the only one the contract does not mark them `required` on while the runtime emits them anyway.
- `Assignment.ApplicationID` is a plain `string`, not the `*string` on `Tag`, `Vocabulary`, and
  `TagAlias`: an assignment is always application-scoped, so there is no tenant-shared case and no nil
  to represent.
- **`Create` is idempotent.** Re-assigning a tag already on a resource returns the existing row with a
  `200` instead of a `201`, and is not an error. The SDK returns the same `*Assignment` either way and
  does not surface which happened — `BulkAssign` with a single tag id answers that directly, since its
  `Created` / `Existing` counts are the same fact carried in the body. The tag may be named by exactly
  one of `TagID`, `AliasID`, or `AliasSlug`.
- **`Remove` sends a body on a `DELETE`**, which is how the row is identified: there is no
  `/tag-assignments/{id}` route. One consequence reaches callers — `WithApplication` is refused there,
  as on any body-carrying request, because `AssignmentRemove.ApplicationID` is authoritative. Removing
  an assignment that does not exist is a `204`, not a `404`, and removal is a real delete rather than
  the deactivation tags, vocabularies, and aliases get: an assignment is a link, and an inactive link
  is an absent one.
- **The bulk responses are composites under the `data` envelope, and the vendored spec describes
  neither correctly.** `openapi-v2.yaml` claims `bulk-assign` returns a bare array and documents no
  schema at all for `bulk-remove`. Probed against a running 3.1.0 server, they return
  `{"data": {"created": N, "existing": N, "skipped": N, "assignments": [...]}}` and
  `{"data": {"removed": N}}`. Both go through `doData` with a result struct: a `[]Assignment` decoder
  written from the spec would return an empty slice and a nil error against the real body, which is
  [#32](https://github.com/octoverse-id/octonomy-go/issues/32) in a new place. A regression test
  asserts both spellings of the spec's claim are errors rather than empty results.
- `BulkAssignResult.Skipped` is **always zero** on server 3.1.x and exists only because the server
  emits it: nothing is skipped because nothing is tolerated, an unknown tag id failing the entire call
  instead. An id outside the request's namespace reports identically to one that exists nowhere, so
  the response cannot be used to probe for tags in namespaces the caller cannot read.
- **The bulk results require the keys a caller acts on**, rather than letting an unexpected object
  shape decode to a zero-valued result with a nil error. `doData` stops at the `data` envelope, which
  is the right line for a *resource* — a zero-valued `Assignment` has an empty `ID` and no caller
  mistakes it for an answer — but a composite of counters is different: `created: 0, existing: 0` with
  no rows is an ordinary result, and `removed: 0` is the single most common one there is. So a renamed
  or missing `created`, `existing`, `assignments`, or `removed` is an error. `Skipped` is exempt,
  being vestigial; a present-but-null `assignments` normalizes to an empty non-nil slice, exactly as
  `doList` treats a null page.
- `Assignment`'s namespace pair is asserted two ways, because neither alone catches a misspelled
  field name: a unit test decodes a **raw** body written with the wire's own key names, and the smoke
  test creates a **namespaced** assignment and checks the pair the server populates. Every other
  fixture is marshalled from `Assignment` itself, so a wrong json tag is used for both the write and
  the read and round-trips perfectly — verified by breaking the tags, which left the entire unit suite
  *and* the real-server smoke test green.
- `BulkRemove` takes canonical tag ids only, with no alias form — the asymmetry with `BulkAssign`,
  which also accepts `AliasSlugs` and unions them with `TagIDs`. It tolerates ids matching nothing,
  counting them out of `Removed` rather than raising. Both bulk calls cap at the deployment's
  `MAX_BULK_TAGS` (200 by default).

### Fixed
- **Path segments were escaped twice, so an id containing a space, `%`, or `#` addressed the wrong
  resource.** `url.PathEscape` produced `%20` and `net/url` escaped the result again when rendering
  the `url.URL.Path` it was assigned to, so `"ord 9"` went out as `ord%2520` and reached the server as
  the literal `ord%209`. `doRaw` now sets both halves of the path pair through `Client.resolvePath`,
  so each segment is escaped exactly once.

  Harmless while every path segment was a uuid, which is why it survived since the first release. It
  stopped being harmless at `/resources/{resource_type}/{resource_id}`: a resource id is a
  **caller-chosen external identifier** the server validates only as non-blank, and `ReplaceTags` is
  destructive — so a wrong id silently replaced the tag set of a resource the caller never named.
  Confirmed against a running server, where the two spellings produce two distinct rows.

  A resource id containing a **`/`** remains unaddressable however it is escaped: the server's Django
  route receives a decoded slash from WSGI and matches nothing, answering an envelope-less `404` that
  the SDK reports as `IsUnexpectedStatus`. Octonomy will *store* such an id (via `Assignments.Create`,
  whose id travels in the body) but will not *route* it — a server-side gap the SDK documents rather
  than rejects, since the failure is already loud.

### Added
- **Health probes** ([#13](https://github.com/octoverse-id/octonomy-go/issues/13)). `Health.Live` and
  `Health.Ready` reach `/health/live` and `/health/ready`, the one group that sits **outside the API
  surface in three ways at once**: rooted at the server root rather than under `/api/<version>`,
  answering with a bare `{"status": "ok"}` that carries **no `data` envelope**, and authenticating
  nobody. They are reachable two ways — `client.Health` for a caller who already has a full client,
  and the new credential-free constructor for one who has no credentials at all — and both run the
  same code and send the same request.
- **`NewHealthClient(baseURL, ...HealthOption)`**, a constructor requiring **only a base URL**, with
  `WithHealthHTTPClient` (the knob a probe loop wants: the 30s default timeout is rarely right for
  one) and `WithHealthUserAgent`. It exists because `New` rejects a blank `Token` or `TenantID`, so
  until now a caller could not construct a client *at all* in order to reach an endpoint that needs
  neither. `New`'s validation was **not** loosened and `doData`'s envelope requirement was **not**
  relaxed to make health fit — either would have cost every other resource the guarantees those
  checks exist for. A `HealthClient` exposes `Health` and nothing else, and the API transport refuses
  a credential-free client outright rather than sending a blank `Authorization` header.
- **`ErrUnreachable`**, matched with `errors.Is`, marks a request that received **no HTTP response at
  all** — connection refused, DNS, TLS, a client timeout, a cancelled context. It is not
  health-specific: every method in the package wraps it, so "the server never answered" is now
  distinguishable from "the server answered and said no" everywhere. The cause survives the wrap
  (`errors.Is(err, context.DeadlineExceeded)` still works), and the error message is unchanged.
- **`CodeNotReady` / `IsNotReady`**, for a health probe the server **answered** with a non-2xx and its
  own `{"status": …}` body — `/health/ready` returns `503 {"status": "unavailable"}` when its
  database connection will not open. `APIError.Details["status"]` carries the server's own word.

  **Unreachable and unready are never collapsed into one error**, because they call for different
  operator responses: back off and re-probe, versus go looking for the process. And `not_ready` is
  **not** the status-to-code mapping `CodeUnexpectedStatus` exists to forbid — nothing is inferred
  from an HTTP status here; the code is established by the health view's own server-authored payload.
  A 503 whose body is an HTML error page is still `CodeUnexpectedStatus`, which is what keeps "the
  application says it is unready" apart from "a load balancer answered because nothing is behind it".
- **Audit logs** ([#12](https://github.com/octoverse-id/octonomy-go/issues/12)). `client.AuditLogs.List`
  reads the append-only mutation history, with `Tags.ListAuditLogs` and `Resources.ListAuditLogs` as
  the pre-filtered nested routes. One new model, `AuditLog`, which completes the seven v2 schemas
  carrying `NamespaceType` / `NamespaceID`. **List-only in every sense**: the server writes these rows
  itself as a side effect of the mutation they describe, so there is no create, no update, no delete,
  and no `Get` — a single row is reached by filtering the collection.
- **Audit reads need the `audit:read` scope**, which service tokens carry separately from `tags:read`
  and `tags:write`. A token without it gets a `403 forbidden` (`IsForbidden`) from all three routes
  while its ordinary reads keep working — never an empty page. It is a token misconfiguration rather
  than a caller mistake, so retrying or narrowing the filters will not help.
- **`AuditLog.Changes` is an open `Metadata` object rather than a `Before`/`After` struct, and one row
  shape forces that.** The contract types the field as nothing at all. The server writes
  `{"before": {…}, "after": {…}}` — but a `tag.deactivated` that cascaded to aliases adds
  `cascaded_alias_ids`, whose value is an **array**. A typed pair would drop it silently; a
  `map[string]Metadata` would fail to decode the row and take the whole page down with it, since one
  bad element fails the list. Callers read `log.Changes["after"].(map[string]any)`.
- **`AuditLog.OperationID` groups every row one operation emitted**, which is what makes a multi-row
  mutation reconstructable weeks later: `Resources.ReplaceTags` and both bulk calls write one row per
  assignment they touch under a single operation id, so the removals and additions read as one act
  rather than as unrelated churn. `RequestID` correlates a row with the one HTTP request that produced
  it, and with `APIError.RequestID` for a request that failed. Pass
  [`WithRequestID`](https://github.com/octoverse-id/octonomy-go/issues/5) to supply your own; the
  server mints one when the caller sends none.
- **Rows arrive newest first** (`created_at` descending, `id` as a stable tiebreak), so offset paging
  walks backwards through history. Asserted against a real server, since page order is a property of
  the server's query and no fixture can establish it.
- **An unknown tag or resource is an empty page on the nested routes, not a `404`** — unlike every
  other `/tags/{id}` route in this SDK. Both filter the audit table by the path's identifier and never
  load the entity, so a row that never existed, one that was deactivated, and one outside the
  request's namespace are reported identically: `200` with no rows. Only a `tagID` that is not a uuid
  fails, at the server's router, as an envelope-less `404` (`IsUnexpectedStatus`).
- `AuditLogListParams` carries the full documented filter set; `TagListAuditLogsParams` and
  `ResourceListAuditLogsParams` carry the four each nested route documents. They are deliberately
  narrower, and deliberately separate types: one filter function serves all three routes on server
  3.1.x, so `entity_type` would be honored on the tag route too, but exposing it would put the SDK
  ahead of the published contract on a route the server is free to narrow — the same reasoning
  `TagListAliasesParams` records against `TagAliasListParams`.
- On `/api/v2`, audit reads are **namespace-filtered and global rows fail closed**, inherited whole
  from the transport's scoping options: a namespaced read returns that namespace's rows and no global
  ones, `WithIncludeGlobal` asks for both, and an exact merchant grant with no global authority still
  sees none. The smoke test proves the exclusion against a real server, which is the only way to prove
  the headers arrived — without them the server serves the global namespace with a `200`.
- **Resource tags** ([#11](https://github.com/octoverse-id/octonomy-go/issues/11)). `client.Resources`
  covers `ListTags` and `ReplaceTags`, and `client.Tags.ListResources` completes the mirror. Two new
  models: `ResourceTag` (a tag as seen from a resource, with the `Tag` nested whole) and `TagResource`
  (a resource as seen from a tag). Both carry `NamespaceType` / `NamespaceID`, taking the SDK to six
  of the seven v2 schemas that do.
- **`ReplaceTags` replaces, it does not merge.** Every tag on the resource and absent from the request
  is removed. To add, read the current set first and send the union.
- **An empty replace is legal and clears the resource.** `ResourceReplace` with no `TagIDs` and no
  `AliasSlugs` removes every tag — a deliberate difference from `BulkAssign`, which refuses an empty
  request. An empty slice reaching that call by accident, from a filter that matched nothing, wipes
  the resource silently and successfully. Proven against a real server rather than only documented.
- **The replace response is a third composite shape, and the vendored spec is wrong about it twice
  over.** `openapi-v2.yaml` claims a bare array *and* claims the elements are `ResourceTag`. The
  server sends `{"data": {"created": N, "removed": N, "tags": [...]}}`, where those are **`Tag`**
  values — no `AssignmentID`, no `AssignedAt`. A client written from the spec decodes an empty slice
  and a nil error; one that guessed the envelope but kept the element type decodes tags with every
  field empty. `ResourceReplaceResult` requires `created`, `removed`, and `tags` on decode, for the
  reason established with the bulk results: zero is an ordinary answer here, so a renamed key would
  read as "nothing needed changing".
- `ResourceReplace` deliberately carries **no** `ResourceType` or `ResourceID`, though the contract
  lists them: they come from the path, and the server overwrites whatever a body sends. A field that
  cannot affect the request does not belong on it.
- `ResourceListTagsParams.ApplicationID` is **required by the server** — the only list in this SDK
  where that holds — and may equally be supplied with `WithApplication`. `IncludeInactive` is not the
  `is_active` filter the tag and alias lists take: it is a different parameter with different
  polarity, where nil means active-only and true *widens* to include deactivated tags. There is no way
  to ask for deactivated tags alone.
- Both new models' namespace pairs are asserted two ways — a unit test decoding a **raw** body written
  with the wire's own key names, and namespaced live calls checking what the server populates. Neither
  alone suffices: renaming the tags left the entire unit suite green until the raw-body test existed,
  because every other fixture is marshalled from the struct it is decoded into.

## [0.1.0] - 2026-06-08

> **Never released.** No `v0.1.0` git tag was ever cut and the module proxy has never served this
> version, so nothing below was ever installable. The entry is kept because it accurately describes
> the code in the tree; the version label is corrected when the first real releases are cut
> (`v1.0.0` on the compat line, `v2.0.0-alpha.1` here) in their dedicated release PRs.

Initial contents of the Octonomy Go SDK. Targets the stable Octonomy REST **v1** API
(server release `1.0.0`, served under `/api/v1`). Dependency-free (standard library only).

### Added
- Client foundation: `New(Config)` with `BaseURL`/`Token`/`TenantID` validation, a configurable
  `*http.Client`, and a shared transport that sets `Authorization`, `X-Tenant-ID`, optional
  `X-Actor-ID`, `Accept`, and `User-Agent` headers.
- Typed error handling: `*APIError` decoded from the `{error:{code,message,details,request_id}}`
  envelope, error `Code*` constants, and `IsNotFound`/`IsConflict`/`IsValidation`/`IsAuthError`/
  `IsForbidden`/`AsAPIError` helpers.
- Pagination: generic `List[T]` decoding the `{data, pagination}` envelope, plus `ListOptions`.
- `Vocabularies` service: Create, Get, List, Update, Delete.
- `Tags` service: Create, Get, List (full filter set), Update, Delete.
- `WithActor` per-request option, and `String`/`Bool`/`Int` pointer helpers for optional fields.
- Runnable `examples/quickstart` program and a vendored `docs/openapi.yaml` contract reference.

[Unreleased]: https://github.com/octoverse-id/octonomy-go/commits/main

<!-- No [0.1.0] link definition: that tag does not exist. Both this and the former
     compare/v0.1.0...HEAD link returned 404 because they referenced a release never cut.
     Real link definitions land with the first release PRs. -->
