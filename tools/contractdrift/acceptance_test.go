package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	octonomy "github.com/octoverse-id/octonomy-go"
)

// #98's acceptance, end to end: `make contract-check` FAILS on a removed query
// parameter, a dropped schema field, a retyped property and a rerouted method,
// one fixture per case.
//
// This file is this line's own (main.pin marks it `own`). main's tests at
// 5e40964 prove each of those findings in process, and they run here too -- but in
// process, the SDK under test is the one compiled into the test binary, so those
// tests synthesize the client-side half (TestUnsentQueryParameterIsReported deletes a key from an
// observation; TestWrongRouteIsReported rewrites one). That proves the CHECK. It
// does not prove the GATE: that the Makefile target builds the tool against the
// tree in front of it, that the replace in go.mod points at that tree, that the
// binary's exit status survives make, and that a defect in the client's own
// source -- not a synthesized observation -- reaches the report.
//
// So each case here stages a copy of the repository, breaks ONE thing in it --
// the SDK's source for the three client-side cases, the vendored specs for the
// retype -- and runs the real target in the copy. A control run of the unbroken
// copy has to pass first, or every failure below could be a staging fault.
//
// Each run builds the gate, so this is the slowest test in the module (a few
// seconds a case with a warm build cache). It does not skip: `make` missing is a
// failure, since the target IS what is under test.

// acceptanceCase is one fixture: what it breaks, and the finding that names it.
type acceptanceCase struct {
	name    string
	breakIt func(t *testing.T, repo string)
	// want are substrings of the report the defect must produce. The exit status
	// alone would pass a gate that fails for the wrong reason, and so would a
	// generic phrase every decode failure shares -- so each case also names what
	// it broke.
	want []string
}

func TestMakeContractCheckFailsOnEachAcceptanceFixture(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Fatal("make is not installed, and `make contract-check` is what this test exercises")
	}

	// The control. A staging fault -- a file the copy lacks, a replace that
	// resolves elsewhere -- fails every case below for a reason none of them is
	// about, so the unbroken copy has to come out clean first.
	if out, err := makeContractCheck(t, stageMakeRepo(t)); err != nil {
		t.Fatalf("the unbroken staged copy failed `make contract-check`, so the cases below would prove nothing:\n%s", out)
	} else if !strings.Contains(out, "No drift.") {
		t.Fatalf("the unbroken staged copy passed without saying `No drift.`:\n%s", out)
	}

	cases := []acceptanceCase{
		{
			// The client stops putting a documented parameter on the wire. Not a
			// synthesized observation: the line that sets it is gone from tags.go.
			name: "a removed query parameter",
			breakIt: func(t *testing.T, repo string) {
				edit(t, filepath.Join(repo, "tags.go"), "func (p *TagListParams) query() url.Values {",
					`q.Set("vocabulary_id", *p.VocabularyID)`, `_ = p.VocabularyID`)
			},
			want: []string{"`get /tags` documents the query parameter `vocabulary_id` and the client did not send it"},
		},
		{
			// The model stops decoding a property the schema documents. `json:"-"`
			// rather than a deleted field, so nothing else in the package stops
			// compiling and the failure is the gate's, not the build's.
			name: "a dropped schema field",
			breakIt: func(t *testing.T, repo string) {
				edit(t, filepath.Join(repo, "tags.go"), "type Tag struct {",
					"`json:\"usage_count\"`", "`json:\"-\"`")
			},
			want: []string{"schema `Tag` documents `usage_count` and it does not survive decoding"},
		},
		{
			// The contract retypes a property the model still has a field for. The
			// name survives, so a name comparison sees nothing; the stub now sends a
			// string into an int, and the decode is what fails.
			name: "a retyped property",
			breakIt: func(t *testing.T, repo string) {
				for _, spec := range []string{"openapi.yaml", "openapi-v2.yaml"} {
					edit(t, filepath.Join(repo, "docs", spec), "\n    Tag:\n",
						"        usage_count:\n          type: integer\n",
						"        usage_count:\n          type: string\n")
				}
			},
			want: []string{"could not handle a response built from the vendored schema", "usage_count"},
		},
		{
			// The method the inventory names requests a different route.
			name: "a rerouted method",
			breakIt: func(t *testing.T, repo string) {
				edit(t, filepath.Join(repo, "tags.go"), "func (s *TagService) Get(",
					`"/tags/"+url.PathEscape(id)`, `"/tag/"+url.PathEscape(id)`)
			},
			want: []string{"`TagService.Get` requests `GET /tag/{tag_id}`"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := stageMakeRepo(t)
			tc.breakIt(t, repo)
			out, err := makeContractCheck(t, repo)
			if err == nil {
				t.Fatalf("`make contract-check` exited 0 on %s:\n%s", tc.name, out)
			}
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Fatalf("`make contract-check` failed on %s without the finding that names it (%q):\n%s", tc.name, want, out)
				}
			}
		})
	}
}

