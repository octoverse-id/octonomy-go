# API mapping

How SDK methods map to Octonomy REST endpoints. The vendored specs are the reference for endpoints,
parameters, and field names; this page is the client-side view.

- [`openapi-v2.yaml`](openapi-v2.yaml) — `/api/v2`, server **3.1.1**. The default surface.
- [`openapi.yaml`](openapi.yaml) — `/api/v1`, still vendored at server **1.0.0** until #6 refreshes
  it. The v1 contract itself barely moved between those releases.

**One exception, and it is load-bearing: the vendored spec is wrong about response envelopes.** The
server wraps every payload under `data` — lists as `{"data": [...], "pagination": {...}}` and single
resources as `{"data": {...}}` — and the generated spec documents neither. Where the two disagree the
running server wins. See [Responses](#responses).

## Base URL and headers

The client targets `Config.BaseURL + /api/<version>`, where the version comes from
`Config.APIVersion` and defaults to `APIV2`. Every request carries:

| Header | Source | Required |
| ------ | ------ | -------- |
| `Authorization: Bearer <token>` | `Config.Token` | yes |
| `X-Tenant-ID` | `Config.TenantID` | yes |
| `X-Actor-ID` | `Config.ActorID` or `WithActor(...)` | no |
| `Accept: application/json` | always | — |
| `Content-Type: application/json` | requests with a body | — |
| `User-Agent` | `Config.UserAgent` (default `octonomy-go/<version>`) | — |
| `X-Namespace-Type` / `X-Namespace-ID` | `WithNamespace(...)` | no — **v2 only**, all-or-nothing |

## API version and namespace scoping

| Surface | Prefix | Namespace axis |
| ------- | ------ | -------------- |
| `APIV2` *(default)* | `/api/v2` | yes — merchant / sub-tenant partitions below the application |
| `APIV1` | `/api/v1` | no — global only; the server rejects namespace headers with `namespace_not_supported` |

Namespace is per-request (`WithNamespace`, `WithGlobalNamespace`) and has no `Config` field on
purpose: omitting the headers is a legal request that returns the **global** namespace with a `200`,
so a client-level default would silently mis-scope reads rather than fail.

| Option | Contributes | Applies to |
| ------ | ----------- | ---------- |
| `WithNamespace(type, id)` | `X-Namespace-Type` + `X-Namespace-ID` headers | v2 only; `global` is a reserved type |
| `WithGlobalNamespace()` | nothing (absence selects global) | any |
| `WithApplication(id)` | `application_id` query param | any; **required** on a namespaced read |
| `WithIncludeGlobal()` | `include_global=true` query param | v2 reads only |

`application_id` is a **query** parameter on reads and may be either a query parameter or a body
field on writes — the server unions both. `include_global` is a **query** parameter, not a header,
and the server ignores it on writes; the SDK refuses it there rather than sending a no-op.

### Rejected before the request is sent

The transport refuses these locally, with an error naming the SDK-level fix and no HTTP round trip:
a namespace on a v1 client, a half-set or reserved (`global`) namespace pair, a blank application id,
a namespaced read with no application, and `WithIncludeGlobal` on a write.

**Contradictory scope is refused, never resolved by precedence.** That covers `WithApplication`
against a `*ListParams` field, and either scope option against a second call to itself with a
different value — `WithApplication("a"), WithApplication("b")` is an error, not "b wins". Repeating
the same value is fine. The one legitimate override is `WithGlobalNamespace()`, which clears the
namespace so a later `WithNamespace` can set a new one; cancelling deliberately and contradicting
yourself are different acts.

The server rejects all but the last of those by name too, so these are ergonomics rather than
correctness — except `include_global` on a write, which the server silently ignores.

### Response fields

`Tag` and `Vocabulary` carry `NamespaceType` and `NamespaceID` (`*string`, decode-only). Both are
nil for a global row, and on every `/api/v1` response. Scope is fixed at creation: changing
`application_id`, `namespace_type`, or `namespace_id` on a PATCH is a `409 scope_immutable`.

## Scopes

Service tokens carry scopes enforced by the server: `tags:read`, `tags:write`, `audit:read`. Read
methods need `tags:read`; mutating methods need `tags:write`.

## Implemented

| SDK method | HTTP | Path |
| ---------- | ---- | ---- |
| `Vocabularies.Create` | POST | `/vocabularies` |
| `Vocabularies.Get` | GET | `/vocabularies/{id}` |
| `Vocabularies.List` | GET | `/vocabularies` |
| `Vocabularies.Update` | PATCH | `/vocabularies/{id}` |
| `Vocabularies.Delete` | DELETE | `/vocabularies/{id}` |
| `Tags.Create` | POST | `/tags` |
| `Tags.Get` | GET | `/tags/{id}` |
| `Tags.List` | GET | `/tags` |
| `Tags.Update` | PATCH | `/tags/{id}` |
| `Tags.Delete` | DELETE | `/tags/{id}` |

### List parameters

`TagListParams` exposes the full server filter set: `application_id`, `include_shared`, `is_active`,
`parent_id`, `q` (as `Query`), `slug`, `type`, `vocabulary_id`, plus `Limit`/`Offset` from the embedded
`ListOptions`. `VocabularyListParams` exposes `application_id`, `include_shared`, `is_active`, and
paging.

## Responses

Every 2xx that carries a payload is wrapped in a `data` envelope. The SDK unwraps it for you; the
wire column is what the server actually sends.

| Call | On the wire | You get |
| ---- | ----------- | ------- |
| Single resource (`Create`/`Get`/`Update`) | `{"data": {...}}` | `*Tag`, `*Vocabulary` |
| List | `{"data": [...], "pagination": {...}}` | `*List[T]` |
| Delete | `204`, no body | `error` only (deactivation on the server) |
| Error | `{"error": {"code", "message", "details", "request_id"}}` | `*APIError` |

`Pagination` carries `limit`, `offset`, `count`, `next`, and `previous`.

A 2xx whose body does not match the shape above is an **error**, never a zero value. Decoding a
wrapped body straight into a `*Tag` yields an empty struct with a nil error, and an unexpected list
shape is indistinguishable from an empty page — so the client rejects both instead of returning
something that looks like data. An empty page (`"data": []`, or `"data": null`) is not an error: it
decodes to an empty non-nil slice either way.

## Error codes

| Code | Status | Helper |
| ---- | ------ | ------ |
| `validation_error` | 400 | `IsValidation` |
| `authentication_required` | 401 | `IsAuthError` |
| `forbidden` | 403 | `IsForbidden` |
| `not_found` | 404 | `IsNotFound` |
| `conflict` | 409 | `IsConflict` |
| `tenant_mismatch` | 400 | `IsTenantMismatch` |
| `application_mismatch` | 400 | `IsApplicationMismatch` |
| `inactive_tag` | 400 | `IsInactiveTag` |
| `namespace_not_supported` | 400 | `IsNamespaceNotSupported` |
| `namespace_invalid` | 400 | `IsNamespaceInvalid` |
| `namespaced_writes_disabled` | 403 | `IsNamespacedWritesDisabled` |
| `namespace_api_disabled` | 503 | `IsNamespaceAPIDisabled` |
| `ambiguous_resolution` | 400 | `IsAmbiguousResolution` |

`namespaced_writes_disabled` and `namespace_api_disabled` are **operator** states, not caller errors:
the deployment has `NAMESPACE_WRITE_ENABLED` or `NAMESPACE_V2_API_ENABLED` off. Retrying or changing
the payload will not help.

### Responses with no error envelope

A non-2xx that did not carry `{"error": {...}}` gets `CodeUnexpectedStatus` (`IsUnexpectedStatus`)
and never a code derived from its HTTP status. It means the failure did not come from Octonomy's
application layer — a proxy, a load balancer, or a server with no route for the requested API
version. **`IsNotFound` is therefore true only for a real Octonomy `not_found`.**

The previous behavior mapped a bare 404 to `not_found`, so a deployment with no `/api/v2` route
looked to every caller like an empty taxonomy with no error at all. A code that *does* arrive in an
envelope is preserved verbatim, including one this SDK has no constant for, which is what keeps a
`503 namespace_api_disabled` distinguishable from an infrastructure 503.

Response bodies are read with a 32 MiB ceiling; exceeding it returns `ErrResponseTooLarge`.

A **non-2xx** whose body exceeds the ceiling still comes back as an `*APIError` carrying the status
and `CodeUnexpectedStatus`, wrapping `ErrResponseTooLarge` so `errors.Is` still reaches it. Dropping
it to a bare read error would take a whole class of failures — the large ones — out of `AsAPIError`
and `IsUnexpectedStatus` while every other response of the same status kept working. A **2xx** read
failure stays a plain read error: a success status with an unusable payload has no classification
worth preserving.

## Not yet implemented

Tag aliases, tag resolution, tag assignments (incl. bulk), resource tags, audit logs, and health — see
[roadmap.md](roadmap.md).
