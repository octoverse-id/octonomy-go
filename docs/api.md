# API mapping

How SDK methods map to Octonomy REST **v1** endpoints. The authoritative contract is the vendored
[`openapi.yaml`](openapi.yaml); this page is the client-side view.

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

Every Octonomy payload arrives under a `data` key. The vendored `openapi.yaml` documents neither
wrapper — it shows bare objects and bare arrays — so both are deliberate spec-vs-server divergences,
and the SDK follows the server (`octonomy/core/responses.py`, `octonomy/core/pagination.py` upstream).

- **Single resource:** `{ "data": { ... } }` → unwrapped by `Client.doData` into e.g. `*Tag`. A 2xx
  body with no `data` key is an error, not an empty struct.
- **List:** `{ "data": [...], "pagination": { "limit", "offset", "count", "next", "previous" } }` →
  `*TagList` / `*VocabularyList` (this line has no `List[T]`; type parameters need Go 1.18).
- **Delete:** `204`, no body (deactivation on the server).
- **Errors:** `{ "error": { "code", "message", "details", "request_id" } }` → `*APIError`.

## Error codes

`validation_error` (400), `authentication_required` (401), `forbidden` (403), `not_found` (404),
`conflict` (409), `tenant_mismatch` (400), `application_mismatch` (400), `inactive_tag` (400). Each
has a `Code*` constant; `IsNotFound`/`IsConflict`/`IsValidation`/`IsAuthError`/`IsForbidden` cover the
common branches.

Server 3.1.0 also returns `scope_immutable` (409) on tag, vocabulary, and alias `PATCH`. This line
ships **no** constant or helper for it — a frozen-scope decision, not an oversight — but the code
string survives decoding, so `apiErr.Code == "scope_immutable"` works today.

## Not yet implemented

Tag aliases, tag resolution, tag assignments (incl. bulk), resource tags, audit logs, and health — see
[roadmap.md](roadmap.md).
