package octonomy

// The AST readers the source-parsing guards share: TestEveryResponseTypeCanRefuseAnEmptyDecode
// (identityfields_test.go) and TestEveryResponseTypeHasASmokeProbe
// (smokeprobes_test.go), and #97's port of readprobes_test.go after them.
//
// Every mention of main in this file means main at 5e40964, which keeps most of
// these in readprobes_test.go and identityfields_test.go. They are in a file of
// their own here so neither guard depends on a
// port that has not landed, and they are REWRITTEN rather than copied (#95), for
// the one reason a transform would have produced a guard that compiles and
// asserts about the wrong dialect: main finds a response type in a TYPE
// ARGUMENT, doData[Tag](…), and this line has no type arguments. Here the
// response type is the type of the destination ARGUMENT of a method call --
// s.client.doData(…, &out, …) after `var out Tag` -- and a list's row type is
// read off its envelope struct's Data field. responseTypes below is that
// derivation, written for this dialect with main's rules: everything it cannot
// resolve is reported rather than skipped.
//
// The Go 1.13 floor shows here too. ast.Unparen is Go 1.22, so unparen is the
// local stand-in, and ast.IndexListExpr (1.18) does not exist to match.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// parsePackageSource parses every non-test .go file of this package, the way
// TestEveryResponseDecodeIsDepthBounded does, and refuses a tree it cannot have
// read: no files, or a file that is not package octonomy.
func parsePackageSource(t *testing.T) map[string]*ast.File {
	t.Helper()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		if f.Name.Name != "octonomy" {
			t.Fatalf("%s is package %s, not octonomy -- the guard would read another package's types", path, f.Name.Name)
		}
		files[path] = f
	}
	if len(files) == 0 {
		t.Fatal("no package source files found; every guard reading them would pass vacuously")
	}
	if _, ok := files["transport.go"]; !ok {
		t.Fatal("transport.go not found; the guard is not looking at this package")
	}
	return files
}

// unparen strips any number of enclosing parentheses, as ast.Unparen does from
// Go 1.22. `(c).doData(…)` and `c.doData(…)` are the same call, and reading the
// bare node is how an earlier guard on main missed one.
func unparen(e ast.Expr) ast.Expr {
	for {
		p, ok := e.(*ast.ParenExpr)
		if !ok {
			return e
		}
		e = p.X
	}
}

// --- deriving the response types ----------------------------------------------

// transportOut names the two transport helpers that decode a response, and the
// index of the argument each one decodes INTO:
//
//	func (c *Client) doData(ctx, method, path string, query url.Values, body, out interface{}, opts ...RequestOption) error
//	func (c *Client) doList(ctx, method, path string, query url.Values, out identifiedList, opts ...RequestOption) error
//
// The index is part of the rule rather than a detail. Reading the wrong
// argument would resolve doData's request BODY -- a TagCreate -- as the
// response type, and then demand an identityFields() on a write struct.
// TestTransportOutMatchesTheHelpersSignatures pins both against transport.go.
var transportOut = map[string]int{"doData": 5, "doList": 4}

// rawTransport names the request paths beneath doData and doList, mapped to the
// functions allowed to call them. A response decoded through one of these anywhere
// else is a response neither guard can see: it reaches no destination argument,
// so its type is neither found nor unresolved -- the one way out of the
// derivation that is silent. AGENTS.md already says a resource file must not
// call doRaw; this is where that rule is checked. doUnversioned is the health
// probes' path, and HealthStatus is decoded by health.go's own helper.
//
// The allowance is by FUNCTION, not by file. Allowing all of transport.go let a
// new helper there -- a `doBare` calling doRaw, and a resource method decoding
// through it -- take its type out of both guards with the whole suite green.
var rawTransport = map[string]map[string]bool{
	"doRaw":         {"Client.do": true, "Client.doData": true, "Client.doList": true},
	"doUnversioned": {"HealthService.probe": true},
}

// typeIndex holds every package-level type declaration, so a list envelope's
// row type can be read off its Data field.
type typeIndex map[string]*ast.TypeSpec

