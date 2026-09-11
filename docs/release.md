# Release Runbook

The SDK is published as a Go module via a git tag `vX.Y.Z`. There is no registry to push to: pushing
the tag is the release. Consumers reach it through `GOPROXY`, which defaults to
`https://proxy.golang.org,direct` — the proxy is tried first and fetches the tag from GitHub, with
`direct` (straight from the VCS) as the fallback in the chain. A consumer can bypass the proxy
entirely with `GOPROXY=direct`, or per-path with `GONOPROXY`/`GOPRIVATE`.

**`proxy.golang.org` retains a version permanently once it has served it, which is why a tag cannot
be unpublished.** Deleting or moving the git tag does not withdraw the release; only `retract` marks
it, and `retract` is inert for a Go 1.13 toolchain (see [versioning.md](versioning.md)).

## Versioning

See [versioning.md](versioning.md). `Version` in `version.go` is canonical and must match the latest
`CHANGELOG.md` release heading (`make version-check`).

## Pre-release gate

```bash
make release-check
```

This runs `require-tools`, `fmt-check`, `vet`, `lint`, `test` (with `-race`), `vuln`, `examples`, and
`version-check`.

**A green `release-check` means all seven ran.** `require-tools` goes first and fails the gate when
`golangci-lint` or `govulncheck` is missing, naming each absent binary and its install command, so
the exit status can be trusted without reading the output. Standalone `make lint` and `make vuln`
still skip with a notice when their tool is absent — the strictness belongs to the release gate, not
to the everyday targets (#53).

A tool installed with `go install` lands in `$(go env GOPATH)/bin`, which is not on `PATH` by
default here. If `require-tools` reports a binary you believe you have installed, that is the first
thing to check.

**Cut the release on a patched Go toolchain.** `vuln` now really runs, and `govulncheck` reports
standard-library advisories against **the Go it resolves** — which is the point, since scanning with a
newer standard library than the one under test would hide exactly what the scan is for (the `vuln` job
in [`ci.yml`](../.github/workflows/ci.yml) explains the same distinction from the other side). So a
local Go a few patch releases behind fails the gate on findings that have nothing to do with this code
and that CI, resolving a current patch, does not see. Check both lines before you start:

```bash
go version                 # a current 1.25.x patch, not a months-old version-manager pin
govulncheck -version       # its `Go:` line is the one that counts, and it can disagree with the above
```

In source mode `govulncheck` takes the scanned standard library from the `go` **it** resolves —
`GOVERSION` in its environment, else `go env GOVERSION` — so ordinarily putting a patched `go` first
on `PATH` is all it takes, and the two lines agree. The toolchain that *built* the scanner does not
enter into it.

What breaks that is a version manager's **shim**. A `govulncheck` reached through one (asdf, for
instance) is re-execed with the manager's selected Go, so the scan uses *that* standard library no
matter how you ordered `PATH`, and `govulncheck -version` says so while `go version` disagrees. Fix
it by pointing the version manager at the patched toolchain, or by calling a non-shimmed binary:

```bash
GOTOOLCHAIN=auto go install golang.org/x/vuln/cmd/govulncheck@latest   # lands in $(go env GOPATH)/bin
$(go env GOPATH)/bin/govulncheck -version                              # must agree with `go version`
```

Verified both ways here: one and the same binary reports `Go: go1.25.14` or `Go: go1.25.4` purely by
which toolchain leads `PATH`, and reports the manager's pick when reached through its shim.
`GOTOOLCHAIN=auto` above covers *building* the scanner only, exactly as in CI. This is a
local-environment problem, never a reason to change anything in the repository.

CI installs both tools and runs them as separate jobs; that remains the enforcement of record, and
this gate is the fast local pre-check for it.

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

   **Push the tag as soon as it merges.** The merged tree already names the release in `version.go`
   and carries a dated CHANGELOG heading, so between the merge and step 7 the branch describes a
   version that cannot yet be fetched. Keep the window to minutes, and keep the prose honest about
   what creates a release: **pushing the tag is the release**, so status text on the branch should
   point at the tag or the proxy query rather than asserting a tag it cannot see. Not at the
   releases page: step 7 pushes the tag *before* `gh release create`, so a module can be fetchable
   while that page still shows nothing. If step 7 is going to be delayed, say so on the PR.
7. **Tag the merge commit on `BASE`:**
   ```bash
   git switch BASE && git pull
   head -1 go.mod                       # last chance: must match MODULE
   git tag -a TAG -m TAG
   git push origin TAG
   gh release create TAG --title TAG --notes-from-tag        # --prerelease if TAG has a suffix
   ```
   Go modules require the `v` prefix on the tag, which is why `TAG` and `VERSION` are separate here.

   **Pass `--prerelease` whenever `TAG` carries a prerelease suffix** — `-alpha.N`, `-beta.N`,
   `-rc.N`. `gh` does not infer it from the tag, and `--latest` defaults to *automatic based on date
   and version*, so without the flag the release is published as an ordinary one and GitHub can label
   it **Latest** — exactly the stability claim a prerelease exists to avoid making. It does not
   affect module resolution, which reads the git tag and not the GitHub release; what it changes is
   what a human reading the releases page concludes. That page answers "what did the maintainers
   publish and how did they label it", never "can I fetch this version" — the tag push on the line
   above has already settled that, and the proxy query in [versioning.md](versioning.md#release-state)
   is what reports it. `v1.0.0` needed none of this, so this is the first release the flag applies
   to.
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
