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
  every `/api/v1` response. All **seven** v2 schemas that carry namespace identity now have them:
  the five remaining (`TagAlias`, `Assignment`, `TagResource`, `ResourceTag`, `AuditLog`) landed with
  their resources later in this same unreleased set (see [`docs/roadmap.md`](docs/roadmap.md)).
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
- **`CodeScopeImmutable` / `IsScopeImmutable`, and `docs/openapi.yaml` re-vendored from server
  3.1.1** ([#6](https://github.com/octoverse-id/octonomy-go/issues/6)). The v1 spec had been pinned
  at server `1.0.0`; both vendored specs now track the same server release. The v1 contract itself
  moved by 53 lines: a documented `409 scope_immutable` on the detail `PATCH` for tags,
  vocabularies, and tag aliases; the `scope` query parameter on `/tag-resolution`, which
  `Tags.Resolve` was already sending against a running server; an `ErrorResponse` schema component;
  and `default: true` on `is_active` in the `Tag`, `TagAlias`, and `Vocabulary` response schemas,
  which records a default the server always applied and needs no SDK change.

  `IsScopeImmutable` is convenience, not a fix: `parseError` already preserved the code verbatim, so
  `APIError.Code == "scope_immutable"` worked before this. What it adds is a name for the one
  branch a caller must not get wrong — the server raises it as a subclass of its conflict error, so
  it carries `409` while its code is **not** `conflict`, and `IsConflict` reports false for it. A
  caller keying on the *status* reads "duplicate slug, pick another" and retries a request that can
  never succeed. Scope is fixed at creation; the remediation is to re-create the row in the target
  scope. `APIError.Details` names the offending fields. From this SDK only `ApplicationID` can raise
  it, on **either** surface: the server's rule covers all three scope fields and its detail-PATCH
  view is shared by `/api/v1` and `/api/v2`, but namespace is header-set rather than body-set, so
  the three `*Update` structs carry no namespace field for a PATCH to move.

  The 409-versus-`conflict` split is asserted in `integration_test.go` against a real server, not
  only against a canned fixture: a fixture asserting that `IsScopeImmutable` is true and
  `IsConflict` is false on the same response is a fixture asserting what this SDK already believes.
  The smoke step moves a global vocabulary into an application, and also asserts that the refused
  PATCH left the row unchanged.

  **No `Scope` field was added to `TagListParams`.** The parameter belongs to `/tag-resolution` on
  both surfaces and appears exactly once per spec; the tags list route has none.
- **`Each` pagination walker and `DecodeMetadata` typed metadata**
  ([#14](https://github.com/octoverse-id/octonomy-go/issues/14)). Both are generic, so both belong to
  the modern line only — the frozen `v1.x` compat line has no generics and gets neither.

  `Each(ctx, start, page, fn)` is the offset loop everyone was writing by hand, with the termination
  condition they were getting wrong. **It issues one HTTP request per page**, which is stated in the
  doc comment, in the README, and in `doc.go`, because one call making N round trips is in real
  tension with this package's no-hidden-behavior promise and naming plus documentation is the whole
  mitigation. It advances by the number of items that **arrived**, not by the `Limit` requested — the
  server silently clamps `Limit` to 200, so a walk at `Limit: 500` that trusted its own arithmetic
  would skip three items in every five. It stops on the server's `next == nil` *and* on an empty
  page, the second being what guarantees termination when the first is wrong.

  It returns `start.Offset` plus the number of items processed — the first item it did **not**
  process, which is what makes it a resume point: the page start on a fetch failure, the failing item
  on a callback failure. A walk that dies on page 40 of 100 keeps 39 pages of progress. An offset is a
  **position, not an identity**, so what a resume does with it is conditional: over a stable,
  unchanged collection it re-delivers the item that failed, but where rows moved — or on the tags
  list, where they need not have — it may address a different row, so a resume *may* retry the failed
  item and may equally skip it. Neither at-least-once nor at-most-once is on offer, and a stable
  `ORDER BY` would not buy them either — a row inserted or removed *before* the offset shifts
  everything after it, deterministic sort or not. Nor would a keyset cursor on its own: it removes
  the positional shift but is only as stable as the key it seeks on, and that varies by endpoint —
  vocabularies and aliases sort on `name`/`slug`, which a caller can edit mid-walk, while audit logs
  and assignments sort on an insert-time timestamp that never changes, and the tags list has no order
  to seek on at all. A consistent view of a moving collection needs a **snapshot**, which is the
  server's to offer. It is also not a polling cursor: a row created
  since that sorts after the old tail turns up, one sorting before it never does.

  A cancelled context is observed **before the next callback**, not only at the next fetch. Handing
  `ctx` to the page function alone left a real hole on the final page — with no further fetch to
  notice, a walk cancelled part-way through it delivered the rest of the page and returned a nil
  error. Caught in review, fixed, and pinned by a test that fails without the check.

  The page function must pass through the `ListOptions` it is handed. Ignoring the offset would
  re-fetch page one forever; `Each` detects that from the offset the server echoes and returns an
  error naming it instead of looping. That guard claims only the non-terminating shape: a dropped
  `Limit` is invisible to it, and a walk that fits in one page succeeds either way.

  **Offset drift is documented, not papered over**, and the ordering it depends on is per endpoint:
  vocabularies and tag aliases sort by `(name, slug, id)`, audit logs by `(created_at DESC, id)`,
  assignments and resource tags by `(assigned_at DESC, id)`. A concurrent create or delete shifts the
  window either way, and an item can be delivered twice or skipped.

  **`GET /tags` has no `ORDER BY` at all**, which is worse than drift and was found while writing
  this. Its view annotates `usage_count`, making the query a `GROUP BY`, and Django drops
  `Meta.ordering` from aggregate queries — verified against a running 3.1.0 server, where the emitted
  SQL ends at `GROUP BY` and Django's own `queryset.ordered` reports false. `LIMIT`/`OFFSET` over an
  unordered query is undefined, so a tags walk may repeat or miss rows **with no concurrent writes at
  all**. Documented on `Each` as best-effort, with the mitigation that is actually available: compare
  the first page's `Pagination.Count` against the number of distinct IDs walked. It reads in **one
  direction only** and only for a complete walk from offset 0 — `Count` is the size of the whole
  collection, not of the part still ahead, so a resumed walk legitimately sees fewer. Fewer proves
  rows were missed; equal proves nothing, since a concurrent create and delete cancel out in the
  total. It detects a short walk; nothing client-side can prevent one.

  `DecodeMetadata[T](m)` decodes a resource's `Metadata` into the caller's own struct. It is a
  **function, not a method**: `Metadata` is a type *alias* for `map[string]any` and Go does not allow
  methods on aliases. Promoting it to a defined type would not break assignment — Go still accepts a
  plain map there — but it would change type identity for every type switch, reflection site and
  signature naming it. A nil or empty map yields the zero value of `T` and no error, short-circuited
  rather than round-tripped so the promise holds for a pointer or map `T` too; on any error the
  **zero** value comes back, never the half-filled struct `encoding/json` leaves behind when it hits a
  type mismatch mid-decode.

  **Large integers MAY lose precision, and not here.** Where they do, it happened when the *response*
  was decoded into `map[string]any`, whose JSON numbers are `float64` — before `DecodeMetadata` is
  called and beyond its power to recover. "Above 2^53" is not the rule, and neither is the tempting
  repair "but even numbers survive": float64 loses resolution in **doubling steps** — every integer is
  exact below 2^53, only the even ones between 2^53 and 2^54, only multiples of four past 2^54. That
  is precisely why the caveat says *may*. A `Metadata` the caller built holding a real `int64` is
  unaffected. The issue suggested "decode the raw JSON yourself"; this SDK has no first-class hook for
  that, though `Config.HTTPClient` does let a custom `RoundTripper` copy the body first. The simple
  fix is to store such values as **strings** and parse them out. Tests pin the loss at `2^53+1`, the
  survival of `2^53+2`, the loss of `2^54+2`, the caller-built exactness, and the string workaround.

  Both are asserted against a real server in `integration_test.go` as well as against fixtures. The
  fixture reproduces four beliefs about the server's paginator — `count` is the total, `next` goes nil
  at the end, `limit` is clamped to 200 and echoed, `offset` is echoed. `Each` reads two of them:
  `next` to stop, and the echoed `offset` to catch a dropped `ListOptions`. The clamp is why
  "advance by what arrived" is the correct rule, and `count` is what a *caller* needs to detect a
  short walk — neither is read by the walker. All four are now asserted against a running server
  rather than only against the fixture that agrees with them. The real-server walk uses the **alias** route, whose order is total, rather than the tags list
  that has none.

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
  change `PATCH` refuses: moving the alias itself between scopes is a `409 scope_immutable`
  (`IsScopeImmutable`), which deliberately does **not** satisfy `IsConflict` — reading a fixed-scope
  refusal as a duplicate slug would send a caller down a retry path that cannot work.
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

  **`Scope` is sent on both surfaces**, and `docs/openapi.yaml` documents it on
  `/api/v1/tag-resolution` as of the #6 refresh above. It was absent from the vendored v1 contract
  only while that file sat at server `1.0.0`, which predates the parameter; sending it on v1 was
  already right then, verified against a 3.1.0 container where `/api/v1/tag-resolution` validates it
  by name — `scope=merchant` on a global request is rejected with "Merchant scope requires a
  namespaced request", and an unknown value with "Use 'global' or 'merchant'". Gating it to v2 would
  have refused a call every current deployment answers, and the SDK has no version handshake with
  which to gate it honestly. Against a v1 deployment older than the release that added it, the
  parameter is silently dropped like any unknown query parameter — the same exposure every other
  post-1.0.0 addition carries. `ResolutionScopeGlobal` is a legal explicit pin — the one place
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

### Documentation
- **Documentation truth pass across the whole tree**
  ([#15](https://github.com/octoverse-id/octonomy-go/issues/15)). Every file that asserted a Go
  floor, an API version, a list-type shape, or a release state that is false for the line it
  describes is corrected, and the facts the two-line split created are written down for the first
  time.
  - **Two Go floors, stated as two.** `AGENTS.md`, `CONTRIBUTING.md`, `docs/development.md`, and the
    README said "Go 1.24+" flatly. Each now names the line it is describing: `main` is
    `.../octonomy-go/v2` at Go 1.24+, `support/go1.13` is `.../octonomy-go` at Go 1.13 with no
    generics, no `any`, and no post-1.13 standard library. `AGENTS.md` and `CONTRIBUTING.md` said
    "List methods return `*List[T]`" as an unqualified rule; that spelling is `main`-only, and the
    compat line's per-resource `*TagList` / `*VocabularyList` is now recorded next to it.
  - **A published sunset date with a named owner: 2027-08-31.** It was previously a rule ("12 months
    from the `v1.0.0` tag") rather than a date, which is not something a blocked team can plan
    against. The date, its derivation from the 2026-08-26 tag, and its owner now appear in
    `SECURITY.md`, `docs/versioning.md`, the README, and `doc.go`, matching the copies already on
    `support/go1.13`.
  - **`SECURITY.md` on `main` reflects both lines.** It claimed security fixes go to "the latest
    `0.x` release" and listed only `0.1.x` — a version that does not exist. It now carries the
    two-module supported-versions table, the sunset, the backport rule, and the two facts a Go 1.13
    consumer needs: that toolchain is itself unpatched past `go1.13.15` (August 2020), and `retract`
    cannot recall a release for it.
  - **The release record is accurate.** `README.md` and `docs/versioning.md` said the repository had
    no git tags and nothing published. `v1.0.0` was tagged on `support/go1.13` on 2026-08-26.
    `docs/versioning.md` now carries a Release state section covering both lines, why `v1.0.0` is not
    an ancestor of `main`, and why `git describe` here will not find it.
  - **One live versioning policy.** The `0.3.0`-minor framing an earlier plan carried is recorded as
    settled: v2 support ships on a new module path at `v2.x`, a major, which is what the
    major-effort rule in `docs/versioning.md` already required.
  - **The consumer-side `exclude` snippet is documented as unnecessary**, not omitted. The `/v2`
    module path made Go itself the enforcement, so a reader following an older plan document is told
    so explicitly rather than left hunting for a snippet that no longer exists.
  - **One canonical resource inventory.** It was restated in five places and drifting. `docs/api.md`
    is now the single source of truth; the README, `docs/roadmap.md`, `docs/architecture.md`, and
    `docs/versioning.md` link to it. `docs/versioning.md` still claimed only Vocabularies and Tags
    were implemented, and `docs/api.md` still listed three namespace-carrying schemas rather than
    seven.
  - **`docs/release.md`** gains the three branch roles (`support/` line, `<type>/<issue>-`
    implementation, `release/vX.Y.Z` tag PR) and a worked backport procedure — branch off the support
    line, expect the cherry-pick to need rewriting against a tree with no generics, and let the
    `compat guard` and `go1.13` jobs prove it, since a modern toolchain enforces the language version
    from `go.mod` but not the standard library.
  - **`docs/roadmap.md`** stops restating what exists and becomes the recipe plus a register of known
    gaps, each against its issue (#36, #37, #40, #49) with the two deliberate deferrals (#20, #22)
    named as such.
- **The `http.RoundTripper` extension point is documented** in the README, `doc.go`, and
  `docs/architecture.md`. Since `AGENTS.md` forbids logging in the library, a `RoundTripper` on the
  caller's `*http.Client` is the sanctioned path for metrics, tracing, request logging, retries, and
  rate limiting — and it pairs with `WithRequestID` to join a client span to the server's audit row.
- **`MaxIdleConnsPerHost` is documented as the first scaling bottleneck *on HTTP/1.1*, and
  deliberately not tuned.** `http.DefaultTransport` sets `ForceAttemptHTTP2`, so against an HTTPS
  endpoint that negotiates h2 every request multiplexes over one connection and none of this applies
  — the docs say so first, because advice given without that qualifier sends readers tuning something
  inert. Where the connection really is HTTP/1.1, `DefaultTransport` leaves the field unset and it
  falls back to `http.DefaultMaxIdleConnsPerHost` — **2** — so every **concurrent** call past the
  second opens and discards its own connection instead of pooling it. It caps pooled connections
  rather than in-flight ones, so nothing blocks and sequential work never meets it on either protocol
  (`Each` issues its pages one at a time and reuses one connection); a fan-out across goroutines
  sharing one `*Client` is what hits it. Raising it silently would be a capacity decision taken inside
  the caller's process, so the README shows the `http.DefaultTransport.Clone()` recipe instead (a bare
  `&http.Transport{}` drops the proxy, dialer, and HTTP/2 settings). The pool belongs to the
  **transport**, not the client, which is what makes sharing one `*Client` the simple correct default.
- **"Never retries" is stated accurately as "adds no retry loop."** `net/http`'s own transport already
  retries a request it failed to write on a *reused* connection; that is recovery from a half-closed
  idle socket rather than a retry policy, and the previous absolute wording would have misled anyone
  reasoning about idempotency.
- **`version.go` trailing the published tags is disclosed rather than left to surprise.** The
  `Version` constant on `main` still reads `0.1.0` — so the default User-Agent is `octonomy-go/0.1.0`
  — because `version.go` is bumped only in the release PR. `docs/versioning.md` now says to read the
  git tag for what is published and `version.go` for what the next release PR will bump. The claim
  that no `v0.x` was ever published is now backed by a re-runnable `proxy.golang.org` query rather
  than by assertion.
- **`docs/development.md` no longer calls the integration suite "six assertions."** It has grown into
  a 1,235-line ordered walk covering both envelopes, pagination and `Each`, `DecodeMetadata`,
  `409 scope_immutable`, the namespace axis, aliases, resolution, both bulk composites, the
  resource-tag replace, audit rows, and request-id correlation.
- **The "two response envelopes" framing is corrected where it implied a closed set.**
  `docs/architecture.md` said the envelopes *are* the deliberate divergences; the two bulk-assignment
  responses and the resource-tag replace are three more, and `docs/api.md` — which carries the
  complete list — is now what it points to. `docs/api.md` also no longer says the server wraps
  *every* payload under `data`: the health probes answer with a bare `{"status": "ok"}`, as the same
  page says further down, and its request-header table was missing `X-Request-ID` entirely.
- **`docs/release.md`** no longer says `go get` resolves tags "directly from GitHub" — the default
  path is `proxy.golang.org`, whose permanent cache is precisely why a tag cannot be unpublished —
  and it now distinguishes what the two compat checks actually do: `go1.13` builds, vets, and runs
  `go test -race` under a real toolchain, while `compat guard` never compiles the package and asserts
  the `go.mod` invariants.
- **The decode guarantee is stated at its real boundary, which is the envelope.** `docs/api.md` and
  `doc.go` said a 2xx whose body "does not match the shape above" is an error, never a zero value.
  `doData` asserts that `data` is present and non-null and then unmarshals; a *well-formed* envelope
  carrying the **wrong object** (`{"data": {"wrong": true}}`) still yields a zero-valued resource with
  a nil error. That is [#40](https://github.com/octoverse-id/octonomy-go/issues/40), open — and the
  overstated guarantee contradicted the gap this same release documents. Both pages now say what #32
  actually closed ("the envelope is missing") and name the composites that do require their keys.
- **The health carve-out is applied everywhere the unqualified claim appeared**, not only where the
  probes are described: `AGENTS.md`, `README.md`, `doc.go`, `docs/architecture.md`, and `docs/api.md`
  each said *every* request carries the token and tenant, or that *every* 2xx payload is `data`-wrapped.
  `docs/api.md` also said the probes "carry none of it" — they carry no credentials, but they do send
  `Accept` and `User-Agent`.
- **"Byte-for-byte identical" is qualified.** The two health entry points share code, path, and
  credential behavior, but `Config.UserAgent` and `WithHealthUserAgent` are set independently, so the
  `User-Agent` can differ.
- **The compat-line install note no longer implies automatic patching.** An unversioned `go get`
  selects the highest `v1.x` at that moment and then records an exact `require`; a later security
  patch needs `go get ...@latest`. The README now shows that command instead of implying it happens.
- **The `MaxIdleConnsPerHost` note is narrowed again** after a second pass: it is an *idle-connection*
  cap, so the symptom is handshake churn rather than a concurrency ceiling; HTTP/2 can still open more
  than one connection; a plaintext HTTP/1.1 connection involves no TLS handshake; and a bare
  `&http.Transport{}` loses `DefaultTransport`'s tuned defaults but **not** HTTP/2, which it still
  negotiates on its own.
- **`version.go`'s `0.1.0` is described as a pre-release placeholder**, not as "trailing the tags" —
  no tag on this line corresponds to it and `v1.0.0` belongs to the other module. The proxy evidence
  is presented as the proxy's current view of resolvable versions, with `-w` on the command so a
  network failure cannot masquerade as an empty list.
- The bug-report template asked for a version "e.g. `v0.1.0`", which was never released, and did not
  ask which of the two modules the reporter imports — the first thing triage needs. Both fixed. The
  PR template now checks the base branch against the line and the no-version-bump rule, and both
  templates cite `openapi-v2.yaml` alongside `openapi.yaml`.

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
