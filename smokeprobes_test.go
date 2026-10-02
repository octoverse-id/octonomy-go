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
// in a TestSmoke_ function statically instead.

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
//     credited, as is one after a t.Fatal that always fires. A t.Skip is the one
//     unreachability it refuses, anywhere in a TestSmoke_ function, because a
//     skip is green; the only skip the smoke run has is newSmokeClient's, which
//     OCTONOMY_SMOKE_REQUIRED=1 turns into a failure in CI.
//   - It binds a type to SOME method that decodes it, not to every one. A Tag
//     probed through Tags.List says nothing about Tags.Get, and both send the
//     same envelope only because this SDK routes them through the same helper.
//   - It counts a call only on a client its own TestSmoke_ function built --
//     from newSmokeClient, or from the SDK's New -- and only outside function
//     literals, even a deferred one that does run: a closure's body is not
//     read at all. The closures this file defers call Delete, which decodes
//     nothing, and a probe moved into a t.Run closure or a helper is reported
//     as missing rather than credited. That is the fail-closed direction.
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
	required, _, _ := responseTypes(files)

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
	if len(required) < knownResponseTypes {
		t.Errorf("found %d response type(s), want at least the %d that exist -- the guard is not "+
			"reading the source. Found: %v", len(required), knownResponseTypes, stringKeys(required))
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

// The guard counts calls in TestSmoke_ functions because that is what the smoke
// run runs. If either runner narrowed its selector -- this line's own smoke job
// ran `-run TestSmoke_RealServer` until #112 widened it, which would have left
// every other smoke test compiled and never executed -- the guard would credit
// probes no job runs. So both runners are held to the prefix the guard reads.
//
// Each file needs at least one integration run that reaches every TestSmoke_
// function: `-run '^TestSmoke_'`, or no -run at all, which runs everything. A
// run whose selector names TestSmoke any other way is refused, since it is a
// smoke run narrower than the guard assumes. A selector that does not name
// TestSmoke belongs to another suite -- #97's integration suite will have one --
// and is left alone, as is a comment.
func TestSmokeSelectorRunsEveryTestSmokeFunction(t *testing.T) {
	for _, path := range []string{"Makefile", ".github/workflows/ci.yml"} {
		raw, err := ioutil.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, problem := range smokeRunProblems(path, string(raw)) {
			t.Error(problem)
		}
	}
}

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

var (
	runSelector = regexp.MustCompile(`(?:^|\s)-run(?:\s+|=)(\S+)`)
	skipFlag    = regexp.MustCompile(`(?:^|\s)-skip(?:\s|=)`)
)

// smokeRunProblems reads one runner file for the integration runs that execute
// the smoke tests, and returns what is wrong with them.
func smokeRunProblems(path, src string) []string {
	var problems []string
	smokeRuns := 0
	for i, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || !strings.Contains(line, "-tags=integration") {
			continue
		}
		// -skip (Go 1.20) subtracts from whatever -run selects, so a run that
		// names the prefix and skips one function reaches fewer smoke tests than
		// the guard credits. Go 1.13 has no -skip, but `make smoke` may run on a
		// modern toolchain.
		if skipFlag.MatchString(line) {
			problems = append(problems, path+":"+strconv.Itoa(i+1)+" passes -skip to an integration run, "+
				"which leaves smoke tests the guard credits unexecuted")
			continue
		}
		m := runSelector.FindStringSubmatch(line)
		if m == nil {
			smokeRuns++ // no selector: every test runs, the smoke tests among them
			continue
		}
		selector := strings.Trim(m[1], `'"`)
		if !strings.Contains(selector, "TestSmoke") {
			continue // another suite's run
		}
		if selector != "^"+smokeTestPrefix {
			problems = append(problems, path+":"+strconv.Itoa(i+1)+" selects -run "+m[1]+", but the smoke "+
				"guard counts every "+smokeTestPrefix+" function as run. A narrower selector leaves probes the "+
				"guard credits unexecuted; select '^"+smokeTestPrefix+"'")
			continue
		}
		smokeRuns++
	}
	if smokeRuns == 0 {
		problems = append(problems, path+" has no `go test -tags=integration` run that reaches every "+
			smokeTestPrefix+" function, so the smoke run the guard reads for is not wired here")
	}
	return problems
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
	if why := checkSmokeConstructor(file, sdk); why != "" {
		problems = append(problems, why)
	}

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Body == nil || !strings.HasPrefix(fn.Name.Name, smokeTestPrefix) {
			continue
		}
		clients := smokeClients(fn.Body, sdk)
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
		// A skip anywhere in the function -- in its body or in a subtest's
		// closure -- turns the calls after it into a green run that asserted
		// nothing, while this guard still credits them.
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := unparen(call.Fun).(*ast.SelectorExpr); ok && skipMethods[sel.Sel.Name] {
				problems = append(problems, fn.Name.Name+" calls "+sel.Sel.Name+"; a smoke test that skips "+
					"itself is a green run that probed nothing after the skip, and the guard would still credit "+
					"those calls. Fail instead -- newSmokeClient is the one place the smoke run may skip")
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
func checkSmokeConstructor(file *ast.File, sdk string) string {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Name.Name != "newSmokeClient" {
			continue
		}
		if got := resultTypes(fn.Type); len(got) != 1 || got[0] != "*"+sdk+".Client" {
			return "newSmokeClient returns " + strings.Join(got, ", ") + ", not *" + sdk +
				".Client, so a call on its result is not a call on a client"
		}
		return ""
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
			if row, _, why := destination(sel, call, fn, types); why == "" {
				out[row] = true
			}
			return true
		}
		// A local binding shadows the package-level function of the same name:
		// after `fetch := func(…) error { … }`, a call to fetch runs the closure,
		// and following the name into the package's fetch would credit the method
		// with a type it never decodes. Such a call is not followed, so the method
		// produces nothing through it -- the fail-closed direction.
		if ident, ok := unparen(call.Fun).(*ast.Ident); ok && len(declarationsOf(ident.Name, fn)) > 0 {
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
			name: "a list names its row", method: "TagService.List", want: []string{"Tag"},
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

// The runner check is only as good as its reader: a comment or another suite's
// run must not count against it, and a run that reaches every smoke test must
// count whether it names the prefix or selects nothing at all.
func TestSmokeRunProblemsReadsTheRunnersLikeTheShellDoes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		src      string
		problems int
	}{
		{"the prefix", "\tgo test -tags=integration -run '^TestSmoke_' -v ./...", 0},
		{"a double-quoted prefix", `run: go test -tags=integration -run "^TestSmoke_" ./...`, 0},
		{"no selector runs everything", "\tgo test -tags=integration -race -count=1 -v ./...", 0},
		{"another suite beside the smoke run", "go test -tags=integration -run '^TestSmoke_' ./...\ngo test -tags=integration -run '^TestIntegration_' ./...", 0},
		{"a comment is not a run", "#   `go test -tags=integration` is the acceptance criterion\ngo test -tags=integration -run '^TestSmoke_' ./...", 0},
		{"one smoke function only", "go test -tags=integration -count=1 -run '^TestSmoke_RealServer$$' -v ./...", 2},
		{"the prefix minus a skipped function", "go test -tags=integration -run '^TestSmoke_' -skip 'TestSmoke_ResourceGroups' ./...", 2},
		{"a -skip= spelling", "go test -tags=integration -skip=TestSmoke_A ./...", 2},
		{"an unanchored name", "run: go test -tags=integration -run TestSmoke_RealServer -v ./...", 2},
		{"only another suite", "go test -tags=integration -run '^TestIntegration_' ./...", 1},
		{"no integration run at all", "go test -race ./...", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := smokeRunProblems("fixture", tc.src); len(got) != tc.problems {
				t.Errorf("problems = %d, want %d: %v", len(got), tc.problems, got)
			}
		})
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
