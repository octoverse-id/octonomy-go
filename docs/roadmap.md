# Roadmap

> **This line has no roadmap.** `support/go1.13` is the frozen Go 1.13 line: Vocabularies and Tags on
> `/api/v1`, security fixes only, sunset 2027-08-31 (see [versioning.md](versioning.md)). Its surface
> is settled, permanently; nothing that lives on the other line is coming here.

What a reader on this line usually wants from a file with this name is what upgrading to the `/v2`
module buys. That is the modern line's question to answer, and it is answered where it is maintained:
[`main`'s API mapping](https://github.com/octoverse-id/octonomy-go/blob/main/docs/api.md#implemented) for what that line implements, and
[`main`'s roadmap](https://github.com/octoverse-id/octonomy-go/blob/main/docs/roadmap.md) for what is still open there.

This file used to carry its own copy of that inventory, method names and routes included. It drifted —
it went on describing a method signature `main` had already corrected, which a copy on a frozen branch
has no way to find out — so it links to the owning branch instead.

The groups this line does not have, and never will, are tag aliases, tag resolution, tag assignments
(including the bulk pair), resource tags, audit logs, and the health probes. [`api.md`](api.md) says
the same from the other side.

## How to add a resource (the recipe)

Copy `tags.go` and `tags_test.go` as the template, then:

1. Read the matching schema(s) in [`openapi.yaml`](openapi.yaml).
2. Create `<resource>.go` with: the model struct, `*Create`/`*Update` write structs (pointer +
   `omitempty`), `*ListParams` with a `query()` method, and a `*Service` whose methods take
   `context.Context` first and `...RequestOption` last and delegate to the transport helper matching
   the response shape: `client.doData` for a single resource, `client.doList` for a list,
   `client.do` for a call with no payload (DELETE). Using `do` where `doData` belongs does not fail
   loudly — it returns a zero-valued struct with a nil error.
3. Wire the service onto `Client` in `New()` (`octonomy.go`).
4. Add table-driven `httptest` tests (assert method/path/headers/query/body server-side; assert decoded
   values client-side; cover the error envelope).
5. Add a `## [Unreleased]` CHANGELOG entry and update [`api.md`](api.md).

The recipe is this line's, and it is here to explain the shape of the code you are reading. Adding a
resource is work for `main` and never for this branch, so a new resource issue belongs there, against
`main`'s copy of this file.
