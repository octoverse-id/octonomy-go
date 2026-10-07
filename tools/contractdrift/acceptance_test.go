package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
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
// this client has no field to send -- each with the field whose absence the row
// asserts. Every other unsent_inputs row is a decision with a carried_in or a path
// that says where the input goes instead.
//
// They need a check of their own because a gap row suppresses exactly the finding
// that would notice the gap closing badly. Port VocabularyListParams.Query and
// forget the driver, and with the row in place the gate reports nothing at all:
// the row says "documented and unsent" is expected, so a field that exists and is
// never driven reads the same as one that does not exist. So a row here has to be
// TRUE -- the field really is missing -- and the moment the field arrives this test
// fails, the row comes out, and the gate takes over: documented and unsent until
// the driver sets it.
var recordedGaps = []recordedGap{
	{"get /vocabularies", "query", "q", reflect.TypeOf(octonomy.VocabularyListParams{}), "Query"},
	{"get /vocabularies", "query", "slug", reflect.TypeOf(octonomy.VocabularyListParams{}), "Slug"},
}

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

// gateRecipePins are the Makefile rules the gate's CI steps and the release gate
// run, PINNED: each rule's header line and recipe, verbatim. CI runs `make
// contract-test` and `make contract-identity` and trusts their status, and
// gateWorkflowProblems holds the STEPS to that -- which proves nothing if the
// recipe behind the step stops doing its job: `scripts/contract-identity.sh report
// >/dev/null` in place of `check` passed every other test here. A shell reader of
// recipes never converged on this repository (#95), so the text is pinned instead,
// like the smoke and isolation recipes in the root package.
//
// A pinned recipe can still be changed from OUTSIDE its text. The file-wide ways --
// a bare `.IGNORE:`, `.ONESHELL`, SHELL, MAKEFLAGS, GOFLAGS, an include -- are
// refused for this Makefile by the root package's smokeprobes_test.go. Every way
// that names a target is caught here, because what is pinned is not one rule but
// EVERY line whose targets reach the pinned one, read as make reads them: a second
// rule, a line naming several targets at once (`noop contract-check: ; @true` is
// an explicit rule for contract-check too), a target-specific variable, a pattern
// that matches the target -- each adds a line the pin does not have. The one way
// that names a target as a PREREQUISITE is `.IGNORE: contract-identity`, and that
// is refused separately. This is the root package's makeRule, ported; an earlier
// reader matched only lines starting with the target and passed the multi-target
// form.
//
// A SOURCE reader still cannot read what make computes: `gate := contract-check
// contract-test` then `$(gate): ; @true` overrides both recipes, and so does an
// `$(eval ...)`, and neither line names a target a reader can see. So there is a
// second layer that does not read the source at all: make's own rule database
// (`make -pq`, which runs no recipe). It is held to the same pins -- each target's
// EFFECTIVE recipe, release-check's prerequisites -- and refuses, as make resolved
// them, a target-specific or pattern-specific variable reaching a gate target,
// `.IGNORE` for one or for every target, `.ONESHELL`, and SHELL, .SHELLFLAGS,
// MAKEFLAGS or GOFLAGS set by the Makefile at all. What the database cannot show
// -- a recipe whose own text runs something else -- is what the source pin is for.
//
// Changing one of these means re-checking what it is pinned for -- the gate's tests
// run, the gate's exit status reaches make, the identity check CHECKS -- and then
// updating the pin in the same commit.
var gateRecipePins = map[string]string{
	"contract-check": "contract-check: ## Offline contract gate: vendored contracts vs what this client sends and decodes\n" +
		"\t@set -e; \\\n" +
		"\tdir=$$(mktemp -d); trap 'rm -rf \"$$dir\"' EXIT; \\\n" +
		"\t(cd tools/contractdrift && go build -o \"$$dir/contractdrift\" .); \\\n" +
		"\t\"$$dir/contractdrift\" -repo . -local",
	"contract-test": "contract-test: ## Run the contract gate's own tests (proves the gate can still fail)\n" +
		"\t@cd tools/contractdrift && go vet ./... && go test -race ./...",
	"contract-identity": "contract-identity: ## Fail unless the gate's shared files are byte-identical to main at the pinned commit\n" +
		"\t@scripts/contract-identity.sh check",
	"contract-identity-test": "contract-identity-test: ## Run the identity check's fixture tests\n" +
		"\t@scripts/contract-identity-test.sh",
	"contract-report": "contract-report: ## Print the diff of the gate's compat-only files against main at the pin (advisory)\n" +
		"\t@scripts/contract-identity.sh report",
}

