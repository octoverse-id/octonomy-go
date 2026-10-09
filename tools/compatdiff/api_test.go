package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeTree writes files (slash-separated paths relative to dir) into dir.
func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// loadFixture loads a module made of files, with a go.mod unless files has one.
func loadFixture(t *testing.T, s *shared, files map[string]string) (*api, error) {
	t.Helper()
	dir := t.TempDir()
	if _, ok := files["go.mod"]; !ok {
		writeTree(t, dir, map[string]string{"go.mod": "module example.com/m\n\ngo 1.24\n"})
	}
	writeTree(t, dir, files)
	return s.loadAPI(dir)
}

func mustLoad(t *testing.T, s *shared, files map[string]string) *api {
	t.Helper()
	a, err := loadFixture(t, s, files)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return a
}

func rootOnly(src string) map[string]string { return map[string]string{"m.go": src} }

// TestSpellingIsNotADifference: two declarations a caller cannot tell apart
// print alike, so the checks never report one -- a parity check that flagged
// `any` against `interface{}` would turn every port into a carve-out.
func TestSpellingIsNotADifference(t *testing.T) {
	s := newShared()
	pairs := []struct{ name, a, b string }{
		{"any and interface{}", "package m\nvar V any\nfunc F(x any) any { return x }\n", "package m\nvar V interface{}\nfunc F(y interface{}) interface{} { return y }\n"},
		{"byte and uint8", "package m\nfunc F([]byte) byte { return 0 }\n", "package m\nfunc F([]uint8) uint8 { return 0 }\n"},
		{"rune and int32", "package m\nvar R rune\n", "package m\nvar R int32\n"},
		{"an alias and what it names", "package m\ntype M = map[string]any\ntype T struct{ F M }\n", "package m\ntype M = map[string]interface{}\ntype T struct{ F map[string]interface{} }\n"},
		{"parameter names", "package m\nfunc F(ctx int, opts ...string) (n int, err error) { return }\n", "package m\nfunc F(a int, b ...string) (int, error) { return 0, nil }\n"},
		{"declaration order", "package m\nconst A = 1\nconst B = 2\n", "package m\nconst B = 2\nconst A = 1\n"},
		{"an embedded interface against its methods", "package m\nimport \"io\"\ntype I interface{ io.Reader; Close() error }\n", "package m\ntype I interface{ Close() error; Read([]byte) (int, error) }\n"},
		{"unexported declarations", "package m\nfunc F() {}\nfunc g() {}\ntype T struct{ A int; b int }\n", "package m\nfunc F() {}\ntype T struct{ A int; c string }\n"},
		{"the root package's Version value", "package m\nconst Version = \"one\"\n", "package m\nconst Version = \"two\"\n"},
		{"a type parameter's name", "package m\nfunc F[T any](T) {}\ntype L[T any] struct{ D []T }\nfunc (l L[T]) Get() T { var t T; return t }\n", "package m\nfunc F[U any](U) {}\ntype L[E any] struct{ D []E }\nfunc (l L[E]) Get() E { var e E; return e }\n"},
		{"a parameter's type spelled as a function type", "package m\ntype H func(int) error\nfunc F(h func(int) error) {}\n", "package m\ntype H func(n int) error\nfunc F(f func(x int) error) {}\n"},
	}
	for _, p := range pairs {
		t.Run(p.name, func(t *testing.T) {
			a := mustLoad(t, s, rootOnly(p.a))
			b := mustLoad(t, s, rootOnly(p.b))
			if d := parityDiffs(a, b); len(d) != 0 {
				t.Errorf("a difference a caller cannot see was reported: %v", d)
			}
		})
	}
}

