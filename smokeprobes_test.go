package octonomy

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"
)

// --- the source-level guard over smokeProbes ------------------------------------

// integrationSmokeFile holds the smokeProbes table this guard checks. It is read
// as SOURCE rather than linked, which is what lets a plain unit test check a
// table that lives behind the `integration` build tag.
const integrationSmokeFile = "integration_test.go"

// smokeProbeExclusions names every response type that deliberately carries no
// smoke probe, with the reason it is not a gap.
//
// IT IS EMPTY, and that is the state to keep it in. Every response type this SDK
// decodes is asserted against the real server today; the map exists because the
// alternative to a reviewed escape hatch is an unreviewed one -- a contributor
// who cannot probe a type would otherwise write a probe that asserts nothing,
// which is exactly the outcome this guard was built to prevent (#77).
//
// A name listed here must still be a live response type, and the reason must not
// be blank. A stale exclusion, or one whose reason nobody wrote, silently shrinks
// the guard, so both fail the test rather than being ignored.
var smokeProbeExclusions = map[string]string{}

// Every response type this SDK decodes needs an entry in smokeProbes.
//
// `make smoke` is the only check that sees the server's real response shapes. A
// unit suite asserts the client against fixtures this repository wrote, so it
// cannot see a fixture-versus-server divergence -- which is #32, where every
// single-resource read decoded to an empty struct and a complete unit suite
// stayed green. The smoke walk closed that class, and it closed it only for the
// shapes somebody remembered to walk: a resource added without touching
// integration_test.go left #32's class unguarded for itself with every job green
// (recipe step 8, docs/roadmap.md).
//
// Nothing enforced it, and for a while the recorded reason was that nothing
// could. That was wrong about the cheap proxy only. "The type name appears
// somewhere in integration_test.go" is satisfied by a comment, and a check
// reporting "covered" on that basis would be worse than none. What the walk now
// has instead is a registry keyed by response type whose entries are executable,
// which is the shape readProbes has had since #73: the keys are compared against
// the response types derived from this package's source -- the SAME derivation
// #76's guard uses, responseTypes() in identityfields_test.go -- and each entry
// is bound to a client method that really decodes its key. A comment cannot
// satisfy that.
//
// IT CARRIES NO `integration` BUILD TAG, DELIBERATELY. The table it checks lives
// behind that tag, but parsing it needs no container, and a guard that ran only
// when the container did would be absent from exactly the pull request that adds
// a resource.
//
// WHERE THIS CHECK ENDS. It is syntactic, and a check nobody knows the edge of is
// worse than none, so the edges are these.
//
//   - IT DOES NOT PROVE AN ASSERTION IS MEANINGFUL. An entry that calls its
//     method and throws the result away passes. What it prevents is the SILENT
//     omission -- a new response type with no entry at all, and an entry left
//     behind by a type that no longer exists -- which is the failure that
//     actually happens. The assertions themselves are still a reviewer's job.
//   - It reads presence, not reachability: a call parked under `if false` would
//     still be credited, as would one in a branch this run never takes.
//   - It binds an entry to a client method that decodes its key, not to every
//     method that does. An entry keyed Tag satisfying itself with Tags.List says
//     nothing about Tags.Get, and both send the same envelope only because this
//     SDK routes them through the same helper.
//   - A response type is one handed to doData or doList. HealthStatus is decoded
//     by health.go's own probe helper and is not one, which is why the health
//     assertions are the smoke test's prologue rather than an entry: giving them
//     a key would put a name in the table that the required set does not hold.
//     A future response type decoded outside those two helpers would be invisible
//     here in the same way, and would need this definition widened rather than an
//     exclusion.
//   - It resolves a method's response type through calls WITHIN this package and
//     not into a closure. A service method that reached doData from a function
//     literal, or through another package, would resolve to no type at all --
//     which is reported below rather than passed over, since a response type with
//     no producing method makes this guard's binding check unsatisfiable rather
//     than merely weaker.
//   - The ORDER of the table is not checked by anything. Entries share the rows
//     they create, so the order is load-bearing at runtime; nothing here can see
//     that, and the walk's own fail-fast is what turns a bad order into one
//     legible failure instead of a cascade.
//   - A doData/doList call site whose type argument cannot be resolved is
//     reported by TestEveryResponseTypeCanRefuseAnEmptyDecode, which shares this
//     derivation. It is not repeated here; what a lost type does to THIS guard is
//     shrink the required set, which the floor at the bottom catches.
func TestEveryResponseTypeHasASmokeProbe(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse the package source: %v", err)
	}
	pkg, ok := pkgs["octonomy"]
	if !ok {
		t.Fatalf("package octonomy not found in the parsed source; got %v", keys(pkgs))
	}

	// The required set, derived exactly as #76's guard derives it.
	required, _ := responseTypes(pkg.Files)

	// And who produces each one. The names are CLIENT FIELD names ("Tags.Get"),
	// not service type names ("TagService.Get"), because that is the spelling a
	// probe's closure uses -- and a service reachable from no field is not part
	// of the surface a caller has.
	producers := responseTypeProducers(pkg.Files)

	probes := parseSmokeProbes(t, integrationSmokeFile)
	if len(probes) == 0 {
		t.Fatalf("no probes parsed out of %s:smokeProbes -- the guard is not reading the table",
			integrationSmokeFile)
	}
	probed := map[string]bool{}
	for _, probe := range probes {
		if probed[probe.name] {
			t.Errorf("%s:smokeProbes has two entries named %q. A duplicate makes the table look "+
				"one response shape wider than it is", integrationSmokeFile, probe.name)
		}
		probed[probe.name] = true
	}

	// 1. The guard proper: a response type the smoke walk never asserts. This is
	// the gap recipe step 8 had no check for.
	for _, name := range sortedKeys(required) {
		if probed[name] || smokeProbeExclusions[name] != "" {
			continue
		}
		t.Errorf("%s is decoded from a response (%s) and has no entry in %s:smokeProbes, so no "+
			"check compares its shape against a real server. A unit fixture cannot: it is "+
			"marshalled from the same struct it is decoded into, which is how #32 survived a "+
			"complete suite. Add an entry keyed %q, or add it to smokeProbeExclusions with the "+
			"reason a real server cannot settle its shape",
			name, required[name], integrationSmokeFile, name)
	}

	// 2. An entry naming a type that is no longer decoded from anything still
	// runs, still passes, and covers nothing -- the same silence from the other
	// direction.
	for _, probe := range probes {
		if _, live := required[probe.name]; !live {
			t.Errorf("%s:smokeProbes has an entry keyed %q, which is not a response type: nothing "+
				"in this package hands it to doData or doList. A renamed or removed model leaves "+
				"an entry that asserts a shape the SDK no longer decodes",
				integrationSmokeFile, probe.name)
		}
	}

	// 3. A response type nothing produces. The binding in check 4 would be
	// unsatisfiable, so this must not read as "no entry needed" -- and the two
	// causes are opposite, so the message names both rather than guessing. The
	// first is a real defect in the SDK and was reproduced: a service the recipe's
	// step 5 never wired onto Client is unreachable from a caller, decodes a type
	// nothing can assert, and lands here.
	for _, name := range sortedKeys(required) {
		if len(producers[name]) == 0 {
			t.Errorf("%s is a response type (%s) and no exported method on a service wired to "+
				"Client decodes it. Either the service is not on Client -- recipe step 5, which "+
				"makes the type unreachable from a caller and unassertable from a probe -- or the "+
				"guard cannot read the shape that reaches doData/doList there. Both leave an "+
				"entry for %s able to satisfy nothing, so neither may pass as \"no entry needed\"",
				name, required[name], name)
		}
	}

	// 4. An entry's KEY is what the guard counts as coverage, and its assert
	// closure is what actually runs. An entry keyed for one shape that exercises
	// another satisfies the count while leaving the named shape unasserted --
	// and, unlike a wrong name, it does so while looking entirely plausible.
	for _, probe := range probes {
		produced := producers[probe.name]
		if len(produced) == 0 {
			continue // already reported by check 2 or 3
		}
		if len(probe.calls) == 0 {
			t.Errorf("%s:smokeProbes entry %q has an assert closure that calls no client method "+
				"the parser can see, so nothing ties the key to what runs. Call the client "+
				"PARAMETER the closure is handed -- a call on anything else is not counted, on "+
				"purpose", integrationSmokeFile, probe.name)
			continue
		}
		bound := false
		for call := range probe.calls {
			if produced[call] {
				bound = true
				break
			}
		}
		if !bound {
			t.Errorf("%s:smokeProbes entry %q calls %v, none of which decodes a %s. The key is "+
				"what this guard counts as coverage, so a mismatch reports a shape as asserted "+
				"while another one is. The methods that decode it are %v",
				integrationSmokeFile, probe.name, sortedKeys(probe.calls), probe.name,
				sortedKeys(produced))
		}
	}

	// 5. An exclusion that no longer names a response type, or that nobody gave
	// a reason for, is a hole with a note over it.
	for _, name := range sortedKeys(smokeProbeExclusions) {
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
		if probed[name] {
			t.Errorf("smokeProbeExclusions lists %q, which is also probed. One of the two is "+
				"wrong, and leaving both means that deleting the entry later would silently fall "+
				"back on the exclusion instead of failing", name)
		}
	}

	// The floor the precedent in optional_test.go sets: a derivation that
	// silently stopped finding response types would satisfy every loop above by
	// finding nothing at all. It is the same count
	// TestEveryResponseTypeCanRefuseAnEmptyDecode holds, because it is the same
	// set.
	const known = 11
	if len(required) < known {
		t.Errorf("found %d response type(s), want at least the %d that exist -- the guard is not "+
			"reading the source. Found: %v", len(required), known, sortedKeys(required))
	}
}

