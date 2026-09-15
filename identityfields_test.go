package octonomy

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"
)

// --- the source-level guard over identityFields() -------------------------------

// transportGenerics are the two helpers whose type argument IS a response type.
var transportGenerics = map[string]bool{"doData": true, "doList": true}

// compositeTypes are the response types with no row identity of their own, which
// require their KEYS in UnmarshalJSON instead. The value says whether the
// composite also delivers a resource and therefore carries identityFields too.
//
// This is declared rather than inferred, and that is the point. An earlier
// revision let ANY UnmarshalJSON excuse a type from identityFields, so a
// resource that grew a custom decoder for an unrelated reason would have been
// excused from the check that matters -- which is the defect this guard exists
// to prevent, wearing the guard's own clothes. A composite is a fact about the
// contract, so it is written down and verified, not guessed from a method set.
var compositeTypes = map[string]bool{
	"BulkAssignResult":      false,
	"BulkRemoveResult":      false,
	"ResourceReplaceResult": false,
	"TagResolution":         true, // the tag it exists to deliver is a resource
}

// Every response type must be able to refuse a payload that decoded to nothing.
//
// `requireIdentity` (transport.go) type-asserts, and returns nil for a model that
// does not implement the interface:
//
//	resource, ok := v.(identifiedResource)
//	if !ok {
//		return nil
//	}
//
// So a model that skips identityFields() is not checked, and is not checked
// SILENTLY -- `{"data": {"id": null}}` or a renamed id then decodes to a
// zero-valued resource behind a nil error, which is #40.
//
// RECEIVER KIND IS PART OF THE RULE, not a detail. doData and doList call
// `requireIdentity(out, …)` with a VALUE of T, so the method set consulted is
// T's, and a pointer-receiver identityFields is not in it -- such a model
// compiles, reads correctly, and is skipped at runtime exactly as if the method
// were absent. json.Unmarshal is handed `&out`, so UnmarshalJSON is the mirror
// image and must be on the POINTER. An earlier revision of this guard credited
// either receiver for either method, which certified a shape the runtime ignores.
//
// This is the second of the three requirements #73 found unguarded (#76). It
// carries no build tag for the same reason readprobes_test.go does not: it has to
// run in the pull request that adds a resource.
//
// WHERE THIS CHECK ENDS, since a check whose edge nobody knows is worse than none:
//
//   - It proves a mechanism EXISTS with the right shape, never that it is RIGHT.
//     A model naming the wrong field in identityFields satisfies this and still
//     decodes a zero-valued row; a composite whose UnmarshalJSON requires none of
//     its keys satisfies it too. Both are a reader's job.
//   - It does not reach NESTED required resources. ResourceTag.Tag and
//     TagResolution.Tag count only where the contract marks them `required` AND
//     the route delivers them -- a fact about openapi-v2.yaml, not about Go
//     source, and the place the first draft of this rule got ResourceTag wrong.
//   - compositeTypes is a declared list. It is verified against the source in
//     both directions, but nothing proves a type listed there is genuinely a
//     composite rather than a resource someone wanted to excuse. It is an escape
//     hatch; it is just one that takes a reviewed edit rather than happening by
//     itself.
//   - It reads DIRECT methods and CANONICAL type spellings. A promoted method, a
//     type alias for a response type, or an alias for []byte or error is
//     rejected rather than resolved. That fails closed, no model uses those
//     shapes, and resolving them properly means go/types.
//   - It matches on BARE type names within this one package. That is sound for
//     package-level types, which Go makes unique, and type-parameter shadowing
//     is resolved above. A type argument from ANOTHER package arrives as a
//     qualified pkg.Type -- not a plain identifier -- so it is reported as
//     unreadable rather than silently mismatched.
func TestEveryResponseTypeCanRefuseAnEmptyDecode(t *testing.T) {
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

	methods := declaredMethods(pkg.Files)
	found, unresolved := responseTypes(pkg.Files)

	for _, where := range unresolved {
		t.Errorf("%s hands a transport helper a type argument this guard cannot resolve to a "+
			"concrete type -- a generic wrapper, or a composite type expression. The instantiation "+
			"it is really called with is invisible here, so the response type behind it would be "+
			"checked by nothing. Call doData/doList with a named type, or teach the guard the shape",
			where)
	}

	for _, name := range sortedKeys(found) {
		alsoResource, isComposite := compositeTypes[name]
		if isComposite {
			if why := hasUnmarshalJSON(name, methods); why != "" {
				t.Errorf("%s is declared a composite and %s. A composite has no row identity and "+
					"must refuse a payload missing its keys, in UnmarshalJSON on the POINTER -- "+
					"json.Unmarshal is handed &out", name, why)
			}
			if !alsoResource {
				continue
			}
		}
		if why := hasIdentityFields(name, methods); why != "" {
			kind := "is decoded from a response"
			if isComposite {
				kind = "is a composite that also delivers a resource"
			}
			t.Errorf("%s %s (%s) and %s. requireIdentity type-asserts on a VALUE of the type, so "+
				"the method must be on the value receiver and return []identityField; anything "+
				"else is skipped in silence and a payload that decoded to nothing returns a zero "+
				"value with a nil error (#40)", name, kind, found[name], why)
		}
	}

	for _, name := range sortedKeys(compositeTypes) {
		if _, live := found[name]; !live {
			t.Errorf("compositeTypes lists %q, which is not a response type. Drop it -- an entry "+
				"for a type that does not exist would also excuse a future one that takes the name",
				name)
		}
	}

	const known = 11
	if len(found) < known {
		t.Errorf("found %d response type(s), want at least the %d that exist -- the guard is not "+
			"reading the source. Found: %v", len(found), known, sortedKeys(found))
	}
}

