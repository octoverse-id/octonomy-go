package octonomy

// A REWRITE of main's smokeprobes_test.go at 5e40964 (#95; the disposition
// table is docs/compat-test-disposition.md). Every mention of main in this file
// means main at that commit. The guard is main's -- every
// response type this SDK decodes is asserted against a real server -- and two
// things about it are this line's:
//
//   - The response types come from responseTypes (sourceguard_test.go), which
//     reads `&out` destinations rather than doData[T] type arguments, and each
//     method's types from decodedTypes below, which does the same.
//   - WHAT COUNTS AS A PROBE. main's smoke walk is a registry, smokeProbes(),
//     with one entry per response type and a client handed to each entry's
//     closure, and main's guard binds an entry to the calls its closure makes
//     on that parameter. This line's walk is five TestSmoke_ functions, each
//     building its own clients on one of two API versions -- /api/v1 by
//     default, /api/v2 opt-in for the namespace pair -- so there is no table to
//     read. The guard binds the same thing
//     directly: a response type is probed when a TestSmoke_ function calls a
//     client method that decodes it, on a client that function built. That is
//     main's check 1 and check 4 taken together (an entry keyed T, whose
//     closure calls a method that decodes T), without a key to keep in step.
//
// What it gives up is main's checks 2 and 4 as SEPARATE failures -- an entry
// naming a dead type, an entry keyed for one type that calls another -- which
// exist only because a registry has keys. A key that cannot drift cannot be
// stale. What it must NOT give up is main's walk failing an entry that skips
// itself, since a skipped probe is a green run that asserted one shape fewer
// than it claims; with no walk to fail it at runtime, the guard refuses a skip
// -- and an early return -- in a TestSmoke_ function statically instead.
//
// A credited call is worth something only if a runner executes it, so the two
// runners are pinned at the bottom of this file; TestMain, which can decide a
// test binary's exit status on its own, is held by internal/testmainguard, in a
// test binary of its own.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/ioutil"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// integrationSmokeFile holds the smoke tests this guard reads. It is read as
// SOURCE rather than linked, which is what lets a plain unit test check a file
// that lives behind the `integration` build tag.
const integrationSmokeFile = "integration_test.go"

// smokeTestPrefix is what makes a function part of the smoke run: `make smoke`
// and the go1.13 smoke job both select `-run '^TestSmoke_'`, and
// TestSmokeSelectorRunsEveryTestSmokeFunction holds them to it. A call in a
// function the selector does not run is not a probe, however real it looks.
const smokeTestPrefix = "TestSmoke_"

// sdkImportPath is how the smoke file, an external test package, imports this
// one.
const sdkImportPath = "github.com/octoverse-id/octonomy-go"

// smokeProbeExclusions names every response type that deliberately carries no
// smoke probe, with the reason it is not a gap.
//
// IT IS EMPTY, and that is the state to keep it in, as on main. Every response
// type this SDK decodes is asserted against the real server today; the map
// exists because the alternative to a reviewed escape hatch is an unreviewed
// one. A name listed here must still be a live response type, the reason must
// not be blank, and it must not also be probed.
var smokeProbeExclusions = map[string]string{}

// Every response type this SDK decodes needs a smoke probe.
//
// `make smoke` is the only check that sees the server's real response shapes. A
// unit suite asserts the client against fixtures this repository wrote, so it
// cannot see a fixture-versus-server divergence -- which is #32, where every
// single-resource read decoded to an empty struct and a complete unit suite
// stayed green. The smoke run closed that class, and it closed it only for the
// shapes somebody remembered to call: a resource added without touching
// integration_test.go left #32's class unguarded for itself with every job
// green.
//
// IT CARRIES NO `integration` BUILD TAG, DELIBERATELY. The file it checks lives
// behind that tag, but parsing it needs no container, and a guard that ran only
// when the container did would be absent from exactly the pull request that adds
// a resource.
//
// WHERE THIS CHECK ENDS. It is syntactic, and a check nobody knows the edge of is
// worse than none, so the edges are these.
//
//   - IT DOES NOT PROVE AN ASSERTION IS MEANINGFUL. A call whose result is
//     thrown away passes. What it prevents is the SILENT omission -- a new
//     response type no smoke test calls anything for -- which is the failure
//     that actually happens. The assertions are a reviewer's job.
//   - It reads presence, not reachability: a call parked under `if false` is
//     credited, as is one after a t.Fatal that always fires. The two
//     unreachabilities it refuses are the GREEN ones. A skip: a Skip call
//     anywhere in a TestSmoke_ function, a call to a helper in the smoke file
//     that can skip, and the function's *testing.T handed to anything the guard
//     cannot see into, which could skip too. And an early return, the skip
//     ban's obvious workaround. The only skip the smoke run has is
//     newSmokeClient's, and it is exempt only in the shape checkSmokeGate holds
//     it to -- one skip, straight after an `if required { t.Fatal(…) }` read
//     from OCTONOMY_SMOKE_REQUIRED -- with the pinned CI job setting it to 1.
//   - It binds a type to SOME method that decodes it, not to every one. A Tag
//     probed through Tags.Get says nothing about Tags.Update, though both decode
//     a Tag through doData. A LIST ENVELOPE is a type of its own on this line --
//     TagList, not main's List[Tag] -- decoded through doList rather than
//     doData, so each one is in the required set too and needs a list method
//     probed: a Tag probed through Tags.Get alone says nothing about the
//     {data, pagination} envelope Tags.List decodes.
//   - It counts a call only on a client its own TestSmoke_ function built --
//     from newSmokeClient, or from the SDK's New -- and only outside function
//     literals, even a deferred one that does run: a closure's body is not
//     read at all. The closures this file defers are cleanup -- Delete calls,
//     and one ReplaceTags emptying a resource -- whose decodes are credited
//     elsewhere or not needed, and a probe moved into a t.Run closure or a
//     helper is reported as missing rather than credited. That is the
//     fail-closed direction.
//   - A response type is one handed to doData or doList. HealthStatus is decoded
//     by health.go's own probe helper and is not one, which is why the health
//     smoke test is not part of the required set.
//   - It resolves a method's response types through calls WITHIN this package
//     and not into a closure; a method that reached doData some other way
//     resolves to no type, and its type is then reported as produced by nothing.
func TestEveryResponseTypeHasASmokeProbe(t *testing.T) {
	files := parsePackageSource(t)

	// The required set, derived exactly as TestEveryResponseTypeCanRefuseAnEmptyDecode
	// derives it.
	required, lists, _ := responseTypes(files)
	for list, row := range lists {
		required[list] = "the list envelope of " + row
	}

	// And who produces each one, spelled the way a smoke test calls it
	// ("Tags.Get"): a service reachable from no Client field is not part of the
	// surface a caller has.
	producers := responseTypeProducers(files)

	calls, problems := parseSmokeCalls(t, integrationSmokeFile)
	for _, p := range problems {
		t.Errorf("%s: %s", integrationSmokeFile, p)
	}
	if len(calls) == 0 {
		t.Fatalf("no client calls parsed out of %s's %s functions -- the guard is not reading the smoke tests",
			integrationSmokeFile, smokeTestPrefix)
	}

	// 1. The guard proper: a response type no smoke test calls anything for.
	for _, name := range stringKeys(required) {
		if smokeProbeExclusions[name] != "" {
			continue
		}
		produced := producers[name]
		if len(produced) == 0 {
			continue // reported by check 2
		}
		if probedBy(produced, calls) == "" {
			t.Errorf("%s is decoded from a response (%s) and no %s function in %s calls a method "+
				"that decodes it, so no check compares its shape against a real server. A unit "+
				"fixture cannot: it is written by the same hand as the decoder, which is how #32 "+
				"survived a complete suite. Call one of %v on a smoke client, or add %q to "+
				"smokeProbeExclusions with the reason a real server cannot settle its shape",
				name, required[name], smokeTestPrefix, integrationSmokeFile, sortedKeys(produced), name)
		}
	}

	// 2. A response type nothing produces. Check 1 would be unsatisfiable, so
	// this must not read as "no probe needed" -- and the two causes are
	// opposite, so the message names both rather than guessing.
	for _, name := range stringKeys(required) {
		if len(producers[name]) == 0 {
			t.Errorf("%s is a response type (%s) and no exported method on a service wired to "+
				"Client decodes it. Either the service is not on Client -- which makes the type "+
				"unreachable from a caller and unassertable from a smoke test -- or the guard cannot "+
				"read the shape that reaches doData/doList there. Neither may pass as \"no probe needed\"",
				name, required[name])
		}
	}

	// 3. An exclusion that no longer names a response type, or that nobody gave
	// a reason for, is a hole with a note over it.
	for _, name := range stringKeys(smokeProbeExclusions) {
		if _, live := required[name]; !live {
			t.Errorf("smokeProbeExclusions lists %q, which is not a response type. Drop the entry "+
				"-- an exclusion for a type that does not exist would also excuse a future one "+
				"that takes the name", name)
		}
		if strings.TrimSpace(smokeProbeExclusions[name]) == "" {
			t.Errorf("smokeProbeExclusions[%q] has a blank reason. An exclusion is a claim that a "+
				"real server cannot settle a shape; an unargued one is the silence this test "+
				"replaces", name)
		}
		if probedBy(producers[name], calls) != "" {
			t.Errorf("smokeProbeExclusions lists %q, which is also probed. One of the two is "+
				"wrong, and leaving both means that deleting the probe later would silently fall "+
				"back on the exclusion instead of failing", name)
		}
	}

	// The floor: a derivation that silently stopped finding response types would
	// satisfy every loop above by finding nothing at all. It is the count
	// TestEveryResponseTypeCanRefuseAnEmptyDecode holds, because it is the same
	// set.
	if want := knownResponseTypes + knownListTypes; len(required) < want {
		t.Errorf("found %d response type(s) and list envelope(s), want at least the %d that exist -- the "+
			"guard is not reading the source. Found: %v", len(required), want, stringKeys(required))
	}
}

