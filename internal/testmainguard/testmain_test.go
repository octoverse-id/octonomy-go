// Package testmainguard holds one check, in a package of its own because of
// what it checks: a TestMain decides its own test binary's exit status before
// any test in that binary runs. A guard in the root package would be the first
// thing an `os.Exit(0)` TestMain there skipped, with `go test` reporting ok.
// Here it is a separate binary that no TestMain in the root package reaches.
package testmainguard

// A TestMain on this line can turn a failing test binary green, and nothing in
// a go1.13 build or vet says so (#95).
//
// Before Go 1.15, the test main the go command generates does not call os.Exit
// after a TestMain returns. So the idiom main may use freely --
//
//	func TestMain(m *testing.M) {
//		setup()
//		m.Run()
//	}
//
// -- exits 0 on go1.13.15 whatever m.Run returned, and `go test` prints ok over
// failing tests. Go 1.15 started exiting with m.Run's code itself. The other
// shape that hides a failure is version-independent: a TestMain that exits 0
// on its own, `if os.Getenv(…) == "" { os.Exit(0) }`, which in the smoke file
// would turn a required run with no harness into a pass.
//
// staticcheck's SA3000 catches the first shape under this module's `go 1.13`
// directive, in the lint job; it does not catch the second. So the rule here is
// the one that covers both: a TestMain in any test file in the repository, the
// integration-tagged ones included, must be exactly `os.Exit(m.Run())`. Setup
// that has to happen first belongs in a helper called from the tests, or in a
// TestMain that is rewritten under review with this check updated.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/ioutil"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// repoRoot is the repository root, relative to this package's directory, which
// is where go test runs it.
var repoRoot = filepath.Join("..", "..")

// The rule is the repository's (AGENTS.md), so the directories are DISCOVERED
// rather than listed. A list of the two test packages that existed when this
// was written would leave the next one -- the contract gate #98 ported, say --
// outside the check with nothing saying so, which is the silent shrinking every
// other guard here refuses. A nested module is walked too: an early
// os.Exit(0) hides failures on any Go version, whichever go.mod runs it.
func TestNoTestMainHidesAFailure(t *testing.T) {
	paths, err := testFiles(repoRoot)
	if err != nil {
		t.Fatalf("walk %s: %v", repoRoot, err)
	}
	// The floor: the walk must reach the root package and this one, or it
	// is not reading the repository at all.
	for _, want := range []string{"smokeprobes_test.go", filepath.Join("internal", "testmainguard", "testmain_test.go")} {
		if !containsPath(paths, filepath.Join(repoRoot, want)) {
			t.Fatalf("the walk did not reach %s; the guard is not reading the repository (found %d files)", want, len(paths))
		}
	}
	fset := token.NewFileSet()
	for _, path := range paths {
		problems, err := testFileProblems(fset, repoRoot, path)
		if err != nil {
			t.Fatal(err)
		}
		for _, why := range problems {
			t.Errorf("%s: %s", path, why)
		}
	}
}

// testFileProblems parses one test file and reports its TestMain problems.
//
// A file in a NESTED module may be written for a newer Go than the toolchain
// running this -- tools/contractdrift is go 1.24 (#98), and go1.13.15's parser
// cannot read a type parameter -- so failing there would let code that is legal
// in its own module turn the go1.13 job red. Such a file gets a lexical check
// instead: it may not declare a TestMain at all, since one this toolchain cannot
// parse is one whose shape it cannot verify. That fails closed, and only for the
// declaration the rule is about. A file of THIS module that does not parse is
// still an error: the go1.13 job builds every one of them anyway.
func testFileProblems(fset *token.FileSet, root, path string) ([]string, error) {
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err == nil {
		return testMainProblems(file), nil
	}
	if !inNestedModule(root, path) {
		return nil, fmt.Errorf("parse %s: %v", path, err)
	}
	src, readErr := ioutil.ReadFile(path)
	if readErr != nil {
		return nil, readErr
	}
	if declaresTestMain.Match(src) {
		return []string{"declares a TestMain this toolchain cannot parse (" + err.Error() + "), so its shape " +
			"cannot be checked: it must be exactly os.Exit(m.Run()) and readable here, or not exist"}, nil
	}
	return nil, nil
}

// declaresTestMain matches a top-level TestMain declaration, lexically.
var declaresTestMain = regexp.MustCompile(`(?m)^func[ \t]+TestMain[ \t]*\(`)

