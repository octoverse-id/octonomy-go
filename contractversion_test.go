package octonomy

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// --- the guard over which server contract this repository claims to vendor ----
//
// Ported from main's contractversion_test.go (#86, landed in #106) by #90, which
// is also the refresh that moved this line's contract from 1.0.0 to the release
// the marker in docs/versioning.md names. The order was deliberate: the marker
// first, then this guard, then the prose -- so that THIS is what reported the
// refresh complete, rather than a reviewer.
//
// That order is the lesson of #84. On main it refreshed the contract to 3.2.1
// and its first pass moved eight of fourteen prose sites and missed six, with
// every gate green; a reviewer and an independent outside pass each found the
// same six. When #90 was scoped this branch had less protection than main had
// then: fourteen prose mentions across seven files, no marker, and no contract
// gate at all.
//
// # Why this is an INVERSE registry
//
// The obvious check -- "no stale version anywhere in tracked files" -- is worse
// than useless. Most version tokens in this tree are CORRECT and are not about
// the vendored contract: the Go 1.13 toolchain, this module's own v1.0.0
// release, the harness pin, the version fixtures of the release-line guard, a
// behaviour a server release added. A checker that cannot tell those from a
// stale claim either fires on all of them or, tuned to silence them, stops
// catching the real thing.
//
// So the predicate is not "mentions a version". It is "names the contract this
// SDK currently vendors", and the two are not the same.
//
// The shape that fits is docs/contract-coverage.yaml's: fail closed, and turn
// "not the current contract" into a decision with a reason attached. Every
// version token in a scoped file must EITHER equal the recorded marker, OR match
// a category in contractVersionExemptions, which carries a written reason. A
// token that is neither fails, naming the file, the line, and both readings.
//
// A registry of sites that MUST match is the weaker form and is deliberately
// refused: it catches drift in the sites someone remembered to register and is
// silent on the one nobody did, which is the failure that actually happened.
//
// # How this copy differs from main's, and why
//
//   - Categories are this tree's, not main's. A category that matches nothing
//     here is a loosening with no site to justify it, so main's
//     dated-decision-record has no counterpart: nothing on this branch needs it.
//     A port that brings in prose needing one brings the category with it, the
//     way AGENTS.md's "port a rule with the code it governs" asks.
//   - main's compat-line-contract category would INVERT here, into one exempting
//     mentions of main's contract. It is absent for the same reason: this branch
//     describes main with a link rather than a restatement (#69), so there is no
//     mention of main's contract to exempt.
//   - release-line-guard is new. scripts/compat-guard.sh and its fixture suite
//     exist only on this line, and every version in them is this module's own.
//   - sdk-version is sharper here than on main, because this line's own first
//     release and the contract it vendored until #90 are the SAME number, 1.0.0.
//     A by-value rule is not merely wrong on this branch; it cannot be written.
//
// # What this guard CANNOT do
//
//   - It is SYNTACTIC. It checks that an exemption exists and that its reason is
//     non-empty. It cannot check that the reason is TRUE. A wrong exemption
//     passes, and no amount of work here changes that.
//   - It cannot classify a new mention on its own. A contributor writing a new
//     sentence about an older server has to say which category it is. Making
//     that a decision rather than an omission is the whole of what this buys.
//   - Category patterns exempt by SHAPE, not per site. "probed against 3.1.0" is
//     a verification note by construction, so a new one needs no ceremony; a new
//     "both vendored at server 3.1.0" matches nothing and fails, which is the
//     case that matters. The cost is that a category written too loosely would
//     exempt a real defect, so each pattern is anchored to the words that make it
//     the category it claims to be.
//   - It says nothing about the two specs or the marker agreeing. On main
//     tools/contractdrift's checkRecordedVersion owns those three. This branch
//     has no tools/ -- porting the gate is #98 -- so the check lives in
//     contractbaseline_test.go instead, and this guard only READS the marker.
//     Duplicating it here would give two tests one job and let each assume the
//     other is doing it.

// contractVersionMarker is the one mechanized statement of the targeted contract.
// contractbaseline_test.go asserts it against both specs' info.version; this
// guard reads it as the value every prose claim is measured against.
var contractVersionMarker = regexp.MustCompile(`<!--\s*contract-version:\s*([0-9]+\.[0-9]+\.[0-9]+)\s*-->`)

