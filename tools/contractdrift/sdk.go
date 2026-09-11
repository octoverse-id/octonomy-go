package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// This file reads the three non-YAML inputs: the SDK's Go sources, the server's
// Python error registry, and the contract version recorded in docs/versioning.md.
//
// All three are read with regexps rather than parsers, which is a real trade and
// is bounded the same way each time: every extractor asserts a floor on what it
// found and fails the run when the floor is not met. A regexp that silently stops
// matching would otherwise report "the SDK defines no error codes" as drift, or
// worse, report a clean comparison it never made.

var (
	// `CodeNotFound = "not_found"`, inside a const block or on its own line.
	goErrorCodeRE = regexp.MustCompile(`\b(Code[A-Za-z0-9]+)\s*=\s*"([a-z0-9_]+)"`)

	// Every code the server can put in the error envelope. Two shapes, because
	// core/errors.py writes them two ways: a `code = "..."` attribute on each
	// DomainError subclass, and bare literals in the DRF exception handler
	// (`error_response("not_found", ...)`, `code = "authentication_required"`).
	pyClassCodeRE = regexp.MustCompile(`(?m)^\s*code\s*=\s*"([a-z0-9_]+)"`)
	pyCallCodeRE  = regexp.MustCompile(`error_response\(\s*"([a-z0-9_]+)"`)

	// The two halves of "which query parameters does this client actually send".
	//
	// A resource file writes the name inline -- `q.Set("application_id", ...)` --
	// while transport.go, which sets the scope parameters for every resource at
	// one chokepoint, writes them through constants: `applicationIDParam =
	// "application_id"`, then `merged.Set(applicationIDParam, ...)`. Matching only
	// the inline form would have reported the transport's own parameters as never
	// sent, so the setter argument is resolved through the constant table below.
	//
	// Scanning for the bare literal instead would be simpler and wrong in the
	// other direction: `scope` also names a struct field and a JSON tag, so a
	// parameter the SDK models but never puts on the wire would read as sent.
	goSetterRE = regexp.MustCompile(`\.Set\(\s*([A-Za-z_][A-Za-z0-9_]*|"[a-z][a-z0-9_]*")\s*,`)
	goConstRE  = regexp.MustCompile(`(?m)^\s*([A-Za-z_][A-Za-z0-9_]*)\s*=\s*"([a-z][a-z0-9_]*)"\s*(?://.*)?$`)

	// The machine-readable marker in docs/versioning.md.
	versioningMarkerRE = regexp.MustCompile(`<!--\s*contract-version:\s*([0-9][^\s]*)\s*-->`)
)

// The floors the two error-code extractors assert. A regexp that stops matching
// reports an empty set, and an empty set compares clean in one direction while
// flooding in the other -- neither of which is "the extractor broke". These turn
// that into a run that fails and says so.
//
// Counted rather than guessed, and deliberately well below the real numbers so an
// ordinary removal does not trip them: the server's registry carried 14 distinct
// codes when this was written (11 DomainError subclasses plus not_found,
// authentication_required, and forbidden from the DRF handler), and errors.go
// carried 16 constants.
const (
	minServerErrorCodes = 10
	minSDKErrorCodes    = 10
)

// GoSources is every non-test .go file in the SDK package directory, by base name.
type GoSources map[string]string

// LoadGoSources reads the SDK's package sources. Tests are excluded on purpose:
// a query parameter that appears only in a fixture is a parameter the client
// never sends.
func LoadGoSources(root string) (GoSources, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	out := make(GoSources)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			return nil, err
		}
		out[name] = string(raw)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no .go sources -- is this the SDK repository root?", root)
	}
	return out, nil
}

// QueryParams returns every query-parameter name the SDK sets anywhere, with the
// files that set it.
//
// "Anywhere" is the right granularity, and narrower would be worse. Mapping a
// parameter to the one operation that sends it would need the params struct
// resolved to a resource method, and the miss this check exists to catch -- a
// vendored contract refreshed with a new parameter that nobody then implemented
// -- shows up as a name the client does not mention at all.
func (s GoSources) QueryParams() map[string][]string {
	// Constant table first: transport.go names the scope parameters through
	// constants, and a setter that takes one has to resolve to the literal.
	consts := make(map[string]string)
	for _, src := range s {
		for _, m := range goConstRE.FindAllStringSubmatch(src, -1) {
			consts[m[1]] = m[2]
		}
	}

	out := make(map[string][]string)
	for name, src := range s {
		for _, m := range goSetterRE.FindAllStringSubmatch(src, -1) {
			arg := m[1]
			param := ""
			switch {
			case strings.HasPrefix(arg, `"`):
				param = strings.Trim(arg, `"`)
			default:
				param = consts[arg]
			}
			if param == "" {
				continue
			}
			out[param] = append(out[param], name)
		}
	}
	for key := range out {
		sort.Strings(out[key])
		out[key] = slicesCompact(out[key])
	}
	return out
}

// slicesCompact removes adjacent duplicates from a sorted slice. A file that sets
// the same parameter in three list methods should be named once.
func slicesCompact(in []string) []string {
	out := in[:0]
	for i, v := range in {
		if i == 0 || in[i-1] != v {
			out = append(out, v)
		}
	}
	return out
}

// HasMethod reports whether file declares the method named "Receiver.Method".
func (s GoSources) HasMethod(file, symbol string) bool {
	src, ok := s[file]
	if !ok {
		return false
	}
	receiver, method, found := strings.Cut(symbol, ".")
	if !found {
		return false
	}
	// The receiver variable is not part of the identity, so it is matched loosely;
	// the receiver TYPE and the method name are the identity and are matched
	// exactly.
	re := regexp.MustCompile(`func \(\w+ \*` + regexp.QuoteMeta(receiver) + `\) ` + regexp.QuoteMeta(method) + `\(`)
	return re.MatchString(src)
}

// SDKErrorCodes extracts the Code* constants from errors.go.
func SDKErrorCodes(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string)
	for _, m := range goErrorCodeRE.FindAllStringSubmatch(string(raw), -1) {
		out[m[2]] = m[1]
	}
	if len(out) < minSDKErrorCodes {
		return nil, fmt.Errorf("%s: found only %d Code* constants (expected at least %d) -- the extractor, not the SDK, is what changed",
			path, len(out), minSDKErrorCodes)
	}
	return out, nil
}

// ServerErrorCodes extracts the error codes from the server's core/errors.py.
func ServerErrorCodes(path string) (map[string]bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool)
	for _, m := range pyClassCodeRE.FindAllStringSubmatch(string(raw), -1) {
		out[m[1]] = true
	}
	for _, m := range pyCallCodeRE.FindAllStringSubmatch(string(raw), -1) {
		out[m[1]] = true
	}
	if len(out) < minServerErrorCodes {
		return nil, fmt.Errorf("%s: found only %d error codes (expected at least %d) -- core/errors.py moved or changed shape",
			path, len(out), minServerErrorCodes)
	}
	return out, nil
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
