# Roadmap

The foundation (transport, auth, errors, pagination, API version selection, namespace scoping) and
the **Vocabularies** and **Tags** resources are implemented. The resources below are queued. Each is
a self-contained unit that follows the established pattern.

**Derived from [`openapi-v2.yaml`](openapi-v2.yaml) (server 3.1.1), not from memory.** Every endpoint,
parameter, and response shape below was enumerated from the vendored v2 spec. The previous revision
of this file was written against server 1.0.0 and had drifted — most visibly, it documented
`Tags.Resolve` as taking `slug` + `application_id` when the endpoint takes four parameters. Since
#8–#13 delegate to this file, that drift would have been copied into six resources. Re-derive rather
than edit if you suspect it has aged again.

## How to add a resource (the recipe)

Copy `tags.go` and `tags_test.go` as the template, then:

1. Read the matching schema(s) in [`openapi-v2.yaml`](openapi-v2.yaml). Read the **v2** spec, not
   [`openapi.yaml`](openapi.yaml): v1 is still vendored at server 1.0.0 until #6 refreshes it.
2. Create `<resource>.go` with: the model struct, `*Create`/`*Update` write structs (pointer +
   `omitempty`), `*ListParams` with a `query()` method, and a `*Service` whose methods take
   `context.Context` first and `...RequestOption` last and delegate to the transport helper matching
   each method's **response shape**: `doData[T]` for a single resource (including a composite
   payload), `doList[T]` for a paginated list, `client.do` for a 204 with no body. See the routing
   diagram at the top of `transport.go`.
3. Put `NamespaceType` / `NamespaceID` (`*string`, decode-only) on every response model listed under
   *Namespace fields* below. They are server-set from the `X-Namespace-*` headers and never accepted
   in a write body, so they belong on the model and **not** on `*Create` / `*Update`.
4. Wire the service onto `Client` in `New()` (`octonomy.go`).
5. Add table-driven `httptest` tests (assert method/path/headers/query/body server-side; assert
   decoded values client-side; cover the error envelope).
6. Add a `## [Unreleased]` CHANGELOG entry and update [`api.md`](api.md).

Scoping is already handled by the transport and needs no per-resource work: `WithNamespace`,
`WithApplication`, and `WithIncludeGlobal` apply to any method, and the guards in
`checkScopeCoherence` cover every resource at the chokepoint.

Each item below is a good single GitHub issue (`feature/<n>-<slug>`).

## Namespace fields

Seven v2 response schemas carry `namespace_type` / `namespace_id`. Two are implemented; the rest
arrive with their resource:

| Schema | Owner | Status |
| ------ | ----- | ------ |
| `Tag` | implemented | ✅ `tags.go` |
| `Vocabulary` | implemented | ✅ `vocabularies.go` |
| `TagAlias` | #8 | pending |
| `Assignment` | #10 | pending |
| `TagResource` | #10 / #11 | pending |
| `ResourceTag` | #11 | pending |
| `AuditLog` | #12 | pending |

Six mark both fields `required`; `Assignment` carries them without. A drift check that keys on
`required` will therefore see six, not seven — the runtime emits them on all seven.

## Tag aliases

Alternate identifiers that resolve to a canonical tag. Schemas: `TagAlias`, `TagAliasWrite`,
`PatchedTagAliasPatch`.

- `Aliases.Create` → `POST /tag-aliases` — body `TagAliasWrite` → `201 TagAlias`
- `Aliases.Get` → `GET /tag-aliases/{alias_id}` → `200 TagAlias`
- `Aliases.List` → `GET /tag-aliases` → `200` list of `TagAlias`
  - query: `application_id`, `include_shared`, `is_active`, `limit`, `offset`, `q`, `slug`, `tag_id`
- `Aliases.Update` → `PATCH /tag-aliases/{alias_id}` — body `PatchedTagAliasPatch` → `200 TagAlias`
- `Aliases.Delete` → `DELETE /tag-aliases/{alias_id}` → `204`
- `Tags.ListAliases` → `GET /tags/{tag_id}/aliases` → `200` list of `TagAlias`
  - query: `application_id`, `include_shared`, `is_active`, `limit`, `offset`

## Tag resolution

Resolve a slug (optionally within an application and scope) to a tag, possibly via an alias. Schema:
`TagResolution`.

- `Tags.Resolve` → `GET /tag-resolution` → `200 TagResolution` (`{matched_type, matched_alias, tag}`)
  - query: `slug`, `application_id`, `type`, **`scope`**

