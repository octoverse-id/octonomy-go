package main

import (
	"fmt"
	"sort"
	"strings"
)

// surfaces are the two REST contracts this SDK speaks, in report order.
var surfaces = []string{"v2", "v1"}

// Inputs is everything a run compares. Upstream is nil on a local-only run.
type Inputs struct {
	Vendored map[string]*Spec
	Upstream map[string]*Spec

	Coverage        *Coverage
	Sources         GoSources
	SDKCodes        map[string]string // wire code -> Go constant name
	ServerCodes     map[string]bool   // nil on a local-only run
	RecordedVersion string
}

// Section is one heading in the report, with one line per finding.
type Section struct {
	Title string
	Items []string
}

// Report accumulates findings. An empty report is the only clean result.
type Report struct {
	Sections []Section
}

// Add appends a section, ignoring an empty one so the report has no silent
// headings.
func (r *Report) Add(title string, items []string) {
	if len(items) == 0 {
		return
	}
	sort.Strings(items)
	r.Sections = append(r.Sections, Section{Title: title, Items: items})
}

// Count is the number of findings across every section.
func (r *Report) Count() int {
	n := 0
	for _, s := range r.Sections {
		n += len(s.Items)
	}
	return n
}

// stripSurface removes the /api/<surface> prefix so an operation can be compared
// against the version-independent suffix the SDK's resource files build. Paths
// outside the versioned API (the health probes) are returned unchanged.
func stripSurface(path, surface string) string {
	prefix := "/api/" + surface
	if strings.HasPrefix(path, prefix+"/") {
		return strings.TrimPrefix(path, prefix)
	}
	return path
}

// strippedOperations indexes a spec by its version-independent operation key.
func strippedOperations(spec *Spec, surface string) map[string]*Operation {
	out := make(map[string]*Operation, len(spec.Operations))
	for _, op := range spec.Operations {
		out[op.Method+" "+stripSurface(op.Path, surface)] = op
	}
	return out
}

// CheckLocal compares the vendored contracts against the SDK itself. Every check
// here is offline and deterministic, which is why it is safe to run on a pull
// request: it fails only on something a person in this repository changed.
func CheckLocal(in Inputs) *Report {
	r := &Report{}
	checkRecordedVersion(in, r)
	checkSurfaceParity(in, r)
	checkInventory(in, r)
	checkImplementedSymbols(in, r)
	checkDocumentedResponses(in, r)
	checkQueryParametersSent(in, r)
	checkStaleErrorCodeAllowlist(in, r)
	return r
}

// CheckUpstream compares the vendored contracts against what the server publishes
// now. It runs only when a fetched upstream copy is supplied.
func CheckUpstream(in Inputs) *Report {
	r := &Report{}
	checkContractVersionDrift(in, r)
	checkOperationDrift(in, r)
	checkParameterDrift(in, r)
	checkResponseDrift(in, r)
	checkSchemaDrift(in, r)
	checkErrorCodeDrift(in, r)
	return r
}

// --- local checks --------------------------------------------------------------

// checkRecordedVersion keeps the three places the targeted contract is written
// down from disagreeing: the two vendored specs and docs/versioning.md. A refresh
// that updates the files and not the prose leaves the documentation lying about
// which server the SDK was written against.
func checkRecordedVersion(in Inputs, r *Report) {
	var items []string
	v1, v2 := in.Vendored["v1"], in.Vendored["v2"]
	if v1.Version != v2.Version {
		items = append(items, fmt.Sprintf("the vendored contracts disagree: %s is %s, %s is %s",
			v1.Path, v1.Version, v2.Path, v2.Version))
	}
	if in.RecordedVersion != v2.Version {
		items = append(items, fmt.Sprintf("docs/versioning.md records server %s, but %s is %s",
			in.RecordedVersion, v2.Path, v2.Version))
	}
	r.Add("Recorded contract version", items)
}

