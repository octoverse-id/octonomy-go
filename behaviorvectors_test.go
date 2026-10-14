package octonomy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// This file is the Go binding for testdata/behavior_vectors.json (#104).
//
// The vectors pin transport behaviour no contract expresses -- which of its own
// requests a client refuses before sending, and what it makes of a response:
// the envelope and identity guards, error-code mapping, the bounded read, the
// depth refusal. They are data, in language-neutral terms, so the same file can
// drive more than one implementation -- the Go 1.13 line (support/go1.13) is the
// other one it exists for (#104). What is Go-specific lives here: how each
// outcome REASON is recognised in a Go error. testdata/README.md describes the
// format.
//
// A change to a behaviour a vector pins fails this test, and it runs in the
// root package, so the required test jobs run it on every pull request. A
// behaviour no vector pins is not protected, which is why each rule is tried at
// more than one point. TestBehaviorVectorsCatchADeviation proves the first half
// of that for the ceiling, the API version and the route check by bending them.

const behaviorVectorsFile = "testdata/behavior_vectors.json"

type behaviorVectorFile struct {
	Comment  string           `json:"$comment"`
	Version  int              `json:"version"`
	Response []responseVector `json:"response"`
	Request  []requestVector  `json:"request"`
}

type responseVector struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Surfaces    []string        `json:"surfaces"`
	Call        string          `json:"call"`
	Status      int             `json:"status"`
	Body        *string         `json:"body"`
	PadToBytes  int             `json:"pad_to_bytes"`
	Nest        *nestSpec       `json:"nest"`
	Outcome     responseOutcome `json:"outcome"`
}

type nestSpec struct {
	Placeholder string `json:"placeholder"`
	Depth       int    `json:"depth"`
}

// responseOutcome is both what a vector expects and what the binding observed,
// so the two compare with reflect.DeepEqual and print the same way.
type responseOutcome struct {
	Reason       string   `json:"reason"`
	Status       int      `json:"status,omitempty"`
	Code         string   `json:"code,omitempty"`
	RequestID    string   `json:"request_id,omitempty"`
	Wraps        string   `json:"wraps,omitempty"`
	ID           *string  `json:"id,omitempty"`
	IDs          []string `json:"ids,omitempty"`
	HealthStatus *string  `json:"health_status,omitempty"`
}

type requestVector struct {
	Name        string                       `json:"name"`
	Description string                       `json:"description"`
	Surfaces    []string                     `json:"surfaces"`
	Call        string                       `json:"call"`
	Params      map[string]string            `json:"params"`
	Options     []map[string]json.RawMessage `json:"options"`
	Outcome     requestOutcome               `json:"outcome"`
}

type requestOutcome struct {
	Reason        string   `json:"reason"`
	Namespace     []string `json:"namespace,omitempty"`
	ApplicationID *string  `json:"application_id,omitempty"`
	IncludeGlobal bool     `json:"include_global,omitempty"`
}

func (o responseOutcome) String() string { return marshalForMessage(o) }
func (o requestOutcome) String() string  { return marshalForMessage(o) }

func marshalForMessage(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%+v", v)
	}
	return string(b)
}

// The reasons each kind of vector may name. A reason in the file that is not
// here fails validation; a reason here that no vector names fails it too, so the
// binding cannot keep recognising a behaviour nothing exercises.
var (
	responseReasons = []string{
		"ok", "api_error", "response_too_large", "envelope_missing", "not_a_resource",
		"identity_missing", "pagination_missing", "pagination_unusable", "type_mismatch",
		"malformed_json", "depth_exceeded", "unexpected_delete_response", "health_status_missing",
	}
	requestReasons = []string{
		"sent", "namespace_requires_v2", "namespace_type_required", "namespace_id_required",
		"namespace_type_reserved", "namespace_contradiction", "namespace_requires_application",
		"application_required", "application_contradiction", "application_on_write",
		"include_global_requires_v2", "include_global_on_write",
		"merchant_scope_requires_namespace", "include_global_contradicts_merchant_scope",
	}
)

// responseErrorPhrases recognise the refusals that carry no sentinel. They are
// this binding's own business -- the vector file names reasons, never messages
// -- and each reason must be recognised by exactly one entry, which
// classifyResponseError enforces at runtime.
var responseErrorPhrases = map[string][]string{
	"envelope_missing": {
		`empty response body where a payload was expected`,
		`response body has no "data" envelope`,
	},
	"not_a_resource": {
		`is null, expected a resource object`,
		`is an array, expected a single resource object`,
		`is not a resource object`,
		`is an empty object, which would decode to a zero-valued resource`,
	},
	"identity_missing":    {`decoded with no "`},
	"pagination_missing":  {`list response has no "pagination" block`},
	"pagination_unusable": {`list response has an unusable "pagination" block`},
	"unexpected_delete_response": {
		`expected 204 No Content`,
		`204 response carried a`,
	},
	"health_status_missing": {
		`empty health response body`,
		`health response carried no "status" field`,
	},
}