// hasIdentityFields returns "" if the type carries the method in the shape the
// runtime consults, or a description of what is wrong.
func hasIdentityFields(name string, methods methodSet) string {
	fn, ok := methods[name+".identityFields"]
	if !ok {
		return "has no identityFields() method"
	}
	if fn.pointer {
		return "declares identityFields() on the POINTER receiver, which is not in the value's " +
			"method set -- requireIdentity is handed a value, so this method is never seen"
	}
	if n := len(paramNames(fn.decl.Type.Params)); n != 0 {
		return "declares identityFields() taking arguments, so it does not satisfy identifiedResource"
	}
	if got := resultTypes(fn.decl.Type); len(got) != 1 || got[0] != "[]identityField" {
		return "declares identityFields() returning " + strings.Join(got, ", ") +
			", not []identityField, so it does not satisfy identifiedResource"
	}
	return ""
}

// hasUnmarshalJSON returns "" if the type carries the decoder in the shape
// encoding/json consults, or a description of what is wrong.
func hasUnmarshalJSON(name string, methods methodSet) string {
	fn, ok := methods[name+".UnmarshalJSON"]
	if !ok {
		return "has no UnmarshalJSON method"
	}
	if !fn.pointer {
		return "declares UnmarshalJSON on the VALUE receiver. json.Unmarshal is handed &out, and " +
			"*T's method set does include T's value methods -- so this IS called, on a copy, and " +
			"cannot populate the value being decoded"
	}
	if got := paramTypes(fn.decl.Type); len(got) != 1 || got[0] != "[]byte" {
		return "declares UnmarshalJSON taking " + strings.Join(got, ", ") + ", not []byte"
	}
	if got := resultTypes(fn.decl.Type); len(got) != 1 || got[0] != "error" {
		return "declares UnmarshalJSON returning " + strings.Join(got, ", ") + ", not error"
	}
	return ""
}

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
			typ := ast.Unparen(fn.Recv.List[0].Type)
			pointer := false
			if star, ok := typ.(*ast.StarExpr); ok {
				pointer, typ = true, ast.Unparen(star.X)
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

// responseTypes collects every type handed to doData or doList, mapped to where
// it was instantiated, plus the call sites whose type argument is not a concrete
// name this guard can resolve.
//
// Type parameters are tracked per DECLARATION rather than per file: a type
// parameter named in one function said nothing about a concrete type of the same
// name instantiated in another, and treating the file as one scope let an
// unrelated `helper[Widget any]` hide a real `doData[Widget]`.
func responseTypes(files map[string]*ast.File) (map[string]string, []string) {
	found := map[string]string{}
	var unresolved []string

	for path, file := range files {
		for _, decl := range file.Decls {
			// A declaration's own type parameters, if it has any. A GenDecl --
			// `var decodeWidget = doData[Widget]` at package level -- has none,
			// and is walked for the same reason: it instantiates the helper.
			var scope ast.Node = decl
			params := map[string]bool{}
			if fn, ok := decl.(*ast.FuncDecl); ok {
				params = typeParamNames(fn)
			}
			ast.Inspect(scope, func(n ast.Node) bool {
				// Every INSTANTIATION of a transport helper, wherever it appears
				// -- not only one sitting in callee position. `decode :=
				// doData[Widget]` and a package-level `var d = doList[Widget]`
				// instantiate it just as much as calling it does, and matching
				// only `doData[Widget](…)` lost the type entirely: not collected,
				// not reported, and the floor cannot see a twelfth type that
				// never arrived.
				var args []ast.Expr
				switch node := n.(type) {
				case *ast.IndexExpr:
					if !isTransportGeneric(node.X) {
						return true
					}
					args = []ast.Expr{node.Index}
				case *ast.IndexListExpr:
					if !isTransportGeneric(node.X) {
						return true
					}
					args = node.Indices
				default:
					return true
				}
				for _, arg := range args {
					ident, ok := ast.Unparen(arg).(*ast.Ident)
					if !ok {
						unresolved = append(unresolved, path)
						continue
					}
					// A transport helper handed the ENCLOSING function's type
					// parameter is a generic wrapper. The concrete type reaches
					// it from the wrapper's own call sites, which this guard does
					// not follow -- so it is reported rather than skipped. An
					// earlier revision skipped it, and a wrapper was then enough
					// to drop a response type out of the guard entirely.
					if params[ident.Name] {
						unresolved = append(unresolved, path+" (a wrapper generic over "+ident.Name+")")
						continue
					}
					if _, seen := found[ident.Name]; !seen {
						found[ident.Name] = path
					}
				}
				return true
			})
		}
	}
	return found, unresolved
}

// typeParamNames collects the type-parameter names in scope for one
// declaration -- the function's own, AND the RECEIVER's.
//
// The receiver half is not a formality. Go lets a generic method's receiver
// declare its own type-parameter names, and they shadow package-level types:
//
//	func (s loader[Tag]) Get(ctx context.Context) (*Tag, error) {
//		return doData[Tag](ctx, s.client, http.MethodGet, "/tags/1", nil, nil)
//	}
//
// That `Tag` is a type parameter, not the package's Tag. Reading only
// fn.Type.TypeParams recorded it as the concrete type, found the real
// Tag.identityFields, and passed -- while loader[string].Get actually decodes a
// string, which requireIdentity skips in silence. It is the one collision in
// this guard that fails OPEN, which is why it is resolved rather than noted.
func typeParamNames(fn *ast.FuncDecl) map[string]bool {
	out := map[string]bool{}
	if fn.Type != nil && fn.Type.TypeParams != nil {
		for _, param := range fn.Type.TypeParams.List {
			for _, name := range param.Names {
				out[name.Name] = true
			}
		}
	}
	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		for _, name := range receiverTypeParams(fn.Recv.List[0].Type) {
			out[name] = true
		}
	}
	return out
}