func TestTheGateRecipesArePinned(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	for _, problem := range gateRecipeProblems(string(raw)) {
		t.Error(problem)
	}
	db, err := makeDatabase(filepath.Join(repoRoot, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	for _, problem := range makeDatabaseProblems(db) {
		t.Error(problem)
	}
}

// makeDatabase prints make's rule database for a Makefile without running any of
// its recipes: -q asks only whether a goal is up to date, and the goal is an empty
// target in a second makefile, so nothing the first one defines is considered at
// all. The make variables a caller's environment can carry -- the MAKEFLAGS a
// parent `make contract-test` exports among them -- are cleared, so the database is
// the Makefile's alone.
func makeDatabase(makefile string) (string, error) {
	if _, err := exec.LookPath("make"); err != nil {
		return "", fmt.Errorf("make is not installed, and its rule database is what this reads")
	}
	goal, err := os.CreateTemp("", "contractdrift-goal-*.mk")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(goal.Name()) }()
	if _, err := goal.WriteString("__contractdrift_database__:\n"); err != nil {
		return "", err
	}
	if err := goal.Close(); err != nil {
		return "", err
	}
	cmd := exec.Command("make", "-pq", "-f", filepath.Base(makefile), "-f", goal.Name(), "__contractdrift_database__")
	cmd.Dir = filepath.Dir(makefile)
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		switch name {
		case "MAKEFLAGS", "MFLAGS", "GNUMAKEFLAGS", "MAKELEVEL", "MAKEFILES", "MAKEOVERRIDES":
			continue
		}
		cmd.Env = append(cmd.Env, kv)
	}
	out, err := cmd.CombinedOutput()
	// -q exits 1 for "not up to date", which is not a failure here; 2 is.
	var exit *exec.ExitError
	if err != nil && !(errors.As(err, &exit) && exit.ExitCode() == 1) {
		return "", fmt.Errorf("make -pq on %s: %v\n%s", makefile, err, out)
	}
	if !strings.Contains(string(out), "\n# Files\n") {
		return "", fmt.Errorf("make -pq on %s printed no rule database:\n%s", makefile, out)
	}
	return string(out), nil
}

