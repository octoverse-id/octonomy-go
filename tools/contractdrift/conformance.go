package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
//     way back out, and one whose type the model cannot read fails to decode.
//
// The response half is representative-value coverage, and worth stating as such:
// two witnesses per operation, one with every property populated and one with
// every nullable property null. That catches a field that is missing, a type the
// model cannot read at all, and a nullable state it cannot hold. It does not
// enumerate a schema's value space -- an `integer` decoded into a float passes, as
// does anything into `any` -- and where the contract constrains nothing there is
// nothing to check.
//
// The cost is a driver per operation (drivers.go) -- a call with every parameter
// populated. That is more typing than a table of expectations and much harder to
// get quietly wrong: a driver that does not compile is a build failure, and a
// driver that calls the wrong method reports the wrong route immediately.

// Observation is what one driver's call did on the wire, and what came back.
type Observation struct {
	// Method and Path are the normalized request line: "get", "/tags/{tag_id}",
	// with every path segment the driver passed in replaced by the placeholder
	// that segment stands for.
	Method string
	Path   string

	// Query is the set of query parameter names the client actually sent.
	Query map[string]bool

	// Headers is the set of contract-relevant header names it sent -- the X-*
	// family, which is where the namespace axis lives.
	Headers map[string]bool

	// Body is the set of top-level JSON keys the request body carried. Empty for
	// a bodyless request, and that difference matters: a write that stopped
	// sending its payload emits the same method, path and query as one that still
	// does.
	Body map[string]bool

	// CallErr is the error the method returned, if any. A decode failure against
	// a schema-derived response body lands here, and that is the point.
	CallErr error

	// NullWitness is the same decode against a body whose nullable properties are
	// all null, and NullWitnessErr the error it produced. A nullable property that
	// comes back as something other than null is one the model cannot represent as
	// absent.
	NullWitness    map[string]json.RawMessage
	NullWitnessErr error

	// Decoded is the re-marshalled response value, when the call returned one:
	// the JSON keys that survived a round trip through the SDK's model, and their
	// values. A documented property missing from here is one the client drops; a
	// nullable property whose value came back as something other than null is one
	// the model cannot represent as absent.
	Decoded map[string]json.RawMessage
}

// Key is the operation identity, matching a coverage row.
func (o Observation) Key() string { return o.Method + " " + o.Path }

// Placeholder sentinels.
//
// Each documented placeholder gets its OWN value, and the recorder maps that
// value back to that placeholder's name. Both used to be reduced to `{}`, which
// meant a driver that passed resource_type and resource_id the wrong way round
// produced exactly the expected route -- argument ORDER was not checked at all.
//
// Two sets, because each driver runs twice. One execution proves what the client
// did with one input; two inputs do not prove a route is invariant either, but
// they do catch a route that depends on the value, which one cannot.
var pathValues = [2]map[string]string{
	{"tag_id": "TAGID1", "alias_id": "ALIASID1", "vocabulary_id": "VOCABID1", "resource_type": "RTYPE1", "resource_id": "RID1"},
	{"tag_id": "TAGID2", "alias_id": "ALIASID2", "vocabulary_id": "VOCABID2", "resource_type": "RTYPE2", "resource_id": "RID2"},
}

// witness names which response body a pass sends.
//
// TWO witnesses, because one is a favorable one. The populated body proves every
// documented property has a field to land in; it says nothing about a property the
// contract marks `nullable`, because the stub picks the non-null value and the
// model is never asked to hold the other state. A model whose field is `int` where
// the contract now permits null decodes `1` perfectly and silently turns `null`
// into `0` -- the same silent-zero family as #32 and #40, arriving through the
// contract instead of through a decoder.
//
// So the second pass sends null for every nullable property and requires it to
// come back as null. encoding/json accepts null into a scalar without error, so a
// decode that merely SUCCEEDS proves nothing here; the round-tripped value is what
// separates a *string from an int.
type witness int

const (
	witnessPopulated witness = iota
	witnessNull
)

// Conformance is what every driver did, keyed by the operation it names.
type Conformance struct {
	Observations map[string]Observation

	// Errors are drivers that could not be run at all: a call that never reached
	// a request, one that issued several, one whose two executions disagreed
	// about the route, or a schema the stub could not build a body from. Distinct
	// from a call error, which is itself an observation.
	Errors map[string]error
}