// receiverTypeParams reads the names a generic receiver declares, through a
// pointer and through parentheses.
func receiverTypeParams(expr ast.Expr) []string {
	var out []string
	switch recv := ast.Unparen(expr).(type) {
	case *ast.StarExpr:
		return receiverTypeParams(recv.X)
	case *ast.IndexExpr:
		if ident, ok := ast.Unparen(recv.Index).(*ast.Ident); ok {
			out = append(out, ident.Name)
		}
	case *ast.IndexListExpr:
		for _, index := range recv.Indices {
			if ident, ok := ast.Unparen(index).(*ast.Ident); ok {
				out = append(out, ident.Name)
			}
		}
	}
	return out
}

func isTransportGeneric(fun ast.Expr) bool {
	ident, ok := ast.Unparen(fun).(*ast.Ident)
	return ok && transportGenerics[ident.Name]
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

// exprString renders the type expressions this guard has to compare. Anything
// else renders as "?" and fails the comparison, which is the safe direction.
func exprString(expr ast.Expr) string {
	switch e := ast.Unparen(expr).(type) {
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
	}
	return "?"
}

// --- fixtures: the guard's own behaviour ---------------------------------------

// On a correct tree the guard passes whether or not it works, so these drive it
// over synthetic source. Each case is a shape it has to get right, and the ones
// that matter most are where a wrong answer is silent: a response type it fails
// to collect, and a method it credits that the runtime would never call.

func parseFixture(t *testing.T, src string) (map[string]*ast.File, methodSet) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", src, 0)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	files := map[string]*ast.File{"fixture.go": file}
	return files, declaredMethods(files)
}

