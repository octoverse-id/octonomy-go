# Octonomy Go SDK

[![CI](https://github.com/octoverse-id/octonomy-go/actions/workflows/ci.yml/badge.svg)](https://github.com/octoverse-id/octonomy-go/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/octoverse-id/octonomy-go.svg)](https://pkg.go.dev/github.com/octoverse-id/octonomy-go)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

The official Go client for [Octonomy](https://github.com/octoverse-id/octonomy) — a multi-tenant,
multi-application tag management and taxonomy service. This SDK is a hand-written, **dependency-free**
(standard library only) client for the Octonomy REST API; this tree speaks `/api/v1`.

> ## This is the Go 1.13 line (`v1.x`)
>
> You are on branch `support/go1.13`. This line exists for one reason: to give a consumer pinned to
> **Go 1.13** a client that compiles.
>
> - **It takes capability parity with the `/v2` line** — the resource groups, `/api/v2` and the
>   namespace axis — ported from `main`'s code, through its sunset.
>   [Epic #88](https://github.com/octoverse-id/octonomy-go/issues/88) tracks the work;
>   [Implemented resources](#implemented-resources) is what this tree has. The security-fixes-only
>   freeze published with `v1.0.0` is withdrawn.
> - **Never a breaking change.** This line can never publish a major — its module path is unsuffixed,
>   and `.../v2` is the other line's module — so every release is a `v1.x`, and every `v1.x` keeps
>   `v1.0.0` code compiling, with no signature, field type, or default changed under it. (The one
>   exception is Go's, not this line's: a minor that adds a struct field breaks an *unkeyed* literal
>   of that struct — see the MINOR rule in [versioning.md](docs/versioning.md).)
> - **Never a webhook receiver.** A consumer needing one moves to `/v2`.
> - **Sunset: 2027-08-31.** After that date this line receives nothing. See
>   [SECURITY.md](SECURITY.md).
> - A published `v1.x` **cannot be recalled** for this audience: `retract` shipped in Go 1.16, so a
>   Go 1.13 toolchain ignores it.
>
> **Able to run the active line's Go, or to upgrade to it?** Then use that line instead — a different
> module path and a different module, with a floor far above this one's. Check the version it requires
> before you switch; it is recorded where it can stay true, in
> [`main`'s README](https://github.com/octoverse-id/octonomy-go/blob/main/README.md).
>
> ```bash
> go get github.com/octoverse-id/octonomy-go/v2   # v2.x, active development
> ```
>
> That line is under active development, so what it implements keeps moving and this page does not
> restate it. Its current surface is described on `main`:
> [README](https://github.com/octoverse-id/octonomy-go/blob/main/README.md) and
> [API mapping](https://github.com/octoverse-id/octonomy-go/blob/main/docs/api.md#implemented).
> See [versioning.md](docs/versioning.md) for both lines' support policies.

## Install

```bash
go get github.com/octoverse-id/octonomy-go
```

Requires Go **1.13** or newer. The two module paths are different modules to Go, so version
selection, `go get -u`, and dependency bots cannot move you between the lines.

## Quickstart

```go
package main

import (
	"context"
	"fmt"
	"log"

	octonomy "github.com/octoverse-id/octonomy-go"
)

func main() {
	client, err := octonomy.New(octonomy.Config{
		BaseURL:  "https://octonomy.example.com", // SDK appends /api/v1
		Token:    "svc_live_...",                 // service token -> Authorization: Bearer
		TenantID: "acme",                         // -> X-Tenant-ID
	})
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()
	tag, err := client.Tags.Create(ctx, octonomy.TagCreate{
		Name: "Featured",
		Slug: "featured",
		Type: "label",
	})
	if err != nil {
		if octonomy.IsConflict(err) {
			log.Fatal("a tag with this (type, slug) already exists")
		}
		log.Fatal(err)
	}
	fmt.Println("created tag", tag.ID)
}
```

A complete, runnable program lives in [`examples/quickstart`](examples/quickstart/main.go).

## Authentication and tenant scope

Every request carries two credentials from `Config`:

| Header | Source | Purpose |
| ------ | ------ | ------- |
| `Authorization: Bearer <token>` | `Config.Token` | Service token (scopes: `tags:read`, `tags:write`, `audit:read`) |
| `X-Tenant-ID` | `Config.TenantID` | Scopes every request to one tenant |
| `X-Actor-ID` *(optional)* | `Config.ActorID` or `WithActor(...)` | Attributes mutations in the audit log |

```go
// Attribute a single mutation to a specific actor.
tag, err := client.Tags.Update(ctx, id, octonomy.TagUpdate{
	IsActive: octonomy.Bool(false),
}, octonomy.WithActor("svc-catalog"))
```

## Errors

Non-2xx responses are returned as `*octonomy.APIError`, which exposes the server's error envelope
(`Code`, `Message`, `Details`, `RequestID`) plus the HTTP `StatusCode`. Branch on the helpers or the
`Code` constants:

```go
_, err := client.Tags.Get(ctx, id)
switch {
case octonomy.IsNotFound(err):
	// 404
case octonomy.IsValidation(err):
	apiErr, _ := octonomy.AsAPIError(err)
	fmt.Println(apiErr.Details)
}
```

## Pagination

List methods return a per-resource envelope — `*octonomy.TagList` and `*octonomy.VocabularyList` —
each with `Data` and `Pagination` (limit, offset, count, next, previous). Page with `ListOptions`:

```go
page, err := client.Tags.List(ctx, &octonomy.TagListParams{
	Type:        octonomy.String("label"),
	ListOptions: octonomy.ListOptions{Limit: 50, Offset: 0},
})
fmt.Println(len(page.Data), "of", page.Pagination.Count)
```

## Implemented resources

| Resource | Status |
| -------- | ------ |
| Vocabularies (`client.Vocabularies`) | ✅ Create / Get / List / Update / Delete |
| Tags (`client.Tags`) | ✅ Create / Get / List / Update / Delete |
| Tag aliases, resolution, assignments (+bulk), resource tags, audit logs | Not on this tree — [#94](https://github.com/octoverse-id/octonomy-go/issues/94) ports them from [`/v2`](https://pkg.go.dev/github.com/octoverse-id/octonomy-go/v2) |
| Health probes | Not on this tree — [#91](https://github.com/octoverse-id/octonomy-go/issues/91) ports them |
| Webhook receiver | ⛔ never on this line, by policy — use [`/v2`](https://pkg.go.dev/github.com/octoverse-id/octonomy-go/v2) |

### Differences from the `/v2` line you may hit

- **No `List[T]`.** Type parameters need Go 1.18. `TagList` and `VocabularyList` replace it; the
  fields are identical.
- **No `CodeScopeImmutable` constant.** Server 3.1.0 added `409 scope_immutable` on tag, vocabulary,
  and alias `PATCH`; this line exposes `PATCH` for the first two. The named constant and an `Is*` helper
  are absent, but nothing is lost at runtime: `parseError` preserves whatever `code` the server
  sends, so branch on the string directly.

  ```go
  if apiErr, ok := octonomy.AsAPIError(err); ok && apiErr.Code == "scope_immutable" {
      // the tag's scope cannot be changed after creation
  }
  ```
- **`Metadata` is `map[string]interface{}`**, not `map[string]any` — the same type, spelled the way
  Go 1.13 spells it.

## Common commands

```bash
make test         # go test -race -cover ./...
make check        # gofmt check + go vet + build + release-line guard + its tests
make test-go113   # the same build and tests on a REAL go1.13 toolchain
make smoke        # integration smoke test against a booted server (see make dev-server)
make lint         # golangci-lint (if installed)
make help         # list all targets
```

`make test` on a modern toolchain is **not** the gate on this line: a modern Go enforces the language
version from `go.mod` but not the stdlib version, so `io.ReadAll` (Go 1.16) compiles clean under
`go 1.13`. Only `make test-go113` catches that. See [development.md](docs/development.md).

## Documentation

- [Architecture](docs/architecture.md) — how the client is layered.
- [API mapping](docs/api.md) — SDK methods ↔ Octonomy endpoints, auth, scopes.
- [Development](docs/development.md) — setup, quality gates, testing.
- [Versioning](docs/versioning.md) — SemVer policy and which server contract this SDK targets.
- [Release](docs/release.md) — the release runbook.
- [Roadmap](docs/roadmap.md) — what the `/v2` line has that this tree does not, and the epic porting
  it here.
- [CHANGELOG](CHANGELOG.md)

## Contributing & security

See [CONTRIBUTING.md](CONTRIBUTING.md), [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md), and
[SECURITY.md](SECURITY.md). The repository follows
[Conventional Branch](https://conventional-branch.github.io/) naming and Semantic Versioning.

## License

Apache License 2.0 — see [LICENSE](LICENSE).
