package octonomy

// The *Update tag guard (#96): the source half of what update_test.go checks by
// marshalling. It is this line's own. main's optional_test.go, at 5e40964,
// guards Optional[T] and its omitzero tags, and neither exists here; the
// disposition table, docs/compat-test-disposition.md, records why.
//
// The failure it exists for is silent on the toolchain a reviewer runs. Go
// 1.13's encoding/json parses `omitzero` as a tag option and matches nothing,
// so a *string tagged `json:"description,omitzero"` is ALWAYS emitted -- as
// "description":null when nil, on every PATCH that does not set it, clearing a
// column the caller never named (#37, #64) on a line whose releases cannot be
// recalled. A modern encoding/json honours the option, so the same diff
// marshals cleanly there. A port that swaps Optional[string] for *string and
// keeps the tag is exactly that diff, and update_test.go's one-key check sees
// it only when run under go1.13.15, and only on the types in its table.
//
// So these read source, hold on any toolchain, and need no table:
//
//   - TestNoStructTagCarriesOmitzero: no struct tag in what this module ships
//     names the option -- in a declared type, or in the anonymous struct each
//     *Update's MarshalJSON marshals, which a scan of declared types would miss.
//   - TestEveryUpdateFieldCanBeLeftOut: every field of every type named *Update
//     is tagged omitempty and has a state that sends nothing and that no value
//     a caller sets can reach -- a pointer, or Metadata behind a value-receiver
//     MarshalJSON.
//   - TestEveryPatchBodyIsAnUpdateType: the check above finds its types by
//     name, so every PATCH sent through the transport -- do and doData, which
//     TestRequestBodyNamesEveryHelperThatSendsOne holds to doRaw's callers --
//     is held to that name.
//
// TestUpdateBodiesNamesEveryUpdateType then holds update_test.go's table to
// the source, so the per-field round-trip there cannot miss a type.
//
// WHERE THIS ENDS: it proves each field CAN be left out, never that the server
// treats the key as the caller meant, and it does not read what a MarshalJSON
// does -- update_test.go marshals every field of every type for that, and pins
// the key names to the contract by hand. A request built outside doRaw, with
// net/http directly, is outside both, as it is outside every transport guard.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// knownUpdateTypes is the floor: a finder that stopped matching would satisfy
// every loop below by finding nothing.
const knownUpdateTypes = 3

// requestBody names the helpers that send a request body, and the index of the
// body argument in each:
//
//	func (c *Client) do(ctx, method, path string, query url.Values, body interface{}, opts ...RequestOption) error
//	func (c *Client) doData(ctx, method, path string, query url.Values, body, out interface{}, opts ...RequestOption) error
//
// doList sends none. The table is closed -- a helper missing from it is not
// read at all -- so TestRequestBodyNamesEveryHelperThatSendsOne holds it to
// every function rawTransport (sourceguard_test.go) allows to call doRaw, the
// one path to the wire: a helper that hands doRaw a body must be listed here,
// at the index of the parameter it hands on.
var requestBody = map[string]int{"do": 4, "doData": 4}

// requestMethodArg is the method's index in each requestBody helper; rawBodyArg
// and rawMethodArg are the body's and method's in doRaw itself.
const (
	requestMethodArg = 1
	rawMethodArg     = 1
	rawBodyArg       = 4
)

// --- the guards, over this tree ---------------------------------------------------

func TestNoStructTagCarriesOmitzero(t *testing.T) {
	found, scanned := omitzeroTags(shippedSource(t))
	if scanned == 0 {
		t.Fatal("no struct tags read; the check would pass vacuously")
	}
	for _, where := range found {
		t.Errorf("%s is tagged omitzero. Go 1.13's encoding/json ignores the option, so the field "+
			"is always sent -- a nil pointer as null, which on a PATCH clears a column the caller "+
			"never named. Use omitempty (docs/porting-checklist.md)", where)
	}
}

func TestEveryUpdateFieldCanBeLeftOut(t *testing.T) {
	files := parsePackageSource(t)
	if n := len(updateTypes(files)); n < knownUpdateTypes {
		t.Fatalf("found %d types named *Update, want at least %d; the guard is not reading them", n, knownUpdateTypes)
	}
	for _, problem := range updateProblems(files) {
		t.Error(problem)
	}
}

func TestEveryPatchBodyIsAnUpdateType(t *testing.T) {
	files := parsePackageSource(t)
	found, problems := patchBodies(files)
	if len(found) < knownUpdateTypes {
		t.Errorf("found %d PATCH body types, want at least %d; the derivation is not reading the "+
			"Update methods", len(found), knownUpdateTypes)
	}
	for _, problem := range problems {
		t.Error(problem)
	}
}

// update_test.go marshals the types in updateBodies, and a hand-written table
// is the shape that rots: a type added later with no row is checked by nothing
// there. This holds the table to the source, in both directions.
func TestUpdateBodiesNamesEveryUpdateType(t *testing.T) {
	want := map[string]bool{}
	for name := range updateTypes(parsePackageSource(t)) {
		want[name] = true
	}
	if len(want) < knownUpdateTypes {
		t.Fatalf("found %d types named *Update, want at least %d", len(want), knownUpdateTypes)
	}
	got := map[string]bool{}
	for _, body := range updateBodies {
		if body.typ.Name() != body.name {
			t.Errorf("updateBodies row %q holds %s; a row's name and type must agree", body.name, body.typ.Name())
		}
		got[body.typ.Name()] = true
	}
	for _, name := range sortedKeys(want) {
		if !got[name] {
			t.Errorf("updateBodies (update_test.go) has no row for %s, so no field of it is marshalled "+
				"and compared with its tag encoding. Add one", name)
		}
	}
	for _, name := range sortedKeys(got) {
		if !want[name] {
			t.Errorf("updateBodies has a row for %s, which the source does not declare as a *Update", name)
		}
	}
}

