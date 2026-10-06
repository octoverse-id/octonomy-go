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
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
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

// readProbeExclusions names every method of the inventoried surface -- an
// exported method on Client or on a service a Client field holds -- that
// deliberately carries no probe, with the reason it is not a gap: the two
// unauthenticated health probes, and Client.APIVersion, an accessor.
//
// A name listed here must still name an existing method of that surface, and
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
// an exported Client field of any other type, and an exported field on a
// service. And whatever way in those miss, the last check closes by what a
// method does: an exported method or function anywhere in the package that
// issues a read, or reaches the transport with a verb the classifier cannot
// resolve, must be on Client or a wired service.
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
//  1. a read method of the surface -- Client's exported methods and those of
//     each service a Client field holds -- with no probe and no exclusion;
//  2. a method of the surface whose verb it cannot resolve, unless probed or
//     excluded;
//  3. a probe naming a method that is not a read of the surface;
//  4. a probe whose find closure calls no client method, or not the one its name
//     claims;
//  5. an exclusion that names no method of the surface, has a blank reason,
//     names a write, or names a probed method;
//  6. an exported read -- method or package function -- anywhere else in the
//     package, which a caller could reach by some way the surface does not
//     account for;
//
// and a duplicate probe name, a table it cannot read at all, and a shape of
// Client or a service the surface cannot be read off (surfaceShapeProblems).
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

	// And the inventory is closed by what a method DOES rather than by where
	// it sits. Shapes that hand a caller a method are many -- an embedded field
	// in another exported type, a value-held service, an interface a type
	// satisfies -- and a rule per shape was found short three rounds running.
	// So every exported method or function in the package that issues a read
	// must be on Client or on a service a Client field holds, where the checks
	// above see it; one anywhere else is a read the isolation suite never asks
	// about.
	inventoried := map[string]bool{"Client": true}
	for service := range fields {
		inventoried[service] = true
	}
	src, why := checkSource(pkg, index)
	if why != "" {
		problems = append(problems, why)
	}
	for _, key := range funcIndexKeys(index) {
		decl := index[key]
		recv := receiverTypeName(decl)
		if !ast.IsExported(decl.Name.Name) || inventoried[recv] || (decl.Recv != nil && recv == "") {
			continue
		}
		// A package function is off the surface by definition: no Client field
		// leads to it, yet a caller names it directly.
		where := recv + " is neither Client nor a service a Client field holds"
		if decl.Recv == nil {
			where = key + " is a package function, on no service a Client field holds"
		}
		switch classify(decl, index) {
		case verbRead:
			problems = append(problems, key+" issues a read, and "+where+", so the guard inventories no probe "+
				"for it -- yet an exported method or function is one a caller can reach, directly, through an "+
				"embedded field, another exported type or an interface. Declare it on a wired service, or teach "+
				"the guard the way in")
		case verbUnknown:
			// Unknown is fail-closed on the surface (check 2) and must be off it
			// too, or a read sent through a method value or a closure would pass
			// here as "sends nothing". Most exported methods off the surface --
			// Error, String, MarshalJSON -- really do send nothing, so what is
			// refused is the unknown method that reaches the transport.
			if reachesTransport(decl, src, map[*ast.FuncDecl]bool{}) {
				problems = append(problems, key+" reaches the transport and the guard could not resolve its "+
					"verb, and "+where+", so an unresolved read there would be inventoried by nothing. Declare "+
					"it on a wired service, or fix the classifier so it can see the verb")
			}
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
				"a read method on Client or a service a Client field holds. A renamed or removed method leaves a probe "+
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
				"exported method on Client or a service a Client field holds. Drop the entry -- an exclusion for a method "+
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
// package's service; and an exported field on a service, reachable as
// client.Tags.Search. Those keep the inventory itself honest; whatever way in
// they miss, readProbeProblems' last check catches by what a method DOES.
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
	return problems
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

// typedSource is the package's own source, type-checked: which declaration a
// selector or a name reaches, exactly as the compiler resolves it -- through
// embedding at Go's own depth rules, aliases, defined types and inferred
// locals. A hand-written resolver was found short of the language four rounds
// running (#97); go/types is the language.
//
// Only the package itself is checked. Every import is an empty package
// (emptyImporter), so nothing outside the module is read or required, and a
// name from an import is an error the checker is told to ignore: what such a
// name selects is unresolved, and each use below says what it does then.
type typedSource struct {
	info   *types.Info
	declOf map[*types.Func]*ast.FuncDecl
	index  funcIndex
}

// checkSource type-checks the files of one package.
func checkSource(files map[string]*ast.File, index funcIndex) (typedSource, string) {
	var list []*ast.File
	for _, name := range sortedFileNames(files) {
		list = append(list, files[name])
	}
	info, why := checkFiles(list)
	declOf := map[*types.Func]*ast.FuncDecl{}
	for _, decl := range index {
		if f, ok := info.Defs[decl.Name].(*types.Func); ok {
			declOf[f] = decl
		}
	}
	return typedSource{info: info, declOf: declOf, index: index}, why
}

// checkFiles runs go/types over files with every import empty, and returns
// what it recorded. The type errors are expected -- every imported name is one
// -- and are dropped. The files must share the FileSet that parsed them
// (parsedWith); files from two sets cannot be checked together, and that is
// reported rather than checked wrongly.
func checkFiles(files []*ast.File) (*types.Info, string) {
	info := &types.Info{
		Types:      map[ast.Expr]types.TypeAndValue{},
		Defs:       map[*ast.Ident]types.Object{},
		Uses:       map[*ast.Ident]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
	}
	if len(files) == 0 {
		return info, ""
	}
	var fset *token.FileSet
	for _, file := range files {
		set, ok := parsedWith.Load(file)
		if !ok || (fset != nil && set.(*token.FileSet) != fset) {
			return info, "the guard was handed files it did not parse into one FileSet, so it cannot type-check them; " +
				"parse them with parseFilesOrFatal"
		}
		fset = set.(*token.FileSet)
	}
	conf := types.Config{Importer: emptyImporter{}, Error: func(error) {}}
	_, _ = conf.Check(files[0].Name.Name, fset, files, info)
	return info, ""
}

// emptyImporter imports every path as a complete package with nothing in it.
type emptyImporter struct{}

func (emptyImporter) Import(path string) (*types.Package, error) {
	name := path[strings.LastIndex(path, "/")+1:]
	pkg := types.NewPackage(path, strings.NewReplacer("-", "_", ".", "_").Replace(name))
	pkg.MarkComplete()
	return pkg, nil
}

// reachesTransport reports whether fn's body -- closures included, and every
// declaration it selects or names, called or taken as a value, followed --
// reaches the transport: a client transport helper, or net/http's request
// constructors.
func reachesTransport(fn *ast.FuncDecl, src typedSource, seen map[*ast.FuncDecl]bool) bool {
	if fn == nil || fn.Body == nil || seen[fn] {
		return false
	}
	seen[fn] = true
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		switch e := n.(type) {
		case *ast.SelectorExpr:
			found = src.selectorReaches(e, seen)
		case *ast.Ident:
			// A package function, called or taken as a value.
			if f, ok := src.info.Uses[e].(*types.Func); ok {
				if sig, ok := f.Type().(*types.Signature); ok && sig.Recv() == nil {
					found = reachesTransport(src.declOf[f], src, seen)
				}
			}
		}
		return !found
	})
	return found
}

