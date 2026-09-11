package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// surfaces are the two REST contracts this SDK speaks, in report order.
var surfaces = []string{"v2", "v1"}

// Inputs is everything a run compares. Upstream is nil on a local-only run.
type Inputs struct {
	Vendored map[string]*Spec
	Upstream map[string]*Spec

	Coverage *Coverage
	SDK      *SDKPackage

	// Conformance is what the client actually did when called: one observation per
	// operation, recorded off the wire. It replaced a static analysis of the same
	// question -- see conformance.go for why.
	Conformance *Conformance

	SDKCodes    map[string]string // wire code -> Go constant name
	ServerCodes map[string]bool   // nil on a local-only run
	// UnreadableCodes are code-producing lines in the server's registry that the
	// extractor could not read. They are reported rather than ignored: a code the
	// gate cannot see is a hole in the comparison, not an absence.
	UnreadableCodes []string

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
//
// A collision is returned rather than resolved. Today none exists, but an
// unversioned route that shadows a versioned one -- /foo alongside /api/v2/foo --
// would otherwise drop an operation out of the inventory comparison entirely, and
// a dropped operation is one this gate reports as accounted for.
func strippedOperations(spec *Spec, surface string) (map[string]*Operation, error) {
	out := make(map[string]*Operation, len(spec.Operations))
	for _, op := range spec.Operations {
		key := op.Method + " " + stripSurface(op.Path, surface)
		if clash, ok := out[key]; ok {
			return nil, fmt.Errorf("%s: %s and %s both reduce to %q once the surface prefix is stripped",
				spec.Path, clash.Path, op.Path, key)
		}
		out[key] = op
	}
	return out, nil
}

// CheckLocal compares the vendored contracts against the SDK itself. Every check
// here is offline and deterministic, which is why it is safe to run on a pull
// request: it fails only on something a person in this repository changed.
func CheckLocal(in Inputs) *Report {
	r := &Report{}
	checkRecordedVersion(in, r)
	checkSurfaceParity(in, r)
	checkInventory(in, r)
	checkImplementation(in, r)
	checkDocumentedResponses(in, r)
	checkQueryParametersSent(in, r)
	checkRequestShapes(in, r)
	checkResponseModels(in, r)
	checkErrorCodesImplemented(in, r)
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
	v1, err1 := strippedOperations(in.Vendored["v1"], "v1")
	v2, err2 := strippedOperations(in.Vendored["v2"], "v2")

	var items []string
	for _, err := range []error{err1, err2} {
		if err != nil {
			items = append(items, err.Error())
		}
	}
	if err1 == nil && err2 == nil {
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
		ops, err := strippedOperations(spec, surface)
		if err != nil {
			continue // reported by checkSurfaceParity
		}
		for key := range ops {
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

// checkImplementation compares each inventory row with what the client really
// did when its method was called.
//
// Nothing here is inferred. drivers.go calls the method, a stub records the
// request, and the route in the report is the request line the SDK emitted. A row
// naming a method that issues a different request, or that no longer reaches the
// wire at all, is a finding; so is an implemented operation no driver covers,
// because an operation nobody exercises is one this gate says nothing about.
func checkImplementation(in Inputs, r *Report) {
	var items []string
	drivers := map[string]Driver{}
	for _, d := range Drivers() {
		drivers[d.Op] = d
	}

	for _, row := range in.Coverage.Operations {
		if row.Unimplemented != "" {
			if _, ok := drivers[row.Key()]; ok {
				items = append(items, fmt.Sprintf("`%s` is recorded as unimplemented and drivers.go calls it", row.Key()))
			}
			continue
		}
		// The row's `sdk:` and `file:` fields are documentation, and cheap to keep
		// true: the method has to exist, and to be where the row says it is. A
		// declaration is the one thing still read out of the source, because it is
		// not control flow -- everything about what the method DOES comes from
		// driving it.
		switch method, ok := in.SDK.Method(row.SDK); {
		case !ok:
			items = append(items, fmt.Sprintf("`%s` claims `%s`, which this package does not declare", row.Key(), row.SDK))
		case method.File != row.File:
			items = append(items, fmt.Sprintf("`%s` says `%s` lives in %s; it is declared in %s",
				row.Key(), row.SDK, row.File, method.File))
		}
		driver, ok := drivers[row.Key()]
		if !ok {
			items = append(items, fmt.Sprintf("`%s` is implemented and drivers.go has no call for it -- add one, or the gate says nothing about this operation", row.Key()))
			continue
		}
		if driver.SDK != row.SDK {
			items = append(items, fmt.Sprintf("`%s`: the inventory names `%s` and drivers.go calls `%s`", row.Key(), row.SDK, driver.SDK))
		}

		if err, failed := in.Conformance.Errors[row.Key()]; failed {
			items = append(items, fmt.Sprintf("`%s`: `%s` did not reach the wire: %v", row.Key(), driver.SDK, err))
			continue
		}
		observed, ok := in.Conformance.Observations[row.Key()]
		if !ok {
			items = append(items, fmt.Sprintf("`%s`: no observation was recorded for `%s`", row.Key(), driver.SDK))
			continue
		}
		// Compared verbatim: the recorder maps each sentinel back to the
		// placeholder it was passed for, so the observed path carries the same
		// names the contract does -- and two arguments in the wrong order no
		// longer produce the expected route.
		if observed.Key() != row.Key() {
			items = append(items, fmt.Sprintf("`%s`: `%s` requests `%s`", row.Key(), driver.SDK, strings.ToUpper(observed.Method)+" "+observed.Path))
		}
		// A call error against a body the STUB built from the vendored schema is a
		// contract failure, not a transport one: the only thing that can go wrong
		// here is decoding, and it goes wrong when the model no longer fits the
		// schema -- a retyped property, most often.
		if observed.CallErr != nil {
			items = append(items, fmt.Sprintf("`%s`: `%s` could not handle a response built from the vendored schema: %v",
				row.Key(), driver.SDK, observed.CallErr))
		}
	}

	for op := range drivers {
		if _, ok := in.Coverage.ByKey()[op]; !ok {
			items = append(items, fmt.Sprintf("drivers.go calls `%s`, which %s does not list", op, in.Coverage.Path))
		}
	}
	r.Add("Implementation", items)
}

// checkDocumentedResponses asserts each recorded divergence between the generated
// spec and the running server is STILL a divergence.
//
// This is the list-envelope exception, and it is written as an assertion rather
// than a suppression on purpose. The spec documents list responses as bare arrays
// while the server returns {data, pagination}; nothing in this gate flags that,
// because the SDK side of the comparison is the envelope the stub wraps its body
// in -- the server's shape, recorded in the inventory -- and a client that could
// not decode it would have failed in checkImplementation above. What is genuinely
// useful to know is the day it stops being true -- the day the server's generator
// learns about the renderer and the SDK can stop carrying a documented workaround.
// So the inventory records the shape the spec documents, and this check reports
// when the spec changes out from under it.
func checkDocumentedResponses(in Inputs, r *Report) {
	var items []string
	for _, surface := range surfaces {
		ops, err := strippedOperations(in.Vendored[surface], surface)
		if err != nil {
			continue
		}
		for _, row := range in.Coverage.Operations {
			op, ok := ops[row.Key()]
			if !ok {
				continue // reported by checkInventory
			}
			if op.OKSchema != row.DocumentedResponse {
				items = append(items, fmt.Sprintf("`%s` (%s): %s now documents a `%s` success body, recorded as `%s` -- the server really returns `%s`, so re-check which side moved",
					row.Key(), surface, in.Vendored[surface].Path, op.OKSchema, row.DocumentedResponse, row.ActualResponse))
			}
		}
	}
	r.Add("Documented response shapes", items)
}

// checkQueryParametersSent compares the query parameters the contract documents
// with the ones the client PUT ON THE WIRE, in both directions.
//
// Both directions, because each names a different mistake. A documented parameter
// the client never sends is an unimplemented filter -- how `scope` on
// /tag-resolution could have gone unnoticed, and how `q` and `slug` on
// /vocabularies (#36) surfaced here. A parameter the client sends that nothing
// documents is a request the server will ignore, silently, which is the failure
// mode this SDK refuses everywhere else.
//
// The driver behind each observation sets every field the method offers, so a
// parameter missing from the wire is missing from the CLIENT, not from the call.
func checkQueryParametersSent(in Inputs, r *Report) {
	allowed := in.Coverage.UnsentIndex()
	used := map[string]bool{}
	var items []string

	// The v2 contract, and only v2. The client under test targets v2 -- that is
	// the SDK's default surface -- so v2 is what its requests must match. v1
	// documents strictly fewer parameters (no namespace axis, so no
	// `include_global`), and comparing a v2 request against it would report every
	// namespaced read as sending something undocumented. Surface parity has its
	// own check.
	{
		surface := "v2"
		ops, err := strippedOperations(in.Vendored[surface], surface)
		if err != nil {
			r.Add("Query parameters", items)
			return
		}
		for _, row := range in.Coverage.Operations {
			op, ok := ops[row.Key()]
			if !ok || row.Unimplemented != "" {
				continue
			}
			observed, ok := in.Conformance.Observations[row.Key()]
			if !ok {
				continue // reported by checkImplementation
			}
			documented := map[string]bool{}
			for _, name := range op.QueryParams() {
				documented[name] = true
				if observed.Query[name] {
					continue
				}
				key := row.Key() + " query " + name
				if _, ok := allowed[key]; ok {
					used[key] = true
					continue
				}
				items = append(items, fmt.Sprintf("`%s` documents the query parameter `%s` and the client did not send it -- implement it or record it under unsent_query_parameters",
					row.Key(), name))
			}
			for _, name := range sortedNames(observed.Query) {
				if !documented[name] {
					items = append(items, fmt.Sprintf("`%s`: the client sends the query parameter `%s`, which no vendored contract documents -- the server will ignore it",
						row.Key(), name))
				}
			}
		}
	}

	for key := range allowed {
		if !strings.Contains(key, " query ") {
			continue // checkRequestShapes owns body and header rows
		}
		if !used[key] {
			items = append(items, fmt.Sprintf("`%s` is listed under unsent_inputs and the client sends it now, or the contract stopped documenting it -- drop the row", key))
		}
	}
	r.Add("Query parameters", dedupe(items))
}

// checkResponseModels compares the contract's response schema with what survived
// a round trip through the SDK's model.
//
// This is "added or changed fields on models the SDK decodes", and it is the half
// a spec-to-spec diff cannot do at all: once someone refreshes the vendored files,
// an upstream comparison is green by construction while the Go model still has no
// field for the property that arrived.
//
// The stub answered with a body built FROM the schema, so the comparison needs no
// knowledge of Go types. A property the model has no field for is dropped on the
// way back out and is missing here; a property whose type no longer fits fails to
// decode at all and was reported by checkImplementation.
//
// The v2 contract is the reference, as it is everywhere else in this SDK: v1 has
// no namespace axis, so its schemas omit the namespace_type / namespace_id fields
// seven models carry. Surface parity is checked separately.
//
// Only rows whose server shape is a plain resource take part. The composites --
// bulk assign, bulk remove, the resource-tag replace -- decode into result structs
// the contract does not describe (it claims a bare array, or nothing at all), and
// the health probes answer outside the API surface entirely; all of those are
// recorded divergences, and comparing them against the spec would report the
// divergence as drift on every run.
func checkResponseModels(in Inputs, r *Report) {
	spec := in.Vendored["v2"]
	ops, err := strippedOperations(spec, "v2")
	if err != nil {
		return // reported by checkSurfaceParity
	}
	undocumented := in.Coverage.UndocumentedFieldIndex()
	used := map[string]bool{}
	var items []string

	for _, row := range in.Coverage.Operations {
		if row.Unimplemented != "" {
			continue
		}
		if row.ActualResponse != "list-envelope" && row.ActualResponse != "data-envelope" {
			continue
		}
		op, ok := ops[row.Key()]
		if !ok || op.OKModel == "" {
			continue
		}
		observed, ok := in.Conformance.Observations[row.Key()]
		if !ok || observed.CallErr != nil {
			continue // reported by checkImplementation
		}
		documented, ok := spec.SchemaProperties(op.OKModel)
		switch {
		case !ok:
			items = append(items, fmt.Sprintf("`%s`: the contract's success body references schema `%s`, which components.schemas does not define", row.Key(), op.OKModel))
			continue
		case len(documented) == 0:
			items = append(items, fmt.Sprintf("`%s`: schema `%s` documents no properties, so there is nothing to compare the decoded model against", row.Key(), op.OKModel))
			continue
		case len(observed.Decoded) == 0:
			items = append(items, fmt.Sprintf("`%s`: the decoded response carried no fields at all -- a body built from schema `%s` went in and nothing came back out", row.Key(), op.OKModel))
			continue
		}

		documentedSet := map[string]bool{}
		for _, property := range documented {
			documentedSet[property] = true
			if _, survived := observed.Decoded[property]; !survived {
				items = append(items, fmt.Sprintf("schema `%s` documents `%s` and it does not survive decoding by `%s` -- the model has no field for it",
					op.OKModel, property, row.SDK))
			}
		}

		// The null witness. A property the contract marks nullable has to come back
		// as null, not as the zero value the model fell back to -- `0` for an int
		// where the contract now permits absence is the silent-zero failure this
		// SDK refuses everywhere else, arriving through the contract rather than
		// through a decoder.
		if observed.NullWitnessErr != nil {
			items = append(items, fmt.Sprintf("`%s`: `%s` could not decode a response whose nullable properties are null: %v",
				row.Key(), row.SDK, observed.NullWitnessErr))
			continue
		}
		for _, property := range nullableProperties(spec, op.OKModel) {
			value, survived := observed.NullWitness[property]
			if !survived {
				continue // already reported above
			}
			if string(value) != "null" {
				items = append(items, fmt.Sprintf("schema `%s` marks `%s` nullable and `%s` decodes null as `%s` -- the model cannot represent the absent state, so a null from the server reads as a value",
					op.OKModel, property, row.SDK, string(value)))
			}
		}
		for _, field := range sortedFields(observed.Decoded) {
			if documentedSet[field] {
				continue
			}
			key := op.OKModel + "." + field
			if _, ok := undocumented[key]; ok {
				used[key] = true
				continue
			}
			items = append(items, fmt.Sprintf("`%s` decodes `%s` into its model, which schema `%s` does not document -- the server withdrew it, or it belongs under undocumented_model_fields",
				row.SDK, field, op.OKModel))
		}
	}

	for key := range undocumented {
		if !used[key] {
			items = append(items, fmt.Sprintf("`%s` is listed under undocumented_model_fields and is either documented now or no longer decoded -- drop the row", key))
		}
	}
	r.Add("Response models", dedupe(items))
}

// checkErrorCodesImplemented is the offline half of the error-code comparison:
// the VENDORED registry in docs/contract-coverage.yaml against errors.go.
//
// It exists for the same reason the vendored contracts do. The codes are not in
// the OpenAPI documents -- ErrorResponse types `code` as a bare string -- so
// without a copy in the repository the only comparison possible would be
// SDK-against-upstream, which needs the network and runs weekly. That would have
// left error codes as the one item on issue #18's list where "refresh and
// implement later" was still a state this repository could be left in.
func checkErrorCodesImplemented(in Inputs, r *Report) {
	vendored := in.Coverage.ServerErrorCodeSet()
	allowed := in.Coverage.SDKOnlyCodeSet()
	var items []string

	for code := range vendored {
		if _, ok := in.SDKCodes[code]; !ok {
			items = append(items, fmt.Sprintf("`%s` is in the vendored error registry and errors.go declares no constant for it", code))
		}
	}
	for code, constant := range in.SDKCodes {
		if vendored[code] || allowed[code] != "" {
			continue
		}
		items = append(items, fmt.Sprintf("`%s` (%s) is in neither the vendored error registry nor sdk_only_error_codes -- record which it is", constant, code))
	}
	for code := range allowed {
		if _, ok := in.SDKCodes[code]; !ok {
			items = append(items, fmt.Sprintf("`%s` is listed under sdk_only_error_codes and errors.go declares no constant for it -- drop the row", code))
		}
	}
	r.Add("Error codes", items)
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
			for _, param := range sortedKeys(now.Params) {
				if _, had := was.Params[param]; !had {
					items = append(items, fmt.Sprintf("%s: `%s` gained parameter `%s`", surface, key, param))
					continue
				}
				for _, line := range diffFlat(was.Params[param], now.Params[param]) {
					items = append(items, fmt.Sprintf("%s: `%s` parameter `%s`: %s", surface, key, param, line))
				}
			}
			for _, param := range sortedKeys(was.Params) {
				if _, still := now.Params[param]; !still {
					items = append(items, fmt.Sprintf("%s: `%s` lost parameter `%s`", surface, key, param))
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
			for _, status := range sortedKeys(now.Responses) {
				if _, had := was.Responses[status]; !had {
					items = append(items, fmt.Sprintf("%s: `%s` gained a documented `%s` response", surface, key, status))
					continue
				}
				for _, line := range diffFlat(was.Responses[status], now.Responses[status]) {
					items = append(items, fmt.Sprintf("%s: `%s` response `%s`: %s", surface, key, status, line))
				}
			}
			for _, status := range sortedKeys(was.Responses) {
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
	vendored := in.Coverage.ServerErrorCodeSet()
	var items []string
	// Vendored registry against the real one, in both directions -- the same shape
	// as the contract comparison above it. The SDK's own constants are checked
	// against the vendored copy offline (checkErrorCodesImplemented), so the two
	// hops together say whether errors.go matches the server, and each hop names
	// which side moved.
	for code := range in.ServerCodes {
		if !vendored[code] {
			items = append(items, fmt.Sprintf("the server can return `%s`, which the vendored registry in %s does not list", code, in.Coverage.Path))
		}
	}
	for code := range vendored {
		if !in.ServerCodes[code] {
			items = append(items, fmt.Sprintf("the vendored registry lists `%s` and the server no longer raises it -- drop it, and decide what the SDK's constant becomes", code))
		}
	}
	// A form the extractor cannot read is reported rather than passed over. The
	// count floor in ServerErrorCodes catches a regexp that stopped matching
	// wholesale; this catches the quieter case of one new code written in a
	// spelling the extractor does not understand, which leaves the count healthy
	// and the comparison one code short.
	for _, line := range in.UnreadableCodes {
		items = append(items, fmt.Sprintf("the server's registry produces a code in a form this gate cannot read: `%s` -- teach tools/contractdrift to read it", line))
	}
	r.Add("Error codes", items)
}

// nullableProperties returns the properties a component schema marks nullable.
func nullableProperties(spec *Spec, name string) []string {
	node, ok := spec.Schemas[name]
	if !ok {
		return nil
	}
	root := node
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		root = root.Content[0]
	}
	props := mappingValue(root, "properties")
	if props == nil {
		return nil
	}
	var out []string
	for i := 0; i+1 < len(props.Content); i += 2 {
		var schema struct {
			Nullable bool `yaml:"nullable"`
		}
		if err := props.Content[i+1].Decode(&schema); err == nil && schema.Nullable {
			out = append(out, props.Content[i].Value)
		}
	}
	sort.Strings(out)
	return out
}

// SchemaProperties returns the property names a component schema documents.
func (s *Spec) SchemaProperties(name string) ([]string, bool) {
	node, ok := s.Schemas[name]
	if !ok {
		return nil, false
	}
	root := node
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		root = root.Content[0]
	}
	if root.Kind != yaml.MappingNode {
		return nil, false
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "properties" {
			continue
		}
		props := root.Content[i+1]
		if props.Kind != yaml.MappingNode {
			return nil, false
		}
		out := make([]string, 0, len(props.Content)/2)
		for j := 0; j < len(props.Content); j += 2 {
			out = append(out, props.Content[j].Value)
		}
		sort.Strings(out)
		return out, true
	}
	return nil, true
}

// dedupe collapses identical findings. One inventory row is checked against both
// surfaces, and an unimplemented parameter is unimplemented on both.
func dedupe(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	sort.Strings(items)
	out := items[:1]
	for _, item := range items[1:] {
		if item != out[len(out)-1] {
			out = append(out, item)
		}
	}
	return out
}

// checkRequestShapes compares what the client PUT IN THE REQUEST with what the
// operation documents: the JSON properties of its request schema, and its header
// parameters.
//
// This is the half the gate did not have, and the hole was the same shape as the
// query one that preceded it. A write whose method stopped passing its payload
// emits the same verb, the same path and the same query as one that still does,
// so every check was satisfied while the client sent nothing at all. Documented
// headers were invisible for the same reason: the v2 contract documents
// `X-Namespace-*` on every operation, and a method that stopped propagating them
// looked exactly like one that never could.
//
// It also makes drivers.go check itself. A driver that leaves a field unset sends
// a body missing that property, and the run says so -- which is what turns "every
// parameter is populated" from a promise in a comment into something a reader can
// stop taking on trust.
func checkRequestShapes(in Inputs, r *Report) {
	spec := in.Vendored["v2"]
	ops, err := strippedOperations(spec, "v2")
	if err != nil {
		return // reported by checkSurfaceParity
	}
	unsent := in.Coverage.UnsentIndex()
	clientHeaders := in.Coverage.ClientHeaderSet()
	used := map[string]bool{}
	var items []string

	for _, row := range in.Coverage.Operations {
		if row.Unimplemented != "" {
			continue
		}
		op, ok := ops[row.Key()]
		if !ok {
			continue
		}
		observed, ok := in.Conformance.Observations[row.Key()]
		if !ok {
			continue // reported by checkImplementation
		}

		// --- headers ---
		documentedHeaders := map[string]bool{}
		for _, name := range op.HeaderParams() {
			canonical := http.CanonicalHeaderKey(name)
			documentedHeaders[canonical] = true
			if observed.Headers[canonical] {
				continue
			}
			key := row.Key() + " header " + name
			if _, ok := unsent[key]; ok {
				used[key] = true
				continue
			}
			items = append(items, fmt.Sprintf("`%s` documents the header `%s` and the client did not send it", row.Key(), name))
		}
		for _, name := range sortedNames(observed.Headers) {
			if !documentedHeaders[name] && !clientHeaders[name] {
				items = append(items, fmt.Sprintf("`%s`: the client sends the header `%s`, which no vendored contract documents", row.Key(), name))
			}
		}

		// --- request body ---
		switch {
		case op.RequestModel == "" && len(observed.Body) > 0:
			// The one live case is DELETE /tag-assignments, whose ids travel in a
			// body the generated spec does not describe -- a fourth divergence in
			// the same family as the envelopes, and recorded the same way.
			if row.UndocumentedRequestBody == "" {
				items = append(items, fmt.Sprintf("`%s`: the client sends a request body (%s) and the contract documents none -- record it under undocumented_request_body",
					row.Key(), strings.Join(sortedNames(observed.Body), ", ")))
			}
			continue
		case op.RequestModel == "":
			continue
		case row.UndocumentedRequestBody != "":
			items = append(items, fmt.Sprintf("`%s` records an undocumented request body and the contract now documents `%s` -- drop the row",
				row.Key(), op.RequestModel))
			continue
		case len(observed.Body) == 0:
			items = append(items, fmt.Sprintf("`%s`: the contract documents a `%s` request body and the client sent none",
				row.Key(), op.RequestModel))
			continue
		}

		documented, ok := spec.SchemaProperties(op.RequestModel)
		if !ok {
			items = append(items, fmt.Sprintf("`%s`: the request body references schema `%s`, which components.schemas does not define", row.Key(), op.RequestModel))
			continue
		}
		documentedSet := map[string]bool{}
		for _, property := range documented {
			documentedSet[property] = true
			if observed.Body[property] {
				continue
			}
			key := row.Key() + " body " + property
			if _, ok := unsent[key]; ok {
				used[key] = true
				continue
			}
			items = append(items, fmt.Sprintf("`%s`: schema `%s` documents `%s` and the client did not send it -- implement it, or populate it in drivers.go",
				row.Key(), op.RequestModel, property))
		}
		for _, property := range sortedNames(observed.Body) {
			if !documentedSet[property] {
				items = append(items, fmt.Sprintf("`%s`: the client sends `%s` in its request body, which schema `%s` does not document",
					row.Key(), property, op.RequestModel))
			}
		}
	}
	// A row claiming an input travels in the body has to be TRUE: the parameter
	// must actually be in the body the client sent. Eleven of these say
	// application_id moves from the query string into the payload, and without
	// this they would turn a missing query key green while proving nothing about
	// the replacement path.
	for key, reason := range unsent {
		if !strings.Contains(key, " query ") || !strings.Contains(reason, "ApplicationID") {
			continue
		}
		op, _, _ := strings.Cut(strings.TrimPrefix(key, ""), " query ")
		observed, ok := in.Conformance.Observations[op]
		if !ok {
			continue
		}
		if !observed.Body["application_id"] {
			items = append(items, fmt.Sprintf("`%s` records that application_id travels in the body, and the request body does not carry it", op))
		}
	}
	for key := range unsent {
		if strings.Contains(key, " query ") {
			continue // checkQueryParametersSent owns those
		}
		if !used[key] {
			items = append(items, fmt.Sprintf("`%s` is listed under unsent_inputs and the client sends it now, or the contract stopped documenting it -- drop the row", key))
		}
	}
	r.Add("Request shapes", dedupe(items))
}
