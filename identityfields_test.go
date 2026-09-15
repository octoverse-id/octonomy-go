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
// Every shape this SDK decodes from the server arrives through one of them.
var transportGenerics = map[string]bool{"doData": true, "doList": true}

// identityExemptions names a response type that carries neither mechanism, with
// the reason. It is empty, and the guard is written so that staying empty is the
// normal state: a response type is either a resource with a row identity or a
// composite that requires its keys, and there is no third kind.
var identityExemptions = map[string]string{}

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
// zero-valued resource behind a nil error, which is #40, the same silent-zero
// family as #32. Omitting the method does not corrupt a valid payload; it removes
// the check that catches an invalid one.
//
// The rule has two halves (AGENTS.md), and both are decidable from the source:
//
//	a RESOURCE      implements identityFields(), naming the field that
//	                identifies its row
//	a COMPOSITE     has no row identity of its own and requires its keys in
//	                UnmarshalJSON instead
//
// TagResolution is both, since the tag it exists to deliver is a resource. So the
// invariant is: every type handed to doData or doList does one or the other, and
// nothing does neither.
//
// This is the second of the three requirements #73 found unguarded (#76). It
// carries no build tag for the same reason readprobes_test.go does not: it has to
// run in the pull request that adds a resource.
//
// WHERE THIS CHECK ENDS, since a check whose edge nobody knows is worse than none:
//
//   - It proves a mechanism EXISTS, never that it is right. A model returning
//     identityFields() for the wrong field satisfies this and still decodes a
//     zero-valued row; a composite whose UnmarshalJSON requires none of its keys
//     satisfies it too. Both are a reader's job.
//   - It does not reach NESTED required resources. ResourceTag.Tag and
//     TagResolution.Tag count only where the contract marks them `required` AND
//     the route delivers them -- a fact about openapi-v2.yaml, not about Go
//     source, and the place the first draft of this rule got ResourceTag wrong.
//     Deriving the expected set from contract-coverage.yaml is what would close
//     that, and is the same move #77 names for the smoke assertion.
//   - It sees only types handed DIRECTLY to doData or doList as a plain name. A
//     future helper wrapping them, or a composite type argument, is reported as
//     unreadable rather than assumed fine.
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

	index := indexFuncs(pkg.Files)
	found, unresolved := responseTypes(pkg.Files)

	// A type argument the parser could not read is not evidence of anything, so
	// it is reported rather than skipped.
	for _, where := range unresolved {
		t.Errorf("%s instantiates a transport helper with a type argument this guard cannot "+
			"read as a plain type name. Either simplify the call or teach the guard the shape -- "+
			"an unread response type is one nothing checks", where)
	}

	for _, name := range sortedKeys(found) {
		if mechanism(name, index) != "" {
			continue
		}
		if _, exempt := identityExemptions[name]; exempt {
			continue
		}
		t.Errorf("%s is decoded from a response (%s) and has neither an identityFields() method "+
			"nor an UnmarshalJSON. requireIdentity type-asserts, so this type is skipped in "+
			"silence and a payload that decoded to nothing returns a zero value with a nil error "+
			"(#40). A resource names its row identity; a composite requires its keys in "+
			"UnmarshalJSON", name, found[name])
	}

	for _, name := range sortedKeys(identityExemptions) {
		if _, live := found[name]; !live {
			t.Errorf("identityExemptions lists %q, which is not a response type. Drop it -- an "+
				"exemption for a type that does not exist would also excuse a future one that "+
				"takes the name", name)
		}
		if strings.TrimSpace(identityExemptions[name]) == "" {
			t.Errorf("identityExemptions[%q] has a blank reason", name)
		}
		if mechanism(name, index) != "" {
			t.Errorf("identityExemptions lists %q, which does carry a mechanism. Drop the entry "+
				"rather than leave one that would excuse its removal", name)
		}
	}

	// The floor optional_test.go sets: a parser that silently stopped finding
	// instantiations would satisfy every loop above by finding nothing.
	const known = 11
	if len(found) < known {
		t.Errorf("found %d response type(s), want at least the %d that exist -- the guard is not "+
			"reading the source. Found: %v", len(found), known, sortedKeys(found))
	}
}