var requestRefusalPatterns = map[string]*regexp.Regexp{
	"namespace_requires_v2":                     regexp.MustCompile(`WithNamespace requires the v2 API surface`),
	"namespace_type_required":                   regexp.MustCompile(`WithNamespace: namespace type is required`),
	"namespace_id_required":                     regexp.MustCompile(`WithNamespace: namespace id is required`),
	"namespace_type_reserved":                   regexp.MustCompile(`WithNamespace: "[^"]*" is a reserved namespace type`),
	"namespace_contradiction":                   regexp.MustCompile(`WithNamespace\(.*\) contradicts the namespace already set on this request`),
	"namespace_requires_application":            regexp.MustCompile(`a namespaced [A-Z]+ must also name its application`),
	"application_required":                      regexp.MustCompile(`WithApplication: application id is required`),
	"application_contradiction":                 regexp.MustCompile(`WithApplication\(.*\) contradicts the (application|application_id) already set on this request`),
	"application_on_write":                      regexp.MustCompile(`WithApplication does not apply to a [A-Z]+, which carries a body`),
	"include_global_requires_v2":                regexp.MustCompile(`WithIncludeGlobal requires the v2 API surface`),
	"include_global_on_write":                   regexp.MustCompile(`WithIncludeGlobal applies to reads only`),
	"merchant_scope_requires_namespace":         regexp.MustCompile(`scope=merchant resolves within the request's namespace, but this request has none`),
	"include_global_contradicts_merchant_scope": regexp.MustCompile(`WithIncludeGlobal contradicts scope=merchant`),
}

func loadBehaviorVectors(t *testing.T) *behaviorVectorFile {
	t.Helper()
	raw, err := os.ReadFile(filepath.FromSlash(behaviorVectorsFile))
	if err != nil {
		t.Fatalf("read the behavior vectors: %v", err)
	}
	vf, err := decodeBehaviorVectors(raw)
	if err != nil {
		t.Fatalf("%s: %v", behaviorVectorsFile, err)
	}
	return vf
}

// decodeBehaviorVectors reads the file strictly -- an unknown key anywhere a
// struct decodes is an error, so a misspelled field cannot be silently ignored
// -- and then validates what strict decoding cannot see.
func decodeBehaviorVectors(raw []byte) (*behaviorVectorFile, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var vf behaviorVectorFile
	if err := dec.Decode(&vf); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if dec.More() {
		return nil, errors.New("trailing data after the vector object")
	}
	if err := vf.validate(); err != nil {
		return nil, err
	}
	return &vf, nil
}

func (vf *behaviorVectorFile) validate() error {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	if vf.Version != 1 {
		add("version is %d; this binding reads version 1", vf.Version)
	}
	if len(vf.Response) == 0 || len(vf.Request) == 0 {
		add("both the response and the request lists must be non-empty")
	}
	seen := map[string]bool{}
	used := map[string]bool{}
	checkCommon := func(kind, name, description string, surfaces []string) {
		if name == "" || strings.TrimSpace(description) == "" {
			add("%s vector %q: name and description are required", kind, name)
		}
		if seen[name] {
			add("vector name %q is used twice", name)
		}
		seen[name] = true
		if len(surfaces) == 0 {
			add("%s vector %q: surfaces is empty", kind, name)
		}
		dup := map[string]bool{}
		for _, s := range surfaces {
			if s != "v1" && s != "v2" {
				add("%s vector %q: unknown surface %q", kind, name, s)
			}
			if dup[s] {
				add("%s vector %q: surface %q listed twice", kind, name, s)
			}
			dup[s] = true
		}
	}

	for _, v := range vf.Response {
		checkCommon("response", v.Name, v.Description, v.Surfaces)
		if err := v.validate(); err != nil {
			add("response vector %q: %v", v.Name, err)
		}
		used[v.Outcome.Reason] = true
	}
	for _, v := range vf.Request {
		checkCommon("request", v.Name, v.Description, v.Surfaces)
		if err := v.validate(); err != nil {
			add("request vector %q: %v", v.Name, err)
		}
		used[v.Outcome.Reason] = true
	}
	for _, r := range append(append([]string(nil), responseReasons...), requestReasons...) {
		if !used[r] {
			add("reason %q is bound here but no vector names it", r)
		}
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "\n"))
	}
	return nil
}

