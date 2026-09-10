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
> pagination foundation plus the **Vocabularies**, **Tags**, **Tag aliases**, **Tag resolution**,
> **Tag assignments**, and **Resource tags** resources are implemented; the remaining resources are
> tracked in [`docs/roadmap.md`](docs/roadmap.md).
>
> The first two releases will be `v1.0.0` on the frozen Go 1.13 compat line and `v2.0.0-alpha.1` on
> this line. See [versioning.md](docs/versioning.md).

> [!IMPORTANT]
> **Upgrading from `0.1.x`: the default REST surface is now `/api/v2`.** If your Octonomy server
> predates **2.0**, set `Config.APIVersion = octonomy.APIV1` — such a deployment has no `/api/v2`
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
| `X-Request-ID` *(optional)* | `WithRequestID(...)` | Correlates one call with the server's records of it |

```go
// Attribute a single mutation to a specific actor.
tag, err := client.Tags.Update(ctx, id, octonomy.TagUpdate{
	IsActive: octonomy.Bool(false),
}, octonomy.WithActor("svc-catalog"))
```

## Request correlation

`WithRequestID` sends your own correlation id as `X-Request-ID`. The server reads it when present
and mints `req_<uuid>` when it is not, then carries whichever id it holds into four places:

- the **audit row** for the mutation — `AuditLog.RequestID`
- the **outbox / webhook event** envelope — its `request_id` field, and the delivered webhook's
  `X-Octonomy-Request-ID` header
- the server's **structured request log**
- the **error envelope** of a failed call — `APIError.RequestID`

```go
requestID := uuid.NewString() // or the trace id your service already carries
log.Printf("updating tag %s request_id=%s", id, requestID)

tag, err := client.Tags.Update(ctx, id, octonomy.TagUpdate{
	Name: octonomy.String("Autumn"),
}, octonomy.WithActor("svc-catalog"), octonomy.WithRequestID(requestID))
```