`scope` is the parameter v2 added here, and it is the one place in the SDK where the literal `global`
is **legal**: `scope=global` explicitly pins the tenant-shared namespace, while `global` as an
`X-Namespace-Type` is reserved and rejected. `scope=merchant` requires a namespaced request; the
transport already refuses the combination locally, so the resource layer does not repeat the check.
Note the server types `scope` as a bare string in the spec while treating it as an enum
(`global` | `merchant`).

## Tag assignments

Link tags to external resources; idempotent writes (re-assigning returns 200, not 201). Schemas:
`Assignment`, `AssignmentWrite`, `BulkAssign`, `BulkRemove`.

- `Assignments.Create` → `POST /tag-assignments` — body `AssignmentWrite` → `200 Assignment`
- `Assignments.Remove` → `DELETE /tag-assignments` → `204`
- `Assignments.BulkAssign` → `POST /tag-assignments/bulk-assign` — body `BulkAssign`
- `Assignments.BulkRemove` → `POST /tag-assignments/bulk-remove` — body `BulkRemove`

Assignment writes accept `tag_id`, `alias_id`, or `alias_slug`. Watch for `application_mismatch` and
`inactive_tag` (`IsApplicationMismatch`, `IsInactiveTag`).

**The bulk responses are the spec's least trustworthy corner.** The spec claims a bare array for
`bulk-assign` and documents no response schema at all for `bulk-remove`. The server really returns a
composite under `data`: `{"data": {"created": N, "existing": N, "skipped": N, "assignments": [...]}}`
and `{"data": {"removed": N}}`. Use `doData[T]` with a composite result struct — a bare-array decoder
against that body yields an empty slice and a nil error, which is #32 again.

## Resource tags

Read or replace the full tag set on a resource. Schemas: `ResourceTag`, `ResourceReplace`,
`TagResource`.

- `Resources.ListTags` → `GET /resources/{resource_type}/{resource_id}/tags` → `200` list of `ResourceTag`
  - query: `application_id`, `include_inactive`, `limit`, `offset`, `type`
- `Resources.ReplaceTags` → `POST /resources/{resource_type}/{resource_id}/tags` — body `ResourceReplace`
  - composite response under `data`, same caveat as the bulk endpoints above
- `Tags.ListResources` → `GET /tags/{tag_id}/resources` → `200` list of `TagResource`
  - query: `application_id`, `limit`, `offset`, `resource_type`

Note `include_inactive` here, versus `is_active` on the tag and alias lists — they are different
parameters with different shapes, not two spellings of one filter.

## Audit logs

Append-only mutation history (needs the `audit:read` scope). Schema: `AuditLog`. **List-only — there
is no `Get`.**

- `AuditLogs.List` → `GET /audit-logs` → `200` list of `AuditLog`
  - query: `action`, `actor_id`, `application_id`, `entity_id`, `entity_type`, `limit`, `offset`,
    `operation_id`, `resource_id`, `resource_type`, `tag_id`
- `Tags.ListAuditLogs` → `GET /tags/{tag_id}/audit-logs` → `200` list of `AuditLog`
  - query: `action`, `actor_id`, `application_id`, `limit`, `offset`, `operation_id`
- `Resources.ListAuditLogs` → `GET /resources/{resource_type}/{resource_id}/audit-logs` → `200` list of `AuditLog`
  - query: `action`, `actor_id`, `application_id`, `limit`, `offset`, `operation_id`

## Health

Unauthenticated liveness/readiness probes.

- `Health.Live` → `GET /health/live`
- `Health.Ready` → `GET /health/ready`

**Outside the API surface in three ways at once**, so it does not fit the recipe above: the routes sit
outside `/api/<version>` (the prefix is unconditional in `doRaw`), the body is a bare `{"status":
"ok"}` with **no `data` envelope**, and they are unauthenticated — while `New` requires both `Token`
and `TenantID`. #13 needs its own request path, its own decoder, and the credential-free constructor
its title names. Do **not** loosen `doData`'s envelope requirement or `New`'s validation to make
health fit: that re-opens #32, and the tenant guarantee, for every other resource.

## Known gaps in implemented resources

- **`VocabularyListParams` is missing `q` and `slug`.** Both have been on `GET /vocabularies` since
  server 1.0.0 — this is not v2 drift but a gap against the contract the SDK already vendored.
  `TagListParams` has the matching pair and is complete. Not fixed under #7, which owns the version
  and namespace axes; it wants its own issue.
