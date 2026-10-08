package octonomy

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// --- what runs the contract gate (#98) ---------------------------------------------
//
// The gate (tools/contractdrift) runs in CI as four `make` steps: contract-test and
// contract-check in the `test` job, contract-identity and its fixture tests in
// `compat-guard`. These tests hold those RUNNERS -- the two jobs, the trigger that
// brings a pull request into them, and the recipes behind the targets -- to what
// they were reviewed as.
//
// They live HERE, in the root package, for one reason: `go test ./...` runs them
// directly, in the required `test` jobs and the `go1.13` job, and never through a
// target they guard. They began inside the gate's own module, and a review showed
// what that bought: override contract-test's recipe with `@true` and the tests that
// would have refused the override are the ones that no longer run. A guard has to
// run outside the thing it guards.
//
// Both halves PIN rather than read. A shell, make or YAML reader never converged on
// this repository (#95): each round found one more construct. A pin refuses every
// edit, so changing a pinned job or recipe means re-checking what it is pinned for
// -- the gate runs on a pull request into this line, against the pull request's
// tree, and a failure fails the job -- and updating the pin in the same commit.
//
// WHERE IT ENDS -- limits a review classified adversarial-only, recorded so they
// are decisions rather than discoveries. Each needs an edit made to defeat the
// gate, not one made in passing:
//
//   - An EARLIER step of a pinned job can change the environment of the ones after
//     it (`echo MAKEFLAGS=-n >> "$GITHUB_ENV"`). The step's text is pinned; what its
//     shell does is not read.
//   - A workflow-level `defaults.run.shell` that ignores its script turns every
//     `run:` step into a no-op -- the `go test ./...` that runs this file included,
//     so workflowTopLevelProblems, which refuses it, never runs either. It is the
//     same self-neutralization as deleting this file.
//   - The recipes are read with `make -pq`, so MAKEFLAGS holds `pq` here and not in
//     a real run. A Makefile that defines something else only when `q` is absent
//     shows the pinned recipe to this check and runs another in CI.
//   - Extra prerequisites of a gate target are not pinned. One that rewrote the
//     checkout before the pinned recipe ran would defeat the gate on purpose.

// contractGateJobPins are ci.yml's two jobs that run the gate, as workflowJob
// (smokeprobes_test.go) reads them: comment and blank lines dropped. Pinned whole,
// so a step-level `env: {MAKEFLAGS: -n}`, a `shell:`, an `if:`, a
// `continue-on-error`, a checkout of `ref: support/go1.13` -- every way of running
// the steps against something other than the pull request, or of letting their
// failure pass -- is an edit the pin refuses. Workflow-level `env` and `defaults`,
// which reach every job without touching its text, are refused for this file by
// workflowTopLevelProblems.
var contractGateJobPins = map[string]string{
	"test": `  test:
    runs-on: ubuntu-latest
    strategy:
      fail-fast: false
      matrix:
        go-version: ["1.24", "1.25"]
    steps:
      - uses: actions/checkout@v7
      - name: Set up Go ${{ matrix.go-version }}
        uses: actions/setup-go@v7
        with:
          go-version: ${{ matrix.go-version }}
          cache: true
          cache-dependency-path: tools/contractdrift/go.sum
      - name: Build
        run: go build ./...
      - name: Build examples
        run: go build ./examples/...
      - name: Test (race + coverage)
        run: go test -race -coverprofile=coverage.out ./...
      - name: Test the contract gate
        run: make contract-test
      - name: Check the vendored contract against the SDK
        run: make contract-check`,
	"compat-guard": `  compat-guard:
    name: compat guard
    runs-on: ubuntu-latest
    timeout-minutes: 5
    steps:
      - uses: actions/checkout@v7
        with:
          fetch-depth: 0
      - name: Assert the release line's go.mod invariants
        run: make compat-guard
      - name: The guard's own tests
        run: make compat-guard-test
      - name: The contract gate's shared files match main at the pin
        run: make contract-identity
      - name: The identity check's own tests
        run: make contract-identity-test`,
}

