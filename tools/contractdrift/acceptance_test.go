package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
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
var recordedGaps = []struct {
	op, in, name string
	params       reflect.Type
	field        string
}{
	{"get /vocabularies", "query", "q", reflect.TypeOf(octonomy.VocabularyListParams{}), "Query"},
	{"get /vocabularies", "query", "slug", reflect.TypeOf(octonomy.VocabularyListParams{}), "Slug"},
}

func TestRecordedGapsStillHaveNoField(t *testing.T) {
	cov, err := LoadCoverage(filepath.Join(repoRoot, "docs", "contract-coverage.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, gap := range recordedGaps {
		listed[gap.op+" "+gap.in+" "+gap.name] = true
	}
	rows := map[string]bool{}
	for _, row := range cov.UnsentInputs {
		key := row.Method + " " + row.Path + " " + row.In + " " + row.Name
		rows[key] = true
		// A row with no replacement path is a gap, and a gap needs a field to hold
		// it to. The resource_type / resource_id rows name the route instead.
		if row.CarriedIn == "" && row.In == "query" && !listed[key] {
			t.Errorf("unsent_inputs records `%s` with nowhere else to carry it -- a gap -- and recordedGaps does not name the field whose absence it asserts", key)
		}
	}
	for _, gap := range recordedGaps {
		key := gap.op + " " + gap.in + " " + gap.name
		if !rows[key] {
			t.Errorf("recordedGaps names `%s`, which unsent_inputs no longer lists -- drop the entry", key)
			continue
		}
		if _, has := gap.params.FieldByName(gap.field); has {
			t.Errorf("%s.%s exists, so the unsent_inputs row for `%s` records a gap that is closed -- drop the row, set the field in drivers.go, and the gate holds it from there",
				gap.params.Name(), gap.field, key)
		}
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
// refused for this Makefile by the root package's smokeprobes_test.go. The ways
// that name a target are refused here: `.IGNORE: contract-identity`, a
// target-specific variable (`contract-identity: SHELL = ...` is a second
// `contract-identity:` line, which the exactly-once count refuses), and a pattern
// rule or pattern-specific variable whose pattern matches a pinned target.
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
	for target, want := range gateRecipePins {
		got, n := makeRule(string(raw), target)
		switch {
		case n != 1:
			t.Errorf("the Makefile defines `%s:` %d times, want exactly once", target, n)
		case got != want:
			t.Errorf("the Makefile's `%s` rule changed. Re-check what gateRecipePins is pinned for, then update the pin.\n got:\n%s\nwant:\n%s", target, got, want)
		}
	}
	for _, problem := range recipeOverrideProblems(string(raw)) {
		t.Error(problem)
	}
	// And the release gate runs the three that hold: the gate's tests, the gate,
	// and the identity check.
	header, n := makeRule(string(raw), "release-check")
	if n != 1 {
		t.Fatalf("the Makefile defines `release-check:` %d times, want exactly once", n)
	}
	prereqs := strings.Fields(strings.SplitN(strings.SplitN(header, "\n", 2)[0], "##", 2)[0])
	for _, want := range []string{"contract-test", "contract-check", "contract-identity"} {
		if !contains(prereqs, want) {
			t.Errorf("release-check does not run %s", want)
		}
	}
}

// makeRule returns a rule's header line and the tab-indented recipe lines under it,
// and how many times the Makefile starts a rule for that target.
func makeRule(makefile, target string) (string, int) {
	var rule []string
	count := 0
	in := false
	for _, line := range strings.Split(makefile, "\n") {
		switch {
		case strings.HasPrefix(line, target+":"):
			count++
			in = true
			rule = append(rule, line)
		case in && strings.HasPrefix(line, "\t"):
			rule = append(rule, line)
		default:
			in = false
		}
	}
	return strings.Join(rule, "\n"), count
}

// recipeOverrideProblems reports the Makefile lines that change how a pinned
// target's recipe runs by naming the target rather than by editing its text.
func recipeOverrideProblems(makefile string) []string {
	var problems []string
	// Logical lines, as make reads them: a backslash-newline continues one.
	lines := strings.Split(strings.ReplaceAll(makefile, "\\\n", " "), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "\t") || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		head, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		head = strings.TrimSpace(head)
		if head == ".IGNORE" {
			for _, name := range strings.Fields(strings.SplitN(rest, "#", 2)[0]) {
				if _, pinned := gateRecipePins[name]; pinned {
					problems = append(problems, fmt.Sprintf("the Makefile declares .IGNORE for %s, so make ignores its recipe's failure", name))
				}
			}
			continue
		}
		for _, pattern := range strings.Fields(head) {
			if !strings.Contains(pattern, "%") {
				continue
			}
			prefix, suffix, _ := strings.Cut(pattern, "%")
			for name := range gateRecipePins {
				if strings.HasPrefix(name, prefix) && strings.HasSuffix(name, suffix) && len(name) > len(prefix)+len(suffix) {
					problems = append(problems, fmt.Sprintf("the Makefile line %q is a pattern that matches %s, so it can change how that pinned recipe runs", line, name))
				}
			}
		}
	}
	sort.Strings(problems)
	return problems
}

// TestRecipeOverrideProblemsRefusesEachForm: the overrides above, against a
// Makefile built for the purpose, and the lines they must leave alone.
func TestRecipeOverrideProblemsRefusesEachForm(t *testing.T) {
	for _, line := range []string{
		".IGNORE: contract-identity",
		".IGNORE: dev-server-down \\\n\tcontract-test",
		"contract-%: SHELL = /bin/true",
		"%-identity: MAKEFLAGS += -n",
		"%: ; @true",
	} {
		if got := recipeOverrideProblems(line + "\n"); len(got) == 0 {
			t.Errorf("%q: no problem reported", line)
		}
	}
	for _, line := range []string{
		".IGNORE: dev-server-down",
		"%.o: %.c",
		"contract-identity: ## a help string mentioning contract-% is not a pattern",
	} {
		if got := recipeOverrideProblems(line + "\n"); len(got) != 0 {
			t.Errorf("%q: unexpected problems %v", line, got)
		}
	}
}
