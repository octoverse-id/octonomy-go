package main

import (
	"errors"
	"fmt"
	"go/ast"
	"go/build"
	"go/build/constraint"
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
// names, type parameter names (they print by position, $0, $1, ...), unexported
// declarations, the order of declarations, and the spelling of a type rather
// than its identity. `any` and `interface{}` are one type, as are `byte` and
// `uint8` and an alias and what it names, so each prints as the same text; a
// difference in a line is a difference a caller can see.
//
// Each package also carries its NAME (the identifier a caller's code uses
// without an import alias), and a line per exported type of the package that
// satisfies an exported interface of the package, since an interface with an
// unexported method is satisfied only by types its own package declares and
// whether one still is shows in no method line.

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
		features, err := featuresOf(pkg, rel, modPath)
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
	if err := contextFree(abs); err != nil {
		return nil, fmt.Errorf("%s: %w", relDisplay(rel), err)
	}
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

// contextFree refuses a directory any of whose files the build context could
// add or remove: a file with a build constraint, a GOOS or GOARCH file name
// suffix, or an import of "C". This tool reads every tree in ONE context, so
// an API such a file adds or removes would be compared on one platform, under
// one Go or one set of tags, and silently not otherwise -- the Go 1.13 a
// consumer of this line builds with among them. Neither line has such a file.
//
// It refuses rather than evaluates. Deciding which contexts a constraint
// admits means enumerating platforms, cgo, release tags, tool tags, compilers
// and custom tags in every combination, and a sample of them is a check with
// holes in it (`!debug`, `!cgo || (linux && amd64)`, `amd64.v1`, `gc` each slip
// past one). A tree with none of these files has one file set everywhere, so
// one context describes it exactly.
//
// The single exception is a constraint of exactly `ignore`, the convention for
// a file no build is meant to include, such as a generator. It is left out of
// the comparison, and the convention is all that keeps it out of a build:
// `go build -tags ignore` would include it. A file whose name starts with `_`
// or `.` is ignored by the go command and is not read either.
func contextFree(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	// A context naming no platform: a file excluded under it is excluded by a
	// GOOS or GOARCH suffix this toolchain's go/build recognizes -- the same
	// go/build the go command of this toolchain uses.
	noPlatform := build.Default
	noPlatform.GOOS, noPlatform.GOARCH = "nonesuch", "nonesuch"
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") ||
			strings.HasPrefix(name, "_") || strings.HasPrefix(name, ".") {
			continue
		}
		file := filepath.Join(dir, name)
		expr, err := buildConstraint(file)
		if err != nil {
			return err
		}
		if expr != "" {
			if expr == "ignore" {
				continue
			}
			return fmt.Errorf("%s has the build constraint %q; this tool reads one build context and refuses a file another could add or remove", name, expr)
		}
		if match, err := noPlatform.MatchFile(dir, name); err != nil {
			return err
		} else if !match {
			return fmt.Errorf("%s is built only for the GOOS or GOARCH its name ends in; this tool reads one build context and refuses a file another could add or remove", name)
		}
		f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			if imp.Path.Value == `"C"` {
				return fmt.Errorf("%s imports \"C\", so cgo decides whether it is built; this tool reads one build context and refuses it", name)
			}
		}
	}
	return nil
}

// buildConstraint is a file's build constraint as its expression's text, or
// "" when it has none: a //go:build line, or the // +build lines Go 1.13 reads,
// before the package clause.
func buildConstraint(file string) (string, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	var plus []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "package ") {
			break
		}
		switch {
		case constraint.IsGoBuild(line):
			expr, err := constraint.Parse(line)
			if err != nil {
				return "", fmt.Errorf("%s: %w", file, err)
			}
			return expr.String(), nil
		case constraint.IsPlusBuild(line):
			plus = append(plus, line)
		}
	}
	var parts []string
	for _, line := range plus {
		expr, err := constraint.Parse(line)
		if err != nil {
			return "", fmt.Errorf("%s: %w", file, err)
		}
		parts = append(parts, expr.String())
	}
	return strings.Join(parts, " && "), nil
}

type importerFunc func(path string) (*types.Package, error)