// contractGateTriggerPin is ci.yml's `on:` block: the gate runs on a pull request
// into this line only while it names support/go1.13.
const contractGateTriggerPin = `on:
  push:
    branches: [main, support/go1.13]
    tags: ["v*"]
  pull_request:
    branches: [main, support/go1.13]`

func TestTheContractGateJobsArePinned(t *testing.T) {
	raw, err := ioutil.ReadFile(filepath.Join(".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, problem := range contractGateWorkflowProblems(string(raw)) {
		t.Error(problem)
	}
}

// contractGateWorkflowProblems holds a ci.yml to the job and trigger pins.
func contractGateWorkflowProblems(src string) []string {
	var problems []string
	for _, job := range sortedPinKeys(contractGateJobPins) {
		if got := workflowJob(src, job); got != contractGateJobPins[job] {
			problems = append(problems, fmt.Sprintf("ci.yml's `%s` job is not the pinned one. Re-check what contractGateJobPins is pinned for, then update the pin.\n--- got\n%s\n--- pinned\n%s",
				job, got, contractGateJobPins[job]))
		}
	}
	if got := workflowTrigger(src); got != contractGateTriggerPin {
		problems = append(problems, fmt.Sprintf("ci.yml's `on:` block is not the pinned one.\n--- got\n%s\n--- pinned\n%s", got, contractGateTriggerPin))
	}
	return problems
}

// workflowTrigger returns a workflow's top-level `on:` block, comment and blank
// lines dropped and trailing space trimmed, up to the next top-level key.
func workflowTrigger(src string) string {
	var out []string
	in := false
	for _, line := range strings.Split(src, "\n") {
		if isBlankOrComment(line) {
			continue
		}
		topLevel := !strings.HasPrefix(line, " ")
		switch {
		case topLevel && strings.TrimRight(line, " ") == "on:":
			in = true
		case topLevel && in:
			return strings.Join(out, "\n")
		}
		if in {
			out = append(out, strings.TrimRight(line, " \t"))
		}
	}
	return strings.Join(out, "\n")
}

// TestContractGateWorkflowProblemsRefusesEachEdit: the job and trigger pins against
// the real ci.yml with ONE edit each -- the forms a review used to neutralize the
// gate -- so passing on the real file is not their only evidence.
func TestContractGateWorkflowProblemsRefusesEachEdit(t *testing.T) {
	raw, err := ioutil.ReadFile(filepath.Join(".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	if got := contractGateWorkflowProblems(src); len(got) != 0 {
		t.Fatalf("the real ci.yml has problems, so the cases below prove nothing: %v", got)
	}
	check := "      - name: Check the vendored contract against the SDK\n        run: make contract-check\n"
	checkout := "          fetch-depth: 0\n"
	trigger := "  pull_request:\n    branches: [main, support/go1.13]\n"
	for _, anchor := range []string{check, checkout, trigger} {
		if !strings.Contains(src, anchor) {
			t.Fatalf("ci.yml no longer contains %q -- the fixtures below no longer match it", anchor)
		}
	}
	cases := []struct{ name, old, new, want string }{
		{"a dry-run MAKEFLAGS on the step", check, "      - name: Check the vendored contract against the SDK\n        env:\n          MAKEFLAGS: -n\n        run: make contract-check\n", "`test` job"},
		{"continue-on-error", check, "      - name: Check the vendored contract against the SDK\n        continue-on-error: true\n        run: make contract-check\n", "`test` job"},
		{"the step gone", check, "", "`test` job"},
		{"the guard checking out the base branch", checkout, "          fetch-depth: 0\n          ref: support/go1.13\n", "`compat-guard` job"},
		{"a shallow guard checkout", checkout, "          fetch-depth: 1\n", "`compat-guard` job"},
		{"no pull request into this line", trigger, "  pull_request:\n    branches: [main]\n", "`on:` block"},
	}
	for _, tc := range cases {
		tc := tc // Go 1.13: the loop variable is shared across iterations
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(contractGateWorkflowProblems(strings.Replace(src, tc.old, tc.new, 1)), "\n")
			if !strings.Contains(got, tc.want) {
				t.Errorf("want a problem naming %q, got:\n%s", tc.want, got)
			}
		})
	}
	// A comment is not an edit: the pins drop them, as workflowJob does.
	commented := strings.Replace(src, check, "      # a note a reviewer added\n"+check, 1)
	if got := contractGateWorkflowProblems(commented); len(got) != 0 {
		t.Errorf("a comment line changed the pinned text: %v", got)
	}
}