// checkSurfaceParity is what lets one inventory row cover both surfaces.
//
// The SDK's resource files build a version-independent suffix and the transport
// prepends /api/<version>, so `TagService.List` is the same method whether the
// client targets v1 or v2. That only holds while both contracts publish the same
// operations. The moment one does not, a row asserting "this operation is
// implemented" is true of one surface and false of the other, and the failure
// reaches a caller as an unrouted 404 -- which this SDK deliberately does not map
// to a semantic code, so it surfaces as IsUnexpectedStatus rather than as an empty
// result, but surfaces late either way.
func checkSurfaceParity(in Inputs, r *Report) {
	v1 := strippedOperations(in.Vendored["v1"], "v1")
	v2 := strippedOperations(in.Vendored["v2"], "v2")

	var items []string
	for key := range v2 {
		if _, ok := v1[key]; !ok {
			items = append(items, fmt.Sprintf("`%s` is published on v2 only -- one inventory row cannot be true of both surfaces", key))
		}
	}
	for key := range v1 {
		if _, ok := v2[key]; !ok {
			items = append(items, fmt.Sprintf("`%s` is published on v1 only -- one inventory row cannot be true of both surfaces", key))
		}
	}
	r.Add("Surface parity", items)
}

// checkInventory is the paths half of the gate. Every operation the vendored
// contracts publish must appear in docs/contract-coverage.yaml, and every row of
// that file must still exist in the contracts.
//
// The first direction is what makes "not implemented" a decision: a new endpoint
// cannot pass unnoticed, because an unlisted one fails here whether or not anyone
// intends to implement it. The second direction keeps the file from accumulating
// rows for endpoints the server withdrew.
func checkInventory(in Inputs, r *Report) {
	byKey := in.Coverage.ByKey()
	var items []string

	declared := make(map[string]bool)
	for _, surface := range surfaces {
		spec := in.Vendored[surface]
		for key := range strippedOperations(spec, surface) {
			declared[key] = true
			if _, ok := byKey[key]; !ok {
				items = append(items, fmt.Sprintf("`%s` (%s, %s) is in the contract and not in %s -- implement it or record why not",
					key, surface, spec.Path, in.Coverage.Path))
			}
		}
	}
	for key := range byKey {
		if !declared[key] {
			items = append(items, fmt.Sprintf("`%s` is listed in %s but no vendored contract publishes it -- drop the row",
				key, in.Coverage.Path))
		}
	}
	r.Add("Inventory", items)
}

// checkImplementedSymbols keeps the inventory honest about the code. A row that
// claims an operation is implemented names the Go method; if that method is gone
// or renamed, the row is asserting something false and the endpoint is quietly
// uncovered again.
func checkImplementedSymbols(in Inputs, r *Report) {
	var items []string
	for _, op := range in.Coverage.Operations {
		if op.Unimplemented != "" {
			continue
		}
		if !in.Sources.HasMethod(op.File, op.SDK) {
			items = append(items, fmt.Sprintf("`%s` claims `%s` in %s, which declares no such method",
				op.Key(), op.SDK, op.File))
		}
	}
	r.Add("Inventory symbols", items)
}

// checkDocumentedResponses asserts each recorded divergence between the generated
// spec and the running server is STILL a divergence.
//
// This is the list-envelope exception, and it is written as an assertion rather
// than a suppression on purpose. The spec documents list responses as bare arrays
// while the server returns {data, pagination}; nothing in this gate flags that,
// because both sides of every comparison read the same spec. What would be
// genuinely useful to know is the day it stops being true -- the day the server's
// generator learns about the renderer and the SDK can stop carrying a documented
// workaround. So the inventory records the shape the spec documents, and this
// check reports when the spec changes out from under it.
func checkDocumentedResponses(in Inputs, r *Report) {
	var items []string
	for _, surface := range surfaces {
		ops := strippedOperations(in.Vendored[surface], surface)
		for _, row := range in.Coverage.Operations {
			op, ok := ops[row.Key()]
			if !ok {
				continue // reported by checkInventory
			}
			if op.OKSchema != row.DocumentedResponse {
				items = append(items, fmt.Sprintf("`%s` (%s): %s now documents a `%s` 200 body, recorded as `%s` -- the server really returns `%s`, so re-check which side moved",
					row.Key(), surface, in.Vendored[surface].Path, op.OKSchema, row.DocumentedResponse, row.ActualResponse))
			}
		}
	}
	r.Add("Documented response shapes", items)
}

