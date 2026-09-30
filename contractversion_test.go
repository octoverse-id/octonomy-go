package octonomy

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
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
//   - It reads the files git TRACKS (repositoryFiles), so a new file is invisible
//     to it until `git add`. That is the price of not failing on a developer's
//     untracked notes (#110), and CI pays none of it. Its protection against
//     `go test` replaying a cached PASS after that `git add` is one Stat of the
//     index, which no unit test here can observe; it was verified by hand on
//     #110 in both directions, and gitTrackedFiles says why it has to stay.

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
			// "probed" and "verified" are the discriminators, and they must be
			// PRESENT -- a bare "against" is not one. "The SDK is written against
			// server 3.1.0" is a stale CLAIM, and an earlier revision of this
			// category exempted it, which is the category-too-loose failure this
			// guard's own doc comment warns about, committed in the guard itself.
			//
			// The bare forms stay because the real sites wrap across lines:
			// "verified across tags, vocabularies ... on server\n// 3.1.0)." and
			// "also probed, via /api/v3 on 3.1.0". No sentence claiming what is
			// vendored says it was probed.
			return s.precededBy(90,
				"probed", "verified", "observed against", "reproduced against",
				"against a running", "against a live", "captured from a live") ||
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
				"drifted", "pinned at server", "predates", "server changelog") ||
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
			// BY CONTEXT, NEVER BY VALUE. An earlier revision exempted every
			// token starting 2.0.0 / 0. / 1.0.1 outright, so "The specs are
			// vendored at server 2.0.0" passed -- a stale contract claim waved
			// through because its digits looked like an SDK release. That is the
			// same by-value mistake the harness-pin category exists to refuse,
			// reproduced two categories down. The server and this module share a
			// number space; only the surrounding words separate them.
			return s.precededBy(80,
				"octonomy-go", "sdk version", "version =", "`version`", "user-agent", "useragent",
				"semver", "changelog heading", "release/v", "tagged", "this module",
				"version constant", "constant read",
				"prerelease", "bump", "alpha", "rc.", "shipping `/api/v2` support as a") ||
				s.followedBy(40, " minor", " major", " patch", " prerelease", "-alpha", "-rc", " tag")
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
			// The token must sit INSIDE the URL, not merely after one on the same
			// line. An earlier revision searched the preceding 60 bytes for
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

// scanContractVersions reads every in-scope file the repository at dir tracks
// and returns every version token that neither equals the marker nor matches a
// registered exemption. Paths in findings are relative to dir, slash-separated,
// which is the form the exemptions match on.
func scanContractVersions(t *testing.T, dir, marker string) []contractVersionFinding {
	t.Helper()
	files, _, err := repositoryFiles(dir)
	if err != nil {
		t.Fatalf("list the repository's files: %v", err)
	}
	var findings []contractVersionFinding
	for _, path := range files {
		if ok, _ := inScope(path); !ok {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(path)))
		if errors.Is(err, fs.ErrNotExist) {
			continue // tracked, but deleted in this checkout: no prose left to check
		}
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		findings = append(findings, scanContractVersionsIn(path, string(raw), marker)...)
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
	return findings
}

// repositoryFiles lists the files this guard reads under dir, as slash-separated
// paths relative to it, and names which source answered: "git" or "walk".
//
// It used to walk the directory, which read whatever else was in the checkout --
// an untracked notes.md, an ignored file under code-review/ -- and failed
// `go test` on one developer's machine over a file the repository does not
// contain (#110, found on the compat port in #109). `git ls-files` is the
// tracked set, exactly. A tracked file deleted in the checkout is still listed,
// and the caller skips it. The cost runs the other way: a new file is read once
// it is in the index and not before, so run the guard after `git add`. CI reads
// a fresh checkout of the commit, where nothing is untracked.
//
// When git cannot answer for dir, it walks instead. That is the direction that
// fails closed -- reading more rather than less -- and each case where it
// happens is one where git's answer would be wrong or missing: no git binary;
// no repository at all, as in the module cache; or a repository that encloses
// dir without tracking any of it, which lists nothing and would pass vacuously.
// The last one is not hypothetical: a module cache under a home directory kept
// in git is exactly that.
func repositoryFiles(dir string) (files []string, source string, err error) {
	if files, ok := gitTrackedFiles(dir); ok {
		return files, "git", nil
	}
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if _, skipped := contractVersionSkipDirs[rel]; skipped {
				return fs.SkipDir
			}
			return nil
		}
		files = append(files, rel)
		return nil
	})
	return files, "walk", err
}

