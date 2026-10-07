package octonomy

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// --- the guard over which server contract this repository claims to vendor ----
//
// Ported by #90 from main's contractversion_test.go as it stood at 61fce9b (#86,
// PR #106). #90 is also the refresh that moved this line's contract from 1.0.0 to
// the release the marker in docs/versioning.md names, and the order was
// deliberate: the marker first, then this guard, then the prose -- so that THIS
// is what reported the refresh complete, rather than a reviewer.
//
// That order is the lesson of #84, which refreshed main's contract to 3.2.1: its
// first pass moved eight of fourteen prose sites and missed six, with every gate
// green, and a reviewer and an independent outside pass each found the same six.
// When #90 was scoped this branch had less protection than main had then:
// fourteen prose mentions across seven files, no marker, and no contract gate.
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
// # A sentence that CLAIMS a contract is exempt only by role
//
// Categories come in two kinds. A ByRole category identifies a token by what it
// IS -- its place in an address, an image tag, a release branch's name, a URL
// or the OpenAPI format key, version.go or a CHANGELOG heading, or a guard's own
// fixture file. The rest identify it by the WORDS around it: "probed", "Server
// X added", "Released entries". Words are the weak kind, because a sentence can carry both
// the exempting word and a stale claim, and the first review of this port showed
// three that did -- "vendored from server 3.1.0" read as a version range,
// "the currently vendored server 3.1.0 contract, reflected in both specs" as
// server history, "the contract vendored by this module is server 3.1.0" as an
// SDK version.
//
// So before any word-based category is consulted, the token's own SENTENCE is
// read for the words that state what is vendored (contractClaim). A token in
// such a sentence can be exempted only by a ByRole category. The veto fails
// closed: it makes some honest history sentences fail too -- "this line vendored
// 1.0.0 until #90" -- and the fix for those is to drop the old number ("two
// server majors behind") or move it into a sentence that claims nothing, never a
// looser category and never a ByRole one.
//
// Word-based categories are also scoped to the token's sentence, not merely to a
// byte window, so "Probed against 3.1.0. The baseline is 3.1.0." does not lend
// the first sentence's probe to the second. A sentence runs to its paragraph's edges
// in both directions -- reviews wrapped a claim word onto the next line, then the
// third -- but never across the boundary between a comment and the code beside
// it, and a YAML block scalar's content is content even where a line of it
// starts with '#' (lineKinds, neighbours).
//
// # How this copy differs from main's at 61fce9b, and why
//
//   - The six categories #90's scoping named as porting unchanged are kept:
//     ipv4-address, harness-image-pin, verification-note, version-range-caveat,
//     go-toolchain-version and external-reference -- even where no site in this
//     tree needs one yet. Each lost only phrases the first review showed were
//     loose ("from server") or that matched by accident ("go " inside
//     "version.go says 0.1.0").
//   - The others keep only the phrases a site in this tree needs. A phrase with
//     no site is a loosening nothing justifies, so server-history and sdk-version
//     are pruned, and dated-decision-record is absent: nothing here needs it. A
//     port that brings prose needing more brings the phrase with it, the way
//     AGENTS.md's "port a rule with the code it governs" asks.
//   - main's compat-line-contract category would INVERT here, into one exempting
//     mentions of main's contract. It is absent: this branch describes main with
//     a link rather than a restatement (#69), so there is nothing to exempt.
//   - release-line-guard is new. scripts/compat-guard.sh and its fixture suite
//     exist only on this line, and every version in them is this module's own.
//     It is scoped by path, so it is NOT ByRole: a claim sentence in either
//     script is vetoed like one anywhere else.
//   - sdk-version is sharper here than on main, because this line's own first
//     release and the contract it sat on until #90 are the SAME number, 1.0.0.
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
//   - Category patterns exempt by SHAPE, not per site, and the claim veto is a
//     list of words. A stale claim written in none of those words -- and outside
//     every word-based category -- still fails, since nothing exempts it; one
//     written in a word-based category's words AND none of the claim words is the
//     residual gap. The regression cases below pin every construction found so
//     far.
//   - Sentences are found by punctuation, blank lines, list items and table rows,
//     less a short list of abbreviations. An abbreviation the list lacks ends a
//     sentence early, narrowing both the veto and the words a category may read;
//     the byte windows bound the second regardless, and a stop right before the
//     token leaves nothing before it to exempt it with.
//   - It sees only three dotted numbers. "`openapi.yaml` is the contract" names
//     the contract with no version in it, and nothing here can see that; the
//     contributor instructions say to read for it by hand.
//   - It says nothing about the two specs or the marker agreeing. That check is
//     contractbaseline_test.go's, and since #98 the contract gate's
//     checkRecordedVersion too; this guard only READS the marker.
//     Duplicating it here would give two tests one job and let each assume the
//     other is doing it.

// contractVersionMarker is the one mechanized statement of the targeted contract.
// contractbaseline_test.go asserts it against both specs' info.version; this
// guard reads it as the value every prose claim is measured against.
var contractVersionMarker = regexp.MustCompile(`<!--\s*contract-version:\s*([0-9]+\.[0-9]+\.[0-9]+)\s*-->`)

// versionToken is three dotted numbers and NOTHING ELSE. Narrowing it to "3.x"
// would encode the server's current major into the guard and stop working the
// day the server ships 4.0.0 -- and this line sat on a 1.0.0 contract until
// #90, so the major has never been a reliable discriminator.
//
// It deliberately does NOT match a SemVer prerelease suffix. "3.2.1-or-newer" is
// valid prerelease SYNTAX and is English in prose: matching it swallowed the
// version-range caveat underneath and reported the whole phrase as an
// unclassified version.
//
// It DOES take an optional leading "v". main's copy at 61fce9b did not, so a
// v-prefixed version was never a token at all -- \b needs a word/non-word
// transition and "v3" has none -- and the second review of this port showed what
// that let through: "Both bundled specs target server v3.1.0" was never looked
// at. The server's own git tags are spelled v3.2.1, so that is a natural way to
// write one. A v-prefixed token is compared with the marker on its digits, and the
// v-tag category classifies the rest.
var versionToken = regexp.MustCompile(`\bv?[0-9]+\.[0-9]+\.[0-9]+\b`)

// codeSpanRest matches what may follow a version inside a code span before it
// closes: a prerelease or build suffix ("`2.0.0-alpha.1`"), then the backtick.
var codeSpanRest = regexp.MustCompile("^[-+.0-9A-Za-z]*`")

// vTagServerSubject matches a sentence about the server or its API, which is
// never offered the v-tag category -- by those words or by the server's name.
// "/api/" is a REST path, not the word, and is left out: "no `/api/v2`, no
// namespaces" beside `v1.0.0` is about this module. So is "octonomy-go", this
// module's own name, which is why the product name must not be followed by a
// hyphen.
var vTagServerSubject = regexp.MustCompile(`\bserver\b|(^|[^/])\bapi\b([^/]|$)|\boctonomy\b([^-]|$)`)

// versionDigits strips the "v" a git tag carries, for comparison with the marker.
func versionDigits(token string) string { return strings.TrimPrefix(token, "v") }

// urlSpan matches a bare URL so a version can be tested for CONTAINMENT in it
// rather than mere proximity to one.
var urlSpan = regexp.MustCompile(`https?://[^\s)\]>"'` + "`" + `]+`)

