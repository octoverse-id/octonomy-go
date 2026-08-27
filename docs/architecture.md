# Architecture

`octonomy-go` is a thin, hand-written client for the Octonomy REST **v1** API. It depends only on the
Go standard library. The design goal is that an agent (or human) can add a new resource by copying an
existing resource file and changing the types and paths.

## Layers

| File | Responsibility |
| ---- | -------------- |
| `octonomy.go` | `Config`, `Client`, `New()` (validation + service wiring). |
| `transport.go` | `doRaw` (URL building under `/api/v1`, auth/tenant headers, request encoding, non-2xx → `*APIError`) plus the three decoders that sit on it: `do`, `doData[T]`, `doList[T]`. Also `RequestOption` / `WithActor`. |
| `errors.go` | `APIError`, error `Code*` constants, and `Is*` / `AsAPIError` helpers. |
| `pagination.go` | `ListOptions`, `Pagination`, and the generic `List[T]` envelope. |
| `types.go` | Shared `Metadata` alias and the `String`/`Bool`/`Int` pointer helpers. |
| `version.go` | `Version` constant (single source of truth) and the default User-Agent. |
| `<resource>.go` | One file per resource: the model, `*Create`/`*Update` write structs, `*ListParams`, and the `*Service` with CRUD methods. |

## Request lifecycle

1. A service method (e.g. `TagService.Create`) calls the helper matching its **response shape**:
   `doData[T]` for a single resource, `doList[T]` for a list, `client.do` for a call with no payload
   (DELETE).
2. All three go through `doRaw`, which builds `BaseURL + /api/v1 + path`, attaches headers,
   JSON-encodes the body, and returns the status plus the raw 2xx body. On a non-2xx it calls
   `parseError`, which decodes the `{error:{...}}` envelope into an `*APIError` (falling back to the
   raw body + a status-derived code when the envelope is absent).
3. The helper decodes: `doData` unwraps `{"data": {...}}` into a `*T`, `doList` decodes
   `{"data": [...], "pagination": {...}}` into a `*List[T]`, and `do` asserts a 204 with no body.

```
Caller ──▶ Service.Method ──▶ doData[T] / doList[T] / do ──▶ doRaw ──▶ net/http ──▶ Octonomy /api/v1
                                       │                        │
                                       │                        └─ !2xx → *APIError (Code, Message,
                                       │                                  Details, RequestID, Status)
                                       ├─ {"data": {...}}              → *Model
                                       ├─ {"data": [...], "pagination"} → *List[Model]
                                       └─ 204, empty                    → nil
```

**Why the split.** Decoding is deliberately outside `doRaw`, because the shape depends on what was
requested. A single `do()` that unmarshalled the whole body into whatever it was handed made every
unexpected shape indistinguishable from real data: a wrapped body decoded into a `*Tag` yields an
empty struct with a nil error, and an unrecognized list body yields an empty page. Splitting by shape
turns each of those into an error ([#32](https://github.com/octoverse-id/octonomy-go/issues/32)).

## Conventions that keep it faithful

- **Contract reference:** `docs/openapi.yaml` is vendored from the server. Types mirror it
  field-for-field. The deliberate divergences are the **two response envelopes** the generated spec
  omits: the server wraps lists in `{data, pagination}` (`octonomy/core/pagination.py` upstream) and
  single resources in `{data}` (`octonomy/core/responses.py`, present since the server's first
  commit). The spec shows a bare array and a bare object respectively. The SDK follows the server;
  both divergences are noted in code. Only the list half was known before #32 — the other was found
  by running against a real container, which is now `make smoke`.
- **Pointers for optionality:** nullable server fields decode into `*string`; write structs use
  pointers + `omitempty` so PATCH only sends what the caller set.
- **No hidden behavior:** the client never retries, panics, logs, or mutates global state. Retries,
  timeouts, and transport tuning are the caller's `*http.Client`.

## Multi-tenancy

Every request is scoped to one tenant via `X-Tenant-ID` (`Config.TenantID`, required). Tags and
vocabularies may be shared (`application_id == nil`) or application-specific; assignments always carry
an `application_id`. The SDK passes these through faithfully — the server enforces isolation.

## Extending the client

To add a resource, follow `tags.go`:

1. Define the model, `*Create`/`*Update`, and `*ListParams` (with a `query()` method) from the
   matching `docs/openapi.yaml` schema.
2. Add a `*Service` with `context.Context`-first, `...RequestOption`-last methods, each delegating
   to the transport helper matching its **response shape**: `doData[T]` for a single resource,
   `doList[T]` for a paginated list, `client.do` for a 204 with no body. Picking by convenience
   rather than by shape is what produced
   [#32](https://github.com/octoverse-id/octonomy-go/issues/32).
3. Wire the service onto `Client` in `New()`.
4. Add table-driven `httptest` tests and a CHANGELOG entry.

See [roadmap.md](roadmap.md) for the queued resources.
