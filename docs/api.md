# API mapping

How SDK methods map to Octonomy REST **v1** endpoints. The vendored [`openapi.yaml`](openapi.yaml)
is the reference for endpoints, parameters, and field names; this page is the client-side view.

**One exception, and it is load-bearing: the vendored spec is wrong about response envelopes.** The
server wraps every payload under `data` — lists as `{"data": [...], "pagination": {...}}` and single
resources as `{"data": {...}}` — and the generated spec documents neither. Where the two disagree the
running server wins. See [Responses](#responses).

## Base URL and headers

The client targets `Config.BaseURL + /api/v1`. Every request carries:

| Header | Source | Required |
| ------ | ------ | -------- |
| `Authorization: Bearer <token>` | `Config.Token` | yes |
| `X-Tenant-ID` | `Config.TenantID` | yes |
| `X-Actor-ID` | `Config.ActorID` or `WithActor(...)` | no |
| `Accept: application/json` | always | — |
| `Content-Type: application/json` | requests with a body | — |
| `User-Agent` | `Config.UserAgent` (default `octonomy-go/<version>`) | — |

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

`validation_error` (400), `authentication_required` (401), `forbidden` (403), `not_found` (404),
`conflict` (409), `tenant_mismatch` (400), `application_mismatch` (400), `inactive_tag` (400). Each
has a `Code*` constant; `IsNotFound`/`IsConflict`/`IsValidation`/`IsAuthError`/`IsForbidden` cover the
common branches.

## Not yet implemented

Tag aliases, tag resolution, tag assignments (incl. bulk), resource tags, audit logs, and health — see
[roadmap.md](roadmap.md).