// --- the recipes, as make resolves them ---------------------------------------------

// contractGateRecipePins are the recipes behind the gate's targets and the release
// gate, as make's rule database prints them. CI runs `make contract-test` and the
// rest and trusts their status, so the recipe behind each target is pinned like
// the jobs: `scripts/contract-identity.sh report >/dev/null` in place of `check`
// passed every other test here.
//
// Held to make's OWN rule database (`make -pq <target>`), never to the Makefile's
// text, because a recipe is also changed from outside its text and make is the one
// reader that sees every way: a second rule, a line naming several targets at once,
// a target list in a variable, an `$(eval ...)`. And resolved under each REAL goal,
// one database per target, because the Makefile can see which goal it is asked for
// (MAKECMDGOALS) and define something else for it alone -- a review did exactly
// that against a database read under a made-up goal.
var contractGateRecipePins = map[string]string{
	"contract-check": "\t@set -e; \\\n" +
		"\tdir=$$(mktemp -d); trap 'rm -rf \"$$dir\"' EXIT; \\\n" +
		"\t(cd tools/contractdrift && go build -o \"$$dir/contractdrift\" .); \\\n" +
		"\t\"$$dir/contractdrift\" -repo . -local",
	"contract-test":          "\t@cd tools/contractdrift && go vet ./... && go test -race ./...",
	"contract-identity":      "\t@scripts/contract-identity.sh check",
	"contract-identity-test": "\t@scripts/contract-identity-test.sh",
	"contract-report":        "\t@scripts/contract-identity.sh report",
	"release-check":          "\t@echo \"release-check passed\"",
}

// contractGateMakeBaseline is what make's database says, under `make -pq` with the
// caller's make variables cleared, about the variables that decide how EVERY recipe
// runs: each one's VALUE, exactly, because it is what a recipe runs under, and it is
// what every harmful edit changes: `MAKEFLAGS += -n` leaves the origin line `#
// makefile`, as if nothing touched it, and turns the value into `npq`.
//
// Neither the origin line nor the flavor (`=` or `:=`) is compared, because the
// same untouched make spells them differently depending on the ENVIRONMENT: GNU make
// 4.3 prints `# makefile` / `SHELL = /bin/sh` when SHELL is set in the environment
// (a CI runner, a login shell) and `# default` / `SHELL := /bin/sh` when it is not
// (`env -i`, cron, a minimal container) -- an exact-line baseline failed on a
// correct tree there (PR #117's review). Neither carries meaning here: an origin-only
// edit (`override SHELL = /bin/sh`) runs exactly the baseline, and a recursive
// `SHELL = $(X)` is printed unexpanded, so its value is `$(X)` and is refused.
var contractGateMakeBaseline = map[string]string{
	"SHELL":         "/bin/sh",
	".SHELLFLAGS":   "-c",
	"MAKEFLAGS":     "pq",
	"MAKEFILES":     "",
	".RECIPEPREFIX": "",
}

