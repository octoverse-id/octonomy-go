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

### Runnable examples

Ten of them, one per resource group plus the quickstart, each a single `main.go`, and every one calls
the API. You can be running them against a real Octonomy in about five minutes: `make dev-server`
boots one — Postgres, the published container, migrations, a minted service token — and ends by
printing the export block they read.

```bash
make dev-server          # boots, then prints the exports; `make dev-server-env` reprints them
export OCTONOMY_BASE_URL='http://127.0.0.1:8000' OCTONOMY_TOKEN='octo_...' OCTONOMY_TENANT_ID='harness-tenant'
export OCTONOMY_APPLICATION_ID='harness-app' OCTONOMY_NAMESPACE_TYPE='merchant' OCTONOMY_NAMESPACE_ID='harness-merchant'

go run ./examples/quickstart
make dev-server-down     # when you are finished
```

Each one demonstrates a semantic that is easy to get wrong, not just a create call:

| Example | What it shows |
| ------- | ------------- |
| [`quickstart`](examples/quickstart/main.go) | Configure, create, recover from a conflict, list, walk every page with `Each`, read metadata with a checked assertion |
| [`vocabularies`](examples/vocabularies/main.go) | Exact-slug lookup; `Metadata` replaces and never merges, and `Metadata{}` empties it where nil leaves it alone; a nil field is omitted, so no field can be cleared to null; `Delete` is deactivation |
| [`tags`](examples/tags/main.go) | Hierarchy via `ParentID`, and a nil `ParentID` leaving the link alone; uniqueness is on `(type, slug)`, so the same slug under another type is legal; assembling the tree, the parent cycle the server accepts and `BuildTagTree` refuses, and the orphan a deactivated parent leaves |
| [`aliases`](examples/aliases/main.go) | Two routes for the same rows; re-pointing an alias; the cascade from a deactivated tag |
| [`resolution`](examples/resolution/main.go) | Alias matches; an unmatched slug is a `400`, not a `404`; the type tie and how to break it |
| [`assignments`](examples/assignments/main.go) | Assignment is idempotent; the alias form; bulk counters; bulk is all-or-nothing |
| [`resources`](examples/resources/main.go) | `ReplaceTags` replaces rather than merges, and an empty request clears the resource |
| [`audit-logs`](examples/audit-logs/main.go) | Newest-first rows, a caller-supplied request id coming back, `OperationID` as one act |
| [`health`](examples/health/main.go) | Credential-free probes, and unreachable versus answered-but-not-ready |
| [`namespaces`](examples/namespaces/main.go) | The `APIV2` opt-in this line needs, merchant scoping, what `include_global` widens, and the options the SDK refuses |

There is no webhook example: this line never ships a webhook receiver.
`make examples` compile-checks all of them, fails if it finds none, and runs inside
`make release-check`.

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

### Walking every page

`octonomy.Each` is the offset loop, written once. **It issues one request per page** — the name and
this sentence are the whole mitigation for a single call making N round trips, so raise `Limit` (the
server caps it at 200) when the collection is large:

```go
offset, err := octonomy.Each(ctx, octonomy.ListOptions{Limit: 200},
	func(ctx context.Context, o octonomy.ListOptions) (octonomy.Page, error) {
		return client.Tags.List(ctx, &octonomy.TagListParams{ListOptions: o, Type: octonomy.String("label")})
	},
	func(item interface{}) error {
		tag, ok := item.(octonomy.Tag)
		if !ok {
			return fmt.Errorf("walk tags: got a %T", item)
		}
		fmt.Println(tag.Slug)
		return nil
	},
)
```

The page function returns an `octonomy.Page`, which every list envelope implements, so a list
method's result is returned from it as it is. The callback receives each row as an `interface{}`
holding the row's **value** — an `octonomy.Tag` from a `*TagList`, never a `*Tag`. Assert it with
the two-value form: the one-value form panics in your callback if the two closures are edited apart.
A mismatch returned as an error stops the walk with the offset of the row it refused.

