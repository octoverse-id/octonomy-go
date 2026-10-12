package main

import (
	"fmt"
	"sort"
)

// diff is one feature present on one side of the parity comparison only.
type diff struct {
	side    string // "main" or "compat"
	feature string
}

func (d diff) String() string { return d.side + " " + d.feature }

// parityDiffs is CHECK A: main's exported surface against this line's, every
// importable package of each. A package one side lacks is one difference, its
// `package` line, rather than one per declaration in it -- the absence is the
// fact, and listing a whole package's contents under it would only make the
// carve-out file restate main.
func parityDiffs(main, compat *api) []diff {
	var out []diff
	for _, rel := range packageUnion(main, compat) {
		m, inMain := main.packages[rel]
		c, inCompat := compat.packages[rel]
		switch {
		case !inCompat:
			out = append(out, diff{"main", "package " + rel})
		case !inMain:
			out = append(out, diff{"compat", "package " + rel})
		default:
			for _, f := range minus(m, c) {
				out = append(out, diff{"main", f})
			}
			for _, f := range minus(c, m) {
				out = append(out, diff{"compat", f})
			}
		}
	}
	return out
}

func packageUnion(a, b *api) []string {
	seen := map[string]bool{}
	for rel := range a.packages {
		seen[rel] = true
	}
	for rel := range b.packages {
		seen[rel] = true
	}
	var rels []string
	for rel := range seen {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	return rels
}

// minus returns the lines of a that b lacks, in a's order.
func minus(a, b []string) []string {
	have := map[string]bool{}
	for _, f := range b {
		have[f] = true
	}
	var out []string
	for _, f := range a {
		if !have[f] {
			out = append(out, f)
		}
	}
	return out
}

// matchCarveouts holds every parity difference to the carve-out file, and the
// file to the differences. Both directions fail: a difference no section lists
// is a divergence nobody decided, and a listed difference that no longer
// exists is a carve-out that would keep excusing whatever later took its
// place.
func matchCarveouts(diffs []diff, sections []*section, file string) []string {
	listed := map[string]excepted{}
	for _, s := range sections {
		for _, e := range s.lines {
			listed[e.side+" "+e.feature] = e
		}
	}
	used := map[string]bool{}
	var findings []string
	for _, d := range diffs {
		if _, ok := listed[d.String()]; ok {
			used[d.String()] = true
			continue
		}
		where := "main has it and this line does not"
		if d.side == "compat" {
			where = "this line has it and main does not"
		}
		findings = append(findings, fmt.Sprintf(
			"parity: %s -- %s, and %s does not list it.\n    Port it, or add `%s` under the section that explains it.",
			d.feature, where, file, d))
	}
	for _, s := range sections {
		for _, e := range s.lines {
			if !used[e.side+" "+e.feature] {
				findings = append(findings, fmt.Sprintf(
					"parity: %s:%d lists `%s %s` under %s, and there is no such difference -- remove the line, or the section if it was the last.",
					file, e.line, e.side, e.feature, s.id))
			}
		}
	}
	return findings
}

// compatibilityFindings is CHECK B: every feature a published release of this
// line exported must still be here, unchanged. A line that changed shows as
// missing, which is what it is to a caller: the declaration they compiled
// against is gone. There is no exception mechanism, and that is the point --
// this line can never publish a major, so a break has no version to ride
// (AGENTS.md).
//
// Additions are not findings, and that is a limitation as well as a policy.
// An interface's whole method set is one line, so a method added to one does
// change it. But an added struct field breaks an unkeyed struct literal, which
// docs/versioning.md's MINOR rule already sets aside, and an added field or
// method can also make a selector AMBIGUOUS in a caller's struct that embeds
// this type beside another declaring the same name. No addition is safe from
// that, so a check that refused it would refuse every minor release; it is
// accepted here, as golang.org/x/exp/apidiff accepts it.
func compatibilityFindings(release string, rel, current *api) []string {
	var findings []string
	var rels []string
	for p := range rel.packages {
		rels = append(rels, p)
	}
	sort.Strings(rels)
	for _, p := range rels {
		cur, ok := current.packages[p]
		if !ok {
			findings = append(findings, fmt.Sprintf(
				"compatibility: %s exports %s, and this tree has no such package -- every caller importing it stops compiling.",
				release, relDisplay(p)))
			continue
		}
		for _, f := range minus(rel.packages[p], cur) {
			findings = append(findings, fmt.Sprintf(
				"compatibility: %s exports `%s`, and this tree does not -- a caller that compiled against %s would not compile against it.",
				release, f, release))
		}
		for _, o := range minus(rel.orders[p], current.orders[p]) {
			findings = append(findings, fmt.Sprintf(
				"compatibility: in %s, `%s`, and no longer here -- a caller converting between that struct and one of the same fields would not compile.",
				release, o))
		}
	}
	return findings
}

// featureCount is how many features an api has, across its packages.
func featureCount(a *api) int {
	n := 0
	for _, fs := range a.packages {
		n += len(fs)
	}
	return n
}
