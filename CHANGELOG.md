# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0] - 2026-06-08

Initial release of the Octonomy Go SDK. Targets the stable Octonomy REST **v1** API
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

[Unreleased]: https://github.com/octoverse-id/octonomy-go/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/octoverse-id/octonomy-go/releases/tag/v0.1.0
