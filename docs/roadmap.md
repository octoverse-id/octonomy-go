# Roadmap

The foundation (transport, auth, errors, pagination) and the **Vocabularies** and **Tags** resources
are implemented. The resources below are queued for future work. Each is a self-contained unit that
follows the established pattern.

## How to add a resource (the recipe)

Copy `tags.go` and `tags_test.go` as the template, then:

1. Read the matching schema(s) in [`openapi.yaml`](openapi.yaml).
2. Create `<resource>.go` with: the model struct, `*Create`/`*Update` write structs (pointer +
   `omitempty`), `*ListParams` with a `query()` method, and a `*Service` whose methods take
   `context.Context` first and `...RequestOption` last and delegate to the transport helper matching
   each method's **response shape**: `doData[T]` for a single resource (including a composite
   payload), `doList[T]` for a paginated list, `client.do` for a 204 with no body. See the routing
   diagram at the top of `transport.go`.
3. Wire the service onto `Client` in `New()` (`octonomy.go`).
4. Add table-driven `httptest` tests (assert method/path/headers/query/body server-side; assert decoded
   values client-side; cover the error envelope).
5. Add a `## [Unreleased]` CHANGELOG entry and update [`api.md`](api.md).

Each item below is a good single GitHub issue (`feature/<n>-<slug>`).

## Tag aliases

Alternate identifiers that resolve to a canonical tag. Schemas: `TagAlias`, `TagAliasWrite`,
`PatchedTagAliasPatch`.

- `Aliases.Create` → POST `/tag-aliases`
- `Aliases.Get` → GET `/tag-aliases/{alias_id}`
- `Aliases.List` → GET `/tag-aliases`
- `Aliases.Update` → PATCH `/tag-aliases/{alias_id}`
- `Aliases.Delete` → DELETE `/tag-aliases/{alias_id}`
- `Tags.ListAliases` → GET `/tags/{tag_id}/aliases`

## Tag resolution

Resolve a slug (optionally within an application) to a tag, possibly via an alias. Schema:
`TagResolution`.

- `Tags.Resolve(ctx, slug, ...)` → GET `/tag-resolution?slug={slug}&application_id={app}` returning
  `{matched_type, matched_alias, tag}`.

## Tag assignments

Link tags to external resources; idempotent writes (re-assigning returns 200, not 201). Schemas:
`Assignment`, `AssignmentWrite`, `BulkAssign`, `BulkRemove`.

- `Assignments.Create` → POST `/tag-assignments`
- `Assignments.Remove` → DELETE `/tag-assignments`
- `Assignments.BulkAssign` → POST `/tag-assignments/bulk-assign`
- `Assignments.BulkRemove` → POST `/tag-assignments/bulk-remove`

Note: assignment writes accept `tag_id`, `alias_id`, or `alias_slug`. Watch for the
`application_mismatch` and `inactive_tag` error codes (constants already defined in `errors.go`).

## Resource tags

Read or replace the full tag set on a resource. Schemas: `ResourceTag`, `ResourceReplace`,
`TagResource`.

- `Resources.ListTags` → GET `/resources/{resource_type}/{resource_id}/tags`
- `Resources.ReplaceTags` → POST `/resources/{resource_type}/{resource_id}/tags`
- `Tags.ListResources` → GET `/tags/{tag_id}/resources`

## Audit logs

Append-only mutation history (needs the `audit:read` scope). Schema: `AuditLog`.

- `AuditLogs.List` → GET `/audit-logs` (with filters)
- `Tags.ListAuditLogs` → GET `/tags/{tag_id}/audit-logs`
- `Resources.ListAuditLogs` → GET `/resources/{resource_type}/{resource_id}/audit-logs`

## Health

Unauthenticated liveness/readiness probes. These live **outside** `/api/v1`, so they need a small
transport variant (or a dedicated method) that skips the prefix and auth headers.

- `Health.Live` → GET `/health/live`
- `Health.Ready` → GET `/health/ready`
