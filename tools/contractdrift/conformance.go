package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

	// ErrorEnvelope is one extra call per surface, answered with a NON-2xx whose
	// body is built from the contract's ErrorResponse schema. It is the most
	// referenced schema in the contract -- 30 `$ref`s -- and nothing offline
	// compared it, because the stub only ever answered 200 or 204. A refresh that
	// renamed `error.code` left every Is* helper silently returning false and the
	// gate said nothing.
	ErrorEnvelope map[string]ErrorObservation

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
		Observations:  map[string]Observation{},
		ErrorEnvelope: map[string]ErrorObservation{},
		Errors:        map[string]error{},
	}
	for _, surface := range surfaces {
		if err := runSurface(vendored[surface], surface, rows, drivers, conf); err != nil {
			return nil, err
		}
		observation, err := runErrorEnvelope(vendored[surface], surface)
		if err != nil {
			return nil, err
		}
		conf.ErrorEnvelope[surface] = observation
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

// runErrorEnvelope asks the client to read one synthesized error body.
//
// A 409, because it is a status the SDK maps to a semantic code, so a renamed
// property is the difference between IsConflict answering true and answering
// false -- and IsConflict is asserted, not merely cited. The request itself does
// not matter here beyond the surface it went to, so this uses the simplest read
// there is.
func runErrorEnvelope(spec *Spec, surface string) (ErrorObservation, error) {
	envelope, err := synthesizeSchema(spec, "ErrorResponse", 0, witnessPopulated)
	if err != nil {
		return ErrorObservation{}, fmt.Errorf("the response stub could not build an error envelope: %w", err)
	}
	// `code` is the one property that cannot take a name-shaped witness. It is an
	// enum in everything but type -- the contract types it `string`, which is why
	// the server's registry has to be vendored separately -- and a made-up value
	// proves the property round-trips while proving nothing about what a caller
	// does with it, since IsConflict answers false for `cd~code` correctly. So the
	// envelope carries a real code, and the helper named after it is asserted.
	// A code constant whose VALUE drifts is not a hole here: checkErrorCodesImplemented
	// compares every Code* constant against the vendored server registry.
	if inner, ok := envelope["error"].(map[string]any); ok {
		if _, documented := inner["code"]; documented {
			inner["code"] = octonomy.CodeConflict
		}
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		return ErrorObservation{}, err
	}

	rec := &recorder{respond: func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusConflict,
			Header:        http.Header{"Content-Type": []string{"application/json"}},
			Body:          io.NopCloser(bytes.NewReader(body)),
			ContentLength: int64(len(body)),
			Request:       req,
		}, nil
	}}

	apiVersion := octonomy.APIV2
	if surface == "v1" {
		apiVersion = octonomy.APIV1
	}
	client, err := octonomy.New(octonomy.Config{
		APIVersion: apiVersion,
		BaseURL:    "https://contractdrift.invalid",
		Token:      strings.TrimPrefix(ExpectedValue("authorization", 0), "Bearer "),
		TenantID:   ExpectedValue("x-tenant-id", 0),
		HTTPClient: &http.Client{Transport: rec},
	})
	if err != nil {
		return ErrorObservation{}, err
	}

	_, callErr := client.Tags.Get(context.Background(), "ERRID")

	observation := ErrorObservation{
		Sent:         errorObject(body),
		Helpers:      map[string][]string{},
		HelperPrefix: map[string]string{},
	}
	if len(rec.requests) > 0 {
		observation.Prefix = versionPrefix(rec.requests[0].URL.Path)
	}
	var apiErr *octonomy.APIError
	if !errors.As(callErr, &apiErr) {
		observation.NotAPIErr = callErr
		return observation, nil
	}
	observation.Code = apiErr.Code
	observation.Message = apiErr.Message
	observation.RequestID = apiErr.RequestID
	observation.Details = apiErr.Details
	observation.Status = apiErr.StatusCode

	// One drive per helper, each answered with that helper's own code.
	for _, helper := range semanticHelpers {
		prefix, err := driveHelperCode(spec, apiVersion, helper.Code, true)
		observation.HelperPrefix[helper.Name] = prefix
		if err == nil {
			observation.Helpers[helper.Name] = []string{"<no error at all>"}
			continue
		}
		var answered []string
		for _, other := range semanticHelpers {
			if other.Is(err) {
				answered = append(answered, other.Name)
			}
		}
		observation.Helpers[helper.Name] = answered
	}

	// And the envelope-less branch, which is where CodeUnexpectedStatus actually
	// comes from.
	fallbackPrefix, fallbackErr := driveHelperCode(spec, apiVersion, "", false)
	observation.FallbackPrefix = fallbackPrefix
	observation.FallbackUnexpected = octonomy.IsUnexpectedStatus(fallbackErr)
	var fallbackAPIErr *octonomy.APIError
	if errors.As(fallbackErr, &fallbackAPIErr) {
		observation.FallbackCode = fallbackAPIErr.Code
		observation.FallbackStatus = fallbackAPIErr.StatusCode
	}

	truncatedPrefix, truncatedErr := driveTruncatedBody(apiVersion)
	observation.TruncatedPrefix = truncatedPrefix
	observation.TruncatedUnexpected = octonomy.IsUnexpectedStatus(truncatedErr)
	var truncatedAPIErr *octonomy.APIError
	if errors.As(truncatedErr, &truncatedAPIErr) {
		observation.TruncatedCode = truncatedAPIErr.Code
		observation.TruncatedStatus = truncatedAPIErr.StatusCode
	}
	observation.TruncatedCause = errors.Is(truncatedErr, errBodyDropped)
	return observation, nil
}

