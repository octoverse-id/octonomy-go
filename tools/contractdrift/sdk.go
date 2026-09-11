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
	// `code = "not_found"` on a DomainError subclass, single or double quoted,
	// with an optional type annotation (`code: str = "not_found"`).
	//
	// NOT anchored to the start of a line. It was, and a one-line class body --
	// `class InlineError(DomainError): code = "inline"` -- was then invisible to
	// this AND to the unreadable-form detector below, so a real code vanished from
	// the comparison while the count floor stayed healthy. `\b` is what keeps it
	// off `error_code = ...`, where the underscore leaves no word boundary.
	pyClassCodeRE = regexp.MustCompile(`\bcode\s*(?::[^=\n]*)?=\s*["']([a-z0-9_]+)["']`)

	// `error_response("not_found", ...)` in the DRF exception handler.
	pyCallCodeRE = regexp.MustCompile(`error_response\(\s*["']([a-z0-9_]+)["']`)

	// The same two shapes with anything other than a plain string literal where
	// the code belongs: an enum member, a lookup, an f-string, a constant from
	// somewhere else. Each match the two extractors above did not already account
	// for is reported as a form the gate cannot read.
	//
	// `(?m)^(?:(?!def )...)` is not available here (Go's regexp has no lookahead),
	// so the definition line -- `def error_response(code: str, ...)` -- is filtered
	// by the caller instead. It has to be filtered somewhere: reporting a
	// function's own signature as an unreadable code is the kind of noise that
	// teaches everyone to stop reading a scheduled job's output.
	pyClassCodeAnyRE = regexp.MustCompile(`(?m)\bcode\s*(?::[^=\n]*)?=\s*(.+)$`)
	pyCallCodeAnyRE  = regexp.MustCompile(`(?m)^(.*?)error_response\(\s*([^,\s][^,]*)`)

	// An argument that resolves to a DomainError's own `code` attribute, which the
	// class pattern above has already read: the local `code` the DRF handler
	// assigns, and `exc.code` / `error.code` on a raised domain error. Suppressed
	// rather than reported, because the code it names IS in the extracted set.
	// The limit is worth knowing: a `.code` attribute on something that is not a
	// DomainError would be suppressed here too.
	pyResolvedCodeRE = regexp.MustCompile(`^(?:[A-Za-z_][A-Za-z0-9_]*\.)?code$`)

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
func ServerErrorCodes(path string) (codes map[string]bool, unreadable []string, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	src := string(raw)

	codes = map[string]bool{}
	for _, m := range pyClassCodeRE.FindAllStringSubmatch(src, -1) {
		codes[m[1]] = true
	}
	for _, m := range pyCallCodeRE.FindAllStringSubmatch(src, -1) {
		codes[m[1]] = true
	}
	if len(codes) < minServerErrorCodes {
		return nil, nil, fmt.Errorf("%s: found only %d error codes (expected at least %d) -- core/errors.py moved or changed shape",
			path, len(codes), minServerErrorCodes)
	}

	seen := map[string]bool{}
	for _, m := range pyClassCodeAnyRE.FindAllStringSubmatch(src, -1) {
		if value := strings.TrimSpace(m[1]); !isPyStringLiteral(value) && !seen[value] {
			seen[value] = true
			unreadable = append(unreadable, "code = "+value)
		}
	}
	for _, m := range pyCallCodeAnyRE.FindAllStringSubmatch(src, -1) {
		if strings.Contains(m[1], "def ") {
			continue // the function's own definition, not a call
		}
		value := strings.TrimSpace(m[2])
		if isPyStringLiteral(value) || pyResolvedCodeRE.MatchString(value) || seen[value] {
			continue
		}
		seen[value] = true
		unreadable = append(unreadable, "error_response("+value+", ...)")
	}
	sort.Strings(unreadable)
	return codes, unreadable, nil
}

// isPyStringLiteral reports whether a Python expression is a plain, single-part
// string literal -- the only form the extractors above can read a code out of.
func isPyStringLiteral(expr string) bool {
	expr = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(expr), ","))
	if len(expr) < 2 {
		return false
	}
	quote := expr[0]
	if quote != '"' && quote != '\'' {
		return false
	}
	if expr[len(expr)-1] != quote {
		return false
	}
	// An f-string or a concatenation is not a literal this can read.
	return !strings.ContainsRune(expr[1:len(expr)-1], rune(quote))
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