// probedBy returns the smoke test that calls one of produced, or "".
func probedBy(produced map[string]bool, calls map[string]string) string {
	for _, method := range sortedKeys(produced) {
		if fn, ok := calls[method]; ok {
			return fn
		}
	}
	return ""
}

// smokeTarget is the make target AGENTS.md and docs/development.md tell a
// contributor to run.
const smokeTarget = "smoke"

// The other half of the same binding: the runners select `-tags=integration`,
// so the smoke file must be built under that tag -- and under BOTH constraint
// spellings, since Go 1.13 reads only `// +build`. A file retagged `smoke`
// still parses, so every probe in it stays credited, while `go test
// -tags=integration -run '^TestSmoke_'` reports "no tests to run" and passes.
func TestSmokeFileCarriesTheTagTheRunnersSelect(t *testing.T) {
	raw, err := ioutil.ReadFile(integrationSmokeFile)
	if err != nil {
		t.Fatalf("read %s: %v", integrationSmokeFile, err)
	}
	for _, problem := range smokeBuildTagProblems(string(raw)) {
		t.Errorf("%s: %s", integrationSmokeFile, problem)
	}
}

// smokeBuildTagProblems checks the constraint lines above the package clause.
func smokeBuildTagProblems(src string) []string {
	want := map[string]bool{"//go:build integration": false, "// +build integration": false}
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "package ") {
			break
		}
		if _, ok := want[trimmed]; ok {
			want[trimmed] = true
			continue
		}
		if strings.HasPrefix(trimmed, "//go:build") || strings.HasPrefix(trimmed, "// +build") {
			return []string{"carries the constraint " + strconv.Quote(trimmed) + "; the smoke runners select " +
				"-tags=integration, and any other constraint builds the file into a different set of runs"}
		}
	}
	var problems []string
	for _, line := range []string{"//go:build integration", "// +build integration"} {
		if !want[line] {
			problems = append(problems, "has no "+strconv.Quote(line)+" line above its package clause, so "+
				"the -tags=integration runs do not build the smoke tests the guard credits")
		}
	}
	return problems
}

// logicalLine is one shell line after its `\` continuations are joined, with
// the physical line it starts on.
type logicalLine struct {
	line int
	text string
}

func logicalLines(src string) []logicalLine {
	var out []logicalLine
	physical := strings.Split(src, "\n")
	for i := 0; i < len(physical); i++ {
		start, text := i+1, physical[i]
		for strings.HasSuffix(strings.TrimRight(text, " \t"), "\\") && i+1 < len(physical) {
			text = strings.TrimSuffix(strings.TrimRight(text, " \t"), "\\") + " " + physical[i+1]
			i++
		}
		out = append(out, logicalLine{line: start, text: text})
	}
	return out
}

// --- what the smoke tests call ------------------------------------------------

// parseSmokeCalls reads the smoke file and returns every client method its
// TestSmoke_ functions call ("Tags.Get"), mapped to the first function that
// calls it, and every shape it refused to read.
func parseSmokeCalls(t *testing.T, path string) (map[string]string, []string) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return smokeCallsIn(file)
}

// smokeCallsIn is parseSmokeCalls over an already-parsed file, so the fixtures
// below can drive it from a source string.
func smokeCallsIn(file *ast.File) (map[string]string, []string) {
	calls := map[string]string{}
	var problems []string

	sdk := importName(file, sdkImportPath)
	if sdk == "" {
		return calls, []string{"does not import " + sdkImportPath + ", so no call in it can be on a client"}
	}
	helpers := readSmokeHelpers(file)
	if why := checkSmokeConstructor(file, sdk, helpers); why != "" {
		problems = append(problems, why)
	}

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Body == nil || !strings.HasPrefix(fn.Name.Name, smokeTestPrefix) {
			continue
		}
		clients := smokeClients(fn.Body, sdk)
		// The constructors are trusted by NAME, so a local binding of either
		// name -- `newSmokeClient := func(…) *fake`, `octonomy := fake{}` --
		// would hand a fake to every call counted on it. Refuse the function's
		// clients rather than resolve which one a call reaches.
		for _, name := range []string{"newSmokeClient", sdk} {
			if len(declarationsOf(name, fn)) > 0 {
				problems = append(problems, fn.Name.Name+" declares a local "+name+", which shadows the "+
					"constructor the guard trusts by name; its calls are not counted")
				clients = map[string]bool{}
			}
		}
		for _, name := range sortedKeys(clients) {
			// The client's own binding is one; any other declaration or
			// assignment of the name means it may hold something else by the
			// time a call reads it, and this parser compares spelling rather
			// than resolving scope. Its calls are not counted, and the type
			// they would have probed is then reported as unprobed.
			if n := len(bindingsOf(fn.Body, name)); n != 1 {
				problems = append(problems, fn.Name.Name+" binds its client "+name+" "+strconv.Itoa(n)+
					" times; a rebound client's calls are not counted, since the name may no longer hold a client")
				delete(clients, name)
			}
		}
		// A skip anywhere in the function -- in its body, in a subtest's
		// closure, or in a helper it hands its *testing.T to -- turns the calls
		// after it into a green run that asserted nothing, while this guard
		// still credits them.
		for _, why := range skipsIn(fn, helpers) {
			problems = append(problems, fn.Name.Name+" "+why+"; a smoke test that can skip itself is a green "+
				"run that probed nothing after the skip, and the guard would still credit those calls. Fail "+
				"instead -- newSmokeClient is the one place the smoke run may skip")
		}
		// An early return is the skip ban's obvious workaround: the calls after
		// it stay credited and the run is green. No smoke test needs one -- a
		// precondition that does not hold is a t.Fatal -- so any return in the
		// function's own body, outside closures, is refused.
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if _, ok := n.(*ast.FuncLit); ok {
				return false
			}
			if _, ok := n.(*ast.ReturnStmt); ok {
				problems = append(problems, fn.Name.Name+" returns early, which leaves the calls after the "+
					"return credited while the run stays green; fail with t.Fatal instead")
			}
			return true
		})
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if _, ok := n.(*ast.FuncLit); ok {
				return false
			}
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			outer, ok := unparen(call.Fun).(*ast.SelectorExpr)
			if !ok {
				return true
			}
			inner, ok := unparen(outer.X).(*ast.SelectorExpr)
			if !ok {
				return true
			}
			client, ok := unparen(inner.X).(*ast.Ident)
			if !ok || !clients[client.Name] {
				return true
			}
			key := inner.Sel.Name + "." + outer.Sel.Name
			if _, seen := calls[key]; !seen {
				calls[key] = fn.Name.Name
			}
			return true
		})
	}
	return calls, problems
}

// skipMethods are testing.TB's ways to end a test as skipped.
var skipMethods = map[string]bool{"Skip": true, "Skipf": true, "SkipNow": true}

// smokeHelpers is what the skip check knows about the smoke file's own
// functions: which exist, and which can skip the test they are handed.
type smokeHelpers struct {
	declared map[string]*ast.FuncDecl
	skips    map[string]bool
}

// readSmokeHelpers finds the smoke file's functions that can skip, to a fixed
// point: one that skips directly, calls one that can, or hands its *testing.T to
// something this reader cannot see into. newSmokeClient is exempt -- it is the
// one sanctioned skip, and CI's OCTONOMY_SMOKE_REQUIRED=1 makes it a failure.
func readSmokeHelpers(file *ast.File) smokeHelpers {
	h := smokeHelpers{declared: map[string]*ast.FuncDecl{}, skips: map[string]bool{}}
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Body != nil {
			h.declared[fn.Name.Name] = fn
		}
	}
	for changed := true; changed; {
		changed = false
		for name, fn := range h.declared {
			if h.skips[name] || name == "newSmokeClient" {
				continue
			}
			if len(skipsIn(fn, h)) > 0 {
				h.skips[name] = true
				changed = true
			}
		}
	}
	return h
}

// skipsIn returns every way fn can end its test as skipped: a Skip call
// anywhere in its body, a call to a helper that can skip, or its *testing.T
// handed to a function this reader cannot see into -- one the smoke file does
// not declare, or a method on anything but the T itself.
func skipsIn(fn *ast.FuncDecl, h smokeHelpers) []string {
	var out []string
	tName := testingParam(fn)
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch callee := unparen(call.Fun).(type) {
		case *ast.SelectorExpr:
			if skipMethods[callee.Sel.Name] {
				out = append(out, "calls "+callee.Sel.Name)
				return true
			}
			if tName != "" && handsOn(call, tName) && !isIdent(callee.X, tName) {
				out = append(out, "hands its *testing.T to "+exprString(callee.X)+"."+callee.Sel.Name+
					", which the guard cannot read for a skip")
			}
		case *ast.Ident:
			if h.skips[callee.Name] {
				out = append(out, "calls "+callee.Name+", which can skip the test")
				return true
			}
			if _, ok := h.declared[callee.Name]; ok || callee.Name == "newSmokeClient" {
				return true
			}
			if tName != "" && handsOn(call, tName) {
				out = append(out, "hands its *testing.T to "+callee.Name+", which the smoke file does not "+
					"declare, so the guard cannot read it for a skip")
			}
		default:
			if tName != "" && handsOn(call, tName) {
				out = append(out, "hands its *testing.T to a function value the guard cannot read for a skip")
			}
		}
		return true
	})
	return out
}