// errAfterSomeBytes is a response body that starts arriving and then fails, which
// is the shape unreadableBodyError is written for.
type errAfterSomeBytes struct{ sent bool }

// errBodyDropped is the read failure this drive injects, kept as a value so the
// drive can ask whether the SDK still carries it: an *APIError that loses its
// cause leaves the caller unable to say why the body never arrived.
var errBodyDropped = errors.New("contractdrift: the connection dropped mid-body")

func (r *errAfterSomeBytes) Read(p []byte) (int, error) {
	if r.sent {
		return 0, errBodyDropped
	}
	r.sent = true
	n := copy(p, []byte(`{"error":`))
	return n, nil
}

func (r *errAfterSomeBytes) Close() error { return nil }

// driveTruncatedBody answers one call with a non-2xx whose body cannot be read to
// completion.
func driveTruncatedBody(apiVersion octonomy.APIVersion) (string, error) {
	rec := &recorder{respond: func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusConflict,
			Header:        http.Header{"Content-Type": []string{"application/json"}},
			Body:          &errAfterSomeBytes{},
			ContentLength: 256,
			Request:       req,
		}, nil
	}}
	client, err := octonomy.New(octonomy.Config{
		APIVersion: apiVersion,
		BaseURL:    "https://contractdrift.invalid",
		Token:      strings.TrimPrefix(ExpectedValue("authorization", 0), "Bearer "),
		TenantID:   ExpectedValue("x-tenant-id", 0),
		HTTPClient: &http.Client{Transport: rec},
	})
	if err != nil {
		return "", err
	}
	_, callErr := client.Tags.Get(context.Background(), "ERRID")
	prefix := ""
	if len(rec.requests) > 0 {
		prefix = versionPrefix(rec.requests[0].URL.Path)
	}
	return prefix, callErr
}

// driveHelperCode answers one call with an envelope carrying exactly `code` --
// or, when enveloped is false, with a body that is NOT an envelope at all -- and
// returns the error the client produced and the /api/<version> the call really
// reached.
//
// The prefix is returned rather than assumed. A drive that returns a fabricated
// error without issuing a request answers every question correctly about
// something no client produced, and the prefix is what makes that visible.
func driveHelperCode(spec *Spec, apiVersion octonomy.APIVersion, code string, enveloped bool) (string, error) {
	// The shape a server that predates the versioned URLconf really returns, and
	// the shape any proxy returns: HTML, no envelope, nothing parseError can read a
	// code out of.
	body := []byte("<h1>Bad Gateway</h1>")
	if enveloped {
		envelope, err := synthesizeSchema(spec, "ErrorResponse", 0, witnessPopulated)
		if err != nil {
			return "", err
		}
		if inner, ok := envelope["error"].(map[string]any); ok {
			inner["code"] = code
		}
		if body, err = json.Marshal(envelope); err != nil {
			return "", err
		}
	}
	status, contentType := http.StatusConflict, "application/json"
	if !enveloped {
		status, contentType = http.StatusBadGateway, "text/html"
	}
	rec := &recorder{respond: func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    status,
			Header:        http.Header{"Content-Type": []string{contentType}},
			Body:          io.NopCloser(bytes.NewReader(body)),
			ContentLength: int64(len(body)),
			Request:       req,
		}, nil
	}}
	client, err := octonomy.New(octonomy.Config{
		APIVersion: apiVersion,
		BaseURL:    "https://contractdrift.invalid",
		Token:      strings.TrimPrefix(ExpectedValue("authorization", 0), "Bearer "),
		TenantID:   ExpectedValue("x-tenant-id", 0),
		HTTPClient: &http.Client{Transport: rec},
	})
	if err != nil {
		return "", err
	}
	_, callErr := client.Tags.Get(context.Background(), "ERRID")
	prefix := ""
	if len(rec.requests) > 0 {
		prefix = versionPrefix(rec.requests[0].URL.Path)
	}
	return prefix, callErr
}

