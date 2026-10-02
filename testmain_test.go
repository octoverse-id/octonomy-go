package octonomy

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
// the one that covers both: a TestMain in this directory's test files, the
// integration-tagged ones included, must be exactly `os.Exit(m.Run())`. Setup
// that has to happen first belongs in a helper called from the tests, or in a
// TestMain that is rewritten under review with this check updated.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

func TestNoTestMainHidesAFailure(t *testing.T) {
	paths, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	fset := token.NewFileSet()
	for _, path := range paths {
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, why := range testMainProblems(file) {
			t.Errorf("%s: %s", path, why)
		}
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
		if !exitsWithRun(fn) {
			problems = append(problems, "TestMain must be exactly os.Exit(m.Run()): before Go 1.15 a "+
				"TestMain that returns exits 0 over failing tests, and one that calls os.Exit itself can "+
				"exit 0 before they run")
		}
	}
	return problems
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
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := "package octonomy_test\n"
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
