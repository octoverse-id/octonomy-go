package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

// The SDK side of the gate, read as Go rather than as text.
//
// The first draft of this file was regexps, and the review that killed it was
// right: `CodeFoo ErrorCode = "foo"` stops matching a pattern that assumes an
// untyped constant, `scopeParam string = "scope"` stops resolving a setter, and
// both failures are SILENT -- the tool reports a comparison it did not make.
// go/parser has none of those failure modes and is in the standard library.
//
// What this buys, beyond robustness, is the two comparisons the issue asks for
// that a spec-to-spec diff structurally cannot make:
//
//   - Query parameters PER OPERATION. A parameter is not "implemented" because
//     its name appears somewhere in the package: `limit` on /tag-resolution is
//     not implemented by pagination.go setting `limit` on the list routes. The
//     analysis walks from the coverage row's method to the params struct in its
//     signature, into that struct's query builder and anything it embeds, and
//     adds the transport's own chokepoint parameters, which really do apply to
//     every call.
//   - The RESPONSE MODEL. `doData[Tag]` names the type the method decodes into,
//     so the contract's Tag schema can be compared against the Go struct's JSON
//     tags field by field -- which is what makes "the server added a field"
//     visible without anyone noticing it by hand.
//
// It also derives each method's route and transport helper, so an inventory row
// is checked against what the method DOES rather than against the existence of a
// function with the right name.

// transportHelpers are the four request paths a resource method may take. The
// helper is not decoration: it determines the response envelope the method
// decodes, which docs/contract-coverage.yaml records as `actual_response`.
var transportHelpers = map[string]string{
	"doList":        "list-envelope",
	"doData":        "data-envelope",
	"do":            "none",
	"doUnversioned": "bare",
}

// SDKPackage is the parsed SDK package.
type SDKPackage struct {
	Root string

	fset    *token.FileSet
	files   map[string]*ast.File // by base file name
	methods map[string]*sdkMethod
	structs map[string]*sdkStruct
	consts  map[string]string // identifier -> its string value
}

type sdkMethod struct {
	File string
	Recv string
	Name string
	Decl *ast.FuncDecl
}

type sdkStruct struct {
	File string
	Name string
	Type *ast.StructType
}

// Route is what a resource method actually does on the wire.
type Route struct {
	// Method is the HTTP method, lowercased: "get", "post", ...
	Method string
	// Path is the request path with every escaped segment normalized to "{}",
	// so it compares against a spec path whose placeholders are named.
	Path string
	// Helper is the transport helper the method calls.
	Helper string
	// Model is the type argument of doData/doList -- the type the response
	// decodes into. Empty for the helpers that decode nothing.
	Model string
}

// LoadSDKPackage parses every non-test .go file in the SDK package directory.
func LoadSDKPackage(root string) (*SDKPackage, error) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, root, func(info fs.FileInfo) bool {
		// Tests are excluded deliberately: a parameter set only by a fixture is a
		// parameter the client never sends.
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", root, err)
	}

	p := &SDKPackage{
		Root:    root,
		fset:    fset,
		files:   map[string]*ast.File{},
		methods: map[string]*sdkMethod{},
		structs: map[string]*sdkStruct{},
		consts:  map[string]string{},
	}
	for name, pkg := range pkgs {
		if strings.HasSuffix(name, "_test") {
			continue
		}
		for path, file := range pkg.Files {
			base := path[strings.LastIndexByte(path, '/')+1:]
			p.files[base] = file
			p.index(base, file)
		}
	}
	if len(p.files) == 0 {
		return nil, fmt.Errorf("%s: no Go sources -- is this the SDK repository root?", root)
	}
	return p, nil
}

func (p *SDKPackage) index(file string, f *ast.File) {
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil || len(d.Recv.List) == 0 {
				continue
			}
			recv := receiverType(d.Recv.List[0].Type)
			if recv == "" {
				continue
			}
			p.methods[recv+"."+d.Name.Name] = &sdkMethod{File: file, Recv: recv, Name: d.Name.Name, Decl: d}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if st, ok := s.Type.(*ast.StructType); ok {
						p.structs[s.Name.Name] = &sdkStruct{File: file, Name: s.Name.Name, Type: st}
					}
				case *ast.ValueSpec:
					// Typed and untyped alike: `scopeParam = "scope"` and
					// `scopeParam string = "scope"` are the same declaration to a
					// reader and must be the same declaration here.
					for i, name := range s.Names {
						if i >= len(s.Values) {
							continue
						}
						if lit, ok := stringLit(s.Values[i]); ok {
							p.consts[name.Name] = lit
						}
					}
				}
			}
		}
	}
}

