package octonomy

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// --- the source-level guard over readProbes -----------------------------------

// integrationSuiteFile holds the readProbes table this guard checks. It is read
// as SOURCE rather than linked, which is what lets a plain unit test check a
// table that lives behind the `integration` build tag.
const integrationSuiteFile = "integration_suite_test.go"

// readProbeExclusions names every exported Service method that deliberately
// carries no probe, with the reason it is not a gap.
//
// A name listed here must still name an existing exported Service method, and
// the reason must not be blank. A stale exclusion, or one whose reason nobody
// wrote, silently shrinks the guard -- which is the exact failure the guard
// exists to prevent -- so both fail the test rather than being ignored.
var readProbeExclusions = map[string]string{
	"Health.Live": "unauthenticated, unversioned, and outside the namespace axis entirely: " +
		"it sends no X-Namespace-* headers and reads no rows, so \"can merchant A see a " +
		"merchant B row\" is not a question it can be asked",
	"Health.Ready": "same as Health.Live -- the other half of the same unauthenticated probe pair",
}

// Every read method this SDK exposes needs a probe in readProbes.
//
// That table is what makes "a merchant-A client never sees a merchant-B row" a
// statement about the WHOLE read surface rather than about whichever endpoints
// someone remembered. Nothing enforced it: the suite iterates whatever the table
// holds, so a read method added without a probe leaves the isolation assertion
// covering less than it did with every gate green, and the table's "complete
// list" was a hand-maintained doc comment -- a reader, not a check (#73).
//
// This reads the SOURCE of both halves, so a resource added later is covered by
// this test existing rather than by somebody remembering.
//
// IT CARRIES NO `integration` BUILD TAG, DELIBERATELY. The table it checks lives
// behind that tag, but parsing it needs no container, and a guard that ran only
// when the container did would be absent from exactly the pull request that adds
// a read method.
//
// WHERE THIS CHECK ENDS. It is syntactic, and a check nobody knows the edge of is
// worse than none, so the edges are these. It proves a probe NAMES and CALLS its
// endpoint, never that the probe asserts anything useful about the result. It
// reads presence, not reachability: a call parked under `if false` would still be
// credited. An indirect read shape it does not recognize -- a function variable,
// a method expression, a closure invoked in place -- classifies as unknown and
// fails closed, except in one corner: a method that ALSO issues a recognized
// write is classified by that write, and the unrecognized read is not reported.
// And services are found by the concrete `*FooService` spelling this package
// uses; a generic or aliased one would not be seen. None of these has a path in
// this repository today, and each is a reason to extend the guard rather than to
// trust it past its edge.
func TestEveryReadMethodHasANamespaceProbe(t *testing.T) {
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

	// The probe names are CLIENT FIELD names ("Tags.List"), not service type
	// names ("TagService.List"), so the Client struct is what maps one to the
	// other -- and a service reachable from no field is not part of the read
	// surface a caller has.
	fields := clientServiceFields(pkg.Files)
	if len(fields) == 0 {
		t.Fatal("no *Service fields found on the Client struct -- the guard is not reading the source")
	}

	methods := serviceMethods(pkg.Files)
	index := indexFuncs(pkg.Files)

	// reads: what a probe is required for. unknown: what the guard could not
	// classify, which is required to be declared rather than assumed harmless.
	reads := map[string]bool{}
	writes := map[string]bool{}
	unknown := map[string]bool{}
	exported := map[string]bool{}
	for service, field := range fields {
		for name, decl := range methods[service] {
			if !ast.IsExported(name) {
				continue
			}
			qualified := field + "." + name
			exported[qualified] = true
			switch classify(decl, index) {
			case verbRead:
				reads[qualified] = true
			case verbWrite:
				writes[qualified] = true
			case verbUnknown:
				unknown[qualified] = true
			}
		}
	}

	probes := parseProbes(t, integrationSuiteFile)
	if len(probes) == 0 {
		t.Fatalf("no probes parsed out of %s:readProbes -- the guard is not reading the table",
			integrationSuiteFile)
	}
	probed := map[string]bool{}
	for _, probe := range probes {
		if probed[probe.name] {
			t.Errorf("%s:readProbes has two probes named %q. A duplicate makes the table look "+
				"one method wider than it is", integrationSuiteFile, probe.name)
		}
		probed[probe.name] = true
	}

	// 1. The guard proper: a read with no probe and no recorded reason.
	for _, name := range sortedKeys(reads) {
		if probed[name] {
			continue
		}
		if _, excluded := readProbeExclusions[name]; excluded {
			continue
		}
		t.Errorf("%s issues a read and has no probe in %s:readProbes, so the cross-merchant "+
			"isolation assertion does not cover it. Add a probe, or add the method to "+
			"readProbeExclusions with the reason it cannot leak a row across namespaces",
			name, integrationSuiteFile)
	}

	// 2. A method the classifier could not read a verb out of. Silence here is
	// what the guard is for, so it is reported rather than assumed benign.
	for _, name := range sortedKeys(unknown) {
		// A probed method is covered whatever the classifier made of it: check 4
		// binds a probe to the endpoint it actually calls, so the isolation
		// matrix really does exercise this one. Failing here anyway would also
		// have no fix -- an exclusion for a probed method is refused below, on
		// purpose -- so the requirement would be unsatisfiable. If the probe is
		// ever removed, this method lands in the unprobed branch and fails then.
		if probed[name] {
			continue
		}
		if _, excluded := readProbeExclusions[name]; excluded {
			continue
		}
		t.Errorf("%s is an exported service method the guard could not classify -- it reaches no "+
			"HTTP verb through any call this parser follows. Either it issues no request (say so "+
			"in readProbeExclusions) or the classifier cannot see its verb, in which case fix the "+
			"classifier rather than the method: an unclassified read needs no probe and says "+
			"nothing, which is the defect this test exists to prevent", name)
	}

	// 3. A probe naming a method that no longer exists still runs, still passes,
	// and covers nothing -- the same silence from the other direction.
	for _, probe := range probes {
		if !reads[probe.name] && !unknown[probe.name] {
			t.Errorf("%s:readProbes probes %q, which is not a read method on any service wired "+
				"to Client. A renamed or removed method leaves a probe that asserts nothing",
				integrationSuiteFile, probe.name)
		}
	}

	// 4. A probe's NAME is what the guard counts as coverage, and its find
	// closure is what actually runs. A probe named for one endpoint that calls
	// another satisfies the count while leaving the named endpoint unprobed.
	for _, probe := range probes {
		if len(probe.calls) == 0 {
			t.Errorf("%s:readProbes entry %q has a find closure that calls no client method the "+
				"parser can see, so nothing ties the name to what runs", integrationSuiteFile, probe.name)
			continue
		}
		if !probe.calls[probe.name] {
			t.Errorf("%s:readProbes entry %q calls %v, not %s. The name is what this guard counts "+
				"as coverage, so a mismatch reports a method as probed while another one is",
				integrationSuiteFile, probe.name, sortedKeys(probe.calls), probe.name)
		}
	}

	// 5. An exclusion that no longer names a method, or that nobody gave a
	// reason for, is a hole with a note over it.
	for _, name := range sortedKeys(readProbeExclusions) {
		if !exported[name] {
			t.Errorf("readProbeExclusions lists %q, which is not an exported method on a service "+
				"wired to Client. Drop the entry -- an exclusion for a method that does not exist "+
				"would also excuse a future method that happens to take the name", name)
		}
		if strings.TrimSpace(readProbeExclusions[name]) == "" {
			t.Errorf("readProbeExclusions[%q] has a blank reason. An exclusion is a claim that a "+
				"read cannot leak a row across namespaces; an unargued one is the silence this "+
				"test replaces", name)
		}
		if writes[name] {
			t.Errorf("readProbeExclusions lists %q, which the guard classifies as a write. "+
				"Excluding a write says nothing and hides the entry that would matter if the "+
				"method ever grew a read", name)
		}
		if probed[name] {
			t.Errorf("readProbeExclusions lists %q, which is also probed. One of the two is "+
				"wrong, and leaving both means that deleting the probe later would silently fall "+
				"back on the exclusion instead of failing", name)
		}
	}

	// The floor the precedent in optional_test.go sets: a classifier that
	// silently stopped recognizing reads would satisfy every loop above by
	// finding nothing at all.
	const known = 15 // 13 probed + the 2 excluded health probes
	if len(reads) < known {
		t.Errorf("found %d read method(s), want at least the %d that exist -- the guard is not "+
			"reading the source. Found: %v", len(reads), known, sortedKeys(reads))
	}
}