// responseTypes collects every type handed to doData or doList, mapped to where
// it was instantiated, plus the call sites whose type argument could not be read.
func responseTypes(files map[string]*ast.File) (map[string]string, []string) {
	found := map[string]string{}
	var unresolved []string

	for path, file := range files {
		// A type PARAMETER is not a response type. doData's own body refers to
		// T, and so would any future generic helper built on it.
		params := map[string]bool{}
		ast.Inspect(file, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Type == nil || fn.Type.TypeParams == nil {
				return true
			}
			for _, param := range fn.Type.TypeParams.List {
				for _, name := range param.Names {
					params[name.Name] = true
				}
			}
			return true
		})

		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			var args []ast.Expr
			switch fun := ast.Unparen(call.Fun).(type) {
			case *ast.IndexExpr:
				if !isTransportGeneric(fun.X) {
					return true
				}
				args = []ast.Expr{fun.Index}
			case *ast.IndexListExpr:
				if !isTransportGeneric(fun.X) {
					return true
				}
				args = fun.Indices
			default:
				return true
			}
			for _, arg := range args {
				ident, ok := ast.Unparen(arg).(*ast.Ident)
				if !ok {
					unresolved = append(unresolved, path)
					continue
				}
				if params[ident.Name] {
					continue
				}
				if _, seen := found[ident.Name]; !seen {
					found[ident.Name] = path
				}
			}
			return true
		})
	}
	return found, unresolved
}

func isTransportGeneric(fun ast.Expr) bool {
	ident, ok := ast.Unparen(fun).(*ast.Ident)
	return ok && transportGenerics[ident.Name]
}

// mechanism names how a response type refuses an empty decode, or "" if it does
// neither. A method on the value and on the pointer both count -- the transport
// decodes into a *T and asserts on it, so either receiver satisfies the
// interface.
func mechanism(name string, index funcIndex) string {
	for _, method := range []string{"identityFields", "UnmarshalJSON"} {
		if _, ok := index[name+"."+method]; ok {
			return method
		}
	}
	return ""
}

// --- fixtures: the guard's own behaviour ---------------------------------------

// On a correct tree the guard passes whether or not it works, so these drive it
// over synthetic source. Each case is a shape it has to get right, and the two
// that matter most are the ones where a wrong answer is silent: a response type
// it fails to collect, and a mechanism it fails to see.

func parseFixture(t *testing.T, src string) (map[string]*ast.File, funcIndex) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", src, 0)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	files := map[string]*ast.File{"fixture.go": file}
	return files, indexFuncs(files)
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
			// A transport helper handed its OWN type parameter. Collecting T
			// would demand a mechanism from a name that is not a type at all,
			// and the guard would fail on correct code forever.
			name: "a type parameter is not a response type",
			src:  `func doData[T any](ctx context.Context) error { return doList[T](ctx) }`,
			want: nil,
		},
		{
			name: "parentheses do not hide the call",
			src:  `func (s *S) Get(ctx context.Context) error { return (doData[Vocabulary])(ctx, s.client, http.MethodGet, "/v", nil, nil) }`,
			want: []string{"Vocabulary"},
		},
		{
			// A composite type argument is not a plain name, so the guard cannot
			// say what it is -- and says so rather than skipping it.
			name:       "a type argument that is not a plain name",
			src:        `func (s *S) Get(ctx context.Context) error { return doData[List[Tag]](ctx, s.client, http.MethodGet, "/x", nil, nil) }`,
			unresolved: 1,
		},
		{
			// A generic call to something else entirely must not be mistaken for
			// a response decode.
			name: "an unrelated generic call",
			src:  `func (s *S) Get(ctx context.Context) error { return decodeMetadata[Shipping](ctx) }`,
			want: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files, _ := parseFixture(t, header+tc.src)
			found, unresolved := responseTypes(files)
			if len(unresolved) != tc.unresolved {
				t.Errorf("unresolved = %d, want %d", len(unresolved), tc.unresolved)
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

func TestMechanismSeesBothHalvesOfTheRule(t *testing.T) {
	const header = "package octonomy\n"

	for _, tc := range []struct {
		name, typ, want, src string
	}{
		{
			name: "a resource names its row identity", typ: "Tag", want: "identityFields",
			src: `func (t Tag) identityFields() []identityField { return nil }`,
		},
		{
			// The transport decodes into a *T and asserts on it, so a pointer
			// receiver satisfies the interface exactly as a value one does.
			name: "a pointer receiver counts", typ: "Tag", want: "identityFields",
			src: `func (t *Tag) identityFields() []identityField { return nil }`,
		},
		{
			name: "a composite requires its keys instead", typ: "BulkAssignResult", want: "UnmarshalJSON",
			src: `func (r *BulkAssignResult) UnmarshalJSON(data []byte) error { return nil }`,
		},
		{
			name: "neither is the defect this guards", typ: "Widget", want: "",
			src: `func (w Widget) Name() string { return "" }`,
		},
		{
			// A method of the right name on the WRONG type must not satisfy this
			// type -- that would excuse the one that actually decodes.
			name: "a mechanism on another type does not count", typ: "Widget", want: "",
			src: `func (t Tag) identityFields() []identityField { return nil }`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, index := parseFixture(t, header+tc.src)
			if got := mechanism(tc.typ, index); got != tc.want {
				t.Errorf("mechanism(%s) = %q, want %q", tc.typ, got, tc.want)
			}
		})
	}
}