// Both indexes are read by POSITION, and the table is closed, so each has to be
// held to the source. Reading the wrong argument as the method would classify
// no request as a PATCH, and a body-sending helper missing from the table would
// not be read at all; either way the guard passes having checked nothing.
//
// What a helper sends is read off its call to doRaw rather than off a
// parameter's name: a helper that hands doRaw nil sends no body, and one that
// hands it a parameter sends that parameter.
func TestRequestBodyNamesEveryHelperThatSendsOne(t *testing.T) {
	methods := declaredMethods(parsePackageSource(t))
	raw, ok := methods["Client.doRaw"]
	if !ok {
		t.Fatal("Client.doRaw is not declared; the transport this guard reads has moved")
	}
	if names := paramNames(raw.decl.Type.Params); len(names) <= rawBodyArg ||
		names[rawBodyArg] != "body" || names[rawMethodArg] != "method" {
		t.Fatalf("doRaw's parameters are %v; rawBodyArg and rawMethodArg must index body and method", names)
	}

	allowed := rawTransport["doRaw"]
	for _, helper := range sortedIntKeys(requestBody) {
		if !allowed["Client."+helper] {
			t.Errorf("requestBody names %s, which rawTransport does not allow to call doRaw", helper)
		}
	}
	for _, label := range sortedKeys(allowed) {
		fn, ok := methods[label]
		if !ok {
			t.Errorf("rawTransport allows %s to call doRaw, and it is not declared", label)
			continue
		}
		helper := fn.decl.Name.Name
		body, why := helperBody(fn.decl)
		idx, listed := requestBody[helper]
		switch {
		case why != "":
			t.Errorf("%s: %s", label, why)
		case body < 0 && listed:
			t.Errorf("requestBody lists %s, which hands doRaw no body", helper)
		case body >= 0 && !listed:
			t.Errorf("%s hands doRaw a body and is not in requestBody, so a PATCH sent through it is "+
				"checked by nothing. Add it, at index %d", label, body)
		case body >= 0 && idx != body:
			t.Errorf("requestBody[%q] = %d, but %s hands doRaw its parameter %d", helper, idx, label, body)
		}
	}
}

// helperBody reads which parameter a transport helper hands doRaw as the
// request body: its index, or -1 for nil. It also requires the method doRaw is
// handed to be the helper's parameter at requestMethodArg, since that is the
// argument TestEveryPatchBodyIsAnUpdateType reads at the helper's call sites.
//
// Both parameters must reach doRaw as they arrived. `body = envelope{Data: body}`
// before the call would send a type the call sites never name, and a reassigned
// method a PATCH they never show, so a helper that rebinds either is refused
// (rebinds, sourceguard_test.go).
func helperBody(fn *ast.FuncDecl) (int, string) {
	params := paramNames(fn.Type.Params)
	indexOf := func(e ast.Expr) int {
		if ident, ok := unparen(e).(*ast.Ident); ok {
			for i, name := range params {
				if name == ident.Name {
					return i
				}
			}
		}
		return -2
	}
	body, calls := -1, 0
	var why string
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := unparen(call.Fun).(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "doRaw" {
			return true
		}
		calls++
		if len(call.Args) <= rawBodyArg {
			why = "calls doRaw with too few arguments to read its body"
			return false
		}
		if isIdent(call.Args[rawBodyArg], "nil") {
			return true
		}
		if body = indexOf(call.Args[rawBodyArg]); body < 0 {
			why = "hands doRaw a body that is not one of its parameters, so what it sends is decided here"
			return false
		}
		if indexOf(call.Args[rawMethodArg]) != requestMethodArg {
			why = "hands doRaw a method that is not its parameter " + strconv.Itoa(requestMethodArg) +
				", where TestEveryPatchBodyIsAnUpdateType reads it"
			return false
		}
		for _, i := range []int{body, requestMethodArg} {
			if rebinds(fn.Body, params[i]) {
				why = "assigns to its parameter " + params[i] + " before handing it to doRaw, so what " +
					"is sent is not what its call sites pass"
			}
		}
		return false
	})
	switch {
	case why != "":
		return 0, why
	case calls != 1:
		return 0, "calls doRaw " + strconv.Itoa(calls) + " times; this guard reads a helper with exactly one call"
	}
	return body, ""
}

// --- omitzero, in every shipped struct ------------------------------------------

// shippedSource parses every non-test .go file in the module: the package, and
// the examples a reader copies into a Go 1.13 program. Test files are left out,
// since nothing in them reaches a caller's wire.
func shippedSource(t *testing.T) map[string]*ast.File {
	t.Helper()
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if name := info.Name(); path != "." && (strings.HasPrefix(name, ".") || name == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(path)] = f
		return nil
	})
	if err != nil {
		t.Fatalf("walk the module: %v", err)
	}
	for _, want := range []string{"transport.go", "tags.go"} {
		if _, ok := files[want]; !ok {
			t.Fatalf("%s not found; the guard is not looking at this module", want)
		}
	}
	return files
}