func (f importerFunc) Import(path string) (*types.Package, error) { return f(path) }

// featuresOf lists one package's exported features, sorted. Lines of a package
// other than the root carry its path as a prefix, so the features of two
// packages can never be mistaken for each other.
func featuresOf(pkg *types.Package, rel, modulePath string) ([]string, error) {
	e := &emitter{p: &printer{local: pkg, modulePath: modulePath}, rel: rel}
	e.add("package name %s", pkg.Name())
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
	if err := e.implements(scope); err != nil {
		return nil, err
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

// versionConst is the one declaration whose VALUE is not compared, in either
// check. Each line carries its own release version in it -- v2.x on main, v1.x
// here -- so its value differs by construction, and it changes on every release
// of either line: comparing it would make Check B fail every release PR, which
// is a check nobody can keep green rather than a stricter one. A caller who
// builds a constant expression out of it (an array length, say) depends on
// which release they compiled against, not on the API, and that is the one
// dependency this leaves unchecked. Its type is still compared, and `make
// version-check` holds its value to the CHANGELOG.
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
				// The field's NAME as well as its type: embedding an alias names
				// the field after the alias, so `struct{ A }` and `struct{ B }`
				// over aliases of one type are the same type to a caller and
				// different fields -- s.A against s.B.
				e.add("type %s struct, embedded %s %s%s", head, f.Name(), e.p.str(f.Type()), tag)
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
			ns = append(ns, fmt.Sprintf("$%d", i))
		}
		recv += "[" + strings.Join(ns, ", ") + "]"
	}
	// Both method sets in full, so *T lists the value methods too. Then a method
	// moving from (*T) to (T) leaves every line *T had in place -- it is
	// compatible: pointer calls, method expressions and interface assignments
	// through *T all still compile -- and moving from (T) to (*T) removes the
	// (T) line, which is the break it is.
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
			e.add("method (%s) %s%s", r, m.Name(), e.p.signature(m.Type().(*types.Signature)))
		}
	}
	return nil
}

// implements adds a line for each exported type T of the package that
// satisfies an exported interface I of the package: `type T implements I`
// when T does -- and then `type *T implements I` too, since *T's method set
// holds T's -- or only `type *T implements I` when only the pointer does. Each
// fact is its own line, so a method moving from (*T) to (T) leaves the *T line
// in place, as it leaves every pointer caller compiling. An interface with an
// unexported method can be satisfied only by types its own package declares,
// through methods no feature line lists, so whether T still satisfies it --
// what `var p I = T{}` in a caller's code needs -- shows nowhere else.
//
// A generic type or interface is where this stops: whether G[X] satisfies I
// depends on the X a caller picks. So a package declaring a SEALED interface
// (one with an unexported method, which only its own types can satisfy) beside
// any exported generic type, or a sealed generic interface, is refused rather
// than compared with that relation left out. Go 1.13 has no generics, so only
// main's side could ever be refused.
func (e *emitter) implements(scope *types.Scope) error {
	var ifaces, concrete, generic []*types.TypeName
	var sealed []string
	for _, name := range scope.Names() {
		tn, ok := scope.Lookup(name).(*types.TypeName)
		if !ok || !tn.Exported() || tn.IsAlias() {
			continue
		}
		named, ok := tn.Type().(*types.Named)
		if !ok {
			continue
		}
		isIface := types.IsInterface(named)
		if isIface && hasUnexportedMethod(named.Underlying().(*types.Interface)) {
			sealed = append(sealed, tn.Name())
		}
		switch {
		case named.TypeParams().Len() > 0:
			generic = append(generic, tn)
		case isIface:
			ifaces = append(ifaces, tn)
		default:
			concrete = append(concrete, tn)
		}
	}
	if len(sealed) > 0 && len(generic) > 0 {
		return fmt.Errorf("the package declares the sealed interface %s beside the generic %s, and whether a generic type satisfies an interface depends on its instantiation, which this tool does not describe -- extend tools/compatdiff's implements() to model it before comparing this tree",
			strings.Join(sealed, ", "), generic[0].Name())
	}
	for _, i := range ifaces {
		it := i.Type().Underlying().(*types.Interface)
		for _, c := range concrete {
			if types.Implements(c.Type(), it) {
				e.add("type %s implements %s", c.Name(), i.Name())
			}
			if types.Implements(types.NewPointer(c.Type()), it) {
				e.add("type *%s implements %s", c.Name(), i.Name())
			}
		}
	}
	return nil
}

