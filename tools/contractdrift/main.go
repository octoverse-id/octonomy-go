// Command contractdrift reports when the Octonomy server's REST contract has
// moved away from what this SDK implements.
//
// It exists because nothing else does. The SDK sat on a server 1.0.0 contract
// while the server shipped 3.1.0 and made an entire second API surface primary,
// and nobody was told, because no mechanism existed to tell anyone (issue #18).
//
// WHAT IT COMPARES, and why a path inventory would not have been enough. The
// drift that prompted this was query parameters, error codes, response schemas,
// and a new surface. A path-to-method inventory reports every one of those as
// green, so this compares:
//
//	contract version   the spec's info.version, on both surfaces, against the
//	                   version recorded in docs/versioning.md
//	operations         path + method, in both directions
//	parameters         per operation, keyed by `in` AND name, including
//	                   `required` and the parameter's own schema
//	responses          per operation, per status, including the request body
//	schemas            components.schemas, property by property
//	error codes        the server's registry in core/errors.py against the SDK's
//	                   Code* constants -- invisible to a schema comparison,
//	                   because ErrorResponse types `code` as a bare string
//
// And, because the question is what the SDK IMPLEMENTS and not what it vendors,
// three comparisons that CALL the client (conformance.go, drivers.go) and read
// the answer off the wire:
//
//	routes             each inventory row against the request its method actually
//	                   issued, twice, with different path values
//	request shape      per operation and in both directions: the query parameters,
//	                   headers and request-body properties on the wire, NAMES AND
//	                   VALUES -- each driver sends a value naming the field it
//	                   belongs to, so a value under the wrong name says so
//	response models    the client decodes a body built FROM the vendored schema,
//	                   twice -- populated, then with every nullable property null --
//	                   and the gate reports what did not survive
//	model field names  each response model's Go field against the property it
//	                   decodes, which is the one defect a round trip cannot see
//
// Reading the source instead was tried first and abandoned: see conformance.go.
//
// TWO MODES, because only one of them can be trusted on a pull request:
//
//	-local             compares the VENDORED contracts against this repository:
//	                   the inventory, the routes and models of the Go methods it
//	                   names, the query parameters each of those methods sends,
//	                   and the recorded contract version. Offline, deterministic,
//	                   and the only half safe to gate a pull request -- it can
//	                   fail only on something in this checkout. It is also the
//	                   only half that can see a vendored contract refreshed
//	                   without the follow-through, since after a refresh both
//	                   sides of the cross-repository comparison are one file.
//	-upstream DIR      adds the cross-repository comparison against a fetched
//	                   copy of the server's contracts. Scheduled only: a job that
//	                   reaches across repositories can fail for reasons that have
//	                   nothing to do with the change under review, and a merge
//	                   gate that does that is a merge gate people learn to
//	                   route around.
//
// Each operation is driven FOUR times: both REST surfaces, and on each, two
// executions with different path values and different response witnesses. The two
// executions on a surface must agree in everything but the path.
//
// Fetching is deliberately NOT this program's job. scripts/contract-fetch.sh
// does it, and this reads plain files -- which is what makes a synthetic
// upstream copy a one-line test rather than a network fixture.
//
// Exit codes: 0 clean, 1 drift found, 2 the comparison could not be made. The
// third is separate because a gate that cannot read its inputs must never be
// mistaken for a gate that found nothing.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	exitClean  = 0
	exitDrift  = 1
	exitCannot = 2
)

func main() {
	var (
		repo     = flag.String("repo", ".", "path to the octonomy-go repository root")
		upstream = flag.String("upstream", "", "directory holding a fetched copy of the server's openapi.yaml, openapi-v2.yaml, and errors.py")
		local    = flag.Bool("local", false, "run only the offline checks (vendored contracts against this repository)")
		summary  = flag.String("summary", "", "append the report to this file as well as stdout (GITHUB_STEP_SUMMARY)")
		source   = flag.String("source", "", "label for the upstream source, e.g. octoverse-id/octonomy@<sha>")
	)
	flag.Parse()

	// Exactly one mode, never a default. If -upstream could be omitted silently
	// the workflow's own bug -- an unset variable, a fetch step that did not run --
	// would show up as a clean report over a comparison that never happened.
	switch {
	case *upstream == "" && !*local:
		fatal("specify -upstream DIR for the full comparison, or -local for the offline checks only")
	case *upstream != "" && *local:
		fatal("-upstream and -local are exclusive: -upstream already runs the local checks")
	}

	code, err := run(*repo, *upstream, *summary, *source)
	if err != nil {
		fatal("%v", err)
	}
	os.Exit(code)
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "contractdrift: "+format+"\n", args...)
	os.Exit(exitCannot)
}

