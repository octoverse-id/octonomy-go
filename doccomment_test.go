package octonomy

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// --- comments that describe main rather than this line (#100) ----------------------
//
// A port takes a file from main and rewrites it into Go 1.13, and its comments
// come along. The code half of that rewrite is LOUD -- a leftover type parameter
// fails go1.13's build -- but a comment compiles whatever it says, so a ported
// doc comment can tell a Go 1.13 consumer to tag a field `omitzero`, to call
// octonomy.Set, or to import the /v2 module: godoc for a language feature their
// toolchain lacks, on a line they cannot upgrade away from, and which a
// published release cannot recall. The porting checklist marked doc comments
// SILENT with nothing to catch them. This catches the tokens that only main's
// dialect has, in every comment of every file a consumer compiles -- the
// package and the examples.
//
// It REFUSES rather than rewrites, and it refuses the token wherever it
// appears, including a sentence that only mentions main's form to contrast it
// with this line's. A rule that tried to tell "use Optional[T]" from "main's
// Optional[T]" would be a prose classifier, and that is exactly where a
// substitution silently misses one. Name main's construct in words instead --
// "main's generic optional type" -- which no reader can copy into code.
//
// The rules, each refusing what a reader of this line could copy and fail on:
//
//   - `omitzero`, the Go 1.24 struct tag option. Go 1.13's encoding/json
//     ignores it, so a field tagged with it is sent as null on every PATCH that
//     does not set it (updateguard_test.go guards the tags themselves).
//   - Type-parameter syntax: an identifier followed by bracketed type
//     arguments, such as Optional[string], List[T] or doData[Tag], and a type
//     parameter list such as [T any]. Go 1.13 has neither. An index expression
//     -- rows[i], data[0], m["k"] -- is not refused: what is inside the
//     brackets has to read as a type.
//   - The /v2 module's path where a reader would copy it into a build: inside
//     quotes, as an import path is written, or after `go get` or `go install`,
//     or with an @version. In prose it is the pointer this line's carve-outs
//     need -- a consumer needing a webhook receiver moves to the /v2 module --
//     and /api/v2 is a REST route this line serves, never refused.
//   - A Go version at or above main's floor, 1.24, which is what main's comments
//     state as their requirement. An older one is how this line's comments
//     explain a rewrite (url.Values.Has needs Go 1.17), and is kept.
//   - octonomy.Name where this package declares no Name: octonomy.Set,
//     octonomy.Null, octonomy.Optional. Whatever else a comment says, a
//     qualified name the package lacks is code that does not compile.
//   - In a comment's CODE lines (a doc comment's tab-indented example block),
//     a standard-library function, type, constant or variable added after Go
//     1.13 -- slices.DeleteFunc in a BuildTagTree example is code a reader
//     copies. The list is the running toolchain's own record of what each
//     release added ($GOROOT/api), not a list kept here. Methods are not in it
//     (url.Values.Has), nor are builtins (min, max, clear): a method call names
//     no package to look up, and a builtin is spelled like a caller's function.
//     On go1.13.15 that record ends at go1.13 and this rule has nothing to
//     refuse; the modern jobs run it.
//
// What it does not catch is a main-only CLAIM made in ordinary words -- "a PATCH
// can clear this field". That stays with the reader.

var (
	omitzeroToken = regexp.MustCompile(`(?i)omitzero`)
	// An identifier, then brackets holding what reads as a type: an exported
	// name, a qualified one, a pointer to either, or a predeclared type. Several
	// comma-separated arguments are allowed. `map` is the one keyword spelled
	// this way, and map[string]interface{} is Go 1.13.
	typeArgs      = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\[(\*?(?:[A-Za-z_][A-Za-z0-9_]*\.)?[A-Za-z_][A-Za-z0-9_]*)((?:\s*,\s*\*?(?:[A-Za-z_][A-Za-z0-9_]*\.)?[A-Za-z_][A-Za-z0-9_]*)*)\]`)
	typeParamList = regexp.MustCompile(`\[[A-Za-z_][A-Za-z0-9_]*(?:\s*,\s*[A-Za-z_][A-Za-z0-9_]*)*\s+(?:any|comparable|interface\s*\{|~)`)
	predeclared   = map[string]bool{
		"any": true, "bool": true, "byte": true, "comparable": true, "complex64": true, "complex128": true,
		"error": true, "float32": true, "float64": true, "int": true, "int8": true, "int16": true,
		"int32": true, "int64": true, "rune": true, "string": true, "uint": true, "uint8": true,
		"uint16": true, "uint32": true, "uint64": true, "uintptr": true,
	}
	v2ModuleImport = regexp.MustCompile(`"github\.com/octoverse-id/octonomy-go/v2(?:/[^"]*)?"|\bgo\s+(?:get|install)\s+\S*octonomy-go/v2\b|octonomy-go/v2(?:/\S*)?@`)
	mainGoFloor    = regexp.MustCompile(`(?i)\bgo\s?1\.(\d+)\b`)
	qualifiedName  = regexp.MustCompile(`\boctonomy\.([A-Z][A-Za-z0-9_]*)`)
	packageMember  = regexp.MustCompile(`\b([a-z][a-z0-9]*)\.([A-Z][A-Za-z0-9_]*)`)
)

