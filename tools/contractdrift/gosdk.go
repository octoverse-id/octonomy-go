package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"unicode"
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
//   - the Code* constants, which are declarations and nothing else;
//   - whether a method an inventory row names exists at all; and
//   - the JSON tag on each response model field, next to the field's own name.
//
// None of those follows a value through the program, which is why none of them
// can quietly answer the wrong question.
//
// The third is here because a JSON round trip structurally cannot see it. Swap
// the tags on two fields and the SAME tags do the decoding and the re-encoding,
// so the bytes that come back are identical while the caller reads the server's
// name out of Tag.Slug. Only the declaration knows that the field called Name is
// meant to carry the property called `name`.

// SDKPackage is the parsed SDK package.
type SDKPackage struct {
	Root string

	fset    *token.FileSet
	files   map[string]*ast.File // by base file name
	methods map[string]*sdkMethod
	structs map[string]*ast.StructType
	consts  map[string]string // identifier -> its string value
}

// sdkMethod is a method declaration. Its file is kept for error text only: a
// working method moving between source files is not drift, and the inventory
// stopped asserting where one lives.
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
		structs: map[string]*ast.StructType{},
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
				if typeSpec, ok := spec.(*ast.TypeSpec); ok {
					if st, ok := typeSpec.Type.(*ast.StructType); ok {
						p.structs[typeSpec.Name.Name] = st
					}
					continue
				}
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

// ModelFields returns a struct's exported fields, keyed by the JSON name each one
// decodes from, with the Go field name as the value. Embedded structs are
// flattened the way encoding/json flattens them.
func (p *SDKPackage) ModelFields(typeName string) (map[string]string, bool) {
	st, ok := p.structs[typeName]
	if !ok {
		return nil, false
	}
	out := map[string]string{}
	p.collectFields(st, out, map[string]bool{typeName: true})
	return out, true
}

func (p *SDKPackage) collectFields(st *ast.StructType, out map[string]string, seen map[string]bool) {
	for _, field := range st.Fields.List {
		tag := ""
		if field.Tag != nil {
			if raw, ok := stringLit(field.Tag); ok {
				tag, _, _ = strings.Cut(reflect.StructTag(raw).Get("json"), ",")
			}
		}
		if len(field.Names) == 0 {
			embedded := baseTypeName(field.Type)
			if tag == "" && !seen[embedded] {
				seen[embedded] = true
				if inner, ok := p.structs[embedded]; ok {
					p.collectFields(inner, out, seen)
				}
			}
			continue
		}
		name := field.Names[0]
		if !name.IsExported() || tag == "-" {
			continue
		}
		if tag == "" {
			tag = name.Name
		}
		out[tag] = name.Name
	}
}

// SnakeCase renders a Go field name the way this SDK's JSON tags spell it:
// TagID -> tag_id, UsageCount -> usage_count, ID -> id.
func SnakeCase(name string) string {
	var b strings.Builder
	runes := []rune(name)
	for i, r := range runes {
		if unicode.IsUpper(r) {
			prevLower := i > 0 && !unicode.IsUpper(runes[i-1])
			nextLower := i+1 < len(runes) && !unicode.IsUpper(runes[i+1])
			if i > 0 && (prevLower || nextLower) {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
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