// contractClaim matches the words a sentence uses to say what is vendored. A
// token whose sentence carries one is a claim, and only a ByRole category may
// exempt it. Broad on purpose: "vendor" catches vendored, vendors and vendoring,
// and a veto that is too broad fails a correct sentence -- visibly, with a line
// number -- where one too narrow passes a stale claim in silence. "track" and
// "target" are the exceptions, matched as whole verbs and never before a hyphen,
// because "target-branch" is this repository's vocabulary for something that is
// not a contract; "tracked" is left out for "tracked file". "contract", "spec"/
// "specs" and "openapi" are here because a sentence about the contract or the
// specs that carries a non-marker version is a claim about them whatever its
// verb: "Both specs shipped at 3.1.0" read as server history until specs was a
// claim word, and "The bundled API contract shipped as 3.1.0" until contract
// was. The price is that HISTORY of this line's contract cannot carry the old
// number in prose -- "two server majors behind", not "the 1.0.0 contract" --
// which is a small price: that number is recorded in the CHANGELOG entry that
// moved it and in git, and a sentence repeating it is one more to go stale.
var contractClaim = regexp.MustCompile(`vendor|written against|speaks|at server|at release|` +
	`\bcontracts?\b|\bspecs?\b|openapi|info\.version|` +
	`\b(track|tracks|tracking|target|targets|targeted|targeting)\b([^-]|$)`)

// sentenceBreak matches the end of a sentence or of a statement within one
// paragraph: a full stop, question or exclamation mark followed by whitespace
// or the end of the text; the start of a list item -- any of CommonMark's three
// bullets, "-", "*" and "+", or a number; or the start of a Markdown table row.
// The last two may sit behind any run of comment markers ("//", "#") and
// blockquote markers (">"): this repository writes lists inside blockquotes
// (README.md's "> -" items), and the sixth review lent one quoted item's probe
// to the next.
//
// Paragraph boundaries are NOT here. They are neighbours', which never hands a
// sentence a blank line, a line of nothing but markers ("//", "#", ">" alone --
// a blank line in its own syntax), a heading, or a line of another kind. This
// regex once matched blank lines too, and a mutation removing neighbours' half
// passed because this half still split the sentence: two layers doing one job,
// with only one of them testable. The job lives in one place now. sentenceBreaksIn then drops the full stops
// that end an abbreviation, which is what keeps "e.g. " from splitting a claim.
//
// Two things are deliberately NOT breaks. A semicolon: the clause after it is
// often the claim, and a narrower sentence is a weaker veto. And a table CELL: a
// row is one statement, and docs/release.md's placeholder table names what a
// column holds in one cell and gives the version in the next.
//
// An earlier version also required a capital letter after the stop, to keep
// "e.g. the" whole. Once abbreviations were handled by name that rule protected
// nothing -- reverting it failed no test -- and it had a cost: a sentence that
// starts with a version ("Probed against 3.2.1. 3.1.0 is the baseline") only
// ended where it should by accident. It is gone.
var sentenceBreak = regexp.MustCompile(`[.!?](\s+|$)|` +
	`\n\s*((//|#|>)\s*)*([-*+]|[0-9]+[.)])\s|\n\s*((//|#|>)\s*)*\|`)

// goWord matches "go " as a word, so "requires go >= 1.26.0" is a toolchain but
// "version.go says 0.1.0" is not. main's copy at 61fce9b matched the bare
// substring, and on this tree that classified the release-line guard's version
// fixtures as Go toolchains -- right answer, wrong reason, and the wrong reason
// would exempt "version.go says the contract is 3.1.0" too.
var goWord = regexp.MustCompile(`(^|[^a-z0-9._/-])go `)

// contractVersionExemption is one category of version mention that is NOT a
// claim about the contract this SDK currently vendors.
//
// Reason is required and is checked for emptiness: an exemption whose reason is
// blank is itself a finding, because the reason is the only thing separating a
// classified mention from an unexamined one.
type contractVersionExemption struct {
	Name   string
	Reason string
	// ByRole is true when Match identifies the token by what it IS -- its place
	// in an address, an image reference or a URL, or the guard fixture it sits
	// in -- rather than by the words around it. Only a ByRole category can exempt
	// a token whose sentence claims a contract.
	ByRole bool
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
	Next  string // the following lines, joined
	Token string
	Idx   int // byte offset of Token within Line
}

// before returns the text preceding the token, spanning into the previous lines.
func (s versionSite) before() string { return s.Prev + "\n" + s.Line[:s.Idx] }

// after returns the text following the token on its own line.
func (s versionSite) after() string { return s.Line[s.Idx+len(s.Token):] }

// sentenceBefore returns the text preceding the token back to the start of its
// sentence, or of its paragraph, whichever is nearer.
func (s versionSite) sentenceBefore() string {
	b := s.before()
	locs := sentenceBreaksIn(b)
	if len(locs) == 0 {
		return b
	}
	return b[locs[len(locs)-1][1]:]
}

// sentenceAfter returns the text following the token up to the end of its
// sentence, spanning into the following lines. It used to stop at the end of the
// token's own line, and prose wraps: "Server 3.1.0 added the default this SDK
// / targets today." put the claim word where the veto could not see it.
func (s versionSite) sentenceAfter() string {
	a := s.after()
	if s.Next != "" {
		a += "\n" + s.Next
	}
	if locs := sentenceBreaksIn(a); len(locs) > 0 {
		return a[:locs[0][0]]
	}
	return a
}

// abbreviation matches text ending in an abbreviation's full stop, which does
// not end a sentence. It is the only thing that keeps "e.g. " whole: without it
// "The specs are vendored at e.g. 3.1.0 and newer" ended the sentence right
// before the token, hid "vendored" from the veto, and left " and newer" to
// exempt it as a range.
var abbreviation = regexp.MustCompile(`(?i)(^|[^a-z])(e\.g|i\.e|etc|vs|cf|incl|approx|resp)\.$`)

// sentenceBreaksIn returns sentenceBreak's matches in text, less the full stops
// that end an abbreviation.
func sentenceBreaksIn(text string) [][]int {
	var out [][]int
	for _, m := range sentenceBreak.FindAllStringSubmatchIndex(text, -1) {
		if text[m[0]] == '.' && abbreviation.MatchString(text[:m[0]+1]) {
			continue
		}
		out = append(out, m)
	}
	return out
}

// claimsContract reports whether the token's sentence, on either side of it,
// carries a word that states what is vendored.
func (s versionSite) claimsContract() bool { return s.claimWord() != "" }

// claimWord returns the first claim word in the token's sentence, or "".
func (s versionSite) claimWord() string {
	if m := contractClaim.FindString(proseOf(s.sentenceBefore())); m != "" {
		return strings.TrimSpace(m)
	}
	return strings.TrimSpace(contractClaim.FindString(proseOf(s.sentenceAfter())))
}

// proseOf lowercases text and blanks its URLs. A link's target is not a word
// the sentence says: ".../compat-line-api-v2-parity.md" put "api" into a sentence
// about this module's v1.0.0 and read it as one about the server's API.
func proseOf(text string) string {
	return strings.ToLower(urlSpan.ReplaceAllString(text, " "))
}

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
// token AND within its sentence. The window keeps one long sentence from
// exempting a version far along it; the sentence keeps a neighbouring one from
// lending it a word.
func (s versionSite) precededBy(window int, phrases ...string) bool {
	b := s.sentenceBefore()
	return containsAny(b[maxInt(len(b)-window, 0):], phrases...)
}