The two options compose — actor is *who*, request id is *which call* — and a request id applies to
every method that takes options, read or write. The health probes are the exception: they take no
options at all, since a probe writes no audit row and emits no event (see [Health probes](#health-probes)).

**Generate the id yourself.** The SDK never mints one: no header is sent unless you use the option,
which leaves the server's own minting intact, and there is deliberately no `Config` field, since a
client-level default would stamp every call the process makes with a single value and correlate
nothing. On a **successful** call the SDK does not hand back the server's id — methods return
`(*T, error)` — so supplying your own is what puts an id in both your logs and Octonomy's. On a
**failed** call you get the server's id either way, from `APIError.RequestID`.

The id must be non-blank printable ASCII with no leading or trailing whitespace; anything else is
refused locally, before the request is sent. Each of those is a way the id you logged and the id the
server stores stop matching: a control byte is rejected by `net/http` itself, a non-ASCII one is
decoded `latin-1` server-side and stored as mojibake, and outer whitespace is trimmed by `net/http`
on the way out — so `" req-abc "` would be recorded as `"req-abc"`.

**Keep it short.** The server stores the id in a 100-character column; a longer one fails the audit
insert and the whole mutation answers `500` with no error envelope. A UUID (36) or a W3C
`traceparent` (55) is well inside it. The SDK does not enforce that width — it is a server rule,
which a later release may widen.

## API version and namespaces

`Config.APIVersion` selects the REST surface and defaults to `APIV2`. Both surfaces are live and
neither is deprecated; what separates them is the **namespace** axis, which exists only on v2.

```go
client, err := octonomy.New(octonomy.Config{
	BaseURL:    "https://octonomy.example.com",
	Token:      "svc_live_...",
	TenantID:   "acme",
	APIVersion: octonomy.APIV1, // for an Octonomy server older than 2.0
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

### Walking every page

`octonomy.Each` is the offset loop, written once. **It issues one request per page** — the name and
this sentence are the whole mitigation for a single call making N round trips, so raise `Limit` (the
server caps it at 200) when the collection is large:

```go
offset, err := octonomy.Each(ctx, octonomy.ListOptions{Limit: 200},
	func(ctx context.Context, o octonomy.ListOptions) (*octonomy.List[octonomy.Tag], error) {
		return client.Tags.List(ctx, &octonomy.TagListParams{ListOptions: o, Type: octonomy.String("label")})
	},
	func(tag octonomy.Tag) error {
		fmt.Println(tag.Slug)
		return nil
	},
)
```

The page function **must pass through** the `ListOptions` it is handed — that is what advances the
walk. A page function that ignores the offset would otherwise re-fetch page one forever; `Each`
detects that from the offset the server echoes and returns an error instead of looping. It catches
only that non-terminating shape — a dropped `Limit` is invisible, and a walk that fits in one page
succeeds either way.

The returned `offset` is `start.Offset` plus the number of items processed — the first item **not**
processed — so a failure is resumable: pass it back as `ListOptions{Offset: offset}` and the walk
picks up where it stopped. A walk that dies on page 40 of 100 keeps 39 pages of progress.

An offset is a **position, not an identity**. Over a stable, unchanged collection a resume
re-delivers the item that failed; where rows moved — or on the tags list, where they need not have —
that offset may address a different row, so a resume *may* retry the failed item and may equally skip
it. It is also not a polling cursor: a row created since that sorts *after* the old tail turns up,
one sorting before it never does.

**Offset drift is real and no client can fix it.** The server pages by limit/offset with no cursor,
so a concurrent create or delete shifts the window and an item can be delivered twice or missed. The
sort order is per endpoint — vocabularies and aliases by `(name, slug, id)`, audit logs and
assignments by their timestamp descending.

**`GET /tags` is worse: it has no `ORDER BY` at all.** Its `usage_count` annotation makes the query a
`GROUP BY`, and Django drops `Meta.ordering` from aggregate queries. `LIMIT`/`OFFSET` without
`ORDER BY` is undefined, so a tags walk may repeat or miss rows *with no concurrent writes*. Treat it
as best-effort unless the filtered set fits in one page.

De-duplicate on ID to remove double delivery. To *detect* the missed half, compare the first page's
`Pagination.Count` against the number of distinct IDs walked — but read it in one direction only, and
only for a complete walk from offset 0: `Count` is the size of the whole collection, so a resumed walk
legitimately sees fewer. **Fewer proves rows were missed; equal proves nothing**, since a concurrent
create and delete cancel out. See the `Each` doc comment for the full picture.

## Typed metadata

`Metadata` is `map[string]any`, so reading a field means a type assertion that panics when the stored
shape changes. `octonomy.DecodeMetadata` turns that into an error:

```go
type shipping struct {
	Carrier  string `json:"carrier"`
	Priority int    `json:"priority"`
}
cfg, err := octonomy.DecodeMetadata[shipping](tag.Metadata)
```

It is a function rather than a method because `Metadata` is a type **alias** and Go does not allow
methods on aliases. A nil map yields the zero value and no error; any error yields the zero value
rather than a half-filled struct.

**Integers beyond ±2^53 may lose precision, and not here.** The response decodes into
`map[string]any`, where every JSON number is a `float64`, so a value float64 cannot represent is
already rounded before `DecodeMetadata` sees it. It is not a clean cutoff — float64 loses resolution
in doubling steps: every integer is exact below 2^53, between 2^53 and 2^54 only the even ones
(`2^53+2` survives, `2^53+1` does not), past 2^54 only multiples of four. That is why this holds in
testing and fails on one production id. A `Metadata` you built yourself holding a real `int64` is
unaffected. Store large ids and amounts as **strings** in metadata and parse them on the way out.

## Implemented resources

| Resource | Status |
| -------- | ------ |
| Vocabularies (`client.Vocabularies`) | ✅ Create / Get / List / Update / Delete |
| Tags (`client.Tags`) | ✅ Create / Get / List / Update / Delete / ListAliases / Resolve |
| Tag aliases (`client.Aliases`) | ✅ Create / Get / List / Update / Delete |
| Tag assignments (`client.Assignments`) | ✅ Create / Remove / BulkAssign / BulkRemove |
| Resource tags (`client.Resources`) | ✅ ListTags / ReplaceTags, plus `Tags.ListResources` |
| Audit logs (`client.AuditLogs`) | ✅ List, plus `Tags.ListAuditLogs` / `Resources.ListAuditLogs` |
| Health (`client.Health`) | ✅ Live / Ready, and `NewHealthClient` for callers with no credentials |

Every implemented resource works on either surface. All seven v2 response models that carry namespace
identity now have it — `Tag`, `Vocabulary`, `TagAlias`, `Assignment`, `ResourceTag`, `TagResource`,
and `AuditLog` — as decode-only `NamespaceType` / `NamespaceID`, nil for a global row and on every
`/api/v1` response.

`Delete` is deactivation on all three, and the alias lists filter to active rows when `IsActive` is
unset — so `IsActive: octonomy.Bool(false)` is how you find deleted ones.

`Tags.Resolve` is the odd one out: an unmatched slug is a `400 validation_error`, **not** a `404`, so
branch on `IsValidation` rather than `IsNotFound`. See [`docs/api.md`](docs/api.md#error-codes).

`Resources.ReplaceTags` **replaces rather than merges**, and an empty request clears the resource
outright — read the current set and send the union if you meant to add.

Audit logs are **list-only** — server-written history, so there is no `Get` and no writes — and they
are the one group needing the `audit:read` scope: a token without it gets a `403` (`IsForbidden`),
not an empty page. `AuditLog.OperationID` groups every row one operation emitted, which is how a
`ReplaceTags` or a bulk call is reconstructed as a single act. See
[`docs/api.md`](docs/api.md#audit-logs).

## Health probes

`/health/live` and `/health/ready` sit at the **server root**, outside `/api/<version>`, and
authenticate nobody. Since `New` requires a token and a tenant, there is a second constructor that
requires neither — a base URL is the whole configuration:

```go
probe, err := octonomy.NewHealthClient("https://octonomy.example.com",
	// A probe loop usually wants a much shorter timeout than the 30s default.
	octonomy.WithHealthHTTPClient(&http.Client{Timeout: 2 * time.Second}),
)

st, err := probe.Health.Ready(ctx)
switch {
case err == nil:
	// ready; st.Status is the server's own word ("ok")
case octonomy.IsNotReady(err):
	// it answered and said it cannot serve: back off and re-probe
case errors.Is(err, octonomy.ErrUnreachable):
	// no response at all — refused, DNS, TLS, timeout, cancelled context
}
```

**Unreachable and unready are never collapsed into one error.** A `503 {"status": "unavailable"}` is
the application answering, so it is an `*APIError` (`IsNotReady`); a request that got no response
produces no `*APIError` at all and matches `errors.Is(err, octonomy.ErrUnreachable)`. They call for
different operator responses — wait versus go looking for the process.

A caller who already holds a full client uses `client.Health.Ready(ctx)`, which runs the same code
and sends the same request: no `Authorization`, no `X-Tenant-ID`, no `/api` prefix, from either entry
point. `ErrUnreachable` is not health-specific — every method in the package wraps it around a
request that got no response. See [`docs/api.md`](docs/api.md#health-probes).

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
