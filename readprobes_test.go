package octonomy

// A REWRITE of main's readprobes_test.go at 5e40964 (#97; the disposition table
// is docs/compat-test-disposition.md). Every mention of main in this file means
// main at that commit. The guard is main's -- every read method this SDK exposes
// has a namespace probe in readProbes, or an argued exclusion -- with its rules
// and its fixtures, and four things about it are this line's:
//
//   - THE TRANSPORT CALLS. main's helpers are free generic functions,
//     doData[T](ctx, c, method, …), with the verb at argument 2. Here every one
//     is a method on the client -- s.client.doData(ctx, method, …) -- with the
//     verb at argument 1, and none takes a type argument. A transformed copy of
//     main's table would read the PATH as the verb, find none, and classify every
//     read as unknown; TestTransportCallsMatchTheHelpersSignatures pins this one
//     against transport.go.
//   - THE SHARED READERS. clientServiceFields, serviceMethods, indexFuncs,
//     resolveCallee and rebinds are in sourceguard_test.go, rewritten for this
//     dialect already (#95), and are used from there rather than copied.
//   - THE GUARD IS A FUNCTION OF THE SOURCE. main's runs its checks inline on
//     this repository, which on a correct tree passes whether or not they work.
//     readProbeProblems takes the package and the suite as parsed files, so
//     TestReadProbeGuardFailsOnEachCondition drives every condition it must fail
//     on with a fixture of its own, against a clean fixture that must not fail.
//   - THE TABLE MUST RUN. On main the isolation suite has a CI job of its own.
//     Here it runs as a step of the required go1.13 smoke job, under the same
//     pin as the smoke run, and TestTheIsolationSuiteRunsItsProbes holds the
//     suite to that runner: a probe table nothing runs proves only that probes
//     exist.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/ioutil"
	"strconv"
	"strings"
	"testing"
)

// integrationSuiteFile holds the readProbes table this guard checks. It is read
// as SOURCE rather than linked, which is what lets a plain unit test check a
// table that lives behind the `integration` build tag.
const integrationSuiteFile = "integration_suite_test.go"

// integrationHarnessFile holds the suite's loadHarness gate and its clients.
const integrationHarnessFile = "integration_harness_test.go"

// isolationTestPrefix is what makes a function part of the isolation run: `make
// test-integration` and the isolation step of the go1.13 smoke job both select
// `-run '^TestIntegration_'`, and their pins hold them to it.
const isolationTestPrefix = "TestIntegration_"

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
	"Client.APIVersion": "an accessor: it returns the API version the client was built with and sends " +
		"no request, so it has no row to show anyone",
}

// Every read method this SDK exposes needs a probe in readProbes.
//
// That table is what makes "a merchant-A client never sees a merchant-B row" a
// statement about the WHOLE read surface rather than about whichever endpoints
// someone remembered. Nothing enforces that by itself: the suite iterates
// whatever the table holds, so a read method added without a probe leaves the
// isolation assertion covering less than it did with every gate green.
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
// a method value, a closure invoked in place -- classifies as unknown and fails
// closed, except in one corner: a method that ALSO issues a recognized write is
// classified by that write, and the unrecognized read is not reported. None of
// these has a path in this repository today, and each is a reason to extend the
// guard rather than to trust it past its edge.
//
// THE SURFACE IT READS is the exported methods declared on Client and on each
// *FooService an exported Client field holds. Everything else that would put a
// method in a caller's hands is refused rather than read (surfaceShapeProblems):
// an embedded field in Client or a service, an aliased or non-struct service,
// an exported Client field of any other type, an exported field on a service,
// and a service that another exported type holds and Client does not.
func TestEveryReadMethodHasANamespaceProbe(t *testing.T) {
	files := parsePackageSource(t)
	suite := parseFileOrFatal(t, integrationSuiteFile)

	reads, problems := readProbeProblems(files, suite, integrationSuiteFile, readProbeExclusions)
	for _, p := range problems {
		t.Error(p)
	}

	// The floor: a classifier that silently stopped recognizing reads would
	// satisfy every check by finding nothing at all.
	const known = 15 // 13 probed + the 2 excluded health probes
	if len(reads) < known {
		t.Errorf("found %d read method(s), want at least the %d that exist -- the guard is not "+
			"reading the source. Found: %v", len(reads), known, sortedKeys(reads))
	}
}