// ErrorCodes returns the wire value of every Code* constant, keyed by value.
func (p *SDKPackage) ErrorCodes() map[string]string {
	out := map[string]string{}
	for name, value := range p.consts {
		if strings.HasPrefix(name, "Code") {
			out[value] = name
		}
	}
	return out
}

// Method reports the declaration behind an inventory row's "Receiver.Method".
func (p *SDKPackage) Method(symbol string) (*sdkMethod, bool) {
	m, ok := p.methods[symbol]
	return m, ok
}

// Route derives what the named method does on the wire.
//
// It fails rather than guesses. A method whose route cannot be read is reported
// as a finding, because the alternative -- treating "I could not tell" as "it is
// fine" -- is the silent pass this whole tool exists to remove. If a refactor
// makes this fail, the fix is to teach it the new shape or to keep the shape
// AGENTS.md already requires of resource files.
func (p *SDKPackage) Route(symbol string) (Route, error) {
	m, ok := p.methods[symbol]
	if !ok {
		return Route{}, fmt.Errorf("no method %s is declared in this package", symbol)
	}
	return p.routeOf(m, nil, 0)
}

func (p *SDKPackage) routeOf(m *sdkMethod, scope map[string]string, depth int) (Route, error) {
	if depth > 1 {
		return Route{}, fmt.Errorf("%s: gave up following calls looking for a transport helper", m.Name)
	}

	var (
		route Route
		found bool
		perr  error
	)
	ast.Inspect(m.Decl.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || found {
			return !found
		}
		helper, model := helperCall(call)
		if helper == "" {
			return true
		}
		method, pathExpr := methodAndPath(call)
		if method == "" {
			// A helper that fixes its own method takes none: doUnversioned issues
			// a GET and nothing else, so the method lives in its declaration and
			// the path is the argument bound to its `path` parameter. Reading it
			// from there rather than special-casing the name keeps the derivation
			// honest -- if that helper ever stops being a GET, this follows.
			method, pathExpr = p.methodAndPathFromHelper(helper, call)
			if method == "" {
				perr = fmt.Errorf("%s: %s neither takes an http.Method* argument nor declares one", m.Name, helper)
				return false
			}
		}
		path, err := p.renderPath(pathExpr, scope)
		if err != nil {
			perr = fmt.Errorf("%s: %w", m.Name, err)
			return false
		}
		route = Route{Method: strings.ToLower(method), Path: path, Helper: helper, Model: model}
		found = true
		return false
	})
	if perr != nil {
		return Route{}, perr
	}
	if found {
		return route, nil
	}

	// One level of indirection, which is exactly what the health probes need:
	// Live and Ready are thin wrappers around a shared probe() that takes the
	// path as a parameter. The callee supplies the method and the helper; the
	// caller supplies the path, substituted into the callee's parameter names.
	var (
		inner    *sdkMethod
		innerArg map[string]string
	)
	ast.Inspect(m.Decl.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || inner != nil {
			return inner == nil
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		next, ok := p.methods[m.Recv+"."+sel.Sel.Name]
		if !ok {
			return true
		}
		inner = next
		innerArg = p.bindParams(next, call.Args)
		return false
	})
	if inner == nil {
		return Route{}, fmt.Errorf("%s: no transport helper call found (expected one of doData, doList, do, doUnversioned)", m.Name)
	}
	return p.routeOf(inner, innerArg, depth+1)
}