// versionToken is three dotted numbers and NOTHING ELSE. Narrowing it to "3.x"
// would encode the server's current major into the guard and stop working the
// day the server ships 4.0.0 -- and this line vendored a 1.0.0 contract until
// #90, so the major has never been a reliable discriminator.
//
// It deliberately does NOT match a SemVer prerelease suffix. "3.2.1-or-newer" is
// valid prerelease SYNTAX and is English in prose: matching it swallowed the
// version-range caveat underneath and reported the whole phrase as an
// unclassified version.
//
// Nor does it see a v-prefixed tag. \b needs a word/non-word transition and "v1"
// has none, so v1.0.0 -- the spelling every mention of this module's releases
// uses -- is not a token at all. An unprefixed SDK version (the CHANGELOG entry
// named "1.0.0", version.go) is, and sdk-version classifies it by context.
var versionToken = regexp.MustCompile(`\b[0-9]+\.[0-9]+\.[0-9]+\b`)

// urlSpan matches a bare URL so a version can be tested for CONTAINMENT in it
// rather than mere proximity to one.
var urlSpan = regexp.MustCompile(`https?://[^\s)\]>"'` + "`" + `]+`)

// contractVersionExemption is one category of version mention that is NOT a
// claim about the contract this SDK currently vendors.
//
// Reason is required and is checked for emptiness: an exemption whose reason is
// blank is itself a finding, because the reason is the only thing separating a
// classified mention from an unexamined one.
type contractVersionExemption struct {
	Name   string
	Reason string
	// Match reports whether this occurrence is covered.
	Match func(site versionSite) bool
}

// versionSite is one occurrence of a version token, with enough context to
// classify it.
//
// Prev carries the PRECEDING lines, and it is load-bearing rather than
// convenience. Prose wraps: "probed against\n// 3.1.0, /health/ready echoes..."
// puts the phrase that makes it a verification note on a different line from the
// version it qualifies. A classifier that reads one line at a time reports every
// wrapped note as unclassified, and the fix a contributor reaches for is to
// loosen the category until it stops complaining -- which is how a guard stops
// catching anything.
type versionSite struct {
	Path  string
	Prev  string // the preceding lines, joined
	Line  string
	Token string
	Idx   int // byte offset of Token within Line
}

// before returns the text preceding the token, spanning into the previous lines.
func (s versionSite) before() string { return s.Prev + "\n" + s.Line[:s.Idx] }

// after returns the text following the token on its own line.
func (s versionSite) after() string { return s.Line[s.Idx+len(s.Token):] }