func (v responseVector) validate() error {
	switch v.Call {
	case "get", "list", "delete", "health":
	default:
		return fmt.Errorf("unknown call %q", v.Call)
	}
	if v.Status < 100 || v.Status > 599 {
		return fmt.Errorf("status %d is not an HTTP status", v.Status)
	}
	if v.Body == nil {
		return errors.New(`body is required; write "" for an empty one`)
	}
	if v.Status == http.StatusNoContent && *v.Body != "" {
		return errors.New("a 204 cannot carry a body")
	}
	if v.PadToBytes != 0 && v.PadToBytes < len(*v.Body) {
		return fmt.Errorf("pad_to_bytes %d is shorter than the body", v.PadToBytes)
	}
	// Each builds the body its own way, so a vector takes at most one, and a 204
	// -- which carries no body -- takes neither.
	if v.PadToBytes != 0 && v.Nest != nil {
		return errors.New("pad_to_bytes and nest are not combined")
	}
	if v.Status == http.StatusNoContent && (v.PadToBytes != 0 || v.Nest != nil) {
		return errors.New("a 204 carries no body to pad or nest")
	}
	if v.Nest != nil {
		if v.Nest.Placeholder == "" || v.Nest.Depth < 1 {
			return errors.New("nest needs a placeholder and a positive depth")
		}
		if strings.Count(*v.Body, v.Nest.Placeholder) != 1 {
			return fmt.Errorf("nest placeholder %q must occur exactly once in the body", v.Nest.Placeholder)
		}
	}
	o := v.Outcome
	if !contains(responseReasons, o.Reason) {
		return fmt.Errorf("unknown response reason %q", o.Reason)
	}
	switch o.Reason {
	case "ok":
		if o.Status != 0 || o.Code != "" || o.RequestID != "" || o.Wraps != "" {
			return errors.New("an ok outcome carries no status, code, request_id or wraps")
		}
		// What arrived is part of the outcome, so a transport that ran every guard
		// and then handed back a zero value cannot read as ok.
		if (o.ID != nil) != (v.Call == "get") {
			return errors.New("an ok outcome carries id exactly when the call is get")
		}
		if (o.IDs != nil) != (v.Call == "list") {
			return errors.New("an ok outcome carries ids exactly when the call is list")
		}
		if (o.HealthStatus != nil) != (v.Call == "health") {
			return errors.New("an ok outcome carries health_status exactly when the call is health")
		}
	case "api_error":
		if o.Status != v.Status || o.Code == "" {
			return errors.New("an api_error outcome carries the vector's own status and a code")
		}
		if o.Wraps != "" && o.Wraps != "response_too_large" {
			return fmt.Errorf("wraps %q is not a reason an api_error can carry", o.Wraps)
		}
		if o.ID != nil || o.IDs != nil || o.HealthStatus != nil {
			return errors.New("an api_error outcome carries no id, ids or health_status")
		}
	default:
		if !reflect.DeepEqual(o, responseOutcome{Reason: o.Reason}) {
			return fmt.Errorf("a %s outcome carries nothing but its reason", o.Reason)
		}
	}
	return nil
}

func (v requestVector) validate() error {
	allowedParams := map[string][]string{
		"list": {"application_id"}, "get": nil, "delete": nil, "create": nil, "update": nil,
		"resolve": {"application_id", "scope"},
	}
	allowed, ok := allowedParams[v.Call]
	if !ok {
		return fmt.Errorf("unknown call %q", v.Call)
	}
	for key := range v.Params {
		if !contains(allowed, key) {
			return fmt.Errorf("param %q does not apply to a %s", key, v.Call)
		}
	}
	if v.Options == nil {
		return errors.New(`options is required; write [] for none`)
	}
	for i, opt := range v.Options {
		if _, err := decodeVectorOption(opt); err != nil {
			return fmt.Errorf("option %d: %w", i, err)
		}
	}
	o := v.Outcome
	if !contains(requestReasons, o.Reason) {
		return fmt.Errorf("unknown request reason %q", o.Reason)
	}
	if o.Reason == "sent" {
		if o.Namespace != nil && len(o.Namespace) != 2 {
			return errors.New("a sent outcome's namespace is null or a [type, id] pair")
		}
		return nil
	}
	if o.Namespace != nil || o.ApplicationID != nil || o.IncludeGlobal {
		return fmt.Errorf("a %s outcome carries nothing but its reason", o.Reason)
	}
	return nil
}

// decodeVectorOption turns one option object -- exactly one key -- into the
// RequestOption it names. It is the whole of the option vocabulary: a key this
// switch does not know is an error rather than a skipped option.
func decodeVectorOption(opt map[string]json.RawMessage) (RequestOption, error) {
	if len(opt) != 1 {
		return nil, fmt.Errorf("an option object has exactly one key, this one has %d", len(opt))
	}
	var key string
	var raw json.RawMessage
	for k, v := range opt {
		key, raw = k, v
	}
	strict := func(into any) error {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		return dec.Decode(into)
	}
	switch key {
	case "namespace":
		var pair []string
		if err := strict(&pair); err != nil || len(pair) != 2 {
			return nil, fmt.Errorf("namespace is a [type, id] pair of strings")
		}
		return WithNamespace(pair[0], pair[1]), nil
	case "global_namespace", "include_global":
		var on bool
		if err := strict(&on); err != nil || !on {
			return nil, fmt.Errorf("%s takes the value true", key)
		}
		if key == "include_global" {
			return WithIncludeGlobal(), nil
		}
		return WithGlobalNamespace(), nil
	case "application":
		var id string
		if err := strict(&id); err != nil {
			return nil, fmt.Errorf("application is a string")
		}
		return WithApplication(id), nil
	default:
		return nil, fmt.Errorf("unknown option %q", key)
	}
}