// omitzeroTags returns every struct field whose json tag names omitzero, and
// how many tagged fields it read. It inspects every struct type node, so the
// anonymous struct inside a MarshalJSON counts as much as a declared type.
func omitzeroTags(files map[string]*ast.File) (found []string, scanned int) {
	for _, path := range sortedFileNames(files) {
		for _, decl := range files[path].Decls {
			where := path
			if fn, ok := decl.(*ast.FuncDecl); ok {
				where = path + " (" + funcLabel(fn) + ")"
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				st, ok := n.(*ast.StructType)
				if !ok {
					return true
				}
				for _, field := range st.Fields.List {
					if field.Tag == nil {
						continue
					}
					scanned++
					if _, opts, _ := jsonTag(field); opts["omitzero"] {
						found = append(found, where+": field "+fieldLabel(field))
					}
				}
				return true
			})
		}
	}
	return found, scanned
}

// --- every *Update field can be left out ----------------------------------------

// updateTypes returns every package-level type whose name ends in Update.
func updateTypes(files map[string]*ast.File) typeIndex {
	out := typeIndex{}
	for name, spec := range indexTypes(files) {
		if strings.HasSuffix(name, "Update") {
			out[name] = spec
		}
	}
	return out
}

// updateProblems returns what is wrong with each *Update type, one line per
// defect. Each is a miss that compiles, vets and marshals without an error, and
// puts a key on the wire the caller never set, or drops one they did.
func updateProblems(files map[string]*ast.File) []string {
	methods := declaredMethods(files)
	types := updateTypes(files)
	var out []string
	for _, name := range sortedTypeNames(types) {
		out = append(out, updateTypeProblems(name, types[name], methods)...)
	}
	return out
}

func updateTypeProblems(name string, spec *ast.TypeSpec, methods methodSet) []string {
	if spec.Assign.IsValid() {
		return []string{name + " is an alias; its fields are another declaration's, which this guard does not follow"}
	}
	st, ok := unparen(spec.Type).(*ast.StructType)
	if !ok {
		return []string{name + " is named as a PATCH body but is not a struct, so it has no fields to read"}
	}
	var out []string
	var metadata []string
	keys := map[string]string{}
	for _, field := range st.Fields.List {
		key, isMetadata, problems := updateFieldProblems(name, field)
		out = append(out, problems...)
		if isMetadata {
			metadata = append(metadata, fieldLabel(field))
		}
		if key == "" {
			continue
		}
		if first, dup := keys[key]; dup {
			out = append(out, name+"."+fieldLabel(field)+" has the json key "+strconv.Quote(key)+", as "+
				first+" does. encoding/json drops every field of a duplicated key, so neither is sent")
			continue
		}
		keys[key] = fieldLabel(field)
	}

	marshal, declared := methods[name+".MarshalJSON"]
	if declared && marshal.pointer {
		out = append(out, name+" declares MarshalJSON on the POINTER receiver. Update takes the struct "+
			"by value, and a pointer method is not in a value's method set, so encoding/json skips it "+
			"without an error and sends the tag encoding instead")
	}
	if declared && (len(paramTypes(marshal.decl.Type)) != 0 ||
		strings.Join(resultTypes(marshal.decl.Type), ", ") != "[]byte, error") {
		out = append(out, name+" declares a MarshalJSON that is not func() ([]byte, error), so it does "+
			"not satisfy json.Marshaler and encoding/json ignores it")
	}
	if len(metadata) > 0 && !declared {
		out = append(out, name+" carries Metadata ("+strings.Join(metadata, ", ")+") and declares no "+
			"MarshalJSON. omitempty drops an empty map, so Metadata{} never reaches the server and the "+
			"stored object is not emptied (#37); TagUpdate.MarshalJSON is the pattern")
	}
	return out
}

// updateFieldProblems checks one field: a single named field, a json key, the
// omitempty option, and a type with a state that sends nothing.
func updateFieldProblems(typeName string, field *ast.Field) (key string, metadata bool, problems []string) {
	if len(field.Names) == 0 {
		return "", false, []string{typeName + " embeds " + exprString(field.Type) + ". An embedded " +
			"field's fields and methods are another declaration's -- a promoted MarshalJSON included -- " +
			"which this guard does not follow"}
	}
	label := typeName + "." + fieldLabel(field)
	if !field.Names[0].IsExported() {
		problems = append(problems, label+" is unexported, so encoding/json never sends it")
	}
	if len(field.Names) > 1 {
		problems = append(problems, label+" are declared together, so they share one tag and one "+
			"json key; encoding/json drops every field of a duplicated key, so none is sent")
	}

	key, opts, why := jsonTag(field)
	switch {
	case why != "":
		problems = append(problems, label+" "+why)
	case !opts["omitempty"]:
		problems = append(problems, label+" is not tagged omitempty, so it is sent on every PATCH "+
			"that does not set it -- as null for a pointer, clearing a column the caller never named")
	}

	switch typ := unparen(field.Type).(type) {
	case *ast.StarExpr:
		// nil sends nothing, and every non-nil pointer is sent, whatever it
		// points at: omitempty tests a pointer for nil only.
	case *ast.Ident:
		if typ.Name == "Metadata" {
			metadata = true
			break
		}
		problems = append(problems, noOmittedState(label, field.Type))
	default:
		problems = append(problems, noOmittedState(label, field.Type))
	}
	return key, metadata, problems
}