func indexTypes(files map[string]*ast.File) typeIndex {
	out := typeIndex{}
	for _, file := range files {
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				if ts, ok := spec.(*ast.TypeSpec); ok {
					out[ts.Name.Name] = ts
				}
			}
		}
	}
	return out
}

// responseTypes collects every type the transport decodes a response into,
// mapped to where it was found:
//
//   - found holds the ROW types -- what doData decodes, and the element type of
//     each list doList decodes. It is the set main derives from doData[T] and
//     doList[T], whose T is the row in both, so the two lines' guards count the
//     same eleven types.
//   - lists maps each list envelope doList decodes (TagList, AuditLogList, …) to
//     its row type. This line has no List[T], so a list type is a declaration of
//     its own, and its rows() method is the mechanism main's doList[T] got for
//     free -- TestTheRuntimeIdentityTablesMatchTheSource holds it to that.
//   - unresolved holds every reference to a transport helper whose destination
//     this guard cannot name. A response type behind one would be checked by
//     nothing, so each is a failure rather than a skip.
//
// A REFERENCE, not only a call: `decode := s.client.doData` hands the helper
// on without this guard seeing what it decodes, so a selector naming a
// transport helper anywhere except in callee position is unresolved.
func responseTypes(files map[string]*ast.File) (found, lists map[string]string, unresolved []string) {
	found, lists = map[string]string{}, map[string]string{}
	types := indexTypes(files)

	for _, path := range sortedFileNames(files) {
		file := files[path]
		// A dot import puts another package's exported names into this file's
		// scope under their bare names, which is what this guard resolves by:
		// `. "debug/dwarf"` would make a `var out Tag` here credit the SDK's own
		// Tag while the runtime decodes dwarf.Tag. Refused outright, as on main.
		if dotImported(file) {
			unresolved = append(unresolved, path+" (dot import: bare type names are ambiguous here)")
			continue
		}
		for _, decl := range file.Decls {
			fn, isFunc := decl.(*ast.FuncDecl)
			where := path
			if isFunc {
				where = path + " (" + funcLabel(fn) + ")"
			}
			called := map[*ast.SelectorExpr]*ast.CallExpr{}
			ast.Inspect(decl, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					if sel := transportSelector(call.Fun); sel != nil {
						called[sel] = call
					}
				}
				return true
			})
			ast.Inspect(decl, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if allowed, raw := rawTransport[sel.Sel.Name]; raw && (!isFunc || !allowed[funcLabel(fn)]) {
					unresolved = append(unresolved, where+" (reaches "+sel.Sel.Name+" directly, which "+
						"decodes outside doData and doList, where neither guard can see the type; use the "+
						"helper matching the response shape)")
					return true
				}
				if !isTransportName(sel.Sel.Name) {
					return true
				}
				call, isCall := called[sel]
				if !isCall || !isFunc {
					unresolved = append(unresolved, where+" (refers to "+sel.Sel.Name+" without calling it)")
					return true
				}
				row, list, why := destination(sel, call, fn, types)
				if why != "" {
					unresolved = append(unresolved, where+" ("+why+")")
					return true
				}
				if _, seen := found[row]; !seen {
					found[row] = where
				}
				if list != "" {
					lists[list] = row
				}
				return true
			})
		}
	}
	return found, lists, unresolved
}

// transportSelector returns the selector a call invokes when that selector
// names a transport helper -- `s.client.doData`, `c.doList`, `(c).doData`.
//
// It matches the METHOD NAME and nothing about the receiver, deliberately. Only
// *Client declares these methods, so nothing else can carry the name, and
// accepting every receiver spelling is the direction that fails closed: a
// transport call on a receiver this guard did not anticipate is still
// collected, rather than silently skipped.
func transportSelector(fun ast.Expr) *ast.SelectorExpr {
	sel, ok := unparen(fun).(*ast.SelectorExpr)
	if !ok || !isTransportName(sel.Sel.Name) {
		return nil
	}
	return sel
}