func TestResponseTypesCollectsWhatTheTransportDecodes(t *testing.T) {
	const header = "package octonomy\n"

	for _, tc := range []struct {
		name       string
		src        string
		want       []string
		unresolved int
	}{
		{
			name: "a doData type argument",
			src:  `func (s *S) Get(ctx context.Context) error { return doData[Tag](ctx, s.client, http.MethodGet, "/t", nil, nil) }`,
			want: []string{"Tag"},
		},
		{
			name: "a doList type argument",
			src:  `func (s *S) List(ctx context.Context) error { return doList[AuditLog](ctx, s.client, http.MethodGet, "/a", nil) }`,
			want: []string{"AuditLog"},
		},
		{
			// A generic WRAPPER. The concrete type reaches doData from the
			// wrapper's own call sites, which this guard does not follow -- so it
			// must be reported, not skipped. Skipping it was enough to drop a
			// response type out of the guard entirely.
			name:       "a transport call handed a type parameter",
			src:        `func fetch[T any](ctx context.Context) error { return doData[T](ctx, nil, "GET", "/x", nil, nil) }`,
			unresolved: 1,
		},
		{
			// Scope. A type parameter in ONE declaration says nothing about a
			// concrete type of the same name instantiated in another; treating
			// the file as one scope let an unrelated helper hide a real type.
			name: "a type parameter does not shadow a concrete type elsewhere",
			src: `func helper[Widget any](ctx context.Context) error { return nil }
			func (s *S) Get(ctx context.Context) error { return doData[Widget](ctx, s.client, http.MethodGet, "/w", nil, nil) }`,
			want: []string{"Widget"},
		},
		{
			// An instantiation that is never in callee position. Matching only
			// `doData[Widget](…)` lost this one silently, and the floor cannot
			// notice a twelfth type that never arrived.
			name: "a transport helper bound to a local variable",
			src:  `func (s *S) Get(ctx context.Context) error { decode := doData[Widget]; return decode(ctx) }`,
			want: []string{"Widget"},
		},
		{
			name: "a transport helper bound at package level",
			src:  `var decodeWidget = doData[Widget]`,
			want: []string{"Widget"},
		},
		{
			// A generic method's RECEIVER may declare type-parameter names, and
			// they shadow package-level types. Reading only the function's own
			// type parameters recorded this `Tag` as the concrete package type,
			// found the real Tag.identityFields and passed -- while
			// loader[string].Get decodes a string that requireIdentity skips.
			// The one collision in this guard that failed OPEN.
			name:       "a receiver type parameter shadowing a real type",
			src:        `func (s loader[Tag]) Get(ctx context.Context) (*Tag, error) { return doData[Tag](ctx, s.client, http.MethodGet, "/t", nil, nil) }`,
			unresolved: 1,
		},
		{
			name:       "the same through a pointer receiver",
			src:        `func (s *loader[Tag]) Get(ctx context.Context) (*Tag, error) { return doData[Tag](ctx, s.client, http.MethodGet, "/t", nil, nil) }`,
			unresolved: 1,
		},
		{
			name:       "and with several receiver type parameters",
			src:        `func (s loader[K, Tag]) List(ctx context.Context) (*Tag, error) { return doList[Tag](ctx, s.client, http.MethodGet, "/t", nil) }`,
			unresolved: 1,
		},
		{
			name: "parentheses do not hide the call",
			src:  `func (s *S) Get(ctx context.Context) error { return (doData[Vocabulary])(ctx, s.client, http.MethodGet, "/v", nil, nil) }`,
			want: []string{"Vocabulary"},
		},
		{
			name:       "a type argument that is not a plain name",
			src:        `func (s *S) Get(ctx context.Context) error { return doData[List[Tag]](ctx, s.client, http.MethodGet, "/x", nil, nil) }`,
			unresolved: 1,
		},
		{
			name: "an unrelated generic call",
			src:  `func (s *S) Get(ctx context.Context) error { return decodeMetadata[Shipping](ctx) }`,
			want: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files, _ := parseFixture(t, header+tc.src)
			found, unresolved := responseTypes(files)
			if len(unresolved) != tc.unresolved {
				t.Errorf("unresolved = %d, want %d (%v)", len(unresolved), tc.unresolved, unresolved)
			}
			got := sortedKeys(found)
			if len(got) != len(tc.want) {
				t.Fatalf("collected %v, want %v", got, tc.want)
			}
			for i, name := range tc.want {
				if got[i] != name {
					t.Errorf("collected %v, want %v", got, tc.want)
					break
				}
			}
		})
	}
}

