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

## Two release lines — read this before starting

This repository publishes **two modules** from two branches. Which line you are releasing determines
the **base branch, the PR target, the commit you tag, and the verify command**. Substitute `BASE` and
`MODULE` from this table throughout the runbook below.

| Line | `MODULE` | `BASE` | Versions | Policy |
| ---- | -------- | ------ | -------- | ------ |
| **Compat** | `github.com/octoverse-id/octonomy-go` | `support/go1.13` | `v1.x` | Frozen. Security fixes only, published sunset |
| **Modern** | `github.com/octoverse-id/octonomy-go/v2` | `main` | `v2.x` | Active development |

> **Getting `BASE` wrong is unrecoverable.** A `v1.x` tag placed on a `main` commit points at a
> `go.mod` that declares the `/v2` module path, so Go rejects the unsuffixed module at that version
> and the release simply cannot be resolved:
>
> ```
> go: github.com/octoverse-id/octonomy-go@v1.0.0: invalid version:
>     go.mod has post-v1 module path "github.com/octoverse-id/octonomy-go/v2" at revision v1.0.0
> ```
>
> Tags cannot be recalled, and `retract` is inert for a Go 1.13 consumer's toolchain — so the compat
> line has no second chance. **Check `go.mod`'s module line before you tag** (steps 1 and 7).

**Backporting to the compat line.** A security fix that applies to both lands on `main` first, then is
cherry-picked onto `support/go1.13` and released as a `v1.x` patch through this same runbook. The
compat line takes **security fixes only** — no features, no ordinary bug fixes.

## Cutting a release

Substitute `BASE` and `MODULE` from the table above.

1. **Confirm the line.** `git switch BASE && git pull` — then check you are where you think you are:
   ```bash
   git branch --show-current && head -1 go.mod
   ```
   The module line must match `MODULE`. If it does not, you are on the wrong branch; stop.
2. **Branch:** `git switch -c release/<version>` off `BASE`
   (e.g. `release/v2.0.0-alpha.1` off `main`, or `release/v1.0.1` off `support/go1.13`).
3. **Bump the version:** set `Version` in `version.go` to the new `X.Y.Z`.
4. **Update the CHANGELOG:** move `[Unreleased]` items under a new `## [X.Y.Z] - <date>` heading and
   refresh the link definitions at the bottom.
5. **Run the gate:** `make release-check`.
6. **Open the release PR targeting `BASE`** — *not* necessarily `main`. Get it reviewed and merged.
7. **Tag the merge commit on `BASE`:**
   ```bash
   git switch BASE && git pull
   head -1 go.mod                       # last chance: must match MODULE
   git tag -a v<version> -m "v<version>"
   git push origin v<version>
   gh release create v<version> --title "v<version>" --notes-from-tag
   ```
   The `v` prefix is required by Go modules.
8. **Verify** the module is resolvable at the path for this line:
   ```bash
   GOPROXY=proxy.golang.org go list -m MODULE@v<version>
   ```
   A path-mismatch error here means the tag landed on the wrong branch. It cannot be fixed by
   retagging — publish a corrected version instead.
9. Close the milestone/issue and delete the release branch.

**A third major (`/v3`)** would append `/v3` to the module path in `go.mod` and to all import
statements. Do that only for a deliberate breaking release — see [versioning.md](versioning.md).

## Server contract changes

If a release targets a new Octonomy server contract, refresh the vendored `docs/openapi.yaml`,
reconcile types, and update the "targeted server contract" note in [versioning.md](versioning.md) in
the same release PR.