func contains(list []string, s string) bool { return slices.Contains(list, s) }

// TestBehaviorVectors runs every vector on every surface it names.
func TestBehaviorVectors(t *testing.T) {
	vf := loadBehaviorVectors(t)
	for _, v := range vf.Response {
		for _, surface := range v.Surfaces {
			t.Run("response/"+v.Name+"/"+surface, func(t *testing.T) {
				got, err := runResponseVector(v, APIVersion(surface), nil)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, v.Outcome) {
					t.Errorf("%s\n  want %s\n  got  %s", v.Description, v.Outcome, got)
				}
			})
		}
	}
	for _, v := range vf.Request {
		for _, surface := range v.Surfaces {
			t.Run("request/"+v.Name+"/"+surface, func(t *testing.T) {
				got, err := runRequestVector(v, APIVersion(surface), nil)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, v.Outcome) {
					t.Errorf("%s\n  want %s\n  got  %s", v.Description, v.Outcome, got)
				}
			})
		}
	}
}

// vectorClient builds a client on the given surface against srv, then lets a
// test bend it. tweak is how TestBehaviorVectorsCatchADeviation simulates a
// behaviour change without editing the transport.
func vectorClient(srv *httptest.Server, surface APIVersion, tweak func(*Client)) (*Client, error) {
	c, err := New(Config{BaseURL: srv.URL, Token: "vector-token", TenantID: "vector-tenant", APIVersion: surface})
	if err != nil {
		return nil, err
	}
	if tweak != nil {
		tweak(c)
	}
	return c, nil
}

// vectorRoute is the one request each call makes, under the surface the vector
// names -- checked on every run, so a vector cannot pass on a request that went
// to another surface or another route than the one its call stands for. The
// health probe sits outside /api/<version> by design.
func vectorRoute(call string, surface APIVersion) string {
	api := "/api/" + string(surface)
	switch call {
	case "get":
		return "GET " + api + "/tags/t-1"
	case "update":
		return "PATCH " + api + "/tags/t-1"
	case "delete":
		return "DELETE " + api + "/tags/t-1"
	case "list":
		return "GET " + api + "/tags"
	case "create":
		return "POST " + api + "/tags"
	case "resolve":
		return "GET " + api + "/tag-resolution"
	case "health":
		return "GET /health/ready"
	}
	return "unknown call " + call
}

// runResponseVector answers the vector's call with its canned response and
// reports what the client made of it. An error return means the run itself went
// wrong -- the server was not asked exactly once, or the outcome could not be
// classified -- which is never a pass.
func runResponseVector(v responseVector, surface APIVersion, tweak func(*Client)) (responseOutcome, error) {
	body := []byte(*v.Body)
	if v.Nest != nil {
		nested := strings.Repeat("[", v.Nest.Depth) + strings.Repeat("]", v.Nest.Depth)
		body = []byte(strings.Replace(*v.Body, v.Nest.Placeholder, nested, 1))
	}
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(v.Status)
		if v.Status == http.StatusNoContent {
			return
		}
		_, _ = w.Write(body)
		// Padding is streamed from one 64 KiB chunk rather than built, so the
		// server side of a 32 MiB vector never holds the whole body; what is
		// measured is the client's read.
		writePadding(w, v.PadToBytes-len(body))
	}))
	defer srv.Close()

	c, err := vectorClient(srv, surface, tweak)
	if err != nil {
		return responseOutcome{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var got responseOutcome
	var callErr error
	switch v.Call {
	case "get":
		var tag *Tag
		tag, callErr = c.Tags.Get(ctx, "t-1")
		if callErr == nil {
			got = responseOutcome{Reason: "ok", ID: &tag.ID}
		}
	case "list":
		var page *List[Tag]
		page, callErr = c.Tags.List(ctx, nil)
		if callErr == nil {
			ids := make([]string, 0, len(page.Data))
			for _, row := range page.Data {
				ids = append(ids, row.ID)
			}
			got = responseOutcome{Reason: "ok", IDs: ids}
		}
	case "delete":
		callErr = c.Tags.Delete(ctx, "t-1")
		got = responseOutcome{Reason: "ok"}
	case "health":
		var h *HealthStatus
		h, callErr = c.Health.Ready(ctx)
		if callErr == nil {
			status := h.Status
			got = responseOutcome{Reason: "ok", HealthStatus: &status}
		}
	default:
		return responseOutcome{}, fmt.Errorf("unknown call %q", v.Call)
	}
	mu.Lock()
	routes := append([]string(nil), seen...)
	mu.Unlock()
	if want := vectorRoute(v.Call, surface); len(routes) != 1 || routes[0] != want {
		return responseOutcome{}, fmt.Errorf("the server was asked %q, want exactly %q once (err: %v)", routes, want, callErr)
	}
	if callErr == nil {
		return got, nil
	}
	return classifyResponseError(callErr)
}

func writePadding(w http.ResponseWriter, n int) {
	if n <= 0 {
		return
	}
	chunk := bytes.Repeat([]byte(" "), 64<<10)
	for n > 0 {
		k := min(n, len(chunk))
		if _, err := w.Write(chunk[:k]); err != nil {
			return
		}
		n -= k
	}
}

// classifyResponseError names the one reason err is. Exactly one: an error no
// reason recognises, or one that two recognise, is a failure of the binding
// rather than a pass, so a new error shape cannot be absorbed by the nearest
// existing reason.
func classifyResponseError(err error) (responseOutcome, error) {
	if errors.Is(err, ErrUnreachable) {
		return responseOutcome{}, fmt.Errorf("the request got no response, which no response vector describes: %w", err)
	}
	if apiErr, ok := AsAPIError(err); ok {
		out := responseOutcome{Reason: "api_error", Status: apiErr.StatusCode, Code: apiErr.Code, RequestID: apiErr.RequestID}
		if errors.Is(err, ErrResponseTooLarge) {
			out.Wraps = "response_too_large"
		}
		return out, nil
	}

	var matched []string
	if errors.Is(err, ErrResponseTooLarge) {
		matched = append(matched, "response_too_large")
	}
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		// encoding/json's own depth refusal, which is what bounds a response here:
		// a modern decoder stops at 10000 levels by itself, reporting the bracket
		// that crossed it ("invalid character '[' exceeded max depth").
		if strings.HasSuffix(syntaxErr.Error(), "exceeded max depth") {
			matched = append(matched, "depth_exceeded")
		} else {
			matched = append(matched, "malformed_json")
		}
	}
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		matched = append(matched, "type_mismatch")
	}
	for reason, phrases := range responseErrorPhrases {
		for _, phrase := range phrases {
			if strings.Contains(err.Error(), phrase) {
				matched = append(matched, reason)
				break
			}
		}
	}
	return pickOneReason(matched, err, func(r string) responseOutcome { return responseOutcome{Reason: r} })
}