// maxInt and minInt stand in for the min and max builtins, which arrived in Go
// 1.21. A modern toolchain compiles those builtins under `go 1.13` only because
// it enforces the language version it is told to; go1.13.15 does not have them.
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// containsAny reports whether line holds any of the given lowercase phrases.
func containsAny(line string, phrases ...string) bool {
	lower := strings.ToLower(line)
	for _, p := range phrases {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// precededBy reports whether any phrase appears within window bytes before the
// token, spanning into the previous lines. Anchoring on proximity rather than on
// the whole file keeps one sentence from exempting an unrelated version further
// down.
func (s versionSite) precededBy(window int, phrases ...string) bool {
	b := s.before()
	return containsAny(b[maxInt(len(b)-window, 0):], phrases...)
}

// followedBy reports whether any phrase appears within window bytes after the
// token. Version-range caveats qualify from either side -- "before 3.2.1" and
// "3.2.1 and newer" are the same category read from opposite ends.
func (s versionSite) followedBy(window int, phrases ...string) bool {
	a := s.after()
	return containsAny(a[:minInt(window, len(a))], phrases...)
}

// contractVersionExemptions is the registry. Order does not matter; the first
// match wins and its name is reported when a test asks why a token passed.
//
// THE HARNESS PIN IS A CATEGORY OF ITS OWN, AND THAT IS LOAD-BEARING. The
// container the smoke test runs against is pinned independently of the vendored
// contract. On main the two were 3.1.0 and 3.2.0 simultaneously until #84; on
// this branch #90 moved the contract and left the pin where it was. A guard that
// let the pin pass by EQUALITY would fail here outright, and on a branch where
// the two happen to agree it would look correct and break the next time one
// moved without the other. So the pin is matched by ROLE -- the image reference
// it sits in -- and never by value. TestHarnessPinIsExemptByRoleNotByValue pins
// that distinction on a value that is neither.
var contractVersionExemptions = []contractVersionExemption{
	{
		Name:   "ipv4-address",
		Reason: "Not a version at all. 127.0.0.1 and 0.0.0.0 contain a three-dotted-number substring; an address is identified by a fourth octet on either side.",
		Match: func(s versionSite) bool {
			return strings.HasSuffix(s.before(), ".") ||
				regexp.MustCompile(`^\.[0-9]`).MatchString(s.after())
		},
	},
	{
		Name:   "harness-image-pin",
		Reason: "The container floor for the smoke test, deliberately independent of the vendored contract. Matched by the image reference it sits in, never by value: #90 moved this line's contract and not its pin.",
		Match: func(s versionSite) bool {
			return strings.Contains(s.Line, "octonomy:"+s.Token) ||
				s.precededBy(60, "harness_image", "harness image", "ghcr.io/")
		},
	},
	{
		Name:   "verification-note",
		Reason: "Records behaviour observed against a running server of that version. Re-pointing it at a newer server would assert a probe nobody ran.",
		Match: func(s versionSite) bool {
			// "probed" and "verified" are the discriminators, and they must be
			// PRESENT -- a bare "against" is not one. "The SDK is written against
			// server 3.1.0" is a stale CLAIM, and a revision of main's copy of this
			// category exempted it, which is the category-too-loose failure this
			// guard's own doc comment warns about, committed in the guard itself.
			return s.precededBy(90,
				"probed", "verified", "observed against", "reproduced against",
				"against a running", "against a live", "captured from a live") ||
				s.followedBy(40, " was verified", " container", " harness", " server, where", " on postgres")
		},
	},
	{
		Name:   "version-range-caveat",
		Reason: "Names the servers on one side of a behaviour change. The boundary is a fact about those releases and does not move when the vendored contract does.",
		Match: func(s versionSite) bool {
			return s.precededBy(30,
				"pre-", "before", "older than", "and older", "≤", "<=", "as of the", "from server",
				"since", "pointed at", "post-", "on a server older than") ||
				s.followedBy(26,
					" and newer", " and older", " or newer", " or older", "-or-newer", "-or-older",
					"** and", "` and", "** or", "` or", "+ ", " which predates", ", which predates")
		},
	},
	{
		Name:   "server-history",
		Reason: "Narrates what the server shipped when, or which contract this SDK sat on before a refresh. The reason a mechanism exists, not a claim about what is vendored now.",
		Match: func(s versionSite) bool {
			// " added" is anchored on what FOLLOWS the token: "Server 3.1.0 added
			// 409 scope_immutable" is a release adding a behaviour. It does not
			// reach "vendored at server 3.1.0", which is followed by nothing of
			// the kind.
			return s.precededBy(110,
				"shipped", "the server was", "while the server", "added the", "fixed it", "fixed the",
				"had no", "sat on", "sit on", "came to sit", "was written against", "moved",
				"drifted", "pinned at server", "predates", "server changelog") ||
				s.followedBy(60, " added ", " contract while", " contract,", " and had drifted", " refresh says", " refresh")
		},
	},
	{
		Name:   "go-toolchain-version",
		Reason: "A Go toolchain or language version, not a server contract version.",
		Match: func(s versionSite) bool {
			return s.precededBy(40, "go ", "go1.", "golang", "go-version", "toolchain", "gotoolchain") ||
				strings.HasPrefix(s.Token, "1.13.") || strings.HasPrefix(s.Token, "1.24.") || strings.HasPrefix(s.Token, "1.25.")
		},
	},
	{
		Name:   "sdk-version",
		Reason: "A version of THIS module, not of the server contract. The SDK versions independently of the server (docs/versioning.md), so its numbers never track the marker.",
		Match: func(s versionSite) bool {
			if s.Path == "version.go" {
				return true
			}
			// A CHANGELOG release heading -- "## [1.0.0]" -- names a release of
			// this module by construction: the CHANGELOG versions nothing else. By
			// SHAPE, anchored on the bracket immediately before the token, so a
			// heading nearby does not exempt a claim further along the line.
			if strings.HasSuffix(s.before(), "## [") {
				return true
			}
			// BY CONTEXT, NEVER BY VALUE, and on this branch there is no other
			// way to write it. The line's own first release is v1.0.0 and the
			// contract it vendored until #90 was info.version 1.0.0: the same
			// three numbers, meaning two unrelated things. A rule exempting
			// "1.0.0" would have waved through every stale contract claim this
			// guard was ported to catch. main's copy made the by-value mistake on
			// 2.0.0 before an outside review found it; here it is unwriteable.
			return s.precededBy(80,
				"octonomy-go", "sdk version", "version =", "`version`", "user-agent", "useragent",
				"semver", "changelog heading", "release/v", "tagged", "this module",
				"version constant", "constant read", "released entr",
				"prerelease", "bump", "alpha", "rc.") ||
				s.followedBy(40, " minor", " major", " patch", " prerelease", "-alpha", "-rc", " tag", "` entry")
		},
	},
	{
		Name:   "release-line-guard",
		Reason: "scripts/compat-guard.sh and its fixture suite exist only on this line and read go.mod, version.go, the CHANGELOG heading and the tag -- never a server contract. Every version in them is this module's own, or a fixture of one.",
		Match: func(s versionSite) bool {
			return s.Path == "scripts/compat-guard.sh" || s.Path == "scripts/compat-guard-test.sh"
		},
	},
	{
		Name:   "external-reference",
		Reason: "A version inside a third-party URL or spec identifier (Keep a Changelog, SemVer, the OpenAPI format version).",
		Match: func(s versionSite) bool {
			// The token must sit INSIDE the URL, not merely after one on the same
			// line. A revision of main's copy searched the preceding 60 bytes for
			// "https://", so "[Contract](https://example.com/spec) is vendored at
			// server 3.1.0" was exempted by a link that had nothing to do with the
			// version.
			for _, span := range urlSpan.FindAllStringIndex(s.Line, -1) {
				if s.Idx >= span[0] && s.Idx+len(s.Token) <= span[1] {
					return true
				}
			}
			return s.precededBy(24, "openapi: ", "openapi version")
		},
	},
	{
		Name:   "gate-test-fixture",
		Reason: "A literal inside a guard's own fixtures, which must name versions the repository does not vendor in order to prove the guard can fail. Covers this file and contractbaseline_test.go: neither can both demonstrate a stale claim and forbid writing one.",
		Match: func(s versionSite) bool {
			return s.Path == "contractversion_test.go" || s.Path == "contractbaseline_test.go"
		},
	},
}

// contractVersionSkipDirs are directories whose contents are out of scope
// wholesale, each for a stated reason rather than because it was inconvenient.
var contractVersionSkipDirs = map[string]string{
	".git":         "not tracked content",
	"docs/designs": "dated design records. A design doc describes the state at the moment it was written, exactly as a released CHANGELOG entry does, and editing one to track the contract would falsify the record it exists to be.",
	"code-review":  "local review-pipeline artifacts. AGENTS.md reserves the directory for them and forbids committing any, so a finding quoting a stale version would fail this guard on the reviewer's machine and nowhere else.",
}

// contractVersionSkipFiles are individual files out of scope, with reasons.
var contractVersionSkipFiles = map[string]string{
	"docs/openapi.yaml":    "generated from the server, and its info.version is one of the three things contractbaseline_test.go already asserts against the marker.",
	"docs/openapi-v2.yaml": "generated from the server, and its info.version is one of the three things contractbaseline_test.go already asserts against the marker.",
}

// inScope reports whether a path is scanned, and why not when it is not.
func inScope(path string) (bool, string) {
	if reason, ok := contractVersionSkipFiles[path]; ok {
		return false, reason
	}
	for dir, reason := range contractVersionSkipDirs {
		if path == dir || strings.HasPrefix(path, dir+"/") {
			return false, reason
		}
	}
	switch filepath.Ext(path) {
	case ".md", ".go", ".yml", ".yaml", ".sh":
		return true, ""
	}
	if filepath.Base(path) == "Makefile" {
		return true, ""
	}
	return false, "not a prose or source file this guard reads"
}

// changelogHistoryStart returns the line number (1-based) of the first RELEASED
// heading in CHANGELOG.md. Everything from there down is history and out of
// scope wholesale, on the #49 / #77 precedent: a released entry describes what
// shipped, and editing it to track the current contract would rewrite the
// record. [Unreleased] is above it and IS in scope, because it describes the
// release being prepared now.
func changelogHistoryStart(body string) int {
	for i, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "## [") && !strings.HasPrefix(line, "## [Unreleased]") {
			return i + 1
		}
	}
	return 0 // no released heading yet: the whole file is in scope
}