func isTransportName(name string) bool {
	_, ok := transportOut[name]
	return ok
}

// isMethodExpression reports whether a transport selector's operand is a TYPE
// rather than a client value: `(*Client).doData(c, ctx, …)`. A method
// expression takes its receiver as the first argument, so every argument sits
// one place later than transportOut says, and reading the usual position would
// resolve the argument BEFORE the destination -- doData's request body -- as
// the response type, with nothing reported.
//
// A pointer type is unambiguous, since `*x.doData` would parse as a dereference
// of the selector rather than as a selector on a star. A bare name is a type
// when the package -- or the enclosing function -- declares one by that name;
// that covers an alias such as `type C = *Client` as well.
func isMethodExpression(sel *ast.SelectorExpr, types typeIndex, scope *ast.FuncDecl) bool {
	switch x := unparen(sel.X).(type) {
	case *ast.StarExpr:
		return true
	case *ast.Ident:
		_, isType := types[x.Name]
		return isType || declaresType(scope, x.Name)
	}
	return false
}

// destination resolves the response type one transport call decodes into. For
// doData the destination IS the row; for doList it is a list envelope, and the
// row is that envelope's element type.
func destination(sel *ast.SelectorExpr, call *ast.CallExpr, scope *ast.FuncDecl, types typeIndex) (row, list, why string) {
	helper := sel.Sel.Name
	if isMethodExpression(sel, types, scope) {
		return "", "", helper + " is called as a method expression, which passes the client as argument 0 and moves the destination one place along; call it on the client"
	}
	idx := transportOut[helper]
	if len(call.Args) <= idx {
		return "", "", helper + " is called with " + strconv.Itoa(len(call.Args)) + " argument(s), so its destination is not argument " + strconv.Itoa(idx)
	}
	name, why := destinationType(unparen(call.Args[idx]), scope)
	if why != "" {
		return "", "", helper + "'s destination " + why
	}
	if declaresType(scope, name) {
		return "", "", helper + " decodes into " + name + ", a type declared inside this function, which this guard would confuse with the package's"
	}
	if helper == "doData" {
		return name, "", ""
	}
	row, why = listRowType(name, types)
	if why != "" {
		return "", "", "doList decodes into " + name + ", which " + why
	}
	return row, name, ""
}

// declaresType reports whether fn declares a type of the given name in its own
// body. Such a type shadows the package's type of that name inside the
// function, and this guard resolves by bare name against the package's, so it
// is refused rather than credited to the wrong declaration.
func declaresType(fn *ast.FuncDecl, name string) bool {
	found := false
	if fn == nil || fn.Body == nil {
		return false
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if spec, ok := n.(*ast.TypeSpec); ok && spec.Name.Name == name {
			found = true
		}
		return !found
	})
	return found
}

// destinationType names the type of a destination argument, in the two shapes
// a decode into a fresh value takes: the address of a local variable declared
// with a named type (`&out` after `var out Tag`), and the address of a composite
// literal (`&Tag{}`).
//
// Everything else is refused, and the shape that matters most is a bare
// identifier. `out` handed through unchanged is a PARAMETER of the enclosing
// function -- an interface{} wrapper around the helper -- and the type that
// reaches it comes from that wrapper's callers, which this guard does not
// follow. It is this line's spelling of main's "a wrapper generic over T", and
// it is reported for the same reason: skipping it drops a response type out of
// the guard entirely.
func destinationType(arg ast.Expr, scope *ast.FuncDecl) (string, string) {
	addr, ok := arg.(*ast.UnaryExpr)
	if !ok || addr.Op != token.AND {
		return "", "is not the address of a local value, so the type that reaches it is decided elsewhere (a wrapper around the helper)"
	}
	switch x := unparen(addr.X).(type) {
	case *ast.Ident:
		return localVarType(x.Name, scope)
	case *ast.CompositeLit:
		if ident, ok := unparen(x.Type).(*ast.Ident); ok {
			return ident.Name, ""
		}
	}
	return "", "is the address of an expression this guard cannot name"
}