// clientServiceFields maps each *Service type to the exported Client field that
// reaches it (TagService -> "Tags").
func clientServiceFields(files map[string]*ast.File) map[string]string {
	out := map[string]string{}
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			spec, ok := n.(*ast.TypeSpec)
			if !ok || spec.Name.Name != "Client" {
				return true
			}
			structType, ok := ast.Unparen(spec.Type).(*ast.StructType)
			if !ok {
				return true
			}
			for _, field := range structType.Fields.List {
				star, ok := ast.Unparen(field.Type).(*ast.StarExpr)
				if !ok {
					continue
				}
				ident, ok := ast.Unparen(star.X).(*ast.Ident)
				if !ok || !strings.HasSuffix(ident.Name, "Service") {
					continue
				}
				for _, name := range field.Names {
					if ast.IsExported(name.Name) {
						out[ident.Name] = name.Name
					}
				}
			}
			return true
		})
	}
	return out
}

// serviceMethods collects every method declared on a *Service receiver, keyed by
// receiver type then method name. A service's methods are spread across resource
// files -- TagService alone is declared in five -- so this walks the whole
// package rather than one file.
func serviceMethods(files map[string]*ast.File) map[string]map[string]*ast.FuncDecl {
	out := map[string]map[string]*ast.FuncDecl{}
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 {
				continue
			}
			typeName := receiverTypeName(fn)
			if !strings.HasSuffix(typeName, "Service") {
				continue
			}
			if out[typeName] == nil {
				out[typeName] = map[string]*ast.FuncDecl{}
			}
			out[typeName][fn.Name.Name] = fn
		}
	}
	return out
}

