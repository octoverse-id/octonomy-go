package main

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// The two inputs that are neither OpenAPI nor Go: the server's Python error
// registry, and the contract version recorded in docs/versioning.md.
//
// The Go side used to live here too, read with regexps. It does not any more --
// gosdk.go parses it, because `CodeFoo ErrorCode = "foo"` and
// `scopeParam string = "scope"` are both ordinary Go that a pattern written for
// untyped constants stops matching, SILENTLY, leaving the gate reporting a
// comparison it never made.
//
// The Python file cannot get the same treatment from a Go program, so it gets the
// next best thing: an extractor that knows which spellings it understands, and
// REPORTS THE ONES IT DOES NOT. A code the server can raise that this cannot read
// is a hole in the comparison, and the run says so instead of quietly shrinking
// the registry it compares against.
var (
	// `code = <anything>` on a DomainError subclass, capturing the WHOLE
	// right-hand side -- a plain literal, an alias, an f-string, a concatenation,
	// whatever is there.
	//
	// One pattern, not two. There used to be a narrow reader for the shapes this
	// understands and a wide detector for the shapes it does not, and three review
	// rounds running found codes that fell between them: a value the narrow one
	// skipped and the wide one called readable vanished with no diagnostic at all.
	// Two predicates that have to agree will eventually disagree. Now there is one
	// capture and one predicate -- pyLiteralValue -- and every right-hand side is
	// either read or reported, with no third outcome available.
	//
	// NOT anchored to the start of a line, so a one-line class body --
	// `class InlineError(DomainError): code = "inline"` -- is seen. `\b` is what
	// keeps it off `error_code = ...`, where the underscore leaves no word boundary.
	// `=([^=].*)$` and not `=\s*(.+)$`, because `if code == "first":` matched the
	// first `=` of `==` and was reported as an unreadable assignment. Three
	// ordinary comparisons in a handler produced three findings, and a scheduled
	// job nobody reads catches nothing.
	// `(?:^|:)` and not `\b`, because `\bcode` matched `response.code = "x"` -- an
	// attribute on an unrelated object, read as a server error code that does not
	// exist. A code is the first thing on its line, or it follows the colon of a
	// one-line class body; nothing else is one.
	pyClassCodeRE = regexp.MustCompile(`(?m)(?:^|:)\s*code\s*(?::[^=\n]*)?=([^=].*)$`)

	// `error_response("not_found", ...)` in the DRF exception handler, capturing
	// the first argument whatever it is.
	//
	// `\s*` before the paren because `error_response ("spaced", ...)` is valid
	// Python and was matched by neither the old reader nor the old detector, so a
	// code written that way disappeared while the count floor stayed healthy.
	//
	// The definition line -- `def error_response(code: str, ...)` -- is filtered by
	// the caller, since Go's regexp has no lookahead. It has to be filtered
	// somewhere: reporting a function's own signature as an unreadable code is the
	// kind of noise that teaches everyone to stop reading a scheduled job.
	// `[\s\\]*` before the paren covers both `error_response ("x", ...)` and a
	// backslash line continuation, each of which is valid Python that matched
	// nothing at all.
	pyCallCodeRE = regexp.MustCompile(`(?m)^(.*?)error_response[\s\\]*\(\s*([^,\n]*)`)

	// The argument spellings that resolve to a DomainError's own `code`, which the
	// class pattern above has already read: the local `code` the DRF handler
	// assigns, and `exc.code` / `error.code` on a raised domain error. Suppressed
	// rather than reported, because the code each names IS in the extracted set --
	// and reporting one line per variable spelling is a flood, not a diagnostic.
	//
	// An EXPLICIT list, not `<any identifier>.code`, which is what it was. That
	// pattern suppressed `response.code` too -- a code this reader cannot see, on
	// an object that is not a DomainError -- so an unreadable call was silently
	// treated as one already accounted for. A spelling not listed here is reported,
	// and the fix for a legitimate new one is to add it deliberately.
	pyResolvedCodes = map[string]bool{
		"code":       true,
		"exc.code":   true,
		"error.code": true,
	}

	// The machine-readable marker in docs/versioning.md.
	versioningMarkerRE = regexp.MustCompile(`<!--\s*contract-version:\s*([0-9][^\s]*)\s*-->`)
)

// minServerErrorCodes is the floor the Python extractor asserts. A regexp that
// stops matching reports an empty set, and an empty set compares clean in one
// direction while flooding in the other -- neither of which is "the extractor
// broke". This turns that into a run that fails and says so.
//
// Counted rather than guessed, and deliberately well below the real number so an
// ordinary removal does not trip it: the registry carried 14 distinct codes when
// this was written -- 11 DomainError subclasses plus not_found,
// authentication_required, and forbidden from the DRF handler.
//
// It is a floor and not a fingerprint, which is why the unreadable-form report
// below matters more: a single new code in a spelling this cannot parse leaves
// the count healthy.
const minServerErrorCodes = 10

// minSDKErrorCodes is the same floor for the SDK's own Code* constants, of which
// errors.go declared 16 when this was written. The Go side is parsed rather than
// matched, so this guards against a move -- errors.go renamed, or the constants
// relocated to a file this package no longer reads -- rather than against a
// pattern that stopped matching.
const minSDKErrorCodes = 10