// methodAndPathFromHelper reads the HTTP method out of a transport helper's own
// declaration, for the helper that does not take one, and picks the argument
// bound to that helper's `path` parameter.
func (p *SDKPackage) methodAndPathFromHelper(helper string, call *ast.CallExpr) (string, ast.Expr) {
	decl, ok := p.methods["Client."+helper]
	if !ok {
		return "", nil
	}
	var method string
	ast.Inspect(decl.Decl.Body, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok || method != "" {
			return method == ""
		}
		pkg, ok := sel.X.(*ast.Ident)
		if ok && pkg.Name == "http" && strings.HasPrefix(sel.Sel.Name, "Method") {
			method = strings.TrimPrefix(sel.Sel.Name, "Method")
		}
		return true
	})
	if method == "" {
		return "", nil
	}
	// The call is a method value (c.doUnversioned(ctx, path)), so its arguments
	// line up with the declaration's parameters directly.
	index := 0
	for _, field := range decl.Decl.Type.Params.List {
		for _, name := range field.Names {
			if name.Name == "path" && index < len(call.Args) {
				return method, call.Args[index]
			}
			index++
		}
	}
	return "", nil
}

// bindParams maps a callee's parameter names to the caller's arguments, as far
// as those arguments resolve to string constants.
func (p *SDKPackage) bindParams(callee *sdkMethod, args []ast.Expr) map[string]string {
	out := map[string]string{}
	i := 0
	for _, field := range callee.Decl.Type.Params.List {
		for _, name := range field.Names {
			if i < len(args) {
				if value, ok := p.resolveString(args[i]); ok {
					out[name.Name] = value
				}
			}
			i++
		}
	}
	return out
}

// QueryParams returns the query parameter names the named method puts on the
// wire: the ones its params struct builds, plus the transport's own.
func (p *SDKPackage) QueryParams(symbol string) (map[string]bool, error) {
	m, ok := p.methods[symbol]
	if !ok {
		return nil, fmt.Errorf("no method %s is declared in this package", symbol)
	}
	out := p.TransportParams()
	for _, typeName := range p.paramStructs(m) {
		for name := range p.settersOn(typeName, map[string]bool{}) {
			out[name] = true
		}
	}
	return out, nil
}

// TransportParams are the parameters set at the request chokepoint rather than
// by any one resource: they reach every method through a RequestOption, so they
// count as sent for every operation that documents them.
func (p *SDKPackage) TransportParams() map[string]bool {
	out := map[string]bool{}
	if f, ok := p.files["transport.go"]; ok {
		for name := range p.settersIn(f) {
			out[name] = true
		}
	}
	return out
}

