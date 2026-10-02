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
| `identityfields_test.go` | 637 | **REWRITTEN** here (#95) | `identityfields_test.go`, `sourceguard_test.go` | `doData[T]` / `doList[T]` type-argument resolution; `ast.Unparen` (1.22); `io/fs` (1.16). See *The two rewrites* |
| `integration_harness_test.go` | 500 | **OWNED BY** [#97](https://github.com/octoverse-id/octonomy-go/issues/97) | — | `t.Cleanup` ×1 in `seed`, a helper whose rows must outlive it (rule 3 of the model); `io.ReadAll`; both build-constraint lines |
| `integration_suite_test.go` | 2,323 | `readProbes` and the isolation tests: **OWNED BY** #97. The rest: **EXCLUDE** | — | `t.Cleanup` ×10; `Optional` ×24; `List[…]`. Per test in *integration_suite_test.go* below |
| `integration_test.go` | 1,701 | **PRESERVE** this line's own | `integration_test.go` | `t.Cleanup` ×1, on the root test (`smokeState.deleteLater`). The shapes it covers landed in #112 (v2 namespace, health, `Metadata{}`) and #113 (the resource groups). `main`'s `smokeProbes()` registry is **not** ported; see *integration_test.go* below |
| `octonomy_test.go` | 952 | **PRESERVE** this line's own; `main`'s cases **PORTED** to `transport_test.go` | `octonomy_test.go` (351 lines, this line's), `transport_test.go` | `t.Cleanup` ×4; `Header.Values` (1.14) → `r.Header[http.CanonicalHeaderKey(k)]`; `List[…]`. Per test below |
| `optional_test.go` | 406 | **EXCLUDE** | — | Everything it asserts is `Optional[T]` and its `omitzero` tags, and neither exists here: `Optional` is deliberately not ported, because the `*Update` structs keep their published pointer fields (`AGENTS.md`, *Porting from `main`*). What it guards is covered for this dialect by `update_test.go` (#112) and, for the source half, by [#96](https://github.com/octoverse-id/octonomy-go/issues/96) |
| `pagination_test.go` | 536 | **OWNED BY** [#99](https://github.com/octoverse-id/octonomy-go/issues/99) | — | All 14 tests are `TestEach_*`, and `Each` arrives with #99. `t.Cleanup` ×1, in a test body (rule 2); `List[…]` ×8 → the per-resource lists |
| `readprobes_test.go` | 1,223 | **OWNED BY** #97, as a rewrite | — | A source-parsing guard with `doData[T]` / `doList[T]` resolution, like the two rewritten here. `sourceguard_test.go` holds the readers it shares with them, rewritten already |
| `resolution_test.go` | 613 | **PORTED** in #113 (#94) | `resolution_test.go` | `url.Values.Has` ×2 |
| `resources_test.go` | 641 | **PORTED** in #113 (#94) | `resources_test.go` | `url.Values.Has`; `Optional`. The resource-tag replace's composite. `TestResources_OnV1` is `TestResources_OnV2` here |
| `scope_test.go` | 808 | **PORTED** in #112 (#91) | `scope_test.go` | The only unit coverage of `checkScopeCoherence`. `t.Cleanup` ×1 → `newVersionedTestClient` returns its cleanup. Range-over-int **plus `i := i`** — see *Flagged* |
| `smokeprobes_test.go` | 720 | **REWRITTEN** here (#95) | `smokeprobes_test.go` | `doData[T]` / `doList[T]` resolution; `List[…]`; and a `smokeProbes()` table this line does not have. See *The two rewrites* |
| `tags_test.go` | 241 | **PRESERVE** this line's own; `TestTags_Get` **PORTED** here | `tags_test.go` | `io.ReadAll`; `Optional`. The fixture is raw wire JSON rather than a marshalled `Tag`, so a misspelled tag on a field it reads fails |
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
| `TestUpdateMetadata_OmitClearAndReplaceOnEveryPatchBody` | Covered by `update_test.go` (#112): the absent, `{}` and populated states on all three `*Update` bodies. `main`'s fourth state, an explicit `null`, needs `Optional` and does not exist here |
| `TestDecodeMetadata`, `…_AbsentMetadataIsTheZeroValueForEveryT`, `…_MismatchIsAnErrorNotAPanic`, `…_UnmarshalableValue`, `…_LargeIntegerPrecision` | **Wait on a port of `DecodeMetadata`**, which this line lacks. No issue owns that port yet — see *Gaps* |

### `vocabularies_test.go`

| `main` test | Verdict |
| ----------- | ------- |
| `TestVocabularies_Create`, `_List`, `_Update`, `_Delete` | Same names in this line's own file |
| `TestVocabularies_Get` | **PORTED** here, with a raw wire fixture |
| `TestVocabularies_List_Params` | **PORTED** here **without** its `q` and `slug` cases: this line's `VocabularyListParams` has no `Query` or `Slug`. See *Gaps* |

### `integration_suite_test.go`

[The epic's design doc](https://github.com/octoverse-id/octonomy-go/blob/main/docs/designs/compat-line-api-v2-parity.md)
puts this file out of scope beyond the `readProbes` harness (its *NOT in scope* list). Every test in it
also stands on `integration_harness_test.go`'s per-merchant clients, which arrive with #97.

| `main` test | Verdict |
| ----------- | ------- |
| `readProbes`, `runProbeMatrix`, `TestIntegration_NamespaceIsolation`, `TestIntegration_IncludeGlobalFailsClosed` | **OWNED BY** #97 — the isolation coverage `/api/v2` exists to provide |
| `TestIntegration_AssignmentIdempotence`, `_BulkPartialFailure`, `_DeactivationCascade`, `_DuplicateSlugScopedPerNamespace` | **EXCLUDE** (epic scope). They assert what the server does with a row, not what this client decodes; the smoke test holds the shapes |
| `TestIntegration_ErrorEnvelopes` | **EXCLUDE** (epic scope), and the strongest candidate to revisit: it pins the code and status a real server sends for twelve `Is*` helpers. This line's smoke tests reach four of them — `IsNotFound` on both surfaces, `IsValidation`, `IsInactiveTag`, `IsApplicationMismatch` — and the other eight need #97's per-merchant clients |
| `TestIntegration_DeactivatedParentOrphansItsLiveChildren`, `_ParentCycleIsReachableAndRefused` | **EXCLUDE** (epic scope). Both read the result through `BuildTagTree` (#99) |
| `TestIntegration_TagsListPagesInATotalOrder` | **EXCLUDE** (epic scope). It walks pages with `Each` (#99) |
| `TestIntegration_NullClearsOnlyTheNullableFields` | **EXCLUDE, for good.** It sends `Null[…]` to clear a nullable field, and this line cannot express a clear: the pointer fields stay (`AGENTS.md`, *Porting from `main`*). That carve-out is the reason, not a gap |

### `integration_test.go`

`main`'s smoke walk is a registry: `smokeProbes()` returns one entry per response type, each handed a
client, and `main`'s `smokeprobes_test.go` binds an entry's key to the calls its closure makes. This
line's walk is five `TestSmoke_` functions on two clients — `/api/v1` by default, and `/api/v2` built
opt-in for the namespace pair — and it stays that way. The registry is test structure, not
capability; restructuring 719 lines verified against a real server to carry it would buy one thing
the rewritten guard already checks directly. See the second rewrite below.

## The two rewrites

Both read the package source, and both found their response types in `main`'s **type arguments** —
`doData[Tag](…)`, `doList[Tag](…)`. This line has no type arguments. Its response type is the type of
the destination **argument** of a method call — `s.client.doData(…, &out, …)` after `var out Tag` —
and a list's row type is its envelope's `Data` field. A transform of `main`'s guards finds nothing
to read here: their floors would fail them, and only a rewrite makes them pass for the right reason.

- **`identityfields_test.go`** keeps `main`'s rule — every response type can refuse a payload that
  decoded to nothing: `identityFields()` on the **value** receiver, a declared composite's
  `UnmarshalJSON` on the pointer — and `main`'s fixtures, rewritten for `&out`. The derivation,
  `responseTypes` in `sourceguard_test.go`, fails closed: a destination it cannot name (an
  `interface{}` wrapper passing `out` through, a shadowed name, a method value) is reported, not
  skipped. One half is new, because one mechanism is: a list's `rows()`, which `main`'s `doList[T]`
  does not need. No source reading can prove `rows()` hands back every row, so
  `TestEveryDecodedModelCarriesAnIdentity` proves it by calling it, and
  `TestTheRuntimeIdentityTablesMatchTheSource` holds that test's hand-written tables to the
  source-derived sets in both directions.
- **`smokeprobes_test.go`** keeps `main`'s guard — every response type is asserted against a real
  server — and binds it to this line's walk: a type is probed when a `TestSmoke_` function calls a
  method that decodes it, on a client that function built (`newSmokeClient`, or the SDK's `New`),
  outside any closure. That is `main`'s checks 1 and 4 taken together. `main`'s separate stale-key
  and wrong-key checks exist only because a registry has keys, and are not needed without one.
  `TestSmokeSelectorRunsEveryTestSmokeFunction` holds `make smoke` and the go1.13 smoke job to
  `-run '^TestSmoke_'`, so the guard cannot credit a probe no job runs.

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
   `defer` at the top of the root test. Nothing on this line needs it yet: the smoke tests create
   their rows at the top level of each `TestSmoke_` function and defer the deletes there. #97 meets it
   first, porting `seed`.

A miss is loud in the direction that matters. A teardown that runs too early fails the test — a
refused connection, a 404 on a row — rather than passing; a forgotten `defer` leaks a test server
until the binary exits.

`main`'s sites, by rule: `octonomy_test.go` ×4 (1 helper, 3 test bodies), `health_test.go` ×5
(1 helper, 4 bodies), `scope_test.go` ×1 (helper) — all ported; `pagination_test.go` ×1 (body, #99);
`integration_harness_test.go` ×1 (rule 3, #97); `integration_suite_test.go` ×10 (bodies and
subtests; #97 for the isolation tests, the rest excluded); `integration_test.go` ×1 (rule 3, not
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
  missing. Two cases of `TestVocabularies_List_Params` wait on it.
- **`DecodeMetadata`** — generic on `main`, with no counterpart here. Its compat signature is an
  open question the epic's design doc records (finding 6A, "define the `Each` / `DecodeMetadata`
  compat signatures"), and only `Each` has an issue (#99). Five tests in `types_test.go` wait on it.

## Test files with no `main` counterpart

| File | From | What it covers |
| ---- | ---- | -------------- |
| `cleanupmodel_test.go` | #95 | Rule 1 of the `t.Cleanup` model |
| `contractbaseline_test.go` | #109 (#90) | The vendored specs, the contract-version marker and the coverage rows agree |
| `disposition_test.go` | #95 | Every test file in this package is named in this table |
| `jsondepth_test.go`, `jsondepth_external_test.go` | #112 (#91) | Go 1.13's `encoding/json` has no depth limit and no cycle detection; `main`'s modern toolchain needs neither guard |
| `sourceguard_test.go` | #95 | The AST readers the two rewritten guards share, and #97's port after them |
| `transport_test.go` | #112 (#91) | Holds `main`'s `octonomy_test.go` cases, since this line's own `octonomy_test.go` is kept |
| `update_test.go` | #112 (#91) | The `*Update` value-receiver `MarshalJSON` (#37) |
| `writebodies_test.go` | #113 (#94) | Every non-`*Update` request body against literal JSON |

## Keeping this table true

The `main` side is pinned to 5e40964 and cannot drift. The "Here" side moves when an owning issue
lands, and that issue's PR updates its row. `TestDispositionTableNamesEveryTestFile` fails when a
test file in this package has no mention here, so a port that lands under a new name, or a new
compat-only file, cannot go unrecorded.
