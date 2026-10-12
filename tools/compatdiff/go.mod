// Tool-only module, separate from the SDK's for the reason tools/contractdrift's
// go.mod gives at length: the LIBRARY is held to Go 1.13 because a consumer
// compiles it with Go 1.13, and nobody compiles this tool but CI and a
// contributor.
//
// Here the modern floor is not a convenience but the job. This tool type-checks
// main's source, which uses type parameters, and go1.13.15's go/parser cannot
// read a single one of its files -- so a Go 1.13 version of it could compare
// nothing. The go1.13 job's `go build ./...` and `go vet ./...` stop at this
// go.mod and never see it.
//
// Standard library only, and it does not import the SDK: it reads three trees
// as source (main at a pinned commit, each published release of this line, and
// the working tree) and compares what they export.
module github.com/octoverse-id/octonomy-go/tools/compatdiff

go 1.24