func hasUnexportedMethod(it *types.Interface) bool {
	for i := 0; i < it.NumMethods(); i++ {
		if !it.Method(i).Exported() {
			return true
		}
	}
	return false
}

// reachedUnexported refuses an exported declaration that hands a caller a
// value of a type the caller cannot name -- an unexported type of the package,
// or a type of one of the module's internal packages. Such a value has an
// underlying type, a comparability and possibly methods and fields, all usable
// by the caller (`len(pkg.New())`, `pkg.New().M()`), and no line describes them.
// Better to fail than to compare a surface with a hole in it. Where the value
// only goes the other way -- into a parameter, at any depth -- the caller
// cannot hold one: to receive it they would have to write a function whose
// signature names the type, so `type Option func(*options)` is fine.
func (e *emitter) reachedUnexported() error {
	var names []string
	for n := range e.p.unexported {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) > 0 {
		return fmt.Errorf("an exported declaration hands a caller a value of %s, a type the caller cannot name, and this tool does not describe what they can do with it", strings.Join(names, ", "))
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
// from another package is qualified by that package's IMPORT PATH (a package of
// the same module by its path within the module, `{module}/sub`, so main's /v2
// suffix is no difference). An interface prints
// its whole method set, embedded interfaces expanded, since two interfaces with
// the same methods are the same type however they were written.
type printer struct {
	local      *types.Package
	modulePath string
	err        error
	unexported map[string]bool
	// inParams counts the parameter lists the type being written is inside,
	// and quiet the unexported interface methods. A hidden type met at either
	// is one a caller cannot come to hold; see reachedUnexported.
	inParams, quiet int
}

func (p *printer) fail(format string, args ...any) {
	if p.err == nil {
		p.err = fmt.Errorf(format, args...)
	}
}

// pkgPath is how a package other than the one being described is written: its
// import path, or a package of the same module by its path within the module,
// `{module}/sub`, so main's /v2 suffix is no difference.
func (p *printer) pkgPath(pkg *types.Package) string {
	path := pkg.Path()
	if path == p.modulePath || strings.HasPrefix(path, p.modulePath+"/") {
		return "{module}" + strings.TrimPrefix(path, p.modulePath)
	}
	return path
}

// hidden records a type a caller cannot name, met where the caller can come to
// hold a value of it.
func (p *printer) hidden(name string) {
	if p.inParams > 0 || p.quiet > 0 {
		return
	}
	if p.unexported == nil {
		p.unexported = map[string]bool{}
	}
	p.unexported[name] = true
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
			if !obj.Exported() {
				p.hidden(obj.Name())
			}
			b.WriteString(obj.Name())
		default:
			path := p.pkgPath(obj.Pkg())
			if strings.HasPrefix(path, "{module}") && (!obj.Exported() || strings.Contains(path+"/", "/internal/")) {
				p.hidden(path + "." + obj.Name())
			}
			b.WriteString(path + "." + obj.Name())
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
		// By position: a renamed type parameter is no difference to a caller.
		fmt.Fprintf(b, "$%d", t.Index())
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
				// A caller can neither call nor implement it, but its signature
				// still decides which of the package's own types satisfy the
				// interface (the implements lines), so it is printed -- without
				// counting a hidden type in it as one a caller holds.
				p.quiet++
				// An unexported name is its package's own: a.seal and b.seal
				// are different methods, and only types of that package (or
				// embedding one of its interfaces) can have it.
				name := m.Name()
				if m.Pkg() != nil && m.Pkg() != p.local {
					name = p.pkgPath(m.Pkg()) + "." + name
				}
				elems = append(elems, "unexported "+name+p.signature(m.Type().(*types.Signature)))
				p.quiet--
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
	p.inParams++
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
	p.inParams--
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
		parts = append(parts, fmt.Sprintf("$%d %s", i, cs))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}