// localVarType reads the declared type of a local variable, by finding its ONE
// declaration in the enclosing function.
//
// One, and not "the nearest": a name declared twice -- in an inner block, or
// in a closure -- would need scope resolution to say which declaration a given
// `&out` refers to, and guessing the wrong one credits a type the runtime never
// decodes. Two declarations of the destination's name are refused instead,
// which is the fail-closed direction, and no method in this package has them.
// A parameter of that name is refused too: its type comes from a caller.
func localVarType(name string, scope *ast.FuncDecl) (string, string) {
	decls := declarationsOf(name, scope)
	if len(decls) != 1 {
		return "", "&" + name + " names a variable declared " + strconv.Itoa(len(decls)) + " times in this function, and only a single declaration is resolved without scope analysis"
	}
	switch d := decls[0].(type) {
	case *ast.ValueSpec:
		if d.Type != nil {
			if ident, ok := unparen(d.Type).(*ast.Ident); ok {
				return ident.Name, ""
			}
			return "", "&" + name + " is declared with a type that is not a plain name"
		}
		for i, ident := range d.Names {
			if ident.Name == name && i < len(d.Values) {
				return compositeType(name, d.Values[i])
			}
		}
	case *ast.AssignStmt:
		if len(d.Lhs) == len(d.Rhs) {
			for i, lhs := range d.Lhs {
				if isIdent(lhs, name) {
					return compositeType(name, d.Rhs[i])
				}
			}
		}
	case *ast.Field:
		return "", "&" + name + " is the address of a parameter, whose type its callers decide"
	}
	return "", "&" + name + " is declared in a form this guard does not read"
}

// compositeType names the type of `out := Tag{}` or `var out = Tag{}`.
func compositeType(name string, value ast.Expr) (string, string) {
	if lit, ok := unparen(value).(*ast.CompositeLit); ok {
		if ident, ok := unparen(lit.Type).(*ast.Ident); ok {
			return ident.Name, ""
		}
	}
	return "", "&" + name + " is initialised from an expression this guard cannot name"
}

// declarationsOf returns every node that declares name anywhere in fn: its
// receiver, parameters and results, a var spec, a short variable declaration
// (which is also how a type switch's `x := v.(type)` binds), a range clause,
// and the parameters of any closure inside it.
func declarationsOf(name string, fn *ast.FuncDecl) []ast.Node {
	var out []ast.Node
	fields := func(list *ast.FieldList) {
		if list == nil {
			return
		}
		for _, field := range list.List {
			for _, ident := range field.Names {
				if ident.Name == name {
					out = append(out, field)
				}
			}
		}
	}
	fields(fn.Recv)
	fields(fn.Type.Params)
	fields(fn.Type.Results)
	if fn.Body == nil {
		return out
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.ValueSpec:
			for _, ident := range node.Names {
				if ident.Name == name {
					out = append(out, node)
				}
			}
		case *ast.AssignStmt:
			if node.Tok != token.DEFINE {
				return true
			}
			for _, lhs := range node.Lhs {
				if isIdent(lhs, name) {
					out = append(out, node)
				}
			}
		case *ast.RangeStmt:
			if node.Tok != token.DEFINE {
				return true
			}
			for _, expr := range []ast.Expr{node.Key, node.Value} {
				if expr != nil && isIdent(expr, name) {
					out = append(out, node)
				}
			}
		case *ast.FuncLit:
			fields(node.Type.Params)
			fields(node.Type.Results)
		}
		return true
	})
	return out
}