// recorder is the transport the client under test is given.
//
// A RoundTripper rather than an httptest.Server, for three reasons. It needs no
// listener, so the gate runs in a sandbox with no loopback socket -- which the
// review environment did not have, and an unrunnable gate cannot be reviewed. It
// has no second goroutine, so there is no shared state to race over. And the
// request arrives as a *http.Request rather than a re-parsed copy of one.
type recorder struct {
	// respond builds the response for the request that was just recorded.
	respond func(*http.Request) (*http.Response, error)

	requests []*http.Request

	// synthErr is the stub's own failure to build a body, kept apart from the
	// client's failure to read one. Without this the two are indistinguishable
	// downstream: a schema shape the stub cannot model reaches the SDK as a
	// transport error and gets reported as the SDK failing to handle a response,
	// which blames the client for the gate's own gap.
	synthErr error
}

func (rec *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	rec.requests = append(rec.requests, req)
	resp, err := rec.respond(req)
	if err != nil {
		rec.synthErr = err
	}
	return resp, err
}

// RunConformance drives the SDK, twice per operation.
//
// The spec supplies the response bodies, so this is a two-way test: the request
// side proves what the client sends, and the response side proves that what the
// contract describes still fits the models the client decodes into.
//
// Twice, with different path values, because one execution only says what the
// client did with one input. Two do not prove a route is invariant -- nothing
// short of reading every branch would -- but they catch a route that varies with
// the value, which one execution cannot.
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

	for _, driver := range drivers {
		var (
			seen    [2]Observation
			failure error
		)
		for pass := range pathValues {
			observation, err := runDriver(spec, ops[driver.Op], rows[driver.Op], driver, pass, witness(pass))
			if err != nil {
				failure = err
				break
			}
			seen[pass] = observation
		}
		if failure != nil {
			conf.Errors[driver.Op] = failure
			continue
		}
		// The two executions differ only in the values substituted into the path,
		// so anything else differing means the client's request depends on them.
		if seen[0].Key() != seen[1].Key() {
			conf.Errors[driver.Op] = fmt.Errorf("the route depends on the values passed: %q with one set of ids and %q with another",
				seen[0].Key(), seen[1].Key())
			continue
		}
		// The request side is the first pass's; the null witness's decoded value is
		// carried alongside it, since that is the only thing the second pass is for.
		observation := seen[0]
		observation.NullWitness = seen[1].Decoded
		observation.NullWitnessErr = seen[1].CallErr
		conf.Observations[driver.Op] = observation
	}
	return conf, nil
}

// runDriver executes one driver once and records what it did.
func runDriver(spec *Spec, op *Operation, row CoverageOperation, driver Driver, pass int, w witness) (Observation, error) {
	values := pathValues[pass]
	rec := &recorder{
		respond: func(req *http.Request) (*http.Response, error) {
			return synthesizeResponse(spec, op, row, req, w)
		},
	}
	httpClient := &http.Client{Transport: rec}

	client, err := octonomy.New(octonomy.Config{
		BaseURL:    "https://contractdrift.invalid",
		Token:      "contractdrift",
		TenantID:   "contractdrift",
		HTTPClient: httpClient,
	})
	if err != nil {
		return Observation{}, fmt.Errorf("constructing the client: %w", err)
	}
	health, err := octonomy.NewHealthClient("https://contractdrift.invalid", octonomy.WithHealthHTTPClient(httpClient))
	if err != nil {
		return Observation{}, fmt.Errorf("constructing the health client: %w", err)
	}

	value, callErr := driver.Call(context.Background(), &Env{Client: client, Health: health, values: values})

	if rec.synthErr != nil {
		return Observation{}, rec.synthErr
	}
	switch {
	case len(rec.requests) == 0:
		return Observation{}, fmt.Errorf("the call reached no request: %v", callErr)
	case len(rec.requests) > 1:
		// Recording every request but comparing one would describe a driver by
		// whichever it happened to keep. A driver exercises one operation.
		return Observation{}, fmt.Errorf("the call issued %d requests; a driver exercises one operation", len(rec.requests))
	}

	req := rec.requests[0]
	observed := Observation{
		Method:  strings.ToLower(req.Method),
		Path:    normalizeObservedPath(req.URL.Path, values),
		Query:   queryNames(req.URL.Query()),
		Headers: headerNames(req.Header),
		Body:    bodyKeys(req),
		CallErr: callErr,
		Decoded: remarshalKeys(value),
	}
	return observed, nil
}

// Env is what a driver is handed. Two clients, because the health probes are
// outside the versioned API and are reached from a credential-free constructor.
type Env struct {
	Client *octonomy.Client
	Health *octonomy.HealthClient

	values map[string]string
}

