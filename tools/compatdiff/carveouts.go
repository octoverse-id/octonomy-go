package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// The carve-out file: every difference between main's exported surface and
// this line's, each under the section that explains it. See carveouts.txt's
// own header for the format; parseCarveouts is strict about it, because a line
// it misread would decide what a difference is allowed to be.

// sectionKind says what a section of the carve-out file is.
type sectionKind string

const (
	// kindCarveout is a deliberate difference: something this line will not
	// have, or has differently, by decision.
	kindCarveout sectionKind = "carveout"
	// kindDialect is the irreducible type-name delta: what main spells with
	// type parameters, this line spells per type, because Go 1.13 has none.
	kindDialect sectionKind = "dialect"
	// kindGap is a difference nobody decided: something main has that this
	// line has not ported yet. It names the issue that owns the port.
	kindGap sectionKind = "gap"
)

type section struct {
	kind  sectionKind
	id    string
	title string
	issue string   // a gap's owning issue, "#N"
	text  []string // prose lines; an empty string is a paragraph break
	lines []excepted
	line  int // where the section starts, for messages
}

// excepted is one difference a section accounts for: a feature line as it
// appears on one side only.
type excepted struct {
	side    string // "main" or "compat"
	feature string
	line    int
}

var sectionIDPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// parseCarveouts reads the carve-out file. It refuses rather than skips: an
// unknown keyword, a feature before any section, a section with no title, a
// gap with no issue, a section id or a feature listed twice. Each of those, read
// leniently, would let a difference through that nobody wrote down.
func parseCarveouts(file string) ([]*section, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var sections []*section
	var cur *section
	ids := map[string]int{}
	features := map[string]int{}
	for i, line := range strings.Split(string(raw), "\n") {
		n := i + 1
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		keyword, rest, _ := strings.Cut(line, " ")
		bad := func(format string, args ...any) error {
			return fmt.Errorf("%s:%d: %s", file, n, fmt.Sprintf(format, args...))
		}
		if keyword != "section" && cur == nil {
			return nil, bad("%q before the first section", keyword)
		}
		switch keyword {
		case "section":
			kind, id, ok := strings.Cut(rest, " ")
			if !ok {
				return nil, bad("expected `section <kind> <id>`, got %q", line)
			}
			switch sectionKind(kind) {
			case kindCarveout, kindDialect, kindGap:
			default:
				return nil, bad("unknown section kind %q (carveout, dialect or gap)", kind)
			}
			if !sectionIDPattern.MatchString(id) {
				return nil, bad("section id %q is not lowercase words joined by hyphens", id)
			}
			if prev, dup := ids[id]; dup {
				return nil, bad("section %s is declared twice (first at line %d)", id, prev)
			}
			ids[id] = n
			if err := finish(file, cur); err != nil {
				return nil, err
			}
			cur = &section{kind: sectionKind(kind), id: id, line: n}
			sections = append(sections, cur)
		case "title":
			if cur.title != "" {
				return nil, bad("section %s has a second title", cur.id)
			}
			if strings.TrimSpace(rest) == "" {
				return nil, bad("an empty title")
			}
			cur.title = rest
		case "issue":
			if cur.kind != kindGap {
				return nil, bad("only a gap names an issue; %s is a %s", cur.id, cur.kind)
			}
			if !regexp.MustCompile(`^#[1-9][0-9]*$`).MatchString(rest) {
				return nil, bad("an issue is written #N, got %q", rest)
			}
			if cur.issue != "" {
				return nil, bad("section %s names a second issue", cur.id)
			}
			cur.issue = rest
		case "text":
			cur.text = append(cur.text, rest)
		case "main", "compat":
			if rest == "" || strings.TrimSpace(rest) != rest {
				return nil, bad("a %s line needs a feature exactly as the tool prints it, with no space around it", keyword)
			}
			key := keyword + " " + rest
			if prev, dup := features[key]; dup {
				return nil, bad("%q is listed twice (first at line %d)", key, prev)
			}
			features[key] = n
			cur.lines = append(cur.lines, excepted{side: keyword, feature: rest, line: n})
		default:
			return nil, bad("unknown keyword %q (section, title, issue, text, main or compat)", keyword)
		}
	}
	if err := finish(file, cur); err != nil {
		return nil, err
	}
	if len(sections) == 0 {
		return nil, fmt.Errorf("%s has no sections", file)
	}
	return sections, nil
}

// finish checks a section once its last line has been read.
func finish(file string, s *section) error {
	if s == nil {
		return nil
	}
	switch {
	case s.title == "":
		return fmt.Errorf("%s:%d: section %s has no title", file, s.line, s.id)
	case s.kind == kindGap && s.issue == "":
		return fmt.Errorf("%s:%d: gap %s names no issue -- a gap nobody owns is not recorded, it is forgotten", file, s.line, s.id)
	case len(s.text) == 0:
		return fmt.Errorf("%s:%d: section %s has no text -- COMPAT.md would list the difference without saying why", file, s.line, s.id)
	case s.kind != kindCarveout && len(s.lines) == 0:
		// Only a carve-out may be prose alone: some of them change behaviour
		// and no declaration (the JSON depth guards). A dialect section or a
		// gap is BY DEFINITION a difference in declarations, so one with none
		// listed accounts for nothing and would only ever be stale.
		return fmt.Errorf("%s:%d: %s %s lists no difference", file, s.line, s.kind, s.id)
	}
	return nil
}
