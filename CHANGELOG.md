# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- **The prose that names the vendored contract is enforced by something that fails**
  ([#86](https://github.com/octoverse-id/octonomy-go/issues/86)). Fourteen places in this repository
  state which server contract the SDK vendors. `make contract-check` mechanized **three** — both
  specs' `info.version` and the `<!-- contract-version: -->` marker, all via `checkRecordedVersion`.
  The other eleven were prose, and nothing checked them at all.

  Not hypothetical: [#84](https://github.com/octoverse-id/octonomy-go/issues/84)'s first pass moved
  eight of the fourteen and **missed six** — `docs/api.md` ×2, `docs/architecture.md`,
  `docs/development.md`, `docs/roadmap.md` ×2 — with every gate green. A reviewer and an independent
  outside pass each caught the same six; had neither looked, the repository would have merged with
  its API reference and its contributor instructions naming a contract it no longer vendored.

  - **`TestEveryContractVersionMentionIsCurrentOrExempt`** (`contractversion_test.go`) walks every
    tracked prose and source file and requires each version token to **either** equal the recorded
    marker **or** match a category carrying a written reason. Anything else fails, naming the file,
    the line, and both readings so the contributor chooses rather than guesses. Verified end to end
    by reintroducing #84's exact defect: the guard flagged both injected sites by line.
  - **It is an INVERSE registry, and the cheap alternative is refused.** A "no stale version
    anywhere" check is worse than useless here: of the mentions that survived #84, eighteen of
    twenty-three were correct — a decision taken when the server was older, an ordering caveat
    naming the releases that lack the `ORDER BY`, a behaviour verified against a running container,
    a schema comparison whose whole point is the old number, and released history. A registry of
    sites that must MATCH is the weaker form, refused for the reason #77 refused its cheap proxy: it
    catches drift in the sites someone registered and is silent on the one nobody did, which is the
    failure that actually happened.
  - **The harness pin is exempt by ROLE, never by value.** `scripts/octonomy-harness.sh` and the
    composite action pin a container floor that is deliberately independent of the vendored
    contract; the two numbers were different simultaneously until #84. They agree today, which is
    the trap — a guard letting the pin pass by equality looks correct now and breaks the next time
    the pin legitimately leads. `TestHarnessPinIsExemptByRoleNotByValue` pins the distinction *while
    the two values still agree*, which is the only window in which a by-value implementation is
    indistinguishable from a by-role one.
  - **It reads two lines of look-back**, because prose wraps and the phrase that classifies a
    mention is regularly on the line above the version it qualifies. Without it every wrapped
    verification note reports as unclassified, and the fix a contributor reaches for is to loosen
    the category until it stops complaining — which is how a guard stops catching anything. A
    regression case pins both directions: a wrapped note is exempt, a wrapped *claim* is not.
  - **Its edges are written next to the code.** It is syntactic: it checks that an exemption exists
    and that its reason is non-empty, never that the reason is true. It classifies by shape rather
    than site by site. It says nothing about the two specs or the marker, because
    `checkRecordedVersion` owns those and giving two gates one job lets each assume the other is
    doing it.
  - Runs in `make test` with **no `integration` build tag**, on the same reasoning as #73's and
    #77's guards: it must run in the pull request that adds the site, not only where a container
    does. `docs/roadmap.md`'s enforcement table gains a row, marked as the one entry that is not a
    recipe step — refreshing a contract is not a step, so the requirement had nowhere else to live.

### Added
- **A design doc for the compat line's `/api/v2` epic**
  ([#88](https://github.com/octoverse-id/octonomy-go/issues/88)).
  [`docs/designs/compat-line-api-v2-parity.md`](docs/designs/compat-line-api-v2-parity.md) records
  the reversal of the compat line's freeze and the reasoning behind it, across a CEO review, an
  engineering review and two outside-voice passes. Nothing in this repository's SDK changes; the
  work it plans lands on `support/go1.13`.
  - The **premise was inverted**. Both vendored contracts publish the same 18 paths, so the gap was
    never `/api/v2` — it was that the compat line implements **2 of 8 resource groups**.
  - Four defects were found live on the frozen line while sizing it: `codeFromStatus` mapping a bare
    404 to `CodeNotFound`, an absent `identityFields()` (#40's guard), an unbounded response read,
    and a vendored contract still at `info.version: 1.0.0`.
  - An AST generator was scoped, approved, then **cut** after the transform surface measured 7% and
    both of its flagship silent-failure cases turned out to live in hand-written files.
  - Three behaviours were settled by probing a real `go1.13.15` rather than reasoning: an unknown
    `omitzero` tag option is inert, so a ported `*string` emits `null` on every PATCH; `encoding/json`
    has neither cycle detection nor an unmarshal depth limit there; and `go vet` does ship
    `loopclosure`, which is why the loop-capture rewrite is caught rather than silent.

### Changed
- **[`docs/designs/octonomy-3.1-upgrade-go113.md`](docs/designs/octonomy-3.1-upgrade-go113.md) says
  it is superseded in part.** Its compat-line freeze is reversed by #88 for `/api/v2`, namespace
  scoping and the eight resource groups — **and only those**. **Webhooks remain out of scope**, now
  as a standing policy rather than as a term of the freeze: the compat line does not ship a webhook
  receiver, and a consumer needing one moves to `/v2`. A reader landing on that document needs to
  find the reversal rather than implement a policy no longer in force, and needs the carve-out with
  it. Everything else in it still holds: the two-module split, the `/v2` import path, and the
  2027-08-31 sunset are unchanged.

## [2.0.0-rc.1] - 2026-09-22

**The first release candidate of the modern line, and the point at which the API surface is frozen.**
`v2.0.0-alpha.3` was the last version that could take a breaking change on a version bump. From here
a break that proves necessary **supersedes this candidate** — `v2.0.0-rc.2` — rather than riding one,
and once `v2.0.0` proper ships it needs a major and a new import path. Nothing in this release breaks
anything: measured against `v2.0.0-alpha.3` the exported surface **grew from 217 declarations to 243
and lost none**, every addition being the `octonomy/webhook` typed-event surface below.

Of the five criteria in [`docs/versioning.md`](docs/versioning.md#modern-line-pre-stability) for
dropping the prerelease suffix altogether, four were met before this cut and the fifth — *one release
candidate validated* — is the one this release exists to satisfy. **Cutting a candidate is not
validating it**: that is what the interval between this tag and `v2.0.0` is for, and it is why the
suffix is still here.

### Changed
- **The integration smoke walk is a registry, and recipe step 8 is enforced by something that fails**
  ([#77](https://github.com/octoverse-id/octonomy-go/issues/77)). `TestSmoke_RealServer` was one
  1,296-line function whose completeness was a claim in its own header; it is now `smokeProbes`, a
  table keyed by response type whose entries run in order against one server and share the rows they
  create through `smokeState`. This was the last of the five requirements
  [#73](https://github.com/octoverse-id/octonomy-go/issues/73) found enforced by nothing — `#73` gave
  step 9 a guard, [#76](https://github.com/octoverse-id/octonomy-go/issues/76) gave step 3 one, and
  this closes the set.
  - **`TestEveryResponseTypeHasASmokeProbe`** (`smokeprobes_test.go`) compares the table's keys
    against the response types derived from this package's own source — the *same* derivation #76's
    guard uses, `responseTypes()` — and binds each entry to a client method that really decodes its
    key. It fails on a response type with no entry, an entry keyed for a type nothing decodes, a
    duplicate key, an entry whose closure calls no client method the parser can see, and one keyed
    for a shape it never exercises. It carries no `integration` build tag, so it runs in the pull
    request that adds a resource rather than only where a container does.
  - **The cheap proxy was refused, and that is why this took a third issue.** "The type name appears
    somewhere in `integration_test.go`" is satisfied by a comment or an unused reference, and a check
    reporting *covered* on that basis would have made the step look enforced while leaving #32's
    class reachable. The registry is what made a real check possible; it cannot prove an assertion is
    meaningful, and it says so next to the code — what it makes impossible is the silent omission,
    which is the failure that actually happens.
  - **No pre-existing assertion was dropped or weakened.** Every assertion message the old walk
    carried is still in the new one, the eight per-resource cleanup reporters having become
    `smokeState.deleteLater` calls under the same labels; the new file adds messages of its own, for
    the row the cleared-resource case sets up and for a probe that failed or skipped. Every check the
    old walk made is in an entry,
    including the ones that were not about a response shape at all: the health probes are the walk's
    prologue, because `HealthStatus` never reaches `doData`/`doList` and so is not a response type by
    the definition the table is checked against. The empty replace now clears a resource of its own
    rather than the one the two list entries read, which keeps the destructive case away from them,
    adds a count assertion on the row it sets up, and still leaves the audit history exactly the two
    operations it reconstructs.
  - **Three behavioural consequences of the split are deliberate.** Each entry is a subtest, so a
    failure names the shape. The walk stops at the first failed entry rather than reporting the
    cascade of entries that depended on it. And an entry that *skips* stops it too — `t.Run` returns
    true for a skipped subtest, so without that check an entry could skip itself and the walk would
    carry on into rows it never created while the run reported PASS. That hazard did not exist in
    the single function, where a step could not skip without skipping everything; a missing harness
    still skips the whole test in `newSmokeClient` and nowhere else.
  - **`docs/roadmap.md`'s enforcement table, its known-gaps table, `AGENTS.md`, `CONTRIBUTING.md` and
    `docs/development.md`** all said step 8 was enforced by nothing, correctly, and now say what
    enforces it and what that guard cannot see. `CONTRIBUTING.md` said it in two places and only one
    of them was found on the first pass, which is the drift surface #73 consolidated the recipe to
    close, reappearing at a smaller scale. The known-gaps table has no rows left; the section stays,
    because a gap with no holder is what it exists to prevent.
  - **The `2.0.0-alpha.3` entries were left alone**, on the precedent #49 set in this same section.
    Both the #76 and the #73 entries there say the smoke assertion is deliberately a reviewer's job,
    and both were true at that release; correcting them in place would rewrite history to describe a
    guard that did not exist yet. This entry is the correction, and the reason recorded there — that
    the nearest syntactic proxy is satisfied by a comment — is still why the cheap check was refused
    rather than built.
- **`examples/webhook` is now the switch and nothing else**
  ([#22](https://github.com/octoverse-id/octonomy-go/issues/22)). The bound-read-verify-parse
  preamble a caller used to have to write is gone into `webhook.Handler`; what is left is the event
  switch, an error hook, and the server-side signing that lets the example run with no emitter. It
  prints a third delivery now — one carrying an event type this SDK predates — because *that* answers
  200 is the contract, and an example that only showed 200 and 401 never showed it. Run: 200, 401,
  200. `AGENTS.md` and `README.md` moved with it; the "no `http.Handler` ships here" rule, true since
  #16, is replaced by the rule that the handler must keep owning the read.
- **The tags-ordering caveats are now qualified by server version rather than stated flatly**
  ([#49](https://github.com/octoverse-id/octonomy-go/issues/49)). Server 3.2.1 fixed
  [octonomy#162](https://github.com/octoverse-id/octonomy/issues/162): `GET /tags` now orders by
  `(name, slug, id)` on both `/api/v1` and `/api/v2`. Every passage claiming the list has **no**
  `ORDER BY` was true when written and became false the moment that release shipped, which is what
  this issue existed to catch — the claims are prose, and the SDK has no mechanism that would have
  flagged them.
  - **The decision was to version-qualify, not to delete**, and it is recorded in the `Each` doc
    comment rather than only here. The SDK performs **no server-version handshake**, so it cannot
    tell a fixed server from an unfixed one at runtime; a consumer pointed at 3.2.0 or older still
    has the entire hazard, and deleting the warning would have been correct about the newest server
    and silently wrong about every other. Each passage now reads: best-effort against **≤ 3.2.0**,
    and against **≥ 3.2.1** exactly as safe as vocabularies and aliases and **no safer** — an
    `ORDER BY` makes a *fixed* result set page deterministically, it does not hand you a snapshot,
    so ordinary offset drift is untouched.
  - **Nine files carried the claim, not the five the issue inventoried.** `tagtree.go`,
    `tagtree_test.go`, `pagination_test.go`, `examples/tags/main.go` and `docs/roadmap.md` also
    asserted it, the first three as the stated *reason* for a behaviour rather than as a caveat. The
    tree's "no ORDER BY to preserve" rationale for input order is gone; input order is still what
    `BuildTagTree` preserves, and the reason assembly cannot assume "parents first" is now the true
    and durable one — no server ordering *guarantees* parents before children, since `(name, slug, id)`
    sorts on the name and says nothing about the parent chain, so it may happen to and is never
    obliged to.
  - **The two released CHANGELOG entries were left alone**, deliberately. The passages in
    `2.0.0-alpha.1` and `2.0.0-alpha.2` are a record of what was true at those releases, not a draft;
    correcting them in place would rewrite history to describe a server that did not exist yet. This
    entry is the correction.
- **The vendored contracts now track server `3.2.1`**
  ([#84](https://github.com/octoverse-id/octonomy-go/issues/84)). A version-only refresh, and the
  first one the scheduled gate drove rather than a person noticing: `info.version` in
  `docs/openapi.yaml` and `docs/openapi-v2.yaml`, the `<!-- contract-version: -->` marker and the
  *Targeted server contract* row in `docs/versioning.md`, the fixture in
  `tools/contractdrift/drift_test.go` that exists to match that marker, and every statement of what
  is vendored — `AGENTS.md`, `CONTRIBUTING.md`, `doc.go`, `docs/api.md`, `docs/architecture.md`,
  `docs/development.md` and `docs/roadmap.md` (twice). No type, method, or inventory row moved
  and `docs/contract-coverage.yaml` is untouched — 3.2.1 is a behavioural patch (the tags `ORDER BY`,
  upstream `octonomy#162`) whose specs are byte-identical to 3.2.0's apart from the version string.
  - **The upstream gate is what made it necessary, and the offline one never could have.**
    `make contract-check` compares the vendored specs against this repository and never leaves it, so
    it reported no drift throughout. `make contract-drift` fetches the server's own specs and
    compares `info.version`, and it had been failing since 3.2.1 shipped on 2026-09-16.
    `checkContractVersionDrift` (`tools/contractdrift/checks.go`) is a string comparison with no
    mechanism for recording an accepted lag, so the tool's standing advice to "record the decision in
    `docs/contract-coverage.yaml`" has nowhere to land for this particular finding — refreshing or
    implementing are the only two resolutions it offers, and for a version-only delta the first is
    the whole of the work.
  - **Nothing reported it for five days**, which is the part worth keeping. The drift job runs
    Mondays at 06:23 UTC, so its last run (2026-09-14) predated the server's release by two days and
    the next Monday was the first that could have seen it. A scheduled gate is evidence only as often
    as it runs, and the window between the server tagging a release and this repository hearing about
    it is a week wide by construction — `workflow_dispatch` is on that workflow for the same reason.
  - **The first pass moved eight and missed six.** Counting **non-CHANGELOG line occurrences**
    rather than files, because `docs/api.md` and `docs/roadmap.md` carry two each: eight moved with
    the first commit, six more were found in review, fourteen in all. The miss happened because *names a version* and
    *names the vendored contract* are not the same predicate, and only the second one was being
    matched against.
  - **Five kinds of `3.2.0` mention are deliberately left alone.** The list covers every survivor
    outside this bullet's own accounting prose, which cannot categorise itself:
    - the dated record of the `/api/v2`-default review (`octonomy.go`, `docs/versioning.md`), which
      describes a decision taken when the server was 3.2.0 and is a statement about that moment;
    - the tags-ordering caveats in `README.md`, `pagination.go` and `docs/api.md`, where "3.2.0 and
      older" names the servers that lack the fix rather than what is vendored;
    - the verification notes in `docs/roadmap.md` and `integration_suite_test.go`, which record
      behaviour observed against a running 3.2.0 — re-pointing them at a server nobody ran them
      against would make them false;
    - the schema comparisons, here and in the harness-pin entry below, which say 3.2.1's specs are
      byte-identical to **3.2.0's**: the old version is the thing being compared against and cannot
      be moved without destroying the sentence;
    - and every released CHANGELOG entry, on the precedent #49 and #77 set in this same section.

    Naming five rather than four is the second correction this bullet has taken. The first draft said
    three and the list was the defect — an exhaustive claim that omits a category is the same failure
    as a refresh that omits a site, one level up, which is why the count is written here rather than
    left implicit.
- **The harness pin moved to `ghcr.io/octoverse-id/octonomy:3.2.1`** from `3.1.0`, in all three
  places that named it: `scripts/octonomy-harness.sh`, the `.github/actions/octonomy-harness`
  composite action (which carried its own copy of the default and would otherwise have left CI on
  3.1.0 while local runs moved), and the harness walkthrough in `docs/development.md`.
  - 3.2.1 is a **floor**, not a refresh: below it the new test is expected to fail rather than pass
    vacuously, and the comment at the pin says so.
  - **The vendored contracts were left alone here and refreshed in
    [#84](https://github.com/octoverse-id/octonomy-go/issues/84) instead**, above. The reasoning
    recorded at the time — `openapi.yaml` and `openapi-v2.yaml` are byte-identical between 3.2.0 and
    3.2.1 apart from the `info.version` string, because the fix is behavioural and invisible to the
    schema — still holds, and is why that refresh carries no code change. What it missed is that
    `make contract-check` is the **offline** gate: passing it says the vendored specs agree with this
    repository, never that they agree with the server. The harness pin and the recorded contract
    version remain independent of each other, which is what this bullet was about.

### Added
- **The webhook typed-event surface and `webhook.Handler`**
  ([#22](https://github.com/octoverse-id/octonomy-go/issues/22)). The other half of
  [#16](https://github.com/octoverse-id/octonomy-go/issues/16): the verifier shipped alone because it
  is correct regardless of payload shape, and this half waited for the shapes to settle.
  - **`Handler(secret, fn, ...opts) (http.Handler, error)`** is a whole webhook endpoint. It owns the
    order the steps have to happen in — bound the body with `http.MaxBytesReader`, read it to bytes,
    verify those bytes, and only then parse — so the body-consumption hazard the package has warned
    about since #16 is impossible by construction rather than documented. `WithMaxBodyBytes`,
    `WithAdditionalSecrets` (a rotation window is not an instant), and `WithErrorHandler` configure
    it; the statuses it answers are a table in its doc comment, and the only 2xx among them is the
    one where the consumer's handler returned nil.
  - **It returns an error, which the issue's sketch did not.** An empty signing secret, a nil
    `EventHandler` and a non-positive ceiling are deployment mistakes knowable where the handler is
    wired, and the alternative is an endpoint that answers 500 forever with the reason reachable only
    through an optional hook — the check-that-appears-to-run failure this package exists to refuse.
    `octonomy.New` refuses a blank `Token` on the same reasoning. An unset
    `OCTONOMY_WEBHOOK_SIGNING_SECRET` arriving as `""` returns `ErrNoSecret` at startup, and a secret
    the runtime itself rejects (`GODEBUG=fips140=only`, under 112 bits) returns `ErrUnusableSecret`
    there too rather than on every delivery.
  - **`ParseEvent`, `Event`, and the eleven `Event*` type constants** decode one verified delivery.
    `Event` carries the whole envelope, `Event.Payload` the raw JSON always, and at most one of
    `Tag` / `Vocabulary` / `TagAlias` / `Assignment` — the typed payload, chosen by event type.
    `Event.Namespace()` reports the routing namespace *and whether there is one*, because a null
    `namespace_type` is the concrete global namespace and not a wildcard.
  - **An unknown `event_type` parses and is acknowledged with 200**, per the issue's second design
    finding. Delivery is at-least-once with backoff and dead-lettering, so an SDK that errored on a
    type it did not recognise would make every deployed Go consumer start dead-lettering the day
    Octonomy adds a twelfth one — consumers who shipped no code and did nothing wrong. The raw type
    and the undecoded payload are both exposed, and no payload is decoded into a struct its event
    type did not name, so a future shape cannot become a permanent 5xx either.
  - **`TagSnapshot`, `VocabularySnapshot`, `TagAliasSnapshot` and `AssignmentSnapshot` are distinct
    from the REST models**, per the first design finding: they omit `usage_count` and the namespace
    pair, and an `*.updated` payload carries only the fields that actually changed.
  - **Every snapshot field is an `octonomy.Optional[T]`, not a pointer, and that is a deliberate
    departure from the issue's wording.** A pointer carries two states and a snapshot field needs
    three. `{"after":{"parent_id":null}}` — an ordinary un-nesting, one of four clears the server
    accepts — is *present and null*, and against a `*string` it is indistinguishable from *absent*,
    so a consumer applying the diff leaves the tag nested under a parent the server no longer has.
    That is #64's wall from the decoding side, which is what `Optional` was built for; `Get`,
    `IsNull` and `IsZero` separate the three, and `Get` cannot nil-panic the way a pointer field
    invites. Fields are tagged `,omitzero`, so a snapshot re-encodes to exactly the keys it arrived
    with, and `TestSnapshotFieldsTagEveryOptionalOmitzero` fails on a missing tag.
  - **A panicking `EventHandler` is recovered and answered with 500** — the decision #22 asked for
    rather than left undefined. Left to escape, net/http recovers it per connection, logs a stack,
    and closes the connection with **no response written**, so the consumer's own error path never
    sees it; `ErrHandlerPanic` carries the value and the stack to `WithErrorHandler` instead.
    `http.ErrAbortHandler` is re-panicked untouched, since that is net/http's documented way to
    abandon a connection on purpose.
  - **Eight new sentinels** (`ErrMalformedEvent`, `ErrIncompleteEvent`, `ErrMalformedPayload`,
    `ErrMethodNotAllowed`, `ErrBodyTooLarge`, `ErrUnreadableBody`, `ErrNoEventHandler`,
    `ErrHandlerPanic`), none of which is about the *signature* contract, so none needs a vector in
    `signature_vectors.json` — the vectors stay payload-agnostic and portable.
    `TestVerifyErrorsAreMutuallyDistinguishable` now parses every non-test file in the package rather
    than `verify.go` alone, so a sentinel added to a new file cannot be added and forgotten.
  - **`webhook` now imports the root package**, for `octonomy.Metadata` and `octonomy.Optional` and
    nothing else. The dependency stays one-way: the root never imports `webhook`.
  - **A required envelope field that is blank is refused** (`ErrIncompleteEvent`), and
    `Event.UnmarshalJSON` does it, so `json.Unmarshal` and `ParseEvent` cannot disagree. Without it a
    delivery with no `id` would decode to a zero-valued `Event` with a nil error, and a consumer
    deduplicating on `""` drops every event after the first — #32 and #40's family, on the event side.
  - **A known event type's payload SIDES are required too.** A `tag.created` always carries `after`,
    a `*.updated` and a `*.deactivated` both, an `assignment.removed` `before` — so
    `event.Tag.After` is safe to dereference inside the case that matched it. Without the check a
    signed `tag.created` with an empty payload decoded with a nil error into a `TagPayload` whose
    `After` was nil, and the handler this package documents nil-panicked on every redelivery of it.
    An *extra* side is still accepted; that direction is forward compatibility.
  - **A half-set namespace pair is refused**, and it is the one decode that would MIS-ROUTE rather
    than mis-read: `{"namespace_type":"merchant","namespace_id":null}` read by either half alone
    reports a merchant's event as global, sends it to the tenant-shared partition, and — because the
    consumer handles it and returns nil — acknowledges it for good. Both halves null (global), both
    set, and both **absent** are all accepted: a server older than the namespace axis emits no
    namespace keys at all, so requiring them would refuse every delivery from one.
  - **`payload` is required on every event type, known or not**, because it is an ENVELOPE field:
    `serialize_outbox_event` emits all sixteen keys whatever the `event_type`, so a delivery without
    one is a malformed envelope rather than a future event type. That is what makes `Event.Payload`
    non-empty on every decoded event — and for an unknown type it is the only way to see what
    arrived, so a nil there would have hollowed out the forward-compatibility path it exists for.
  - **A panicked `error` is wrapped with `%w`**, so `errors.Is`/`errors.As` reach it through
    `ErrHandlerPanic`. `panic(err)` is the common spelling, and collapsing it to text left an error
    handler able to see *that* something panicked and never what.
  - **`WithMaxBodyBytes` bounds size, not time**, and that is now said where the ceiling is
    configured, in the README, and in `examples/webhook`, which sets `ReadTimeout` and `WriteTimeout`
    rather than `ReadHeaderTimeout` alone. Nothing in this package can bound how long a sender takes
    to deliver its bytes — the deadline belongs to the consumer's `http.Server` — so without one,
    anybody who finds the URL can trickle a sub-ceiling body and hold a connection and a goroutine
    indefinitely. `ReadHeaderTimeout` has already elapsed by the time the body starts.
  - **The "lossless round trip" claim on the snapshot types was false and is gone.** Re-encoding a
    snapshot preserves the keys this package MODELS and no others: a field a later server adds is
    dropped by design, and `Metadata` has already been through `map[string]any` — where an integer
    beyond ±2^53 may have been rounded before the struct existed. `Event.Payload` is the durable
    copy, and `TestSnapshotRoundTripsTheModelledKeysAndOnlyThose` now asserts the limit as well as
    the property.
  - **The handler's guarantees are stated with their precondition: it has to be FIRST on the
    request.** Body-reading middleware in front of it fails safe — the check runs over zero bytes
    and is refused as `ErrEmptyBody` with a 401, which is what that sentinel is for. Response-writing
    middleware does not: net/http ignores the second `WriteHeader`, so a committed 200 stands even
    when the `EventHandler` refused the event, and the dispatcher acknowledges work that never
    happened. Nothing inside a handler can detect or repair that, so it is written down and pinned by
    `TestHandlerIsOnlyCorrectWhenItIsFirstOnTheRequest` rather than guarded against.
- **`TestIntegration_TagsListPagesInATotalOrder`** (`integration_suite_test.go`) — the tags-ordering
  wording is now evidence-backed by a test rather than by prose alone. It walks an eight-tag fixture
  **one row per page**, so every page boundary is a separate query, and asserts the sequence equals
  `(name, slug, id)` computed locally from what the server echoed on create.
  - **It pins the expected order, not a self-consistent one**, and that is the entire design. A walk
    compared only against itself — or against a single-page read taken moments later — **can pass
    against the broken selector**, because one PostgreSQL backend generally keeps the same aggregate
    plan for the life of a process. Confirmed the hard way: run against a 3.1.0 harness the walk
    returned every row exactly once, none repeated and none missed, and simply in the wrong order. A
    self-comparison would have been green.
  - The fixture makes name order, slug order and insertion order **mutually disagree**, so the two
    trivial agreements a broken server could produce are both denied it. That is a much weaker claim
    than "cannot be a coincidence", and the weaker claim is the true one: an unordered query is
    *undefined*, not adversarial, so no fixture can force a pre-3.2.1 server to fail on every run.
  - **All three sort keys are exercised.** Two pairs share a name, so the **slug** tiebreaker decides
    them rather than just "sorted by name". One pair shares name *and* slug and differs only in
    **type** — permitted, because the uniqueness constraint is `(type, slug)` and not `slug` alone —
    so nothing but the **id** tiebreaker can order it. An earlier draft of this entry claimed that
    tie was unreachable through the API; it is not, and the test now covers it. What it proves is
    bounded and the test says so: with the id tiebreaker dropped those two rows have no defined
    order, so the case catches its absence only when the planner disagrees, not every run.
  - Verified in both directions against real containers: **passes on 3.2.1, fails on 3.1.0**, where
    the verifying run placed every one of the eight rows wrong. How badly a pre-3.2.1 server fails is
    a property of its plan rather than of the fixture, so that count is evidence, not a guarantee.


## [2.0.0-alpha.3] - 2026-09-15

### Changed
- **The release runbook names every site that carries the version, and there are five**
  ([`docs/release.md`](docs/release.md)). It listed `version.go` and the CHANGELOG, and
  `make version-check` compares exactly those two against each other — so `README.md`, `doc.go` and
  the four passages in `docs/versioning.md` were held by a reader and nothing else. This release
  found that the hard way: the first draft of it stamped the two the runbook named and left the rest
  reading `v2.0.0-alpha.2`. The step now carries the table, and the grep that proves it was done.
  - **A prerelease changes wording and not only digits**, which the grep cannot catch. The alphas
    allowed a necessary break to ride a version bump; **a candidate is the point at which no further
    break is intended**, and one that proves necessary supersedes the candidate rather than riding
    it. `README.md`, `doc.go` and `docs/versioning.md` all stated the alpha rule and now state this
    one.
  - `doc.go` also enumerated the alpha-exit gate as four criteria. It is five as of
    [#75](https://github.com/octoverse-id/octonomy-go/issues/75) — and the fifth, the
    `/api/v2`-by-default decision, is **answered in this same release** — see the Documentation
    entry below. What remains before `v2.0.0` is a validated release candidate.

### Added
- **`identityFields()` is a check rather than a convention**
  ([#76](https://github.com/octoverse-id/octonomy-go/issues/76)).
  `TestEveryResponseTypeCanRefuseAnEmptyDecode` (`identityfields_test.go`) collects every type handed
  to `doData` or `doList` and requires each to carry one of the two mechanisms the rule allows: a
  **resource** implements `identityFields()`, naming the field that identifies its row; a
  **composite** has no row identity and requires its keys in `UnmarshalJSON` instead. `TagResolution`
  is both. Eleven response types today, eleven satisfy it, and a type the guard cannot classify fails
  rather than passing.
  - **Why it was silent.** `requireIdentity` type-asserts and returns `nil` for a model that does not
    implement the interface, so skipping the method did not weaken the check — it removed it, without
    a sound. `{"data": {"id": null}}` or a renamed id then decodes to a zero-valued resource behind a
    `nil` error, which is [#40](https://github.com/octoverse-id/octonomy-go/issues/40), the same
    family as #32.
  - **Receiver kind is part of the rule**, and getting that wrong was the guard's own first defect.
    `doData`/`doList` call `requireIdentity(out, …)` with a **value**, so a pointer-receiver
    `identityFields` is not in the method set consulted — such a model compiles, reads correctly,
    and is skipped at runtime exactly as if the method were absent. `json.Unmarshal` is handed
    `&out`, so `UnmarshalJSON` is the mirror image and must be on the pointer. The first draft
    credited either receiver for either method, certifying a shape the runtime ignores; the guard
    now checks receiver kind and full signature, and the fixtures assert both failures.
  - **Composites are declared, not inferred.** The first draft let *any* `UnmarshalJSON` excuse a
    type from `identityFields`, so a resource that grew a custom decoder for an unrelated reason
    would have been excused from the check that matters. `compositeTypes` is a written list,
    verified against the source in both directions.
  - **A generic wrapper fails rather than hiding a type.** A transport helper handed its caller's
    type parameter is reported, because the concrete instantiation reaches it from call sites this
    guard does not follow. Type parameters are tracked per declaration, so an unrelated
    `helper[Widget any]` cannot mask a real `doData[Widget]` in the same file.
  - **What it does not do**, stated next to the code: it proves a mechanism exists with the right
    shape, never that it is *right* — a model naming the wrong field satisfies it. It does not reach
    nested resources the contract marks `required`, which is a fact about `openapi-v2.yaml` rather
    than about Go source. And nothing proves a type listed in `compositeTypes` is genuinely a
    composite rather than a resource someone wanted to excuse.
  - This is the second of the three requirements #73 found unguarded. The third, the smoke assertion,
    is deliberately staying with a reviewer ([#77](https://github.com/octoverse-id/octonomy-go/issues/77)):
    what would have to be checked is that the walk *meaningfully asserts* a shape, and the nearest
    syntactic proxy is satisfied by a comment. A check reporting "covered" on that basis would make
    the step look enforced while leaving #32's class reachable.
- **`readProbes` exhaustiveness is a check rather than a doc comment**
  ([#73](https://github.com/octoverse-id/octonomy-go/issues/73)).
  `TestEveryReadMethodHasANamespaceProbe` (`readprobes_test.go`) resolves each exported service
  method's HTTP verb from the source — following a call into a helper, since `Health.Live` names no
  verb of its own — and checks it against the entries `readProbes` returns. It fails on a read with
  no probe, a probe naming a method that no longer exists, a probe whose `find` closure calls a
  different endpoint than its name claims, a duplicate name, and an exclusion that is stale or has
  no reason written. The one deliberate exclusion, the unauthenticated health probes, moves out of
  prose and into `readProbeExclusions`.
  - **A method whose verb the guard cannot resolve fails — unless it is probed or excluded —
    rather than passing as "not a read".**
    The first draft defaulted the unknown case to silence, which reproduced #73's own defect inside
    the check written to prevent it: a read the classifier could not see needed no probe and said
    nothing.
  - **The guard has its own tests**, because on a correct tree it would pass whether or not it
    worked. `TestClassifyTakesTheVerbFromTheCallThatSendsIt` (15 cases),
    `TestParseProbesReadsOnlyTheTableThatRuns` (7 cases) and
    `TestClientServiceFieldsSeesAParenthesizedField` drive it over synthetic source, one case per
    way it was wrong across six rounds of review: a verb reached two calls away; a method that
    writes directly and then reads through a helper; a path segment spelled `DELETE` and a header
    value spelled `GET`, which matching verbs anywhere in a body classified backwards in both
    directions; a verb in an argument that is not the method position; an unrelated method that
    merely shares the name `do`; a transport call inside a closure nobody invokes;
    `s.cache.lookup()` resolved as a `Client` method; a method that rebinds its own receiver; a
    probe crediting a call made on something other than its client, or on a client that was
    shadowed or reassigned first; and every place a parenthesis could hide one of those shapes — a
    parenthesized rebinding, receiver, callee, transport call or service field, each of which is a
    legal Go spelling that silently defeated the check rather than failing closed; a call inside an uninvoked closure; and a second, unreachable
    `[]readProbe` table registering as coverage.
  - **Why this table and not the other two unguarded requirements.** `readProbes` is what makes "a
    merchant-A client never sees a merchant-B row" a statement about the whole read surface rather
    than about whichever endpoints someone remembered. The suite iterates whatever the table holds,
    so a read method that arrived without a probe quietly shrank that assertion with every job
    green, and the table's "complete list" was a hand-maintained doc comment — a reader, not a gate.
  - **It carries no `integration` build tag, deliberately.** The table lives behind that tag but is
    parsed rather than linked, so the guard runs in `make test` with no container. A check that ran
    only when the container did would be absent from exactly the pull request that adds a read
    method. `TestUpdateBodiesTagEveryOptionalOmitzero` and `TestVerifyComparesDigestsInConstantTime`
    are the precedents for reading this repository's own source to enforce a rule about it.
  - **As #73 landed**, `identityFields()` and the smoke assertion were both still enforced by
    nothing, and the recipe said so in a table rather than leaving it to be discovered.
    `identityFields()` got its guard later in this same release (#76, above); the smoke assertion
    is deliberately still a reviewer's job ([#77](https://github.com/octoverse-id/octonomy-go/issues/77)).

### Documentation
- **The `/api/v2` default was revisited, and kept** — the fifth alpha-exit criterion is now
  answered, and four of the five are met. `Config.APIVersion` continues to default to `/api/v2`:
  server 3.2.0 makes it the primary advertised surface and the only one carrying the namespace axis,
  so defaulting to `v1` would ship an SDK whose out-of-the-box behaviour ignored the dimension the
  server added. `/api/v1` stays fully supported behind one field. **Flipping the default was
  refused** — it breaks the current alpha line, points new consumers at the surface the server no
  longer advertises, and trades a loud one-line fix for a quiet wrong-surface default nothing would
  report. The reasoning is in [`docs/versioning.md`](docs/versioning.md#the-apiv2-default-revisited-and-kept).
  What remains before `v2.0.0` is a validated release candidate.
- **The `/api/v2`-by-default decision is now a gate item for `v2.0.0`, not an epic's footnote**
  ([#21](https://github.com/octoverse-id/octonomy-go/issues/21)). Defaulting to `/api/v2` is a
  wire-level change against a pre-2.0 deployment, and it was accepted for the prerelease line on two
  conditions: that the resulting failure be made loud, and that the default itself be revisited
  before the line goes stable. The first shipped with `v2.0.0-alpha.1` — an envelope-less non-2xx no
  longer becomes a semantic code, so `IsNotFound` stopped reporting true for a bare 404. The second
  was held only by the epic's risk table, and an epic closes. It is now the fifth criterion in
  [`docs/versioning.md`](docs/versioning.md)'s alpha-exit gate, where the person dropping the
  prerelease suffix will read it.
- **The resource recipe is complete, and has one home instead of four**
  ([#73](https://github.com/octoverse-id/octonomy-go/issues/73)). `docs/roadmap.md` omitted five
  requirements this repository states elsewhere and depends on — the `docs/contract-coverage.yaml`
  row, the `tools/contractdrift/drivers.go` driver, `identityFields()`, the `integration_test.go`
  assertion, and the `readProbes` probe — while `docs/architecture.md` certified it as carrying "the
  recipe in full". Two of the five fail CI loudly. **Three passed every check**, so a resource added
  by following the page verbatim compiled, went green, and arrived with three of this repository's
  own safeguards not applied to it — each of which exists because of a defect already shipped here
  (#40, #32, and the cross-merchant isolation assertion).
  - **The decision, recorded because #68 established that this page states reasoning and not only
    results: consolidate rather than patch.** `docs/roadmap.md` holds the recipe;
    `docs/architecture.md` and `CONTRIBUTING.md` link it instead of keeping shorter copies. *Adding
    the five and keeping all four copies* was refused: it fixes the symptom and leaves the drift
    surface exactly as wide, and the defect was never that someone wrote the list carelessly but
    that four lists can disagree while nothing compares them. *Deleting the recipe and pointing at
    `AGENTS.md`* was refused for removing the page's stated first purpose and sending a human
    contributor into the agent instruction file to learn how to add a resource.
  - **`AGENTS.md` keeps its own statement of every rule, on purpose.** It is not a fourth copy of
    the list but the argument behind each item — why `identityFields` exists, which harness token
    makes an isolation test mean anything — and an agent needs that at the point of use rather than
    behind a link. What it no longer has to be is the only complete place.
  - `CONTRIBUTING.md` gains an *Adding a resource* section, since a human contributor reads it and
    not `AGENTS.md`. It links the recipe and warns that a green CI run does not mean a resource is
    complete, but does **not** restate which steps are enforced — that status is the part most
    likely to move, and a second copy of it is how this defect started.
  - **Review of the consolidated recipe found three more things it had wrong**, all now fixed: it
    required a `*Create` and `*Update` of every resource, though a read-only group such as audit
    logs has neither, and described `*Create` fields as pointers when the required ones are values;
    it omitted the runnable example entirely, which `AGENTS.md` requires to be **run** against a
    real server rather than written; and its scoping note listed three request options where four
    exist, having never named `WithGlobalNamespace`.
- **`docs/roadmap.md` stops reporting issue status, and the rule that replaces it is written down**
  ([#68](https://github.com/octoverse-id/octonomy-go/issues/68)). The *What is planned that is not a
  resource* paragraph called [#19](https://github.com/octoverse-id/octonomy-go/issues/19) "Still
  open" and pointed at a milestone that had closed 4 of 4 with `v2.0.0-alpha.2`. Every issue that
  paragraph named was closed, so the passage described an empty queue as a live one.
  - **The decision, which is what #68 asked for instead of an edit: the hand-maintained *Landed /
    Still open* split is gone rather than corrected.** Correcting it buys one true day — the split
    was false within hours of #19 closing, and this is the third issue of exactly this shape (#51,
    #52, #68). The rule that replaces it, stated at the end of that page and in `AGENTS.md`: prose
    states what happened, and for what is true now it names whatever keeps it true — a link where
    the tracker holds the state, the gate where one exists (`docs/contract-coverage.yaml` is why
    *the resource queue is empty* may stay prose), and a date where a status has to be written out
    anyway. The known-gaps table carries the day its snapshot was taken (2026-09-14).
  - **Two sections were in the wrong place, which is the same defect structurally.** `BuildTagTree`
    (#20) sat under *Known gaps* describing itself as "no longer deferred" after it had shipped; it
    is now its own section next to Health, which is the other implemented thing this page explains
    rather than lists. `octonomy/webhook` (#16) sat under *What is planned*; that section is now
    *Work alongside the client rather than inside it* and names the release that closed each issue.

## [2.0.0-alpha.2] - 2026-09-13

### Added
- **`BuildTagTree`: the client-side tag hierarchy, assembled once from a fetched slice**
  ([#20](https://github.com/octoverse-id/octonomy-go/issues/20)). `TagTree`, `TagNode`, and the
  refusals `ErrTagCycle` and `ErrDuplicateTagID`, in `tagtree.go`. Tags nest through `ParentID` and
  the server returns them flat — there is no tree endpoint and no children route — so a category
  browser either filters the list once per parent (one request per node) or assembles the set
  locally. This is the assembly and nothing else: it makes no request, takes no `context.Context`,
  and never consults the server.
  - **It was deferred, and what unblocked it was grounding rather than a consumer.** #20 was held
    back as the one expansion candidate with *no grounding in a server contract*, on four questions
    with no answer that suits everybody: does a tag whose parent is absent become a root or get
    dropped, are inactive tags pruned, is a cycle an error, is there a depth limit. Reading the
    server settles three of them and turns the fourth into a filter the caller already applies. The
    answers are not this SDK's taste; they are Octonomy's behavior, and two of them are now pinned
    by the integration suite rather than asserted.
  - **NO TAG IS EVER DROPPED.** On success `tree.Len() == len(tags)` and every input tag is
    reachable from `Roots` exactly once. Ambiguity is an error rather than a quiet choice, because
    the failure #20 named — a helper that is *almost* right, which consumers work around — outlives
    the bug it hides.
  - **A missing parent is ORDINARY, so the tag is promoted to a root and named in `Orphans`.**
    Three routine things produce one, none a data defect: a deactivated parent (`deactivate_tag`
    cascades to the tag's *aliases* and never its children, while `filter_tags` applies
    `is_active=True` when the parameter is absent, so a live child comes back from the default list
    alone); namespace scope (a namespaced tag may name a **global** parent, which a read without
    `WithIncludeGlobal` does not return); and any filter or page at all. Dropping it would lose a
    row silently, and disguising it as a real root would lie about the taxonomy — so it is kept,
    promoted, and indexed. `TagNode.IsOrphan` is the per-node form.
  - **Inactive tags are kept, untouched.** The server permits an *active* tag under an *inactive*
    parent, so pruning during assembly would orphan live children of a deactivated category.
    Pruning is a filter (`TagListParams.IsActive`), applied when you fetch — the SDK adds
    ergonomics, not behavior.
  - **A cycle is refused with `ErrTagCycle`, and it is not defensive programming.** The database
    forbids only the one-hop case (`tag_parent_cannot_be_self`, `parent_id != id`) and
    `validate_tag_parent` checks tenant, application and namespace compatibility without ever
    walking the ancestry, so `A -> B -> A` is two ordinary PATCHes — **verified against a running
    3.2.0, which answers `200` to the one that closes the ring**. Every tag in a cycle has a parent
    inside the set and therefore never becomes a root, so a naive assembler returns a tree silently
    missing that cycle and everything beneath it. The message names the chain, id and slug, in
    parent-to-child order, deterministically.
  - **A repeated id is refused with `ErrDuplicateTagID` rather than resolved.** Two copies may
    disagree about `ParentID`, so choosing one is the single place assembly could silently build a
    *different* tree. `Each`'s own doc comment already warns that offset drift can deliver a row
    twice and says to de-duplicate on id; `BuildTagTree`'s doc comment carries the two-line form,
    and a test runs that snippet so the documentation cannot rot.
  - **Depth is reported, never limited**, and nothing here recurses: assembly, the cycle check,
    `Walk` and `Path` all use explicit stacks, so a 50,000-deep chain costs memory rather than a
    stack overflow — asserted by a test at exactly that depth, because recursion would take the
    process down and this library promises not to panic. Every method tolerates a nil receiver for
    the same reason: `BuildTagTree` returns `(nil, err)` on a refusal, and the natural call site
    reaches `Len` with it.
  - **`Walk` refuses a nil callback and every method tolerates a nil receiver**, in the same words
    `Each` uses. A `Walk` that dereferenced a nil callback would panic only on a tree with at least
    one node — the shape that passes a test suite and fails in production — and `BuildTagTree`
    returns `(nil, err)` on a refusal, so the natural call site reaches these methods with a nil
    tree. The copy of each `Tag` is SHALLOW and the doc comment says so rather than cloning: the
    `*string` fields and the `Metadata` map still point where the input pointed, *including* when
    the input is the slice a `Tags.List` response decoded into, so writing through one of them can
    leave `Tag.ParentID` disagreeing with the `Parent` link assembled from it. Cloning six pointers
    per node plus a faithful clone of an arbitrarily nested `map[string]any` is a much larger
    contract than this helper should take on; the rule is to build again rather than mutate what
    you assembled, and tests pin both halves.
  - **Order is INPUT order**, for `Roots`, `Orphans` and every `Children` slice, and nothing is
    sorted. `GET /tags` has no `ORDER BY` at all, so there is no server order to preserve and none
    to invent; `Walk` visits a node before its children, so sorting `Children` inside the callback
    is how a stable rendering is had.
  - **Two integration tests, because each is a property of the server no fixture can settle**
    (`integration_suite_test.go`): `TestIntegration_DeactivatedParentOrphansItsLiveChildren` walks
    the whole shape — intact tree, delete the parent, the child comes back alone and lands in
    `Orphans` with nothing dropped, then refetching the deactivated parent with `Tags.Get` repairs
    it — and `TestIntegration_ParentCycleIsReachableAndRefused` closes a ring through the API,
    proves the server stores it, and asserts `BuildTagTree` refuses it and names both rows.
  - `examples/tags` now assembles the tree it creates and then deactivates the parent to produce a
    real orphan, run against a live server as the rules here require. Its standing claim that "the
    tree is walked by filtering the list on each parent in turn" was true and is now only half the
    story, so it says both.
- **`VocabularyListParams` gained the `q` and `slug` filters**
  ([#36](https://github.com/octoverse-id/octonomy-go/issues/36)). `Query *string` (the server's `q`)
  and `Slug *string` — the pair `TagListParams` and `TagAliasListParams` already carried. Two optional
  pointer fields on an existing params struct, omitted from the query string while nil like every
  other filter, so a MINOR-shaped addition: existing callers keep compiling and behaving identically,
  with the one caveat `docs/versioning.md` states for every field addition — an *unkeyed* composite
  literal of `VocabularyListParams` in a consumer's code stops compiling, which `go vet`'s
  `composites` check flags and no example here writes.
  - **This is a gap against a contract this repository already vendored, not v2 drift.** Both
    parameters have been on `GET /vocabularies` since server **1.0.0**, they are in the SDK's own
    `docs/openapi.yaml`, and both surfaces document the same vocabulary filters — v2 adds only the
    namespace axis (`X-Namespace-*`, `include_global`) on top of them. Verified against the server
    source rather than the spec, as the rules here require: `slug` is an exact match, and `q` filters
    `name__icontains OR slug__icontains` — byte-identical to `filter_tags`, which
    `TagListParams.Query` already maps.
  - **Until now an SDK caller could not look up a vocabulary by slug at all.** The only way to find
    one was to page the whole collection and filter in Go — correct until a tenant outgrows the page
    you happened to ask for, and then quietly wrong. The integration suite's own namespace-isolation
    probe for `Vocabularies.List` was written that way, and was the single probe in that table that
    walked rather than narrowed; it now uses the exact slug filter like every other one, which
    removes the page-boundary caveat from an isolation assertion.
  - **The contract gate found this, and the same gate is what proves it closed.** `q` and `slug` sat
    in `docs/contract-coverage.yaml` under `unsent_inputs` with a written reason; the driver in
    `tools/contractdrift/drivers.go` now populates both, so the gate requires them on the wire and
    reports the two rows as stale unless they are dropped. They are. A row there is a parking place
    with an expiry rather than a permanent exemption, and this is the first one to reach it.
  - **Two tests, because they answer different questions.** A table-driven unit test asserts each
    filter on the query string one at a time — a combined "set everything" case cannot tell a
    parameter emitted under its own name from one emitted under its neighbour's — and asserts the
    query string *exactly*, so an unset filter is proved to stay off the wire rather than go out
    empty. `make smoke` then asserts both against a real server, which is the only place a filter can
    be shown to be **read**: an unknown query parameter is silently dropped, so a name the SDK got
    wrong comes back as a full, plausible page that looks exactly like a filter that worked. That
    assertion needed a **second** visible vocabulary to mean anything — a freshly booted harness holds
    exactly one, the smoke test's own, since the harness probe rows are namespaced and invisible to a
    global client, so "the filtered page holds only our row" would have been equally true of a server
    that ignored the parameter. With a decoy row present an ignored filter returns two rows and the
    test fails, which was confirmed by deleting the emission and watching it go red against the
    container.
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
    turning every `IsNotFound`, `IsConflict` and `IsValidation` into `false`. **This found a real gap on its first run** — `VocabularyListParams` was missing `q` and `slug`
    ([#36](https://github.com/octoverse-id/octonomy-go/issues/36)), closed later in this same
    unreleased cycle; the entry above is the fix. An earlier draft read the package
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

- **A runnable example per resource group, and `make dev-server` now hands you the credentials**
  ([#19](https://github.com/octoverse-id/octonomy-go/issues/19)). Ten new programs under
  `examples/` — `vocabularies`, `tags`, `aliases`, `resolution`, `assignments`, `resources`,
  `audit-logs`, `health`, `namespaces`, `webhook` — beside the quickstart that was the only one
  before. The ten that call the API run against `make dev-server` with no file edits; `webhook` is a
  receiver and needs no server at all, which the bullet below says more about.
  - **They demonstrate the semantics that are easy to get wrong, not the happy call.** Assignment
    being idempotent rather than a conflict; `ReplaceTags` replacing rather than merging, and an
    empty request clearing the resource outright; `Delete` being deactivation, which is why an
    absent `IsActive` lists active rows only; deactivating a canonical tag cascading to its aliases;
    tag uniqueness being on the pair `(type, slug)`, so the same slug under another type is an
    ordinary create; an unmatched resolution being a `400` and not a `404`; `include_global`
    widening a namespaced read but never a write. A caller who reads only the happy path is the one
    who gets these wrong.
  - **`make dev-server` ends by printing an export block**, and `make dev-server-env` reprints it
    without rebooting the container. The harness writes `OCTONOMY_TEST_*` because the integration
    suites gate on those names — an empty `OCTONOMY_TEST_BASE_URL` is what makes them skip instead
    of fail — while an example is a program a reader copies into their own service, where the
    variables are `OCTONOMY_*`. The target is the bridge, so neither set has to give up its
    property, and a freshly minted token is copied rather than retyped.
  - **`make examples` compiles each example separately and fails when it finds none.** It was
    `go build ./examples/...`, which means two different things depending on how many examples
    exist: several main packages are discarded, but exactly one is WRITTEN into the working
    directory. CI now calls the target instead of the bare command, so there is one definition. The
    emptiness guard is the same vacuous-green rule the `smoke` and `test-integration` targets carry
    — `find` matching nothing would otherwise report success having compiled nothing.
  - **`make cover` now excludes the examples, and the reason is arithmetic.** They are main packages
    with no tests, so every statement in them lands in the profile uncovered: the ten new examples
    moved the reported total from 96.7% to 54.0% without one line of the library becoming less
    tested. That figure is what "keep new code covered" is read off, and a number that says
    "coverage collapsed" when nothing collapsed is worse than no number. `make test` still runs
    every package and `make examples` is what proves the examples build.
  - **The webhook example is a receiver, not a handler the SDK ships.** `webhook.Handler` was
    deferred with the rest of the typed-event surface (#22) because no deployment emits webhooks
    yet, so the example is the shape a consumer has to write: bound the body, read it, verify, and
    only then parse and route — from the verified body, never from an `X-Octonomy-*` header. It
    signs its own sample delivery so it is exercisable with no emitter, and says plainly that the
    server is what does that.

### Fixed
- **Resolution's application "tie" does not exist, and three places said it did**
  ([#19](https://github.com/octoverse-id/octonomy-go/issues/19)). `TagResolveParams`,
  `TagService.Resolve`, and `docs/api.md` all documented two ambiguity cases — a `type` tie reported
  as `validation_error`, and an application tie reported as `ambiguous_resolution` — and told a
  caller to handle both. Only the first is reachable through the REST surface.
  - **What the server actually does**, probed against the same 3.1.0 container the suites run
    against: a resolution naming no application searches application-shared rows **alone**
    (`filter_no_application_resolution`), one naming an application ranks that application's rows
    above the shared ones, and a namespaced request must name an application at all. Three rules
    that leave no same-rung tie behind. A slug that exists only inside an application therefore
    resolves to the ordinary no-match `400`, not to a tie — which is the practical half of this: a
    caller following the old comment would have written an `IsAmbiguousResolution` branch that never
    runs, and been surprised by the `IsValidation` one that does.
  - `IsAmbiguousResolution` stays, with its unreachability recorded on it. The code exists in the
    server as defence-in-depth for its own internal callers — its test suite says so in as many
    words — and a code that arrives in an envelope is preserved verbatim whatever raised it.
  - **An example is what found this.** The first draft of `examples/resolution` demonstrated the
    application tie, and it would not reproduce.
  - `TestTags_Resolve_AmbiguityAxes` keeps both rows and now says what each one proves: the type row
    is a route behaviour, the application row is a code-preservation case asking whether a code that
    ARRIVES in an envelope is surfaced verbatim whatever raised it. Its earlier comment read that
    fixture as evidence the route emits one.
  - The same claim stands in the `2.0.0-alpha.1` entry below and is **left alone**. A released
    changelog entry records what was believed when it shipped; this entry is the correction that
    supersedes it, and rewriting history would remove the only trace that the SDK ever said
    otherwise.
- **`AuditLog.RequestID` no longer claims the SDK cannot send one.** Its doc comment still said
  "this SDK does not send one yet (#5)", which stopped being true when `WithRequestID` shipped —
  and the smoke test has been asserting the caller-supplied id lands in `audit.request_id` ever
  since. `examples/audit-logs` demonstrates the correlation the comment denied.

### Changed
- **BREAKING: every field of `TagUpdate`, `VocabularyUpdate`, and `TagAliasUpdate` is now an
  `Optional[T]`, so a PATCH can say `null`**
  ([#64](https://github.com/octoverse-id/octonomy-go/issues/64)). New exported surface in
  `optional.go`: the generic type `Optional[T]` with `Set`, `Null`, `IsZero`, `IsNull`, and `Get`.
  Field types change on three exported structs and `octonomy.String` / `octonomy.Bool` stop being how
  those structs are filled — `octonomy.Set(v)` replaces both. `*Create` structs and every
  `*ListParams` are untouched and still take the pointer helpers.

  ```go
  octonomy.TagUpdate{Name: octonomy.Set("Autumn")}       // {"name":"Autumn"}    — set it
  octonomy.TagUpdate{ParentID: octonomy.Null[string]()}  // {"parent_id":null}   — clear it
  octonomy.TagUpdate{}                                   // {}                   — touch nothing
  ```

  - **The request was inexpressible, not merely awkward.** Every optional field was a `*T` with
    `omitempty`, where `nil` meant "leave this one alone" — which spends the pointer's one spare
    state on absent-versus-set and leaves no value a caller can put in the struct that sends
    `"parent_id": null`. **A caller could not un-nest a tag, detach it from a vocabulary, or remove a
    description through this SDK at all.** This is the sibling of
    [#37](https://github.com/octoverse-id/octonomy-go/issues/37), which hit the same wall on
    `Metadata` and could still solve it by *adding* a pointer; here the pointer was already spent.
  - **Nothing was previously lying, and the severity is not being overclaimed.** `nil` left the field
    alone, which is exactly what it did; no call silently succeeded and nothing reported a clear that
    had not happened. What was missing was a way to say the third thing.
  - **Four fields are clearable, and the set is the server's rather than this SDK's.** The vendored
    v2 contract marks seven properties `nullable: true` across the three patch schemas; three of the
    seven are `application_id`, refused on every resource as a scope change, so the reachable set is
    `TagUpdate.ParentID`, `TagUpdate.VocabularyID`, `TagUpdate.Description`, and
    `VocabularyUpdate.Description`. **`TagAliasUpdate` has none.** A null anywhere else is a `400`
    (`IsValidation`), and on `ApplicationID` a `409` (`IsScopeImmutable`). None of that is enforced
    client-side — re-running server validation here is out of bounds, and the server names the
    offending field in `APIError.Details`.
  - **A null `ApplicationID` on a row that is already tenant-shared answers `200`, not `409`.** The
    server refuses a scope *change*, not the literal null, so the no-op case passes. Both halves are
    asserted, because reading only the first would leave the documentation claiming a refusal the
    server does not always make.
  - **`Metadata` is emptied with `Set(Metadata{})`, never with `Null`.** `metadata` is not nullable
    on any of the three patch serializers, so `Null[Metadata]()` compiles and is always refused; so
    is `Set` of a *nil* map, which encodes as `null`. The #37 semantics are otherwise unchanged —
    and `omitzero` cannot re-open #37, because it consults `Optional.IsZero`, which reports which of
    the three states the field is in and never inspects the payload.
  - **`omitzero`, not `omitempty`, and the difference is silent.** `omitempty` never omits a struct,
    so a field tagged with it would put `"field": null` on every PATCH that does not touch it —
    clearing columns the caller never named, with a 200 and no error. Two guards, because a struct
    tag has nothing else keeping it honest: `Optional.MarshalJSON` refuses to encode an omitted value
    rather than falling back to `null`, and `TestUpdateBodiesTagEveryOptionalOmitzero` parses this
    package's own source and fails on either mistake for **any** type whose name ends in `Update`, so
    a `*Update` struct added for a future resource is covered by that test existing rather than by
    somebody remembering. Both were confirmed by mutation: each guard was watched failing against a
    deliberately broken field.
  - **`Optional[T]` compares by value where `T` is comparable** — `Set("x") == Set("x")` is true,
    which the `*string` it replaced never was. `Optional[Metadata]` is the exception, since a map is
    not comparable, which also makes the three `*Update` structs non-comparable with `==`; that is a
    compile error rather than a silently wrong answer.
  - **It carries `UnmarshalJSON` as well**, so an `Optional` recovers an absent key, an explicit
    null, and any value that does not itself encode as null — rather than decoding to nothing with a
    nil error, which is the silent-zero family of #32 and #40. A *value* whose own encoding is the
    literal `null` (`Set` of a nil map) is indistinguishable from `Null` on the wire and decodes to
    the null state; that is JSON rather than this type, and both spellings are refused by the server
    for the same reason.
  - **Why now.** A type change on an exported struct is cheap before the first stable tag and
    expensive after it — the same reasoning that made #24 blocking. This tree is
    `v2.0.0-alpha.1`, and the `-alpha.N` suffix comes off at API freeze, so this is what an alpha
    window is for. The compat line (`support/go1.13`) is unaffected: it has no generics, takes
    security fixes only, and its server contract predates this.
  - **Migration is mechanical wherever a field is assigned in Go**, and the compiler finds every
    such site: `octonomy.String(v)` → `octonomy.Set(v)`, `octonomy.Bool(v)` → `octonomy.Set(v)`,
    `&octonomy.Metadata{…}` → `octonomy.Set(octonomy.Metadata{…})`, and a field left unset stays
    unset. Every old spelling fails to compile, so nothing in that shape changes silently.
  - **The exception is a `*Update` struct you fill by DECODING JSON**, which keeps compiling and
    does change behaviour — a gateway that unmarshals an inbound patch body into `octonomy.TagUpdate`
    and forwards it is the realistic case. An incoming `{"description": null}` used to decode into a
    nil `*string` and then be **dropped** from the outgoing PATCH; it now decodes to the null state
    and clears the column. That is this issue's bug being fixed one layer further out — the caller
    asked for a clear and was silently not getting one — but it is a behaviour change the compiler
    cannot point at, so audit any code that decodes into these structs rather than assigning their
    fields.
  - **Verified against a running server**, one PATCH per field, re-reading the row after each:
    `TestIntegration_NullClearsOnlyTheNullableFields` walks all four clears, a refusal on every
    resource, and both `application_id` outcomes. `TestIntegration_ParentCycleIsReachableAndRefused`
    now breaks its ring by **clearing** the offending link rather than re-pointing it at a third tag
    — the repair an operator holding that data would actually make, and the one the old shape could
    not express. `examples/tags` and `examples/vocabularies` demonstrate the clear and were run.
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

[Unreleased]: https://github.com/octoverse-id/octonomy-go/compare/v2.0.0-rc.1...main
[2.0.0-rc.1]: https://github.com/octoverse-id/octonomy-go/compare/v2.0.0-alpha.3...v2.0.0-rc.1
[2.0.0-alpha.3]: https://github.com/octoverse-id/octonomy-go/compare/v2.0.0-alpha.2...v2.0.0-alpha.3
[2.0.0-alpha.2]: https://github.com/octoverse-id/octonomy-go/compare/v2.0.0-alpha.1...v2.0.0-alpha.2
[2.0.0-alpha.1]: https://github.com/octoverse-id/octonomy-go/releases/tag/v2.0.0-alpha.1

<!-- Every link here points at THIS line. The compat line is a different module with its own
     versions and its own copy of this file on support/go1.13, so a link to v1.0.0 from
     here would compare a consumer of /v2 against code they cannot install.
     There is still no [0.1.0] link definition, because that tag does not exist: the
     section it used to head is now filed under this release, where its contents were
     actually published for the first time. -->