// paramStructs names the local struct types in a method's signature -- the
// params struct it builds its query from.
func (p *SDKPackage) paramStructs(m *sdkMethod) []string {
	var out []string
	for _, field := range m.Decl.Type.Params.List {
		name := baseTypeName(field.Type)
		if name == "" {
			continue
		}
		if _, ok := p.structs[name]; ok {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// settersOn collects the query parameters set by any method on the type, and by
// anything it embeds. Embedding is how every list params struct gets limit and
// offset from ListOptions.
func (p *SDKPackage) settersOn(typeName string, seen map[string]bool) map[string]bool {
	out := map[string]bool{}
	if seen[typeName] {
		return out
	}
	seen[typeName] = true

	for _, m := range p.methods {
		if m.Recv != typeName || m.Decl.Body == nil {
			continue
		}
		for name := range p.settersIn(m.Decl.Body) {
			out[name] = true
		}
	}
	st, ok := p.structs[typeName]
	if !ok {
		return out
	}
	for _, field := range st.Type.Fields.List {
		if len(field.Names) != 0 {
			continue // named field, not embedded
		}
		embedded := baseTypeName(field.Type)
		if embedded == "" {
			continue
		}
		for name := range p.settersOn(embedded, seen) {
			out[name] = true
		}
	}
	return out
}

// settersIn collects `X.Set("name", ...)` and `X.Set(nameConst, ...)` under a
// node. Header sets do not survive: their names are canonical HTTP headers, and
// resolveString returns them verbatim, so the caller's snake_case expectation
// simply never matches -- but the filter is explicit below rather than implied.
func (p *SDKPackage) settersIn(node ast.Node) map[string]bool {
	out := map[string]bool{}
	ast.Inspect(node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (sel.Sel.Name != "Set" && sel.Sel.Name != "Add") {
			return true
		}
		value, ok := p.resolveString(call.Args[0])
		if !ok || !isQueryParamName(value) {
			return true
		}
		out[value] = true
		return true
	})
	return out
}

// isQueryParamName keeps HTTP header names out of the query-parameter set. The
// contracts name every query parameter in lower snake_case and every header in
// canonical Header-Case, so the two vocabularies do not overlap.
func isQueryParamName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}

// JSONFields returns the JSON names a struct decodes, mapped to the Go field.
// Embedded structs are flattened the way encoding/json flattens them.
func (p *SDKPackage) JSONFields(typeName string) (map[string]string, bool) {
	st, ok := p.structs[typeName]
	if !ok {
		return nil, false
	}
	out := map[string]string{}
	p.collectJSONFields(st, out, map[string]bool{})
	return out, true
}

func (p *SDKPackage) collectJSONFields(st *sdkStruct, out map[string]string, seen map[string]bool) {
	if seen[st.Name] {
		return
	}
	seen[st.Name] = true

	for _, field := range st.Type.Fields.List {
		name, omitted := jsonName(field)
		if len(field.Names) == 0 {
			// Embedded: encoding/json promotes its fields unless the embedded
			// field itself carries a name.
			if name != "" {
				out[name] = baseTypeName(field.Type)
				continue
			}
			if embedded, ok := p.structs[baseTypeName(field.Type)]; ok {
				p.collectJSONFields(embedded, out, seen)
			}
			continue
		}
		if omitted || name == "" {
			continue
		}
		out[name] = field.Names[0].Name
	}
}

// --- expression helpers ---------------------------------------------------------

// helperCall reports the transport helper a call invokes and, for the generic
// ones, the type it decodes into.
func helperCall(call *ast.CallExpr) (helper, model string) {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		if _, ok := transportHelpers[fun.Name]; ok {
			return fun.Name, ""
		}
	case *ast.SelectorExpr:
		// c.do(...) and friends.
		if _, ok := transportHelpers[fun.Sel.Name]; ok {
			return fun.Sel.Name, ""
		}
	case *ast.IndexExpr:
		// doData[Tag](...) -- one type argument.
		if ident, ok := fun.X.(*ast.Ident); ok {
			if _, known := transportHelpers[ident.Name]; known {
				return ident.Name, baseTypeName(fun.Index)
			}
		}
	case *ast.IndexListExpr:
		if ident, ok := fun.X.(*ast.Ident); ok {
			if _, known := transportHelpers[ident.Name]; known && len(fun.Indices) > 0 {
				return ident.Name, baseTypeName(fun.Indices[0])
			}
		}
	}
	return "", ""
}

// methodAndPath finds the http.Method* argument and the expression right after
// it. Positional indices are deliberately not used: the four helpers have four
// different signatures, and the one invariant they share is that the path
// follows the method.
func methodAndPath(call *ast.CallExpr) (method string, path ast.Expr) {
	for i, arg := range call.Args {
		sel, ok := arg.(*ast.SelectorExpr)
		if !ok {
			continue
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "http" || !strings.HasPrefix(sel.Sel.Name, "Method") {
			continue
		}
		if i+1 >= len(call.Args) {
			return "", nil
		}
		return strings.TrimPrefix(sel.Sel.Name, "Method"), call.Args[i+1]
	}
	return "", nil
}

// renderPath turns a path expression into the request path, with every escaped
// segment collapsed to "{}" so it can be compared with a spec path whose
// placeholders carry names this package does not know.
func (p *SDKPackage) renderPath(expr ast.Expr, scope map[string]string) (string, error) {
	switch e := expr.(type) {
	case *ast.BasicLit:
		if value, ok := stringLit(e); ok {
			return value, nil
		}
	case *ast.Ident:
		if value, ok := scope[e.Name]; ok {
			return value, nil
		}
		if value, ok := p.consts[e.Name]; ok {
			return value, nil
		}
		return "", fmt.Errorf("path identifier %q does not resolve to a string constant", e.Name)
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			break
		}
		left, err := p.renderPath(e.X, scope)
		if err != nil {
			return "", err
		}
		right, err := p.renderPath(e.Y, scope)
		if err != nil {
			return "", err
		}
		return left + right, nil
	case *ast.CallExpr:
		return p.renderPathCall(e, scope)
	}
	return "", fmt.Errorf("cannot read the request path from a %T", expr)
}