The page function **must pass through** the `ListOptions` it is handed — that is what advances the
walk. A page function that ignores the offset would otherwise re-fetch page one forever; `Each`
detects that from the offset the server echoes and returns an error instead of looping. It catches
only that non-terminating shape — a dropped `Limit` is invisible, and a walk that fits in one page
succeeds either way.

The returned `offset` is `start.Offset` plus the number of items processed — the first item **not**
processed — so a failure is resumable: pass it back as `ListOptions{Offset: offset}` and the walk
picks up where it stopped. A walk that dies on page 40 of 100 keeps 39 pages of progress.

An offset is a **position, not an identity**. Over a stable, unchanged collection a resume
re-delivers the item that failed; where rows moved — or on a pre-3.2.1 tags list, where they need not
have — that offset may address a different row, so a resume *may* retry the failed item and may
equally skip it. It is also not a polling cursor: a row created since that sorts *after* the old tail
turns up, one sorting before it never does.

**Offset drift is real and no client can fix it.** The server pages by limit/offset with no cursor,
so a concurrent create or delete shifts the window and an item can be delivered twice or missed. The
sort order is per endpoint — vocabularies and aliases by `(name, slug, id)`, and tags too *from
server 3.2.1* (see below), audit logs and assignments by their timestamp descending.

