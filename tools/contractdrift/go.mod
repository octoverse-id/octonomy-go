// Tool-only module, deliberately separate from the SDK's own -- and on this
// line, deliberately on a different Go.
//
// The SDK module is standard-library only and stays that way: a nested module
// is invisible to `go build ./...`, `go.sum`, and dependency resolution in the
// parent, so gopkg.in/yaml.v3 below is a dependency of THIS directory and of
// nothing a consumer of the SDK ever compiles. That is the same standing
// golangci-lint and govulncheck have -- a tool the repository uses, not a
// dependency the library carries.
//
// The `go 1.24` below is not a slip on a Go 1.13 line. The LIBRARY is held to Go
// 1.13 because a consumer compiles it with Go 1.13; nobody compiles this gate but
// CI and a contributor, so it runs on the modern toolchain the gate was written
// for, and the go1.13 job's `go build ./...` and `go vet ./...` stop at this
// directory's go.mod and never see it. That split is what made porting the gate
// affordable (#98): five of its files are main's, byte for byte, which a Go 1.13
// rewrite would have ended.
//
// It also imports the SDK, through a replace on the checkout. That is how the
// gate learns what each method sends and decodes: it CALLS the method against a
// recording stub and reads the request off the wire, rather than inferring it
// from the source. Nothing flows the other way -- the SDK module neither requires
// nor knows about this one.
module github.com/octoverse-id/octonomy-go/tools/contractdrift

go 1.24

require (
	github.com/octoverse-id/octonomy-go v1.0.0
	gopkg.in/yaml.v3 v3.0.1
)

// The SDK under test is this checkout, never a published version: the gate's
// whole job is to compare the contract with the code in front of it.
replace github.com/octoverse-id/octonomy-go => ../..
