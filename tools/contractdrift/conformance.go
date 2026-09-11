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

	// Prefix is the /api/<version> the request really went to, kept because the
	// suffix normalization below erases it -- and a client that ignored its
	// configured surface would then be invisible, which is exactly what happened.
	Prefix string

	// Query, Headers and Body are what the client sent, VALUES AND ALL.
	//
	// Names alone were not enough, and a review proved it four ways: a parameter
	// retyped in the contract while the client kept sending a string, a params
	// struct wired so `q` carried the Slug field, two JSON tags swapped on a write
	// model, and the two namespace headers crossed. Every one of those keeps every
	// name in place and sends the wrong thing under it.
	//
	// What makes the values checkable is that each driver sends a value naming the
	// wire field it belongs to -- see driverValue in drivers.go. A value that
	// arrives under the wrong name says so on sight.
	Query   map[string]string
	Headers map[string]string

	// Body is the top-level JSON of the request body, empty for a bodyless
	// request. That difference matters on its own: a write that stopped sending
	// its payload emits the same method, path and query as one that still does.
	Body map[string]json.RawMessage

	// CallErr is the error the method returned, if any. A decode failure against
	// a schema-derived response body lands here, and that is the point.
	CallErr error

	// Second is the second execution's request, kept whole. It used to contribute
	// only its decoded body, so the two runs were compared for EQUALITY and the
	// per-execution expectation was merely ALLOWED when they differed -- which
	// means a client hard-coding the first pass's value satisfied both. An
	// exemption is not an assertion.
	Second *Observation

	// NullWitness is the same decode against a body whose nullable properties are
	// all null, and NullWitnessErr the error it produced. A nullable property that
	// comes back as something other than null is one the model cannot represent as
	// absent.
	NullWitness    map[string]json.RawMessage
	NullWitnessErr error

	// Sent is the response body the stub answered with, unwrapped to the same
	// shape Decoded takes. Keeping it is what lets the two be compared: a model
	// that puts one property's value into another's field returns every key with
	// the wrong contents, and only the pair says so.
	Sent map[string]json.RawMessage

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

// Conformance is what every driver did, keyed by "<surface> <operation>".
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
// Twice, with different path values and different boolean witnesses, because one
// execution only says what the client did with one input. Two do not prove a route
// is invariant -- nothing short of reading every branch would -- but they catch a
// route that varies with the value, which one execution cannot. Each execution is
// held to its OWN expected values rather than merely compared with the other: the
// comparison alone was an exemption, and a client hard-coding one pass's value
// satisfied it on both.
func RunConformance(vendored map[string]*Spec, cov *Coverage, drivers []Driver) (*Conformance, error) {
	rows := cov.ByKey()
	conf := &Conformance{
		Observations: map[string]Observation{},
		Errors:       map[string]error{},
	}
	for _, surface := range surfaces {
		if err := runSurface(vendored[surface], surface, rows, drivers, conf); err != nil {
			return nil, err
		}
	}
	return conf, nil
}

