package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The two things about the SDK worth reading out of the source, and nothing else.
//
// An earlier version of this file was 700 lines and tried to answer the real
// questions statically: which route does this method request, which query
// parameters does its params struct build, which type does it decode into. It
// grew a special case for every Go shape it met -- generic instantiation, path
// helpers, embedded structs, one level of delegation, constant resolution -- and
// a review pass still reproduced three confident green answers against it: a
// method that stopped passing `params.query()`, a method that branched between
// two private helpers, a schema property whose type changed. Inferring control
// flow from an AST is the wrong tool for "what does this client do", and every
// repair made the wrong tool bigger.
//
// conformance.go answers those questions by calling the client and reading the
// request off the wire. What is left here is the part where the source really is
// the source of truth:
//
//   - the Code* constants, which are declarations and nothing else; and
//   - whether a method an inventory row names exists at all.
//
// Neither involves following a value through the program, which is why neither
// can quietly answer the wrong question.

// SDKPackage is the parsed SDK package.
type SDKPackage struct {
	Root string

	fset    *token.FileSet
	files   map[string]*ast.File // by base file name
	methods map[string]*sdkMethod
	consts  map[string]string // identifier -> its string value
}

type sdkMethod struct {
	File string
	Recv string
	Name string
}

// LoadSDKPackage parses every non-test .go file in the SDK package directory.
//
// ParseFile per entry rather than parser.ParseDir, which is deprecated for being
// blind to build tags. This reader is blind to them too -- but it reads one flat
// directory of unconstrained sources, and the one build-tagged file in the tree is
// integration_test.go, excluded here with every other test.
func LoadSDKPackage(root string) (*SDKPackage, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}

	p := &SDKPackage{
		Root:    root,
		fset:    token.NewFileSet(),
		files:   map[string]*ast.File{},
		methods: map[string]*sdkMethod{},
		consts:  map[string]string{},
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(p.fset, filepath.Join(root, name), nil, 0)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		p.files[name] = file
		p.index(name, file)
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
			recv := baseTypeName(d.Recv.List[0].Type)
			if recv == "" {
				continue
			}
			p.methods[recv+"."+d.Name.Name] = &sdkMethod{File: file, Recv: recv, Name: d.Name.Name}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				// Typed and untyped alike: `CodeFoo = "foo"` and
				// `CodeFoo ErrorCode = "foo"` are the same declaration to a reader
				// and must be the same declaration here. A pattern written for one
				// of the two stops matching the other in silence, which is how the
				// regexp version of this failed review.
				for i, name := range value.Names {
					if i >= len(value.Values) {
						continue
					}
					if lit, ok := stringLit(value.Values[i]); ok {
						p.consts[name.Name] = lit
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

// baseTypeName strips pointers down to the type name a method is declared on.
func baseTypeName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.StarExpr:
		return baseTypeName(e.X)
	case *ast.IndexExpr:
		return baseTypeName(e.X)
	case *ast.SelectorExpr:
		return e.Sel.Name
	}
	return ""
}