// selectorReaches is reachesTransport for one selector.
//
//   - A method the checker resolved is the method Go would call. One of the
//     client's transport helpers is the transport; any other declared here is
//     followed. A transport helper's name on a method declared nowhere in the
//     package -- an interface's -- is taken at its word.
//   - A name off an imported package is the transport only if it is one of
//     net/http's request constructors.
//   - A selector the checker could not resolve -- its receiver's type comes
//     from an import, or does not check -- is taken at its word: a transport
//     helper's name counts, and a Client method of that name is followed. That
//     is the fail-closed direction, and a receiver typed in this package never
//     lands here.
func (src typedSource) selectorReaches(e *ast.SelectorExpr, seen map[*ast.FuncDecl]bool) bool {
	name := e.Sel.Name
	transport, isTransport := transportCalls[name]
	clientHelper := isTransport && transport.shape == "client"
	if sel, ok := src.info.Selections[e]; ok {
		f, ok := sel.Obj().(*types.Func)
		if !ok {
			return false // a field; its own selections are read where they occur
		}
		decl, declared := src.declOf[f]
		if !declared {
			return clientHelper
		}
		if clientHelper && receiverTypeName(decl) == "Client" {
			return true
		}
		return reachesTransport(decl, src, seen)
	}
	if id, ok := unparen(e.X).(*ast.Ident); ok {
		if pkg, ok := src.info.Uses[id].(*types.PkgName); ok {
			return isTransport && transport.shape == "http" && pkg.Imported().Path() == "net/http"
		}
	}
	if clientHelper {
		return true
	}
	return reachesTransport(src.index["Client."+name], src, seen)
}

func funcIndexKeys(index funcIndex) []string {
	out := make([]string, 0, len(index))
	for key := range index {
		out = append(out, key)
	}
	return sortedStrings(out)
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
	return parseFilesOrFatal(t, map[string]string{path: ""})[path]
}

// parseFilesOrFatal parses files into ONE FileSet, so they can be type-checked
// together (checkFiles). A source of "" reads the file at the path.
func parseFilesOrFatal(t *testing.T, sources map[string]string) map[string]*ast.File {
	t.Helper()
	fset := token.NewFileSet()
	out := map[string]*ast.File{}
	for _, path := range sortedStringKeys(sources) {
		var src interface{}
		if sources[path] != "" {
			src = sources[path]
		}
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		parsedWith.Store(file, fset)
		out[path] = file
	}
	return out
}

func sortedStringKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return sortedStrings(out)
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

// isolationRun is what one run of the matrix MEANS, read off its probeRun
// literal: who reads, in which namespace, looking for which fixture, with which
// option, and what it is entitled to.
//
//	grant   "A" / "B" -- the exact grant h.merchantClient(t, h.merchantA / h.merchantB);
//	        "wildcard" -- h.wildcard(t)
//	readNS  "A" / "B" -- h.merchantA.id / h.merchantB.id
//	want    "A" / "B" / "global" -- the fixture h.seed(…, h.merchantA.id / h.merchantB.id / "")
//	option  "include_global" when extra carries WithIncludeGlobal, else ""
//	expect  the outcome constant
//
// Anything the reader cannot name reads as "?", which matches no requirement.
type isolationRun struct {
	grant, readNS, want, option, expect string
	why                                 string // in isolationTests only: what the run proves
}

func (r isolationRun) describe() string {
	who := map[string]string{"A": "the exact merchant-A grant", "B": "the exact merchant-B grant", "wildcard": "the wildcard grant"}
	where := map[string]string{"A": "merchant A's namespace", "B": "merchant B's namespace"}
	what := map[string]string{"A": "merchant A's fixture", "B": "merchant B's fixture", "global": "the global fixture"}
	name := func(m map[string]string, k string) string {
		if v, ok := m[k]; ok {
			return v
		}
		return "an unreadable " + k
	}
	out := name(who, r.grant) + " reading " + name(where, r.readNS) + " for " + name(what, r.want)
	if r.option != "" {
		out += " with WithIncludeGlobal"
	}
	return out + ", expecting " + r.expect
}

func (r isolationRun) key() string {
	return r.grant + "|" + r.readNS + "|" + r.want + "|" + r.option + "|" + r.expect
}