// readContractVersionMarker returns the version recorded in docs/versioning.md.
func readContractVersionMarker(t *testing.T) string {
	t.Helper()
	raw, err := ioutil.ReadFile(filepath.Join("docs", "versioning.md"))
	if err != nil {
		t.Fatalf("read docs/versioning.md: %v", err)
	}
	found := contractVersionMarker.FindAllStringSubmatch(string(raw), -1)
	switch len(found) {
	case 0:
		t.Fatal("docs/versioning.md carries no <!-- contract-version: X.Y.Z --> marker. " +
			"It is the value every prose claim in this repository is measured against, and " +
			"contractbaseline_test.go asserts it against both specs' info.version.")
	case 1:
	default:
		t.Fatalf("docs/versioning.md carries %d contract-version markers; exactly one is the "+
			"whole point of it being the single recorded value", len(found))
	}
	return found[0][1]
}

// contractVersionFinding is one token the guard could not classify.
type contractVersionFinding struct {
	Path  string
	Line  int
	Token string
	Text  string
}

// scanContractVersions walks the repository and returns every version token that
// neither equals the marker nor matches a registered exemption.
//
// filepath.Walk rather than WalkDir: WalkDir and io/fs arrived in Go 1.16, and
// this file has to compile on go1.13.15.
func scanContractVersions(t *testing.T, marker string) []contractVersionFinding {
	t.Helper()
	var findings []contractVersionFinding

	err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		path = filepath.ToSlash(path)
		if info.IsDir() {
			if path == "." {
				return nil
			}
			if _, skipped := contractVersionSkipDirs[path]; skipped {
				return filepath.SkipDir
			}
			return nil
		}
		if ok, _ := inScope(path); !ok {
			return nil
		}
		raw, readErr := ioutil.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		findings = append(findings, scanContractVersionsIn(path, string(raw), marker)...)
		return nil
	})
	if err != nil {
		t.Fatalf("walk the repository: %v", err)
	}
	return findings
}

