package main

import (
	"errors"
	"fmt"
	"go/ast"
	"go/build"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// A module's exported API, as one line per FEATURE: every exported constant
// with its type and value, every variable with its type, every function with
// its signature, every type with its kind, every exported struct field with its
// type and struct tag, and every method in the method sets of T and *T. The
// lines are Go's own api/go1.*.txt in spirit -- a set a caller's code could
// depend on, written so that two of them can be compared as strings.
//
// What a line does NOT carry is what a caller cannot depend on: parameter
// names, unexported declarations, the order of declarations, and the spelling
// of a type rather than its identity. `any` and `interface{}` are one type, as
// are `byte` and `uint8` and an alias and what it names, so each prints as the
// same text; a difference in a line is a difference a caller can see.

// api is one tree's exported surface, keyed by package path relative to the
// module root ("." for the root package).
type api struct {
	modulePath string
	packages   map[string][]string
}

// shared holds the one FileSet and the one standard-library importer every
// load in a run uses. The importer type-checks the standard library from the
// RUNNING toolchain's GOROOT and caches it, so the three trees a check reads
// pay for net/http once rather than three times.
type shared struct {
	fset *token.FileSet
	std  types.Importer
}

func newShared() *shared {
	fset := token.NewFileSet()
	return &shared{fset: fset, std: importer.ForCompiler(fset, "source", nil)}
}

// loadAPI reads the module rooted at dir: its go.mod for the module path, and
// every importable package under it.
//
// Importable means what a caller of the module can import: a directory with
// buildable non-test Go files whose package is not `main`, outside any
// `internal`, `testdata` or `vendor` directory, outside a directory whose name
// starts with `.` or `_` (the go command ignores those), and outside a nested
// module -- a directory below dir with a go.mod of its own is another module,
// as tools/contractdrift and this tool are.
func (s *shared) loadAPI(dir string) (*api, error) {
	modPath, err := readModulePath(filepath.Join(dir, "go.mod"))
	if err != nil {
		return nil, err
	}
	l := &loader{shared: s, root: dir, modulePath: modPath, done: map[string]*types.Package{}, busy: map[string]bool{}}
	rels, err := importableDirs(dir)
	if err != nil {
		return nil, err
	}
	out := &api{modulePath: modPath, packages: map[string][]string{}}
	for _, rel := range rels {
		pkg, err := l.load(rel)
		if err != nil {
			return nil, err
		}
		if pkg == nil {
			continue
		}
		features, err := featuresOf(pkg, rel)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", relDisplay(rel), err)
		}
		out.packages[rel] = features
	}
	if _, ok := out.packages["."]; !ok {
		return nil, fmt.Errorf("%s has no importable root package", dir)
	}
	return out, nil
}

func relDisplay(rel string) string {
	if rel == "." {
		return "the root package"
	}
	return "package " + rel
}

// readModulePath returns the module path a go.mod declares. It reads only the
// `module` directive, which is all a comparison of exported surfaces needs: the
// path decides how one package of the module imports another.
func readModulePath(gomod string) (string, error) {
	raw, err := os.ReadFile(gomod)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "module" {
			p, err := strconv.Unquote(fields[1])
			if err != nil {
				p = fields[1]
			}
			return p, nil
		}
	}
	return "", fmt.Errorf("%s declares no module path", gomod)
}

// importableDirs lists, relative to root and in sorted order, every directory a
// package could be imported from. Whether one holds a package is decided later,
// by go/build.
func importableDirs(root string) ([]string, error) {
	var rels []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if p != root {
			name := d.Name()
			if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") ||
				name == "testdata" || name == "vendor" || name == "internal" {
				return filepath.SkipDir
			}
			if _, err := os.Stat(filepath.Join(p, "go.mod")); err == nil {
				return filepath.SkipDir
			}
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rels = append(rels, filepath.ToSlash(rel))
		return nil
	})
	sort.Strings(rels)
	return rels, err
}

// loader type-checks the packages of one module, resolving an import of a
// package in the same module from the tree itself and everything else from the
// standard library. Anything else -- a third-party import -- fails the load:
// both lines are standard-library only, so one would be news.
type loader struct {
	*shared
	root       string
	modulePath string
	done       map[string]*types.Package
	busy       map[string]bool
}