**`GET /tags` was worse before server 3.2.1: it had no `ORDER BY` at all.** Its `usage_count`
annotation makes the query a `GROUP BY`, and Django drops `Meta.ordering` from aggregate queries, so
`Tag.Meta.ordering` was silently dropped. `LIMIT`/`OFFSET` without `ORDER BY` is undefined, so a tags
walk could repeat or miss rows *with no concurrent writes at all*. [Server
3.2.1](https://github.com/octoverse-id/octonomy/issues/162) orders the list by `(name, slug, id)` on
both API surfaces and the hazard is gone with it.

**The caveat is qualified by server version rather than deleted**, deliberately
([#49](https://github.com/octoverse-id/octonomy-go/issues/49)): the SDK does no version handshake, so
it cannot tell a fixed server from an unfixed one, and a caller pointed at 3.2.0 or older still has
the whole problem. Against **3.2.0 and older**, treat a tags walk as best-effort unless the filtered
set fits in one page. Against **3.2.1 and newer**, the tags list is as safe to walk as vocabularies
and aliases and no safer — ordering makes a *fixed* result set page deterministically, it does not
give you a snapshot, so ordinary offset drift still applies.

De-duplicate on ID to remove double delivery. To *detect* the missed half, compare the first page's
`Pagination.Count` against the number of distinct IDs walked — but read it in one direction only, and
only for a complete walk from offset 0: `Count` is the size of the whole collection, so a resumed walk
legitimately sees fewer. **Fewer proves rows were missed; equal proves nothing**, since a concurrent
create and delete cancel out. See the `Each` doc comment for the full picture.

## Assembling the tag hierarchy

Tags nest through `ParentID` and the server returns them flat — there is no tree endpoint and no
children route, so a category browser either filters the list once per parent (one request per node)
or fetches the set and assembles it locally. `octonomy.BuildTagTree` is that assembly: it makes no
request, takes no context, and never consults the server.

```go
page, err := client.Tags.List(ctx, &octonomy.TagListParams{
	VocabularyID: octonomy.String(vocab.ID),
	ListOptions:  octonomy.ListOptions{Limit: 200},
})
if err != nil {
	return err
}

tree, err := octonomy.BuildTagTree(page.Data)
if err != nil {
	return err // a cycle or a repeated id -- see below
}

// Walk, not a loop over Roots: a root's Depth is always 0, and its children
// are not in that slice.
if err := tree.Walk(func(n *octonomy.TagNode) error {
	fmt.Println(strings.Repeat("  ", n.Depth) + n.Tag.Name)
	return nil
}); err != nil {
	return err // whatever your callback returned, unchanged
}
```

**Check that second error.** `BuildTagTree` returns `(nil, err)` on a refusal, and every method here
tolerates a nil receiver — so a snippet that drops it walks an empty tree and renders nothing, which
turns the loud refusal below into the silent empty page it exists to prevent.

**No tag is ever dropped.** On success `tree.Len() == len(tags)` and every tag is reachable from
`Roots` exactly once. Ambiguity is an error rather than a quiet choice — a helper that is *almost*
right is worse than none, because the workaround outlives the bug. That leaves four questions, and
Octonomy's own behavior answers all four:

| Case | What happens | Why |
| ---- | ------------ | --- |
| Parent not in the slice | The tag becomes a root and is listed in `Orphans` (`node.IsOrphan()`) | It is **ordinary**, not corruption — see below |
| Inactive tags | Kept, untouched | Pruning is a filter (`TagListParams.IsActive`); the server allows an *active* tag under an *inactive* parent, so pruning here would orphan live children |
| A parent cycle | `ErrTagCycle`, naming the chain | The server permits one: the database forbids only `parent_id = id`, and the parent validator never walks the ancestry. Every tag in a cycle has a parent in the set, so a naive assembler silently returns fewer rows than it was given |
| Depth | Reported on each node, never limited | Assembly, the cycle check and `Walk` all use explicit stacks, so a deep chain costs memory rather than a stack overflow |

**A missing parent is the normal case, and three routine things produce one:** a deactivated parent
(delete is deactivation, the cascade reaches the tag's *aliases* and never its children, and an
unfiltered list returns active rows only), namespace scope (on `/api/v2`, a namespaced tag may name a
**global** parent, which a read without `WithIncludeGlobal` does not return), and any filter or page at all. So
an orphan is promoted to a root rather than dropped, and named in `Orphans` rather than disguised as
a real root. The remedy is a fetch, not a guess — `Tags.Get` reads deactivated rows:

```go
for _, node := range tree.Orphans {
	parent, err := client.Tags.Get(ctx, *node.Tag.ParentID) // readable even when deactivated
	...
}
```

The tree is a **snapshot**, and `Tag` is copied **shallowly**: its `*string` fields and its
`Metadata` map still point at what the input pointed at, including when the input is the slice a
`Tags.List` response decoded into. Writing through one of those (`*page.Data[1].ParentID = ...`)
changes what a node reports and can leave `Tag.ParentID` disagreeing with the `Parent` link built
from it, so edit the slice and build again rather than mutating tags you have already assembled.

`TagNode.Path()` is the breadcrumb — root first, the node last — and `tree.Node(id)` is where it
starts. `Roots`, `Orphans` and every `Children` slice are in **input order**; nothing is sorted. When
the input is a list page from a 3.2.1-or-newer server that is the server's `(name, slug, id)` order,
but `BuildTagTree` neither requires nor imposes it — the input can equally be a concatenated walk or
a hand-built slice. Sort the input, or sort `Children` in a `Walk`, if you need a particular
rendering. Note that no server ordering *guarantees* parents before children — `(name, slug, id)`
sorts on the name, so it may happen to and is never obliged to — and assembly never assumes it.

A repeated id is refused with `ErrDuplicateTagID` rather than resolved: the copies may disagree about
`ParentID`, and choosing between them is the one place assembly could silently build a *different*
tree. An `Each` walk can deliver a row twice, so de-duplicate on id first — the short loop is in the
`BuildTagTree` doc comment.

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
- **`Each` is not generic.** Its page function returns an `octonomy.Page` rather than a
  `*List[T]`, and its callback takes an `interface{}` holding the row's value rather than a `T` —
  see [Walking every page](#walking-every-page).
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
make examples     # compile-check every runnable example (fails if there are none)
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