func noOmittedState(label string, typ ast.Expr) string {
	return label + " is a " + exprString(typ) + ", which has no state that sends nothing and that no " +
		"caller's value reaches: under omitempty its zero value -- \"\", false, 0, an empty slice or " +
		"map -- can never be sent, and without it the zero value is sent on every PATCH. Make it a pointer"
}

// jsonTag reads a field's json key and options as encoding/json does, or says
// why the field cannot be a PATCH field.
func jsonTag(field *ast.Field) (key string, opts map[string]bool, why string) {
	if field.Tag == nil {
		return "", nil, "has no struct tag, so encoding/json sends it under its Go name"
	}
	raw, err := strconv.Unquote(field.Tag.Value)
	if err != nil {
		return "", nil, "has a tag this guard cannot unquote: " + err.Error()
	}
	value, ok := reflect.StructTag(raw).Lookup("json")
	if !ok {
		return "", nil, "has no json tag, so encoding/json sends it under its Go name"
	}
	if value == "-" {
		return "", nil, "is tagged json:\"-\", so it is never sent: a field the caller sets that the server never sees"
	}
	parts := strings.Split(value, ",")
	opts = map[string]bool{}
	for _, opt := range parts[1:] {
		opts[opt] = true
	}
	if parts[0] == "" {
		return "", opts, "has a json tag with no key, so encoding/json sends it under its Go name"
	}
	return parts[0], opts, ""
}

// --- every PATCH body is a *Update ----------------------------------------------

// patchBodies collects the type of every request body the package sends with
// PATCH, mapped to where it is sent, and a problem for each body that is not a
// declared *Update and each call it cannot classify. A call it cannot read
// could be a PATCH with any body, so it is reported rather than skipped.
func patchBodies(files map[string]*ast.File) (found map[string]string, problems []string) {
	found = map[string]string{}
	types := indexTypes(files)
	updates := updateTypes(files)
	for _, path := range sortedFileNames(files) {
		// A dot import puts another package's exported names into this file
		// under their bare names, which is what bodyType resolves by: a foreign
		// TagUpdate would be credited to the SDK's. Refused, as in responseTypes.
		if dotImported(files[path]) {
			problems = append(problems, path+" dot-imports a package, so a bare body type name there is ambiguous")
			continue
		}
		for _, decl := range files[path].Decls {
			fn, isFunc := decl.(*ast.FuncDecl)
			where := path
			if isFunc {
				where = path + " (" + funcLabel(fn) + ")"
			}
			called := map[*ast.SelectorExpr]*ast.CallExpr{}
			ast.Inspect(decl, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					if sel, ok := unparen(call.Fun).(*ast.SelectorExpr); ok && sendsBody(sel.Sel.Name) {
						called[sel] = call
					}
				}
				return true
			})
			ast.Inspect(decl, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok || !sendsBody(sel.Sel.Name) {
					return true
				}
				name, why := patchBody(sel, called[sel], fn, types)
				switch {
				case why != "":
					problems = append(problems, where+" "+why)
				case name == "":
				default:
					if _, seen := found[name]; !seen {
						found[name] = where
					}
					if _, ok := updates[name]; !ok {
						problems = append(problems, where+" sends a "+name+" as a PATCH body. The *Update "+
							"guard finds PATCH bodies by name, so this one is checked by nothing: name it "+
							"*Update")
					}
				}
				return true
			})
		}
	}
	return found, problems
}

func sendsBody(name string) bool {
	_, ok := requestBody[name]
	return ok
}

// patchBody classifies one reference to a body-sending helper. It returns the
// body's type name for a PATCH, "" for any other request or a PATCH with no
// body, and a reason when the reference cannot be classified.
func patchBody(sel *ast.SelectorExpr, call *ast.CallExpr, fn *ast.FuncDecl, types typeIndex) (string, string) {
	helper := sel.Sel.Name
	if call == nil {
		return "", "refers to " + helper + " without calling it, so what it sends is decided elsewhere"
	}
	if fn == nil {
		return "", "calls " + helper + " outside a function, where this guard resolves no declaration"
	}
	if isMethodExpression(sel, types, fn) {
		return "", "calls " + helper + " as a method expression, which moves every argument one place along"
	}
	idx := requestBody[helper]
	if len(call.Args) <= idx {
		return "", "calls " + helper + " with " + strconv.Itoa(len(call.Args)) + " argument(s), so its body is not argument " + strconv.Itoa(idx)
	}
	patch, why := isPatch(call.Args[requestMethodArg])
	if why != "" {
		return "", "calls " + helper + " with " + why
	}
	if !patch {
		return "", ""
	}
	name, why := bodyType(call.Args[idx], fn)
	if why != "" {
		return "", "sends a PATCH whose body " + why
	}
	return name, ""
}