// responseTypeProducers maps each response type to every exported client method
// that decodes it, spelled the way a probe's closure calls it ("Tags.Get").
func responseTypeProducers(files map[string]*ast.File) map[string]map[string]bool {
	fields := clientServiceFields(files)
	methods := serviceMethods(files)
	index := indexFuncs(files)

	out := map[string]map[string]bool{}
	for service, field := range fields {
		for name, decl := range methods[service] {
			if !ast.IsExported(name) {
				continue
			}
			for _, typ := range sortedKeys(decodedTypes(decl, index)) {
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
// within the package exactly as classify follows them for a verb.
//
// The indirection is the same one Health.Live has, and the reason not to assume
// it away: a method that reached doData through a helper would otherwise resolve
// to no type, and its type would then look like one nothing produces.
func decodedTypes(fn *ast.FuncDecl, index funcIndex) map[string]bool {
	return decodedTypesIn(fn, index, map[*ast.FuncDecl]bool{})
}

func decodedTypesIn(fn *ast.FuncDecl, index funcIndex, seen map[*ast.FuncDecl]bool) map[string]bool {
	out := map[string]bool{}
	if fn == nil || fn.Body == nil || seen[fn] {
		return out
	}
	seen[fn] = true

	recv := receiverName(fn)
	recvType := receiverTypeName(fn)

	// A body that rebinds its receiver can make an unrelated `x.client.foo(…)`
	// resolve as the client's. Rather than resolve scope, refuse to read such a
	// method at all: the caller reports a type with no producer, which is the
	// fail-closed direction. No method in this package rebinds its receiver.
	if recv != "" && rebinds(fn.Body, recv) {
		return out
	}

	// The declaration's own type parameters. doData handed its CALLER's T says
	// nothing about a concrete type of that name, and crediting it would let a
	// generic wrapper supply a response type that is never decoded.
	params := typeParamNames(fn)

	collect := func(args []ast.Expr) {
		for _, arg := range args {
			ident, ok := ast.Unparen(arg).(*ast.Ident)
			if !ok || params[ident.Name] {
				continue
			}
			out[ident.Name] = true
		}
	}

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		// A nested closure's calls are not this method's request, the same rule
		// classify applies: a transport call inside an unused literal would
		// otherwise give the method a response type it never decodes.
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		switch node := n.(type) {
		case *ast.IndexExpr:
			if isTransportGeneric(node.X) {
				collect([]ast.Expr{node.Index})
				return false
			}
		case *ast.IndexListExpr:
			if isTransportGeneric(node.X) {
				collect(node.Indices)
				return false
			}
		case *ast.CallExpr:
			// A transport generic is read from its instantiation above and never
			// followed: doData's own body names only its type parameter, and
			// following it would say nothing while looking like it had.
			if transportGenerics[calleeName(node.Fun)] {
				return true
			}
			if callee := resolveCallee(node.Fun, recv, recvType, index); callee != nil {
				for typ := range decodedTypesIn(callee, index, seen) {
					out[typ] = true
				}
			}
		}
		return true
	})
	return out
}

// smokeProbeEntry is one row of the smokeProbes table: the response type it
// CLAIMS to cover, and the client methods its assert closure actually calls.
type smokeProbeEntry struct {
	name  string
	calls map[string]bool
}

// parseSmokeProbes extracts the entries of the []smokeProbe literal that
// smokeProbes returns.
//
// ONLY A SINGLE TOP-LEVEL RETURN COUNTS, for the reason parseProbes gives: a
// literal in a branch that never runs would otherwise register as coverage for a
// shape nothing asserts. Requiring one return, declared directly in the function
// body, makes the table's shape part of what the guard checks -- restructure it
// and this finds nothing and the caller fails, which is the safe direction.
func parseSmokeProbes(t *testing.T, path string) []smokeProbeEntry {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return smokeProbesIn(t, file, path)
}

// smokeProbesIn is parseSmokeProbes over an already-parsed file, so the fixtures
// below can drive it from a source string rather than from a file on disk.
func smokeProbesIn(t *testing.T, file *ast.File, path string) []smokeProbeEntry {
	t.Helper()

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Name.Name != "smokeProbes" || fn.Body == nil {
			continue
		}

		// Count only the returns that belong to smokeProbes. Each entry's assert
		// closure has returns of its own, and descending into them would count
		// dozens.
		returns := 0
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if _, ok := n.(*ast.FuncLit); ok {
				return false
			}
			if _, ok := n.(*ast.ReturnStmt); ok {
				returns++
			}
			return true
		})
		var top *ast.ReturnStmt
		for _, stmt := range fn.Body.List {
			if ret, ok := stmt.(*ast.ReturnStmt); ok {
				top = ret
			}
		}
		if top == nil || returns != 1 {
			t.Errorf("%s:smokeProbes must have exactly one return, declared in the function body, "+
				"returning a []smokeProbe literal (found %d return statements). The guard reads "+
				"that literal; a second one -- in a branch, or unreachable -- would register as "+
				"coverage for a shape nothing asserts", path, returns)
			return nil
		}
		if len(top.Results) == 0 {
			return nil
		}
		lit, ok := ast.Unparen(top.Results[0]).(*ast.CompositeLit)
		if !ok {
			t.Errorf("%s:smokeProbes does not return a composite literal, so the guard cannot read "+
				"the table", path)
			return nil
		}
		array, ok := ast.Unparen(lit.Type).(*ast.ArrayType)
		if !ok {
			return nil
		}
		if elem, ok := ast.Unparen(array.Elt).(*ast.Ident); !ok || elem.Name != "smokeProbe" {
			return nil
		}

		var out []smokeProbeEntry
		for _, elt := range lit.Elts {
			entry, ok := ast.Unparen(elt).(*ast.CompositeLit)
			if !ok {
				continue
			}
			name, ok := stringField(entry, "name")
			if !ok {
				t.Errorf("%s:smokeProbes has an entry with no literal name. The key is what the "+
					"guard compares against the response types, so it cannot be computed", path)
				continue
			}
			out = append(out, smokeProbeEntry{name: name, calls: closureClientCalls(entry, "assert")})
		}
		return out
	}
	return nil
}