// makeDatabaseProblems holds make's own resolved view of the Makefile to the pins.
func makeDatabaseProblems(db string) []string {
	var problems []string
	guarded := func(target string) bool {
		_, pinned := gateRecipePins[target]
		return pinned || target == "release-check"
	}
	lines := strings.Split(db, "\n")

	// Variables the Makefile itself set that decide how EVERY recipe runs. The
	// origin line above each one says where it came from.
	for i, line := range lines {
		name, _, ok := strings.Cut(line, " ")
		if !ok || i == 0 {
			continue
		}
		switch name {
		case "SHELL", ".SHELLFLAGS", "MAKEFLAGS", "GOFLAGS":
		default:
			continue
		}
		if strings.HasPrefix(lines[i-1], "# makefile (from ") {
			problems = append(problems, fmt.Sprintf("make resolves %s from the Makefile (%s), and it decides how every gate recipe runs", name, strings.TrimPrefix(lines[i-1], "# makefile ")))
		}
	}

	// Pattern-specific variables, as make indexed them.
	inPatterns := false
	for _, line := range lines {
		switch {
		case line == "# Pattern-specific Variable Values":
			inPatterns = true
			continue
		case inPatterns && strings.HasPrefix(line, "# ") && strings.Contains(line, "pattern-specific variable values"):
			inPatterns = false
		case inPatterns && strings.HasSuffix(line, " :") && !strings.HasPrefix(line, "#"):
			pattern := strings.TrimSuffix(line, " :")
			for _, target := range append(sortedStrings(gateRecipePins), "release-check") {
				if namesMakeTarget([]string{pattern}, target) {
					problems = append(problems, fmt.Sprintf("make holds a pattern-specific variable for %s, which reaches %s", pattern, target))
				}
			}
		}
	}

	// The rules, as make resolved them: one block per target, each with its
	// effective recipe.
	files := strings.Index(db, "\n# Files\n")
	recipes, prereqs := map[string]string{}, map[string][]string{}
	var current string
	inRecipe := false
	for _, line := range strings.Split(db[files:], "\n") {
		switch {
		case line == "":
			current, inRecipe = "", false
		case strings.HasPrefix(line, "#  recipe to execute"):
			inRecipe = current != ""
		case strings.HasPrefix(line, "\t") && inRecipe:
			recipes[current] += line + "\n"
		case strings.HasPrefix(line, "#"):
		default:
			target, rest, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			if strings.Contains(rest, "=") {
				if guarded(target) {
					problems = append(problems, fmt.Sprintf("make holds a target-specific variable for %s: %s", target, line))
				}
				continue
			}
			current = target
			prereqs[target] = strings.Fields(rest)
			switch target {
			case ".ONESHELL":
				problems = append(problems, "make holds .ONESHELL, so each gate recipe runs as one script and its last line decides its status")
			case ".IGNORE":
				if len(prereqs[target]) == 0 {
					problems = append(problems, "make holds .IGNORE for every target, so it ignores every gate recipe's failure")
				}
				for _, name := range prereqs[target] {
					if guarded(name) {
						problems = append(problems, fmt.Sprintf("make holds .IGNORE for %s, so it ignores that recipe's failure", name))
					}
				}
			}
		}
	}
	for _, target := range sortedStrings(gateRecipePins) {
		pin := gateRecipePins[target]
		want := pin[strings.Index(pin, "\n")+1:] + "\n"
		if got, ok := recipes[target]; !ok {
			problems = append(problems, fmt.Sprintf("make's database has no recipe for %s", target))
		} else if got != want {
			problems = append(problems, fmt.Sprintf("the recipe make will run for %s is not the pinned one:\n got:\n%s\nwant:\n%s", target, got, want))
		}
	}
	if got := recipes["release-check"]; got != "\t@echo \"release-check passed\"\n" {
		problems = append(problems, fmt.Sprintf("the recipe make will run for release-check is %q", got))
	}
	for _, want := range []string{"contract-test", "contract-check", "contract-identity"} {
		if !contains(prereqs["release-check"], want) {
			problems = append(problems, fmt.Sprintf("in make's database release-check does not depend on %s", want))
		}
	}
	return problems
}

// TestMakeDatabaseProblemsRefusesWhatMakeComputes: the forms a source reader
// cannot see, each appended to the real Makefile, read back through make itself.
func TestMakeDatabaseProblemsRefusesWhatMakeComputes(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, add, want string }{
		{"a target list in a variable", "gate := contract-check contract-test contract-identity contract-identity-test contract-report release-check\n$(gate): ; @true\n", "the recipe make will run for contract-check"},
		{"a rule from eval", "$(eval contract-test: ; @true)\n", "the recipe make will run for contract-test"},
		{"a computed target-specific variable", "t := contract-identity\n$(t): MAKEFLAGS += -n\n", "target-specific variable for contract-identity"},
		{"a computed pattern-specific variable", "p := contract-%\n$(p): SHELL = /bin/true\n", "pattern-specific variable for contract-%"},
		{"a computed .IGNORE", "i := .IGNORE\n$(i): contract-test\n", ".IGNORE for contract-test"},
		{"a computed bare .IGNORE", "i := .IGNORE\n$(i):\n", ".IGNORE for every target"},
		{"a computed .ONESHELL", "o := .ONESHELL\n$(o):\n", ".ONESHELL"},
		{"a computed SHELL", "s := SHELL\n$(s) := /bin/true\n", "resolves SHELL from the Makefile"},
		{"the release gate overridden", "r := release-check\n$(r): ; @true\n", "release-check is"},
	}
	dir := t.TempDir()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, "Makefile")
			write(t, path, string(raw)+"\n"+tc.add)
			db, err := makeDatabase(path)
			if err != nil {
				t.Fatal(err)
			}
			got := strings.Join(makeDatabaseProblems(db), "\n")
			if !strings.Contains(got, tc.want) {
				t.Errorf("want a problem containing %q, got:\n%s", tc.want, got)
			}
		})
	}
}