// gitTrackedFiles returns the files git tracks under dir, and false when git
// cannot answer for it -- see repositoryFiles for the cases.
//
// It also Stats the index, and that call is load-bearing. `go test` caches a
// passing result against the files the test process opened or stat-ed, and it
// cannot see what a subprocess read. The walk opened every directory, so a new
// file changed a recorded listing and invalidated the cache; `git ls-files`
// reads the index instead, and without the Stat a `git add` of a new file
// carrying a stale claim changes nothing the cache recorded, so `go test ./...`
// replays the last PASS (verified on #110). The index path comes from git
// rather than from ".git/index" because in a linked worktree .git is a file,
// and a Stat that always fails records the same error every run.
func gitTrackedFiles(dir string) ([]string, bool) {
	cmd := exec.Command("git", "ls-files", "-z")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	var files []string
	for name := range bytes.SplitSeq(out, []byte{0}) {
		if len(name) > 0 {
			files = append(files, string(name))
		}
	}
	if len(files) == 0 {
		return nil, false // an enclosing repository that tracks none of dir
	}
	index, err := gitIndexPath(dir)
	if err != nil {
		return nil, false
	}
	_, _ = os.Stat(index) // recorded by go test's cache; the result is not needed
	return files, true
}

// gitIndexPath returns the path of the index git reads for dir.
func gitIndexPath(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--git-path", "index")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	index := strings.TrimSuffix(string(out), "\n")
	if !filepath.IsAbs(index) {
		index = filepath.Join(dir, index)
	}
	return index, nil
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

	findings := scanContractVersions(t, ".", marker)
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
		// The three below were found by an outside review of this guard's first
		// revision, and each was a real bypass. They are the category-too-loose
		// failure this file's own doc comment warns about, committed inside the
		// guard that warns about it -- which is why they are pinned rather than
		// just fixed.
		{
			name:     "a stale claim using bare 'against'",
			path:     "docs/api.md",
			line:     "The SDK is written against server 3.1.0.",
			token:    "3.1.0",
			exempt:   false,
			whatItIs: "verification-note once accepted a bare 'against'; only 'probed'/'verified' establish a probe",
		},
		{
			name:     "a stale claim whose digits look like an SDK release",
			path:     "docs/api.md",
			line:     "The specs are vendored at server 2.0.0.",
			token:    "2.0.0",
			exempt:   false,
			whatItIs: "sdk-version once matched on the token's VALUE -- the same by-value mistake the harness-pin category exists to refuse",
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

// The guard reads what the repository TRACKS, and nothing else in the checkout
// (#110). Built on a throwaway repository so the untracked, ignored and deleted
// cases are real rather than assumed: a local notes file carrying a stale claim
// must not fail anyone's `go test`, and a tracked one still must.
func TestContractVersionGuardReadsOnlyTrackedFiles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git binary: repositoryFiles walks instead, and the git path is what this test is about")
	}
	// Hermetic git. A hook exports GIT_INDEX_FILE and friends to whatever it
	// runs, so under a pre-commit hook this test's `git add` would otherwise
	// write into the index of the commit in progress. The developer's own config
	// is shut out too: a global hooks path or commit signing is not this test's
	// business.
	for _, k := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR",
		"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES"} {
		t.Setenv(k, "") // restores the original value when the test ends
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	listed := func(files []string) string { return strings.Join(files, ",") }

	// A claim no exemption covers, measured against a marker it does not equal.
	const stale, marker = "The two specs are vendored at server 9.9.9.\n", "7.7.7"
	git(dir, "init", "-q")
	write(".gitignore", "ignored/\n")
	write("docs/tracked.md", stale)
	write("docs/deleted.md", stale)
	write("notes.md", stale)           // untracked
	write("ignored/scratch.md", stale) // ignored
	git(dir, "add", ".gitignore", "docs/tracked.md", "docs/deleted.md")
	if err := os.Remove(filepath.Join(dir, "docs", "deleted.md")); err != nil {
		t.Fatal(err)
	}

	t.Run("git lists exactly the tracked set", func(t *testing.T) {
		files, source, err := repositoryFiles(dir)
		if err != nil || source != "git" {
			t.Fatalf("a git work tree must be listed by git, got %q, %v", source, err)
		}
		if got := listed(files); got != ".gitignore,docs/deleted.md,docs/tracked.md" {
			t.Errorf("want the tracked files, slash-separated, the deleted one included; got %q", got)
		}
	})

	t.Run("only the tracked stale claim is a finding", func(t *testing.T) {
		got := scanContractVersions(t, dir, marker)
		if len(got) != 1 || got[0].Path != "docs/tracked.md" || got[0].Line != 1 || got[0].Token != "9.9.9" {
			t.Errorf("want one finding at docs/tracked.md:1, and nothing from the untracked, ignored "+
				"or deleted files; got %+v", got)
		}
	})

	t.Run("an enclosing repository that tracks none of dir is walked", func(t *testing.T) {
		// ignored/ sits inside a repository that lists nothing under it, which
		// is a module cache under a home directory kept in git. Taking that empty
		// answer would scan nothing and pass.
		files, source, err := repositoryFiles(filepath.Join(dir, "ignored"))
		if err != nil || source != "walk" {
			t.Fatalf("an empty listing must fall back to the walk, got %q, %v", source, err)
		}
		if got := listed(files); got != "scratch.md" {
			t.Errorf("the walk reads what is there, got %q", got)
		}
	})

	t.Run("the index go test is told about exists, in a linked worktree too", func(t *testing.T) {
		git(dir, "-c", "user.name=guard", "-c", "user.email=guard@example.invalid",
			"commit", "-q", "-m", "fixture")
		worktree := filepath.Join(t.TempDir(), "wt")
		git(dir, "worktree", "add", "-q", worktree)
		for _, d := range []string{dir, worktree} {
			index, err := gitIndexPath(d)
			if err != nil {
				t.Fatalf("gitIndexPath(%s): %v", d, err)
			}
			if info, err := os.Stat(index); err != nil || !info.Mode().IsRegular() {
				t.Errorf("%s: the index Stat must name a real file, or go test's cache records the "+
					"same error every run and replays a PASS after `git add`; got %s: %v", d, index, err)
			}
		}
	})

	t.Run("with no git binary the walk reads everything but .git", func(t *testing.T) {
		t.Setenv("PATH", "")
		files, source, err := repositoryFiles(dir)
		if err != nil || source != "walk" {
			t.Fatalf("without git the walk must answer, got %q, %v", source, err)
		}
		if got := listed(files); got != ".gitignore,docs/tracked.md,ignored/scratch.md,notes.md" {
			t.Errorf("the fallback reads every file outside .git, got %q", got)
		}
	})

	t.Run("with no .git the walk reads everything", func(t *testing.T) {
		if err := os.RemoveAll(filepath.Join(dir, ".git")); err != nil {
			t.Fatal(err)
		}
		files, source, err := repositoryFiles(dir)
		if err != nil || source != "walk" {
			t.Fatalf("without .git the walk must answer, got %q, %v", source, err)
		}
		if got := listed(files); got != ".gitignore,docs/tracked.md,ignored/scratch.md,notes.md" {
			t.Errorf("the fallback reads every file, which fails closed; got %q", got)
		}
		if got := scanContractVersions(t, dir, marker); len(got) != 3 {
			t.Errorf("with nothing to say what is tracked, every stale claim is a finding; got %+v", got)
		}
	})
}
