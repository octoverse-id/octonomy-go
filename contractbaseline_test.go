package octonomy

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/ioutil"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// --- the contract baseline: two specs, one marker, one row per operation -------
//
// #90 brought this line's vendored contract from 1.0.0 to the release the marker
// in docs/versioning.md names, vendored a second spec for /api/v2, and wrote
// docs/contract-coverage.yaml. Its acceptance was three claims:
//
//   - both specs' info.version equals the marker;
//   - every operation the contracts publish has a coverage row;
//   - a row for an operation this tree does not implement carries a reason.
//
// On main at 61fce9b, tools/contractdrift held all three (checkRecordedVersion and
// the coverage checks), in a nested go 1.24 module with a real YAML parser. This
// branch has no contract gate -- porting one is #98 -- and a claim that nothing
// checks is exactly the state #90 existed to end. So these tests hold them in the
// meantime, in the root package, where `make test` and the go1.13 job both run
// them.
//
// # Why this reads YAML by hand, and how far it trusts itself
//
// main's gate refused a hand-rolled OpenAPI reader, and was right to: the specs
// use folded block scalars, multi-line plain scalars and quoted keys, and a reader
// wrong about any of them would be a gate that quietly stops reporting. This file
// takes a far narrower job, and it takes it FAIL-CLOSED rather than by trusting a
// grammar it does not implement:
//
//   - From a spec it reads info.version and the keys directly under `paths:` --
//     path keys at two spaces, their method keys at four. Everything deeper is
//     ignored, which is where every scalar form above lives. A line at either of
//     those two depths that it cannot account for is an ERROR, not a skip: a path
//     the reader does not recognise is an operation it would otherwise miss.
//   - From contract-coverage.yaml, a file this repository writes, it reads the
//     `operations:` rows and refuses any key, value form or layout it was not
//     written for -- the analogue of the strict decoding (yaml.v3's
//     KnownFields) main's gate used at 61fce9b.
//   - It finds zero operations in no case. An empty read of either file fails.
//
// # What it does NOT check
//
//   - The response fields, the composite bodies, and whether a method sends what
//     the contract documents. Those take a gate that calls each method, and
//     porting one is #98.
//   - That an `sdk:` method implements the operation its row names. It checks the
//     method is declared on that receiver; which route it requests is what the
//     gate's recording stub proves, by calling it.
//   - That an `unimplemented:` reason is TRUE. Like contractversion_test.go it is
//     syntactic: the reason must exist and say something, and no more.

// specSurfaces are the vendored contracts, each with the /api/<version> segment
// its versioned paths must carry. A path in openapi.yaml under /api/v2 is not a
// v1 operation, and reading it as one would let the surfaces disagree silently.
var specSurfaces = []struct {
	Path    string
	Version string
}{
	{"docs/openapi.yaml", "v1"},
	{"docs/openapi-v2.yaml", "v2"},
}

// httpMethods are the OpenAPI path-item keys that name an operation.
var httpMethods = map[string]bool{
	"get": true, "put": true, "post": true, "delete": true,
	"options": true, "head": true, "patch": true, "trace": true,
}

// pathItemFields are the OpenAPI path-item keys that are NOT operations. Named so
// that anything else at that depth is refused rather than ignored.
//
// `$ref` is deliberately absent. A path item may be a reference to another one,
// which publishes that item's operations under this path, and this reader does
// not resolve references -- so accepting the key would read a path that publishes
// operations as a path that publishes none. Refusing it is the fail-closed
// answer; the generator does not emit one today.
var pathItemFields = map[string]bool{
	"summary": true, "description": true, "servers": true, "parameters": true,
}

// vendoredSpec is what this file reads out of one OpenAPI document.
type vendoredSpec struct {
	Path    string
	Version string          // info.version
	Ops     map[string]bool // "method /suffix"
}

