# Versioning Policy

`octonomy-go` follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html). This document is the
source of truth for how a change maps to a version bump.

> **You are reading the copy on `support/go1.13`.** This branch *is* the compat line, so the policy
> below is written for it and its support terms are binding here. `main` carries the canonical copy
> for the `/v2` line; where the two disagree about the modern line, `main` wins. The compat line's
> own terms — security fixes only, the sunset date, no features ever — are settled and are repeated in
> [`../SECURITY.md`](../SECURITY.md).

## Version surfaces

| Surface | Where | Meaning |
| ------- | ----- | ------- |
| **Module path** | `module` line in `go.mod` | Which release line you are on. The `/v2` suffix is what makes the two lines *different modules* to Go. |
| **SDK version** | `Version` in `version.go` + git tag `vX.Y.Z` | Canonical SemVer for the SDK and CHANGELOG. Go modules resolve versions from git tags. |
| **Targeted server contract** | this document + the vendored `docs/openapi.yaml` | Which Octonomy REST contract the SDK is written against (currently **v1**, server `1.0.0`). |

The SDK versions **independently** of the Octonomy server. A new SDK release does not require a new
server release, and vice versa. `make version-check` asserts `version.go` matches the latest
`CHANGELOG.md` release heading.

## Two release lines

This repository publishes **two modules**, because two audiences have opposite requirements: one needs
a client that keeps growing, the other is pinned to Go 1.13 and needs one that never changes.

| Line | Module path | Branch | Versions | Go | Scope |
| ---- | ----------- | ------ | -------- | -- | ----- |
| **Compat** | `github.com/octoverse-id/octonomy-go` | `support/go1.13` | `v1.x` | 1.13 | Vocabularies + Tags, `/api/v1` only. **Frozen.** |
| **Modern** | `github.com/octoverse-id/octonomy-go/v2` | `main` | `v2.x` | 1.24+ | Active development. `/api/v1` today; `/api/v2`, namespaces, and the remaining resources are the roadmap. |

**Go enforces the separation.** The two paths are different modules, so minimal version selection,
`go get -u`, and dependency bots cannot move a consumer from one line to the other. A tag whose
`go.mod` declares a path that does not match the version being requested is rejected outright:

```
go: example.com/m@v2.0.0: invalid version: go.mod has post-v2 module path "example.com/m/v2" at revision v2.0.0
```

That is the entire reason for the `/v2` suffix. It replaced an earlier scheme that tried to separate
the lines by version range on a single path, which Go does **not** enforce — a `require` is a floor,
not a ceiling, and Go before 1.16 auto-resolves `@latest` on a first build.

### Compat line support policy

- **Security fixes only.** No features, no ordinary bug fixes, no `/api/v2`, no namespaces, no webhooks.
- **Sunset: 2027-08-31.** After that date this line receives nothing at all, including security fixes.
  Owner: the SDK maintainer ([`.github/CODEOWNERS`](../.github/CODEOWNERS)), revisable only by
  agreement with the consuming team. The rule is **12 months from the `v1.0.0` tag**, published to the
  end of the twelfth month so the commitment is a fixed date rather than a function of when the tag
  was pushed — rounding to the month's end can only give the consuming team more time, never less.
- **The one change that is not a "fix": the `go` directive.** `go.mod` must keep declaring `go 1.13`.
  Bumping it is what makes a `v1.x` release uninstallable for the audience this line exists for, and
  Go cannot catch it (same module path, so the tag resolves). `scripts/compat-guard.sh` blocks it in
  CI; see the guard job in `.github/workflows/ci.yml`.
- Fixes land on `main` first, then are cherry-picked onto `support/go1.13` and released as a `v1.x`
  patch. See [release.md](release.md).
- **`retract` does not help this audience.** The directive shipped in Go 1.16, so a Go 1.13 toolchain
  ignores it. A published `v1.x` cannot be recalled for the people it exists to serve, which is why
  its releases are kept deliberately small and its CI runs a real `go1.13` job — intended as a
  required check, and pending branch protection on this branch before it actually blocks a merge.

### Modern line pre-stability

The modern line is versioned `v2.0.0-alpha.N` until the API is frozen. The gate for dropping the
prerelease suffix is **not** resource coverage — counting endpoints says nothing about whether the API
has stopped moving. It is: no further breaking changes intended, real-server integration green, docs
current, and one release candidate validated.

Because no stable `v2` exists yet, `go get github.com/octoverse-id/octonomy-go/v2` resolves the highest
prerelease, so adoption works normally.

### What "frozen" costs, concretely

