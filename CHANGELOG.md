# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- **`octonomy/webhook` — HMAC signature verification and portable test vectors**
  ([#16](https://github.com/octoverse-id/octonomy-go/issues/16)). A new package with one function:
  `Verify(secret, signatureHeader string, body []byte) error`. Standard library only, like the rest
  of the SDK, and a one-way dependency — it may import the root package, and the root never imports
  it. No change to any existing exported API.
  - **It ships without the typed events it would normally come with, and the split is the point.**
    No Octonomy deployment emits webhooks today — `OUTBOX_TRANSPORT` defaults to `logging` — so
    typed envelopes, 11 event types, and an `http.Handler` would be built for a consumer who does
    not exist, on payload shapes that may still be refined
    ([#22](https://github.com/octoverse-id/octonomy-go/issues/22) tracks them). Verification is the
    opposite case: it depends on the signature contract rather than on any payload, so it is correct
    now and stays correct, and it is the half where a mistake is both easy and silent — comparing
    digests with `==` leaks timing, parsing before verifying acts on unverified data, and **a broken
    check still answers 200**, so nothing ever reports it. Roughly thirty correct lines here beat
    every consumer deriving them from the server's Python later.
  - **`Verify` takes `[]byte`, never an `*http.Request`.** The HMAC covers the raw bytes, so a body
    that any middleware, logger, or `json.NewDecoder(r.Body)` read first is verified as empty or
    partial: a check that appears to run, always fails, and gets "fixed" by deleting it. Accepting
    bytes the caller has already read makes handing it an unread stream structurally impossible.
    Zero bytes are refused as `ErrEmptyBody` — a *policy* refusal, since HMAC of the empty message
    is perfectly well defined — so the drained-stream case names itself instead of surfacing as a
    permanent mismatch. An empty secret is refused as `ErrNoSecret` for the same reason: HMAC under
    an empty key verifies, and the key is then one every attacker also has. A secret the RUNTIME
    refuses is refused as `ErrUnusableSecret`: under `GODEBUG=fips140=only`, `crypto/hmac.New`
    **panics** for a key under 112 bits, which would take a panic out of a library that promises
    never to raise one — inside an HTTP handler, where it becomes a 500 and a stack trace rather
    than a diagnosable error. The `recover` is scoped to that single call, and the refusal is the
    runtime's own, not a minimum-length policy this SDK invented: outside that mode a short secret
    verifies genuine deliveries perfectly well. A subprocess test proves it, since `fips140` is read
    once at startup.
  - **Digests are compared with `hmac.Equal`, over the decoded bytes.** Never `==`, never on the hex
    text. A test parses `verify.go` and fails the build on `bytes.Equal`, `reflect.DeepEqual`, or an
    `==` that touches a digest, because the wrong comparison passes every functional test while
    leaking how far a forgery got — the defect is invisible to behavior, so it is checked in the
    source. Six distinct errors (`ErrNoSecret`, `ErrMissingSignature`, `ErrUnsupportedAlgorithm`,
    `ErrMalformedSignature`, `ErrSignatureMismatch`, `ErrEmptyBody`), none of which can be mistaken
    for another or for success: a verifier whose failures are indistinguishable cannot say whether
    it is misconfigured or under attack.
  - **The signature vectors are the durable artifact**
    ([`webhook/testdata/signature_vectors.json`](webhook/testdata/signature_vectors.json)): fixed
    secrets, fixed bodies, and the correct digests, plus sixteen deliveries that must be rejected
    with a language-neutral reason for each. They are generated from the server's own signing code
    rather than from this package — a vector computed by the implementation under test proves only
    that it agrees with itself — and every one was independently confirmed against `openssl`. They
    carry nothing Go-specific and nothing payload-specific, so **an SDK in any language can drive
    its verifier from the same file** rather than re-deriving the contract from Python, and they
    stay valid as event payloads evolve.
  - **Replay is not prevented, and this package cannot prevent it.** The server sends no timestamp
    header, so there is no signed freshness claim and no window to enforce; the outbox is
    at-least-once and redelivers on its own besides. The package documents that, points callers at
    the envelope's stable `id` for dedupe, and documents that bounding the body with
    `http.MaxBytesReader` is the caller's job — since no handler ships, nothing else will do it.
- **A contract drift gate** ([#18](https://github.com/octoverse-id/octonomy-go/issues/18)). Nothing
  told this SDK when the Octonomy server's contract moved — it sat on a server 1.0.0 contract while
  the server shipped 3.1.0 and made a second API surface primary, and the gap was found by reading
  the server's repository rather than by any mechanism. `tools/contractdrift` is that mechanism. No
  change to the SDK's exported API, and no new dependency for consumers: the gate is its own Go
  module, so the YAML parser it needs is invisible to `go build ./...`, to `go.sum`, and to anything
  a consumer resolves.
  - **It compares schemas and parameters, not just paths.** A path-to-method inventory reports the
    drift that prompted this as green: what actually changed was query parameters, error codes,
    response schemas, and the arrival of a second surface. The gate compares the contract version,
    operations, per-operation parameters (by location *and* name), responses and request bodies,
    `components.schemas` property by property, and — from the server's `core/errors.py`, because
    `ErrorResponse` types `code` as a bare string and a schema comparison therefore cannot see it —
    the error-code registry against this SDK's `Code*` constants, as a set in both directions **and**
    as declarations, since a set cannot see two constants whose values are swapped.
  - **The SDK side is driven, not read.** For each operation the gate calls the method with every
    parameter populated, against a stub that records the request and answers with a body
    **synthesized from the vendored schema**. The request is what it compares against the contract —
    route, query parameters, headers, and request-body properties, **names and values**, in both
    directions, on **both REST surfaces**. Values are comparable because every scalar and array input has
    a canonical value per execution that the wire must carry exactly — proving those executions
    rather than arbitrary propagation, a boundary `docs/development.md` states — so a parameter retyped in the contract, a params struct wiring
    one input to another's name, two JSON tags swapped on a write model, the two namespace headers
    crossed, a value hard-coded to what one execution expects, two swapped integers or booleans, an
    emptied array and a wrong credential are each reported — every one of which keeps all the right names in place. The response is what it decodes,
    twice: once with every property populated and once with every `nullable` property null, so a field
    the model lacks, a type it cannot read, and a nullable state it cannot hold are all reported. And
    because a JSON round trip structurally cannot see two response tags swapped — the same tags decode
    and re-encode — each model's Go field name is checked against the property it decodes. One extra
    drive per surface answers a **409** with an envelope built from `ErrorResponse` — the most
    referenced schema in either contract, and for a while the one nothing exercised, since the stub
    only ever answered `200` or `204` — so a renamed `error.code` is reported, instead of silently
    turning every `IsNotFound`, `IsConflict` and `IsValidation` into `false`. **This found a real gap on its first run** — `VocabularyListParams` is missing `q` and `slug`
    ([#36](https://github.com/octoverse-id/octonomy-go/issues/36)). An earlier draft read the package
    statically instead and was replaced: inferring control flow from an AST answered *clean* for a
    method that stopped passing its query builder, one that branched between two private helpers, and
    a schema property whose type changed.
  - **Two halves, only one of which gates a pull request.** The offline half (`make contract-check`,
    CI job `contract inventory`) compares the *vendored* contracts against this repository and fails
    the PR; it catches a contract refresh that landed without the follow-through, which the
    cross-repository half structurally cannot see, since after a refresh both its sides are the same
    file. The cross-repository half (`make contract-drift`) runs **weekly** and never gates a merge —
    a job that reaches into another repository can go red for reasons unrelated to the change under
    review.
  - **`docs/contract-coverage.yaml`** is the new inventory: every published operation, each naming
    the Go method that implements it or carrying a written reason it does not. An operation missing
    from it fails the gate, which is what turns "not implemented" into a decision rather than an
    oversight. It also **vendors the server's error registry**, the way the two OpenAPI documents are
    vendored and for the same reason — the codes are in neither contract, so without a copy here they
    could only ever be checked with the network, leaving them the one item where "refresh now,
    implement later" was still possible.
  - **The spec-versus-server envelope divergence is recorded, not suppressed.** Each row carries what
    the spec documents and what the server really returns. The documented shape is checked against the
    spec, and the recorded one is what the gate's stub answers with — so a row naming the wrong
    envelope hands the real client a shape it cannot decode. The gate stays quiet while the divergence
    holds and speaks up the day it ends.
  - `docs/versioning.md` gained a `<!-- contract-version: X.Y.Z -->` marker so the contract version in
    prose can be checked against the vendored specs.

- **A full integration suite against the published container**
  ([#17](https://github.com/octoverse-id/octonomy-go/issues/17)). Behind the same `integration` build
  tag as the smoke test, so `go test ./...` is unchanged — still fast, still offline, still hermetic.
  No exported API moves. `make test-integration` runs it against a harness booted by
  `make dev-server`; CI runs it in the new **`integration suite`** job, which is a **required check**
  on `main` — promoted on introduction, because the property it guards is the one whose regression is
  least likely to be caught anywhere else, and it boots the same container the already-blocking smoke
  job does, so it adds little flake risk of its own.

  The smoke test asks whether the server's PAYLOAD matches what this SDK decodes, which is #32's
  class of defect. This suite asks the next question down: whether the server BEHAVES the way our doc
  comments claim. Those are properties of authorization and persistence, and a fake answers whatever
  its fixture says, so none of them was previously checked anywhere.
  - **Namespace isolation, across every read method the SDK exposes.** Thirteen endpoints, each
    probed six times against two seeded merchants. Three runs must find the row — without them
    "merchant A cannot see merchant B" also passes when merchant B was never written — and three
    must not, covering three *different mechanisms*, each of which has to hold alone: the namespace
    filter under a merchant token; the filter alone under a wildcard token, which is authorized for
    both merchants so nothing refuses it; and authorization, where merchant A **asks for** merchant
    B and is refused 403 before any queryset runs. The third is the request an attacker actually
    makes and is not implied by the others — a token reading its own namespace exercises the filter
    whatever the permission layer does — so a suite with only the filter runs stays green through a
    permission regression on any individual route. Both directions verified to fail by mutation.
  - **An error is not evidence of isolation unless it is the right error, from the right route.** The
    SDK turns every non-2xx into an `*APIError` by design, so a bare "did it error?" check would read
    a crashed container's 500, a proxy 502 and an unrouted HTML 404 as a successful boundary. Nor is
    a shared allowlist enough: each endpoint declares how *it* declines an out-of-namespace row — a
    200 with the row absent, a 404 `not_found`, or resolution's 400 `validation_error` — and both
    status and code are asserted against that, so a list route that began answering 400, or an object
    lookup answering 409, is a failure rather than a pass. The authorization runs require a 403
    `forbidden` specifically.
  - **`include_global` is fail-closed, proved with a token that has no global authority — and proved
    on every read endpoint, not one.** The option widens what a request ASKS for; whether global rows
    come back depends on the grant. A merchant token that asks for them gets a 200, its own rows, and
    nothing in the response saying the opt-in was declined — unfalsifiable from a fixture, and
    unreachable with a wildcard token, for which the opt-in always succeeds. It runs as a matrix over
    the same thirteen endpoints because the server threads `request_include_global` through the tag
    detail, resolution, vocabulary, alias, resource and audit views *separately*, so one view can
    misuse it while `Tags.List` stays correct. Five runs per endpoint: the default read excludes the
    global rows; an authorized token can opt in (the control, without which "the merchant saw
    nothing" also passes on a route that ignores the option); the merchant token still sees none; the
    option widens to **global, never to every namespace** — that one asserted with the wildcard token,
    which *is* authorized for the second merchant, so authorization cannot be what withholds the row;
    and the same opt-in read still returns the caller's OWN rows, without which each of the negatives
    would also pass on a request that failed or came back empty.
  - **Assignment idempotence: 201 once, 200 forever after, same row.** The status split is the only
    thing `AssignmentService.Create`'s documented idempotency rests on, and `doData` deliberately
    surfaces no 2xx status — so this is the suite's one assertion made off the wire rather than
    through a method.
  - **Bulk partial failure is atomic, and reports no existence oracle.** A bulk assign naming one good
    id and one bad one writes neither. More importantly, an id naming a real tag in ANOTHER merchant
    must be reported exactly as an id naming nothing at all — and the comparison is over the WHOLE
    canonicalised envelope (status, code, message, every details key, with the offending id
    substituted out), not one field. An oracle does not have to live where the test happens to look:
    a reworded message or one extra details key would name which of the two ids was real while a
    single-field check stayed green.
  - **Deactivation cascade, per-namespace slug uniqueness** — asserted on the status as well as the
    code, since `IsConflict` reads the code alone and #17 asks for a **409** — **and every `Is*`
    helper** against the error the server really sends, including two a caller is most likely to get
    wrong: an unmatched resolution slug is a 400 `validation_error`, not a 404, and a rejected bearer
    token arrives as a **403** whose code is `authentication_required`, so status alone cannot tell
    authentication from authorization. Four helpers are structurally out of reach against a working
    harness and are listed with reasons rather than quietly omitted.

  **The harness gained the tokens this needs** and the two version lines are unaffected by them.
  `scripts/octonomy-harness.sh` now mints, alongside the wildcard grant, one EXACT merchant grant on
  each side of the isolation boundary — and asserts both directions before reporting ready: a 201
  inside merchant A's own namespace and a 403 reaching for merchant B. The negative is what earns
  the round trip, since a grant that reached every namespace would still satisfy the positive probe.
  The CI composite action now masks every exported `*_TOKEN` rather than the one variable that
  existed when it was written.

### Changed
- **The vendored contracts now track server `3.2.0`**
  ([#57](https://github.com/octoverse-id/octonomy-go/issues/57)). A bookkeeping refresh and nothing
  more: server 3.2.0 is a minor release for operator-facing capability — subpath deployments,
  self-hosted API-docs assets, two new system checks — and both generated schemas regenerate
  byte-identical apart from `info.version`. Diffing the vendored files against the server confirms
  it: one line changed in each, and `core/errors.py` is unchanged, so the error-code registry is
  too. No SDK behavior changes, and no exported API moves.

  It is recorded rather than skipped because the vendored files and `docs/versioning.md` are how
  this SDK states which server it was written against, and a claim of 3.1.1 stops being true the
  moment 3.2.0 ships. This is also the first refresh the new gate drove end to end: it reported the
  version delta and nothing else — every schema, parameter, response and error-code comparison came
  back clean — then failed the half-finished refresh that moved the specs without the marker, which
  is the state it exists to forbid.

## [2.0.0-alpha.1] - 2026-09-11

**The first published release of the modern line** (`main`, module
`github.com/octoverse-id/octonomy-go/v2`), and the first **tagged** version at the `/v2` path — until
this tag, `go get` there resolved a pseudo-version off the default branch, which is a resolvable
version too, just not a released one.

No `0.x` of either line was ever released, so this entry covers everything in the tree: the original
`/api/v1` client, the server 3.1.x upgrade that made `/api/v2` the default surface, the remaining six
resource groups, and the pre-release fixes. The `### BREAKING` and `### Changed` sections below
describe deltas against the **untagged** tree and against the `1.x` compat line, since no consumer can
have been running an earlier *released* version of this module. Untagged is not the same as
uninstallable, though: `go get` on the `/v2` path resolved a **pseudo-version** off the default
branch, so anyone tracking `main` that way has been running some earlier state of this tree, and these
entries are written for them too.

**It is a prerelease on purpose.** A bare `v2.0.0` would promise SemVer stability this API does not
have yet, and the first break would force a `/v3` path migration. The gate for dropping the
`-alpha.N` suffix is API freeze — no further breaking changes intended, real-server integration
green, docs current, one release candidate validated — not endpoint count, which is already complete.

### BREAKING
- **The default REST surface is now `/api/v2`.** `Config.APIVersion` selects it and defaults to
  `APIV2`, the server's primary advertised surface; the client previously targeted `/api/v1`
  unconditionally. **If your Octonomy server predates 2.0, set `Config.APIVersion = APIV1`** — such a
  deployment has no `/api/v2` route and answers every call with an unrouted 404. The SDK cannot
  detect this in advance (there is no version handshake), so this is a wire-level change that
  compiles clean. It does not fail silently: see the error-mapping entry below, which is what makes
  the misconfiguration loud, and which was a condition of making v2 the default at all.
- **An envelope-less non-2xx no longer gets a semantic error code.** A response that did not carry
  Octonomy's `{"error": {...}}` envelope now yields `CodeUnexpectedStatus` (`IsUnexpectedStatus`)
  instead of a code derived from its HTTP status. **`IsNotFound(err)` no longer reports true for a
  bare 404** from a proxy, a gateway, or a server with no route for the requested API version — only
  for a real Octonomy `not_found`. This changes behavior for existing v1 callers who branch on
  `IsNotFound` for a bare 404, independently of the version default above.

  The old mapping is what made the v2 default unsafe: an unrouted 404 became `CodeNotFound`, so a
  caller's ordinary "that tag doesn't exist" branch read a missing `/api/v2` as an empty taxonomy
  with no error at all. Codes that *do* arrive in an envelope are preserved verbatim, including ones
  this SDK has no constant for, so a `503 namespace_api_disabled` stays distinguishable from an
  infrastructure 503.
- **`Metadata` on the three `*Update` structs is now `*Metadata`.** `TagUpdate`, `VocabularyUpdate`,
  and `TagAliasUpdate` change the field's type so that clearing a metadata object can be expressed at
  all: `&octonomy.Metadata{}` sends `"metadata": {}`, and `nil` still omits the key
  ([#37](https://github.com/octoverse-id/octonomy-go/issues/37)). `Metadata` is `map[string]any` and
  `encoding/json` counts a zero-length map as empty under `omitempty`, so `Metadata{}` previously
  sent **no** `metadata` key — a caller asking to clear the stored object got a 200 with the old
  object still in place and no error, which is the silent-success failure this SDK refuses
  everywhere else. Dropping `omitempty` instead would have put `"metadata": null` on every PATCH that
  does not touch metadata. Callers setting metadata on an update take the address of the literal
  (`&octonomy.Metadata{"team": "growth"}`); the `*Create` structs and every response model are
  unchanged. All three structs moved together so that no resource is the pointer-typed outlier.

### Added
- **`/api/v2` and namespace scoping** ([#7](https://github.com/octoverse-id/octonomy-go/issues/7)).
  `APIVersion` (`APIV1`, `APIV2`), `Config.APIVersion`, and `Client.APIVersion()`. Namespace
  (merchant / sub-tenant) scoping is per-request via `WithNamespace(nsType, nsID)` and
  `WithGlobalNamespace()`, which set or clear the `X-Namespace-Type` / `X-Namespace-ID` pair. There
  is deliberately **no** `Config` namespace field: omitting the headers is a legal request that
  returns the *global* namespace with a 200, so a client-level default would silently mis-scope every
  read at call sites that still look correct.
- `WithApplication(applicationID)` contributes the `application_id` query parameter on **bodyless**
  requests (`GET`, `HEAD`, `DELETE`), which take their application scope from the query and must
  carry one when namespaced. Without it the SDK could not construct a valid namespaced detail read at
  all. It is refused on a `POST`/`PATCH`, where the body's `ApplicationID` is authoritative: the
  server drops the query value on a global create, so honoring the option there would silently create
  a tenant-shared row for a caller who asked for application scope.
- `WithIncludeGlobal()` asks a namespaced read to also return the global rows the caller is
  authorized for (`include_global`, a query parameter — fail-closed on the server). It is refused on
  writes, where the server ignores it, rather than being sent to do nothing.
- `NamespaceType` / `NamespaceID` on `Tag` and `Vocabulary` — decode-only, nil on a global row and on
  every `/api/v1` response. All **seven** v2 schemas that carry namespace identity now have them:
  the five remaining (`TagAlias`, `Assignment`, `TagResource`, `ResourceTag`, `AuditLog`) landed with
  their resources later in this same release (see [`docs/roadmap.md`](docs/roadmap.md)).
- Error codes and helpers for the namespace surface: `namespace_not_supported`, `namespace_invalid`,
  `namespaced_writes_disabled`, `namespace_api_disabled`, `ambiguous_resolution`, each with an `Is*`
  helper. `namespaced_writes_disabled` and `namespace_api_disabled` are **operator** states — rollout
  flags, not caller errors — and their doc comments say so.
- `IsTenantMismatch`, `IsApplicationMismatch`, and `IsInactiveTag`: the constants shipped without
  helpers, and the latter two are what assignment writes raise.
- Response bodies are bounded at 32 MiB, reported as `ErrResponseTooLarge`. A caller cannot express a
  size ceiling through `*http.Client` — its `Timeout` bounds duration, not bytes — so the limit lives
  at the one chokepoint every method shares. A non-2xx that trips the ceiling still returns an
  `*APIError` with its status and `CodeUnexpectedStatus`, wrapping the cause so `errors.Is` reaches
  it; otherwise the large failures would silently fall out of `AsAPIError` while identical smaller
  ones kept working. `APIError` gained `Unwrap` for this.
- Contradictory scope options are refused rather than resolved by precedence — including a second
  `WithApplication` or `WithNamespace` naming a different value. Last-wins on a scope axis is a
  silent cross-merchant read, and on `Get`/`Delete` (no params struct) option-versus-option is the
  only way the value can be set. `WithGlobalNamespace` stays the one explicit override.
- The missing-application guard on a namespaced request keys on whether the request carries a **body**
  rather than on whether it is a read. A bodyless `DELETE` is exactly the case where the query string
  is the whole request and `WithApplication` is the only way to supply an application, so it is now
  checked locally instead of being sent to a certain `403`.
- **`WithRequestID(id)` sends `X-Request-ID`** ([#5](https://github.com/octoverse-id/octonomy-go/issues/5)),
  threading a caller's own correlation id through the four places the server records a request: the
  audit row (`AuditLog.RequestID`), the outbox / webhook event envelope (its `request_id` field and
  the delivered webhook's `X-Octonomy-Request-ID`), the structured request log, and the error
  envelope (`APIError.RequestID`). It composes with `WithActor` — actor is *who*, request id is
  *which call* — and applies to every method that takes options. The health probes are the
  exception: they take none, and `HealthService` now records why this option is excluded along with
  the scoping knobs.

  **The SDK never mints one, and there is no `Config` field.** No header is sent unless the option is
  used, which leaves the server's own `req_<uuid>` minting intact; a client-minted id would replace a
  value the caller can at least read back off an error envelope with one that was never surfaced
  anywhere. A client-level default is worse still: a request id names *one* request, so it would
  stamp every call the process makes with a single value and correlate nothing while looking like it
  worked. The server's id is still not surfaced on the **success** path — methods return
  `(*T, error)` — so callers who want correlation on a success generate the id themselves, which is
  the whole point of the option.

  An id that is blank, non-printable-ASCII, or surrounded by whitespace is refused locally, before
  the request is sent. That is wire grammar, not a server rule, and each case is a distinct way the
  id the caller logged and the id the server stores stop matching: a control byte is rejected by
  `net/http` inside `Do`, where the SDK would report it as `ErrUnreachable` ("nothing answered") for
  a request that was never sent; a byte above `0x7e` is *accepted* and then decoded `latin-1`
  server-side, landing in the audit row as mojibake; and outer whitespace is silently trimmed by
  `net/http` while writing the header, so `" req-abc "` would be recorded as `"req-abc"`.
- `docs/openapi-v2.yaml`, vendored from server 3.1.1.
- **`CodeScopeImmutable` / `IsScopeImmutable`, and `docs/openapi.yaml` re-vendored from server
  3.1.1** ([#6](https://github.com/octoverse-id/octonomy-go/issues/6)). The v1 spec had been pinned
  at server `1.0.0`; both vendored specs now track the same server release. The v1 contract itself
  moved by 53 lines: a documented `409 scope_immutable` on the detail `PATCH` for tags,
  vocabularies, and tag aliases; the `scope` query parameter on `/tag-resolution`, which
  `Tags.Resolve` was already sending against a running server; an `ErrorResponse` schema component;
  and `default: true` on `is_active` in the `Tag`, `TagAlias`, and `Vocabulary` response schemas,
  which records a default the server always applied and needs no SDK change.

  `IsScopeImmutable` is convenience, not a fix: `parseError` already preserved the code verbatim, so
  `APIError.Code == "scope_immutable"` worked before this. What it adds is a name for the one
  branch a caller must not get wrong — the server raises it as a subclass of its conflict error, so
  it carries `409` while its code is **not** `conflict`, and `IsConflict` reports false for it. A
  caller keying on the *status* reads "duplicate slug, pick another" and retries a request that can
  never succeed. Scope is fixed at creation; the remediation is to re-create the row in the target
  scope. `APIError.Details` names the offending fields. From this SDK only `ApplicationID` can raise
  it, on **either** surface: the server's rule covers all three scope fields and its detail-PATCH
  view is shared by `/api/v1` and `/api/v2`, but namespace is header-set rather than body-set, so
  the three `*Update` structs carry no namespace field for a PATCH to move.

  The 409-versus-`conflict` split is asserted in `integration_test.go` against a real server, not
  only against a canned fixture: a fixture asserting that `IsScopeImmutable` is true and
  `IsConflict` is false on the same response is a fixture asserting what this SDK already believes.
  The smoke step moves a global vocabulary into an application, and also asserts that the refused
  PATCH left the row unchanged.

  **No `Scope` field was added to `TagListParams`.** The parameter belongs to `/tag-resolution` on
  both surfaces and appears exactly once per spec; the tags list route has none.
- **`Each` pagination walker and `DecodeMetadata` typed metadata**
  ([#14](https://github.com/octoverse-id/octonomy-go/issues/14)). Both are generic, so both belong to
  the modern line only — the frozen `v1.x` compat line has no generics and gets neither.

  `Each(ctx, start, page, fn)` is the offset loop everyone was writing by hand, with the termination
  condition they were getting wrong. **It issues one HTTP request per page**, which is stated in the
  doc comment, in the README, and in `doc.go`, because one call making N round trips is in real
  tension with this package's no-hidden-behavior promise and naming plus documentation is the whole
  mitigation. It advances by the number of items that **arrived**, not by the `Limit` requested — the
  server silently clamps `Limit` to 200, so a walk at `Limit: 500` that trusted its own arithmetic
  would skip three items in every five. It stops on the server's `next == nil` *and* on an empty
  page, the second being what guarantees termination when the first is wrong.

  It returns `start.Offset` plus the number of items processed — the first item it did **not**
  process, which is what makes it a resume point: the page start on a fetch failure, the failing item
  on a callback failure. A walk that dies on page 40 of 100 keeps 39 pages of progress. An offset is a
  **position, not an identity**, so what a resume does with it is conditional: over a stable,
  unchanged collection it re-delivers the item that failed, but where rows moved — or on the tags
  list, where they need not have — it may address a different row, so a resume *may* retry the failed
  item and may equally skip it. Neither at-least-once nor at-most-once is on offer, and a stable
  `ORDER BY` would not buy them either — a row inserted or removed *before* the offset shifts
  everything after it, deterministic sort or not. Nor would a keyset cursor on its own: it removes
  the positional shift but is only as stable as the key it seeks on, and that varies by endpoint —
  vocabularies and aliases sort on `name`/`slug`, which a caller can edit mid-walk, while audit logs
  and assignments sort on an insert-time timestamp that never changes, and the tags list has no order
  to seek on at all. A consistent view of a moving collection needs a **snapshot**, which is the
  server's to offer. It is also not a polling cursor: a row created
  since that sorts after the old tail turns up, one sorting before it never does.

  A cancelled context is observed **before the next callback**, not only at the next fetch. Handing
  `ctx` to the page function alone left a real hole on the final page — with no further fetch to
  notice, a walk cancelled part-way through it delivered the rest of the page and returned a nil
  error. Caught in review, fixed, and pinned by a test that fails without the check.

  The page function must pass through the `ListOptions` it is handed. Ignoring the offset would
  re-fetch page one forever; `Each` detects that from the offset the server echoes and returns an
  error naming it instead of looping. That guard claims only the non-terminating shape: a dropped
  `Limit` is invisible to it, and a walk that fits in one page succeeds either way.

  **Offset drift is documented, not papered over**, and the ordering it depends on is per endpoint:
  vocabularies and tag aliases sort by `(name, slug, id)`, audit logs by `(created_at DESC, id)`,
  assignments and resource tags by `(assigned_at DESC, id)`. A concurrent create or delete shifts the
  window either way, and an item can be delivered twice or skipped.

  **`GET /tags` has no `ORDER BY` at all**, which is worse than drift and was found while writing
  this. Its view annotates `usage_count`, making the query a `GROUP BY`, and Django drops
  `Meta.ordering` from aggregate queries — verified against a running 3.1.0 server, where the emitted
  SQL ends at `GROUP BY` and Django's own `queryset.ordered` reports false. `LIMIT`/`OFFSET` over an
  unordered query is undefined, so a tags walk may repeat or miss rows **with no concurrent writes at
  all**. Documented on `Each` as best-effort, with the mitigation that is actually available: compare
  the first page's `Pagination.Count` against the number of distinct IDs walked. It reads in **one
  direction only** and only for a complete walk from offset 0 — `Count` is the size of the whole
  collection, not of the part still ahead, so a resumed walk legitimately sees fewer. Fewer proves
  rows were missed; equal proves nothing, since a concurrent create and delete cancel out in the
  total. It detects a short walk; nothing client-side can prevent one.

  `DecodeMetadata[T](m)` decodes a resource's `Metadata` into the caller's own struct. It is a
  **function, not a method**: `Metadata` is a type *alias* for `map[string]any` and Go does not allow
  methods on aliases. Promoting it to a defined type would not break assignment — Go still accepts a
  plain map there — but it would change type identity for every type switch, reflection site and
  signature naming it. A nil or empty map yields the zero value of `T` and no error, short-circuited
  rather than round-tripped so the promise holds for a pointer or map `T` too; on any error the
  **zero** value comes back, never the half-filled struct `encoding/json` leaves behind when it hits a
  type mismatch mid-decode.

  **Large integers MAY lose precision, and not here.** Where they do, it happened when the *response*
  was decoded into `map[string]any`, whose JSON numbers are `float64` — before `DecodeMetadata` is
  called and beyond its power to recover. "Above 2^53" is not the rule, and neither is the tempting
  repair "but even numbers survive": float64 loses resolution in **doubling steps** — every integer is
  exact below 2^53, only the even ones between 2^53 and 2^54, only multiples of four past 2^54. That
  is precisely why the caveat says *may*. A `Metadata` the caller built holding a real `int64` is
  unaffected. The issue suggested "decode the raw JSON yourself"; this SDK has no first-class hook for
  that, though `Config.HTTPClient` does let a custom `RoundTripper` copy the body first. The simple
  fix is to store such values as **strings** and parse them out. Tests pin the loss at `2^53+1`, the
  survival of `2^53+2`, the loss of `2^54+2`, the caller-built exactness, and the string workaround.

  Both are asserted against a real server in `integration_test.go` as well as against fixtures. The
  fixture reproduces four beliefs about the server's paginator — `count` is the total, `next` goes nil
  at the end, `limit` is clamped to 200 and echoed, `offset` is echoed. `Each` reads two of them:
  `next` to stop, and the echoed `offset` to catch a dropped `ListOptions`. The clamp is why
  "advance by what arrived" is the correct rule, and `count` is what a *caller* needs to detect a
  short walk — neither is read by the walker. All four are now asserted against a running server
  rather than only against the fixture that agrees with them. The real-server walk uses the **alias** route, whose order is total, rather than the tags list
  that has none.

### Changed
- `docs/roadmap.md` is re-derived from `openapi-v2.yaml` rather than edited. It had been written
  against server 1.0.0 and had drifted: `Tags.Resolve` was documented as taking `slug` +
  `application_id` when the endpoint takes four parameters including `scope`. Since #8–#13 delegate
  to that file, the drift would have been copied into six resources.
- Header assembly moved out of `doRaw` into `Client.headers`, which had grown past the point where
  the branches read clearly inline.

### Fixed
- **A 2xx whose `data` envelope held the wrong object decoded to a zero-valued resource.** The
  envelope assertion stopped at "is `data` present and non-null", so `{"data": {}}` filled in nothing
  and every single-resource call returned a blank struct with a **nil error** — verified on tags,
  vocabularies, tag aliases, and tag assignments alike. The check now reaches one level in: a `data`
  object that is empty, null, or not an object is an error, and so is such an element inside a list
  page (`{"data": [null]}`) or inside a composite's array of rows
  ([#40](https://github.com/octoverse-id/octonomy-go/issues/40)). List-element errors name the index.
  A NON-empty object needed a second check, because it is still a well-formed one: `{"id": null}` and
  `{"identifier": "tag_1"}` both decode to a blank resource with a nil error, since `encoding/json`
  ignores a null for a string field and skips unknown keys. So every decoded model now has to carry
  the field that identifies its row — `id`, except `assignment_id` on `ResourceTag` and `resource_id`
  on `TagResource` — and a blank one after a successful decode is an error. `ResourceTag` and
  `TagResolution` additionally require their nested `tag.id`, since both contracts mark `tag`
  required there and both routes exist to deliver it inline. The lists are the vendored schemas' own
  `required:` entries. That closes the runtime half no contract gate can see.
  The id made the defect self-evident to anyone who read it, which is why the line was drawn where it
  was; every other field — an empty `Slug`, a nil `Metadata`, an `AssignedAt.IsZero()` — makes it a
  plausible-looking blank instead. `TagResolution` gains the required-keys treatment the other
  composites already had, since it carries no id of its own and its entire purpose is to hand back a
  tag: `matched_type`, `matched_alias`, and `tag` are all required keys (as both contracts mark them
  — `matched_alias` required as a key, whose value is nullable), an empty `matched_type` is an error
  while an unknown one is preserved verbatim as an unknown error code is, and a `matched_type` of
  `alias` with a null `matched_alias` is refused because `TagResolution` documents the alias as
  non-nil whenever the match is an alias, and a caller writing `res.MatchedAlias.Slug` against that
  invariant would panic. The guarantee stops short of requiring every field a resource documents: that is the server's
  validation rather than the client's, and drift against the published schema is the contract gate's
  job ([#18](https://github.com/octoverse-id/octonomy-go/issues/18)), from the other side. Found by an independent Codex review of
  [#10](https://github.com/octoverse-id/octonomy-go/issues/10).
- **`make release-check` reported success without running `lint` or `vuln`.** Both targets skip with
  a notice when their binary is missing and neither `else` branch exits non-zero, so on a machine
  without `golangci-lint` and `govulncheck` a green gate proved that neither ran — in front of a
  release that cannot be recalled. A new `require-tools` target runs first in `release-check` and
  fails naming each absent binary and its install command; standalone `make lint` and `make vuln`
  still skip, since hard-failing a fresh clone over an optional dev tool is what that behavior is
  for ([#53](https://github.com/octoverse-id/octonomy-go/issues/53)). It was a live case rather than
  a hypothetical: `golangci-lint` installs under the active toolchain's `GOPATH` and is off `PATH` by
  default in this project's own dev setup. The caveat `docs/release.md` carried is replaced by a
  statement of what the gate now guarantees.
- **A nil `RequestOption` panicked instead of returning an error.** The transport now refuses one by
  name (`RequestOption 2 is nil`) before anything is sent, as `NewHealthClient` does for a nil
  `HealthOption`. Conditionally assembled option slices are where a nil comes from, and this library
  never panics. Found by Codex review of
  [#13](https://github.com/octoverse-id/octonomy-go/issues/13).
- **Single-resource responses decoded to zero-valued structs.** The server wraps every payload under
  `data` — single resources as `{"data": {...}}`, not only lists — so `Tags.Create`/`Get`/`Update`
  and the three `Vocabularies` equivalents returned an **empty struct with a nil error** against a
  real server. The vendored `docs/openapi.yaml` documents bare objects, every canned test body
  encoded the spec rather than the server, and so a complete unit suite stayed green throughout.
  Found by the compat line's smoke test on its first run against a container
  ([#32](https://github.com/octoverse-id/octonomy-go/issues/32)).
- **The `vuln` CI job stopped running govulncheck at all.** `golang/govulncheck-action` installs
  `golang.org/x/vuln/cmd/govulncheck@latest` and offers no version input, while `actions/setup-go`
  exports `GOTOOLCHAIN=local` so the pinned Go really is the Go used. When x/vuln v1.8.0
  (2026-09-08) raised its own minimum to go 1.26, the install began failing on
  `requires go >= 1.26.0 (running go 1.25.x; GOTOOLCHAIN=local)` — on `main` as well as on every
  PR — and the scan never ran. The job now installs with `GOTOOLCHAIN=auto` (which applies to
  *building* the tool; the scan still uses the pinned Go, so standard-library advisories stay
  reported against the version under test) and invokes `govulncheck` directly, so a failed install
  is a failed step rather than a skipped scan. No advisories against this module: all 18 findings
  seen while reproducing were artifacts of a local go1.25.4 and are fixed at the 1.25 patch CI
  resolves to. Same one-line fix applied to the install hints in `docs/development.md` and the
  `Makefile`, which reproduce the identical error on a go1.25 toolchain.

### Changed
- The transport is now one request path (`doRaw`) and three decoders chosen by response shape:
  `doData[T]` unwraps the single-resource envelope, `doList[T]` decodes the list envelope, and
  `Client.do` handles a call with no payload. Every **envelope** shape that would previously have
  decoded to a zero value with a nil error is an error instead: a 2xx with no `data` key, a null
  `data` where a resource was expected, an empty body, a list response with no usable `pagination`
  block, and a non-204 answer to `Delete`. A present-but-null `"data"` on a list normalizes to an
  empty non-nil slice, identical to `"data": []`. The check stopped at the envelope, which left a
  well-formed envelope carrying the *wrong object* decoding to a zero value; that is
  [#40](https://github.com/octoverse-id/octonomy-go/issues/40), fixed in this same set — see above.

### Added
- `integration_test.go` (build tag `integration`, `make smoke`): a smoke test against a real server,
  covering both response envelopes on both resources. It has grown with each resource landed in this
  same set and is now an ordered walk over every group. Wired into CI as a
  non-advisory `smoke` job running `make smoke` with `OCTONOMY_SMOKE_REQUIRED=1`, so neither a
  harness that failed to export its credentials nor a test that no longer runs can report a vacuous
  green. It replaces the advisory bootstrap-only `harness` job, keeping that job's cross-step
  credential assertion as a step. Making it block the *merge* additionally needs its check context
  added to branch protection.
- Reusable Octonomy container harness (`scripts/octonomy-harness.sh`, `make dev-server`) that boots
  Postgres plus the pinned `ghcr.io/octoverse-id/octonomy:3.1.0` image, applies migrations, mints a
  namespace-capable service token, and writes `OCTONOMY_TEST_*` credentials to
  `.octonomy-harness.env`. Both SDK version lines invoke it, so neither carries a bootstrap of its
  own. Exposed to CI as the `.github/actions/octonomy-harness` composite action.

### Added
- **Tag aliases** ([#8](https://github.com/octoverse-id/octonomy-go/issues/8)). `client.Aliases`
  covers the full CRUD surface — `Create`, `Get`, `List`, `Update`, `Delete` on `/tag-aliases` — plus
  `client.Tags.ListAliases` for `GET /tags/{tag_id}/aliases`. `TagAlias` carries `NamespaceType` /
  `NamespaceID` (decode-only), which makes it the third of the seven v2 schemas that do.
- `TagAliasUpdate.TagID` re-points an alias at a different tag. That is a normal edit, not the scope
  change `PATCH` refuses: moving the alias itself between scopes is a `409 scope_immutable`
  (`IsScopeImmutable`), which deliberately does **not** satisfy `IsConflict` — reading a fixed-scope
  refusal as a duplicate slug would send a caller down a retry path that cannot work.
- `TagAliasListParams` exposes the full documented filter set for the collection route
  (`application_id`, `include_shared`, `is_active`, `q` as `Query`, `slug`, `tag_id`, plus paging).
  `TagListAliasesParams` is a separate, narrower type for the nested route, which the contract
  documents with five parameters. One server function backs both routes, so `q` and `slug` would be
  honored on the nested one too; exposing them would put the SDK ahead of the published contract on a
  route the server is free to narrow.
- Note for callers filtering aliases: the server lists **active rows only** when `is_active` is
  absent. Since `Delete` is deactivation, `IsActive: octonomy.Bool(false)` is how deleted aliases are
  found.

### Added
- **Tag resolution** ([#9](https://github.com/octoverse-id/octonomy-go/issues/9)).
  `client.Tags.Resolve(ctx, slug, params, opts...)` resolves a slug to a tag, possibly by way of an
  alias, returning `*TagResolution` (`{MatchedType, MatchedAlias, Tag}`). One specialized read — the
  group has no list form and no writes. It works on both surfaces, with one caveat on `Scope` below;
  the namespace headers and `include_global` are v2-only.
- `ResolutionScope` (`ResolutionScopeGlobal`, `ResolutionScopeMerchant`) types the `scope` parameter
  the spec describes as a bare string. The server accepts exactly these two values.
  `ResolutionScopeMerchant` resolves within the request's own namespace, so the SDK refuses it
  locally on a request that has none.

  **`Scope` is sent on both surfaces**, and `docs/openapi.yaml` documents it on
  `/api/v1/tag-resolution` as of the #6 refresh above. It was absent from the vendored v1 contract
  only while that file sat at server `1.0.0`, which predates the parameter; sending it on v1 was
  already right then, verified against a 3.1.0 container where `/api/v1/tag-resolution` validates it
  by name — `scope=merchant` on a global request is rejected with "Merchant scope requires a
  namespaced request", and an unknown value with "Use 'global' or 'merchant'". Gating it to v2 would
  have refused a call every current deployment answers, and the SDK has no version handshake with
  which to gate it honestly. Against a v1 deployment older than the release that added it, the
  parameter is silently dropped like any unknown query parameter — the same exposure every other
  post-1.0.0 addition carries. `ResolutionScopeGlobal` is a legal explicit pin — the one place
  in this SDK where the literal `global` is accepted, as against the reserved `X-Namespace-Type` — and
  from a namespaced request it is *also* the authorization opt-in, so it does not need
  `WithIncludeGlobal` beside it: the server widens the authorized set for this route only when it
  sees `scope=global`.
- `MatchedType` (`MatchedTypeTag`, `MatchedTypeAlias`) types the response's `matched_type`.
  `MatchedAlias` is non-nil whenever the match came through an alias — required on decode, so the
  branch that reads it cannot nil-deref — and `Tag` is the canonical tag either way.
- **An unmatched slug is a `400 validation_error`, not a `404`.** `IsValidation` is the branch that
  means "nothing is called that"; `IsNotFound` reports false. A `scope=global` resolution by a caller
  without the authority to see global rows returns that same error, indistinguishable on purpose, so
  the response cannot disclose the existence of rows the caller may not read.
- `WithIncludeGlobal` is now refused alongside `ResolutionScopeMerchant`, which is the second place
  the server silently discards that option rather than reporting it: merchant scope pins resolution
  to the request's namespace and `effective_resolution_scope` returns `include_global` false on that
  branch, whatever the query said. A caller who asked for global rows would have got merchant-only
  results with no sign the option did nothing — the same reason the option is already refused on
  writes. Pairing it with `ResolutionScopeGlobal` stays legal: redundant is not contradictory.
- **The two ambiguity axes arrive under different codes**, so handling only one misses half the
  cases: rows differing by *application* are `ambiguous_resolution` (`IsAmbiguousResolution`) with
  `Details["application_id"]`, while canonical tags differing by *type* are a plain
  `validation_error` (`IsValidation`) with `Details["type"]`. Both verified against a running 3.1.0
  server, not read off the spec.

### Added
- **Tag assignments, including bulk** ([#10](https://github.com/octoverse-id/octonomy-go/issues/10)).
  `client.Assignments` covers `Create`, `Remove`, `BulkAssign`, and `BulkRemove` — linking tags to
  external resources, which is the core operation of a tagging service. `Assignment` carries
  `NamespaceType` / `NamespaceID` (decode-only), making it the fourth of the seven v2 schemas that do,
  and the only one the contract does not mark them `required` on while the runtime emits them anyway.
- `Assignment.ApplicationID` is a plain `string`, not the `*string` on `Tag`, `Vocabulary`, and
  `TagAlias`: an assignment is always application-scoped, so there is no tenant-shared case and no nil
  to represent.
- **`Create` is idempotent.** Re-assigning a tag already on a resource returns the existing row with a
  `200` instead of a `201`, and is not an error. The SDK returns the same `*Assignment` either way and
  does not surface which happened — `BulkAssign` with a single tag id answers that directly, since its
  `Created` / `Existing` counts are the same fact carried in the body. The tag may be named by exactly
  one of `TagID`, `AliasID`, or `AliasSlug`.
- **`Remove` sends a body on a `DELETE`**, which is how the row is identified: there is no
  `/tag-assignments/{id}` route. One consequence reaches callers — `WithApplication` is refused there,
  as on any body-carrying request, because `AssignmentRemove.ApplicationID` is authoritative. Removing
  an assignment that does not exist is a `204`, not a `404`, and removal is a real delete rather than
  the deactivation tags, vocabularies, and aliases get: an assignment is a link, and an inactive link
  is an absent one.
- **The bulk responses are composites under the `data` envelope, and the vendored spec describes
  neither correctly.** `openapi-v2.yaml` claims `bulk-assign` returns a bare array and documents no
  schema at all for `bulk-remove`. Probed against a running 3.1.0 server, they return
  `{"data": {"created": N, "existing": N, "skipped": N, "assignments": [...]}}` and
  `{"data": {"removed": N}}`. Both go through `doData` with a result struct: a `[]Assignment` decoder
  written from the spec would return an empty slice and a nil error against the real body, which is
  [#32](https://github.com/octoverse-id/octonomy-go/issues/32) in a new place. A regression test
  asserts both spellings of the spec's claim are errors rather than empty results.
- `BulkAssignResult.Skipped` is **always zero** on server 3.1.x and exists only because the server
  emits it: nothing is skipped because nothing is tolerated, an unknown tag id failing the entire call
  instead. An id outside the request's namespace reports identically to one that exists nowhere, so
  the response cannot be used to probe for tags in namespaces the caller cannot read.
- **The bulk results require the keys a caller acts on**, rather than letting an unexpected object
  shape decode to a zero-valued result with a nil error. `doData` validates the `data` envelope's own
  shape and identity, which is the right line for a *resource* — past that, a zero-valued
  `Assignment` has an empty `ID` and no caller mistakes it for an answer — but a composite of
  counters is different: `created: 0, existing: 0` with
  no rows is an ordinary result, and `removed: 0` is the single most common one there is. So a renamed
  or missing `created`, `existing`, `assignments`, or `removed` is an error. `Skipped` is exempt,
  being vestigial; a present-but-null `assignments` normalizes to an empty non-nil slice, exactly as
  `doList` treats a null page.
- `Assignment`'s namespace pair is asserted two ways, because neither alone catches a misspelled
  field name: a unit test decodes a **raw** body written with the wire's own key names, and the smoke
  test creates a **namespaced** assignment and checks the pair the server populates. Every other
  fixture is marshalled from `Assignment` itself, so a wrong json tag is used for both the write and
  the read and round-trips perfectly — verified by breaking the tags, which left the entire unit suite
  *and* the real-server smoke test green.
- `BulkRemove` takes canonical tag ids only, with no alias form — the asymmetry with `BulkAssign`,
  which also accepts `AliasSlugs` and unions them with `TagIDs`. It tolerates ids matching nothing,
  counting them out of `Removed` rather than raising. Both bulk calls cap at the deployment's
  `MAX_BULK_TAGS` (200 by default).

### Fixed
- **Path segments were escaped twice, so an id containing a space, `%`, or `#` addressed the wrong
  resource.** `url.PathEscape` produced `%20` and `net/url` escaped the result again when rendering
  the `url.URL.Path` it was assigned to, so `"ord 9"` went out as `ord%2520` and reached the server as
  the literal `ord%209`. `doRaw` now sets both halves of the path pair through `Client.resolvePath`,
  so each segment is escaped exactly once.

  Harmless while every path segment was a uuid, which is why it survived from the original client. It
  stopped being harmless at `/resources/{resource_type}/{resource_id}`: a resource id is a
  **caller-chosen external identifier** the server validates only as non-blank, and `ReplaceTags` is
  destructive — so a wrong id silently replaced the tag set of a resource the caller never named.
  Confirmed against a running server, where the two spellings produce two distinct rows.

  A resource id containing a **`/`** remains unaddressable however it is escaped: the server's Django
  route receives a decoded slash from WSGI and matches nothing, answering an envelope-less `404` that
  the SDK reports as `IsUnexpectedStatus`. Octonomy will *store* such an id (via `Assignments.Create`,
  whose id travels in the body) but will not *route* it — a server-side gap the SDK documents rather
  than rejects, since the failure is already loud.

### Added
- **Health probes** ([#13](https://github.com/octoverse-id/octonomy-go/issues/13)). `Health.Live` and
  `Health.Ready` reach `/health/live` and `/health/ready`, the one group that sits **outside the API
  surface in three ways at once**: rooted at the server root rather than under `/api/<version>`,
  answering with a bare `{"status": "ok"}` that carries **no `data` envelope**, and authenticating
  nobody. They are reachable two ways — `client.Health` for a caller who already has a full client,
  and the new credential-free constructor for one who has no credentials at all — and both run the
  same code, hit the same path, and send no credentials. `Config.UserAgent` and `WithHealthUserAgent`
  are set independently, so `User-Agent` is the one header that can differ between them.
- **`NewHealthClient(baseURL, ...HealthOption)`**, a constructor requiring **only a base URL**, with
  `WithHealthHTTPClient` (the knob a probe loop wants: the 30s default timeout is rarely right for
  one) and `WithHealthUserAgent`. It exists because `New` rejects a blank `Token` or `TenantID`, so
  until now a caller could not construct a client *at all* in order to reach an endpoint that needs
  neither. `New`'s validation was **not** loosened and `doData`'s envelope requirement was **not**
  relaxed to make health fit — either would have cost every other resource the guarantees those
  checks exist for. A `HealthClient` exposes `Health` and nothing else, and the API transport refuses
  a credential-free client outright rather than sending a blank `Authorization` header.
- **`ErrUnreachable`**, matched with `errors.Is`, marks a request that received **no HTTP response at
  all** — connection refused, DNS, TLS, a client timeout, a cancelled context. It is not
  health-specific: every method in the package wraps it, so "the server never answered" is now
  distinguishable from "the server answered and said no" everywhere. The cause survives the wrap
  (`errors.Is(err, context.DeadlineExceeded)` still works), and the error message is unchanged.
- **`CodeNotReady` / `IsNotReady`**, for a health probe the server **answered** with a non-2xx and its
  own `{"status": …}` body — `/health/ready` returns `503 {"status": "unavailable"}` when its
  database connection will not open. `APIError.Details["status"]` carries the server's own word.

  **Unreachable and unready are never collapsed into one error**, because they call for different
  operator responses: back off and re-probe, versus go looking for the process. And `not_ready` is
  **not** the status-to-code mapping `CodeUnexpectedStatus` exists to forbid — nothing is inferred
  from an HTTP status here; the code is established by the health view's own server-authored payload.
  A 503 whose body is an HTML error page is still `CodeUnexpectedStatus`, which is what keeps "the
  application says it is unready" apart from "a load balancer answered because nothing is behind it".
- **Audit logs** ([#12](https://github.com/octoverse-id/octonomy-go/issues/12)). `client.AuditLogs.List`
  reads the append-only mutation history, with `Tags.ListAuditLogs` and `Resources.ListAuditLogs` as
  the pre-filtered nested routes. One new model, `AuditLog`, which completes the seven v2 schemas
  carrying `NamespaceType` / `NamespaceID`. **List-only in every sense**: the server writes these rows
  itself as a side effect of the mutation they describe, so there is no create, no update, no delete,
  and no `Get` — a single row is reached by filtering the collection.
- **Audit reads need the `audit:read` scope**, which service tokens carry separately from `tags:read`
  and `tags:write`. A token without it gets a `403 forbidden` (`IsForbidden`) from all three routes
  while its ordinary reads keep working — never an empty page. It is a token misconfiguration rather
  than a caller mistake, so retrying or narrowing the filters will not help.
- **`AuditLog.Changes` is an open `Metadata` object rather than a `Before`/`After` struct, and one row
  shape forces that.** The contract types the field as nothing at all. The server writes
  `{"before": {…}, "after": {…}}` — but a `tag.deactivated` that cascaded to aliases adds
  `cascaded_alias_ids`, whose value is an **array**. A typed pair would drop it silently; a
  `map[string]Metadata` would fail to decode the row and take the whole page down with it, since one
  bad element fails the list. Callers read `log.Changes["after"].(map[string]any)`.
- **`AuditLog.OperationID` groups every row one operation emitted**, which is what makes a multi-row
  mutation reconstructable weeks later: `Resources.ReplaceTags` and both bulk calls write one row per
  assignment they touch under a single operation id, so the removals and additions read as one act
  rather than as unrelated churn. `RequestID` correlates a row with the one HTTP request that produced
  it, and with `APIError.RequestID` for a request that failed. Pass
  [`WithRequestID`](https://github.com/octoverse-id/octonomy-go/issues/5) to supply your own; the
  server mints one when the caller sends none.
- **Rows arrive newest first** (`created_at` descending, `id` as a stable tiebreak), so offset paging
  walks backwards through history. Asserted against a real server, since page order is a property of
  the server's query and no fixture can establish it.
- **An unknown tag or resource is an empty page on the nested routes, not a `404`** — unlike every
  other `/tags/{id}` route in this SDK. Both filter the audit table by the path's identifier and never
  load the entity, so a row that never existed, one that was deactivated, and one outside the
  request's namespace are reported identically: `200` with no rows. Only a `tagID` that is not a uuid
  fails, at the server's router, as an envelope-less `404` (`IsUnexpectedStatus`).
- `AuditLogListParams` carries the full documented filter set; `TagListAuditLogsParams` and
  `ResourceListAuditLogsParams` carry the four each nested route documents. They are deliberately
  narrower, and deliberately separate types: one filter function serves all three routes on server
  3.1.x, so `entity_type` would be honored on the tag route too, but exposing it would put the SDK
  ahead of the published contract on a route the server is free to narrow — the same reasoning
  `TagListAliasesParams` records against `TagAliasListParams`.
- On `/api/v2`, audit reads are **namespace-filtered and global rows fail closed**, inherited whole
  from the transport's scoping options: a namespaced read returns that namespace's rows and no global
  ones, `WithIncludeGlobal` asks for both, and an exact merchant grant with no global authority still
  sees none. The smoke test proves the exclusion against a real server, which is the only way to prove
  the headers arrived — without them the server serves the global namespace with a `200`.
- **Resource tags** ([#11](https://github.com/octoverse-id/octonomy-go/issues/11)). `client.Resources`
  covers `ListTags` and `ReplaceTags`, and `client.Tags.ListResources` completes the mirror. Two new
  models: `ResourceTag` (a tag as seen from a resource, with the `Tag` nested whole) and `TagResource`
  (a resource as seen from a tag). Both carry `NamespaceType` / `NamespaceID`, taking the SDK to six
  of the seven v2 schemas that do.
- **`ReplaceTags` replaces, it does not merge.** Every tag on the resource and absent from the request
  is removed. To add, read the current set first and send the union.
- **An empty replace is legal and clears the resource.** `ResourceReplace` with no `TagIDs` and no
  `AliasSlugs` removes every tag — a deliberate difference from `BulkAssign`, which refuses an empty
  request. An empty slice reaching that call by accident, from a filter that matched nothing, wipes
  the resource silently and successfully. Proven against a real server rather than only documented.
- **The replace response is a third composite shape, and the vendored spec is wrong about it twice
  over.** `openapi-v2.yaml` claims a bare array *and* claims the elements are `ResourceTag`. The
  server sends `{"data": {"created": N, "removed": N, "tags": [...]}}`, where those are **`Tag`**
  values — no `AssignmentID`, no `AssignedAt`. A client written from the spec decodes an empty slice
  and a nil error; one that guessed the envelope but kept the element type decodes tags with every
  field empty. `ResourceReplaceResult` requires `created`, `removed`, and `tags` on decode, for the
  reason established with the bulk results: zero is an ordinary answer here, so a renamed key would
  read as "nothing needed changing".
- `ResourceReplace` deliberately carries **no** `ResourceType` or `ResourceID`, though the contract
  lists them: they come from the path, and the server overwrites whatever a body sends. A field that
  cannot affect the request does not belong on it.
- `ResourceListTagsParams.ApplicationID` is **required by the server** — the only list in this SDK
  where that holds — and may equally be supplied with `WithApplication`. `IncludeInactive` is not the
  `is_active` filter the tag and alias lists take: it is a different parameter with different
  polarity, where nil means active-only and true *widens* to include deactivated tags. There is no way
  to ask for deactivated tags alone.
- Both new models' namespace pairs are asserted two ways — a unit test decoding a **raw** body written
  with the wire's own key names, and namespaced live calls checking what the server populates. Neither
  alone suffices: renaming the tags left the entire unit suite green until the raw-body test existed,
  because every other fixture is marshalled from the struct it is decoded into.

### Documentation
- **Documentation truth pass across the whole tree**
  ([#15](https://github.com/octoverse-id/octonomy-go/issues/15)). Every file that asserted a Go
  floor, an API version, a list-type shape, or a release state that is false for the line it
  describes is corrected, and the facts the two-line split created are written down for the first
  time.
  - **Two Go floors, stated as two.** `AGENTS.md`, `CONTRIBUTING.md`, `docs/development.md`, and the
    README said "Go 1.24+" flatly. Each now names the line it is describing: `main` is
    `.../octonomy-go/v2` at Go 1.24+, `support/go1.13` is `.../octonomy-go` at Go 1.13 with no
    generics, no `any`, and no post-1.13 standard library. `AGENTS.md` and `CONTRIBUTING.md` said
    "List methods return `*List[T]`" as an unqualified rule; that spelling is `main`-only, and the
    compat line's per-resource `*TagList` / `*VocabularyList` is now recorded next to it.
  - **A published sunset date with a named owner: 2027-08-31.** It was previously a rule ("12 months
    from the `v1.0.0` tag") rather than a date, which is not something a blocked team can plan
    against. The date, its derivation from the 2026-08-26 tag, and its owner now appear in
    `SECURITY.md`, `docs/versioning.md`, the README, and `doc.go`, matching the copies already on
    `support/go1.13`.
  - **`SECURITY.md` on `main` reflects both lines.** It claimed security fixes go to "the latest
    `0.x` release" and listed only `0.1.x` — a version that does not exist. It now carries the
    two-module supported-versions table, the sunset, the backport rule, and the two facts a Go 1.13
    consumer needs: that toolchain is itself unpatched past `go1.13.15` (August 2020), and `retract`
    cannot recall a release for it.
  - **The release record is accurate.** `README.md` and `docs/versioning.md` said the repository had
    no git tags and nothing published. `v1.0.0` was tagged on `support/go1.13` on 2026-08-26.
    `docs/versioning.md` now carries a Release state section covering both lines, why `v1.0.0` is not
    an ancestor of `main`, and why `git describe` here will not find it.
  - **One live versioning policy.** The `0.3.0`-minor framing an earlier plan carried is recorded as
    settled: v2 support ships on a new module path at `v2.x`, a major, which is what the
    major-effort rule in `docs/versioning.md` already required.
  - **The consumer-side `exclude` snippet is documented as unnecessary**, not omitted. The `/v2`
    module path made Go itself the enforcement, so a reader following an older plan document is told
    so explicitly rather than left hunting for a snippet that no longer exists.
  - **One complete resource inventory.** It was restated in five places and drifting. `docs/api.md`
    now holds the only complete method-to-endpoint mapping and is the one place to update; the
    README, `docs/roadmap.md`, `docs/architecture.md`, and `docs/versioning.md` link to it, naming
    individual routes only where they are making some other point. `docs/versioning.md` still claimed only Vocabularies and Tags
    were implemented, and `docs/api.md` still listed three namespace-carrying schemas rather than
    seven.
  - **`docs/release.md`** gains the three branch roles (`support/` line, `<type>/<issue>-`
    implementation, `release/vX.Y.Z` tag PR) and a worked backport procedure — branch off the support
    line, expect the cherry-pick to need rewriting against a tree with no generics, and let the
    `compat guard` and `go1.13` jobs prove it, since a modern toolchain enforces the language version
    from `go.mod` but not the standard library.
  - **`docs/roadmap.md`** stops restating what exists and becomes the recipe plus a register of known
    gaps, each against its issue (#36 and #49, after #37 and #40 were closed in this same set) with
    the two deliberate deferrals (#20, #22) named as such.
- **The `http.RoundTripper` extension point is documented** in the README, `doc.go`, and
  `docs/architecture.md`. Since `AGENTS.md` forbids logging in the library, a `RoundTripper` on the
  caller's `*http.Client` is the sanctioned path for metrics, tracing, request logging, retries, and
  rate limiting — and it pairs with `WithRequestID` to join a client span to the server's audit row.
  The worked example **guards its read of the response**: a transport failure returns a nil
  `*http.Response` with a non-nil error, so an unguarded `resp.StatusCode` panics on exactly the
  failures the wrapper exists to observe. This package's no-panic guarantee covers its own code, not
  the transport you supply.
- **`MaxIdleConnsPerHost` is documented, and deliberately not tuned.** It caps how many **idle**
  connections to one host are kept for reuse; `http.DefaultTransport` leaves it unset, so it falls
  back to `http.DefaultMaxIdleConnsPerHost` — **2**. Check whether it applies before acting on it:
  `DefaultTransport` sets `ForceAttemptHTTP2`, so against an HTTPS endpoint that negotiates h2 the
  requests multiplex and pool size largely stops mattering. On HTTP/1.1, surplus connections beyond
  two in flight are closed on completion rather than pooled, so the symptom is handshake churn rather
  than a ceiling — nothing blocks. Sequential work never reaches it on either protocol (`Each` issues
  its pages one at a time); a fan-out across goroutines sharing one `*Client` is what produces the
  churn. Raising it silently would be a capacity decision taken inside the caller's process, so the
  README shows the `http.DefaultTransport.Clone()` recipe instead — `Clone` keeps the tuned defaults
  a bare `&http.Transport{}` starts without, though a bare transport does still negotiate HTTP/2. The
  pool belongs to the **transport**, not the client, which is what makes sharing one `*Client` the
  simple correct default.
- **The library "adds no retry loop of its own"**, stated that way rather than as "never retries":
  `net/http`'s transport already retries a request it failed to write on a *reused* connection, which
  is recovery from a half-closed idle socket rather than a retry policy.
- **`version.go` names this release**, and the `0.1.0` that preceded it is on the record as what it
  was. That constant sat in the tree as a placeholder from before anything was released, making the
  default User-Agent `octonomy-go/0.1.0` with no tag anywhere corresponding to it; the documentation
  truth pass disclosed it rather than leaving it to surprise, and this release PR replaces it, since
  `version.go` is bumped there and nowhere else. The default User-Agent is now
  `octonomy-go/2.0.0-alpha.1`. The claim that no `v0.x` was published is backed by a re-runnable
  `proxy.golang.org` query rather than by assertion, framed as the proxy's current set of
  tag-resolvable versions.
- **The "two response envelopes" framing is corrected where it implied a closed set.**
  `docs/architecture.md` said the envelopes *are* the deliberate divergences; the two bulk-assignment
  responses and the resource-tag replace are three more, and `docs/api.md` — which carries the
  complete list — is now what it points to. `docs/api.md` also no longer says the server wraps
  *every* payload under `data`: the health probes answer with a bare `{"status": "ok"}`, as the same
  page says further down, and its request-header table was missing `X-Request-ID` entirely.
- **`docs/release.md`** no longer says `go get` resolves tags "directly from GitHub" — the default
  path is `proxy.golang.org`, whose permanent cache is precisely why a tag cannot be unpublished —
  and it now distinguishes what the two compat checks actually do: `go1.13` builds, vets, and runs
  `go test -race` under a real toolchain, while `compat guard` never compiles the package and asserts
  the `go.mod` invariants.
- The bug-report template asked for a version "e.g. `v0.1.0`", which was never released, and did not
  ask which of the two modules the reporter imports — the first thing triage needs. Both fixed. The
  PR template now checks the base branch against the line and the no-version-bump rule, and both
  templates cite `openapi-v2.yaml` alongside `openapi.yaml`.

### Added (the original client, carried in from the never-released tree)

The initial contents of the SDK, targeting the stable Octonomy REST **v1** API at server contract
`1.0.0`, served under `/api/v1`. Dependency-free — standard library only. This section was previously
filed under a `## [0.1.0] - 2026-06-08` heading describing a release that was never cut; the label is
corrected here, per #24 and #29, rather than left implying an installable version that never existed.
Everything in it ships for the first time in `v2.0.0-alpha.1`, reshaped by the entries above — most
of all the default surface, which is now `/api/v2`.

- Client foundation: `New(Config)` with `BaseURL`/`Token`/`TenantID` validation, a configurable
  `*http.Client`, and a shared transport that sets `Authorization`, `X-Tenant-ID`, optional
  `X-Actor-ID`, `Accept`, and `User-Agent` headers.
- Typed error handling: `*APIError` decoded from the `{error:{code,message,details,request_id}}`
  envelope, error `Code*` constants, and `IsNotFound`/`IsConflict`/`IsValidation`/`IsAuthError`/
  `IsForbidden`/`AsAPIError` helpers.
- Pagination: generic `List[T]` decoding the `{data, pagination}` envelope, plus `ListOptions`.
- `Vocabularies` service: Create, Get, List, Update, Delete.
- `Tags` service: Create, Get, List (full filter set), Update, Delete.
- `WithActor` per-request option, and `String`/`Bool`/`Int` pointer helpers for optional fields.
- Runnable `examples/quickstart` program and a vendored `docs/openapi.yaml` contract reference.

[Unreleased]: https://github.com/octoverse-id/octonomy-go/compare/v2.0.0-alpha.1...main
[2.0.0-alpha.1]: https://github.com/octoverse-id/octonomy-go/releases/tag/v2.0.0-alpha.1

<!-- Both links point at THIS line. The compat line is a different module with its own
     versions and its own copy of this file on support/go1.13, so a link to v1.0.0 from
     here would compare a consumer of /v2 against code they cannot install.
     There is still no [0.1.0] link definition, because that tag does not exist: the
     section it used to head is now filed under this release, where its contents were
     actually published for the first time. -->