var (
	specPathKey   = regexp.MustCompile(`^  (/[^\s:'"]*):$`)
	specFieldKey  = regexp.MustCompile(`^    ([A-Za-z$][A-Za-z0-9_$-]*):`)
	specInfoVer   = regexp.MustCompile(`^  version:\s*['"]?([^'"\s]+)['"]?\s*$`)
	specTopKey    = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_-]*):`)
	surfacePrefix = regexp.MustCompile(`^/api/([^/]+)(/.*)$`)
)

// indentOf returns the number of leading spaces. A tab in the indentation is not
// YAML and is reported as -1 so the caller refuses the line.
func indentOf(line string) int {
	n := 0
	for n < len(line) && line[n] == ' ' {
		n++
	}
	if n < len(line) && line[n] == '\t' {
		return -1
	}
	return n
}

// parseVendoredSpec reads info.version and the operation keys of one spec.
// surface is the /api/<version> segment every versioned path must carry.
func parseVendoredSpec(path, body, surface string) (*vendoredSpec, error) {
	spec := &vendoredSpec{Path: path, Ops: map[string]bool{}}
	section := ""
	currentPath := ""
	versions := 0
	// seqOK is true while the last depth-4 key was a path-item field. YAML lets
	// that field's list sit at the key's own depth ("indentless"), so a "- " line
	// at depth 4 is its item -- and anywhere else it is a layout this reader was
	// not written for.
	seqOK := false

	for i, line := range strings.Split(body, "\n") {
		lineNo := i + 1
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := indentOf(line)
		if indent < 0 {
			return nil, fmt.Errorf("%s:%d: tab in indentation", path, lineNo)
		}
		if indent == 0 {
			m := specTopKey.FindStringSubmatch(line)
			if m == nil {
				return nil, fmt.Errorf("%s:%d: unrecognised top-level line %q", path, lineNo, line)
			}
			section = m[1]
			currentPath = ""
			seqOK = false
			continue
		}

		switch section {
		case "info":
			if indent == 2 && strings.HasPrefix(trimmed, "version:") {
				m := specInfoVer.FindStringSubmatch(line)
				if m == nil {
					return nil, fmt.Errorf("%s:%d: unreadable info.version %q", path, lineNo, line)
				}
				spec.Version = m[1]
				versions++
			}
		case "paths":
			switch {
			case indent == 2:
				m := specPathKey.FindStringSubmatch(line)
				if m == nil {
					return nil, fmt.Errorf("%s:%d: a key directly under paths: that is not a plain path %q -- "+
						"refused rather than skipped, since a path this reader cannot see is an operation "+
						"no coverage row is asked for", path, lineNo, line)
				}
				suffix := m[1]
				if sm := surfacePrefix.FindStringSubmatch(suffix); sm != nil {
					if sm[1] != surface {
						return nil, fmt.Errorf("%s:%d: %s is under /api/%s, but this file is the %s contract",
							path, lineNo, suffix, sm[1], surface)
					}
					suffix = sm[2]
				}
				currentPath = suffix
				seqOK = false
			case indent == 4 && strings.HasPrefix(trimmed, "- "):
				if !seqOK {
					return nil, fmt.Errorf("%s:%d: a sequence item at depth 4 that belongs to no path-item field", path, lineNo)
				}
			case indent == 4:
				if currentPath == "" {
					return nil, fmt.Errorf("%s:%d: a path-item key with no path above it", path, lineNo)
				}
				m := specFieldKey.FindStringSubmatch(line)
				if m == nil {
					return nil, fmt.Errorf("%s:%d: unrecognised path-item line %q", path, lineNo, line)
				}
				key := m[1]
				switch {
				case httpMethods[key]:
					op := key + " " + currentPath
					if spec.Ops[op] {
						return nil, fmt.Errorf("%s:%d: %s is published twice", path, lineNo, op)
					}
					spec.Ops[op] = true
					seqOK = false
				case pathItemFields[key]:
					seqOK = true
				default:
					return nil, fmt.Errorf("%s:%d: %q under %s is neither an HTTP method nor a path-item field",
						path, lineNo, key, currentPath)
				}
			case indent < 4:
				return nil, fmt.Errorf("%s:%d: line at depth %d under paths:, where only 2 and 4 are keys",
					path, lineNo, indent)
			}
		}
	}

	switch {
	case versions != 1:
		return nil, fmt.Errorf("%s: found %d info.version keys, want exactly 1", path, versions)
	case len(spec.Ops) == 0:
		return nil, fmt.Errorf("%s: no operations under paths: -- an empty read would make every row look stale "+
			"and every gap look covered", path)
	}
	return spec, nil
}

// coverageRow is one entry under `operations:` in contract-coverage.yaml.
type coverageRow struct {
	Line          int
	Path          string
	Method        string
	SDK           string
	Unimplemented string
}

func (r coverageRow) key() string { return r.Method + " " + r.Path }

// coverageRowKeys are the keys a row may carry -- the fields of main's
// CoverageOperation at 61fce9b, so the file stays loadable by the gate #98 ports. Anything else is a
// typo, and a typo'd key is a row that silently does nothing.
var coverageRowKeys = map[string]bool{
	"path": true, "method": true, "sdk": true, "unimplemented": true,
	"documented_response": true, "actual_response": true,
	"composite_body": true, "undocumented_request_body": true,
}

var (
	coverageItemStart = regexp.MustCompile(`^  - ([a-z_]+):(.*)$`)
	coverageItemKey   = regexp.MustCompile(`^    ([a-z_]+):(.*)$`)
	coverageAnchor    = regexp.MustCompile(`^&([A-Za-z0-9_-]+)(?:\s+(.*))?$`)
	coverageAlias     = regexp.MustCompile(`^\*([A-Za-z0-9_-]+)$`)
)

// parseCoverageRows reads the `operations:` rows of contract-coverage.yaml.
//
// It understands exactly the value forms that file uses -- a plain scalar, a
// single-quoted scalar, a folded `>-` block, and an `&anchor` / `*alias` pair --
// and refuses the rest. Other top-level sections are skipped: they belong to the
// gate #98 ports, and none of them can add or remove an operation row.
func parseCoverageRows(path, body string) ([]coverageRow, error) {
	lines := strings.Split(body, "\n")
	anchors := map[string]string{}
	var rows []coverageRow
	var cur *coverageRow
	var curKeys map[string]int // key -> line, for the row being read
	sections := 0
	inOps := false

	flush := func() {
		if cur != nil {
			rows = append(rows, *cur)
			cur = nil
		}
	}

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		lineNo := i + 1
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := indentOf(line)
		if indent < 0 {
			return nil, fmt.Errorf("%s:%d: tab in indentation", path, lineNo)
		}
		if indent == 0 {
			flush()
			inOps = trimmed == "operations:"
			if inOps {
				sections++
			}
			continue
		}
		if !inOps {
			continue
		}

		var key, raw string
		if m := coverageItemStart.FindStringSubmatch(line); m != nil {
			flush()
			cur = &coverageRow{Line: lineNo}
			curKeys = map[string]int{}
			key, raw = m[1], m[2]
		} else if m := coverageItemKey.FindStringSubmatch(line); m != nil && cur != nil {
			key, raw = m[1], m[2]
		} else {
			return nil, fmt.Errorf("%s:%d: a line this reader was not written for, under operations: %q", path, lineNo, line)
		}
		if !coverageRowKeys[key] {
			return nil, fmt.Errorf("%s:%d: unknown row key %q", path, lineNo, key)
		}
		// A key given twice in one row is an error, as it is to yaml.v3. Letting
		// the second win would read "sdk: NoSuch.Method" + "sdk: TagService.List"
		// as a covered row that the gate #98 ports refuses to load at all.
		if first, dup := curKeys[key]; dup {
			return nil, fmt.Errorf("%s:%d: %q is given twice in one row (first at line %d)", path, lineNo, key, first)
		}
		curKeys[key] = lineNo

		value, consumed, err := coverageValue(path, lineNo, strings.TrimSpace(raw), lines[i+1:], anchors)
		if err != nil {
			return nil, err
		}
		i += consumed

		switch key {
		case "path":
			cur.Path = value
		case "method":
			cur.Method = value
		case "sdk":
			cur.SDK = value
		case "unimplemented":
			cur.Unimplemented = value
		}
	}
	flush()

	switch {
	case sections != 1:
		return nil, fmt.Errorf("%s: found %d top-level operations: sections, want exactly 1", path, sections)
	case len(rows) == 0:
		return nil, fmt.Errorf("%s: no operation rows -- an empty inventory would make every endpoint look declared", path)
	}
	return rows, nil
}

// coverageValue resolves one value, returning it and how many following lines a
// folded block consumed.
func coverageValue(path string, lineNo int, raw string, rest []string, anchors map[string]string) (string, int, error) {
	anchor := ""
	if m := coverageAnchor.FindStringSubmatch(raw); m != nil {
		anchor, raw = m[1], strings.TrimSpace(m[2])
		if _, dup := anchors[anchor]; dup {
			return "", 0, fmt.Errorf("%s:%d: anchor &%s is defined twice", path, lineNo, anchor)
		}
	}

	var value string
	consumed := 0
	switch {
	case raw == ">-":
		// Every line of the block at ONE indentation, the first line's. YAML
		// keeps a more-indented line verbatim rather than folding it, and a
		// less-indented one ends the block -- neither of which this join
		// reproduces, so both are refused rather than read differently.
		var parts []string
		blockIndent := -1
		for _, next := range rest {
			if strings.TrimSpace(next) == "" || indentOf(next) <= 4 {
				break
			}
			if blockIndent < 0 {
				blockIndent = indentOf(next)
			}
			if indentOf(next) != blockIndent {
				return "", 0, fmt.Errorf("%s:%d: a folded block whose lines are not at one indentation", path, lineNo+1+consumed)
			}
			parts = append(parts, strings.TrimSpace(next))
			consumed++
		}
		if len(parts) == 0 {
			return "", 0, fmt.Errorf("%s:%d: a folded block with no text under it", path, lineNo)
		}
		value = strings.Join(parts, " ")
	case strings.HasPrefix(raw, "*"):
		m := coverageAlias.FindStringSubmatch(raw)
		if m == nil {
			return "", 0, fmt.Errorf("%s:%d: unreadable alias %q", path, lineNo, raw)
		}
		if anchor != "" {
			return "", 0, fmt.Errorf("%s:%d: an anchor on an alias", path, lineNo)
		}
		v, ok := anchors[m[1]]
		if !ok {
			return "", 0, fmt.Errorf("%s:%d: alias *%s names no anchor defined above it", path, lineNo, m[1])
		}
		value = v
	case strings.HasPrefix(raw, "'"):
		if len(raw) < 2 || !strings.HasSuffix(raw, "'") {
			return "", 0, fmt.Errorf("%s:%d: a single-quoted value that does not close on its line", path, lineNo)
		}
		value = strings.ReplaceAll(raw[1:len(raw)-1], "''", "'")
	case raw == "" || strings.ContainsAny(raw[:1], "#\"|>&!%@`{}[],?:-") ||
		strings.Contains(raw, " #") || strings.Contains(raw, "\t#") || strings.Contains(raw, ": "):
		// An empty value is null to YAML, and so is one that starts with '#':
		// "unimplemented: # not ported yet" is a key followed by a COMMENT, and
		// reading that comment as the reason would turn a silent row into a
		// covered one. The other leading characters are YAML indicators whose
		// meaning this reader does not implement.
		return "", 0, fmt.Errorf("%s:%d: a value form this reader was not written for: %q", path, lineNo, raw)
	default:
		value = raw
	}

	if anchor != "" {
		anchors[anchor] = value
	}
	return value, consumed, nil
}