// testingParam is the name fn gives its *testing.T parameter, or "".
func testingParam(fn *ast.FuncDecl) string {
	if fn.Type.Params == nil {
		return ""
	}
	for _, field := range fn.Type.Params.List {
		if exprString(field.Type) == "*testing.T" && len(field.Names) > 0 {
			return field.Names[0].Name
		}
	}
	return ""
}

// handsOn reports whether a call passes the named value as an argument.
func handsOn(call *ast.CallExpr, name string) bool {
	for _, arg := range call.Args {
		if isIdent(arg, name) {
			return true
		}
	}
	return false
}

// smokeClients returns the names a smoke test binds to a client it built:
// `client := newSmokeClient(t)`, or `client, err := octonomy.New(…)`. Only
// those two constructors count, and only outside a function literal.
func smokeClients(body *ast.BlockStmt, sdk string) map[string]bool {
	out := map[string]bool{}
	bind := func(lhs []ast.Expr, rhs []ast.Expr) {
		switch {
		case len(rhs) == 1 && len(lhs) >= 1:
			if isClientConstructor(rhs[0], sdk) {
				if ident, ok := unparen(lhs[0]).(*ast.Ident); ok && ident.Name != "_" {
					out[ident.Name] = true
				}
			}
		case len(rhs) == len(lhs):
			for i := range rhs {
				if isClientConstructor(rhs[i], sdk) {
					if ident, ok := unparen(lhs[i]).(*ast.Ident); ok && ident.Name != "_" {
						out[ident.Name] = true
					}
				}
			}
		}
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.AssignStmt:
			bind(node.Lhs, node.Rhs)
		case *ast.ValueSpec:
			lhs := make([]ast.Expr, len(node.Names))
			for i, ident := range node.Names {
				lhs[i] = ident
			}
			bind(lhs, node.Values)
		}
		return true
	})
	return out
}

// isClientConstructor reports whether expr calls one of the two constructors a
// smoke client comes from.
func isClientConstructor(expr ast.Expr, sdk string) bool {
	call, ok := unparen(expr).(*ast.CallExpr)
	if !ok {
		return false
	}
	switch fun := unparen(call.Fun).(type) {
	case *ast.Ident:
		return fun.Name == "newSmokeClient"
	case *ast.SelectorExpr:
		return isIdent(fun.X, sdk) && fun.Sel.Name == "New"
	}
	return false
}

// checkSmokeConstructor holds newSmokeClient to what smokeClients assumes of
// it: a function declared in the smoke file, returning the SDK's *Client. A
// helper of that name returning anything else would make every call on its
// result count, and so would one declared somewhere this guard does not read --
// another file, or a package-level func variable -- so a smoke file that calls
// newSmokeClient without declaring it as a function is refused too.
func checkSmokeConstructor(file *ast.File, sdk string, helpers smokeHelpers) string {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Name.Name != "newSmokeClient" {
			continue
		}
		if got := resultTypes(fn.Type); len(got) != 1 || got[0] != "*"+sdk+".Client" {
			return "newSmokeClient returns " + strings.Join(got, ", ") + ", not *" + sdk +
				".Client, so a call on its result is not a call on a client"
		}
		return checkSmokeGate(fn, helpers)
	}
	called := false
	ast.Inspect(file, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && isIdent(call.Fun, "newSmokeClient") {
			called = true
		}
		return !called
	})
	if called {
		return "calls newSmokeClient but does not declare it as a function, so the guard cannot check " +
			"that what it returns is a client"
	}
	return ""
}

// smokeRequiredEnv is the variable that turns the smoke run's one skip into a
// failure. CI sets it; a laptop with no harness does not.
const smokeRequiredEnv = "OCTONOMY_SMOKE_REQUIRED"

// checkSmokeGate holds newSmokeClient's skip to the one shape that makes it
// safe to exempt from the skip check: a single Skip, reachable only when the
// required gate is off. That is
//
//	required := os.Getenv("OCTONOMY_SMOKE_REQUIRED") == "1"
//	…
//	if required {
//		t.Fatal(…)
//	}
//	t.Skip(…)
//
// with the gate declared once and the Skip the statement straight after an
// `if required` whose body fails the test. newSmokeClient is exempt from
// skipsIn because CI sets the variable, and that exemption was trust: turning
// its credentials check from t.Fatal into t.Skip would have skipped every smoke
// test, required or not, with every guard green. A helper that never skips
// needs no gate.
func checkSmokeGate(fn *ast.FuncDecl, helpers smokeHelpers) string {
	// The gate governs newSmokeClient's own Skip call. Any other way out --
	// a helper that skips, or its *testing.T handed to something the guard
	// cannot read -- bypasses the gate exactly as it would in a smoke test.
	for _, why := range skipsIn(fn, helpers) {
		if strings.HasPrefix(why, "calls ") && skipMethods[strings.TrimPrefix(why, "calls ")] {
			continue // its own Skip, which the gate below must govern
		}
		return "newSmokeClient " + why + ", which skips past the " + smokeRequiredEnv + " gate"
	}
	var skips []*ast.CallExpr
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := unparen(call.Fun).(*ast.SelectorExpr); ok && skipMethods[sel.Sel.Name] {
				skips = append(skips, call)
			}
		}
		return true
	})
	switch {
	case len(skips) == 0:
		return ""
	case len(skips) > 1:
		return "newSmokeClient skips in " + strconv.Itoa(len(skips)) + " places; it may skip only once, " +
			"behind the " + smokeRequiredEnv + " gate, or a run CI requires can still skip"
	}
	const shape = "newSmokeClient's skip must be the statement straight after `if required { t.Fatal(…) }`, " +
		"with required := os.Getenv(\"" + smokeRequiredEnv + "\") == \"1\" declared once, so a run CI " +
		"requires fails instead of skipping"
	block, at := enclosingStatement(fn.Body, skips[0])
	if block == nil || at == 0 {
		return shape
	}
	gate, ok := block.List[at-1].(*ast.IfStmt)
	if !ok || gate.Init != nil || gate.Else != nil || !failsTest(gate.Body, testingParam(fn)) {
		return shape
	}
	name, ok := unparen(gate.Cond).(*ast.Ident)
	if !ok {
		return shape
	}
	bindings := bindingsOf(fn.Body, name.Name)
	if len(bindings) != 1 || !isRequiredGate(bindings[0], name.Name) {
		return shape
	}
	return ""
}

// enclosingStatement finds the block whose statement list holds call as a
// statement of its own, and that statement's index.
func enclosingStatement(body *ast.BlockStmt, call *ast.CallExpr) (*ast.BlockStmt, int) {
	var found *ast.BlockStmt
	at := -1
	ast.Inspect(body, func(n ast.Node) bool {
		block, ok := n.(*ast.BlockStmt)
		if !ok || found != nil {
			return found == nil
		}
		for i, stmt := range block.List {
			if expr, ok := stmt.(*ast.ExprStmt); ok && expr.X == ast.Expr(call) {
				found, at = block, i
			}
		}
		return true
	})
	return found, at
}

// failsTest reports whether a block, at its own top level, ends the test as
// failed: t.Fatal, t.Fatalf or t.FailNow on the function's own *testing.T,
// named tName. A Fatal on anything else -- a reporter, a fake -- need not stop
// the test, and then the skip after it runs in a required run.
func failsTest(block *ast.BlockStmt, tName string) bool {
	if tName == "" {
		return false
	}
	for _, stmt := range block.List {
		expr, ok := stmt.(*ast.ExprStmt)
		if !ok {
			continue
		}
		call, ok := expr.X.(*ast.CallExpr)
		if !ok {
			continue
		}
		if sel, ok := unparen(call.Fun).(*ast.SelectorExpr); ok && isIdent(sel.X, tName) {
			switch sel.Sel.Name {
			case "Fatal", "Fatalf", "FailNow":
				return true
			}
		}
	}
	return false
}

// isRequiredGate reports whether node is `name := os.Getenv("OCTONOMY_SMOKE_REQUIRED") == "1"`.
func isRequiredGate(node ast.Node, name string) bool {
	assign, ok := node.(*ast.AssignStmt)
	if !ok || assign.Tok != token.DEFINE || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 || !isIdent(assign.Lhs[0], name) {
		return false
	}
	cmp, ok := unparen(assign.Rhs[0]).(*ast.BinaryExpr)
	if !ok || cmp.Op != token.EQL {
		return false
	}
	call, ok := unparen(cmp.X).(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return false
	}
	if sel, ok := unparen(call.Fun).(*ast.SelectorExpr); !ok || !isIdent(sel.X, "os") || sel.Sel.Name != "Getenv" {
		return false
	}
	return stringLit(call.Args[0]) == smokeRequiredEnv && stringLit(cmp.Y) == "1"
}

// stringLit returns a string literal's value, or "".
func stringLit(expr ast.Expr) string {
	lit, ok := unparen(expr).(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return ""
	}
	v, err := strconv.Unquote(lit.Value)
	if err != nil {
		return ""
	}
	return v
}