func (p *SDKPackage) renderPathCall(call *ast.CallExpr, scope map[string]string) (string, error) {
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		// url.PathEscape(id) -- a caller-supplied segment, whatever it is named.
		pkg, ok := fun.X.(*ast.Ident)
		if ok && pkg.Name == "url" && fun.Sel.Name == "PathEscape" {
			return "{}", nil
		}
	case *ast.Ident:
		// resourcePath(resourceType, resourceID, "/tags") -- the one path helper
		// resources.go and audit.go share. Its first two arguments are escaped
		// segments and its third is a literal suffix.
		if fun.Name == "resourcePath" && len(call.Args) == 3 {
			suffix, err := p.renderPath(call.Args[2], scope)
			if err != nil {
				return "", err
			}
			return "/resources/{}/{}" + suffix, nil
		}
	}
	return "", fmt.Errorf("cannot read the request path from a call to %s", exprName(call.Fun))
}

// resolveString reads a string literal, or an identifier that names one.
func (p *SDKPackage) resolveString(expr ast.Expr) (string, bool) {
	switch e := expr.(type) {
	case *ast.BasicLit:
		return stringLit(e)
	case *ast.Ident:
		value, ok := p.consts[e.Name]
		return value, ok
	}
	return "", false
}

func stringLit(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return value, true
}

// jsonName reads a field's JSON name from its struct tag, and reports whether
// the field is excluded from JSON entirely.
func jsonName(field *ast.Field) (name string, omitted bool) {
	if field.Tag == nil {
		if len(field.Names) == 1 {
			return "", false
		}
		return "", false
	}
	tag, ok := stringLit(field.Tag)
	if !ok {
		return "", false
	}
	value, ok := structTag(tag, "json")
	if !ok {
		return "", false
	}
	first, _, _ := strings.Cut(value, ",")
	if first == "-" {
		return "", true
	}
	return first, false
}

// structTag reads one key out of a struct tag. It is reflect.StructTag.Get,
// spelled out here so this file does not reach for reflect to parse text.
func structTag(tag, key string) (string, bool) {
	st := tag
	for st != "" {
		i := 0
		for i < len(st) && st[i] == ' ' {
			i++
		}
		st = st[i:]
		i = 0
		for i < len(st) && st[i] > ' ' && st[i] != ':' && st[i] != '"' {
			i++
		}
		if i == 0 || i+1 >= len(st) || st[i] != ':' || st[i+1] != '"' {
			return "", false
		}
		name := st[:i]
		st = st[i+1:]
		i = 1
		for i < len(st) && st[i] != '"' {
			if st[i] == '\\' {
				i++
			}
			i++
		}
		if i >= len(st) {
			return "", false
		}
		quoted := st[:i+1]
		st = st[i+1:]
		if name == key {
			value, err := strconv.Unquote(quoted)
			if err != nil {
				return "", false
			}
			return value, true
		}
	}
	return "", false
}

// receiverType names the type a method is declared on, pointer or not.
func receiverType(expr ast.Expr) string { return baseTypeName(expr) }

// baseTypeName strips pointers, slices, and package qualifiers down to the type
// name this package would index it under.
func baseTypeName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.StarExpr:
		return baseTypeName(e.X)
	case *ast.ArrayType:
		return baseTypeName(e.Elt)
	case *ast.IndexExpr:
		return baseTypeName(e.X)
	case *ast.SelectorExpr:
		return e.Sel.Name
	}
	return ""
}

func exprName(expr ast.Expr) string {
	if name := baseTypeName(expr); name != "" {
		return name
	}
	return fmt.Sprintf("%T", expr)
}

// normalizePath collapses a spec path's named placeholders to "{}", so
// /api/v2/tags/{tag_id} and the SDK's "/tags/" + url.PathEscape(id) compare.
func normalizePath(path string) string {
	var b strings.Builder
	depth := 0
	for _, r := range path {
		switch {
		case r == '{':
			depth++
			if depth == 1 {
				b.WriteString("{}")
			}
		case r == '}':
			if depth > 0 {
				depth--
			}
		case depth == 0:
			b.WriteRune(r)
		}
	}
	return b.String()
}
