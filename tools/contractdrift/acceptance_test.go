package main

import (
	"errors"
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

// gateRecipePins are the recipes behind the gate's CI steps and the release gate,
// PINNED, verbatim. CI runs `make contract-test` and `make contract-identity` and
// trusts their status, and gateWorkflowProblems holds the STEPS to that -- which
// proves nothing if the recipe behind a step stops doing its job:
// `scripts/contract-identity.sh report >/dev/null` in place of `check` passed every
// other test here. A shell reader of recipes never converged on this repository
// (#95), so the text is pinned instead, like the smoke and isolation recipes in the
// root package.
//
// The pins are held to make's OWN rule database (`make -pq`, which runs no recipe),
// never to the Makefile's text, because a recipe is also changed from outside its
// text, and make is the only reader that sees every way: a second rule, a line
// naming several targets at once (`noop contract-check: ; @true`), a target list in
// a variable (`$(gate): ; @true`), an `$(eval ...)`, a target- or pattern-specific
// variable, `.IGNORE`, `.ONESHELL`, a Makefile-set SHELL. Each target's EFFECTIVE
// recipe must be its pin, release-check's must still name the three contract
// targets, and the rest is refused as make resolved it. A source reader did this
// job first -- the root package's makeRule, ported -- and a review walked a
// variable target list past it; with the database beside it, it caught nothing the
// database did not, so it went.
//
// Changing one of these means re-checking what it is pinned for -- the gate's tests
// run, the gate's exit status reaches make, the identity check CHECKS -- and then
// updating the pin in the same commit.
var gateRecipePins = map[string]string{
	"contract-check": "\t@set -e; \\\n" +
		"\tdir=$$(mktemp -d); trap 'rm -rf \"$$dir\"' EXIT; \\\n" +
		"\t(cd tools/contractdrift && go build -o \"$$dir/contractdrift\" .); \\\n" +
		"\t\"$$dir/contractdrift\" -repo . -local",
	"contract-test":          "\t@cd tools/contractdrift && go vet ./... && go test -race ./...",
	"contract-identity":      "\t@scripts/contract-identity.sh check",
	"contract-identity-test": "\t@scripts/contract-identity-test.sh",
	"contract-report":        "\t@scripts/contract-identity.sh report",
}

func TestTheGateRecipesArePinned(t *testing.T) {
	problems, err := gateMakefileProblems(filepath.Join(repoRoot, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	for _, problem := range problems {
		t.Error(problem)
	}
}

// gateMakefileProblems reads one Makefile through make and holds it to the pins.
// The real tree and every fixture below go through it, so what the fixtures prove
// is what the real tree is held to.
func gateMakefileProblems(path string) ([]string, error) {
	db, err := makeDatabase(path)
	if err != nil {
		return nil, err
	}
	return makeDatabaseProblems(db), nil
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
	notUpToDate := errors.As(err, &exit) && exit.ExitCode() == 1
	if err != nil && !notUpToDate {
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
		want := gateRecipePins[target] + "\n"
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

// TestGateMakefileProblemsRefusesEachOverride: every way above to make a gate
// target, or the release gate, run something other than its pin, each as ONE edit
// of the real Makefile read back through make -- the literal forms and the ones
// only make can compute -- and the edits it must leave alone.
func TestGateMakefileProblemsRefusesEachOverride(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	const identityRecipe = "\t@scripts/contract-identity.sh check\n"
	if !strings.Contains(src, identityRecipe) {
		t.Fatal("the Makefile no longer contains the identity recipe a fixture edits")
	}
	cases := []struct{ name, makefile, want string }{
		{"the recipe edited", strings.Replace(src, identityRecipe, "\t@scripts/contract-identity.sh report >/dev/null\n", 1), "the recipe make will run for contract-identity"},
		{"a multi-target rule", src + "\nnoop contract-check contract-test contract-identity contract-identity-test release-check: ; @true\n", "the recipe make will run for contract-check"},
		{"a target list in a variable", src + "\ngate := contract-check contract-test contract-identity contract-identity-test contract-report release-check\n$(gate): ; @true\n", "the recipe make will run for contract-test"},
		{"a rule from eval", src + "\n$(eval contract-test: ; @true)\n", "the recipe make will run for contract-test"},
		{"a target-specific variable", src + "\ncontract-test: MAKEFLAGS += -n\n", "target-specific variable for contract-test"},
		{"a computed target-specific variable", src + "\nt := contract-identity\n$(t): MAKEFLAGS += -n\n", "target-specific variable for contract-identity"},
		{"a pattern-specific variable", src + "\ncontract-%: SHELL = /bin/true\n", "pattern-specific variable for contract-%"},
		{"a computed pattern-specific variable", src + "\np := contract-%\n$(p): SHELL = /bin/true\n", "pattern-specific variable for contract-%"},
		{"a .IGNORE naming a gate target", src + "\n.IGNORE: dev-server-down \\\n\tcontract-test\n", ".IGNORE for contract-test"},
		{"a computed .IGNORE", src + "\ni := .IGNORE\n$(i): contract-test\n", ".IGNORE for contract-test"},
		{"a computed bare .IGNORE", src + "\ni := .IGNORE\n$(i):\n", ".IGNORE for every target"},
		{"a computed .ONESHELL", src + "\no := .ONESHELL\n$(o):\n", ".ONESHELL"},
		{"a computed SHELL", src + "\ns := SHELL\n$(s) := /bin/true\n", "resolves SHELL from the Makefile"},
		{"the release gate overridden", src + "\nr := release-check\n$(r): ; @true\n", "the recipe make will run for release-check"},
		{"the release gate dropping the identity check", strings.Replace(src, " contract-identity ## Full pre-release gate", " ## Full pre-release gate", 1), "release-check does not depend on contract-identity"},
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "Makefile")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			write(t, path, tc.makefile)
			problems, err := gateMakefileProblems(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(problems, "\n"); !strings.Contains(got, tc.want) {
				t.Errorf("want a problem containing %q, got:\n%s", tc.want, got)
			}
		})
	}
	// What must stay quiet: a .IGNORE for something else, a pattern rule that no
	// explicit gate rule uses, the .PHONY line naming the gate targets.
	write(t, path, src+"\n.IGNORE: dev-server-down\n%.o: %.c\n\t@true\n%:\n\t@true\n")
	problems, err := gateMakefileProblems(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 {
		t.Errorf("unexpected problems: %v", problems)
	}
}
