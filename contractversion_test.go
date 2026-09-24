package octonomy

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// --- the guard over which server contract this repository claims to vendor ----
//
// Fourteen places in this repository state which server contract the SDK vendors.
// `make contract-check` checks THREE of them -- the two specs' info.version and
// the <!-- contract-version: --> marker in docs/versioning.md, all three via
// tools/contractdrift's checkRecordedVersion. The rest are prose, and before
// this guard nothing checked them at all (#86).
//
// That is not hypothetical. #84 refreshed the contract to 3.2.1 and its first
// pass moved eight of the fourteen and missed six -- docs/api.md x2,
// docs/architecture.md, docs/development.md, docs/roadmap.md x2. Every gate
// stayed green. A reviewer and an independent outside pass each found the same
// six; had neither looked, the repository would have merged with its API
// reference and its contributor instructions naming a contract it no longer
// vendored.
//
// # Why this is an INVERSE registry
//
// The obvious check -- "no stale version anywhere in tracked files" -- is worse
// than useless here. Of the 3.2.0 mentions that survived #84, eighteen of
// twenty-three were CORRECT: a decision taken when the server was 3.2.0, an
// ordering caveat naming the servers that lack the ORDER BY, a behaviour
// verified against a running 3.2.0, a schema comparison whose whole point is the
// old version, and released CHANGELOG history. A checker that cannot tell those
// from a stale claim either fires eighteen false positives or, tuned to silence
// them, stops catching the real thing.
//
// So the predicate is not "mentions a version". It is "names the contract this
// SDK currently vendors", and the two are not the same -- which is precisely the
// distinction #84's first pass failed to make.
//
// The shape that fits is docs/contract-coverage.yaml's: fail closed, and turn
// "not the current contract" into a decision with a reason attached. Every
// version token in a scoped file must EITHER equal the recorded marker, OR match
// a category in contractVersionExemptions, which carries a written reason. A
// token that is neither fails, naming the file, the line, and both readings.
//
// A registry of sites that MUST match is the weaker form and is deliberately
// refused, for the same reason #77 refused its cheap proxy: it catches drift in
// the sites someone remembered to register and is silent on the one nobody did,
// which is the failure that actually happened.
//
// # What this guard CANNOT do, stated here rather than in the issue
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
//   - It says nothing about the two specs or the marker. checkRecordedVersion
//     already owns those three, and duplicating them here would give two gates
//     one job and let each assume the other is doing it.

// contractVersionMarker is the one mechanized statement of the targeted contract.
// tools/contractdrift asserts it against both specs' info.version; this guard
// reads it as the value every prose claim is measured against.
var contractVersionMarker = regexp.MustCompile(`<!--\s*contract-version:\s*([0-9]+\.[0-9]+\.[0-9]+)\s*-->`)

// versionToken is three dotted numbers and NOTHING ELSE. Narrowing it to "3.x"
// would encode the server's current major into the guard and stop working the
// day the server ships 4.0.0 -- and the compat line vendors a 1.0.0 contract
// today, so the major is not a reliable discriminator even now.
//
// It deliberately does NOT match a SemVer prerelease suffix. "3.2.1-or-newer" is
// valid prerelease SYNTAX and is English in prose: matching it swallowed the
// version-range caveat underneath and reported the whole phrase as an
// unclassified version. A real prerelease (2.0.0-rc.1) is caught on its 2.0.0
// base by the sdk-version category, which is the only place prereleases appear.
var versionToken = regexp.MustCompile(`\b[0-9]+\.[0-9]+\.[0-9]+\b`)

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
	return containsAny(b[max(len(b)-window, 0):], phrases...)
}

// followedBy reports whether any phrase appears within window bytes after the
// token. Version-range caveats qualify from either side -- "before 3.2.1" and
// "3.2.1 and newer" are the same category read from opposite ends.
func (s versionSite) followedBy(window int, phrases ...string) bool {
	a := s.after()
	return containsAny(a[:min(window, len(a))], phrases...)
}

