# Roadmap

The foundation (transport, auth, errors, pagination, API version selection, namespace scoping) and
the **Vocabularies**, **Tags**, **Tag aliases**, **Tag resolution**, **Tag assignments**,
**Resource tags**, and **Audit logs** resources are implemented. **Health** is the one group left,
and it is the one that does not fit the recipe.

**Derived from [`openapi-v2.yaml`](openapi-v2.yaml) (server 3.1.1), not from memory.** Every endpoint,
parameter, and response shape below was enumerated from the vendored v2 spec. The previous revision
of this file was written against server 1.0.0 and had drifted — most visibly, it documented
`Tags.Resolve` as taking `slug` + `application_id` when the endpoint takes four parameters. Since
#8–#13 delegate to this file, that drift would have been copied into six resources. Re-derive rather
than edit if you suspect it has aged again.

## How to add a resource (the recipe)

Copy `tags.go` and `tags_test.go` as the template — or `aliases.go` and `aliases_test.go`, which
were written against the v2-aware transport and cover a nested list route (`Tags.ListAliases`) as
well as the collection — then:

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
| `TagAlias` | implemented | ✅ `aliases.go` |
| `Assignment` | implemented | ✅ `assignments.go` |
| `TagResource` | implemented | ✅ `resources.go` |
| `ResourceTag` | implemented | ✅ `resources.go` |
| `AuditLog` | implemented | ✅ `audit.go` |

Six mark both fields `required`; `Assignment` carries them without. A drift check that keys on
`required` will therefore see six, not seven — the runtime emits them on all seven. All seven are now
implemented, so this table is a drift reference rather than a queue.

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