// packageMethods returns every "Receiver.Method" declared in this package's
// non-test source, pointer receivers and value receivers alike.
func packageMethods(t *testing.T) map[string]bool {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	fset := token.NewFileSet()
	out := map[string]bool{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 {
				continue
			}
			recv := fn.Recv.List[0].Type
			if star, ok := recv.(*ast.StarExpr); ok {
				recv = star.X
			}
			if ident, ok := recv.(*ast.Ident); ok {
				out[ident.Name+"."+fn.Name.Name] = true
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("found no methods in this package; the sdk: check would pass vacuously")
	}
	return out
}

// checkContractBaseline returns every way the specs, the marker and the rows
// disagree. Pure, so its own tests can feed it fixtures.
func checkContractBaseline(marker string, specs []*vendoredSpec, rows []coverageRow, methods map[string]bool) []string {
	var problems []string

	for _, s := range specs {
		if s.Version != marker {
			problems = append(problems, fmt.Sprintf("%s: info.version is %s, but the contract-version marker "+
				"in docs/versioning.md is %s -- a refresh that moved one without the other", s.Path, s.Version, marker))
		}
	}

	// Surface parity: a version-independent row can only be true of both surfaces
	// if both publish the operation.
	published := map[string]bool{}
	for _, s := range specs {
		for op := range s.Ops {
			published[op] = true
		}
	}
	for _, s := range specs {
		for _, op := range sortedKeys(published) {
			if !s.Ops[op] {
				problems = append(problems, fmt.Sprintf("%s does not publish %s, which another vendored "+
					"spec does; one version-independent row cannot describe both surfaces", s.Path, op))
			}
		}
	}

	seen := map[string]int{}
	for _, r := range rows {
		where := fmt.Sprintf("contract-coverage.yaml:%d", r.Line)
		switch {
		case r.Path == "" || r.Method == "":
			problems = append(problems, where+": a row is missing path or method")
			continue
		case !httpMethods[r.Method]:
			problems = append(problems, fmt.Sprintf("%s: %q is not a lowercase HTTP method", where, r.Method))
		case !strings.HasPrefix(r.Path, "/") || strings.HasPrefix(r.Path, "/api/"):
			problems = append(problems, fmt.Sprintf("%s: %s must be the version-independent suffix -- "+
				"/tags, never /api/v1/tags", where, r.Path))
		}
		if first, dup := seen[r.key()]; dup {
			problems = append(problems, fmt.Sprintf("%s: %s is listed twice (first at line %d)", where, r.key(), first))
		}
		seen[r.key()] = r.Line

		switch {
		case r.SDK == "" && strings.TrimSpace(r.Unimplemented) == "":
			problems = append(problems, fmt.Sprintf("%s: %s names neither an sdk method nor an unimplemented "+
				"reason -- a silence, which is the one thing a row may not be", where, r.key()))
		case r.SDK != "" && r.Unimplemented != "":
			problems = append(problems, fmt.Sprintf("%s: %s cannot be both implemented and unimplemented", where, r.key()))
		case r.SDK != "" && !methods[r.SDK]:
			problems = append(problems, fmt.Sprintf("%s: %s names %s, which is not a method declared in this "+
				"package (Receiver.Method)", where, r.key(), r.SDK))
		case r.SDK == "" && len(strings.TrimSpace(r.Unimplemented)) < 30:
			problems = append(problems, fmt.Sprintf("%s: %s has no usable unimplemented reason; a reason that "+
				"says nothing is a silence with a key in front of it", where, r.key()))
		}
		if !published[r.key()] {
			problems = append(problems, fmt.Sprintf("%s: %s is not an operation either vendored spec publishes "+
				"-- a stale row, or a typo that leaves the real operation uncovered", where, r.key()))
		}
	}
	for _, op := range sortedKeys(published) {
		if _, ok := seen[op]; !ok {
			problems = append(problems, fmt.Sprintf("%s is published by the vendored contracts and has no row "+
				"in docs/contract-coverage.yaml: add one naming its method, or the reason it has none", op))
		}
	}
	return problems
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func readVendoredSpecs(t *testing.T) []*vendoredSpec {
	t.Helper()
	var specs []*vendoredSpec
	for _, s := range specSurfaces {
		raw, err := ioutil.ReadFile(filepath.FromSlash(s.Path))
		if err != nil {
			t.Fatalf("read %s: %v", s.Path, err)
		}
		spec, err := parseVendoredSpec(s.Path, string(raw), s.Version)
		if err != nil {
			t.Fatal(err)
		}
		specs = append(specs, spec)
	}
	return specs
}

func readCoverageRows(t *testing.T) []coverageRow {
	t.Helper()
	const path = "docs/contract-coverage.yaml"
	raw, err := ioutil.ReadFile(filepath.FromSlash(path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	rows, err := parseCoverageRows(path, string(raw))
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// The vendored contracts, the marker and the coverage rows agree.
//
// This is #90's acceptance, held rather than ticked: both specs at the marker,
// every published operation with a row, and every row naming a real method or
// giving a reason.
func TestVendoredContractsMatchTheMarkerAndTheCoverageRows(t *testing.T) {
	marker := readContractVersionMarker(t)
	problems := checkContractBaseline(marker, readVendoredSpecs(t), readCoverageRows(t), packageMethods(t))
	for _, p := range problems {
		t.Error(p)
	}
}

// --- the baseline check's own fixtures -----------------------------------------
//
// On a correct tree the check above passes whether or not it works. These prove
// each refusal can fire.

const fixtureSpec = `openapi: 3.0.3
info:
  title: Octonomy API
  version: 9.9.9
  description: Multi-tenant tag management and taxonomy service.
paths:
  /api/v1/tags:
    get:
      operationId: api_v1_tags_list
      description: |-
        A block scalar whose content looks like structure:
        post:
          /api/v1/phantom:
      summary: a plain scalar that wraps
        get: onto a continuation line
    post:
      operationId: api_v1_tags_create
  /api/v1/tags/{tag_id}:
    parameters:
    - in: path
      name: tag_id
    delete:
      operationId: api_v1_tags_destroy
  /health/live:
    get:
      operationId: health_live
components:
  schemas:
    Tag:
      type: object
`

// Content deeper than four spaces is never structure the reader needs. YAML
// requires a block scalar's lines, and a plain scalar's continuation, to sit
// deeper than the key that owns them, so none of them can be a path (depth 2) or
// a path-item key (depth 4). The fixture puts key-shaped text there on purpose:
// "post:", "/api/v1/phantom:" and "get:" must not become operations.
func TestContractBaselineParsesTheOperationsAndNothingElse(t *testing.T) {
	spec, err := parseVendoredSpec("fixture.yaml", fixtureSpec, "v1")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if spec.Version != "9.9.9" {
		t.Errorf("info.version: got %q", spec.Version)
	}
	want := []string{"delete /tags/{tag_id}", "get /health/live", "get /tags", "post /tags"}
	if got := sortedKeys(spec.Ops); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("operations:\n  got  %v\n  want %v", got, want)
	}
}

func TestContractBaselineRefusesASpecLayoutItWasNotWrittenFor(t *testing.T) {
	cases := []struct {
		name, from, to, want string
	}{
		// Every edit is anchored on the newline before it: the fixture repeats
		// key-shaped text deeper down on purpose, and an unanchored replacement
		// lands there and tests nothing.
		{"a quoted path key", "\n  /api/v1/tags:\n", "\n  '/api/v1/tags':\n", "not a plain path"},
		{"an unknown path-item key", "\n    post:\n", "\n    x-internal:\n", "neither an HTTP method"},
		{"a path under the wrong surface", "\n  /api/v1/tags:\n", "\n  /api/v2/tags:\n", "this file is the v1 contract"},
		{"a second info.version", "\n  title: Octonomy API\n", "\n  title: Octonomy API\n  version: 1.2.3\n", "want exactly 1"},
		{"no info.version", "\n  version: 9.9.9\n", "\n", "want exactly 1"},
		{"a duplicated operation", "\n    post:\n", "\n    get:\n", "published twice"},
		{"an odd depth under paths", "\n    post:\n", "\n   post:\n", "only 2 and 4 are keys"},
		{"a stray sequence item", "\n      operationId: api_v1_tags_create\n",
			"\n      operationId: api_v1_tags_create\n    - in: query\n", "belongs to no path-item field"},
		{"a path item that is a reference", "\n  /health/live:\n    get:\n",
			"\n  /health/live:\n    $ref: '#/paths/~1api~1v1~1tags'\n", "neither an HTTP method"},
		{"no operations at all", "paths:\n", "paths: {}\nignored:\n", "no operations"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			body := strings.Replace(fixtureSpec, tc.from, tc.to, 1)
			if body == fixtureSpec {
				t.Fatalf("fixture edit %q matched nothing; this case tests nothing", tc.from)
			}
			if _, err := parseVendoredSpec("fixture.yaml", body, "v1"); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want an error containing %q, got %v", tc.want, err)
			}
		})
	}
}

const fixtureCoverage = `# header comment
operations:
  - path: /tags
    method: get
    sdk: TagService.List
    documented_response: array
    actual_response: list-envelope
  - path: /tags
    method: post
    unimplemented: &reason >-
      A reason long enough to say something,
      folded across two lines.
    documented_response: ref:Tag
    actual_response: data-envelope
  - path: /health/live
    method: get
    unimplemented: *reason
    composite_body: '{"it''s": 1}'
    documented_response: none
    actual_response: bare

unsent_inputs:
  - path: /nowhere
    method: get
`

func TestContractBaselineReadsTheCoverageRows(t *testing.T) {
	rows, err := parseCoverageRows("fixture.yaml", fixtureCoverage)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("want 3 rows (other sections are not rows), got %d: %+v", len(rows), rows)
	}
	if rows[0].SDK != "TagService.List" || rows[0].key() != "get /tags" {
		t.Errorf("row 1: %+v", rows[0])
	}
	const folded = "A reason long enough to say something, folded across two lines."
	if rows[1].Unimplemented != folded {
		t.Errorf("folded block: got %q", rows[1].Unimplemented)
	}
	if rows[2].Unimplemented != folded {
		t.Errorf("an alias resolves to its anchor's text: got %q", rows[2].Unimplemented)
	}
}

func TestContractBaselineRefusesACoverageLayoutItWasNotWrittenFor(t *testing.T) {
	cases := []struct {
		name, from, to, want string
	}{
		{"a typo'd key", "    sdk: TagService.List\n", "    skd: TagService.List\n", "unknown row key"},
		{"an alias with no anchor", "*reason", "*nosuch", "names no anchor"},
		{"a double-quoted value", "    sdk: TagService.List\n", "    sdk: \"TagService.List\"\n", "value form"},
		{"a literal block", "&reason >-", "&reason |", "value form"},
		{"a flow mapping", "    sdk: TagService.List\n", "    sdk: {a: b}\n", "value form"},
		{"a trailing comment", "    sdk: TagService.List\n", "    sdk: TagService.List # x\n", "value form"},
		{"a plain value with a colon", "    sdk: TagService.List\n", "    sdk: TagService: List\n", "value form"},
		{"a key given twice", "    sdk: TagService.List\n", "    sdk: NoSuch.Method\n    sdk: TagService.List\n", "given twice in one row"},
		{"a reason that is a comment", "    unimplemented: *reason\n",
			"    unimplemented: # a comment YAML reads as null, long enough to pass as a reason\n", "value form"},
		{"an empty value", "    sdk: TagService.List\n", "    sdk:\n", "value form"},
		{"a folded block that changes indentation", "      folded across two lines.\n",
			"        folded across two lines.\n", "not at one indentation"},
		{"an empty folded block", "      A reason long enough to say something,\n      folded across two lines.\n", "", "no text under it"},
		{"a mis-indented key", "    method: get\n    sdk:", "     method: get\n    sdk:", "was not written for"},
		{"no operations section", "operations:\n", "operation:\n", "want exactly 1"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			body := strings.Replace(fixtureCoverage, tc.from, tc.to, 1)
			if body == fixtureCoverage {
				t.Fatalf("fixture edit %q matched nothing; this case tests nothing", tc.from)
			}
			if _, err := parseCoverageRows("fixture.yaml", body); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want an error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestContractBaselineReportsEveryDisagreement(t *testing.T) {
	spec := func(path, version string, ops ...string) *vendoredSpec {
		s := &vendoredSpec{Path: path, Version: version, Ops: map[string]bool{}}
		for _, op := range ops {
			s.Ops[op] = true
		}
		return s
	}
	both := func(ops ...string) []*vendoredSpec {
		return []*vendoredSpec{spec("v1.yaml", "9.9.9", ops...), spec("v2.yaml", "9.9.9", ops...)}
	}
	const reason = "A reason long enough to say something about the gap."
	methods := map[string]bool{"TagService.List": true}
	good := []coverageRow{
		{Line: 1, Path: "/tags", Method: "get", SDK: "TagService.List"},
		{Line: 2, Path: "/tags", Method: "post", Unimplemented: reason},
	}
	if got := checkContractBaseline("9.9.9", both("get /tags", "post /tags"), good, methods); len(got) != 0 {
		t.Fatalf("a consistent baseline must pass, got %v", got)
	}

	cases := []struct {
		name   string
		marker string
		specs  []*vendoredSpec
		rows   []coverageRow
		want   string
	}{
		{"a spec behind the marker", "9.9.9",
			[]*vendoredSpec{spec("v1.yaml", "1.0.0", "get /tags", "post /tags"), spec("v2.yaml", "9.9.9", "get /tags", "post /tags")},
			good, "info.version is 1.0.0"},
		{"an operation with no row", "9.9.9", both("get /tags", "post /tags", "get /audit-logs"), good,
			"get /audit-logs is published by the vendored contracts and has no row"},
		{"a stale row", "9.9.9", both("get /tags"), good, "post /tags is not an operation either vendored spec publishes"},
		{"a surface that lacks an operation", "9.9.9",
			[]*vendoredSpec{spec("v1.yaml", "9.9.9", "get /tags"), spec("v2.yaml", "9.9.9", "get /tags", "post /tags")},
			good, "v1.yaml does not publish post /tags"},
		{"a silent row", "9.9.9", both("get /tags", "post /tags"),
			[]coverageRow{good[0], {Line: 2, Path: "/tags", Method: "post"}}, "a silence"},
		{"a reason that says nothing", "9.9.9", both("get /tags", "post /tags"),
			[]coverageRow{good[0], {Line: 2, Path: "/tags", Method: "post", Unimplemented: "later"}}, "no usable unimplemented reason"},
		{"both sdk and a reason", "9.9.9", both("get /tags", "post /tags"),
			[]coverageRow{good[0], {Line: 2, Path: "/tags", Method: "post", SDK: "TagService.List", Unimplemented: reason}},
			"cannot be both"},
		{"a method that does not exist", "9.9.9", both("get /tags", "post /tags"),
			[]coverageRow{good[0], {Line: 2, Path: "/tags", Method: "post", SDK: "TagService.Creat"}}, "not a method declared"},
		{"a duplicated row", "9.9.9", both("get /tags", "post /tags"),
			append(append([]coverageRow{}, good...), coverageRow{Line: 3, Path: "/tags", Method: "get", SDK: "TagService.List"}),
			"listed twice"},
		{"a versioned path", "9.9.9", both("get /tags", "post /tags"),
			[]coverageRow{{Line: 1, Path: "/api/v1/tags", Method: "get", SDK: "TagService.List"}, good[1]},
			"version-independent suffix"},
		{"an uppercase method", "9.9.9", both("get /tags", "post /tags"),
			[]coverageRow{{Line: 1, Path: "/tags", Method: "GET", SDK: "TagService.List"}, good[1]},
			"not a lowercase HTTP method"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := checkContractBaseline(tc.marker, tc.specs, tc.rows, methods)
			if !strings.Contains(strings.Join(got, "\n"), tc.want) {
				t.Errorf("want a problem containing %q, got %v", tc.want, got)
			}
		})
	}
}

// packageMethods must see the methods the real rows name, on both receiver
// spellings, or every sdk: row would fail -- or, with a laxer check, pass for a
// method nobody declared.
func TestPackageMethodsSeesTheServiceMethods(t *testing.T) {
	methods := packageMethods(t)
	// ListOptions.apply has a VALUE receiver, APIError.Error a pointer one.
	for _, m := range []string{"TagService.List", "VocabularyService.Delete", "APIError.Error", "ListOptions.apply"} {
		if !methods[m] {
			t.Errorf("packageMethods does not see %s", m)
		}
	}
	if methods["TagService.Resolve"] {
		t.Error("packageMethods reports a method this tree does not declare")
	}
}