// readProbeProblems is the guard proper, over parsed source: pkg is the SDK
// package, suite the file holding readProbes. It returns the read methods it
// found, for the caller's floor, and every condition the guard fails on:
//
//  1. a read method with no probe and no exclusion;
//  2. an exported method whose verb it cannot resolve, unless probed or excluded;
//  3. a probe naming a method that is not a read on a service wired to Client;
//  4. a probe whose find closure calls no client method, or not the one its name
//     claims;
//  5. an exclusion that names no exported method, has a blank reason, names a
//     write, or names a probed method;
//
// and a duplicate probe name, and a table it cannot read at all.
func readProbeProblems(pkg map[string]*ast.File, suite *ast.File, suitePath string, exclusions map[string]string) (map[string]bool, []string) {
	var problems []string

	// The probe names are CLIENT FIELD names ("Tags.List"), not service type
	// names ("TagService.List"), so the Client struct is what maps one to the
	// other -- and a service reachable from no field is not part of the read
	// surface a caller has.
	fields := clientServiceFields(pkg)
	if len(fields) == 0 {
		problems = append(problems, "no *Service fields found on the Client struct -- the guard is not reading the source")
	}

	// The read surface is the methods DECLARED on each wired service, and that
	// is all of it only while nothing is promoted into it: an embedded field
	// in a service -- or in Client itself -- hands callers methods this guard
	// never reads, and an alias puts them on another type's name.
	problems = append(problems, surfaceShapeProblems(pkg, fields)...)

	methods := serviceMethods(pkg)
	index := indexFuncs(pkg)

	// reads: what a probe is required for. unknown: what the guard could not
	// classify, which is required to be declared rather than assumed harmless.
	reads := map[string]bool{}
	writes := map[string]bool{}
	unknown := map[string]bool{}
	exported := map[string]bool{}
	record := func(qualified string, decl *ast.FuncDecl) {
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
	for service, field := range fields {
		for name, decl := range methods[service] {
			if ast.IsExported(name) {
				record(field+"."+name, decl)
			}
		}
	}
	// Client's own exported methods are surface too: a client.Ping would reach
	// the server through no service at all. A probe calls one as c.Ping(…).
	for key, decl := range index {
		if name := strings.TrimPrefix(key, "Client."); name != key && decl.Recv != nil && ast.IsExported(name) {
			record(key, decl)
		}
	}

	probes, shape := probesIn(suite, suitePath)
	problems = append(problems, shape...)
	if len(probes) == 0 && len(shape) == 0 {
		problems = append(problems, "no probes parsed out of "+suitePath+":readProbes -- the guard is not reading the table")
	}
	probed := map[string]bool{}
	for _, probe := range probes {
		if probed[probe.name] {
			problems = append(problems, suitePath+":readProbes has two probes named "+strconv.Quote(probe.name)+
				". A duplicate makes the table look one method wider than it is")
		}
		probed[probe.name] = true
	}

	// 1. The guard proper: a read with no probe and no recorded reason.
	for _, name := range sortedKeys(reads) {
		if _, excluded := exclusions[name]; probed[name] || excluded {
			continue
		}
		problems = append(problems, name+" issues a read and has no probe in "+suitePath+":readProbes, so the "+
			"cross-merchant isolation assertion does not cover it. Add a probe, or add the method to "+
			"readProbeExclusions with the reason it cannot leak a row across namespaces")
	}

	// 2. A method the classifier could not read a verb out of. Silence here is
	// what the guard is for, so it is reported rather than assumed benign.
	for _, name := range sortedKeys(unknown) {
		// A probed method is covered whatever the classifier made of it: check 4
		// binds a probe to the endpoint it actually calls, so the isolation
		// matrix really does exercise this one. Failing here anyway would also
		// have no fix -- an exclusion for a probed method is refused below, on
		// purpose -- so the requirement would be unsatisfiable. If the probe is
		// ever removed, this method lands here unprobed and fails then.
		if _, excluded := exclusions[name]; probed[name] || excluded {
			continue
		}
		problems = append(problems, name+" is an exported method the guard could not classify -- it "+
			"reaches no HTTP verb through any call this parser follows. Either it issues no request (say so "+
			"in readProbeExclusions) or the classifier cannot see its verb, in which case fix the classifier "+
			"rather than the method: an unclassified read needs no probe and says nothing, which is the "+
			"defect this test exists to prevent")
	}

	// 3. A probe naming a method that no longer exists still runs, still passes,
	// and covers nothing -- the same silence from the other direction.
	for _, probe := range probes {
		if !reads[probe.name] && !unknown[probe.name] {
			problems = append(problems, suitePath+":readProbes probes "+strconv.Quote(probe.name)+", which is not "+
				"a read method on any service wired to Client. A renamed or removed method leaves a probe "+
				"that asserts nothing")
		}
	}

	// 4. A probe's NAME is what the guard counts as coverage, and its find
	// closure is what actually runs. A probe named for one endpoint that calls
	// another satisfies the count while leaving the named endpoint unprobed.
	for _, probe := range probes {
		if len(probe.calls) == 0 {
			problems = append(problems, suitePath+":readProbes entry "+strconv.Quote(probe.name)+" has a find "+
				"closure that calls no client method the parser can see, so nothing ties the name to what runs")
			continue
		}
		if !probe.calls[probe.name] {
			problems = append(problems, suitePath+":readProbes entry "+strconv.Quote(probe.name)+" calls "+
				strings.Join(sortedKeys(probe.calls), ", ")+", not "+probe.name+". The name is what this guard "+
				"counts as coverage, so a mismatch reports a method as probed while another one is")
		}
	}

	// 5. An exclusion that no longer names a method, or that nobody gave a
	// reason for, is a hole with a note over it.
	for _, name := range stringKeys(exclusions) {
		if !exported[name] {
			problems = append(problems, "readProbeExclusions lists "+strconv.Quote(name)+", which is not an "+
				"exported method on a service wired to Client. Drop the entry -- an exclusion for a method "+
				"that does not exist would also excuse a future method that happens to take the name")
		}
		if strings.TrimSpace(exclusions[name]) == "" {
			problems = append(problems, "readProbeExclusions["+strconv.Quote(name)+"] has a blank reason. An "+
				"exclusion is a claim that a read cannot leak a row across namespaces; an unargued one is "+
				"the silence this test replaces")
		}
		if writes[name] {
			problems = append(problems, "readProbeExclusions lists "+strconv.Quote(name)+", which the guard "+
				"classifies as a write. Excluding a write says nothing and hides the entry that would "+
				"matter if the method ever grew a read")
		}
		if probed[name] {
			problems = append(problems, "readProbeExclusions lists "+strconv.Quote(name)+", which is also "+
				"probed. One of the two is wrong, and leaving both means that deleting the probe later "+
				"would silently fall back on the exclusion instead of failing")
		}
	}
	return reads, problems
}

// surfaceShapeProblems fails closed on every way a caller can reach a method
// readProbeProblems does not list. It reads the methods declared on Client and
// on each *FooService an exported Client field holds, so anything else that
// would hand a caller a method is refused: an embedded field in Client or in a
// service, whose methods are promoted; an aliased service, whose methods are
// declared under another name; a service that is not a struct declared here; an
// exported Client field of any other type -- an interface, a func, another
// package's service; an exported field on a service, reachable as
// client.Tags.Search; and a service another exported type -- HealthClient --
// holds and Client does not.
func surfaceShapeProblems(pkg map[string]*ast.File, fields map[string]string) []string {
	var problems []string
	types := indexTypes(pkg)
	check := func(owner string, st *ast.StructType, reads func(ast.Expr) bool) {
		for _, field := range st.Fields.List {
			if len(field.Names) == 0 {
				problems = append(problems, owner+" embeds "+exprString(field.Type)+", whose promoted methods are "+
					"part of the read surface a caller has and are not read by this guard. Declare the methods "+
					"on the service, or teach the guard the promoted method set")
				continue
			}
			for _, name := range field.Names {
				if ast.IsExported(name.Name) && !reads(field.Type) {
					problems = append(problems, owner+"."+name.Name+" is an exported "+typeDescription(field.Type)+
						", and a method a caller reaches through it is part of the read surface this guard does "+
						"not read. Hold it as a *FooService on Client, or teach the guard the shape")
				}
			}
		}
	}
	if spec, ok := types["Client"]; !ok {
		problems = append(problems, "the package declares no Client, so the guard has no surface to read")
	} else if st, ok := unparen(spec.Type).(*ast.StructType); ok && !spec.Assign.IsValid() {
		check("Client", st, func(typ ast.Expr) bool {
			star, ok := unparen(typ).(*ast.StarExpr)
			if !ok {
				return false
			}
			ident, ok := unparen(star.X).(*ast.Ident)
			return ok && strings.HasSuffix(ident.Name, "Service")
		})
	} else {
		problems = append(problems, "Client is not a struct declared in this package, so the guard cannot read its fields")
	}
	for _, service := range stringKeys(fields) {
		spec, ok := types[service]
		switch {
		case !ok:
			problems = append(problems, "Client."+fields[service]+" is a *"+service+", which this package does "+
				"not declare, so the guard cannot read its methods")
		case spec.Assign.IsValid():
			problems = append(problems, service+" is an alias, so its methods are declared under another type's "+
				"name and the guard does not read them")
		default:
			st, ok := unparen(spec.Type).(*ast.StructType)
			if !ok {
				problems = append(problems, service+" is not a struct, so the guard cannot tell which methods a "+
					"caller reaches through Client."+fields[service])
				continue
			}
			check(service, st, func(ast.Expr) bool { return false })
		}
	}
	// Another exported type holding a service -- HealthClient holds Health --
	// is another way in. It is read only for services Client also holds; one
	// reachable through it alone would be a surface no Client field leads to.
	wired := map[string]bool{}
	for service := range fields {
		wired[service] = true
	}
	for _, name := range declaredTypeNames(types) {
		st, ok := unparen(types[name].Type).(*ast.StructType)
		if !ok || name == "Client" || wired[name] || !ast.IsExported(name) {
			continue // Client and the wired services are read above
		}
		for _, field := range st.Fields.List {
			star, ok := unparen(field.Type).(*ast.StarExpr)
			if !ok {
				continue
			}
			ident, ok := unparen(star.X).(*ast.Ident)
			if !ok || !strings.HasSuffix(ident.Name, "Service") || wired[ident.Name] {
				continue
			}
			for _, fieldName := range field.Names {
				if ast.IsExported(fieldName.Name) {
					problems = append(problems, name+"."+fieldName.Name+" holds a *"+ident.Name+" that no Client "+
						"field holds, so its methods reach callers through a surface this guard does not read")
				}
			}
		}
	}
	return problems
}

func declaredTypeNames(types typeIndex) []string {
	out := make([]string, 0, len(types))
	for name := range types {
		out = append(out, name)
	}
	return sortedStrings(out)
}

// typeDescription names a field's type for a message, where exprString would
// render an interface or a func as "?".
func typeDescription(typ ast.Expr) string {
	switch unparen(typ).(type) {
	case *ast.InterfaceType:
		return "interface"
	case *ast.FuncType:
		return "func"
	}
	return exprString(typ)
}

// --- classifying a method by the verb it sends ---------------------------------

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
// `s.cache.do(ctx, http.MethodDelete, …)` register as a transport call on main,
// and scanning every argument let a verb-shaped path segment or header value
// stand in for the method -- either of which turns a read into a write and drops
// it out of the probe requirement. The verb of a request is the argument in the
// method position of the call that sends it, and nothing else.
//
// On this line every transport helper is a method on *Client, so the verb is
// argument 1 for all of them -- main's free doData[T](ctx, c, method, …) has it
// at 2. TestTransportCallsMatchTheHelpersSignatures pins each index to the
// parameter transport.go names `method`.
//
// verbArg -1 means the call carries no verb and is followed into instead;
// doUnversioned builds its own request.
var transportCalls = map[string]struct {
	shape   string // "client" | "http"
	verbArg int
}{
	"doData":                {"client", 1}, // s.client.doData(ctx, method, …)
	"doList":                {"client", 1}, // s.client.doList(ctx, method, …)
	"do":                    {"client", 1}, // s.client.do(ctx, method, …)
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
	// what it cannot tell as something a human has to declare. A revision on
	// main defaulted this to "not a read", which reproduced the defect inside
	// the check written to prevent it: a read the classifier could not see
	// needed no probe and said nothing.
	verbUnknown classification = iota
	verbRead
	verbWrite
)

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
// bodies name all of them. That would classify every Delete as a read.
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
// name. Every transport call on this line is a selector: a method on the client,
// or a function of net/http.
func matchesTransportShape(fun ast.Expr, shape, recv, recvType string) bool {
	sel, ok := unparen(fun).(*ast.SelectorExpr)
	if !ok {
		return false
	}
	switch shape {
	case "http":
		return isIdent(sel.X, "http")
	case "client":
		if recv == "" {
			return false
		}
		// c.doRaw(…) from inside a Client method.
		if isIdent(sel.X, recv) {
			return recvType == "Client"
		}
		// s.client.do(…) from a service method, and only that field.
		if inner, ok := unparen(sel.X).(*ast.SelectorExpr); ok && inner.Sel.Name == "client" {
			return isIdent(inner.X, recv)
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
	switch arg := unparen(call.Args[i]).(type) {
	case *ast.SelectorExpr:
		if isIdent(arg.X, "http") {
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

// calleeName is the bare name a call expression invokes, through a selector.
func calleeName(fun ast.Expr) string {
	switch f := unparen(fun).(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
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

// The verb indexes above are part of the rule, not a detail: reading the wrong
// argument reads the PATH as the verb, finds none, and leaves every read
// unclassified. Each client helper's index must be the parameter transport.go
// names `method`, and a helper that carries no verb must take none.
func TestTransportCallsMatchTheHelpersSignatures(t *testing.T) {
	methods := declaredMethods(parsePackageSource(t))
	for name, transport := range transportCalls {
		if transport.shape != "client" {
			continue
		}
		m, ok := methods["Client."+name]
		if !ok {
			t.Errorf("transportCalls names %s, which *Client does not declare -- a renamed helper is no longer recognized as a transport call", name)
			continue
		}
		params := paramNames(m.decl.Type.Params)
		if transport.verbArg < 0 {
			for _, p := range params {
				if p == "method" {
					t.Errorf("Client.%s takes a method parameter, but transportCalls follows it as verbless", name)
				}
			}
			continue
		}
		if transport.verbArg >= len(params) || params[transport.verbArg] != "method" {
			t.Errorf("Client.%s's parameters are %v; transportCalls reads its verb from argument %d, which is not `method`",
				name, params, transport.verbArg)
		}
	}
	// The other direction needs no table of its own. A *Client helper missing
	// from transportCalls is followed into like any other callee, and the verb
	// it was handed reaches doRaw as its `method` parameter -- an identifier,
	// which verbAt reads as unknown. So a read sent through a new helper fails
	// closed, as an unclassifiable method, rather than passing as a write.
}

// --- reading the probe table ---------------------------------------------------

// probe is one row of the readProbes table: the method it CLAIMS to cover, and
// the client methods its find closure actually calls.
type probe struct {
	name  string
	calls map[string]bool
}

// parseFileOrFatal parses one source file of this repository.
func parseFileOrFatal(t *testing.T, path string) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return file
}

// probesIn extracts the entries of the []readProbe literal that readProbes
// returns, and every way the table's shape stopped it from reading them.
//
// ONLY A SINGLE TOP-LEVEL RETURN COUNTS. Earlier revisions on main took every
// []readProbe literal in the function, then every return anywhere under it --
// so a literal in a branch that never runs (`if false { return []readProbe{…} }`)
// registered as coverage for a method nothing probes. Requiring one return,
// declared directly in the function body, makes the table's shape part of what
// the guard checks. If it is ever restructured -- built by a helper, appended to
// in a loop, returned from a branch -- this reports it, which is the safe
// direction.
func probesIn(file *ast.File, path string) ([]probe, []string) {
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
			return nil, []string{path + ":readProbes must have exactly one return, declared in the function body, " +
				"returning a []readProbe literal (found " + strconv.Itoa(returns) + " return statements). The guard " +
				"reads that literal; a second one -- in a branch, or unreachable -- would register as coverage " +
				"for probes nothing runs"}
		}
		const unreadable = ":readProbes does not return a []readProbe composite literal, so the guard cannot read the table"
		if len(top.Results) != 1 {
			return nil, []string{path + unreadable}
		}
		lit, ok := unparen(top.Results[0]).(*ast.CompositeLit)
		if !ok {
			return nil, []string{path + unreadable}
		}
		array, ok := unparen(lit.Type).(*ast.ArrayType)
		if !ok || array.Len != nil || !isIdent(array.Elt, "readProbe") {
			return nil, []string{path + unreadable}
		}

		var out []probe
		var problems []string
		for i, elt := range lit.Elts {
			entry, ok := unparen(elt).(*ast.CompositeLit)
			if !ok {
				problems = append(problems, path+":readProbes entry "+strconv.Itoa(i)+" is not a struct literal the guard can read")
				continue
			}
			name, ok := stringField(entry, "name")
			if !ok {
				problems = append(problems, path+":readProbes entry "+strconv.Itoa(i)+" has no literal name")
				continue
			}
			// How an endpoint declines an out-of-scope row is per-probe by
			// design, and the zero value is filteredEmpty -- so a probe that
			// leaves it out is a claim nobody made, about the one answer a
			// filtered run must match.
			if !hasField(entry, "filtered") {
				problems = append(problems, path+":readProbes entry "+strconv.Quote(name)+" does not set filtered, "+
					"so it inherits the zero value, filteredEmpty, without anyone having said that is how its "+
					"endpoint declines a row out of scope")
			}
			out = append(out, probe{name: name, calls: closureClientCalls(entry, "find")})
		}
		return out, problems
	}
	return nil, []string{path + " declares no readProbes function, so the guard has no table to read"}
}

// closureClientCalls collects the "Field.Method" of every call a probe's find
// closure makes ON ITS OWN CLIENT PARAMETER -- "Client.Method" for a method
// declared on Client itself -- what the probe actually
// exercises, as opposed to what its name says it does.
//
// Binding to the closure's own client PARAMETER matters: matching any
// `x.y.z(...)` shape, as a revision on main did, let a call on some other value
// (a fake, a fixture field) register as coverage and satisfy an entry whose real
// client call went somewhere else. It is also why the table passes the client in
// rather than reading it off a shared struct.
func closureClientCalls(entry *ast.CompositeLit, field string) map[string]bool {
	out := map[string]bool{}
	for _, elt := range entry.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok || !isIdent(kv.Key, field) {
			continue
		}
		lit, ok := unparen(kv.Value).(*ast.FuncLit)
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
			// c.Ping(…): a method declared on Client itself.
			if isIdent(outer.X, client) {
				out["Client."+outer.Sel.Name] = true
				return true
			}
			inner, ok := unparen(outer.X).(*ast.SelectorExpr)
			if !ok || !isIdent(inner.X, client) {
				return true
			}
			out[inner.Sel.Name+"."+outer.Sel.Name] = true
			return true
		})
	}
	return out
}

// clientParamName is the name the find closure gives its *octonomy.Client
// parameter.
func clientParamName(sig *ast.FuncType) string {
	if sig == nil || sig.Params == nil {
		return ""
	}
	for _, param := range sig.Params.List {
		star, ok := unparen(param.Type).(*ast.StarExpr)
		if !ok {
			continue
		}
		var name string
		switch typ := unparen(star.X).(type) {
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

// hasField reports whether a keyed struct literal sets key.
func hasField(lit *ast.CompositeLit, key string) bool {
	for _, elt := range lit.Elts {
		if kv, ok := elt.(*ast.KeyValueExpr); ok && isIdent(kv.Key, key) {
			return true
		}
	}
	return false
}

// stringField returns the value of a string-literal field in a struct literal.
func stringField(lit *ast.CompositeLit, key string) (string, bool) {
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok || !isIdent(kv.Key, key) {
			continue
		}
		if v := stringLit(kv.Value); v != "" {
			return v, true
		}
	}
	return "", false
}

// --- the table must run -------------------------------------------------------

// isolationRequirement is one run an isolation test must make under an EXACT
// merchant grant -- a client from h.merchantClient, never the wildcard.
type isolationRequirement struct {
	expect        string // the outcome constant the run expects
	includeGlobal bool   // the run must carry WithIncludeGlobal
}

// isolationTests names the tests that run the probe table, and the runs under
// an exact grant each one must make.
//
// WHICH TOKEN A RUN USES IS THE TEST. The wildcard grant matches every
// partition, global included, so under it authorization never refuses anything
// and include_global's opt-in always succeeds: it can demonstrate the server's
// namespace FILTER and nothing else. The refusal path (outcomeForbidden) and
// the fail-closed branch of include_global run only under an exact grant, and
// an exact-grant visible run is the control that proves the grant reaches its
// own rows at all -- without it, a token that reached nothing would pass every
// negative.
var isolationTests = map[string][]isolationRequirement{
	"TestIntegration_NamespaceIsolation": {
		{expect: "outcomeVisible"},
		{expect: "outcomeFiltered"},
		{expect: "outcomeForbidden"},
	},
	"TestIntegration_IncludeGlobalFailsClosed": {
		{expect: "outcomeVisible", includeGlobal: true},
		{expect: "outcomeFiltered", includeGlobal: true},
	},
}

// The probe table is worth something only if the isolation run executes it,
// under the grants that make its assertions mean anything.
//
// The issue this closes (#97) found the shape that fails here: a ported
// isolation test that the only real-server job's selector did not match would
// sit in the tree passing nothing, and its job would report green. So the
// suite is read for what makes it RUN -- built by the runners' tag, gated by a
// skip that the required run turns into a failure, its tests selected by the
// runners' prefix, each of them running the matrix over readProbes -- and for
// what makes it MEAN something: the runs listed in isolationTests, under exact
// grants. The runners themselves are pinned (isolationRecipePin below, and the
// isolation step in smokeprobes_test.go's smokeJobPin).
//
// WHERE IT ENDS, as with the smoke guard: it reads presence, not reachability,
// and it proves which runs a test makes, not that their assertions are right.
// The assertions are runProbeMatrix's, and a reviewer's.
func TestTheIsolationSuiteRunsItsProbes(t *testing.T) {
	parsed := map[string]*ast.File{}
	for _, path := range []string{integrationSuiteFile, integrationHarnessFile} {
		raw, err := ioutil.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, problem := range smokeBuildTagProblems(string(raw)) {
			t.Errorf("%s: %s", path, problem)
		}
		parsed[path] = parseFileOrFatal(t, path)
	}
	for _, problem := range isolationSuiteProblems(parsed[integrationSuiteFile], parsed[integrationHarnessFile]) {
		t.Error(problem)
	}
}

// isolationSuiteProblems is TestTheIsolationSuiteRunsItsProbes over parsed
// files, so the fixtures below can drive it.
func isolationSuiteProblems(suite, harnessFile *ast.File) []string {
	var problems []string
	decls := append(append([]ast.Decl{}, suite.Decls...), harnessFile.Decls...)
	helpers := readGateHelpers(decls, "loadHarness")
	funcs := map[string]*ast.FuncDecl{}
	for _, decl := range decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Body != nil {
			funcs[fn.Name.Name] = fn
		}
	}

	// The gate: the suite's one sanctioned skip, held to newSmokeClient's shape
	// so that OCTONOMY_SMOKE_REQUIRED=1 turns it into a failure.
	if gate, ok := funcs["loadHarness"]; !ok {
		problems = append(problems, "the isolation suite declares no loadHarness, the gate its tests skip "+
			"through; the guard cannot hold the skip to the required run")
	} else if why := checkSmokeGate(gate, helpers); why != "" {
		problems = append(problems, why)
	}

	// The runner: it must iterate the table the probe guard reads, and run each
	// probe's find. A matrix over a different list -- or a copy of the table --
	// would leave readProbes checked and unexecuted.
	if matrix, ok := funcs["runProbeMatrix"]; !ok {
		problems = append(problems, "the isolation suite declares no runProbeMatrix, so nothing runs readProbes")
	} else {
		problems = append(problems, matrixProblems(matrix, helpers)...)
	}

	// Every caller of the matrix is a test the runners select, calling it from
	// its own body: a call from a helper, a closure or a renamed test runs only
	// if something else happens to reach it, which is the ceremonial green #97
	// was opened for.
	for _, name := range sortedFuncNames(funcs) {
		fn := funcs[name]
		top, nested := callsOf(fn.Body, "runProbeMatrix")
		if top+nested == 0 {
			continue
		}
		if !strings.HasPrefix(name, isolationTestPrefix) {
			problems = append(problems, name+" runs the probe matrix but is not a "+isolationTestPrefix+
				" function, so the isolation runners' -run '^"+isolationTestPrefix+"' does not select it")
		}
		if nested > 0 {
			problems = append(problems, name+" runs the probe matrix from inside a function literal, which "+
				"the guard cannot hold to running at all; call it from the test's own body")
		}
	}

	for _, name := range sortedFuncNames(funcs) {
		if !strings.HasPrefix(name, isolationTestPrefix) {
			continue
		}
		problems = append(problems, isolationTestProblems(funcs[name], helpers)...)
	}

	for _, name := range isolationTestNames() {
		fn, ok := funcs[name]
		if !ok {
			problems = append(problems, "the isolation suite declares no "+name+"; it is one of the tests "+
				"isolationTests requires, and without it the runs it names are made by nothing")
			continue
		}
		if top, _ := callsOf(fn.Body, "loadHarness"); top == 0 {
			problems = append(problems, name+" does not call loadHarness from its own body, so nothing "+
				"gates it on the harness or fails it when the required run has none")
		}
		if top, _ := callsOf(fn.Body, "runProbeMatrix"); top == 0 {
			problems = append(problems, name+" does not run the probe matrix, so readProbes is checked and "+
				"never executed")
			continue
		}
		problems = append(problems, exactGrantProblems(fn, isolationTests[name])...)
	}
	return problems
}

// isolationTestProblems refuses the two ways a selected test can turn green
// without running what follows: a skip, and an early return. The suite's only
// skip is loadHarness's, behind the required gate.
func isolationTestProblems(fn *ast.FuncDecl, helpers smokeHelpers) []string {
	var problems []string
	for _, why := range skipsIn(fn, helpers) {
		problems = append(problems, fn.Name.Name+" "+why+"; an isolation test that can skip itself is a "+
			"green run that asserted nothing after the skip. Fail instead -- loadHarness is the one place "+
			"the isolation run may skip")
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		if _, ok := n.(*ast.ReturnStmt); ok {
			problems = append(problems, fn.Name.Name+" returns early, which leaves the matrix after the "+
				"return unrun while the test stays green; fail with t.Fatal instead")
		}
		return true
	})
	return problems
}

// matrixProblems holds runProbeMatrix to the one shape that asks every probe
// every run:
//
//	func runProbeMatrix(ctx context.Context, t *testing.T, h harness, runs []probeRun) {
//		for _, probe := range readProbes(h) {
//			…
//				for _, run := range runs {
//					…
//						… probe.find(ctx, run.client, run.readNS, run.want, run.extra...)
//
// -- the probe loop at the top of its body, ranging over readProbes called on
// its own harness parameter; the run loop inside it, ranging over the runs
// parameter itself, not a slice of it; and the find call inside that, handed
// the run's own fields. Neither parameter nor loop variable may be bound again,
// and nothing in the function -- a subtest's closure included -- may skip,
// return, break, continue or goto: each is a way to leave a probe or a run out
// of the matrix while the test stays green, and a run the matrix leaves out is
// as unrun as one the test never declared. What the matrix asserts about each
// answer is the reviewer's.
func matrixProblems(fn *ast.FuncDecl, helpers smokeHelpers) []string {
	var problems []string
	for _, why := range skipsIn(fn, helpers) {
		problems = append(problems, "runProbeMatrix "+why+"; a skipped probe is a green run that asserted nothing")
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.ReturnStmt:
			problems = append(problems, "runProbeMatrix returns, in its body or a subtest's, which can end the "+
				"matrix before every probe has asked every run while the test stays green")
		case *ast.BranchStmt:
			problems = append(problems, "runProbeMatrix uses "+node.Tok.String()+", which can leave a probe or a "+
				"run out of the matrix while the test stays green")
		}
		return true
	})

	const shape = "; the matrix must be `for _, probe := range readProbes(h)` at the top of its body, " +
		"`for _, run := range runs` inside it, and `probe.find(ctx, run.client, run.readNS, run.want, " +
		"run.extra...)` inside that, so every probe asks every run"
	var hName, runsName string
	params, types := paramNames(fn.Type.Params), paramTypes(fn.Type)
	for i := range params {
		switch types[i] {
		case "harness":
			hName = params[i]
		case "[]probeRun":
			runsName = params[i]
		}
	}
	if hName == "" || runsName == "" {
		return append(problems, "runProbeMatrix takes no harness and []probeRun parameters"+shape)
	}
	for _, name := range []string{hName, runsName} {
		if len(bindingsOf(fn.Body, name)) > 0 {
			problems = append(problems, "runProbeMatrix binds its parameter "+name+" again, so the matrix may not "+
				"range over what it was handed")
		}
	}

	probeVar := ""
	var probeLoop *ast.RangeStmt
	for _, stmt := range fn.Body.List {
		loop, ok := stmt.(*ast.RangeStmt)
		if !ok {
			continue
		}
		call, ok := unparen(loop.X).(*ast.CallExpr)
		if !ok || !isIdent(call.Fun, "readProbes") || len(call.Args) != 1 || !isIdent(call.Args[0], hName) {
			continue
		}
		if v, ok := loop.Value.(*ast.Ident); ok {
			probeVar, probeLoop = v.Name, loop
		}
	}
	if probeLoop == nil {
		return append(problems, "runProbeMatrix does not range over readProbes("+hName+") at the top of its "+
			"body, so the table TestEveryReadMethodHasANamespaceProbe checks is not the one the isolation run "+
			"executes"+shape)
	}

	runVar := ""
	var runLoop *ast.RangeStmt
	ast.Inspect(probeLoop.Body, func(n ast.Node) bool {
		if loop, ok := n.(*ast.RangeStmt); ok && runLoop == nil && isIdent(loop.X, runsName) {
			if v, ok := loop.Value.(*ast.Ident); ok {
				runVar, runLoop = v.Name, loop
			}
		}
		return runLoop == nil
	})
	if runLoop == nil {
		return append(problems, "runProbeMatrix does not range over all of "+runsName+" inside the probe loop, so "+
			"a run a test declares need not be asked"+shape)
	}

	found := false
	ast.Inspect(runLoop.Body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && isFindOfRun(call, probeVar, runVar) {
			found = true
		}
		return !found
	})
	if !found {
		problems = append(problems, "runProbeMatrix does not call "+probeVar+".find with "+runVar+"'s own client, "+
			"namespace, fixture and options inside the run loop"+shape)
	}
	for _, name := range []string{probeVar, runVar} {
		if n := len(bindingsOf(fn.Body, name)); n != 1 {
			problems = append(problems, "runProbeMatrix binds "+name+" "+strconv.Itoa(n)+" times; only its loop "+
				"may, or the find call need not see the probe and run the loops are on")
		}
	}
	return problems
}