// contractVersionExemptions is the registry. Order does not matter; the first
// match wins and its name is reported when a test asks why a token passed.
//
// THE HARNESS PIN IS A CATEGORY OF ITS OWN, AND THAT IS LOAD-BEARING. The
// container the integration suite runs against is pinned independently of the
// vendored contract -- the two were 3.1.0 and 3.2.0 simultaneously until #84,
// and the harness-pin CHANGELOG entry says so in as many words. They happen to
// be EQUAL today, which is the trap: a guard that let them pass by equality
// would look correct now and break the next time the pin legitimately leads the
// contract, which is the normal state rather than the exception. So the pin is
// matched by ROLE -- the image reference it sits in -- and never by value.
// TestHarnessPinIsExemptByRoleNotByValue pins that distinction while the two
// values still agree.
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
		Reason: "The container floor for the integration suite, deliberately independent of the vendored contract. Matched by the image reference it sits in, never by value: the two numbers were 3.1.0 and 3.2.0 at the same time until #84.",
		Match: func(s versionSite) bool {
			return strings.Contains(s.Line, "octonomy:"+s.Token) ||
				s.precededBy(60, "harness_image", "harness image", "ghcr.io/")
		},
	},
	{
		Name:   "verification-note",
		Reason: "Records behaviour observed against a running server of that version. Re-pointing it at a newer server would assert a probe nobody ran.",
		Match: func(s versionSite) bool {
			return s.precededBy(90,
				"probed against", "verified against", "against a running", "against a live",
				"captured from a live", "re-verified", "was verified", "verified to fail",
				"verified live against", "live against", "observed against", "reproduced against",
				"against", "confirmed", "run against",
				// Bare "probed" / "verified" are safe discriminators: a sentence
				// CLAIMING what is vendored says "vendored at", "both track", "both
				// at" -- never that it was probed. Needed because these wrap:
				// "verified across tags, vocabularies ... on server\n// 3.1.0)."
				"probed", "verified") ||
				s.followedBy(40, " was verified", " container", " harness", " server, where", " on postgres")
		},
	},
	{
		Name:   "version-range-caveat",
		Reason: "Names the servers on one side of a behaviour change (the tags ORDER BY, the namespace surface). The boundary is a fact about those releases and does not move when the vendored contract does.",
		Match: func(s versionSite) bool {
			return s.precededBy(30,
				"pre-", "before", "older than", "and older", "\u2264", "<=", "as of the", "from server",
				"since", "pointed at", "post-", "on a server older than") ||
				s.followedBy(26,
					" and newer", " and older", " or newer", " or older", "-or-newer", "-or-older",
					"** and", "` and", "** or", "` or", "+ ", " which predates", ", which predates")
		},
	},
	{
		Name:   "dated-decision-record",
		Reason: "Describes a decision taken when the server WAS that version. A statement about the past, and re-pointing it would falsify the record.",
		Match: func(s versionSite) bool {
			return s.precededBy(90, "decision", "was settled", "kept, and why", "revisit", "accepted for", "at the time", "an earlier plan", "one live policy") ||
				s.followedBy(60, " makes `/api/v2` the", " makes /api/v2 the", " minor", " is a behavioural patch")
		},
	},
	{
		Name:   "server-history",
		Reason: "Narrates what the server shipped when, or which contract this SDK sat on before a refresh. The reason a mechanism exists, not a claim about what is vendored now.",
		Match: func(s versionSite) bool {
			return s.precededBy(110,
				"shipped", "the server was", "while the server", "added the", "fixed it", "fixed the",
				"had no", "sat on", "sit on", "came to sit", "was written against", "moved",
				"drifted", "pinned at server", "predates") ||
				s.followedBy(60, " contract while", " contract,", " and had drifted", " refresh says", " refresh", " threads", " with a second", " with an entire")
		},
	},
	{
		Name:   "go-toolchain-version",
		Reason: "A Go toolchain or language version, not a server contract version.",
		Match: func(s versionSite) bool {
			return s.precededBy(40, "go ", "go1.", "golang", "go-version", "toolchain", "gotoolchain") ||
				strings.HasPrefix(s.Token, "1.13") || strings.HasPrefix(s.Token, "1.24") || strings.HasPrefix(s.Token, "1.25")
		},
	},
	{
		Name:   "sdk-version",
		Reason: "A version of THIS module, not of the server contract. The SDK versions independently of the server (docs/versioning.md), so its numbers never track the marker.",
		Match: func(s versionSite) bool {
			if s.Path == "version.go" {
				return true
			}
			return s.precededBy(70,
				"octonomy-go", "sdk version", "version =", "`version`", "user-agent", "useragent",
				"semver", "changelog heading", "release/v", "tagged", "`v") ||
				strings.HasPrefix(s.Token, "2.0.0") || strings.HasPrefix(s.Token, "0.") ||
				strings.HasPrefix(s.Token, "1.0.1")
		},
	},
	{
		Name:   "compat-line-contract",
		Reason: "Names the contract vendored by support/go1.13, which tracks its own marker and is not this module's. That line's refresh is scoped on #90; until then its version is deliberately behind.",
		Match: func(s versionSite) bool {
			return s.precededBy(110, "compat line", "support/go1.13", "compat branch", "info.version:", "still at")
		},
	},
	{
		Name:   "external-reference",
		Reason: "A version inside a third-party URL or spec identifier (Keep a Changelog, SemVer, the OpenAPI format version).",
		Match: func(s versionSite) bool {
			return s.precededBy(60, "keepachangelog", "semver.org", "openapi: ", "spec/v", "http://", "https://")
		},
	},
	{
		Name:   "gate-test-fixture",
		Reason: "A literal inside a gate's own fixtures, which must name versions the repository does not vendor in order to prove the gate can fail. Includes this guard's own file: it cannot both demonstrate a stale claim and forbid writing one.",
		Match: func(s versionSite) bool {
			if s.Path == "contractversion_test.go" {
				return true
			}
			return strings.HasSuffix(s.Path, "_test.go") && strings.HasPrefix(s.Path, "tools/")
		},
	},
}