// HTTP verbs, in every spelling this package could use. The string literals are
// here because a method that wrote "GET" instead of http.MethodGet would
// otherwise be invisible to the classifier -- and invisible is the one thing a
// guard against invisible omissions may not be.
var (
	readVerbs  = map[string]bool{"MethodGet": true, "MethodHead": true, "GET": true, "HEAD": true}
	writeVerbs = map[string]bool{
		"MethodPost": true, "MethodPut": true, "MethodPatch": true, "MethodDelete": true,
		"POST": true, "PUT": true, "PATCH": true, "DELETE": true,
	}
)

// transportCalls describes every call in this package that issues a request:
// the SHAPE that identifies it, and WHICH argument carries the verb.
//
// Both halves matter. Matching a bare callee name let an unrelated
// `cache.do(ctx, http.MethodDelete, …)` register as a transport call, and
// scanning every argument let a verb-shaped path segment or header value stand
// in for the method — either of which turns a read into a write and drops it out
// of the probe requirement. The verb of a request is the argument in the method
// position of the call that sends it, and nothing else.
//
// verbArg -1 means the call carries no verb and is followed into instead;
// doUnversioned builds its own request.
var transportCalls = map[string]struct {
	shape   string // "free" | "client" | "http"
	verbArg int
}{
	"doData":                {"free", 2},   // doData[T](ctx, c, method, …)
	"doList":                {"free", 2},   // doList[T](ctx, c, method, …)
	"do":                    {"client", 1}, // c.do(ctx, method, …)
	"doRaw":                 {"client", 1}, // c.doRaw(ctx, method, …)
	"doUnversioned":         {"client", -1},
	"NewRequest":            {"http", 0},
	"NewRequestWithContext": {"http", 1},
}

// classification is what the guard could determine about a method's HTTP verb.
type classification int

const (
	// verbUnknown: the classifier reached no verb at all. It is NOT a synonym
	// for "not a read" -- it means the guard cannot tell, and the guard treats
	// what it cannot tell as something a human has to declare. An earlier
	// revision defaulted this to "not a read", which reproduced #73's own defect
	// inside the check written to prevent it: a read the classifier could not
	// see needed no probe and said nothing.
	verbUnknown classification = iota
	verbRead
	verbWrite
)

// funcIndex holds every function and method in the package, keyed by "Name" for
// a plain function and "RecvType.Name" for a method, so a call can be followed
// across declarations.
type funcIndex map[string]*ast.FuncDecl

func indexFuncs(files map[string]*ast.File) funcIndex {
	out := funcIndex{}
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if fn.Recv == nil || len(fn.Recv.List) == 0 {
				out[fn.Name.Name] = fn
				continue
			}
			if recv := receiverTypeName(fn); recv != "" {
				out[recv+"."+fn.Name.Name] = fn
			}
		}
	}
	return out
}

// classify resolves a method's HTTP verb from the source.
//
// The rule is: a call to a transport function settles the verb from the
// argument it is handed, and every OTHER call is followed into its callee. The
// indirection is not hypothetical -- Health.Live issues no request of its own,
// it calls the unexported probe helper, which calls Client.doUnversioned, which
// is where the http.MethodGet actually is (transport.go).
//
// Following a transport call that WAS handed a verb would be wrong: those
// helpers take the verb as a parameter and are shared by every method, so their
// bodies name all of them -- doRaw's own read check is literally
// `method == http.MethodGet || method == http.MethodHead`. That would classify
// every Delete as a read.
func classify(fn *ast.FuncDecl, index funcIndex) classification {
	return classifyVerb(fn, index, map[*ast.FuncDecl]bool{})
}

func classifyVerb(fn *ast.FuncDecl, index funcIndex, seen map[*ast.FuncDecl]bool) classification {
	if fn == nil || fn.Body == nil || seen[fn] {
		return verbUnknown
	}
	seen[fn] = true

	recv := receiverName(fn)
	recvType := receiverTypeName(fn)

	// The shapes below identify the transport by the receiver's NAME, so a body
	// that rebinds that name can make an unrelated `c.do(…)` look like the
	// client's. Rather than resolve scope, refuse to classify such a method at
	// all: unknown is the fail-closed answer, and it is reported rather than
	// assumed harmless. No method in this package rebinds its receiver.
	if recv != "" && rebinds(fn.Body, recv) {
		return verbUnknown
	}

	found := verbUnknown
	promote := func(verb classification) {
		// A read beats a write. A method that does both can still expose a row
		// across namespaces, and the probe is what proves it does not.
		if verb == verbRead {
			found = verbRead
		} else if verb == verbWrite && found == verbUnknown {
			found = verbWrite
		}
	}

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if found == verbRead {
			return false
		}
		// A nested closure's calls are not this method's request. Descending
		// into one let a dead transport-shaped call inside an unused literal
		// set the method's verb. A method that really does issue its request
		// from a closure classifies as unknown and has to be declared, which is
		// the safe direction.
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if transport, ok := transportCalls[calleeName(call.Fun)]; ok &&
			matchesTransportShape(call.Fun, transport.shape, recv, recvType) {
			if transport.verbArg >= 0 {
				promote(verbAt(call, transport.verbArg))
				return true
			}
			// Carries no verb of its own: fall through and follow it.
		}
		if callee := resolveCallee(call.Fun, recv, recvType, index); callee != nil {
			promote(classifyVerb(callee, index, seen))
		}
		return true
	})
	return found
}

