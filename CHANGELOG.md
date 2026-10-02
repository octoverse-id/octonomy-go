# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- **`main`'s test suite, accounted for file by file**
  ([#95](https://github.com/octoverse-id/octonomy-go/issues/95), for
  [epic #88](https://github.com/octoverse-id/octonomy-go/issues/88)). Tests and docs only; no
  exported symbol changes.
  - **`docs/compat-test-disposition.md`** gives each of the 27 test files in `main`'s tree at 5e40964 a
    verdict — ported, rewritten, preserved, owned by another sub-issue, or excluded with its reason —
    and writes down the `t.Cleanup` replacement model for Go 1.13. `TestDispositionTableNamesEveryTestFile`
    fails on a test file here that the table does not name.
  - **Two source-parsing guards, rewritten for this dialect** rather than ported, because `main`'s
    read response types off `doData[T]` type arguments this line does not have.
    `TestEveryResponseTypeCanRefuseAnEmptyDecode` requires every type the transport decodes into —
    read off its `&out` destination — to carry `identityFields()` on the value receiver or, for a
    declared composite, a pointer-receiver `UnmarshalJSON`; `TestTheRuntimeIdentityTablesMatchTheSource`
    holds the runtime check of each list's `rows()` to the same set.
    `TestEveryResponseTypeHasASmokeProbe` requires a `TestSmoke_` function to call a method decoding
    each of those types against a real server, and `TestSmokeSelectorRunsEveryTestSmokeFunction`
    keeps `make smoke` and the CI smoke job selecting `-run '^TestSmoke_'`. Both fail closed on a
    shape they cannot read.
  - **`main`'s test cases this line lacked**: `TestTags_Get`, `TestVocabularies_Get`,
    `TestVocabularies_List_Params` (without its `q` and `slug` cases — see below),
    `TestDoData_UndecodableBodies`, `TestTransport_ErrorsPropagateFromEveryHelper` and
    `TestMetadataIsStillAnAlias`. The `Get` fixtures are raw wire JSON, not marshalled structs.
  - The table records two capability gaps that no sub-issue owns yet: `VocabularyListParams` has no
    `Query` or `Slug`, although both vendored specs list the filters, and `DecodeMetadata` has no
    counterpart here.
- **The six missing resource groups, hand-ported from `main`**
  ([#94](https://github.com/octoverse-id/octonomy-go/issues/94), for
  [epic #88](https://github.com/octoverse-id/octonomy-go/issues/88)). `main`'s `aliases.go`,
  `resolution.go`, `assignments.go`, `resources.go` and `audit.go` at 5e40964, rewritten into the
  Go 1.13 dialect under `docs/porting-checklist.md`; the health probes, the sixth group, landed with
  the compat core below. Every operation in `docs/contract-coverage.yaml` now names a method, and
  `docs/api.md` maps each one. Nothing published in `v1.0.0` changes type or signature.
  - **New services on `Client`:** `Aliases` (`AliasService`: Create / Get / List / Update / Delete),
    `Assignments` (`AssignmentService`: Create / Remove / BulkAssign / BulkRemove), `Resources`
    (`ResourceService`: ListTags / ReplaceTags / ListAuditLogs) and `AuditLogs` (`AuditLogService`:
    List — list-only, as on the server). `TagService` gains `ListAliases`, `Resolve`,
    `ListResources` and `ListAuditLogs`.
  - **Four list types** where `main` returns `*List[T]`: `TagAliasList`, `ResourceTagList`,
    `TagResourceList` and `AuditLogList`, each with `rows()` so `doList` checks every row's identity.
  - **Three composite results**, decoded through `doData`, never `doList`: `BulkAssignResult`,
    `BulkRemoveResult` and `ResourceReplaceResult`. Both vendored specs describe these bodies
    wrongly — a bare array for bulk-assign and the replace, no schema at all for bulk-remove — so a
    decoder written from them returns an empty slice and a nil error. Each requires its own keys,
    and holds every row to the resource standard; a new smoke test asserts the real counts against a
    booted server.
  - **`TagAliasUpdate` has pointer fields and a value-receiver `MarshalJSON`**, as `TagUpdate` and
    `VocabularyUpdate` do, so `TagAliasUpdate{Metadata: Metadata{}}` sends `{}` and empties the stored
    object. `main`'s `Optional[T]` is not ported, by design.
  - **Every new model implements `identityFields()`**: `id` on most, `assignment_id` and the nested
    `tag.id` on `ResourceTag`, `resource_id` on `TagResource`, and the tag (plus the alias, when one
    matched) on `TagResolution`.
  - **The composite and resolution decoders go through `decodeJSON`**, so they are depth-bounded on
    Go 1.13 like every other response decode.
  - **`ResolutionScopeMerchant` with no namespace names the opt-in on a v1 client.** The transport's
    refusal said "add WithNamespace(...)", which on this line's default `APIV1` client leads straight
    into `WithNamespace`'s own refusal, so there it names `Config.APIVersion = APIV2` too.
  - `main`'s five test files for these groups are ported alongside, and `TestSmoke_ResourceGroups`
    and `TestSmoke_APIV2ResourceGroups` call every new method against a real server.
- **The compat core: `/api/v2` opt-in, the namespace axis, request correlation, the health probes,
  and `main`'s error vocabulary** ([#91](https://github.com/octoverse-id/octonomy-go/issues/91), for
  [epic #88](https://github.com/octoverse-id/octonomy-go/issues/88)). A hand-port of `main`'s
  `transport.go`, `octonomy.go`, `errors.go` and `health.go` at 5e40964 into the Go 1.13 dialect, under
  the rules in the new `docs/porting-checklist.md`. Every exported `v1.0.0` symbol keeps its type and
  signature; what changes behaviour is listed under *Fixed*.
  - **`Config.APIVersion`, with `APIV1` and `APIV2`, defaults to `APIV1` on this line** —
    `DefaultAPIVersion` is `APIV1` here, where `main` had `APIV2` at the commit this was ported from,
    and that is deliberate. `v1.0.0` sent
    every request to `/api/v1`, and this line can never change a default under a caller, so a client
    that sets nothing keeps speaking `/api/v1` after the upgrade. Set `APIVersion: octonomy.APIV2` to
    reach `/api/v2`; it needs an Octonomy server of 2.0 or later. `Client.APIVersion()` reports the
    surface. The `apiPrefix` constant is gone.
  - **The four scope options** — `WithNamespace`, `WithGlobalNamespace`, `WithApplication`,
    `WithIncludeGlobal` — enforced at the transport chokepoint by `checkScopeCoherence`, so every
    method inherits them. A request whose own options contradict each other, or the API version, is
    refused before anything is sent: a namespace on a v1 client (the default), a half or reserved
    namespace pair, a namespaced bodyless request with no application, `WithApplication` on a write,
    `WithIncludeGlobal` on a write or on v1. **A contradictory scope is an error, never last-wins**,
    option-versus-option and option-versus-params alike; `WithGlobalNamespace` is the one explicit
    override. `Tag` and `Vocabulary` gain decode-only `NamespaceType` / `NamespaceID`, nil on a global
    row and on every `/api/v1` response.
  - **`WithRequestID`**, send-only and per call, with `main`'s wire-grammar checks (non-blank,
    printable ASCII, no outer whitespace). The SDK never mints one and `Config` has no field for it.
  - **The health probes**: `Client.Health` and `NewHealthClient` (with `WithHealthHTTPClient` and
    `WithHealthUserAgent`), `HealthService.Live` / `Ready`, `HealthStatus`, `HealthStatusOK` /
    `HealthStatusUnavailable`. They sit outside `/api/<version>`, authenticate nobody and carry no
    data envelope, so they have a request path, a decoder and a constructor of their own; `doData`'s
    envelope requirement and `New`'s validation were not loosened to fit them. An unready server is an
    `*APIError` with `CodeNotReady` (`IsNotReady`); an unreachable one is no `*APIError` at all.
  - **`ErrUnreachable`**, wrapping every request that got no HTTP response. `errors.Is` finds both it
    and the cause — `context.Canceled`, a `*net.OpError` — through a small wrapper type, because
    `main`'s `fmt.Errorf("%w: %w", …)` returns an error with no `Unwrap` at all before Go 1.20. The
    message is the `octonomy: request failed: …` it always was.
  - **`ErrResponseTooLarge`**: response bodies are read under a 32 MiB ceiling. A non-2xx that trips
    it is still an `*APIError` (`CodeUnexpectedStatus`), and `APIError.Unwrap` lets `errors.Is` find
    the cause.
  - **`main`'s 16 error codes and 16 `Is*` helpers**, up from 8 and 5. New codes:
    `CodeScopeImmutable`, the four namespace codes, `CodeAmbiguousResolution`,
    `CodeUnexpectedStatus` and `CodeNotReady`; new helpers: `IsTenantMismatch`,
    `IsApplicationMismatch`, `IsInactiveTag`, and one per new code. `IsScopeImmutable` reports a `409`
    that `IsConflict` does not, which is the point of it.
  - Unkeyed literals: `Config`, `Tag`, `Vocabulary` gain fields, and `APIError` gains an unexported
    one, so an *unkeyed* composite literal of any of them stops compiling — the Go-level caveat
    `docs/versioning.md`'s MINOR rule describes. Keyed literals are unaffected.
- **`docs/porting-checklist.md`** — this branch's copy of the epic's porting rules, one row per rule,
  each **LOUD** (a miss fails `go build` / `go vet` under go1.13.15) or **SILENT** (it compiles and is
  wrong), and each SILENT row naming what catches it or saying nothing does
  ([#103](https://github.com/octoverse-id/octonomy-go/issues/103)). It lands with the first port that
  needs it; `AGENTS.md` points at it.
- **This line's vendored contract moves up two server majors, to 3.2.1, and the claim is held by
  tests rather than by prose** ([#90](https://github.com/octoverse-id/octonomy-go/issues/90), for
  [epic #88](https://github.com/octoverse-id/octonomy-go/issues/88)). This line had vendored a
  contract two server majors old, carried no `docs/openapi-v2.yaml`, no `docs/contract-coverage.yaml`
  and no contract-version marker, and fourteen prose sites across seven files stated which contract
  it vendored with nothing checking any of them. **No code changes**: no exported symbol, request or
  decode moves.
  - **`docs/openapi.yaml` is refreshed** and **`docs/openapi-v2.yaml` is vendored**, both
    byte-identical to `main`'s copies of the server's 3.2.1 output at 61fce9b. The v1 diff adds 53 lines and changes one: `409
    scope_immutable` on tag, vocabulary and alias `PATCH`; a `scope` query parameter on
    `/tag-resolution`; `default: true` on three `is_active` fields; and an `ErrorResponse` schema.
    None of it needed a type change on the two groups this tree then implemented — `default: true`
    documents a server default, `ErrorResponse` is the shape `APIError` already decodes, `parseError`
    already preserves `scope_immutable` verbatim, and `/tag-resolution` was not yet on this tree
    (#94 ported it, `scope` included). Nothing
    here sends a request to `/api/v2`; the spec is vendored for
    [#91](https://github.com/octoverse-id/octonomy-go/issues/91), which adds that surface opt-in.
  - **`<!-- contract-version: 3.2.1 -->`** in `docs/versioning.md`, the single recorded value.
  - **`docs/contract-coverage.yaml`**: a row for each of the 29 operations the two specs publish, as
    version-independent suffixes. When it landed, ten named the `TagService` / `VocabularyService`
    method that implements them and nineteen carried a written `unimplemented:` reason naming the
    issue that would close the gap — [#94](https://github.com/octoverse-id/octonomy-go/issues/94) for
    the five resource groups, #91 for the health probes. Both have since landed, and every row names
    a method. It carries the `operations` section only: the rest of `main`'s
    file at 61fce9b states what the client sends and decodes, which only a contract gate can prove,
    and that arrives with [#98](https://github.com/octoverse-id/octonomy-go/issues/98). The rows use
    that file's schema, so the gate #98 ports can read them as they stand: `LoadCoverage` from `main`
    at 61fce9b accepted the section, strict key checking included, and objected only to the absent
    error-code registry.
  - **`contractbaseline_test.go`** (new; `main` at 61fce9b has no counterpart, because its contract
    gate does this job) holds #90's acceptance in `make test` and the `go1.13` job: both specs'
    `info.version` equals the marker; the two publish the same operations; every published operation
    has exactly one row and every row names a published operation; each row carries an `sdk:` method
    declared on that receiver **or** a reason, never both and never neither. It reads YAML without a
    YAML library, so it takes a narrow job and takes it fail-closed. From a spec it reads only
    `info.version` and the keys at the two depths under `paths:`, refusing any line at those depths
    it cannot account for — a path-item `$ref` included, since it does not resolve references, and a
    key without YAML's space after the colon. From the coverage file it reads only the forms that
    file uses, refusing a typo'd key, a key given twice in one row, a key with no space after its
    colon, a value YAML would read as null (`unimplemented: # …` is a comment, not a reason), a
    single-quoted value with an undoubled quote inside it, and a folded block whose lines change
    indentation — each a place where a last-wins or lenient reader would see a covered row that
    yaml.v3 refuses. On the two real specs it extracts the same 29 operations yaml.v3 does. Its
    limits are written at the top of the file.
  - **`contractversion_test.go`** ports `main`'s prose guard
    ([#86](https://github.com/octoverse-id/octonomy-go/issues/86)) as it stood at 61fce9b — every
    version token in a tracked prose or source file must equal the marker or match a category
    carrying a written reason — in the Go 1.13 dialect (`filepath.Walk` and `ioutil.ReadFile`, which
    predate `io/fs`; no `min`/`max` builtins; `tc := tc` in the table loops). Three rounds of outside
    review showed stale claims passing — through words the categories accepted ("vendored *from
    server* X.Y.Z" read as a version range), through context read too narrowly, and through a
    spelling that was never a token — and the port was changed in these ways:
    - **A sentence that claims a contract is exempt only by role.** Before any category that works
      from the words around a token, the token's own sentence is read for the words that say what is
      vendored — *vendor*, *contract*, *spec*/*specs*, `openapi`, *tracks*, *targets*, *at server*,
      `info.version` and the like — and if one is there, only a category that identifies the token by
      what it IS may exempt it: an address, an image tag, a release branch's name, a URL or the
      OpenAPI format key, `version.go` or a CHANGELOG heading, a guard's own fixtures. The veto fails
      closed. Its price is that the history of this line's contract carries no old number in prose —
      "two server majors behind" — since that number is recorded where the refresh happened; the
      three sentences that carried one were reworded. The failure message says when the veto fired,
      and that the fix is a rewording, never a looser category or a ByRole one.
    - **Word-based categories read the token's sentence**, not only a byte window. A list item —
      bulleted or numbered, in or out of a blockquote — is a statement of its own, and so is a
      Markdown heading. A sentence runs to its paragraph's edges in both directions, and never past
      a blank line or a bare `//`, `#` or `>` (a blank line in its own syntax), nor across the
      boundary between a comment and the code beside it, in a source file or a fenced block; a YAML
      block scalar's `#91 …` line is read as the content it is. An abbreviation's full stop ("e.g.")
      does not end a sentence; a link's target is not a word the sentence says.
    - **A `v`-prefixed version is a token.** `main`'s copy never saw one, so "Both bundled specs
      target server vX.Y.Z" went unread — and the server's own tags are spelled `v3.2.1`. A word-based
      `v-tag` category classifies this module's tags, and is not offered a sentence about the server,
      its API, or Octonomy by name (`octonomy-go`, this module, excepted).
    - **Every category is as narrow as a site here needs.** `server-history` is exactly one shape,
      "Server X added"; `sdk-version` needs the version in a code span as well as its phrase; the
      harness pin is a token immediately after `octonomy:`; an address needs a digit before its dot.
      Each historical bypass is also checked against its category with the veto out of the way, so
      a re-loosened category cannot hide behind the veto.

    The categories are this tree's. The six #90's scoping named as porting unchanged are kept, less
    the phrases review showed loose (`"from server"`, a bare `"go "` that matched inside
    `version.go`, the harness pin's nearby words). `dated-decision-record` and `compat-line-contract`
    are dropped, since nothing here needs them. New: `release-line-guard`, for
    `scripts/compat-guard.sh` and its fixture suite, path-scoped and therefore subject to the veto;
    `release-branch-name`; and `v-tag`. It reads the files git tracks (`git ls-files`), not whatever
    else is in the checkout, so a local untracked or ignored note cannot fail anyone's `go test`;
    where git cannot answer — the module cache has no `.git` — it walks the tree instead, reading
    more rather than less. `code-review/` is out of scope, since AGENTS.md forbids committing
    anything in it. The by-value trap `main`'s guard warned about is concrete here. This
    line's first release is `v1.0.0`. Until this change the contract it vendored carried the same
    three numbers, so a rule exempting SDK versions by value could not have been written.
  - **The prose follows the marker.** The order the issue set — marker, guard, prose — let the guard
    report the refresh complete: its first run flagged `doc.go`, `docs/api.md` and
    `docs/development.md`. Sentences naming the contract with no version in them are invisible to it
    and were found by reading: `AGENTS.md`, `CONTRIBUTING.md`, `docs/api.md`, `docs/architecture.md`,
    `docs/development.md`, `docs/release.md`, `docs/roadmap.md`, `docs/versioning.md`, the PR template,
    the feature-request template, and one `transport.go` comment. The contributor instructions now
    say what a refresh has to move, and which tests fail until it does.
  - **Mutation-tested**, since on a correct tree both files pass whether or not they work. Each of 71
    mutations failed the suite and was reverted: stale claims reintroduced in five files; either spec
    off the marker; the marker moved, removed or duplicated; surface parity broken; coverage rows
    deleted, misnamed, silent, doubled, typo'd, commented out or misquoted; and, in both guards'
    code, every layer above disabled or re-loosened one at a time. Several first PASSED, and each
    was a finding: a layer masked by another (the veto hiding a re-loosened category; "contract" as
    a claim word stopping a case written for "specs"; a wrapped claim whose first line already
    carried a claim word), fixed by a case only that layer can stop — or, twice, a layer no test
    could miss because another did its job: the rule that a sentence must start with a capital,
    made redundant by handling abbreviations by name, and a blank-line break in the sentence regex
    that duplicated the paragraph bound. Both were removed, so each job lives in one place.
  - **The harness image stays `ghcr.io/octoverse-id/octonomy:3.1.0`.** It is pinned independently of the contract, and moving it
    is a change to what the smoke test proves, not to what the SDK is written against; `main` moved
    its own in a separate change. `docs/development.md` now says the two are different numbers and
    where each is written, instead of calling the harness newer than the contract.

### Changed
- **The freeze is withdrawn: this line now takes capability parity with `main`**
  ([#89](https://github.com/octoverse-id/octonomy-go/issues/89), for
  [epic #88](https://github.com/octoverse-id/octonomy-go/issues/88)). `v1.0.0` shipped under a
  published "security fixes only" policy — no features, no ordinary bug fixes, no `/api/v2`, no
  namespaces, no webhooks. That policy is reversed, deliberately and with the sunset unchanged: the
  line takes security fixes, bug fixes, and ports of what `main` already has, through
  **2027-08-31**. **No code changes in this entry**; it moves the policy, and the guard enforcing it,
  so that the work the epic plans can land here at all. Before it, `AGENTS.md` instructed an agent to
  refuse that work, and `scripts/compat-guard.sh` would have failed the `v1.1.0` release PR it ships
  in.
  - **Two things stay out, and are now written as policy rather than as terms of a freeze.** No
    webhook receiver — a consumer needing one moves to `/v2`. And **no major, ever**: the module path
    is unsuffixed, Go accepts only `v0`/`v1` versions on it, and `.../v2` is `main`'s module. So every
    change here has to keep `v1.0.0` callers compiling — unkeyed struct literals aside, a caveat
    `docs/versioning.md`'s MINOR rule now carries on this branch too — and two decisions follow for the
    port: the `*Update` structs keep their pointer fields rather than taking `main`'s `Optional[T]`
    (so PATCH still cannot clear a nullable field here), and `/api/v2` arrives opt-in.
  - **`scripts/compat-guard.sh` refuses a major instead of a minor.** `compat_minor_violation`,
    which failed every `v1.x.0` at both of its call sites — the release-PR block and the tag-push
    block — is replaced by `compat_major_violation`, which fails a `v2+` release PR into this
    branch, or a `v2+` tag on a tree carrying this line's module path, and says why by name. Go
    already rejected such a tag as a path mismatch, but that message reads as an instruction to
    suffix `go.mod`, which would put `main`'s module on this branch. The major is compared as a
    string, since a numeric test overflows on a long major and reports no violation. One gap is
    recorded rather than closed: a tag pushed on a commit whose `go.mod` had already been suffixed
    away from this line's path is not caught, because a tag push has no base branch. The original
    guard did not catch it either, and the guard's comment says so.
  - **`scripts/compat-guard-test.sh`** flips the two `v1.1.0` cases from BLOCK to pass, adds a local
    run on `release/v1.1.0` (#89's acceptance case), and adds five major refusals, including a compat
    branch whose `go.mod` was suffixed to `/v2`. 85 checks, up from 78. Each guard change was
    mutation-tested: disabling the major check, removing either call site, dropping the release
    site's compat-line condition, restoring the minor refusal, and switching to a numeric comparison
    each fail at least one case.
  - **The documentation says so everywhere it said the opposite** — `AGENTS.md`, `README.md`,
    `SECURITY.md`, `CONTRIBUTING.md`, `doc.go`, `docs/versioning.md`, `docs/release.md`,
    `docs/development.md`, `docs/roadmap.md`, `docs/api.md`, `docs/architecture.md`, the CI and
    integration-test comments, the PR template, and a supersession banner on the old design doc.
    `AGENTS.md` gains a *Porting from `main`* section naming the three porting mistakes that compile
    clean on go1.13.15: a leftover `omitzero`, a second `%w`, and main-dialect doc comments.
  - `docs/architecture.md`'s transport diagram names `apiPrefix` beside `/api/v1`, so the change that
    replaces that constant finds the diagram with a search, and its first rows are realigned.
  - Released entries below are left as they were written, including the `1.0.0` guard description
    that calls a compat-line minor policy-invalid — that is what was true when it shipped.

### Fixed
- **`IsNotFound` no longer reports true for a bare 404**
  ([#91](https://github.com/octoverse-id/octonomy-go/issues/91)). This line's first release mapped
  any non-2xx that arrived without the Octonomy error envelope to a code by its status
  (`codeFromStatus`), so a 404 from a wrong `BaseURL`, a path-stripping proxy, or a server with no
  route for the requested surface satisfied `IsNotFound`, and a caller's ordinary "that tag doesn't exist" branch read an
  infrastructure failure as an empty taxonomy, with no error. That mapping is **removed, on both API
  versions**: an envelope-less non-2xx now carries `CodeUnexpectedStatus` (`IsUnexpectedStatus`),
  with the raw body in `Message` and `StatusCode` unchanged. The same holds for the other four codes
  it used to infer — a bare 400, 401, 403 or 409 no longer satisfies `IsValidation`, `IsAuthError`,
  `IsForbidden` or `IsConflict`. An enveloped response keeps the server's code verbatim, so a real
  Octonomy `not_found` is still `IsNotFound`. **What to check:** code that relied on `IsNotFound` (or
  the other four) for a response Octonomy itself did not write — a gateway's 404, for instance — now
  sees `IsUnexpectedStatus` instead. This is the same change `main` made in
  [#7](https://github.com/octoverse-id/octonomy-go/issues/7); it ships here as a fix, which the
  compat policy allows to change behaviour but not a signature, a field type or a default.
- **`TagUpdate{Metadata: Metadata{}}` now empties the stored metadata**
  ([#37](https://github.com/octoverse-id/octonomy-go/issues/37), on this line). `Metadata` with
  `omitempty` counts an empty map as empty, so `v1.0.0` sent no `metadata` key at all and a caller
  clearing the object got a 200 with the old object still in place. `TagUpdate` and
  `VocabularyUpdate` gain a value-receiver `MarshalJSON`: a nil `Metadata` still omits the key, a
  non-nil empty one sends `"metadata":{}`, a populated one replaces the object. Every other field,
  and the order of all of them, goes out byte-identical to `v1.0.0`; the field types do not change.
  (Clearing any *other* nullable field still cannot be expressed here — the pointer fields have no
  third state, and `main`'s `Optional[T]` is not ported, by design.)
- **A 2xx whose payload is not a resource is an error, not a zero value**
  ([#40](https://github.com/octoverse-id/octonomy-go/issues/40)'s class). `{"data": {}}`,
  `{"data": {"id": null}}`, a renamed `id`, and a list row that is `null`, `{}` or id-less all used
  to decode into a zero-valued `Tag` or `Vocabulary` with a nil error; each is now an error naming
  the position, and the list index of a bad row.
- **Response bodies are bounded.** `v1.0.0` read every body with an unbounded `ioutil.ReadAll`, so
  one response could grow the client's memory without limit. The ceiling is 32 MiB
  (`ErrResponseTooLarge`, above).
- **Two Go 1.13 hazards no longer reach the process.** Go 1.13's `encoding/json` has no cycle
  detection and no depth limit: probed on go1.13.15, a `Metadata` that contains itself hangs
  `json.Marshal`, and a deeply nested response decodes until the stack is exhausted — a fatal runtime
  error, not a panic, so nothing can recover it. A request body is now walked before it is encoded —
  following what `encoding/json` follows, so a field tagged `json:"-"` or a caller's own
  `json.Marshaler` is not walked (one conservative difference, embedded-field dominance, is recorded
  in `jsondepth.go`) — and a response is scanned before it is decoded, each refused past 10,000
  levels: the limit the standard library adopted once it had one, so the two lines refuse the same
  responses. Both are proven
  in child processes, since the unguarded case cannot be observed from inside the process it kills.
  `main` had no counterpart when this was written: a modern `encoding/json` detects encoding cycles
  and bounds decoding depth itself.
- **A list whose `pagination` block is unusable is an error.** The first release required only that
  the key be present and not null, so `{"data": [], "pagination": {}}` decoded to a page with
  `Limit` 0 and `Count` 0 — "one page, nothing after it" to a caller paging on `Count` — with a nil
  error. A real response always carries `limit` of at least 1 (the server falls back to 50), so a
  block below that is now refused, as `main` refuses it. `"data": null` still decodes to a nil slice,
  as it did.
- **`Delete` requires the `204 No Content` Octonomy answers it with.** The first release accepted
  any 2xx and discarded the body, so a `200 {"data": {...}}` — a host that is not Octonomy answering
  at the `BaseURL`, or a helper routed to the wrong call — reported a deactivation that may not have
  happened, with a nil error. Any other status, or a body on a 204, is now an error (not an
  `*APIError`: nothing said no). Octonomy answers every `DELETE` with 204 and no body.
- **A nil `RequestOption` is an error, not a panic.** `v1.0.0` called every option unconditionally.
- **A path segment is escaped once.** The first release assigned an already-escaped path to
  `url.URL.Path`, so `String()` escaped it again and an id of `a b` reached the server as the literal `a%20b`. Tag and
  vocabulary ids are server-minted uuids, which never need escaping, so no call this tree could then
  make was affected; the fix landed with the transport port because the resource groups #94 later
  ported address rows by caller-chosen ids.
- **The `vuln` CI job stopped running govulncheck at all, and took every merge with it.**
  `golang/govulncheck-action` installs `golang.org/x/vuln/cmd/govulncheck@latest` and offers no
  version input, while `actions/setup-go` exports `GOTOOLCHAIN=local` so the pinned Go really is the
  Go used. When x/vuln v1.8.0 (2026-09-08) raised its own minimum to go 1.26, the install began
  failing on `requires go >= 1.26.0 (running go 1.25.x; GOTOOLCHAIN=local)` and the scan never ran.
  It broke on this line silently — the previous CI run here was 2026-08-26, cutting `v1.0.0` — and
  because `vuln` is a required context on `support/go1.13`, a dark scanner was also blocking the
  security fixes this line exists to receive. The job now installs with `GOTOOLCHAIN=auto` (which
  applies to *building* the tool; the scan still uses the pinned Go, so standard-library advisories
  stay reported against the version under test) and invokes `govulncheck` directly, so a failed
  install is a failed step rather than a skipped scan. No advisories against this module: the 18
  findings seen while reproducing were artifacts of a local go1.25.4, all fixed at or below
  go1.25.13, and CI resolves `"1.25"` to a later patch. Same one-line fix applied to the install
  hints in `docs/development.md` and the `Makefile`, which reproduce the identical error on a go1.25
  toolchain. Ports [#45](https://github.com/octoverse-id/octonomy-go/pull/45) to this line (#70).
- **Documentation only; no code change, and no version bump or tag is planned for it.** This branch
  carried its own copies of `main`'s documentation, written when `main` was a `/api/v1`-only client
  with two resource groups, and they had aged into false statements about it — most harmfully a line
  in `docs/versioning.md` telling a reader **not** to adopt the `/v2` module if they wanted REST v2
  endpoints, which is now backwards. Descriptions of the modern line are replaced by links to `main`'s
  own files, so the same drift has nothing left to attach to (#51). Note the reach: pkg.go.dev
  renders the README of the *released* version, so `v1.0.0`'s rendered page keeps the old text until
  some other fix cuts a `v1.0.1`. This corrects what a reader sees on GitHub, which is where the two
  lines get compared.
- **CI comments no longer paraphrase branch protection.** Two comments in `.github/workflows/ci.yml`,
  and the matching prose in `docs/development.md` and in this file's `1.0.0` entry, described the
  `go1.13` and smoke jobs as required by intent but unenforced. The contexts had since been added,
  and an automated reviewer on PR #50 read one of those comments and filed a finding against
  documentation that was correct. The reasoning each comment exists for is kept; the claim about
  mutable repository settings is gone, since no file in the repository can observe them (#52).

## [1.0.0] - 2026-08-26

**The first published release of this SDK**, and the whole of the frozen Go 1.13 compatibility line
(`support/go1.13`, module `github.com/octoverse-id/octonomy-go`).

No `0.x` version was ever released — no tag was cut and the module proxy served nothing — so this
entry covers both the original client and the Go 1.13 conversion that made it installable on the
toolchain this line exists for. The `### Changed` and `### Fixed` sections below describe deltas
against the unreleased tree and against the `/v2` line on `main`; they are written for a reader
arriving from `main`, since no consumer can have been running an earlier version of *this* module.

**This release cannot be recalled.** `retract` shipped in Go 1.16, so a Go 1.13 consumer's toolchain
ignores it, and `GOPROXY` caches tags permanently.

### Changed
- **The client now compiles and tests on real Go 1.13** (#4). `go.mod` declares `go 1.13` and the
  **unsuffixed** module path `github.com/octoverse-id/octonomy-go`; `main` keeps `/v2`. Go treats the
  two paths as different modules, so nothing can move a consumer between the lines.
- **`List[T]` is gone.** Type parameters need Go 1.18. Each resource declares its own list envelope
  with identical fields:

  | Before (`/v2`, Go 1.18+) | After (this line) | Returned by |
  | ------------------------ | ----------------- | ----------- |
  | `List[Tag]` | `TagList` | `Tags.List` |
  | `List[Vocabulary]` | `VocabularyList` | `Vocabularies.List` |
  | `Pagination` | `Pagination` *(unchanged, shared)* | both |
  | `ListOptions` | `ListOptions` *(unchanged, shared)* | both |

  Both new types keep `Data []T` and `Pagination Pagination`, so only the type name in a variable
  declaration or function signature changes. `page.Data` and `page.Pagination.Count` are untouched.
- `Metadata` is `map[string]interface{}`; `any` is replaced by `interface{}` throughout (5 library
  sites, 18 in tests). Same type, Go 1.13 spelling — no caller change.
- `io.ReadAll` → `ioutil.ReadAll` at all five call sites (1 library, 4 test). Internal only.
- Tests: `newTestClient` returns `(*Client, func())` and callers `defer cleanup()` at all 14 sites.
  `t.Cleanup` needs Go 1.14. Test-only.

### Fixed
- **`Create`, `Get`, and `Update` decoded every response into a zero-valued struct against a real
  server** — silently, with a nil error. The server wraps single resources in `{"data": {...}}`
  (`octonomy/core/responses.py`, present since its first release) and the client decoded the wrapper
  straight into `*Tag`/`*Vocabulary`, so nothing matched and nothing complained. Lists were unaffected
  because their envelope has a `Data` field.

  `Client.doData` now unwraps the envelope, and a 2xx body with no `data` key is an error instead of an
  empty struct. Found by the new integration smoke test on its first real run; the httptest suite had
  encoded the vendored spec's bare-object shape rather than the server's, which is why it passed.
- **List responses had the same silent-zero trap one type further out.** Decoding straight into a
  `*TagList` turned an empty body, a `{}`, or a response whose `data` key the server renamed into a
  nil `Data` slice and `Count: 0` — indistinguishable from a genuine empty page. `Client.doList` now
  requires **both** envelope keys before decoding: a missing or null `pagination` block zeroes `Count`
  and `Limit`, which a caller paging on `Count` reads as "one page, nothing after it". A real empty
  page (`"data": []` with a pagination block) is still a success, and `"data": null` is still accepted
  as a nil slice — this line preserves the wire form rather than normalizing it, and that behavior is
  frozen with the rest.
- **A 2xx with an empty body where a resource was expected is now an error.** `do` previously
  returned nil in that case, so a truncated or misrouted 2xx produced a zero-valued struct. `Delete`
  is unaffected: 204-with-no-body is its documented shape and stays lenient.

  Transport is now one request path plus one decoder per response shape — `doRaw` performs the call;
  `doData`, `doList`, and `do` decode a single resource, a list envelope, and nothing respectively.
  Picking the wrong one is the mistake that started this entry, so `AGENTS.md`, `docs/architecture.md`,
  and the resource recipe in `docs/roadmap.md` now say which to use.

### Added
- **Integration smoke test** (`integration_test.go`, build tag `integration`, `make smoke`) — five
  assertions against a real server via the container harness: both response envelopes, both list
  endpoints, and one real error envelope. Gated on `OCTONOMY_TEST_BASE_URL`, so the default test run
  stays hermetic. CI sets `OCTONOMY_SMOKE_REQUIRED=1`, which turns a missing base URL into a failure
  rather than a skip — a skip is a green job, and this is the only real-server check the line has.
- **CI for this line, which previously had none.** `ci.yml` fired only on `main`, so pushes to
  `support/go1.13` ran nothing and no tag triggered anything. Added: the branch to both trigger lists,
  a `v*` tag trigger, a real `go1.13.15` job running `go test -race` (not merely
  `go build`, since four of five `ioutil` sites and all 18 test-file `interface{}` sites live in
  `_test.go`), and a `smoke` job that boots the pinned container and runs the smoke test on the
  go1.13 toolchain. Both are meant to block a merge — the `go1.13` job is this line's only gate on the
  stdlib floor, and the smoke job its only check against a real server — while what is *enforced* is
  branch-protection state that lives in the branch's settings, not in a file here. The go1.13 jobs set
  `cache: false` — `actions/setup-go` derives its cache
  path from `go env GOMODCACHE`, which does not exist before Go 1.15.
- **Release-line guard** (`scripts/compat-guard.sh`, `make compat-guard`, CI job) with three tiers,
  chosen by how recoverable the mistake is at that moment:

  **On an ordinary branch or PR**, one blocking check: `go.mod` must keep declaring `go 1.13`. That is
  the mistake with no toolchain backstop — a `v1.x` tag cut from a drifted `go.mod` keeps the same
  module path, so Go resolves it happily and `retract` (Go 1.16) cannot reach a Go 1.13 consumer.
  Module-path and branch consistency only warn here, because Go rejects a path mismatch itself. The
  guard also warns when `main` is found carrying this line's module path or `go` directive, the shape
  a cherry-pick that included `go.mod` would take.

  **On a release PR** (`release/vX.Y.Z`), six more blocking checks — the last point where the answer is
  still "push another commit":

  1. the branch names a valid version;
  2. `version.go` equals it;
  3. the module path is **exactly** the one that major publishes under — a matching major is not
     enough, since `example.test/wrong` and an illegal `/v1` suffix both carry a v1-shaped major, and
     Go forbids `/v0` and `/v1` outright;
  4. it targets the branch that major is cut from — a `v1` release PR retargeted at `main` otherwise
     passes every other check;
  5. it is a version this repository publishes: no `v0` (neither line does), and no compat-line minor,
     because `docs/versioning.md` says `v1.0.x` and a minor means an API addition to a frozen line;
  6. the latest `CHANGELOG.md` heading agrees — and a release PR that *deletes* the changelog fails
     rather than skipping the check.

  Item 6 closes a real gap: `make version-check` ties `version.go` to the changelog, but nothing in CI
  ran it — only `release-check` does, and CI never calls `release-check`.

  **On a tag push**, a *subset* runs again as **detection, not prevention**: the tag's SemVer, its
  module path, `version.go`, and the v0/minor policy. Not the CHANGELOG heading and not the base
  branch — a tag push has no base branch to compare. GitHub already has the ref by then and a
  published version cannot be withdrawn for this audience; the value is catching a tag pushed without
  a release PR early enough that deleting the ref may still beat the first fetch. Tags are validated
  against the real SemVer grammar, not a shell glob — the obvious `v[0-9]*.[0-9]*.[0-9]*` pattern
  accepts `v1.2.3foo`, `v1.02.3`, `v1.2.3-`, and `v1.2.3.4`, none of which Go can resolve.
- **Tests for the guard** (`scripts/compat-guard-test.sh`, `make compat-guard-test`, run in CI and by
  `make check`). 78 checks: ordinary PRs, release PRs to both lines, tag pushes, malformed tags,
  wrong-module-path releases, `v0`, compat-line minors, no-event-context runs against a real git
  fixture, `go.mod`/`version.go` parsing edge cases, and the SemVer grammar exercised directly against
  the guard's own function. Every guard execution asserts the *reason*, not just the exit status — an
  rc-only assertion also passes when the code path never ran; the grammar cases assert accept/reject,
  which is all they test. Without these the release-PR block would first execute on the release PR
  itself: every ordinary run has `GITHUB_HEAD_REF=<type>/<issue>-…`, so that path is never otherwise
  reached.
- `make test-go113` for the real-toolchain gate locally, with two documented ways to fetch a go1.13
  toolchain (`docs/development.md`).
- `make tools-check`, now a prerequisite of `release-check`. `make lint` and `make vuln` skip silently
  when their tool is absent, which is right day to day and wrong for a release gate that then prints
  "release-check passed" having run neither.
- Documented support policy for this line across `SECURITY.md`, `README.md`, `docs/versioning.md`,
  `docs/development.md`, `docs/release.md`, and `AGENTS.md`: security fixes only, **sunset
  2027-08-31** (12 months from the `v1.0.0` tag, owned by the SDK maintainer), the frozen scope, and
  what is deliberately absent — including `CodeScopeImmutable`, for which `apiErr.Code ==
  "scope_immutable"` is the documented workaround.

### Added (inherited from `main`)
- Reusable Octonomy container harness (`scripts/octonomy-harness.sh`, `make dev-server`) that boots
  Postgres plus the pinned `ghcr.io/octoverse-id/octonomy:3.1.0` image, applies migrations, mints a
  namespace-capable service token, and writes `OCTONOMY_TEST_*` credentials to
  `.octonomy-harness.env`. It is this line's only bootstrap — nothing else here boots a server.
  Exposed to CI as the `.github/actions/octonomy-harness` composite action.

### Added (the original client, carried in from the never-released tree)

Targets the stable Octonomy REST **v1** API, served under `/api/v1`. Dependency-free — standard
library only. This section was previously filed under a `## [0.1.0] - 2026-06-08` heading describing
a release that was never cut; the label is corrected here, per #24 and #29, rather than left implying
an installable version that never existed.

- Client foundation: `New(Config)` with `BaseURL`/`Token`/`TenantID` validation, a configurable
  `*http.Client`, and a shared transport that sets `Authorization`, `X-Tenant-ID`, optional
  `X-Actor-ID`, `Accept`, and `User-Agent` headers.
- Typed error handling: `*APIError` decoded from the `{error:{code,message,details,request_id}}`
  envelope, error `Code*` constants, and `IsNotFound`/`IsConflict`/`IsValidation`/`IsAuthError`/
  `IsForbidden`/`AsAPIError` helpers.
- Pagination: the `{data, pagination}` envelope plus `ListOptions`. (Originally a generic `List[T]`;
  see the type mapping under **Changed** for what this line ships instead.)
- `Vocabularies` service: Create, Get, List, Update, Delete.
- `Tags` service: Create, Get, List (full filter set), Update, Delete.
- `WithActor` per-request option, and `String`/`Bool`/`Int` pointer helpers for optional fields.
- Runnable `examples/quickstart` program and a vendored `docs/openapi.yaml` contract reference.

[Unreleased]: https://github.com/octoverse-id/octonomy-go/compare/v1.0.0...support/go1.13
[1.0.0]: https://github.com/octoverse-id/octonomy-go/releases/tag/v1.0.0

<!-- Both links point at THIS line. `main` is a different module (/v2) with its own
     versions, so an [Unreleased] link to main's commits would compare a consumer of
     v1.0.0 against code they cannot install. -->