// checkQueryParametersSent is the parameter half of the gate, pointed at the SDK
// rather than at the server.
//
// The upstream comparison catches the server adding a parameter. This catches the
// step after it: a vendored contract refreshed to include that parameter while no
// resource ever learned to send it. That is exactly how `scope` on
// /tag-resolution could have been missed, and an upstream-versus-vendored diff
// alone goes green the moment the files are refreshed.
func checkQueryParametersSent(in Inputs, r *Report) {
	sent := in.Sources.QueryParams()
	allowed := in.Coverage.UnsentNames()
	documented := make(map[string][]string)

	for _, surface := range surfaces {
		for _, op := range in.Vendored[surface].Operations {
			for name, param := range op.Params {
				if param["in"] != "query" {
					continue
				}
				documented[name] = append(documented[name], surface+" "+op.Key())
			}
		}
	}

	var items []string
	for _, name := range sortedKeys(documented) {
		if _, ok := sent[name]; ok {
			continue
		}
		if _, ok := allowed[name]; ok {
			continue
		}
		sort.Strings(documented[name])
		items = append(items, fmt.Sprintf("`%s` is documented as a query parameter on %d operation(s) (e.g. %s) and the client never sends it -- implement it or record it under unsent_query_parameters",
			name, len(documented[name]), documented[name][0]))
	}
	for name := range allowed {
		if _, ok := documented[name]; !ok {
			items = append(items, fmt.Sprintf("`%s` is listed under unsent_query_parameters but no vendored contract documents it -- drop the row", name))
		}
	}
	r.Add("Query parameters the client does not send", items)
}

// checkStaleErrorCodeAllowlist keeps sdk_only_error_codes pointed at constants
// that exist. The rest of the error-code comparison needs the server's registry
// and lives in checkErrorCodeDrift.
func checkStaleErrorCodeAllowlist(in Inputs, r *Report) {
	var items []string
	for code := range in.Coverage.SDKOnlyCodeSet() {
		if _, ok := in.SDKCodes[code]; !ok {
			items = append(items, fmt.Sprintf("`%s` is listed under sdk_only_error_codes and errors.go declares no constant for it -- drop the row", code))
		}
	}
	r.Add("Error code allowlist", items)
}

// --- upstream checks -----------------------------------------------------------

func checkContractVersionDrift(in Inputs, r *Report) {
	var items []string
	for _, surface := range surfaces {
		vendored, upstream := in.Vendored[surface], in.Upstream[surface]
		if vendored.Version != upstream.Version {
			items = append(items, fmt.Sprintf("%s: server publishes contract %s, this SDK vendors %s",
				surface, upstream.Version, vendored.Version))
		}
	}
	r.Add("Contract version", items)
}

func checkOperationDrift(in Inputs, r *Report) {
	var items []string
	for _, surface := range surfaces {
		vendored, upstream := in.Vendored[surface], in.Upstream[surface]
		for key := range upstream.Operations {
			if _, ok := vendored.Operations[key]; !ok {
				items = append(items, fmt.Sprintf("%s: `%s` is new upstream", surface, key))
			}
		}
		for key := range vendored.Operations {
			if _, ok := upstream.Operations[key]; !ok {
				items = append(items, fmt.Sprintf("%s: `%s` is gone upstream", surface, key))
			}
		}
	}
	r.Add("Operations", items)
}

