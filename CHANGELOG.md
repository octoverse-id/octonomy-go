# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

> **This branch is the frozen Go 1.13 compat line** (`support/go1.13`, module
> `github.com/octoverse-id/octonomy-go`, `v1.x`). Entries below ship as **`v1.0.0`**, cut in its own
> `release/v1.0.0` PR (#29) — `AGENTS.md` keeps version bumps and tags out of chore PRs, so
> `version.go` still reads `0.1.0` here.

### Changed
- **The client now compiles and tests on real Go 1.13** (#4). `go.mod` declares `go 1.13` and the
  **unsuffixed** module path `github.com/octoverse-id/octonomy-go`; `main` keeps `/v2`. Go treats the
  two paths as different modules, so nothing can move a consumer between the lines.
- **`List[T]` is gone.** Type parameters need Go 1.18. Each resource declares its own list envelope
  with identical fields:

  | Before (`/v2`, Go 1.18+) | After (this line) | Returned by |
  | ------------------------ | ----------------- | ----------- |
  | `List[Tag]` | `TagList` | `Tags.List` |
  | `List[Vocabulary]` | `VocabularyList` | `Vocabularies.List` |
  | `Pagination` | `Pagination` *(unchanged, shared)* | both |
  | `ListOptions` | `ListOptions` *(unchanged, shared)* | both |

  Both new types keep `Data []T` and `Pagination Pagination`, so only the type name in a variable
  declaration or function signature changes. `page.Data` and `page.Pagination.Count` are untouched.
- `Metadata` is `map[string]interface{}`; `any` is replaced by `interface{}` throughout (5 library
  sites, 18 in tests). Same type, Go 1.13 spelling — no caller change.
- `io.ReadAll` → `ioutil.ReadAll` at all five call sites (1 library, 4 test). Internal only.
- Tests: `newTestClient` returns `(*Client, func())` and callers `defer cleanup()` at all 14 sites.
  `t.Cleanup` needs Go 1.14. Test-only.

### Fixed
- **`Create`, `Get`, and `Update` decoded every response into a zero-valued struct against a real
  server** — silently, with a nil error. The server wraps single resources in `{"data": {...}}`
  (`octonomy/core/responses.py`, present since its first release) and the client decoded the wrapper
  straight into `*Tag`/`*Vocabulary`, so nothing matched and nothing complained. Lists were unaffected
  because their envelope has a `Data` field.

  `Client.doData` now unwraps the envelope, and a 2xx body with no `data` key is an error instead of an
  empty struct. Found by the new integration smoke test on its first real run; the httptest suite had
  encoded the vendored spec's bare-object shape rather than the server's, which is why it passed.
- **List responses had the same silent-zero trap one type further out.** Decoding straight into a
  `*TagList` turned an empty body, a `{}`, or a response whose `data` key the server renamed into a
  nil `Data` slice and `Count: 0` — indistinguishable from a genuine empty page. `Client.doList` now
  requires **both** envelope keys before decoding: a missing or null `pagination` block zeroes `Count`
  and `Limit`, which a caller paging on `Count` reads as "one page, nothing after it". A real empty
  page (`"data": []` with a pagination block) is still a success, and `"data": null` is still accepted
  as a nil slice, since nil-versus-empty semantics are an open question on the modern line rather than
  something this frozen line should settle.
- **A 2xx with an empty body where a resource was expected is now an error.** `do` previously
  returned nil in that case, so a truncated or misrouted 2xx produced a zero-valued struct. `Delete`
  is unaffected: 204-with-no-body is its documented shape and stays lenient.

  Transport is now one request path plus one decoder per response shape — `doRaw` performs the call;
  `doData`, `doList`, and `do` decode a single resource, a list envelope, and nothing respectively.
  Picking the wrong one is the mistake that started this entry, so `AGENTS.md`, `docs/architecture.md`,
  and the resource recipe in `docs/roadmap.md` now say which to use.

### Added
- **Integration smoke test** (`integration_test.go`, build tag `integration`, `make smoke`) — five
  assertions against a real server via the container harness: both response envelopes, both list
  endpoints, and one real error envelope. Gated on `OCTONOMY_TEST_BASE_URL`, so the default test run
  stays hermetic. CI sets `OCTONOMY_SMOKE_REQUIRED=1`, which turns a missing base URL into a failure
  rather than a skip — a skip is a green job, and this is the only real-server check the line has.
- **CI for this line, which previously had none.** `ci.yml` fired only on `main`, so pushes to
  `support/go1.13` ran nothing and no tag triggered anything. Added: the branch to both trigger lists,
  a `v*` tag trigger, a **required** real `go1.13.15` job running `go test -race` (not merely
  `go build`, since four of five `ioutil` sites and all 18 test-file `interface{}` sites live in
  `_test.go`), and a `smoke` job that boots the pinned container and runs the smoke test on the
  go1.13 toolchain. Both are **intended as required checks and are not yet enforced as such**:
  branch protection for `support/go1.13` does not exist, so until the maintainer adds these contexts
  (the `gh api` call is in the PR description) "required" describes the intent, not the enforcement. The go1.13 jobs set `cache: false` — `actions/setup-go` derives its cache
  path from `go env GOMODCACHE`, which does not exist before Go 1.15.
- **Release-line guard** (`scripts/compat-guard.sh`, `make compat-guard`, CI job) with three tiers,
  chosen by how recoverable the mistake is at that moment:

  **On an ordinary branch or PR**, one blocking check: `go.mod` must keep declaring `go 1.13`. That is
  the mistake with no toolchain backstop — a `v1.x` tag cut from a drifted `go.mod` keeps the same
  module path, so Go resolves it happily and `retract` (Go 1.16) cannot reach a Go 1.13 consumer.
  Module-path and branch consistency only warn here, because Go rejects a path mismatch itself. The
  guard also warns when `main` is found carrying this line's module path or `go` directive, the shape
  a cherry-pick that included `go.mod` would take.

  **On a release PR** (`release/vX.Y.Z`), four more blocking checks — this is the last point where the
  answer is "push another commit": the branch must name a valid version, `version.go` must equal it,
  the module path must be **exactly** the one that major publishes under (a matching major is not
  enough: `example.test/wrong` and an illegal `/v1` suffix both carry a v1-shaped major), and it must
  target the branch that major is cut from (a `v1` release PR retargeted at `main` otherwise passes
  every other check). The latest `CHANGELOG.md`
  heading must agree too, and a release PR that deletes the changelog fails rather than skipping the
  check. That closes a real gap: `make version-check` ties `version.go` to the changelog, but nothing
  in CI ran it — only `release-check` does, and CI never calls `release-check`.

  **On a tag push**, a *subset* runs again as **detection, not prevention**: the tag's SemVer, its
  module path, and `version.go`. Not the CHANGELOG heading and not the base branch — a tag push has no
  base branch to compare. GitHub already has the ref by then and a published version cannot be
  withdrawn for this audience; the value is catching a tag pushed without a release PR early enough
  that deleting the ref may still beat the first fetch. Tags are validated against the real SemVer grammar, not a shell glob — the obvious
  `v[0-9]*.[0-9]*.[0-9]*` pattern accepts `v1.2.3foo`, `v1.02.3`, `v1.2.3-`, and `v1.2.3.4`, none of
  which Go can resolve.
- **Tests for the guard** (`scripts/compat-guard-test.sh`, `make compat-guard-test`, run in CI and by
  `make check`). 71 checks across ordinary PRs, release PRs to both lines, tag pushes, malformed tags,
  wrong-module-path releases, `v0` (which no line publishes, and whose `/v0` suffix Go forbids),
  partial event context, `go.mod`/`version.go` parsing edge cases, and the SemVer grammar exercised
  directly against the guard's own function. Every case asserts the *reason*, not just the
  exit status — an rc-only assertion also passes when the code path never ran. Without these the
  release-PR block would first execute on the release PR itself: every ordinary run has
  `GITHUB_HEAD_REF=<type>/<issue>-…`, so that code path is never otherwise reached.
- `make test-go113` for the real-toolchain gate locally, with two documented ways to fetch a go1.13
  toolchain (`docs/development.md`).
- `make tools-check`, now a prerequisite of `release-check`. `make lint` and `make vuln` skip silently
  when their tool is absent, which is right day to day and wrong for a release gate that then prints
  "release-check passed" having run neither.
- Documented support policy for this line across `SECURITY.md`, `README.md`, `docs/versioning.md`,
  `docs/development.md`, `docs/release.md`, and `AGENTS.md`: security fixes only, **sunset
  2027-08-31** (12 months from the `v1.0.0` tag, owned by the SDK maintainer), the frozen scope, and
  what is deliberately absent — including `CodeScopeImmutable`, for which `apiErr.Code ==
  "scope_immutable"` is the documented workaround.

### Added (inherited from `main`)
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
