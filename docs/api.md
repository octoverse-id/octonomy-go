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
| `WithApplication(id)` | `application_id` query param | **bodyless requests only**; required on a namespaced one |
| `WithIncludeGlobal()` | `include_global=true` query param | v2 reads only |

**Application scope follows the body.** On a bodyless request (`GET`, `HEAD`, `DELETE`) the query
string is authoritative, so `WithApplication` is how you set it. On a `POST` or `PATCH` the **body**
is authoritative and `WithApplication` is refused: probed against 3.1.0, the query value persists on
a namespaced create but is **dropped** on a global one, and a body `application_id` beats the query
in both. Honoring the option there would silently produce a tenant-shared row for a caller who asked
for application scope. Write bodies carry `ApplicationID`, which is authoritative in every case.

`include_global` is a **query** parameter, not a header, and the server ignores it on writes; the SDK
refuses it there for the same reason.

### Rejected before the request is sent

The transport refuses these locally, with an error naming the SDK-level fix and no HTTP round trip:
a namespace on a v1 client, a half-set or reserved (`global`) namespace pair, a blank application id,
a namespaced **bodyless** request with no application, `WithIncludeGlobal` on a write, and
`WithIncludeGlobal` alongside `scope=merchant`.

"Bodyless" is the real criterion for that last one, not "read": on a GET, HEAD, or DELETE the query
string is the whole request, so the check is complete. A namespaced `POST`/`PATCH` is still sent —
its application may be a body field the transport cannot see without reflection, and the server
answers `403` naming the namespace grant if it truly is missing.

**Contradictory scope is refused, never resolved by precedence.** That covers `WithApplication`
against a `*ListParams` field, and either scope option against a second call to itself with a
different value — `WithApplication("a"), WithApplication("b")` is an error, not "b wins". Repeating
the same value is fine. The one legitimate override is `WithGlobalNamespace()`, which clears the
namespace so a later `WithNamespace` can set a new one; cancelling deliberately and contradicting
yourself are different acts.

The server rejects all but the last two by name too, so most of these are ergonomics rather than
correctness. The exceptions are the two `include_global` refusals, which the server does **not**
report: on a write it ignores the parameter, and on a `scope=merchant` resolution it discards it in
favor of the scope (`effective_resolution_scope` returns `include_global` false on that branch,
whatever the query said). Either way the caller asked for global rows, did not get them, and would
see no sign the option did nothing — so the SDK makes it loud.

### Response fields

`Tag`, `Vocabulary`, and `TagAlias` carry `NamespaceType` and `NamespaceID` (`*string`,
decode-only). Both are nil for a global row, and on every `/api/v1` response. Scope is fixed at
creation: changing `application_id`, `namespace_type`, or `namespace_id` on a PATCH is a
`409 scope_immutable`.