func pickOneReason[T any](matched []string, err error, build func(string) T) (T, error) {
	var zero T
	sort.Strings(matched)
	switch len(matched) {
	case 1:
		return build(matched[0]), nil
	case 0:
		return zero, fmt.Errorf("no reason recognises this error, so a vector cannot describe it: %v", err)
	default:
		return zero, fmt.Errorf("reasons %v all recognise this error; each must have exactly one: %v", matched, err)
	}
}

// runRequestVector makes the vector's call and reports whether it was sent --
// and with what scoping -- or which refusal stopped it first.
func runRequestVector(v requestVector, surface APIVersion, tweak func(*Client)) (requestOutcome, error) {
	var mu sync.Mutex
	var seen []*http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch v.Call {
		case "list":
			_, _ = w.Write([]byte(`{"data":[],"pagination":{"limit":50,"offset":0,"count":0,"next":null}}`))
		case "delete":
			w.WriteHeader(http.StatusNoContent)
		case "resolve":
			_, _ = w.Write([]byte(`{"data":{"matched_type":"tag","matched_alias":null,"tag":{"id":"t-1","slug":"summer-sale"}}}`))
		default:
			_, _ = w.Write([]byte(`{"data":{"id":"t-1","slug":"summer-sale"}}`))
		}
	}))
	defer srv.Close()

	c, err := vectorClient(srv, surface, tweak)
	if err != nil {
		return requestOutcome{}, err
	}
	opts := make([]RequestOption, 0, len(v.Options))
	for i, raw := range v.Options {
		opt, err := decodeVectorOption(raw)
		if err != nil {
			return requestOutcome{}, fmt.Errorf("option %d: %w", i, err)
		}
		opts = append(opts, opt)
	}
	var appID *string
	if id, ok := v.Params["application_id"]; ok {
		appID = &id
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	var callErr error
	switch v.Call {
	case "list":
		var params *TagListParams
		if appID != nil {
			params = &TagListParams{ApplicationID: appID}
		}
		_, callErr = c.Tags.List(ctx, params, opts...)
	case "get":
		_, callErr = c.Tags.Get(ctx, "t-1", opts...)
	case "delete":
		callErr = c.Tags.Delete(ctx, "t-1", opts...)
	case "create":
		_, callErr = c.Tags.Create(ctx, TagCreate{Name: "Summer Sale", Slug: "summer-sale", Type: "campaign"}, opts...)
	case "update":
		_, callErr = c.Tags.Update(ctx, "t-1", TagUpdate{Name: Set("Summer Clearance")}, opts...)
	case "resolve":
		params := &TagResolveParams{ApplicationID: appID, Scope: ResolutionScope(v.Params["scope"])}
		_, callErr = c.Tags.Resolve(ctx, "summer-sale", params, opts...)
	default:
		return requestOutcome{}, fmt.Errorf("unknown call %q", v.Call)
	}

	mu.Lock()
	requests := append([]*http.Request(nil), seen...)
	mu.Unlock()
	switch {
	case callErr == nil && len(requests) == 1:
		if got, want := requests[0].Method+" "+requests[0].URL.Path, vectorRoute(v.Call, surface); got != want {
			return requestOutcome{}, fmt.Errorf("the request went to %q, want %q", got, want)
		}
		return observeSent(requests[0])
	case callErr != nil && len(requests) == 0:
		return classifyRefusal(callErr)
	default:
		return requestOutcome{}, fmt.Errorf("the call returned %v after %d request(s); a request vector is either sent once and succeeds, or refused with nothing sent", callErr, len(requests))
	}
}

