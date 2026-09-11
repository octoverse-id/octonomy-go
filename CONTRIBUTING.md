# Contributing to octonomy-go

Thanks for contributing! This is the official Go SDK for the Octonomy taxonomy service. It is a
hand-written, dependency-free client; please keep it that way.

## Getting started

```bash
git clone https://github.com/octoverse-id/octonomy-go.git
cd octonomy-go
go build ./...
make test
```

Requires **Go 1.24+** on `main`, which is the module `github.com/octoverse-id/octonomy-go/v2`.

**Check which line you are on before you write anything.** This repository publishes two modules from
two branches, and they have different Go floors and different rules:

| Branch | Module | Go | Takes |
| ------ | ------ | -- | ----- |
| `main` | `github.com/octoverse-id/octonomy-go/v2` | 1.24+ | Everything — features, fixes, security |
| `support/go1.13` | `github.com/octoverse-id/octonomy-go` | 1.13 | **Security fixes only**, until its 2027-08-31 sunset |

The compat line has **no generics, no `any`, and no post-1.13 standard library**, and it must compile
*and test* under a real `go1.13` toolchain. A fix that applies to both lands on `main` first and is
cherry-picked — see [docs/release.md](docs/release.md) for the backport step and
[docs/versioning.md](docs/versioning.md) for the policy. Everything below describes `main`.

The SDK has **no runtime dependencies** on either line — `go.mod` must stay free of a `require`
block for runtime packages. Dev tools (`golangci-lint`, `govulncheck`) are installed separately.

## Quality gates

Run these before opening a PR; CI runs the same checks and must pass before merge:

```bash
make fmt-check   # gofmt -l . (no output = clean)
make vet         # go vet ./...
make lint        # golangci-lint run (if installed)
make test        # go test -race -cover ./...
make examples    # go build ./examples/...
```

`make check` bundles fmt-check + vet + build; `make release-check` runs the full pre-release gate.

## Coding conventions

These mirror [AGENTS.md](AGENTS.md):

- **Standard library only.** No third-party runtime dependencies.
- **One file per resource** (`tags.go`, `vocabularies.go`, …), each exposing a `*Service` reached
  from a field on `Client`.
- Methods take `context.Context` first and `...RequestOption` last.
- List methods return `*List[T]` and decode the `{data, pagination}` envelope. That spelling is
  specific to this line; `support/go1.13` has no type parameters and declares a `*TagList` /
  `*VocabularyList` per resource instead.
- Non-2xx responses become `*APIError`; add `Is<Code>` helpers for common codes.
- Write structs use pointer fields with `omitempty` so PATCH sends only what is set; server
  read-only fields are decode-only.
- The library never panics, exits, or logs — it returns wrapped errors (`octonomy:` prefix, `%w`).
- Keep types faithful to the vendored contracts — `docs/openapi-v2.yaml` (`/api/v2`, the default
  surface) and `docs/openapi.yaml` (`/api/v1`), both at server 3.1.1. Read the **v2** spec when
  adding a resource. Document any deliberate divergence.
- Every exported symbol has a doc comment.

## Testing expectations

- Table-driven tests with `net/http/httptest`. Assert request method/path/headers/query/body on the
  server side (`t.Errorf` in handlers) and decoded values on the client side.
- Cover success, the list envelope, and error decoding (`IsNotFound`/`IsConflict`/`IsValidation`).
- Run with `-race`. Keep new code covered.

## Branches, commits, and PRs

- Branch names follow [Conventional Branch](https://conventional-branch.github.io/):
  `<type>/<description>`, types `feature|feat|bugfix|fix|hotfix|release|support|chore`.
  `support/<description>` names a **long-lived** maintenance line (`support/go1.13`) rather than a
  unit of work, so it closes no issue and is exempt from the issue-number rule below; work targeting
  it still branches off it with an ordinary issue-numbered branch.
- For planned work tracked by an issue, use `<type>/<issue-number>-<description>` and put
  `Closes #<n>` in the PR body.
- Commits follow [Conventional Commits](https://www.conventionalcommits.org/) (e.g.
  `feat: add assignments client`).
- Fill out the PR checklist (gofmt, vet, lint, `go test -race`, examples build, docs, CHANGELOG).

## Changelog and versioning

- Add a bullet under `## [Unreleased]` in [CHANGELOG.md](CHANGELOG.md) for any user-facing change
  ([Keep a Changelog](https://keepachangelog.com/) format).
- **Do not** bump the version in feature/fix PRs. Version bumps happen only in a dedicated
  `release/<version>` PR — see [docs/versioning.md](docs/versioning.md) and
  [docs/release.md](docs/release.md).

## Security

Please report vulnerabilities privately — see [SECURITY.md](SECURITY.md). Do not open a public issue
for security problems.
