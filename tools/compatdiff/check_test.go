package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func carveoutFixture(t *testing.T, body string) []*section {
	t.Helper()
	p := filepath.Join(t.TempDir(), "carveouts.txt")
	writeTree(t, filepath.Dir(p), map[string]string{"carveouts.txt": body})
	sections, err := parseCarveouts(p)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return sections
}

// TestMatchCarveouts: every difference must be listed, and every listing must
// still be a difference. Each direction is a finding of its own.
func TestMatchCarveouts(t *testing.T) {
	s := newShared()
	main := mustLoad(t, s, map[string]string{
		"m.go":         "package m\nconst Default = \"v2\"\nfunc Shared() {}\nfunc MainOnly() {}\n",
		"hook/hook.go": "package hook\nfunc Verify() {}\nfunc Parse() {}\n",
	})
	compat := mustLoad(t, s, rootOnly("package m\nconst Default = \"v1\"\nfunc Shared() {}\n"))

	diffs := parityDiffs(main, compat)
	var got []string
	for _, d := range diffs {
		got = append(got, d.String())
	}
	// The package one side lacks is ONE difference, not one per declaration.
	want := []string{
		`main const Default untyped string = "v2"`,
		"main func MainOnly()",
		`compat const Default untyped string = "v1"`,
		"main package hook",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("diffs:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	complete := "section carveout default\ntitle Default\ntext why\n" +
		"main const Default untyped string = \"v2\"\ncompat const Default untyped string = \"v1\"\n" +
		"section carveout hook\ntitle Hook\ntext why\nmain package hook\n" +
		"section gap main-only\ntitle Not ported\nissue #1\ntext why\nmain func MainOnly()\n"
	if f := matchCarveouts(diffs, carveoutFixture(t, complete), "c.txt"); len(f) != 0 {
		t.Errorf("a complete carve-out file has findings: %v", f)
	}

	missing := strings.Replace(complete, "main func MainOnly()\n", "main func Gone()\n", 1)
	f := matchCarveouts(diffs, carveoutFixture(t, missing), "c.txt")
	if len(f) != 2 || !strings.Contains(f[0], "main func MainOnly()") || !strings.Contains(f[0], "does not list it") ||
		!strings.Contains(f[1], "`main func Gone()`") || !strings.Contains(f[1], "no such difference") {
		t.Errorf("want one unlisted difference and one stale line, got:\n%s", strings.Join(f, "\n"))
	}

	// The SIDE is part of a listing: the right feature under the wrong side
	// accounts for nothing.
	wrongSide := strings.Replace(complete, "main package hook", "compat package hook", 1)
	if f := matchCarveouts(diffs, carveoutFixture(t, wrongSide), "c.txt"); len(f) != 2 {
		t.Errorf("a feature listed under the wrong side was accepted: %v", f)
	}
}

// TestCompatibilityFindings is Check B against each way a release's caller
// stops compiling, and against the additions that break nobody.
func TestCompatibilityFindings(t *testing.T) {
	s := newShared()
	base := `package m

const Default = "v1"

type U struct {
	P *string ` + "`json:\"p,omitempty\"`" + `
}

type K struct{ A int }

type I interface{ A() }

type T struct{}

func (T) Value() {}

func F(int) error { return nil }
`
	release := mustLoad(t, s, rootOnly(base))
	if f := compatibilityFindings("v1.0.0", release, mustLoad(t, s, rootOnly(base))); len(f) != 0 {
		t.Fatalf("an unchanged tree has findings: %v", f)
	}

	additions := base + "\nfunc G() {}\n\nfunc (T) Added() {}\n\ntype New struct{}\n\nfunc (*T) Moved() {}\n"
	additions = strings.Replace(additions, "type K struct{ A int }", "type K struct{ A int; B int }", 1)
	if f := compatibilityFindings("v1.0.0", release, mustLoad(t, s, rootOnly(additions))); len(f) != 0 {
		t.Errorf("additions were reported as breaks: %v", f)
	}
	// A method moving from the pointer receiver to the value one keeps every
	// call through *T compiling, so it is no break.
	withMoved := mustLoad(t, s, rootOnly(additions))
	movedToValue := strings.Replace(additions, "func (*T) Moved() {}", "func (T) Moved() {}", 1)
	if f := compatibilityFindings("a later release", withMoved, mustLoad(t, s, rootOnly(movedToValue))); len(f) != 0 {
		t.Errorf("a method moved to the value receiver was reported as a break: %v", f)
	}

	for _, tc := range []struct{ name, old, new string }{
		{"a removed function", "func F(int) error { return nil }", ""},
		{"a changed signature", "func F(int) error", "func F(int64) error"},
		{"a changed constant value", `const Default = "v1"`, `const Default = "v2"`},
		{"a field's type", "P *string", "P *int"},
		{"a field's tag", "`json:\"p,omitempty\"`", "`json:\"p\"`"},
		{"comparability lost", "type K struct{ A int }", "type K struct{ A int; m []int }"},
		{"an interface method added", "type I interface{ A() }", "type I interface{ A(); B() }"},
		{"a method moved to the pointer receiver", "func (T) Value() {}", "func (*T) Value() {}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := strings.Replace(base, tc.old, tc.new, 1)
			if changed == base {
				t.Fatalf("fixture does not contain %q", tc.old)
			}
			f := compatibilityFindings("v1.0.0", release, mustLoad(t, s, rootOnly(changed)))
			if len(f) == 0 {
				t.Errorf("no finding for %s", tc.name)
			}
			for _, msg := range f {
				if !strings.HasPrefix(msg, "compatibility: v1.0.0 exports `") {
					t.Errorf("finding does not name the release and the feature: %q", msg)
				}
			}
		})
	}

	// Nor does it break *T's satisfaction of a sealed interface.
	sealedPtr := mustLoad(t, s, rootOnly("package m\ntype I interface{ seal() }\ntype T struct{}\nfunc (*T) seal() {}\n"))
	sealedValue := mustLoad(t, s, rootOnly("package m\ntype I interface{ seal() }\ntype T struct{}\nfunc (T) seal() {}\n"))
	if f := compatibilityFindings("a later release", sealedPtr, sealedValue); len(f) != 0 {
		t.Errorf("a sealing method moved to the value receiver was reported as a break: %v", f)
	}
	if f := compatibilityFindings("a later release", sealedValue, sealedPtr); len(f) == 0 {
		t.Error("a sealing method moved to the pointer receiver was not reported: T no longer satisfies I")
	}
	// An unexported method is its package's own, so embedding another
	// package's sealed interface is another interface.
	sibling := func(which string) *api {
		return mustLoad(t, s, map[string]string{
			"m.go":   "package m\nimport \"example.com/m/" + which + "\"\ntype I interface{ " + which + ".Seal }\n",
			"a/a.go": "package a\ntype Seal interface{ seal() }\n",
			"b/b.go": "package b\ntype Seal interface{ seal() }\n",
		})
	}
	if f := compatibilityFindings("a later release", sibling("a"), sibling("b")); len(f) == 0 {
		t.Error("an interface embedding another package's sealed interface changed and nothing was reported")
	}

	// Field order: a reorder breaks a caller's conversion to a struct type of
	// the same fields; an insertion anywhere keeps every pair it found.
	twoFields := mustLoad(t, s, rootOnly("package m\ntype K struct{ A int; B string }\n"))
	if f := compatibilityFindings("a later release", twoFields, mustLoad(t, s, rootOnly("package m\ntype K struct{ B string; A int }\n"))); len(f) == 0 || !strings.Contains(f[0], "field A before B") {
		t.Errorf("two fields reordered: %v", f)
	}
	if f := compatibilityFindings("a later release", twoFields, mustLoad(t, s, rootOnly("package m\ntype K struct{ A int; C bool; B string; D int }\n"))); len(f) != 0 {
		t.Errorf("fields inserted between and after were reported as a break: %v", f)
	}
	// Parity does not read order: the lines are different modules.
	if d := parityDiffs(twoFields, mustLoad(t, s, rootOnly("package m\ntype K struct{ B string; A int }\n"))); len(d) != 0 {
		t.Errorf("field order reached the parity check: %v", d)
	}

	withSub := mustLoad(t, s, map[string]string{"m.go": base, "sub/s.go": "package sub\nfunc S() {}\n"})
	if f := compatibilityFindings("v1.0.0", withSub, release); len(f) != 1 || !strings.Contains(f[0], "no such package") {
		t.Errorf("a removed package: got %v", f)
	}
}

func TestParseCarveoutsRefuses(t *testing.T) {
	const ok = "section carveout a\ntitle A\ntext why\nmain func F()\n"
	if _, err := parseCarveoutsString(t, ok); err != nil {
		t.Fatalf("the valid fixture is refused: %v", err)
	}
	for _, tc := range []struct{ name, body, want string }{
		{"a feature before any section", "main func F()\n" + ok, "before the first section"},
		{"an unknown keyword", ok + "mian func G()\n", "unknown keyword"},
		{"an unknown kind", "section carvout a\ntitle A\ntext why\n", "unknown section kind"},
		{"a bad id", "section carveout A_b\ntitle A\ntext why\n", "lowercase words"},
		{"a section declared twice", ok + "section carveout a\ntitle B\ntext why\n", "declared twice"},
		{"a feature listed twice", ok + "section dialect b\ntitle B\ntext why\nmain func F()\n", "listed twice"},
		{"no title", "section carveout a\ntext why\n", "has no title"},
		{"two titles", "section carveout a\ntitle A\ntitle B\ntext why\n", "second title"},
		{"an empty title", "section carveout a\ntitle  \ntext why\n", "empty title"},
		{"no text", "section carveout a\ntitle A\nmain func F()\n", "has no text"},
		{"a gap with no issue", "section gap a\ntitle A\ntext why\nmain func F()\n", "names no issue"},
		{"an issue on a carve-out", "section carveout a\ntitle A\nissue #3\ntext why\n", "only a gap names an issue"},
		{"a malformed issue", "section gap a\ntitle A\nissue 3\ntext why\nmain func F()\n", "written #N"},
		{"a dialect section listing nothing", "section dialect a\ntitle A\ntext why\n", "lists no difference"},
		{"a gap listing nothing", "section gap a\ntitle A\nissue #3\ntext why\n", "lists no difference"},
		{"a feature with trailing space", "section carveout a\ntitle A\ntext why\nmain func F() \n", "no space around it"},
		{"an empty feature", "section carveout a\ntitle A\ntext why\nmain\n", "needs a feature"},
		{"an empty file", "# only a comment\n", "no sections"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseCarveoutsString(t, tc.body)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want one containing %q", err, tc.want)
			}
		})
	}
	// A carve-out may be prose alone: a behavioural difference has no feature.
	if _, err := parseCarveoutsString(t, "section carveout a\ntitle A\ntext why\n"); err != nil {
		t.Errorf("a prose-only carve-out was refused: %v", err)
	}
	// A CRLF checkout reads as the file it is.
	crlf, err := parseCarveoutsString(t, strings.ReplaceAll(ok, "\n", "\r\n"))
	if err != nil || crlf[0].id != "a" || crlf[0].lines[0].feature != "func F()" {
		t.Errorf("a CRLF carve-out file: %+v, %v", crlf, err)
	}
}

