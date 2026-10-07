# Octonomy Go SDK

[![CI](https://github.com/octoverse-id/octonomy-go/actions/workflows/ci.yml/badge.svg)](https://github.com/octoverse-id/octonomy-go/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/octoverse-id/octonomy-go.svg)](https://pkg.go.dev/github.com/octoverse-id/octonomy-go)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

The official Go client for [Octonomy](https://github.com/octoverse-id/octonomy) — a multi-tenant,
multi-application tag management and taxonomy service. This SDK is a hand-written, **dependency-free**
(standard library only) client for the Octonomy REST API. It speaks `/api/v1` by default and
`/api/v2` — with the namespace axis — opt-in; see [API version and namespaces](#api-version-and-namespaces).

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
		BaseURL:  "https://octonomy.example.com", // SDK appends /api/v1 (the default surface)
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

Every request on the versioned API carries two credentials from `Config` (the
[health probes](#health-probes) carry neither):

| Header | Source | Purpose |
| ------ | ------ | ------- |
| `Authorization: Bearer <token>` | `Config.Token` | Service token (scopes: `tags:read`, `tags:write`, `audit:read`) |
| `X-Tenant-ID` | `Config.TenantID` | Scopes every request to one tenant |
| `X-Actor-ID` *(optional)* | `Config.ActorID` or `WithActor(...)` | Attributes mutations in the audit log |
| `X-Request-ID` *(optional)* | `WithRequestID(...)`, per call | Joins the server's audit row, event and logs to your own; never sent unless you supply one |

```go
// Attribute a single mutation to a specific actor, and correlate it with your logs.
tag, err := client.Tags.Update(ctx, id, octonomy.TagUpdate{
	IsActive: octonomy.Bool(false),
}, octonomy.WithActor("svc-catalog"), octonomy.WithRequestID(traceID))
```

## API version and namespaces

`Config.APIVersion` selects the REST surface. **On this line it defaults to `APIV1`**, which is where
every `v1.0.0` request went, so upgrading moves no existing caller to another surface. The `/v2`
module defaulted to `APIV2` when this selector was ported from it; this line cannot copy that,
because it can never change a default under a caller.

`/api/v2` adds the namespace axis — merchant or sub-tenant scoping below the tenant — and needs an
Octonomy server of 2.0 or later. Opt in, then scope each request; there is deliberately no client-level
namespace, so one shared `*Client` cannot scope every read to whichever merchant was configured first:

```go
client, err := octonomy.New(octonomy.Config{
	BaseURL:    "https://octonomy.example.com",
	Token:      "svc_live_...",
	TenantID:   "acme",
	APIVersion: octonomy.APIV2,
})

// A namespaced read must name its application; namespaced reads exclude
// tenant-shared rows unless you add WithIncludeGlobal.
tags, err := client.Tags.List(ctx, nil,
	octonomy.WithNamespace("merchant", "acme-store"),
	octonomy.WithApplication("storefront"))
```

| Option | Sends | Applies to |
| ------ | ----- | ---------- |
| `WithNamespace(type, id)` | `X-Namespace-Type` / `X-Namespace-ID` | `APIV2` only |
| `WithGlobalNamespace()` | no namespace headers — the tenant-shared namespace | any request; cancels an earlier `WithNamespace` |
| `WithApplication(id)` | `application_id` query parameter | bodyless requests (`Get`, `List`, `Delete`); a write names its application in the body's `ApplicationID` |
| `WithIncludeGlobal()` | `include_global=true` | `APIV2` reads only |

A request whose options contradict each other or the client's API version — a namespace on a v1
client, two different namespaces, `WithApplication` on a create — is refused **before it is sent**,
with an error naming the fix. Last-wins on a scope is never how a contradiction is resolved. `Tag` and
`Vocabulary` carry `NamespaceType` / `NamespaceID`, which are nil on a global row and on every
`/api/v1` response.

## Errors

Non-2xx responses are returned as `*octonomy.APIError`, which exposes the server's error envelope
(`Code`, `Message`, `Details`, `RequestID`) plus the HTTP `StatusCode`. Branch on the helpers or the
`Code` constants:

```go
_, err := client.Tags.Get(ctx, id)
switch {
case octonomy.IsNotFound(err):
	// Octonomy said 404 not_found
case octonomy.IsValidation(err):
	apiErr, _ := octonomy.AsAPIError(err)
	fmt.Println(apiErr.Details)
case octonomy.IsUnexpectedStatus(err):
	// a non-2xx that Octonomy did not write: a proxy, a wrong BaseURL, or a
	// server with no route for this API version
case errors.Is(err, octonomy.ErrUnreachable):
	// no response at all
}
```

**The helpers match the code in Octonomy's error envelope, never a bare status.** A 404 with no
envelope — a wrong `BaseURL`, a misrouted proxy — is `IsUnexpectedStatus`, not `IsNotFound`; reading it
as "no such tag" would turn an infrastructure failure into an empty taxonomy. (This line's first
release inferred a code from the status, and that was a defect; see the [CHANGELOG](CHANGELOG.md).) `IsScopeImmutable` names
the `409` a `PATCH` gets for trying to move a row's scope, which `IsConflict` deliberately does not
match: re-create the row in the target scope instead of retrying.

## Health probes

`/health/live` and `/health/ready` are unauthenticated and sit outside `/api/<version>`, so they need no
token and no tenant. Use `client.Health` on a full client, or build a probe-only client from a base URL:

```go
hc, err := octonomy.NewHealthClient(baseURL,
	octonomy.WithHealthHTTPClient(&http.Client{Timeout: 2 * time.Second}))

_, err = hc.Health.Ready(ctx)
switch {
case err == nil:
	// ready
case octonomy.IsNotReady(err):
	// the server answered and is not serving -- back off and re-probe
case errors.Is(err, octonomy.ErrUnreachable):
	// nothing answered -- wrong URL, process gone, or the deadline passed
}
```

## Pagination

List methods return a per-resource envelope — `*octonomy.TagList`, `*octonomy.VocabularyList`,
`*octonomy.TagAliasList`, `*octonomy.ResourceTagList`, `*octonomy.TagResourceList` and
`*octonomy.AuditLogList` — each with `Data` and `Pagination` (limit, offset, count, next, previous).
Page with `ListOptions`:

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
| Tag aliases (`client.Aliases`, `Tags.ListAliases`) | ✅ Create / Get / List / Update / Delete |
| Tag resolution (`Tags.Resolve`) | ✅ one read: a slug to its canonical tag, directly or by alias |
| Assignments (`client.Assignments`) | ✅ Create / Remove / BulkAssign / BulkRemove |
| Resource tags (`client.Resources`, `Tags.ListResources`) | ✅ ListTags / ReplaceTags / ListResources |
| Audit logs (`client.AuditLogs`, `Tags.ListAuditLogs`, `Resources.ListAuditLogs`) | ✅ List — list-only by design, there is no `Get` |
| Health probes (`client.Health`, `NewHealthClient`) | ✅ Live / Ready |
| Webhook receiver | ⛔ never on this line, by policy — use [`/v2`](https://pkg.go.dev/github.com/octoverse-id/octonomy-go/v2) |

### Differences from the `/v2` line you may hit

- **No `List[T]`.** Type parameters need Go 1.18. A per-resource type replaces each instantiation —
  `TagList`, `VocabularyList`, `TagAliasList`, `ResourceTagList`, `TagResourceList`, `AuditLogList`;
  the fields are identical.
- **The default API version is `APIV1`**, not `APIV2` — see
  [API version and namespaces](#api-version-and-namespaces).
- **A `PATCH` cannot clear a nullable field.** The `*Update` fields are pointers, where nil means
  "leave it alone", and the `/v2` module's `Optional[T]` is not ported because changing a published
  field's type would break `v1.0.0` callers. `Metadata` is the exception: `Metadata{}` sends `{}` and
  empties the stored object, while a nil `Metadata` leaves it alone.
- **JSON nests at most 10,000 levels**, in a request body and in a response. Go 1.13's
  `encoding/json` has no limit of its own, so a self-containing `Metadata` would hang it and a deeply
  nested response would exhaust the stack; this line refuses both with an error. A modern toolchain's
  `encoding/json` enforces the same response limit itself.
- **`Metadata` is `map[string]interface{}`**, not `map[string]any` — the same type, spelled the way
  Go 1.13 spells it.

## Common commands

```bash
make test         # go test -race -cover ./...
make check        # gofmt check + go vet + build + release-line guard + its tests
make test-go113   # the same build and tests on a REAL go1.13 toolchain
make smoke        # integration smoke test against a booted server (see make dev-server)
make test-integration # namespace isolation suite against a booted server
make lint         # golangci-lint on both modules (if installed)
make contract-check # the contract gate: what this client sends and decodes vs the vendored specs
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
- [Roadmap](docs/roadmap.md) — the parity policy and where its remaining work is tracked; it links to
  [`main`'s API mapping](https://github.com/octoverse-id/octonomy-go/blob/main/docs/api.md#implemented)
  for what the `/v2` line implements.
- [Porting checklist](docs/porting-checklist.md) — the rewrites a port from `main` makes, each marked
  by whether a miss fails the go1.13 build or compiles and is wrong.
- [Test disposition](docs/compat-test-disposition.md) — what became of each of `main`'s test files on
  this line, and how `t.Cleanup` is replaced on Go 1.13.
- [CHANGELOG](CHANGELOG.md)

## Contributing & security

See [CONTRIBUTING.md](CONTRIBUTING.md), [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md), and
[SECURITY.md](SECURITY.md). The repository follows
[Conventional Branch](https://conventional-branch.github.io/) naming and Semantic Versioning.

## License

Apache License 2.0 — see [LICENSE](LICENSE).