// isPatch reads a request's method argument: an http.Method constant or a
// string literal. Anything else -- a variable, a parameter -- could be PATCH.
func isPatch(arg ast.Expr) (bool, string) {
	switch a := unparen(arg).(type) {
	case *ast.SelectorExpr:
		if isIdent(a.X, "http") && strings.HasPrefix(a.Sel.Name, "Method") {
			return a.Sel.Name == "MethodPatch", ""
		}
	case *ast.BasicLit:
		if a.Kind == token.STRING {
			if s, err := strconv.Unquote(a.Value); err == nil {
				return strings.EqualFold(s, "PATCH"), ""
			}
		}
	}
	return false, "a method argument, " + exprString(arg) + ", that this guard cannot read as a constant, so it may be a PATCH"
}

// bodyType names the type of a PATCH body: a parameter or local declared with a
// named type (`in` in `Update(ctx, id string, in TagUpdate)`), its address, or a
// composite literal. nil is a request with no body, which sends no field.
func bodyType(arg ast.Expr, scope *ast.FuncDecl) (string, string) {
	expr := unparen(arg)
	if addr, ok := expr.(*ast.UnaryExpr); ok && addr.Op == token.AND {
		expr = unparen(addr.X)
	}
	switch x := expr.(type) {
	case *ast.CompositeLit:
		if name := namedType(x.Type); name != "" {
			return checkedBodyType(name, scope)
		}
	case *ast.Ident:
		if x.Name == "nil" {
			return "", ""
		}
		decls := declarationsOf(x.Name, scope)
		if len(decls) != 1 {
			return "", "is " + x.Name + ", declared " + strconv.Itoa(len(decls)) + " times in this function, " +
				"and only a single declaration is resolved without scope analysis"
		}
		name := declaredType(x.Name, decls[0])
		if name == "" {
			return "", "is " + x.Name + ", declared with a type that is not a plain name"
		}
		return checkedBodyType(name, scope)
	}
	return "", "is " + exprString(arg) + ", an expression this guard cannot name"
}

// checkedBodyType refuses a body type declared inside the function. Such a type
// shadows the package's type of that name, and this guard resolves by bare name
// against the package's, so it would credit the guarded declaration instead.
func checkedBodyType(name string, scope *ast.FuncDecl) (string, string) {
	if declaresType(scope, name) {
		return "", "is a " + name + ", a type declared inside this function, which this guard would confuse with the package's"
	}
	return name, ""
}

// declaredType reads the named type one declaration gives name: a parameter's,
// a var spec's, or that of the composite literal it is initialised from.
func declaredType(name string, decl ast.Node) string {
	switch d := decl.(type) {
	case *ast.Field:
		return namedType(d.Type)
	case *ast.ValueSpec:
		if d.Type != nil {
			return namedType(d.Type)
		}
		for i, ident := range d.Names {
			if ident.Name == name && i < len(d.Values) {
				return literalType(d.Values[i])
			}
		}
	case *ast.AssignStmt:
		if len(d.Lhs) != len(d.Rhs) {
			return ""
		}
		for i, lhs := range d.Lhs {
			if isIdent(lhs, name) {
				return literalType(d.Rhs[i])
			}
		}
	}
	return ""
}

// literalType returns T for `T{…}` or `&T{…}`, and "" for anything else.
func literalType(expr ast.Expr) string {
	expr = unparen(expr)
	if addr, ok := expr.(*ast.UnaryExpr); ok && addr.Op == token.AND {
		expr = unparen(addr.X)
	}
	if lit, ok := expr.(*ast.CompositeLit); ok {
		return namedType(lit.Type)
	}
	return ""
}

// namedType returns T for `T` or `*T`, and "" for anything else.
func namedType(expr ast.Expr) string {
	expr = unparen(expr)
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = unparen(star.X)
	}
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name
	}
	return ""
}

// --- small readers ----------------------------------------------------------------

func fieldLabel(field *ast.Field) string {
	if len(field.Names) == 0 {
		return exprString(field.Type)
	}
	names := make([]string, len(field.Names))
	for i, name := range field.Names {
		names[i] = name.Name
	}
	return strings.Join(names, ", ")
}

func sortedTypeNames(types typeIndex) []string {
	out := make([]string, 0, len(types))
	for name := range types {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func sortedIntKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// --- fixtures: the guards' own behaviour -----------------------------------------

// On a correct tree a guard passes whether or not it works, so these drive each
// one over synthetic source. The cases that matter most are the silent ones: a
// tag the scan never reaches, a field it lets through, a PATCH it does not see.

func TestOmitzeroTagsFindsTheOptionWhereverAStructIsDeclared(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want int
	}{
		{"a declared type", "type TagUpdate struct { D *string `json:\"d,omitzero\"` }", 1},
		{
			// The shape every *Update's MarshalJSON has: a scan of declared types
			// alone does not see this struct.
			name: "the anonymous struct a MarshalJSON marshals",
			src: "func (u TagUpdate) MarshalJSON() ([]byte, error) {\n" +
				"	return json.Marshal(struct { D *string `json:\"d,omitzero\"` }{D: u.D})\n}",
			want: 1,
		},
		{"a package-level var's type", "var v struct { D *string `json:\"d,omitzero\"` }", 1},
		{"a struct nested in a field", "type T struct { In struct { D *string `json:\"d,omitzero\"` } `json:\"in\"` }", 1},
		{"after another option", "type T struct { D *string `json:\"d,omitempty,omitzero\"` }", 1},
		{"beside another tag key", "type T struct { D *string `db:\"d\" json:\"d,omitzero\"` }", 1},
		{"an interpreted string tag", "type T struct { D *string \"json:\\\"d,omitzero\\\"\" }", 1},
		{"one per field", "type T struct { A *string `json:\"a,omitzero\"`; B *string `json:\"b,omitzero\"` }", 2},
		// A key that happens to be spelled omitzero is a key, not the option.
		{"a key named omitzero", "type T struct { D *string `json:\"omitzero\"` }", 0},
		{"omitempty", "type T struct { D *string `json:\"d,omitempty\"` }", 0},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			found, scanned := omitzeroTags(parseFixture(t, "package octonomy\n"+tc.src))
			if len(found) != tc.want {
				t.Errorf("found %d (%v), want %d", len(found), found, tc.want)
			}
			if scanned == 0 {
				t.Error("read no tags")
			}
		})
	}
}

