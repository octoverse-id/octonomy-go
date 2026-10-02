# Roadmap

> **This line's roadmap is [epic #88](https://github.com/octoverse-id/octonomy-go/issues/88)**:
> capability parity with the `/v2` line, ported from `main`, through the 2027-08-31 sunset (see
> [versioning.md](versioning.md)). The epic and its sub-issues carry the order and the state of that
> work; this file does not restate either.

What the parity work ports is what the modern line implements, and that is answered where it is
maintained: [`main`'s API mapping](https://github.com/octoverse-id/octonomy-go/blob/main/docs/api.md#implemented) for what that line implements, and
[`main`'s roadmap](https://github.com/octoverse-id/octonomy-go/blob/main/docs/roadmap.md) for what is still open there.

This file used to carry its own copy of that inventory, method names and routes included. It drifted —
it went on describing a method signature `main` had already corrected, which a copy on another branch
has no way to find out — so it links to the owning branch instead.

Every resource group the vendored contracts publish is on this tree since
[#94](https://github.com/octoverse-id/octonomy-go/issues/94) ported the last five.
[`contract-coverage.yaml`](contract-coverage.yaml) is the record that a test holds true: every
operation names its method, or would name the reason there is none. [`api.md`](api.md) says the same from the
other side. The one group that is **never** coming is a
webhook receiver: that is policy, and a consumer needing one moves to `/v2`.

## How to add a resource (the recipe)

Copy `tags.go` and `tags_test.go` as the template, then:

1. Read the matching schema(s) in [`openapi-v2.yaml`](openapi-v2.yaml), the superset of the two
   vendored specs (both at server 3.2.1): it carries the `namespace_type` / `namespace_id` fields
   that [`openapi.yaml`](openapi.yaml) omits. The operation needs a row in
   [`contract-coverage.yaml`](contract-coverage.yaml) naming `sdk: <Service>.<Method>`; the contract
   refresh that published it adds the row with an `unimplemented:` reason, which this replaces.
2. Create `<resource>.go` with: the model struct and its value-receiver `identityFields()`,
   `*Create`/`*Update` write structs (pointer + `omitempty`, and a value-receiver `MarshalJSON` on a
   `*Update` carrying `Metadata`), a `*List` type with `rows()`, `*ListParams` with a `query()`
   method, and a `*Service` whose methods take `context.Context` first and `...RequestOption` last
   and delegate to the transport helper matching the response shape: `client.doData` for a single
   resource, `client.doList` for a list, `client.do` for a call with no payload (DELETE). The wrong
   helper compiles: `do` on a call that returns a resource fails with "expected 204", and `doData` or
   `doList` on a shape they do not expect fails on the envelope, identity or pagination check. A
   composite response — counts plus rows, like the bulk calls — is not a list: it goes through
   `client.doData` with a result struct whose `UnmarshalJSON` requires its keys (`assignments.go`).
   Scoping, request ids and the depth guards come from the transport; add nothing for them.
3. Wire the service onto `Client` in `New()` (`octonomy.go`).
4. Add table-driven `httptest` tests (assert method/path/headers/query/body server-side; assert decoded
   values client-side; cover the error envelope).
5. Add a `## [Unreleased]` CHANGELOG entry and update [`api.md`](api.md).

The recipe is this line's, and it is here to explain the shape of the code you are reading. A resource
new to *both* lines is work for `main` first, so a new resource issue belongs there, against `main`'s
copy of this file; porting a resource `main` already has starts from `main`'s file rather than from
`tags.go`, under the porting rules in [AGENTS.md](../AGENTS.md).
