# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