// matchesTransportShape rejects a call that merely shares a transport function's
// name.
func matchesTransportShape(fun ast.Expr, shape, recv, recvType string) bool {
	switch f := ast.Unparen(fun).(type) {
	case *ast.IndexExpr:
		return matchesTransportShape(f.X, shape, recv, recvType)
	case *ast.IndexListExpr:
		return matchesTransportShape(f.X, shape, recv, recvType)
	case *ast.Ident:
		return shape == "free"
	case *ast.SelectorExpr:
		switch shape {
		case "http":
			ident, ok := ast.Unparen(f.X).(*ast.Ident)
			return ok && ident.Name == "http"
		case "client":
			if recv == "" {
				return false
			}
			// c.doRaw(…) from inside a Client method.
			if ident, ok := ast.Unparen(f.X).(*ast.Ident); ok {
				return ident.Name == recv && recvType == "Client"
			}
			// s.client.do(…) from a service method, and only that field.
			if inner, ok := ast.Unparen(f.X).(*ast.SelectorExpr); ok && inner.Sel.Name == "client" {
				return isIdent(inner.X, recv)
			}
		}
	}
	return false
}

// verbAt reads the verb from one argument position, which is where a transport
// call's method actually is.
func verbAt(call *ast.CallExpr, i int) classification {
	if i >= len(call.Args) {
		return verbUnknown
	}
	switch arg := ast.Unparen(call.Args[i]).(type) {
	case *ast.SelectorExpr:
		if ident, ok := ast.Unparen(arg.X).(*ast.Ident); ok && ident.Name == "http" {
			return verbOf(arg.Sel.Name)
		}
	case *ast.BasicLit:
		if arg.Kind == token.STRING {
			if unquoted, err := strconv.Unquote(arg.Value); err == nil {
				return verbOf(unquoted)
			}
		}
	}
	return verbUnknown
}

// calleeName is the bare name a call expression invokes, through a selector or a
// generic instantiation.
func calleeName(fun ast.Expr) string {
	switch f := ast.Unparen(fun).(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	case *ast.IndexExpr:
		return calleeName(f.X)
	case *ast.IndexListExpr:
		return calleeName(f.X)
	}
	return ""
}

func verbOf(name string) classification {
	switch {
	case readVerbs[name]:
		return verbRead
	case writeVerbs[name]:
		return verbWrite
	}
	return verbUnknown
}

// resolveCallee maps a call expression to the declaration it reaches, for the
// call shapes this package uses: a plain function, a generic instantiation
// (doData[T]), a sibling method on the receiver, and a method on the client the
// service holds.
//
// The client shape is matched EXACTLY -- `<receiver>.client.<method>`. Accepting
// any nested selector, as an earlier revision did, resolved `s.cache.lookup()`
// as `Client.lookup` and would substitute an unrelated body's classification for
// this one's.
func resolveCallee(fun ast.Expr, recv, recvType string, index funcIndex) *ast.FuncDecl {
	switch f := ast.Unparen(fun).(type) {
	case *ast.Ident:
		return index[f.Name]
	case *ast.IndexExpr:
		return resolveCallee(f.X, recv, recvType, index)
	case *ast.IndexListExpr:
		return resolveCallee(f.X, recv, recvType, index)
	case *ast.SelectorExpr:
		if recv == "" {
			return nil
		}
		// s.helper(...) -- a sibling method on the same service.
		if isIdent(f.X, recv) && recvType != "" {
			return index[recvType+"."+f.Sel.Name]
		}
		// s.client.method(...) -- the transport, and only through that field.
		if inner, ok := ast.Unparen(f.X).(*ast.SelectorExpr); ok && inner.Sel.Name == "client" {
			if isIdent(inner.X, recv) {
				return index["Client."+f.Sel.Name]
			}
		}
	}
	return nil
}

func receiverTypeName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	switch recv := ast.Unparen(fn.Recv.List[0].Type).(type) {
	case *ast.StarExpr:
		if ident, ok := ast.Unparen(recv.X).(*ast.Ident); ok {
			return ident.Name
		}
	case *ast.Ident:
		return recv.Name
	}
	return ""
}

func receiverName(decl *ast.FuncDecl) string {
	if decl.Recv == nil || len(decl.Recv.List) == 0 || len(decl.Recv.List[0].Names) == 0 {
		return ""
	}
	return decl.Recv.List[0].Names[0].Name
}

// probe is one row of the readProbes table: the method it CLAIMS to cover, and
// the client methods its find closure actually calls.
type probe struct {
	name  string
	calls map[string]bool
}

// parseProbes extracts the entries of the []readProbe literal that readProbes
// returns.
//
// ONLY A SINGLE TOP-LEVEL RETURN COUNTS. Earlier revisions took every
// []readProbe literal in the function, then every return anywhere under it --
// so a literal in a branch that never runs (`if false { return []readProbe{…} }`)
// registered as coverage for a method nothing probes. Requiring one return,
// declared directly in the function body, makes the table's shape part of what
// the guard checks. If it is ever restructured -- built by a helper, appended to
// in a loop, returned from a branch -- this finds nothing and the caller fails,
// which is the safe direction.
func parseProbes(t *testing.T, path string) []probe {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return probesIn(t, file, path)
}

