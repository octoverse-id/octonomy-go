# Octonomy Go SDK — Agent Instructions

`octonomy-go` is the official Go client SDK for [Octonomy](https://github.com/octoverse-id/octonomy),
a multi-tenant, multi-application tag management / taxonomy service. The SDK is a hand-written,
dependency-free client for the Octonomy REST API. This tree speaks `/api/v1` only — `apiPrefix` in
`octonomy.go` is a constant — and porting `main`'s `/api/v2` surface is
[#91](https://github.com/octoverse-id/octonomy-go/issues/91).

## READ FIRST — this branch is the Go 1.13 line, and its freeze is reversed

You are on `support/go1.13`: module `github.com/octoverse-id/octonomy-go`, versions `v1.x`, Go
**1.13**. `main` is a different module (`/v2`, on a modern Go floor) and is where capabilities
originate.

- **This line takes capability parity with `main`**
  ([epic #88](https://github.com/octoverse-id/octonomy-go/issues/88)). Until #89 this file told
  agents to refuse features, new resources, `/api/v2` and namespaces here and send them to `main`.
  That policy is withdrawn. The line takes security fixes, bug fixes, and ports of what `main`
  already has — its resource groups, `/api/v2` with the namespace axis, and its transport and decode
  guards. Do not refuse that work, and do not route it to `main`: it is already there.
- **Features originate on `main`; this line ports them.** A task asking for something `main` does
  not have belongs on `main` first. What may originate here is a *fix* — a security fix, a bug fix,
  or a guard against a hazard only Go 1.13 has.
- **No webhook receiver, ever.** That is policy, not an observation about any server's default: a
  consumer needing one moves to `/v2`. Do not port `webhook/`.
- **No major, ever — so no breaking change, ever.** The module path is unsuffixed, Go accepts only
  `v0`/`v1` versions on it, and the suffixed path a major would need is `main`'s module. Every
  change here must therefore keep `v1.0.0` callers compiling — an *unkeyed* struct literal aside,
  which any Go minor that adds a field can break (`docs/versioning.md`, the MINOR rule) — and must
  not move behaviour they correctly rely on: a break has no version to ride. In practice — never
  change an exported field's type or a method's signature, never remove or rename a symbol, and
  never change a default (so `/api/v2` arrives opt-in: an existing caller's requests must not move
  to another surface on an in-range upgrade). A bug fix still changes behaviour; that is what makes
  it a fix, and it is admissible. `scripts/compat-guard.sh` refuses a `v2+` release PR into this
  branch, and a `v2+` tag on a tree that carries this line's module path.
- **Sunset 2027-08-31**, unchanged by the reversal. See `SECURITY.md` and `docs/versioning.md`.
- **A published `v1.x` cannot be recalled.** `retract` shipped in Go 1.16, so a Go 1.13 consumer's
  toolchain ignores it, and `GOPROXY` caches tags forever. Verify before tagging, not after.
- **`go.mod` must keep `go 1.13` and the unsuffixed module path.** `make compat-guard` blocks the
  first; Go itself catches the second. Never "modernize" either.
- **A modern `go build`/`go vet`/`staticcheck` pass proves nothing here.** The language version is
  enforced from `go.mod`; the stdlib version is not. Run `make test-go113` (real toolchain) before
  claiming anything compiles on this line.

## Porting from `main`

Parity work is a **hand-port**. An AST generator was scoped for it and cut once the transform
surface measured 7% of the code — do not build one; the reasoning is in
[the epic's design doc](https://github.com/octoverse-id/octonomy-go/blob/main/docs/designs/compat-line-api-v2-parity.md)
on `main`.

- Branch off `support/go1.13` with an issue-numbered branch and PR back into it (see *Development
  Pipeline*). Take the file from `main`, then rewrite it into this line's dialect.
- The rewrite rules are the design doc's *Porting checklist* table, each marked **LOUD** (a miss
  fails `go build` or `go vet` under a real go1.13.15, so `make test-go113` catches it) or **SILENT**
  (a miss compiles clean and is wrong). [#103](https://github.com/octoverse-id/octonomy-go/issues/103)
  tracks this branch's own copy of that table.
- **The SILENT rules are the ones to hold in your head**, because each one compiles and vets clean on
  go1.13.15 as well as on a modern toolchain:
  - **A leftover `omitzero`.** Go 1.13's `encoding/json` does not know the option and ignores it, so
    a ported `*string` tagged `,omitzero` emits `"field": null` on every PATCH that does not touch
    it — a PATCH that clears columns the caller never named. Optional write fields are `omitempty`
    here. [#96](https://github.com/octoverse-id/octonomy-go/issues/96) tracks a guard for it.
  - **More than one `%w`.** Before Go 1.20, `fmt.Errorf` with two `%w` verbs returns an error with
    no `Unwrap` at all, so `errors.Is` finds neither. `%w` plus `%v` keeps one and loses the other.
    Where a sentinel and a cause must both survive, write a wrapper type whose `Unwrap()` returns the
    cause and whose `Is()` matches the sentinel.
  - **Doc comments.** Strip `Optional[T]`, `omitzero`, a Go 1.24 floor, and post-1.13 standard
    library in examples (`slices.*`). Rewrite the **module** path `.../octonomy-go/v2` to the
    unsuffixed one — but never strip `/api/v2`, which is a REST route and the capability being
    ported.
- **Port a rule with the code it governs.** Several rules in this file describe the tree as it is —
  every request tenant-scoped, every method taking `...RequestOption` — and `main`'s `AGENTS.md`
  records exceptions to them that arrive with the code: the health probes are unversioned,
  unauthenticated and take no options, and have their own constructor and request path. When a
  port brings in code `main` governs with such an exception, bring the exception into this file in
  the same PR. Do not bend the ported code to fit a rule written before it existed.
- **Do not port `main`'s `Optional[T]`.** The `*Update` structs keep their pointer fields, because
  changing a published field's type breaks `v1.0.0` callers (the no-major rule above). A PATCH here
  therefore cannot clear a nullable field; that is a deliberate carve-out, not a porting gap.

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
- Keep the SDK faithful to the bundled contract references: `docs/openapi.yaml` (`/api/v1`, the only
  surface this tree's requests reach) and `docs/openapi-v2.yaml` (`/api/v2`), both vendored at server
  **3.2.1**. A port that adds the namespace axis reads the **v2** spec — v1's schemas omit the
  `namespace_type` / `namespace_id` fields. Where the live server diverges from the generated spec —
  notably the **two response envelopes** the spec omits: `{data, pagination}` on lists and `{data}`
  on single resources — trust the server's real behavior and document the divergence in a comment.
  Both were verified against a running server, not read off the spec.

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
  with `omitempty` so PATCH sends only what the caller set. They stay pointers when `main`'s
  `*Update` structs are ported — see *Porting from `main`*.
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
- **A contract refresh is not done when the YAML lands.** Refresh both vendored specs from the Octonomy
  server (`make openapi` there, one per `--api-version`), then move three things with them: the
  `<!-- contract-version: X.Y.Z -->` marker in `docs/versioning.md`, a row per operation in
  `docs/contract-coverage.yaml` — naming the Go method that implements it or a written reason it is
  not implemented — and every sentence naming the contract. `make test` fails until the specs, the
  marker and the rows agree (`contractbaseline_test.go`) and until every version token in the tree
  equals the marker or carries a registered reason (`contractversion_test.go`). A sentence naming the
  contract *without* a version in it is invisible to both; read for those by hand. Nothing on this
  branch calls a method and compares what it sends with the contract: porting
  [`main`'s contract gate](https://github.com/octoverse-id/octonomy-go/tree/main/tools/contractdrift) to do that is [#98](https://github.com/octoverse-id/octonomy-go/issues/98).

## Development Pipeline

- Branch names must follow Conventional Branch naming from
  https://conventional-branch.github.io/.
- Use `<type>/<description>` with lowercase alphanumerics, hyphens, and dots only where valid.
- Allowed branch types are `feature`, `feat`, `bugfix`, `fix`, `hotfix`, `release`, `support`,
  and `chore`.
- `support/<description>` names a **long-lived** maintenance line that outlives any single issue
  (for example `support/go1.13`, the Go 1.13 client line). Because such a line closes no
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
  A change that applies to both lines lands on `main` first and is then ported here: a cherry-pick
  where the hunk is dialect-neutral, a hand-port otherwise (`docs/release.md`).
- **`Closes #N` does not fire here.** GitHub closes an issue only on a merge into the default branch,
  so an issue a PR into this line resolves has to be closed by hand after the merge.
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