// isolationTests names the tests that run the probe table, and EVERY run each
// one must make -- not a sample. Each run asserts one mechanism, and the
// others pass against a server that has lost it: a suite missing any one row
// below stays green through exactly the regression that row exists for.
//
// WHICH TOKEN A RUN USES IS THE TEST. The wildcard grant matches every
// partition, global included, so under it authorization never refuses anything
// and include_global's opt-in always succeeds: it can demonstrate the server's
// namespace FILTER and nothing else. The refusal path (outcomeForbidden) and
// the fail-closed branch of include_global run only under an exact grant. AND
// WHICH ROW IT LOOKS FOR: the fail-closed run means something only when it asks
// for a GLOBAL row -- pointed at merchant B's fixture it passes whether or not
// include_global fails closed.
var isolationTests = map[string][]isolationRun{
	"TestIntegration_NamespaceIsolation": {
		{grant: "wildcard", readNS: "B", want: "B", expect: "outcomeVisible",
			why: "the wildcard's control: without a visible run, the filtered runs pass when the rows were never written"},
		{grant: "B", readNS: "B", want: "B", expect: "outcomeVisible",
			why: "merchant B's grant reaches its own rows, or merchant B's isolation is never shown to hold under a real grant"},
		{grant: "A", readNS: "A", want: "A", expect: "outcomeVisible",
			why: "merchant A's grant reaches its own rows, or a token that reached nothing would pass every negative"},
		{grant: "A", readNS: "A", want: "B", expect: "outcomeFiltered",
			why: "the namespace filter under an exact grant"},
		{grant: "wildcard", readNS: "A", want: "B", expect: "outcomeFiltered",
			why: "the namespace filter where authorization cannot be what hides the row"},
		{grant: "A", readNS: "B", want: "B", expect: "outcomeForbidden",
			why: "the refusal: the request an attacker makes, which no filtered run performs"},
	},
	"TestIntegration_IncludeGlobalFailsClosed": {
		{grant: "wildcard", readNS: "A", want: "global", expect: "outcomeFiltered",
			why: "a namespaced read excludes global rows by default, or the option means nothing"},
		{grant: "wildcard", readNS: "A", want: "global", option: "include_global", expect: "outcomeVisible",
			why: "the authorized opt-in works, or a route ignoring the option would pass the fail-closed run too"},
		{grant: "A", readNS: "A", want: "global", option: "include_global", expect: "outcomeFiltered",
			why: "the fail-closed assertion itself: an exact grant asking for global rows gets none"},
		{grant: "wildcard", readNS: "A", want: "B", option: "include_global", expect: "outcomeFiltered",
			why: "the option widens to global, never to every namespace"},
		{grant: "A", readNS: "A", want: "A", option: "include_global", expect: "outcomeVisible",
			why: "the opted-in read still works, or the negatives pass whenever it failed"},
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
// what makes it MEAN something: every run listed in isolationTests, read as
// its grant, namespace, fixture, option and outcome. The runners themselves
// are pinned (isolationRecipePin below, and the isolation step in
// smokeprobes_test.go's smokeJobPin).
//
// WHERE IT ENDS, as with the smoke guard: it reads presence, not reachability,
// and it proves which runs a test makes, not that their assertions are right.
// The assertions are runProbeMatrix's, and a reviewer's.
func TestTheIsolationSuiteRunsItsProbes(t *testing.T) {
	for _, path := range []string{integrationSuiteFile, integrationHarnessFile} {
		raw, err := ioutil.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, problem := range smokeBuildTagProblems(string(raw)) {
			t.Errorf("%s: %s", path, problem)
		}
	}
	parsed := parseFilesOrFatal(t, map[string]string{integrationSuiteFile: "", integrationHarnessFile: ""})
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
	info, why := checkFiles([]*ast.File{suite, harnessFile})
	if why != "" {
		problems = append(problems, why)
	}
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
		problems = append(problems, matrixProblems(matrix, helpers, info)...)
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
		problems = append(problems, isolationTestProblems(funcs[name], helpers, info)...)
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
		problems = append(problems, runRequirementProblems(fn, isolationTests[name])...)
	}
	return problems
}

// isolationTestProblems refuses the two ways a selected test can turn green
// without running what follows: a skip, and an early return. The suite's only
// skip is loadHarness's, behind the required gate.
func isolationTestProblems(fn *ast.FuncDecl, helpers smokeHelpers, info *types.Info) []string {
	var problems []string
	// The seeded rows' teardown is this test's own defer (rules 2 and 3 of the
	// t.Cleanup model), which holds only while its subtests are sequential; and
	// the Vocabularies.List walk counts a collection another test running at
	// the same time would change.
	if callsParallel(fn, helpers, info, map[*ast.FuncDecl]bool{}) {
		problems = append(problems, fn.Name.Name+" calls Parallel, directly or through a helper. The isolation "+
			"tests run in sequence: a parallel subtest outlives the deferred teardown of the rows it reads, and "+
			"two isolation tests at once change the collections the Vocabularies.List walk counts")
	}
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
func matrixProblems(fn *ast.FuncDecl, helpers smokeHelpers, info *types.Info) []string {
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
	if callsParallel(fn, helpers, info, map[*ast.FuncDecl]bool{}) {
		problems = append(problems, "runProbeMatrix calls Parallel, directly or through a helper. Its subtests "+
			"close over the loops' probe and run, which before Go 1.22 a parallel subtest reads at the loop's "+
			"last value -- copies of one probe or one run, not the matrix -- and the fixtures' teardown is the "+
			"root test's defer, which a parallel subtest outlives")
	}

	const shape = "; the matrix must be `for _, probe := range readProbes(h)` at the top of its body, " +
		"`for _, run := range runs` inside it, and `probe.find(ctx, run.client, run.readNS, run.want, " +
		"run.extra...)` inside that, so every probe asks every run"
	var hName, runsName string
	params, ptypes := paramNames(fn.Type.Params), paramTypes(fn.Type)
	for i := range params {
		switch ptypes[i] {
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

// callsParallel reports whether fn can call Parallel: directly, anywhere in its
// body, closures included, or through a function or method the files read
// declare, resolved as the skip reader resolves them. A helper the reader
// cannot resolve is already refused when it is handed the T (skipsIn).
func callsParallel(fn *ast.FuncDecl, h smokeHelpers, info *types.Info, seen map[*ast.FuncDecl]bool) bool {
	if fn == nil || fn.Body == nil || seen[fn] {
		return false
	}
	seen[fn] = true
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || found {
			return !found
		}
		switch callee := unparen(call.Fun).(type) {
		case *ast.SelectorExpr:
			if callee.Sel.Name == "Parallel" && mayBeTestingT(callee.X, info) {
				found = true
			} else if typ := receiverTypeOf(fn, callee.X, h); typ != "" {
				found = callsParallel(h.methods[typ+"."+callee.Sel.Name], h, info, seen)
			}
		case *ast.Ident:
			if len(declarationsOf(callee.Name, fn)) == 0 {
				found = callsParallel(h.declared[callee.Name], h, info, seen)
			}
		}
		return !found
	})
	return found
}

// mayBeTestingT reports whether a Parallel call's receiver may be a test's T,
// read off the files' own type-check (checkFiles): anything but a value whose
// type, under every alias and definition, is concrete and declared in those
// files -- `var p pool` with pool a struct. An interface may hold a
// *testing.T, and a type from an import -- testing.T itself, which the checker
// sees as an empty package's -- or one that does not check is taken to be one:
// the fail-closed direction.
func mayBeTestingT(x ast.Expr, info *types.Info) bool {
	typ := info.TypeOf(x)
	if typ == nil {
		return true
	}
	// Look through pointers. The toolchains differ here, and this line's real
	// one is the reason: go1.13.15's go/types types `*testing.T`, with testing
	// stubbed empty, as a pointer to an invalid type, where a modern go/types
	// makes the whole type invalid. Without this, a direct t.Parallel() passes
	// the guard on go1.13 alone -- which a modern-toolchain run cannot see.
	for {
		ptr, ok := typ.Underlying().(*types.Pointer)
		if !ok {
			break
		}
		typ = ptr.Elem()
	}
	switch under := typ.Underlying().(type) {
	case *types.Interface:
		return true
	case *types.Basic:
		return under.Kind() == types.Invalid
	}
	return false
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

// runRequirementProblems reads the []probeRun literals a test hands
// runProbeMatrix, names what each run means, and reports every required run
// none of them is.
func runRequirementProblems(fn *ast.FuncDecl, want []isolationRun) []string {
	have := map[string]bool{}
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
				"literal, so the guard cannot read what each run means")
			return true
		}
		for _, elt := range lit.Elts {
			if entry, ok := unparen(elt).(*ast.CompositeLit); ok {
				have[readIsolationRun(fn, entry).key()] = true
			}
		}
		return true
	})
	for _, req := range want {
		if !have[req.key()] {
			problems = append(problems, fn.Name.Name+" makes no run of "+req.describe()+" -- "+req.why+
				". Every run in isolationTests asserts a mechanism the others cannot, so a suite without it "+
				"stays green through exactly the regression it exists for")
		}
	}
	return problems
}