// load type-checks the package in the directory rel, or returns nil when that
// directory holds none a caller could import.
func (l *loader) load(rel string) (*types.Package, error) {
	if pkg, ok := l.done[rel]; ok {
		return pkg, nil
	}
	if l.busy[rel] {
		return nil, fmt.Errorf("import cycle through %s", relDisplay(rel))
	}
	l.busy[rel] = true
	defer delete(l.busy, rel)

	abs := filepath.Join(l.root, filepath.FromSlash(rel))
	ctxt := build.Default
	ctxt.CgoEnabled = false
	bp, err := ctxt.ImportDir(abs, 0)
	var noGo *build.NoGoError
	switch {
	case errors.As(err, &noGo):
		l.done[rel] = nil
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("%s: %w", relDisplay(rel), err)
	}
	if bp.Name == "main" || len(bp.GoFiles) == 0 {
		l.done[rel] = nil
		return nil, nil
	}
	if len(bp.CgoFiles) != 0 {
		return nil, fmt.Errorf("%s has cgo files, which this tool does not read", relDisplay(rel))
	}
	var files []*ast.File
	for _, name := range bp.GoFiles {
		f, err := parser.ParseFile(l.fset, filepath.Join(abs, name), nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	importPath := l.modulePath
	if rel != "." {
		importPath = path.Join(l.modulePath, rel)
	}
	var firstErr error
	conf := types.Config{
		Importer: importerFunc(func(p string) (*types.Package, error) { return l.importPath(p) }),
		Error: func(err error) {
			if firstErr == nil {
				firstErr = err
			}
		},
	}
	pkg, _ := conf.Check(importPath, l.fset, files, nil)
	if firstErr != nil {
		return nil, fmt.Errorf("type-check %s: %w", relDisplay(rel), firstErr)
	}
	l.done[rel] = pkg
	return pkg, nil
}

func (l *loader) importPath(p string) (*types.Package, error) {
	if p == l.modulePath || strings.HasPrefix(p, l.modulePath+"/") {
		rel := "."
		if p != l.modulePath {
			rel = strings.TrimPrefix(p, l.modulePath+"/")
		}
		pkg, err := l.load(rel)
		if err != nil {
			return nil, err
		}
		if pkg == nil {
			return nil, fmt.Errorf("%s imports %s, which holds no importable package", l.modulePath, p)
		}
		return pkg, nil
	}
	return l.std.Import(p)
}

type importerFunc func(path string) (*types.Package, error)

func (f importerFunc) Import(path string) (*types.Package, error) { return f(path) }

// featuresOf lists one package's exported features, sorted. Lines of a package
// other than the root carry its path as a prefix, so the features of two
// packages can never be mistaken for each other.
func featuresOf(pkg *types.Package, rel string) ([]string, error) {
	e := &emitter{p: &printer{local: pkg}, rel: rel}
	scope := pkg.Scope()
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		if !obj.Exported() {
			continue
		}
		if err := e.object(obj); err != nil {
			return nil, err
		}
	}
	if err := e.p.err; err != nil {
		return nil, err
	}
	if err := e.reachedUnexported(); err != nil {
		return nil, err
	}
	sort.Strings(e.out)
	return e.out, nil
}

type emitter struct {
	p   *printer
	rel string
	out []string
}

func (e *emitter) add(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	if e.rel != "." {
		line = e.rel + ": " + line
	}
	e.out = append(e.out, line)
}

// versionConst is the one declaration whose VALUE is not compared. Each line
// carries its own release version in it -- v2.x on main, v1.x here -- so its
// value differs by construction and changes on every release of either line.
// Its type is still compared, and `make version-check` holds its value to the
// CHANGELOG.
const versionConst = "Version"

func (e *emitter) object(obj types.Object) error {
	name := obj.Name()
	switch o := obj.(type) {
	case *types.Const:
		if e.rel == "." && name == versionConst {
			e.add("const %s %s", name, e.p.str(o.Type()))
			return nil
		}
		e.add("const %s %s = %s", name, e.p.str(o.Type()), o.Val().ExactString())
	case *types.Var:
		e.add("var %s %s", name, e.p.str(o.Type()))
	case *types.Func:
		sig := o.Type().(*types.Signature)
		e.add("func %s%s%s", name, e.p.typeParams(sig.TypeParams()), e.p.signature(sig))
	case *types.TypeName:
		return e.typeName(o)
	default:
		return fmt.Errorf("%s: a package-level %T, which this tool does not describe", name, obj)
	}
	return nil
}

func (e *emitter) typeName(o *types.TypeName) error {
	name := o.Name()
	if o.IsAlias() {
		if a, ok := o.Type().(*types.Alias); ok && a.TypeParams().Len() > 0 {
			return fmt.Errorf("%s: a generic alias, which this tool does not describe", name)
		}
		e.add("type %s = %s", name, e.p.str(types.Unalias(o.Type())))
		return nil
	}
	named, ok := o.Type().(*types.Named)
	if !ok {
		return fmt.Errorf("%s: a type name of %T, which this tool does not describe", name, o.Type())
	}
	head := name + e.p.typeParams(named.TypeParams())
	switch u := named.Underlying().(type) {
	case *types.Struct:
		e.add("type %s struct", head)
		// Comparability is part of what a caller can rely on -- `==`, a map key
		// -- and a field added to a struct can take it away without changing a
		// single exported line, so it is a line of its own.
		if types.Comparable(named) {
			e.add("type %s struct, comparable", head)
		}
		for i := 0; i < u.NumFields(); i++ {
			f := u.Field(i)
			tag := tagSuffix(u.Tag(i))
			if f.Embedded() {
				// An embedded field is API whatever its name: its fields and
				// methods are promoted. Through an exported type, that type's
				// own lines carry them; through an unexported one, nothing here
				// would, so it is refused rather than silently left out.
				if !f.Exported() {
					return fmt.Errorf("%s embeds the unexported %s, whose promoted fields this tool does not describe", name, f.Name())
				}
				e.add("type %s struct, embedded %s%s", head, e.p.str(f.Type()), tag)
				continue
			}
			if f.Exported() {
				e.add("type %s struct, %s %s%s", head, f.Name(), e.p.str(f.Type()), tag)
			}
		}
	case *types.Interface:
		// The whole method set on one line: adding a method to an interface
		// breaks every type implementing it, and a set of per-method lines
		// would show that only as an addition.
		e.add("type %s %s", head, e.p.str(u))
		return nil
	default:
		e.add("type %s %s", head, e.p.str(u))
	}
	recv := name
	if tps := named.TypeParams(); tps.Len() > 0 {
		var ns []string
		for i := 0; i < tps.Len(); i++ {
			ns = append(ns, tps.At(i).Obj().Name())
		}
		recv += "[" + strings.Join(ns, ", ") + "]"
	}
	valueSet := types.NewMethodSet(named)
	for _, ptr := range []bool{false, true} {
		var t types.Type = named
		r := recv
		if ptr {
			t = types.NewPointer(named)
			r = "*" + recv
		}
		ms := types.NewMethodSet(t)
		for i := 0; i < ms.Len(); i++ {
			m := ms.At(i).Obj()
			if !m.Exported() {
				continue
			}
			if ptr && valueSet.Lookup(m.Pkg(), m.Name()) != nil {
				continue
			}
			e.add("method (%s) %s%s", r, m.Name(), e.p.signature(m.Type().(*types.Signature)))
		}
	}
	return nil
}

// reachedUnexported refuses an exported line that hands a caller a value of an
// unexported type with exported members. A caller can call those methods and
// read those fields, so they are API, and no line above describes them. Better
// to fail than to compare a surface with a hole in it.
func (e *emitter) reachedUnexported() error {
	var reached []*types.Named
	for n := range e.p.unexported {
		reached = append(reached, n)
	}
	sort.Slice(reached, func(i, j int) bool { return reached[i].Obj().Name() < reached[j].Obj().Name() })
	for _, n := range reached {
		name := n.Obj().Name()
		if st, ok := n.Underlying().(*types.Struct); ok {
			for i := 0; i < st.NumFields(); i++ {
				if st.Field(i).Exported() {
					return fmt.Errorf("an exported declaration reaches the unexported %s, whose exported field %s this tool does not describe", name, st.Field(i).Name())
				}
			}
		}
		for _, t := range []types.Type{n, types.NewPointer(n)} {
			ms := types.NewMethodSet(t)
			for i := 0; i < ms.Len(); i++ {
				if ms.At(i).Obj().Exported() {
					return fmt.Errorf("an exported declaration reaches the unexported %s, whose exported method %s this tool does not describe", name, ms.At(i).Obj().Name())
				}
			}
		}
	}
	return nil
}

func tagSuffix(tag string) string {
	if tag == "" {
		return ""
	}
	if strings.Contains(tag, "`") {
		return " " + strconv.Quote(tag)
	}
	return " `" + tag + "`"
}

// printer writes a type by IDENTITY, not by spelling: every alias is resolved
// (so `any`, `interface{}` and an alias of either print alike), `byte` and
// `rune` print as the types they are, parameter names are dropped, and a type
// from another package is qualified by that package's name. An interface prints
// its whole method set, embedded interfaces expanded, since two interfaces with
// the same methods are the same type however they were written.
type printer struct {
	local      *types.Package
	err        error
	unexported map[*types.Named]bool
	// negative counts the function parameter lists the type being written is
	// inside. An odd count is a value the CALLER supplies, an even one a value
	// the caller can come to hold -- and only the second hands a caller the
	// methods of an unexported type. `type HandlerOption func(*handler)` gives
	// a caller no *handler to call anything on; `func New() *handler` does.
	negative int
}

func (p *printer) fail(format string, args ...any) {
	if p.err == nil {
		p.err = fmt.Errorf(format, args...)
	}
}

func (p *printer) str(t types.Type) string {
	var b strings.Builder
	p.write(&b, t)
	return b.String()
}

func (p *printer) write(b *strings.Builder, t types.Type) {
	switch t := t.(type) {
	case *types.Alias:
		p.write(b, types.Unalias(t))
	case *types.Basic:
		switch {
		case t.Kind() == types.Invalid:
			p.fail("an invalid type")
			b.WriteString("<invalid>")
		case t.Kind() == types.UnsafePointer:
			b.WriteString("unsafe.Pointer")
		case t.Info()&types.IsUntyped != 0:
			b.WriteString(t.Name())
		default:
			b.WriteString(types.Typ[t.Kind()].Name())
		}
	case *types.Pointer:
		b.WriteString("*")
		p.write(b, t.Elem())
	case *types.Slice:
		b.WriteString("[]")
		p.write(b, t.Elem())
	case *types.Array:
		fmt.Fprintf(b, "[%d]", t.Len())
		p.write(b, t.Elem())
	case *types.Map:
		b.WriteString("map[")
		p.write(b, t.Key())
		b.WriteString("]")
		p.write(b, t.Elem())
	case *types.Chan:
		switch t.Dir() {
		case types.SendRecv:
			b.WriteString("chan ")
			if c, ok := types.Unalias(t.Elem()).(*types.Chan); ok && c.Dir() == types.RecvOnly {
				b.WriteString("(")
				p.write(b, t.Elem())
				b.WriteString(")")
				return
			}
		case types.SendOnly:
			b.WriteString("chan<- ")
		case types.RecvOnly:
			b.WriteString("<-chan ")
		}
		p.write(b, t.Elem())
	case *types.Signature:
		b.WriteString("func")
		b.WriteString(p.signature(t))
	case *types.Struct:
		b.WriteString("struct{")
		for i := 0; i < t.NumFields(); i++ {
			if i > 0 {
				b.WriteString("; ")
			}
			f := t.Field(i)
			if !f.Embedded() {
				b.WriteString(f.Name())
				b.WriteString(" ")
			}
			p.write(b, f.Type())
			if tag := t.Tag(i); tag != "" {
				b.WriteString(" " + strconv.Quote(tag))
			}
		}
		b.WriteString("}")
	case *types.Interface:
		p.iface(b, t)
	case *types.Named:
		obj := t.Obj()
		switch {
		case obj.Pkg() == nil:
			b.WriteString(obj.Name())
		case obj.Pkg() == p.local:
			if !obj.Exported() && p.negative%2 == 0 {
				if p.unexported == nil {
					p.unexported = map[*types.Named]bool{}
				}
				p.unexported[t.Origin()] = true
			}
			b.WriteString(obj.Name())
		default:
			b.WriteString(obj.Pkg().Name() + "." + obj.Name())
		}
		if args := t.TypeArgs(); args.Len() > 0 {
			b.WriteString("[")
			for i := 0; i < args.Len(); i++ {
				if i > 0 {
					b.WriteString(", ")
				}
				p.write(b, args.At(i))
			}
			b.WriteString("]")
		}
	case *types.TypeParam:
		b.WriteString(t.Obj().Name())
	case *types.Union:
		for i := 0; i < t.Len(); i++ {
			if i > 0 {
				b.WriteString(" | ")
			}
			if t.Term(i).Tilde() {
				b.WriteString("~")
			}
			p.write(b, t.Term(i).Type())
		}
	default:
		p.fail("a type of kind %T, which this tool does not describe", t)
		fmt.Fprintf(b, "<%T>", t)
	}
}

func (p *printer) iface(b *strings.Builder, t *types.Interface) {
	var elems []string
	if t.IsMethodSet() {
		for i := 0; i < t.NumMethods(); i++ {
			m := t.Method(i)
			if !m.Exported() {
				// A caller can neither call nor implement it, so only its
				// presence is API: it is what stops a type outside the package
				// from satisfying the interface except by embedding.
				elems = append(elems, "unexported "+m.Name())
				continue
			}
			elems = append(elems, m.Name()+p.signature(m.Type().(*types.Signature)))
		}
	} else {
		for i := 0; i < t.NumEmbeddeds(); i++ {
			elems = append(elems, p.str(t.EmbeddedType(i)))
		}
		for i := 0; i < t.NumExplicitMethods(); i++ {
			m := t.ExplicitMethod(i)
			elems = append(elems, m.Name()+p.signature(m.Type().(*types.Signature)))
		}
	}
	sort.Strings(elems)
	if len(elems) == 0 {
		b.WriteString("interface{}")
		return
	}
	b.WriteString("interface{" + strings.Join(elems, "; ") + "}")
}

// signature is a signature without `func`: its parameter and result types,
// names dropped.
func (p *printer) signature(sig *types.Signature) string {
	var b strings.Builder
	b.WriteString("(")
	params := sig.Params()
	p.negative++
	defer func() { p.negative-- }()
	for i := 0; i < params.Len(); i++ {
		if i > 0 {
			b.WriteString(", ")
		}
		t := params.At(i).Type()
		if sig.Variadic() && i == params.Len()-1 {
			b.WriteString("...")
			if s, ok := types.Unalias(t).(*types.Slice); ok {
				t = s.Elem()
			}
		}
		p.write(&b, t)
	}
	b.WriteString(")")
	p.negative--
	defer func() { p.negative++ }()
	results := sig.Results()
	switch results.Len() {
	case 0:
	case 1:
		b.WriteString(" ")
		p.write(&b, results.At(0).Type())
	default:
		b.WriteString(" (")
		for i := 0; i < results.Len(); i++ {
			if i > 0 {
				b.WriteString(", ")
			}
			p.write(&b, results.At(i).Type())
		}
		b.WriteString(")")
	}
	return b.String()
}

// typeParams prints a type parameter list, or nothing. An empty constraint
// prints as `any`, the one place the spelling is kept: a constraint is not a
// type a value has, and `[T interface{}]` reads as a mistake.
func (p *printer) typeParams(tps *types.TypeParamList) string {
	if tps.Len() == 0 {
		return ""
	}
	var parts []string
	for i := 0; i < tps.Len(); i++ {
		tp := tps.At(i)
		c := tp.Constraint()
		cs := ""
		if it, ok := types.Unalias(c).Underlying().(*types.Interface); ok && it.Empty() {
			cs = "any"
		} else {
			cs = p.str(c)
		}
		parts = append(parts, tp.Obj().Name()+" "+cs)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}
