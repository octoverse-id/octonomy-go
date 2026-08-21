# Octonomy Go SDK

[![CI](https://github.com/octoverse-id/octonomy-go/actions/workflows/ci.yml/badge.svg)](https://github.com/octoverse-id/octonomy-go/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/octoverse-id/octonomy-go/v2.svg)](https://pkg.go.dev/github.com/octoverse-id/octonomy-go/v2)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

The official Go client for [Octonomy](https://github.com/octoverse-id/octonomy) — a multi-tenant,
multi-application tag management and taxonomy service. This SDK is a hand-written, **dependency-free**
(standard library only) client for the stable REST **v1** API.

> **Status: nothing published yet.** No version of this SDK has ever been released — there are no
> git tags and the module proxy has served no semantic version. The transport, auth, error, and
> pagination foundation plus the **Vocabularies** and **Tags** resources are implemented; the
> remaining resources are tracked in [`docs/roadmap.md`](docs/roadmap.md).
>
> The first two releases will be `v1.0.0` on the frozen Go 1.13 compat line and `v2.0.0-alpha.1` on
> this line. See [versioning.md](docs/versioning.md).

## Install

```bash
go get github.com/octoverse-id/octonomy-go/v2
```

Requires Go 1.24 or newer.

> **On Go 1.13?** Use the frozen compatibility line instead — it lives at the **unsuffixed** module
> path and never receives features:
>
> ```bash
> go get github.com/octoverse-id/octonomy-go   # v1.x, Go 1.13, security fixes only
> ```
>
> The two paths are different modules, so Go will not move you between them. See
> [versioning.md](docs/versioning.md) for the support policy and the sunset date.

## Quickstart

```go
package main

import (
	"context"
	"fmt"
	"log"

	octonomy "github.com/octoverse-id/octonomy-go/v2"
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

List methods return a `*octonomy.List[T]` with `Data` and `Pagination` (limit, offset, count, next,
previous). Page with `ListOptions`:

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
| Tag aliases, resolution, assignments (+bulk), resource tags, audit logs, health | 🚧 see [`docs/roadmap.md`](docs/roadmap.md) |

## Common commands

```bash
make test    # go test -race -cover ./...
make check   # gofmt check + go vet + build
make lint    # golangci-lint (if installed)
make help    # list all targets
```

## Documentation

- [Architecture](docs/architecture.md) — how the client is layered.
- [API mapping](docs/api.md) — SDK methods ↔ Octonomy endpoints, auth, scopes.
- [Development](docs/development.md) — setup, quality gates, testing.
- [Versioning](docs/versioning.md) — SemVer policy and which server contract this SDK targets.
- [Release](docs/release.md) — the release runbook.
- [Roadmap](docs/roadmap.md) — the backlog of remaining resources.
- [CHANGELOG](CHANGELOG.md)

## Contributing & security

See [CONTRIBUTING.md](CONTRIBUTING.md), [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md), and
[SECURITY.md](SECURITY.md). The repository follows
[Conventional Branch](https://conventional-branch.github.io/) naming and Semantic Versioning.

## License

Apache License 2.0 — see [LICENSE](LICENSE).