func checkParameterDrift(in Inputs, r *Report) {
	var items []string
	for _, surface := range surfaces {
		vendored, upstream := in.Vendored[surface], in.Upstream[surface]
		for _, key := range sortedKeys(upstream.Operations) {
			was, ok := vendored.Operations[key]
			if !ok {
				continue // reported by checkOperationDrift
			}
			now := upstream.Operations[key]
			for name := range now.Params {
				if _, had := was.Params[name]; !had {
					items = append(items, fmt.Sprintf("%s: `%s` gained %s parameter `%s`",
						surface, key, now.Params[name]["in"], name))
					continue
				}
				for _, line := range diffFlat(was.Params[name], now.Params[name]) {
					items = append(items, fmt.Sprintf("%s: `%s` parameter `%s`: %s", surface, key, name, line))
				}
			}
			for name := range was.Params {
				if _, still := now.Params[name]; !still {
					items = append(items, fmt.Sprintf("%s: `%s` lost %s parameter `%s`",
						surface, key, was.Params[name]["in"], name))
				}
			}
		}
	}
	r.Add("Parameters", items)
}

func checkResponseDrift(in Inputs, r *Report) {
	var items []string
	for _, surface := range surfaces {
		vendored, upstream := in.Vendored[surface], in.Upstream[surface]
		for _, key := range sortedKeys(upstream.Operations) {
			was, ok := vendored.Operations[key]
			if !ok {
				continue
			}
			now := upstream.Operations[key]
			for status := range now.Responses {
				if _, had := was.Responses[status]; !had {
					items = append(items, fmt.Sprintf("%s: `%s` gained a documented `%s` response", surface, key, status))
					continue
				}
				for _, line := range diffFlat(was.Responses[status], now.Responses[status]) {
					items = append(items, fmt.Sprintf("%s: `%s` response `%s`: %s", surface, key, status, line))
				}
			}
			for status := range was.Responses {
				if _, still := now.Responses[status]; !still {
					items = append(items, fmt.Sprintf("%s: `%s` lost its documented `%s` response", surface, key, status))
				}
			}
			for _, line := range diffFlat(was.RequestBody, now.RequestBody) {
				items = append(items, fmt.Sprintf("%s: `%s` request body: %s", surface, key, line))
			}
		}
	}
	r.Add("Responses and request bodies", items)
}

func checkSchemaDrift(in Inputs, r *Report) {
	var items []string
	for _, surface := range surfaces {
		vendored, upstream := in.Vendored[surface], in.Upstream[surface]
		for _, name := range sortedKeys(upstream.Schemas) {
			was, ok := vendored.Schemas[name]
			if !ok {
				items = append(items, fmt.Sprintf("%s: schema `%s` is new upstream", surface, name))
				continue
			}
			for _, line := range diffFlat(flatten(was), flatten(upstream.Schemas[name])) {
				items = append(items, fmt.Sprintf("%s: schema `%s`: %s", surface, name, line))
			}
		}
		for name := range vendored.Schemas {
			if _, still := upstream.Schemas[name]; !still {
				items = append(items, fmt.Sprintf("%s: schema `%s` is gone upstream", surface, name))
			}
		}
	}
	r.Add("Schemas", items)
}

// checkErrorCodeDrift compares the server's error registry with the SDK's Code*
// constants in both directions.
//
// The spec cannot answer this: ErrorResponse types `code` as a bare string, so
// every code the envelope can carry is invisible to a schema comparison. The
// registry in the server's core/errors.py is the real list, which is why the
// fetch pulls that file alongside the two contracts.
func checkErrorCodeDrift(in Inputs, r *Report) {
	allowed := in.Coverage.SDKOnlyCodeSet()
	var items []string
	for code := range in.ServerCodes {
		if _, ok := in.SDKCodes[code]; !ok {
			items = append(items, fmt.Sprintf("the server can return `%s` and errors.go has no constant for it", code))
		}
	}
	for code, constant := range in.SDKCodes {
		if in.ServerCodes[code] {
			continue
		}
		if _, ok := allowed[code]; ok {
			continue
		}
		items = append(items, fmt.Sprintf("`%s` (%s) is not in the server's error registry -- it was removed upstream, or it belongs under sdk_only_error_codes with a reason",
			constant, code))
	}
	r.Add("Error codes", items)
}
