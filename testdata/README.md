# Transport behavior vectors — this line's copy

`behavior_vectors.json` is **`main`'s file, byte for byte as committed**, at the commit
[`tools/compatdiff/baselines.pin`](../tools/compatdiff/baselines.pin) names for `main`
([#104](https://github.com/octoverse-id/octonomy-go/issues/104)). It pins, as language-neutral data,
the transport behaviour no API contract expresses — the scope refusals, the `data` envelope,
identity and pagination guards, error-code mapping, the 32 MiB ceiling and the 10000-level nesting
limit — and both lines run it, each with a binding of its own, so a rule changed on one line and not
the other fails on the line that missed it.

- **The format and the sampling rule the vectors meet** are `main`'s
  [`testdata/README.md`](https://github.com/octoverse-id/octonomy-go/blob/main/testdata/README.md),
  not restated here. The version that describes this copy is the one at the pinned commit:
  `git show "$(awk '$1 == "main" {print $2}' tools/compatdiff/baselines.pin):testdata/README.md"`.
- **This line's binding** is `behaviorvectors_test.go`: `main`'s, rewritten into the Go 1.13
  dialect (the row in [`docs/compat-test-disposition.md`](../docs/compat-test-disposition.md) lists
  each rewrite). It recognises `depth_exceeded` by this line's own guard, `errJSONTooDeep` in
  `jsondepth.go`, since Go 1.13's `encoding/json` has no depth limit of its own.
- **Never edit the file here.** `make compat-diff` (in the required `compat guard` job, and in
  `release-check`) fails on a copy that would not commit as `main`'s blob — any differing byte, a
  checkout's own line-ending conversion aside — on one that is not a regular file, on a missing
  copy, and on a pin that predates the file (`tools/compatdiff/vectors.go`). A rule changes on `main` first, in `main`'s file; it
  reaches this line when the pin advances — a port of everything the newer commit brings — and
  this line's binding has to pass it then.
