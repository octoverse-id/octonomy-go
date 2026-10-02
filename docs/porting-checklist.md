# Porting checklist — `main` → `support/go1.13`

Parity work on this line is a **hand-port**: take the file from `main` and rewrite it into Go 1.13
([`AGENTS.md`](../AGENTS.md), *Porting from `main`*). This is the table of rewrites, one row per rule.
It is this branch's own copy of the table in
[the epic's design doc](https://github.com/octoverse-id/octonomy-go/blob/main/docs/designs/compat-line-api-v2-parity.md)
on `main` ([#103](https://github.com/octoverse-id/octonomy-go/issues/103)); rows the compat core
([#91](https://github.com/octoverse-id/octonomy-go/issues/91)) added are marked as such.

Which of `main`'s test files meet which rows, and what became of each, is
[`compat-test-disposition.md`](compat-test-disposition.md).

- **LOUD** — a miss fails `go build` or `go vet` under a real go1.13.15, so `make test-go113` (the
  `go1.13` CI job) catches it. A modern toolchain does **not**: it enforces the `go 1.13` directive's
  language version but not its standard library, so `url.Values.Has` compiles there.
- **SILENT** — a miss compiles and vets clean on go1.13.15 and is wrong. Each SILENT row names what
  catches it, or says that nothing does.

## The table

| `main` | compat | Verdict | What catches a miss |
| ------ | ------ | ------- | ------------------- |
| `any` | `interface{}` | **LOUD** | go1.13 build |
| `Optional[string]` / `[bool]` / `[Metadata]` | `*string` / `*bool` / `Metadata` — `Optional` is **not** ported | **LOUD** | go1.13 build |
| `*List[Tag]` (and each other `List[T]`) | a per-resource `*TagList`, with a `rows()` method | **LOUD** | go1.13 build; `doList` takes an `identifiedList`, so a list type without `rows()` does not compile as its argument |
| `doData[Tag](ctx, c, …)` | `var out Tag; c.doData(…, &out)` — a statement rewrite, not an expression | **LOUD** | go1.13 build |
| `doList[Tag](ctx, c, …)` | `var out TagList; c.doList(…, &out)` | **LOUD** | go1.13 build |
| `decodeResourceArray[T]` | in a list, `requireResourceArray` + the list type's `rows()` (#91); in a composite's `UnmarshalJSON`, `requireResourceArray`, then `decodeJSON` into the typed slice, then `requireIdentity` per row (#94) | **LOUD** | go1.13 build |
| `decodeResourceArray[T]`'s null handling, in a composite | after the decode, `if rows == nil { rows = []T{} }` — `main`'s helper returns an empty non-nil slice for a present-but-null array, and a whole-slice `decodeJSON` leaves nil (#94). **Not** in `doList`, which keeps a null page nil as `v1.0.0` did | **SILENT** | It compiles either way and only a nil-versus-empty check sees it: the null cases in `assignments_test.go` and `resources_test.go`. A new composite needs its own. A type error inside one row also loses its index — the whole slice decodes at once — which those tests pin as the accepted message |
| `slices.Reverse`, `slices.DeleteFunc`, `slices.SortFunc`, … | a local helper, or `sort.Slice` | **LOUD** | go1.13 build |
| `strings.Cut` / `CutPrefix`, `errors.Join`, `min` / `max` builtins | spell it out | **LOUD** | go1.13 build |
| `url.Values.Has` (1.17) | `_, ok := q[key]` | **LOUD** | go1.13 build |
| `http.Header.Values` (1.14), in tests (#91) | `r.Header[http.CanonicalHeaderKey(name)]` | **LOUD** | go1.13 build |
| `t.Cleanup` (1.14), `t.TempDir` (1.15), `t.Setenv` (1.17) | in a helper, return a cleanup func and `defer` it at the call site; in a test body, `defer`; a teardown that must outlive its helper, a stack the root test drains (the model: [`compat-test-disposition.md`](compat-test-disposition.md)); `ioutil.TempDir` + `defer os.RemoveAll`; save and restore by hand | **LOUD** | go1.13 build. A `defer` inside the helper instead compiles and vets clean, and fails the test that uses it: the server is closed before the first request (`cleanupmodel_test.go`) |
| `io.ReadAll`, `os.ReadFile`, `os.WriteFile` (1.16) | `ioutil.ReadAll`, `ioutil.ReadFile`, `ioutil.WriteFile` | **LOUD** | go1.13 build |
| range-over-int, `for i := range n` (1.22) | a C-style loop, **plus `i := i`** wherever a closure captures `i` | **LOUD** | go1.13 build for the loop; go1.13 `vet`'s `loopclosure` for a missing shadow under `go`/`defer` (`loop variable i captured by func literal`) |
| `json:"<n>,omitzero"` | `json:"<n>,omitempty"` | **SILENT** | Go 1.13 parses the option and matches nothing, so a nil pointer emits `"<n>":null` — a PATCH that clears every column it did not name. On `TagUpdate` and `VocabularyUpdate`, `update_test.go` catches it under go1.13 (a field set alone then sends more than one key). **Nothing** catches it on any other struct; [#96](https://github.com/octoverse-id/octonomy-go/issues/96) tracks a guard |
| `fmt.Errorf("%w: %w", sentinel, err)` (1.20) | a wrapper type whose `Unwrap()` returns the cause and whose `Is()` matches the sentinel — `unreachableError` in `transport.go` is the pattern | **SILENT** | Before 1.20 two `%w` verbs produce **no `Unwrap` at all**, and go1.13's `vet` does not object. `"%w: %v"` is no fix: it keeps one half. For `ErrUnreachable`, `TestDoRaw_TransportFailureKeepsTheSentinelAndTheCause` and `TestHealth_UnreachableIsNotUnready` assert both halves. **Nothing** catches a new site |
| `func TestMain(m *testing.M) { …; m.Run() }` — returning is fine from Go 1.15 | `os.Exit(m.Run())`, and nothing else in the body (#95) | **SILENT** | Before Go 1.15 the generated test main does not exit after a `TestMain` returns, so the binary exits 0 over failing tests, and go1.13.15's `vet` does not object. staticcheck's SA3000 flags the returning shape under the `go 1.13` directive, in the lint job; `TestNoTestMainHidesAFailure` refuses every shape but the exiting one, an early `os.Exit(0)` included |
| `json.Unmarshal(responseBytes, &v)` (#91) | `decodeJSON(responseBytes, &v)` | **SILENT** at compile time | Go 1.13's decoder has no depth limit, so an unguarded decode lets a server exhaust the stack — fatal, not a panic. `TestEveryResponseDecodeIsDepthBounded` fails on any `json.Unmarshal` or `json.NewDecoder` outside `decodeJSON` |
| a `*Update` with `Optional[Metadata]` | `Metadata` + a **value-receiver** `MarshalJSON` sending a non-nil map, empty or not (#91) | **SILENT** | Without the method, `Metadata{}` is dropped by `omitempty` and the clear never reaches the server (#37); with a pointer receiver, `encoding/json` skips it, since `Update` takes the struct by value. `update_test.go` covers the types in its `updateBodies` table; a new `*Update` type must be added to it. [#96](https://github.com/octoverse-id/octonomy-go/issues/96) tracks a source guard that needs no table |
| doc comments | hand-edited | **SILENT** | Nothing; read for it. Strip `omitzero`, `Optional[T]`, the `/v2` module's Go floor, generics, and post-1.13 standard library **inside examples** — `slices.DeleteFunc` in a `BuildTagTree` example is code a reader copies. Rewrite the **module** path `.../octonomy-go/v2` to the unsuffixed one. **Never** strip `/api/v2`: it is a REST route, and the capability being ported |
| a default copied from `main` | this line's own default | **SILENT** | `DefaultAPIVersion` was `APIV2` on `main` at 5e40964 and is `APIV1` here (#91): copying it would move every v1.0.0 caller's requests to another surface. `TestNew_APIVersion` pins it. Nothing catches a default added later; this line can never change one ([`versioning.md`](versioning.md)) |

## What the LOUD half needs

Only a real go1.13.15 sees it. `make test-go113` runs build, vet and `test -race` under one, and the
`go1.13` CI job does the same; see [`development.md`](development.md) for fetching a toolchain. A pass
on a modern Go proves nothing on this line.
