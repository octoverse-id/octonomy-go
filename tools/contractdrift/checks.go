package main

import (
	"fmt"
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

// checkImplementation reads what each implemented row's method actually does and
// compares it with what the row claims.
//
// The first version of this checked only that a function with the right name
// existed, which proves very little: a row could name an unrelated method, or the
// method could have been repointed at a different route, and the gate would agree
// the operation was covered. So the route -- HTTP method, path, and the transport
// helper that determines the response envelope -- is derived from the method body
// and compared instead.
//
// A route that cannot be derived is a FINDING, not a pass. "I could not tell" and
// "it is fine" are different answers, and only one of them is safe to act on.
func checkImplementation(in Inputs, r *Report) {
	var items []string
	for _, row := range in.Coverage.Operations {
		if row.Unimplemented != "" {
			continue
		}
		method, ok := in.SDK.Method(row.SDK)
		if !ok {
			items = append(items, fmt.Sprintf("`%s` claims `%s`, which this package does not declare", row.Key(), row.SDK))
			continue
		}
		if method.File != row.File {
			items = append(items, fmt.Sprintf("`%s` says `%s` lives in %s; it is declared in %s",
				row.Key(), row.SDK, row.File, method.File))
		}

		route, err := in.SDK.Route(row.SDK)
		if err != nil {
			items = append(items, fmt.Sprintf("`%s`: cannot read the route out of `%s`: %v -- teach tools/contractdrift the new shape, or keep the one AGENTS.md asks resource files to hold",
				row.Key(), row.SDK, err))
			continue
		}
		if route.Method != row.Method {
			items = append(items, fmt.Sprintf("`%s`: `%s` issues a %s, not a %s", row.Key(), row.SDK, strings.ToUpper(route.Method), strings.ToUpper(row.Method)))
		}
		if want := normalizePath(row.Path); route.Path != want {
			items = append(items, fmt.Sprintf("`%s`: `%s` requests `%s`, not `%s`", row.Key(), row.SDK, route.Path, want))
		}
		if envelope, ok := transportHelpers[route.Helper]; ok && !envelopeAllows(envelope, row.ActualResponse) {
			items = append(items, fmt.Sprintf("`%s`: `%s` decodes through %s, which yields `%s`, but the row records `%s`",
				row.Key(), row.SDK, route.Helper, envelope, row.ActualResponse))
		}
		// A decoding helper whose type argument could not be read leaves the
		// response-model comparison with nothing to compare -- which it would then
		// skip in silence. Go can infer a type argument, so this is reachable
		// without anyone doing anything wrong; it just has to be said out loud.
		if decodes := route.Helper == "doData" || route.Helper == "doList"; decodes && route.Model == "" {
			items = append(items, fmt.Sprintf("`%s`: cannot read the type `%s` decodes into -- write the type argument out (`%s[T](...)`) so the response model can be compared",
				row.Key(), row.SDK, route.Helper))
		}
	}
	r.Add("Implementation", items)
}

// envelopeAllows maps a transport helper's envelope to the `actual_response`
// values a row may record for it. The one place two values are legal is doData:
// it unwraps `{"data": ...}` whether what is inside is a resource or one of the
// bulk composites, and the distinction between those two is documentary.
func envelopeAllows(envelope, recorded string) bool {
	if envelope == recorded {
		return true
	}
	return envelope == "data-envelope" && recorded == "composite-envelope"
}

// checkDocumentedResponses asserts each recorded divergence between the generated
// spec and the running server is STILL a divergence.
//
// This is the list-envelope exception, and it is written as an assertion rather
// than a suppression on purpose. The spec documents list responses as bare arrays
// while the server returns {data, pagination}; nothing in this gate flags that,
// because the SDK side of the comparison is the transport helper the method calls
// (checkImplementation above), which is the server's shape and not the spec's.
// What is genuinely useful to know is the day it stops being true -- the day the
// server's generator learns about the renderer and the SDK can stop carrying a
// documented workaround. So the inventory records the shape the spec documents,
// and this check reports when the spec changes out from under it.
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

// checkQueryParametersSent is the parameter half of the gate, pointed at the SDK
// rather than at the server, and it is PER OPERATION.
//
// Per operation is the whole point and the first draft got it wrong: it asked
// whether a parameter name appeared anywhere in the package, so `limit` on
// /tag-resolution counted as implemented because pagination.go sets `limit` on
// the list routes. What it asks now is whether THIS method's params struct builds
// it -- following the struct in the method's signature into its query builder and
// anything it embeds -- or whether the transport sets it at the chokepoint for
// every call, which `application_id` and `include_global` genuinely are.
//
// The upstream comparison catches the server adding a parameter. This catches the
// step after it: a vendored contract refreshed to include that parameter while no
// resource ever learned to send it. That is how `scope` on /tag-resolution could
// have been missed, and an upstream-versus-vendored diff alone goes green the
// moment the files are refreshed.
func checkQueryParametersSent(in Inputs, r *Report) {
	allowed := in.Coverage.UnsentIndex()
	used := map[string]bool{}
	var items []string

	for _, surface := range surfaces {
		ops, err := strippedOperations(in.Vendored[surface], surface)
		if err != nil {
			continue
		}
		for _, row := range in.Coverage.Operations {
			op, ok := ops[row.Key()]
			if !ok || row.Unimplemented != "" {
				continue
			}
			sent, err := in.SDK.QueryParams(row.SDK)
			if err != nil {
				continue // reported by checkImplementation
			}
			for _, name := range op.QueryParams() {
				if sent[name] {
					continue
				}
				key := row.Key() + " " + name
				if _, ok := allowed[key]; ok {
					used[key] = true
					continue
				}
				// No surface in the message: both contracts document the same
				// parameters, so naming one would suggest the gap is
				// version-specific when it is not. dedupe collapses the pair.
				items = append(items, fmt.Sprintf("`%s` documents the query parameter `%s` and `%s` never sends it -- implement it or record it under unsent_query_parameters",
					row.Key(), name, row.SDK))
			}
		}
	}

	for key := range allowed {
		if !used[key] {
			items = append(items, fmt.Sprintf("`%s` is listed under unsent_query_parameters and no vendored contract documents it there -- drop the row", key))
		}
	}
	r.Add("Query parameters the client does not send", dedupe(items))
}

// checkResponseModels compares the contract's response schema with the Go struct
// the method decodes into, field by field.
//
// This is "added or changed fields on models the SDK decodes", and it is the half
// a spec-to-spec diff cannot do at all: once someone refreshes the vendored files,
// an upstream comparison is green by construction, while the Go model still has no
// field for the property that arrived. The decoded type comes from the method's
// own `doData[T]` / `doList[T]`, so it is what the code really does rather than
// what a table says.
//
// The v2 contract is the reference, as it is everywhere else in this SDK: v1 has
// no namespace axis, so its schemas omit the namespace_type / namespace_id fields
// seven models carry, and comparing a Go model against v1 would report those as
// undocumented on every run. Surface parity is checked separately.
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
		route, err := in.SDK.Route(row.SDK)
		if err != nil || route.Model == "" {
			continue // both reported by checkImplementation
		}
		documented, ok := spec.SchemaProperties(op.OKModel)
		switch {
		case !ok:
			items = append(items, fmt.Sprintf("`%s`: the contract's success body references schema `%s`, which components.schemas does not define", row.Key(), op.OKModel))
			continue
		case len(documented) == 0:
			// Said once, rather than reporting every field of the Go model as
			// undocumented: a schema with no properties is a document this gate
			// could not read, not a model with nothing in it.
			items = append(items, fmt.Sprintf("`%s`: schema `%s` documents no properties, so there is nothing to compare `%s` against", row.Key(), op.OKModel, route.Model))
			continue
		}
		decoded, ok := in.SDK.JSONFields(route.Model)
		if !ok {
			items = append(items, fmt.Sprintf("`%s`: `%s` decodes into `%s`, which is not a struct this package declares", row.Key(), row.SDK, route.Model))
			continue
		}

		for _, property := range documented {
			if _, has := decoded[property]; !has {
				items = append(items, fmt.Sprintf("schema `%s` documents `%s` and the Go model `%s` has no field for it -- %s decodes it away",
					op.OKModel, property, route.Model, row.SDK))
			}
		}
		documentedSet := map[string]bool{}
		for _, property := range documented {
			documentedSet[property] = true
		}
		for _, field := range sortedKeys(decoded) {
			if documentedSet[field] {
				continue
			}
			key := route.Model + "." + field
			if _, ok := undocumented[key]; ok {
				used[key] = true
				continue
			}
			items = append(items, fmt.Sprintf("the Go model `%s` decodes `%s`, which schema `%s` does not document -- the server withdrew it, or it belongs under undocumented_model_fields",
				route.Model, field, op.OKModel))
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