// gateRecipeProblems holds a Makefile to gateRecipePins, and its release gate to
// running the three contract targets that hold.
func gateRecipeProblems(makefile string) []string {
	var problems []string
	for _, target := range sortedStrings(gateRecipePins) {
		if got := makeRule(makefile, target); got != gateRecipePins[target] {
			problems = append(problems, fmt.Sprintf("the Makefile's lines for `%s` are not the pinned rule. Re-check what gateRecipePins is pinned for, then update the pin.\n got:\n%s\nwant:\n%s",
				target, got, gateRecipePins[target]))
		}
	}
	// The release gate: exactly one line reaching it -- its own rule -- whose
	// prerequisites include the gate's tests, the gate and the identity check.
	rule := makeRule(makefile, "release-check")
	headers := 0
	var prereqs []string
	for _, line := range strings.Split(rule, "\n") {
		if strings.HasPrefix(line, "\t") {
			continue
		}
		headers++
		if _, rest, ok := strings.Cut(line, ":"); ok {
			prereqs = strings.Fields(strings.SplitN(rest, "##", 2)[0])
		}
	}
	if headers != 1 {
		problems = append(problems, fmt.Sprintf("%d Makefile lines reach `release-check`, want exactly its own rule:\n%s", headers, rule))
	}
	for _, want := range []string{"contract-test", "contract-check", "contract-identity"} {
		if !contains(prereqs, want) {
			problems = append(problems, fmt.Sprintf("release-check does not run %s", want))
		}
	}
	// The one override that names a pinned target as a prerequisite.
	for _, ll := range makeLogicalLines(makefile) {
		head, rest, ok := strings.Cut(ll, ":")
		if !ok || strings.TrimSpace(head) != ".IGNORE" {
			continue
		}
		for _, name := range strings.Fields(strings.SplitN(rest, "#", 2)[0]) {
			if _, pinned := gateRecipePins[name]; pinned || name == "release-check" {
				problems = append(problems, fmt.Sprintf("the Makefile declares .IGNORE for %s, so make ignores its recipe's failure", name))
			}
		}
	}
	return problems
}

// makeRule returns every make line whose targets reach target -- the rule, a
// target-specific variable, a line naming several targets at once, and a pattern
// that matches it -- each followed by its recipe lines, trimmed of trailing space
// and joined by newlines. Ported from the root package's smokeprobes_test.go.
// Continuations are joined first, as make joins them; a `target :=` is a
// variable named target, not a rule; and `.PHONY: … target` names it as a
// prerequisite, not a target.
func makeRule(src, target string) string {
	lines := strings.Split(src, "\n")
	var out []string
	for _, ll := range makeLogicalLinesAt(src) {
		if strings.HasPrefix(ll.text, "\t") {
			continue
		}
		m := makeTargets.FindStringSubmatch(ll.text)
		if m == nil || !namesMakeTarget(strings.Fields(m[1]), target) {
			continue
		}
		out = append(out, strings.TrimRight(ll.text, " \t"))
		end := ll.line
		for end < len(lines) && strings.HasSuffix(strings.TrimRight(lines[end-1], " \t"), "\\") {
			end++
		}
		for j := end; j < len(lines); j++ {
			if strings.HasPrefix(lines[j], "\t") {
				out = append(out, strings.TrimRight(lines[j], " \t"))
				continue
			}
			if strings.TrimSpace(lines[j]) == "" {
				continue
			}
			break
		}
	}
	return strings.Join(out, "\n")
}

// makeTargets matches a rule or target-specific line, capturing its targets: the
// text before a `:` or `::` that does not open an assignment.
var makeTargets = regexp.MustCompile(`^([^\t#=:][^#=:]*?)\s*::?([^=]|$)`)

