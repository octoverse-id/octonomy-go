package main

import (
	"fmt"
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
// this client has no field to send -- each held to the struct that would carry it.
// Every other unsent_inputs row is a decision with a carried_in, or a route that
// names the input as a path parameter, saying where it goes instead.
//
// They need a check of their own because a gap row suppresses exactly the finding
// that would notice the gap closing badly. Port the missing field and forget the
// driver, and with the row in place the gate reports nothing at all: the row says
// "documented and unsent" is expected, so a field that exists and is never driven
// reads the same as one that does not exist. So a row here has to be TRUE -- the
// field really is missing -- and the moment the field arrives this test fails, the
// row comes out, and the gate takes over: documented and unsent until the driver
// sets it.
//
// EMPTY, and kept. Its only entries were `q` and `slug` on GET /vocabularies,
// recorded from #98 until #118 ported VocabularyListParams.Query and .Slug, when
// this test failed exactly as described above. The table stays because the empty
// case is still a rule: recordedGapProblems refuses every unsent_inputs row that
// is a gap -- in the query, the body or a header -- unless this table names it, so
// a future gap row cannot be added without holding it to its carrier.
//
// An entry names no struct and no field, because both were once typed by hand and
// neither was checked: a gap recorded against the wrong struct, or a misspelled
// field, passed while the real field sat undriven. The carrier is DERIVED --
// sdkCarrier reads it off the signature of the method contract-coverage.yaml names
// for the operation -- and the entry records that carrier's exported fields as
// they stood when the gap was recorded. Any change to them fails the check, so the
// field arriving fails it whatever it is called; a field added for another reason
// fails it too, and the entry is re-recorded once someone has looked. Write one as
// {op, in, name, []string{...}} and let the failure message supply the list.
var recordedGaps []recordedGap

// recordedGap is one gap row and the carrier fields it was recorded against.
type recordedGap struct {
	op, in, name string
	fields       []string
}

func TestRecordedGapsStillHaveNoField(t *testing.T) {
	cov, err := LoadCoverage(filepath.Join(repoRoot, "docs", "contract-coverage.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, problem := range recordedGapProblems(cov.UnsentInputs, recordedGaps, coverageCarriers(cov)) {
		t.Error(problem)
	}
}

// carrierFunc finds the struct that would carry an input of an operation, in the
// query or the body.
type carrierFunc func(op, in string) (reflect.Type, error)

// recordedGapProblems holds the unsent_inputs rows to the gaps table: every row
// with nowhere else to carry its input is a gap the table names, every entry in
// the table is still a row, and every gap's carrier is unchanged.
//
// A gap is decided by what the row says, never by where the input is documented.
// Until #118 only a query row was held, so an uncarried body or header row -- a
// write field dropped from its driver, say -- suppressed the gate's finding with
// nothing holding it to its field's absence. A header gap cannot be held at all:
// this client sends a header from a RequestOption, not a struct field, so there
// is no carrier to hold it to, and the row is refused until recordedGap learns
// another way to say what is missing.
func recordedGapProblems(rows []UnsentInput, gaps []recordedGap, carrierOf carrierFunc) []string {
	var problems []string
	listed := map[string]bool{}
	for _, gap := range gaps {
		listed[gap.op+" "+gap.in+" "+gap.name] = true
	}
	present := map[string]bool{}
	for _, row := range rows {
		key := row.Method + " " + row.Path + " " + row.In + " " + row.Name
		present[key] = true
		// A row with no replacement path is a gap, and a gap needs a carrier to
		// hold it to. The resource_type / resource_id rows name the route instead.
		if row.CarriedIn != "" || routeCarries(row) || listed[key] {
			continue
		}
		problems = append(problems, fmt.Sprintf("unsent_inputs records `%s` with nowhere else to carry it -- a gap -- and recordedGaps does not hold it to the struct that would carry it", key))
	}
	for _, gap := range gaps {
		key := gap.op + " " + gap.in + " " + gap.name
		if !present[key] {
			problems = append(problems, fmt.Sprintf("recordedGaps names `%s`, which unsent_inputs no longer lists -- drop the entry", key))
			continue
		}
		if gap.in == "header" {
			problems = append(problems, fmt.Sprintf("recordedGaps names `%s`, a header: this client sends headers from RequestOptions, not struct fields, so no carrier can hold the row -- extend recordedGap before recording a header gap", key))
			continue
		}
		carrier, err := carrierOf(gap.op, gap.in)
		if err != nil {
			problems = append(problems, fmt.Sprintf("recordedGaps names `%s`, and nothing holds it: %v", key, err))
			continue
		}
		// A body input is named by its JSON tag, so its arrival is visible
		// directly. A query input has no tag to read -- each params struct maps
		// its fields to names in code -- which is what the field list below is for.
		if gap.in == "body" {
			if field, ok := jsonField(carrier, gap.name); ok {
				problems = append(problems, fmt.Sprintf("%s.%s carries `%s` now, so the unsent_inputs row for `%s` records a gap that is closed -- drop the row, set the field in drivers.go, and the gate holds it from there",
					carrier.Name(), field, gap.name, key))
				continue
			}
		}
		if now := exportedFields(carrier); !reflect.DeepEqual(now, gap.fields) {
			problems = append(problems, fmt.Sprintf("%s's exported fields are now %q, not the %q the gap `%s` was recorded against. If one of them carries `%s`, the gap is closed -- drop the row, set the field in drivers.go, and the gate holds it from there; if none does, record the new list",
				carrier.Name(), now, gap.fields, key, gap.name))
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

// coverageCarriers derives each operation's carrier from the SDK method the
// coverage file names for it.
func coverageCarriers(cov *Coverage) carrierFunc {
	symbols := map[string]string{}
	for _, op := range cov.Operations {
		symbols[op.Method+" "+op.Path] = op.SDK
	}
	return func(op, in string) (reflect.Type, error) {
		symbol, ok := symbols[op]
		if !ok || symbol == "" {
			return nil, fmt.Errorf("contract-coverage.yaml names no SDK method for `%s`", op)
		}
		return sdkCarrier(symbol, in)
	}
}

// sdkCarrier reads the carrier of an input off the signature of the SDK method
// "Receiver.Method": the one pointer-to-struct parameter for the query (a
// *FooParams), the one struct passed by value for the body (a FooCreate, FooUpdate
// or other write struct). Anything else -- no such parameter, or two -- is an
// error, never a guess, since a guessed carrier is the hole this replaces.
func sdkCarrier(symbol, in string) (reflect.Type, error) {
	recv, name, ok := strings.Cut(symbol, ".")
	if !ok {
		return nil, fmt.Errorf("%q is not Receiver.Method", symbol)
	}
	var method reflect.Method
	found := false
	client := reflect.TypeOf(&octonomy.Client{})
	if recv == "Client" {
		method, found = client.MethodByName(name)
	} else {
		for i := 0; i < client.Elem().NumField(); i++ {
			field := client.Elem().Field(i)
			if field.IsExported() && field.Type.Kind() == reflect.Ptr && field.Type.Elem().Name() == recv {
				method, found = field.Type.MethodByName(name)
				break
			}
		}
	}
	if !found {
		return nil, fmt.Errorf("no method %s reachable from octonomy.Client", symbol)
	}
	var carriers []reflect.Type
	for i := 1; i < method.Type.NumIn(); i++ { // In(0) is the receiver
		param := method.Type.In(i)
		switch {
		case in == "query" && param.Kind() == reflect.Ptr && param.Elem().Kind() == reflect.Struct:
			carriers = append(carriers, param.Elem())
		case in == "body" && param.Kind() == reflect.Struct:
			carriers = append(carriers, param)
		}
	}
	if len(carriers) != 1 {
		return nil, fmt.Errorf("%s takes %d parameters that could carry a %s input, not one", symbol, len(carriers), in)
	}
	return carriers[0], nil
}

// exportedFields lists a struct's exported fields as encoding/json and a caller
// see them -- an embedded struct by its own name and by every field it promotes.
func exportedFields(t reflect.Type) []string {
	var out []string
	for _, field := range reflect.VisibleFields(t) {
		if field.IsExported() {
			out = append(out, field.Name)
		}
	}
	return out
}

// jsonField reports the exported field of t that encodes as name, if any.
func jsonField(t reflect.Type, name string) (string, bool) {
	for _, field := range reflect.VisibleFields(t) {
		if !field.IsExported() {
			continue
		}
		tag, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if tag == "-" || (field.Anonymous && tag == "") {
			continue
		}
		if tag == "" {
			tag = field.Name
		}
		if tag == name {
			return field.Name, true
		}
	}
	return "", false
}

// TestRecordedGapProblemsRefusesEachForm: the check above against a gap that has
// closed, a gap row nobody listed, and a listed gap with no row -- in the query,
// the body and a header -- so passing on the real file is not its only evidence.
func TestRecordedGapProblemsRefusesEachForm(t *testing.T) {
	type openParams struct{ Other *string }
	type closedParams struct {
		Other *string
		Query *string
	}
	type openWrite struct {
		Name *string `json:"name,omitempty"`
	}
	type closedWrite struct {
		Name *string `json:"name,omitempty"`
		Body *string `json:"description,omitempty"`
	}
	carriers := func(params, write reflect.Type) carrierFunc {
		return func(_, in string) (reflect.Type, error) {
			if in == "query" {
				return params, nil
			}
			return write, nil
		}
	}
	open := carriers(reflect.TypeOf(openParams{}), reflect.TypeOf(openWrite{}))
	closed := carriers(reflect.TypeOf(closedParams{}), reflect.TypeOf(closedWrite{}))
	missing := func(_, _ string) (reflect.Type, error) { return nil, fmt.Errorf("no carrier") }

	row := UnsentInput{Path: "/things", Method: "get", In: "query", Name: "q", Reason: "r"}
	gap := recordedGap{"get /things", "query", "q", []string{"Other"}}
	body := UnsentInput{Path: "/things", Method: "post", In: "body", Name: "description", Reason: "r"}
	bodyGap := recordedGap{"post /things", "body", "description", []string{"Name"}}
	header := UnsentInput{Path: "/things", Method: "get", In: "header", Name: "X-Thing", Reason: "r"}
	headerGap := recordedGap{"get /things", "header", "X-Thing", nil}

	if got := recordedGapProblems([]UnsentInput{row, body}, []recordedGap{gap, bodyGap}, open); len(got) != 0 {
		t.Fatalf("a listed gap whose carrier is unchanged is fine, in the query or the body, got %v", got)
	}
	cases := []struct {
		name    string
		rows    []UnsentInput
		gaps    []recordedGap
		carrier carrierFunc
		want    string
	}{
		{"a query field arrived", []UnsentInput{row}, []recordedGap{gap}, closed, `exported fields are now ["Other" "Query"]`},
		{"a body field arrived, whatever its Go name", []UnsentInput{body}, []recordedGap{bodyGap}, closed, "closedWrite.Body carries `description` now"},
		{"a gap row nobody listed", []UnsentInput{row}, nil, open, "recordedGaps does not hold it"},
		{"a listed gap with no row", nil, []recordedGap{gap}, open, "no longer lists"},
		{"a body gap row nobody listed", []UnsentInput{body}, nil, open, "recordedGaps does not hold it"},
		{"a header gap row nobody listed", []UnsentInput{header}, nil, open, "recordedGaps does not hold it"},
		{"a header gap listed", []UnsentInput{header}, []recordedGap{headerGap}, open, "a header"},
		{"a gap with no carrier", []UnsentInput{row}, []recordedGap{gap}, missing, "nothing holds it: no carrier"},
	}
	for _, tc := range cases {
		got := strings.Join(recordedGapProblems(tc.rows, tc.gaps, tc.carrier), "\n")
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
		if got := recordedGapProblems([]UnsentInput{carried}, nil, open); len(got) != 0 {
			t.Errorf("a carried_in %s row is not a gap, got %v", r.In, got)
		}
	}
	// So is one whose route names it as a path parameter -- but only by its whole
	// name: `id` is not carried by a route whose parameter is `{thing_id}`.
	routed := UnsentInput{Path: "/things/{thing_id}/parts", Method: "post", In: "body", Name: "thing_id", Reason: "r"}
	if got := recordedGapProblems([]UnsentInput{routed}, nil, open); len(got) != 0 {
		t.Errorf("a row its route carries is not a gap, got %v", got)
	}
	partial := routed
	partial.Name = "id"
	if got := strings.Join(recordedGapProblems([]UnsentInput{partial}, nil, open), "\n"); !strings.Contains(got, "recordedGaps does not hold it") {
		t.Errorf("`id` on a route carrying {thing_id} is a gap, got %q", got)
	}
}

// TestSDKCarrierReadsTheSignature: the derivation against the real client, so a
// gap is held to the struct its operation's method really takes -- and refused,
// not guessed, where there is none.
func TestSDKCarrierReadsTheSignature(t *testing.T) {
	cov, err := LoadCoverage(filepath.Join(repoRoot, "docs", "contract-coverage.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	carrierOf := coverageCarriers(cov)
	for _, tc := range []struct{ op, in, want string }{
		{"get /vocabularies", "query", "VocabularyListParams"},
		{"post /vocabularies", "body", "VocabularyCreate"},
		{"patch /vocabularies/{vocabulary_id}", "body", "VocabularyUpdate"},
		{"get /tags/{tag_id}/aliases", "query", "TagListAliasesParams"},
		{"post /resources/{resource_type}/{resource_id}/tags", "body", "ResourceReplace"},
		{"delete /tag-assignments", "body", "AssignmentRemove"},
	} {
		got, err := carrierOf(tc.op, tc.in)
		if err != nil || got.Name() != tc.want {
			t.Errorf("carrier of %s %s = %v, %v; want %s", tc.op, tc.in, got, err, tc.want)
		}
	}
	for _, tc := range []struct{ op, in, want string }{
		{"get /vocabularies/{vocabulary_id}", "query", "takes 0 parameters"},
		{"get /vocabularies", "body", "takes 0 parameters"},
		{"get /nowhere", "query", "names no SDK method"},
	} {
		if _, err := carrierOf(tc.op, tc.in); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("carrier of %s %s: want an error containing %q, got %v", tc.op, tc.in, tc.want, err)
		}
	}
	if _, err := sdkCarrier("NoSuchService.List", "query"); err == nil {
		t.Error("an unknown receiver must be an error")
	}
}
