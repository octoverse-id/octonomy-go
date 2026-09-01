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
- `docs/openapi-v2.yaml`, vendored from server 3.1.1.

### Changed
- `docs/roadmap.md` is re-derived from `openapi-v2.yaml` rather than edited. It had been written
  against server 1.0.0 and had drifted: `Tags.Resolve` was documented as taking `slug` +
  `application_id` when the endpoint takes four parameters including `scope`. Since #8–#13 delegate
  to that file, the drift would have been copied into six resources.
- Header assembly moved out of `doRaw` into `Client.headers`, which had grown past the point where
  the branches read clearly inline.

### Fixed
- **Single-resource responses decoded to zero-valued structs.** The server wraps every payload under
  `data` — single resources as `{"data": {...}}`, not only lists — so `Tags.Create`/`Get`/`Update`
  and the three `Vocabularies` equivalents returned an **empty struct with a nil error** against a
  real server. The vendored `docs/openapi.yaml` documents bare objects, every canned test body
  encoded the spec rather than the server, and so a complete unit suite stayed green throughout.
  Found by the compat line's smoke test on its first run against a container
  ([#32](https://github.com/octoverse-id/octonomy-go/issues/32)).

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