// probesIn is parseProbes over an already-parsed file, so the fixtures below can
// drive it from a source string rather than from a file on disk.
func probesIn(t *testing.T, file *ast.File, path string) []probe {
	t.Helper()

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Name.Name != "readProbes" || fn.Body == nil {
			continue
		}

		// Count only the returns that belong to readProbes. Each probe's find
		// closure has returns of its own, and descending into them would count
		// thirty-odd.
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
			t.Errorf("%s:readProbes must have exactly one return, declared in the function body, "+
				"returning a []readProbe literal (found %d return statements). The guard reads "+
				"that literal; a second one -- in a branch, or unreachable -- would register as "+
				"coverage for probes nothing runs", path, returns)
			return nil
		}
		if len(top.Results) == 0 {
			return nil
		}
		lit, ok := ast.Unparen(top.Results[0]).(*ast.CompositeLit)
		if !ok {
			t.Errorf("%s:readProbes does not return a composite literal, so the guard cannot read "+
				"the table", path)
			return nil
		}
		array, ok := ast.Unparen(lit.Type).(*ast.ArrayType)
		if !ok {
			return nil
		}
		if elem, ok := ast.Unparen(array.Elt).(*ast.Ident); !ok || elem.Name != "readProbe" {
			return nil
		}

		var out []probe
		for _, elt := range lit.Elts {
			entry, ok := ast.Unparen(elt).(*ast.CompositeLit)
			if !ok {
				continue
			}
			name, ok := stringField(entry, "name")
			if !ok {
				t.Errorf("%s:readProbes has an entry with no literal name", path)
				continue
			}
			out = append(out, probe{name: name, calls: probeCalls(entry)})
		}
		return out
	}
	return nil
}

// probeCalls collects the "Field.Method" of every call a probe's find closure
// makes ON ITS OWN CLIENT PARAMETER -- what the probe actually exercises, as
// opposed to what its name says it does.
func probeCalls(entry *ast.CompositeLit) map[string]bool {
	return closureClientCalls(entry, "find")
}

// closureClientCalls is probeCalls over any table whose entries hold a closure
// under a named field -- readProbes' `find`, and smokeProbes' `assert`
// (smokeprobes_test.go), which is checked the same way for the same reason.
//
// Binding to the closure's own client PARAMETER matters: matching any `x.y.z(...)`
// shape, as an earlier revision did, let a call on some other value (a fake, a
// fixture field) register as coverage and satisfy an entry whose real client call
// went somewhere else. It is also why both tables pass the client in rather than
// reading it off a shared struct.
func closureClientCalls(entry *ast.CompositeLit, field string) map[string]bool {
	out := map[string]bool{}
	for _, elt := range entry.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if key, ok := ast.Unparen(kv.Key).(*ast.Ident); !ok || key.Name != field {
			continue
		}
		lit, ok := ast.Unparen(kv.Value).(*ast.FuncLit)
		if !ok {
			continue
		}
		client := clientParamName(lit.Type)
		if client == "" {
			continue
		}
		// A rebinding of that name means the identifier no longer refers to the
		// probe's client, and this parser compares spelling rather than
		// resolving scope. Rather than credit a call that may be on something
		// else entirely, report nothing and let the caller fail the entry for
		// exercising no client method it can see.
		if rebinds(lit.Body, client) {
			continue
		}
		ast.Inspect(lit.Body, func(n ast.Node) bool {
			// A call inside a nested closure is not necessarily a call this
			// probe makes -- the literal may never be invoked. Counting one let
			// an uninvoked closure supply the coverage a probe claims while the
			// live path called a different endpoint.
			if _, ok := n.(*ast.FuncLit); ok && n != ast.Node(lit) {
				return false
			}
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			outer, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
			if !ok {
				return true
			}
			inner, ok := ast.Unparen(outer.X).(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if !isIdent(inner.X, client) {
				return true
			}
			out[inner.Sel.Name+"."+outer.Sel.Name] = true
			return true
		})
	}
	return out
}

// isIdent reports whether expr is the identifier name, ignoring parentheses.
// `(c) = other` rebinds c exactly as `c = other` does, and reading the bare node
// missed it -- which put back the false green the assignment check closed.
func isIdent(expr ast.Expr, name string) bool {
	ident, ok := ast.Unparen(expr).(*ast.Ident)
	return ok && ident.Name == name
}

// rebinds reports whether name is declared again OR assigned to anywhere inside
// body -- a short variable declaration, a plain assignment, a var spec, a range
// clause, or a nested closure parameter.
//
// Assignment matters as much as declaration. After `c = other` the identifier
// still denotes the same variable, so a scope resolver would say it is the
// client; what it holds is something else, and crediting a call on it would
// report coverage the probe never delivers.
func rebinds(body *ast.BlockStmt, name string) bool {
	shadowed := false
	ast.Inspect(body, func(n ast.Node) bool {
		if shadowed {
			return false
		}
		switch node := n.(type) {
		case *ast.AssignStmt:
			for _, lhs := range node.Lhs {
				if isIdent(lhs, name) {
					shadowed = true
				}
			}
		case *ast.ValueSpec:
			for _, ident := range node.Names {
				if ident.Name == name {
					shadowed = true
				}
			}
		case *ast.RangeStmt:
			for _, expr := range []ast.Expr{node.Key, node.Value} {
				if isIdent(expr, name) {
					shadowed = true
				}
			}
		case *ast.FuncLit:
			// A nested closure may legitimately name its own parameter the
			// same thing; its calls are not counted anyway.
			if node.Type != nil && node.Type.Params != nil {
				for _, param := range node.Type.Params.List {
					for _, ident := range param.Names {
						if ident.Name == name {
							shadowed = true
						}
					}
				}
			}
		}
		return true
	})
	return shadowed
}

