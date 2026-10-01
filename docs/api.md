# API mapping

How SDK methods map to Octonomy REST endpoints; this page is the client-side view. The
authoritative contracts are vendored from the server:

- [`openapi.yaml`](openapi.yaml) — `/api/v1`, server **3.2.1**. The default surface on this line.
- [`openapi-v2.yaml`](openapi-v2.yaml) — `/api/v2`, server **3.2.1**. Reached when
  `Config.APIVersion` is `APIV2`; it publishes the same paths, plus the namespace axis.

[`contract-coverage.yaml`](contract-coverage.yaml) lists every operation the two publish, each naming
the method below that implements it. Since [#94](https://github.com/octoverse-id/octonomy-go/issues/94)
every one of them has a method.

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
| `Tags.ListAliases` | GET | `/tags/{tag_id}/aliases` |
| `Aliases.Create` | POST | `/tag-aliases` |
| `Aliases.Get` | GET | `/tag-aliases/{id}` |
| `Aliases.List` | GET | `/tag-aliases` |
| `Aliases.Update` | PATCH | `/tag-aliases/{id}` |
| `Aliases.Delete` | DELETE | `/tag-aliases/{id}` |
| `Tags.Resolve` | GET | `/tag-resolution` |
| `Assignments.Create` | POST | `/tag-assignments` |
| `Assignments.Remove` | DELETE | `/tag-assignments` — **with a body** |
| `Assignments.BulkAssign` | POST | `/tag-assignments/bulk-assign` |
| `Assignments.BulkRemove` | POST | `/tag-assignments/bulk-remove` |
| `Resources.ListTags` | GET | `/resources/{resource_type}/{resource_id}/tags` |
| `Resources.ReplaceTags` | POST | `/resources/{resource_type}/{resource_id}/tags` |
| `Tags.ListResources` | GET | `/tags/{tag_id}/resources` |
| `AuditLogs.List` | GET | `/audit-logs` |
| `Tags.ListAuditLogs` | GET | `/tags/{tag_id}/audit-logs` |
| `Resources.ListAuditLogs` | GET | `/resources/{resource_type}/{resource_id}/audit-logs` |
| `Health.Live` | GET | `/health/live` (outside `/api/<version>`) |
| `Health.Ready` | GET | `/health/ready` (outside `/api/<version>`) |

### List parameters

`TagListParams` exposes the full server filter set: `application_id`, `include_shared`, `is_active`,
`parent_id`, `q` (as `Query`), `slug`, `type`, `vocabulary_id`, plus `Limit`/`Offset` from the embedded
`ListOptions`. `VocabularyListParams` exposes `application_id`, `include_shared`, `is_active`, and
paging. `TagAliasListParams` exposes `application_id`, `include_shared`, `is_active`, `q` (as `Query`),
`slug`, `tag_id`, and paging.

**`slug` is an exact match and `q` is not.** On the tag and alias lists the server matches `slug`
exactly and reads `q` as a case-insensitive substring of the name *or* the slug, so `q` is the search
box and `slug` is the lookup.

**`is_active` absent means active rows only.** The server applies that default on the tag, vocabulary,
and alias lists alike, so a nil `IsActive` is not "every row". Since `Delete` is deactivation,
`IsActive: octonomy.Bool(false)` is how you find deleted ones.

**The two alias list routes take different parameter sets, on purpose.** `Aliases.List` takes
`TagAliasListParams`; `Tags.ListAliases` takes `TagListAliasesParams`, which carries only what the
contract documents for `GET /tags/{tag_id}/aliases` — `application_id`, `include_shared`,
`is_active`, and paging. One server function backs both routes, so `q` and `slug` would in fact be
honored on the nested one; exposing them would put this SDK ahead of the published contract on a
route the server is free to narrow. `tag_id` has no meaning there at all — the path names the tag.

### Resolution parameters

`Tags.Resolve` is not a list, so `TagResolveParams` embeds no `ListOptions`. It carries
`ApplicationID`, `Type`, and `Scope`; the required `slug` is a positional argument. `ApplicationID`
both filters and **orders** — with it set, a row in that application outranks a tenant-shared one
carrying the same slug. Without it, application-scoped rows are not candidates at all.

`Scope` is typed (`ResolutionScopeGlobal`, `ResolutionScopeMerchant`), not a free string: the spec
describes it as a bare `string` while the server accepts exactly two values. An unset `Scope` is
omitted rather than sent empty.

**`Scope` is documented on both surfaces, and is sent on both.** Both vendored specs carry it on
`/tag-resolution`. A running server agrees: probed against 3.1.0, the v1 route validates it by
name, rejecting `scope=merchant` on a global request and an unknown value with
`Use 'global' or 'merchant'`. Gating it to `APIV2` would refuse a call every current deployment
supports, and the SDK has no version handshake with which to tell an old server from a new one.

**`global` is legal here and reserved elsewhere.** As a *scope* it pins the tenant-shared namespace;
as an `X-Namespace-Type` it is refused (see `WithNamespace`). `ResolutionScopeMerchant` resolves
inside the request's own namespace, so the SDK refuses it locally on a request that has none — and
since `WithNamespace` needs `Config.APIVersion = APIV2`, on this line's default client the error names
that opt-in too. `ResolutionScopeGlobal` needs no namespace, and from a namespaced request it does not
need `WithIncludeGlobal` either: the server adds the global namespace to the authorized set for
**this route only** when it sees `scope=global`. Passing both anyway is redundant, not contradictory,
and is allowed.

**`WithIncludeGlobal` with `ResolutionScopeMerchant` is refused**, though — they ask for opposite
things, and the server resolves the conflict silently in favor of the scope rather than reporting it.
Drop one: omit the option to stay inside the namespace, or omit the merchant scope to let global rows
back in.

**No match is not a `404`.** An unmatched slug is a `400 validation_error` whose details name
`slug`, so the branch that means "nothing is called that" is `IsValidation`, never `IsNotFound`.

## Assignments

Assignments are the one group where the vendored specs are wrong about **two** response shapes, and
where three request shapes differ from every other resource. All four calls carry their payload in
the body, so `WithApplication` is refused on all of them — each write struct names its own
`ApplicationID`, which is authoritative.

| Call | Shape worth knowing |
| ---- | ------------------- |
| `Create` | Idempotent. Re-assigning returns the existing row with `200` rather than `201`, and is not an error. Name the tag with **exactly one** of `TagID`, `AliasID`, `AliasSlug`. How a bad target is refused depends on how it was named — see the note below the table. |
| `Remove` | A `DELETE` **carrying a JSON body** — the row has no id route, so the four body fields identify it. Removing what is not there is a `204`. |
| `BulkAssign` | All or nothing; one unknown id fails the whole call. Takes `TagIDs`, `AliasSlugs`, or both. |
| `BulkRemove` | A **`POST`**, not a `DELETE`. Canonical tag ids only — no alias form. Tolerates ids that match nothing. |

**How a bad target is refused depends on how it was named.** By `TagID` or `AliasID` the server
reports the first check that fails, in the order of the columns below, so a row that is both inactive
and in another application reports its inactivity, never the mismatch. By `AliasSlug` there is no
order: visibility, activity and application are all conditions of one lookup, so every failure is
the same not-found `validation_error`, indistinguishable from an unknown slug:

| Named by | Not visible | Inactive | Another application |
| -------- | ----------- | -------- | ------------------- |
| `TagID` | `validation_error` | `inactive_tag` (`IsInactiveTag`) | `application_mismatch` |
| `AliasID` | `validation_error` | `validation_error` (alias, then its tag) | `application_mismatch` |
| `AliasSlug` | `validation_error` | `validation_error` — not a candidate | `validation_error` — not a candidate |

`Assignment.ApplicationID` (and `TagResource.ApplicationID`, which reads the same row) is a plain
`string` rather than the `*string` that `Tag`, `Vocabulary`, `TagAlias` and `AuditLog` carry: an
assignment is always application-scoped, so there is no tenant-shared case to represent. `Remove`
deletes the row outright — the exception to Octonomy's deactivate-don't-delete rule, since an
assignment is a link and an inactive link is an absent one.

**The two bulk responses are composites, and neither vendored spec describes them correctly.** Both
claim `bulk-assign` returns a bare array and document *no schema at all* for `bulk-remove`. Probed
against a running 3.1.0 server:

```
POST /tag-assignments/bulk-assign
  {"data":{"created":1,"existing":0,"skipped":0,"assignments":[{...}]}}

POST /tag-assignments/bulk-remove
  {"data":{"removed":1}}
```

A `[]Assignment` decoder written from the spec returns an empty slice and a nil error against that
body, so both go through `doData` with a result struct, and the envelope assertion is what catches a
mis-routed decode. The smoke test asserts the real counts against a booted server.

**The bulk results require their keys.** A composite of counters is not a resource: `created: 0,
existing: 0` with no rows is an ordinary result, and `removed: 0` is the most common answer bulk
remove gives, so a body whose keys the server renamed would be read as "nothing needed doing" instead
of as the contract break it is. A missing `created`, `existing`, `assignments`, or `removed` is
therefore an error. `Skipped` is exempt (it is vestigial), a present-but-null `assignments`
normalizes to an empty non-nil slice, and every row must be an object carrying its `id`.

`BulkAssignResult.Skipped` is **always zero** on 3.1.x and exists only because the server emits it.
Nothing is skipped because nothing is tolerated: an unknown tag id fails the entire call, and an id
outside the request's namespace is reported identically to one that exists nowhere. Both bulk calls
cap at the deployment's `MAX_BULK_TAGS`, 200 by default.

## Resource tags

The resource's side of tagging. `ResourceTag` is a tag as seen **from** a resource, with the `Tag`
nested whole; `TagResource` is a resource as seen **from** a tag. Both carry the namespace pair.

**`ReplaceTags` replaces — it does not merge.** Every tag on the resource and absent from the request
is removed. To add a tag, read the current set and send the union. And **an empty replace is legal**:
`ResourceReplace` with no `TagIDs` and no `AliasSlugs` clears every tag, returning `Created: 0` with
`Removed` equal to what was there. That is a deliberate difference from `BulkAssign`, which refuses an
empty request — so an empty slice arriving from a filter that matched nothing wipes the resource
silently and successfully. The whole operation is atomic and shares one `operation_id` across every
event it emits.

`ResourceReplace` carries no `ResourceType` or `ResourceID` even though the contract lists them: they
come from the path, and the server overwrites whatever a body sends.

**The replace response is a third composite, and the specs are wrong about it twice.** They claim a
bare array *and* claim the elements are `ResourceTag`. Probed against a running 3.1.0 server:

```
POST /resources/cart/{id}/tags
  {"data":{"created":1,"removed":0,"tags":[{"id":"…","slug":"…","usage_count":1,…}]}}
```

Those are **`Tag`** values — no `assignment_id`, no `assigned_at`. `ResourceReplaceResult` therefore
requires `created`, `removed`, and `tags`, on the same reasoning as the bulk results.

| Call | Shape worth knowing |
| ---- | ------------------- |
| `ListTags` | `ApplicationID` is **required** — the only list in this SDK where that holds. Supply it in the params or with `WithApplication`. |
| `ReplaceTags` | Full replace. Empty request clears the resource. Body carries the application, so `WithApplication` is refused. |
| `Tags.ListResources` | `ApplicationID` optional here; unset spans every application the caller can see. |

`ResourceListTagsParams.IncludeInactive` is **not** the `is_active` filter the tag and alias lists
take — different parameter, different polarity. Nil means active-only; `true` *widens* to include
deactivated tags. There is no way to ask for deactivated tags alone.

A `resource_id` containing a slash cannot be addressed on these routes: the server's router splits
the decoded path, so the call fails loudly as `IsUnexpectedStatus`, never `IsNotFound`.

## Audit logs

Octonomy's append-only mutation history, written by the server as a side effect of the mutation each
row describes. **List-only, on all three routes: no `Get`, no writes, and no `/audit-logs/{id}`** —
a single row is reached by filtering the collection. Every route needs `audit:read` (see
[Scopes](#scopes)); a token without it gets a `403` (`IsForbidden`), not an empty page.

Rows arrive **newest first** (`created_at` descending, `id` as a stable tiebreak), so offset paging
walks backwards through history.

| Call | Filters |
| ---- | ------- |
| `AuditLogs.List` | the full set: `action`, `actor_id`, `application_id`, `entity_id`, `entity_type`, `operation_id`, `resource_id`, `resource_type`, `tag_id` |
| `Tags.ListAuditLogs` | `action`, `actor_id`, `application_id` (documented on `/api/v2` only), `operation_id` |
| `Resources.ListAuditLogs` | `action`, `actor_id`, `application_id`, `operation_id` |

The two nested routes take narrower params types than the collection, matching what the contract
documents for each — as `TagListAliasesParams` does against `TagAliasListParams`. The one exception
is `application_id` on the tag route: `/api/v2` documents it, as the application scope a namespaced
read must carry, and `/api/v1` does not. The server's shared filter honors it on both, so the field
is kept on both surfaces rather than split by version. Every filter is an
**exact match**, they combine with AND, and the server ignores one set to the empty string.

**`EntityType` and `Action` are spelled differently for assignments.** The entity is
`tag_assignment` while its actions are `assignment.created` and `assignment.removed`; the other three
agree with themselves (`tag` / `tag.*`, `tag_alias` / `tag_alias.*`, `vocabulary` / `vocabulary.*`).
Since both are exact-match filters, `EntityType: octonomy.String("assignment")` returns an empty page
rather than an error.

**`OperationID` is the field that makes a multi-row mutation reconstructable.** `Resources.ReplaceTags`
and both bulk calls write one row per assignment they touch, all sharing an operation id, so the
removals and additions of a single replace read as one act. Read any row of an operation, then list
by its `OperationID` for the rest. `RequestID` correlates a row with the one HTTP request that
produced it; pass `WithRequestID` to supply your own and join the row to your service's logs.

**`Changes` is `Metadata` — an open object — and it has to be.** The contract gives the field no type
at all. What the server writes is `{"before": {…}, "after": {…}}`, but a `tag.deactivated` that
cascaded to aliases adds a third key, `cascaded_alias_ids`, whose value is an **array**:

```json
{"before": {"is_active": true}, "after": {"is_active": false}, "cascaded_alias_ids": ["…", "…"]}
```

A `Before`/`After` struct would drop that silently, and a `map[string]Metadata` would fail to decode
the row and take the whole page with it. So callers read
`log.Changes["after"].(map[string]interface{})`.

**An unknown tag or resource is an empty page here, not a `404`.** These routes filter the audit table
and never load the entity, so a row that never existed, one that was deactivated, and one outside the
request's namespace all answer `200` with no rows. Only a `tagID` that is not a uuid fails, at the
server's router: an envelope-less `404` that surfaces as `IsUnexpectedStatus`.

On `/api/v2`, audit reads are **namespace-filtered and global rows fail closed**. A namespaced read
(`WithNamespace`, on a client that set `Config.APIVersion = APIV2`) returns that namespace's rows and
no global ones; `WithIncludeGlobal` asks for both, and only widens what the request *asks* for. A
global request sees global rows only. There is no way to read across namespaces in one call.

## Responses

Every Octonomy payload arrives under a `data` key. Neither vendored spec documents either wrapper —
both show bare objects and bare arrays — so both are deliberate spec-vs-server divergences, and the
SDK follows the server (`octonomy/core/responses.py`, `octonomy/core/pagination.py` upstream).

- **Single resource:** `{ "data": { ... } }` → unwrapped by `Client.doData` into e.g. `*Tag`. A 2xx
  body with no `data` key is an error, not an empty struct.
- **List:** `{ "data": [...], "pagination": { "limit", "offset", "count", "next", "previous" } }` →
  a per-resource list type — `*TagList`, `*VocabularyList`, `*TagAliasList`, `*ResourceTagList`,
  `*TagResourceList`, `*AuditLogList` (this line has no `List[T]`; type parameters need Go 1.18). Both keys
  are required, and `pagination.limit` must be at least 1 — a real response always carries one — so
  `{}` cannot pass for "one page, nothing after it".
- **Composite:** `{ "data": { counts…, rows… } }` → `*BulkAssignResult`, `*BulkRemoveResult`,
  `*ResourceReplaceResult`. Not a page, so no pagination block; each requires its own keys, since a
  zero count is an ordinary answer.
- **Delete:** `204`, no body (deactivation on the server, except `Assignments.Remove`, which deletes
  the link). Any other 2xx, or a body, is an error: it is not evidence the row was deactivated.
- **Health:** a bare `{ "status": "ok" }`, with **no** `data` envelope — the one route without one,
  decoded by its own decoder. A 2xx with no readable `status` is an error.
- **Errors:** `{ "error": { "code", "message", "details", "request_id" } }` → `*APIError`.

A single resource, and every row of a list, must decode with its identity — `id` on most models,
`assignment_id` and the nested `tag.id` on `ResourceTag`, `resource_id` on `TagResource`: `{"data": {}}`,
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

Every operation the vendored contracts publish is implemented. A webhook receiver is the one thing
never implemented on this line, by policy: a consumer needing one moves to the `/v2` module.