// ServerErrorCodes extracts the error codes from the server's core/errors.py,
// alongside the code-producing lines it could not read.
//
// Every right-hand side the two patterns capture reaches exactly one of three
// places: the extracted set, the suppressed-alias case, or the unreadable list.
// There is deliberately no fourth, because the fourth was where codes went to
// disappear.
func ServerErrorCodes(path string) (codes map[string]bool, unreadable []string, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	src := stripPyComments(string(raw))

	codes = map[string]bool{}
	seen := map[string]bool{}
	record := func(form, rhs string) {
		if value, ok := pyLiteralValue(rhs); ok {
			codes[value] = true
			return
		}
		if pyResolvedCodes[trimPyExpr(rhs)] {
			return // a DomainError's own code, already read from its class
		}
		if !seen[form] {
			seen[form] = true
			unreadable = append(unreadable, form)
		}
	}

	for _, m := range pyClassCodeRE.FindAllStringSubmatch(src, -1) {
		record("code = "+trimPyExpr(m[1]), m[1])
	}
	for _, m := range pyCallCodeRE.FindAllStringSubmatch(src, -1) {
		// The function's own definition, not a call -- and matched on the LINE's
		// shape rather than on the text containing "def " anywhere, which suppressed
		// a real call on any line that happened to mention it.
		if prefix := strings.TrimSpace(m[1]); prefix == "def" || strings.HasPrefix(prefix, "def ") {
			continue
		}
		record("error_response("+trimPyExpr(m[2])+", ...)", m[2])
	}

	if len(codes) < minServerErrorCodes {
		return nil, nil, fmt.Errorf("%s: found only %d error codes (expected at least %d) -- core/errors.py moved or changed shape",
			path, len(codes), minServerErrorCodes)
	}
	sort.Strings(unreadable)
	return codes, unreadable, nil
}

// pyLiteralValue returns the value of a Python expression that is one plain
// string literal, and reports whether it was one.
//
// The single predicate the extractor runs on. It is deliberately strict, and
// every case it refuses is REPORTED rather than guessed at:
//
//   - a backslash, because `"brand\x5fnew"` is a valid literal for `brand_new`
//     and reading it verbatim records a code the server does not have;
//   - a quote inside the literal, which means an f-string, a concatenation, or
//     two adjacent literals -- `"joined" "_code"` is one Python string, and
//     reading the first half invents `joined`;
//   - anything not quoted at both ends: an alias, a lookup, a call.
func pyLiteralValue(expr string) (string, bool) {
	expr = trimPyExpr(expr)
	if len(expr) < 2 {
		return "", false
	}
	quote := expr[0]
	if (quote != '"' && quote != '\'') || expr[len(expr)-1] != quote {
		return "", false
	}
	inner := expr[1 : len(expr)-1]
	if strings.ContainsAny(inner, "\\'\"") {
		return "", false
	}
	return inner, true
}

// trimPyExpr strips the whitespace and trailing comma around a captured
// expression, so `"not_found",` and `"not_found"` are the same thing.
func trimPyExpr(expr string) string {
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(expr), ","))
}

// stripPyComments blanks out `#` comments, line by line.
//
// A commented-out `code = "..."` was read as a live server code, and a phantom
// code demands a Code* constant that must not exist -- a weekly job reporting a
// line the server deleted is the nag this gate was told not to become. Only
// comments that begin a line or follow code outside a string are removed; a `#`
// inside a literal is left alone, since that is part of a value.
//
// Docstrings are NOT stripped. A prose `code = "..."` inside one is still read,
// and that is the deliberate side of the trade: narrowing the readers to exclude
// prose would narrow them to exclude real codes too, and a phantom code fails
// loudly while a missed one fails silently.
func stripPyComments(src string) string {
	lines := strings.Split(src, "\n")
	for n, line := range lines {
		var quote rune
		escaped := false
		cut := -1
		for i, r := range line {
			switch {
			case escaped:
				// The character after a backslash is data, whatever it is. Without
				// this, `code = "tag\"#collision"` read the escaped quote as closing
				// the string and everything after `#` as a comment -- which did not
				// merely lose the code, it recorded `tag\` as one. A wrong code is
				// worse than an unreadable one: the unreadable ones are reported.
				escaped = false
			case quote != 0 && r == '\\':
				escaped = true
			case quote != 0:
				if r == quote {
					quote = 0
				}
			case r == '\'' || r == '"':
				quote = r
			case r == '#':
				cut = i
			}
			if cut >= 0 {
				break
			}
		}
		if cut >= 0 {
			line = line[:cut]
		}
		lines[n] = line
	}
	return strings.Join(lines, "\n")
}

// RecordedContractVersion reads the server contract docs/versioning.md claims the
// SDK targets.
func RecordedContractVersion(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	m := versioningMarkerRE.FindSubmatch(raw)
	if m == nil {
		return "", fmt.Errorf("%s: no `<!-- contract-version: X.Y.Z -->` marker -- the recorded contract version cannot be checked", path)
	}
	return string(m[1]), nil
}