// TestEveryKindOfChangeIsADifference: the things a symbol-name diff cannot see
// -- the reason #100 asked for features rather than names -- each produce one.
func TestEveryKindOfChangeIsADifference(t *testing.T) {
	s := newShared()
	pairs := []struct{ name, a, b string }{
		{"a constant's value", "package m\nconst Default = \"v1\"\n", "package m\nconst Default = \"v2\"\n"},
		{"a constant's type", "package m\ntype V string\nconst Default V = \"v1\"\n", "package m\ntype V string\nconst Default = \"v1\"\n"},
		{"a field's type", "package m\ntype U struct{ P *string }\n", "package m\ntype U struct{ P *int }\n"},
		{"a field's struct tag", "package m\ntype U struct{ P *string `json:\"p,omitempty\"` }\n", "package m\ntype U struct{ P *string `json:\"p,omitzero\"` }\n"},
		{"a parameter's type", "package m\nfunc F(int) {}\n", "package m\nfunc F(int64) {}\n"},
		{"a result added", "package m\nfunc F() {}\n", "package m\nfunc F() error { return nil }\n"},
		{"variadic against a slice", "package m\nfunc F(...string) {}\n", "package m\nfunc F([]string) {}\n"},
		{"a value receiver moved to a pointer", "package m\ntype T struct{}\nfunc (T) M() {}\n", "package m\ntype T struct{}\nfunc (*T) M() {}\n"},
		{"comparability lost to an unexported field", "package m\ntype T struct{ A int }\n", "package m\ntype T struct{ A int; m map[string]int }\n"},
		{"an interface gaining a method", "package m\ntype I interface{ A() }\n", "package m\ntype I interface{ A(); B() }\n"},
		{"an interface gaining an unexported method", "package m\ntype I interface{ A() }\n", "package m\ntype I interface{ A(); b() }\n"},
		{"an alias becoming a defined type", "package m\ntype M = map[string]any\n", "package m\ntype M map[string]any\n"},
		{"a type's underlying type", "package m\ntype V string\n", "package m\ntype V int\n"},
		{"a type parameter", "package m\nfunc F[T any](T) {}\n", "package m\nfunc F(any) {}\n"},
		{"an embedded field", "package m\ntype O struct{ L int }\ntype P struct{ O }\n", "package m\ntype O struct{ L int }\ntype P struct{ O O }\n"},
		{"a variable's type", "package m\nvar V int\n", "package m\nvar V int64\n"},
		{"Version's type", "package m\nconst Version = \"1\"\n", "package m\nconst Version = 1\n"},
		{"the package's name", "package m\nfunc F() {}\n", "package renamed\nfunc F() {}\n"},
		{"two packages with one name", "package m\nimport \"html/template\"\nvar V *template.Template\n", "package m\nimport \"text/template\"\nvar V *template.Template\n"},
		{"an unexported interface method's signature", "package m\ntype I interface{ seal() }\n", "package m\ntype I interface{ seal(int) }\n"},
		{"a type no longer satisfying a sealed interface", "package m\ntype I interface{ seal() }\ntype T struct{}\nfunc (T) seal() {}\n", "package m\ntype I interface{ seal() }\ntype T struct{}\nfunc (T) seal(int) {}\n"},
		{"satisfying it only through the pointer", "package m\ntype I interface{ seal() }\ntype T struct{}\nfunc (T) seal() {}\n", "package m\ntype I interface{ seal() }\ntype T struct{}\nfunc (*T) seal() {}\n"},
	}
	for _, p := range pairs {
		t.Run(p.name, func(t *testing.T) {
			a := mustLoad(t, s, rootOnly(p.a))
			b := mustLoad(t, s, rootOnly(p.b))
			if d := parityDiffs(a, b); len(d) == 0 {
				t.Errorf("no difference reported between\n%s\nand\n%s", p.a, p.b)
			}
		})
	}
}