// listRowType reads a list envelope's row type off its Data field, which is
// what doList decodes `{"data": [...]}` into: `Data []Tag` gives Tag.
//
// A row type spelled any other way -- a pointer element, a qualified name, an
// alias for the envelope, no Data field at all -- is refused. None exists, and
// each would need a rule of its own to be read correctly.
func listRowType(list string, types typeIndex) (string, string) {
	spec, ok := types[list]
	if !ok {
		return "", "is not a type declared in this package"
	}
	if spec.Assign.IsValid() {
		return "", "is an alias, and an alias's row type is another declaration's"
	}
	st, ok := unparen(spec.Type).(*ast.StructType)
	if !ok {
		return "", "is not a struct, so it has no Data field to read the row type from"
	}
	var rows []ast.Expr
	for _, field := range st.Fields.List {
		for _, ident := range field.Names {
			if ident.Name == "Data" {
				rows = append(rows, field.Type)
			}
		}
	}
	if len(rows) != 1 {
		return "", "has no Data field"
	}
	array, ok := unparen(rows[0]).(*ast.ArrayType)
	if !ok || array.Len != nil {
		return "", "has a Data field that is not a slice"
	}
	elem, ok := unparen(array.Elt).(*ast.Ident)
	if !ok {
		return "", "has a Data field whose element is not a plain type name"
	}
	return elem.Name, ""
}

// dotImported reports whether a file dot-imports another package.
func dotImported(file *ast.File) bool {
	for _, imp := range file.Imports {
		if imp.Name != nil && imp.Name.Name == "." {
			return true
		}
	}
	return false
}

// funcLabel names a declaration the way an error message should: "TagService.Get"
// for a method, "decodeJSON" for a function.
func funcLabel(fn *ast.FuncDecl) string {
	if recv := receiverTypeName(fn); recv != "" {
		return recv + "." + fn.Name.Name
	}
	return fn.Name.Name
}

// --- methods and their receivers ----------------------------------------------

// method is one declared method and whether its receiver is a pointer.
type method struct {
	decl    *ast.FuncDecl
	pointer bool
}

type methodSet map[string]method

// declaredMethods indexes every method by "RecvType.Name", recording the
// receiver kind, which is load-bearing here rather than incidental.
func declaredMethods(files map[string]*ast.File) methodSet {
	out := methodSet{}
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 {
				continue
			}
			typ := unparen(fn.Recv.List[0].Type)
			pointer := false
			if star, ok := typ.(*ast.StarExpr); ok {
				pointer, typ = true, unparen(star.X)
			}
			ident, ok := typ.(*ast.Ident)
			if !ok {
				continue
			}
			out[ident.Name+"."+fn.Name.Name] = method{decl: fn, pointer: pointer}
		}
	}
	return out
}