// readIsolationRun names what one probeRun literal means.
func readIsolationRun(fn *ast.FuncDecl, entry *ast.CompositeLit) isolationRun {
	r := isolationRun{grant: "?", readNS: "?", want: "?", expect: "?"}
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
			if call, method := boundCall(fn, kv.Value); method == "wildcard" {
				r.grant = "wildcard"
			} else if method == "merchantClient" && len(call.Args) == 2 {
				r.grant = merchantOf(call.Args[1])
			}
		case "readNS":
			r.readNS = merchantOf(kv.Value)
		case "want":
			if call, method := boundCall(fn, kv.Value); method == "seed" && len(call.Args) > 0 {
				r.want = merchantOf(call.Args[len(call.Args)-1])
			}
		case "extra":
			if carriesCall(fn, kv.Value, "WithIncludeGlobal") {
				r.option = "include_global"
			}
		case "expect":
			if ident, ok := unparen(kv.Value).(*ast.Ident); ok {
				r.expect = ident.Name
			}
		}
	}
	return r
}

// merchantOf names the namespace an expression denotes: "A" for h.merchantA or
// h.merchantA.id, "B" likewise, "global" for the literal "", and "?" otherwise.
func merchantOf(expr ast.Expr) string {
	switch e := unparen(expr).(type) {
	case *ast.BasicLit:
		if e.Kind == token.STRING && e.Value == `""` {
			return "global"
		}
	case *ast.SelectorExpr:
		switch e.Sel.Name {
		case "id":
			return merchantOf(e.X)
		case "merchantA":
			return "A"
		case "merchantB":
			return "B"
		}
	}
	return "?"
}