// TestFeatureLines pins the format the carve-out file is written in, since a
// line there must match a line here character for character.
func TestFeatureLines(t *testing.T) {
	s := newShared()
	a := mustLoad(t, s, rootOnly(`package m

import "context"

const Version = "this line's own"

type APIVersion string

const Default APIVersion = "v1"

type List[T any] struct {
	Data []T `+"`json:\"data\"`"+`
	n    int
}

type Opts struct{ Limit int }

type Params struct {
	Opts
	Q *string `+"`json:\"q,omitempty\"`"+`
}

type Page interface{ contents() []any }

func Each[T any](ctx context.Context, page func(context.Context) (*List[T], error), fn func(T) error) (int, error) {
	return 0, nil
}

func (l *List[T]) Len() int { return len(l.Data) }
func (Params) String() string { return "" }
func (*Params) contents() []any { return nil }
`))
	want := []string{
		"const Default APIVersion = \"v1\"",
		"const Version untyped string",
		"func Each[$0 any](context.Context, func(context.Context) (*List[$0], error), func($0) error) (int, error)",
		"method (*List[$0]) Len() int",
		"method (*Params) String() string",
		"method (Params) String() string",
		"package name m",
		"type *Params implements Page",
		"type APIVersion string",
		"type List[$0 any] struct",
		"type List[$0 any] struct, Data []$0 `json:\"data\"`",
		"type Opts struct",
		"type Opts struct, Limit int",
		"type Opts struct, comparable",
		"type Page interface{unexported contents() []interface{}}",
		"type Params struct",
		"type Params struct, Q *string `json:\"q,omitempty\"`",
		"type Params struct, comparable",
		"type Params struct, embedded Opts",
	}
	if got := a.packages["."]; !reflect.DeepEqual(got, want) {
		t.Errorf("features:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestWhatCountsAsAPackage: what a caller can import, and nothing else.
func TestWhatCountsAsAPackage(t *testing.T) {
	s := newShared()
	a := mustLoad(t, s, map[string]string{
		"root.go":                   "package m\nfunc Root() {}\n",
		"sub/sub.go":                "package sub\nimport \"example.com/m\"\nfunc Sub() { m.Root() }\n",
		"sub/sub_test.go":           "package sub\nfunc TestOnly() {}\n",
		"internal/x/x.go":           "package x\nfunc Internal() {}\n",
		"sub/internal/y/y.go":       "package y\nfunc Internal() {}\n",
		"testdata/t.go":             "package t\nfunc Fixture() {}\n",
		"vendor/v/v.go":             "package v\nfunc Vendored() {}\n",
		"_hidden/h.go":              "package h\nfunc Hidden() {}\n",
		".dot/d.go":                 "package d\nfunc Dot() {}\n",
		"cmd/tool/main.go":          "package main\nfunc main() {}\n",
		"nested/go.mod":             "module example.com/m/nested\n",
		"nested/n.go":               "package nested\nfunc Nested() {}\n",
		"onlytests/x_test.go":       "package onlytests\n",
		"ignored/ignored.go":        "//go:build ignore\n\npackage ignored\nfunc Ignored() {}\n",
		"sub/deeper/deeper.go":      "package deeper\nfunc Deeper() {}\n",
		"sub/deeper/notbuilt_x.go":  "//go:build ignore\n\npackage deeper\nfunc NotBuilt() {}\n",
		"sub/deeper/also_ignore.go": "//go:build ignore\n\npackage other\n",
	})
	var got []string
	for rel, fs := range a.packages {
		for _, f := range fs {
			got = append(got, rel+" | "+f)
		}
	}
	want := map[string]bool{
		". | func Root()":                              true,
		". | package name m":                           true,
		"sub | sub: func Sub()":                        true,
		"sub | sub: package name sub":                  true,
		"sub/deeper | sub/deeper: func Deeper()":       true,
		"sub/deeper | sub/deeper: package name deeper": true,
	}
	if len(got) != len(want) {
		t.Errorf("got %d features, want %d:\n%s", len(got), len(want), strings.Join(got, "\n"))
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("unexpected feature %q", g)
		}
	}
}

// TestShapesItCannotDescribeFailClosed: an exported surface with a part this
// tool would leave out is refused, never compared with the hole in it.
func TestShapesItCannotDescribeFailClosed(t *testing.T) {
	s := newShared()
	for _, tc := range []struct{ name, src, want string }{
		{"an unexported embedded type promotes fields", "package m\ntype inner struct{ F int }\ntype T struct{ inner }\n", "embeds the unexported inner"},
		{"a result of an unexported type with a method", "package m\ntype client struct{}\nfunc (client) Do() {}\nfunc New() client { return client{} }\n", "a value of client, a type the caller cannot name"},
		{"a field of an unexported type with a field", "package m\ntype cfg struct{ URL string }\ntype T struct{ C *cfg }\n", "a value of cfg"},
		{"a variable of an unexported type", "package m\ntype st struct{}\nfunc (*st) Reset() {}\nvar S = &st{}\n", "a value of st"},
		{"a result of a function-typed result", "package m\ntype h struct{}\nfunc (h) X() {}\nfunc F() func() h { return nil }\n", "a value of h"},
		// No member at all, and still usable: len(pkg.New()) compiles, and
		// changes meaning when hidden becomes an int.
		{"an unexported type with no members", "package m\ntype hidden string\nfunc New() hidden { return \"\" }\n", "a value of hidden"},
		{"a pointer to one", "package m\ntype h struct{}\nfunc New() *h { return nil }\n", "a value of h"},
		{"a generic alias", "package m\ntype A[T any] = []T\n", "generic alias"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadFixture(t, s, rootOnly(tc.src))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want one containing %q", err, tc.want)
			}
		})
	}
	// A type of one of the module's internal packages is as unnameable to a
	// caller as an unexported one.
	_, err := loadFixture(t, s, map[string]string{
		"m.go":            "package m\nimport \"example.com/m/internal/x\"\nfunc New() x.T { return x.T{} }\n",
		"internal/x/x.go": "package x\ntype T struct{}\nfunc (T) M() {}\n",
	})
	if err == nil || !strings.Contains(err.Error(), "{module}/internal/x.T") {
		t.Errorf("an internal package's type reached from the root: err = %v", err)
	}
	// A caller who can only SUPPLY a value of the type -- a parameter, at any
	// depth -- holds nothing to use. To receive one they would have to write a
	// function whose signature names the type. Refusing these would refuse
	// every functional option.
	for _, src := range []string{
		"package m\ntype h struct{}\nfunc (h) X() {}\ntype Option func(*h)\n",
		"package m\ntype h struct{ F int }\nfunc Apply(*h) {}\n",
		"package m\ntype h struct{}\nfunc (h) X() {}\nfunc WithX() func(*h) { return nil }\n",
		"package m\ntype h struct{}\nfunc (h) X() {}\nfunc Register(func(*h)) {}\n",
		"package m\ntype h struct{}\nfunc Register(func() *h) {}\n",
		"package m\ntype h struct{}\ntype I interface{ seal(*h) }\n",
	} {
		if _, err := loadFixture(t, s, rootOnly(src)); err != nil {
			t.Errorf("refused a surface that hands a caller nothing undescribed:\n%s\nerr = %v", src, err)
		}
	}
}