func observeSent(r *http.Request) (requestOutcome, error) {
	out := requestOutcome{Reason: "sent"}
	nsType, nsID := r.Header.Values(namespaceTypeHeader), r.Header.Values(namespaceIDHeader)
	switch {
	case len(nsType) == 0 && len(nsID) == 0:
	case len(nsType) == 1 && len(nsID) == 1:
		out.Namespace = []string{nsType[0], nsID[0]}
	default:
		return requestOutcome{}, fmt.Errorf("sent namespace headers %q / %q, which is neither a pair nor none", nsType, nsID)
	}
	q := r.URL.Query()
	if values, ok := q[applicationIDParam]; ok {
		if len(values) != 1 {
			return requestOutcome{}, fmt.Errorf("sent %s %d times", applicationIDParam, len(values))
		}
		out.ApplicationID = &values[0]
	}
	if values, ok := q[includeGlobalParam]; ok {
		if len(values) != 1 || values[0] != "true" {
			return requestOutcome{}, fmt.Errorf("sent %s=%q, which is neither absent nor true", includeGlobalParam, values)
		}
		out.IncludeGlobal = true
	}
	return out, nil
}

// classifyRefusal names the one refusal err is. A refusal is never an API error
// and never unreachable -- nothing was sent -- and like a response error it must
// match exactly one reason.
func classifyRefusal(err error) (requestOutcome, error) {
	if _, ok := AsAPIError(err); ok || errors.Is(err, ErrUnreachable) {
		return requestOutcome{}, fmt.Errorf("a request nothing was sent for cannot fail with a response: %w", err)
	}
	var matched []string
	for reason, pattern := range requestRefusalPatterns {
		if pattern.MatchString(err.Error()) {
			matched = append(matched, reason)
		}
	}
	return pickOneReason(matched, err, func(r string) requestOutcome { return requestOutcome{Reason: r} })
}

// TestBehaviorVectorsBindEveryReason holds the binding's tables to its reason
// lists: a refusal reason with no pattern, or a pattern for no reason, is a
// binding that cannot be right whatever the vectors say.
func TestBehaviorVectorsBindEveryReason(t *testing.T) {
	for reason := range responseErrorPhrases {
		if !contains(responseReasons, reason) {
			t.Errorf("responseErrorPhrases recognises %q, which is not a response reason", reason)
		}
	}
	for _, reason := range requestReasons {
		if _, ok := requestRefusalPatterns[reason]; !ok && reason != "sent" {
			t.Errorf("request reason %q has no pattern in requestRefusalPatterns", reason)
		}
	}
	for reason := range requestRefusalPatterns {
		if !contains(requestReasons, reason) || reason == "sent" {
			t.Errorf("requestRefusalPatterns recognises %q, which is not a refusal reason", reason)
		}
	}
}

// TestBehaviorVectorsCatchADeviation is the proof #104 asks for: bend a covered
// behaviour without touching the vectors, and the vectors fail. Each case moves
// one thing the transport reads -- the response ceiling, the API version -- and
// names the vectors that must stop matching.
func TestBehaviorVectorsCatchADeviation(t *testing.T) {
	vf := loadBehaviorVectors(t)
	response := map[string]responseVector{}
	for _, v := range vf.Response {
		response[v.Name] = v
	}
	request := map[string]requestVector{}
	for _, v := range vf.Request {
		request[v.Name] = v
	}

	responseCases := []struct {
		vector string
		tweak  func(*Client)
	}{
		{"body-at-the-ceiling", func(c *Client) { c.maxResponseBytes-- }},
		{"body-over-the-ceiling", func(c *Client) { c.maxResponseBytes++ }},
		// The response is the same on either surface, so only the route check can
		// notice this one: it is what proves that check is load-bearing.
		{"single-resource", func(c *Client) { c.apiVersion = APIV2 }},
	}
	for _, tc := range responseCases {
		v, ok := response[tc.vector]
		if !ok {
			t.Fatalf("no response vector %q: this proof needs it", tc.vector)
		}
		// A run error fails TestBehaviorVectors too, so it counts as noticed.
		got, err := runResponseVector(v, APIVersion(v.Surfaces[0]), tc.tweak)
		if err == nil && reflect.DeepEqual(got, v.Outcome) {
			t.Errorf("%s still matched %s with the client bent: the vectors would not notice the change", tc.vector, got)
		}
	}

	requestCases := []struct {
		vector string
		tweak  func(*Client)
	}{
		{"v2-namespaced-read", func(c *Client) { c.apiVersion = APIV1 }},
		{"v1-include-global", func(c *Client) { c.apiVersion = APIV2 }},
		// Sent on either surface with the same scoping: only the route check sees
		// the request move.
		{"v1-plain-read", func(c *Client) { c.apiVersion = APIV2 }},
	}
	for _, tc := range requestCases {
		v, ok := request[tc.vector]
		if !ok {
			t.Fatalf("no request vector %q: this proof needs it", tc.vector)
		}
		// A run error is a failure in TestBehaviorVectors too, so it counts as
		// noticed: moving the surface under a sent vector shows up as the wrong route.
		got, err := runRequestVector(v, APIVersion(v.Surfaces[0]), tc.tweak)
		if err == nil && reflect.DeepEqual(got, v.Outcome) {
			t.Errorf("%s still matched %s on the other surface: the vectors would not notice the change", tc.vector, got)
		}
	}
}