// inNestedModule reports whether a directory between path and root, root
// excluded, holds its own go.mod.
func inNestedModule(root, path string) bool {
	root = filepath.Clean(root)
	for dir := filepath.Dir(path); dir != root && dir != "." && dir != string(filepath.Separator); dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return true
		}
	}
	return false
}

// TestUnparseableNestedTestFilesAreReadLexically: a nested module's test file in
// syntax this toolchain cannot parse passes when it declares no TestMain and is
// refused when it does; a file of the root module that does not parse is still an
// error. Each fixture needs a type parameter only on a toolchain that lacks them,
// so each also carries a syntax error, and the outcome is the same on every Go.
func TestUnparseableNestedTestFilesAreReadLexically(t *testing.T) {
	root, err := ioutil.TempDir("", "testmainguard-nested")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(root) }()
	files := map[string]string{
		"go.mod":                    "module example.test/m\n",
		"tools/gate/go.mod":         "module example.test/gate\n",
		"tools/gate/clean_test.go":  "package main\n\nfunc keys[V any](m map[string]V) {}\nfunc ( {\n",
		"tools/gate/main_test.go":   "package main\n\nfunc keys[V any]() {}\nfunc ( {\n\nfunc TestMain(m *testing.M) { m.Run() }\n",
		"root_broken_test.go":       "package m\n\nfunc ( {\n",
		"tools/gate/sub/ok_test.go": "package sub\n\nfunc ( {\n",
	}
	for rel, body := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := ioutil.WriteFile(path, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	fset := token.NewFileSet()
	at := func(rel string) string { return filepath.Join(root, filepath.FromSlash(rel)) }

	for _, rel := range []string{"tools/gate/clean_test.go", "tools/gate/sub/ok_test.go"} {
		if problems, err := testFileProblems(fset, root, at(rel)); err != nil || len(problems) != 0 {
			t.Errorf("%s: an unparseable nested file with no TestMain should pass, got %v / %v", rel, problems, err)
		}
	}
	if problems, err := testFileProblems(fset, root, at("tools/gate/main_test.go")); err != nil || len(problems) != 1 {
		t.Errorf("an unparseable nested file declaring TestMain should be refused, got %v / %v", problems, err)
	}
	if _, err := testFileProblems(fset, root, at("root_broken_test.go")); err == nil {
		t.Error("a root-module test file that does not parse should be an error, not a lexical pass")
	}
}