// runSurface drives every operation against one REST surface.
//
// Both surfaces, because the SDK speaks both and the gate used to speak only the
// default. A v1-only contract change could land with no client follow-through and
// the offline gate stayed clean -- the exact hole it exists to close, on the half
// of the API it was not looking at. The client is configured for the surface, and
// its requests are compared against that surface's own contract.
func runSurface(spec *Spec, surface string, rows map[string]CoverageOperation, drivers []Driver, conf *Conformance) error {
	ops, err := strippedOperations(spec, surface)
	if err != nil {
		return err
	}

	declared := map[string]bool{}
	for _, driver := range drivers {
		key := surface + " " + driver.Op
		// A duplicate silently overwrote the first entry's observation, so a
		// wrong-route driver followed by a correct one for the same operation
		// produced green.
		if declared[driver.Op] {
			conf.Errors[key] = fmt.Errorf("drivers.go declares %q more than once; the later call would overwrite the earlier one's evidence", driver.Op)
			continue
		}
		declared[driver.Op] = true

		var (
			seen    [2]Observation
			failure error
		)
		for pass := range pathValues {
			observation, err := runDriver(spec, surface, ops[driver.Op], rows[driver.Op], driver, pass, witness(pass))
			if err != nil {
				failure = err
				break
			}
			seen[pass] = observation
		}
		if failure != nil {
			conf.Errors[key] = failure
			continue
		}
		// The two executions differ only in the values substituted into the path,
		// so ANY other difference means the client's request depends on them --
		// and comparing only the route let a driver that passed its options on one
		// execution and not the other go unnoticed, since the first execution
		// supplied all the request-shape evidence.
		if diff := requestDiff(seen[0], seen[1]); diff != "" {
			conf.Errors[key] = fmt.Errorf("the two executions sent different requests: %s", diff)
			continue
		}
		// The request side is the first pass's; the null witness's decoded value is
		// carried alongside it, since that is the only thing the second pass is for.
		observation := seen[0]
		second := seen[1]
		observation.Second = &second
		observation.NullWitness = seen[1].Decoded
		observation.NullWitnessErr = seen[1].CallErr
		conf.Observations[key] = observation
	}
	return nil
}

// requestDiff describes the first difference between two executions' requests,
// ignoring the path values that are supposed to differ.
func requestDiff(a, b Observation) string {
	if a.Key() != b.Key() {
		return fmt.Sprintf("routes %q and %q", a.Key(), b.Key())
	}
	// The prefix, which this did not compare: a client that sent only its second
	// execution to the wrong /api/<version> was invisible, because the prefix
	// assertion downstream sees the first pass alone.
	if a.Prefix != b.Prefix {
		return fmt.Sprintf("prefixes %q and %q", a.Prefix, b.Prefix)
	}
	// And the call outcome. A second-pass failure was only consulted on ordinary
	// model rows, so a composite or no-content operation could fail on its second
	// witness with nothing said.
	if (a.CallErr == nil) != (b.CallErr == nil) {
		return fmt.Sprintf("one execution failed and the other did not: %v / %v", a.CallErr, b.CallErr)
	}
	if d := diffStringMaps("query parameter", a.Query, b.Query); d != "" {
		return d
	}
	if d := diffStringMaps("header", a.Headers, b.Headers); d != "" {
		return d
	}
	aBody := map[string]string{}
	for k, v := range a.Body {
		aBody[k] = string(v)
	}
	bBody := map[string]string{}
	for k, v := range b.Body {
		bBody[k] = string(v)
	}
	return diffStringMaps("body property", aBody, bBody)
}

func diffStringMaps(what string, a, b map[string]string) string {
	for _, key := range sortedStrings(a) {
		bv, ok := b[key]
		if !ok {
			return fmt.Sprintf("%s %s sent once and not the second time", what, key)
		}
		// Identical between the two runs, with ONE declared exception: an input
		// whose expectation differs by execution, which is how three booleans on
		// one request are told apart when there are only two values to go round.
		// A difference that is not exactly that pair is a difference.
		//
		// No path-value exception here. There used to be one, suppressing any
		// difference where either side happened to be a path witness, and it
		// accepted a path argument reused as a query value across both runs. The
		// path is normalized in Key().
		if a[key] != bv && !declaredPassDifference(key, a[key], bv) {
			return fmt.Sprintf("%s %s carried %q then %q", what, key, a[key], bv)
		}
	}
	for _, key := range sortedStrings(b) {
		if _, ok := a[key]; !ok {
			return fmt.Sprintf("%s %s sent only the second time", what, key)
		}
	}
	return ""
}

// declaredPassDifference reports whether two values are exactly the pair this
// input is expected to carry across the two executions.
func declaredPassDifference(name, first, second string) bool {
	return first == ExpectedValue(name, 0) && second == ExpectedValue(name, 1)
}

