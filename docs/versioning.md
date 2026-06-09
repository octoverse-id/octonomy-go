# Versioning Policy

`octonomy-go` follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html). This document is the
source of truth for how a change maps to a version bump.

## Version surfaces

| Surface | Where | Meaning |
| ------- | ----- | ------- |
| **SDK version** | `Version` in `version.go` + git tag `vX.Y.Z` | Canonical SemVer for the SDK and CHANGELOG. Go modules resolve versions from git tags. |
| **Targeted server contract** | this document + the vendored `docs/openapi.yaml` | Which Octonomy REST contract the SDK is written against (currently **v1**, server `1.0.0`). |

The SDK versions **independently** of the Octonomy server. A new SDK release does not require a new
server release, and vice versa. `make version-check` asserts `version.go` matches the latest
`CHANGELOG.md` release heading.

## Pre-1.0 (current: 0.x)

While the SDK is `0.x`, the public Go API may still change between minor versions as resource coverage
is filled in. We still record every user-facing change in the CHANGELOG and avoid gratuitous breakage.

## Bump rules

Decide the bump from the **most significant** change in the release.

### PATCH — `0.1.x` / `1.0.x`
Backward-compatible **bug fixes**. No change to the exported Go API.
- Examples: fix a header, correct envelope decoding, fix a query param name.

### MINOR — `0.x.0` / `1.x.0`
Backward-compatible **additions** to the exported API.
- Examples: a new resource service, a new method, a new optional field on a `*Params`/`*Create` struct,
  a new `Is*` helper.
- In `1.x`, existing callers keep compiling and working unchanged. In `0.x`, additive is preferred but
  a necessary breaking change may ride a minor bump (documented in the CHANGELOG).

### MAJOR — `x.0.0`
Backward-**incompatible** changes to the exported Go API once the SDK is `1.0.0`.
- Examples: removing/renaming an exported symbol, changing a method signature, changing a field type.
- For Go modules, a `v2+` major also changes the import path (`.../octonomy-go/v2`). Plan majors
  deliberately.

## Relationship to the server's API version

The Octonomy server keeps the `/api/v1` URL contract for its entire `1.x` line. As long as the SDK
targets `/api/v1`, server minor/patch releases are additive and require at most a **minor** SDK bump
to surface new fields or endpoints. A server **major** (`/api/v2`) would be tracked by a corresponding
major SDK effort.

## Where this shows up

- **Per PR:** keep `CHANGELOG.md` `[Unreleased]` current. Do **not** bump `version.go` in feature/fix
  PRs.
- **At release time:** the version bump in `version.go` and the git tag happen in a dedicated release
  PR — see the runbook in [`release.md`](release.md).
