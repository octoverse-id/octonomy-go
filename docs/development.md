# Development

## Setup

```bash
git clone https://github.com/octoverse-id/octonomy-go.git
cd octonomy-go
go build ./...
make test
```

Requires Go 1.24+. There are **no runtime dependencies** — keep `go.mod` free of a runtime `require`
block.

## Quality gates

```bash
make fmt-check   # gofmt -l . (no output = clean)
make vet         # go vet ./...
make lint        # golangci-lint (if installed)
make test        # go test -race -cover ./...
make cover       # prints total coverage
make examples    # go build ./examples/...
make check       # fmt-check + vet + build (fast pre-push gate)
make release-check  # the full pre-release gate
```

Optional local tools (CI installs them automatically):

```bash
# linter
go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
# vulnerability scanner
go install golang.org/x/vuln/cmd/govulncheck@latest
```

## Testing approach

Tests use `net/http/httptest` to stand up a fake Octonomy and assert the wire contract:

- **Server side** (inside the handler, with `t.Errorf` — handlers run on another goroutine): request
  method, path, `Authorization` and `X-Tenant-ID` headers, query params, and request body.
- **Client side** (in the test goroutine): the decoded return value, the `{data, pagination}` envelope,
  and error decoding via `IsNotFound`/`IsConflict`/`IsValidation`.

`newTestClient(t, handler)` in `octonomy_test.go` is the shared helper. Keep new code covered and run
with `-race`.

## Running against a local Octonomy

Start an Octonomy server (see that repo's README), create a service token, then run the example:

```bash
OCTONOMY_BASE_URL=http://localhost:8000 \
OCTONOMY_TOKEN=svc_... \
OCTONOMY_TENANT_ID=acme \
go run ./examples/quickstart
```

## Keeping the contract current

`docs/openapi.yaml` is vendored from the Octonomy server. When targeting a new server contract,
refresh it (regenerate on the server with `make openapi`, copy the file here), reconcile any type
changes, and note the server version in [versioning.md](versioning.md).
