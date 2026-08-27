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
  diverges from the generated spec — notably the **two response envelopes** the spec omits:
  `{data, pagination}` on lists and `{data}` on single resources — trust the server's real behavior
  and document the divergence in a comment. Both were verified against a running server, not read
  off the spec; only one of the two was known before #32.

## API Client Rules

- One file per resource (`tags.go`, `vocabularies.go`, …). Each defines a `*Service` reached from a
  field on `Client`.
- Methods take `context.Context` first and accept variadic `...RequestOption` last.
- List methods return `*List[T]` and decode the `{data, pagination}` envelope; embed `ListOptions`
  in each resource's `*ListParams`.
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
- **The queued groups are not all CRUD, and `docs/openapi.yaml` will mislead you about two of
  them.** Shapes verified live against server 3.1.0, not read off the spec:

  | Group | Endpoints | Helper |
  | ----- | --------- | ------ |
  | Tag aliases (#8) | full CRUD | `doData`/`doList`/`do` — the `tags.go` template applies as-is |
  | Tag resolution (#9) | `GET /tag-resolution` only | `doData` — one specialized read, no list, no writes |
  | Assignments (#10) | `POST`/`DELETE /tag-assignments`, plus `bulk-assign`/`bulk-remove` | `doData` + `do`; see the composite note below |
  | Resource tags (#11) | `GET` list + `POST` composite replace | `doList` + `doData` (composite) |
  | Audit logs (#12) | `GET` only, three list routes | `doList` — **list-only**, there is no `Get` |
  | Health (#13) | `/health/live`, `/health/ready` | neither — see below |

  - **Bulk and replace return a composite object under `data`**, e.g.
    `{"data": {"created": 1, "existing": 0, "skipped": 0, "assignments": [...]}}`. The spec is no
    guide here: it claims a bare array (`type: array`) for `bulk-assign` and the resource-tag
    replace, and documents **no response schema at all** for `bulk-remove` (which really returns
    `{"data": {"removed": N}}`). The bare-array claim is a third divergence in the same family as
    the two above, and the one most likely to reproduce #32 — a bare-array decoder against this
    body yields an empty slice and a nil error. It still fits `doData[T]`; `T` is a composite result
    struct, not the resource. Do **not** relax `doList`'s pagination requirement to accept these:
    they carry no pagination block because they are not pages.
  - **Health is outside the API surface in three ways at once.** It is rooted outside `/api/v1`
    (the prefix is unconditional in `doRaw`), its body is a bare `{"status": "ok"}` with **no `data`
    envelope**, and it is **unauthenticated** — while `New` requires both `Token` and `TenantID`, so
    the tenant-scoping rule above does not apply to it. #13 needs its own request path, its own
    decoder, and the credential-free constructor its title names. Do **not** loosen `doData`'s
    envelope requirement or `New`'s validation to make health fit: that would re-open #32, and the
    tenant guarantee, for every other resource.
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
