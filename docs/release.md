# Release Runbook

The SDK is published as a Go module via a git tag `vX.Y.Z`. There is no registry to push to: pushing
the tag is the release. Consumers reach it through Go's configured module proxy —
`proxy.golang.org` by default, which fetches from GitHub and then caches the version permanently —
falling back to direct VCS access only for `GOPRIVATE`/`GONOSUMDB` paths or `GOFLAGS=-mod=mod`
with `GOPROXY=direct`. **The cache is why a tag cannot be unpublished.**

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

### Three branch roles, and they are not interchangeable

| Role | Shape | Lives for | Version bumps? | Closes an issue? |
| ---- | ----- | -------- | -------------- | ---------------- |
| **Support line** | `support/<description>` — `support/go1.13` | Indefinitely; outlives every issue | No | No — exempt from the issue-number rule |
| **Implementation** | `<type>/<issue>-<description>` — `chore/15-documentation-truth-pass` | One issue | **Never** | Yes — `Closes #<n>` |
| **Release** | `release/vX.Y.Z` — `release/v1.0.1` | One release | **Only here** | Closes the milestone |

A support line is a *base*, not a unit of work: you branch off it and merge back into it, exactly as
with `main`. It is the only branch type in this repository that is allowed to exist without an issue
number, because it tracks a support commitment rather than a task. `version.go` and the CHANGELOG
heading move in a `release/` PR and nowhere else — that is what keeps `make version-check` meaningful
and stops two feature PRs from racing the same version number.

### Backporting to the compat line

A security fix that applies to both lands on `main` first, then is cherry-picked onto
`support/go1.13` and released as a `v1.x` patch through this same runbook. The compat line takes
**security fixes only** — no features, no ordinary bug fixes.

**Land it on `main` first**, then take its commit onto a branch cut from the support line — not from
`main`:

```bash
git switch main && git pull
git log --oneline -1                      # the merged fix; note its SHA

git switch support/go1.13 && git pull     # branch off the SUPPORT LINE
git switch -c fix/<issue>-<description>
git cherry-pick <SHA>
```

**Expect the cherry-pick to need work, and never resolve a conflict by taking `main`'s side
wholesale.** The two trees have diverged on purpose: the compat line has no generics, no `any`, no
post-1.13 standard library, and no v2, namespace, health, or webhook code for a `main` hunk to land
in. A fix touching `List[T]` has to be rewritten against `TagList` / `VocabularyList`; a fix touching
code that exists only on `main` needs no backport at all.

**Then open the PR against `support/go1.13`.** Two required checks cover different halves, and only
one of them runs your code: **`go1.13`** builds, vets, and runs `go test -race` under a real
`go1.13.15` toolchain, which is what proves the result works on the toolchain the line exists for;
**`compat guard`** never compiles the package and instead asserts the release line's `go.mod`
invariants, catching a drifted `go` directive or module path before a tag makes it permanent. A modern toolchain
enforces the *language* version declared in `go.mod` but **not** the standard library, so an
`io.ReadAll` that rode in on a backported hunk passes `go build`, `go vet`, and staticcheck at
`go 1.13` and fails only under a real `go1.13`.

**Release it as a `v1.x` patch** through the runbook below with `BASE = support/go1.13`, record the
backport in **both** CHANGELOGs, and check the sunset date in [versioning.md](versioning.md) and
[`SECURITY.md`](../SECURITY.md) has not passed — after **2027-08-31** the answer to a compat-line
advisory is "upgrade", not "patch".

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

If a release targets a new Octonomy server contract, refresh **both** vendored specs —
`docs/openapi-v2.yaml` (`/api/v2`) and `docs/openapi.yaml` (`/api/v1`) — reconcile types, and update
the "targeted server contract" note in [versioning.md](versioning.md) in the same release PR. The
compat line vendors `/api/v1` only, and refreshing it is a feature-shaped change that its
security-fixes-only policy does not admit.