// mainGoFloorMinor is main's Go floor at 5e40964, the commit this line ports
// from: `go 1.24` in its go.mod.
const mainGoFloorMinor = 24

func TestNoCommentCarriesAMainOnlyToken(t *testing.T) {
	rules := commentRules{}
	rules.newer, rules.packages = stdlibRecord(t)
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	for rel := range shippedSourceIn(t, ".") {
		f, err := parser.ParseFile(fset, rel, nil, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		files[rel] = f
	}
	rules.declared = packageLevelNames(files)
	if !rules.declared["TagUpdate"] || !rules.declared["DecodeMetadata"] {
		t.Fatalf("read %d files and found no TagUpdate or DecodeMetadata: the walk is not reading this package", len(files))
	}
	var problems []string
	for _, rel := range sortedFileNames(files) {
		problems = append(problems, commentProblems(fset, files[rel], rules)...)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

// packageLevelNames is every name the root package declares at package level --
// what octonomy.Name can resolve to. Files in a subdirectory are another
// package (the examples are package main) and declare nothing it can name.
func packageLevelNames(files map[string]*ast.File) map[string]bool {
	names := map[string]bool{}
	for rel, f := range files {
		if strings.Contains(rel, "/") {
			continue
		}
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil {
					names[d.Name.Name] = true
				}
			case *ast.GenDecl:
				for _, s := range d.Specs {
					switch s := s.(type) {
					case *ast.TypeSpec:
						names[s.Name.Name] = true
					case *ast.ValueSpec:
						for _, n := range s.Names {
							names[n.Name] = true
						}
					}
				}
			}
		}
	}
	return names
}

// commentRules is what the rules read besides the line: the names this
// package declares, the standard library's post-1.13 additions, and the
// standard library's package names.
type commentRules struct {
	declared, newer, packages map[string]bool
}

// commentProblems checks every comment in one file, line by line.
func commentProblems(fset *token.FileSet, f *ast.File, rules commentRules) []string {
	var problems []string
	for _, group := range f.Comments {
		for _, c := range group.List {
			start := fset.Position(c.Slash)
			text := c.Text
			if strings.HasPrefix(text, "//") {
				text = text[2:]
			} else {
				text = strings.TrimSuffix(strings.TrimPrefix(text, "/*"), "*/")
			}
			for i, line := range strings.Split(text, "\n") {
				for _, why := range commentLineProblems(line, rules) {
					problems = append(problems, fmt.Sprintf("%s:%d: %s\n    %s", start.Filename, start.Line+i, why, strings.TrimSpace(line)))
				}
			}
		}
	}
	return problems
}

// commentLineProblems is every rule above against one line of comment text,
// with the comment marker already removed.
func commentLineProblems(line string, rules commentRules) []string {
	var why []string
	if omitzeroToken.MatchString(line) {
		why = append(why, "names omitzero, a struct tag option Go 1.13's encoding/json ignores")
	}
	for _, m := range typeArgs.FindAllStringSubmatch(line, -1) {
		if m[1] != "map" && rules.readsAsType(m[2]) {
			why = append(why, fmt.Sprintf("%s is type-parameter syntax, which Go 1.13 does not have", m[0]))
		}
	}
	if m := typeParamList.FindString(line); m != "" {
		why = append(why, fmt.Sprintf("%q opens a type parameter list, which Go 1.13 does not have", m))
	}
	if m := v2ModuleImport.FindString(line); m != "" {
		why = append(why, fmt.Sprintf("%s is the /v2 module's path where a reader would build with it -- this line's is github.com/octoverse-id/octonomy-go", m))
	}
	for _, m := range mainGoFloor.FindAllStringSubmatch(line, -1) {
		if minor, err := strconv.Atoi(m[1]); err == nil && minor >= mainGoFloorMinor {
			why = append(why, fmt.Sprintf("%q is main's Go floor or later; this line's is Go 1.13", m[0]))
		}
	}
	for _, m := range qualifiedName.FindAllStringSubmatch(line, -1) {
		if !rules.declared[m[1]] {
			why = append(why, fmt.Sprintf("octonomy.%s is not declared on this line", m[1]))
		}
	}
	if strings.HasPrefix(line, "\t") {
		for _, m := range packageMember.FindAllStringSubmatch(line, -1) {
			if rules.newer[m[1]+"."+m[2]] {
				why = append(why, fmt.Sprintf("%s.%s, in a code example, was added to the standard library after Go 1.13", m[1], m[2]))
			}
		}
	}
	return why
}

// readsAsType: bracket contents that read as a type argument rather than an
// index -- an exported name, a predeclared type, or an exported name qualified
// by a PACKAGE. seen[t.ID] indexes by a field: t is no package, so it is kept,
// where List[octonomy.Tag] and Optional[json.RawMessage] are not.
func (r commentRules) readsAsType(arg string) bool {
	arg = strings.TrimPrefix(arg, "*")
	if i := strings.LastIndex(arg, "."); i >= 0 {
		qualifier, name := arg[:i], arg[i+1:]
		return (qualifier == "octonomy" || r.packages[qualifier]) && name != "" && name[0] >= 'A' && name[0] <= 'Z'
	}
	return predeclared[arg] || (arg[0] >= 'A' && arg[0] <= 'Z')
}

// stdlibRecord reads the running toolchain's API record. newer is "pkg.Name"
// for every package-level function, type, constant and variable it lists as
// added in Go 1.14 or later and in no release up to 1.13; packages is every
// package it names. Both are keyed by the package's NAME, which is how a comment
// spells it (math/rand/v2 is "rand").
func stdlibRecord(t *testing.T) (newer, packages map[string]bool) {
	t.Helper()
	dir := filepath.Join(toolchainRoot(t), "api")
	entries, err := ioutil.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the toolchain's API record: %v", err)
	}
	release := regexp.MustCompile(`^go1(?:\.(\d+))?\.txt$`)
	feature := regexp.MustCompile(`^pkg ([^ ,]+)(?: \([^)]*\))?, (?:func|type|const|var) ([A-Za-z_][A-Za-z0-9_]*)`)
	old, added, packages := map[string]bool{}, map[string]bool{}, map[string]bool{}
	newest := 0
	for _, e := range entries {
		m := release.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		minor := 0
		if m[1] != "" {
			minor, _ = strconv.Atoi(m[1])
		}
		if minor > newest {
			newest = minor
		}
		f, err := os.Open(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for scanner.Scan() {
			fm := feature.FindStringSubmatch(scanner.Text())
			if fm == nil {
				continue
			}
			elems := strings.Split(fm[1], "/")
			name := elems[len(elems)-1]
			if len(elems) > 1 && regexp.MustCompile(`^v[0-9]+$`).MatchString(name) {
				name = elems[len(elems)-2]
			}
			packages[name] = true
			if minor <= 13 {
				old[name+"."+fm[2]] = true
			} else {
				added[name+"."+fm[2]] = true
			}
		}
		err = scanner.Err()
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	if newest < 13 {
		t.Fatalf("%s lists no release through go1.13; this is not a Go toolchain's API record", dir)
	}
	for k := range old {
		delete(added, k)
	}
	if newest >= 21 && !added["slices.DeleteFunc"] {
		t.Fatalf("%s runs through go1.%d and slices.DeleteFunc is not among the additions: the record is not being read", dir, newest)
	}
	if newest == 13 {
		t.Logf("%s ends at go1.13, so the standard-library rule has nothing to refuse on this toolchain; the modern jobs run it", runtime.Version())
	}
	if !packages["json"] || !packages["http"] {
		t.Fatalf("%s names no json or http package: the record is not being read", dir)
	}
	return added, packages
}

// toolchainRoot is the running toolchain's GOROOT. runtime.GOROOT is empty in
// a test binary built with -trimpath, so the go command is asked then.
func toolchainRoot(t *testing.T) string {
	t.Helper()
	if root := runtime.GOROOT(); root != "" {
		return root
	}
	out, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		t.Fatalf("cannot find the toolchain's GOROOT, whose api directory this guard reads: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// TestTheCommentGuardRefusesMainOnlyTokens is the guard's fixture: main's own
// comment forms, each refused, and this line's own forms that look like them,
// each kept. Passing on the real tree is not its only evidence.
func TestTheCommentGuardRefusesMainOnlyTokens(t *testing.T) {
	rules := commentRules{
		declared: map[string]bool{"TagUpdate": true, "String": true, "Metadata": true, "APIV2": true, "Each": true},
		newer:    map[string]bool{"slices.DeleteFunc": true, "strings.Cut": true},
		packages: map[string]bool{"json": true, "context": true, "slices": true, "strings": true},
	}
	refused := []struct{ line, want string }{
		// main's comments at 5e40964, verbatim or nearly.
		{" A field of this type MUST be tagged `json:\",omitzero\"`, never `omitempty`:", "omitzero"},
		{" Go 1.24 added omitzero and the IsZero method it consults.", "omitzero"},
		{" Go 1.24 added omitzero and the IsZero method it consults.", "main's Go floor"},
		{" Optional[T], which says one of three things:", "type-parameter syntax"},
		{"\toctonomy.TagUpdate{ParentID: octonomy.Null[string]()}  // {\"parent_id\":null} -- clear it", "type-parameter syntax"},
		{"\toctonomy.TagUpdate{ParentID: octonomy.Null[string]()}  // {\"parent_id\":null} -- clear it", "octonomy.Null is not declared"},
		{"\toctonomy.TagUpdate{Name: octonomy.Set(\"Autumn\")}", "octonomy.Set is not declared"},
		{" List methods return a *List[T] holding the Data slice", "type-parameter syntax"},
		{"\tfunc(ctx context.Context, o octonomy.ListOptions) (*octonomy.List[octonomy.Tag], error) {", "type-parameter syntax"},
		{"\t  (c *Client) do            doData[T](ctx, c, …)", "type-parameter syntax"},
		{"\ttags = slices.DeleteFunc(tags, func(t octonomy.Tag) bool {", "added to the standard library after Go 1.13"},
		{"\tbefore, after, ok := strings.Cut(s, \"=\")", "added to the standard library after Go 1.13"},
		{"\timport octonomy \"github.com/octoverse-id/octonomy-go/v2\"", "/v2 module's path"},
		{"\tgo get github.com/octoverse-id/octonomy-go/v2", "/v2 module's path"},
		{" require github.com/octoverse-id/octonomy-go/v2@latest", "/v2 module's path"},
		{"   - github.com/octoverse-id/octonomy-go/v2   v2.x, Go 1.24+, active development", "main's Go floor"},
		{" requires go1.25 or later", "main's Go floor"},
		{" func DecodeMetadata[T any](m Metadata) (T, error)", "type parameter list"},
		{" Each[T] over a List[T]", "type-parameter syntax"},
		{" an Optional[octonomy.Metadata] holds it", "type-parameter syntax"},
		{" an Optional[json.RawMessage] holds it", "type-parameter syntax"},
		{" func Pair[K comparable, V any]()", "type parameter list"},
	}
	for _, tc := range refused {
		got := strings.Join(commentLineProblems(tc.line, rules), "\n")
		if !strings.Contains(got, tc.want) {
			t.Errorf("%q: want a refusal containing %q, got:\n%s", tc.line, tc.want, got)
		}
	}
	kept := []string{
		" Config.APIVersion selects /api/v1 or /api/v2; set it to octonomy.APIV2 for the namespace axis.",
		" a consumer needing a webhook receiver moves to the /v2 module",
		"   - github.com/octoverse-id/octonomy-go/v2   v2.x, a modern Go, where features originate",
		" import \"github.com/octoverse-id/octonomy-go\"",
		" rows[i], data[0], m[\"key\"] and q[key] are index expressions",
		" Metadata is map[string]interface{}, and a []map[string]interface{} is a slice of them",
		" url.Values.Has needs Go 1.17, and slices.Reverse Go 1.21; both are written out here",
		" Probed on go1.13.15 and go1.23.4",
		"\tvar s []string = strings.Split(x, \",\")",
		" (prose, not code) slices.DeleteFunc is what main's example used",
		"\tocto := octonomy.TagUpdate{Metadata: octonomy.Metadata{}}",
		" octonomy.Each walks every page",
		" a generic [T] in prose is a doc link, not syntax",
		" rows[r.i] indexes by a field",
		"\tif !seen[t.ID] {",
	}
	for _, line := range kept {
		if got := commentLineProblems(line, rules); len(got) != 0 {
			t.Errorf("%q was refused, and it is this line's own form:\n%s", line, strings.Join(got, "\n"))
		}
	}

	// And through the file reader: a block comment's lines, a doc comment's
	// code block, and the position each refusal names.
	src := "package p\n\n/*\n\tx := octonomy.Null[string]()\n*/\n\n// F does it.\n//\n//\ttags = slices.DeleteFunc(tags, f)\nfunc F() {}\n"
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fixture.go", src, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(commentProblems(fset, f, rules), "\n")
	for _, want := range []string{"fixture.go:4: Null[string] is type-parameter syntax", "fixture.go:4: octonomy.Null is not declared", "fixture.go:9: slices.DeleteFunc, in a code example"} {
		if !strings.Contains(got, want) {
			t.Errorf("want %q in:\n%s", want, got)
		}
	}
}