// followedBy reports whether any phrase appears within window bytes after the
// token, within its sentence. Version-range caveats qualify from either side --
// "before 3.2.1" and "3.2.1 and newer" are the same category read from opposite
// ends.
func (s versionSite) followedBy(window int, phrases ...string) bool {
	a := s.sentenceAfter()
	return containsAny(a[:minInt(window, len(a))], phrases...)
}

// contractVersionExemptions is the registry. Order does not matter for the
// verdict; the first match wins and its name is reported when a test asks why a
// token passed.
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
		ByRole: true,
		Match: func(s versionSite) bool {
			return regexp.MustCompile(`[0-9]\.$`).MatchString(s.before()) ||
				regexp.MustCompile(`^\.[0-9]`).MatchString(s.after())
		},
	},
	{
		Name:   "harness-image-pin",
		Reason: "The image the smoke-test harness boots, pinned independently of the vendored contract. Matched by the image reference it sits in, never by value: #90 moved this line's contract and not its pin.",
		ByRole: true,
		Match: func(s versionSite) bool {
			// THIS token is the image's tag: "octonomy:" immediately before it.
			// Not "the line contains octonomy:<token>" -- that exempted "The
			// vendored contract is 3.1.0; the image is ghcr.io/...octonomy:3.1.0"
			// on the image reference's value. And no nearby words: "harness
			// image" and "ghcr.io/" describe or neighbour a pin without being one,
			// and a ByRole category exempts claim sentences too.
			return strings.HasSuffix(s.Line[:s.Idx], "octonomy:")
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
			// main's copy at 61fce9b also accepted "from server", which the first
			// review of this port showed exempting "The spec is vendored from
			// server 3.1.0". The claim veto now catches that sentence regardless;
			// the phrase is gone anyway, since nothing here needs it.
			return s.precededBy(30,
				"pre-", "before", "older than", "and older", "≤", "<=", "as of the",
				"since", "pointed at", "post-", "on a server older than") ||
				s.followedBy(26,
					" and newer", " and older", " or newer", " or older", "-or-newer", "-or-older",
					"** and", "` and", "** or", "` or", "+ ", " which predates", ", which predates")
		},
	},
	{
		Name:   "server-history",
		Reason: "Narrates what a server release added. The reason a behaviour exists, not a claim about what is vendored now.",
		Match: func(s versionSite) bool {
			// Exactly one shape: "Server 3.1.0 added ...", the server immediately
			// before the version and "added" immediately after it. Each of the
			// looser phrases this category once carried exempted a stale claim
			// in review -- "moved" (inside "removed", then "the contract baseline
			// moved to server 3.1.0"), "shipped" ("the bundled API contract
			// shipped as 3.1.0"), " contract," -- and "sat on" / "sit on" lost
			// their sites once "contract" became a claim word, since every
			// sentence using them was about the contract.
			return strings.HasSuffix(strings.ToLower(s.Line[:s.Idx]), "server ") &&
				strings.HasPrefix(s.after(), " added ")
		},
	},
	{
		Name:   "go-toolchain-version",
		Reason: "A Go toolchain or language version, not a server contract version.",
		Match: func(s versionSite) bool {
			b := s.sentenceBefore()
			return goWord.MatchString(strings.ToLower(b[maxInt(len(b)-40, 0):])) ||
				s.precededBy(40, "go1.", "golang", "go-version", "toolchain", "gotoolchain") ||
				strings.HasPrefix(s.Token, "1.13.") || strings.HasPrefix(s.Token, "1.24.") || strings.HasPrefix(s.Token, "1.25.")
		},
	},
	{
		Name:   "sdk-version-by-shape",
		Reason: "A version of THIS module identified by where it is written: version.go, or a CHANGELOG release heading, which versions nothing but this module.",
		ByRole: true,
		Match: func(s versionSite) bool {
			// "## [1.0.0]" is anchored on the bracket immediately before the
			// token, so a heading nearby does not exempt a claim further along
			// the line.
			return s.Path == "version.go" || strings.HasSuffix(s.before(), "## [")
		},
	},
	{
		Name:   "sdk-version",
		Reason: "A version of THIS module, not of the server contract. The SDK versions independently of the server (docs/versioning.md), so its numbers never track the marker.",
		Match: func(s versionSite) bool {
			// BY CONTEXT, NEVER BY VALUE, and on this branch there is no other
			// way to write it. The line's own first release is v1.0.0 and the
			// contract it sat on until #90 was info.version 1.0.0: the same three
			// numbers, meaning two unrelated things. A rule exempting "1.0.0"
			// would have waved through every stale contract claim this guard was
			// ported to catch. main's copy made the by-value mistake on 2.0.0
			// before an outside review found it; here it is unwriteable.
			//
			// Pruned to this tree's phrases, and the token must be a code span,
			// "`1.0.0`", which is how this repository writes a release's number
			// in prose. The release-line guard's fixtures, which account for most
			// SDK versions here, are release-line-guard's.
			if !strings.HasSuffix(s.Line[:s.Idx], "`") || !codeSpanRest.MatchString(s.after()) {
				return false
			}
			return s.precededBy(80, "changelog heading", "released entr") ||
				s.followedBy(40, "` entry")
		},
	},
	{
		Name:   "release-branch-name",
		Reason: "Part of a release branch's name, release/vX.Y.Z -- the branch a release PR for this module is cut on (AGENTS.md, docs/release.md). A branch name, not a contract.",
		ByRole: true,
		Match: func(s versionSite) bool {
			return strings.HasSuffix(s.Line[:s.Idx], "release/")
		},
	},
	{
		Name:   "v-tag",
		Reason: "A v-prefixed version, the spelling of a git tag. Every one in this tree is this module's own release (v1.0.0) or another Go module's (x/vuln v1.8.0). The server tags its releases the same way, which is why this is word-based, and why a sentence naming the server or its API is not offered it at all.",
		Match: func(s versionSite) bool {
			// "This SDK uses server v3.1.0" carries no claim word, and was a
			// v-tag until the sentence's SUBJECT was checked too: a v-prefixed
			// version in a sentence about the server or its API is the server's.
			sentence := proseOf(s.sentenceBefore() + " " + s.sentenceAfter())
			return strings.HasPrefix(s.Token, "v") && !vTagServerSubject.MatchString(sentence)
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
		ByRole: true,
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
			// The OpenAPI document's own format version, as the YAML key spells it.
			return strings.HasSuffix(s.before(), "openapi: ")
		},
	},
	{
		Name:   "gate-test-fixture",
		Reason: "A literal inside a guard's own fixtures, which must name versions the repository does not vendor in order to prove the guard can fail. Covers this file, contractbaseline_test.go, and the contract gate's tests under tools/contractdrift (#98), which bump info.version and the marker to prove the gate notices: none of them can both demonstrate a stale claim and forbid writing one.",
		ByRole: true,
		Match: func(s versionSite) bool {
			if s.Path == "contractversion_test.go" || s.Path == "contractbaseline_test.go" {
				return true
			}
			return strings.HasPrefix(s.Path, "tools/contractdrift/") && strings.HasSuffix(s.Path, "_test.go")
		},
	},
	{
		Name:   "main-pinned-copy",
		Reason: "A file tools/contractdrift/main.pin marks `identical`: main's bytes at the pinned commit, which scripts/contract-identity.sh refuses to let differ here by one byte (#98). Its prose is main's, already held to main's marker by main's own copy of this guard, and a correction has one route -- land on main, then advance the pin. Rewording it here is not a fix this guard could ask for.",
		ByRole: true,
		Match: func(s versionSite) bool {
			name := strings.TrimPrefix(s.Path, "tools/contractdrift/")
			return name != s.Path && pinnedIdenticalFiles()[name]
		},
	},
}