func isBlankOrComment(line string) bool {
	trimmed := strings.TrimSpace(line)
	return trimmed == "" || strings.HasPrefix(trimmed, "#")
}

// importName returns the name a file imports path under, or "" if it does not.
func importName(file *ast.File, path string) string {
	for _, imp := range file.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p != path {
			continue
		}
		if imp.Name != nil {
			if imp.Name.Name == "_" || imp.Name.Name == "." {
				return ""
			}
			return imp.Name.Name
		}
		return "octonomy"
	}
	return ""
}

// --- what each method decodes ---------------------------------------------------

// responseTypeProducers maps each response type to every exported client method
// that decodes it, spelled the way a smoke test calls it ("Tags.Get").
func responseTypeProducers(files map[string]*ast.File) map[string]map[string]bool {
	fields := clientServiceFields(files)
	methods := serviceMethods(files)
	index := indexFuncs(files)
	types := indexTypes(files)

	out := map[string]map[string]bool{}
	for service, field := range fields {
		for name, decl := range methods[service] {
			if !ast.IsExported(name) {
				continue
			}
			for typ := range decodedTypes(decl, index, types) {
				if out[typ] == nil {
					out[typ] = map[string]bool{}
				}
				out[typ][field+"."+name] = true
			}
		}
	}
	return out
}

// decodedTypes resolves the response types one method decodes, following calls
// within the package -- a sibling helper, a method on the client the service
// holds -- so a method that reaches doData through a helper still names its
// type, and the type does not look like one nothing produces.
func decodedTypes(fn *ast.FuncDecl, index funcIndex, types typeIndex) map[string]bool {
	return decodedTypesIn(fn, index, types, map[*ast.FuncDecl]bool{})
}

func decodedTypesIn(fn *ast.FuncDecl, index funcIndex, types typeIndex, seen map[*ast.FuncDecl]bool) map[string]bool {
	out := map[string]bool{}
	if fn == nil || fn.Body == nil || seen[fn] {
		return out
	}
	seen[fn] = true

	recv := receiverName(fn)
	recvType := receiverTypeName(fn)

	// A body that rebinds its receiver can make an unrelated `x.client.foo(…)`
	// resolve as the client's. Rather than resolve scope, refuse to read such a
	// method at all: its type is then reported as produced by nothing, which is
	// the fail-closed direction. No method in this package rebinds its receiver.
	if recv != "" && rebinds(fn.Body, recv) {
		return out
	}

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		// A nested closure's calls are not this method's request: a transport
		// call inside an unused literal would otherwise give the method a
		// response type it never decodes.
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		// A transport call is read from its destination and never followed:
		// doData's own body decodes into an interface{} it was handed, and
		// following it would say nothing while looking like it had.
		if sel := transportSelector(call.Fun); sel != nil {
			if row, list, why := destination(sel, call, fn, types); why == "" {
				out[row] = true
				if list != "" {
					out[list] = true // the envelope is a decoded shape of its own
				}
			}
			return true
		}
		// A local binding shadows the package-level function of the same name:
		// after `fetch := func(…) error { … }`, a call to fetch runs the closure,
		// and following the name into the package's fetch would credit the method
		// with a type it never decodes. A local `type fetch …` makes fetch(x) a
		// conversion, the same mistake. Neither is followed, so the method
		// produces nothing through it -- the fail-closed direction.
		if ident, ok := unparen(call.Fun).(*ast.Ident); ok && (len(declarationsOf(ident.Name, fn)) > 0 || declaresType(fn, ident.Name)) {
			return true
		}
		if callee := resolveCallee(call.Fun, recv, recvType, index); callee != nil {
			for typ := range decodedTypesIn(callee, index, types, seen) {
				out[typ] = true
			}
		}
		return true
	})
	return out
}

// --- fixtures: the guard's own behaviour ---------------------------------------

// The guard above reads this repository, so on a correct tree it passes whether
// or not it works. These drive its two halves over synthetic source instead: one
// case per way a smoke file could look covered while covering nothing, and one
// per shape the response-type resolver has to get right for a probe to mean
// anything.

