package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strconv"
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
	checkEmittedValues(in, r)
	checkResponseModels(in, r)
	checkDecodedValues(in, r)
	checkModelFieldNames(in, r)
	checkErrorCodesImplemented(in, r)
	checkErrorEnvelope(in, r)
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

	for _, surface := range surfaces {
		for _, row := range in.Coverage.Operations {
			if row.Unimplemented != "" {
				if _, ok := drivers[row.Key()]; ok {
					items = append(items, fmt.Sprintf("`%s` is recorded as unimplemented and drivers.go calls it", row.Key()))
				}
				continue
			}
			// The row's `sdk:` field is documentation, and cheap to keep true: the
			// method it names has to exist. A declaration is the one thing still read
			// out of the source, because it is not control flow -- everything about
			// what the method DOES comes from driving it.
			//
			// It used to assert the FILE too. That went: a warning when a working
			// method moves between source files is not worth a field in a checked
			// inventory, and the compiled driver plus the observed route prove the
			// behavior either way.
			if _, ok := in.SDK.Method(row.SDK); !ok {
				items = append(items, fmt.Sprintf("`%s` claims `%s`, which this package does not declare", row.Key(), row.SDK))
			}
			driver, ok := drivers[row.Key()]
			if !ok {
				items = append(items, fmt.Sprintf("`%s` is implemented and drivers.go has no call for it -- add one, or the gate says nothing about this operation", row.Key()))
				continue
			}
			if driver.SDK != row.SDK {
				items = append(items, fmt.Sprintf("`%s`: the inventory names `%s` and drivers.go calls `%s`", row.Key(), row.SDK, driver.SDK))
			}

			if err, failed := in.Conformance.Errors[surface+" "+row.Key()]; failed {
				items = append(items, fmt.Sprintf("`%s`: `%s` did not reach the wire: %v", row.Key(), driver.SDK, err))
				continue
			}
			observed, ok := in.Conformance.Observations[surface+" "+row.Key()]
			if !ok {
				items = append(items, fmt.Sprintf("`%s`: no observation was recorded for `%s`", row.Key(), driver.SDK))
				continue
			}
			// The versioned prefix, which the suffix normalization erases. A client
			// that ignored its configured APIVersion sent every "v1" request to
			// /api/v2 and nothing saw it, because the one field that differed was
			// the one being thrown away.
			if want := versionedPrefix(surface, row.Path); observed.Prefix != want {
				items = append(items, fmt.Sprintf("`%s` (%s): the client sent it to `%s`",
					row.Key(), surface, observed.Prefix+observed.Path))
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

	}
	for op := range drivers {
		if _, ok := in.Coverage.ByKey()[op]; !ok {
			items = append(items, fmt.Sprintf("drivers.go calls `%s`, which %s does not list", op, in.Coverage.Path))
		}
	}
	r.Add("Implementation", dedupe(items))
}

// versionedPrefix is the /api/<version> an operation should be sent to. The health
// probes sit outside the versioned API and carry none.
func versionedPrefix(surface, path string) string {
	if strings.HasPrefix(path, "/health/") {
		return ""
	}
	return "/api/" + surface
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
	undocumented := in.Coverage.UndocumentedIndex()
	used := map[string]bool{}
	usedUndocumented := map[string]bool{}
	var items []string

	// BOTH surfaces, each against a client configured for it. This used to be v2
	// only -- the default -- so a parameter added to a v1 operation could land with
	// no client follow-through and the offline gate stayed clean, which is the
	// hole this gate exists to close, on the half of the API it was not looking at.
	for _, surface := range surfaces {
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
			observed, ok := in.Conformance.Observations[surface+" "+row.Key()]
			if !ok {
				continue // reported by checkImplementation
			}
			documented := map[string]bool{}
			for _, name := range op.QueryParams() {
				documented[name] = true
				value, sent := observed.Query[name]
				if !sent {
					key := row.Key() + " query " + name
					if _, ok := allowed[key]; ok {
						used[key] = true
						continue
					}
					items = append(items, fmt.Sprintf("`%s` documents the query parameter `%s` and the client did not send it -- implement it or record it under unsent_query_parameters",
						row.Key(), name))
					continue
				}
				// BOTH executions, each against its OWN expectation. Comparing the
				// two runs for equality and merely ALLOWING the declared pair when
				// they differed was an exemption rather than an assertion: a client
				// hard-coding one pass's value satisfied it on both.
				items = append(items, valueFindings(row.Key(), "query parameter", name, value, 0, op.ParamSchema("query", name))...)
				if second := observed.Second; second != nil {
					if v, ok := second.Query[name]; ok {
						items = append(items, valueFindings(row.Key(), "query parameter", name, v, 1, op.ParamSchema("query", name))...)
					}
				}
			}
			for _, name := range sortedStrings(observed.Query) {
				if documented[name] {
					continue
				}
				key := surface + " query " + name
				if _, ok := undocumented[key]; ok {
					usedUndocumented[key] = true
					continue
				}
				items = append(items, fmt.Sprintf("`%s` (%s): the client sends the query parameter `%s`, which that surface's contract does not document -- the server will ignore it",
					row.Key(), surface, name))
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
	for key := range undocumented {
		if !usedUndocumented[key] {
			items = append(items, fmt.Sprintf("`%s` is listed under undocumented_inputs and that surface documents it now, or the client stopped sending it -- drop the row", key))
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
	undocumented := in.Coverage.UndocumentedFieldIndex()
	used := map[string]bool{}
	var items []string

	// BOTH surfaces for the FORWARD direction -- every documented property has to
	// survive decoding -- because an unknown JSON property decodes without error,
	// so merely calling the v1 method proves nothing about its fields. A review
	// added a v1-only property to the staged Tag schema, the v1 stub sent it, the
	// model dropped it, and the report stayed clean.
	//
	// The REVERSE direction stays v2-authoritative: v1's schemas omit the
	// namespace_type / namespace_id every model carries, so asking v1 to account
	// for them would report the namespace axis as undocumented on every row.
	for _, surface := range surfaces {
		spec := in.Vendored[surface]
		ops, err := strippedOperations(spec, surface)
		if err != nil {
			continue // reported by checkSurfaceParity
		}
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
			observed, ok := in.Conformance.Observations[surface+" "+row.Key()]
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

			// The list envelope, from the SENT side so absence is visible.
			for _, field := range sortedFields(observed.Sent) {
				if !strings.HasPrefix(field, "pagination.") {
					continue
				}
				name := strings.TrimPrefix(field, "pagination.")
				decoded, survived := observed.Decoded[field]
				switch {
				case !survived:
					items = append(items, fmt.Sprintf("`%s`: the list envelope carries `%s` and `%s` decodes it away",
						row.Key(), name, row.SDK))
				case !containsJSON(observed.Sent[field], decoded):
					items = append(items, fmt.Sprintf("`%s`: the list envelope sent `%s` as %s and `%s` returned %s",
						row.Key(), name, string(observed.Sent[field]), row.SDK, string(decoded)))
				}
			}

			documentedSet := map[string]bool{}
			for _, property := range documented {
				documentedSet[property] = true
				decoded, survived := observed.Decoded[property]
				if survived {
					// The VALUE, not just the key. The stub knows what it sent, so a
					// model that puts one property's contents into another field --
					// through a custom UnmarshalJSON, a post-decode assignment, or a
					// composite decoder -- returns every key with the wrong data, and
					// only comparing the pair says so.
					if sent, ok := observed.Sent[property]; ok && !containsJSON(sent, decoded) {
						items = append(items, fmt.Sprintf("schema `%s` sent `%s` as %s and `%s` returned %s -- the model is not putting it where it belongs",
							op.OKModel, property, string(sent), row.SDK, string(decoded)))
					}
				}
				if !survived {
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
					// The field carries `omitempty`, so null came back as the zero
					// value and the key was dropped entirely. Whether that is a
					// problem depends on what the zero value IS, and the populated
					// witness says: a field that marshals as an object or an array is
					// a Go map or slice, whose nil is exactly the absent state; a
					// field that marshals as a scalar has a zero value indistinguishable
					// from a real one, and the null is lost.
					//
					// This was a silent pass until a review's own experiment walked
					// into it -- the skip here read "already reported above", and
					// above only reports what the POPULATED witness dropped.
					if populated, ok := observed.Decoded[property]; ok && !isJSONContainer(populated) {
						items = append(items, fmt.Sprintf("schema `%s` marks `%s` nullable and `%s` decodes null into a field that omits it -- a scalar with `omitempty` cannot tell an absent value from a zero one",
							op.OKModel, property, row.SDK))
					}
					continue
				}
				if string(value) != "null" {
					items = append(items, fmt.Sprintf("schema `%s` marks `%s` nullable and `%s` decodes null as `%s` -- the model cannot represent the absent state, so a null from the server reads as a value",
						op.OKModel, property, row.SDK, string(value)))
				}
			}
			if surface != "v2" {
				continue // the reverse direction is v2-authoritative; see above
			}
			for _, field := range sortedFields(observed.Decoded) {
				if documentedSet[field] {
					continue
				}
				// The pagination block belongs to the ENVELOPE, not to the resource
				// schema -- the contract describes neither, which is the recorded
				// divergence. It is compared separately, below, from the SENT side:
				// walking the decoded side alone could never notice a field the model
				// dropped, because a dropped field is not there to walk.
				if strings.HasPrefix(field, "pagination.") {
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

	}
	for key := range undocumented {
		if !used[key] {
			items = append(items, fmt.Sprintf("`%s` is listed under undocumented_model_fields and is either documented now or no longer decoded -- drop the row", key))
		}
	}
	r.Add("Response models", dedupe(items))
}

// unreachableModels are the response models no operation's success schema names,
// and which the loop in checkModelFieldNames therefore cannot discover: the list
// envelope's pagination block, the three composite results whose bodies the
// contract describes wrongly or not at all, and the health payload, which sits
// outside the API surface.
var unreachableModels = []string{
	"Pagination", "BulkAssignResult", "BulkRemoveResult", "ResourceReplaceResult", "HealthStatus",
}

// fieldNameFindings compares one model's Go field names with the properties they
// decode.
func fieldNameFindings(sdk *SDKPackage, model string) []string {
	fields, ok := sdk.ModelFields(model)
	if !ok {
		return nil // the model is named by the contract, not by this package
	}
	var items []string
	for _, property := range sortedStrings(fields) {
		field := fields[property]
		if SnakeCase(field) == property {
			continue
		}
		items = append(items, fmt.Sprintf("model `%s`: the field `%s` decodes `%s` -- a caller reading `%s.%s` would get the contract's `%s`, so rename the field or teach tools/contractdrift the exception",
			model, field, property, model, field, property))
	}
	return items
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

	// Each constant's NAME against the value it carries.
	//
	// Everything above compares SETS, and a set cannot see a swap. Exchange the
	// values of CodeNotFound and CodeForbidden and the registry still holds exactly
	// the same fourteen codes, every one still has a constant, every constant still
	// has a code -- and IsNotFound answers true for a forbidden while IsForbidden
	// answers true for a missing row. It is the same defect two crossed JSON tags
	// are, and it needs the same kind of check: one that reads the declaration.
	//
	// The rule is that a constant is named for its value, which this SDK already
	// obeys everywhere but two places, both recorded with their reason.
	//
	// KNOWN LIMIT, and a deliberate one: the comparison runs Go name -> wire code,
	// not the reverse, so it cannot see a rename that spells the same code
	// differently -- CodeNamespaceAPIDisabled to CodeNamespaceApiDisabled is a
	// breaking change to every caller and clean here. Going the other way means
	// deriving `NamespaceAPIDisabled` from `namespace_api_disabled`, which needs a
	// table of initialisms; a code whose initialism is not in that table reports a
	// correctly named constant as wrong, and this gate was told not to become a
	// nag. An exported rename is a release-gate question, not a contract-drift
	// one, and no part of this tool claims to answer it.
	abbreviated := map[string]string{}
	for _, row := range in.Coverage.AbbreviatedErrorConstants {
		abbreviated[row.Constant] = row.Code
	}
	named := map[string]bool{}
	carriers := map[string][]string{}
	for constant, code := range in.SDK.ErrorConstants() {
		named[constant] = true
		carriers[code] = append(carriers[code], constant)
		if want, recorded := abbreviated[constant]; recorded {
			if want != code {
				items = append(items, fmt.Sprintf("`%s` is recorded as abbreviating `%s` and now carries `%s`", constant, want, code))
			}
			continue
		}
		if spelled := SnakeCase(strings.TrimPrefix(constant, "Code")); spelled != code {
			items = append(items, fmt.Sprintf("`%s` carries `%s`, and its name spells `%s` -- a constant named for one code and carrying another is what a swap looks like; rename it, or record the abbreviation in `abbreviated_error_constants`",
				constant, code, spelled))
		}
	}
	// Two constants carrying one value. The set comparisons cannot see it -- the
	// registry still has every code, and every code still has a constant -- and one
	// of the two is dead weight a caller may well be switching on.
	for code, constants := range carriers {
		if len(constants) > 1 {
			sort.Strings(constants)
			for i, name := range constants {
				constants[i] = "`" + name + "`"
			}
			items = append(items, fmt.Sprintf("`%s` is carried by %s -- two constants for one code, and only one of them can be the one callers mean",
				code, strings.Join(constants, " and ")))
		}
	}
	for constant := range abbreviated {
		if !named[constant] {
			items = append(items, fmt.Sprintf("`%s` is recorded in abbreviated_error_constants and errors.go declares no such constant -- drop the row", constant))
		}
	}
	r.Add("Error codes", items)
}

// quoted wraps each name in backticks for a report line.
func quoted(names []string) []string {
	out := make([]string, len(names))
	for i, name := range names {
		out[i] = "`" + name + "`"
	}
	return out
}

// checkErrorEnvelope proves the client can still read an error.
//
// `ErrorResponse` is the most referenced schema in either contract -- every
// operation documents it on every failure -- and it was the one schema nothing
// here compared, because the response stub only ever answered 200 or 204. The
// hole was demonstrated: renaming `error.code` to `error_code` in BOTH vendored
// contracts produced "No drift". What it produces in the SDK is that parseError
// finds no code, falls through to its envelope-less branch, and stamps
// CodeUnexpectedStatus on every error the server sends -- so IsNotFound,
// IsConflict and IsValidation each answer false for the error they are named
// after, and Details and RequestID come back empty.
//
// The one drive behind this answers 409, a status whose meaning the SDK learns
// only from the envelope, with a body the stub builds from the contract exactly
// as it builds every success body. So the comparison is the same comparison the
// response checks make, pointed at the schema they cannot reach.
func checkErrorEnvelope(in Inputs, r *Report) {
	var items []string
	for _, surface := range surfaces {
		observation, ok := in.Conformance.ErrorEnvelope[surface]
		if !ok {
			items = append(items, fmt.Sprintf("`%s`: no error envelope was driven, so nothing exercised `ErrorResponse` on this surface", surface))
			continue
		}
		if observation.NotAPIErr != nil {
			items = append(items, fmt.Sprintf("`%s`: a 409 carrying the contract's own `ErrorResponse` came back as %v, which is not an `*APIError` -- callers cannot reach a code, a status or a request id through it",
				surface, observation.NotAPIErr))
			continue
		}
		// The surface, before anything else: a v1 drive that silently went to
		// /api/v2 proves nothing about v1, and says so in v1's voice.
		if want := "/api/" + surface; observation.Prefix != want {
			items = append(items, fmt.Sprintf("`%s`: the error drive went to %q, not %q -- this surface is not the one being exercised",
				surface, observation.Prefix, want))
		}
		if observation.Status != http.StatusConflict {
			items = append(items, fmt.Sprintf("`%s`: the error drive answers 409 and the returned `*APIError` reports status %d -- a caller reading `StatusCode` is reading something the server did not send",
				surface, observation.Status))
		}
		// The semantic helpers are the whole reason a code matters. A caller writes
		// IsNotFound, not `err.(*APIError).Code == "not_found"`, so each helper is
		// driven with its OWN code and must answer for itself and nothing else.
		// Asserting one of them -- IsConflict, because it was the code this drive
		// already sent -- left the other fifteen unexercised, and rewiring
		// IsNotFound to CodeForbidden was clean.
		for _, helper := range semanticHelpers {
			answered := observation.Helpers[helper.Name]
			if len(answered) == 1 && answered[0] == helper.Name {
				continue
			}
			switch {
			case len(answered) == 0:
				items = append(items, fmt.Sprintf("`%s`: an envelope carrying `%s` makes `%s` answer false -- the helper and the code it is named after have come apart",
					surface, helper.Code, helper.Name))
			default:
				items = append(items, fmt.Sprintf("`%s`: an envelope carrying `%s` makes %s answer true, and only `%s` should",
					surface, helper.Code, strings.Join(quoted(answered), " and "), helper.Name))
			}
		}

		if len(observation.Sent) == 0 {
			items = append(items, fmt.Sprintf("`%s`: `ErrorResponse` no longer describes an `error` object with properties, and that object is the whole of what the SDK decodes an error from", surface))
			continue
		}

		// The four properties parseError reads, against the four the contract
		// documents. A property on one side and not the other is the finding.
		slots := map[string]string{
			"code":       observation.Code,
			"message":    observation.Message,
			"request_id": observation.RequestID,
		}
		for _, property := range []string{"code", "message", "request_id"} {
			raw, documented := observation.Sent[property]
			if !documented {
				items = append(items, fmt.Sprintf("`%s`: `ErrorResponse.error.%s` is gone from the contract, and the SDK decodes that property -- the one it reads and the one the server sends are no longer the same name",
					surface, property))
				continue
			}
			var want string
			if err := json.Unmarshal(raw, &want); err != nil {
				items = append(items, fmt.Sprintf("`%s`: `ErrorResponse.error.%s` is no longer a string, and the SDK decodes it into one", surface, property))
				continue
			}
			if got := slots[property]; got != want {
				items = append(items, fmt.Sprintf("`%s`: the envelope carried %q in `error.%s` and the returned `*APIError` reports %q",
					surface, want, property, got))
			}
		}

		if raw, documented := observation.Sent["details"]; !documented {
			items = append(items, fmt.Sprintf("`%s`: `ErrorResponse.error.details` is gone from the contract, and `APIError.Details` is the only place a caller reads a field-level validation error from", surface))
		} else {
			got, err := json.Marshal(observation.Details)
			if err != nil || !sameJSON(raw, got) {
				items = append(items, fmt.Sprintf("`%s`: the envelope carried %s in `error.details` and the returned `*APIError` reports %s",
					surface, raw, got))
			}
		}

		// A property the envelope grew. parseError reads a fixed four, so anything
		// else the server starts sending is dropped in silence -- which is worth a
		// line, because the envelope is where an error explains itself.
		for property := range observation.Sent {
			switch property {
			case "code", "message", "request_id", "details":
				continue
			}
			items = append(items, fmt.Sprintf("`%s`: `ErrorResponse.error.%s` is new in the contract and the SDK's `*APIError` has nowhere to put it", surface, property))
		}
	}
	r.Add("The error envelope", items)
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

// isJSONContainer reports whether a marshalled value is an object or an array --
// which is to say, whether the Go field behind it is a map or a slice, whose nil
// really does mean absent.
func isJSONContainer(raw json.RawMessage) bool {
	for _, b := range raw {
		switch b {
		case ' ', '\t', '\n', '\r':
			continue
		case '{', '[':
			return true
		default:
			return false
		}
	}
	return false
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

// checkEmittedValues holds EVERY value the client emitted to the value the driver
// supplied for it, on both executions, whether or not the contract documents it.
//
// It is separate from the documented-input checks above because those walk the
// CONTRACT and stop wherever an exception is recorded -- and two of those
// exceptions were bypassing value validation entirely. A query parameter listed
// under `undocumented_inputs` was accepted on sight, so v1 could send
// `application_id=wrong-application` and be waved through; an operation with an
// `undocumented_request_body` took an early continue, so the body-carrying DELETE
// could send its tag id under `resource_type` with nothing said.
//
// A recorded divergence means the CONTRACT has nothing to compare against. It
// never meant the DRIVER had nothing to compare against.
func checkEmittedValues(in Inputs, r *Report) {
	var items []string
	for _, surface := range surfaces {
		for _, row := range in.Coverage.Operations {
			if row.Unimplemented != "" {
				continue
			}
			observed, ok := in.Conformance.Observations[surface+" "+row.Key()]
			if !ok {
				continue // reported by checkImplementation
			}
			for pass, execution := range []*Observation{&observed, observed.Second} {
				if execution == nil {
					continue
				}
				for _, name := range sortedStrings(execution.Query) {
					items = append(items, exactValueFindings(row.Key(), "query parameter", name, execution.Query[name], pass)...)
				}
				for _, name := range sortedStrings(execution.Headers) {
					items = append(items, exactValueFindings(row.Key(), "header", name, execution.Headers[name], pass)...)
				}
				for _, name := range sortedFields(execution.Body) {
					items = append(items, exactJSONFindings(row.Key(), "body property", name, execution.Body[name], pass)...)
				}
			}
		}
	}
	r.Add("Emitted values", dedupe(items))
}

// exactValueFindings requires one emitted string to be exactly what the driver
// supplied for that field on that execution.
func exactValueFindings(op, kind, name, value string, pass int) []string {
	want := ExpectedValue(name, pass)
	if value == want {
		return nil
	}
	if origin, ok := sentinelOrigin(value); ok && !strings.EqualFold(origin, name) {
		return []string{fmt.Sprintf("`%s`: the %s `%s` carries the value the driver supplied for `%s` -- the client is wiring one input to another's name",
			op, kind, name, origin)}
	}
	return []string{fmt.Sprintf("`%s`: on execution %d the driver sent %q for the %s `%s` and the client put %q on the wire",
		op, pass+1, want, kind, name, value)}
}

// exactJSONFindings is the same for a value that arrives as JSON. Booleans and
// numbers used to reach only a TYPE check here, which is how a request-body
// boolean hard-coded to one execution's value passed on both.
func exactJSONFindings(op, kind, name string, raw json.RawMessage, pass int) []string {
	want := ExpectedValue(name, pass)

	var elements []json.RawMessage
	if err := json.Unmarshal(raw, &elements); err == nil {
		if len(elements) != 1 {
			return []string{fmt.Sprintf("`%s`: the driver sent one element for the %s `%s` and the client put %d on the wire",
				op, kind, name, len(elements))}
		}
		return exactJSONFindings(op, kind, name, elements[0], pass)
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return exactValueFindings(op, kind, name, text, pass)
	}

	var object map[string]any
	if err := json.Unmarshal(raw, &object); err == nil {
		expected, _ := json.Marshal(map[string]any{want: "value"})
		if !sameJSON(expected, raw) {
			return []string{fmt.Sprintf("`%s`: on execution %d the driver sent %s for the %s `%s` and the client put %s on the wire",
				op, pass+1, string(expected), kind, name, string(raw))}
		}
		return nil
	}

	// A boolean or a number. The driver's value is held in the table as text, and
	// `true` / `1` are already valid JSON, so it compares directly.
	if !sameJSON(json.RawMessage(want), raw) {
		return []string{fmt.Sprintf("`%s`: on execution %d the driver sent %s for the %s `%s` and the client put %s on the wire",
			op, pass+1, want, kind, name, string(raw))}
	}
	return nil
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
	unsent := in.Coverage.UnsentIndex()
	clientHeaders := in.Coverage.ClientHeaderSet()
	used := map[string]bool{}
	seenClientHeader := map[string]bool{}
	var items []string

	// Both surfaces, for the same reason the query check runs on both: a request
	// shape is per surface, and only one of the two was ever exercised.
	for _, surface := range surfaces {
		spec := in.Vendored[surface]
		ops, err := strippedOperations(spec, surface)
		if err != nil {
			continue // reported by checkSurfaceParity
		}
		for _, row := range in.Coverage.Operations {
			if row.Unimplemented != "" {
				continue
			}
			op, ok := ops[row.Key()]
			if !ok {
				continue
			}
			observed, ok := in.Conformance.Observations[surface+" "+row.Key()]
			if !ok {
				continue // reported by checkImplementation
			}

			// --- headers ---
			documentedHeaders := map[string]bool{}
			for _, name := range op.HeaderParams() {
				canonical := http.CanonicalHeaderKey(name)
				documentedHeaders[canonical] = true
				value, sent := observed.Headers[canonical]
				if !sent {
					key := row.Key() + " header " + name
					if _, ok := unsent[key]; ok {
						used[key] = true
						continue
					}
					items = append(items, fmt.Sprintf("`%s` documents the header `%s` and the client did not send it", row.Key(), name))
					continue
				}
				items = append(items, valueFindings(row.Key(), "header", canonical, value, 0, op.ParamSchema("header", name))...)
				if second := observed.Second; second != nil {
					if v, ok := second.Headers[canonical]; ok {
						items = append(items, valueFindings(row.Key(), "header", canonical, v, 1, op.ParamSchema("header", name))...)
					}
				}
			}

			// client_headers means EVERY versioned request, with the value the client was
			// configured with -- an arbitrary token under Authorization used to pass,
			// because only presence was checked. And it is checked per operation
			// -- once per operation, not once per run. Requiring each name to have been
			// seen somewhere let a client drop X-Tenant-ID from every request carrying
			// a body while the reads kept it, which is every write losing its tenant
			// scope, reported as nothing at all.
			//
			// And forbidden on the unversioned probes, which is the other half of the
			// same rule: /health authenticates nobody, so an Authorization or
			// X-Tenant-ID leaking onto it is a finding rather than an absence.
			versioned := !strings.HasPrefix(row.Path, "/health/")
			for name := range clientHeaders {
				switch value, sent := observed.Headers[name]; {
				case sent && versioned:
					seenClientHeader[name] = true
					if want := ExpectedValue(name, 0); value != want {
						items = append(items, fmt.Sprintf("`%s`: the client was configured with %q and sent `%s: %s`",
							row.Key(), want, name, value))
					}
					if second := observed.Second; second != nil {
						if v, ok := second.Headers[name]; ok && v != ExpectedValue(name, 1) {
							items = append(items, fmt.Sprintf("`%s`: on its second execution the client was configured with %q and sent `%s: %s`",
								row.Key(), ExpectedValue(name, 1), name, v))
						}
					}
				case !sent && versioned:
					items = append(items, fmt.Sprintf("`%s` does not carry `%s`, which client_headers records as sent on every versioned request", row.Key(), name))
				case sent && !versioned:
					seenClientHeader[name] = true
					items = append(items, fmt.Sprintf("`%s` carries `%s`, and the unversioned probes authenticate nobody -- it must not be sent there", row.Key(), name))
				}
			}
			for _, name := range sortedStrings(observed.Headers) {
				if clientHeaders[name] || documentedHeaders[name] {
					continue
				}
				items = append(items, fmt.Sprintf("`%s`: the client sends the header `%s`, which no vendored contract documents", row.Key(), name))
			}

			// --- request body ---
			switch {
			case op.RequestModel == "" && len(observed.Body) > 0:
				// The one live case is DELETE /tag-assignments, whose ids travel in a
				// body the generated spec does not describe -- a fourth divergence in
				// the same family as the envelopes, and recorded the same way.
				if row.UndocumentedRequestBody == "" {
					items = append(items, fmt.Sprintf("`%s`: the client sends a request body (%s) and the contract documents none -- record it under undocumented_request_body",
						row.Key(), strings.Join(sortedFields(observed.Body), ", ")))
				}
				continue
			case op.RequestModel == "" && row.UndocumentedRequestBody != "":
				items = append(items, fmt.Sprintf("`%s` records an undocumented request body and the client sends no body at all -- drop the row", row.Key()))
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
				raw, sent := observed.Body[property]
				if !sent {
					key := row.Key() + " body " + property
					if _, ok := unsent[key]; ok {
						used[key] = true
						continue
					}
					items = append(items, fmt.Sprintf("`%s`: schema `%s` documents `%s` and the client did not send it -- implement it, or populate it in drivers.go",
						row.Key(), op.RequestModel, property))
					continue
				}
				items = append(items, jsonValueFindings(row.Key(), "body property", property, raw, 0, spec.PropertySchema(op.RequestModel, property))...)
				if second := observed.Second; second != nil {
					if v, ok := second.Body[property]; ok {
						items = append(items, jsonValueFindings(row.Key(), "body property", property, v, 1, spec.PropertySchema(op.RequestModel, property))...)
					}
				}
			}
			for _, property := range sortedFields(observed.Body) {
				if !documentedSet[property] {
					items = append(items, fmt.Sprintf("`%s`: the client sends `%s` in its request body, which schema `%s` does not document",
						row.Key(), property, op.RequestModel))
				}
			}
		}
	}
	// A row claiming an input travels somewhere else has to be TRUE: the name must
	// actually be there. Eleven of these say application_id moves from the query
	// string into the payload, and without this they would turn a missing query key
	// green while proving nothing about the replacement path.
	for _, row := range in.Coverage.UnsentInputs {
		if row.CarriedIn == "" {
			continue
		}
		observed, ok := in.Conformance.Observations["v2 "+row.Method+" "+row.Path]
		if !ok {
			continue
		}
		carried := map[string]bool{}
		for name := range observed.Query {
			carried["query "+name] = true
		}
		for name := range observed.Headers {
			carried["header "+name] = true
		}
		for name := range observed.Body {
			carried["body "+name] = true
		}
		if !carried[row.CarriedIn+" "+row.Name] {
			items = append(items, fmt.Sprintf("`%s %s` records that `%s` travels in the %s instead, and the request's %s does not carry it",
				row.Method, row.Path, row.Name, row.CarriedIn, row.CarriedIn))
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
	// client_headers gets the same staleness check every other allowlist here has.
	// A header recorded as always-sent that the client no longer sends is a row
	// suppressing nothing, and the next header to go missing would be suppressed
	// by it just as quietly.
	for name := range clientHeaders {
		if !seenClientHeader[name] {
			items = append(items, fmt.Sprintf("`%s` is listed under client_headers and the client sends it on no operation -- drop the row", name))
		}
	}
	r.Add("Request shapes", dedupe(items))
}

// --- request values -------------------------------------------------------------
//
// Names were not enough. A review sent four requests past this gate with every
// documented name in place and the wrong thing under it: a parameter the contract
// had retyped, a params struct wiring `q` to the Slug field, two JSON tags swapped
// on a write model, and the two namespace headers crossed.
//
// What makes a value checkable without a schema validator is that the drivers send
// SELF-IDENTIFYING values: the string a driver supplies for the field that should
// arrive as `slug` is the sentinel for `slug` (driverValue in drivers.go). So a
// value that turns up under another name announces where it came from, and the two
// halves below are all the checking needed -- the sentinel says the wiring is
// right, and the type says the contract still describes what is being sent.

// valueFindings checks one emitted string value: query parameter or header.
//
// EXACT EQUALITY against the value the driver supplied for that field. The rule
// used to be weaker -- report only a value that still looked like a sentinel for
// some other field -- and a review walked through it six ways, including a
// hard-coded string, two swapped integers, two swapped booleans and a reused path
// argument. A field carries what the driver gave it or it does not.
//
// The type check still runs on top, because a value can be exactly what the driver
// sent and the CONTRACT can be the thing that moved: a parameter retyped upstream
// leaves the client sending a perfectly correct string for a documented integer.
func valueFindings(op, kind, name, value string, pass int, schema map[string]string) []string {
	var items []string
	if want := ExpectedValue(name, pass); value != want {
		// Where the value belongs to another field, say so: that names the defect
		// rather than merely reporting a mismatch.
		if origin, ok := sentinelOrigin(value); ok && !strings.EqualFold(origin, name) {
			items = append(items, fmt.Sprintf("`%s`: the %s `%s` carries the value the driver supplied for `%s` -- the client is wiring one input to another's name",
				op, kind, name, origin))
		} else {
			items = append(items, fmt.Sprintf("`%s`: on execution %d the driver sent %q for the %s `%s` and the client put %q on the wire",
				op, pass+1, want, kind, name, value))
		}
		return items
	}
	if documented := schema["type"]; documented != "" && !valueMatchesType(value, documented) {
		items = append(items, fmt.Sprintf("`%s`: the %s `%s` is documented as `%s` and the client sends %q",
			op, kind, name, documented, value))
	}
	return items
}

// jsonValueFindings is the same for a request-body property, whose value arrives
// as JSON rather than as a string.
func jsonValueFindings(op, kind, name string, raw json.RawMessage, pass int, schema map[string]string) []string {
	// An array is compared WHOLE. Recursing over the elements that remain let an
	// emptied array through, since there was then nothing left to disagree with.
	var elements []json.RawMessage
	if err := json.Unmarshal(raw, &elements); err == nil {
		if len(elements) != 1 {
			return []string{fmt.Sprintf("`%s`: the driver sent one element for the %s `%s` and the client put %d on the wire",
				op, kind, name, len(elements))}
		}
		return jsonValueFindings(op, kind, name, elements[0], pass, itemSchema(schema))
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return valueFindings(op, kind, name, text, pass, schema)
	}
	// A free-form object -- `metadata`, which the contract constrains in no way.
	// Compared exactly all the same: the gate controls both sides, so a driver
	// whose object stopped arriving intact was otherwise invisible.
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err == nil {
		want, _ := json.Marshal(map[string]any{ExpectedValue(name, pass): "value"})
		if !sameJSON(want, raw) {
			return []string{fmt.Sprintf("`%s`: the driver sent %s for the %s `%s` and the client put %s on the wire",
				op, string(want), kind, name, string(raw))}
		}
		return nil
	}
	if documented := schema["type"]; documented != "" && !jsonMatchesType(raw, documented) {
		return []string{fmt.Sprintf("`%s`: the %s `%s` is documented as `%s` and the client sends %s",
			op, kind, name, documented, string(raw))}
	}
	return nil
}

// itemSchema reduces an array's flattened schema to its element's.
func itemSchema(schema map[string]string) map[string]string {
	out := map[string]string{}
	for key, value := range schema {
		if rest, ok := strings.CutPrefix(key, "items."); ok {
			out[rest] = value
		}
	}
	return out
}

func valueMatchesType(value, documented string) bool {
	switch documented {
	case "string":
		return true
	case "integer":
		_, err := strconv.Atoi(value)
		return err == nil
	case "number":
		_, err := strconv.ParseFloat(value, 64)
		return err == nil
	case "boolean":
		_, err := strconv.ParseBool(value)
		return err == nil
	}
	// A type this does not model is not a finding: saying nothing is honest, and
	// claiming a mismatch would be worse than silence.
	return true
}

func jsonMatchesType(raw json.RawMessage, documented string) bool {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return true
	}
	switch documented {
	case "string":
		_, ok := value.(string)
		return ok
	case "integer", "number":
		_, ok := value.(float64)
		return ok || value == nil
	case "boolean":
		_, ok := value.(bool)
		return ok || value == nil
	case "object":
		_, ok := value.(map[string]any)
		return ok || value == nil
	case "array":
		_, ok := value.([]any)
		return ok || value == nil
	}
	return true
}

// containsJSON reports whether everything SENT survived in what came back.
//
// The forward direction, and it has to be containment rather than equality once
// both surfaces are driven: v1's schemas omit the namespace_type / namespace_id
// that every model carries, so a nested object decoded from a v1 body comes back
// with two keys the body never had. Those are the reverse direction's business,
// and the reverse direction is v2-authoritative.
func containsJSON(sent, decoded json.RawMessage) bool {
	var want, got any
	if err := json.Unmarshal(sent, &want); err != nil {
		return false
	}
	if err := json.Unmarshal(decoded, &got); err != nil {
		return false
	}
	return containsValue(want, got)
}

func containsValue(want, got any) bool {
	switch want := want.(type) {
	case map[string]any:
		gotMap, ok := got.(map[string]any)
		if !ok {
			return false
		}
		for key, value := range want {
			other, present := gotMap[key]
			if !present {
				// A null that came back as an absent key is a field carrying
				// `omitempty` whose zero value is nil -- a map, a slice, a pointer.
				// Whether that is acceptable depends on what the zero value IS, and
				// the null witness in checkResponseModels is what judges it, using
				// the populated witness to tell a container from a scalar. Here it
				// is simply not a missing value.
				if value == nil {
					continue
				}
				return false
			}
			if !containsValue(value, other) {
				return false
			}
		}
		return true
	case []any:
		gotSlice, ok := got.([]any)
		if !ok || len(gotSlice) != len(want) {
			return false
		}
		for i := range want {
			if !containsValue(want[i], gotSlice[i]) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(want, got)
	}
}

// sameJSON compares two encodings of the same value, ignoring formatting.
func sameJSON(a, b json.RawMessage) bool {
	var left, right any
	if err := json.Unmarshal(a, &left); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &right); err != nil {
		return false
	}
	return reflect.DeepEqual(left, right)
}

// checkDecodedValues holds every decoded response to what the stub sent, on BOTH
// surfaces and BOTH executions, for every shape including the ones the schema
// comparison steps around.
//
// checkResponseModels walks the CONTRACT's properties, which means it stops
// wherever the contract stops: it reads execution 1 only, it is v2-authoritative
// in reverse, it skips the composites because their bodies are recorded rather
// than documented, and it skips the health probes because they are outside the API
// surface entirely. Each of those was a place a decoded value could be wrong with
// nothing said -- a review cleared `Tag.Name` on the second execution alone,
// cleared a composite counter on the v1 client alone, and replaced the health word
// outright, and all three came back clean.
//
// This walks the STUB's side instead. Whatever it sent has to come back, whatever
// the operation's shape and whichever execution it was.
func checkDecodedValues(in Inputs, r *Report) {
	var items []string
	for _, surface := range surfaces {
		ops, err := strippedOperations(in.Vendored[surface], surface)
		if err != nil {
			continue // reported by checkSurfaceParity
		}
		for _, row := range in.Coverage.Operations {
			if row.Unimplemented != "" || row.ActualResponse == "none" {
				continue
			}
			observed, ok := in.Conformance.Observations[surface+" "+row.Key()]
			if !ok {
				continue // reported by checkImplementation
			}
			for pass, execution := range []*Observation{&observed, observed.Second} {
				if execution == nil || execution.CallErr != nil {
					continue // reported by checkImplementation
				}
				// The null witness runs only where the operation has an OKModel, so
				// deferring to it for a composite defers to nothing -- a `null` in a
				// composite_body decoded away was covered by neither check. Not live
				// as the file stands (no composite_body carries a null) but the
				// subsumption claim that retired checkCompositeResponses has to be
				// true for the shapes that file could take, not only the ones it does.
				op, documented := ops[row.Key()]
				nullWitnessSpeaks := documented && op.OKModel != ""
				for _, property := range sortedFields(execution.Sent) {
					decoded, survived := execution.Decoded[property]
					// A null the model omits is the null witness's business, and it
					// has the populated witness on hand to tell a container's nil
					// from a scalar's zero. Saying anything here would be saying it
					// with less information.
					if !survived && string(execution.Sent[property]) == "null" && nullWitnessSpeaks {
						continue
					}
					switch {
					case !survived:
						items = append(items, fmt.Sprintf("`%s` (%s, execution %d): the response carried `%s` and `%s` decoded it away",
							row.Key(), surface, pass+1, property, row.SDK))
					case !containsJSON(execution.Sent[property], decoded):
						items = append(items, fmt.Sprintf("`%s` (%s, execution %d): the response sent `%s` as %s and `%s` returned %s",
							row.Key(), surface, pass+1, property, string(execution.Sent[property]), row.SDK, string(decoded)))
					}
				}
			}
		}
	}
	r.Add("Decoded values", dedupe(items))
}

// checkModelFieldNames asserts each response model's Go field name matches the
// JSON property it decodes.
//
// This is the one defect a round trip structurally cannot see. Swap the tags on
// two fields and the SAME tags do the decoding and the re-encoding: identical
// bytes go out, every documented property survives, every value is intact -- and
// the caller reads the server's `name` out of `Tag.Slug`. No amount of comparing
// what came back can distinguish that, because nothing about it is different.
// Only the declaration knows the field called Name is meant to carry `name`.
//
// The rule is the SDK's own spelling convention, and it holds today with no
// exceptions across every model the gate compares: TagID decodes `tag_id`,
// UsageCount decodes `usage_count`, ID decodes `id`.
func checkModelFieldNames(in Inputs, r *Report) {
	const surface = "v2"
	spec := in.Vendored[surface]
	ops, err := strippedOperations(spec, surface)
	if err != nil {
		return // reported by checkSurfaceParity
	}

	// Pagination rides in every list envelope and is a response model like any
	// other -- and, like any other, a pure tag swap on it is invisible to a round
	// trip, which is the whole reason this check exists. It is not reachable from
	// any operation's OKModel, so it is named here.
	// The models no operation's success schema names, and which are therefore
	// unreachable from the loop below: the list envelope's pagination block, the
	// three composite results whose bodies the contract describes wrongly or not
	// at all, and the health payload, which sits outside the API surface. A pure
	// tag swap is invisible to a round trip on every one of them, which is exactly
	// what this check is for -- and a review proved it by crossing two counters on
	// BulkAssignResult and its wire struct together, which re-marshalled to the
	// original bytes.
	checked := map[string]bool{}
	var items []string
	for _, model := range unreachableModels {
		// A name that no longer resolves switches this check off for that model in
		// silence -- and these five are hard-coded precisely because nothing else
		// reaches them. Renaming the type was enough to retire the only check that
		// can see a swapped pair of tags, with nothing said.
		if _, ok := in.SDK.ModelFields(model); !ok {
			items = append(items, fmt.Sprintf("`%s` is named here as a model no success schema reaches, and this package no longer declares it -- rename it here too, or drop it if it is gone", model))
			continue
		}
		items = append(items, fieldNameFindings(in.SDK, model)...)
		checked[model] = true
	}

	for _, row := range in.Coverage.Operations {
		if row.Unimplemented != "" {
			continue
		}
		op, ok := ops[row.Key()]
		if !ok || op.OKModel == "" || checked[op.OKModel] {
			continue
		}
		checked[op.OKModel] = true
		items = append(items, fieldNameFindings(in.SDK, op.OKModel)...)
	}
	r.Add("Model field names", items)
}