// scanContractVersionsIn classifies every version token in one file's body.
func scanContractVersionsIn(path, body, marker string) []contractVersionFinding {
	var findings []contractVersionFinding

	historyFrom := 0
	if path == "CHANGELOG.md" {
		historyFrom = changelogHistoryStart(body)
	}

	lines := strings.Split(body, "\n")
	for i, line := range lines {
		lineNo := i + 1
		if historyFrom > 0 && lineNo >= historyFrom {
			break
		}
		// Two lines of look-back. Prose wraps, and the phrase that classifies a
		// mention is regularly on the line above it.
		prev := strings.Join(lines[maxInt(i-2, 0):i], "\n")
		for _, m := range versionToken.FindAllStringIndex(line, -1) {
			token := line[m[0]:m[1]]
			if token == marker {
				continue
			}
			site := versionSite{Path: path, Prev: prev, Line: line, Token: token, Idx: m[0]}
			if classifyContractVersion(site) != "" {
				continue
			}
			findings = append(findings, contractVersionFinding{
				Path: path, Line: lineNo, Token: token, Text: strings.TrimSpace(line),
			})
		}
	}
	return findings
}

// classifyContractVersion returns the name of the first exemption covering this
// occurrence, or "" when none does.
func classifyContractVersion(site versionSite) string {
	for _, ex := range contractVersionExemptions {
		if ex.Match(site) {
			return ex.Name
		}
	}
	return ""
}

// siteIn builds a versionSite for a token on a single line, for the guard's own
// tests. Production scanning supplies Prev; these cases are deliberately
// single-line, because a category that needs look-back to reject a stale claim
// would be a category that rejects nothing when the claim fits on one line.
func siteIn(path, line, token string) versionSite {
	return versionSite{Path: path, Line: line, Token: token, Idx: strings.Index(line, token)}
}

// Every version this repository writes down is either the contract it vendors or
// a classified exception.
//
// It fails closed: a version token that matches no category is a finding,
// reported with both readings so the contributor chooses rather than guesses.
func TestEveryContractVersionMentionIsCurrentOrExempt(t *testing.T) {
	marker := readContractVersionMarker(t)

	findings := scanContractVersions(t, marker)
	if len(findings) == 0 {
		return
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d version mention(s) are neither the recorded contract (%s) nor a registered exemption:\n\n",
		len(findings), marker)
	for _, f := range findings {
		text := f.Text
		if len(text) > 120 {
			text = text[:117] + "..."
		}
		fmt.Fprintf(&b, "  %s:%d  %s\n      %s\n", f.Path, f.Line, f.Token, text)
	}
	b.WriteString("\nEach one is one of two things, and only you can say which:\n")
	fmt.Fprintf(&b, "  (a) It names the contract this SDK vendors, and is now STALE. Change it to %s.\n", marker)
	b.WriteString("  (b) It is deliberately about another version -- a verification note, a version-range\n" +
		"      caveat, server history, the harness pin, a toolchain or SDK version. Add it to\n" +
		"      contractVersionExemptions in contractversion_test.go, WITH A REASON.\n\n" +
		"On main, #84 moved eight of fourteen such sites and missed six, with every gate green.\n" +
		"This guard exists so the sixth is a decision rather than an omission.")
	t.Error(b.String())
}