func TestUpdateProblemsRefuseAFieldThatCannotBeLeftOut(t *testing.T) {
	const header = "package octonomy\ntype Metadata = map[string]interface{}\n"
	const valueMarshal = "\nfunc (u AUpdate) MarshalJSON() ([]byte, error) { return nil, nil }"
	for _, tc := range []struct {
		name    string
		src     string
		want    int
		mention string // a word every problem must carry
	}{
		{name: "pointer fields tagged omitempty", src: "type AUpdate struct {\n" +
			"	Name *string `json:\"name,omitempty\"`\n	On *bool `json:\"on,omitempty\"`\n}"},
		{name: "Metadata behind a value-receiver MarshalJSON",
			src: "type AUpdate struct { Metadata Metadata `json:\"metadata,omitempty\"` }" + valueMarshal},
		// A type not named *Update is not read here; TestEveryPatchBodyIsAnUpdateType
		// refuses one sent as a PATCH body.
		{name: "a type with another name", src: "type TagPatch struct { Name string `json:\"name\"` }"},

		{name: "a leftover omitzero", src: "type AUpdate struct { Name *string `json:\"name,omitzero\"` }",
			want: 1, mention: "omitempty"},
		{name: "no omitempty", src: "type AUpdate struct { Name *string `json:\"name\"` }",
			want: 1, mention: "omitempty"},
		{name: "a string", src: "type AUpdate struct { Name string `json:\"name,omitempty\"` }",
			want: 1, mention: "pointer"},
		{name: "a bool", src: "type AUpdate struct { On bool `json:\"on,omitempty\"` }",
			want: 1, mention: "pointer"},
		{name: "a slice", src: "type AUpdate struct { IDs []string `json:\"ids,omitempty\"` }",
			want: 1, mention: "pointer"},
		{name: "a map that is not Metadata", src: "type AUpdate struct { M map[string]string `json:\"m,omitempty\"` }",
			want: 1, mention: "pointer"},
		{name: "a value field without omitempty", src: "type AUpdate struct { Name string `json:\"name\"` }",
			want: 2, mention: "AUpdate.Name"},
		{name: "Metadata with no MarshalJSON", src: "type AUpdate struct { Metadata Metadata `json:\"metadata,omitempty\"` }",
			want: 1, mention: "#37"},
		{name: "a pointer-receiver MarshalJSON", src: "type AUpdate struct { Metadata Metadata `json:\"metadata,omitempty\"` }\n" +
			"func (u *AUpdate) MarshalJSON() ([]byte, error) { return nil, nil }",
			want: 1, mention: "POINTER"},
		{name: "a pointer-receiver MarshalJSON on pointer fields", src: "type AUpdate struct { Name *string `json:\"name,omitempty\"` }\n" +
			"func (u *AUpdate) MarshalJSON() ([]byte, error) { return nil, nil }",
			want: 1, mention: "POINTER"},
		{name: "no tag", src: "type AUpdate struct { Name *string }", want: 1, mention: "Go name"},
		{name: "no json tag", src: "type AUpdate struct { Name *string `db:\"name\"` }", want: 1, mention: "Go name"},
		{name: "no key", src: "type AUpdate struct { Name *string `json:\",omitempty\"` }", want: 1, mention: "Go name"},
		{name: "never sent", src: "type AUpdate struct { Name *string `json:\"-\"` }", want: 1, mention: "never sent"},
		{name: "two names, one tag", src: "type AUpdate struct { A, B *string `json:\"a,omitempty\"` }",
			want: 1, mention: "duplicated key"},
		{name: "two fields, one key", src: "type AUpdate struct {\n" +
			"	A *string `json:\"a,omitempty\"`\n	B *string `json:\"a,omitempty\"`\n}",
			want: 1, mention: "duplicated key"},
		{name: "an embedded field", src: "type Base struct{}\ntype AUpdate struct { Base; Name *string `json:\"name,omitempty\"` }",
			want: 1, mention: "embeds"},
		{name: "an alias", src: "type BUpdate struct { Name *string `json:\"name,omitempty\"` }\ntype AUpdate = BUpdate",
			want: 1, mention: "alias"},
		{name: "not a struct", src: "type AUpdate map[string]interface{}", want: 1, mention: "not a struct"},
		{name: "an unexported *Update is read too", src: "type scopeUpdate struct { Name string `json:\"name,omitempty\"` }",
			want: 1, mention: "scopeUpdate.Name"},
		{name: "an unexported field", src: "type AUpdate struct { name *string `json:\"name,omitempty\"` }",
			want: 1, mention: "unexported"},
		{name: "a MarshalJSON with no error result", src: "type AUpdate struct { Metadata Metadata `json:\"metadata,omitempty\"` }\n" +
			"func (u AUpdate) MarshalJSON() []byte { return nil }",
			want: 1, mention: "json.Marshaler"},
		{name: "a MarshalJSON taking an argument", src: "type AUpdate struct { Name *string `json:\"name,omitempty\"` }\n" +
			"func (u AUpdate) MarshalJSON(indent bool) ([]byte, error) { return nil, nil }",
			want: 1, mention: "json.Marshaler"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := updateProblems(parseFixture(t, header+tc.src))
			if len(got) != tc.want {
				t.Fatalf("got %d problem(s), want %d:\n%s", len(got), tc.want, strings.Join(got, "\n"))
			}
			for _, problem := range got {
				if !strings.Contains(problem, tc.mention) {
					t.Errorf("problem %q does not mention %q", problem, tc.mention)
				}
			}
		})
	}
}

