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

Four placeholders, substituted throughout. `VERSION` is **unprefixed**; `TAG` always carries the `v`.

| Placeholder | Meaning | Compat example | Modern example |
| ----------- | ------- | -------------- | -------------- |
| `BASE` | base branch, PR target, tagged branch | `support/go1.13` | `main` |
| `MODULE` | module path for this line | `github.com/octoverse-id/octonomy-go` | `github.com/octoverse-id/octonomy-go/v2` |
| `VERSION` | SemVer, **no `v`** — goes in `version.go` and the CHANGELOG heading | `1.0.1` | `2.0.0-alpha.1` |
| `TAG` | `v` + `VERSION` — the git tag and GitHub release name | `v1.0.1` | `v2.0.0-alpha.1` |

1. **Confirm the line.** `git switch BASE && git pull`, then check you are where you think you are:
   ```bash
   git branch --show-current && head -1 go.mod
   ```
   The module line must match `MODULE`. If it does not, you are on the wrong branch; stop.
2. **Branch:** `git switch -c release/TAG` off `BASE`
   (e.g. `release/v2.0.0-alpha.1` off `main`, or `release/v1.0.1` off `support/go1.13`).
3. **Bump the version:** set `Version` in `version.go` to `VERSION` — **unprefixed**.
4. **Update the CHANGELOG:** move `[Unreleased]` items under a new `## [VERSION] - <date>` heading —
   also unprefixed, because `make version-check` compares it against `version.go` verbatim — and
   refresh the link definitions at the bottom.
5. **Run the gate:** `make release-check`.
6. **Open the release PR targeting `BASE`** — *not* necessarily `main`. Get it reviewed and merged.
7. **Tag the merge commit on `BASE`:**
   ```bash
   git switch BASE && git pull
   head -1 go.mod                       # last chance: must match MODULE
   git tag -a TAG -m TAG
   git push origin TAG
   gh release create TAG --title TAG --notes-from-tag
   ```
   Go modules require the `v` prefix on the tag, which is why `TAG` and `VERSION` are separate here.
8. **Verify** the module is resolvable at the path for this line:
   ```bash
   GOPROXY=proxy.golang.org go list -m MODULE@TAG
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