// TestBehaviorVectorClassifiersFailClosed holds the classifiers to the rule that
// makes the deviation proof meaningful: an error they do not recognise, or one
// they recognise twice, is an error -- never the nearest reason.
func TestBehaviorVectorClassifiersFailClosed(t *testing.T) {
	if _, err := classifyResponseError(errors.New("octonomy: something no vector describes")); err == nil {
		t.Error("an unrecognised response error was classified")
	}
	// The message matches a reason, so only the unreachable check stands between
	// this and a classification.
	if _, err := classifyResponseError(fmt.Errorf(`%w: list response has no "pagination" block`, ErrUnreachable)); err == nil {
		t.Error("an unreachable request was classified as a response")
	}
	both := errors.New(`octonomy: expected 204 No Content, and list response has no "pagination" block`)
	if _, err := classifyResponseError(both); err == nil {
		t.Error("an error two response reasons recognise was classified")
	}
	if _, err := classifyRefusal(errors.New("octonomy: something no vector describes")); err == nil {
		t.Error("an unrecognised refusal was classified")
	}
	twice := errors.New("octonomy: WithNamespace requires the v2 API surface, and WithIncludeGlobal applies to reads only")
	if _, err := classifyRefusal(twice); err == nil {
		t.Error("a refusal two reasons recognise was classified")
	}
	// The message matches a refusal pattern, so only the API-error check stands
	// between this and a classification: without it the case would pass.
	apiErr := &APIError{StatusCode: 400, Code: CodeValidation, Message: "WithNamespace requires the v2 API surface"}
	if _, err := classifyRefusal(apiErr); err == nil {
		t.Error("an API error was classified as a refusal, although a refusal sends nothing")
	}
	if _, err := classifyRefusal(fmt.Errorf("%w: WithNamespace requires the v2 API surface", ErrUnreachable)); err == nil {
		t.Error("an unreachable request was classified as a refusal")
	}
}

// TestBehaviorVectorsReadSentRequestsStrictly holds observeSent to the reading
// the README specifies, because a lenient one hides a transport that sends the
// scoping wrong: include_global=1, half a namespace pair, a parameter twice.
func TestBehaviorVectorsReadSentRequestsStrictly(t *testing.T) {
	build := func(query string, headers map[string]string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/api/v2/tags?"+query, nil)
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		return r
	}
	good, err := observeSent(build("application_id=storefront&include_global=true",
		map[string]string{namespaceTypeHeader: "merchant", namespaceIDHeader: "m-1"}))
	want := requestOutcome{Reason: "sent", Namespace: []string{"merchant", "m-1"}, ApplicationID: strPtrForVectors("storefront"), IncludeGlobal: true}
	if err != nil || !reflect.DeepEqual(good, want) {
		t.Fatalf("a well-formed request read as %s, %v; want %s", good, err, want)
	}
	for name, r := range map[string]*http.Request{
		"include_global=1":            build("include_global=1", nil),
		"include_global twice":        build("include_global=true&include_global=true", nil),
		"application_id twice":        build("application_id=a&application_id=a", nil),
		"a namespace type with no id": build("", map[string]string{namespaceTypeHeader: "merchant"}),
		"a namespace id with no type": build("", map[string]string{namespaceIDHeader: "m-1"}),
	} {
		if got, err := observeSent(r); err == nil {
			t.Errorf("%s was read as %s", name, got)
		}
	}
}

func strPtrForVectors(s string) *string { return &s }