// Path returns the value to pass for a documented path placeholder. It differs
// between a driver's two executions, which is what makes a value-dependent route
// visible -- and it is per placeholder, so passing resource_type where
// resource_id belongs no longer produces the expected route.
func (e *Env) Path(placeholder string) string {
	value, ok := e.values[placeholder]
	if !ok {
		return "UNDECLARED-" + placeholder
	}
	return value
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

// normalizeObservedPath maps each sentinel back to the placeholder it stands for,
// so the observed path is directly comparable with the documented one -- and a
// swapped pair of arguments is not.
func normalizeObservedPath(path string, values map[string]string) string {
	path = strings.TrimPrefix(path, "/api/v2")
	byValue := make(map[string]string, len(values))
	for placeholder, value := range values {
		byValue[value] = "{" + placeholder + "}"
	}
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if placeholder, ok := byValue[part]; ok {
			parts[i] = placeholder
		}
	}
	return strings.Join(parts, "/")
}

// headerNames records the contract-relevant request headers. The X- family is
// where the namespace axis lives, and the rest (Authorization, Accept,
// User-Agent) are transport concerns no operation documents.
func headerNames(header http.Header) map[string]bool {
	out := map[string]bool{}
	for name := range header {
		if strings.HasPrefix(strings.ToLower(name), "x-") {
			out[http.CanonicalHeaderKey(name)] = true
		}
	}
	return out
}

// bodyKeys reads the top-level JSON keys of a request body. The body is consumed
// and restored, because the client still owns the request.
func bodyKeys(req *http.Request) map[string]bool {
	if req.Body == nil || req.GetBody == nil {
		return map[string]bool{}
	}
	body, err := req.GetBody()
	if err != nil {
		return map[string]bool{}
	}
	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	if err != nil || len(raw) == 0 {
		return map[string]bool{}
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return map[string]bool{}
	}
	out := make(map[string]bool, len(object))
	for key := range object {
		out[key] = true
	}
	return out
}

func queryNames(values url.Values) map[string]bool {
	out := map[string]bool{}
	for name := range values {
		out[name] = true
	}
	return out
}

// remarshalKeys renders a decoded response back to JSON and returns its fields --
// for a list, the fields of its first element. A documented property the Go model
// has no field for cannot appear here, which is the whole point; and the VALUES
// are kept, because a nullable property that comes back as `0` rather than `null`
// is one the model cannot represent as absent.
func remarshalKeys(value any) map[string]json.RawMessage {
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
				return map[string]json.RawMessage{}
			}
			object = elements[0]
		}
	}
	return object
}

// synthesizeResponse builds the response the recorder answers with: the
// operation's documented schema, filled with type-appropriate values, wrapped in
// the envelope the coverage row records the real server using.
//
// A schema it cannot build a body from becomes a transport error rather than a
// 500, so the run reports "the gate could not synthesize this" instead of
// blaming the SDK for failing to decode the gate's own apology page.
func synthesizeResponse(spec *Spec, op *Operation, row CoverageOperation, req *http.Request, w witness) (*http.Response, error) {
	body, status, err := synthesizeBody(spec, op, row, w)
	if err != nil {
		return nil, fmt.Errorf("the response stub could not build a body: %w", err)
	}
	resp := &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    req,
	}
	resp.ContentLength = int64(len(body))
	return resp, nil
}

func synthesizeBody(spec *Spec, op *Operation, row CoverageOperation, w witness) ([]byte, int, error) {
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
	object, err := synthesizeSchema(spec, op.OKModel, 0, w)
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
func synthesizeSchema(spec *Spec, name string, depth int, w witness) (map[string]any, error) {
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
		value, err := synthesizeValue(spec, props.Content[i+1], depth, w)
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", name, key, err)
		}
		out[key] = value
	}
	return out, nil
}

func synthesizeValue(spec *Spec, node *yaml.Node, depth int, w witness) (any, error) {
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
		// Nullable is read, not ignored. It was ignored, and a property the
		// contract newly permitted to be null passed against a Go model that
		// cannot hold that state.
		Nullable bool `yaml:"nullable"`
	}
	if err := node.Decode(&schema); err != nil {
		return nil, err
	}
	// The null witness, and the whole reason there are two passes.
	if w == witnessNull && schema.Nullable {
		return nil, nil
	}
	if schema.Ref != "" {
		return synthesizeSchema(spec, schemaName(schema.Ref), depth+1, w)
	}
	// `allOf: [$ref]` plus `nullable` is how drf-spectacular writes a nullable
	// reference -- TagResolution.matched_alias. One member is a wrapper around
	// that member; more than one is a composition this stub does not model, and
	// guessing at it would surface as a decode error blamed on the SDK.
	if len(schema.AllOf) == 1 {
		return synthesizeValue(spec, &schema.AllOf[0], depth+1, w)
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
				value, err := synthesizeValue(spec, inline.Content[i+1], depth+1, w)
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
		item, err := synthesizeValue(spec, &schema.Items, depth+1, w)
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

// sortedFields renders a decoded field set for a report line.
func sortedFields(set map[string]json.RawMessage) []string {
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
