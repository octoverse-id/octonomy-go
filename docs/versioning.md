# Versioning Policy

`octonomy-go` follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html). This document is the
source of truth for how a change maps to a version bump.

> **You are reading the copy on `support/go1.13`.** This branch *is* the compat line, so the policy
> below is written for it and its support terms are binding here. `main` carries
> [the canonical copy](https://github.com/octoverse-id/octonomy-go/blob/main/docs/versioning.md)
> for the `/v2` line; where the two disagree about the modern line, `main` wins — which is why what
> follows *links* to it rather than repeating it. The compat line's own terms — what it takes, the
> two things it never takes, and the sunset date — are repeated in [`../SECURITY.md`](../SECURITY.md).
> They have changed once: the security-fixes-only freeze published with `v1.0.0` was withdrawn in
> [#89](https://github.com/octoverse-id/octonomy-go/issues/89), for
> [epic #88](https://github.com/octoverse-id/octonomy-go/issues/88).

## Version surfaces

| Surface | Where | Meaning |
| ------- | ----- | ------- |
| **Module path** | `module` line in `go.mod` | Which release line you are on. The `/v2` suffix is what makes the two lines *different modules* to Go. |
| **SDK version** | `Version` in `version.go` + git tag `vX.Y.Z` | Canonical SemVer for the SDK and CHANGELOG. Go modules resolve versions from git tags. |
| **Targeted server contract** | this document + the vendored `docs/openapi.yaml` / `docs/openapi-v2.yaml` + [`docs/contract-coverage.yaml`](contract-coverage.yaml) | Which Octonomy REST contract the SDK is written against. **Both specs track server `3.2.1`**. This tree's methods reach `/api/v1` by default and `/api/v2` when `Config.APIVersion` is `APIV2`, so both are surfaces they are held to; the models follow `openapi-v2.yaml`, whose schemas are the superset. |

<!-- contract-version: 3.2.1 -->

> The marker above is the one mechanized statement of the targeted contract. `contractbaseline_test.go`
> asserts it against `info.version` in both vendored specs and holds `docs/contract-coverage.yaml` to
> a row for every operation they publish; `contractversion_test.go` measures every other version this
> repository writes down against it, and each must equal it or carry a registered reason. Both run in
> `make test` and in the `go1.13` job. Update the marker in the same commit that refreshes the specs.
> This branch has no contract gate yet; porting [`main`'s](https://github.com/octoverse-id/octonomy-go/tree/main/tools/contractdrift) is [#98](https://github.com/octoverse-id/octonomy-go/issues/98).

The SDK versions **independently** of the Octonomy server. A new SDK release does not require a new
server release, and vice versa. `make version-check` asserts `version.go` matches the latest
`CHANGELOG.md` release heading.

## Two release lines

This repository publishes **two modules**, because two audiences have opposite requirements: one can
move to a modern Go and take a breaking change at a major, the other is pinned to Go 1.13 and needs a
client that never breaks under it.

| Line | Module path | Branch | Versions | Go | Scope |
| ---- | ----------- | ------ | -------- | -- | ----- |
| **Compat** | `github.com/octoverse-id/octonomy-go` | `support/go1.13` | `v1.x` | 1.13 | Capability parity with `main`, ported ([#88](https://github.com/octoverse-id/octonomy-go/issues/88)); what this tree has is [the README's table](../README.md#implemented-resources). Never a major, never a webhook receiver. |
| **Modern** | `github.com/octoverse-id/octonomy-go/v2` | `main` | `v2.x` | [see `main`](https://github.com/octoverse-id/octonomy-go/blob/main/README.md) | Active development. Both its Go floor and its surface move, so read them from `main` rather than from this table. |

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

- **Capability parity with `main`, through the sunset.** The line takes security fixes, bug fixes,
  and ports of what `main` already has: its resource groups, `/api/v2` with the namespace axis, and
  its transport and decode guards ([epic #88](https://github.com/octoverse-id/octonomy-go/issues/88)).
  Features originate on `main` and reach this line by port; what originates here is a fix. This
  reverses the security-fixes-only freeze published with `v1.0.0` — deliberately, in
  [#89](https://github.com/octoverse-id/octonomy-go/issues/89); the reasoning is the epic's
  [design doc](https://github.com/octoverse-id/octonomy-go/blob/main/docs/designs/compat-line-api-v2-parity.md)
  on `main`.
- **No webhook receiver.** This line does not ship one; a consumer needing one moves to `/v2`. That is
  a standing policy, not an observation about any server's configuration.
- **No major, ever — and so no breaking change, ever.** This is Go's rule, not one this repository
  could revise: the line's module path is unsuffixed, a tree carrying a `go.mod` can tag only
  `v0`/`v1` versions on an unsuffixed path, and the suffixed path a major would need is a different
  module — `.../v2` is `main`'s. Every release here is therefore a `v1.x`, and every change must
  keep `v1.0.0` callers compiling — with the one Go-level exception every minor carries, an
  *unkeyed* struct literal (see the MINOR rule) — and must not move behaviour they correctly rely
  on: a break has no version to ride. (A bug fix still changes behaviour — that is what makes it
  one. What it may not change is a signature, a field's type, or a default.) "Breaking" on this
  branch has the meaning Go's own compatibility promise gives it: an added struct field is not a
  break, and an unkeyed literal of that struct is the exposure the MINOR rule names. Two
  consequences are worth naming because the port meets them first. The `*Update` structs keep their
  pointer fields rather than taking `main`'s `Optional[T]`, so a PATCH on this line cannot clear a
  nullable field. And `/api/v2` arrives **opt-in**: a caller who sets nothing keeps speaking
  `/api/v1`, because an in-range upgrade that moved existing callers' requests to another surface
  would be exactly the break this line cannot publish. `scripts/compat-guard.sh` refuses a `v2+`
  release PR into this branch, and a `v2+` tag on a tree that carries this line's module path; its
  comment on `compat_major_violation` says what a tag push cannot see.
- **Sunset: 2027-08-31**, unchanged by the reversal. After that date this line receives nothing at
  all, including security fixes.
  Owner: the SDK maintainer ([`.github/CODEOWNERS`](../.github/CODEOWNERS)), revisable only by
  agreement with the consuming team. The rule is **12 months from the `v1.0.0` tag**, published to the
  end of the twelfth month so the commitment is a fixed date rather than a function of when the tag
  was pushed — rounding to the month's end can only give the consuming team more time, never less.
- **The `go` directive never moves.** `go.mod` must keep declaring `go 1.13`, parity work included.
  Bumping it is what makes a `v1.x` release uninstallable for the audience this line exists for, and
  Go cannot catch it (same module path, so the tag resolves). `scripts/compat-guard.sh` blocks it in
  CI; see the guard job in `.github/workflows/ci.yml`.
- A change that applies to both lines lands on `main` first, then is ported onto `support/go1.13` —
  a cherry-pick where the hunk is dialect-neutral, a hand-port otherwise — and released as a `v1.x`
  patch, or a `v1.x` minor when it adds exported API. See [release.md](release.md).
- **`retract` does not help this audience.** The directive shipped in Go 1.16, so a Go 1.13 toolchain
  ignores it. A published `v1.x` cannot be recalled for the people it exists to serve, which is why
  its CI runs a real `go1.13` job: the only gate on this line's stdlib floor, since a modern
  toolchain enforces the `go` directive's *language* version and not its *stdlib* version. Which
  checks block a merge is branch-protection state, which no file in this repository can observe —
  read it from the branch's settings.

### Adopting the modern line

Where that line stands in its own stabilization — whether it is still on prereleases, and what its
API freeze requires — is `main`'s to state, and it states it in
[its versioning policy](https://github.com/octoverse-id/octonomy-go/blob/main/docs/versioning.md). (No section anchor: the heading that carries this
today is named for a phase that ends.)

What is worth stating here is the rule that carries a reader across, because the rule does not decay.
For a module **not already required**, `go get github.com/octoverse-id/octonomy-go/v2` takes Go's
`@latest` selection: the highest eligible **stable** release; failing that, the highest eligible
**prerelease**; failing that, a pseudo-version for the newest commit on the repository's default
branch. So adoption works without anyone naming a version, whichever of those three that line is
currently in. (For a module already required, a bare `go get` is an *upgrade* rather than a fresh
selection, and can leave a newer required version in place.)

Which one it actually picks is a question for the toolchain, never a claim a file on this branch can
hold — this page held one and it aged. Note that the proxy's `@v/list` cannot answer it either: that
endpoint omits pseudo-versions, so it reports what is *tagged*, not what a fetch would select.

```console
$ go list -m github.com/octoverse-id/octonomy-go/v2@latest
```

### What this line lacks, concretely

Some of what `main` has is not on this tree **yet** — [epic #88](https://github.com/octoverse-id/octonomy-go/issues/88)
ports it — and some never will be. The third column says which:

| Missing | Why | Workaround |
| ------- | --- | ---------- |
| `List[T]` | Type parameters need Go 1.18 — **never**, since the floor never moves | `TagList`, `VocabularyList` — same fields |
| Every other resource group | Not ported yet — [#94](https://github.com/octoverse-id/octonomy-go/issues/94); [`contract-coverage.yaml`](contract-coverage.yaml) records which operations this tree implements | Wait for the port, or upgrade the toolchain and move to the `/v2` module |
| `/api/v2` **by default** | **Never** — this line cannot change a default under a caller, so `DefaultAPIVersion` is `APIV1` here and `APIV2` on `main` | Set `Config.APIVersion = APIV2` |
| A webhook receiver | **Never** — policy, above | Move to the `/v2` module |
| Clearing a nullable field with PATCH | **Not planned** — a named carve-out of the epic: the `*Update` fields stay pointers, since `main`'s `Optional[T]` would change their types (no major, above). `Metadata` is not affected: `Metadata{}` sends `{}` and empties it | Move to the `/v2` module |
| `t.Cleanup` in tests | Needs Go 1.14 — **never** | `newTestClient` returns a cleanup func the caller defers |

## Release state

**`v1.0.0` is the first version this repository ever published**, and it is this line. Before it there
were no git tags at all and the module proxy had served nothing; `CHANGELOG.md` carried a `## [0.1.0]`
heading for a release that was never cut, and that label is corrected in the `1.0.0` entry rather than
left implying an installable version that never existed.

| Line | First release | State |
| ---- | ------------- | ----- |
| **Compat** (`github.com/octoverse-id/octonomy-go`) | `v1.0.0` | Released. Taking capability parity with `main` ([#88](https://github.com/octoverse-id/octonomy-go/issues/88)); never a major; sunset 2027-08-31 |
| **Modern** (`github.com/octoverse-id/octonomy-go/v2`) | `v2.0.0-alpha.1` | Released, and moving on its own schedule. [`main`'s release state](https://github.com/octoverse-id/octonomy-go/blob/main/docs/versioning.md#release-state) is the record; the `go list -m` query under [Adopting the modern line](#adopting-the-modern-line) is what settles what a fresh `go get` would pick |

Nothing about `v1.0.0` can be withdrawn: `retract` shipped in Go 1.16, so a Go 1.13 consumer's
toolchain ignores it, and `GOPROXY` caches tags permanently. That is why this line's CI runs a real
`go1.13` job and a real-server smoke test, and why `scripts/compat-guard.sh` blocks a release PR whose
version, module path, base branch, and CHANGELOG heading do not all agree.

## Bump rules

Decide the bump from the **most significant** change in the release.

### PATCH — `v1.x.y` (compat) / `v2.x.y` (modern)
Backward-compatible **bug fixes**. No change to the exported Go API.
- Examples: fix a header, correct envelope decoding, fix a query param name.

### MINOR — `v1.x.0` (compat) / `v2.x.0` (modern)
Backward-compatible **additions** to the exported API.
- Examples: a new resource service, a new method, a new optional field on a `*Params`/`*Create` struct,
  a new `Is*` helper.
- Existing callers keep compiling and working unchanged, with **one Go-level caveat**: adding a
  field to an exported struct breaks a caller who wrote an *unkeyed* composite literal, because such
  a literal must supply exactly one value per field, in order. It applies to every exported struct
  whose fields are all exported — among them `Config`, `ListOptions`, `Pagination`, `APIError`, the
  models, and the `*Create` / `*Update` / `*ListParams` / `*List` types — and so to the parity work,
  which adds `Config.APIVersion` and the namespace fields on the models. It does not apply to
  `Client` or the `*Service` types, which carry unexported fields and cannot be written unkeyed from
  outside the package. `go vet`'s `composites` check flags unkeyed literals of imported types, so
  the practical exposure is low; it is not zero, which is what "backward-compatible" would otherwise
  imply.
- A prerelease does not carry that guarantee:
  while a line is on prereleases, a necessary breaking change may ride a prerelease bump with a
  CHANGELOG entry, and that stops the moment a stable release of that major ships. Whether the modern
  line is still in that state is [its versioning policy](https://github.com/octoverse-id/octonomy-go/blob/main/docs/versioning.md) to say.
- **On the compat line a minor is the only way an addition ships**, and the parity work does ship
  this way. The line took no minors until [#89](https://github.com/octoverse-id/octonomy-go/issues/89)
  withdrew the freeze, and `v1.0.x` was the only shape a release could take; `scripts/compat-guard.sh`
  enforced that, and was changed with the policy.

### MAJOR — `vN.0.0`
Backward-**incompatible** changes to the exported Go API once a line has shipped a stable release.
- Examples: removing/renaming an exported symbol, changing a method signature, changing a field type.
- For Go modules, a `v2+` major also changes the import path. The modern line is already at
  `.../octonomy-go/v2`; a future `v3` would move to `.../octonomy-go/v3` and every importer would
  have to update. Plan majors deliberately.
- **The compat line can never publish one.** Its unsuffixed path takes `v0`/`v1` versions only, and
  the suffixed path a major needs is the modern line's module, so a `v2.0.0` tag on this branch is
  one the go command rejects. A change that would need a major here does not ship here — it ships on
  `main`, and a consumer who needs it moves to `/v2`.

## Relationship to the server's API version

The Octonomy server keeps the `/api/v1` URL contract for its entire `1.x` line. As long as a line
targets `/api/v1`, server minor/patch releases are additive and require at most a **minor** SDK bump
to surface new fields or endpoints.

A server **major** (`/api/v2`) was tracked by a corresponding **major SDK effort** on the modern
line: `v2.x`, on the `/v2` module path, with `/api/v2` as its *default*. A default that moves every
caller's requests is a break, so that line needed its major. It is not the only way a server major
can be surfaced, and the compat line cannot use it: with no major available, `/api/v2` reaches this
line **additively** — opt-in, in a `v1.x` minor, with every existing caller still on `/api/v1`.

**Note the two axes are independent.** The SDK's major version tracks *its own* Go API
compatibility, not the server's REST version. A future server `/api/v3` would not automatically force
an SDK `/v3` — only a break in the SDK's own exported Go API would. The compat line's `/api/v2` is
that rule applied: a new surface, no break, so no major.

> **Where *this* tree stands, to be exact:** `Config.APIVersion` selects the surface, and it defaults
> to `APIV1` — so a caller who sets nothing reaches `/api/v1`, exactly as this line's first release
> did, and `APIV2` is the opt-in. [#91](https://github.com/octoverse-id/octonomy-go/issues/91) ported the
> selector from `main` with that one value changed, per the support policy above, after
> [#89](https://github.com/octoverse-id/octonomy-go/issues/89) withdrew the freeze that had kept
> this tree on `/api/v1` alone. What the modern line reaches is stated on `main` — in
> [its README](https://github.com/octoverse-id/octonomy-go/blob/main/README.md) and
> [its versioning policy](https://github.com/octoverse-id/octonomy-go/blob/main/docs/versioning.md)
> — and not here.

## Where this shows up

- **Per PR:** keep `CHANGELOG.md` `[Unreleased]` current. Do **not** bump `version.go` in feature/fix
  PRs.
- **At release time:** the version bump in `version.go` and the git tag happen in a dedicated release
  PR — see the runbook in [`release.md`](release.md).