// namesMakeTarget reports whether a list of make targets reaches target: by name,
// or as a pattern (`%oke`, `sm%`, `%`).
func namesMakeTarget(words []string, target string) bool {
	for _, w := range words {
		if w == target {
			return true
		}
		if i := strings.IndexByte(w, '%'); i >= 0 {
			prefix, suffix := w[:i], w[i+1:]
			if len(target) >= len(prefix)+len(suffix) && strings.HasPrefix(target, prefix) && strings.HasSuffix(target, suffix) {
				return true
			}
		}
	}
	return false
}

type makeLogical struct {
	line int // the physical line it starts on, 1-based
	text string
}

// makeLogicalLinesAt joins backslash continuations as make does.
func makeLogicalLinesAt(src string) []makeLogical {
	var out []makeLogical
	physical := strings.Split(src, "\n")
	for i := 0; i < len(physical); i++ {
		start, text := i+1, physical[i]
		for strings.HasSuffix(strings.TrimRight(text, " \t"), "\\") && i+1 < len(physical) {
			text = strings.TrimSuffix(strings.TrimRight(text, " \t"), "\\") + " " + physical[i+1]
			i++
		}
		out = append(out, makeLogical{line: start, text: text})
	}
	return out
}

func makeLogicalLines(src string) []string {
	var out []string
	for _, ll := range makeLogicalLinesAt(src) {
		out = append(out, ll.text)
	}
	return out
}

// TestGateRecipeProblemsRefusesEachOverride: the check above against the real
// Makefile with ONE edit each -- every form that could make a gate target, or the
// release gate, run something other than its pinned recipe -- so passing on the
// real file is not its only evidence.
func TestGateRecipeProblemsRefusesEachOverride(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	if got := gateRecipeProblems(src); len(got) != 0 {
		t.Fatalf("the real Makefile has problems, so the cases below prove nothing: %v", got)
	}
	const identityRecipe = "\t@scripts/contract-identity.sh check\n"
	if !strings.Contains(src, identityRecipe) {
		t.Fatal("the Makefile no longer contains the identity recipe the fixtures edit")
	}
	cases := []struct{ name, edit, want string }{
		{"the recipe edited", strings.Replace(src, identityRecipe, "\t@scripts/contract-identity.sh report >/dev/null\n", 1), "`contract-identity` are not the pinned rule"},
		{"a multi-target rule overriding every gate target", src + "\nnoop contract-check contract-test contract-identity contract-identity-test release-check:\n\t@true\n", "`contract-check` are not the pinned rule"},
		{"the same, for the release gate", src + "\nnoop release-check:\n\t@true\n", "lines reach `release-check`"},
		{"a target-specific variable", src + "\ncontract-test: MAKEFLAGS += -n\n", "`contract-test` are not the pinned rule"},
		{"a pattern-specific variable", src + "\ncontract-%: SHELL = /bin/true\n", "`contract-identity` are not the pinned rule"},
		{"a match-anything pattern rule", src + "\n%:\n\t@true\n", "`contract-check` are not the pinned rule"},
		{"a .IGNORE naming a gate target", src + "\n.IGNORE: dev-server-down \\\n\tcontract-test\n", ".IGNORE for contract-test"},
		{"the release gate dropping the identity check", strings.Replace(src, " contract-identity ## Full pre-release gate", " ## Full pre-release gate", 1), "release-check does not run contract-identity"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(gateRecipeProblems(tc.edit), "\n")
			if !strings.Contains(got, tc.want) {
				t.Errorf("want a problem containing %q, got:\n%s", tc.want, got)
			}
		})
	}
	// And what must stay quiet: a .IGNORE for something else, a pattern that
	// matches no gate target, the .PHONY line naming them as prerequisites.
	quiet := src + "\n.IGNORE: dev-server-down\n%.o: %.c\n\t@true\n"
	if got := gateRecipeProblems(quiet); len(got) != 0 {
		t.Errorf("unexpected problems: %v", got)
	}
}
