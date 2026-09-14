# Roadmap

The foundation (transport, auth, errors, pagination, API version selection, namespace scoping) and
every endpoint group the vendored contracts publish are implemented.
**[`api.md`](api.md#implemented) holds the only complete inventory** — every SDK method, verb, and
path — and is the one place to update when a method is added. This page names a route only where it
is making some other point (the health section below does). What is left are the gaps *within*
implemented resources, registered below.

This page is therefore three things: the **recipe** for adding the next resource the server ships,
the **register of known gaps**, and the **reasoning** behind decisions the issue tracker records but
cannot explain. None of them is a status board; see
[the rule](#work-alongside-the-client-rather-than-inside-it) at the end of this page.

**Derived from [`openapi-v2.yaml`](openapi-v2.yaml) (server 3.2.0), not from memory.** Every endpoint
and parameter below was enumerated from the vendored v2 spec. **Response shapes are a different
matter** and were verified against a running server: the spec omits both `data` envelopes, describes
the two bulk composites and the resource-tag replace wrongly or not at all, and carries no schema for
the health probes, which are outside the API surface entirely. Where spec and server disagree, the
server wins — see [`api.md`](api.md). The previous revision
of this file was written against server 1.0.0 and had drifted — most visibly, it documented
`Tags.Resolve` as taking `slug` + `application_id` when the endpoint takes four parameters. #8–#13
delegated to this file, so that drift would have been copied into six resources. Re-derive rather
than edit if you suspect it has aged again.

## How to add a resource (the recipe)

Copy `tags.go` and `tags_test.go` as the template — or `aliases.go` and `aliases_test.go`, which
were written against the v2-aware transport and cover a nested list route (`Tags.ListAliases`) as
well as the collection — then:

1. Read the matching schema(s) in [`openapi-v2.yaml`](openapi-v2.yaml). Read the **v2** spec, not
   [`openapi.yaml`](openapi.yaml): both are vendored at server 3.2.0, but v1 has no namespace axis,
   so its schemas omit the `namespace_type` / `namespace_id` fields every new resource needs.
2. Create `<resource>.go` with: the model struct, a `*Create` write struct (pointer + `omitempty`), a
   `*Update` write struct (**`Optional[T]` + `omitzero` on every field**, so a PATCH can send a null
   as well as omit a key — see [`api.md`](api.md#update-bodies)), `*ListParams` with a `query()`
   method, and a `*Service` whose methods take
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
— the namespace-field register, the one group the recipe does not cover, the one helper that needed
answers before it needed code, and the known gaps.

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

## `BuildTagTree` — implemented, and the four questions it had to answer first

The client-side tag-tree helper ([#20](https://github.com/octoverse-id/octonomy-go/issues/20))
shipped in `tagtree.go` with `v2.0.0-alpha.2`. It keeps a section here, rather than only a row in
[`api.md`](api.md#implemented), because what it lacked was answers and not code: it wanted
consumer-defined semantics — four questions (an absent parent, inactive rows, a cycle, a depth
limit) with no answer that suits everybody — and it was the one expansion candidate with *no
grounding in a server contract*. What unblocked it was finding that grounding: the server answers
three of the four, and the fourth is a filter the caller already applies when fetching. Both load-
bearing answers are pinned by the integration suite rather than asserted —
`TestIntegration_DeactivatedParentOrphansItsLiveChildren` and
`TestIntegration_ParentCycleIsReachableAndRefused` — because each is a property of the server's
persistence that no fixture can settle:

- **An absent parent is ordinary, so its tag is promoted to a root and named in `Orphans`, never
  dropped.** `deactivate_tag` cascades to the tag's *aliases* and never to its children, and
  `filter_tags` applies `is_active=True` when the parameter is absent, so a live child of a
  deactivated parent comes back from the default list alone. Namespace scope and any filter or page
  produce the same shape.
- **Inactive tags are kept.** The server permits an *active* tag under an *inactive* parent, so
  pruning during assembly would orphan live children. Pruning is `TagListParams.IsActive`, applied
  when you fetch.
- **A cycle is refused with `ErrTagCycle`.** The database forbids only `parent_id = id`
  (`tag_parent_cannot_be_self`) and `validate_tag_parent` never walks the ancestry, so `A -> B -> A`
  is two ordinary PATCHes — verified against a running 3.2.0, which answers `200` to the one that
  closes the ring. Every tag in a cycle has a parent inside the set, so a naive assembler returns a
  tree silently missing rows.
- **Depth is reported, not limited** — a rendering decision that stays with the caller.

## Known gaps in implemented resources

Each has an issue; none is a missing endpoint group. **The rows are a snapshot, taken 2026-09-14**
— each links the issue that holds its live state, and what a row adds is the reasoning, not the
status.

| Gap | Issue |
| --- | ----- |
| **The tags-ordering caveats want revisiting** once the server adds an `ORDER BY` to the annotated tags list (upstream `octonomy#162`). | [#49](https://github.com/octoverse-id/octonomy-go/issues/49) |

Deferred by design, not a gap: the webhook typed-event surface and `http.Handler`
([#22](https://github.com/octoverse-id/octonomy-go/issues/22) — when that was decided, no deployment
emitted webhooks at all, `OUTBOX_TRANSPORT` defaulting to `logging`, so those would have been built
for a consumer who did not exist, on payload shapes that could still move). The *verification* half
of webhooks did not wait on an emitter and has shipped — see below.

## Work alongside the client rather than inside it

An empty resource queue is not an empty backlog. Four pieces of this repository grew next to the
client instead of in it, and all four landed in `v2.0.0-alpha.2`: the OpenAPI contract drift gate
([#18](https://github.com/octoverse-id/octonomy-go/issues/18)), the full integration suite against
the published container ([#17](https://github.com/octoverse-id/octonomy-go/issues/17)),
`octonomy/webhook` ([#16](https://github.com/octoverse-id/octonomy-go/issues/16)), and a runnable
example per resource group with `make dev-server` behind it
([#19](https://github.com/octoverse-id/octonomy-go/issues/19)). They were the whole of the
[v2.0.0-alpha.2 milestone](https://github.com/octoverse-id/octonomy-go/milestone/3), which closed
with that release on 2026-09-13. Cutting `v2.0.0-alpha.1` before it was
[#29](https://github.com/octoverse-id/octonomy-go/issues/29).

**What is open right now is a question for the tracker, and this page has stopped answering it** —
[open issues](https://github.com/octoverse-id/octonomy-go/issues) and
[open milestones](https://github.com/octoverse-id/octonomy-go/milestones?state=open) answer it live.
Until [#68](https://github.com/octoverse-id/octonomy-go/issues/68) this paragraph kept a
hand-maintained *Landed / Still open* split, and the open half was false within hours of #19
closing; `make contract-check` compares the client to the contract, never the docs to the tracker,
so a reader was the only gate that sentence ever had. **The rule that replaced it: prose states what
happened, and for what is true now it names whatever keeps it true.** Three cases, and this page
uses all three. What is already past — a closed issue, a shipped release, a decision taken — stays
prose, which is why the paragraph above names five issues and the two releases they belong to. What
the tracker holds is a link, because it moves with no edit here. What a gate holds may stay prose:
*the resource queue is empty*, near the top, cannot go **quietly** stale, since a newly published
operation fails `make contract-check` until it has either a Go method or a written reason in
[`contract-coverage.yaml`](contract-coverage.yaml). That sentence can still age — a written reason
is a gap — but only inside the pull request already being made to account for the operation that
aged it, which is where someone is looking. Anything left over is dated, as the gaps table above is.

### `octonomy/webhook` — why the verifier shipped without the typed events

[#16](https://github.com/octoverse-id/octonomy-go/issues/16): `Verify(secret, signatureHeader
string, body []byte) error`, its distinct refusals, and portable signature vectors. One-way
dependency: it may import the root package, and the root never imports it.

**It shipped ahead of the typed events it would normally come with, and the split is the design.**
Verification is the half that is dangerous to get wrong — a `==` instead of `hmac.Equal` leaks
timing, a body parsed before it is verified acts on unverified data, and **a broken check still
returns 200**, so nothing ever reports it — and it is the half that stays correct no matter how
payloads evolve, because it depends on the signature contract rather than on any event shape. The
typed half is the opposite on both counts, which is why it was deferred to
[#22](https://github.com/octoverse-id/octonomy-go/issues/22).

**`Verify` takes `[]byte` and not an `*http.Request`, deliberately.** The HMAC is over the raw
bytes, so a body any middleware, logger, or `json.NewDecoder(r.Body)` read first verifies as empty
or partial — a check that appears to run, always fails, and gets "fixed" by deletion. Bytes cannot
be handed an unread stream. Since no `http.Handler` ships, bounding the body with
`http.MaxBytesReader` is the caller's job and the package says so.

**Replay is not prevented and cannot be**: the server sends no timestamp header, so there is no
window to enforce. Signature proves authenticity, not freshness; dedupe on the envelope's stable
`id`, which the outbox's at-least-once delivery makes necessary anyway.

The vectors in [`webhook/testdata/`](../webhook/testdata/README.md) are the durable artifact. They
are generated from the server's own signing code rather than from this package, independently
confirmed against `openssl`, and free of anything Go-specific or payload-specific, so another
language's SDK can drive its verifier from the same file instead of re-deriving the contract from
Python.