// TestOneBuildContextOrNone: a file that is in some builds and not others would
// be compared in one of them only, so it is refused; a file in none is left out.
func TestOneBuildContextOrNone(t *testing.T) {
	s := newShared()
	for _, tc := range []struct{ name, file, src string }{
		{"a GOOS file name", "api_windows.go", "package m\nfunc Windows() {}\n"},
		{"a GOARCH file name", "api_arm64.go", "package m\nfunc Arm() {}\n"},
		{"a GOOS constraint", "w.go", "//go:build windows\n\npackage m\nfunc Windows() {}\n"},
		{"a Go version only Go 1.13 builds", "old.go", "// +build !go1.14\n\npackage m\nfunc Old() {}\n"},
		{"a Go version Go 1.13 does not build", "new.go", "//go:build go1.18\n\npackage m\nfunc New() {}\n"},
		{"cgo", "c.go", "//go:build cgo\n\npackage m\nfunc C() {}\n"},
		{"a GOOS no fixed context names", "api_freebsd.go", "package m\nfunc BSD() {}\n"},
		{"a GOARCH no fixed context names", "api_s390x.go", "package m\nfunc Z() {}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadFixture(t, s, map[string]string{"m.go": "package m\n", tc.file: tc.src})
			if err == nil || !strings.Contains(err.Error(), "built in some contexts and not others") {
				t.Errorf("err = %v", err)
			}
		})
	}
	for _, tag := range []string{"//go:build debug", "// +build debug", "//go:build ignore && debug"} {
		_, err := loadFixture(t, s, map[string]string{"m.go": "package m\n", "d.go": tag + "\n\npackage m\nfunc Debug() {}\n"})
		if err == nil || !strings.Contains(err.Error(), "built in no context this tool reads") {
			t.Errorf("%s: err = %v", tag, err)
		}
	}
	a := mustLoad(t, s, map[string]string{
		"m.go":       "package m\n",
		"gen.go":     "//go:build ignore\n\npackage main\nfunc main() {}\n",
		"old.go":     "// +build ignore\n\npackage main\nfunc main() {}\n",
		"_skip.go":   "package m\nfunc Skipped() {}\n",
		".hidden.go": "package m\nfunc Hidden() {}\n",
	})
	if got := a.packages["."]; len(got) != 1 || got[0] != "package name m" {
		t.Errorf("a file no build includes was read: %v", got)
	}
}

func TestLoadFailures(t *testing.T) {
	s := newShared()
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"no go.mod module line", map[string]string{"go.mod": "go 1.24\n", "m.go": "package m\n"}, "declares no module path"},
		{"no root package", map[string]string{"sub/s.go": "package sub\n"}, "no importable root package"},
		{"a type error", rootOnly("package m\nvar V = undefined\n"), "type-check the root package"},
		{"a third-party import", rootOnly("package m\nimport _ \"example.org/elsewhere\"\n"), "elsewhere"},
		{"two packages in one directory", map[string]string{"a.go": "package m\n", "b.go": "package n\n"}, "the root package"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadFixture(t, s, tc.files)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want one containing %q", err, tc.want)
			}
		})
	}
}