func sortedStrings(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// runDriver executes one driver once and records what it did.
func runDriver(spec *Spec, surface string, op *Operation, row CoverageOperation, driver Driver, pass int, w witness) (Observation, error) {
	values := pathValues[pass]
	var sent map[string]json.RawMessage
	rec := &recorder{
		respond: func(req *http.Request) (*http.Response, error) {
			return synthesizeResponse(spec, op, row, req, w, &sent)
		},
	}
	httpClient := &http.Client{Transport: rec}

	// The surface under test, and the whole point of running twice. This was
	// missing -- the field simply was not set, so both passes built a default v2
	// client and the "v1" run was a v2 run compared against the v1 document. The
	// prefix is asserted below for the same reason: a silent default cannot be
	// caught by a check that erases the thing it would have changed.
	apiVersion := octonomy.APIV2
	if surface == "v1" {
		apiVersion = octonomy.APIV1
	}
	client, err := octonomy.New(octonomy.Config{
		APIVersion: apiVersion,
		BaseURL:    "https://contractdrift.invalid",
		// The credentials the client_headers expectations are written against, so
		// Authorization and X-Tenant-ID are checked for their VALUES and not only
		// their presence.
		Token:      strings.TrimPrefix(ExpectedValue("authorization", pass), "Bearer "),
		TenantID:   ExpectedValue("x-tenant-id", pass),
		HTTPClient: httpClient,
	})
	if err != nil {
		return Observation{}, fmt.Errorf("constructing the client: %w", err)
	}
	health, err := octonomy.NewHealthClient("https://contractdrift.invalid", octonomy.WithHealthHTTPClient(httpClient))
	if err != nil {
		return Observation{}, fmt.Errorf("constructing the health client: %w", err)
	}

	value, callErr := driver.Call(context.Background(), &Env{
		Client: client, Health: health, values: values, surface: surface, pass: pass,
	})

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
		Prefix:  versionPrefix(req.URL.Path),
		Path:    normalizeObservedPath(req.URL.Path, values),
		Query:   queryNames(req.URL.Query()),
		Headers: headerNames(req.Header),
		Body:    bodyKeys(req),
		CallErr: callErr,
		Sent:    sent,
		Decoded: remarshalKeys(value),
	}
	return observed, nil
}

// Env is what a driver is handed. Two clients, because the health probes are
// outside the versioned API and are reached from a credential-free constructor.
type Env struct {
	Client *octonomy.Client
	Health *octonomy.HealthClient

	values  map[string]string
	surface string
	pass    int
}

// Namespaced returns the namespace option for the surface under test, and NOTHING
// on v1 -- which has no namespace axis, refuses the headers with
// namespace_not_supported, and whose client rejects the option before the wire.
// A driver that hard-coded it could not run against v1 at all.
func (e *Env) Namespaced() []octonomy.RequestOption {
	if e.surface == "v1" {
		return nil
	}
	return []octonomy.RequestOption{octonomy.WithNamespace(
		ExpectedValue("x-namespace-type", e.pass), ExpectedValue("x-namespace-id", e.pass))}
}

// ReadScope is what a bodyless read with NO params struct carries: an
// application, the namespace pair where the surface has one, and the opt-in that
// widens a namespaced read back to global rows.
//
// WithApplication only where the method has nowhere else to put it. A list driver
// that passed both this and its params struct's ApplicationID put the same value
// on the wire twice, so either implementation path could break and the other
// covered for it -- a review removed TagListParams' emission entirely and the gate
// stayed clean. One input, one source.
func (e *Env) ReadScope() []octonomy.RequestOption {
	opts := []octonomy.RequestOption{octonomy.WithApplication(ExpectedValue("application_id", e.pass))}
	opts = append(opts, e.Namespaced()...)
	return append(opts, e.IncludeGlobal()...)
}