func TestPatchBodiesFindsEveryPatch(t *testing.T) {
	const header = "package octonomy\n" +
		"type TagUpdate struct { Name *string `json:\"name,omitempty\"` }\n" +
		"type TagCreate struct{}\ntype TagPatch struct{}\n"
	for _, tc := range []struct {
		name     string
		src      string // appended to header
		full     string // a whole file, for a case the header cannot precede
		found    []string
		problems int
	}{
		{
			name: "a dot import",
			full: "package octonomy\nimport . \"example.com/other\"\n" +
				`func (s *S) Patch(ctx context.Context, in TagUpdate) error { return s.client.do(ctx, http.MethodPatch, "/t", nil, in) }`,
			problems: 1,
		},
		{
			name: "doData with an http.MethodPatch and a parameter",
			src: `func (s *TagService) Update(ctx context.Context, id string, in TagUpdate) error {
				var out Tag
				return s.client.doData(ctx, http.MethodPatch, "/tags/"+id, nil, in, &out)
			}`,
			found: []string{"TagUpdate"},
		},
		{
			name:  "do, which sends a body too",
			src:   `func (s *S) Patch(ctx context.Context, in TagUpdate) error { return s.client.do(ctx, http.MethodPatch, "/t", nil, in) }`,
			found: []string{"TagUpdate"},
		},
		{
			name:  "a string literal, in any case",
			src:   `func (s *S) Patch(ctx context.Context, in TagUpdate) error { return s.client.do(ctx, "patch", "/t", nil, in) }`,
			found: []string{"TagUpdate"},
		},
		{
			name:  "the address of the parameter",
			src:   `func (s *S) Patch(ctx context.Context, in TagUpdate) error { return s.client.do(ctx, http.MethodPatch, "/t", nil, &in) }`,
			found: []string{"TagUpdate"},
		},
		{
			name:  "a pointer parameter",
			src:   `func (s *S) Patch(ctx context.Context, in *TagUpdate) error { return s.client.do(ctx, http.MethodPatch, "/t", nil, in) }`,
			found: []string{"TagUpdate"},
		},
		{
			name:  "a composite literal",
			src:   `func (s *S) Patch(ctx context.Context) error { return (s.client).do(ctx, http.MethodPatch, "/t", nil, TagUpdate{}) }`,
			found: []string{"TagUpdate"},
		},
		{
			name:  "a local declared with the type",
			src:   `func (s *S) Patch(ctx context.Context) error { var in TagUpdate; return s.client.do(ctx, http.MethodPatch, "/t", nil, in) }`,
			found: []string{"TagUpdate"},
		},
		{
			name:  "a local from a literal",
			src:   `func (s *S) Patch(ctx context.Context) error { in := &TagUpdate{}; return s.client.do(ctx, http.MethodPatch, "/t", nil, in) }`,
			found: []string{"TagUpdate"},
		},
		{
			name: "every other method is left alone",
			src: `func (s *S) Create(ctx context.Context, in TagCreate) error { return s.client.do(ctx, http.MethodPost, "/t", nil, in) }
			func (s *S) Delete(ctx context.Context) error { return s.client.do(ctx, "DELETE", "/t", nil, nil) }`,
		},
		{
			name: "a PATCH with no body sends no field",
			src:  `func (s *S) Touch(ctx context.Context) error { return s.client.do(ctx, http.MethodPatch, "/t", nil, nil) }`,
		},

		// The one this half exists for: a PATCH body the name-keyed guard never reads.
		{
			name:     "a PATCH body not named *Update",
			src:      `func (s *S) Patch(ctx context.Context, in TagPatch) error { return s.client.do(ctx, http.MethodPatch, "/t", nil, in) }`,
			found:    []string{"TagPatch"},
			problems: 1,
		},
		{
			name:     "a type named *Update but declared nowhere at package level",
			src:      `func (s *S) Patch(ctx context.Context, in OtherUpdate) error { return s.client.do(ctx, http.MethodPatch, "/t", nil, in) }`,
			found:    []string{"OtherUpdate"},
			problems: 1,
		},
		{
			name:     "a method this guard cannot read",
			src:      `func (s *S) Send(ctx context.Context, method string, in TagCreate) error { return s.client.do(ctx, method, "/t", nil, in) }`,
			problems: 1,
		},
		{
			name:     "an http package under another name",
			src:      `func (s *S) Patch(ctx context.Context, in TagPatch) error { return s.client.do(ctx, nethttp.MethodPatch, "/t", nil, in) }`,
			problems: 1,
		},
		{
			name:     "a body handed through as interface{}",
			src:      `func (c *Client) patch(ctx context.Context, body interface{}) error { return c.do(ctx, http.MethodPatch, "/t", nil, body) }`,
			problems: 1,
		},
		{
			name: "a body declared twice",
			src: `func (s *S) Patch(ctx context.Context, in TagUpdate) error {
				if true { in := TagPatch{}; return s.client.do(ctx, http.MethodPatch, "/t", nil, in) }
				return nil
			}`,
			problems: 1,
		},
		{
			name: "a local type shadowing the package's",
			src: `func (s *S) Patch(ctx context.Context) error {
				type TagUpdate struct { Name string ` + "`json:\"name\"`" + ` }
				return s.client.do(ctx, http.MethodPatch, "/t", nil, TagUpdate{})
			}`,
			problems: 1,
		},
		{
			name:     "a body this guard cannot name",
			src:      `func (s *S) Patch(ctx context.Context) error { return s.client.do(ctx, http.MethodPatch, "/t", nil, s.body()) }`,
			problems: 1,
		},
		{
			name:     "a helper bound to a variable",
			src:      `func (s *S) Patch(ctx context.Context, in TagPatch) error { send := s.client.do; return send(ctx, http.MethodPatch, "/t", nil, in) }`,
			problems: 1,
		},
		{
			name:     "a method expression",
			src:      `func (s *S) Patch(ctx context.Context, in TagPatch) error { return (*Client).do(s.client, ctx, http.MethodPatch, "/t", nil, in) }`,
			problems: 1,
		},
		{
			name:     "too few arguments to reach the body",
			src:      `func (s *S) Patch(ctx context.Context) error { return s.client.do(ctx, http.MethodPatch) }`,
			problems: 1,
		},
		{
			name:     "a call outside a function",
			src:      `var patched = client.do(ctx, http.MethodPatch, "/t", nil, TagPatch{})`,
			problems: 1,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			src := header + tc.src
			if tc.full != "" {
				src = tc.full
			}
			found, problems := patchBodies(parseFixture(t, src))
			var names []string
			for name := range found {
				names = append(names, name)
			}
			sort.Strings(names)
			if strings.Join(names, ",") != strings.Join(tc.found, ",") {
				t.Errorf("found %v, want %v", names, tc.found)
			}
			if len(problems) != tc.problems {
				t.Errorf("got %d problem(s), want %d:\n%s", len(problems), tc.problems, strings.Join(problems, "\n"))
			}
		})
	}
}