// testFiles returns every *_test.go file under root, sorted, skipping what the
// go command skips -- directories named testdata or vendor, or starting with
// "." or "_" -- which also keeps out .git and any agent worktree checked out
// under .claude.
func testFiles(root string) ([]string, error) {
	var out []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		name := info.Name()
		if info.IsDir() {
			if path != root && (name == "testdata" || name == "vendor" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(name, "_test.go") {
			out = append(out, path)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

func containsPath(paths []string, want string) bool {
	for _, p := range paths {
		if p == want {
			return true
		}
	}
	return false
}

// The walk is the guard's scope, so it is held to finding a test package
// wherever one is added, nested module included, and to skipping only what the
// go command skips.
func TestTestFilesFindsEveryTestPackage(t *testing.T) {
	root, err := ioutil.TempDir("", "testmainguard")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(root) }()
	for _, f := range []string{
		"a_test.go",
		"internal/x/b_test.go",
		"tools/contractdrift/drift_test.go", // a nested module, on main and here (#98)
		"tools/contractdrift/go.mod",
		"pkg/plain.go",
		".claude/worktrees/agent/c_test.go",
		"pkg/testdata/d_test.go",
		"vendor/e_test.go",
		"_scratch/f_test.go",
	} {
		path := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := ioutil.WriteFile(path, []byte("package p\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := testFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	var rel []string
	for _, p := range got {
		r, _ := filepath.Rel(root, p)
		rel = append(rel, filepath.ToSlash(r))
	}
	want := "a_test.go internal/x/b_test.go tools/contractdrift/drift_test.go"
	if strings.Join(rel, " ") != want {
		t.Errorf("testFiles = %v, want %s", rel, want)
	}
}

// testMainProblems reports a TestMain whose body is anything but
// `os.Exit(m.Run())`.
func testMainProblems(file *ast.File) []string {
	var problems []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Name.Name != "TestMain" || fn.Body == nil {
			continue
		}
		if !exitsWithRun(fn) || !importsOS(file) {
			problems = append(problems, "TestMain must be exactly os.Exit(m.Run()): before Go 1.15 a "+
				"TestMain that returns exits 0 over failing tests, and one that calls os.Exit itself can "+
				"exit 0 before they run")
		}
	}
	return problems
}

// importsOS reports whether the file imports the os package under the name os.
// The check above reads the spelling `os.Exit`; in a file that does not import
// os, that name could be a package-level fake whose Exit returns, and on Go
// 1.13 a TestMain that returns exits 0.
func importsOS(file *ast.File) bool {
	for _, imp := range file.Imports {
		if imp.Path.Value == `"os"` && (imp.Name == nil || imp.Name.Name == "os") {
			return true
		}
	}
	return false
}

// exitsWithRun reports whether fn's whole body is `os.Exit(<m>.Run())`, with
// <m> its *testing.M parameter.
func exitsWithRun(fn *ast.FuncDecl) bool {
	if fn.Type.Params == nil || len(fn.Type.Params.List) != 1 || len(fn.Type.Params.List[0].Names) != 1 {
		return false
	}
	m := fn.Type.Params.List[0].Names[0].Name
	if len(fn.Body.List) != 1 {
		return false
	}
	stmt, ok := fn.Body.List[0].(*ast.ExprStmt)
	if !ok {
		return false
	}
	exit, ok := unparen(stmt.X).(*ast.CallExpr)
	if !ok || len(exit.Args) != 1 {
		return false
	}
	if sel, ok := unparen(exit.Fun).(*ast.SelectorExpr); !ok || !isIdent(sel.X, "os") || sel.Sel.Name != "Exit" {
		return false
	}
	run, ok := unparen(exit.Args[0]).(*ast.CallExpr)
	if !ok || len(run.Args) != 0 {
		return false
	}
	sel, ok := unparen(run.Fun).(*ast.SelectorExpr)
	return ok && isIdent(sel.X, m) && sel.Sel.Name == "Run"
}

// The guard reads the spelling os.Exit, so it also needs os to BE the os
// package: a file that does not import it could declare a fake whose Exit
// returns.
func TestTestMainProblemsRequiresTheRealOS(t *testing.T) {
	src := "package octonomy_test\ntype fakeOS struct{}\nfunc (fakeOS) Exit(int) {}\nvar os fakeOS\nfunc TestMain(m *testing.M) {\n\tos.Exit(m.Run())\n}\n"
	file, err := parser.ParseFile(token.NewFileSet(), "fixture_test.go", src, 0)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if len(testMainProblems(file)) == 0 {
		t.Error("a TestMain calling a fake os.Exit was accepted")
	}
}

func TestTestMainProblemsAcceptsOnlyTheExitingShape(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		ok         bool
	}{
		{"the exiting shape", "os.Exit(m.Run())", true},
		{"no TestMain at all", "", true},
		{"a TestMain that returns", "setup()\n\tm.Run()", false},
		{"one that defers and returns", `defer fmt.Println("done")` + "\n\tm.Run()", false},
		{"one that exits 0 early", `if os.Getenv("OCTONOMY_TEST_BASE_URL") == "" { os.Exit(0) }` + "\n\tos.Exit(m.Run())", false},
		{"one that exits with a constant", "m.Run()\n\tos.Exit(0)", false},
		{"one that runs another M", "os.Exit(other.Run())", false},
		{"parentheses do not hide the shape", "(os.Exit)((m.Run()))", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := "package octonomy_test\nimport \"os\"\n"
			if tc.body != "" {
				src += "func TestMain(m *testing.M) {\n\t" + tc.body + "\n}\n"
			}
			file, err := parser.ParseFile(token.NewFileSet(), "fixture_test.go", src, 0)
			if err != nil {
				t.Fatalf("parse fixture: %v", err)
			}
			if got := len(testMainProblems(file)) == 0; got != tc.ok {
				t.Errorf("accepted = %v, want %v for:\n%s", got, tc.ok, strings.TrimSpace(src))
			}
		})
	}
}

// unparen and isIdent are the root package's sourceguard_test.go helpers,
// repeated here because this package cannot import a test file.
func unparen(e ast.Expr) ast.Expr {
	for {
		p, ok := e.(*ast.ParenExpr)
		if !ok {
			return e
		}
		e = p.X
	}
}

func isIdent(expr ast.Expr, name string) bool {
	ident, ok := unparen(expr).(*ast.Ident)
	return ok && ident.Name == name
}