var (
	pinnedIdenticalOnce sync.Once
	pinnedIdentical     map[string]bool
)

// pinnedIdenticalFiles reads the `identical` lines of tools/contractdrift/main.pin
// -- the file scripts/contract-identity.sh reads, so the guard and the identity
// check cannot disagree about which files are main's. An unreadable pin file
// yields an empty set, which exempts nothing: the failure mode is a finding, not
// a pass.
func pinnedIdenticalFiles() map[string]bool {
	pinnedIdenticalOnce.Do(func() {
		pinnedIdentical = map[string]bool{}
		raw, err := ioutil.ReadFile(filepath.Join("tools", "contractdrift", "main.pin"))
		if err != nil {
			return
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if name := strings.TrimPrefix(line, "identical "); name != line && name != "" {
				pinnedIdentical[name] = true
			}
		}
	})
	return pinnedIdentical
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
	// Claim is the claim word the token's sentence carries, when it carries
	// one. It changes the remedy: no word-based category can exempt the token,
	// so the only fixes are the marker or a rewording.
	Claim string
}

// scanContractVersions reads every repository-owned file in scope and returns
// every version token that neither equals the marker nor matches a registered
// exemption.
func scanContractVersions(t *testing.T, marker string) []contractVersionFinding {
	t.Helper()
	files, _, err := repositoryFiles(".")
	if err != nil {
		t.Fatalf("list the repository's files: %v", err)
	}
	var findings []contractVersionFinding
	for _, path := range files {
		if ok, _ := inScope(path); !ok {
			continue
		}
		raw, err := ioutil.ReadFile(filepath.FromSlash(path))
		if os.IsNotExist(err) {
			continue // tracked, but deleted in this checkout: no prose left to check
		}
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		findings = append(findings, scanContractVersionsIn(path, string(raw), marker)...)
	}
	return findings
}

// repositoryFiles lists the files this guard reads: the ones git tracks under
// dir, as slash-separated paths relative to it, and which source answered.
//
// It used to walk the directory, which read whatever else happened to be in the
// checkout -- an untracked notes.md, an ignored scratch file -- and failed
// `go test` on a developer's machine over a file the repository does not
// contain, while this file's own doc comment promised TRACKED files (the review
// on #109). `git ls-files` is the tracked set, exactly. A tracked file deleted
// in the checkout is still listed; the caller skips it. The cost runs the other
// way: a new file is read once it is added to the index, not before, so run the
// guard after `git add` -- CI reads the commit, where nothing is untracked.
//
// When git cannot answer -- no git binary, or a copy with no .git such as the
// module cache -- it falls back to the walk. That is the direction that fails
// closed, reading more rather than less, and a copy without .git has no
// untracked files to be wrong about.
func repositoryFiles(dir string) (files []string, source string, err error) {
	cmd := exec.Command("git", "ls-files", "-z")
	cmd.Dir = dir
	out, gitErr := cmd.Output()
	if gitErr == nil {
		for _, name := range bytes.Split(out, []byte{0}) {
			if len(name) > 0 {
				files = append(files, string(name))
			}
		}
		return files, "git", nil
	}
	err = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if info.IsDir() {
			if _, skipped := contractVersionSkipDirs[rel]; skipped {
				return filepath.SkipDir
			}
			return nil
		}
		files = append(files, rel)
		return nil
	})
	return files, "walk", err
}

// scanContractVersionsIn classifies every version token in one file's body.
func scanContractVersionsIn(path, body, marker string) []contractVersionFinding {
	var findings []contractVersionFinding

	historyFrom := 0
	if path == "CHANGELOG.md" {
		historyFrom = changelogHistoryStart(body)
	}

	lines := strings.Split(body, "\n")
	kinds := lineKinds(path, lines)
	for i, line := range lines {
		lineNo := i + 1
		if historyFrom > 0 && lineNo >= historyFrom {
			break
		}
		// The token's paragraph, either side of it (neighbours). Prose wraps, and
		// the phrase that classifies a mention -- or the word that makes it a
		// claim -- is regularly on another line of the same sentence.
		prev, next := neighbours(kinds, lines, i)
		for _, m := range versionToken.FindAllStringIndex(line, -1) {
			token := line[m[0]:m[1]]
			if versionDigits(token) == marker {
				continue
			}
			site := versionSite{Path: path, Prev: prev, Line: line, Next: next, Token: token, Idx: m[0]}
			if classifyContractVersion(site) != "" {
				continue
			}
			f := contractVersionFinding{Path: path, Line: lineNo, Token: token, Text: strings.TrimSpace(line)}
			f.Claim = site.claimWord()
			findings = append(findings, f)
		}
	}
	return findings
}

// lineKind tells prose from code in a source file: "comment", "code", or, for
// Markdown, "prose". neighbours uses it so that a sentence never runs across the
// boundary between the two -- a shell comment saying the release "targets
// release/v1.0.1" lent its claim word to the fixture line beneath it, which is
// code, not the end of the comment's sentence.
func lineKind(path, line string) string {
	t := strings.TrimSpace(line)
	switch {
	case strings.HasSuffix(path, ".md"):
		return "prose"
	case strings.HasSuffix(path, ".go"):
		if strings.HasPrefix(t, "//") {
			return "comment"
		}
	case strings.HasPrefix(t, "#"):
		return "comment"
	}
	return "code"
}

// markdownFence and markdownHeading match a fenced code block's delimiter line
// and an ATX heading.
var (
	markdownFence   = regexp.MustCompile("^\\s{0,3}(```|~~~)")
	markdownHeading = regexp.MustCompile(`^\s{0,3}#{1,6}(\s|$)`)
)

// markerOnly matches a line that is blank once its comment and blockquote
// markers are gone -- "//", "#", ">" alone. Each is a blank line in its own
// syntax, and ends a paragraph like one.
var markerOnly = regexp.MustCompile(`^\s*((//|#|>)\s*)*$`)

// yamlBlockOpener matches a YAML key whose value is a block scalar: `key: >-`,
// `key: |`, optionally anchored (`key: &name >-`).
var yamlBlockOpener = regexp.MustCompile(`:\s+(&\S+\s+)?[|>][-+0-9]*\s*$`)

