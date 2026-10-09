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
`))
	want := []string{
		"const Default APIVersion = \"v1\"",
		"const Version untyped string",
		"func Each[T any](context.Context, func(context.Context) (*List[T], error), func(T) error) (int, error)",
		"method (*List[T]) Len() int",
		"method (Params) String() string",
		"type APIVersion string",
		"type List[T any] struct",
		"type List[T any] struct, Data []T `json:\"data\"`",
		"type Opts struct",
		"type Opts struct, Limit int",
		"type Opts struct, comparable",
		"type Page interface{unexported contents}",
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
		"sub/deeper/notbuilt_x.go":  "//go:build never\n\npackage deeper\nfunc NotBuilt() {}\n",
		"sub/deeper/also_ignore.go": "//go:build ignore\n\npackage other\n",
	})
	var got []string
	for rel, fs := range a.packages {
		for _, f := range fs {
			got = append(got, rel+" | "+f)
		}
	}
	want := map[string]bool{
		". | func Root()":                        true,
		"sub | sub: func Sub()":                  true,
		"sub/deeper | sub/deeper: func Deeper()": true,
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
		{"a result of an unexported type with a method", "package m\ntype client struct{}\nfunc (client) Do() {}\nfunc New() client { return client{} }\n", "unexported client, whose exported method Do"},
		{"a field of an unexported type with a field", "package m\ntype cfg struct{ URL string }\ntype T struct{ C *cfg }\n", "unexported cfg, whose exported field URL"},
		{"a variable of an unexported type", "package m\ntype st struct{}\nfunc (*st) Reset() {}\nvar S = &st{}\n", "unexported st, whose exported method Reset"},
		{"a result of a function-typed result", "package m\ntype h struct{}\nfunc (h) X() {}\nfunc F() func() h { return nil }\n", "unexported h, whose exported method X"},
		{"a generic alias", "package m\ntype A[T any] = []T\n", "generic alias"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadFixture(t, s, rootOnly(tc.src))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want one containing %q", err, tc.want)
			}
		})
	}
	// A caller who can only SUPPLY a value of the type -- a parameter -- holds
	// nothing to call its methods on, and neither does one handed a function
	// that takes it. Refusing these would refuse every functional option.
	for _, src := range []string{
		"package m\ntype h struct{}\nfunc (h) X() {}\ntype Option func(*h)\n",
		"package m\ntype h struct{ F int }\nfunc Apply(*h) {}\n",
		"package m\ntype h struct{}\nfunc (h) X() {}\nfunc WithX() func(*h) { return nil }\n",
		"package m\ntype h struct{}\nfunc New() *h { return nil }\n", // no exported members to describe
	} {
		if _, err := loadFixture(t, s, rootOnly(src)); err != nil {
			t.Errorf("refused a surface that hands a caller nothing undescribed:\n%s\nerr = %v", src, err)
		}
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
