# Architecture

`octonomy-go` is a thin, hand-written client for the Octonomy REST API — `/api/v1` by default on
this line, `/api/v2` when `Config.APIVersion` is `APIV2`. It depends only on the Go standard
library. The design goal is that an agent (or human) can add a new resource by copying an
existing resource file and changing the types and paths.

## Layers

| File | Responsibility |
| ---- | -------------- |
| `octonomy.go` | `Config`, `APIVersion` (default `APIV1` on this line), `Client`, `New()` (validation + service wiring). |
| `transport.go` | `doRaw()`: URL building under `/api/<version>`, auth/tenant/scope headers, the request-option chokepoint (`WithActor`, `WithRequestID`, the four scope options, `checkScopeCoherence`), JSON encoding, the 32 MiB read ceiling, non-2xx → `*APIError`. Then one decoder per response shape: `doData()` (single resource, unwraps `{"data": ...}`), `doList()` (list envelope), `do()` (no payload: requires DELETE's 204 with an empty body). `doData` and `doList` reject a decode with no identity, and `doList` a pagination block with no usable limit. `doUnversioned()` is the separate, unauthenticated path for the health probes. |
| `jsondepth.go` | Compat-only: the nesting guards Go 1.13's `encoding/json` lacks — `checkBodyDepth` before every request is encoded, `decodeJSON` around every response decode. |
| `errors.go` | `APIError`, error `Code*` constants, and `Is*` / `AsAPIError` helpers. |
| `health.go` | `HealthService`, `NewHealthClient`, and the bare `{"status": …}` decoder. |
| `pagination.go` | `ListOptions` and `Pagination`. The list envelope itself is per-resource on this line (`TagList`, `VocabularyList`) because `List[T]` needs Go 1.18. |
| `types.go` | Shared `Metadata` alias and the `String`/`Bool`/`Int` pointer helpers. |
| `tags.go`, `vocabularies.go` | The two resources, each with a value-receiver `MarshalJSON` on its `*Update` so `Metadata{}` reaches the server as `{}`. |
| `version.go` | `Version` constant (single source of truth) and the default User-Agent. |
| `<resource>.go` | One file per resource: the model, `*Create`/`*Update` write structs, `*ListParams`, and the `*Service` with CRUD methods. |

## Request lifecycle

1. A service method picks the transport helper that matches the response shape it expects:
   `doData` for a single resource (`Create`/`Get`/`Update`), `doList` for a list, plain `do` for a
   call with no payload to decode (`Delete`). All four funnel into `doRaw`.
2. `doRaw` applies the request options, refuses an incoherent scope before anything is sent, builds
   `BaseURL + /api/<version> + path`, attaches headers, bounds and JSON-encodes the body, and returns
   the raw 2xx body. On a non-2xx it calls `parseError`, which decodes the `{error:{...}}` envelope
   into an `*APIError` — or, when the envelope is absent, keeps the raw body under
   `CodeUnexpectedStatus`, never a code inferred from the status. A request that got no response
   wraps `ErrUnreachable` instead.
3. The caller decodes: `doData` unwraps `{"data": {...}}`, `doList` asserts the envelope then decodes
   `{data, pagination}` whole. Either way a 2xx whose body does not carry `data` is an **error**, not
   a zero-valued result — decoding a wrapped body straight into a `*Tag` or a `*TagList` produces an
   empty struct with a nil error, and a caller cannot tell that from "no such tag" or "no tags". The
   same holds one level in: a resource, or a list row, that decodes with no `id` is an error too.

```
                           ┌─ doData ─┐                                2xx body
Caller ──▶ Service.Method ─┼─ doList ─┼──▶ doRaw ──▶ net/http ──▶ Octonomy /api/<version>
                           └─ do ─────┘    │
   pick by response shape:                 ├─ 2xx → raw body back to the caller above:
     doData  single resource               │         doData → unwrap {"data":{…}}  → *Model
     doList  list envelope                 │         doList → require {"data":[…], "pagination":{…}}
     do      no payload (DELETE 204)       │                  → *ModelList
                                           │         do     → require 204, no body
                                           └─ !2xx → *APIError (Code, Message, Details,
                                                     RequestID, StatusCode)

Health.Live / Ready ──▶ doUnversioned ──▶ net/http ──▶ Octonomy /health/{live,ready}
                        no auth, no tenant, no options; bare {"status": …} body

   A 2xx that does not carry the envelope its caller expects is an ERROR, never a
   zero-valued result: an empty struct with a nil error is indistinguishable from
   "no such tag", and an empty page from "no tags".
```

## Conventions that keep it faithful

- **Contract reference:** `docs/openapi.yaml` (`/api/v1`) and `docs/openapi-v2.yaml` (`/api/v2`) are
  vendored from the server, both at release 3.2.1. This tree's types mirror the **v2** schemas, the
  superset: v1's carry no namespace fields, so `NamespaceType` / `NamespaceID` decode to nil on a v1
  response. The deliberate
  divergences are both response envelopes: the generated specs show a bare array for lists and a bare
  object for single resources, while the server wraps lists in `{data, pagination}`
  (`octonomy/core/pagination.py`) and single resources in `{data}` (`octonomy/core/responses.py`).
  The SDK follows the server; both divergences are noted in code.
  Only an integration test against a real server can catch a regression here, which is what
  `integration_test.go` is for.
- **Pointers for optionality:** nullable server fields decode into `*string`; write structs use
  pointers + `omitempty` so PATCH only sends what the caller set. That stays true when `main`'s
  structs are ported: `main` moved its `*Update` fields to `Optional[T]`, and doing the same here
  would change published field types, which this line — unable to publish a major — cannot do (see
  [versioning.md](versioning.md)). The cost is that PATCH cannot clear a nullable field on this line.
- **No hidden behavior:** the client never retries, panics, logs, or mutates global state. Retries,
  timeouts, and transport tuning are the caller's `*http.Client`.

## Multi-tenancy

Every request on the versioned API is scoped to one tenant via `X-Tenant-ID` (`Config.TenantID`,
required); the health probes, outside `/api/<version>`, carry no tenant and no token. Tags and
vocabularies may be shared (`application_id == nil`) or application-specific; assignments always carry
an `application_id`. The SDK passes these through faithfully — the server enforces isolation.

## Extending the client

To add a resource, follow `tags.go`:

1. Define the model, `*Create`/`*Update`, and `*ListParams` (with a `query()` method) from the
   matching schema in the vendored contracts, and give the operation its row in
   `docs/contract-coverage.yaml` (replacing its `unimplemented:` reason with `sdk:`).
2. Add a `*Service` with `context.Context`-first, `...RequestOption`-last methods delegating to the
   helper that matches each response shape: `client.doData` for a single resource, `client.doList`
   for a list, `client.do` where there is no payload (DELETE). Reaching for `do` when the response
   carries a resource compiles and returns a zero-valued struct with a nil error.
3. Wire the service onto `Client` in `New()`.
4. Add table-driven `httptest` tests and a CHANGELOG entry.

See [roadmap.md](roadmap.md) for the resource groups this tree does not have yet, and the issues
porting them from `main`.