// isFindOfRun reports whether call is probe.find(<ctx>, run.client,
// run.readNS, run.want, run.extra...).
func isFindOfRun(call *ast.CallExpr, probeVar, runVar string) bool {
	sel, ok := unparen(call.Fun).(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "find" || !isIdent(sel.X, probeVar) || len(call.Args) != 5 || !call.Ellipsis.IsValid() {
		return false
	}
	for i, field := range []string{"client", "readNS", "want", "extra"} {
		arg, ok := unparen(call.Args[i+1]).(*ast.SelectorExpr)
		if !ok || arg.Sel.Name != field || !isIdent(arg.X, runVar) {
			return false
		}
	}
	return true
}

// exactGrantProblems reads the []probeRun literals a test hands runProbeMatrix
// and reports each requirement no run meets under an exact merchant grant.
func exactGrantProblems(fn *ast.FuncDecl, want []isolationRequirement) []string {
	type run struct {
		exact, includeGlobal bool
		expect               string
	}
	var runs []run
	var problems []string
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok || !isIdent(call.Fun, "runProbeMatrix") {
			return true
		}
		if len(call.Args) != 4 {
			problems = append(problems, fn.Name.Name+" calls runProbeMatrix with "+strconv.Itoa(len(call.Args))+
				" arguments; the guard reads its runs from the fourth")
			return true
		}
		lit, ok := unparen(call.Args[3]).(*ast.CompositeLit)
		if !ok {
			problems = append(problems, fn.Name.Name+" hands runProbeMatrix runs that are not a []probeRun "+
				"literal, so the guard cannot read which grant each run uses")
			return true
		}
		for _, elt := range lit.Elts {
			entry, ok := unparen(elt).(*ast.CompositeLit)
			if !ok {
				continue
			}
			var r run
			for _, field := range entry.Elts {
				kv, ok := field.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, _ := kv.Key.(*ast.Ident)
				if key == nil {
					continue
				}
				switch key.Name {
				case "client":
					r.exact = boundToCall(fn, kv.Value, "merchantClient")
				case "expect":
					if ident, ok := unparen(kv.Value).(*ast.Ident); ok {
						r.expect = ident.Name
					}
				case "extra":
					r.includeGlobal = carriesCall(fn, kv.Value, "WithIncludeGlobal")
				}
			}
			runs = append(runs, r)
		}
		return true
	})
	for _, req := range want {
		met := false
		for _, r := range runs {
			if r.exact && r.expect == req.expect && (!req.includeGlobal || r.includeGlobal) {
				met = true
			}
		}
		if !met {
			what := req.expect + " run under an exact merchant grant"
			if req.includeGlobal {
				what += " carrying WithIncludeGlobal"
			}
			problems = append(problems, fn.Name.Name+" makes no "+what+" (a client from h.merchantClient). "+
				"The wildcard grant matches every partition, so under it authorization never refuses and "+
				"include_global never fails closed; a run on it cannot stand in for this one")
		}
	}
	return problems
}

