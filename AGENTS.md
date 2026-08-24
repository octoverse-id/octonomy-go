# Octonomy Go SDK — Agent Instructions

`octonomy-go` is the official Go client SDK for [Octonomy](https://github.com/octoverse-id/octonomy),
a multi-tenant, multi-application tag management / taxonomy service. The SDK is a hand-written,
dependency-free client for the stable REST **v1** API (`/api/v1`).

## READ FIRST — this branch is the frozen Go 1.13 line

You are on `support/go1.13`: module `github.com/octoverse-id/octonomy-go`, versions `v1.x`, Go
**1.13**. `main` is a different module (`/v2`, Go 1.24+) and is where active development happens.

- **Security fixes only.** No features, no new resources, no `/api/v2`, no namespaces, no webhooks —
  ever. If a task asks for any of those on this branch, stop and say it belongs on `main`.
- **Sunset 2027-08-31.** See `SECURITY.md` and `docs/versioning.md`.
- **A published `v1.x` cannot be recalled.** `retract` shipped in Go 1.16, so a Go 1.13 consumer's
  toolchain ignores it, and `GOPROXY` caches tags forever. Verify before tagging, not after.
- **`go.mod` must keep `go 1.13` and the unsuffixed module path.** `make compat-guard` blocks the
  first; Go itself catches the second. Never "modernize" either.
- **A modern `go build`/`go vet`/`staticcheck` pass proves nothing here.** The language version is
  enforced from `go.mod`; the stdlib version is not. Run `make test-go113` (real toolchain) before
  claiming anything compiles on this line.

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
  diverges from the generated spec — notably the **two response envelopes** the spec omits:
  `{data, pagination}` on lists and `{data}` on single resources — trust the server's real behavior
  and document the divergence in a comment. Both were verified against a running server, not read off
  the spec.

## API Client Rules

- One file per resource (`tags.go`, `vocabularies.go`, …). Each defines a `*Service` reached from a
  field on `Client`.
- Methods take `context.Context` first and accept variadic `...RequestOption` last.
- List methods return a **per-resource** envelope (`*TagList`, `*VocabularyList`) decoding
  `{data, pagination}` — this line has no `List[T]`, because type parameters need Go 1.18. Embed
  `ListOptions` in each resource's `*ListParams`.
- Pick the transport helper by response shape: `client.doData` for a single resource (unwraps the
  server's `{"data": {...}}`), `client.doList` for a list envelope, `client.do` for a call with no
  payload to decode (DELETE's 204). Getting this wrong does not fail loudly — it returns a
  zero-valued struct or an empty-looking page with a nil error. `doRaw` is the shared request path;
  do not call it directly from a resource file.
- Non-2xx responses become `*APIError` carrying the `{error:{code,message,details,request_id}}`
  envelope. Add `Is<Code>` helpers for common error codes.
- Server read-only fields are decode-only; write structs (`*Create`/`*Update`) use pointer fields
  with `omitempty` so PATCH sends only what the caller set.
- No new exported surface without doc comments and tests.

## Go Conventions

- Target Go **1.13**. **Standard library only** — no third-party runtime dependencies. Dev tools
  (`golangci-lint`, `govulncheck`) are not module dependencies.
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
  the test runs.
- Canned **single-resource** responses go through `writeData`, which adds the server's `{"data": ...}`
  wrapper. `writeJSON` sends the body verbatim — use it for list and error envelopes only. Handlers
  that returned bare objects matched the vendored spec instead of the server and hid a real defect.
- Cover success paths, the `{data, pagination}` list envelope, and error decoding
  (404 → `IsNotFound`, 409 → `IsConflict`, 400 → `IsValidation`).
- Run tests with `-race`. Keep new code covered.

## Local Development

- Run `make check` before pushing and `make release-check` before a release. On this line neither is
  the real gate: also run `make test-go113` (real go1.13 toolchain) and, for anything touching
  decoding or transport, `make dev-server && make smoke` against a real server.
- Keep the README quickstart, `examples/`, and `Makefile` current with the public API.
- Refresh the vendored `docs/openapi.yaml` from the Octonomy server when targeting a new contract,
  and record the server version it tracks in `docs/versioning.md`.

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
- Work targeting this line branches **off `support/go1.13`** and PRs back into it — never into `main`.
  A fix that applies to both lines lands on `main` first and is cherry-picked here
  (`docs/release.md`).
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