func TestHelperBodyReadsWhatAHelperHandsDoRaw(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want int
		why  string // a substring of the refusal; "" means none
	}{
		{"a body parameter", `func (c *Client) do(ctx context.Context, method, path string, q url.Values, body interface{}) error {
			_, _, err := c.doRaw(ctx, method, path, q, body); return err }`, 4, ""},
		// The parameter's NAME is not the rule: what is handed to doRaw is.
		{"a body under another name", `func (c *Client) send(ctx context.Context, method string, payload interface{}) error {
			_, _, err := c.doRaw(ctx, method, "/t", nil, payload); return err }`, 2, ""},
		{"nil", `func (c *Client) doList(ctx context.Context, method, path string, q url.Values, out interface{}) error {
			_, _, err := c.doRaw(ctx, method, path, q, nil); return err }`, -1, ""},
		{"a body built inside the helper", `func (c *Client) send(ctx context.Context, method string) error {
			_, _, err := c.doRaw(ctx, method, "/t", nil, TagPatch{}); return err }`, 0, "not one of its parameters"},
		{"a method that is not the method parameter", `func (c *Client) send(ctx context.Context, body interface{}) error {
			_, _, err := c.doRaw(ctx, http.MethodPatch, "/t", nil, body); return err }`, 0, "method"},
		{"a body rewrapped before the call", `func (c *Client) send(ctx context.Context, method string, body interface{}) error {
			body = envelope{Data: body}
			_, _, err := c.doRaw(ctx, method, "/t", nil, body); return err }`, 0, "assigns to its parameter body"},
		{"a method rewritten before the call", `func (c *Client) send(ctx context.Context, method string, body interface{}) error {
			if method == "PUT" { method = http.MethodPatch }
			_, _, err := c.doRaw(ctx, method, "/t", nil, body); return err }`, 0, "assigns to its parameter method"},
		{"no call", `func (c *Client) send(ctx context.Context) error { return nil }`, 0, "0 times"},
		{"two calls", `func (c *Client) send(ctx context.Context, method string, body interface{}) error {
			c.doRaw(ctx, method, "/a", nil, body); _, _, err := c.doRaw(ctx, method, "/b", nil, body); return err }`, 0, "2 times"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			files := parseFixture(t, "package octonomy\n"+tc.src)
			var fn *ast.FuncDecl
			for _, decl := range files["fixture.go"].Decls {
				if d, ok := decl.(*ast.FuncDecl); ok {
					fn = d
				}
			}
			got, why := helperBody(fn)
			if tc.why != "" {
				if !strings.Contains(why, tc.why) {
					t.Errorf("refusal %q, want one mentioning %q", why, tc.why)
				}
				return
			}
			if why != "" || got != tc.want {
				t.Errorf("got %d (%q), want %d", got, why, tc.want)
			}
		})
	}
}