func TestSmokeCallsCountOnlyWhatTheSmokeRunRuns(t *testing.T) {
	const header = `package octonomy_test

import octonomy "github.com/octoverse-id/octonomy-go"

func newSmokeClient(t *testing.T) *octonomy.Client { return nil }
`
	for _, tc := range []struct {
		name     string
		src      string // appended to header
		full     string // a whole file, for a case the header cannot precede
		want     []string
		problems int
	}{
		{
			name: "a call on newSmokeClient's client counts",
			src:  `func TestSmoke_A(t *testing.T) { client := newSmokeClient(t); client.Tags.Get(ctx, id) }`,
			want: []string{"Tags.Get"},
		},
		{
			name: "a call on the SDK's New counts",
			src:  `func TestSmoke_A(t *testing.T) { c, err := octonomy.New(octonomy.Config{}); _ = err; c.Aliases.Get(ctx, id) }`,
			want: []string{"Aliases.Get"},
		},
		{
			name: "an aliased import is followed",
			full: `package octonomy_test
import sdk "github.com/octoverse-id/octonomy-go"
func newSmokeClient(t *testing.T) *sdk.Client { return nil }
func TestSmoke_A(t *testing.T) { var c, _ = sdk.New(sdk.Config{}); c.AuditLogs.List(ctx, nil) }`,
			want: []string{"AuditLogs.List"},
		},
		{
			// The binding is what keeps a call on some other value -- a fake, a
			// fixture field -- from standing in as coverage for the real one.
			name: "a call on something other than a smoke client is not coverage",
			src: `func TestSmoke_A(t *testing.T) {
				client := newSmokeClient(t)
				s.client.Tags.Get(ctx, id)
				fake.Tags.List(ctx, nil)
				client.Vocabularies.Get(ctx, id)
			}`,
			want: []string{"Vocabularies.Get"},
		},
		{
			name: "a call inside a closure is not coverage",
			src: `func TestSmoke_A(t *testing.T) {
				client := newSmokeClient(t)
				defer func() { client.Tags.Get(ctx, id) }()
				t.Run("x", func(t *testing.T) { client.Tags.List(ctx, nil) })
			}`,
		},
		{
			// The smoke run is `-run '^TestSmoke_'`. A function it does not
			// select is compiled and never executed, so its calls probe nothing.
			name: "a function the smoke run does not select is not coverage",
			src: `func TestRealServer(t *testing.T) { client := newSmokeClient(t); client.Tags.Get(ctx, id) }
			func assertTag(c *octonomy.Client) { c.Tags.Get(ctx, id) }`,
		},
		{
			name: "a method named like a smoke test is not a test",
			src:  `func (s *suite) TestSmoke_A(t *testing.T) { client := newSmokeClient(t); client.Tags.Get(ctx, id) }`,
		},
		{
			name:     "a rebound client is refused",
			src:      `func TestSmoke_A(t *testing.T) { client := newSmokeClient(t); client = other; client.Tags.Get(ctx, id) }`,
			problems: 1,
		},
		{
			name:     "a closure parameter shadowing the client is refused",
			src:      `func TestSmoke_A(t *testing.T) { client := newSmokeClient(t); f := func(client *fake) {}; _ = f; client.Tags.Get(ctx, id) }`,
			problems: 1,
		},
		{
			name: "a method value is not a call",
			src:  `func TestSmoke_A(t *testing.T) { client := newSmokeClient(t); get := client.Tags.Get; get(ctx, id) }`,
		},
		{
			name: "parentheses do not hide a call",
			src:  `func TestSmoke_A(t *testing.T) { client := newSmokeClient(t); (client).Tags.Get(ctx, id); (client.Tags).List(ctx, nil) }`,
			want: []string{"Tags.Get", "Tags.List"},
		},
		{
			name: "a discarded constructor binds nothing",
			src:  `func TestSmoke_A(t *testing.T) { newSmokeClient(t); client.Tags.Get(ctx, id) }`,
		},
		{
			// smokeClients trusts the name newSmokeClient. A helper of that name
			// returning something else would make every call on it count.
			name: "a newSmokeClient that does not return a client is refused",
			full: `package octonomy_test
import octonomy "github.com/octoverse-id/octonomy-go"
func newSmokeClient(t *testing.T) *fake { return nil }
func TestSmoke_A(t *testing.T) { client := newSmokeClient(t); client.Tags.Get(ctx, id) }`,
			want:     []string{"Tags.Get"},
			problems: 1,
		},
		{
			// main's walk fails an entry that skips; with no walk here, the
			// guard refuses the skip itself, or the calls after it stay credited
			// while the run is green.
			name:     "a smoke test that skips itself is refused",
			src:      `func TestSmoke_A(t *testing.T) { client := newSmokeClient(t); t.Skip("later"); client.Tags.Get(ctx, id) }`,
			want:     []string{"Tags.Get"},
			problems: 1,
		},
		{
			name: "so is a skip inside a subtest",
			src: `func TestSmoke_A(t *testing.T) {
				client := newSmokeClient(t)
				t.Run("x", func(t *testing.T) { t.SkipNow() })
				client.Tags.Get(ctx, id)
			}`,
			want:     []string{"Tags.Get"},
			problems: 1,
		},
		{
			// A conditional skip moved into a helper is the realistic shape. The
			// check follows it, and it follows it down.
			name: "a skip in a helper the smoke test calls is refused",
			src: `func requireFeature(t *testing.T) { if unavailable { t.Skip("unavailable") } }
			func TestSmoke_A(t *testing.T) { client := newSmokeClient(t); requireFeature(t); client.Tags.Get(ctx, id) }`,
			want:     []string{"Tags.Get"},
			problems: 1,
		},
		{
			name: "so is one two helpers down",
			src: `func requireFeature(t *testing.T) { requireFlag(t) }
			func requireFlag(t *testing.T) { t.SkipNow() }
			func TestSmoke_A(t *testing.T) { client := newSmokeClient(t); requireFeature(t); client.Tags.Get(ctx, id) }`,
			want:     []string{"Tags.Get"},
			problems: 1,
		},
		{
			name: "a helper that cannot skip may be handed the test",
			src: `func mark(t *testing.T) { t.Helper(); t.Logf("x") }
			func TestSmoke_A(t *testing.T) { client := newSmokeClient(t); mark(t); t.Run("x", func(t *testing.T) {}); client.Tags.Get(ctx, id) }`,
			want: []string{"Tags.Get"},
		},
		{
			// A function the smoke file does not declare could do anything with
			// the T, and the guard cannot see into it.
			name: "the test handed to a function the file does not declare is refused",
			src:  `func TestSmoke_A(t *testing.T) { client := newSmokeClient(t); requireHarness(t); client.Tags.Get(ctx, id) }`,
			want: []string{"Tags.Get"}, problems: 1,
		},
		{
			name: "so is the test handed to a method",
			src:  `func TestSmoke_A(t *testing.T) { client := newSmokeClient(t); h.require(t); client.Tags.Get(ctx, id) }`,
			want: []string{"Tags.Get"}, problems: 1,
		},
		{
			name: "and a helper that hands its test on to one counts as able to skip",
			src: `func requireFeature(t *testing.T) { external(t) }
			func TestSmoke_A(t *testing.T) { client := newSmokeClient(t); requireFeature(t); client.Tags.Get(ctx, id) }`,
			want: []string{"Tags.Get"}, problems: 1,
		},
		{
			name: "a local newSmokeClient is refused",
			src: `func TestSmoke_A(t *testing.T) {
				newSmokeClient := func(t *testing.T) *fake { return &fake{} }
				client := newSmokeClient(t)
				client.Tags.Get(ctx, id)
			}`,
			problems: 1,
		},
		{
			name: "so is a local named like the SDK import",
			src: `func TestSmoke_A(t *testing.T) {
				octonomy := fakeSDK{}
				client, _ := octonomy.New(octonomy.Config{})
				client.Tags.Get(ctx, id)
			}`,
			problems: 1,
		},
		{
			name:     "an early return is refused",
			src:      `func TestSmoke_A(t *testing.T) { client := newSmokeClient(t); if app == "" { t.Log("no app"); return }; client.Tags.Get(ctx, id) }`,
			want:     []string{"Tags.Get"},
			problems: 1,
		},
		{
			name: "a return inside a closure is the closure's",
			src:  `func TestSmoke_A(t *testing.T) { client := newSmokeClient(t); f := func() error { return nil }; _ = f; client.Tags.Get(ctx, id) }`,
			want: []string{"Tags.Get"},
		},
		{
			name: "a skip outside the smoke functions is newSmokeClient's business",
			src:  `func helper(t *testing.T) { t.Skipf("no harness") }` + "\n" + `func TestSmoke_A(t *testing.T) { client := newSmokeClient(t); client.Tags.Get(ctx, id) }`,
			want: []string{"Tags.Get"},
		},
		{
			name: "a newSmokeClient declared elsewhere is refused",
			full: `package octonomy_test
import octonomy "github.com/octoverse-id/octonomy-go"
func TestSmoke_A(t *testing.T) { client := newSmokeClient(t); client.Tags.Get(ctx, id) }`,
			want:     []string{"Tags.Get"},
			problems: 1,
		},
		{
			name: "so is a newSmokeClient that is a func variable",
			full: `package octonomy_test
import octonomy "github.com/octoverse-id/octonomy-go"
var newSmokeClient = func(t *testing.T) *fake { return nil }
func TestSmoke_A(t *testing.T) { client := newSmokeClient(t); client.Tags.Get(ctx, id) }`,
			want:     []string{"Tags.Get"},
			problems: 1,
		},
		{
			// The one sanctioned skip, in the shape that makes it one: reachable
			// only when CI's required gate is off.
			name: "newSmokeClient's gated skip is allowed",
			full: smokeGateFixture(`if baseURL == "" {
				if required {
					t.Fatal("required")
				}
				t.Skip("no harness")
			}`),
			want: []string{"Tags.Get"},
		},
		{
			// The change that turned the exemption into a hole: the incomplete-
			// credentials check skipping instead of failing.
			name: "a second skip in newSmokeClient is refused",
			full: smokeGateFixture(`if baseURL == "" {
				if required {
					t.Fatal("required")
				}
				t.Skip("no harness")
			}
			if token == "" {
				t.Skip("incomplete")
			}`),
			want: []string{"Tags.Get"}, problems: 1,
		},
		{
			name: "an ungated skip is refused",
			full: smokeGateFixture(`if baseURL == "" {
				t.Skip("no harness")
			}`),
			want: []string{"Tags.Get"}, problems: 1,
		},
		{
			name: "a gate that does not fail is refused",
			full: smokeGateFixture(`if baseURL == "" {
				if required {
					t.Log("required")
				}
				t.Skip("no harness")
			}`),
			want: []string{"Tags.Get"}, problems: 1,
		},
		{
			// A Fatal on something other than the test's own T need not stop
			// it, so a no-op reporter.Fatal lets the skip after it run in a
			// required run.
			name: "a gate that fails something other than the test is refused",
			full: smokeGateFixture(`if baseURL == "" {
				if required {
					reporter.Fatal("required")
				}
				t.Skip("no harness")
			}`),
			want: []string{"Tags.Get"}, problems: 1,
		},
		{
			name: "a gate read from another variable is refused",
			full: strings.Replace(smokeGateFixture(`if baseURL == "" {
				if required {
					t.Fatal("required")
				}
				t.Skip("no harness")
			}`), "OCTONOMY_SMOKE_REQUIRED", "OCTONOMY_SMOKE_STRICT", 1),
			want: []string{"Tags.Get"}, problems: 1,
		},
		{
			name: "a gate turned off before it is read is refused",
			full: smokeGateFixture(`required = false
			if baseURL == "" {
				if required {
					t.Fatal("required")
				}
				t.Skip("no harness")
			}`),
			want: []string{"Tags.Get"}, problems: 1,
		},
		{
			// The gate governs newSmokeClient's own Skip. A skip moved into a
			// helper, or its T handed on, goes around it.
			name: "newSmokeClient skipping through a helper is refused",
			full: strings.Replace(smokeGateFixture(`if baseURL == "" {
				if required {
					t.Fatal("required")
				}
				t.Skip("no harness")
			}
			requireToken(t, token)`), "func TestSmoke_A", "func requireToken(t *testing.T, token string) { if token == \"\" { t.Skip(\"no token\") } }\nfunc TestSmoke_A", 1),
			want: []string{"Tags.Get"}, problems: 1,
		},
		{
			name: "newSmokeClient handing its T to something unread is refused",
			full: smokeGateFixture(`if baseURL == "" {
				if required {
					t.Fatal("required")
				}
				t.Skip("no harness")
			}
			harness.Require(t)`),
			want: []string{"Tags.Get"}, problems: 1,
		},
		{
			name:     "a file that does not import the SDK is refused",
			full:     `package octonomy_test` + "\n" + `func TestSmoke_A(t *testing.T) { client := newSmokeClient(t); client.Tags.Get(ctx, id) }`,
			problems: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := header + tc.src
			if tc.full != "" {
				src = tc.full
			}
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "fixture.go", src, 0)
			if err != nil {
				t.Fatalf("parse fixture: %v", err)
			}
			calls, problems := smokeCallsIn(file)
			if len(problems) != tc.problems {
				t.Errorf("problems = %d, want %d (%v)", len(problems), tc.problems, problems)
			}
			if got := stringKeys(calls); strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("calls = %v, want %v", got, tc.want)
			}
		})
	}
}