The frozen scope is a real trade, not a formality. On this line:

| Missing | Why | Workaround |
| ------- | --- | ---------- |
| `List[T]` | Type parameters need Go 1.18 | `TagList`, `VocabularyList` — same fields |
| `CodeScopeImmutable` + `Is*` helper | Server 3.1.0 added `409 scope_immutable` on tag/vocabulary/alias PATCH after this line was scoped | `apiErr.Code == "scope_immutable"` — `parseError` preserves any code the server sends |
| Six resource groups, `/api/v2`, namespaces, webhooks | Frozen scope | Upgrade to Go 1.24+ and the `/v2` module |
| `t.Cleanup` in tests | Needs Go 1.14 | `newTestClient` returns a cleanup func the caller defers |

## Release state

**`v1.0.0` is the first version this repository ever published**, and it is this line. Before it there
were no git tags at all and the module proxy had served nothing; `CHANGELOG.md` carried a `## [0.1.0]`
heading for a release that was never cut, and that label is corrected in the `1.0.0` entry rather than
left implying an installable version that never existed.

| Line | First release | State |
| ---- | ------------- | ----- |
| **Compat** (`github.com/octoverse-id/octonomy-go`) | `v1.0.0` | Released. Frozen — security fixes only, sunset 2027-08-31 |
| **Modern** (`github.com/octoverse-id/octonomy-go/v2`) | `v2.0.0-alpha.1` | Not yet cut; tracked in the release issue |

Nothing about `v1.0.0` can be withdrawn: `retract` shipped in Go 1.16, so a Go 1.13 consumer's
toolchain ignores it, and `GOPROXY` caches tags permanently. That is why this line's CI runs a real
`go1.13` job and a real-server smoke test, and why `scripts/compat-guard.sh` blocks a release PR whose
version, module path, base branch, and CHANGELOG heading do not all agree.

## Bump rules

Decide the bump from the **most significant** change in the release.

### PATCH — `v1.0.x` (compat) / `v2.x.y` (modern)
Backward-compatible **bug fixes**. No change to the exported Go API.
- Examples: fix a header, correct envelope decoding, fix a query param name.

### MINOR — `v2.x.0` (modern only; the compat line takes no minors)
Backward-compatible **additions** to the exported API.
- Examples: a new resource service, a new method, a new optional field on a `*Params`/`*Create` struct,
  a new `Is*` helper.
- Existing callers keep compiling and working unchanged. While the modern line is still on
  `v2.0.0-alpha.N` prereleases a necessary breaking change may ride an alpha bump, documented in the
  CHANGELOG; once `v2.0.0` proper ships, that stops being true.

### MAJOR — `vN.0.0`
Backward-**incompatible** changes to the exported Go API once a line has shipped a stable release.
- Examples: removing/renaming an exported symbol, changing a method signature, changing a field type.
- For Go modules, a `v2+` major also changes the import path. The modern line is already at
  `.../octonomy-go/v2`; a future `v3` would move to `.../octonomy-go/v3` and every importer would
  have to update. Plan majors deliberately.

## Relationship to the server's API version

The Octonomy server keeps the `/api/v1` URL contract for its entire `1.x` line. As long as a line
targets `/api/v1`, server minor/patch releases are additive and require at most a **minor** SDK bump
to surface new fields or endpoints.

A server **major** (`/api/v2`) is tracked by a corresponding **major SDK effort** — and that is
exactly what the modern line is. Adding `/api/v2` support is why this repository moved to the
`/v2` module path at `v2.x`, so the rule is satisfied rather than bent.

**Note the two axes are independent.** The SDK's major version tracks *its own* Go API
compatibility, not the server's REST version. A future server `/api/v3` would not automatically force
an SDK `/v3` — only a break in the SDK's own exported Go API would.

> **Current state, to be exact:** *both* lines speak `/api/v1` only. `apiPrefix` is a hardcoded
> constant (`octonomy.go:14`) and `Config` has no API-version selector yet. Adding `/api/v2` support —
> the selector, the namespace axis, and the six remaining resource groups — is what the modern line is
> *for*, and it is why this repository took the `/v2` module path now rather than after the fact. But
> it has not landed. Do not adopt the `/v2` module expecting to reach REST v2 endpoints today.

## Where this shows up

- **Per PR:** keep `CHANGELOG.md` `[Unreleased]` current. Do **not** bump `version.go` in feature/fix
  PRs.
- **At release time:** the version bump in `version.go` and the git tag happen in a dedicated release
  PR — see the runbook in [`release.md`](release.md).
