package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"time"

	octonomy "github.com/octoverse-id/octonomy-go/v2"
	"gopkg.in/yaml.v3"
)

// What this SDK sends and decodes, established by CALLING it.
//
// The first version of this gate read the answer out of the source: it walked
// each method for a transport call, rendered the path expression, followed the
// params struct into its query builder, and read the type argument of doData[T].
// It was 700 lines, it grew a new special case for every shape it met, and a
// second review pass still reproduced three silent green answers against it --
// a method that stopped passing `params.query()`, a method that branched between
// two private helpers, a schema field whose TYPE changed. Every one of those is
// invisible to a reader that infers control flow, and every one is obvious to a
// client that actually issues the request.
//
// So the analysis is gone, and this runs the client instead:
//
//   - a recording stub server stands in for Octonomy, and the request the client
//     sends IS the answer -- method, path, query parameters, no inference;
//   - the response the stub sends back is SYNTHESIZED FROM THE VENDORED SCHEMA,
//     so decoding it is a direct test of whether the SDK's model still matches
//     the contract. A property the Go struct has no field for disappears on the
//     way back out; a property whose type changed fails to decode at all.
//
// The cost is a driver per operation (drivers.go) -- a call with every parameter
// populated. That is more typing than a table of expectations and much harder to
// get quietly wrong: a driver that does not compile is a build failure, and a
// driver that calls the wrong method reports the wrong route immediately.

// Observation is what one driver's call did on the wire, and what came back.
type Observation struct {
	// Requested is the normalized request: "get /tags", with every path segment
	// the driver passed in reduced to {}.
	Method string
	Path   string

	// Query is the set of query parameter names the client actually sent.
	Query map[string]bool

	// Body reports whether the request carried one.
	Body bool

	// CallErr is the error the method returned, if any. A decode failure against
	// a schema-derived response body lands here, and that is the point.
	CallErr error

	// Decoded is the re-marshalled response value, when the call returned one:
	// the set of JSON keys that survived a round trip through the SDK's model.
	// A documented property missing from here is a property the client drops.
	Decoded map[string]bool
}

// Key is the operation identity, matching a coverage row.
func (o Observation) Key() string { return o.Method + " " + o.Path }

// pathSentinels are the values every driver passes for a path segment. The stub
// reduces any segment equal to one of these to {}, so an observed path compares
// with a spec path whose placeholders are named.
var pathSentinels = map[string]bool{
	"SEG1": true,
	"SEG2": true,
}

// Conformance runs every driver against a recording stub and returns what each
// one did, keyed by the operation the driver names.
type Conformance struct {
	Observations map[string]Observation

	// Errors are drivers that could not be run at all -- a client that would not
	// construct, a stub that could not synthesize a body. Distinct from a call
	// error, which is itself an observation.
	Errors map[string]error
}

// RunConformance drives the SDK once per operation.
//
// The spec supplies the response bodies, so this is a two-way test: the request
// side proves what the client sends, and the response side proves that what the
// contract describes still fits the models the client decodes into.
func RunConformance(spec *Spec, cov *Coverage, drivers []Driver) (*Conformance, error) {
	rows := cov.ByKey()
	ops, err := strippedOperations(spec, "v2")
	if err != nil {
		return nil, err
	}

	conf := &Conformance{
		Observations: map[string]Observation{},
		Errors:       map[string]error{},
	}

	// One stub for the whole run. `current` names the operation being exercised,
	// so the handler knows which schema to answer with; drivers run one at a time.
	var (
		current  Driver
		observed Observation
	)
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observed = Observation{
			Method: strings.ToLower(r.Method),
			Path:   normalizeObservedPath(r.URL.Path),
			Query:  queryNames(r.URL.Query()),
			Body:   r.ContentLength != 0,
		}
		row, ok := rows[current.Op]
		if !ok {
			http.Error(w, "no coverage row", http.StatusInternalServerError)
			return
		}
		body, status, err := synthesizeResponse(spec, ops[current.Op], row)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if len(body) > 0 {
			_, _ = w.Write(body)
		}
	}))
	defer stub.Close()

	client, err := octonomy.New(octonomy.Config{
		BaseURL:  stub.URL,
		Token:    "contractdrift",
		TenantID: "contractdrift",
	})
	if err != nil {
		return nil, fmt.Errorf("constructing the client: %w", err)
	}
	health, err := octonomy.NewHealthClient(stub.URL)
	if err != nil {
		return nil, fmt.Errorf("constructing the health client: %w", err)
	}
	env := &Env{Client: client, Health: health}

	for _, driver := range drivers {
		current = driver
		observed = Observation{}

		value, callErr := driver.Call(context.Background(), env)
		if observed.Method == "" {
			conf.Errors[driver.Op] = fmt.Errorf("the call reached no request: %v", callErr)
			continue
		}
		observed.CallErr = callErr
		observed.Decoded = remarshalKeys(value)
		conf.Observations[driver.Op] = observed
	}
	return conf, nil
}

