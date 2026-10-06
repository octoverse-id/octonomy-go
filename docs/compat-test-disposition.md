# Test disposition — `main`'s test files on `support/go1.13`

One row per `_test.go` file in
[`main`'s tree at 5e40964](https://github.com/octoverse-id/octonomy-go/tree/5e40964), each with a
verdict and, where it is not ported as-is, the reason
([#95](https://github.com/octoverse-id/octonomy-go/issues/95), part of epic
[#88](https://github.com/octoverse-id/octonomy-go/issues/88)). It describes `main` **at that
commit**, never as it is now: a test file `main` gains later gets a row when a port compares against
a later commit.

The table exists because "port the mechanical tests" hides three rewrites and would silently destroy
work. A source-parsing guard carried over by transform can compile here and then assert about the
wrong dialect, so that whether it passes says nothing about the code — and this repository has
shipped that class of failure twice (#32 and #40 both survived a complete unit suite). Each row makes the choice a decision with a reason
attached. The per-file rewrite rules themselves are [`porting-checklist.md`](porting-checklist.md);
this table says which file meets which, and what became of it.

## Verdicts

| Verdict | Meaning |
| ------- | ------- |
| **PORTED** | Copied from `main` and rewritten by the porting checklist. Names the PR and the file it lives in here. |
| **REWRITTEN** | A source-parsing guard whose `main`-dialect assumptions make a port compile and assert nothing. Written anew for this dialect, keeping `main`'s rule and its fixtures. |
| **PRESERVE** | This line's own file predates the port and tests this line's `Config`, constructor and transport. It is kept, never overwritten; `main`'s cases it lacks are added beside its own. |
| **OWNED BY #N** | A port another sub-issue carries, because the code it tests arrives there. |
| **EXCLUDE** | Not ported, with the reason written down. |

## 21 or 22

#95 counts 21 files. `main` at 5e40964 has **22** in its root package and 5 more outside it
(`tools/contractdrift`, and four under `webhook/`): `contractversion_test.go` landed on `main` in
61fce9b, the day after #95 was filed. All 27 have a row below.

## The root package

Line counts are `main`'s at 5e40964. "Rules" are the porting-checklist rows the file meets on `main`;
every file also meets `any` → `interface{}`.

| `main` file | Lines | Verdict | Here | Rules met, and what became of them |
| ----------- | ----: | ------- | ---- | ---------------------------------- |
| `aliases_test.go` | 507 | **PORTED** in #113 (#94) | `aliases_test.go` | `Optional` → pointer helpers; `io.ReadAll` → `ioutil.ReadAll` |
| `assignments_test.go` | 733 | **PORTED** in #113 (#94) | `assignments_test.go` | `io.ReadAll`. The sole unit coverage of the composite envelopes the specs misdocument as bare arrays. `TestAssignments_OnV1` is `TestAssignments_OnEitherSurface` here, since v1 is the default |
| `audit_test.go` | 571 | **PORTED** in #113 (#94) | `audit_test.go` | `url.Values.Has` → `_, ok := q[k]` |
| `contractversion_test.go` | 1,032 | **EXCLUDE** — not a port target | `contractversion_test.go` (this line's own) | This line has its own prose guard since #109 (#90), written against its own tree and marker. Porting `main`'s would replace a guard that reads this branch with one that reads the other |
| `errors_test.go` | 483 | **PORTED** in #112 (#91) | `errors_test.go` | `Optional`. `main`'s two 404 tests are one table here, `TestParseError_Bare404IsNotANotFoundOnEitherSurface`, asserted on **both** surfaces — v1 is where this line's first release mapped a bare 404 to `CodeNotFound` |
| `health_test.go` | 549 | **PORTED** in #112 (#91) | `health_test.go` | `t.Cleanup` ×5 → `newTestHealthClient` returns its cleanup (see the model below) |
| `identityfields_test.go` | 637 | **REWRITTEN** here (#95) | `identityfields_test.go`, `sourceguard_test.go` | `doData[T]` / `doList[T]` type-argument resolution; `ast.Unparen` (1.22); `io/fs` (1.16). See *The three rewrites* |
| `integration_harness_test.go` | 500 | **PORTED** here ([#97](https://github.com/octoverse-id/octonomy-go/issues/97)), less the helpers only excluded tests call | `integration_harness_test.go` | `t.Cleanup` ×1 in `seed`, a helper whose rows must outlive it → rule 3 of the model, the root-owned `teardown` stack; `io.ReadAll` went with `rawPost`, which only `TestIntegration_AssignmentIdempotence` calls; both build-constraint lines kept. `main`'s clients rely on its `/api/v2` default and set no `APIVersion`; here they set `APIV2`, or every namespaced read is refused client-side (the porting checklist's row for it). `rawPost`, `namespaceInjectingTransport`, `detailStrings` and `joinDetails` serve only excluded tests and are not ported |
| `integration_suite_test.go` | 2,323 | `readProbes` and the isolation tests: **PORTED** here (#97). The rest: **EXCLUDE** | `integration_suite_test.go` | `t.Cleanup` ×10, none in the ported tests (their rows come from `seed`); `Optional` (`Set(…)` / `Null[…]`) on 23 lines, none in the ported part; `List[…]`. Per test in *integration_suite_test.go* below |
| `integration_test.go` | 1,701 | **PRESERVE** this line's own | `integration_test.go` | `t.Cleanup` ×1, on the root test (`smokeState.deleteLater`). The shapes it covers landed in #112 (v2 namespace, health, `Metadata{}`) and #113 (the resource groups). `main`'s `smokeProbes()` registry is **not** ported; see *integration_test.go* below |
| `octonomy_test.go` | 952 | **PRESERVE** this line's own; `main`'s cases **PORTED** to `transport_test.go` | `octonomy_test.go` (351 lines, this line's), `transport_test.go` | `t.Cleanup` ×4; `Header.Values` (1.14) → `r.Header[http.CanonicalHeaderKey(k)]`; `List[…]`. Per test below |
| `optional_test.go` | 406 | **EXCLUDE** | — | Everything it asserts is `Optional[T]` and its `omitzero` tags, and neither exists here: `Optional` is deliberately not ported, because the `*Update` structs keep their published pointer fields (`AGENTS.md`, *Porting from `main`*). What it guards is covered for this dialect by `update_test.go` (#112) and, for the source half, by `updateguard_test.go` ([#96](https://github.com/octoverse-id/octonomy-go/issues/96)) |
| `pagination_test.go` | 536 | **OWNED BY** [#99](https://github.com/octoverse-id/octonomy-go/issues/99) | — | All 14 tests are `TestEach_*`, and `Each` arrives with #99. `t.Cleanup` ×1, in a test body (rule 2); `List[…]` ×8 → the per-resource lists |
| `readprobes_test.go` | 1,223 | **REWRITTEN** here (#97) | `readprobes_test.go` | A source-parsing guard reading the verb off `doData[T](ctx, c, method, …)`, argument 2; here every transport helper is a client method with the verb at argument 1. `ast.Unparen` (1.22), `ast.IndexListExpr` (1.18), `io/fs` (1.16). See *The three rewrites* |
| `resolution_test.go` | 613 | **PORTED** in #113 (#94) | `resolution_test.go` | `url.Values.Has` ×2 |
| `resources_test.go` | 641 | **PORTED** in #113 (#94) | `resources_test.go` | `url.Values.Has`; `Optional`. The resource-tag replace's composite. `TestResources_OnV1` is `TestResources_OnV2` here |
| `scope_test.go` | 808 | **PORTED** in #112 (#91) | `scope_test.go` | The only unit coverage of `checkScopeCoherence`. `t.Cleanup` ×1 → `newVersionedTestClient` returns its cleanup. Range-over-int **plus `i := i`** — see *Flagged* |
| `smokeprobes_test.go` | 720 | **REWRITTEN** here (#95) | `smokeprobes_test.go` | `doData[T]` / `doList[T]` resolution; `List[…]`; and a `smokeProbes()` table this line does not have. See *The three rewrites* |
| `tags_test.go` | 241 | **PRESERVE** this line's own; `TestTags_Get` **PORTED** here | `tags_test.go` | `io.ReadAll`; `Optional`. The fixture is raw wire JSON rather than a marshalled `Tag`, and every field it expects a value in is sent a non-zero one — `main`'s sent `"parent_id": null`, which a misspelled tag also decodes to — so a misspelled tag on one of them fails. The null case is `TestTags_Get_NullParentStaysNil` |
| `tagtree_test.go` | 738 | **OWNED BY** #99 | — | `BuildTagTree` arrives with #99. `slices.*` ×13; range-over-int (`for i := range 50`) |
| `types_test.go` | 369 | Mixed — per test below | `types_test.go` | `Optional` ×11; `io.ReadAll`; `DecodeMetadata[T]` |
| `vocabularies_test.go` | 278 | **PRESERVE** this line's own; two of `main`'s tests **PORTED** here | `vocabularies_test.go` | `io.ReadAll`; `Optional`. Per test below |

## Outside the root package

| `main` file | Lines | Verdict | Reason |
| ----------- | ----: | ------- | ------ |
| `tools/contractdrift/drift_test.go` | 3,355 | **OWNED BY** [#98](https://github.com/octoverse-id/octonomy-go/issues/98) | The contract gate's own tests travel with the gate |
| `webhook/events_test.go` | 998 | **EXCLUDE** | Policy: this line never ships a webhook receiver; a consumer needing one moves to `/v2` (`AGENTS.md`, *READ FIRST*) |
| `webhook/example_test.go` | 257 | **EXCLUDE** | As above |
| `webhook/handler_test.go` | 719 | **EXCLUDE** | As above |
| `webhook/verify_test.go` | 667 | **EXCLUDE** | As above |

## Per test, where a file is split

### `octonomy_test.go`

This line's `octonomy_test.go` is its own, against its own `Config`, constructor and transport, and
stays. `main`'s cases went to `transport_test.go`.

| `main` test | Here |
| ----------- | ---- |
| `TestNew_Validation`, `TestDo_SetsHeadersAndPath`, `TestDo_ActorHeader`, `TestDo_ErrorEnvelope`, `TestDo_ErrorFallbackNonEnvelope`, `TestDoData_MissingEnvelopeIsAnError`, `TestDoData_NullEnvelopeIsAnError`, `TestDoData_EmptyBodyIsAnError`, `TestDoList_RejectsBodiesThatWouldLookLikeAnEmptyPage`, `TestDoList_EmptyPageIsNotAnError`, `TestDo_DeleteAcceptsEmpty204` | Same names in this line's own `octonomy_test.go` |
| `TestDo_RequestIDHeader`, `TestWithRequestID_RejectsUnusableValues`, `TestWithRequestID_DoesNotMaskAnEarlierOptionFailure`, `TestBaseURL_WithPathPrefix`, `TestDoData_ContentsThatWouldDecodeToAZeroValue`, `TestDoData_ContentsThatDecodeToABlankIdentity` | Same names, `transport_test.go` (#112) |
| `TestDoList_RejectsAnElementThatWouldBeZeroValued`, `TestDoList_ElementsThatDecodeToABlankIdentity` | One table, `TestDoList_RejectsARowThatWouldBeZeroValued` (#112) |
| `TestDo_RejectsA2xxThatCarriesAPayload` | `TestDo_RefusesA2xxThatIsNotTheDeleteAnswer` (#112) |
| `TestComposites_RowsWithABlankIdentityAreErrors` | The blank-row cases of the composite tests in `assignments_test.go` and `resources_test.go` (#113) |
| `TestDoData_UndecodableBodies` | **PORTED** here, `transport_test.go`: the non-JSON body, on `doData` and `doList`. Its other two cases are shape cases of `TestDoData_ContentsThatWouldDecodeToAZeroValue` |
| `TestTransport_ErrorsPropagateFromEveryHelper` | **PORTED** here, `transport_test.go` |

### `types_test.go`

| `main` test | Verdict |
| ----------- | ------- |
| `TestMetadataIsStillAnAlias` | **PORTED** here. On this line the reason is this line's first release: it declared `Metadata` an alias, and a defined type would break a caller's type switch, which no major can carry |
| `TestUpdateMetadata_OmitClearAndReplaceOnEveryPatchBody` | Covered by `update_test.go` (#112): the absent, `{}` and populated states on every `Metadata` field of every `*Update` body in `updateBodies` (#96). `main`'s fourth state, an explicit `null`, needs `Optional` and does not exist here |
| `TestDecodeMetadata`, `…_AbsentMetadataIsTheZeroValueForEveryT`, `…_MismatchIsAnErrorNotAPanic`, `…_UnmarshalableValue`, `…_LargeIntegerPrecision` | **Wait on a port of `DecodeMetadata`**, which this line lacks. No issue owns that port yet — see *Gaps* |

### `vocabularies_test.go`

| `main` test | Verdict |
| ----------- | ------- |
| `TestVocabularies_Create`, `_List`, `_Update`, `_Delete` | Same names in this line's own file |
| `TestVocabularies_Get` | **PORTED** here, with a raw wire fixture whose `application_id` is a value; `main`'s null case is its `a shared vocabulary` subtest |
| `TestVocabularies_List_Params` | **PORTED** here **without** its `q` and `slug` cases: this line's `VocabularyListParams` has no `Query` or `Slug`. See *Gaps* |

### `integration_suite_test.go`

[The epic's design doc](https://github.com/octoverse-id/octonomy-go/blob/main/docs/designs/compat-line-api-v2-parity.md)
puts this file out of scope beyond the `readProbes` harness (its *NOT in scope* list). Every test in it
also stands on `integration_harness_test.go`'s per-merchant clients, which #97 ported.

| `main` test | Verdict |
| ----------- | ------- |
| `readProbes`, `runProbeMatrix`, `TestIntegration_NamespaceIsolation`, `TestIntegration_IncludeGlobalFailsClosed` | **PORTED** here (#97) — the isolation coverage `/api/v2` exists to provide. One probe differs: `main`'s `Vocabularies.List` narrows with a slug filter, which this line's `VocabularyListParams` lacks (see *Gaps*), so here it walks every page and fails unless it saw as many distinct rows as the server counts. The suite runs in the required go1.13 smoke job, as a step with its own `^TestIntegration_` selector |
| `TestIntegration_AssignmentIdempotence`, `_BulkPartialFailure`, `_DeactivationCascade`, `_DuplicateSlugScopedPerNamespace` | **EXCLUDE** (epic scope). They assert what the server does with a row, not what this client decodes; the smoke test holds the shapes |
| `TestIntegration_ErrorEnvelopes` | **EXCLUDE** (epic scope), and the strongest candidate to revisit: it pins the code and status a real server sends for twelve `Is*` helpers. This line's smoke tests reach four of them — `IsNotFound` on both surfaces, `IsValidation`, `IsInactiveTag`, `IsApplicationMismatch` — and the other eight need the per-merchant clients #97 ported |
| `TestIntegration_DeactivatedParentOrphansItsLiveChildren`, `_ParentCycleIsReachableAndRefused` | **EXCLUDE** (epic scope). Both read the result through `BuildTagTree` (#99) |
| `TestIntegration_TagsListPagesInATotalOrder` | **EXCLUDE** (epic scope). It walks pages with `Each` (#99) |
| `TestIntegration_NullClearsOnlyTheNullableFields` | **EXCLUDE, for good.** It sends `Null[…]` to clear a nullable field, and this line cannot express a clear: the pointer fields stay (`AGENTS.md`, *Porting from `main`*). That carve-out is the reason, not a gap |

### `integration_test.go`

`main`'s smoke walk is a registry: `smokeProbes()` returns one entry per response type, each handed a
client, and `main`'s `smokeprobes_test.go` binds an entry's key to the calls its closure makes. This
line's walk is five `TestSmoke_` functions, each building its own clients on one of two API versions —
`/api/v1` by default, and `/api/v2` opt-in for the namespace pair — and it stays that way. The registry is test structure, not
capability; restructuring 719 lines verified against a real server to carry it would buy one thing
the rewritten guard already checks directly. See the second rewrite below.

## The three rewrites

The first two, #95's, read the package source, and both found their response types in `main`'s **type arguments** —
`doData[Tag](…)`, `doList[Tag](…)`. This line has no type arguments. Its response type is the type of
the destination **argument** of a method call — `s.client.doData(…, &out, …)` after `var out Tag` —
and a list's row type is its envelope's `Data` field. A transform of `main`'s guards finds nothing
to read here: their floors would fail them, and only a rewrite makes them pass for the right reason.

- **`identityfields_test.go`** keeps `main`'s rule — every response type can refuse a payload that
  decoded to nothing: `identityFields()` on the **value** receiver, a declared composite's
  `UnmarshalJSON` on the pointer — and `main`'s fixtures, rewritten for `&out`. The derivation,
  `responseTypes` in `sourceguard_test.go`, fails closed: a destination it cannot name (an
  `interface{}` wrapper passing `out` through, a shadowed name, a method value, a method expression,
  whose explicit receiver moves every argument one place, a type declared inside the function) is
  reported, not skipped — and so is a call to `doRaw` from anything but `Client.do`, `doData` and
  `doList`, a new helper beside them in `transport.go` included, which decodes beneath the helpers
  where no destination argument exists to read. One half is new, because one mechanism
  is: a list's `rows()`, which `main`'s `doList[T]` does not need. No source reading can prove
  `rows()` hands back every row, so
  `TestEveryDecodedModelCarriesAnIdentity` proves it by calling it, and
  `TestTheRuntimeIdentityTablesMatchTheSource` holds that test's hand-written tables to the
  source-derived sets in both directions.
- **`smokeprobes_test.go`** keeps `main`'s guard — every response type is asserted against a real
  server — and binds it to this line's walk: a type is probed when a `TestSmoke_` function calls a
  method that decodes it, on a client that function built (`newSmokeClient`, or the SDK's `New`),
  outside any closure. That is `main`'s checks 1 and 4 taken together; `main`'s separate stale-key
  and wrong-key checks exist only because a registry has keys. Each list envelope (`TagList`, …) is
  a type of its own here, decoded through `doList` rather than `doData`, so it needs a list method
  probed as well — a guarantee `main`'s row-only derivation does not give. A credited call is only
  worth something if the smoke run executes it, and with no registry walk to fail at runtime, the
  rest of the file checks that statically:
  - **No skip, no early return.** A smoke test that can skip is refused — a `Skip` call in it, a
    smoke-file helper it calls that can, or its `*testing.T` handed to anything the guard cannot
    read — and so is a `return` in its body, the skip ban's obvious workaround. The one skip allowed
    is `newSmokeClient`'s, and only as a single `Skip` straight after `if required { t.Fatal(…) }`,
    with `required` read from `OCTONOMY_SMOKE_REQUIRED`; `newSmokeClient` itself is held to the same
    no-helper, no-handed-on-T rule.
  - **The runners are pinned.** The Makefile's `smoke:` rule and recipe
    (`TestSmokeSelectorRunsEveryTestSmokeFunction`) and the CI smoke job, comments aside
    (`TestSmokeJobRequiresTheSmokeRun`), must equal the text in `smokeRecipePin` and `smokeJobPin`,
    which was run against a real server for this change. The pins' comment lists what each runner
    was checked for — every `TestSmoke_` function selected, `-count=1`, `-tags=integration`, a
    failure that fails the runner, and in CI `OCTONOMY_SMOKE_REQUIRED=1` on go1.13 — and a change to
    either runner re-checks those and updates its pin in the same commit. Around the pins, a
    Makefile-wide setting that changes how a recipe runs (`.IGNORE` for every target or for `smoke`,
    `.ONESHELL`, `SHELL`, `.SHELLFLAGS`, `MAKEFLAGS` outside an allow-list, `GOFLAGS`, the required
    variable set to anything but `1`, an `include`) and a workflow-level `env` or `defaults` are
    refused, since each would change a pinned runner without touching its text.
  - **The smoke file is built by those runs** (`TestSmokeFileCarriesTheTagTheRunnersSelect`): it
    carries the `integration` tag in both constraint spellings.
  - **No `TestMain` hides a failure** (`TestNoTestMainHidesAFailure`). Before Go 1.15 a `TestMain`
    that returns exits 0 over failing tests, and one that exits 0 early does on any version, so a
    `TestMain` anywhere in the repository must be exactly `os.Exit(m.Run())` — the guard walks for
    every test file, a nested module's included, rather than listing packages. The check lives in a package
    of its own, `internal/testmainguard`, because a `TestMain` decides its own binary's exit status
    before any test in that binary runs: in the root package it would be the first thing an early
    exit skipped.

  The pins replaced a reader of the runners' shell, make and YAML that ten review rounds grew and
  that never converged: each round found a construct it got wrong, in both directions — `set +e`
  and a toolchain move slipped through, while `main`'s capture-and-rethrow recipe and a JavaScript
  `if` in an unrelated job were refused. A pin refuses nothing outside the two runners and lets
  nothing inside them change unreviewed. What stays a reviewer's: the workflow's triggers, a
  `needs:`, the harness action's own steps, and branch protection.

- **`readprobes_test.go`** (#97) keeps `main`'s guard — every read method has a namespace probe in
  `readProbes`, or an argued exclusion — and its fixtures. It reads HTTP verbs rather than response
  types, and `main`'s finds the verb at **argument 2** of `doData[T](ctx, c, method, …)`; here every
  transport helper is a method on the client, `s.client.doData(ctx, method, …)`, with the verb at
  argument 1. A transformed copy reads the path as the verb, classifies every read as unknown, and
  fails for the wrong reason; `TestTransportCallsMatchTheHelpersSignatures` pins each index to the
  parameter `transport.go` names `method`. The guard is a function of the parsed source here, so each
  condition it must fail on — a read with no probe, a probe naming a missing method, a probe whose
  `find` calls another endpoint, a duplicate, a stale or unargued exclusion, an unresolvable verb —
  has a fixture of its own against a clean one (`TestReadProbeGuardFailsOnEachCondition`). Unlike
  `main`'s, it reads `Client`'s own exported methods too, and fails closed on any shape that would
  hand a caller a method it does not read: an embedded field, an aliased service, an exported
  `Client` field that is not a `*FooService`, an exported field on a service — and on an exported
  method anywhere else in the package that issues a read, which closes the surface by what a method
  does rather than by one more shape. And one
  half is new, because the table has to RUN: `TestTheIsolationSuiteRunsItsProbes` holds the isolation
  suite to the runners' tag and `^TestIntegration_` prefix, `loadHarness`'s skip to `newSmokeClient`'s
  required-gate shape, `runProbeMatrix` to ranging over `readProbes`, and each isolation test to the
  runs it must make under an **exact** merchant grant — a refusal, a filtered read, a visible
  control, and for `include_global` the fail-closed read — since under the wildcard grant
  authorization never refuses. `make test-integration` is pinned beside it, and the CI step is part
  of `smokeJobPin`.

## The `t.Cleanup` replacement model

`t.Cleanup` needs Go 1.14. A cleanup registered **inside a helper** runs when the *test* finishes;
a `defer` inside that helper runs when the *helper* returns, tearing the resource down before the
caller uses it. So the rewrite is not mechanical, and this is the model, in three rules:

1. **A helper that owns a resource returns its teardown, and the caller defers it at the call site.**
   This is what pre-1.14 Go did, and what this line has done since its first release:
   `newTestClient`, `newVersionedTestClient` and `newTestHealthClient` return `(…, func())`, and the
   caller writes `defer cleanup()`. `cleanupmodel_test.go` proves each helper's server outlives the
   helper, survives the caller's assertions, and is gone once the cleanup runs.
2. **A `t.Cleanup` in a test's own body becomes a `defer` in that body.** The two are equivalent
   because a sequential `t.Run` returns only after its subtest finishes, so the deferral runs after
   every subtest. The equivalence breaks under `t.Parallel`: a parallel subtest runs after its
   parent's body has returned, by which time the parent's deferrals have fired. No test on this line
   calls `t.Parallel`; one that does must keep its subtests' resources out of the parent's defers.
3. **A teardown that must outlive the helper or subtest that registers it belongs to the root test.**
   `main` registers its smoke rows on the root test (`smokeState.deleteLater` calls
   `s.root.Cleanup`) so a row one registry entry creates survives for the entries after it, and its
   harness's `seed` helper registers the rows it seeds. Here that becomes a stack the root test owns —
   a slice of funcs the helpers append to, drained last-in-first-out, as `t.Cleanup` runs, by one
   `defer` at the top of the root test. The smoke tests do not need it: they create their rows at the
   top level of each `TestSmoke_` function and defer the deletes there. #97 met it first, porting
   `seed`: `teardown` in `integration_harness_test.go` is the stack, each isolation test declares one
   and defers its `run`, and `seed` pushes each row's delete the moment the row exists. It drains on a
   `t.Fatalf` inside `seed` too, which ends the test goroutine with `runtime.Goexit`, running the root
   test's defers.

A miss is loud in the direction that matters. A teardown that runs too early fails the test — a
refused connection, a 404 on a row — rather than passing; a forgotten `defer` leaks a test server
until the binary exits.

`main`'s sites, by rule: `octonomy_test.go` ×4 (1 helper, 3 test bodies), `health_test.go` ×5
(1 helper, 4 bodies), `scope_test.go` ×1 (helper) — all ported; `pagination_test.go` ×1 (body, #99);
`integration_harness_test.go` ×1 (rule 3, ported in #97); `integration_suite_test.go` ×10 (bodies
and subtests of the excluded tests; the isolation tests have none); `integration_test.go` ×1 (rule 3, not
needed while the registry is not ported). #95 counts 24: a search for the name finds 24 lines, and
one of them is a comment in `integration_harness_test.go`, so the call sites number 23.

## Flagged

- **`i := i`.** `main`'s `scope_test.go:528` — `for i := range goroutines` with a goroutine reading
  `i` — is this line's `scope_test.go:610`, in `TestNamespace_IsPerRequestUnderConcurrency`: a C-style
  loop with an explicit `i := i`. Without the shadow every goroutine reads the final `i` on Go 1.13 and
  the concurrency test passes while proving nothing. It is **LOUD**: go1.13.15's `vet` reports the
  missing shadow (`loopclosure`), per the range-over-int row of the porting checklist.

## Gaps this table found

Two of `main`'s test files reach exported surface this line lacks, and no sub-issue of #88 owns
porting it. Each is a capability gap, not a test one, so it is recorded here rather than ported in a
test change:

- **`VocabularyListParams.Query` and `.Slug`** — the `q` and `slug` filters `main` gained in #61.
  Both vendored specs list them on `GET /vocabularies`, and `docs/contract-coverage.yaml` maps that
  operation to `VocabularyService.List`, so the coverage reads complete while two parameters are
  missing. Two cases of `TestVocabularies_List_Params` wait on it, and the `Vocabularies.List`
  isolation probe walks the whole collection where `main`'s narrows by slug (#97).
- **`DecodeMetadata`** — generic on `main`, with no counterpart here. Its compat signature is an
  open question the epic's design doc records (finding 6A, "define the `Each` / `DecodeMetadata`
  compat signatures"), and only `Each` has an issue (#99). Five tests in `types_test.go` wait on it.

## Test files with no `main` counterpart

| File | From | What it covers |
| ---- | ---- | -------------- |
| `cleanupmodel_test.go` | #95 | Rule 1 of the `t.Cleanup` model |
| `contractbaseline_test.go` | #109 (#90) | The vendored specs, the contract-version marker and the coverage rows agree |
| `disposition_test.go` | #95 | Every test file in this package is named in this table |
| `internal/testmainguard/testmain_test.go` | #95 | A `TestMain` cannot turn a failing test binary green on Go 1.13; a package of its own, so a root-package `TestMain` cannot skip it |
| `jsondepth_test.go`, `jsondepth_external_test.go` | #112 (#91) | Go 1.13's `encoding/json` has no depth limit and no cycle detection; `main`'s modern toolchain needs neither guard |
| `sourceguard_test.go` | #95 | The AST readers the rewritten guards share: the two of #95, and `readprobes_test.go` (#97) |
| `transport_test.go` | #112 (#91) | Holds `main`'s `octonomy_test.go` cases, since this line's own `octonomy_test.go` is kept |
| `update_test.go` | #112 (#91) | The `*Update` value-receiver `MarshalJSON` (#37), and the per-field wire round-trip of every `*Update` type |
| `updateguard_test.go` | #96 | The source half of the `*Update` checks: no shipped struct tag names `omitzero`, which Go 1.13's `encoding/json` ignores; every `*Update` field can be left out; every PATCH body is a `*Update`; `updateBodies` names every `*Update` type |
| `writebodies_test.go` | #113 (#94) | Every non-`*Update` request body against literal JSON |

## Keeping this table true

The `main` side is pinned to 5e40964 and cannot drift. The "Here" side moves when an owning issue
lands, and that issue's PR updates its row. `TestDispositionTableNamesEveryTestFile` fails when a
test file in this package has no mention here, so a port that lands under a new name, or a new
compat-only file, cannot go unrecorded.
