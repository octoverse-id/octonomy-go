# Versioning Policy

`octonomy-go` follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html). This document is the
source of truth for how a change maps to a version bump.

## Version surfaces

| Surface | Where | Meaning |
| ------- | ----- | ------- |
| **Module path** | `module` line in `go.mod` | Which release line you are on. The `/v2` suffix is what makes the two lines *different modules* to Go. |
| **SDK version** | `Version` in `version.go` + git tag `vX.Y.Z` | Canonical SemVer for the SDK and CHANGELOG. Go modules resolve versions from git tags. |
| **Targeted server contract** | this document + the vendored `docs/openapi-v2.yaml` / `docs/openapi.yaml` | Which Octonomy REST contract the SDK is written against. **Both surfaces track server `3.1.1`**: `/api/v2` is the default, `/api/v1` is selectable via `Config.APIVersion`. |

The SDK versions **independently** of the Octonomy server. A new SDK release does not require a new
server release, and vice versa. `make version-check` asserts `version.go` matches the latest
`CHANGELOG.md` release heading.

## Two release lines

This repository publishes **two modules**, because two audiences have opposite requirements: one needs
a client that keeps growing, the other is pinned to Go 1.13 and needs one that never changes.

| Line | Module path | Branch | Versions | Go | Scope |
| ---- | ----------- | ------ | -------- | -- | ----- |
| **Compat** | `github.com/octoverse-id/octonomy-go` | `support/go1.13` | `v1.x` | 1.13 | Vocabularies + Tags, `/api/v1` only. **Frozen.** |
| **Modern** | `github.com/octoverse-id/octonomy-go/v2` | `main` | `v2.x` | 1.24+ | Active development. `/api/v1` **and** `/api/v2` with namespace scoping; every resource group the vendored contracts publish. |

**Go enforces the separation.** The two paths are different modules, so minimal version selection,
`go get -u`, and dependency bots cannot move a consumer from one line to the other. A tag whose
`go.mod` declares a path that does not match the version being requested is rejected outright:

```
go: example.com/m@v2.0.0: invalid version: go.mod has post-v2 module path "example.com/m/v2" at revision v2.0.0
```

That is the entire reason for the `/v2` suffix. It replaced an earlier scheme that tried to separate
the lines by version range on a single path, which Go does **not** enforce — a `require` is a floor,
not a ceiling, and Go before 1.16 auto-resolves `@latest` on a first build.

**Consumers need no `exclude`, no pin, and no build tag.** Earlier drafts of this split asked a Go
1.13 consumer to add an upper-bound `exclude` block to their own `go.mod`, because nothing else could
express one. The `/v2` module path made that unnecessary: importing
`github.com/octoverse-id/octonomy-go` can only ever resolve to the compat line, so the enforcement is
the go command's rather than the consumer's diligence. If you are following instructions from an
older plan document, this paragraph supersedes them.

### Compat line support policy

- **Security fixes only.** No features, no ordinary bug fixes, no `/api/v2`, no namespaces, no webhooks.
- **Sunset: 2027-08-31**, after which the line receives nothing at all, including security fixes.
  Owner: the SDK maintainer ([`.github/CODEOWNERS`](../.github/CODEOWNERS)); revisable only by
  agreement with the consuming team. The rule is **12 months from the `v1.0.0` tag** (2026-08-26),
  published to the end of the twelfth month so the date is fixed rather than dependent on the hour
  the tag was pushed. The same date and owner are published in [`SECURITY.md`](../SECURITY.md) on
  both branches.
- **Go 1.13 receives no security patches of its own.** `go1.13.15` (August 2020) was its last
  release, and the Go team supports only the two most recent major versions — so a consumer on that
  toolchain carries unpatched standard-library and toolchain advisories no matter what this SDK
  ships. The compat line is an informed trade against a fixed date, not a supported-forever state.
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