func parseCarveoutsString(t *testing.T, body string) ([]*section, error) {
	t.Helper()
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"c.txt": body})
	return parseCarveouts(filepath.Join(dir, "c.txt"))
}

func TestParsePinsRefuses(t *testing.T) {
	const sha = "5e409641e3dfafb83227e02efee3d8a50ec5602c"
	const sha2 = "ad3ced172994940edaccfa3e52e2d76060e4e9c4"
	ok := "main " + sha + "\nrelease v1.0.0 " + sha2 + "\n"
	parse := func(body string) (*pins, error) {
		dir := t.TempDir()
		writeTree(t, dir, map[string]string{"p": body})
		return parsePins(filepath.Join(dir, "p"))
	}
	if p, err := parse("# c\n" + ok + "release v1.1.0 " + sha + "\n"); err != nil || p.main != sha || len(p.releases) != 2 {
		t.Fatalf("the valid fixture: %+v, %v", p, err)
	}
	if p, err := parse(strings.ReplaceAll(ok, "\n", "\r\n")); err != nil || p.main != sha {
		t.Errorf("a CRLF pin file: %+v, %v", p, err)
	}
	for _, tc := range []struct{ name, body, want string }{
		{"no main", "release v1.0.0 " + sha2 + "\n", "no main line"},
		{"no release", "main " + sha + "\n", "no release line"},
		{"two mains", ok + "main " + sha + "\n", "second main"},
		{"a short sha", "main 5e40964\nrelease v1.0.0 " + sha2 + "\n", "forty-hex"},
		{"an uppercase sha", "main " + strings.ToUpper(sha) + "\nrelease v1.0.0 " + sha2 + "\n", "forty-hex"},
		{"a v2 tag", ok + "release v2.0.0 " + sha + "\n", "not a release tag of this line"},
		{"a v0 tag", ok + "release v0.9.0 " + sha + "\n", "not a release tag of this line"},
		{"a release listed twice", ok + "release v1.0.0 " + sha + "\n", "listed twice"},
		{"a short release sha", "main " + sha + "\nrelease v1.0.0 ad3ced1\n", "forty-hex"},
		{"a tab", "main\t" + sha + "\nrelease v1.0.0 " + sha2 + "\n", "expected"},
		{"two spaces", "main  " + sha + "\nrelease v1.0.0 " + sha2 + "\n", "expected"},
		{"an unknown line", ok + "identical x\n", "expected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parse(tc.body)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

// TestRenderCompat: COMPAT.md says what each section lists, side by side, and
// the generated-file header a reader needs before editing it.
func TestRenderCompat(t *testing.T) {
	sections := carveoutFixture(t, "section carveout a\ntitle First\ntext One\ntext two.\ntext\ntext Next paragraph.\nmain func F()\ncompat func G()\n"+
		"section carveout b\ntitle Prose only\ntext Behaviour.\n"+
		"section dialect d\ntitle Dialect\ntext D.\ncompat type L struct\n")
	out := renderCompat(compatReport{
		mainPin:      "5e409641e3dfafb83227e02efee3d8a50ec5602c",
		mainFeatures: 7,
		releases:     []releaseReport{{tag: "v1.0.0", commit: "ad3ced172994940edaccfa3e52e2d76060e4e9c4", features: 3}},
		sections:     sections,
	})
	for _, want := range []string{
		"<!-- Generated by `make compat-md`",
		"### 1. First\n\nOne\ntwo.\n\nNext paragraph.\n",
		"Only on `main`:\n\n```text\nfunc F()\n```",
		"Only on this line:\n\n```text\nfunc G()\n```",
		"### 2. Prose only\n\nBehaviour.\n\nNo declaration differs.\n",
		"## The irreducible type-name delta\n\n### Dialect\n",
		"## Open gaps\n\nNone.",
		"(`ad3ced172994`): 3 features.",
		"(7 features on `main` at\n   that commit)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("COMPAT.md lacks %q:\n%s", want, out)
		}
	}
	gap := carveoutFixture(t, "section gap g\ntitle Unported\nissue #42\ntext G.\nmain func H()\n")
	if out := renderCompat(compatReport{sections: gap}); !strings.Contains(out, "### Unported ([#42](https://github.com/octoverse-id/octonomy-go/issues/42))") {
		t.Errorf("a gap's heading does not link its issue:\n%s", out)
	}
}