// ListScope is what a read WITH a params struct carries: everything ReadScope
// does except the application, which its own ApplicationID field supplies.
func (e *Env) ListScope() []octonomy.RequestOption {
	return append(e.Namespaced(), e.IncludeGlobal()...)
}

// IncludeGlobal is the namespaced-read opt-in, and v2 only: v1 has no namespace
// axis for it to widen.
func (e *Env) IncludeGlobal() []octonomy.RequestOption {
	if e.surface != "v2" {
		return nil
	}
	return []octonomy.RequestOption{octonomy.WithIncludeGlobal()}
}

// DeleteScope is what a BODYLESS write carries: an application, which a
// namespaced bodyless request requires, and the namespace pair. Not
// WithIncludeGlobal -- the server reads that only on safe methods, so the SDK
// refuses it on a write rather than let it be dropped in silence.
func (e *Env) DeleteScope() []octonomy.RequestOption {
	opts := []octonomy.RequestOption{octonomy.WithApplication(ExpectedValue("application_id", e.pass))}
	return append(opts, e.Namespaced()...)
}

// Scope is the namespace alone, for a request whose BODY carries the application:
// on a POST or PATCH the body is authoritative and WithApplication is refused.
func (e *Env) Scope() []octonomy.RequestOption { return e.Namespaced() }

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

// versionPrefix is the /api/<version> segment a request was sent to, or "" for
// the unversioned probes.
func versionPrefix(path string) string {
	for _, prefix := range []string{"/api/v1", "/api/v2"} {
		if strings.HasPrefix(path, prefix+"/") {
			return prefix
		}
	}
	return ""
}