// Env is what a driver is handed. Two clients, because the health probes are
// outside the versioned API and are reached from a credential-free constructor.
type Env struct {
	Client *octonomy.Client
	Health *octonomy.HealthClient
}

// Driver calls one operation with every parameter it accepts populated.
type Driver struct {
	// Op is the version-independent operation key, matching a coverage row.
	Op string
	// SDK names the method for report lines.
	SDK string
	// Call issues the request and returns the decoded value, when there is one.
	Call func(ctx context.Context, env *Env) (any, error)
}

func normalizeObservedPath(path string) string {
	path = strings.TrimPrefix(path, "/api/v2")
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if pathSentinels[part] {
			parts[i] = "{}"
		}
	}
	return strings.Join(parts, "/")
}

// normalizePath collapses a documented path's named placeholders to {}, so
// /tags/{tag_id} compares with the request the client actually issued.
func normalizePath(path string) string {
	var b strings.Builder
	depth := 0
	for _, r := range path {
		switch {
		case r == '{':
			depth++
			if depth == 1 {
				b.WriteString("{}")
			}
		case r == '}':
			if depth > 0 {
				depth--
			}
		case depth == 0:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func queryNames(values url.Values) map[string]bool {
	out := map[string]bool{}
	for name := range values {
		out[name] = true
	}
	return out
}

// remarshalKeys renders a decoded response back to JSON and returns its keys --
// for a list, the keys of its first element. A documented property the Go model
// has no field for cannot appear here, which is the whole point.
func remarshalKeys(value any) map[string]bool {
	if value == nil {
		return nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil
	}
	// A *List[T] re-marshals as {"data": [...], "pagination": {...}}; the model
	// under test is the element type.
	if data, ok := object["data"]; ok {
		var elements []map[string]json.RawMessage
		if err := json.Unmarshal(data, &elements); err == nil {
			if len(elements) == 0 {
				return map[string]bool{}
			}
			object = elements[0]
		}
	}
	out := make(map[string]bool, len(object))
	for key := range object {
		out[key] = true
	}
	return out
}

// synthesizeResponse builds the body the stub answers with: the operation's
// documented schema, filled with type-appropriate values, wrapped in the envelope
// the coverage row records the real server using.
func synthesizeResponse(spec *Spec, op *Operation, row CoverageOperation) ([]byte, int, error) {
	switch row.ActualResponse {
	case "none":
		return nil, http.StatusNoContent, nil
	case "bare":
		return []byte(`{"status":"ok"}`), http.StatusOK, nil
	}

	// The composites -- bulk assign, bulk remove, the resource-tag replace -- are
	// the recorded divergence: the contract claims a bare array for two of them
	// and documents no body at all for the third, while the server returns a
	// result object the contract never describes. There is no schema to
	// synthesize from, so the stub answers with the shape the SDK documents and
	// the request side of the observation still counts.
	if row.ActualResponse == "composite-envelope" {
		return []byte(`{"data":{"created":1,"existing":0,"skipped":0,"removed":1,"assignments":[],"tags":[]}}`), http.StatusOK, nil
	}

	if op == nil || op.OKModel == "" {
		return []byte(`{"data":{}}`), http.StatusOK, nil
	}
	object, err := synthesizeSchema(spec, op.OKModel, 0)
	if err != nil {
		return nil, 0, err
	}
	payload := map[string]any{"data": object}
	if row.ActualResponse == "list-envelope" {
		payload = map[string]any{
			"data":       []any{object},
			"pagination": map[string]any{"limit": 1, "offset": 0, "count": 1, "next": nil, "previous": nil},
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, err
	}
	return body, http.StatusOK, nil
}

// synthesizeSchema builds a value for a component schema: every documented
// property, with a value matching its documented type.
//
// Type fidelity is the point. A property the contract retypes from integer to
// string produces a body the SDK's model cannot decode, and the call returns an
// error -- which is a finding, reported against the operation. Nothing here
// needs to know what the Go type is.
func synthesizeSchema(spec *Spec, name string, depth int) (map[string]any, error) {
	if depth > 4 {
		return nil, fmt.Errorf("schema %s nests deeper than this stub will follow", name)
	}
	node, ok := spec.Schemas[name]
	if !ok {
		return nil, fmt.Errorf("components.schemas has no %q", name)
	}
	props := mappingValue(node, "properties")
	if props == nil {
		return map[string]any{}, nil
	}

	out := map[string]any{}
	for i := 0; i+1 < len(props.Content); i += 2 {
		key := props.Content[i].Value
		value, err := synthesizeValue(spec, props.Content[i+1], depth)
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", name, key, err)
		}
		out[key] = value
	}
	return out, nil
}

func synthesizeValue(spec *Spec, node *yaml.Node, depth int) (any, error) {
	// Value yaml.Node fields, never pointers: yaml.v3 leaves a *yaml.Node field
	// nil rather than filling it, so `items` and `allOf` decoded as absent and
	// every array and every nullable reference fell through to the unconstrained
	// branch below. Caught because TagResolution.matched_alias came back as a
	// free-form object and the SDK refused it -- which is the gate reporting its
	// own bug as a finding, and the reason the stub's errors are reported rather
	// than swallowed.
	var schema struct {
		Type   string      `yaml:"type"`
		Format string      `yaml:"format"`
		Ref    string      `yaml:"$ref"`
		Items  yaml.Node   `yaml:"items"`
		Enum   []string    `yaml:"enum"`
		AllOf  []yaml.Node `yaml:"allOf"`
	}
	if err := node.Decode(&schema); err != nil {
		return nil, err
	}
	if schema.Ref != "" {
		return synthesizeSchema(spec, schemaName(schema.Ref), depth+1)
	}
	// `allOf: [$ref]` plus `nullable` is how drf-spectacular writes a nullable
	// reference -- TagResolution.matched_alias. One member is a wrapper around
	// that member; more than one is a composition this stub does not model, and
	// guessing at it would surface as a decode error blamed on the SDK.
	if len(schema.AllOf) == 1 {
		return synthesizeValue(spec, &schema.AllOf[0], depth+1)
	}
	if len(schema.AllOf) > 1 {
		return nil, fmt.Errorf("an allOf of %d members -- the response stub does not compose schemas", len(schema.AllOf))
	}
	if len(schema.Enum) > 0 {
		return schema.Enum[0], nil
	}

	switch schema.Type {
	case "string":
		switch schema.Format {
		case "date-time":
			return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).Format(time.RFC3339), nil
		case "uuid":
			return "00000000-0000-4000-8000-000000000000", nil
		case "date":
			return "2026-01-02", nil
		}
		return "contractdrift", nil
	case "integer":
		return 1, nil
	case "number":
		return 1.5, nil
	case "boolean":
		return true, nil
	case "object":
		// An inline object with no properties of its own is a free-form map --
		// `metadata` and the error envelope's `details`. A non-empty value
		// matters: a field carrying `omitempty` would vanish on the way back out
		// and read as a property the model dropped.
		if inline := mappingValue(node, "properties"); inline != nil {
			out := map[string]any{}
			for i := 0; i+1 < len(inline.Content); i += 2 {
				value, err := synthesizeValue(spec, inline.Content[i+1], depth+1)
				if err != nil {
					return nil, err
				}
				out[inline.Content[i].Value] = value
			}
			return out, nil
		}
		return map[string]any{"contractdrift": "value"}, nil
	case "array":
		if schema.Items.Kind == 0 {
			return []any{}, nil
		}
		item, err := synthesizeValue(spec, &schema.Items, depth+1)
		if err != nil {
			return nil, err
		}
		return []any{item}, nil
	case "":
		// An UNCONSTRAINED property: `metadata: {}` and `changes: {readOnly: true}`
		// say nothing about the value at all. The contract permits anything there,
		// so nothing about it can be verified -- the stub sends the shape the
		// server really sends for these, a JSON object, which is enough to keep
		// the property present through the round trip and prove the model has a
		// field for it. If an unconstrained property ever needs to be something
		// other than an object, this is the line to change, and the decode error
		// will say so.
		return map[string]any{"contractdrift": "value"}, nil
	}
	return nil, fmt.Errorf("unsupported type %q", schema.Type)
}

// mappingValue returns the value node for a key in a mapping.
func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil {
		return nil
	}
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		node = node.Content[0]
	}
	if node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

// sortedNames renders a name set for a report line.
func sortedNames(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