func run(repo, upstream, summary, source string) (int, error) {
	in := Inputs{
		Vendored: make(map[string]*Spec, 2),
		Coverage: nil,
	}

	var err error
	if in.Vendored["v1"], err = LoadSpec(filepath.Join(repo, "docs", "openapi.yaml")); err != nil {
		return 0, err
	}
	if in.Vendored["v2"], err = LoadSpec(filepath.Join(repo, "docs", "openapi-v2.yaml")); err != nil {
		return 0, err
	}
	if in.Coverage, err = LoadCoverage(filepath.Join(repo, "docs", "contract-coverage.yaml")); err != nil {
		return 0, err
	}
	if in.SDK, err = LoadSDKPackage(repo); err != nil {
		return 0, err
	}
	in.SDKCodes = in.SDK.ErrorCodes()
	if len(in.SDKCodes) < minSDKErrorCodes {
		return 0, fmt.Errorf("%s: found only %d Code* constants (expected at least %d) -- errors.go moved or changed shape",
			filepath.Join(repo, "errors.go"), len(in.SDKCodes), minSDKErrorCodes)
	}
	if in.RecordedVersion, err = RecordedContractVersion(filepath.Join(repo, "docs", "versioning.md")); err != nil {
		return 0, err
	}
	// Call the client against a stub that answers with bodies built from the
	// vendored schemas: every operation, on BOTH surfaces, twice each. Everything the local checks say about what
	// the SDK sends and decodes comes from here rather than from reading its source.
	if in.Conformance, err = RunConformance(in.Vendored, in.Coverage, Drivers()); err != nil {
		return 0, err
	}

	if upstream != "" {
		in.Upstream = make(map[string]*Spec, 2)
		if in.Upstream["v1"], err = LoadSpec(filepath.Join(upstream, "openapi.yaml")); err != nil {
			return 0, err
		}
		if in.Upstream["v2"], err = LoadSpec(filepath.Join(upstream, "openapi-v2.yaml")); err != nil {
			return 0, err
		}
		if in.ServerCodes, in.UnreadableCodes, err = ServerErrorCodes(filepath.Join(upstream, "errors.py")); err != nil {
			return 0, err
		}
	}

	report := CheckLocal(in)
	if upstream != "" {
		report.Sections = append(report.Sections, CheckUpstream(in).Sections...)
	}

	markdown := render(in, report, source, upstream != "")
	fmt.Print(markdown)
	if summary != "" {
		if err := appendFile(summary, markdown); err != nil {
			return 0, err
		}
	}
	if report.Count() > 0 {
		return exitDrift, nil
	}
	return exitClean, nil
}

// render writes the report as markdown, so the same bytes read well in a terminal
// and in a GitHub job summary.
func render(in Inputs, report *Report, source string, withUpstream bool) string {
	var b strings.Builder
	b.WriteString("# Octonomy contract drift\n\n")

	fmt.Fprintf(&b, "- Vendored contract: **%s** (`docs/openapi.yaml`, `docs/openapi-v2.yaml`)\n", in.Vendored["v2"].Version)
	if withUpstream {
		label := source
		if label == "" {
			label = "a fetched copy"
		}
		fmt.Fprintf(&b, "- Upstream contract: **%s** (%s)\n", in.Upstream["v2"].Version, label)
	} else {
		b.WriteString("- Upstream comparison: **not run** (`-local`)\n")
	}
	fmt.Fprintf(&b, "- Inventory: %d operations in `docs/contract-coverage.yaml`\n\n", len(in.Coverage.Operations))

	if report.Count() == 0 {
		if withUpstream {
			b.WriteString("No drift. The vendored contracts match the server's, and what the client sent and decoded matches what they document.\n")
		} else {
			b.WriteString("No drift. What the client sent and decoded matches the vendored contracts. The cross-repository comparison did not run.\n")
		}
		return b.String()
	}

	fmt.Fprintf(&b, "## %d finding(s)\n\n", report.Count())
	for _, section := range report.Sections {
		fmt.Fprintf(&b, "### %s\n\n", section.Title)
		for _, item := range section.Items {
			fmt.Fprintf(&b, "- %s\n", item)
		}
		b.WriteString("\n")
	}
	b.WriteString("Refresh the vendored contracts, implement the change, or record the decision in ")
	b.WriteString("`docs/contract-coverage.yaml` -- see `docs/development.md#contract-drift`.\n")
	return b.String()
}

func appendFile(path, content string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(content); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