// normalizeObservedPath maps each sentinel back to the placeholder it stands for,
// so the observed path is directly comparable with the documented one -- and a
// swapped pair of arguments is not.
func normalizeObservedPath(path string, values map[string]string) string {
	path = strings.TrimPrefix(strings.TrimPrefix(path, "/api/v2"), "/api/v1")
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

// recordedHeaders are the request headers this gate reasons about: the X- family,
// where the namespace and tenant axes live, and Authorization -- which no
// operation documents but which the health probes must NOT carry, and that is a
// rule worth checking rather than assuming. Accept, Content-Type and User-Agent
// are transport decoration and are left out.
func headerNames(header http.Header) map[string]string {
	out := map[string]string{}
	for name, values := range header {
		canonical := http.CanonicalHeaderKey(name)
		if !strings.HasPrefix(strings.ToLower(name), "x-") && canonical != "Authorization" {
			continue
		}
		if len(values) > 0 {
			out[canonical] = values[0]
		} else {
			out[canonical] = ""
		}
	}
	return out
}

// bodyKeys reads the top-level JSON keys of a request body. The body is consumed
// and restored, because the client still owns the request.
func bodyKeys(req *http.Request) map[string]json.RawMessage {
	if req.Body == nil || req.GetBody == nil {
		return map[string]json.RawMessage{}
	}
	body, err := req.GetBody()
	if err != nil {
		return map[string]json.RawMessage{}
	}
	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	if err != nil || len(raw) == 0 {
		return map[string]json.RawMessage{}
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return map[string]json.RawMessage{}
	}
	return object
}

func queryNames(values url.Values) map[string]string {
	out := map[string]string{}
	for name, v := range values {
		if len(v) > 0 {
			out[name] = v[0]
		} else {
			out[name] = ""
		}
	}
	return out
}

// unwrapEnvelope reduces a response body to the model the client decodes: the
// object under `data`, or the first element of a list under it.
func unwrapEnvelope(body []byte) map[string]json.RawMessage {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(body, &object); err != nil {
		return nil
	}
	data, ok := object["data"]
	if !ok {
		return object
	}
	var inner map[string]json.RawMessage
	if err := json.Unmarshal(data, &inner); err == nil {
		return inner
	}
	var elements []map[string]json.RawMessage
	if err := json.Unmarshal(data, &elements); err != nil {
		return nil
	}
	out := map[string]json.RawMessage{}
	if len(elements) > 0 {
		out = elements[0]
	}
	// The pagination block, under the same prefix remarshalKeys uses, so the two
	// sides line up.
	if page, ok := object["pagination"]; ok {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(page, &fields); err == nil {
			for name, value := range fields {
				out["pagination."+name] = value
			}
		}
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
	// under test is the element type, and the pagination block rides alongside it
	// under a prefix. It used to be dropped, so a client that crossed `offset` and
	// `count` on the way out was invisible.
	if data, ok := object["data"]; ok {
		var elements []map[string]json.RawMessage
		if err := json.Unmarshal(data, &elements); err == nil {
			out := map[string]json.RawMessage{}
			if len(elements) > 0 {
				out = elements[0]
			}
			if page, ok := object["pagination"]; ok {
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(page, &fields); err == nil {
					for name, value := range fields {
						out["pagination."+name] = value
					}
				}
			}
			return out
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
func synthesizeResponse(spec *Spec, op *Operation, row CoverageOperation, req *http.Request, w witness, sent *map[string]json.RawMessage) (*http.Response, error) {
	body, status, err := synthesizeBody(spec, op, row, w)
	if err == nil && sent != nil {
		*sent = unwrapEnvelope(body)
	}
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
	// result object the contract never describes. There is no schema to synthesize
	// from, so the body comes from the inventory, where it is recorded as the
	// reviewed fact it is -- and one per operation, since a universal object
	// carrying every composite's keys left each decoder dropping the others'.
	if row.ActualResponse == "composite-envelope" {
		return []byte(`{"data":` + row.CompositeBody + `}`), http.StatusOK, nil
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
		// Distinct numbers, for the same reason the composite counters are: a
		// client crossing two of them has to change both.
		payload = map[string]any{
			"data": []any{object},
			"pagination": map[string]any{
				"limit": 11, "offset": 22, "count": 33, "next": nil, "previous": nil,
			},
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
		value, err := synthesizeValue(spec, props.Content[i+1], depth, w, key)
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", name, key, err)
		}
		out[key] = value
	}
	return out, nil
}

func synthesizeValue(spec *Spec, node *yaml.Node, depth int, w witness, property string) (any, error) {
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
		return synthesizeValue(spec, &schema.AllOf[0], depth+1, w, property)
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
			// Derived from the property name, so `created_at` and `updated_at` are
			// not the same instant. Every date-time used to be one timestamp, and a
			// decoder crossing two of them preserved every compared byte.
			return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).
				Add(time.Duration(nameOffset(property)) * time.Second).Format(time.RFC3339), nil
		case "uuid":
			return "00000000-0000-4000-8000-000000000000", nil
		case "date":
			return "2026-01-02", nil
		}
		// The property's OWN name, in the same form the drivers use, so a response
		// value that lands in the wrong field says where it came from. Every string
		// used to be the same word.
		return ExpectedValue(property, 0), nil
	case "integer":
		// Also derived from the name: every integer was 1, so two integer
		// properties could be crossed without changing anything compared.
		return 1 + nameOffset(property), nil
	case "number":
		return 1.5 + float64(nameOffset(property)), nil
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
				value, err := synthesizeValue(spec, inline.Content[i+1], depth+1, w, inline.Content[i].Value)
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
		item, err := synthesizeValue(spec, &schema.Items, depth+1, w, property)
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
// nameOffset turns a property name into a small stable number, so two properties
// of the same type get different witnesses.
func nameOffset(name string) int {
	sum := 0
	for _, r := range name {
		sum = sum*31 + int(r)
	}
	if sum < 0 {
		sum = -sum
	}
	return sum%97 + 1
}

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

// sortedFields renders a decoded field set for a report line.
func sortedFields(set map[string]json.RawMessage) []string {
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
