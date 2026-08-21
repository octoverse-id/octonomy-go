# Release Runbook

The SDK is published as a Go module via a git tag `vX.Y.Z`. There is no registry to push to —
`go get` resolves the tag directly from GitHub.

## Versioning

See [versioning.md](versioning.md). `Version` in `version.go` is canonical and must match the latest
`CHANGELOG.md` release heading (`make version-check`).

## Pre-release gate

```bash
make release-check
```

This runs `fmt-check`, `vet`, `lint`, `test` (with `-race`), `vuln`, `examples`, and `version-check`.
All must pass.

## Cutting a release

1. **Branch:** `git switch -c release/<version>` (e.g. `release/0.2.0`).
2. **Bump the version:** set `Version` in `version.go` to the new `X.Y.Z`.
3. **Update the CHANGELOG:** move `[Unreleased]` items under a new `## [X.Y.Z] - <date>` heading and
   refresh the compare links at the bottom.
4. **Run the gate:** `make release-check`.
5. **Open the release PR**, get it reviewed, and merge to `main`.
6. **Tag the merge commit and publish:**
   ```bash
   git tag -a v<version> -m "v<version>"
   git push origin v<version>
   gh release create v<version> --title "v<version>" --notes-from-tag
   ```
   The `v` prefix is required by Go modules.
7. **Verify** the module is resolvable:
   ```bash
   GOPROXY=proxy.golang.org go list -m github.com/octoverse-id/octonomy-go/v2@v<version>
   ```
8. Close the milestone/issue and delete the release branch.

## Two release lines

This repository publishes **two modules** from two branches. Which line you are releasing determines
the module path, the branch, and the verify command.

| Line | Module path | Branch | Versions | Policy |
| ---- | ----------- | ------ | -------- | ------ |
| Compat | `github.com/octoverse-id/octonomy-go` | `support/go1.13` | `v1.x` | Frozen. Security fixes only, published sunset |
| Modern | `github.com/octoverse-id/octonomy-go/v2` | `main` | `v2.x` | Active development |

Go treats the two paths as different modules, so version selection cannot cross between them. That is
the whole point of the split: a Go 1.13 consumer cannot be dragged onto code they cannot compile.

Substitute the right path in step 7's verify command for the line you are releasing.

**Backporting to the compat line.** A security fix that applies to both lands on `main` first, then is
cherry-picked onto `support/go1.13` and released as a `v1.x` patch through this same runbook. The
compat line takes **security fixes only** — no features, no ordinary bug fixes.

**A third major (`/v3`)** would append `/v3` to the module path in `go.mod` and to all import
statements. Do that only for a deliberate breaking release — see [versioning.md](versioning.md).

## Server contract changes

If a release targets a new Octonomy server contract, refresh the vendored `docs/openapi.yaml`,
reconcile types, and update the "targeted server contract" note in [versioning.md](versioning.md) in
the same release PR.
