package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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
// this client has no field to send -- each with the field whose absence the row
// asserts. Every other unsent_inputs row is a decision with a carried_in or a path
// that says where the input goes instead.
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
// case is still a rule: recordedGapProblems refuses any unsent_inputs query row
// with no carried_in that this table does not name, so a future gap row cannot be
// added without naming the field whose absence it asserts. An entry is written
// {op, in, name, reflect.TypeOf(octonomy.FooListParams{}), "Field"}.
var recordedGaps []recordedGap

// recordedGap is one gap row and the field whose absence it asserts.
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
	for _, problem := range recordedGapProblems(cov.UnsentInputs, recordedGaps) {
		t.Error(problem)
	}
}

// recordedGapProblems holds the unsent_inputs rows to the gaps table: every query
// row with nowhere else to carry its input is a gap the table names, every entry
// in the table is still a row, and every gap's field is still missing.
func recordedGapProblems(rows []UnsentInput, gaps []recordedGap) []string {
	var problems []string
	listed := map[string]bool{}
	for _, gap := range gaps {
		listed[gap.op+" "+gap.in+" "+gap.name] = true
	}
	present := map[string]bool{}
	for _, row := range rows {
		key := row.Method + " " + row.Path + " " + row.In + " " + row.Name
		present[key] = true
		// A row with no replacement path is a gap, and a gap needs a field to hold
		// it to. The resource_type / resource_id rows name the route instead.
		if row.CarriedIn == "" && row.In == "query" && !listed[key] {
			problems = append(problems, fmt.Sprintf("unsent_inputs records `%s` with nowhere else to carry it -- a gap -- and recordedGaps does not name the field whose absence it asserts", key))
		}
	}
	for _, gap := range gaps {
		key := gap.op + " " + gap.in + " " + gap.name
		if !present[key] {
			problems = append(problems, fmt.Sprintf("recordedGaps names `%s`, which unsent_inputs no longer lists -- drop the entry", key))
			continue
		}
		if _, has := gap.params.FieldByName(gap.field); has {
			problems = append(problems, fmt.Sprintf("%s.%s exists, so the unsent_inputs row for `%s` records a gap that is closed -- drop the row, set the field in drivers.go, and the gate holds it from there",
				gap.params.Name(), gap.field, key))
		}
	}
	return problems
}

// TestRecordedGapProblemsRefusesEachForm: the check above against a gap that has
// closed, a gap row nobody listed, and a listed gap with no row -- so passing on
// the real file is not its only evidence.
func TestRecordedGapProblemsRefusesEachForm(t *testing.T) {
	type closed struct{ Query *string }
	type open struct{ Other *string }
	row := UnsentInput{Path: "/things", Method: "get", In: "query", Name: "q", Reason: "r"}
	gap := recordedGap{"get /things", "query", "q", reflect.TypeOf(open{}), "Query"}

	if got := recordedGapProblems([]UnsentInput{row}, []recordedGap{gap}); len(got) != 0 {
		t.Fatalf("a listed gap whose field is missing is fine, got %v", got)
	}
	closedGap := gap
	closedGap.params = reflect.TypeOf(closed{})
	cases := []struct {
		name string
		rows []UnsentInput
		gaps []recordedGap
		want string
	}{
		{"the field arrived", []UnsentInput{row}, []recordedGap{closedGap}, "closed.Query exists"},
		{"a gap row nobody listed", []UnsentInput{row}, nil, "recordedGaps does not name"},
		{"a listed gap with no row", nil, []recordedGap{gap}, "no longer lists"},
	}
	for _, tc := range cases {
		got := strings.Join(recordedGapProblems(tc.rows, tc.gaps), "\n")
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s: want a problem containing %q, got %q", tc.name, tc.want, got)
		}
	}
	// A row that names where the input travels instead is a decision, not a gap.
	carried := row
	carried.CarriedIn = "body"
	if got := recordedGapProblems([]UnsentInput{carried}, nil); len(got) != 0 {
		t.Errorf("a carried_in row is not a gap, got %v", got)
	}
}