func receiverTypeName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	switch recv := unparen(fn.Recv.List[0].Type).(type) {
	case *ast.StarExpr:
		if ident, ok := unparen(recv.X).(*ast.Ident); ok {
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
			structType, ok := unparen(spec.Type).(*ast.StructType)
			if !ok {
				return true
			}
			for _, field := range structType.Fields.List {
				star, ok := unparen(field.Type).(*ast.StarExpr)
				if !ok {
					continue
				}
				ident, ok := unparen(star.X).(*ast.Ident)
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

// resolveCallee maps a call expression to the declaration it reaches, for the
// call shapes this package uses: a plain function, a sibling method on the
// receiver, and a method on the client the service holds.
//
// The client shape is matched EXACTLY -- `<receiver>.client.<method>`. Accepting
// any nested selector, as an earlier revision on main did, resolved
// `s.cache.lookup()` as `Client.lookup` and substituted an unrelated body for
// this one's.
func resolveCallee(fun ast.Expr, recv, recvType string, index funcIndex) *ast.FuncDecl {
	switch f := unparen(fun).(type) {
	case *ast.Ident:
		return index[f.Name]
	case *ast.SelectorExpr:
		if recv == "" {
			return nil
		}
		// s.helper(...) -- a sibling method on the same service.
		if isIdent(f.X, recv) && recvType != "" {
			return index[recvType+"."+f.Sel.Name]
		}
		// s.client.method(...) -- the client, and only through that field.
		if inner, ok := unparen(f.X).(*ast.SelectorExpr); ok && inner.Sel.Name == "client" {
			if isIdent(inner.X, recv) {
				return index["Client."+f.Sel.Name]
			}
		}
	}
	return nil
}

// isIdent reports whether expr is the identifier name, ignoring parentheses.
// `(c) = other` rebinds c exactly as `c = other` does.
func isIdent(expr ast.Expr, name string) bool {
	ident, ok := unparen(expr).(*ast.Ident)
	return ok && ident.Name == name
}

// rebinds reports whether name is declared again OR assigned to anywhere inside
// body -- a short variable declaration, a plain assignment, a var spec, a range
// clause, or a nested closure parameter.
//
// Assignment matters as much as declaration. After `s = other` the identifier
// still denotes the same variable, so a scope resolver would say it is the
// receiver; what it holds is something else, and crediting a call on it would
// report a decode the method never makes.
func rebinds(body *ast.BlockStmt, name string) bool {
	return len(bindingsOf(body, name)) > 0
}

// bindingsOf returns every node inside body that declares or assigns name.
func bindingsOf(body *ast.BlockStmt, name string) []ast.Node {
	var out []ast.Node
	if body == nil {
		return out
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.AssignStmt:
			for _, lhs := range node.Lhs {
				if isIdent(lhs, name) {
					out = append(out, node)
				}
			}
		case *ast.ValueSpec:
			for _, ident := range node.Names {
				if ident.Name == name {
					out = append(out, node)
				}
			}
		case *ast.RangeStmt:
			for _, expr := range []ast.Expr{node.Key, node.Value} {
				if expr != nil && isIdent(expr, name) {
					out = append(out, node)
				}
			}
		case *ast.IncDecStmt:
			if isIdent(node.X, name) {
				out = append(out, node)
			}
		case *ast.FuncLit:
			if node.Type != nil && node.Type.Params != nil {
				for _, param := range node.Type.Params.List {
					for _, ident := range param.Names {
						if ident.Name == name {
							out = append(out, node)
						}
					}
				}
			}
		}
		return true
	})
	return out
}

// --- small AST readers ----------------------------------------------------------

func paramNames(fields *ast.FieldList) []string {
	if fields == nil {
		return nil
	}
	var out []string
	for _, field := range fields.List {
		for _, name := range field.Names {
			out = append(out, name.Name)
		}
		if len(field.Names) == 0 {
			out = append(out, "_")
		}
	}
	return out
}

func paramTypes(sig *ast.FuncType) []string  { return fieldTypes(sig.Params) }
func resultTypes(sig *ast.FuncType) []string { return fieldTypes(sig.Results) }

func fieldTypes(fields *ast.FieldList) []string {
	if fields == nil {
		return nil
	}
	var out []string
	for _, field := range fields.List {
		n := len(field.Names)
		if n == 0 {
			n = 1
		}
		for i := 0; i < n; i++ {
			out = append(out, exprString(field.Type))
		}
	}
	return out
}

// exprString renders the type expressions these guards have to compare.
// Anything else renders as "?" and fails the comparison, which is the safe
// direction.
func exprString(expr ast.Expr) string {
	switch e := unparen(expr).(type) {
	case *ast.Ident:
		return e.Name
	case *ast.StarExpr:
		return "*" + exprString(e.X)
	case *ast.ArrayType:
		if e.Len == nil {
			return "[]" + exprString(e.Elt)
		}
	case *ast.SelectorExpr:
		return exprString(e.X) + "." + e.Sel.Name
	case *ast.InterfaceType:
		if e.Methods == nil || len(e.Methods.List) == 0 {
			return "interface{}"
		}
	case *ast.Ellipsis:
		return "..." + exprString(e.Elt)
	}
	return "?"
}

// stringKeys returns a map[string]string's keys, sorted. contractbaseline_test.go
// has sortedKeys for a map[string]bool; without type parameters, each map type
// needs its own.
func stringKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedFileNames(files map[string]*ast.File) []string {
	out := make([]string, 0, len(files))
	for k := range files {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// parseFixture parses one synthetic source file for a guard's own tests.
func parseFixture(t *testing.T, src string) map[string]*ast.File {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", src, 0)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	return map[string]*ast.File{"fixture.go": file}
}