// errorObject pulls the inner `error` object out of a synthesized envelope, which
// is the part the SDK actually decodes.
func errorObject(body []byte) map[string]json.RawMessage {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil
	}
	var inner map[string]json.RawMessage
	if err := json.Unmarshal(envelope["error"], &inner); err != nil {
		return nil
	}
	return inner
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

// ErrorObservation is what the client made of a synthesized error envelope.
type ErrorObservation struct {
	// Sent is the envelope's inner `error` object, as the stub built it.
	Sent map[string]json.RawMessage

	// Code, Message and RequestID are what the SDK's *APIError carried back, and
	// NotAPIErr is the error itself when it was not an *APIError at all -- which is
	// the failure this exists to catch: a renamed property makes parseError fall
	// through to CodeUnexpectedStatus and every Is* helper answer false.
	Code      string
	Message   string
	RequestID string
	Details   map[string]any
	NotAPIErr error

	// Prefix is the /api/<version> the call really went to. Without it the v1
	// drive is a v1 drive only by intention: changing the surface test in
	// runErrorEnvelope to something that never matches left both drives on v2 and
	// nothing said so -- the same shape as the Config.APIVersion that was never
	// set, recurring in new code.
	Prefix string

	// Status and Conflict are what a caller actually reaches for. The comment
	// above picked 409 because it is a status the SDK maps to a semantic code, and
	// an unasserted claim is not a claim: with these unrecorded, StatusCode: 0 in
	// parseError and an IsConflict rewired to CodeValidation were both green.
	Status int

	// HelperPrefix is where each helper's drive actually went, keyed by helper
	// name. Without it the drive can stop driving: replacing driveHelperCode's body
	// with a fabricated *APIError bypasses synthesis, transport and decoding, and
	// every helper still answered correctly about an error no client produced.
	HelperPrefix map[string]string

	// FallbackCode, FallbackUnexpected and FallbackPrefix are one more drive, for
	// the branch of parseError that no envelope reaches: a non-2xx whose body is
	// NOT the contract's shape. That is where CodeUnexpectedStatus is really
	// manufactured, and driving IsUnexpectedStatus through a synthesized envelope
	// carrying `unexpected_status` proved it for a response no server sends.
	FallbackCode       string
	FallbackUnexpected bool
	FallbackPrefix     string
	FallbackStatus     int

	// TruncatedCode, TruncatedUnexpected and TruncatedPrefix are the OTHER path
	// that manufactures CodeUnexpectedStatus: a non-2xx whose body cannot be read
	// to completion. No envelope was decoded there either, so no semantic code was
	// established -- and guessing one from the status is precisely what that
	// constant exists to forbid. It is a separate drive because a complete body,
	// however unparseable, reaches parseError instead.
	TruncatedCode       string
	TruncatedUnexpected bool
	TruncatedPrefix     string
	TruncatedStatus     int

	// TruncatedCause is whether the read failure is still reachable through
	// errors.Is. `err: cause` dropped to nil left the *APIError intact and the
	// caller with nothing to say WHY the body did not arrive.
	TruncatedCause bool

	// Helpers maps each semantic helper's name to the names of ALL the helpers that
	// answered true when that helper's own code was sent. Each must answer for
	// itself and nothing else.
	//
	// One helper was asserted at first -- IsConflict, because it was the code the
	// drive happened to send -- and rewiring IsNotFound to CodeForbidden stayed
	// clean. A caller reaches for the helper, not the constant, so each of the
	// sixteen is a claim and each one gets driven.
	Helpers map[string][]string
}

