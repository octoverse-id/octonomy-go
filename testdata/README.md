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

The Go 1.13 line was ported from this one by hand rather than generated — the
decision recorded on [#88](https://github.com/octoverse-id/octonomy-go/issues/88)
— and the API contracts cannot see the rules listed above: they are the
client's, not the server's. A change to one of them on one line that the other
does not make is drift with no other signal, since each line's own tests keep
passing against its own implementation. A rule written here is checked on every
line that runs the file.

## Changing a covered behaviour

Change the vector in the same pull request. The binding runs in the root
package, so a change to a behaviour a vector pins fails `go test ./...` — and
with it the required test jobs — until the file says what the code now does.
That is the point: a behaviour change has to be written down where every line
reads it. A behaviour no vector pins is not protected, which is why the vectors
try each rule at more than one point.

Add a vector when you add a refusal or change what a response decodes to. A new
reason needs a binding for it in the same change; the binding refuses a reason it
does not know, and a reason it knows that no vector names.

## Format

```jsonc
{
  "$comment": "...",     // free text, for a reader of the file
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
| `pad_to_bytes` | optional: append ASCII spaces (0x20) until the body is exactly this many bytes |
| `nest` | optional: replace `placeholder`, which occurs exactly once in the body, with `depth` `[` characters followed by `depth` `]` characters |

A vector takes at most one of `pad_to_bytes` and `nest`, and a `204` takes
neither. The server answers with the vector's status, `Content-Type:
application/json`, and the body — none on a `204`.
| `outcome` | what the client must make of it |

| Reason | Meaning |
| ------ | ------- |
| `ok` | the call succeeds, and carries what arrived: a `get` outcome the decoded `id`, a `list` outcome the decoded `ids` in order (`[]` for none), a `health` outcome `health_status`, the word the server sent. A `delete` outcome carries nothing |
| `api_error` | the call fails with the API error type, carrying `status`, `code` (the envelope's code verbatim, `unexpected_status` without one, `not_ready` for a probe that answered with its own status body), `request_id` when the envelope carried one, and `wraps: response_too_large` when the body was over the ceiling |
| `envelope_missing` | a 2xx with no body, or with no `data` key |
| `not_a_resource` | a resource position holding `null`, an array, a scalar, or `{}` |
| `identity_missing` | a resource whose identity is absent, `null` or the empty string. An identity of only whitespace is present: it decodes as sent |
| `pagination_missing` | a list with no `pagination` block, or a null one |
| `pagination_unusable` | a pagination block whose `limit` is below 1 |
| `type_mismatch` | a value of the wrong JSON type where the response has a typed place for it: a list's `data` or `pagination`, or a field of a resource or of a row |
| `malformed_json` | a body that is not JSON |
| `depth_exceeded` | a body nested past 10000 levels, the outermost value counting as level 1 |
| `response_too_large` | a 2xx body over 32 MiB (33554432 bytes) |
| `unexpected_delete_response` | a delete answered with a 2xx other than `204`, whatever its body, or with a `204` that carries one (a non-2xx is an `api_error`, as on any call) |
| `health_status_missing` | a 2xx probe response with no non-blank `status` |

### Request vectors

| Field | Meaning |
| ----- | ------- |
| `call` | `list`, `get`, `delete`, `create`, `update`, or `resolve` (tag resolution) |
| `params` | optional: the call's own parameters — `application_id` (`list`, `resolve`) and `scope` (`resolve`). A present `""` is a value |
| `options` | the scoping options, in order, each an object with exactly one key: `namespace` (a `[type, id]` pair of strings), `application` (a string), or `global_namespace` / `include_global`, which take only `true` |
| `outcome` | `sent`, carrying the `namespace` pair or `null`, the `application_id` sent or `null`, and `include_global`; or the reason it was refused, with nothing sent |

A `sent` outcome is read off the one request: `namespace` from the
`X-Namespace-Type` and `X-Namespace-ID` headers, which are both present or both
absent; `application_id` from the query parameter of that name, present at most
once; `include_global` true exactly when the query carries `include_global=true`
once, and false when it is absent — any other value is a failed run. The server
for a request vector answers every call successfully: a `list` with an empty page
(`{"data":[],"pagination":{"limit":50,"offset":0,"count":0,"next":null}}`), a
`delete` with `204`, a `resolve` with a tag match, and anything else with one
resource (`{"data":{"id":"t-1",...}}`).

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
