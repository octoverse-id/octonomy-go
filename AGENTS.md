# Octonomy Go SDK — Agent Instructions

`octonomy-go` is the official Go client SDK for [Octonomy](https://github.com/octoverse-id/octonomy),
a multi-tenant, multi-application tag management / taxonomy service. The SDK is a hand-written,
dependency-free client for the stable REST **v1** API (`/api/v1`).

## Product Rules

These mirror server semantics the client must respect — business rules live on the server; the SDK
stays a faithful, ergonomic client.

- The SDK adds ergonomics, not behavior. Do not encode server-side validation or invariants here.
- Every request is tenant-scoped via the `X-Tenant-ID` header; `Config.TenantID` is required.
- `application_id` is optional on tags and vocabularies (`nil` = shared across the tenant) and is
  required for assignments.
- Tag deletion is **deactivation** on the server, not hard delete. `Delete` methods call HTTP
  `DELETE` and must document the deactivation semantics rather than implying data loss.
- Tag aliases are alternate identifiers that resolve to canonical tags and follow tenant/application
  compatibility rules.
- Keep the SDK faithful to `docs/openapi.yaml`, the bundled contract reference. Where the live server
  diverges from the generated spec — notably the `{data, pagination}` list envelope the spec omits —
  trust the server's real behavior and document the divergence in a comment.

## API Client Rules

- One file per resource (`tags.go`, `vocabularies.go`, …). Each defines a `*Service` reached from a
  field on `Client`.
- Methods take `context.Context` first and accept variadic `...RequestOption` last.
- List methods return `*List[T]` and decode the `{data, pagination}` envelope; embed `ListOptions`
  in each resource's `*ListParams`.
- Non-2xx responses become `*APIError` carrying the `{error:{code,message,details,request_id}}`
  envelope. Add `Is<Code>` helpers for common error codes.
- Server read-only fields are decode-only; write structs (`*Create`/`*Update`) use pointer fields
  with `omitempty` so PATCH sends only what the caller set.
- No new exported surface without doc comments and tests.

## Go Conventions

- Target Go **1.24+**. **Standard library only** — no third-party runtime dependencies. Dev tools
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
- Cover success paths, the `{data, pagination}` list envelope, and error decoding
  (404 → `IsNotFound`, 409 → `IsConflict`, 400 → `IsValidation`).
- Run tests with `-race`. Keep new code covered.

## Local Development

- Run `make check` before pushing and `make release-check` before a release.
- Keep the README quickstart, `examples/`, and `Makefile` current with the public API.
- Refresh the vendored `docs/openapi.yaml` from the Octonomy server when targeting a new contract,
  and record the server version it tracks in `docs/versioning.md`.

## Development Pipeline

- Branch names must follow Conventional Branch naming from
  https://conventional-branch.github.io/.
- Use `<type>/<description>` with lowercase alphanumerics, hyphens, and dots only where valid.
- Allowed branch types are `feature`, `feat`, `bugfix`, `fix`, `hotfix`, `release`, and `chore`.
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