// --- fixtures: the guard's own behaviour ---------------------------------------

// The guard above reads this repository, so on a correct tree it passes whether
// or not it works. These drive its two halves over synthetic source instead: one
// case per way a table could look covered while covering nothing, and one per
// shape the response-type resolver has to get right for the binding in check 4
// to mean anything.

func TestParseSmokeProbesReadsOnlyTheTableThatRuns(t *testing.T) {
	const header = `package octonomy_test

type smokeProbe struct {
	name   string
	assert func(c *octonomy.Client, s *smokeState)
}
`
	parse := func(t *testing.T, src string) (*ast.File, string) {
		t.Helper()
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "fixture.go", src, 0)
		if err != nil {
			t.Fatalf("parse fixture: %v", err)
		}
		return file, "fixture.go"
	}

	t.Run("an entry is bound to the call its closure makes", func(t *testing.T) {
		file, path := parse(t, header+`
func smokeProbes() []smokeProbe {
	return []smokeProbe{
		{name: "Tag", assert: func(c *octonomy.Client, s *smokeState) { c.Tags.Get(ctx, id) }},
	}
}`)
		probes := smokeProbesIn(t, file, path)
		if len(probes) != 1 || probes[0].name != "Tag" || !probes[0].calls["Tags.Get"] {
			t.Fatalf("got %+v, want one entry Tag calling Tags.Get", probes)
		}
	})

	t.Run("a call on something other than the client is not coverage", func(t *testing.T) {
		file, path := parse(t, header+`
func smokeProbes() []smokeProbe {
	return []smokeProbe{
		{name: "Tag", assert: func(c *octonomy.Client, s *smokeState) {
			s.client.Tags.Get(ctx, id)
			c.Vocabularies.Get(ctx, id)
		}},
	}
}`)
		probes := smokeProbesIn(t, file, path)
		if len(probes) != 1 {
			t.Fatalf("got %d entries, want 1", len(probes))
		}
		// The whole reason assert takes the client as a parameter: a call reached
		// through the shared state is not the one the guard binds to, so an entry
		// cannot satisfy its key by way of a value the parser cannot follow.
		if probes[0].calls["Tags.Get"] {
			t.Error("a call on a non-client value registered as coverage")
		}
		if !probes[0].calls["Vocabularies.Get"] {
			t.Error("the real client call did not register")
		}
	})

	t.Run("a call inside an uninvoked closure is not coverage", func(t *testing.T) {
		file, path := parse(t, header+`
func smokeProbes() []smokeProbe {
	return []smokeProbe{
		{name: "Tag", assert: func(c *octonomy.Client, s *smokeState) {
			_ = func() { c.Tags.Get(ctx, id) }
			c.Vocabularies.Get(ctx, id)
		}},
	}
}`)
		probes := smokeProbesIn(t, file, path)
		if len(probes) != 1 {
			t.Fatalf("got %d entries, want 1", len(probes))
		}
		if probes[0].calls["Tags.Get"] {
			t.Error("a call inside an uninvoked closure registered as coverage")
		}
	})

	t.Run("a computed name is refused", func(t *testing.T) {
		file, path := parse(t, header+`
func smokeProbes() []smokeProbe {
	return []smokeProbe{
		{name: tagTypeName, assert: func(c *octonomy.Client, s *smokeState) { c.Tags.Get(ctx, id) }},
	}
}`)
		fake := &testing.T{}
		if probes := smokeProbesIn(fake, file, path); len(probes) != 0 {
			t.Errorf("a computed key was accepted: %+v", probes)
		}
		if !fake.Failed() {
			t.Error("a non-literal name did not fail the parser")
		}
	})

	t.Run("a table built by a helper is not read", func(t *testing.T) {
		file, path := parse(t, header+`
func smokeProbes() []smokeProbe {
	return append(tagProbes(), vocabProbes()...)
}`)
		fake := &testing.T{}
		if probes := smokeProbesIn(fake, file, path); probes != nil {
			t.Errorf("a computed table was read as a literal: %+v", probes)
		}
		if !fake.Failed() {
			t.Error("a table the guard cannot read did not fail it -- which would report every " +
				"response type as unprobed, or none, depending on which loop ran first")
		}
	})

	t.Run("a second, unreachable table is refused", func(t *testing.T) {
		file, path := parse(t, header+`
func smokeProbes() []smokeProbe {
	if false {
		return []smokeProbe{{name: "Widget", assert: func(c *octonomy.Client, s *smokeState) { c.Widgets.Get(ctx) }}}
	}
	return []smokeProbe{
		{name: "Tag", assert: func(c *octonomy.Client, s *smokeState) { c.Tags.Get(ctx, id) }},
	}
}`)
		fake := &testing.T{}
		if probes := smokeProbesIn(fake, file, path); probes != nil {
			t.Errorf("a dead second table was accepted: %+v", probes)
		}
		if !fake.Failed() {
			t.Error("two returns in smokeProbes did not fail the parser")
		}
	})
}

