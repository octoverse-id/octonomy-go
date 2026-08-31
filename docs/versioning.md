# Versioning Policy

`octonomy-go` follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html). This document is the
source of truth for how a change maps to a version bump.

## Version surfaces

| Surface | Where | Meaning |
| ------- | ----- | ------- |
| **Module path** | `module` line in `go.mod` | Which release line you are on. The `/v2` suffix is what makes the two lines *different modules* to Go. |
| **SDK version** | `Version` in `version.go` + git tag `vX.Y.Z` | Canonical SemVer for the SDK and CHANGELOG. Go modules resolve versions from git tags. |
| **Targeted server contract** | this document + the vendored `docs/openapi-v2.yaml` / `docs/openapi.yaml` | Which Octonomy REST contract the SDK is written against. **`/api/v2` at server `3.1.1`** is the default surface; `/api/v1` is selectable via `Config.APIVersion` and is still vendored at server `1.0.0` pending #6. |

The SDK versions **independently** of the Octonomy server. A new SDK release does not require a new
server release, and vice versa. `make version-check` asserts `version.go` matches the latest
`CHANGELOG.md` release heading.

## Two release lines

This repository publishes **two modules**, because two audiences have opposite requirements: one needs
a client that keeps growing, the other is pinned to Go 1.13 and needs one that never changes.

| Line | Module path | Branch | Versions | Go | Scope |
| ---- | ----------- | ------ | -------- | -- | ----- |
| **Compat** | `github.com/octoverse-id/octonomy-go` | `support/go1.13` | `v1.x` | 1.13 | Vocabularies + Tags, `/api/v1` only. **Frozen.** |
| **Modern** | `github.com/octoverse-id/octonomy-go/v2` | `main` | `v2.x` | 1.24+ | Active development. `/api/v1` **and** `/api/v2` with namespace scoping; the remaining resources are the roadmap. |

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
- A **published sunset date**, after which the line receives nothing. The rule is **12 months from the
  `v1.0.0` tag**, owned by the SDK maintainer, revisable only by agreement with the consuming team.
- Fixes land on `main` first, then are cherry-picked onto `support/go1.13` and released as a `v1.x`
  patch. See [release.md](release.md).
- **`retract` does not help this audience.** The directive shipped in Go 1.16, so a Go 1.13 toolchain
  ignores it. A published `v1.x` cannot be recalled for the people it exists to serve, which is why
  its releases are kept deliberately small and its CI runs a real `go1.13` job as a required check.

### Modern line pre-stability

The modern line is versioned `v2.0.0-alpha.N` until the API is frozen. The gate for dropping the
prerelease suffix is **not** resource coverage — counting endpoints says nothing about whether the API
has stopped moving. It is: no further breaking changes intended, real-server integration green, docs
current, and one release candidate validated.

Because no stable `v2` exists yet, `go get github.com/octoverse-id/octonomy-go/v2` resolves the highest
prerelease, so adoption works normally.

## Nothing has been released yet

At the time of writing this repository has **no git tags** and the module proxy has served **no**
semantic version. `CHANGELOG.md` carries a `## [0.1.0]` heading describing the current tree, but that
release was never cut — see the note under that heading. The first two real releases will be `v1.0.0`
(compat) and `v2.0.0-alpha.1` (modern), each in its own `release/<version>` PR.

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

> **Current state, to be exact:** the modern line speaks **both** surfaces. `Config.APIVersion`
> selects one and defaults to `APIV2`, and the namespace axis is per-request (`WithNamespace`). The
> compat line remains `/api/v1` only, permanently — that is its policy, not a gap. What has *not*
> landed on the modern line is the six remaining resource groups; `Vocabularies` and `Tags` are the
> two that exist, on either surface. See [`roadmap.md`](roadmap.md).

## Where this shows up

- **Per PR:** keep `CHANGELOG.md` `[Unreleased]` current. Do **not** bump `version.go` in feature/fix
  PRs.
- **At release time:** the version bump in `version.go` and the git tag happen in a dedicated release
  PR — see the runbook in [`release.md`](release.md).