// Every exemption carries a reason, because the reason is the only thing
// separating a classified mention from an unexamined one.
func TestEveryContractVersionExemptionHasAReason(t *testing.T) {
	seen := map[string]bool{}
	for _, ex := range contractVersionExemptions {
		if strings.TrimSpace(ex.Name) == "" {
			t.Error("an exemption has no name; findings report the name that let a token pass")
		}
		if len(strings.TrimSpace(ex.Reason)) < 30 {
			t.Errorf("exemption %q has no usable reason. This guard cannot check that a reason is "+
				"TRUE, so a reason that says nothing is the same as no classification at all", ex.Name)
		}
		if ex.Match == nil {
			t.Errorf("exemption %q has no Match func", ex.Name)
		}
		if seen[ex.Name] {
			t.Errorf("two exemptions are named %q; the name is what a finding reports", ex.Name)
		}
		seen[ex.Name] = true
	}
	for path, reason := range contractVersionSkipFiles {
		if len(strings.TrimSpace(reason)) < 30 {
			t.Errorf("skipped file %q has no usable reason", path)
		}
	}
	for dir, reason := range contractVersionSkipDirs {
		if dir == ".git" {
			continue
		}
		if len(strings.TrimSpace(reason)) < 30 {
			t.Errorf("skipped directory %q has no usable reason", dir)
		}
	}
}

// The harness pin is exempt by ROLE, never by value.
//
// scripts/octonomy-harness.sh and the composite action pin a container floor
// that is deliberately independent of the vendored contract. A by-value
// implementation -- "a pin equal to the marker passes" -- would fail on this
// branch's real tree, since #90 moved the contract and not the pin; but on a
// tree where the two happen to agree it is indistinguishable from a by-role one.
// So the distinction is pinned on a synthetic value that is neither.
func TestHarnessPinIsExemptByRoleNotByValue(t *testing.T) {
	const pinned = "9.9.9" // deliberately not the marker, and not any real release

	line := `HARNESS_IMAGE="${OCTONOMY_HARNESS_IMAGE:-ghcr.io/octoverse-id/octonomy:` + pinned + `}"`
	if got := classifyContractVersion(siteIn("scripts/octonomy-harness.sh", line, pinned)); got != "harness-image-pin" {
		t.Errorf("a harness pin that does NOT equal the marker must still be exempt by role, got %q.\n"+
			"An implementation that exempts the pin by comparing it to the contract version passes "+
			"whenever the two agree and fails the moment one moves without the other.", got)
	}

	// The converse: the same number in a sentence CLAIMING what is vendored must
	// not inherit the pin's exemption.
	claim := "The two specs are vendored at server " + pinned + "."
	if got := classifyContractVersion(siteIn("docs/api.md", claim, pinned)); got != "" {
		t.Errorf("a claim about the vendored contract must not be exempt, got %q", got)
	}
}

