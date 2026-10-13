# Transport behavior vectors

`behavior_vectors.json` pins the transport behaviour that no API contract
expresses: which of its own requests a client refuses before sending anything,
and what it makes of a response — the `data` envelope, the identity of a decoded
resource, the pagination block, how an error maps to a code, the 32 MiB read
ceiling, and the 10000-level nesting limit.

**The file is data, and it is language-neutral.** Inputs and outcomes are plain
JSON; an outcome names a **reason**, never a message or a type from any one
language. Each implementation that runs the file supplies a *binding*: the code
that performs each input and recognises each reason in its own errors. This
module's binding is `behaviorvectors_test.go`. The file exists to be run by the
Go 1.13 line
([`support/go1.13`](https://github.com/octoverse-id/octonomy-go/tree/support/go1.13))
as well, with a binding of its own
([#104](https://github.com/octoverse-id/octonomy-go/issues/104)); how that line
keeps its copy identical to this one is for that branch to state.

## Why it exists

Both lines hand-write their transport, and the API contracts cannot see the rules
listed above — they are the client's, not the server's. A change to one of them
on one line that the other line does not make is drift with no other signal:
each line's own tests keep passing against its own implementation. A rule
written here is checked on every line that runs the file.

## Changing a covered behaviour

Change the vector in the same pull request. The binding runs in the root
package, so `go test ./...` — and with it the required test jobs — fails until
the file says what the code now does. That is the point: a behaviour change has
to be written down where every line reads it.

Add a vector when you add a refusal or change what a response decodes to. A new
reason needs a binding for it in the same change; the binding refuses a reason it
does not know, and a reason it knows that no vector names.

## Format

```jsonc
{
  "version": 1,          // bump if the shape below changes
  "response": [ ... ],   // a canned response to one call
  "request":  [ ... ]    // a call whose own scoping is checked before it is sent
}
```

Every vector has a unique `name`, a `description` saying what it pins and why,
and `surfaces` — `"v1"`, `"v2"` or both — naming the client API version it runs
on. **Each vector carries exactly one defect.** When a response carries two,
which one an implementation reports first is not something these vectors pin.

### Response vectors

| Field | Meaning |
| ----- | ------- |
| `call` | `get` (one resource), `list` (a page), `delete` (no payload), `health` (the readiness probe) |
| `status`, `body` | the HTTP response. `body` is required; `""` is an empty body |
| `pad_to_bytes` | optional: append ASCII spaces until the body is exactly this many bytes |
| `nest` | optional: replace `placeholder` in the body with `depth` opening brackets followed by `depth` closing ones |
| `outcome` | what the client must make of it |

| Reason | Meaning |
| ------ | ------- |
| `ok` | the call succeeds, and carries what arrived: a `get` outcome the decoded `id`, a `list` outcome the decoded `ids` in order (`[]` for none), a `health` outcome `health_status`, the word the server sent. A `delete` outcome carries nothing |
| `api_error` | the call fails with the API error type, carrying `status`, `code` (the envelope's code verbatim, `unexpected_status` without one, `not_ready` for a probe that answered with its own status body), `request_id` when the envelope carried one, and `wraps: response_too_large` when the body was over the ceiling |
| `envelope_missing` | a 2xx with no body, or with no `data` key |
| `not_a_resource` | a resource position holding `null`, an array, a scalar, or `{}` |
| `identity_missing` | a resource that decodes with a blank identity |
| `pagination_missing` | a list with no `pagination` block, or a null one |
| `pagination_unusable` | a pagination block whose `limit` is below 1 |
| `type_mismatch` | a list's `data` or `pagination` of the wrong JSON type |
| `malformed_json` | a body that is not JSON |
| `depth_exceeded` | a body nested past 10000 levels, the outermost value counting as level 1 |
| `response_too_large` | a 2xx body over 32 MiB (33554432 bytes) |
| `unexpected_delete_response` | a delete answered with anything but `204` and no body |
| `health_status_missing` | a 2xx probe response with no non-blank `status` |

### Request vectors

| Field | Meaning |
| ----- | ------- |
| `call` | `list`, `get`, `delete`, `create`, `update`, or `resolve` (tag resolution) |
| `params` | optional: the call's own parameters — `application_id` (`list`, `resolve`) and `scope` (`resolve`). A present `""` is a value |
| `options` | the scoping options, in order, each an object with exactly one key: `namespace` (a `[type, id]` pair), `global_namespace`, `application`, `include_global` |
| `outcome` | `sent`, carrying the `namespace` pair or `null`, the `application_id` sent or `null`, and `include_global`; or the reason it was refused, with nothing sent |

Refusal reasons: `namespace_requires_v2`, `namespace_type_required`,
`namespace_id_required`, `namespace_type_reserved`, `namespace_contradiction`,
`namespace_requires_application`, `application_required`,
`application_contradiction`, `application_on_write`,
`include_global_requires_v2`, `include_global_on_write`,
`merchant_scope_requires_namespace`, `include_global_contradicts_merchant_scope`.
The `description` of each vector that names one says what it means.

## What a binding must do

- Run every vector on every surface it names, against a server that answers the
  response (or, for a request vector, answers any well-formed request
  successfully), and require exactly one request for a response vector, and one
  or none for a request vector — none exactly when it was refused.
- Require that request to be the one the call stands for, on the surface the
  vector names: under `/api/v1` or `/api/v2`, or the readiness probe, which sits
  outside both. A vector that passes on a request to another route proves
  nothing about its own.
- Recognise every outcome as **exactly one** reason. An error no reason
  recognises, or one that two recognise, is a failure, never the nearest match.
- Read the file strictly: an unknown key, call, surface, option or reason is an
  error, not something to skip.