// A response type the resolver cannot reach makes every probe for it look
// missing, and one it credits too freely lets a call satisfy a type it never
// decodes.
func TestDecodedTypesResolvesWhatAMethodReallyDecodes(t *testing.T) {
	const header = "package octonomy\ntype TagList struct { Data []Tag }\n"

	for _, tc := range []struct {
		name   string
		method string
		want   []string
		src    string
	}{
		{
			name: "a single resource", method: "TagService.Get", want: []string{"Tag"},
			src: `func (s *TagService) Get(ctx context.Context, id string) (*Tag, error) {
				var out Tag
				err := s.client.doData(ctx, http.MethodGet, "/tags/"+id, nil, nil, &out)
				return &out, err
			}`,
		},
		{
			name: "a list names its row and its envelope", method: "TagService.List", want: []string{"Tag", "TagList"},
			src: `func (s *TagService) List(ctx context.Context) (*TagList, error) {
				var out TagList
				err := s.client.doList(ctx, http.MethodGet, "/tags", nil, &out)
				return &out, err
			}`,
		},
		{
			name: "through a sibling helper", method: "TagService.Resolve", want: []string{"TagResolution"},
			src: `func (s *TagService) Resolve(ctx context.Context, slug string) (*TagResolution, error) { return s.resolve(ctx, slug) }
			func (s *TagService) resolve(ctx context.Context, slug string) (*TagResolution, error) {
				var out TagResolution
				err := s.client.doData(ctx, http.MethodGet, "/tag-resolution", nil, nil, &out)
				return &out, err
			}`,
		},
		{
			name: "through a method on the client", method: "TagService.Get", want: []string{"Tag"},
			src: `func (s *TagService) Get(ctx context.Context, id string) (*Tag, error) { return s.client.getTag(ctx, id) }
			func (c *Client) getTag(ctx context.Context, id string) (*Tag, error) {
				var out Tag
				err := c.doData(ctx, http.MethodGet, "/tags/"+id, nil, nil, &out)
				return &out, err
			}`,
		},
		{
			name: "a method that decodes nothing", method: "TagService.Delete", want: nil,
			src: `func (s *TagService) Delete(ctx context.Context, id string) error {
				return s.client.do(ctx, http.MethodDelete, "/tags/"+id, nil, nil)
			}`,
		},
		{
			// A WRAPPER. The concrete type reaches doData from the wrapper's own
			// call sites, which this resolver does not follow, and the wrapper's
			// body names only an interface{} -- so the method resolves to nothing
			// and Tag is reported as a type no method produces. Loud and
			// wrong-looking is the point; TestEveryResponseTypeCanRefuseAnEmptyDecode
			// reports the same call site as unresolved from its side.
			name: "a wrapper resolves to nothing", method: "TagService.Get", want: nil,
			src: `func (s *TagService) Get(ctx context.Context, id string) (*Tag, error) {
				var out Tag
				err := s.client.get(ctx, "/tags/"+id, &out)
				return &out, err
			}
			func (c *Client) get(ctx context.Context, path string, out interface{}) error {
				return c.doData(ctx, http.MethodGet, path, nil, nil, out)
			}`,
		},
		{
			name: "a closure's decode is not the method's", method: "TagService.List", want: nil,
			src: `func (s *TagService) List(ctx context.Context) error {
				_ = func() error { var out Tag; return s.client.doData(ctx, http.MethodGet, "/tags/1", nil, nil, &out) }
				return nil
			}`,
		},
		{
			name: "an invoked method expression credits nothing", method: "TagService.Create", want: nil,
			src: `func (s *TagService) Create(ctx context.Context) error {
				var request Tag
				var out Widget
				return (*Client).doData(s.client, ctx, http.MethodPost, "/w", nil, &request, &out)
			}`,
		},
		{
			// Go calls the local closure, not the package helper of that name,
			// and following the name credited the method with the helper's type.
			name: "a local binding shadowing a package helper is not followed", method: "TagService.Probe", want: nil,
			src: `func fetch(ctx context.Context, c *Client) error {
				var out Tag
				return c.doData(ctx, http.MethodGet, "/tags/1", nil, nil, &out)
			}
			func (s *TagService) Probe(ctx context.Context) error {
				fetch := func(context.Context, *Client) error { return nil }
				return fetch(ctx, s.client)
			}`,
		},
		{
			name: "the package helper itself is still followed", method: "TagService.Get", want: []string{"Tag"},
			src: `func fetch(ctx context.Context, c *Client) error {
				var out Tag
				return c.doData(ctx, http.MethodGet, "/tags/1", nil, nil, &out)
			}
			func (s *TagService) Get(ctx context.Context) error { return fetch(ctx, s.client) }`,
		},
		{
			name: "a local type conversion named like a helper is not followed", method: "TagService.Probe", want: nil,
			src: `func fetch(ctx context.Context, c *Client) error {
				var out Tag
				return c.doData(ctx, http.MethodGet, "/tags/1", nil, nil, &out)
			}
			func (s *TagService) Probe(ctx context.Context) error {
				type fetch int
				_ = fetch(1)
				return nil
			}`,
		},
		{
			// A rebound receiver makes every `s.client.…` in the body ambiguous,
			// so the method resolves to nothing rather than to whatever the name
			// now holds.
			name: "a rebound receiver resolves to nothing", method: "TagService.Get", want: nil,
			src: `func (s *TagService) Get(ctx context.Context, id string) (*Tag, error) {
				s = other
				var out Tag
				err := s.client.doData(ctx, http.MethodGet, "/tags/"+id, nil, nil, &out)
				return &out, err
			}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := parseFixture(t, header+tc.src)
			index := indexFuncs(files)
			fn, ok := index[tc.method]
			if !ok {
				t.Fatalf("fixture declares no %s", tc.method)
			}
			got := sortedKeys(decodedTypes(fn, index, indexTypes(files)))
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("decodedTypes(%s) = %v, want %v", tc.method, got, tc.want)
			}
		})
	}
}

// The producer map is what a probe is bound against, and it is keyed by the
// CLIENT FIELD spelling a smoke test uses. A service reachable from no field is
// not part of the surface a caller has, and one whose field the reader misses
// takes every type it decodes out of the guard with it.
func TestResponseTypeProducersUsesTheClientFieldSpelling(t *testing.T) {
	files := parseFixture(t, `package octonomy

type Client struct {
	Tags    *TagService
	Aliases *AliasService
	client  *TagService
}

type TagAliasList struct { Data []TagAlias }

func (s *TagService) Get(ctx context.Context, id string) (*Tag, error) {
	var out Tag
	err := s.client.doData(ctx, http.MethodGet, "/tags/"+id, nil, nil, &out)
	return &out, err
}

func (s *TagService) ListAliases(ctx context.Context, id string) (*TagAliasList, error) {
	var out TagAliasList
	err := s.client.doList(ctx, http.MethodGet, "/tags/"+id+"/aliases", nil, &out)
	return &out, err
}

func (s *AliasService) Get(ctx context.Context, id string) (*TagAlias, error) {
	var out TagAlias
	err := s.client.doData(ctx, http.MethodGet, "/tag-aliases/"+id, nil, nil, &out)
	return &out, err
}

func (s *AliasService) decode(ctx context.Context) (*TagAlias, error) {
	var out TagAlias
	err := s.client.doData(ctx, http.MethodGet, "/tag-aliases", nil, nil, &out)
	return &out, err
}
`)
	producers := responseTypeProducers(files)

	if got := sortedKeys(producers["Tag"]); strings.Join(got, ",") != "Tags.Get" {
		t.Errorf("producers[Tag] = %v, want [Tags.Get]", got)
	}
	// Both routes to TagAlias, under the field each is reached by -- the nested
	// one lives on TagService, so a probe of either satisfies TagAlias.
	if got := sortedKeys(producers["TagAlias"]); strings.Join(got, ",") != "Aliases.Get,Tags.ListAliases" {
		t.Errorf("producers[TagAlias] = %v, want [Aliases.Get Tags.ListAliases]", got)
	}
	// An unexported method is not a call a smoke test can make, so it is not a
	// producer either, and neither is a method reached through an unexported
	// field.
	for typ, methods := range producers {
		for m := range methods {
			if strings.HasSuffix(m, ".decode") || strings.HasPrefix(m, "client.") {
				t.Errorf("producers[%s] includes %s, which no caller can reach", typ, m)
			}
		}
	}
}

func TestSmokeBuildTagProblemsReadsBothConstraintLines(t *testing.T) {
	for _, tc := range []struct {
		name     string
		src      string
		problems int
	}{
		{"both lines", "//go:build integration\n// +build integration\n\npackage octonomy_test\n", 0},
		{"Go 1.13's line missing", "//go:build integration\n\npackage octonomy_test\n", 1},
		{"the modern line missing", "// +build integration\n\npackage octonomy_test\n", 1},
		{"another tag", "//go:build smoke\n// +build smoke\n\npackage octonomy_test\n", 1},
		{"no constraint at all", "// A smoke test.\npackage octonomy_test\n", 2},
		{"a constraint after the package clause is not one", "package octonomy_test\n//go:build integration\n// +build integration\n", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := smokeBuildTagProblems(tc.src); len(got) != tc.problems {
				t.Errorf("problems = %d, want %d: %v", len(got), tc.problems, got)
			}
		})
	}
}

// smokeGateFixture is a smoke file whose newSmokeClient has body after its gate
// is declared, and one smoke test that probes Tags.Get.
func smokeGateFixture(body string) string {
	return `package octonomy_test
import (
	"os"
	octonomy "github.com/octoverse-id/octonomy-go"
)
func newSmokeClient(t *testing.T) *octonomy.Client {
	required := os.Getenv("OCTONOMY_SMOKE_REQUIRED") == "1"
	baseURL := os.Getenv("OCTONOMY_TEST_BASE_URL")
	token := os.Getenv("OCTONOMY_TEST_TOKEN")
	` + body + `
	return nil
}
func TestSmoke_A(t *testing.T) { client := newSmokeClient(t); client.Tags.Get(ctx, id) }`
}

// makefileProblems reports the Makefile-wide settings that decide how the
// smoke recipe runs, which no recipe line shows.
//
// They are read as GNU make reads them, not matched as text. `.IGNORE:` with no
// prerequisites ignores every recipe's failures and is refused; with
// prerequisites it ignores only theirs, and is refused only if one is the
// smoke target -- `.IGNORE: dev-server-down` is a reasonable cleanup policy.
// `.ONESHELL` runs a recipe as one script, so a line after the smoke run would
// decide its exit status. MAKEFLAGS is held to an allow-list, because a word
// in it need not start with a dash: `MAKEFLAGS += i` is -i, ignore errors. And
// SHELL or .SHELLFLAGS decide what runs a recipe line at all. A file-wide
// GOFLAGS applies its flags to the pinned go test (-count=0 runs nothing), and
// an OCTONOMY_SMOKE_REQUIRED other than 1 turns the required gate off. An
// include brings in settings from a file this reader does not see, so it is
// refused too.
//
// WHERE IT ENDS. The recipe is pinned, so this reads only what can change how
// a pinned recipe runs from outside it. A make function computing a setting, or
// a generated makefile, is a reviewer's.
func makefileProblems(src, target string) []string {
	var problems []string
	// Logical lines, as make reads them: a backslash-newline continues a
	// declaration (`.IGNORE: dev-server-down \` / `smoke`), and a comment too,
	// which stripping after the # then removes whole.
	for _, ll := range logicalLines(src) {
		if strings.HasPrefix(ll.text, "\t") {
			continue // a recipe line is shell, not make
		}
		line := ll.text
		if hash := strings.IndexByte(line, '#'); hash >= 0 {
			line = line[:hash]
		}
		where := "line " + strconv.Itoa(ll.line)
		if m := specialTarget.FindStringSubmatch(line); m != nil {
			prereqs := strings.Fields(m[2])
			switch m[1] {
			case "IGNORE":
				if len(prereqs) == 0 {
					problems = append(problems, where+" declares .IGNORE for every target, so make ignores the "+
						"smoke recipe's failure")
				}
				for _, p := range prereqs {
					if p == target {
						problems = append(problems, where+" declares .IGNORE for "+target+", so make ignores its "+
							"recipe's failure")
					}
				}
			case "ONESHELL":
				problems = append(problems, where+" declares .ONESHELL, so a recipe runs as one script and a "+
					"line after the smoke run decides its exit status")
			}
			continue
		}
		if makeInclude.MatchString(line) {
			problems = append(problems, where+" includes another makefile, whose settings decide how the smoke "+
				"recipe runs and which this reader does not see")
			continue
		}
		m := makeAssignment.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		switch m[1] {
		case "GOFLAGS":
			problems = append(problems, where+" sets GOFLAGS, whose flags apply to the pinned go test from "+
				"outside its recipe; put a flag on the command instead")
		case smokeRequiredEnv:
			if strings.Trim(strings.TrimSpace(m[2]), `"'`) != "1" {
				problems = append(problems, where+" sets "+smokeRequiredEnv+" to something other than 1, "+
					"which turns newSmokeClient's required gate off")
			}
		case "SHELL", ".SHELLFLAGS":
			problems = append(problems, where+" sets "+m[1]+", which decides what runs the smoke recipe; the "+
				"guard reads that recipe as POSIX sh")
		case "MAKEFLAGS":
			for _, word := range strings.Fields(m[2]) {
				if !safeMakeflag.MatchString(word) {
					problems = append(problems, where+" puts "+strconv.Quote(word)+" in MAKEFLAGS, which the guard "+
						"does not know leaves recipe failures alone (a bare `i` is -i)")
				}
			}
		}
	}
	return problems
}