// contractVersionSkipDirs are directories whose contents are out of scope
// wholesale, each for a stated reason rather than because it was inconvenient.
var contractVersionSkipDirs = map[string]string{
	".git":         "not tracked content",
	"docs/designs": "dated design records. A design doc describes the state at the moment it was written, exactly as a released CHANGELOG entry does, and editing one to track the contract would falsify the record it exists to be.",
}

// contractVersionSkipFiles are individual files out of scope, with reasons.
var contractVersionSkipFiles = map[string]string{
	"docs/openapi.yaml":    "generated from the server, and its info.version is one of the three things checkRecordedVersion already asserts.",
	"docs/openapi-v2.yaml": "generated from the server, and its info.version is one of the three things checkRecordedVersion already asserts.",
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
	raw, err := os.ReadFile(filepath.Join("docs", "versioning.md"))
	if err != nil {
		t.Fatalf("read docs/versioning.md: %v", err)
	}
	found := contractVersionMarker.FindAllStringSubmatch(string(raw), -1)
	switch len(found) {
	case 0:
		t.Fatal("docs/versioning.md carries no <!-- contract-version: X.Y.Z --> marker. " +
			"It is the value every prose claim in this repository is measured against, and " +
			"tools/contractdrift asserts it against both specs' info.version.")
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
func scanContractVersions(t *testing.T, marker string) []contractVersionFinding {
	t.Helper()
	var findings []contractVersionFinding

	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		path = filepath.ToSlash(path)
		if d.IsDir() {
			if ok, _ := inScope(path); !ok && path != "." {
				if _, skipped := contractVersionSkipDirs[path]; skipped {
					return fs.SkipDir
				}
			}
			if strings.HasPrefix(path, ".git/") || path == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		if ok, _ := inScope(path); !ok {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		body := string(raw)

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
			// Two lines of look-back. Prose wraps, and the phrase that classifies
			// a mention is regularly on the line above it.
			prev := strings.Join(lines[max(i-2, 0):i], "\n")
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
		return nil
	})
	if err != nil {
		t.Fatalf("walk the repository: %v", err)
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
// This is the guard #86 asked for. It fails closed: a version token that matches
// no category is a finding, reported with both readings so the contributor
// chooses rather than guesses.
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
		"      caveat, a dated decision, the harness pin, a toolchain or SDK version. Add it to\n" +
		"      contractVersionExemptions in contractversion_test.go, WITH A REASON.\n\n" +
		"#84 moved eight of fourteen such sites and missed six, with every gate green. This guard\n" +
		"exists so the sixth is a decision rather than an omission.")
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
// This is the trap #86 named. scripts/octonomy-harness.sh and the composite
// action pin a container floor that is deliberately independent of the vendored
// contract; the two numbers were 3.1.0 and 3.2.0 simultaneously until #84. They
// are EQUAL today, so a guard that let the pin pass by equality would look
// correct and break the next time the pin legitimately leads -- which is the
// normal state, not the exception.
//
// This test pins the distinction WHILE THE TWO VALUES STILL AGREE, which is the
// only window in which a by-value implementation is indistinguishable from a
// by-role one.
func TestHarnessPinIsExemptByRoleNotByValue(t *testing.T) {
	const pinned = "9.9.9" // deliberately not the marker, and not any real release

	line := `OCTONOMY_HARNESS_IMAGE="${OCTONOMY_HARNESS_IMAGE:-ghcr.io/octoverse-id/octonomy:` + pinned + `}"`
	if got := classifyContractVersion(siteIn("scripts/octonomy-harness.sh", line, pinned)); got != "harness-image-pin" {
		t.Errorf("a harness pin that does NOT equal the marker must still be exempt by role, got %q.\n"+
			"An implementation that exempts the pin by comparing it to the contract version passes "+
			"today (the two agree) and fails the next time the pin leads the contract.", got)
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
// The `make contract-test` standard: assert the finding, not just the green path.
// A guard nobody has seen fail is a guard nobody should trust.
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
			name:     "a stale vendored-at claim",
			path:     "docs/api.md",
			line:     "`openapi-v2.yaml` — `/api/v2`, server **3.1.0**. The default surface.",
			token:    "3.1.0",
			exempt:   false,
			whatItIs: "the exact shape #84 missed six times",
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
			name:     "the harness pin",
			path:     "scripts/octonomy-harness.sh",
			line:     `: "${OCTONOMY_HARNESS_IMAGE:-ghcr.io/octoverse-id/octonomy:3.1.0}"`,
			token:    "3.1.0",
			exempt:   true,
			whatItIs: "a container floor, a different number by role",
		},
	}

	for _, tc := range cases {
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

// Released CHANGELOG entries are history, and the cut is the first released
// heading rather than a line number somebody maintains.
func TestChangelogHistoryIsOutOfScopeFromTheFirstReleasedHeading(t *testing.T) {
	body := "# Changelog\n\n## [Unreleased]\n\nvendored at server 9.9.9\n\n## [2.0.0-rc.1] - 2026-09-22\n\nvendored at server 8.8.8\n"
	at := changelogHistoryStart(body)
	if at != 7 {
		t.Fatalf("history starts at the first released heading (line 7), got %d", at)
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
			t.Errorf("%s must be out of scope with a reason: checkRecordedVersion already owns it", path)
		}
	}
	if ok, _ := inScope("docs/designs/octonomy-3.1-upgrade-go113.md"); ok {
		t.Error("docs/designs/ is a dated record and must be out of scope wholesale")
	}
	for _, path := range []string{"docs/api.md", "AGENTS.md", "README.md", "transport.go", "scripts/octonomy-harness.sh"} {
		if ok, _ := inScope(path); !ok {
			t.Errorf("%s carries contract claims and must be in scope", path)
		}
	}
}