**No `v2` tag exists yet at all**, prerelease or otherwise, so
`go get github.com/octoverse-id/octonomy-go/v2` currently resolves a **pseudo-version** off the
default branch. Once `v2.0.0-alpha.1` is tagged ([#29](https://github.com/octoverse-id/octonomy-go/issues/29))
it resolves the highest prerelease, and adoption works normally without anyone naming a version:
`go get` prefers a prerelease when no stable release of that major exists.

## Release state

| Line | Latest tag | State |
| ---- | ---------- | ----- |
| **Compat** (`github.com/octoverse-id/octonomy-go`) | `v1.0.0` (2026-08-26) | Released. Frozen — security fixes only, sunset 2027-08-31 |
| **Modern** (`github.com/octoverse-id/octonomy-go/v2`) | *none yet* | `v2.0.0-alpha.1` is unreleased ([#29](https://github.com/octoverse-id/octonomy-go/issues/29)) |

**There is no published `v0.x`.** `CHANGELOG.md` carries a `## [0.1.0]` heading describing an early
state of the tree, but that release was never cut — see the note under that heading. Checkable, and
worth re-checking rather than trusting:

```console
$ curl -sS -w '\n[HTTP %{http_code}]\n' https://proxy.golang.org/github.com/octoverse-id/octonomy-go/@v/list
v1.0.0
[HTTP 200]
$ curl -sS -w '\n[HTTP %{http_code}]\n' https://proxy.golang.org/github.com/octoverse-id/octonomy-go/v2/@v/list
                                            # no rows: no TAGGED /v2 release exists
[HTTP 200]
```

Read that for what it is: the proxy's **current** view of **tagged** versions. `@v/list` deliberately
omits pseudo-versions, so an empty list means "nothing is released", not "nothing resolves" — a
`go get` on the `/v2` path still resolves the default branch to a pseudo-version, as noted above.
Keep `-w` on the command, too: a bare `curl -s` renders a network failure as the same blank output a
genuinely empty list produces, which is how this kind of evidence turns into a false claim.

That is what made adopting the `/v2` module path free, since no import path was in the wild to break.

> **`version.go` does not agree with that yet, and should not.** The `Version` constant on `main`
> still reads `0.1.0`, so the default User-Agent is `octonomy-go/0.1.0`. It is a **placeholder left
> from before anything was released**, kept to match the historical `## [0.1.0]` CHANGELOG heading,
> and no tag anywhere corresponds to it — `v1.0.0` belongs to the other module. The first `/v2`
> release PR replaces it, since `version.go` is bumped there and nowhere else (see
> [Where this shows up](#where-this-shows-up)); from that point on the constant names the most recent
> release of *this* line until the next release PR moves it.

**Why the tag history looks odd.** `v1.0.0` is not an ancestor of `main`. It sits on
`support/go1.13`, whose `go.mod` declares the **unsuffixed** module path, while `main` declares
`.../v2`. Tags on the two lines interleave by date and never by lineage, so
`git describe` on `main` will not find `v1.0.0` and should not. Read a tag's line from the branch it
points into, not from its number's position in the sequence.

## Bump rules

Decide the bump from the **most significant** change in the release.

### PATCH — `v1.0.x` (compat) / `v2.x.y` (modern)
Backward-compatible **bug fixes**. No change to the exported Go API.
- Examples: fix a header, correct envelope decoding, fix a query param name.

### MINOR — `v2.x.0` (modern only; the compat line takes no minors)
Backward-compatible **additions** to the exported API.
- Examples: a new resource service, a new method, a new optional field on a `*Params`/`*Create` struct,
  a new `Is*` helper.
- Existing callers keep compiling and working unchanged, with **one Go-level caveat**: adding a field
  to an exported struct breaks a caller who wrote an *unkeyed* composite literal
  (`octonomy.TagCreate{"Featured", "featured", "label"}`). Every example in this repository uses
  keyed fields, which is the reason to — but if you are weighing a field addition against a
  consumer you do not control, that is the exposure. Adding a field to a struct the caller can only
  receive (a response model) has no such issue.
- While the modern line is still on `v2.0.0-alpha.N` prereleases a necessary breaking change may ride
  an alpha bump, documented in the CHANGELOG; once `v2.0.0` proper ships, that stops being true.

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

> **One live policy, recorded.** An earlier plan proposed shipping `/api/v2` support as a `0.3.0`
> **minor**, which would have contradicted the major-effort rule above. That is settled and this
> paragraph is the only policy in force: v2 support ships on a new module path at `v2.x`, a major.
> The `0.x` framing it came from no longer applies to anything — see [Release state](#release-state).

**Note the two axes are independent.** The SDK's major version tracks *its own* Go API
compatibility, not the server's REST version. A future server `/api/v3` would not automatically force
an SDK `/v3` — only a break in the SDK's own exported Go API would.

> **Current state, to be exact:** the modern line speaks **both** surfaces. `Config.APIVersion`
> selects one and defaults to `APIV2`, and the namespace axis is per-request (`WithNamespace`). The
> compat line remains `/api/v1` only, permanently — that is its policy, not a gap. Every resource
> group the vendored contracts publish is implemented on the modern line, on either surface;
> [`api.md`](api.md#implemented) is the canonical inventory and [`roadmap.md`](roadmap.md) records
> the gaps that remain *within* those resources.

## Where this shows up

- **Per PR:** keep `CHANGELOG.md` `[Unreleased]` current. Do **not** bump `version.go` in feature/fix
  PRs.
- **At release time:** the version bump in `version.go` and the git tag happen in a dedicated release
  PR — see the runbook in [`release.md`](release.md).