That code has no constant in this package yet (it arrives with the v1 contract refresh, #6), which
costs a caller nothing: a code the server sends in the envelope is preserved verbatim, so
`APIError.Code == "scope_immutable"` works today. It is deliberately **not** matched by `IsConflict`,
which keys on `conflict` — a fixed-scope refusal read as a duplicate slug sends a caller down a retry
path that cannot succeed. Re-create the row in the target scope instead.

`TagAlias.TagID` is decode-only on the response but is not immutable: `TagAliasUpdate.TagID`
re-points an alias at a different tag, which is an ordinary edit rather than a scope change. The new
target must satisfy the same rules a create does — active, same tenant, and scope-compatible (an
application-specific tag takes aliases only in its own application, else `application_mismatch`; a
namespaced alias may target only a global or same-namespace tag). An inactive target is a plain
`validation_error`, not `inactive_tag`, which the server reserves for assignment.

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

### List parameters

`TagListParams` exposes the full server filter set: `application_id`, `include_shared`, `is_active`,
`parent_id`, `q` (as `Query`), `slug`, `type`, `vocabulary_id`, plus `Limit`/`Offset` from the embedded
`ListOptions`. `VocabularyListParams` exposes `application_id`, `include_shared`, `is_active`, and
paging. `TagAliasListParams` exposes `application_id`, `include_shared`, `is_active`, `q` (as
`Query`), `slug`, `tag_id`, and paging.

**`is_active` absent means active rows only.** The server applies that default on the tag, vocabulary,
and alias lists alike, so a nil `IsActive` is not "every row". Since `Delete` is deactivation,
`IsActive: octonomy.Bool(false)` is how you find deleted ones.

### Resolution parameters

`Tags.Resolve` is not a list, so `TagResolveParams` embeds no `ListOptions`. It carries
`ApplicationID`, `Type`, and `Scope`; the required `slug` is a positional argument. `ApplicationID`
both filters and **orders** — with it set, a row in that application outranks a tenant-shared one
carrying the same slug.

`Scope` is typed (`ResolutionScopeGlobal`, `ResolutionScopeMerchant`), not a free string: the spec
describes it as a bare `string` while the server accepts exactly two values. An unset `Scope` is
omitted rather than sent empty.

**`Scope` is absent from the vendored v1 contract, and is still sent on v1.** `openapi.yaml` is
pinned at server `1.0.0`, which predates the parameter (#6 re-vendors it at `3.1.1`). The running
server carries it on *both* surfaces: probed against 3.1.0, `GET /api/v1/tag-resolution` validates it
by name, rejecting `scope=merchant` on a global request and an unknown value with
`Use 'global' or 'merchant'`. Where the vendored spec is stale rather than divergent the server wins,
and gating the parameter to `APIV2` would refuse a call every current deployment supports — the SDK
has no version handshake, so it cannot tell a 1.0-era v1 server from a 3.1 one. Against a server old
enough to predate the parameter it is dropped like any unknown query parameter, which is the exposure
every post-`1.0.0` addition shares.

**`global` is legal here and reserved elsewhere.** As a *scope* it pins the tenant-shared namespace;
as an `X-Namespace-Type` it is refused (see `WithNamespace`). `ResolutionScopeMerchant` resolves
inside the request's own namespace, so the SDK refuses it locally on a request that has none —
`checkScopeCoherence` already carried that guard, and this endpoint is the first to reach it from a
resource method. `ResolutionScopeGlobal` needs no namespace, and from a namespaced request it does
not need `WithIncludeGlobal` either: the server adds the global namespace to the authorized set for
**this route only** when it sees `scope=global`, deliberately not treating the parameter as a general
alias for `include_global`. Passing both anyway is redundant, not contradictory, and is allowed.

**`WithIncludeGlobal` with `ResolutionScopeMerchant` is refused**, though — they ask for opposite
things, and the server resolves the conflict silently in favor of the scope rather than reporting it.
Drop one: omit the option to stay inside the namespace, or omit the merchant scope to let global rows
back in.

**The two alias list routes take different parameter sets, on purpose.** `Aliases.List` takes
`TagAliasListParams` (eight filters); `Tags.ListAliases` takes `TagListAliasesParams`, which carries
only the five the contract documents for `GET /tags/{tag_id}/aliases` — `application_id`,
`include_shared`, `is_active`, and paging. One server function backs both routes, so `q` and `slug`
would in fact be honored on the nested one; exposing them would put this SDK ahead of the published
contract on a route the server is free to narrow, and is the kind of divergence the drift gate (#18)
exists to catch. `tag_id` has no meaning there at all — the path names the tag.

## Assignments

Assignments are the one group where the vendored spec is wrong about **two** response shapes, and
where three request shapes differ from every other resource. All four calls carry their payload in
the body, so `WithApplication` is refused on all of them — each write struct names its own
`ApplicationID`, which is authoritative.

| Call | Shape worth knowing |
| ---- | ------------------- |
| `Create` | Idempotent. Re-assigning returns the existing row with `200` rather than `201`, and is not an error. Name the tag with **exactly one** of `TagID`, `AliasID`, `AliasSlug`. |
| `Remove` | A `DELETE` **carrying a JSON body** — the row has no id route, so the four body fields identify it. Removing what is not there is a `204`. |
| `BulkAssign` | All or nothing; one unknown id fails the whole call. Takes `TagIDs`, `AliasSlugs`, or both. |
| `BulkRemove` | A **`POST`**, not a `DELETE`. Canonical tag ids only — no alias form. Tolerates ids that match nothing. |

`Assignment.ApplicationID` is a plain `string` rather than the `*string` the other models carry: an
assignment is always application-scoped, so there is no tenant-shared case to represent. `Remove`
deletes the row outright — the exception to Octonomy's deactivate-don't-delete rule, since an
assignment is a link and an inactive link is an absent one.

**The two bulk responses are composites, and `openapi-v2.yaml` describes neither correctly.** It
claims `bulk-assign` returns a bare array and documents *no schema at all* for `bulk-remove`. Probed
against a running 3.1.0 server:

```
POST /tag-assignments/bulk-assign
  {"data":{"created":1,"existing":0,"skipped":0,"assignments":[{...}]}}

POST /tag-assignments/bulk-remove
  {"data":{"removed":1}}
```

A `[]Assignment` decoder written from the spec returns an empty slice and a nil error against that
body — #32 in a new place — so both go through `doData` with a result struct, and the envelope
assertion is what catches a mis-routed decode.

**The bulk results require their keys.** `doData` stops at the `data` envelope, which is the right
line for a resource: a zero-valued `Assignment` has an empty `ID`, and nobody reads that as an answer.
A composite of counters is different — `created: 0, existing: 0` with no rows is an ordinary result,
and `removed: 0` is the most common answer bulk remove gives — so a body whose keys the server renamed
would be read as "nothing needed doing" instead of as the contract break it is. A missing `created`,
`existing`, `assignments`, or `removed` is therefore an error. `Skipped` is exempt (it is vestigial),
and a present-but-null `assignments` normalizes to an empty non-nil slice, as `doList` does for a null
page.

`BulkAssignResult.Skipped` is **always zero** on 3.1.x and exists only because the server emits it.
Nothing is skipped because nothing is tolerated: an unknown tag id fails the entire call, and an id
outside the request's namespace is reported identically to one that exists nowhere, so the response
cannot be used to probe for tags the caller may not read. Both bulk calls cap at the deployment's
`MAX_BULK_TAGS`, 200 by default.

## Responses

Every 2xx that carries a payload is wrapped in a `data` envelope. The SDK unwraps it for you; the
wire column is what the server actually sends.

| Call | On the wire | You get |
| ---- | ----------- | ------- |
| Single resource (`Create`/`Get`/`Update`) | `{"data": {...}}` | `*Tag`, `*Vocabulary`, `*TagAlias` |
| Composite (`Tags.Resolve`) | `{"data": {...}}` | `*TagResolution` — a payload, not a resource |
| Composite (`Assignments.BulkAssign`) | `{"data": {"created", "existing", "skipped", "assignments"}}` | `*BulkAssignResult` |
| Composite (`Assignments.BulkRemove`) | `{"data": {"removed"}}` | `*BulkRemoveResult` |
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

### Resolution does not use 404, and splits its ambiguity across two codes

`Tags.Resolve` answers an **unmatched slug with a `400 validation_error`**, not a `404`. The branch
that means "nothing is called that" is `IsValidation`, and `IsNotFound` reports false. A
`ResolutionScopeGlobal` request from a caller without the authority to see global rows returns that
same error, indistinguishable on purpose: distinguishing them would disclose the existence of rows
the caller may not read.

Two matches of equal specificity are refused rather than broken arbitrarily, and the axis that
disambiguates them arrives in `Details` — under **two different codes**, so a caller handling only
one misses half the cases:

| Tie | Code | Helper | `Details` key | Fix |
| --- | ---- | ------ | ------------- | --- |
| Rows in different applications | `ambiguous_resolution` | `IsAmbiguousResolution` | `application_id` | set `TagResolveParams.ApplicationID` |
| Canonical tags of different types | `validation_error` | `IsValidation` | `type` | set `TagResolveParams.Type` |

Both were verified against a running 3.1.0 server rather than read off the spec, which describes
neither.

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

Resource tags, audit logs, and health — see [roadmap.md](roadmap.md).