// boundToCall reports whether expr is an identifier bound exactly once in fn,
// to a call of a method named method: `clientA := h.merchantClient(t, …)`.
func boundToCall(fn *ast.FuncDecl, expr ast.Expr, method string) bool {
	ident, ok := unparen(expr).(*ast.Ident)
	if !ok {
		return false
	}
	bindings := bindingsOf(fn.Body, ident.Name)
	if len(bindings) != 1 {
		return false
	}
	assign, ok := bindings[0].(*ast.AssignStmt)
	if !ok || assign.Tok != token.DEFINE || len(assign.Lhs) != len(assign.Rhs) {
		return false
	}
	for i, lhs := range assign.Lhs {
		if !isIdent(lhs, ident.Name) {
			continue
		}
		call, ok := unparen(assign.Rhs[i]).(*ast.CallExpr)
		if !ok {
			return false
		}
		sel, ok := unparen(call.Fun).(*ast.SelectorExpr)
		return ok && sel.Sel.Name == method
	}
	return false
}

// carriesCall reports whether expr -- inline, or through an identifier bound
// exactly once in fn -- contains a call to a function or method named name.
func carriesCall(fn *ast.FuncDecl, expr ast.Expr, name string) bool {
	if ident, ok := unparen(expr).(*ast.Ident); ok {
		bindings := bindingsOf(fn.Body, ident.Name)
		if len(bindings) != 1 {
			return false
		}
		assign, ok := bindings[0].(*ast.AssignStmt)
		if !ok || assign.Tok != token.DEFINE || len(assign.Lhs) != len(assign.Rhs) {
			return false
		}
		for i, lhs := range assign.Lhs {
			if isIdent(lhs, ident.Name) {
				expr = assign.Rhs[i]
			}
		}
	}
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && calleeName(call.Fun) == name {
			found = true
		}
		return !found
	})
	return found
}

// callsOf counts the calls to the plain function name in body: at its top
// level, and inside function literals.
func callsOf(body *ast.BlockStmt, name string) (top, nested int) {
	var walk func(n ast.Node, inLit bool)
	walk = func(root ast.Node, inLit bool) {
		ast.Inspect(root, func(n ast.Node) bool {
			if lit, ok := n.(*ast.FuncLit); ok && n != root {
				walk(lit.Body, true)
				return false
			}
			if call, ok := n.(*ast.CallExpr); ok && isIdent(call.Fun, name) {
				if inLit {
					nested++
				} else {
					top++
				}
			}
			return true
		})
	}
	if body != nil {
		walk(body, false)
	}
	return top, nested
}

func isolationTestNames() []string {
	out := make([]string, 0, len(isolationTests))
	for name := range isolationTests {
		out = append(out, name)
	}
	return sortedStrings(out)
}

func sortedFuncNames(funcs map[string]*ast.FuncDecl) []string {
	out := make([]string, 0, len(funcs))
	for name := range funcs {
		out = append(out, name)
	}
	return sortedStrings(out)
}

func sortedStrings(in []string) []string {
	set := make(map[string]bool, len(in))
	for _, s := range in {
		set[s] = true
	}
	return sortedKeys(set)
}

// --- the isolation runner, pinned ----------------------------------------------

// isolationTarget is the make target that runs the isolation suite.
const isolationTarget = "test-integration"

// isolationRecipePin is the Makefile's `test-integration:` rule and recipe, as
// makeRule reads them. It is pinned for the reason the smoke recipe is
// (smokeprobes_test.go): a reader of shell never converged here, and a pin makes
// every change a reviewed one.
//
// WHAT IT WAS CHECKED AGAINST, so the next edit knows what to re-check: run
// against a booted Octonomy (`make dev-server`) on go1.13.15 and go1.25 for #97,
// every TestIntegration_ function ran and passed, and the recipe (a) selects
// every TestIntegration_ function and nothing narrower, (b) passes -count=1, (c)
// builds with -tags=integration, which TestTheIsolationSuiteRunsItsProbes holds
// both suite files to, and (d) fails when the run fails. The CI side is the
// isolation step of the smoke job, pinned in smokeJobPin, which adds (e)
// OCTONOMY_SMOKE_REQUIRED=1 on the go1.13 toolchain. Change the recipe, re-check
// (a)-(d), then update this pin in the same commit.
const isolationRecipePin = `test-integration: ## Run the namespace isolation suite against a booted harness (see dev-server)
	@if [ -f .octonomy-harness.env ]; then set -a; . ./.octonomy-harness.env; set +a; fi; \
	go test -tags=integration -count=1 -run '^TestIntegration_' -v ./...`

// `make test-integration` runs the pinned recipe, and nothing elsewhere in the
// Makefile changes how a recipe for it runs.
func TestIsolationSelectorRunsEveryTestIntegrationFunction(t *testing.T) {
	raw, err := ioutil.ReadFile("Makefile")
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	if got := makeRule(string(raw), isolationTarget); got != isolationRecipePin {
		t.Errorf("Makefile's `%s:` rule is not the pinned one. Re-check what the comment above "+
			"isolationRecipePin lists, then update the pin in the same commit.\n--- got\n%s\n--- pinned\n%s",
			isolationTarget, got, isolationRecipePin)
	}
	for _, why := range makefileProblems(string(raw), isolationTarget) {
		t.Errorf("Makefile: %s", why)
	}
}

