// Tool-only module, deliberately separate from the SDK's own.
//
// The SDK module is standard-library only and stays that way: a nested module
// is invisible to `go build ./...`, `go.sum`, and dependency resolution in the
// parent, so gopkg.in/yaml.v3 below is a dependency of THIS directory and of
// nothing a consumer of the SDK ever compiles. That is the same standing
// golangci-lint and govulncheck have -- a tool the repository uses, not a
// dependency the library carries.
//
// The alternative was parsing OpenAPI YAML by hand from the standard library.
// The vendored specs use folded block scalars, plain multi-line scalars, and
// quoted keys, so a hand-rolled reader would be a second contract to maintain,
// and its bugs would show up as a drift gate that quietly stops reporting.
module github.com/octoverse-id/octonomy-go/v2/tools/contractdrift

go 1.24

require gopkg.in/yaml.v3 v3.0.1