// Check 4 is only as good as this: a response type the resolver cannot reach
// makes every entry for it look unbound, and one it credits too freely lets an
// entry satisfy a key it never exercises.
func TestDecodedTypesResolvesWhatAMethodReallyDecodes(t *testing.T) {
	const header = "package octonomy\n"

	for _, tc := range []struct {
		name   string
		method string
		want   []string
		src    string
	}{
		{
			name: "a single resource", method: "TagService.Get", want: []string{"Tag"},
			src: `func (s *TagService) Get(ctx context.Context, id string) (*Tag, error) {
				return doData[Tag](ctx, s.client, http.MethodGet, "/tags/"+id, nil, nil)
			}`,
		},
		{
			name: "a list", method: "TagService.List", want: []string{"Tag"},
			src: `func (s *TagService) List(ctx context.Context) (*List[Tag], error) {
				return doList[Tag](ctx, s.client, http.MethodGet, "/tags", nil)
			}`,
		},
		{
			// The shape Health.Live has, and the reason this resolver follows
			// calls at all: a method that decodes through a helper must still
			// name its type, or the type looks like one nothing produces.
			name: "through a sibling helper", method: "TagService.Resolve", want: []string{"TagResolution"},
			src: `func (s *TagService) Resolve(ctx context.Context, slug string) (*TagResolution, error) { return s.resolve(ctx, slug) }
			func (s *TagService) resolve(ctx context.Context, slug string) (*TagResolution, error) {
				return doData[TagResolution](ctx, s.client, http.MethodGet, "/tag-resolution", nil, nil)
			}`,
		},
		{
			name: "a method that decodes nothing", method: "TagService.Delete", want: nil,
			src: `func (s *TagService) Delete(ctx context.Context, id string) error {
				return s.client.do(ctx, http.MethodDelete, "/tags/"+id, nil, nil)
			}`,
		},
		{
			// A GENERIC WRAPPER, and the one edge worth knowing here. The
			// concrete type reaches doData from the wrapper's call sites, which
			// this resolver does not follow, and the wrapper's own body names
			// only its type parameter -- so the method resolves to nothing and
			// Tag is reported as a response type no method produces. That is
			// loud and wrong-looking, which is the point: crediting the type
			// parameter would invent a response type named after a letter, and
			// TestEveryResponseTypeCanRefuseAnEmptyDecode reports the same call
			// site as unresolved from its own side.
			name: "a generic wrapper resolves to nothing", method: "TagService.Get", want: nil,
			src: `func (s *TagService) Get(ctx context.Context, id string) (*Tag, error) {
				return decodeOne[Tag](ctx, s.client, "/tags/"+id)
			}
			func decodeOne[T any](ctx context.Context, c *Client, path string) (*T, error) {
				return doData[T](ctx, c, http.MethodGet, path, nil, nil)
			}`,
		},
		{
			// The mirror of readProbes' rule: a transport call inside a literal
			// that is never invoked is not what the method decodes.
			name: "a closure's decode is not the method's", method: "TagService.List", want: nil,
			src: `func (s *TagService) List(ctx context.Context) error {
				_ = func() (*Tag, error) { return doData[Tag](ctx, s.client, http.MethodGet, "/tags/1", nil, nil) }
				return nil
			}`,
		},
		{
			// A rebound receiver makes every `s.client.…` in the body ambiguous,
			// so the method resolves to nothing and its type is reported as
			// unproduced rather than credited to whatever the name now holds.
			name: "a rebound receiver resolves to nothing", method: "TagService.Get", want: nil,
			src: `func (s *TagService) Get(ctx context.Context, id string) (*Tag, error) {
				s = other
				return doData[Tag](ctx, s.client, http.MethodGet, "/tags/"+id, nil, nil)
			}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "fixture.go", header+tc.src, 0)
			if err != nil {
				t.Fatalf("parse fixture: %v", err)
			}
			index := indexFuncs(map[string]*ast.File{"fixture.go": file})
			fn, ok := index[tc.method]
			if !ok {
				t.Fatalf("fixture declares no %s", tc.method)
			}
			got := sortedKeys(decodedTypes(fn, index))
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("decodedTypes(%s) = %v, want %v", tc.method, got, tc.want)
			}
		})
	}
}

// The producer map is what check 4 binds an entry against, and it is keyed by the
// CLIENT FIELD spelling a probe's closure uses. A service reachable from no field
// is not part of the surface a caller has, and one whose field the reader misses
// takes every type it decodes out of the guard with it.
func TestResponseTypeProducersUsesTheClientFieldSpelling(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", `package octonomy

type Client struct {
	Tags    *TagService
	Aliases *AliasService
}

func (s *TagService) Get(ctx context.Context, id string) (*Tag, error) {
	return doData[Tag](ctx, s.client, http.MethodGet, "/tags/"+id, nil, nil)
}

func (s *TagService) ListAliases(ctx context.Context, id string) (*List[TagAlias], error) {
	return doList[TagAlias](ctx, s.client, http.MethodGet, "/tags/"+id+"/aliases", nil)
}

func (s *AliasService) Get(ctx context.Context, id string) (*TagAlias, error) {
	return doData[TagAlias](ctx, s.client, http.MethodGet, "/tag-aliases/"+id, nil, nil)
}

func (s *AliasService) decode(ctx context.Context) (*TagAlias, error) {
	return doData[TagAlias](ctx, s.client, http.MethodGet, "/tag-aliases", nil, nil)
}
`, 0)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	producers := responseTypeProducers(map[string]*ast.File{"fixture.go": file})

	if got := sortedKeys(producers["Tag"]); strings.Join(got, ",") != "Tags.Get" {
		t.Errorf("producers[Tag] = %v, want [Tags.Get]", got)
	}
	// Both routes to TagAlias, under the field each is reached by -- the nested
	// one lives on TagService, so an entry keyed TagAlias may satisfy itself with
	// either.
	if got := sortedKeys(producers["TagAlias"]); strings.Join(got, ",") != "Aliases.Get,Tags.ListAliases" {
		t.Errorf("producers[TagAlias] = %v, want [Aliases.Get Tags.ListAliases]", got)
	}
	// An unexported method is not a call a probe can make, so it is not a
	// producer either: crediting it would let a type look produced while no entry
	// could ever bind to it.
	if producers["TagAlias"]["Aliases.decode"] {
		t.Error("an unexported method registered as a producer")
	}
}