// --- fixtures: the guards' own behaviour ---------------------------------------

// The guards above read this repository, so on a correct tree they pass whether
// or not they work. These drive them over synthetic source instead.

// probeFixturePackage is a small SDK: two services on Client, reads, a write,
// a read two calls away like Health.Live, and nothing unclassifiable.
const probeFixturePackage = `package octonomy

type Client struct {
	Tags   *TagService
	Health *HealthService
}

type TagService struct{ client *Client }
type HealthService struct{ client *Client }

func (s *TagService) List(ctx context.Context) error {
	return s.client.doList(ctx, http.MethodGet, "/tags", nil, nil)
}
func (s *TagService) Get(ctx context.Context, id string) error {
	return s.client.doData(ctx, http.MethodGet, "/tags/"+id, nil, nil, nil)
}
func (s *TagService) Delete(ctx context.Context, id string) error {
	return s.client.do(ctx, http.MethodDelete, "/tags/"+id, nil, nil)
}
func (s *HealthService) Live(ctx context.Context) error { return s.probe(ctx, "/health/live") }
func (s *HealthService) probe(ctx context.Context, p string) error {
	_, _, err := s.client.doUnversioned(ctx, p)
	return err
}
func (c *Client) doUnversioned(ctx context.Context, p string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p, nil)
	_ = req
	return 0, nil, err
}
`

// probeFixtureSuite renders a suite whose readProbes returns entries.
func probeFixtureSuite(entries string) string {
	return `package octonomy_test

type readProbe struct {
	name string
	find func(ctx context.Context, c *octonomy.Client) (bool, error)
}

func readProbes(h harness) []readProbe {
	return []readProbe{
` + entries + `
	}
}
`
}

// probeEntry renders one table entry named name whose find calls call.
func probeEntry(name, call string) string {
	return `		{name: "` + name + `", filtered: filteredEmpty, find: func(ctx context.Context, c *octonomy.Client) (bool, error) { return true, c.` + call + `(ctx) }},
`
}

// clientPing is a read declared on Client itself, reached as client.Ping.
const clientPing = `
func (c *Client) Ping(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/ping", nil, nil)
}
`

var (
	cleanProbeEntries    = probeEntry("Tags.List", "Tags.List") + probeEntry("Tags.Get", "Tags.Get")
	cleanProbeExclusions = map[string]string{"Health.Live": "unauthenticated and outside the namespace axis"}
)

// TestReadProbeGuardFailsOnEachCondition is #97's acceptance, one fixture per
// condition the guard must fail on, each against a clean baseline that must not
// fail -- so a fixture cannot pass because everything fails.
func TestReadProbeGuardFailsOnEachCondition(t *testing.T) {
	const unclassifiable = `
func (s *TagService) Peek(ctx context.Context) error {
	send := s.client.doList
	return send(ctx, http.MethodGet, "/tags", nil, nil)
}
`
	for _, tc := range []struct {
		name       string
		pkg        string    // appended to probeFixturePackage
		swap       [2]string // a replacement made in probeFixturePackage first
		entries    string
		exclusions map[string]string
		want       string // a substring of the one problem expected; "" for none
	}{
		{name: "the clean baseline", entries: cleanProbeEntries, exclusions: cleanProbeExclusions},
		{
			name:       "a read with no probe",
			entries:    probeEntry("Tags.List", "Tags.List"),
			exclusions: cleanProbeExclusions,
			want:       "Tags.Get issues a read and has no probe",
		},
		{
			name:       "a probe naming a method that no longer exists",
			entries:    cleanProbeEntries + probeEntry("Tags.Gone", "Tags.Gone"),
			exclusions: cleanProbeExclusions,
			want:       `probes "Tags.Gone", which is not a read method`,
		},
		{
			name:       "a probe whose find calls a different endpoint than its name",
			entries:    probeEntry("Tags.List", "Tags.List") + probeEntry("Tags.Get", "Tags.List"),
			exclusions: cleanProbeExclusions,
			want:       `entry "Tags.Get" calls Tags.List, not Tags.Get`,
		},
		{
			name: "a probe whose find calls nothing on its client",
			entries: probeEntry("Tags.List", "Tags.List") + `		{name: "Tags.Get", filtered: filteredEmpty, find: func(ctx context.Context, c *octonomy.Client) (bool, error) { return true, nil }},
`,
			exclusions: cleanProbeExclusions,
			want:       `entry "Tags.Get" has a find closure that calls no client method`,
		},
		{
			name: "a service that embeds another type's methods",
			pkg: `
type tagReads struct{ client *Client }

func (r *tagReads) Peek(ctx context.Context) error {
	return r.client.doList(ctx, http.MethodGet, "/tags", nil, nil)
}
`,
			swap:       [2]string{"type TagService struct{ client *Client }", "type TagService struct {\n\t*tagReads\n\tclient *Client\n}"},
			entries:    cleanProbeEntries,
			exclusions: cleanProbeExclusions,
			want:       "TagService embeds *tagReads, whose promoted methods are part of the read surface",
		},
		{
			name:       "a Client that embeds a service",
			swap:       [2]string{"\tHealth *HealthService\n}", "\tHealth *HealthService\n\t*HealthService\n}"},
			entries:    cleanProbeEntries,
			exclusions: cleanProbeExclusions,
			want:       "Client embeds *HealthService",
		},
		{
			name:       "a service that is an alias",
			swap:       [2]string{"type HealthService struct{ client *Client }", "type HealthService = healthService\ntype healthService struct{ client *Client }"},
			entries:    cleanProbeEntries,
			exclusions: cleanProbeExclusions,
			want:       "HealthService is an alias",
		},
		{
			name:       "a read on Client itself",
			pkg:        clientPing,
			entries:    cleanProbeEntries,
			exclusions: cleanProbeExclusions,
			want:       "Client.Ping issues a read and has no probe",
		},
		{
			name:       "a read on Client itself, probed",
			pkg:        clientPing,
			entries:    cleanProbeEntries + probeEntry("Client.Ping", "Ping"),
			exclusions: cleanProbeExclusions,
		},
		{
			name:       "an accessor on Client that sends nothing",
			pkg:        "\nfunc (c *Client) Version() string { return \"v\" }\n",
			entries:    cleanProbeEntries,
			exclusions: cleanProbeExclusions,
			want:       "Client.Version is an exported method the guard could not classify",
		},
		{
			name:       "an accessor on Client, excluded with its reason",
			pkg:        "\nfunc (c *Client) Version() string { return \"v\" }\n",
			entries:    cleanProbeEntries,
			exclusions: map[string]string{"Health.Live": "outside the axis", "Client.Version": "sends no request"},
		},
		{
			name:       "an interface on Client",
			swap:       [2]string{"\tHealth *HealthService\n}", "\tHealth *HealthService\n\tReader interface{ Get(ctx context.Context) error }\n}"},
			entries:    cleanProbeEntries,
			exclusions: cleanProbeExclusions,
			want:       "Client.Reader is an exported interface",
		},
		{
			name:       "a func on Client",
			swap:       [2]string{"\tHealth *HealthService\n}", "\tHealth *HealthService\n\tFetch  func(ctx context.Context) error\n}"},
			entries:    cleanProbeEntries,
			exclusions: cleanProbeExclusions,
			want:       "Client.Fetch is an exported func",
		},
		{
			name:       "another package's service on Client",
			swap:       [2]string{"\tHealth *HealthService\n}", "\tHealth *HealthService\n\tRemote *remote.TagService\n}"},
			entries:    cleanProbeEntries,
			exclusions: cleanProbeExclusions,
			want:       "Client.Remote is an exported *remote.TagService",
		},
		{
			name:       "a service nested in a service",
			swap:       [2]string{"type TagService struct{ client *Client }", "type TagService struct {\n\tclient *Client\n\tSearch *SearchService\n}\ntype SearchService struct{ client *Client }"},
			entries:    cleanProbeEntries,
			exclusions: cleanProbeExclusions,
			want:       "TagService.Search is an exported *SearchService",
		},
		{
			name:       "another client type holding a service Client holds",
			pkg:        "\ntype HealthClient struct{ Health *HealthService }\n",
			entries:    cleanProbeEntries,
			exclusions: cleanProbeExclusions,
		},
		{
			name:       "another client type holding a service Client does not",
			pkg:        "\ntype AdminClient struct{ Audit *AuditService }\ntype AuditService struct{ client *Client }\n",
			entries:    cleanProbeEntries,
			exclusions: cleanProbeExclusions,
			want:       "AdminClient.Audit holds a *AuditService that no Client field holds",
		},
		{
			name:       "an unexported field on Client is not surface",
			swap:       [2]string{"\tHealth *HealthService\n}", "\tHealth *HealthService\n\tcache  map[string]string\n}"},
			entries:    cleanProbeEntries,
			exclusions: cleanProbeExclusions,
		},
		{
			name:       "a duplicate name",
			entries:    cleanProbeEntries + probeEntry("Tags.List", "Tags.List"),
			exclusions: cleanProbeExclusions,
			want:       `has two probes named "Tags.List"`,
		},
		{
			name:       "a stale exclusion",
			entries:    cleanProbeEntries,
			exclusions: map[string]string{"Health.Live": "outside the axis", "Health.Gone": "removed"},
			want:       `lists "Health.Gone", which is not an exported method`,
		},
		{
			name:       "an unargued exclusion",
			entries:    cleanProbeEntries,
			exclusions: map[string]string{"Health.Live": "  "},
			want:       `readProbeExclusions["Health.Live"] has a blank reason`,
		},
		{
			name:       "a method whose verb the guard cannot resolve",
			pkg:        unclassifiable,
			entries:    cleanProbeEntries,
			exclusions: cleanProbeExclusions,
			want:       "Tags.Peek is an exported method the guard could not classify",
		},
		{
			name:       "the unresolved method, probed, is covered",
			pkg:        unclassifiable,
			entries:    cleanProbeEntries + probeEntry("Tags.Peek", "Tags.Peek"),
			exclusions: cleanProbeExclusions,
		},
		{
			name:       "the unresolved method, excluded with a reason, is covered",
			pkg:        unclassifiable,
			entries:    cleanProbeEntries,
			exclusions: map[string]string{"Health.Live": "outside the axis", "Tags.Peek": "issues no request"},
		},
		{
			name:       "an excluded write",
			entries:    cleanProbeEntries,
			exclusions: map[string]string{"Health.Live": "outside the axis", "Tags.Delete": "a write"},
			want:       `lists "Tags.Delete", which the guard classifies as a write`,
		},
		{
			name:       "an exclusion that is also probed",
			entries:    cleanProbeEntries + probeEntry("Health.Live", "Health.Live"),
			exclusions: cleanProbeExclusions,
			want:       `lists "Health.Live", which is also probed`,
		},
		{
			name: "a probe that does not say how its endpoint declines",
			entries: probeEntry("Tags.List", "Tags.List") + `		{name: "Tags.Get", find: func(ctx context.Context, c *octonomy.Client) (bool, error) { return true, c.Tags.Get(ctx) }},
`,
			exclusions: cleanProbeExclusions,
			want:       `entry "Tags.Get" does not set filtered`,
		},
		{
			name:       "a health probe that is neither probed nor excluded",
			entries:    cleanProbeEntries,
			exclusions: map[string]string{},
			want:       "Health.Live issues a read and has no probe",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := probeFixturePackage
			if tc.swap[0] != "" {
				if !strings.Contains(src, tc.swap[0]) {
					t.Fatalf("the fixture package has no %q to replace", tc.swap[0])
				}
				src = strings.Replace(src, tc.swap[0], tc.swap[1], 1)
			}
			pkg := parseFixture(t, src+tc.pkg)
			suite := parseFixture(t, probeFixtureSuite(tc.entries))["fixture.go"]
			_, problems := readProbeProblems(pkg, suite, "fixture.go", tc.exclusions)
			if tc.want == "" {
				if len(problems) != 0 {
					t.Fatalf("problems = %v, want none", problems)
				}
				return
			}
			if len(problems) != 1 || !strings.Contains(problems[0], tc.want) {
				t.Fatalf("problems = %v, want exactly one containing %q", problems, tc.want)
			}
		})
	}
}