// semanticHelpers pairs each exported Is* predicate with the code it is named
// after. TestEveryHelperIsDriven fails if errors.go grows one this does not list,
// so a new helper cannot arrive uncovered.
var semanticHelpers = []struct {
	Name string
	Code string
	Is   func(error) bool
}{
	{"IsNotFound", octonomy.CodeNotFound, octonomy.IsNotFound},
	{"IsConflict", octonomy.CodeConflict, octonomy.IsConflict},
	{"IsValidation", octonomy.CodeValidation, octonomy.IsValidation},
	{"IsAuthError", octonomy.CodeAuthRequired, octonomy.IsAuthError},
	{"IsForbidden", octonomy.CodeForbidden, octonomy.IsForbidden},
	{"IsTenantMismatch", octonomy.CodeTenantMismatch, octonomy.IsTenantMismatch},
	{"IsApplicationMismatch", octonomy.CodeApplicationMismatch, octonomy.IsApplicationMismatch},
	{"IsInactiveTag", octonomy.CodeInactiveTag, octonomy.IsInactiveTag},
	{"IsScopeImmutable", octonomy.CodeScopeImmutable, octonomy.IsScopeImmutable},
	{"IsNamespaceNotSupported", octonomy.CodeNamespaceNotSupported, octonomy.IsNamespaceNotSupported},
	{"IsNamespaceInvalid", octonomy.CodeNamespaceInvalid, octonomy.IsNamespaceInvalid},
	{"IsNamespacedWritesDisabled", octonomy.CodeNamespacedWritesDisabled, octonomy.IsNamespacedWritesDisabled},
	{"IsNamespaceAPIDisabled", octonomy.CodeNamespaceAPIDisabled, octonomy.IsNamespaceAPIDisabled},
	{"IsAmbiguousResolution", octonomy.CodeAmbiguousResolution, octonomy.IsAmbiguousResolution},
	{"IsNotReady", octonomy.CodeNotReady, octonomy.IsNotReady},
	{"IsUnexpectedStatus", octonomy.CodeUnexpectedStatus, octonomy.IsUnexpectedStatus},
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
			// `next` and `previous` carry real values, not nil. They used to be the
			// one pair here sent only as null, so the type BEHIND the pointer was
			// never exercised -- and `Each` terminates on `Pagination.Next == nil`,
			// so a mistyped Next breaks every list walk against a real server.
			"pagination": map[string]any{
				"limit": 11, "offset": 22, "count": 33,
				"next":     "https://contractdrift.invalid/next",
				"previous": "https://contractdrift.invalid/previous",
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
			// Derived from the property name, like the timestamps and integers
			// above and for the same reason: Tag.id, Tag.parent_id and
			// Tag.vocabulary_id are three uuids on one model, and one shared
			// witness meant crossing any two of them changed nothing compared.
			return fmt.Sprintf("%08d-0000-4000-8000-000000000000", nameOffset(property)), nil
		case "date":
			return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC).
				AddDate(0, 0, nameOffset(property)).Format(time.DateOnly), nil
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
		// Keyed by the property, not a constant: `AuditLog.changes` and
		// `AuditLog.metadata` are both free-form objects, and one shared value made
		// crossing them invisible -- the same defect the uuid and date witnesses
		// above carried until it was found.
		return map[string]any{"contractdrift": property}, nil
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
		// will say so. Keyed by the property for the same reason as the branch
		// above: two of them on one schema have to be distinguishable.
		return map[string]any{"contractdrift": property}, nil
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
	// The modulus is wide enough that no two property names in either contract
	// collide. It was 97, and `id` and `operation_id` both landed on 58 -- so
	// AuditLog's two uuids shared a witness and crossing them was invisible, which
	// is the exact defect the per-property witnesses were introduced to close,
	// surviving inside the fix for it. TestWitnessesDoNotCollide walks both
	// contracts and fails on any same-schema pair that shares one.
	return sum%99991 + 1
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
