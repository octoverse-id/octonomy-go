# API mapping

How SDK methods map to Octonomy REST endpoints; this page is the client-side view. The
authoritative contracts are vendored from the server:

- [`openapi.yaml`](openapi.yaml) — `/api/v1`, server **3.2.1**. The default surface on this line.
- [`openapi-v2.yaml`](openapi-v2.yaml) — `/api/v2`, server **3.2.1**. Reached when
  `Config.APIVersion` is `APIV2`; it publishes the same paths, plus the namespace axis.

[`contract-coverage.yaml`](contract-coverage.yaml) lists every operation the two publish, each naming
the method below that implements it or the reason there is none yet.

## Base URL and headers

The client targets `Config.BaseURL + /api/<version>`, where the version is `Config.APIVersion` —
**`v1` by default on this line**, `v2` when it is `APIV2`. Every request on that surface carries:

| Header | Source | Required |
| ------ | ------ | -------- |
| `Authorization: Bearer <token>` | `Config.Token` | yes |
| `X-Tenant-ID` | `Config.TenantID` | yes |
| `X-Actor-ID` | `Config.ActorID` or `WithActor(...)` | no |
| `X-Request-ID` | `WithRequestID(...)`, per call; never minted by the SDK | no |
| `X-Namespace-Type` / `X-Namespace-ID` | `WithNamespace(...)`, per call; `/api/v2` only | no |
| `Accept: application/json` | always | — |
| `Content-Type: application/json` | requests with a body | — |
| `User-Agent` | `Config.UserAgent` (default `octonomy-go/<version>`) | — |

`WithApplication(...)` adds `application_id` to the query of a bodyless request, and
`WithIncludeGlobal()` adds `include_global=true` to an `/api/v2` read. A request whose options
contradict each other or the API version is refused before it is sent.

The health probes are the exception to all of this: they are rooted at `Config.BaseURL` itself, outside
`/api/<version>`, and send no `Authorization`, no `X-Tenant-ID` and no options.

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
| `Health.Live` | GET | `/health/live` (outside `/api/<version>`) |
| `Health.Ready` | GET | `/health/ready` (outside `/api/<version>`) |

### List parameters

`TagListParams` exposes the full server filter set: `application_id`, `include_shared`, `is_active`,
`parent_id`, `q` (as `Query`), `slug`, `type`, `vocabulary_id`, plus `Limit`/`Offset` from the embedded
`ListOptions`. `VocabularyListParams` exposes `application_id`, `include_shared`, `is_active`, and
paging.

## Responses

Every Octonomy payload arrives under a `data` key. Neither vendored spec documents either wrapper —
both show bare objects and bare arrays — so both are deliberate spec-vs-server divergences, and the
SDK follows the server (`octonomy/core/responses.py`, `octonomy/core/pagination.py` upstream).

- **Single resource:** `{ "data": { ... } }` → unwrapped by `Client.doData` into e.g. `*Tag`. A 2xx
  body with no `data` key is an error, not an empty struct.
- **List:** `{ "data": [...], "pagination": { "limit", "offset", "count", "next", "previous" } }` →
  `*TagList` / `*VocabularyList` (this line has no `List[T]`; type parameters need Go 1.18). Both keys
  are required, and `pagination.limit` must be at least 1 — a real response always carries one — so
  `{}` cannot pass for "one page, nothing after it".
- **Delete:** `204`, no body (deactivation on the server). Any other 2xx, or a body, is an error:
  it is not evidence the row was deactivated.
- **Health:** a bare `{ "status": "ok" }`, with **no** `data` envelope — the one route without one,
  decoded by its own decoder. A 2xx with no readable `status` is an error.
- **Errors:** `{ "error": { "code", "message", "details", "request_id" } }` → `*APIError`.

A single resource, and every row of a list, must decode with its identity (`id`): `{"data": {}}`,
`{"data": {"id": null}}` or a `null` row is an error, not a zero-valued struct. Bodies are read under a
32 MiB ceiling (`ErrResponseTooLarge`) and decoded under a 10,000-level nesting limit, which Go 1.13's
`encoding/json` does not enforce on its own.

## Error codes

Each code in the envelope has a `Code*` constant and an `Is*` helper:

| Code | Helper | Notes |
| ---- | ------ | ----- |
| `validation_error` (400) | `IsValidation` | |
| `authentication_required` (401) | `IsAuthError` | |
| `forbidden` (403) | `IsForbidden` | |
| `not_found` (404) | `IsNotFound` | only from Octonomy's envelope — a bare 404 is `unexpected_status` |
| `conflict` (409) | `IsConflict` | |
| `tenant_mismatch` | `IsTenantMismatch` | |
| `application_mismatch` | `IsApplicationMismatch` | |
| `inactive_tag` | `IsInactiveTag` | |
| `scope_immutable` (409) | `IsScopeImmutable` | a `PATCH` moving `application_id` or namespace; `IsConflict` does not match it |
| `namespace_not_supported` | `IsNamespaceNotSupported` | namespace headers on `/api/v1` |
| `namespace_invalid` | `IsNamespaceInvalid` | |
| `namespaced_writes_disabled` (403) | `IsNamespacedWritesDisabled` | an operator flag, not a caller error |
| `namespace_api_disabled` (503) | `IsNamespaceAPIDisabled` | an operator flag, not a caller error |
| `ambiguous_resolution` | `IsAmbiguousResolution` | the tag-resolution route |

Two codes are the SDK's own, never sent by the server. `unexpected_status` (`IsUnexpectedStatus`) is
any non-2xx without the envelope — a proxy, a wrong `BaseURL`, a server with no route for the
requested version — on **both** surfaces; no code is ever inferred from a bare status. `not_ready`
(`IsNotReady`) is a health probe the server answered with a non-2xx and its own `status` body. A
request that got no response at all is no `*APIError`: it matches `errors.Is(err, ErrUnreachable)`, and
the cause (`context.Canceled`, a `*net.OpError`) stays reachable through it.

## Not implemented on this tree

Tag aliases, tag resolution, tag assignments (incl. bulk), resource tags, and audit logs.
They are ported from `main` under [epic #88](https://github.com/octoverse-id/octonomy-go/issues/88);
[roadmap.md](roadmap.md) says which issue carries each and where they are implemented now. A webhook
receiver is the one thing never implemented on this line, by policy.
