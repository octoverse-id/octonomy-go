# Roadmap

The foundation (transport, auth, errors, pagination, API version selection, namespace scoping) and
every endpoint group the vendored contracts publish are implemented.
**[`api.md`](api.md#implemented) holds the only complete inventory** — every SDK method, verb, and
path — and is the one place to update when a method is added. This page names a route only where it
is making some other point (the health section below does). What is left are the gaps *within*
implemented resources, at the bottom of this page.

This page is therefore two things: the **recipe** for adding the next resource the server ships, and
the **register of known gaps**. Neither is a list of what exists.

**Derived from [`openapi-v2.yaml`](openapi-v2.yaml) (server 3.1.1), not from memory.** Every endpoint
and parameter below was enumerated from the vendored v2 spec. **Response shapes are a different
matter** and were verified against a running server: the spec omits both `data` envelopes, describes
the two bulk composites and the resource-tag replace wrongly or not at all, and carries no schema for
the health probes, which are outside the API surface entirely. Where spec and server disagree, the
server wins — see [`api.md`](api.md). The previous revision
of this file was written against server 1.0.0 and had drifted — most visibly, it documented
`Tags.Resolve` as taking `slug` + `application_id` when the endpoint takes four parameters. Since
#8–#13 delegate to this file, that drift would have been copied into six resources. Re-derive rather
than edit if you suspect it has aged again.

## How to add a resource (the recipe)

Copy `tags.go` and `tags_test.go` as the template — or `aliases.go` and `aliases_test.go`, which
were written against the v2-aware transport and cover a nested list route (`Tags.ListAliases`) as
well as the collection — then:

1. Read the matching schema(s) in [`openapi-v2.yaml`](openapi-v2.yaml). Read the **v2** spec, not
   [`openapi.yaml`](openapi.yaml): both are vendored at server 3.1.1, but v1 has no namespace axis,
   so its schemas omit the `namespace_type` / `namespace_id` fields every new resource needs.
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
6. Add a `## [Unreleased]` CHANGELOG entry and add every new method to the inventory table in
   [`api.md`](api.md#implemented) — the one place the complete list is kept.

Scoping is already handled by the transport and needs no per-resource work: `WithNamespace`,
`WithApplication`, and `WithIncludeGlobal` apply to any method, and the guards in
`checkScopeCoherence` cover every resource at the chokepoint.

The queue this section fed is empty; what follows is reference for the next resource the server adds
— the namespace-field register, the one group the recipe does not cover, and the known gaps.

## Namespace fields

Seven v2 response schemas carry `namespace_type` / `namespace_id`, and all seven are implemented:

| Schema | Declared in |
| ------ | ----------- |
| `Tag` | `tags.go` |
| `Vocabulary` | `vocabularies.go` |
| `TagAlias` | `aliases.go` |
| `Assignment` | `assignments.go` |
| `TagResource` | `resources.go` |
| `ResourceTag` | `resources.go` |
| `AuditLog` | `audit.go` |

Six mark both fields `required`; `Assignment` carries them without. A drift check that keys on
`required` will therefore see six, not seven — the runtime emits them on all seven. All seven are now
implemented, so this table is a drift reference rather than a queue.

## Health — implemented, and the one group the recipe does not describe

Unauthenticated liveness/readiness probes (#13).

- `Health.Live` → `GET /health/live`
- `Health.Ready` → `GET /health/ready`

**Outside the API surface in three ways at once**, which is why it needed its own everything: the
routes sit outside `/api/<version>` (the prefix is unconditional in `doRaw`), the body is a bare
`{"status": "ok"}` with **no `data` envelope**, and they are unauthenticated — while `New` requires
both `Token` and `TenantID`, so a caller with no credentials could not construct a client at all in
order to reach an endpoint that needs none.

What landed, and the constraints that shaped it — all four still bind anyone editing `health.go`:

- `NewHealthClient(baseURL, ...HealthOption)` builds a credential-free client. `New`'s validation was
  **not** loosened, and `doData`'s envelope requirement was **not** relaxed: either would re-open #32,
  and the tenant guarantee, for every other resource.
- `doUnversioned` in `transport.go` is a separate request path, not a `skipAuth` flag threaded through
  `doRaw`. `doRaw` already carries the version and scope logic and is over the complexity threshold;
  here the absence of auth is the whole function and cannot be reached by accident.
- Health has its own decoder. A 2xx with no readable `status` is an **error**, never a zero-valued
  `HealthStatus` — the same rule `doData` enforces for the envelope.
- **Unreachable and unready must stay distinguishable.** A 503 carrying the probe's own body is an
  `*APIError` with `not_ready` (`IsNotReady`); a request that got no response matches
  `errors.Is(err, ErrUnreachable)` and produces no `*APIError` at all. Collapsing them loses the
  distinction an operator most needs.

## Known gaps in implemented resources

Each has an issue; none is a missing endpoint group.

| Gap | Issue |
| --- | ----- |
| **`VocabularyListParams` is missing `q` and `slug`.** Both have been on `GET /vocabularies` since server 1.0.0 — not v2 drift but a gap against the contract the SDK already vendored. `TagListParams` has the matching pair and is complete. | [#36](https://github.com/octoverse-id/octonomy-go/issues/36) |
| **The tags-ordering caveats want revisiting** once the server adds an `ORDER BY` to the annotated tags list (upstream `octonomy#162`). | [#49](https://github.com/octoverse-id/octonomy-go/issues/49) |

Deferred by design, not gaps: the webhook typed-event surface and `http.Handler`
([#22](https://github.com/octoverse-id/octonomy-go/issues/22) — no deployment emits webhooks, since
`OUTBOX_TRANSPORT` defaults to `logging`) and the client-side tag-tree helper
([#20](https://github.com/octoverse-id/octonomy-go/issues/20) — needs consumer-defined semantics).

## What is planned that is not a resource

An empty resource queue is not an empty backlog. The
[v2.0.0-alpha.2 milestone](https://github.com/octoverse-id/octonomy-go/milestone/3) is additive work
alongside the client rather than inside it: `octonomy/webhook` with HMAC `Verify` and signature test
vectors ([#16](https://github.com/octoverse-id/octonomy-go/issues/16)), the full integration suite
against the published container ([#17](https://github.com/octoverse-id/octonomy-go/issues/17)), the
OpenAPI contract drift gate that would have caught this documentation's own drift automatically
([#18](https://github.com/octoverse-id/octonomy-go/issues/18)), and a runnable example per resource
group ([#19](https://github.com/octoverse-id/octonomy-go/issues/19)). Cutting
`v2.0.0-alpha.1` itself is [#29](https://github.com/octoverse-id/octonomy-go/issues/29).