// TestBehaviorVectorsFileIsStrict proves the validation the real file passes is
// not vacuous: each malformed file below is one mistake an editor could make,
// and each must be refused.
func TestBehaviorVectorsFileIsStrict(t *testing.T) {
	raw, err := os.ReadFile(filepath.FromSlash(behaviorVectorsFile))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeBehaviorVectors(raw); err != nil {
		t.Fatalf("the real file must decode: %v", err)
	}
	// An empty list is awkward to write as one edit to the real file, so it is
	// built: a file with no request vectors at all must be refused.
	vf, err := decodeBehaviorVectors(raw)
	if err != nil {
		t.Fatal(err)
	}
	// The unused-reason check also refuses such a file, so the case asserts the
	// complaint that is about the empty list, or it would pass without it.
	if err := (&behaviorVectorFile{Version: 1, Response: vf.Response}).validate(); err == nil ||
		!strings.Contains(err.Error(), "must be non-empty") {
		t.Errorf("a file with no request vectors was not refused for being empty: %v", err)
	}

	// Retiring a reason from every vector that names it leaves the binding
	// recognising a reason nothing exercises, which must be refused. Every
	// occurrence, since a reason may have several vectors.
	retired := bytes.ReplaceAll(raw, []byte(`"outcome": {"reason": "depth_exceeded"}`), []byte(`"outcome": {"reason": "malformed_json"}`))
	if bytes.Equal(retired, raw) {
		t.Fatal("no vector names depth_exceeded any more; update the retired-reason case")
	}
	if _, err := decodeBehaviorVectors(retired); err == nil || !strings.Contains(err.Error(), "no vector names it") {
		t.Errorf("a file in which no vector names depth_exceeded was not refused for it: %v", err)
	}

	mutations := []struct{ name, from, to string }{
		{"a misspelled field", `"outcome": {"reason": "envelope_missing"}`, `"outcome": {"reason": "envelope_missing", "resaon": "x"}`},
		// pagination_missing has two vectors, so the other still names it and only
		// the unknown-reason check can refuse this one.
		{"an unknown reason", `"outcome": {"reason": "pagination_missing"}`, `"outcome": {"reason": "pagination_mising"}`},
		{"an empty surface list", `"surfaces": ["v1"],`, `"surfaces": [],`},
		{"an option set to false", `{"include_global": true}`, `{"include_global": false}`},
		{"padding and nesting combined", `"nest": {"placeholder": "@NEST@", "depth": 9997},`, `"nest": {"placeholder": "@NEST@", "depth": 9997}, "pad_to_bytes": 40000,`},
		{"a padded 204", `"status": 204,`, `"status": 204, "pad_to_bytes": 10,`},
		{"a name used twice", `"name": "single-resource-empty-body",`, `"name": "single-resource",`},
		{"trailing data after the object", "  ]\n}", "  ]\n}\n{}"},
		{"a health ok without its word", `"outcome": {"reason": "ok", "health_status": "ok"}`, `"outcome": {"reason": "ok"}`},
		{"an api_error status that is not the response's", `"code": "not_found", "request_id": "req_4F1c-Xy"}`, `"code": "not_found", "request_id": "req_4F1c-Xy", "status": 403}`},
		{"a 204 with a body", "\"status\": 204,\n      \"body\": \"\",", "\"status\": 204,\n      \"body\": \"x\","},
		{"a body with no body field", "\"status\": 204,\n      \"body\": \"\",", "\"status\": 204,"},
		{"padding shorter than the body", `"pad_to_bytes": 33554432,`, `"pad_to_bytes": 3,`},
		{"half a sent namespace", `"outcome": {"reason": "sent", "namespace": ["merchant", "m-1"]`, `"outcome": {"reason": "sent", "namespace": ["merchant"]`},
		{"a nest of depth 0", `"depth": 9997}`, `"depth": 0}`},
		{"a surface listed twice", `"surfaces": ["v1"],`, `"surfaces": ["v1", "v1"],`},
		{"a request with no options field", "\"options\": [],\n      ", ""},
		{"an unknown surface", `"surfaces": ["v1"],`, `"surfaces": ["v3"],`},
		{"an unknown call", `"call": "delete",`, `"call": "remove",`},
		{"an unknown option", `{"global_namespace": true}`, `{"globalnamespace": true}`},
		{"a param the call does not take", `"params": {"scope": "merchant"},`, `"params": {"scope": "merchant", "slug": "x"},`},
		{"ids missing from a list", `"outcome": {"reason": "ok", "ids": []}`, `"outcome": {"reason": "ok"}`},
		{"id missing from a get", `"outcome": {"reason": "ok", "id": "t-1"}`, `"outcome": {"reason": "ok"}`},
		{"the retired rows field", `"outcome": {"reason": "ok", "ids": []}`, `"outcome": {"reason": "ok", "ids": [], "rows": 0}`},
	}
	for _, m := range mutations {
		t.Run(m.name, func(t *testing.T) {
			if !bytes.Contains(raw, []byte(m.from)) {
				t.Fatalf("the real file no longer contains %q; update this case", m.from)
			}
			mutated := bytes.Replace(raw, []byte(m.from), []byte(m.to), 1)
			if _, err := decodeBehaviorVectors(mutated); err == nil {
				t.Errorf("a file with %s was accepted", m.name)
			}
		})
	}
}
