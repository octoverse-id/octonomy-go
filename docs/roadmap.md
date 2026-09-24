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

**Derived from [`openapi-v2.yaml`](openapi-v2.yaml) (server 3.2.1), not from memory.** Every endpoint
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

**Every step is named here**, which was not true before
[#73](https://github.com/octoverse-id/octonomy-go/issues/73): this page carried six steps and left
out five requirements, while [`architecture.md`](architecture.md#extending-the-client) certified it
as carrying the recipe in full. That page and [`CONTRIBUTING.md`](../CONTRIBUTING.md#adding-a-resource)
now link this section instead of keeping shorter copies, and [`AGENTS.md`](../AGENTS.md) carries the
argument behind each rule rather than a second copy of the list. Where the two differ on *why* a rule
holds, `AGENTS.md` is the longer account and wins — see
[the decision](#why-this-page-carries-the-recipe) at the end of this section.

**Nothing checks that this list stays complete**, and saying so is the point. Individual steps have
gates — the contract gate stands behind step 7, a test behind step 9 — but nothing compares this page
against the set of requirements the repository actually has, and the day the server publishes a group
nobody implements, `make contract-check` fails without a word about this page. The table below covers
the five requirements #73 found missing here, which is where the difference matters most.

Copy `tags.go` and `tags_test.go` as the template — or `aliases.go` and `aliases_test.go`, which
were written against the v2-aware transport and cover a nested list route (`Tags.ListAliases`) as
well as the collection — then:

1. Read the matching schema(s) in [`openapi-v2.yaml`](openapi-v2.yaml). Read the **v2** spec, not
   [`openapi.yaml`](openapi.yaml): both are vendored at server 3.2.1, but v1 has no namespace axis,
   so its schemas omit the `namespace_type` / `namespace_id` fields every new resource needs.
2. Create `<resource>.go` with the model struct and **only the write shapes the contract actually
   publishes** — a read-only group such as audit logs has no `*Create` and no `*Update`, and adding
   them would advertise routes the server does not serve. Where they exist: a `*Create` whose
   **required fields are plain values and whose optional fields are omittable** — a pointer for a
   scalar, or a nil-able type like `Metadata` — each tagged `omitempty`, so a create sends exactly
   what the contract requires plus whatever the caller set; a `*Update` with
   **`Optional[T]` + `omitzero` on every field**, so a PATCH can send a null as well as omit a key
   (see [`api.md`](api.md#update-bodies)); `*ListParams` with a `query()` method for a list route;
   and a `*Service` whose methods take
   `context.Context` first and `...RequestOption` last and delegate to the transport helper matching
   each method's **response shape**: `doData[T]` for a single resource (including a composite
   payload), `doList[T]` for a paginated list, `client.do` for a 204 with no body. See the routing
   diagram at the top of `transport.go`. A list method returns `*List[T]` and its `*ListParams`
   embeds `ListOptions`. **Every exported symbol gets a doc comment.**
3. **Give every new model in resource position an `identityFields()` method** (`transport.go`),
   naming the field that identifies its **row** — `id` for most, `assignment_id` on `ResourceTag`,
   `resource_id` on `TagResource` — and only that, never every field the schema documents. Three
   cases, and they are not the same:
   - **A resource** implements it. A **nested** resource counts only where the contract marks it
     `required` *and* the route exists to deliver it — `ResourceTag.Tag` and `TagResolution.Tag`
     both qualify. Read the schema's `required:` list rather than assuming; that is where the first
     draft of this rule got `ResourceTag` wrong.
   - **A composite** (a bulk result, a replace result) has no row identity of its own and requires
     its **keys in `UnmarshalJSON`** instead. `TagResolution` does both, since the tag it exists to
     deliver is a resource.
   - Omitting it does not corrupt a valid payload; it removes the check that catches an invalid one.
     `requireIdentity` type-asserts, so a model that does not implement the interface is skipped
     silently, and `{"data": {"id": null}}` or a renamed id then decodes to a zero-valued resource
     behind a `nil` error — [#40](https://github.com/octoverse-id/octonomy-go/issues/40), the same
     silent-zero family as #32.
4. Put `NamespaceType` / `NamespaceID` (`*string`, decode-only) on every response model listed under
   *Namespace fields* below. They are server-set from the `X-Namespace-*` headers and never accepted
   in a write body, so they belong on the model and **not** on `*Create` / `*Update`.
5. Wire the service onto `Client` in `New()` (`octonomy.go`).
6. Add table-driven `httptest` tests: assert method, path, auth headers, query and body
   server-side (`t.Errorf` inside handlers — they run on another goroutine) and decoded values
   client-side; cover **every response envelope the resource actually returns** — a list-only group such as
   audit logs has just the one — and error decoding (404 → `IsNotFound`,
   409 → `IsConflict`, 400 → `IsValidation`). Single-resource fixtures go through `writeData`, which
   wraps the body in `{"data": {…}}`; `writeJSON` is for bodies meant to go out verbatim — list and
   error envelopes. Add an `Is<Code>` helper for any error code the resource introduces.
7. **Declare each new operation to the contract gate**: a row in
   [`contract-coverage.yaml`](contract-coverage.yaml) and a driver in
   `tools/contractdrift/drivers.go` that calls the method with every documented parameter, property
   and header populated. A row with no driver is an operation nobody exercises; a driver with no row
   is a call nobody declared. See
   [`development.md`](development.md#adding-a-resource-or-adding-to-the-gate) for what the gate does
   with them.
8. **Add an entry to `smokeProbes` for every new response shape** (`integration_test.go`,
   `make smoke`), keyed by the type and calling a client method that decodes it. A unit suite asserts
   the client against fixtures this repository wrote, so it cannot see a fixture-versus-server
   divergence — which is [#32](https://github.com/octoverse-id/octonomy-go/issues/32), where every
   single-resource read decoded to an empty struct and a complete unit suite stayed green. The table
   is ordered and its entries share the rows they create, so a new one goes where its dependencies
   are already in `smokeState` rather than at the end by default.
9. **Add a probe to `readProbes` for every new read method** (`integration_suite_test.go`). That
   table is what makes *a merchant-A client never sees a merchant-B row* a statement about the whole
   read surface rather than about whichever endpoints someone remembered. Each probe also declares
   how its endpoint declines an out-of-scope row — 200 with the row absent, 404 `not_found`, or
   resolution's 400 `validation_error` — because "it errored" is not evidence of isolation. The two
   integration files answer different questions: a new response **shape** goes in step 8's
   `integration_test.go`, and a new claim about **authorization or persistence** goes here.
10. **Add a runnable example under `examples/`** for a new resource group, or extend the existing
    one for a new method on a group that has one. `make examples` compile-checks every program, and
    an example that calls the API is **run against a real server** through `make dev-server` before
    it lands — a comment in an example is documentation a reader will copy, and one the server
    contradicts is worse than no example at all. Each example repeats its own configuration block
    rather than sharing one.
11. Add a `## [Unreleased]` CHANGELOG entry and add every new method to the inventory table in
    [`api.md`](api.md#implemented) — the one place the complete list is kept.

Scoping is already handled by the transport and needs no per-resource work: `WithNamespace`,
`WithGlobalNamespace`, `WithApplication`, and `WithIncludeGlobal` apply to any method, and the guards
in `checkScopeCoherence` cover every resource at the chokepoint.

### Which of these steps anything catches

Steps 3 and 7–9 carry the five requirements this recipe omitted until
[#73](https://github.com/octoverse-id/octonomy-go/issues/73). They are not interchangeable with the
rest: **each exists because of a defect this repository already shipped**, and three of the five were
invisible to every job. The other steps are not listed here — they are enforced by the compiler, by
review, or by nothing, like most of any contributing guide.

| Step | What catches an omission | What you get if you skip it |
| --- | --- | --- |
| 7 — coverage row | `checkInventory`, `tools/contractdrift/checks.go` | **Red CI**: "`<op>` is in the contract and not in `docs/contract-coverage.yaml`" |
| 7 — driver | `checkImplementation`, same file | **Red CI** |
| 3 — `identityFields()` | `TestEveryResponseTypeCanRefuseAnEmptyDecode` (`identityfields_test.go`), added by [#76](https://github.com/octoverse-id/octonomy-go/issues/76) | **Red `make test`**, naming the type and both mechanisms it could carry |
| 8 — smoke assertion | `TestEveryResponseTypeHasASmokeProbe` (`smokeprobes_test.go`), added by [#77](https://github.com/octoverse-id/octonomy-go/issues/77) | **Red `make test`**, naming the response type and the table — and equally for an entry keyed for one shape that calls another |
| 9 — `readProbes` probe | `TestEveryReadMethodHasANamespaceProbe` (`readprobes_test.go`), added by #73 | **Red `make test`**, naming the method and the table — and equally for a probe that names one endpoint and calls another |
| *(not a recipe step)* — a prose claim about the vendored contract | `TestEveryContractVersionMentionIsCurrentOrExempt` (`contractversion_test.go`), added by [#86](https://github.com/octoverse-id/octonomy-go/issues/86) | **Red `make test`**, naming the file, the line and both readings — the claim is stale, or it is a classified exception and needs one |

**The last row is deliberately not a step**, and it is in this table because it has the table's
defining property rather than its shape. Fourteen places in this repository state which server
contract the SDK vendors; `make contract-check` mechanizes three of them (the two specs'
`info.version` and the `<!-- contract-version: -->` marker, all via `checkRecordedVersion`). The rest
were prose enforced by nothing, which is the same condition steps 3, 8 and 9 were in before #76, #77
and #73. [#84](https://github.com/octoverse-id/octonomy-go/issues/84) proved it live: its first pass
moved eight of the fourteen and missed six — `docs/api.md` ×2, `docs/architecture.md`,
`docs/development.md`, `docs/roadmap.md` ×2 — with every gate green. Refreshing a contract is not a
recipe step, so the requirement has nowhere else to live.

**What that guard does not do.** It is syntactic: it checks that an exemption exists and that its
reason is non-empty, never that the reason is *true*, so a wrong exemption passes. It classifies by
the SHAPE of a mention rather than site by site — "probed against 3.1.0" is a verification note by
construction — which means a category written too loosely would exempt a real defect, and each
pattern is anchored to the words that make it the category it claims. It reads two lines of
look-back, because prose wraps and the phrase that classifies a mention is regularly on the line
above the version it qualifies. And it says nothing about the two specs or the marker:
`checkRecordedVersion` owns those three, and giving two gates one job lets each assume the other is
doing it.

Step 9 was the third silent one until #73 gave it a guard. The test resolves each exported service
method's HTTP verb from the source — following a call into a helper, since `Health.Live` names no
verb of its own — and checks it against the `readProbes` entries the table actually returns. It fails
on a read with no probe, a probe naming a method that no longer exists, a probe whose `find` closure
calls a *different* endpoint than its name claims, a duplicate name, and an exclusion that is stale
or has no reason written. **A method whose verb it cannot resolve, and which is neither probed nor
excluded, fails too** rather than passing as "not a read": defaulting the unknown case to silence is
the defect the guard exists to prevent, reproduced inside the guard. A probed one is accepted —
whatever the classifier made of it, a probe bound to the endpoint it calls means the isolation
matrix does exercise it. It runs in `make test` and carries no `integration` build tag,
because a check that ran only when the container did would be absent from exactly the pull request
that adds a read method. `TestUpdateBodiesTagEveryOptionalOmitzero` and
`TestVerifyComparesDigestsInConstantTime` are the precedents for reading this repository's own source
to enforce a rule about it.

**What that guard does not do**, since a check nobody knows the edge of is worse than none: it is
syntactic. It follows calls only within this package, and it reads the `[]readProbe` literal that
`readProbes` returns — restructure the table so the returned value is built by a helper or appended
in a loop and the guard finds nothing and fails, which is the safe direction, but it is a shape
constraint rather than a free-standing proof. It checks that a probe *calls* the endpoint it is named
for; it does not check that the probe asserts anything useful about the result, nor that the call is
reachable rather than parked under a dead branch. A read reached by a shape it does not recognize —
a function variable, a method expression, a closure invoked in place — classifies as unknown and
fails, except where the same method also issues a recognized write, which classifies it. The full
list of edges is in `readprobes_test.go`, next to the code that has them.

**Step 3 was the tractable half and got its guard first**
([#76](https://github.com/octoverse-id/octonomy-go/issues/76)): every type handed to `doData` or
`doList` must either implement `identityFields()` — a resource, naming its row identity — or declare
`UnmarshalJSON` — a composite, requiring its keys instead. Both halves are decidable from the source,
which is what made it possible.

**Step 8 was the last one enforced by nothing, and [#77](https://github.com/octoverse-id/octonomy-go/issues/77)
closed it.** The reason it went last is worth keeping, because it is the reason the obvious check was
refused: the nearest *cheap* proxy — the type name appearing somewhere in `integration_test.go` — is
satisfied by a comment or an unused reference, and a check reporting "covered" on that basis would
have made the step look enforced while leaving #32's class reachable. What replaced it is not that
proxy. The smoke walk is now an executable registry, `smokeProbes`, keyed by response type: its keys
are compared against the response types derived from this package's own source — the *same*
derivation #76's guard uses — and each entry is bound to a client method that really decodes its key.
A comment cannot satisfy that.

**What that guard does not do**, since a check nobody knows the edge of is worse than none: it cannot
prove an assertion is meaningful. An entry that calls its method and discards the result passes. What
it makes impossible is the *silent omission* — a new response type with no entry, and an entry left
behind by a model that no longer exists — which is the failure that actually happens; the assertions
themselves are still a reviewer's job. It reads presence rather than reachability, and it binds an
entry to *a* method that decodes its key rather than to every one. A response type is one handed to
`doData` or `doList`, which is why the health probes are the walk's prologue rather than an entry:
`HealthStatus` is decoded by `health.go`'s own helper and is not a response type by that definition.
The full list of edges is in `smokeprobes_test.go`, next to the code that has them.

**One thing nothing checks, and it is load-bearing**: the *order* of that table. Entries run in slice
order against one server and share the rows they create through `smokeState` — the `Vocabulary` entry
creates the vocabulary the `Tag` entry hangs its tag off, and the `AuditLog` entry reads the history
every entry before it wrote. Reordering it is a behavioural change. The walk fails fast on the first
failed entry rather than carrying on, which is what turns a bad order into one legible failure
instead of a cascade of consequences.

### Why this page carries the recipe

The recipe existed in four places at four levels of completeness — here, a shorter five-step copy in
`architecture.md`, the full set of rules scattered through `AGENTS.md`, and a subset in
`CONTRIBUTING.md` — which is precisely how it came to be **wrong in one of them**: this page
certified as complete by `architecture.md` while omitting five requirements, three of them silent.
#73 consolidated rather than patched, and the two rejected options are worth recording:

- **Adding the five here and keeping all four copies** would have fixed the symptom and left the
  drift surface exactly as wide. The defect was never that someone wrote the list carelessly; it was
  that four lists can disagree and nothing compares them.
- **Deleting the recipe here and pointing at `AGENTS.md` + `development.md`** is the smallest diff,
  but it removes this page's stated first purpose and sends a human contributor to the agent
  instruction file to find out how to add a resource.

`AGENTS.md` deliberately keeps its own statement of every rule. It is not a fourth copy of the list
but the argument behind each item — why `identityFields` exists, why scope belongs to the transport,
which harness token makes an isolation test mean anything — and an agent reading it needs the
reasoning at the point of use, not a link. What it no longer has to be is *the only complete place*,
which is what it was when this page was missing five steps.

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

Each has an issue; none is a missing endpoint group. **The rows are a snapshot, taken 2026-09-18**
— each links the issue that holds its live state, and what a row adds is the reasoning, not the
status.

**There are no rows as of this snapshot**, which is a statement about the table and not about the
code. The section stays because an empty table is the useful form of it: a gap with no holder is the
thing this page exists to prevent, so the next one gets an issue and a row rather than a paragraph.
What the two guards above still cannot see is written where they are — the nested-required-resource
limit in `identityfields_test.go`, the syntactic edges in `smokeprobes_test.go` and
`readprobes_test.go` — because an edge recorded away from the code that has it is the copy that goes
stale first.

Closed since the last snapshot: **the smoke assertion (recipe step 8)**
([#77](https://github.com/octoverse-id/octonomy-go/issues/77)) and **the tags-ordering caveats**
([#49](https://github.com/octoverse-id/octonomy-go/issues/49)). #77 turned the smoke walk into the
`smokeProbes` registry described above, so all five of the safeguards #73 found missing are now
enforced by something that fails; server 3.2.1 added the `ORDER BY` that the annotated tags list
never had (upstream `octonomy#162`), the caveats are now qualified by server version rather than
stated flatly, and `TestIntegration_TagsListPagesInATotalOrder` holds the claim against the pinned
harness.

The webhook typed-event surface and `http.Handler`
([#22](https://github.com/octoverse-id/octonomy-go/issues/22)) were deferred rather than skipped —
when that was decided, no deployment emitted webhooks at all, `OUTBOX_TRANSPORT` defaulting to
`logging`, so they would have been built for a consumer who did not exist, on payload shapes that
could still move. They have since landed; the split, and what the wait bought, is below.

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

**What the wait bought.** [#22](https://github.com/octoverse-id/octonomy-go/issues/22) landed after
it, and two of its design findings could only have been written by reading the emitter rather than
by guessing at it. **Event snapshots are not the REST models** — they omit `usage_count` and the
namespace pair, and an `*.updated` payload carries only the fields that changed, so `octonomy.Tag`
would have decoded a zero value for every field the update did not touch, and a pointer field would
still have collapsed "not changed" into "cleared to null". And **an unknown `event_type` must be
acknowledged with a 200**, because delivery is at-least-once with dead-lettering: an SDK that
errored on a type it did not recognise would make every deployed consumer start dead-lettering the
day the server adds a twelfth one. Both are the kind of thing a typed surface built ahead of an
emitter gets wrong and then cannot change without breaking its callers.

**`Verify` takes `[]byte` and not an `*http.Request`, deliberately.** The HMAC is over the raw
bytes, so a body any middleware, logger, or `json.NewDecoder(r.Body)` read first verifies as empty
or partial — a check that appears to run, always fails, and gets "fixed" by deletion. Bytes cannot
be handed an unread stream, and `ParseEvent` takes the bytes `Verify` accepted for the same reason.
`Handler` is what removes the hazard rather than documenting it: it bounds the body, reads it,
verifies, and only then parses, so no consumer code is given a chance to touch the body first.

**Replay is not prevented and cannot be**: the server sends no timestamp header, so there is no
window to enforce. Signature proves authenticity, not freshness; dedupe on the envelope's stable
`id`, which the outbox's at-least-once delivery makes necessary anyway.

The vectors in [`webhook/testdata/`](../webhook/testdata/README.md) are the durable artifact. They
are generated from the server's own signing code rather than from this package, independently
confirmed against `openssl`, and free of anything Go-specific or payload-specific, so another
language's SDK can drive its verifier from the same file instead of re-deriving the contract from
Python.