var (
	makeInclude    = regexp.MustCompile(`^\s*(-include|sinclude|include)\s`)
	specialTarget  = regexp.MustCompile(`^\.(IGNORE|ONESHELL)\s*:([^=].*|)$`)
	makeAssignment = regexp.MustCompile(`^\s*(?:(?:export|override)\s+)*(SHELL|\.SHELLFLAGS|MAKEFLAGS|GOFLAGS|OCTONOMY_SMOKE_REQUIRED)\s*(?:\+|::?|\?|!)?=(.*)$`)
	// safeMakeflag allows what changes how make reads the Makefile or how
	// loudly it runs, never whether a failure counts.
	safeMakeflag = regexp.MustCompile(`^(--no-print-directory|-r|--no-builtin-rules|-R|--no-builtin-variables|--warn-undefined-variables|-j\d*|--jobs(=\d+)?)$`)
)

// GNU make's own reading, not a text match: the settings that ignore the smoke
// recipe's failure are refused, and the ones that do not are left alone.
func TestMakefileProblemsReadsMakeLikeMakeDoes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		src      string
		problems int
	}{
		{"this repository's Makefile shape", ".PHONY: smoke test\nsmoke: ## run it\n\tgo test ./...\n", 0},
		{".IGNORE for every target", ".IGNORE:\nsmoke:\n\tgo test ./...\n", 1},
		{".IGNORE for the smoke target", ".IGNORE: smoke dev-server-down\n", 1},
		{".IGNORE for cleanup only", ".IGNORE: dev-server-down # tearing down is best effort\n", 0},
		{".ONESHELL", ".ONESHELL:\n", 1},
		{"MAKEFLAGS with -i", "MAKEFLAGS += -i\n", 1},
		{"MAKEFLAGS with a bare i", "MAKEFLAGS += i\n", 1},
		{"MAKEFLAGS with --ignore-errors", "export MAKEFLAGS := --ignore-errors\n", 1},
		{"MAKEFLAGS with -ik", "MAKEFLAGS = -ik\n", 1},
		{"MAKEFLAGS quieting directories", "MAKEFLAGS += --no-print-directory -r\n", 0},
		{"MAKEFLAGS with jobs", "MAKEFLAGS += -j4\n", 0},
		{"SHELL", "SHELL := /bin/bash\n", 1},
		{".SHELLFLAGS", ".SHELLFLAGS := -c\n", 1},
		{"a recipe line mentioning MAKEFLAGS is shell, not make", "smoke:\n\techo MAKEFLAGS=i\n", 0},
		{"a comment is not a setting", "# MAKEFLAGS += i\n", 0},
		{"a declaration continued onto the next line", ".IGNORE: dev-server-down \\\n  smoke\n", 1},
		{"MAKEFLAGS continued onto the next line", "MAKEFLAGS += --no-print-directory \\\n  i\n", 1},
		{"a comment continued onto the next line", "# best effort \\\n.IGNORE:\n", 0},
		{"an include", "include local.mk\n", 1},
		{"a file-wide GOFLAGS", "export GOFLAGS := -count=0\n", 1},
		{"the required gate turned off file-wide", "export OCTONOMY_SMOKE_REQUIRED := 0\n", 1},
		{"the required gate turned on file-wide", "export OCTONOMY_SMOKE_REQUIRED := 1\n", 0},
		{"an optional include", "-include .octonomy-harness.mk\n", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := makefileProblems(tc.src, smokeTarget); len(got) != tc.problems {
				t.Errorf("problems = %d, want %d: %v", len(got), tc.problems, got)
			}
		})
	}
}

// --- the smoke runners, pinned -------------------------------------------------

// The probes above are worth something only if a runner executes them, against
// a real server, with a failure that fails it. Two runners do: `make smoke` and
// the CI smoke job. Both are PINNED -- held to the exact text below -- rather
// than read for meaning.
//
// That is a choice, and the reason for it is recorded because the other one was
// tried. Ten review rounds grew a reader of these runners' shell, make and YAML:
// operators, quotes, continuations, if-blocks, folded scalars, flag allow-lists,
// step conditions, env precedence. Every round found one more construct it got
// wrong, in BOTH directions -- `set +e` and a move to another toolchain slipped
// through, while main's own capture-and-rethrow recipe, a toolchain variable,
// and a JavaScript `if` in an unrelated job were refused. A runner is two short
// blocks of text that change rarely; a pin makes every change to them a
// deliberate, reviewed edit, and refuses nothing outside them.
//
// WHAT A PIN WAS CHECKED AGAINST, so the next edit knows what to re-check: the
// pinned recipe and job were run against a booted Octonomy (`make dev-server`)
// on go1.13.15 and go1.25 for #95, every TestSmoke_ function ran and passed, and
// each runner (a) selects every TestSmoke_ function and nothing narrower, (b)
// passes -count=1, so go test cannot answer from its result cache, (c) builds
// with -tags=integration, which TestSmokeFileCarriesTheTagTheRunnersSelect holds
// the smoke file to, (d) fails when the run fails -- nothing masks the exit
// status, no condition can skip it -- and (e), in CI, runs with
// OCTONOMY_SMOKE_REQUIRED=1 on the go1.13 toolchain. Change a runner, re-check
// (a)-(e), then update its pin in the same commit.

// smokeRecipePin is the Makefile's `smoke:` rule and recipe, as makeRule reads
// them: every logical make line whose targets reach smoke, by name or as a
// pattern (`%oke:`) -- a target- or pattern-specific variable is one, alone or
// beside other targets, and changes the recipe's environment -- each followed
// by its tab-indented recipe lines.
const smokeRecipePin = `smoke: ## Run the integration smoke test against a booted harness (see dev-server)
	@if [ -f .octonomy-harness.env ]; then set -a; . ./.octonomy-harness.env; set +a; fi; \
	go test -tags=integration -count=1 -run '^TestSmoke_' -v ./...`