// The guard can fail, and these are the shapes it must catch.
//
// Assert the finding, not just the green path. A guard nobody has seen fail is a
// guard nobody should trust.
func TestContractVersionGuardCatchesAStaleClaim(t *testing.T) {
	cases := []struct {
		name     string
		path     string
		prev     string
		line     string
		token    string
		exempt   bool
		whatItIs string
	}{
		{
			name:     "this line's own pre-#90 claim",
			path:     "doc.go",
			line:     "// This SDK targets the stable v1 API (server release 1.0.0) served under /api/v1.",
			token:    "1.0.0",
			exempt:   false,
			whatItIs: "the doc.go sentence #90 had to move; 1.0.0 is also this module's first release, which is the trap",
		},
		{
			name:     "this line's own pre-#90 table row",
			path:     "docs/versioning.md",
			line:     "| **Targeted server contract** | ... | (currently **v1**, server `1.0.0`). |",
			token:    "1.0.0",
			exempt:   false,
			whatItIs: "a backtick before the token must not read as an SDK-version context",
		},
		{
			name:     "a stale vendored-at claim",
			path:     "docs/api.md",
			line:     "`openapi-v2.yaml` — `/api/v2`, server **3.1.0**. The default surface.",
			token:    "3.1.0",
			exempt:   false,
			whatItIs: "the exact shape #84 missed six times on main",
		},
		{
			name:     "a stale both-vendored-at claim",
			path:     "AGENTS.md",
			line:     "both vendored at server **3.1.0**. Read the v2 spec when adding a resource.",
			token:    "3.1.0",
			exempt:   false,
			whatItIs: "the AGENTS.md site, which instructs every future contributor",
		},
		{
			name:     "a stale claim naming the harness in the same breath",
			path:     "docs/development.md",
			line:     "The vendored contract is 3.1.0, the same release the smoke test runs.",
			token:    "3.1.0",
			exempt:   false,
			whatItIs: "harness words AFTER the token, but not the ones verification-note anchors on",
		},
		{
			name:     "a verification note",
			path:     "transport.go",
			line:     "// there. Probed against 3.1.0, the query value persists on a namespaced read.",
			token:    "3.1.0",
			exempt:   true,
			whatItIs: "records a probe that was actually run against that version",
		},
		{
			name:     "a version-range caveat",
			path:     "README.md",
			line:     "Against **3.2.0 and older**, treat a tags walk as best-effort.",
			token:    "3.2.0",
			exempt:   true,
			whatItIs: "names the servers lacking the ORDER BY; the boundary does not move",
		},
		{
			name:     "server history: a release adding a behaviour",
			path:     "README.md",
			line:     "- **No `CodeScopeImmutable` constant.** Server 3.1.0 added `409 scope_immutable` on tag,",
			token:    "3.1.0",
			exempt:   true,
			whatItIs: "which release introduced the 409 is a fact about that release",
		},
		{
			name:     "a verification note that WRAPPED onto the next line",
			path:     "transport.go",
			line:     "// 3.1.0).",
			prev:     "// (verified across tags, vocabularies, aliases, and assignments on server",
			token:    "3.1.0",
			exempt:   true,
			whatItIs: "prose wraps; the phrase that classifies a mention is regularly on the line above",
		},
		{
			name:     "a stale claim that wrapped is still NOT exempt",
			path:     "docs/api.md",
			line:     "server **3.1.0**. The default surface.",
			prev:     "Both specs are vendored from the Octonomy server and both track",
			token:    "3.1.0",
			exempt:   false,
			whatItIs: "look-back must not become a way for any nearby sentence to exempt a claim",
		},
		{
			name:     "a stale claim using bare 'against'",
			path:     "docs/api.md",
			line:     "The SDK is written against server 3.1.0.",
			token:    "3.1.0",
			exempt:   false,
			whatItIs: "verification-note once accepted a bare 'against'; only 'probed'/'verified' establish a probe",
		},
		{
			name:     "a stale claim whose digits are this line's first release",
			path:     "docs/api.md",
			line:     "The spec is vendored at server 1.0.0.",
			token:    "1.0.0",
			exempt:   false,
			whatItIs: "sdk-version must classify by CONTEXT; on this branch 1.0.0 is both the release and the old contract",
		},
		{
			name:     "a stale claim on a line that also has a link",
			path:     "docs/api.md",
			line:     "[Contract](https://example.com/spec) is vendored at server 3.1.0.",
			token:    "3.1.0",
			exempt:   false,
			whatItIs: "external-reference once matched a URL merely NEAR the token; the token must sit inside it",
		},
		{
			name:     "a stale claim in a file the release-line guard does not own",
			path:     "scripts/octonomy-harness.sh",
			line:     "# The contract this harness serves is 3.1.0.",
			token:    "3.1.0",
			exempt:   false,
			whatItIs: "release-line-guard is scoped to the two compat-guard files by path, and to nothing beside them",
		},
		{
			name:     "a version genuinely inside a URL",
			path:     "CHANGELOG.md",
			line:     "based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),",
			token:    "1.1.0",
			exempt:   true,
			whatItIs: "containment is the test, and it must still accept the real case",
		},
		{
			name:     "the harness pin",
			path:     "scripts/octonomy-harness.sh",
			line:     `HARNESS_IMAGE="${OCTONOMY_HARNESS_IMAGE:-ghcr.io/octoverse-id/octonomy:3.1.0}"`,
			token:    "3.1.0",
			exempt:   true,
			whatItIs: "a container floor, a different number by role",
		},
		{
			name:     "the released CHANGELOG entry this line corrected",
			path:     "docs/versioning.md",
			line:     "heading for a release that was never cut, and that label is corrected in the `1.0.0` entry rather than",
			token:    "1.0.0",
			exempt:   true,
			whatItIs: "an SDK release named by its CHANGELOG entry, the same digits as the old contract",
		},
		{
			name:     "a CHANGELOG release heading quoted in prose",
			path:     "docs/versioning.md",
			line:     "were no git tags at all and the module proxy had served nothing; `CHANGELOG.md` carried a `## [0.1.0]`",
			token:    "0.1.0",
			exempt:   true,
			whatItIs: "the CHANGELOG's headings are this module's releases and nothing else",
		},
		{
			name:     "a stale claim on a line that also quotes a heading",
			path:     "docs/versioning.md",
			line:     "The `## [1.1.0]` release is vendored at server 3.1.0.",
			token:    "3.1.0",
			exempt:   false,
			whatItIs: "the heading shape is anchored on the bracket right before the token, not anywhere on the line",
		},
		{
			name:     "a released CHANGELOG entry named in [Unreleased]",
			path:     "CHANGELOG.md",
			line:     "  - Released entries below are left as they were written, including the `1.0.0` guard description",
			token:    "1.0.0",
			exempt:   true,
			whatItIs: "an SDK release named by the entry that shipped it",
		},
		{
			name:     "a release-line guard fixture",
			path:     "scripts/compat-guard-test.sh",
			line:     `fixture "$COMPAT" 1.13 1.0.0 1.0.0`,
			token:    "1.0.0",
			exempt:   true,
			whatItIs: "the guard's fixtures name this module's versions, never a contract",
		},
	}

	for _, tc := range cases {
		tc := tc // Go 1.13: the loop variable is shared across iterations
		t.Run(tc.name, func(t *testing.T) {
			site := siteIn(tc.path, tc.line, tc.token)
			site.Prev = tc.prev
			got := classifyContractVersion(site)
			if tc.exempt && got == "" {
				t.Errorf("%s should be exempt (%s) but matched no category.\n  line: %s",
					tc.name, tc.whatItIs, tc.line)
			}
			if !tc.exempt && got != "" {
				t.Errorf("%s must NOT be exempt (%s) but matched %q.\n  line: %s\n"+
					"A category loose enough to swallow this is a category that stops catching the "+
					"thing this guard exists for.", tc.name, tc.whatItIs, got, tc.line)
			}
		})
	}
}