// clientParamName is the name the find closure gives its *octonomy.Client
// parameter.
func clientParamName(sig *ast.FuncType) string {
	if sig == nil || sig.Params == nil {
		return ""
	}
	for _, param := range sig.Params.List {
		star, ok := ast.Unparen(param.Type).(*ast.StarExpr)
		if !ok {
			continue
		}
		var name string
		switch typ := ast.Unparen(star.X).(type) {
		case *ast.SelectorExpr: // octonomy.Client, from the external test package
			name = typ.Sel.Name
		case *ast.Ident: // Client, were the suite ever in-package
			name = typ.Name
		}
		if name == "Client" && len(param.Names) > 0 {
			return param.Names[0].Name
		}
	}
	return ""
}

// stringField returns the value of a string-literal field in a struct literal.
func stringField(lit *ast.CompositeLit, key string) (string, bool) {
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if ident, ok := ast.Unparen(kv.Key).(*ast.Ident); !ok || ident.Name != key {
			continue
		}
		basic, ok := ast.Unparen(kv.Value).(*ast.BasicLit)
		if !ok || basic.Kind != token.STRING {
			continue
		}
		unquoted, err := strconv.Unquote(basic.Value)
		if err != nil {
			continue
		}
		return unquoted, true
	}
	return "", false
}

func sortedKeys[V any](m map[string]V) []string {
	out := keys(m)
	sort.Strings(out)
	return out
}

// --- fixtures: the guard's own behaviour ---------------------------------------

// The guard above reads this repository, so on a correct tree it passes whether
// or not it works. These drive it over synthetic source instead, one case per
// way it has been wrong or could be: each fixture is a defect that a previous
// revision of the classifier or the parser accepted.

func classifyFixture(t *testing.T, src, method string) classification {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", src, 0)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	index := indexFuncs(map[string]*ast.File{"fixture.go": file})
	fn, ok := index[method]
	if !ok {
		t.Fatalf("fixture declares no %s", method)
	}
	return classify(fn, index)
}