// lineKinds classifies every line of a file. It is lineKind plus one piece of
// YAML: inside a block scalar a line starting with '#' is CONTENT, not a
// comment -- "#91 says ..." wrapped onto its own line of an `unimplemented: >-`
// reason once split that reason's sentence in two. Block-scalar content is every
// line more indented than the key that opened it, blank lines included.
//
// Markdown gets the same treatment for its own structures, which the seventh
// review showed joining statements: an ATX heading is a line of its own kind, so
// it never runs into the paragraph under it; a fence delimiter is too; and inside
// a fenced block a shell or Go comment line and a command line are different
// kinds, as they are in a .sh or .go file.
func lineKinds(path string, lines []string) []string {
	kinds := make([]string, len(lines))
	isYAML := strings.HasSuffix(path, ".yml") || strings.HasSuffix(path, ".yaml")
	isMarkdown := strings.HasSuffix(path, ".md")
	blockKey := -1 // indentation of the key that opened the current block scalar
	inFence := false
	for i, line := range lines {
		if isMarkdown {
			t := strings.TrimSpace(line)
			switch {
			case markdownFence.MatchString(line):
				inFence = !inFence
				kinds[i] = "fence"
			case inFence && (strings.HasPrefix(t, "#") || strings.HasPrefix(t, "//")):
				kinds[i] = "fenced-comment"
			case inFence:
				kinds[i] = "fenced-code"
			case markdownHeading.MatchString(line):
				kinds[i] = "heading"
			default:
				kinds[i] = "prose"
			}
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if isYAML && blockKey >= 0 {
			if strings.TrimSpace(line) == "" || indent > blockKey {
				kinds[i] = "code"
				continue
			}
			blockKey = -1
		}
		kinds[i] = lineKind(path, line)
		if isYAML && kinds[i] == "code" && yamlBlockOpener.MatchString(line) {
			blockKey = indent
			if strings.HasPrefix(strings.TrimSpace(line), "- ") {
				blockKey += 2 // "- key: >-": the key sits after the dash
			}
		}
	}
	return kinds
}

// neighbours returns the rest of line i's paragraph on either side, joined: the
// contiguous non-blank lines of the same kind. main's copy at 61fce9b read two
// lines back; this port first read two forward as well, and the third review
// wrapped a claim word onto the third line. A paragraph is the natural bound,
// sentenceBefore and sentenceAfter cut it to the sentence, and the word-based
// categories' byte windows still bound how far a phrase may reach.
func neighbours(kinds, lines []string, i int) (prev, next string) {
	// A line of nothing but comment or blockquote markers is a blank line.
	same := func(j int) bool { return kinds[j] == kinds[i] && !markerOnly.MatchString(lines[j]) }
	from := i
	for from > 0 && same(from-1) {
		from--
	}
	to := i + 1
	for to < len(lines) && same(to) {
		to++
	}
	return strings.Join(lines[from:i], "\n"), strings.Join(lines[i+1:to], "\n")
}

// classifyContractVersion returns the name of the first exemption covering this
// occurrence, or "" when none does.
//
// A token whose sentence claims a contract is offered to the ByRole categories
// only; see "A sentence that CLAIMS a contract is exempt only by role" above.
func classifyContractVersion(site versionSite) string {
	claim := site.claimsContract()
	for _, ex := range contractVersionExemptions {
		if claim && !ex.ByRole {
			continue
		}
		if ex.Match(site) {
			return ex.Name
		}
	}
	return ""
}

// siteIn builds a versionSite for a token on a single line, for the guard's own
// tests. Production scanning supplies Prev and Next; these cases are deliberately
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
		if f.Claim != "" {
			fmt.Fprintf(&b, "      its sentence says %q, so it CLAIMS a contract: only (a) or (c) below applies\n", f.Claim)
		}
	}
	b.WriteString("\nEach one is one of three things, and only you can say which:\n")
	fmt.Fprintf(&b, "  (a) It names the contract this SDK vendors, and is now STALE. Change it to %s.\n", marker)
	b.WriteString("  (b) It is deliberately about another version -- a verification note, a version-range\n" +
		"      caveat, server history, the harness pin, a toolchain or SDK version -- in a sentence\n" +
		"      that claims NO contract. Add its shape to contractVersionExemptions in\n" +
		"      contractversion_test.go, WITH A REASON and a regression case.\n" +
		"  (c) It is history, but its sentence also uses a word that claims a contract (vendored,\n" +
		"      contract, specs, tracks, ...). Drop the old number (\"two server majors behind\") or\n" +
		"      move it into a sentence that claims nothing. Do NOT make a category ByRole to get\n" +
		"      past the veto: a ByRole category exempts claims, which is what this guard catches.\n\n" +
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
		next     string
		token    string
		exempt   bool
		whatItIs string
	}{
		{
			name:     "a copy main.pin marks identical",
			path:     "tools/contractdrift/coverage.go",
			line:     "// on a server 1.0.0 contract while the server shipped 3.1.0.",
			token:    "1.0.0",
			exempt:   true,
			whatItIs: "main's bytes at the pin; the identity check refuses rewording them here",
		},
		{
			name:     "this line's own gate file is not a pinned copy",
			path:     "tools/contractdrift/drivers.go",
			line:     "// on a server 1.0.0 contract while the server shipped 3.1.0.",
			token:    "1.0.0",
			exempt:   false,
			whatItIs: "drivers.go is advisory in main.pin, written here, and reworded here when it is wrong",
		},
		{
			name:     "a pinned name outside the gate's directory",
			path:     "coverage.go",
			line:     "// on a server 1.0.0 contract while the server shipped 3.1.0.",
			token:    "1.0.0",
			exempt:   false,
			whatItIs: "main.pin names files of tools/contractdrift only; a root file sharing a name is not one",
		},
		{
			name:     "the gate's tests are fixtures",
			path:     "tools/contractdrift/drift_test.go",
			line:     "\"<!-- contract-version: 2.0.0 -->\")",
			token:    "2.0.0",
			exempt:   true,
			whatItIs: "the gate's own test bumps the marker to prove checkRecordedVersion notices",
		},
		{
			name:     "the gate's source is not a fixture",
			path:     "tools/contractdrift/conformance.go",
			line:     "// The specs are vendored at server 2.0.0.",
			token:    "2.0.0",
			exempt:   false,
			whatItIs: "gate-test-fixture covers the gate's _test.go files, not its sources",
		},
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
		// The three below are the first review's constructions against this port:
		// each named a stale vendored version and each was exempted by a word
		// main's copy at 61fce9b accepted. They are why the claim veto exists.
		{
			name:     "review 1: vendored FROM server",
			path:     "docs/api.md",
			line:     "The spec is vendored from server 3.1.0.",
			token:    "3.1.0",
			exempt:   false,
			whatItIs: "\"from server\" read it as a version range",
		},
		{
			name:     "review 1: a claim followed by a comma",
			path:     "docs/api.md",
			line:     "The currently vendored server 3.1.0 contract, reflected in both specs, is the baseline.",
			token:    "3.1.0",
			exempt:   false,
			whatItIs: "\" contract,\" read it as server history",
		},
		{
			name:     "review 1: vendored by this module",
			path:     "docs/api.md",
			line:     "The contract vendored by this module is server 3.1.0.",
			token:    "3.1.0",
			exempt:   false,
			whatItIs: "\"this module\" read it as an SDK version",
		},
		{
			name:     "a claim whose claim word FOLLOWS the token",
			path:     "docs/api.md",
			line:     "Server 3.1.0 added nothing: it is what both specs track.",
			token:    "3.1.0",
			exempt:   false,
			whatItIs: "the veto reads the whole sentence, not only what precedes the version",
		},
		{
			name:     "a probe in one sentence does not lend itself to the next",
			path:     "transport.go",
			line:     "// Probed against 3.1.0. The baseline is 3.1.0.",
			token:    "3.1.0",
			exempt:   true,
			whatItIs: "the FIRST token is a real note; the case below takes the second",
		},
		{
			name:     "a history word next to a claim word is still a claim",
			path:     "docs/development.md",
			line:     "The refresh that shipped the specs left them vendored at 3.1.0.",
			token:    "3.1.0",
			exempt:   false,
			whatItIs: "\"shipped\" would exempt it as server history; \"vendored\" vetoes every word-based category",
		},
		{
			name:     "a claim in a release-line guard script",
			path:     "scripts/compat-guard.sh",
			line:     "# The contract this guard assumes is vendored at 3.1.0.",
			token:    "3.1.0",
			exempt:   false,
			whatItIs: "release-line-guard is scoped by path, which is not a role, so the veto applies",
		},
		{
			name:     "version.go mentioned before a claim is not a Go toolchain",
			path:     "docs/development.md",
			line:     "Unlike version.go the contract targets 3.1.0.",
			token:    "3.1.0",
			exempt:   false,
			whatItIs: "main's bare \"go \" matched the end of version.go",
		},
		{
			name:     "a harness image reference is exempt even in a claim sentence",
			path:     "docs/development.md",
			line:     "The vendored contract is newer than ghcr.io/octoverse-id/octonomy:3.1.0, which the harness boots.",
			token:    "3.1.0",
			exempt:   true,
			whatItIs: "a ByRole category: the token IS the image tag, whatever the sentence says around it",
		},
		{
			name:     "contract history carrying the old number is a claim now",
			path:     "docs/contract-coverage.yaml",
			line:     "# how this line came to sit on a server 1.0.0 contract while the server shipped 3.1.0 with a second, primary API surface.",
			token:    "1.0.0",
			exempt:   false,
			whatItIs: "\"contract\" is a claim word; this sentence was reworded to carry no version",
		},
		// The second review's constructions, and the author's own probes of the
		// round-1 fix, each of which passed the guard before this round.
		{
			name:     "review 2: a claim word wrapped onto the next line",
			path:     "docs/api.md",
			line:     "Server 3.1.0 added the default this SDK",
			next:     "targets today.",
			token:    "3.1.0",
			exempt:   false,
			whatItIs: "the veto reads to the end of the SENTENCE, not of the physical line",
		},
		{
			name:     "review 2: the harness tag elsewhere on the line",
			path:     "docs/development.md",
			line:     "The vendored contract is 3.1.0; the smoke image is ghcr.io/octoverse-id/octonomy:3.1.0.",
			token:    "3.1.0",
			exempt:   false,
			whatItIs: "the FIRST 3.1.0 is a claim; only a token immediately after octonomy: is the image tag",
		},
		{
			name:     "review 2: a v-prefixed server version",
			path:     "docs/api.md",
			line:     "Both bundled specs target server v3.1.0.",
			token:    "v3.1.0",
			exempt:   false,
			whatItIs: "v3.1.0 was never a token; the server's own tags are spelled that way",
		},
		{
			name:     "review 2: a baseline that moved",
			path:     "CHANGELOG.md",
			line:     "The SDK's contract baseline moved to server 3.1.0.",
			token:    "3.1.0",
			exempt:   false,
			whatItIs: "\"moved\" had no site in this tree and exempted a stale claim",
		},
		{
			name:     "probe: an abbreviation does not end a sentence",
			path:     "docs/api.md",
			line:     "The specs are vendored (e.g. as the server shipped them) at 3.1.0.",
			token:    "3.1.0",
			exempt:   false,
			whatItIs: "\"e.g. \" split the claim word from \"shipped\"",
		},
		{
			name:     "probe: an abbreviation right before the token",
			path:     "docs/api.md",
			line:     "The specs are vendored at e.g. 3.1.0 and newer.",
			token:    "3.1.0",
			exempt:   false,
			whatItIs: "a stop at the very end of the look-back ended the sentence and hid \"vendored\"",
		},
		// The third review's constructions.
		{
			name:     "review 3: a v-tag naming the server",
			path:     "docs/api.md",
			line:     "This SDK uses server v3.1.0.",
			token:    "v3.1.0",
			exempt:   false,
			whatItIs: "no claim word; the v-tag category now refuses a sentence about the server",
		},
		{
			name:     "review 3: a contract that shipped",
			path:     "docs/api.md",
			line:     "The bundled API contract shipped as 3.1.0.",
			token:    "3.1.0",
			exempt:   false,
			whatItIs: "\"shipped\" read it as server history; \"contract\" is now a claim word",
		},
		{
			name:     "review 3: a claim word on the paragraph's fourth line",
			path:     "docs/api.md",
			line:     "The server shipped 3.1.0 as the API version that",
			next:     "every example in this file uses, and which the\nbundled client is written to, and which is the\ntarget for this SDK today.",
			token:    "3.1.0",
			exempt:   false,
			whatItIs: "context was capped at two lines; it is the paragraph now",
		},
		{
			name:     "review 3: a version starting its sentence after a probe",
			path:     "transport.go",
			line:     "// Probed against 3.2.1. 3.1.0 is the baseline.",
			token:    "3.1.0",
			exempt:   false,
			whatItIs: "the review read this as one sentence; the stop right before the token ends it (asserted, not argued)",
		},
		{
			name:     "\"Server X added\" does not exempt a sentence about the contract",
			path:     "docs/api.md",
			line:     "Server 3.1.0 added the contract this SDK is on.",
			token:    "3.1.0",
			exempt:   false,
			whatItIs: "only the claim word \"contract\" stops this: server-history's one shape matches it",
		},
		{
			name:     "an SDK-version phrase needs the version in a code span",
			path:     "CHANGELOG.md",
			line:     "Released entries record 3.1.0 as the baseline.",
			token:    "3.1.0",
			exempt:   false,
			whatItIs: "\"released entr\" alone once exempted any version after it",
		},
		{
			name:     "a link's target is not a word the sentence says",
			path:     "docs/versioning.md",
			line:     "The freeze published with `v1.0.0` is reversed; see [the doc](https://example.com/compat-line-api-v2-parity.md).",
			token:    "v1.0.0",
			exempt:   true,
			whatItIs: "\"api\" inside the URL made this read as a sentence about the server's API",
		},
		{
			name:     "review 4: a v-tag naming the server by its product name",
			path:     "docs/api.md",
			line:     "The SDK baseline is Octonomy v3.1.0.",
			token:    "v3.1.0",
			exempt:   false,
			whatItIs: "the server's name is a server subject as much as the word \"server\" is",
		},
		{
			name:     "this module's own name is not the server's",
			path:     "docs/release.md",
			line:     "> go: github.com/octoverse-id/octonomy-go@v1.0.0: invalid version:",
			token:    "v1.0.0",
			exempt:   true,
			whatItIs: "\"octonomy-go\" is this module, so the product-name subject stops before a hyphen",
		},
		{
			name:     "server history in exactly its one shape",
			path:     "README.md",
			line:     "- **No `CodeScopeImmutable` constant.** Server 3.1.0 added `409 scope_immutable` on tag, vocabulary,",
			token:    "3.1.0",
			exempt:   true,
			whatItIs: "\"Server X added\" is what a release did, not what is vendored",
		},
		{
			name:     "probe: the image registry nearby is not the image tag",
			path:     "docs/development.md",
			line:     "Pulled from ghcr.io/octoverse-id/octonomy, the specs are at server 3.1.0.",
			token:    "3.1.0",
			exempt:   false,
			whatItIs: "\"ghcr.io/\" within 60 bytes made a claim ByRole-exempt",
		},
		{
			name:     "probe: specs that shipped",
			path:     "docs/api.md",
			line:     "Both specs shipped at 3.1.0.",
			token:    "3.1.0",
			exempt:   false,
			whatItIs: "a sentence about the specs is a claim about them whatever its verb",
		},
		{
			name:     "\"Server X added\" does not exempt a sentence about the specs",
			path:     "docs/api.md",
			line:     "Server 3.1.0 added what both specs describe.",
			token:    "3.1.0",
			exempt:   false,
			whatItIs: "only the claim word \"specs\" stops this: server-history's one shape matches it",
		},
		{
			name:     "probe: a full stop is not an octet",
			path:     "docs/api.md",
			line:     "The vendored contract.3.1.0 is here.",
			token:    "3.1.0",
			exempt:   false,
			whatItIs: "ipv4-address needs a DIGIT and a dot, not any dot",
		},
		{
			name:     "this module's tag",
			path:     "AGENTS.md",
			line:     "change here must therefore keep `v1.0.0` callers compiling",
			token:    "v1.0.0",
			exempt:   true,
			whatItIs: "the v-tag category, in a sentence that claims no contract",
		},
		{
			name:     "a release branch named in a claim-shaped sentence",
			path:     "scripts/compat-guard.sh",
			line:     `# reported that the release "targets release/v1.0.1, but v1 releases are cut from`,
			token:    "v1.0.1",
			exempt:   true,
			whatItIs: "release/vX.Y.Z is a branch name by position, whatever verb precedes it",
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
			site.Next = tc.next
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

// Each category is tight ON ITS OWN, not only behind the claim veto.
//
// The veto now stops every construction below, so the classifier test above
// would stay green if a category were loosened again. That is defence in depth
// becoming a blind spot: a stale claim phrased without a claim word reaches the
// categories directly. So each known bypass is checked against the category it
// once passed through, with the veto out of the way.
func TestEachCategoryIsTightWithoutTheVeto(t *testing.T) {
	cases := []struct {
		category, prev, line, token, why string
	}{
		{"verification-note", "", "The SDK is written against server 3.1.0.", "3.1.0",
			"a bare 'against' is not a probe"},
		{"version-range-caveat", "", "The spec is vendored from server 3.1.0.", "3.1.0",
			"\"from server\" is not a range"},
		{"server-history", "", "The currently vendored server 3.1.0 contract, reflected in both specs, is the baseline.", "3.1.0",
			"\" contract,\" is not history"},
		{"server-history", "  - a row removed or duplicated, and a lookup that ignores the receiver",
			"  while the pin stays at 3.1.0.", "3.1.0",
			"\"removed\" must not contain the \" moved\" of history"},
		{"sdk-version", "", "The contract vendored by this module is server 3.1.0.", "3.1.0",
			"\"this module\" is not an SDK version"},
		{"sdk-version", "", "The contract vendored by this module is `3.1.0`.", "3.1.0",
			"\"this module\" is not an SDK version even in a code span, which alone would not exempt it"},
		{"server-history", "", "The bundled API release shipped as 3.1.0.", "3.1.0",
			"\"shipped\" is not the one shape"},
		{"server-history", "", "The SDK sat on 3.1.0 until the refresh.", "3.1.0",
			"\"sat on\" is not the one shape"},
		{"server-history", "", "The baseline 3.1.0 added to this tree is stale.", "3.1.0",
			"\"added\" needs \"Server\" immediately before the version"},
		{"sdk-version", "", "The specs are vendored at server 1.0.0.", "1.0.0",
			"never by value: 1.0.0 is this line's first release AND its old contract"},
		{"go-toolchain-version", "", "Unlike version.go the contract targets 3.1.0.", "3.1.0",
			"\"go \" inside version.go is not the Go toolchain"},
		{"harness-image-pin", "", "The harness image is the same 3.1.0 the contract names.", "3.1.0",
			"the words 'harness image' describe a pin; only the image reference is one"},
		{"external-reference", "", "[Contract](https://example.com/spec) is vendored at server 3.1.0.", "3.1.0",
			"the token must sit inside the URL"},
		{"harness-image-pin", "", "The contract is 3.1.0, like ghcr.io/octoverse-id/octonomy:3.1.0 is.", "3.1.0",
			"the image tag elsewhere on the line is not this token"},
		{"harness-image-pin", "", "Pulled from ghcr.io/octoverse-id/octonomy, the contract is 3.1.0.", "3.1.0",
			"the registry nearby is not the image tag"},
		{"server-history", "", "The SDK's contract baseline moved to server 3.1.0.", "3.1.0",
			"\"moved\" is not history"},
		{"ipv4-address", "", "The vendored contract.3.1.0 is here.", "3.1.0",
			"a full stop before the token is not a fourth octet"},
	}
	for _, tc := range cases {
		var ex *contractVersionExemption
		for i := range contractVersionExemptions {
			if contractVersionExemptions[i].Name == tc.category {
				ex = &contractVersionExemptions[i]
			}
		}
		if ex == nil {
			t.Fatalf("no category named %q", tc.category)
		}
		site := siteIn("docs/api.md", tc.line, tc.token)
		site.Prev = tc.prev
		if ex.Match(site) {
			t.Errorf("%s exempts %q on its own: %s", tc.category, tc.line, tc.why)
		}
	}
}

// A word-based category is scoped to the token's sentence. The line below has a
// real verification note followed by a stale claim with no claim word in it; a
// byte window alone would lend the first sentence's "Probed" to the second.
//
// "No claim word" is load-bearing. This case once read "Both specs: 3.1.0",
// and when "specs" became a claim word the veto stopped the second token on its
// own -- so reverting the sentence scoping passed the suite. The claim must be
// one only the scoping can stop.
func TestContractVersionPhrasesDoNotCrossASentence(t *testing.T) {
	line := "// Probed against 3.1.0. The baseline is 3.1.0."
	if w := (versionSite{Path: "transport.go", Line: line, Token: "3.1.0", Idx: strings.LastIndex(line, "3.1.0")}).claimWord(); w != "" {
		t.Fatalf("the second sentence must carry no claim word, or this tests the veto instead: found %q", w)
	}
	second := versionSite{Path: "transport.go", Line: line, Token: "3.1.0", Idx: strings.LastIndex(line, "3.1.0")}
	if got := classifyContractVersion(second); got != "" {
		t.Errorf("the second 3.1.0 is in its own sentence and must not inherit the probe, got %q", got)
	}

	// A sentence that STARTS with the version: the stop before it is followed by
	// whitespace and is not an abbreviation's, so the token begins a new
	// sentence and nothing before it can lend it a word.
	line = "// Probed against 3.1.0. 3.1.0 is the baseline."
	second = versionSite{Path: "transport.go", Line: line, Token: "3.1.0", Idx: strings.LastIndex(line, "3.1.0")}
	if got := classifyContractVersion(second); got != "" {
		t.Errorf("a sentence starting with the version must not inherit the probe before it, got %q", got)
	}
}

// The veto reads both sides of the token and stops at the sentence's edges.
func TestClaimsContractReadsOneSentence(t *testing.T) {
	cases := []struct {
		line  string
		token string
		want  bool
	}{
		{"The spec is vendored at 3.1.0.", "3.1.0", true},
		{"Server 3.1.0 is what both specs track.", "3.1.0", true},
		{"Both specs track server 3.1.0.", "3.1.0", true},
		{"The contract vendored earlier is gone. Server 3.1.0 added a 409.", "3.1.0", false},
		{"Server 3.1.0 added a 409. The specs are vendored.", "3.1.0", false},
		{"The target-branch check is skipped for 0.1.0.", "0.1.0", false},
		{"The SDK targets 3.1.0.", "3.1.0", true},
		{"| `BASE` | PR target |\n| `VERSION` | the CHANGELOG heading | `1.0.1` |", "1.0.1", false},
	}
	for _, tc := range cases {
		site := versionSite{Path: "docs/api.md", Line: tc.line, Token: tc.token, Idx: strings.Index(tc.line, tc.token)}
		if got := site.claimsContract(); got != tc.want {
			t.Errorf("claimsContract(%q) = %v, want %v", tc.line, got, tc.want)
		}
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

	// A v-prefixed version is a token, compared with the marker on its digits.
	// main's copy at 61fce9b never saw one, so a claim spelled the way the
	// server tags its releases was never read at all.
	got = scanContractVersionsIn("docs/api.md", "Both bundled specs target server v8.8.8.\nAnd v7.7.7 is the marker.\n", "7.7.7")
	if len(got) != 1 || got[0].Token != "v8.8.8" {
		t.Errorf("want exactly the v8.8.8 claim (v7.7.7 is the marker), got %+v", got)
	}
}

// A comment's sentence does not run into the code under it, nor the reverse.
// The release-line guard's test has exactly this shape: a comment quoting an
// error that says the release "targets" a branch, then a fixture line of SDK
// versions. Read as one sentence, the fixture inherited the comment's claim word.
func TestContractVersionContextStaysWithinOneKindOfLine(t *testing.T) {
	body := "# the release \"targets release/v1.0.1\" -- and the spec is vendored at 8.8.8\n" +
		"fixture \"$COMPAT\" 1.13 1.0.1 1.0.1\n"
	got := scanContractVersionsIn("scripts/compat-guard-test.sh", body, "7.7.7")
	if len(got) != 1 || got[0].Token != "8.8.8" || got[0].Line != 1 {
		t.Errorf("want only the comment's own claim (8.8.8 on line 1), got %+v", got)
	}
	// Review 7: a bare comment marker is a blank line in that syntax.
	for path, body := range map[string]string{
		"transport.go":    "// Probed against 3.1.0\n//\n// The baseline is 8.8.8\n",
		"scripts/x.sh":    "# Probed against 3.1.0\n#\n# The baseline is 8.8.8\n",
		".github/ci.yml":  "# Probed against 3.1.0\n#\n# The baseline is 8.8.8\n",
		"docs/example.md": "> Probed against 3.1.0\n> >\n> The baseline is 8.8.8\n",
	} {
		if got := scanContractVersionsIn(path, body, "7.7.7"); len(got) != 1 || got[0].Token != "8.8.8" {
			t.Errorf("%s: a marker-only line must end the paragraph, got %+v", path, got)
		}
	}

	// neighbours itself stops at a marker-only line, on either side.
	gocomment := []string{"// a", "//", "// b", "//", "// c"}
	if prev, next := neighbours(lineKinds("x.go", gocomment), gocomment, 2); prev != "" || next != "" {
		t.Errorf("a bare // is a blank line: \"// b\" has no neighbours, got %q / %q", prev, next)
	}

	sh := []string{"# a", "# b", "code", "# c"}
	if prev, next := neighbours(lineKinds("x.sh", sh), sh, 2); prev != "" || next != "" {
		t.Errorf("a code line between comments has no neighbours of its kind, got %q / %q", prev, next)
	}
	// Three lines each side of the token, so a two-line cap (this port's first
	// version, and main's look-back) cannot pass it.
	md := []string{"zero", "", "one", "two", "three", "four", "five", "six", "seven", "", "eight"}
	if prev, next := neighbours(lineKinds("x.md", md), md, 5); prev != "one\ntwo\nthree" || next != "five\nsix\nseven" {
		t.Errorf("Markdown prose runs to the paragraph's edges, got %q / %q", prev, next)
	}

	// A numbered item is its own statement, like a bulleted one. Review 4: an
	// unpunctuated probe item lent "Probed" to the numbered item after it. "2) "
	// rather than "2. ", because the full stop in "2. " is already a break and
	// would pass this whether or not numbered items are.
	for _, list := range []string{
		"1) Probed against 3.1.0\n2) The baseline is 8.8.8\n",
		// Review 5: "+" is a CommonMark bullet too.
		"+ Probed against 3.1.0\n+ The baseline is 8.8.8\n",
		"- Probed against 3.1.0\n- The baseline is 8.8.8\n",
		"* Probed against 3.1.0\n* The baseline is 8.8.8\n",
		// Review 6: this repository writes lists inside blockquotes.
		"> - Probed against 3.1.0\n> - The baseline is 8.8.8\n",
		"> 1) Probed against 3.1.0\n> 2) The baseline is 8.8.8\n",
		// And a quoted blank line ends a quoted paragraph.
		"> Probed against 3.1.0\n>\n> The baseline is 8.8.8\n",
		// Review 7: a heading does not run into the paragraph under it, and
		// inside a fence a comment line and a command line are different kinds.
		"## Probed against 3.1.0\nThe baseline is 8.8.8\n",
		"```sh\n# Probed against 3.1.0\necho 8.8.8\n```\n",
	} {
		if got := scanContractVersionsIn("docs/development.md", list, "7.7.7"); len(got) != 1 || got[0].Token != "8.8.8" {
			t.Errorf("the second item must not inherit the first's probe in %q, got %+v", list, got)
		}
	}

	// A YAML block scalar's '#' line is content, not a comment.
	yaml := []string{
		"    unimplemented: >-",
		"      The server release named 3.1.0 is the one that",
		"      #91 says both bundled specs target today.",
		"    documented_response: none",
	}
	kinds := lineKinds("docs/contract-coverage.yaml", yaml)
	if kinds[2] != "code" || kinds[1] != "code" {
		t.Errorf("block-scalar content must be one kind whatever it starts with, got %v", kinds)
	}
	if got := scanContractVersionsIn("docs/contract-coverage.yaml", strings.Join(yaml, "\n"), "7.7.7"); len(got) != 1 || got[0].Token != "3.1.0" {
		t.Errorf("the reason's own claim must be read across its '#91' line, got %+v", got)
	}
}

// The guard reads the files the repository TRACKS, and nothing else in the
// checkout. Built on a throwaway repository so the untracked and ignored cases
// are real rather than assumed: a local notes file with a stale claim in it must
// not fail anyone's `go test` (the review on #109).
func TestContractVersionGuardReadsOnlyTrackedFiles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		// Only the git path is under test here, and without a git binary it
		// cannot run: repositoryFiles walks instead, which the guard's own run
		// above exercises. Every CI job this line has checks out with git.
		t.Skip("no git binary; repositoryFiles falls back to walking, which this test does not cover")
	}
	dir, err := ioutil.TempDir("", "contractversion-files")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { // t.Cleanup needs Go 1.14
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("remove %s: %v", dir, err)
		}
	}()

	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, body string) {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := ioutil.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q")
	write("docs/tracked.md", "tracked\n")
	write("notes.md", "Scratch notes: the spec is vendored at server 3.1.0.\n")
	write("ignored/scratch.md", "vendored at server 3.1.0\n")
	write(".gitignore", "ignored/\n")
	git("add", "docs/tracked.md", ".gitignore")

	files, source, err := repositoryFiles(dir)
	if err != nil {
		t.Fatalf("repositoryFiles: %v", err)
	}
	if source != "git" {
		t.Fatalf("a git work tree must be listed by git, got %q", source)
	}
	if got := strings.Join(files, ","); got != ".gitignore,docs/tracked.md" {
		t.Errorf("want exactly the tracked files, slash-separated; got %q", got)
	}

	// And with no .git to ask, the walk is the fallback: it reads everything,
	// which fails closed.
	if err := os.RemoveAll(filepath.Join(dir, ".git")); err != nil {
		t.Fatal(err)
	}
	files, source, err = repositoryFiles(dir)
	if err != nil || source != "walk" {
		t.Fatalf("without .git the walk must answer, got %q, %v", source, err)
	}
	if got := strings.Join(files, ","); !strings.Contains(got, "notes.md") || !strings.Contains(got, "ignored/scratch.md") {
		t.Errorf("the fallback walk reads every file, got %q", got)
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
