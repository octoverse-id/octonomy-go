# Architecture

`octonomy-go` is a thin, hand-written client for the Octonomy REST **v1** API. It depends only on the
Go standard library. The design goal is that an agent (or human) can add a new resource by copying an
existing resource file and changing the types and paths.

## Layers

| File | Responsibility |
| ---- | -------------- |
| `octonomy.go` | `Config`, `Client`, `New()` (validation + service wiring). |
| `transport.go` | `do()`: URL building under `/api/v1`, auth/tenant headers, JSON encode/decode, non-2xx → `*APIError`. Plus `doData()`, which unwraps the server's single-resource `{"data": ...}` envelope. Also `RequestOption` / `WithActor`. |
| `errors.go` | `APIError`, error `Code*` constants, and `Is*` / `AsAPIError` helpers. |
| `pagination.go` | `ListOptions` and `Pagination`. The list envelope itself is per-resource on this line (`TagList`, `VocabularyList`) because `List[T]` needs Go 1.18. |
| `types.go` | Shared `Metadata` alias and the `String`/`Bool`/`Int` pointer helpers. |
| `version.go` | `Version` constant (single source of truth) and the default User-Agent. |
| `<resource>.go` | One file per resource: the model, `*Create`/`*Update` write structs, `*ListParams`, and the `*Service` with CRUD methods. |

## Request lifecycle

1. A service method (e.g. `TagService.Create`) calls `client.do(ctx, method, path, query, body, out, opts...)`.
2. `do` builds `BaseURL + /api/v1 + path`, attaches headers, and JSON-encodes the body.
3. On a 2xx it decodes the response into `out`; on anything else it calls `parseError`, which decodes
   the `{error:{...}}` envelope into an `*APIError` (falling back to the raw body + a status-derived
   code when the envelope is absent).

```
Caller ──▶ Service.Method ──▶ Client.do ──▶ net/http ──▶ Octonomy /api/v1
                                  │
                                  ├─ 2xx → unwrap {"data": ...} → *Model
                                  │         (lists decode the whole body → *ModelList)
                                  └─ !2xx → *APIError (Code, Message, Details, RequestID, StatusCode)
```

## Conventions that keep it faithful

- **Contract reference:** `docs/openapi.yaml` is vendored from the server. Types mirror it
  field-for-field. The deliberate divergences are both response envelopes: the generated spec shows a
  bare array for lists and a bare object for single resources, while the server wraps lists in
  `{data, pagination}` (`octonomy/core/pagination.py`) and single resources in `{data}`
  (`octonomy/core/responses.py`). The SDK follows the server; both divergences are noted in code.
  Only an integration test against a real server can catch a regression here, which is what
  `integration_test.go` is for.
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
2. Add a `*Service` with `context.Context`-first, `...RequestOption`-last methods delegating to
   `client.do`.
3. Wire the service onto `Client` in `New()`.
4. Add table-driven `httptest` tests and a CHANGELOG entry.

See [roadmap.md](roadmap.md) for the queued resources.