func TestClassifyTakesTheVerbFromTheCallThatSendsIt(t *testing.T) {
	const header = "package octonomy\n"

	for _, tc := range []struct {
		name   string
		method string
		want   classification
		src    string
	}{
		{
			name: "the verb handed to a transport call", method: "TagService.List", want: verbRead,
			src: `func (s *TagService) List(ctx context.Context) error {
				return doList[Tag](ctx, s.client, http.MethodGet, "/tags", nil)
			}`,
		},
		{
			name: "a write is not a read", method: "TagService.Delete", want: verbWrite,
			src: `func (s *TagService) Delete(ctx context.Context, id string) error {
				return s.client.do(ctx, http.MethodDelete, "/tags/"+id, nil, nil)
			}`,
		},
		{
			// Health.Live's shape: no verb of its own, two calls deep.
			name: "a verb two calls away", method: "HealthService.Live", want: verbRead,
			src: `func (s *HealthService) Live(ctx context.Context) error { return s.probe(ctx, "/health/live") }
			func (s *HealthService) probe(ctx context.Context, p string) error { return s.client.doUnversioned(ctx, p) }
			func (c *Client) doUnversioned(ctx context.Context, p string) error {
				req, _ := http.NewRequestWithContext(ctx, http.MethodGet, p, nil)
				_ = req
				return nil
			}`,
		},
		{
			// A read reached through a helper must not be masked by a write the
			// same method issues directly.
			name: "writes directly and reads through a helper", method: "TagService.Sync", want: verbRead,
			src: `func (s *TagService) Sync(ctx context.Context, id string) error {
				if err := s.client.do(ctx, http.MethodDelete, "/tags/"+id, nil, nil); err != nil { return err }
				_, err := s.peek(ctx)
				return err
			}
			func (s *TagService) peek(ctx context.Context) (any, error) {
				return doList[Tag](ctx, s.client, http.MethodGet, "/tags", nil)
			}`,
		},
		{
			// A verb-shaped string that is not the verb. Matching literals
			// anywhere in the body classified this as a write and dropped it out
			// of the probe requirement.
			name: "a path segment spelled DELETE", method: "TagService.ListDeleted", want: verbRead,
			src: `func (s *TagService) ListDeleted(ctx context.Context) error {
				return doList[Tag](ctx, s.client, http.MethodGet, "/tags/DELETE", nil)
			}`,
		},
		{
			// The same error in the other direction: a stray "GET" turning a
			// write into a read.
			name: "a header value spelled GET", method: "TagService.Purge", want: verbWrite,
			src: `func (s *TagService) Purge(ctx context.Context) error {
				h := map[string]string{"Allow": "GET"}
				return s.client.do(ctx, http.MethodDelete, "/tags", h, nil)
			}`,
		},
		{
			name: "no request at all", method: "TagService.Local", want: verbUnknown,
			src: `func (s *TagService) Local(ctx context.Context) error { return nil }`,
		},
		{
			// A method that merely shares a transport name. Matching the bare
			// callee name let this set verbWrite and drop the method out of the
			// probe requirement altogether.
			name: "an unrelated method named do", method: "TagService.Evict", want: verbUnknown,
			src: `func (s *TagService) Evict(ctx context.Context) error {
				return s.cache.do(ctx, http.MethodDelete, "/tags")
			}`,
		},
		{
			// The verb is the argument in the METHOD position. A verb-shaped
			// path segment in another position is not the verb.
			name: "a verb-shaped path argument", method: "TagService.ListPurges", want: verbRead,
			src: `func (s *TagService) ListPurges(ctx context.Context) error {
				return doData[Tag](ctx, s.client, http.MethodGet, "DELETE", nil, nil)
			}`,
		},
		{
			// A transport call inside a literal nobody invokes is not this
			// method's request.
			name: "a dead nested closure", method: "TagService.Prepare", want: verbUnknown,
			src: `func (s *TagService) Prepare(ctx context.Context) error {
				_ = func() error { return doList[Tag](ctx, s.client, http.MethodGet, "/tags", nil) }
				return nil
			}`,
		},
		{
			// Parentheses are not a call shape. A write found first, plus a read
			// reached through a parenthesized helper, classified as a write and
			// dropped the method out of the probe requirement.
			name: "a parenthesized helper call", method: "TagService.Rotate", want: verbRead,
			src: `func (s *TagService) Rotate(ctx context.Context, id string) error {
				if err := s.client.do(ctx, http.MethodDelete, "/tags/"+id, nil, nil); err != nil { return err }
				_, err := (s).peek(ctx)
				return err
			}
			func (s *TagService) peek(ctx context.Context) (any, error) {
				return doList[Tag](ctx, s.client, http.MethodGet, "/tags", nil)
			}`,
		},
		{
			// The other place parentheses can sit: around the whole callee rather
			// than around the receiver. These reach different branches, so both
			// spellings are here.
			name: "a parenthesized callee", method: "TagService.Refresh", want: verbRead,
			src: `func (s *TagService) Refresh(ctx context.Context, id string) error {
				if err := s.client.do(ctx, http.MethodDelete, "/tags/"+id, nil, nil); err != nil { return err }
				_, err := (s.sniff)(ctx)
				return err
			}
			func (s *TagService) sniff(ctx context.Context) (any, error) {
				return doList[Tag](ctx, s.client, http.MethodGet, "/tags", nil)
			}`,
		},
		{
			name: "a parenthesized transport call", method: "TagService.ListParen", want: verbRead,
			src: `func (s *TagService) ListParen(ctx context.Context) error {
				return (doList[Tag])(ctx, s.client, http.MethodGet, "/tags", nil)
			}`,
		},
		{
			// The transport shapes key on the receiver's name, so a body that
			// rebinds it is not readable this way and must not be guessed at.
			name: "a method that rebinds its receiver", method: "TagService.Muddle", want: verbUnknown,
			src: `func (s *TagService) Muddle(ctx context.Context) error {
				if cond {
					s := cache
					return s.client.do(ctx, http.MethodDelete, "/x", nil, nil)
				}
				return doList[Tag](ctx, s.client, http.MethodGet, "/tags", nil)
			}`,
		},
		{
			// s.cache is not s.client. Resolving any nested selector as a Client
			// method substituted an unrelated body's verb for this one's.
			name: "a lookalike field is not the client", method: "TagService.Warm", want: verbUnknown,
			src: `func (s *TagService) Warm(ctx context.Context) error { return s.cache.lookup(ctx) }
			func (c *Client) lookup(ctx context.Context) error {
				return doList[Tag](ctx, c, http.MethodGet, "/tags", nil)
			}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyFixture(t, header+tc.src, tc.method); got != tc.want {
				t.Errorf("classify(%s) = %v, want %v", tc.method, got, tc.want)
			}
		})
	}
}

func TestParseProbesReadsOnlyTheTableThatRuns(t *testing.T) {
	const header = `package octonomy_test

type readProbe struct {
	name string
	find func(c *octonomy.Client) (bool, error)
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

	t.Run("a probe is bound to the call its closure makes", func(t *testing.T) {
		file, path := parse(t, header+`
func readProbes(h harness) []readProbe {
	return []readProbe{
		{name: "Tags.Get", find: func(c *octonomy.Client) (bool, error) { return c.Tags.Get(ctx, id) }},
	}
}`)
		probes := probesIn(t, file, path)
		if len(probes) != 1 || probes[0].name != "Tags.Get" || !probes[0].calls["Tags.Get"] {
			t.Fatalf("got %+v, want one probe Tags.Get calling Tags.Get", probes)
		}
	})

	t.Run("a call on something other than the client is not coverage", func(t *testing.T) {
		file, path := parse(t, header+`
func readProbes(h harness) []readProbe {
	return []readProbe{
		{name: "Tags.List", find: func(c *octonomy.Client) (bool, error) {
			_, _ = fake.Tags.List(ctx)
			return c.Tags.Get(ctx, id)
		}},
	}
}`)
		probes := probesIn(t, file, path)
		if len(probes) != 1 {
			t.Fatalf("got %d probes, want 1", len(probes))
		}
		// fake.Tags.List must not register: the probe claims Tags.List while the
		// only call on the client is Tags.Get, and the caller's name check is
		// what turns that into a failure.
		if probes[0].calls["Tags.List"] {
			t.Error("a call on a non-client value registered as coverage")
		}
		if !probes[0].calls["Tags.Get"] {
			t.Error("the real client call did not register")
		}
	})

	t.Run("a shadowed client is not the client", func(t *testing.T) {
		file, path := parse(t, header+`
func readProbes(h harness) []readProbe {
	return []readProbe{
		{name: "Tags.Get", find: func(c *octonomy.Client) (bool, error) {
			if cond {
				c := other
				return c.Tags.Get(ctx, id)
			}
			return c.Tags.Get(ctx, id)
		}},
	}
}`)
		probes := probesIn(t, file, path)
		if len(probes) != 1 {
			t.Fatalf("got %d probes, want 1", len(probes))
		}
		if len(probes[0].calls) != 0 {
			t.Errorf("calls were credited to a rebound identifier: %v", sortedKeys(probes[0].calls))
		}
	})

	t.Run("a reassigned client is not the client", func(t *testing.T) {
		file, path := parse(t, header+`
func readProbes(h harness) []readProbe {
	return []readProbe{
		{name: "Tags.Get", find: func(c *octonomy.Client) (bool, error) {
			c = other
			return c.Tags.Get(ctx, id)
		}},
	}
}`)
		probes := probesIn(t, file, path)
		if len(probes) != 1 {
			t.Fatalf("got %d probes, want 1", len(probes))
		}
		// The name still binds the parameter; the value no longer does, and a
		// call on it delivers none of the coverage the probe claims.
		if len(probes[0].calls) != 0 {
			t.Errorf("calls were credited after the client was reassigned: %v", sortedKeys(probes[0].calls))
		}
	})

	t.Run("a parenthesized reassignment is still a reassignment", func(t *testing.T) {
		file, path := parse(t, header+`
func readProbes(h harness) []readProbe {
	return []readProbe{
		{name: "Tags.Get", find: func(c *octonomy.Client) (bool, error) {
			(c) = other
			return c.Tags.Get(ctx, id)
		}},
	}
}`)
		probes := probesIn(t, file, path)
		if len(probes) != 1 {
			t.Fatalf("got %d probes, want 1", len(probes))
		}
		if len(probes[0].calls) != 0 {
			t.Errorf("parentheses hid a reassignment: %v", sortedKeys(probes[0].calls))
		}
	})

	t.Run("a call in an uninvoked closure is not coverage", func(t *testing.T) {
		file, path := parse(t, header+`
func readProbes(h harness) []readProbe {
	return []readProbe{
		{name: "Tags.Get", find: func(c *octonomy.Client) (bool, error) {
			_ = func() { c.Tags.Get(ctx, id) }
			return c.Tags.List(ctx)
		}},
	}
}`)
		probes := probesIn(t, file, path)
		if len(probes) != 1 {
			t.Fatalf("got %d probes, want 1", len(probes))
		}
		if probes[0].calls["Tags.Get"] {
			t.Error("a call inside an uninvoked closure registered as coverage")
		}
		if !probes[0].calls["Tags.List"] {
			t.Error("the live client call did not register")
		}
	})

	t.Run("a second, unreachable table is refused", func(t *testing.T) {
		file, path := parse(t, header+`
func readProbes(h harness) []readProbe {
	if false {
		return []readProbe{{name: "Widgets.Get", find: func(c *octonomy.Client) (bool, error) { return c.Widgets.Get(ctx) }}}
	}
	return []readProbe{
		{name: "Tags.Get", find: func(c *octonomy.Client) (bool, error) { return c.Tags.Get(ctx, id) }},
	}
}`)
		fake := &testing.T{}
		if probes := probesIn(fake, file, path); probes != nil {
			t.Errorf("a dead second table was accepted: %+v", probes)
		}
		if !fake.Failed() {
			t.Error("two returns in readProbes did not fail the parser")
		}
	})
}

// A service the Client struct reaches through a parenthesized field type is
// still part of the read surface. Missing one drops every method on it out of
// the guard, and the floor below would not notice: it only catches a count that
// falls, not one that fails to rise.
func TestClientServiceFieldsSeesAParenthesizedField(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", `package octonomy

type Client struct {
	Tags    *TagService
	Widgets (*WidgetService)
}`, 0)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	fields := clientServiceFields(map[string]*ast.File{"fixture.go": file})
	if got := fields["WidgetService"]; got != "Widgets" {
		t.Errorf("clientServiceFields missed the parenthesized field: got %q, want \"Widgets\" (all: %v)",
			got, fields)
	}
	if got := fields["TagService"]; got != "Tags" {
		t.Errorf("clientServiceFields lost the ordinary field: got %q", got)
	}
}
