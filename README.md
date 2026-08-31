# Octonomy Go SDK

[![CI](https://github.com/octoverse-id/octonomy-go/actions/workflows/ci.yml/badge.svg)](https://github.com/octoverse-id/octonomy-go/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/octoverse-id/octonomy-go/v2.svg)](https://pkg.go.dev/github.com/octoverse-id/octonomy-go/v2)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

The official Go client for [Octonomy](https://github.com/octoverse-id/octonomy) — a multi-tenant,
multi-application tag management and taxonomy service. This SDK is a hand-written, **dependency-free**
(standard library only) client for the REST **v2** API, with `/api/v1` available as a configuration
option.

> **Status: nothing published yet.** No version of this SDK has ever been released — there are no
> git tags and the module proxy has served no semantic version. The transport, auth, error, and
> pagination foundation plus the **Vocabularies** and **Tags** resources are implemented; the
> remaining resources are tracked in [`docs/roadmap.md`](docs/roadmap.md).
>
> The first two releases will be `v1.0.0` on the frozen Go 1.13 compat line and `v2.0.0-alpha.1` on
> this line. See [versioning.md](docs/versioning.md).

> [!IMPORTANT]
> **Upgrading from `0.1.x`: the default REST surface is now `/api/v2`.** If your Octonomy server
> predates **3.0**, set `Config.APIVersion = octonomy.APIV1` — such a deployment has no `/api/v2`
> route and answers every call with an unrouted 404. This is a wire-level change that compiles
> clean, and there is no version handshake for the SDK to detect it with.
>
> The same release stops mapping an envelope-less non-2xx to a semantic code, so **`IsNotFound` no
> longer reports true for a bare 404** from a proxy or an unrouted path — see
> [Errors](#errors). That change is what makes the misconfiguration above loud rather than silent.

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
		BaseURL:  "https://octonomy.example.com", // SDK appends /api/v2
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

## API version and namespaces

`Config.APIVersion` selects the REST surface and defaults to `APIV2`. Both surfaces are live and
neither is deprecated; what separates them is the **namespace** axis, which exists only on v2.

```go
client, err := octonomy.New(octonomy.Config{
	BaseURL:    "https://octonomy.example.com",
	Token:      "svc_live_...",
	TenantID:   "acme",
	APIVersion: octonomy.APIV1, // for an Octonomy server older than 3.0
})
```

A namespace partitions rows below the application level, for merchant or sub-tenant isolation. It is
**per-request**, and there is deliberately no `Config` field for it:

```go
page, err := client.Tags.List(ctx, params,
	octonomy.WithNamespace("merchant", "acme-store"),
	octonomy.WithApplication("storefront"), // required: namespace sits below application
	octonomy.WithIncludeGlobal(),           // also return the tenant-shared rows
)
```

| Option | Sends | Notes |
| ------ | ----- | ----- |
| `WithNamespace(type, id)` | `X-Namespace-Type` / `X-Namespace-ID` | v2 only. `global` is a reserved type |
| `WithGlobalNamespace()` | *nothing* | The tenant-shared namespace is selected by sending no headers |
| `WithApplication(id)` | `?application_id=` | Bodyless requests only; required on a namespaced one. Writes use the body's `ApplicationID` |
| `WithIncludeGlobal()` | `?include_global=true` | Reads only; fail-closed on the server |

**Why no `Config.Namespace`.** Omitting the headers is not an error — the server returns the
**global** namespace with a `200`. A client-level default would therefore scope every read to
whichever merchant was configured at startup, silently, at call sites that all still look correct.
Per-request keeps the scope visible where the data is requested, and lets one shared `*Client` serve
many merchants concurrently.

The SDK refuses a few combinations locally, before issuing a request: a namespace on a v1 client, a
half-set or reserved namespace pair, a namespaced read with no application, and `WithIncludeGlobal`
on a write (which the server would ignore).

## Errors

Non-2xx responses are returned as `*octonomy.APIError`, which exposes the server's error envelope
(`Code`, `Message`, `Details`, `RequestID`) plus the HTTP `StatusCode`. Branch on the helpers or the
`Code` constants:

```go
_, err := client.Tags.Get(ctx, id)
switch {
case octonomy.IsNotFound(err):
	// a real Octonomy not_found
case octonomy.IsValidation(err):
	apiErr, _ := octonomy.AsAPIError(err)
	fmt.Println(apiErr.Details)
case octonomy.IsUnexpectedStatus(err):
	// non-2xx with no Octonomy error envelope: a proxy, a gateway, or a
	// server with no route for the API version this client targets
}
```

**A non-2xx that carries no error envelope gets `CodeUnexpectedStatus`, never a code derived from its
status.** `IsNotFound` is therefore true only for a real Octonomy `not_found` — a bare 404 from a
proxy or an unrouted path is not one. Deriving `not_found` from a status the SDK did not generate is
what let a missing `/api/v2` read as an empty taxonomy with no error. Codes that *do* arrive in an
envelope are preserved verbatim, including ones this SDK has no constant for.

Namespace errors have their own helpers. Two of them are **operator** states rather than caller
mistakes: `IsNamespacedWritesDisabled` (403) and `IsNamespaceAPIDisabled` (503) mean a rollout flag
is off on the server, so retrying or changing the payload will not help.

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

Both implemented resources work on either surface. `Tag` and `Vocabulary` carry `NamespaceType` /
`NamespaceID`, which are nil for a global row and on every `/api/v1` response.

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