// boundCall returns the call an identifier is bound to -- exactly once in fn,
// by `x := recv.method(…)` -- and that method's name, or (nil, "").
func boundCall(fn *ast.FuncDecl, expr ast.Expr) (*ast.CallExpr, string) {
	ident, ok := unparen(expr).(*ast.Ident)
	if !ok {
		return nil, ""
	}
	bindings := bindingsOf(fn.Body, ident.Name)
	if len(bindings) != 1 {
		return nil, ""
	}
	assign, ok := bindings[0].(*ast.AssignStmt)
	if !ok || assign.Tok != token.DEFINE || len(assign.Lhs) != len(assign.Rhs) {
		return nil, ""
	}
	for i, lhs := range assign.Lhs {
		if !isIdent(lhs, ident.Name) {
			continue
		}
		call, ok := unparen(assign.Rhs[i]).(*ast.CallExpr)
		if !ok {
			return nil, ""
		}
		if sel, ok := unparen(call.Fun).(*ast.SelectorExpr); ok {
			return call, sel.Sel.Name
		}
	}
	return nil, ""
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

// auditService is a service with one read, wired to no Client field.
const auditService = `type AuditService struct{ client *Client }

func (s *AuditService) List(ctx context.Context) error {
	return s.client.doList(ctx, http.MethodGet, "/audit-logs", nil, nil)
}
`

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
		want       string   // a substring of the one problem expected; "" for none
		wants      []string // or of each of several, in order of the problems
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

// Promoted, and sends nothing: the shape alone is refused. A promoted READ
// is also refused by what it does, in the fixtures below.
func (r *tagReads) Peek() string { return "" }
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
			name:       "a read on a service another type embeds",
			pkg:        "\ntype AdminClient struct{ *AuditService }\n" + auditService,
			entries:    cleanProbeEntries,
			exclusions: cleanProbeExclusions,
			want:       "AuditService.List issues a read, and AuditService is neither Client nor a service",
		},
		{
			name:       "a read on a service another type holds by value",
			pkg:        "\ntype AdminClient struct{ Audit AuditService }\n" + auditService,
			entries:    cleanProbeEntries,
			exclusions: cleanProbeExclusions,
			want:       "AuditService.List issues a read, and AuditService is neither Client nor a service",
		},
		{
			name:       "a read on an unexported type, promoted",
			pkg:        "\ntype AdminClient struct{ *auditReads }\ntype auditReads struct{ client *Client }\nfunc (r *auditReads) Recent(ctx context.Context) error {\n\treturn r.client.doList(ctx, http.MethodGet, \"/audit-logs\", nil, nil)\n}\n",
			entries:    cleanProbeEntries,
			exclusions: cleanProbeExclusions,
			want:       "auditReads.Recent issues a read, and auditReads is neither Client nor a service",
		},
		{
			// Unexported is unreachable: no caller outside the package can name
			// it, and promotion carries only exported methods.
			name:       "an unexported read on an uninventoried type is not surface",
			pkg:        "\ntype warmer struct{ client *Client }\nfunc (w *warmer) refresh(ctx context.Context) error {\n\treturn w.client.doList(ctx, http.MethodGet, \"/tags\", nil, nil)\n}\n",
			entries:    cleanProbeEntries,
			exclusions: cleanProbeExclusions,
		},
		{
			// An unresolved read must fail closed off the surface as it does on
			// it, or "unknown" passes here as "sends nothing".
			name:       "an indirect read on an uninventoried type",
			pkg:        "\ntype AdminClient struct{ c *Client }\nfunc (a *AdminClient) Recent(ctx context.Context) error {\n\tsend := a.c.doList\n\treturn send(ctx, http.MethodGet, \"/audit-logs\", nil, nil)\n}\n",
			entries:    cleanProbeEntries,
			exclusions: cleanProbeExclusions,
			want:       "AdminClient.Recent reaches the transport and the guard could not resolve its verb",
		},
		{
			name:       "an indirect read two calls away on an uninventoried type",
			pkg:        "\ntype AdminClient struct{ c *Client }\nfunc (a *AdminClient) Recent(ctx context.Context) error { return a.fetch(ctx) }\nfunc (a *AdminClient) fetch(ctx context.Context) error {\n\treturn func() error { return a.c.doList(ctx, http.MethodGet, \"/audit-logs\", nil, nil) }()\n}\n",
			entries:    cleanProbeEntries,
			exclusions: cleanProbeExclusions,
			want:       "AdminClient.Recent reaches the transport and the guard could not resolve its verb",
		},
		{
			// A method value of an unexported method: the reference is followed.
			name:       "an indirect read through a method value of a helper",
			pkg:        "\ntype AdminClient struct{ c *Client }\nfunc (a *AdminClient) Recent(ctx context.Context) error {\n\tfetch := a.fetch\n\treturn fetch(ctx)\n}\nfunc (a *AdminClient) fetch(ctx context.Context) error {\n\treturn a.c.doList(ctx, http.MethodGet, \"/audit-logs\", nil, nil)\n}\n",
			entries:    cleanProbeEntries,
			exclusions: cleanProbeExclusions,
			want:       "AdminClient.Recent reaches the transport and the guard could not resolve its verb",
		},
		{
			name:       "an indirect read through a function value",
			pkg:        "\ntype AdminClient struct{ c *Client }\nfunc (a *AdminClient) Recent(ctx context.Context) error {\n\tload := loadRecent\n\treturn load(ctx, a.c)\n}\nfunc loadRecent(ctx context.Context, c *Client) error {\n\treturn c.doList(ctx, http.MethodGet, \"/audit-logs\", nil, nil)\n}\n",
			entries:    cleanProbeEntries,
			exclusions: cleanProbeExclusions,
			want:       "AdminClient.Recent reaches the transport and the guard could not resolve its verb",
		},
		{
			name:       "an exported package function that reads",
			pkg:        "\nfunc Fetch(ctx context.Context, c *Client) error {\n\treturn c.doList(ctx, http.MethodGet, \"/tags\", nil, nil)\n}\n",
			entries:    cleanProbeEntries,
			exclusions: cleanProbeExclusions,
			want:       "Fetch reaches the transport and the guard could not resolve its verb, and Fetch is a package function",
		},
		{
			// The receiver's TYPE decides, not the field's name: a *Client in a
			// field called c is the client.
			name:       "a client held in a field of another name",
			pkg:        "\ntype AdminClient struct{ c *Client }\nfunc (a *AdminClient) Recent(ctx context.Context) error { return a.c.fetch(ctx) }\nfunc (c *Client) fetch(ctx context.Context) error {\n\treturn c.doList(ctx, http.MethodGet, \"/audit-logs\", nil, nil)\n}\n",
			entries:    cleanProbeEntries,
			exclusions: cleanProbeExclusions,
			want:       "AdminClient.Recent reaches the transport",
		},
		{
			name:       "a transport name on a receiver it cannot type is taken at its word",
			pkg:        "\ntype AdminClient struct{}\nfunc (a *AdminClient) Recent(ctx context.Context) error {\n\treturn lookup().doList(ctx, http.MethodGet, \"/audit-logs\", nil, nil)\n}\n",
			entries:    cleanProbeEntries,
			exclusions: cleanProbeExclusions,
			want:       "AdminClient.Recent reaches the transport",
		},
		{
			// An untyped receiver calling a name Client declares is followed into
			// Client's method: it may be the client.
			name:       "a Client method on a receiver it cannot type is followed",
			pkg:        "\ntype AdminClient struct{}\nfunc (a *AdminClient) Recent(ctx context.Context) error { return lookup().fetch(ctx) }\nfunc (c *Client) fetch(ctx context.Context) error {\n\treturn c.doList(ctx, http.MethodGet, \"/audit-logs\", nil, nil)\n}\n",
			entries:    cleanProbeEntries,
			exclusions: cleanProbeExclusions,
			want:       "AdminClient.Recent reaches the transport",
		},
		{
			// Promotion: a wrapper embedding *Client calls Client's helpers as
			// its own.
			name:       "a wrapper that embeds the client",
			pkg:        "\ntype AdminClient struct{ *Client }\nfunc (a *AdminClient) Recent(ctx context.Context) error {\n\treturn a.doList(ctx, http.MethodGet, \"/audit-logs\", nil, nil)\n}\n",
			entries:    cleanProbeEntries,
			exclusions: cleanProbeExclusions,
			want:       "AdminClient.Recent reaches the transport",
		},
		{
			name:       "a wrapper that embeds a type holding the client",
			pkg:        "\ntype base struct{ c *Client }\nfunc (b *base) fetch(ctx context.Context) error {\n\treturn b.c.doList(ctx, http.MethodGet, \"/audit-logs\", nil, nil)\n}\ntype AdminClient struct{ base }\nfunc (a *AdminClient) Recent(ctx context.Context) error { return a.fetch(ctx) }\nfunc (a *AdminClient) Again(ctx context.Context) error { return a.c.doList(ctx, http.MethodGet, \"/x\", nil, nil) }\n",
			entries:    cleanProbeEntries,
			exclusions: cleanProbeExclusions,
			wants:      []string{"AdminClient.Again reaches the transport", "AdminClient.Recent reaches the transport"},
		},
		{
			// A promoted field is read through the embedded type: a do on a
			// promoted *memo is memo's, not the transport.
			name:       "a do on a promoted field of another type is not the transport",
			pkg:        "\ntype memo struct{}\nfunc (m *memo) do(ctx context.Context) error { return nil }\ntype base struct{ cache *memo }\ntype AdminClient struct{ base }\nfunc (a *AdminClient) Warm(ctx context.Context) error { return a.cache.do(ctx) }\n",
			entries:    cleanProbeEntries,
			exclusions: cleanProbeExclusions,
		},
		{
			// Round 8's constructions, which a hand-written resolver got wrong and
			// go/types does not: an alias embedded for the client.
			name:       "a wrapper that embeds the client through an alias",
			pkg:        "\ntype clientAlias = Client\ntype AdminClient struct{ *clientAlias }\nfunc (c *Client) doList(ctx context.Context, method, path string) error { return nil }\nfunc (a *AdminClient) Recent(ctx context.Context) error {\n\treturn a.doList(ctx, http.MethodGet, \"/audit-logs\")\n}\n",
			entries:    cleanProbeEntries,
			exclusions: cleanProbeExclusions,
			want:       "AdminClient.Recent reaches the transport",
		},
		{
			// Go selects the SHALLOWEST promoted method: Client's fetch at depth
			// one, not the harmless one two embeddings down.
			name:       "promotion at Go's own depth",
			pkg:        "\ntype harmless struct{}\nfunc (harmless) fetch(ctx context.Context) error { return nil }\ntype mid struct{ harmless }\ntype AdminClient struct {\n\tmid\n\t*Client\n}\nfunc (c *Client) fetch(ctx context.Context) error {\n\treturn c.doList(ctx, http.MethodGet, \"/audit-logs\", nil, nil)\n}\nfunc (a *AdminClient) Recent(ctx context.Context) error { return a.fetch(ctx) }\n",
			entries:    cleanProbeEntries,
			exclusions: cleanProbeExclusions,
			want:       "AdminClient.Recent reaches the transport",
		},
		{
			name:       "a promoted field through an aliased holder is read as itself",
			pkg:        "\ntype memo struct{}\nfunc (m *memo) do(ctx context.Context) error { return nil }\ntype holder struct{ cache *memo }\ntype holderAlias = holder\ntype AdminClient struct{ holderAlias }\nfunc (a *AdminClient) Warm(ctx context.Context) error { return a.cache.do(ctx) }\n",
			entries:    cleanProbeEntries,
			exclusions: cleanProbeExclusions,
		},
		{
			name:       "a constructor with two results, and an inferred var",
			pkg:        "\ntype AdminClient struct{}\ntype memo struct{}\nfunc newMemo() (*memo, error) { return &memo{}, nil }\nfunc oneMemo() *memo { return &memo{} }\nfunc (m *memo) do(ctx context.Context) error { return nil }\nfunc (a *AdminClient) Warm(ctx context.Context) error {\n\tcache, err := newMemo()\n\tif err != nil {\n\t\treturn err\n\t}\n\tvar other = oneMemo()\n\t_ = other.do(ctx)\n\treturn cache.do(ctx)\n}\n",
			entries:    cleanProbeEntries,
			exclusions: cleanProbeExclusions,
		},
		{
			// A local from a declared constructor has that constructor's type.
			name:       "a do on a constructed receiver of another type is not the transport",
			pkg:        "\ntype AdminClient struct{}\ntype memo struct{}\nfunc newMemo() *memo { return &memo{} }\nfunc (m *memo) do(ctx context.Context) error { return nil }\nfunc (a *AdminClient) Warm(ctx context.Context) error {\n\tcache := newMemo()\n\treturn cache.do(ctx)\n}\n",
			entries:    cleanProbeEntries,
			exclusions: cleanProbeExclusions,
		},
		{
			// A transport helper's name on an interface's method is taken at its
			// word: what holds the interface may be the client.
			name:       "a transport name through an interface",
			pkg:        "\ntype lister interface{ doList(ctx context.Context, method, path string) error }\ntype AdminClient struct{ l lister }\nfunc (a *AdminClient) Recent(ctx context.Context, method string) error {\n\treturn a.l.doList(ctx, method, \"/audit-logs\")\n}\n",
			entries:    cleanProbeEntries,
			exclusions: cleanProbeExclusions,
			want:       "AdminClient.Recent reaches the transport",
		},
		{
			name:       "a do on a receiver of another type is not the transport",
			pkg:        "\ntype AdminClient struct{ cache *memo }\ntype memo struct{}\nfunc (m *memo) do(ctx context.Context, verb, key string) error { return nil }\nfunc (a *AdminClient) Warm(ctx context.Context) error {\n\treturn a.cache.do(ctx, http.MethodDelete, \"tags\")\n}\n",
			entries:    cleanProbeEntries,
			exclusions: cleanProbeExclusions,
		},
		{
			// net/http's constructor with a verb the classifier cannot read.
			name:       "net/http's request constructor, verb unresolved",
			swap:       [2]string{"package octonomy\n", "package octonomy\n\nimport \"net/http\"\n"},
			pkg:        "\ntype AdminClient struct{}\nfunc (a *AdminClient) Build(method string) error {\n\t_, err := http.NewRequest(method, \"/x\", nil)\n\treturn err\n}\n",
			entries:    cleanProbeEntries,
			exclusions: cleanProbeExclusions,
			want:       "AdminClient.Build reaches the transport",
		},
		{
			name:       "an imported package's NewRequest is not net/http's",
			swap:       [2]string{"package octonomy\n", "package octonomy\n\nimport fake \"example.com/fake\"\n"},
			pkg:        "\ntype AdminClient struct{}\nfunc (a *AdminClient) Build(method string) error {\n\t_, err := fake.NewRequest(method, \"/x\", nil)\n\treturn err\n}\n",
			entries:    cleanProbeEntries,
			exclusions: cleanProbeExclusions,
		},
		{
			name:       "another package's NewRequest is not net/http's",
			pkg:        "\ntype AdminClient struct{}\nfunc (a *AdminClient) Build() error {\n\t_, err := fake.NewRequest(\"GET\", \"/x\", nil)\n\treturn err\n}\n",
			entries:    cleanProbeEntries,
			exclusions: cleanProbeExclusions,
		},
		{
			name:       "an exported method off the surface that sends nothing",
			pkg:        "\ntype Oops struct{ Code string }\nfunc (o Oops) Error() string { return o.Code }\n",
			entries:    cleanProbeEntries,
			exclusions: cleanProbeExclusions,
		},
		{
			name:       "a write on an uninventoried type is not a read",
			pkg:        "\ntype AdminClient struct{ Audit AuditService }\ntype AuditService struct{ client *Client }\nfunc (s *AuditService) Purge(ctx context.Context) error {\n\treturn s.client.do(ctx, http.MethodDelete, \"/audit-logs\", nil, nil)\n}\n",
			entries:    cleanProbeEntries,
			exclusions: cleanProbeExclusions,
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
			if len(tc.wants) > 0 {
				if len(problems) != len(tc.wants) {
					t.Fatalf("problems = %v, want %d", problems, len(tc.wants))
				}
				for i, want := range tc.wants {
					if !strings.Contains(problems[i], want) {
						t.Errorf("problem %d = %q, want it to contain %q", i, problems[i], want)
					}
				}
				return
			}
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
func (h harness) seed(t *testing.T, td *teardown, c *octonomy.Client, ns string) namespaceFixture {
	return namespaceFixture{}
}
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

// renderIsolationTest renders name as a test making exactly the runs
// isolationTests requires of it, skipping the run at index skip (-1 for none).
// The clean fixtures are rendered from the table they are checked against, so
// a requirement added to the table is a fixture added here.
func renderIsolationTest(name string, skip int) string {
	clients := map[string]string{"A": "clientA", "B": "clientB", "wildcard": "wildcard"}
	fixtures := map[string]string{"A": "fixtureA", "B": "fixtureB", "global": "fixtureGlobal"}
	var b strings.Builder
	b.WriteString("func " + name + `(t *testing.T) {
	h := loadHarness(t)
	var rows teardown
	defer rows.run()
	wildcard := h.wildcard(t)
	clientA := h.merchantClient(t, h.merchantA)
	clientB := h.merchantClient(t, h.merchantB)
	fixtureA := h.seed(t, &rows, wildcard, h.merchantA.id)
	fixtureB := h.seed(t, &rows, wildcard, h.merchantB.id)
	fixtureGlobal := h.seed(t, &rows, wildcard, "")
	includeGlobal := []octonomy.RequestOption{octonomy.WithIncludeGlobal()}
	runProbeMatrix(ctx, t, h, []probeRun{
`)
	for i, r := range isolationTests[name] {
		if i == skip {
			continue
		}
		extra := ""
		if r.option == "include_global" {
			extra = " extra: includeGlobal,"
		}
		fmt.Fprintf(&b, "\t\t{name: \"run %d\", client: %s, readNS: h.merchant%s.id, want: %s,%s expect: %s},\n",
			i, clients[r.grant], r.readNS, fixtures[r.want], extra, r.expect)
	}
	b.WriteString("\t})\n}")
	return b.String()
}

// isolationWith and includeGlobalWith are the two clean tests with one edit,
// which must apply: a fixture whose edit silently missed would test the clean
// shape.
func isolationWith(from, to string) string {
	return mustEdit(renderIsolationTest("TestIntegration_NamespaceIsolation", -1), from, to)
}

func includeGlobalWith(from, to string) string {
	return mustEdit(renderIsolationTest("TestIntegration_IncludeGlobalFailsClosed", -1), from, to)
}

func mustEdit(src, from, to string) string {
	if !strings.Contains(src, from) {
		panic("fixture edit: the source has no " + strconv.Quote(from))
	}
	return strings.Replace(src, from, to, 1)
}

// isolationSuiteFixture is a suite whose runProbeMatrix and two isolation
// tests have the required shape, with each part replaceable.
type isolationSuiteFixture struct {
	matrix, isolation, includeGlobal, extra string
}

// defaultMatrix is runProbeMatrix in the required shape.
func (isolationSuiteFixture) defaultMatrix() string {
	return `func runProbeMatrix(ctx context.Context, t *testing.T, h harness, runs []probeRun) {
	for _, probe := range readProbes(h) {
		t.Run(probe.name, func(t *testing.T) {
			for _, run := range runs {
				_, _ = probe.find(ctx, run.client, run.readNS, run.want, run.extra...)
			}
		})
	}
}`
}

func (f isolationSuiteFixture) source() string {
	if f.matrix == "" {
		f.matrix = f.defaultMatrix()
	}
	if f.isolation == "" {
		f.isolation = renderIsolationTest("TestIntegration_NamespaceIsolation", -1)
	}
	if f.includeGlobal == "" {
		f.includeGlobal = renderIsolationTest("TestIntegration_IncludeGlobalFailsClosed", -1)
	}
	return "package octonomy_test\n\n" + f.matrix + "\n\n" + f.isolation + "\n\n" + f.includeGlobal + "\n\n" + f.extra + "\n"
}

// parseIsolationFixture parses a suite fixture and a harness fixture into one
// FileSet, as the guard needs to type-check them together.
func parseIsolationFixture(t *testing.T, suite isolationSuiteFixture, gate, extra string) map[string]*ast.File {
	t.Helper()
	return parseFilesOrFatal(t, map[string]string{
		"suite.go":   suite.source(),
		"harness.go": isolationHarnessFixture(gate, extra),
	})
}

// A guard handed files from two FileSets cannot type-check them and says so,
// rather than checking them wrongly or not at all.
func TestCheckFilesRefusesFilesFromTwoFileSets(t *testing.T) {
	a := parseFixture(t, "package octonomy_test\n")["fixture.go"]
	b := parseFixture(t, "package octonomy_test\n")["fixture.go"]
	if _, why := checkFiles([]*ast.File{a, b}); !strings.Contains(why, "did not parse into one FileSet") {
		t.Errorf("checkFiles over two FileSets = %q, want it refused", why)
	}
}

// Every run isolationTests lists is required, one at a time: a suite missing
// any single one of them is refused, naming it.
func TestIsolationSuiteProblemsRequiresEveryRun(t *testing.T) {
	for _, name := range isolationTestNames() {
		for i, run := range isolationTests[name] {
			fixture := isolationSuiteFixture{}
			if name == "TestIntegration_NamespaceIsolation" {
				fixture.isolation = renderIsolationTest(name, i)
			} else {
				fixture.includeGlobal = renderIsolationTest(name, i)
			}
			files := parseIsolationFixture(t, fixture, "", "")
			problems := isolationSuiteProblems(files["suite.go"], files["harness.go"])
			if len(problems) != 1 || !strings.Contains(problems[0], name+" makes no run of "+run.describe()) {
				t.Errorf("%s without run %d (%s): problems = %v, want exactly that run reported", name, i, run.describe(), problems)
			}
		}
	}
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
			// Matching by method name let x.run(t) borrow teardown.run's
			// harmless body, whatever x was.
			name:  "a method of another type is not borrowed by name",
			extra: "type teardown struct{}\nfunc (td *teardown) run() {}\n",
			suite: isolationSuiteFixture{extra: `func TestIntegration_Extra(t *testing.T) {
	h := loadHarness(t)
	_ = h
	x := elsewhere()
	x.run(t)
}`},
			want: []string{"TestIntegration_Extra hands its *testing.T to x.run, which the guard cannot read"},
		},
		{
			name:  "a method another receiver declares is not the harness's",
			extra: "func (m merchant) settle(t *testing.T) {}\n",
			suite: isolationSuiteFixture{extra: `func TestIntegration_Extra(t *testing.T) {
	h := loadHarness(t)
	h.settle(t)
}`},
			want: []string{"TestIntegration_Extra hands its *testing.T to h.settle, which the guard cannot read"},
		},
		{
			name: "a receiver bound twice is not resolved",
			suite: isolationSuiteFixture{extra: `func TestIntegration_Extra(t *testing.T) {
	h := loadHarness(t)
	h = other
	_ = h.wildcard(t)
}`},
			want: []string{"TestIntegration_Extra hands its *testing.T to h.wildcard, which the guard cannot read"},
		},
		{
			name: "a gate shadowed by a local is not the gate",
			suite: isolationSuiteFixture{extra: `func TestIntegration_Extra(t *testing.T) {
	loadHarness := elsewhere
	h := loadHarness(t)
	h.wildcard(t)
}`},
			want: []string{
				"TestIntegration_Extra hands its *testing.T to loadHarness, which the files the guard reads do not declare",
				"TestIntegration_Extra hands its *testing.T to h.wildcard, which the guard cannot read",
			},
		},
		{
			name:  "a method resolved through var and a composite literal",
			extra: "type probeKit struct{}\nfunc (k probeKit) settle(t *testing.T) { t.Skip(\"no\") }\n",
			suite: isolationSuiteFixture{extra: `func TestIntegration_Extra(t *testing.T) {
	h := loadHarness(t)
	_ = h
	var k probeKit
	k.settle(t)
	kit := &probeKit{}
	kit.settle(t)
}`},
			want: []string{
				"TestIntegration_Extra calls k.settle, which can skip the test",
				"TestIntegration_Extra calls kit.settle, which can skip the test",
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
			suite: isolationSuiteFixture{includeGlobal: includeGlobalWith("\trunProbeMatrix(ctx, t, h, []probeRun{\n",
				"\tlater := func() { runProbeMatrix(ctx, t, h, nil) }\n\t_ = later\n\trunProbeMatrix(ctx, t, h, []probeRun{\n")},
			want: []string{"runs the probe matrix from inside a function literal"},
		},
		{
			name:  "the refusal run on the wildcard",
			suite: isolationSuiteFixture{isolation: isolationWith(`{name: "run 5", client: clientA,`, `{name: "run 5", client: wildcard,`)},
			want:  []string{"TestIntegration_NamespaceIsolation makes no run of the exact merchant-A grant reading merchant B's namespace for merchant B's fixture, expecting outcomeForbidden"},
		},
		{
			name: "the fail-closed run without include_global",
			suite: isolationSuiteFixture{includeGlobal: includeGlobalWith(
				`{name: "run 2", client: clientA, readNS: h.merchantA.id, want: fixtureGlobal, extra: includeGlobal,`,
				`{name: "run 2", client: clientA, readNS: h.merchantA.id, want: fixtureGlobal,`)},
			want: []string{"makes no run of the exact merchant-A grant reading merchant A's namespace for the global fixture with WithIncludeGlobal, expecting outcomeFiltered"},
		},
		{
			// One edited identifier: aimed at merchant B's row, the run passes
			// whether or not include_global fails closed.
			name: "the fail-closed run looking for merchant B",
			suite: isolationSuiteFixture{includeGlobal: includeGlobalWith(
				`{name: "run 2", client: clientA, readNS: h.merchantA.id, want: fixtureGlobal,`,
				`{name: "run 2", client: clientA, readNS: h.merchantA.id, want: fixtureB,`)},
			want: []string{"makes no run of the exact merchant-A grant reading merchant A's namespace for the global fixture with WithIncludeGlobal, expecting outcomeFiltered"},
		},
		{
			// Only seed(…, "") is the global namespace; any other literal is a
			// merchant's.
			name: "the global fixture seeded by a literal merchant id",
			suite: isolationSuiteFixture{includeGlobal: includeGlobalWith(
				`fixtureGlobal := h.seed(t, &rows, wildcard, "")`,
				`fixtureGlobal := h.seed(t, &rows, wildcard, "harness-merchant-b")`)},
			want: []string{
				"makes no run of the wildcard grant reading merchant A's namespace for the global fixture, expecting outcomeFiltered",
				"makes no run of the wildcard grant reading merchant A's namespace for the global fixture with WithIncludeGlobal",
				"makes no run of the exact merchant-A grant reading merchant A's namespace for the global fixture",
			},
		},
		{
			name:  "the opt-in on an exact grant is no control",
			suite: isolationSuiteFixture{includeGlobal: includeGlobalWith(`{name: "run 1", client: wildcard,`, `{name: "run 1", client: clientA,`)},
			want:  []string{"makes no run of the wildcard grant reading merchant A's namespace for the global fixture with WithIncludeGlobal, expecting outcomeVisible"},
		},
		{
			name:  "the refusal run reading its own namespace",
			suite: isolationSuiteFixture{isolation: isolationWith(`{name: "run 5", client: clientA, readNS: h.merchantB.id,`, `{name: "run 5", client: clientA, readNS: h.merchantA.id,`)},
			want:  []string{"makes no run of the exact merchant-A grant reading merchant B's namespace"},
		},
		{
			name: "a matrix that runs its subtests in parallel",
			suite: isolationSuiteFixture{matrix: `func runProbeMatrix(ctx context.Context, t *testing.T, h harness, runs []probeRun) {
	for _, probe := range readProbes(h) {
		t.Run(probe.name, func(t *testing.T) {
			for _, run := range runs {
				t.Run(run.name, func(t *testing.T) {
					t.Parallel()
					_, _ = probe.find(ctx, run.client, run.readNS, run.want, run.extra...)
				})
			}
		})
	}
}`},
			want: []string{
				"runProbeMatrix calls Parallel",
				"TestIntegration_IncludeGlobalFailsClosed calls Parallel, directly or through a helper",
				"TestIntegration_NamespaceIsolation calls Parallel, directly or through a helper",
			},
		},
		{
			name: "a matrix that goes parallel through a helper",
			suite: isolationSuiteFixture{
				matrix: strings.Replace(isolationSuiteFixture{}.defaultMatrix(), "\t\t\tfor _, run := range runs {\n",
					"\t\t\trunParallel(t)\n\t\t\tfor _, run := range runs {\n", 1),
				extra: "func runParallel(t *testing.T) { t.Parallel() }",
			},
			want: []string{
				"runProbeMatrix calls Parallel",
				"TestIntegration_IncludeGlobalFailsClosed calls Parallel, directly or through a helper",
				"TestIntegration_NamespaceIsolation calls Parallel, directly or through a helper",
			},
		},
		{
			name:  "an isolation test that goes parallel through a harness method",
			extra: "func (h harness) settle(t *testing.T) { t.Parallel() }\n",
			suite: isolationSuiteFixture{includeGlobal: includeGlobalWith("\th := loadHarness(t)\n", "\th := loadHarness(t)\n\th.settle(t)\n")},
			want:  []string{"TestIntegration_IncludeGlobalFailsClosed calls Parallel"},
		},
		{
			// Parallel on a type that is not a test's T is not testing's.
			name: "an unrelated type's Parallel",
			suite: isolationSuiteFixture{extra: `type pool struct{}

func (p pool) Parallel() {}

func TestIntegration_Extra(t *testing.T) {
	h := loadHarness(t)
	_ = h
	var p pool
	p.Parallel()
	q := pool{}
	q.Parallel()
}`},
		},
		{
			// A *testing.T passed as an interface is still the test's T.
			name: "a T made parallel through a named interface",
			suite: isolationSuiteFixture{extra: `type parallelT interface{ Parallel() }

func runParallel(t parallelT) { t.Parallel() }

func TestIntegration_Extra(t *testing.T) {
	h := loadHarness(t)
	_ = h
	runParallel(t)
}`},
			want: []string{"TestIntegration_Extra calls Parallel"},
		},
		{
			// A type the files read do not declare -- one in another test file
			// -- might be an interface a T satisfies.
			name: "a T made parallel through a type the guard cannot see",
			suite: isolationSuiteFixture{extra: `func runParallel(t runner) { t.Parallel() }

func TestIntegration_Extra(t *testing.T) {
	h := loadHarness(t)
	_ = h
	runParallel(t)
}`},
			want: []string{"TestIntegration_Extra calls Parallel"},
		},
		{
			// A defined type whose underlying type is an interface may hold a T.
			name: "a T made parallel through a type defined over an interface",
			suite: isolationSuiteFixture{extra: `type parallelT interface{ Parallel() }

type suiteT parallelT

func runParallel(t suiteT) { t.Parallel() }

func TestIntegration_Extra(t *testing.T) {
	h := loadHarness(t)
	_ = h
	runParallel(t)
}`},
			want: []string{"TestIntegration_Extra calls Parallel"},
		},
		{
			name: "a T made parallel through an interface literal",
			suite: isolationSuiteFixture{extra: `func runParallel(t interface{ Parallel() }) { t.Parallel() }

func TestIntegration_Extra(t *testing.T) {
	h := loadHarness(t)
	_ = h
	runParallel(t)
}`},
			want: []string{"TestIntegration_Extra calls Parallel"},
		},
		{
			name:  "an isolation test that runs in parallel",
			suite: isolationSuiteFixture{includeGlobal: includeGlobalWith("\th := loadHarness(t)\n", "\th := loadHarness(t)\n\tt.Parallel()\n")},
			want:  []string{"TestIntegration_IncludeGlobalFailsClosed calls Parallel"},
		},
		{
			name:  "an exact-grant client rebound to the wildcard",
			suite: isolationSuiteFixture{isolation: isolationWith("\tclientB := ", "\tclientA = h.wildcard(t)\n\tclientB := ")},
			want: []string{
				"makes no run of the exact merchant-A grant reading merchant A's namespace for merchant A's fixture",
				"makes no run of the exact merchant-A grant reading merchant A's namespace for merchant B's fixture",
				"makes no run of the exact merchant-A grant reading merchant B's namespace",
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
				"makes no run of the wildcard grant reading merchant B's namespace",
				"makes no run of the exact merchant-B grant",
				"makes no run of the exact merchant-A grant reading merchant A's namespace for merchant A's fixture",
				"makes no run of the exact merchant-A grant reading merchant A's namespace for merchant B's fixture",
				"makes no run of the wildcard grant reading merchant A's namespace for merchant B's fixture",
				"makes no run of the exact merchant-A grant reading merchant B's namespace",
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
			name:  "a required isolation test with no gate",
			suite: isolationSuiteFixture{includeGlobal: includeGlobalWith("\th := loadHarness(t)\n", "\th := harness{}\n")},
			want:  []string{"TestIntegration_IncludeGlobalFailsClosed does not call loadHarness"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := parseIsolationFixture(t, tc.suite, tc.gate, tc.extra)
			problems := isolationSuiteProblems(files["suite.go"], files["harness.go"])
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