// The scanner itself reports what the classifier rejects, with the line number,
// and skips what equals the marker. Without this the classifier tests above
// could all pass over a scanner that never calls it.
func TestContractVersionScannerReportsTheUnclassifiedToken(t *testing.T) {
	body := "line one\nThe contract is vendored at server 8.8.8.\nIt is vendored at server 7.7.7 too.\n"
	got := scanContractVersionsIn("docs/api.md", body, "7.7.7")
	if len(got) != 1 {
		t.Fatalf("want exactly one finding (8.8.8 on line 2; 7.7.7 is the marker), got %d: %+v", len(got), got)
	}
	if got[0].Line != 2 || got[0].Token != "8.8.8" {
		t.Errorf("finding names the wrong site: %+v", got[0])
	}
}

// Released CHANGELOG entries are history, and the cut is the first released
// heading rather than a line number somebody maintains.
func TestChangelogHistoryIsOutOfScopeFromTheFirstReleasedHeading(t *testing.T) {
	body := "# Changelog\n\n## [Unreleased]\n\nvendored at server 9.9.9\n\n## [1.0.0] - 2026-08-21\n\nvendored at server 8.8.8\n"
	at := changelogHistoryStart(body)
	if at != 7 {
		t.Fatalf("history starts at the first released heading (line 7), got %d", at)
	}
	if got := scanContractVersionsIn("CHANGELOG.md", body, "7.7.7"); len(got) != 1 || got[0].Token != "9.9.9" {
		t.Errorf("[Unreleased] is in scope and released history is not; want only 9.9.9, got %+v", got)
	}

	// And a CHANGELOG with nothing released yet is entirely in scope, rather than
	// entirely skipped -- which is the direction that fails safe.
	if got := changelogHistoryStart("# Changelog\n\n## [Unreleased]\n\nvendored at server 9.9.9\n"); got != 0 {
		t.Errorf("with no released heading the whole file is in scope, got cut at %d", got)
	}
}

// The scope exclusions are decisions, so they are enumerated rather than implied.
func TestContractVersionScopeExclusionsAreDeliberate(t *testing.T) {
	for _, path := range []string{"docs/openapi.yaml", "docs/openapi-v2.yaml"} {
		if ok, reason := inScope(path); ok || reason == "" {
			t.Errorf("%s must be out of scope with a reason: contractbaseline_test.go already owns it", path)
		}
	}
	for _, path := range []string{"docs/designs/octonomy-3.1-upgrade-go113.md", "code-review/findings.md"} {
		if ok, _ := inScope(path); ok {
			t.Errorf("%s must be out of scope wholesale", path)
		}
	}
	for _, path := range []string{
		"docs/api.md", "AGENTS.md", "README.md", "doc.go", "transport.go", "docs/contract-coverage.yaml",
		"scripts/octonomy-harness.sh", ".github/actions/octonomy-harness/action.yml", "Makefile",
	} {
		if ok, _ := inScope(path); !ok {
			t.Errorf("%s carries contract claims and must be in scope", path)
		}
	}
}