// The runtime consults a VALUE's method set for identityFields and a POINTER's
// for UnmarshalJSON. A guard that credited either receiver for either method
// certified shapes that are never called -- which is #40 reintroduced by the
// check written to prevent it.
func TestIdentityMechanismsMatchWhatTheRuntimeCalls(t *testing.T) {
	const header = "package octonomy\n"

	for _, tc := range []struct {
		name, typ, src string
		wantIdentityOK bool
		wantDecoderOK  bool
	}{
		{
			name: "a value receiver is what requireIdentity sees", typ: "Tag",
			src:            `func (t Tag) identityFields() []identityField { return nil }`,
			wantIdentityOK: true,
		},
		{
			// requireIdentity is handed a value, so this method is never in the
			// method set it asserts against. It compiles and reads correctly.
			name: "a pointer receiver is skipped at runtime", typ: "Tag",
			src:            `func (t *Tag) identityFields() []identityField { return nil }`,
			wantIdentityOK: false,
		},
		{
			name: "the wrong return type does not satisfy the interface", typ: "Widget",
			src:            `func (w Widget) identityFields() string { return "" }`,
			wantIdentityOK: false,
		},
		{
			name: "a method taking arguments does not satisfy it either", typ: "Widget",
			src:            `func (w Widget) identityFields(deep bool) []identityField { return nil }`,
			wantIdentityOK: false,
		},
		{
			name: "a pointer receiver is what json.Unmarshal calls", typ: "BulkAssignResult",
			src:           `func (r *BulkAssignResult) UnmarshalJSON(data []byte) error { return nil }`,
			wantDecoderOK: true,
		},
		{
			// *T's method set DOES include T's value methods, so json.Unmarshal
			// calls this one -- on a copy. It cannot populate the value being
			// decoded, which is the same silent nothing as not having it.
			name: "a value-receiver decoder cannot populate the value", typ: "BulkAssignResult",
			src:           `func (r BulkAssignResult) UnmarshalJSON(data []byte) error { return nil }`,
			wantDecoderOK: false,
		},
		{
			name: "a decoder with the wrong result is not json.Unmarshaler", typ: "Result",
			src:           `func (r *Result) UnmarshalJSON(data []byte) bool { return true }`,
			wantDecoderOK: false,
		},
		{
			name: "a method of the right name on another type does not count", typ: "Widget",
			src: `func (t Tag) identityFields() []identityField { return nil }`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, methods := parseFixture(t, header+tc.src)
			if got := hasIdentityFields(tc.typ, methods) == ""; got != tc.wantIdentityOK {
				t.Errorf("hasIdentityFields(%s) ok = %v, want %v (%s)",
					tc.typ, got, tc.wantIdentityOK, hasIdentityFields(tc.typ, methods))
			}
			if got := hasUnmarshalJSON(tc.typ, methods) == ""; got != tc.wantDecoderOK {
				t.Errorf("hasUnmarshalJSON(%s) ok = %v, want %v (%s)",
					tc.typ, got, tc.wantDecoderOK, hasUnmarshalJSON(tc.typ, methods))
			}
		})
	}
}