// smokeJobPin is ci.yml's smoke job, with its comment lines and blank lines
// dropped and trailing space trimmed. A comment LINE is free to change; nothing
// else is, an inline comment after a value included.
const smokeJobPin = `  smoke:
    name: go1.13 smoke test
    runs-on: ubuntu-latest
    timeout-minutes: 20
    steps:
      - uses: actions/checkout@v7
      - name: Set up Go 1.13
        uses: actions/setup-go@v7
        with:
          go-version: "1.13.15"
          cache: false
      - name: Boot the Octonomy harness
        uses: ./.github/actions/octonomy-harness
      - name: Smoke test against the real server
        env:
          OCTONOMY_SMOKE_REQUIRED: "1"
        run: go test -tags=integration -count=1 -run '^TestSmoke_' -v ./...
      - name: Capture container logs
        if: failure()
        run: make dev-server-logs
      - name: Tear down the harness
        if: always()
        run: make dev-server-down`

// `make smoke` runs the pinned recipe, and nothing elsewhere in the Makefile
// changes how a recipe runs. The pin covers the rule; makefileProblems covers
// the file-wide settings no recipe line shows.
func TestSmokeSelectorRunsEveryTestSmokeFunction(t *testing.T) {
	raw, err := ioutil.ReadFile("Makefile")
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	if got := makeRule(string(raw), smokeTarget); got != smokeRecipePin {
		t.Errorf("Makefile's `%s:` rule is not the pinned one. Re-check what the comment above "+
			"smokeRecipePin lists, then update the pin in the same commit.\n--- got\n%s\n--- pinned\n%s",
			smokeTarget, got, smokeRecipePin)
	}
	for _, why := range makefileProblems(string(raw), smokeTarget) {
		t.Errorf("Makefile: %s", why)
	}
}

// The CI smoke job is the pinned one, and nothing at the workflow's top level
// reaches into it: a workflow-wide env or defaults applies to every job, so it
// would change the pinned job without touching its text.
func TestSmokeJobRequiresTheSmokeRun(t *testing.T) {
	const path = ".github/workflows/ci.yml"
	raw, err := ioutil.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if got := workflowJob(string(raw), smokeTarget); got != smokeJobPin {
		t.Errorf("%s's `%s` job is not the pinned one. Re-check what the comment above smokeRecipePin "+
			"lists, then update smokeJobPin in the same commit.\n--- got\n%s\n--- pinned\n%s",
			path, smokeTarget, got, smokeJobPin)
	}
	for _, why := range workflowTopLevelProblems(string(raw)) {
		t.Errorf("%s: %s", path, why)
	}
}

// makeRule returns every make line whose targets reach target -- the rule, a
// target-specific variable (`smoke: export X := …`), a line naming several
// targets at once (`test smoke: SHELL := …`), and a pattern that matches it
// (`%oke: SHELL := …`) -- each followed by its
// recipe lines, trimmed of trailing space and joined by newlines.
// Continuations are joined first, as make joins them. A `target :=` is a
// variable named target, not a rule, and `.PHONY: … smoke` names smoke as a
// prerequisite, not a target.
func makeRule(src, target string) string {
	lines := strings.Split(src, "\n")
	var out []string
	for _, ll := range logicalLines(src) {
		if strings.HasPrefix(ll.text, "\t") {
			continue
		}
		m := makeTargets.FindStringSubmatch(ll.text)
		if m == nil || !namesTarget(strings.Fields(m[1]), target) {
			continue
		}
		out = append(out, strings.TrimRight(ll.text, " \t"))
		end := ll.line // the logical line may span several physical ones
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

// makeTargets matches a rule or target-specific line, capturing its targets:
// the text before a `:` or `::` that does not open an assignment.
var makeTargets = regexp.MustCompile(`^([^\t#=:][^#=:]*?)\s*::?([^=]|$)`)

// namesTarget reports whether a list of make targets reaches target: by name,
// or as a pattern (`%oke`, `sm%`, `%`) -- a pattern-specific variable applies to
// every target the pattern matches, the smoke target among them.
func namesTarget(words []string, target string) bool {
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

// workflowJob returns the job named name under `jobs:`, from its key to the
// next line at its own indentation or less, with comment and blank lines
// dropped and trailing space trimmed.
func workflowJob(src, name string) string {
	lines := strings.Split(src, "\n")
	var out []string
	in := false
	for _, line := range lines {
		if isBlankOrComment(line) {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if in && indent <= 2 {
			break
		}
		if !in && indent == 2 && strings.TrimRight(line, " ") == "  "+name+":" {
			in = true
		}
		if in {
			out = append(out, strings.TrimRight(line, " \t"))
		}
	}
	return strings.Join(out, "\n")
}

// workflowTopLevelProblems refuses the workflow-level keys that reach every job.
func workflowTopLevelProblems(src string) []string {
	var problems []string
	for i, line := range strings.Split(src, "\n") {
		if isBlankOrComment(line) || strings.HasPrefix(line, " ") {
			continue
		}
		key := strings.TrimSpace(line)
		if c := strings.IndexByte(key, ':'); c >= 0 {
			key = key[:c]
		}
		if key == "env" || key == "defaults" {
			problems = append(problems, "line "+strconv.Itoa(i+1)+" sets a workflow-level "+key+", which "+
				"reaches the pinned smoke job without changing its text; set it on the jobs that need it")
		}
	}
	return problems
}

// The pin is only as good as its extraction: a target-specific variable or a
// second rule must land in the compared text, and so must a changed recipe
// line; a comment in the workflow must not.
func TestThePinnedRunnersAreReadWhole(t *testing.T) {
	const rule = "smoke: ## run\n\tgo test ./...\n"
	t.Run("the rule and its recipe", func(t *testing.T) {
		if got := makeRule("help:\n\t@echo\n\n"+rule+"\ntest:\n\tgo test\n", "smoke"); got != "smoke: ## run\n\tgo test ./..." {
			t.Errorf("makeRule = %q", got)
		}
	})
	t.Run("a target-specific variable is part of it", func(t *testing.T) {
		if got := makeRule("smoke: export GOFLAGS := -count=0\n"+rule, "smoke"); !strings.Contains(got, "GOFLAGS") {
			t.Errorf("makeRule dropped the target-specific variable: %q", got)
		}
	})
	t.Run("so is a second rule", func(t *testing.T) {
		if got := makeRule(rule+"\nsmoke:\n\t@echo again\n", "smoke"); !strings.Contains(got, "again") {
			t.Errorf("makeRule dropped the second rule: %q", got)
		}
	})
	t.Run("a variable named smoke is not a rule", func(t *testing.T) {
		if got := makeRule("smoke := yes\n", "smoke"); got != "" {
			t.Errorf("makeRule read a variable as a rule: %q", got)
		}
	})
	t.Run("a line naming several targets is part of it", func(t *testing.T) {
		if got := makeRule("test smoke: SHELL := /bin/true\n"+rule, "smoke"); !strings.Contains(got, "/bin/true") {
			t.Errorf("makeRule dropped the multi-target assignment: %q", got)
		}
	})
	t.Run("so is one continued onto the next line", func(t *testing.T) {
		if got := makeRule("test \\\n  smoke: SHELL := /bin/true\n"+rule, "smoke"); !strings.Contains(got, "/bin/true") {
			t.Errorf("makeRule dropped the continued assignment: %q", got)
		}
	})
	t.Run("a pattern-specific variable matching smoke is part of it", func(t *testing.T) {
		if got := makeRule("%oke: SHELL := /bin/true\n"+rule, "smoke"); !strings.Contains(got, "/bin/true") {
			t.Errorf("makeRule dropped the pattern-specific assignment: %q", got)
		}
	})
	t.Run("a pattern that does not match smoke is not", func(t *testing.T) {
		if got := makeRule("%.o: %.c\n\tcc $<\n"+rule, "smoke"); got != "smoke: ## run\n\tgo test ./..." {
			t.Errorf("makeRule = %q", got)
		}
	})
	t.Run("a .PHONY naming smoke is not a rule for it", func(t *testing.T) {
		if got := makeRule(".PHONY: help smoke test\n"+rule, "smoke"); got != "smoke: ## run\n\tgo test ./..." {
			t.Errorf("makeRule = %q", got)
		}
	})
	t.Run("a renamed target is no rule at all", func(t *testing.T) {
		if got := makeRule("smoke-real:\n\tgo test ./...\n", "smoke"); got != "" {
			t.Errorf("makeRule read smoke-real: %q", got)
		}
	})
	const job = "jobs:\n  lint:\n    runs-on: x\n  smoke:\n    # why\n    runs-on: ubuntu-latest\n\n    steps:\n      - run: go test\n        # note\n  other:\n    runs-on: y\n"
	t.Run("the job, comments aside", func(t *testing.T) {
		if got := workflowJob(job, "smoke"); got != "  smoke:\n    runs-on: ubuntu-latest\n    steps:\n      - run: go test" {
			t.Errorf("workflowJob = %q", got)
		}
	})
	t.Run("a condition added to the job is part of it", func(t *testing.T) {
		withIf := strings.Replace(job, "    runs-on: ubuntu-latest\n", "    runs-on: ubuntu-latest\n    if: false\n", 1)
		if got := workflowJob(withIf, "smoke"); !strings.Contains(got, "if: false") {
			t.Errorf("workflowJob dropped the condition: %q", got)
		}
	})
	t.Run("workflow-level env and defaults are refused", func(t *testing.T) {
		if got := workflowTopLevelProblems("name: CI\nenv:\n  GOFLAGS: -count=0\ndefaults:\n  run:\n    shell: bash\njobs:\n  env: x\n"); len(got) != 2 {
			t.Errorf("problems = %v, want the env and the defaults", got)
		}
	})
}
