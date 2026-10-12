package main

import (
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestTheCarveOutsAreTheEpicsDefinitionOfDone holds the real carve-out file to
// what #88 says COMPAT.md may contain when the epic is done: the four
// deliberate differences #100 names -- no webhook receiver, PATCH cannot clear
// a nullable field, the /api/v1 default, the Go 1.13-only JSON depth guards --
// and the irreducible type-name delta. Nothing else, and no gap.
//
// `make compat-diff` proves the file matches the trees; this proves the file
// says what the epic decided. A new section fails here until this list
// changes in the same pull request, so admitting one -- a gap recorded while
// main's pin advances, say -- is a decision someone writes down rather than a
// line that slipped into a data file.
func TestTheCarveOutsAreTheEpicsDefinitionOfDone(t *testing.T) {
	sections, err := parseCarveouts("carveouts.txt")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range sections {
		got = append(got, string(s.kind)+" "+s.id)
	}
	sort.Strings(got)
	want := []string{
		"carveout default-api-version",
		"carveout json-depth-guards",
		"carveout no-webhook-receiver",
		"carveout update-fields-cannot-clear",
		"dialect generics",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("carveouts.txt sections:\n%s\nwant the epic's definition of done:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// And each section lists only what its reason covers. Matching is by side
	// and feature, so a difference moved to another section would still pass
	// `make compat-diff` -- and COMPAT.md would then explain it with the wrong
	// reason. Each pattern is the shape of that section's features.
	shape := map[string]*regexp.Regexp{
		"no-webhook-receiver": regexp.MustCompile(`^main package webhook$`),
		"default-api-version": regexp.MustCompile(`^(main|compat) const DefaultAPIVersion APIVersion = "v[12]"$`),
		"json-depth-guards":   regexp.MustCompile(`^$`), // prose only: no line matches
		"update-fields-cannot-clear": regexp.MustCompile(`^(main func (Null|Set)\[` +
			`|main type Optional\[` +
			`|main method \(\*?Optional\[` +
			`|(main|compat) type (Tag|Vocabulary|TagAlias)Update struct, ` +
			`|compat method \(\*?(Tag|Vocabulary|TagAlias)Update\) MarshalJSON\(\) )`),
		"generics": regexp.MustCompile(`^((main|compat) func (Each|DecodeMetadata)[\[(]` +
			`|main type List\[` +
			`|compat type \w+List struct` +
			`|compat type Page interface` +
			`|compat type \*\w+List implements Page$` +
			`|(main|compat) method \(\*\w+Service\) List\w*\()`),
	}
	for _, s := range sections {
		re, ok := shape[s.id]
		if !ok {
			continue // the section list above already failed
		}
		for _, e := range s.lines {
			if line := e.side + " " + e.feature; !re.MatchString(line) {
				t.Errorf("carveouts.txt:%d: %q is listed under %s, which does not explain it", e.line, line, s.id)
			}
		}
	}
}

// TestTheFirstReleaseIsNeverDropped: Check B holds the tree to every release
// the pin file lists, so deleting a line would quietly stop holding it to that
// release -- and a release, once published, has callers forever. A later
// release ADDS a line; this one never leaves. `make compat-diff` proves the
// commit is the tag's.
func TestTheFirstReleaseIsNeverDropped(t *testing.T) {
	p, err := parsePins("baselines.pin")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range p.releases {
		if r == (releasePin{"v1.0.0", "ad3ced172994940edaccfa3e52e2d76060e4e9c4"}) {
			return
		}
	}
	t.Errorf("baselines.pin does not hold this line to v1.0.0 at ad3ced17: %+v", p.releases)
}