func TestTheContractGateRecipesArePinned(t *testing.T) {
	problems, err := contractGateMakefileProblems("Makefile", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, problem := range problems {
		t.Error(problem)
	}
}

// contractGateMakefileProblems reads one Makefile through make, once per guarded
// goal, and holds each database to the pins. extraEnv is for the fixtures: a
// Makefile can also branch on the environment, and the real tree is read under the
// environment the test runs in -- CI's, in CI.
func contractGateMakefileProblems(makefile string, extraEnv []string) ([]string, error) {
	var problems []string
	seen := map[string]bool{}
	for _, goal := range sortedPinKeys(contractGateRecipePins) {
		db, err := makeDatabase(makefile, goal, extraEnv)
		if err != nil {
			return nil, err
		}
		for _, problem := range makeDatabaseProblems(db) {
			if !seen[problem] {
				seen[problem] = true
				problems = append(problems, fmt.Sprintf("(make %s) %s", goal, problem))
			}
		}
	}
	return problems, nil
}

// makeDatabase prints make's rule database for one goal without running the
// goal's recipes: -q only asks whether the goal is up to date. (GNU make can still
// remake a makefile that has a rule of its own under -q; this Makefile has none,
// and an include, the way one would arrive, is refused by smokeprobes_test.go.)
// The make variables a
// caller's environment can carry -- the MAKEFLAGS a parent make exports, a
// MAKEFILES naming extra makefiles -- are cleared, so the database is the
// Makefile's alone.
func makeDatabase(makefile, goal string, extraEnv []string) (string, error) {
	if _, err := exec.LookPath("make"); err != nil {
		return "", fmt.Errorf("make is not installed, and its rule database is what these tests read")
	}
	cmd := exec.Command("make", "-pq", "-f", filepath.Base(makefile), goal)
	cmd.Dir = filepath.Dir(makefile)
	cmd.Env = makeEnv(os.Environ(), extraEnv)
	out, err := cmd.CombinedOutput()
	// -q exits 1 for "not up to date", which is not a failure here; 2 is.
	exit, ok := err.(*exec.ExitError)
	notUpToDate := ok && exit.ExitCode() == 1
	if err != nil && !notUpToDate {
		return "", fmt.Errorf("make -pq %s in %s: %v\n%s", goal, cmd.Dir, err, out)
	}
	if !strings.Contains(string(out), "\n# Files\n") {
		return "", fmt.Errorf("make -pq %s printed no rule database:\n%s", goal, out)
	}
	return string(out), nil
}

// makeEnv is the environment make runs under: base, less the make variables a
// caller can carry, then extraEnv applied in order -- `NAME=value` sets NAME, a bare
// `NAME` unsets it. The fixtures need both, since make reads an unset SHELL
// differently from a set one.
func makeEnv(base, extraEnv []string) []string {
	envName := func(kv string) string { return kv[:strings.IndexByte(kv+"=", '=')] }
	var env []string
	for _, kv := range base {
		switch envName(kv) {
		case "MAKEFLAGS", "MFLAGS", "GNUMAKEFLAGS", "MAKELEVEL", "MAKEFILES", "MAKEOVERRIDES":
			continue
		}
		env = append(env, kv)
	}
	for _, kv := range extraEnv {
		kept := env[:0]
		for _, have := range env {
			if envName(have) != envName(kv) {
				kept = append(kept, have)
			}
		}
		env = kept
		if strings.Contains(kv, "=") {
			env = append(env, kv)
		}
	}
	return env
}

func TestMakeEnvClearsSetsAndUnsets(t *testing.T) {
	got := makeEnv(
		[]string{"PATH=/bin", "SHELL=/bin/bash", "MAKEFLAGS=-n", "MAKEFILES=x.mk", "HOME=/h"},
		[]string{"SHELL", "HOME=/other", "CONTRACTGATE_FIXTURE_CI=1"},
	)
	want := "PATH=/bin HOME=/other CONTRACTGATE_FIXTURE_CI=1"
	if strings.Join(got, " ") != want {
		t.Errorf("makeEnv = %v, want %s", got, want)
	}
}

// makeDatabaseProblems holds one database to the recipe pins and the baseline.
func makeDatabaseProblems(db string) []string {
	var problems []string
	guarded := func(target string) bool {
		_, pinned := contractGateRecipePins[target]
		return pinned
	}
	lines := strings.Split(db, "\n")

	// The variables that decide how every recipe runs: the value, exactly. A
	// variable's definition is the line after its origin comment.
	found := map[string]bool{}
	for i := 1; i < len(lines); i++ {
		// A multi-line value is printed as a `define NAME` ... `endef` block, and
		// is read as that NAME -- and as unreadable, below -- rather than skipped.
		name := strings.TrimPrefix(lines[i], "define ")
		if sp := strings.IndexByte(name, ' '); sp > 0 {
			name = name[:sp]
		}
		want, ok := contractGateMakeBaseline[name]
		if !ok || !strings.HasPrefix(lines[i-1], "# ") {
			continue
		}
		found[name] = true
		value, readable := makeVariableValue(lines[i], name)
		switch {
		case !readable:
			problems = append(problems, fmt.Sprintf("make prints %s as %q, which is not `%s = …` or `%s := …` -- whether it was changed cannot be read", name, lines[i], name, name))
		case value != want:
			problems = append(problems, fmt.Sprintf("make resolves %s as %q (%s), not %q -- it decides how every gate recipe runs",
				name, value, strings.TrimPrefix(lines[i-1], "# "), want))
		}
	}
	for _, name := range sortedPinKeys(contractGateMakeBaseline) {
		if !found[name] {
			problems = append(problems, fmt.Sprintf("make's database does not list %s, so whether it was changed cannot be checked", name))
		}
	}

	// Pattern-specific variables, as make indexed them.
	inPatterns := false
	for _, line := range lines {
		switch {
		case line == "# Pattern-specific Variable Values":
			inPatterns = true
		case inPatterns && strings.HasPrefix(line, "# ") && strings.Contains(line, "pattern-specific variable values"):
			inPatterns = false
		case inPatterns && strings.HasSuffix(line, " :") && !strings.HasPrefix(line, "#"):
			pattern := strings.TrimSuffix(line, " :")
			for _, target := range sortedPinKeys(contractGateRecipePins) {
				if namesTarget([]string{pattern}, target) {
					problems = append(problems, fmt.Sprintf("make holds a pattern-specific variable for %s, which reaches %s", pattern, target))
				}
			}
		}
	}

	// The rules, as make resolved them: one block per target, each with its
	// effective recipe.
	files := strings.Index(db, "\n# Files\n")
	recipes, prereqs := map[string]string{}, map[string][]string{}
	phony := map[string]bool{}
	current, inRecipe := "", false
	for _, line := range strings.Split(db[files:], "\n") {
		switch {
		case line == "":
			current, inRecipe = "", false
		case line == "#  Phony target (prerequisite of .PHONY).":
			phony[current] = current != ""
		case strings.HasPrefix(line, "#  recipe to execute"):
			inRecipe = current != ""
		case strings.HasPrefix(line, "\t") && inRecipe:
			recipes[current] += line + "\n"
		case strings.HasPrefix(line, "#"):
		default:
			colon := strings.IndexByte(line, ':')
			if colon < 0 {
				continue
			}
			target, rest := line[:colon], line[colon+1:]
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
	for _, target := range sortedPinKeys(contractGateRecipePins) {
		// Phony, or the recipe may never run at all: a target with no
		// prerequisites that is NOT phony is up to date whenever a file of its
		// name exists, and make then exits 0 having done nothing -- while printing
		// the pinned recipe here, unchanged.
		if !phony[target] {
			problems = append(problems, fmt.Sprintf("make does not hold %s as phony, so a file named %s makes it up to date and its recipe never runs", target, target))
		}
		want := contractGateRecipePins[target] + "\n"
		if got, ok := recipes[target]; !ok {
			problems = append(problems, fmt.Sprintf("make's database has no recipe for %s", target))
		} else if got != want {
			problems = append(problems, fmt.Sprintf("the recipe make will run for %s is not the pinned one:\n got:\n%s\nwant:\n%s", target, got, want))
		}
	}
	for _, want := range []string{"contract-test", "contract-check", "contract-identity"} {
		if !containsWord(prereqs["release-check"], want) {
			problems = append(problems, fmt.Sprintf("release-check does not depend on %s", want))
		}
	}
	return problems
}

// makeVariableValue reads the value out of a variable line of make's database:
// `NAME = value` (recursive, printed unexpanded) or `NAME := value` (simple). Any
// other shape is unreadable, and the caller refuses it rather than guess.
func makeVariableValue(line, name string) (string, bool) {
	rest := strings.TrimPrefix(line, name)
	for _, op := range []string{" := ", " = "} {
		if strings.HasPrefix(rest, op) {
			return rest[len(op):], true
		}
	}
	return "", false
}

// TestTheBaselineHoldsWhateverMakesEnvironment: the real Makefile, read with SHELL
// unset and with it set -- the two environments in which GNU make spells its
// untouched SHELL differently -- is clean in both, and the value reader takes both
// spellings and refuses what is neither.
func TestTheBaselineHoldsWhateverMakesEnvironment(t *testing.T) {
	for _, env := range [][]string{{"SHELL"}, {"SHELL=/bin/bash"}, {"SHELL="}} {
		problems, err := contractGateMakefileProblems("Makefile", env)
		if err != nil {
			t.Fatal(err)
		}
		if len(problems) != 0 {
			t.Errorf("environment %v: the real Makefile reads as changed: %v", env, problems)
		}
	}
	for line, want := range map[string]string{"SHELL = /bin/sh": "/bin/sh", "SHELL := /bin/sh": "/bin/sh", "MAKEFILES := ": ""} {
		name := line[:strings.IndexByte(line, ' ')]
		if got, ok := makeVariableValue(line, name); !ok || got != want {
			t.Errorf("makeVariableValue(%q) = %q, %v; want %q", line, got, ok, want)
		}
	}
	for _, line := range []string{"SHELL += /bin/sh", "SHELL ?= /bin/sh", "SHELL=/bin/sh", "SHELLX = /bin/sh"} {
		if got, ok := makeVariableValue(line, "SHELL"); ok {
			t.Errorf("makeVariableValue(%q) read %q; a shape it does not know must be refused", line, got)
		}
	}
}

// TestContractGateMakefileProblemsRefusesEachOverride: every way to make a gate
// target, or the release gate, run something other than its pin, each as ONE edit
// of the real Makefile read back through make -- literal and computed, goal- and
// environment-dependent -- and the edits it must leave alone.
func TestContractGateMakefileProblemsRefusesEachOverride(t *testing.T) {
	raw, err := ioutil.ReadFile("Makefile")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	const identityRecipe = "\t@scripts/contract-identity.sh check\n"
	if !strings.Contains(src, identityRecipe) {
		t.Fatal("the Makefile no longer contains the identity recipe a fixture edits")
	}
	dir, err := ioutil.TempDir("", "contractgate-makefile")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path := filepath.Join(dir, "Makefile")

	cases := []struct {
		name, makefile, want string
		env                  []string
	}{
		{"the recipe edited", strings.Replace(src, identityRecipe, "\t@scripts/contract-identity.sh report >/dev/null\n", 1), "the recipe make will run for contract-identity", nil},
		{"a multi-target rule", src + "\nnoop contract-check contract-test contract-identity contract-identity-test release-check: ; @true\n", "the recipe make will run for contract-check", nil},
		{"a target list in a variable", src + "\ngate := contract-check contract-test contract-identity contract-identity-test contract-report release-check\n$(gate): ; @true\n", "the recipe make will run for contract-test", nil},
		{"a rule from eval", src + "\n$(eval contract-test: ; @true)\n", "the recipe make will run for contract-test", nil},
		{"a rule only for its own goal", src + "\nifneq (,$(filter contract-check,$(MAKECMDGOALS)))\ncontract-check: ; @true\nendif\n", "(make contract-check) the recipe make will run for contract-check", nil},
		{"a rule only in CI", src + "\nifdef CONTRACTGATE_FIXTURE_CI\ncontract-test: ; @true\nendif\n", "the recipe make will run for contract-test", []string{"CONTRACTGATE_FIXTURE_CI=1"}},
		{"a target-specific variable", src + "\ncontract-test: MAKEFLAGS += -n\n", "target-specific variable for contract-test", nil},
		{"a computed target-specific variable", src + "\nt := contract-identity\n$(t): MAKEFLAGS += -n\n", "target-specific variable for contract-identity", nil},
		{"a pattern-specific variable", src + "\ncontract-%: SHELL = /bin/true\n", "pattern-specific variable for contract-%", nil},
		{"a computed pattern-specific variable", src + "\np := contract-%\n$(p): SHELL = /bin/true\n", "pattern-specific variable for contract-%", nil},
		{"a .IGNORE naming a gate target", src + "\n.IGNORE: dev-server-down \\\n\tcontract-test\n", ".IGNORE for contract-test", nil},
		{"a computed .IGNORE", src + "\ni := .IGNORE\n$(i): contract-test\n", ".IGNORE for contract-test", nil},
		{"a computed bare .IGNORE", src + "\ni := .IGNORE\n$(i):\n", ".IGNORE for every target", nil},
		{"a computed .ONESHELL", src + "\no := .ONESHELL\n$(o):\n", ".ONESHELL", nil},
		{"SHELL set by the Makefile", src + "\nSHELL := /bin/true\n", "make resolves SHELL", nil},
		{"SHELL by override, through eval", src + "\n$(eval override SHELL := /bin/true)\n", "make resolves SHELL", nil},
		{"a dry-run MAKEFLAGS", src + "\nMAKEFLAGS += -n\n", "make resolves MAKEFLAGS", nil},
		{"a different recipe prefix", src + "\n.RECIPEPREFIX := >\n", "make resolves .RECIPEPREFIX", nil},
		{"a multi-line SHELL", src + "\ndefine SHELL\n/bin/true\nexit 0\nendef\n", "make prints SHELL as", nil},
		{"the release gate overridden", src + "\nr := release-check\n$(r): ; @true\n", "the recipe make will run for release-check", nil},
		{"the release gate dropping the identity check", strings.Replace(src, " contract-identity ## Full pre-release gate", " ## Full pre-release gate", 1), "release-check does not depend on contract-identity", nil},
		{"a gate target no longer phony", strings.Replace(src, " contract-check contract-test", " contract-test", 1), "does not hold contract-check as phony", nil},
	}
	for _, tc := range cases {
		tc := tc // Go 1.13: the loop variable is shared across iterations
		t.Run(tc.name, func(t *testing.T) {
			if tc.makefile == src {
				t.Fatal("the fixture's edit did not apply")
			}
			if err := ioutil.WriteFile(path, []byte(tc.makefile), 0o644); err != nil {
				t.Fatal(err)
			}
			// A file named for each gate target, beside the fixture's Makefile: a
			// target that is not phony is then up to date, as it would be in a
			// checkout carrying such a file, and the real tree is what is held.
			for _, target := range sortedPinKeys(contractGateRecipePins) {
				if err := ioutil.WriteFile(filepath.Join(dir, target), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			problems, err := contractGateMakefileProblems(path, tc.env)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(problems, "\n"); !strings.Contains(got, tc.want) {
				t.Errorf("want a problem containing %q, got:\n%s", tc.want, got)
			}
		})
	}
	// What must stay quiet: a .IGNORE for something else, a pattern rule no
	// explicit gate rule uses, and the environment of a fixture that sets nothing
	// the Makefile reads.
	quiet := src + "\n.IGNORE: dev-server-down\n%.o: %.c\n\t@true\n%:\n\t@true\n"
	if err := ioutil.WriteFile(path, []byte(quiet), 0o644); err != nil {
		t.Fatal(err)
	}
	problems, err := contractGateMakefileProblems(path, []string{"CONTRACTGATE_FIXTURE_CI=1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 {
		t.Errorf("unexpected problems: %v", problems)
	}
}

func sortedPinKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func containsWord(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
