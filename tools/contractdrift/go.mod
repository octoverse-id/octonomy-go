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
//
// It also imports the SDK, through a replace on the checkout. That is how the
// gate learns what each method sends and decodes: it CALLS the method against a
// recording stub and reads the request off the wire, rather than inferring it
// from the source. Nothing flows the other way -- the SDK module neither requires
// nor knows about this one.
module github.com/octoverse-id/octonomy-go/v2/tools/contractdrift

go 1.24

require (
	github.com/octoverse-id/octonomy-go/v2 v2.0.0-alpha.1
	gopkg.in/yaml.v3 v3.0.1
)

// The SDK under test is this checkout, never a published version: the gate's
// whole job is to compare the contract with the code in front of it.
replace github.com/octoverse-id/octonomy-go/v2 => ../..