// makeContractCheck runs the real target in a staged repository.
func makeContractCheck(t *testing.T, repo string) (string, error) {
	t.Helper()
	cmd := exec.Command("make", "--no-print-directory", "-C", repo, "contract-check")
	// The staged copy is not a git checkout and has no go.work; GOFLAGS from the
	// caller's environment would change how the copy builds, so it is cleared.
	cmd.Env = append(os.Environ(), "GOFLAGS=", "GOWORK=off")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// stageMakeRepo copies what `make contract-check` reads into a temp directory:
// the SDK's sources and go.mod, the Makefile, the four documents the gate loads,
// and the gate's own module. It is stageRepo plus the parts a BUILD needs, which
// stageRepo leaves out because the in-process tests compile the SDK in rather than
// building it.
func stageMakeRepo(t *testing.T) string {
	t.Helper()
	dir := stageRepo(t)
	copyFile(t, filepath.Join(repoRoot, "go.mod"), filepath.Join(dir, "go.mod"))
	copyFile(t, filepath.Join(repoRoot, "Makefile"), filepath.Join(dir, "Makefile"))

	gate := filepath.Join(dir, "tools", "contractdrift")
	if err := os.MkdirAll(gate, 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	copied := 0
	for _, entry := range entries {
		name := entry.Name()
		switch {
		case entry.IsDir(), strings.HasSuffix(name, "_test.go"):
			continue
		case strings.HasSuffix(name, ".go"), name == "go.mod", name == "go.sum":
			copyFile(t, name, filepath.Join(gate, name))
			copied++
		}
	}
	if copied < 3 {
		t.Fatalf("staged only %d of the gate's own files", copied)
	}
	return dir
}

// recordedGaps are the unsent_inputs rows that record a GAP -- a documented input
// this client has no field to send. Every other unsent_inputs row is a decision
// with a carried_in, or a route that names the input as a path parameter, saying
// where it goes instead.
//
// They need a check of their own because a gap row suppresses exactly the finding
// that would notice the gap closing badly. Port the missing field and forget the
// driver, and with the row in place the gate reports nothing at all: the row says
// "documented and unsent" is expected, so a field that exists and is never driven
// reads the same as one that does not exist. So a row here has to be TRUE -- the
// field really is missing -- and the moment it arrives this test fails, the row
// comes out, and the gate takes over: documented and unsent until the driver
// sets it.
//
// EMPTY, and kept. Its only entries were `q` and `slug` on GET /vocabularies,
// recorded from #98 until #118 ported VocabularyListParams.Query and .Slug, when
// this test failed exactly as described above. The table stays because the empty
// case is still a rule: recordedGapProblems refuses every unsent_inputs row that
// is a gap -- in the query, the body or a header -- unless this table names it,
// so a future gap row cannot be added without naming the field whose absence it
// asserts.
//
// HOW A GAP IS HELD -- twice, and neither is a proof.
//
//  1. To the field's absence, as AGENTS.md has it: the entry names the struct
//     that would carry the input (the *Params struct for a query input, the
//     write struct for a body one, Config for a header) and the Go field it
//     would be, and the test
//     fails the moment that field exists. That trusts the entry's naming: a
//     misspelled field, or the wrong struct, is absent forever.
//  2. So a second witness does not read the naming at all. observeSDK calls the
//     SDK method contract-coverage.yaml names for the operation, on a client
//     built from a filled Config, with every fixed parameter filled as fully as
//     reflection can fill it, through a transport that records the request, and
//     the test fails the moment that request carries the input -- whatever the
//     field is called, and however encoding/json or a params struct's query()
//     spells it.
//
// What neither sees, and the reader of a row's reason still has to: the call
// passes no RequestOption, since options cannot be enumerated by reflection, so
// an input an option carries -- application_id through WithApplication,
// include_global through WithIncludeGlobal -- passes both. Such an input is not a
// gap at all, and its driver should send it. And one fully filled call cannot see
// a field the client sends only while another is unset. The option blind spot
// matters most for headers: every header a caller controls comes from a Config
// field (Token as Authorization, TenantID, ActorID, UserAgent) or a
// RequestOption (WithNamespace, WithRequestID, WithActor) -- transport.go sets no
// other -- so a header gap is held only as far as the header would arrive
// through Config.
//
// An entry is written {op, in, name, reflect.TypeOf(T{}), "Field"}, where T is
// the struct the first witness reads: the params struct for a query input
// (octonomy.FooListParams, not the pointer the method takes), the write struct
// for a body one (octonomy.FooCreate, FooUpdate, ...), and octonomy.Config for a
// header. Naming another struct passes the first witness forever.
var recordedGaps []recordedGap

// recordedGap is one gap row and the field whose absence it asserts: params is
// the struct that would carry the input, and field the Go name it would have.
type recordedGap struct {
	op, in, name string
	params       reflect.Type
	field        string
}

func TestRecordedGapsStillHaveNoField(t *testing.T) {
	cov, err := LoadCoverage(filepath.Join(repoRoot, "docs", "contract-coverage.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, problem := range recordedGapProblems(cov.UnsentInputs, recordedGaps, coverageObserver(cov)) {
		t.Error(problem)
	}
}

// sentRequest is what one SDK call put on the wire: its method, and the names in
// its query string, its JSON object body and its headers (canonical form).
type sentRequest struct {
	method  string
	query   map[string]bool
	body    map[string]bool
	headers map[string]bool
}

// observer reports the request the client sends for an operation, "method /path".
type observer func(op string) (sentRequest, error)

// recordedGapProblems holds the unsent_inputs rows to the gaps table: every row
// with nowhere else to carry its input is a gap the table names, every entry in
// the table is still a row, and each gap is held both ways recordedGaps
// describes -- its field still absent, and a fully filled call still not sending
// its input.
//
// A gap is decided by what the row says, never by where the input is documented.
// Until #118 only a query row was held, so an uncarried body or header row -- a
// write field dropped from its driver, say -- suppressed the gate's finding
// unheld; a header name is compared in its canonical form.
func recordedGapProblems(rows []UnsentInput, gaps []recordedGap, observe observer) []string {
	var problems []string
	listed := map[string]bool{}
	for _, gap := range gaps {
		listed[gap.op+" "+gap.in+" "+gap.name] = true
	}
	present := map[string]bool{}
	for _, row := range rows {
		key := row.Method + " " + row.Path + " " + row.In + " " + row.Name
		present[key] = true
		// A row with no replacement path is a gap, and a gap has to be held to
		// the client. The resource_type / resource_id rows name the route instead.
		if row.CarriedIn != "" || routeCarries(row) || listed[key] {
			continue
		}
		problems = append(problems, fmt.Sprintf("unsent_inputs records `%s` with nowhere else to carry it -- a gap -- and recordedGaps does not name it, so nothing checks that the client still cannot send it", key))
	}
	for _, gap := range gaps {
		key := gap.op + " " + gap.in + " " + gap.name
		if !present[key] {
			problems = append(problems, fmt.Sprintf("recordedGaps names `%s`, which unsent_inputs no longer lists -- drop the entry", key))
			continue
		}
		if gap.in != "query" && gap.in != "body" && gap.in != "header" {
			problems = append(problems, fmt.Sprintf("recordedGaps names `%s`, in no location the request has", key))
			continue
		}
		if gap.params == nil || gap.field == "" {
			problems = append(problems, fmt.Sprintf("recordedGaps names `%s` with no struct and field, so nothing holds the row to its field's absence", key))
			continue
		}
		if _, has := gap.params.FieldByName(gap.field); has {
			problems = append(problems, fmt.Sprintf("%s.%s exists, so the unsent_inputs row for `%s` records a gap that is closed -- drop the row, set the field in drivers.go, and the gate holds it from there",
				gap.params.Name(), gap.field, key))
			continue
		}
		sent, err := observe(gap.op)
		if err != nil {
			problems = append(problems, fmt.Sprintf("recordedGaps names `%s`, and the client's request could not be observed, so nothing holds it: %v", key, err))
			continue
		}
		if method, _, _ := strings.Cut(gap.op, " "); !strings.EqualFold(sent.method, method) {
			problems = append(problems, fmt.Sprintf("recordedGaps names `%s`, and the method behind it sent %s, not %s", key, sent.method, strings.ToUpper(method)))
			continue
		}
		carried, name := sent.query, gap.name
		switch gap.in {
		case "body":
			carried = sent.body
		case "header":
			carried, name = sent.headers, http.CanonicalHeaderKey(gap.name)
		}
		if carried[name] {
			problems = append(problems, fmt.Sprintf("the client sends `%s` in the %s of `%s` once every field it takes is set, although %s.%s does not exist -- the entry names the wrong struct or field, and the gap is closed: drop the row, set the field in drivers.go, and the gate holds it from there",
				gap.name, gap.in, gap.op, gap.params.Name(), gap.field))
		}
	}
	return problems
}

// routeCarries reports whether row's input is a path parameter of its own route:
// the contract documents it elsewhere as well, and the path the client requests
// already carries it -- the gate holds the route to the contract's, so the row
// cannot claim a route the client does not use. resource_type and resource_id on
// the resource-tag replace are the rows this describes.
func routeCarries(row UnsentInput) bool {
	return strings.Contains(row.Path, "{"+row.Name+"}")
}

// coverageObserver observes each operation through the SDK method the coverage
// file names for it.
func coverageObserver(cov *Coverage) observer {
	symbols := map[string]string{}
	for _, op := range cov.Operations {
		symbols[op.Method+" "+op.Path] = op.SDK
	}
	return func(op string) (sentRequest, error) {
		symbol, ok := symbols[op]
		if !ok || symbol == "" {
			return sentRequest{}, fmt.Errorf("contract-coverage.yaml names no SDK method for `%s`", op)
		}
		return observeSDK(symbol)
	}
}

// observeSDK calls the SDK method "Receiver.Method" -- a method of Client, or of
// the service an exported Client field holds -- on a client built from a filled
// Config, whose transport records the request instead of sending it.
func observeSDK(symbol string) (sentRequest, error) {
	rec := &gapRecorder{}
	// The Config is filled like every parameter, so an input a Config field
	// carries is observed too; only what decides where and how the request goes
	// is set by hand.
	var cfg octonomy.Config
	fill(reflect.ValueOf(&cfg).Elem(), 0)
	cfg.BaseURL = "http://contractdrift.invalid"
	cfg.APIVersion = octonomy.APIV2
	cfg.HTTPClient = &http.Client{Transport: rec}
	client, err := octonomy.New(cfg)
	if err != nil {
		return sentRequest{}, err
	}
	recv, name, ok := strings.Cut(symbol, ".")
	if !ok {
		return sentRequest{}, fmt.Errorf("%q is not Receiver.Method", symbol)
	}
	var method reflect.Value
	if recv == "Client" {
		method = reflect.ValueOf(client).MethodByName(name)
	} else {
		value := reflect.ValueOf(client).Elem()
		for i := 0; i < value.NumField(); i++ {
			field := value.Type().Field(i)
			if field.IsExported() && field.Type.Kind() == reflect.Ptr && field.Type.Elem().Name() == recv {
				method = value.Field(i).MethodByName(name)
				break
			}
		}
	}
	if !method.IsValid() {
		return sentRequest{}, fmt.Errorf("no method %s reachable from octonomy.Client", symbol)
	}
	return observeCall(method, rec)
}

// observeCall calls fn with every fixed parameter filled and no variadic ones,
// and returns the one request rec saw. Anything but exactly one request is an
// error: a call refused before it reached the transport proves nothing about
// what the client can send.
func observeCall(fn reflect.Value, rec *gapRecorder) (sent sentRequest, err error) {
	// A panic in fill or in the call is a gap nothing could observe, reported as
	// one rather than taking the test binary down with it.
	defer func() {
		if r := recover(); r != nil {
			sent, err = sentRequest{}, fmt.Errorf("the call panicked: %v", r)
		}
	}()
	sig := fn.Type()
	fixed := sig.NumIn()
	if sig.IsVariadic() {
		fixed--
	}
	contextType := reflect.TypeOf((*context.Context)(nil)).Elem()
	args := make([]reflect.Value, fixed)
	for i := range args {
		arg := reflect.New(sig.In(i)).Elem()
		if sig.In(i) == contextType {
			arg.Set(reflect.ValueOf(context.Background()))
		} else {
			fill(arg, 0)
		}
		args[i] = arg
	}
	fn.Call(args)
	if len(rec.sent) != 1 {
		return sentRequest{}, fmt.Errorf("the call sent %d requests, not one (errors: %v)", len(rec.sent), rec.errs)
	}
	return rec.sent[0], nil
}

// fill sets v, and everything reachable from it that the program could set, to
// a non-zero value: a pointer to a filled value, a one-element slice or map,
// "x", true, 1. It is how a caller who set every field would build the value,
// which is the question a gap row asks. An exported field promoted through an
// unexported embedded struct is filled too, since reflect allows it and
// encoding/json encodes it; an unexported field is not, and neither encodes.
func fill(v reflect.Value, depth int) {
	if depth > 8 {
		return
	}
	if v.Kind() == reflect.Struct {
		for i := 0; i < v.NumField(); i++ {
			fill(v.Field(i), depth+1)
		}
		return
	}
	if !v.CanSet() {
		return
	}
	switch v.Kind() {
	case reflect.Ptr:
		elem := reflect.New(v.Type().Elem())
		fill(elem.Elem(), depth+1)
		v.Set(elem)
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(1)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(1)
	case reflect.Slice:
		slice := reflect.MakeSlice(v.Type(), 1, 1)
		fill(slice.Index(0), depth+1)
		v.Set(slice)
	case reflect.Array:
		for i := 0; i < v.Len(); i++ {
			fill(v.Index(i), depth+1)
		}
	case reflect.Map:
		key := reflect.New(v.Type().Key()).Elem()
		fill(key, depth+1)
		elem := reflect.New(v.Type().Elem()).Elem()
		fill(elem, depth+1)
		m := reflect.MakeMap(v.Type())
		m.SetMapIndex(key, elem)
		v.Set(m)
	case reflect.Interface:
		if v.NumMethod() == 0 {
			v.Set(reflect.ValueOf("x"))
		}
	}
}

// gapRecorder is an http.RoundTripper that records each request and sends none.
type gapRecorder struct {
	sent []sentRequest
	errs []error
}

// RoundTrip records req and refuses it, so the client sees a transport error.
func (r *gapRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	r.record(req)
	return nil, errors.New("recorded, not sent")
}

func (r *gapRecorder) record(req *http.Request) {
	got := sentRequest{method: req.Method, query: map[string]bool{}, body: map[string]bool{}, headers: map[string]bool{}}
	for name := range req.Header {
		got.headers[name] = true
	}
	for name := range req.URL.Query() {
		got.query[name] = true
	}
	if req.Body != nil {
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			r.errs = append(r.errs, err)
		}
		if len(raw) > 0 {
			var object map[string]json.RawMessage
			if err := json.Unmarshal(raw, &object); err != nil {
				r.errs = append(r.errs, fmt.Errorf("the body is not a JSON object: %w", err))
			}
			for name := range object {
				got.body[name] = true
			}
		}
	}
	r.sent = append(r.sent, got)
}

// TestRecordedGapProblemsRefusesEachForm: the check above against a gap that has
// closed, a gap row nobody listed, and a listed gap with no row -- in the query,
// the body and a header -- so passing on the real file is not its only evidence.
func TestRecordedGapProblemsRefusesEachForm(t *testing.T) {
	sends := func(method string, query, body []string, headers ...string) observer {
		return func(string) (sentRequest, error) {
			got := sentRequest{method: method, query: map[string]bool{}, body: map[string]bool{}, headers: map[string]bool{}}
			for _, name := range query {
				got.query[name] = true
			}
			for _, name := range body {
				got.body[name] = true
			}
			for _, name := range headers {
				got.headers[http.CanonicalHeaderKey(name)] = true
			}
			return got, nil
		}
	}
	openGet := sends("GET", []string{"limit"}, nil)
	closedGet := sends("GET", []string{"limit", "q"}, nil)
	openPost := sends("POST", nil, []string{"name"})
	closedPost := sends("POST", nil, []string{"name", "description"})
	unobservable := func(string) (sentRequest, error) { return sentRequest{}, errors.New("no request") }

	row := UnsentInput{Path: "/things", Method: "get", In: "query", Name: "q", Reason: "r"}
	type openParams struct{ Other *string }
	type closedParams struct {
		Other *string
		Query *string
	}
	type openWrite struct {
		Name *string `json:"name,omitempty"`
	}
	type closedWrite struct {
		Name        *string `json:"name,omitempty"`
		Description *string `json:"description,omitempty"`
	}
	gap := recordedGap{"get /things", "query", "q", reflect.TypeOf(openParams{}), "Query"}
	body := UnsentInput{Path: "/things", Method: "post", In: "body", Name: "description", Reason: "r"}
	bodyGap := recordedGap{"post /things", "body", "description", reflect.TypeOf(openWrite{}), "Description"}
	header := UnsentInput{Path: "/things", Method: "get", In: "header", Name: "X-Thing-ID", Reason: "r"}
	type openConfig struct{ Token string }
	type closedConfig struct {
		Token string
		Thing string
	}
	headerGap := recordedGap{"get /things", "header", "X-Thing-ID", reflect.TypeOf(openConfig{}), "Thing"}
	closedHeaderGap := headerGap
	closedHeaderGap.params = reflect.TypeOf(closedConfig{})
	closedGap, closedBodyGap, noField := gap, bodyGap, gap
	closedGap.params = reflect.TypeOf(closedParams{})
	closedBodyGap.params = reflect.TypeOf(closedWrite{})
	noField.params = nil

	if got := recordedGapProblems([]UnsentInput{row}, []recordedGap{gap}, openGet); len(got) != 0 {
		t.Fatalf("a query gap the client cannot send is fine, got %v", got)
	}
	if got := recordedGapProblems([]UnsentInput{body}, []recordedGap{bodyGap}, openPost); len(got) != 0 {
		t.Fatalf("a body gap the client cannot send is fine, got %v", got)
	}
	cases := []struct {
		name    string
		rows    []UnsentInput
		gaps    []recordedGap
		observe observer
		want    string
	}{
		{"the named query field arrived", []UnsentInput{row}, []recordedGap{closedGap}, openGet, "closedParams.Query exists"},
		{"the named body field arrived", []UnsentInput{body}, []recordedGap{closedBodyGap}, openPost, "closedWrite.Description exists"},
		{"a gap listed with no struct", []UnsentInput{row}, []recordedGap{noField}, openGet, "with no struct and field"},
		{"the client sends the query input under a field the entry does not name", []UnsentInput{row}, []recordedGap{gap}, closedGet, "the client sends `q` in the query"},
		{"the client sends the body input under a field the entry does not name", []UnsentInput{body}, []recordedGap{bodyGap}, closedPost, "names the wrong struct or field"},
		{"sent in the other location only", []UnsentInput{row}, []recordedGap{gap}, sends("GET", nil, []string{"q"}), ""},
		{"a request for another method", []UnsentInput{row}, []recordedGap{gap}, openPost, "sent POST, not GET"},
		{"a gap row nobody listed", []UnsentInput{row}, nil, openGet, "recordedGaps does not name it"},
		{"a listed gap with no row", nil, []recordedGap{gap}, openGet, "no longer lists"},
		{"a body gap row nobody listed", []UnsentInput{body}, nil, openPost, "recordedGaps does not name it"},
		{"a header gap row nobody listed", []UnsentInput{header}, nil, openGet, "recordedGaps does not name it"},
		{"a header gap the client cannot send", []UnsentInput{header}, []recordedGap{headerGap}, openGet, ""},
		{"the named header field arrived", []UnsentInput{header}, []recordedGap{closedHeaderGap}, openGet, "closedConfig.Thing exists"},
		{"the client sends the header, in any case", []UnsentInput{header}, []recordedGap{headerGap}, sends("GET", nil, nil, "x-thing-id"), "the client sends `X-Thing-ID` in the header"},
		{"a gap in no location", []UnsentInput{{Path: "/things", Method: "get", In: "cookie", Name: "c", Reason: "r"}}, []recordedGap{{"get /things", "cookie", "c", reflect.TypeOf(openConfig{}), "C"}}, openGet, "in no location the request has"},
		{"an unobservable gap", []UnsentInput{row}, []recordedGap{gap}, unobservable, "could not be observed"},
	}
	for _, tc := range cases {
		got := strings.Join(recordedGapProblems(tc.rows, tc.gaps, tc.observe), "\n")
		if tc.want == "" {
			if got != "" {
				t.Errorf("%s: want no problem, got %q", tc.name, got)
			}
			continue
		}
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s: want a problem containing %q, got %q", tc.name, tc.want, got)
		}
	}
	// A row that names where the input travels instead is a decision, not a gap,
	// in every location.
	for _, r := range []UnsentInput{row, body, header} {
		carried := r
		carried.CarriedIn = "body"
		if r.In == "body" {
			carried.CarriedIn = "query"
		}
		if got := recordedGapProblems([]UnsentInput{carried}, nil, openGet); len(got) != 0 {
			t.Errorf("a carried_in %s row is not a gap, got %v", r.In, got)
		}
	}
	// So is one whose route names it as a path parameter -- but only by its whole
	// name: `id` is not carried by a route whose parameter is `{thing_id}`.
	routed := UnsentInput{Path: "/things/{thing_id}/parts", Method: "post", In: "body", Name: "thing_id", Reason: "r"}
	if got := recordedGapProblems([]UnsentInput{routed}, nil, openPost); len(got) != 0 {
		t.Errorf("a row its route carries is not a gap, got %v", got)
	}
	partial := routed
	partial.Name = "id"
	if got := strings.Join(recordedGapProblems([]UnsentInput{partial}, nil, openPost), "\n"); !strings.Contains(got, "recordedGaps does not name it") {
		t.Errorf("`id` on a route carrying {thing_id} is a gap, got %q", got)
	}
}

// TestEveryOperationIsObservable: the observer reaches every operation the
// coverage file maps to a method, and sees the request that operation's method
// sends, so a gap recorded against any of them is held -- not refused for want of
// a way to ask. The vocabulary rows also pin what a fully filled call sends,
// including VocabularyUpdate's custom MarshalJSON and the two inputs #118 ported.
func TestEveryOperationIsObservable(t *testing.T) {
	cov, err := LoadCoverage(filepath.Join(repoRoot, "docs", "contract-coverage.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	observe := coverageObserver(cov)
	observed := map[string]sentRequest{}
	for _, op := range cov.Operations {
		if op.SDK == "" {
			continue
		}
		key := op.Method + " " + op.Path
		sent, err := observe(key)
		if err != nil {
			t.Errorf("%s (%s): %v", key, op.SDK, err)
			continue
		}
		if !strings.EqualFold(sent.method, op.Method) {
			t.Errorf("%s (%s) sent %s", key, op.SDK, sent.method)
		}
		// The Config is filled too, which only a Config field's own input shows:
		// ActorID's header, on every request but the unauthenticated probes.
		if !strings.HasPrefix(op.Path, "/health/") && !sent.headers[http.CanonicalHeaderKey("X-Actor-ID")] {
			t.Errorf("%s (%s) sent no X-Actor-ID, so the observed client's Config was not filled", key, op.SDK)
		}
		observed[key] = sent
	}
	for _, tc := range []struct {
		op, in string
		names  []string
	}{
		{"get /vocabularies", "query", []string{"limit", "offset", "application_id", "include_shared", "is_active", "q", "slug"}},
		{"post /vocabularies", "body", []string{"application_id", "name", "slug", "description", "metadata", "is_active"}},
		{"patch /vocabularies/{vocabulary_id}", "body", []string{"application_id", "name", "slug", "description", "metadata", "is_active"}},
	} {
		carried := observed[tc.op].query
		if tc.in == "body" {
			carried = observed[tc.op].body
		}
		for _, name := range tc.names {
			if !carried[name] {
				t.Errorf("%s: a fully filled call did not send `%s` in the %s; sent %v", tc.op, name, tc.in, carried)
			}
		}
	}
}

// TestObserveCallSeesWhatTheEncoderSends: the shapes reflection over a struct's
// fields got wrong, observed through a real json.Marshal -- two embedded structs
// promoting one Go name under two JSON names (encoding/json sends both; a field
// walk dropped both), a field promoted through an unexported embedded struct, and
// a custom MarshalJSON. Plus the refusals: no request, two, and a panic.
func TestObserveCallSeesWhatTheEncoderSends(t *testing.T) {
	type left struct {
		Value *string `json:"q,omitempty"`
	}
	type right struct {
		Value *string `json:"other,omitempty"`
	}
	type hidden struct {
		Promoted *string `json:"promoted,omitempty"`
	}
	type body struct {
		left
		right
		hidden
		Plain *string `json:"plain,omitempty"`
	}
	rec := &gapRecorder{}
	post := func(_ context.Context, in body, rename customBody) error {
		for _, value := range []interface{}{in, rename} {
			raw, err := json.Marshal(value)
			if err != nil {
				return err
			}
			req, err := http.NewRequest(http.MethodPost, "http://x/things", bytes.NewReader(raw))
			if err != nil {
				return err
			}
			rec.record(req)
		}
		return nil
	}
	if _, err := observeCall(reflect.ValueOf(post), rec); err == nil || !strings.Contains(err.Error(), "sent 2 requests") {
		t.Fatalf("two requests must be refused, got %v", err)
	}
	if len(rec.sent) != 2 {
		t.Fatalf("recorded %d requests", len(rec.sent))
	}
	for _, name := range []string{"q", "other", "promoted", "plain"} {
		if !rec.sent[0].body[name] {
			t.Errorf("a filled body did not send `%s`: %v", name, rec.sent[0].body)
		}
	}
	if !rec.sent[1].body["renamed"] || rec.sent[1].body["Field"] {
		t.Errorf("a custom MarshalJSON's names were not the ones observed: %v", rec.sent[1].body)
	}

	if _, err := observeCall(reflect.ValueOf(func(context.Context) error { panic("boom") }), &gapRecorder{}); err == nil || !strings.Contains(err.Error(), "panicked: boom") {
		t.Errorf("a panicking call must be reported, got %v", err)
	}

	silent := &gapRecorder{}
	if _, err := observeCall(reflect.ValueOf(func(context.Context, *body) error { return nil }), silent); err == nil || !strings.Contains(err.Error(), "sent 0 requests") {
		t.Errorf("a call that sends nothing must be refused, got %v", err)
	}
}

// customBody marshals under a name its field does not carry.
type customBody struct{ Field *string }

func (c customBody) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]*string{"renamed": c.Field})
}