// The floor and the read set: the fixture package's three reads are found,
// Health.Live's two calls deep, and the write is not one of them.
func TestReadProbeProblemsReturnsTheReadsItFound(t *testing.T) {
	pkg := parseFixture(t, probeFixturePackage)
	suite := parseFixture(t, probeFixtureSuite(cleanProbeEntries))["fixture.go"]
	reads, _ := readProbeProblems(pkg, suite, "fixture.go", cleanProbeExclusions)
	if got := strings.Join(sortedKeys(reads), " "); got != "Health.Live Tags.Get Tags.List" {
		t.Errorf("reads = %q, want the three reads", got)
	}
}

func classifyFixture(t *testing.T, src, method string) classification {
	t.Helper()
	index := indexFuncs(parseFixture(t, src))
	fn, ok := index[method]
	if !ok {
		t.Fatalf("fixture declares no %s", method)
	}
	return classify(fn, index)
}

// main's classifier fixtures, rewritten for this dialect's transport calls: each
// is a defect a previous revision of the classifier accepted.
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
				return s.client.doList(ctx, http.MethodGet, "/tags", nil, nil)
			}`,
		},
		{
			name: "a write is not a read", method: "TagService.Delete", want: verbWrite,
			src: `func (s *TagService) Delete(ctx context.Context, id string) error {
				return s.client.do(ctx, http.MethodDelete, "/tags/"+id, nil, nil)
			}`,
		},
		{
			name: "a verb spelled as a string literal", method: "TagService.Head", want: verbRead,
			src: `func (s *TagService) Head(ctx context.Context) error {
				return s.client.do(ctx, "HEAD", "/tags", nil, nil)
			}`,
		},
		{
			// Health.Live's shape: no verb of its own, two calls deep.
			name: "a verb two calls away", method: "HealthService.Live", want: verbRead,
			src: `func (s *HealthService) Live(ctx context.Context) error { return s.probe(ctx, "/health/live") }
			func (s *HealthService) probe(ctx context.Context, p string) error { _, _, err := s.client.doUnversioned(ctx, p); return err }
			func (c *Client) doUnversioned(ctx context.Context, p string) (int, []byte, error) {
				req, _ := http.NewRequestWithContext(ctx, http.MethodGet, p, nil)
				_ = req
				return 0, nil, nil
			}`,
		},
		{
			// A Client method sending through doRaw, as do, doData and doList do.
			name: "a Client method's own doRaw call", method: "Client.peek", want: verbRead,
			src: `func (c *Client) peek(ctx context.Context) error {
				_, _, err := c.doRaw(ctx, http.MethodGet, "/tags", nil, nil)
				return err
			}`,
		},
		{
			// A read reached through a helper must not be masked by a write the
			// same method issues directly.
			name: "writes directly and reads through a helper", method: "TagService.Sync", want: verbRead,
			src: `func (s *TagService) Sync(ctx context.Context, id string) error {
				if err := s.client.do(ctx, http.MethodDelete, "/tags/"+id, nil, nil); err != nil { return err }
				return s.peek(ctx)
			}
			func (s *TagService) peek(ctx context.Context) error {
				return s.client.doList(ctx, http.MethodGet, "/tags", nil, nil)
			}`,
		},
		{
			// A verb-shaped string that is not the verb. Matching literals
			// anywhere in the body classified this as a write and dropped it out
			// of the probe requirement.
			name: "a path segment spelled DELETE", method: "TagService.ListDeleted", want: verbRead,
			src: `func (s *TagService) ListDeleted(ctx context.Context) error {
				return s.client.doList(ctx, http.MethodGet, "/tags/DELETE", nil, nil)
			}`,
		},
		{
			// The same error in the other direction: a stray "GET" turning a
			// write into a read.
			name: "a header value spelled GET", method: "TagService.Purge", want: verbWrite,
			src: `func (s *TagService) Purge(ctx context.Context) error {
				h := map[string]string{"Allow": "GET"}
				return s.client.do(ctx, http.MethodDelete, "/tags", nil, h)
			}`,
		},
		{
			// The verb is the argument in the METHOD position. main's position,
			// argument 2, is this line's path.
			name: "a verb-shaped path argument", method: "TagService.ListPurges", want: verbRead,
			src: `func (s *TagService) ListPurges(ctx context.Context) error {
				return s.client.doData(ctx, http.MethodGet, "DELETE", nil, nil, nil)
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
			// A helper missing from transportCalls is followed into, and the
			// verb it was handed reaches doRaw as an identifier: unknown, which
			// fails closed, never a guess.
			name: "a read through a helper the table does not know", method: "TagService.Bare", want: verbUnknown,
			src: `func (s *TagService) Bare(ctx context.Context) error { return s.client.doBare(ctx, http.MethodGet, "/tags") }
			func (c *Client) doBare(ctx context.Context, method, path string) error {
				_, _, err := c.doRaw(ctx, method, path, nil, nil)
				return err
			}`,
		},
		{
			// The request constructors are net/http's. Another package's
			// NewRequestWithContext builds nothing this guard can vouch for.
			name: "a NewRequest from another package", method: "Client.peek", want: verbUnknown,
			src: `func (c *Client) peek(ctx context.Context) error {
				req, err := fake.NewRequestWithContext(ctx, http.MethodGet, "/tags", nil)
				_ = req
				return err
			}`,
		},
		{
			// And a verb is net/http's constant or a literal: another package's
			// MethodGet could hold anything.
			name: "a verb constant from another package", method: "TagService.Odd", want: verbUnknown,
			src: `func (s *TagService) Odd(ctx context.Context) error {
				return s.client.doList(ctx, verbs.MethodGet, "/tags", nil, nil)
			}`,
		},
		{
			// A free function sharing a helper's name is not the client's.
			name: "a free function named doList", method: "TagService.Shadow", want: verbUnknown,
			src: `func (s *TagService) Shadow(ctx context.Context) error {
				return doList(ctx, http.MethodGet, "/tags")
			}`,
		},
		{
			// A transport call inside a literal nobody invokes is not this
			// method's request.
			name: "a dead nested closure", method: "TagService.Prepare", want: verbUnknown,
			src: `func (s *TagService) Prepare(ctx context.Context) error {
				_ = func() error { return s.client.doList(ctx, http.MethodGet, "/tags", nil, nil) }
				return nil
			}`,
		},
		{
			// A method value is a reference, not a call this parser follows.
			name: "a transport helper called through a method value", method: "TagService.Peek", want: verbUnknown,
			src: `func (s *TagService) Peek(ctx context.Context) error {
				send := s.client.doList
				return send(ctx, http.MethodGet, "/tags", nil, nil)
			}`,
		},
		{
			// Parentheses are not a call shape. A write found first, plus a read
			// reached through a parenthesized helper, classified as a write and
			// dropped the method out of the probe requirement.
			name: "a parenthesized helper call", method: "TagService.Rotate", want: verbRead,
			src: `func (s *TagService) Rotate(ctx context.Context, id string) error {
				if err := s.client.do(ctx, http.MethodDelete, "/tags/"+id, nil, nil); err != nil { return err }
				return (s).peek(ctx)
			}
			func (s *TagService) peek(ctx context.Context) error {
				return s.client.doList(ctx, http.MethodGet, "/tags", nil, nil)
			}`,
		},
		{
			// The other place parentheses can sit: around the whole callee rather
			// than around the receiver.
			name: "a parenthesized callee", method: "TagService.Refresh", want: verbRead,
			src: `func (s *TagService) Refresh(ctx context.Context, id string) error {
				if err := s.client.do(ctx, http.MethodDelete, "/tags/"+id, nil, nil); err != nil { return err }
				return (s.sniff)(ctx)
			}
			func (s *TagService) sniff(ctx context.Context) error {
				return s.client.doList(ctx, http.MethodGet, "/tags", nil, nil)
			}`,
		},
		{
			name: "a parenthesized transport call", method: "TagService.ListParen", want: verbRead,
			src: `func (s *TagService) ListParen(ctx context.Context) error {
				return (s.client.doList)(ctx, http.MethodGet, "/tags", nil, nil)
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
				return s.client.doList(ctx, http.MethodGet, "/tags", nil, nil)
			}`,
		},
		{
			// s.cache is not s.client. Resolving any nested selector as a Client
			// method substituted an unrelated body's verb for this one's.
			name: "a lookalike field is not the client", method: "TagService.Warm", want: verbUnknown,
			src: `func (s *TagService) Warm(ctx context.Context) error { return s.cache.lookup(ctx) }
			func (c *Client) lookup(ctx context.Context) error {
				return c.doList(ctx, http.MethodGet, "/tags", nil, nil)
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

func TestProbesInReadsOnlyTheTableThatRuns(t *testing.T) {
	const header = `package octonomy_test

type readProbe struct {
	name string
	find func(c *octonomy.Client) (bool, error)
}
`
	parse := func(t *testing.T, src string) ([]probe, []string) {
		t.Helper()
		return probesIn(parseFixture(t, header+src)["fixture.go"], "fixture.go")
	}

	t.Run("a probe is bound to the call its closure makes", func(t *testing.T) {
		probes, problems := parse(t, `
func readProbes(h harness) []readProbe {
	return []readProbe{
		{name: "Tags.Get", filtered: filteredNotFound, find: func(c *octonomy.Client) (bool, error) { return c.Tags.Get(ctx, id) }},
	}
}`)
		if len(problems) != 0 || len(probes) != 1 || probes[0].name != "Tags.Get" || !probes[0].calls["Tags.Get"] {
			t.Fatalf("got %+v %v, want one probe Tags.Get calling Tags.Get", probes, problems)
		}
	})

	t.Run("a call on something other than the client is not coverage", func(t *testing.T) {
		probes, _ := parse(t, `
func readProbes(h harness) []readProbe {
	return []readProbe{
		{name: "Tags.List", filtered: filteredEmpty, find: func(c *octonomy.Client) (bool, error) {
			_, _ = fake.Tags.List(ctx)
			return c.Tags.Get(ctx, id)
		}},
	}
}`)
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

	for _, tc := range []struct{ name, body string }{
		{"a shadowed client is not the client", `
			if cond {
				c := other
				return c.Tags.Get(ctx, id)
			}
			return c.Tags.Get(ctx, id)`},
		{"a reassigned client is not the client", `
			c = other
			return c.Tags.Get(ctx, id)`},
		{"a parenthesized reassignment is still a reassignment", `
			(c) = other
			return c.Tags.Get(ctx, id)`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probes, _ := parse(t, `
func readProbes(h harness) []readProbe {
	return []readProbe{
		{name: "Tags.Get", filtered: filteredNotFound, find: func(c *octonomy.Client) (bool, error) {`+tc.body+`
		}},
	}
}`)
			if len(probes) != 1 {
				t.Fatalf("got %d probes, want 1", len(probes))
			}
			// The name still binds the parameter; the value may no longer, and a
			// call on it delivers none of the coverage the probe claims.
			if len(probes[0].calls) != 0 {
				t.Errorf("calls were credited to a rebound identifier: %v", sortedKeys(probes[0].calls))
			}
		})
	}

	t.Run("a call in an uninvoked closure is not coverage", func(t *testing.T) {
		probes, _ := parse(t, `
func readProbes(h harness) []readProbe {
	return []readProbe{
		{name: "Tags.Get", filtered: filteredNotFound, find: func(c *octonomy.Client) (bool, error) {
			_ = func() { c.Tags.Get(ctx, id) }
			return c.Tags.List(ctx)
		}},
	}
}`)
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

	for _, tc := range []struct{ name, src, want string }{
		{"a second, unreachable table is refused", `
func readProbes(h harness) []readProbe {
	if false {
		return []readProbe{{name: "Widgets.Get", find: func(c *octonomy.Client) (bool, error) { return c.Widgets.Get(ctx) }}}
	}
	return []readProbe{
		{name: "Tags.Get", filtered: filteredNotFound, find: func(c *octonomy.Client) (bool, error) { return c.Tags.Get(ctx, id) }},
	}
}`, "exactly one return"},
		{"a table built by a helper is refused", `
func readProbes(h harness) []readProbe { return build(h) }`, "does not return a []readProbe composite literal"},
		{"a table of another type is refused", `
func readProbes(h harness) []readProbe { return []otherProbe{{name: "Tags.Get"}} }`, "does not return a []readProbe composite literal"},
		{"an entry with no literal name is refused", `
func readProbes(h harness) []readProbe {
	return []readProbe{{name: probeName, filtered: filteredNotFound, find: func(c *octonomy.Client) (bool, error) { return c.Tags.Get(ctx, id) }}}
}`, "has no literal name"},
		{"no table at all is refused", `
func notReadProbes(h harness) []readProbe { return nil }`, "declares no readProbes function"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probes, problems := parse(t, tc.src)
			if len(probes) != 0 {
				t.Errorf("an unreadable table yielded probes: %+v", probes)
			}
			if len(problems) != 1 || !strings.Contains(problems[0], tc.want) {
				t.Errorf("problems = %v, want one containing %q", problems, tc.want)
			}
		})
	}
}

// A service the Client struct reaches through a parenthesized field type is
// still part of the read surface. Missing one drops every method on it out of
// the guard, and the floor would not notice: it only catches a count that
// falls, not one that fails to rise.
func TestClientServiceFieldsSeesAParenthesizedField(t *testing.T) {
	fields := clientServiceFields(parseFixture(t, `package octonomy

type Client struct {
	Tags    *TagService
	Widgets (*WidgetService)
}`))
	if got := fields["WidgetService"]; got != "Widgets" {
		t.Errorf("clientServiceFields missed the parenthesized field: got %q, want \"Widgets\" (all: %v)",
			got, fields)
	}
	if got := fields["TagService"]; got != "Tags" {
		t.Errorf("clientServiceFields lost the ordinary field: got %q", got)
	}
}

// isolationHarnessFixture is a harness file whose loadHarness has the gate's
// required shape unless gate replaces it, with the client methods the suite
// hands its *testing.T to, and extra declarations after them.
func isolationHarnessFixture(gate, extra string) string {
	if gate == "" {
		gate = `if baseURL == "" {
		if required {
			t.Fatal("required")
		}
		t.Skip("no harness")
	}`
	}
	return `package octonomy_test

func loadHarness(t *testing.T) harness {
	required := os.Getenv("OCTONOMY_SMOKE_REQUIRED") == "1"
	baseURL := os.Getenv("OCTONOMY_TEST_BASE_URL")
	` + gate + `
	return harness{}
}

func (h harness) wildcard(t *testing.T) *octonomy.Client { return h.client(t, "wildcard") }
func (h harness) merchantClient(t *testing.T, m merchant) *octonomy.Client { return h.client(t, m.token) }
func (h harness) client(t *testing.T, token string) *octonomy.Client {
	c, err := octonomy.New(octonomy.Config{Token: token})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}
` + extra
}

// isolationSuiteFixture is a suite whose runProbeMatrix and two isolation
// tests have the required shape, with each part replaceable.
type isolationSuiteFixture struct {
	matrix, isolation, includeGlobal, extra string
}

func (f isolationSuiteFixture) source() string {
	if f.matrix == "" {
		f.matrix = `func runProbeMatrix(ctx context.Context, t *testing.T, h harness, runs []probeRun) {
	for _, probe := range readProbes(h) {
		t.Run(probe.name, func(t *testing.T) {
			for _, run := range runs {
				_, _ = probe.find(ctx, run.client, run.readNS, run.want, run.extra...)
			}
		})
	}
}`
	}
	if f.isolation == "" {
		f.isolation = `func TestIntegration_NamespaceIsolation(t *testing.T) {
	h := loadHarness(t)
	wildcard := h.wildcard(t)
	clientA := h.merchantClient(t, h.merchantA)
	runProbeMatrix(ctx, t, h, []probeRun{
		{name: "sees", client: clientA, expect: outcomeVisible},
		{name: "filtered", client: clientA, expect: outcomeFiltered},
		{name: "wildcard filtered", client: wildcard, expect: outcomeFiltered},
		{name: "refused", client: clientA, expect: outcomeForbidden},
	})
}`
	}
	if f.includeGlobal == "" {
		f.includeGlobal = `func TestIntegration_IncludeGlobalFailsClosed(t *testing.T) {
	h := loadHarness(t)
	clientA := h.merchantClient(t, h.merchantA)
	includeGlobal := []octonomy.RequestOption{octonomy.WithIncludeGlobal()}
	runProbeMatrix(ctx, t, h, []probeRun{
		{name: "fails closed", client: clientA, extra: includeGlobal, expect: outcomeFiltered},
		{name: "own rows", client: clientA, extra: includeGlobal, expect: outcomeVisible},
	})
}`
	}
	return "package octonomy_test\n\n" + f.matrix + "\n\n" + f.isolation + "\n\n" + f.includeGlobal + "\n\n" + f.extra + "\n"
}

// TestTheIsolationSuiteRunsItsProbes, driven over fixtures: the suite that runs
// its table under exact grants passes, and each way of making it ceremonial --
// or of making it assert nothing about authorization -- fails.
func TestIsolationSuiteProblemsRefusesACeremonialSuite(t *testing.T) {
	for _, tc := range []struct {
		name  string
		suite isolationSuiteFixture
		gate  string
		extra string   // appended to the harness file
		want  []string // a substring of each problem expected, in any order; none for a clean suite
	}{
		{name: "the suite as it is meant to be"},
		{
			name: "an ungated skip in loadHarness",
			gate: `if baseURL == "" {
		t.Skip("no harness")
	}`,
			want: []string{"loadHarness's skip must be the statement straight after `if required"},
		},
		{
			name: "a renamed isolation test",
			suite: isolationSuiteFixture{isolation: `func TestNamespaceIsolation(t *testing.T) {
	h := loadHarness(t)
	clientA := h.merchantClient(t, h.merchantA)
	runProbeMatrix(ctx, t, h, []probeRun{{client: clientA, expect: outcomeForbidden}})
}`},
			want: []string{
				"TestNamespaceIsolation runs the probe matrix but is not a TestIntegration_ function",
				"declares no TestIntegration_NamespaceIsolation",
			},
		},
		{
			name: "an isolation test that skips",
			suite: isolationSuiteFixture{extra: `func TestIntegration_Extra(t *testing.T) {
	t.Skip("later")
}`},
			want: []string{"TestIntegration_Extra calls Skip"},
		},
		{
			name:  "a harness method that skips",
			extra: "func (h harness) settle(t *testing.T) { t.Skip(\"not yet\") }\n",
			suite: isolationSuiteFixture{extra: `func TestIntegration_Extra(t *testing.T) {
	h := loadHarness(t)
	h.settle(t)
}`},
			want: []string{"TestIntegration_Extra calls h.settle, which can skip the test"},
		},
		{
			name:  "a method declared on two receivers is not read",
			extra: "func (m merchant) wildcard(t *testing.T) {}\n",
			want: []string{
				"TestIntegration_NamespaceIsolation hands its *testing.T to h.wildcard, which the guard cannot read",
			},
		},
		{
			name: "an isolation test that returns early",
			suite: isolationSuiteFixture{extra: `func TestIntegration_Extra(t *testing.T) {
	if flaky {
		return
	}
}`},
			want: []string{"TestIntegration_Extra returns early"},
		},
		{
			name: "a matrix over a copy of the table",
			suite: isolationSuiteFixture{matrix: `func runProbeMatrix(ctx context.Context, t *testing.T, h harness, runs []probeRun) {
	for _, probe := range otherProbes(h) {
		t.Run(probe.name, func(t *testing.T) {
			for _, run := range runs {
				_, _ = probe.find(ctx, run.client, run.readNS, run.want, run.extra...)
			}
		})
	}
}`},
			want: []string{
				"runProbeMatrix does not range over readProbes(h)",
			},
		},
		{
			name: "a matrix over another harness's table",
			suite: isolationSuiteFixture{matrix: `func runProbeMatrix(ctx context.Context, t *testing.T, h harness, runs []probeRun) {
	for _, probe := range readProbes(harness{}) {
		t.Run(probe.name, func(t *testing.T) {
			for _, run := range runs {
				_, _ = probe.find(ctx, run.client, run.readNS, run.want, run.extra...)
			}
		})
	}
}`},
			want: []string{
				"runProbeMatrix does not range over readProbes(h)",
			},
		},
		{
			name: "a matrix that never calls find",
			suite: isolationSuiteFixture{matrix: `func runProbeMatrix(ctx context.Context, t *testing.T, h harness, runs []probeRun) {
	for _, probe := range readProbes(h) {
		t.Run(probe.name, func(t *testing.T) {
			for _, run := range runs {
				t.Log(probe.name, run.name)
			}
		})
	}
}`},
			want: []string{
				"runProbeMatrix does not call probe.find",
			},
		},
		{
			name: "a matrix that asks a different run",
			suite: isolationSuiteFixture{matrix: `func runProbeMatrix(ctx context.Context, t *testing.T, h harness, runs []probeRun) {
	for _, probe := range readProbes(h) {
		t.Run(probe.name, func(t *testing.T) {
			for _, run := range runs {
				_, _ = probe.find(ctx, runs[0].client, run.readNS, run.want, run.extra...)
			}
		})
	}
}`},
			want: []string{
				"runProbeMatrix does not call probe.find",
			},
		},
		{
			name: "a matrix that drops a run's options",
			suite: isolationSuiteFixture{matrix: `func runProbeMatrix(ctx context.Context, t *testing.T, h harness, runs []probeRun) {
	for _, probe := range readProbes(h) {
		t.Run(probe.name, func(t *testing.T) {
			for _, run := range runs {
				_, _ = probe.find(ctx, run.client, run.readNS, run.want)
			}
		})
	}
}`},
			want: []string{
				"runProbeMatrix does not call probe.find",
			},
		},
		{
			name: "a matrix that asks only the first run",
			suite: isolationSuiteFixture{matrix: `func runProbeMatrix(ctx context.Context, t *testing.T, h harness, runs []probeRun) {
	for _, probe := range readProbes(h) {
		t.Run(probe.name, func(t *testing.T) {
			for _, run := range runs[:1] {
				_, _ = probe.find(ctx, run.client, run.readNS, run.want, run.extra...)
			}
		})
	}
}`},
			want: []string{
				"runProbeMatrix does not range over all of runs",
			},
		},
		{
			name: "a matrix that shortens its runs",
			suite: isolationSuiteFixture{matrix: `func runProbeMatrix(ctx context.Context, t *testing.T, h harness, runs []probeRun) {
	runs = runs[:1]
	for _, probe := range readProbes(h) {
		t.Run(probe.name, func(t *testing.T) {
			for _, run := range runs {
				_, _ = probe.find(ctx, run.client, run.readNS, run.want, run.extra...)
			}
		})
	}
}`},
			want: []string{
				"runProbeMatrix binds its parameter runs again",
			},
		},
		{
			name: "a matrix that stops after the first run",
			suite: isolationSuiteFixture{matrix: `func runProbeMatrix(ctx context.Context, t *testing.T, h harness, runs []probeRun) {
	for _, probe := range readProbes(h) {
		t.Run(probe.name, func(t *testing.T) {
			for _, run := range runs {
				_, _ = probe.find(ctx, run.client, run.readNS, run.want, run.extra...)
				break
			}
		})
	}
}`},
			want: []string{
				"runProbeMatrix uses break",
			},
		},
		{
			name: "a matrix that leaves a probe out",
			suite: isolationSuiteFixture{matrix: `func runProbeMatrix(ctx context.Context, t *testing.T, h harness, runs []probeRun) {
	for _, probe := range readProbes(h) {
		if probe.name == "Tags.Resolve" {
			continue
		}
		t.Run(probe.name, func(t *testing.T) {
			for _, run := range runs {
				_, _ = probe.find(ctx, run.client, run.readNS, run.want, run.extra...)
			}
		})
	}
}`},
			want: []string{
				"runProbeMatrix uses continue",
			},
		},
		{
			name: "a matrix that can stop before it starts",
			suite: isolationSuiteFixture{matrix: `func runProbeMatrix(ctx context.Context, t *testing.T, h harness, runs []probeRun) {
	if len(runs) == 0 {
		return
	}
	for _, probe := range readProbes(h) {
		t.Run(probe.name, func(t *testing.T) {
			for _, run := range runs {
				_, _ = probe.find(ctx, run.client, run.readNS, run.want, run.extra...)
			}
		})
	}
}`},
			want: []string{
				"runProbeMatrix returns",
			},
		},
		{
			name: "a subtest that returns before asking",
			suite: isolationSuiteFixture{matrix: `func runProbeMatrix(ctx context.Context, t *testing.T, h harness, runs []probeRun) {
	for _, probe := range readProbes(h) {
		t.Run(probe.name, func(t *testing.T) {
			if probe.name == "Tags.Resolve" {
				return
			}
			for _, run := range runs {
				_, _ = probe.find(ctx, run.client, run.readNS, run.want, run.extra...)
			}
		})
	}
}`},
			want: []string{
				"runProbeMatrix returns",
			},
		},
		{
			name: "a matrix that rebinds its run",
			suite: isolationSuiteFixture{matrix: `func runProbeMatrix(ctx context.Context, t *testing.T, h harness, runs []probeRun) {
	for _, probe := range readProbes(h) {
		t.Run(probe.name, func(t *testing.T) {
			for _, run := range runs {
				run := runs[0]
				_, _ = probe.find(ctx, run.client, run.readNS, run.want, run.extra...)
			}
		})
	}
}`},
			want: []string{
				"runProbeMatrix binds run 2 times",
			},
		},
		{
			name: "a matrix whose subtests skip",
			suite: isolationSuiteFixture{matrix: `func runProbeMatrix(ctx context.Context, t *testing.T, h harness, runs []probeRun) {
	for _, probe := range readProbes(h) {
		t.Run(probe.name, func(t *testing.T) {
			t.Skip("flaky")
			for _, run := range runs {
				_, _ = probe.find(ctx, run.client, run.readNS, run.want, run.extra...)
			}
		})
	}
}`},
			want: []string{
				"runProbeMatrix calls Skip",
				"TestIntegration_IncludeGlobalFailsClosed calls runProbeMatrix, which can skip the test",
				"TestIntegration_NamespaceIsolation calls runProbeMatrix, which can skip the test",
			},
		},
		{
			name: "the matrix run from a closure",
			suite: isolationSuiteFixture{includeGlobal: `func TestIntegration_IncludeGlobalFailsClosed(t *testing.T) {
	h := loadHarness(t)
	clientA := h.merchantClient(t, h.merchantA)
	includeGlobal := []octonomy.RequestOption{octonomy.WithIncludeGlobal()}
	runs := func() {
		runProbeMatrix(ctx, t, h, []probeRun{{client: clientA, extra: includeGlobal, expect: outcomeVisible}})
	}
	_ = runs
	runProbeMatrix(ctx, t, h, []probeRun{
		{client: clientA, extra: includeGlobal, expect: outcomeFiltered},
		{client: clientA, extra: includeGlobal, expect: outcomeVisible},
	})
}`},
			want: []string{"runs the probe matrix from inside a function literal"},
		},
		{
			name: "the refusal run on the wildcard",
			suite: isolationSuiteFixture{isolation: `func TestIntegration_NamespaceIsolation(t *testing.T) {
	h := loadHarness(t)
	wildcard := h.wildcard(t)
	clientA := h.merchantClient(t, h.merchantA)
	runProbeMatrix(ctx, t, h, []probeRun{
		{client: clientA, expect: outcomeVisible},
		{client: clientA, expect: outcomeFiltered},
		{client: wildcard, expect: outcomeForbidden},
	})
}`},
			want: []string{"TestIntegration_NamespaceIsolation makes no outcomeForbidden run under an exact merchant grant"},
		},
		{
			name: "the fail-closed run without include_global",
			suite: isolationSuiteFixture{includeGlobal: `func TestIntegration_IncludeGlobalFailsClosed(t *testing.T) {
	h := loadHarness(t)
	clientA := h.merchantClient(t, h.merchantA)
	includeGlobal := []octonomy.RequestOption{octonomy.WithIncludeGlobal()}
	runProbeMatrix(ctx, t, h, []probeRun{
		{client: clientA, expect: outcomeFiltered},
		{client: clientA, extra: includeGlobal, expect: outcomeVisible},
	})
}`},
			want: []string{"makes no outcomeFiltered run under an exact merchant grant carrying WithIncludeGlobal"},
		},
		{
			name: "an exact-grant client rebound to the wildcard",
			suite: isolationSuiteFixture{isolation: `func TestIntegration_NamespaceIsolation(t *testing.T) {
	h := loadHarness(t)
	clientA := h.merchantClient(t, h.merchantA)
	clientA = h.wildcard(t)
	runProbeMatrix(ctx, t, h, []probeRun{
		{client: clientA, expect: outcomeVisible},
		{client: clientA, expect: outcomeFiltered},
		{client: clientA, expect: outcomeForbidden},
	})
}`},
			want: []string{
				"makes no outcomeVisible run under an exact merchant grant",
				"makes no outcomeFiltered run under an exact merchant grant",
				"makes no outcomeForbidden run under an exact merchant grant",
			},
		},
		{
			name: "runs the guard cannot read",
			suite: isolationSuiteFixture{isolation: `func TestIntegration_NamespaceIsolation(t *testing.T) {
	h := loadHarness(t)
	runProbeMatrix(ctx, t, h, isolationRuns(h))
}`},
			want: []string{
				"hands runProbeMatrix runs that are not a []probeRun literal",
				"makes no outcomeVisible run",
				"makes no outcomeFiltered run",
				"makes no outcomeForbidden run",
			},
		},
		{
			name: "a required isolation test that is gone",
			suite: isolationSuiteFixture{includeGlobal: `func TestIntegration_SomethingElse(t *testing.T) {
	h := loadHarness(t)
	_ = h
}`},
			want: []string{"declares no TestIntegration_IncludeGlobalFailsClosed"},
		},
		{
			name: "a required isolation test that does not run the matrix",
			suite: isolationSuiteFixture{includeGlobal: `func TestIntegration_IncludeGlobalFailsClosed(t *testing.T) {
	h := loadHarness(t)
	_ = h
}`},
			want: []string{"TestIntegration_IncludeGlobalFailsClosed does not run the probe matrix"},
		},
		{
			name: "a required isolation test with no gate",
			suite: isolationSuiteFixture{includeGlobal: `func TestIntegration_IncludeGlobalFailsClosed(t *testing.T) {
	h := harness{}
	clientA := h.merchantClient(t, h.merchantA)
	includeGlobal := []octonomy.RequestOption{octonomy.WithIncludeGlobal()}
	runProbeMatrix(ctx, t, h, []probeRun{
		{client: clientA, extra: includeGlobal, expect: outcomeFiltered},
		{client: clientA, extra: includeGlobal, expect: outcomeVisible},
	})
}`},
			want: []string{"TestIntegration_IncludeGlobalFailsClosed does not call loadHarness"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			suite := parseFixture(t, tc.suite.source())["fixture.go"]
			harnessFile := parseFixture(t, isolationHarnessFixture(tc.gate, tc.extra))["fixture.go"]
			problems := isolationSuiteProblems(suite, harnessFile)
			if len(problems) != len(tc.want) {
				t.Fatalf("problems = %v, want exactly %d, containing %q", problems, len(tc.want), tc.want)
			}
			for _, want := range tc.want {
				found := false
				for _, p := range problems {
					found = found || strings.Contains(p, want)
				}
				if !found {
					t.Errorf("no problem contains %q: %v", want, problems)
				}
			}
		})
	}
}
