package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	octonomy "github.com/octoverse-id/octonomy-go/v2"
	"gopkg.in/yaml.v3"
)

// These tests are the gate's own proof. A drift gate that has never been shown to
// fail is indistinguishable from a drift gate that cannot fail, and this one runs
// weekly against a repository that is usually in step -- so a regexp that stops
// matching, or a comparison that silently drops half the contract, would show up
// as a permanently green job rather than as a broken one. Each test below feeds a
// deliberately modified copy of the real contracts and asserts the finding.
//
// The fixtures are mutations of the vendored specs rather than hand-written
// miniatures on purpose: a miniature proves the checker works on the shape its
// author imagined, and this gate exists because the contract stopped being the
// shape its author imagined.

// --- fixtures ------------------------------------------------------------------

// syntheticErrorsPy stands in for the server's octonomy/core/errors.py. It carries
// the real 3.1.1 code set, in the two shapes that file writes them: a `code`
// attribute per DomainError subclass, and bare literals in the DRF handler.
const syntheticErrorsPy = `
class DomainError(Exception):
    code = "validation_error"

class ConflictError(DomainError):
    code = "conflict"

class TenantMismatchError(DomainError):
    code = "tenant_mismatch"

class ApplicationMismatchError(DomainError):
    code = "application_mismatch"

class InactiveTagError(DomainError):
    code = "inactive_tag"

class NamespaceNotSupportedError(DomainError):
    code = "namespace_not_supported"

class NamespaceHeaderError(DomainError):
    code = "namespace_invalid"

class NamespacedWritesDisabledError(DomainError):
    code = "namespaced_writes_disabled"

class NamespaceApiDisabledError(DomainError):
    code = "namespace_api_disabled"

class AmbiguousResolutionError(DomainError):
    code = "ambiguous_resolution"

class ScopeImmutableError(ConflictError):
    code = "scope_immutable"

def error_response(code: str, message: str, details: Any, request, http_status: int) -> Response:
    return Response({"error": {"code": code}}, status=http_status)

def exception_handler(exc, context):
    if isinstance(exc, DomainError):
        return error_response(exc.code, exc.message, exc.details, request, exc.status_code)
    if isinstance(exc, Http404):
        return error_response("not_found", "Resource not found.", {}, request, 404)
    code = "validation_error"
    if isinstance(exc, exceptions.NotAuthenticated):
        code = "authentication_required"
    elif isinstance(exc, exceptions.PermissionDenied):
        code = "forbidden"
    return error_response(code, message, response.data, request, response.status_code)
`

// repoRoot is this tool's view of the SDK it checks.
const repoRoot = "../.."

// stageRepo copies the inputs a local run reads into a temp directory, so a test
// can modify one of them without touching the working tree.
func stageRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	copied := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		copyFile(t, filepath.Join(repoRoot, name), filepath.Join(dir, name))
		copied++
	}
	if copied == 0 {
		t.Fatalf("staged no Go sources from %s", repoRoot)
	}
	for _, name := range []string{"openapi.yaml", "openapi-v2.yaml", "contract-coverage.yaml", "versioning.md"} {
		copyFile(t, filepath.Join(repoRoot, "docs", name), filepath.Join(dir, "docs", name))
	}
	return dir
}

// stageUpstream writes a fetched-upstream directory whose contents match the
// vendored ones exactly -- the no-drift baseline every upstream test mutates.
func stageUpstream(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	copyFile(t, filepath.Join(repoRoot, "docs", "openapi.yaml"), filepath.Join(dir, "openapi.yaml"))
	copyFile(t, filepath.Join(repoRoot, "docs", "openapi-v2.yaml"), filepath.Join(dir, "openapi-v2.yaml"))
	write(t, filepath.Join(dir, "errors.py"), syntheticErrorsPy)
	return dir
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	raw, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// edit replaces old with new in the file, after the first occurrence of anchor.
// The anchor is what keeps a mutation aimed at one operation from landing on the
// half-dozen others that share the same generated block.
func edit(t *testing.T, path, anchor, old, replacement string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	at := strings.Index(src, anchor)
	if at < 0 {
		t.Fatalf("%s: anchor %q not found -- the fixture no longer matches the contract", path, anchor)
	}
	rest := strings.Replace(src[at:], old, replacement, 1)
	if rest == src[at:] {
		t.Fatalf("%s: %q not found after %q -- the fixture no longer matches the contract", path, old, anchor)
	}
	write(t, path, src[:at]+rest)
}

// --- helpers -------------------------------------------------------------------

// runLocal runs the offline checks over a staged repository.
func runLocal(t *testing.T, repo string) *Report {
	t.Helper()
	return CheckLocal(load(t, repo, ""))
}

// runFull runs every check, offline and cross-repository.
func runFull(t *testing.T, repo, upstream string) *Report {
	t.Helper()
	in := load(t, repo, upstream)
	report := CheckLocal(in)
	report.Sections = append(report.Sections, CheckUpstream(in).Sections...)
	return report
}

func load(t *testing.T, repo, upstream string) Inputs {
	t.Helper()
	in := Inputs{Vendored: map[string]*Spec{}}
	var err error
	if in.Vendored["v1"], err = LoadSpec(filepath.Join(repo, "docs", "openapi.yaml")); err != nil {
		t.Fatal(err)
	}
	if in.Vendored["v2"], err = LoadSpec(filepath.Join(repo, "docs", "openapi-v2.yaml")); err != nil {
		t.Fatal(err)
	}
	if in.Coverage, err = LoadCoverage(filepath.Join(repo, "docs", "contract-coverage.yaml")); err != nil {
		t.Fatal(err)
	}
	if in.SDK, err = LoadSDKPackage(repo); err != nil {
		t.Fatal(err)
	}
	in.SDKCodes = in.SDK.ErrorCodes()
	if in.RecordedVersion, err = RecordedContractVersion(filepath.Join(repo, "docs", "versioning.md")); err != nil {
		t.Fatal(err)
	}
	// Conformance drives the SDK COMPILED INTO THIS BINARY -- the real one --
	// against response bodies built from the STAGED spec. That split is what the
	// tests below exploit: a mutated schema changes what the stub sends, and the
	// unmutated client either copes with it or does not.
	if in.Conformance, err = RunConformance(in.Vendored["v2"], in.Coverage, Drivers()); err != nil {
		t.Fatal(err)
	}
	if upstream != "" {
		in.Upstream = map[string]*Spec{}
		if in.Upstream["v1"], err = LoadSpec(filepath.Join(upstream, "openapi.yaml")); err != nil {
			t.Fatal(err)
		}
		if in.Upstream["v2"], err = LoadSpec(filepath.Join(upstream, "openapi-v2.yaml")); err != nil {
			t.Fatal(err)
		}
		if in.ServerCodes, in.UnreadableCodes, err = ServerErrorCodes(filepath.Join(upstream, "errors.py")); err != nil {
			t.Fatal(err)
		}
	}
	return in
}

// findings flattens a report for assertion.
func findings(r *Report) []string {
	var all []string
	for _, section := range r.Sections {
		for _, item := range section.Items {
			all = append(all, section.Title+": "+item)
		}
	}
	return all
}

func assertClean(t *testing.T, r *Report) {
	t.Helper()
	if r.Count() != 0 {
		t.Fatalf("expected no findings, got %d:\n%s", r.Count(), strings.Join(findings(r), "\n"))
	}
}

func assertFinding(t *testing.T, r *Report, substrings ...string) {
	t.Helper()
	all := findings(r)
	for _, want := range substrings {
		found := false
		for _, item := range all {
			if strings.Contains(item, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no finding contains %q; got:\n%s", want, strings.Join(all, "\n"))
		}
	}
}

// --- the working tree itself ----------------------------------------------------

// TestWorkingTreeIsClean is the check CI runs on every pull request, asserted here
// too so a failure names the specific inconsistency rather than an exit code.
func TestWorkingTreeIsClean(t *testing.T) {
	assertClean(t, runLocal(t, repoRoot))
}

// TestKnownEnvelopeDivergenceIsNotFlagged is the acceptance criterion that the
// gate must not nag. The specs document every list response as a bare array while
// the server returns {data, pagination}; the same holds for single resources, the
// bulk composites, and the health probes. All four are recorded in
// docs/contract-coverage.yaml and none of them may produce a finding.
func TestKnownEnvelopeDivergenceIsNotFlagged(t *testing.T) {
	in := load(t, repoRoot, "")

	divergent := 0
	for _, row := range in.Coverage.Operations {
		if row.DocumentedResponse == "array" && row.ActualResponse == "list-envelope" {
			divergent++
		}
	}
	if divergent == 0 {
		t.Fatal("the inventory records no list-envelope divergence -- this test is asserting nothing")
	}

	report := CheckLocal(in)
	for _, item := range findings(report) {
		if strings.Contains(item, "list-envelope") {
			t.Errorf("the recorded divergence was reported as drift: %s", item)
		}
	}
	assertClean(t, report)
}

// TestUpstreamInStepIsClean is the other half: an upstream copy identical to the
// vendored one produces nothing, so every finding below is caused by its mutation
// and not by a comparison that reports noise.
func TestUpstreamInStepIsClean(t *testing.T) {
	assertClean(t, runFull(t, repoRoot, stageUpstream(t)))
}

// --- cross-repository drift -----------------------------------------------------

// TestDetectsAddedQueryParameter is the epic's own drift, reproduced: the server
// added `scope` to /tag-resolution and nothing said so. A path inventory reports
// this as green.
func TestDetectsAddedQueryParameter(t *testing.T) {
	upstream := stageUpstream(t)
	edit(t, filepath.Join(upstream, "openapi-v2.yaml"),
		"operationId: api_v2_tag_resolution_retrieve",
		"      parameters:\n",
		"      parameters:\n      - in: query\n        name: scope_hint\n        schema:\n          type: string\n")

	assertFinding(t, runFull(t, repoRoot, upstream),
		"gained parameter `query scope_hint`",
		"get /api/v2/tag-resolution")
}

// TestDetectsChangedQueryParameter covers the quieter half: a parameter that keeps
// its name and changes what it means.
func TestDetectsChangedQueryParameter(t *testing.T) {
	upstream := stageUpstream(t)
	edit(t, filepath.Join(upstream, "openapi-v2.yaml"),
		"operationId: api_v2_tags_list",
		"      - in: query\n        name: is_active\n        schema:\n          type: boolean\n",
		"      - in: query\n        name: is_active\n        required: true\n        schema:\n          type: string\n")

	assertFinding(t, runFull(t, repoRoot, upstream),
		"parameter `query is_active`: + required: true",
		"parameter `query is_active`: ~ schema.type: boolean -> string")
}

// TestDetectsNewErrorCode is the second acceptance criterion. The spec cannot
// answer this one at all: ErrorResponse types `code` as a bare string, so a new
// code is invisible to every schema comparison.
func TestDetectsNewErrorCode(t *testing.T) {
	upstream := stageUpstream(t)
	write(t, filepath.Join(upstream, "errors.py"), syntheticErrorsPy+`

class VocabularyLockedError(ConflictError):
    code = "vocabulary_locked"
`)

	assertFinding(t, runFull(t, repoRoot, upstream),
		"the server can return `vocabulary_locked`, which the vendored registry")
}

// TestDetectsWithdrawnErrorCode covers the other direction: a constant this SDK
// still exports for a code the server no longer has.
func TestDetectsWithdrawnErrorCode(t *testing.T) {
	upstream := stageUpstream(t)
	write(t, filepath.Join(upstream, "errors.py"),
		strings.Replace(syntheticErrorsPy, `code = "scope_immutable"`, `code = "scope_frozen"`, 1))

	assertFinding(t, runFull(t, repoRoot, upstream),
		"the vendored registry lists `scope_immutable` and the server no longer raises it")
}

// TestSDKOnlyCodesAreNotFlagged guards the allowlist that keeps the offline check
// from firing every run on the two codes the server never sends. It is the
// OFFLINE check that judges them: errors.go is compared with the vendored
// registry, and only that registry is compared with the server's.
func TestSDKOnlyCodesAreNotFlagged(t *testing.T) {
	in := load(t, repoRoot, "")
	if len(in.Coverage.SDKOnlyErrorCodes) == 0 {
		t.Fatal("no sdk_only_error_codes recorded -- this test is asserting nothing")
	}
	for _, item := range findings(CheckLocal(in)) {
		if strings.Contains(item, "unexpected_status") || strings.Contains(item, "not_ready") {
			t.Errorf("an allowlisted SDK-only code was reported: %s", item)
		}
	}
}

// TestUnlistedErrorCodeConstantFails is the offline half doing its job: a new
// Code* constant that is in neither the vendored registry nor the SDK-only list
// has no recorded provenance, and a code with no provenance is how a constant for
// something the server never sends gets exported forever.
func TestUnlistedErrorCodeConstantFails(t *testing.T) {
	repo := stageRepo(t)
	edit(t, filepath.Join(repo, "errors.go"),
		"const (",
		"const (\n",
		"const (\n\tCodeVocabularyLocked = \"vocabulary_locked\"\n")

	assertFinding(t, runLocal(t, repo),
		"`CodeVocabularyLocked` (vocabulary_locked) is in neither the vendored error registry nor sdk_only_error_codes")
}

// TestVendoredErrorCodeWithoutConstantFails is the other direction, and the one
// that closes "refresh the registry, implement it later". Before the registry was
// vendored this was only reachable weekly, with the network.
func TestVendoredErrorCodeWithoutConstantFails(t *testing.T) {
	repo := stageRepo(t)
	edit(t, filepath.Join(repo, "docs", "contract-coverage.yaml"),
		"\nserver_error_codes:\n",
		"\nserver_error_codes:\n",
		"\nserver_error_codes:\n  - vocabulary_locked\n")

	assertFinding(t, runLocal(t, repo),
		"`vocabulary_locked` is in the vendored error registry and errors.go declares no constant for it")
}

// TestDetectsAddedSchemaField covers "added or changed fields on models the SDK
// decodes" -- the class of change that grows a response the client silently drops.
func TestDetectsAddedSchemaField(t *testing.T) {
	upstream := stageUpstream(t)
	edit(t, filepath.Join(upstream, "openapi-v2.yaml"),
		"\n    Tag:\n",
		"      properties:\n",
		"      properties:\n        colour:\n          type: string\n          maxLength: 32\n")

	assertFinding(t, runFull(t, repoRoot, upstream),
		"schema `Tag`: + properties.colour.type: string")
}

// TestDetectsChangedRequiredSet catches a field becoming required, which a
// property-by-property comparison alone would miss.
func TestDetectsChangedRequiredSet(t *testing.T) {
	upstream := stageUpstream(t)
	edit(t, filepath.Join(upstream, "openapi-v2.yaml"),
		"\n    Tag:\n",
		"      required:\n",
		"      required:\n      - namespace_type\n")

	assertFinding(t, runFull(t, repoRoot, upstream), "schema `Tag`: ~ required:")
}

// TestDetectsNewOperationUpstream is the paths check, and the one an inventory on
// its own would have covered.
func TestDetectsNewOperationUpstream(t *testing.T) {
	upstream := stageUpstream(t)
	edit(t, filepath.Join(upstream, "openapi-v2.yaml"), "paths:\n", "paths:\n", "paths:\n"+syntheticPath)

	assertFinding(t, runFull(t, repoRoot, upstream), "`get /api/v2/tag-suggestions` is new upstream")
}

// TestDetectsContractVersionBump is the cheapest signal and the one that says
// "read the rest of this report".
func TestDetectsContractVersionBump(t *testing.T) {
	upstream := stageUpstream(t)
	edit(t, filepath.Join(upstream, "openapi-v2.yaml"), "info:", "version: ", "version: 9.9.9 # ")

	assertFinding(t, runFull(t, repoRoot, upstream), "server publishes contract 9.9.9, this SDK vendors")
}

// --- the offline half ------------------------------------------------------------

const syntheticPath = `  /api/v2/tag-suggestions:
    get:
      operationId: api_v2_tag_suggestions_list
      tags:
      - api
      responses:
        '200':
          content:
            application/json:
              schema:
                type: array
                items:
                  $ref: '#/components/schemas/Tag'
          description: ''
`

// TestUnlistedOperationFails is what makes "not implemented" a decision: an
// operation the vendored contract publishes and the inventory does not list fails
// the offline gate, whether or not anyone means to implement it.
func TestUnlistedOperationFails(t *testing.T) {
	repo := stageRepo(t)
	edit(t, filepath.Join(repo, "docs", "openapi-v2.yaml"), "paths:\n", "paths:\n", "paths:\n"+syntheticPath)

	assertFinding(t, runLocal(t, repo),
		"`get /tag-suggestions` (v2",
		"implement it or record why not")
}

// TestRecordedUnimplementedOperationPasses is the allowlist working: the same
// operation, with a written reason, is silent.
func TestRecordedUnimplementedOperationPasses(t *testing.T) {
	repo := stageRepo(t)
	edit(t, filepath.Join(repo, "docs", "openapi-v2.yaml"), "paths:\n", "paths:\n", "paths:\n"+syntheticPath)
	edit(t, filepath.Join(repo, "docs", "openapi.yaml"), "paths:\n", "paths:\n",
		"paths:\n"+strings.ReplaceAll(syntheticPath, "/api/v2/", "/api/v1/"))
	// Inserted at the head of `operations:` rather than appended to the file, whose
	// last key is a different list entirely.
	edit(t, filepath.Join(repo, "docs", "contract-coverage.yaml"),
		"\noperations:\n", "\noperations:\n",
		"\noperations:\n"+`  - path: /tag-suggestions
    method: get
    unimplemented: >-
      Deliberately out of scope while the server's ranking is unstable.
    documented_response: array
    actual_response: list-envelope
`)

	in := load(t, repo, "")
	if _, ok := in.Coverage.ByKey()["get /tag-suggestions"]; !ok {
		t.Fatal("the fixture row did not parse -- the append landed outside operations:")
	}
	assertClean(t, CheckLocal(in))
}

// TestSurfaceAsymmetryFails guards the assumption one inventory row rests on:
// that both contracts publish the same operations, so one row can name one Go
// method for both. An operation on v2 alone breaks that, and the gate says so
// rather than leaving the row quietly half-true.
func TestSurfaceAsymmetryFails(t *testing.T) {
	repo := stageRepo(t)
	edit(t, filepath.Join(repo, "docs", "openapi-v2.yaml"), "paths:\n", "paths:\n", "paths:\n"+syntheticPath)

	assertFinding(t, runLocal(t, repo), "`get /tag-suggestions` is published on v2 only")
}

// TestUnsentQueryParameterFails is the check the upstream comparison cannot make.
// A refreshed contract that nobody implemented passes an upstream-versus-vendored
// diff by construction: both sides are then the same file.
func TestUnsentQueryParameterFails(t *testing.T) {
	repo := stageRepo(t)
	edit(t, filepath.Join(repo, "docs", "openapi-v2.yaml"),
		"operationId: api_v2_tag_resolution_retrieve",
		"      parameters:\n",
		"      parameters:\n      - in: query\n        name: scope_hint\n        schema:\n          type: string\n")

	assertFinding(t, runLocal(t, repo),
		"`get /tag-resolution` documents the query parameter `scope_hint` and the client did not send it")
}

// TestAllowlistedUnsentQueryParameterPasses is that allowlist working.
func TestAllowlistedUnsentQueryParameterPasses(t *testing.T) {
	repo := stageRepo(t)
	edit(t, filepath.Join(repo, "docs", "openapi-v2.yaml"),
		"operationId: api_v2_tag_resolution_retrieve",
		"      parameters:\n",
		"      parameters:\n      - in: query\n        name: scope_hint\n        schema:\n          type: string\n")
	edit(t, filepath.Join(repo, "docs", "contract-coverage.yaml"),
		"\nunsent_inputs:\n",
		"\nunsent_inputs:\n",
		"\nunsent_inputs:\n"+`  - path: /tag-resolution
    method: get
    in: query
    name: scope_hint
    reason: server-side ranking hint, not a client concern
`)

	assertClean(t, runLocal(t, repo))
}

// TestStaleUnsentAllowlistFails keeps the allowlist from outliving its parameter.
func TestStaleUnsentAllowlistFails(t *testing.T) {
	repo := stageRepo(t)
	edit(t, filepath.Join(repo, "docs", "contract-coverage.yaml"),
		"\nunsent_inputs:\n",
		"\nunsent_inputs:\n",
		"\nunsent_inputs:\n"+`  - path: /tag-resolution
    method: get
    in: query
    name: nothing_documents_this
    reason: left behind by an earlier refresh
`)

	assertFinding(t, runLocal(t, repo),
		"`get /tag-resolution query nothing_documents_this` is listed under unsent_inputs",
		"drop the row")
}

// TestChangedDocumentedResponseFails is the day the divergence ends. The spec
// starts documenting the envelope, the recorded shape no longer matches, and the
// gate says so instead of staying quiet about a workaround nobody needs any more.
func TestChangedDocumentedResponseFails(t *testing.T) {
	repo := stageRepo(t)
	edit(t, filepath.Join(repo, "docs", "openapi-v2.yaml"),
		"operationId: api_v2_tags_list",
		"              schema:\n                type: array\n                items:\n                  $ref: '#/components/schemas/Tag'\n",
		"              schema:\n                $ref: '#/components/schemas/TagPage'\n")

	assertFinding(t, runLocal(t, repo),
		"documents a `ref:TagPage` success body, recorded as `array`")
}

// TestRenamedMethodFailsInventory keeps a row from asserting something false about
// the code it names.
func TestRenamedMethodFailsInventory(t *testing.T) {
	repo := stageRepo(t)
	edit(t, filepath.Join(repo, "tags.go"),
		"func (s *TagService) List(",
		"func (s *TagService) List(",
		"func (s *TagService) ListEverything(")

	assertFinding(t, runLocal(t, repo),
		"claims `TagService.List`, which this package does not declare")
}

// TestRecordedVersionMismatchFails covers the prose in docs/versioning.md, which
// nothing else checks.
func TestRecordedVersionMismatchFails(t *testing.T) {
	repo := stageRepo(t)
	edit(t, filepath.Join(repo, "docs", "versioning.md"),
		"<!-- contract-version:",
		"<!-- contract-version: 3.1.1 -->",
		"<!-- contract-version: 2.0.0 -->")

	assertFinding(t, runLocal(t, repo), "docs/versioning.md records server 2.0.0")
}

// --- extractors ------------------------------------------------------------------

// TestExtractorFloors is the guard against the quietest failure this tool has: a
// regexp that stops matching reports "no codes" rather than "cannot read", and
// "no codes" compares clean in one direction and floods in the other.
func TestExtractorFloors(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "errors.py"), "class Nothing:\n    pass\n")
	if _, _, err := ServerErrorCodes(filepath.Join(dir, "errors.py")); err == nil {
		t.Error("a file with no error codes was accepted")
	}
	write(t, filepath.Join(dir, "versioning.md"), "# no marker here\n")
	if _, err := RecordedContractVersion(filepath.Join(dir, "versioning.md")); err == nil {
		t.Error("a document with no contract-version marker was accepted")
	}
}

// TestTypedErrorCodeConstantsAreRead covers the spelling that broke the regexp
// this replaced: `CodeFoo ErrorCode = "foo"` is the same declaration to a reader
// as `CodeFoo = "foo"`, and must be the same to the gate. Constants are the one
// thing still read out of the source, because a declaration is not control flow.
func TestTypedErrorCodeConstantsAreRead(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "errors.go"), `package octonomy

type ErrorCode = string

const (
	CodeUntyped         = "untyped"
	CodeTyped ErrorCode = "typed"
)

var CodeVar ErrorCode = "var"
`)
	sdk, err := LoadSDKPackage(dir)
	if err != nil {
		t.Fatal(err)
	}
	codes := sdk.ErrorCodes()
	for value, want := range map[string]string{"untyped": "CodeUntyped", "typed": "CodeTyped", "var": "CodeVar"} {
		if codes[value] != want {
			t.Errorf("%q resolved to %q, want %q", value, codes[value], want)
		}
	}
}

// TestOperationDecodeFailsLoudly pins the bug this tool shipped with in its first
// draft: a path item that would not decode was swallowed, and every POST, PATCH,
// and body-carrying DELETE lost its parameters and responses while the run stayed
// green.
func TestOperationDecodeFailsLoudly(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "broken.yaml"), `openapi: 3.0.3
info:
  version: 3.1.1
paths:
  /api/v2/tags:
    get:
      operationId: api_v2_tags_list
      parameters: "not a list"
      responses:
        '200':
          description: ''
`)
	if _, err := LoadSpec(filepath.Join(dir, "broken.yaml")); err == nil {
		t.Fatal("an undecodable operation was accepted")
	}
}

// TestWriteOperationsCarryTheirContract is the regression test for the same bug,
// from the other side: the real contract's write operations must come back with
// parameters and responses, not as empty shells.
func TestWriteOperationsCarryTheirContract(t *testing.T) {
	spec, err := LoadSpec(filepath.Join(repoRoot, "docs", "openapi-v2.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"post /api/v2/tags", "patch /api/v2/tags/{tag_id}", "post /api/v2/tag-assignments/bulk-assign"} {
		op, ok := spec.Operations[key]
		if !ok {
			t.Fatalf("%s is missing from the contract", key)
		}
		if op.ID == "" || len(op.Responses) == 0 || len(op.RequestBody) == 0 {
			t.Errorf("%s decoded as an empty shell: id=%q responses=%d body=%d",
				key, op.ID, len(op.Responses), len(op.RequestBody))
		}
	}
	// The body-carrying DELETE is checked without a request body on purpose: the
	// SDK sends one on /tag-assignments (the ids travel in it) and the generated
	// spec documents none, which is a fourth divergence in the same family as the
	// envelopes -- recorded here rather than asserted away.
	if op := spec.Operations["delete /api/v2/tag-assignments"]; op == nil || op.ID == "" || len(op.Responses) == 0 {
		t.Error("delete /api/v2/tag-assignments decoded as an empty shell")
	}
}

// --- what the client really does ------------------------------------------------
//
// These replaced a set of tests that mutated Go source and asserted what a static
// analyzer made of it. The analyzer is gone (see conformance.go), and so is that
// shape of test: the client under test is the one compiled into this binary, so a
// mutated source file would not be the code being driven. What IS mutable is the
// contract the stub answers from -- which is the more useful half anyway, because
// it is the half that moves.

// TestRoutesAreObservedOnTheWire is the inventory's route claim, proven by the
// request the client actually issued rather than by reading its body.
func TestRoutesAreObservedOnTheWire(t *testing.T) {
	in := load(t, repoRoot, "")
	if len(in.Conformance.Errors) != 0 {
		t.Fatalf("drivers that never reached the wire: %v", in.Conformance.Errors)
	}
	for _, row := range in.Coverage.Operations {
		if row.Unimplemented != "" {
			continue
		}
		observed, ok := in.Conformance.Observations[row.Key()]
		if !ok {
			t.Errorf("%s: no observation", row.Key())
			continue
		}
		if observed.Key() != row.Key() {
			t.Errorf("%s: the client requested %q", row.Key(), observed.Key())
		}
	}
}

// TestQueryParametersAreObservedPerOperation is the reason this is driven rather
// than read. `limit` and `offset` are sent by every list route; /tag-resolution
// does not page, and an analysis that asked whether the NAME appeared anywhere in
// the package called it implemented there too.
func TestQueryParametersAreObservedPerOperation(t *testing.T) {
	in := load(t, repoRoot, "")
	resolution, ok := in.Conformance.Observations["get /tag-resolution"]
	if !ok {
		t.Fatal("no observation for get /tag-resolution")
	}
	for _, name := range []string{"slug", "type", "scope", "application_id", "include_global"} {
		if !resolution.Query[name] {
			t.Errorf("TagService.Resolve did not send %q", name)
		}
	}
	for _, name := range []string{"limit", "offset"} {
		if resolution.Query[name] {
			t.Errorf("TagService.Resolve sent %q; it does not page", name)
		}
	}

	list, ok := in.Conformance.Observations["get /tags"]
	if !ok {
		t.Fatal("no observation for get /tags")
	}
	for _, name := range []string{"limit", "offset", "q", "slug", "vocabulary_id"} {
		if !list.Query[name] {
			t.Errorf("TagService.List did not send %q", name)
		}
	}
}

// TestUnsentQueryParameterIsReported is the first review's BLOCKER, tested where
// it lives: a client that stops putting a documented parameter on the wire.
//
// The observation is synthesized rather than produced by breaking the SDK, because
// the SDK under test is compiled in. What matters is that the check reads the
// observation and not the source -- so an operation that stops passing its query
// builder, branches past it, or drops it in a refactor all arrive here identically.
func TestUnsentQueryParameterIsReported(t *testing.T) {
	in := load(t, repoRoot, "")
	observed := in.Conformance.Observations["get /tags"]
	delete(observed.Query, "vocabulary_id")
	delete(observed.Query, "parent_id")
	in.Conformance.Observations["get /tags"] = observed

	assertFinding(t, CheckLocal(in),
		"`get /tags` documents the query parameter `vocabulary_id` and the client did not send it",
		"`get /tags` documents the query parameter `parent_id` and the client did not send it")
}

// TestUndocumentedQueryParameterIsReported is the other direction, which the first
// version of this check did not have at all: a parameter the client sends that
// nothing documents is a filter the server ignores in silence.
func TestUndocumentedQueryParameterIsReported(t *testing.T) {
	in := load(t, repoRoot, "")
	observed := in.Conformance.Observations["get /tags"]
	observed.Query["colour"] = true
	in.Conformance.Observations["get /tags"] = observed

	assertFinding(t, CheckLocal(in),
		"`get /tags`: the client sends the query parameter `colour`, which no vendored contract documents")
}

// TestWrongRouteIsReported: the row names a method, the method requests something
// else. Under the analyzer this needed a mutated source file and a derivation that
// could read it; here it is the request line.
func TestWrongRouteIsReported(t *testing.T) {
	in := load(t, repoRoot, "")
	observed := in.Conformance.Observations["get /tags/{tag_id}"]
	observed.Path = "/vocabularies/{}"
	in.Conformance.Observations["get /tags/{tag_id}"] = observed

	assertFinding(t, CheckLocal(in), "`TagService.Get` requests `GET /vocabularies/{}`")
}

// TestAddedSchemaPropertyIsReported is the first review's repro, and it now runs
// end to end: the contract grows a property, the stub sends it, and the Go model
// drops it on the way back out.
func TestAddedSchemaPropertyIsReported(t *testing.T) {
	repo := stageRepo(t)
	for _, spec := range []string{"openapi-v2.yaml", "openapi.yaml"} {
		edit(t, filepath.Join(repo, "docs", spec), "\n    Tag:\n",
			"      properties:\n",
			"      properties:\n        colour:\n          type: string\n")
	}

	assertFinding(t, runLocal(t, repo),
		"schema `Tag` documents `colour` and it does not survive decoding by `TagService.List`")
}

// TestRetypedSchemaPropertyIsReported is the second review's repro, and the one a
// name-only comparison could never catch: the property keeps its name and changes
// its type, so the model still has a field and the body no longer fits it.
func TestRetypedSchemaPropertyIsReported(t *testing.T) {
	repo := stageRepo(t)
	for _, spec := range []string{"openapi-v2.yaml", "openapi.yaml"} {
		edit(t, filepath.Join(repo, "docs", spec), "\n    Tag:\n",
			"        usage_count:\n          type: integer\n",
			"        usage_count:\n          type: string\n")
	}

	assertFinding(t, runLocal(t, repo),
		"could not handle a response built from the vendored schema")
}

// TestWithdrawnSchemaPropertyIsReported is the other direction: the SDK keeps
// decoding something the contract no longer documents.
func TestWithdrawnSchemaPropertyIsReported(t *testing.T) {
	repo := stageRepo(t)
	for _, spec := range []string{"openapi-v2.yaml", "openapi.yaml"} {
		edit(t, filepath.Join(repo, "docs", spec), "\n    Tag:\n",
			"        usage_count:\n", "        usage_count_renamed:\n")
	}

	assertFinding(t, runLocal(t, repo),
		"decodes `usage_count` into its model, which schema `Tag` does not document")
}

// TestWrongRecordedEnvelopeIsReported: the recorded envelope is what the stub
// wraps its body in, so a row that names the wrong one hands the client a shape it
// cannot decode. The envelope is not a label anyone can get away with mistyping.
func TestWrongRecordedEnvelopeIsReported(t *testing.T) {
	repo := stageRepo(t)
	edit(t, filepath.Join(repo, "docs", "contract-coverage.yaml"),
		"  - path: /tags\n    method: get\n",
		"    actual_response: list-envelope\n",
		"    actual_response: data-envelope\n")

	assertFinding(t, runLocal(t, repo),
		"`TagService.List` could not handle a response built from the vendored schema")
}

// TestResponseModelComparisonIsNotVacuous guards the check above from quietly
// comparing nothing: an empty property set on either side would make every model
// "match".
func TestResponseModelComparisonIsNotVacuous(t *testing.T) {
	in := load(t, repoRoot, "")
	spec := in.Vendored["v2"]
	ops, err := strippedOperations(spec, "v2")
	if err != nil {
		t.Fatal(err)
	}

	compared := 0
	for _, row := range in.Coverage.Operations {
		if row.ActualResponse != "list-envelope" && row.ActualResponse != "data-envelope" {
			continue
		}
		op := ops[row.Key()]
		if op == nil || op.OKModel == "" {
			continue
		}
		documented, _ := spec.SchemaProperties(op.OKModel)
		observed := in.Conformance.Observations[row.Key()]
		if len(documented) == 0 || len(observed.Decoded) == 0 {
			t.Errorf("%s: documented=%d decoded=%d -- nothing is being compared",
				row.Key(), len(documented), len(observed.Decoded))
			continue
		}
		compared += len(documented)
	}
	if compared < 100 {
		t.Errorf("only %d properties take part in the model comparison; it is close to vacuous", compared)
	}
}

// TestEveryOperationHasADriver: an operation nobody calls is an operation this
// gate says nothing about, which is the same silence the inventory exists to end.
func TestEveryOperationHasADriver(t *testing.T) {
	in := load(t, repoRoot, "")
	drivers := map[string]bool{}
	for _, d := range Drivers() {
		drivers[d.Op] = true
	}
	for _, row := range in.Coverage.Operations {
		if row.Unimplemented != "" {
			continue
		}
		if !drivers[row.Key()] {
			t.Errorf("%s is implemented and drivers.go does not call it", row.Key())
		}
	}
}

// --- spec-reading corners the review found ---------------------------------------

// TestPathItemParametersAreMerged covers a parameter form OpenAPI allows and this
// server does not currently emit: declared once on the path item, inherited by
// every operation under it. Skipping it silently would have made an entire class
// of added parameters invisible.
func TestPathItemParametersAreMerged(t *testing.T) {
	upstream := stageUpstream(t)
	edit(t, filepath.Join(upstream, "openapi-v2.yaml"),
		"  /api/v2/tag-resolution:\n",
		"  /api/v2/tag-resolution:\n",
		"  /api/v2/tag-resolution:\n    parameters:\n    - in: query\n      name: shared_hint\n      schema:\n        type: string\n")

	assertFinding(t, runFull(t, repoRoot, upstream),
		"gained parameter `query shared_hint`")
}

// TestComponentParameterRefsResolve covers the other form: a parameter written as
// a reference, which carries no inline name.
func TestComponentParameterRefsResolve(t *testing.T) {
	upstream := stageUpstream(t)
	edit(t, filepath.Join(upstream, "openapi-v2.yaml"),
		"operationId: api_v2_tag_resolution_retrieve",
		"      parameters:\n",
		"      parameters:\n      - $ref: '#/components/parameters/SharedHint'\n")
	edit(t, filepath.Join(upstream, "openapi-v2.yaml"),
		"components:\n",
		"components:\n",
		"components:\n  parameters:\n    SharedHint:\n      in: query\n      name: shared_hint\n      schema:\n        type: string\n")

	assertFinding(t, runFull(t, repoRoot, upstream),
		"gained parameter `query shared_hint`")
}

// TestUnresolvableParameterRefFailsLoudly is the same form pointed at nothing.
// A parameter the gate cannot identify must stop the run, not vanish from it.
func TestUnresolvableParameterRefFailsLoudly(t *testing.T) {
	upstream := stageUpstream(t)
	edit(t, filepath.Join(upstream, "openapi-v2.yaml"),
		"operationId: api_v2_tag_resolution_retrieve",
		"      parameters:\n",
		"      parameters:\n      - $ref: '#/components/parameters/NotThere'\n")

	if _, err := LoadSpec(filepath.Join(upstream, "openapi-v2.yaml")); err == nil {
		t.Fatal("a parameter reference that resolves to nothing was accepted")
	}
}

// TestParameterIdentityIncludesIn: OpenAPI identifies a parameter by name AND
// location, so a header may share a query parameter's name without being it.
func TestParameterIdentityIncludesIn(t *testing.T) {
	upstream := stageUpstream(t)
	edit(t, filepath.Join(upstream, "openapi-v2.yaml"),
		"operationId: api_v2_tags_list",
		"      parameters:\n",
		"      parameters:\n      - in: header\n        name: slug\n        schema:\n          type: string\n")

	report := runFull(t, repoRoot, upstream)
	assertFinding(t, report, "gained parameter `header slug`")
	for _, item := range findings(report) {
		if strings.Contains(item, "lost parameter `query slug`") {
			t.Errorf("the header displaced the query parameter of the same name: %s", item)
		}
	}
}

// TestReorderedMappingSequenceIsNotDrift keeps a generator's incidental ordering
// out of the report. A scheduled job that goes red for a reshuffle is a job people
// learn to close unread.
//
// It tests `flatten` directly, and the previous version of it did not: it moved a
// parameter within an operation's `parameters` list, and parameters are keyed by
// `in` + name long before flatten sees them -- so it passed with the normalization
// removed. `oneOf` and `allOf` members are the sequences that really do reach it.
func TestReorderedMappingSequenceIsNotDrift(t *testing.T) {
	parse := func(src string) map[string]string {
		t.Helper()
		var node yaml.Node
		if err := yaml.Unmarshal([]byte(src), &node); err != nil {
			t.Fatal(err)
		}
		return flatten(&node)
	}

	first := parse(`
oneOf:
- type: string
  maxLength: 10
- type: integer
  minimum: 1
`)
	reordered := parse(`
oneOf:
- type: integer
  minimum: 1
- type: string
  maxLength: 10
`)
	if lines := diffFlat(first, reordered); len(lines) != 0 {
		t.Errorf("reordering two oneOf members reported drift:\n%s", strings.Join(lines, "\n"))
	}

	// And a real change to one of those members still reports, so the
	// normalization above is not hiding content.
	changed := parse(`
oneOf:
- type: string
  maxLength: 20
- type: integer
  minimum: 1
`)
	if lines := diffFlat(first, changed); len(lines) == 0 {
		t.Error("changing a oneOf member reported nothing")
	}
}

// TestInlineClassBodyErrorCodeIsRead covers the Python shape that was invisible to
// both the value extractor and the unreadable-form detector while they were
// anchored to the start of a line: a one-line class body. A real code vanished
// from the comparison while the count floor stayed healthy -- the quietest failure
// this extractor has.
func TestInlineClassBodyErrorCodeIsRead(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "errors.py"), syntheticErrorsPy+`

class InlineError(DomainError): code = "inline_code"
class InlineEnumError(DomainError): code = ErrorCodes.LOCKED
`)
	codes, unreadable, err := ServerErrorCodes(filepath.Join(dir, "errors.py"))
	if err != nil {
		t.Fatal(err)
	}
	if !codes["inline_code"] {
		t.Error("a one-line class body's literal code was not read")
	}
	found := false
	for _, line := range unreadable {
		if strings.Contains(line, "ErrorCodes.LOCKED") {
			found = true
		}
	}
	if !found {
		t.Errorf("a one-line class body's unreadable code was not reported: %v", unreadable)
	}
}

// TestUnreadableServerErrorCodeIsReported is the answer to "the extractor only
// understands one spelling". It still only understands one -- but it now says so
// when the registry uses another, instead of comparing against a set that is
// quietly one code short while the count floor stays healthy.
func TestUnreadableServerErrorCodeIsReported(t *testing.T) {
	upstream := stageUpstream(t)
	write(t, filepath.Join(upstream, "errors.py"), syntheticErrorsPy+`

class VocabularyLockedError(ConflictError):
    code = ErrorCodes.VOCABULARY_LOCKED
`)

	assertFinding(t, runFull(t, repoRoot, upstream),
		"produces a code in a form this gate cannot read",
		"ErrorCodes.VOCABULARY_LOCKED")
}

// TestAnnotatedServerErrorCodeIsRead covers the spelling that IS understood but
// that the first pattern missed: `code: str = "..."`.
func TestAnnotatedServerErrorCodeIsRead(t *testing.T) {
	upstream := stageUpstream(t)
	write(t, filepath.Join(upstream, "errors.py"), syntheticErrorsPy+`

class VocabularyLockedError(ConflictError):
    code: str = "vocabulary_locked"
`)

	assertFinding(t, runFull(t, repoRoot, upstream),
		"the server can return `vocabulary_locked`, which the vendored registry")
}

// TestNonListParametersFailLoudly: a `parameters` key that is not a list would
// otherwise yield an operation with no parameters at all, and report every one of
// them as removed.
func TestNonListParametersFailLoudly(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "broken.yaml"), `openapi: 3.0.3
info:
  version: 3.1.1
paths:
  /api/v2/tags:
    get:
      operationId: api_v2_tags_list
      parameters: "not a list"
      responses:
        '200':
          description: ''
`)
	if _, err := LoadSpec(filepath.Join(dir, "broken.yaml")); err == nil {
		t.Fatal("an operation whose parameters are not a list was accepted")
	}
}

// TestOrdinaryErrorRegistryShapesAreNotReportedAsUnreadable is the nag guard on
// the unreadable-form report. Run against the server's real errors.py it first
// produced two findings a reader could do nothing with: the definition line of
// `error_response` itself, and `error_response(exc.code, ...)`, whose code the
// class-attribute pattern had already read. A scheduled job that reports those
// every week is one people stop opening.
func TestOrdinaryErrorRegistryShapesAreNotReportedAsUnreadable(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "errors.py"), syntheticErrorsPy)

	codes, unreadable, err := ServerErrorCodes(filepath.Join(dir, "errors.py"))
	if err != nil {
		t.Fatal(err)
	}
	if len(unreadable) != 0 {
		t.Errorf("ordinary registry shapes were reported as unreadable: %v", unreadable)
	}
	// And the shapes it does read are all there -- including the one the DRF
	// handler assigns to a local before passing it on.
	for _, code := range []string{"validation_error", "not_found", "authentication_required", "forbidden", "scope_immutable"} {
		if !codes[code] {
			t.Errorf("%q was not extracted from the registry", code)
		}
	}
}

// TestInventoryFileMismatchFails: the row names the file as well as the method,
// and a row that is wrong about where the code lives is a row nobody can follow.
func TestInventoryFileMismatchFails(t *testing.T) {
	repo := stageRepo(t)
	edit(t, filepath.Join(repo, "docs", "contract-coverage.yaml"),
		"    sdk: TagService.Resolve\n",
		"    file: resolution.go\n",
		"    file: tags.go\n")

	assertFinding(t, runLocal(t, repo),
		"says `TagService.Resolve` lives in tags.go; it is declared in resolution.go")
}

// TestCoverageValidationRefusesRowsThatWouldDoNothing. Every rule here exists
// because the row it rejects would otherwise load, match nothing, and leave an
// endpoint or an allowlist entry silently unchecked -- a gate reporting green over
// something nobody reviewed.
func TestCoverageValidationRefusesRowsThatWouldDoNothing(t *testing.T) {
	valid := `
operations:
  - path: /tags
    method: get
    sdk: TagService.List
    file: tags.go
    documented_response: array
    actual_response: list-envelope
server_error_codes: [a, b, c, d, e, f, g, h, i, j]
`
	for _, tc := range []struct{ name, yaml, want string }{
		{"prefixed path", strings.Replace(valid, "path: /tags", "path: /api/v2/tags", 1), "must not carry the /api/<version> prefix"},
		{"unknown method", strings.Replace(valid, "method: get", "method: GET", 1), "is not a lowercase HTTP method"},
		{"bare sdk name", strings.Replace(valid, "sdk: TagService.List", "sdk: List", 1), "must name the method as Receiver.Method"},
		{"neither implemented nor not", strings.Replace(valid, "    sdk: TagService.List\n    file: tags.go\n", "", 1), "needs either sdk+file or an unimplemented reason"},
		{"unknown envelope", strings.Replace(valid, "actual_response: list-envelope", "actual_response: list_envelope", 1), "is not one of"},
		{"unknown documented shape", strings.Replace(valid, "documented_response: array", "documented_response: list", 1), "is not `array`, `none`, `other`, or `ref:<Schema>`"},
		{"unknown key", valid + "unexpected_key: 1\n", "field unexpected_key not found"},
		{"allowlist for a missing operation", valid + "unsent_inputs:\n  - path: /nowhere\n    method: get\n    in: query\n    name: q\n    reason: x\n", "is not an operation in this file"},
		{"allowlist with an unknown location", valid + "unsent_inputs:\n  - path: /tags\n    method: get\n    in: cookie\n    name: q\n    reason: x\n", "not query, body, or header"},
		{"code in both lists", valid + "sdk_only_error_codes:\n  - code: a\n    reason: x\n", "cannot be both the server's and the SDK's alone"},
		{"registry too small", strings.Replace(valid, "server_error_codes: [a, b, c, d, e, f, g, h, i, j]", "server_error_codes: [a]", 1), "an empty one checks nothing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "contract-coverage.yaml")
			write(t, path, tc.yaml)
			_, err := LoadCoverage(path)
			if err == nil {
				t.Fatalf("%s was accepted", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// TestMultiRequestDriverIsRejected: only the last request is recorded, so a
// driver that issued several would hand the gate an observation describing one of
// them and have it read as describing the operation. A driver exercises one
// operation, once.
func TestMultiRequestDriverIsRejected(t *testing.T) {
	spec, err := LoadSpec(filepath.Join(repoRoot, "docs", "openapi-v2.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cov, err := LoadCoverage(filepath.Join(repoRoot, "docs", "contract-coverage.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	conf, err := RunConformance(spec, cov, []Driver{{
		Op:  "get /tags",
		SDK: "TagService.List",
		Call: func(ctx context.Context, env *Env) (any, error) {
			if _, err := env.Client.Tags.List(ctx, nil); err != nil {
				return nil, err
			}
			return env.Client.Tags.List(ctx, nil)
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := conf.Observations["get /tags"]; ok {
		t.Error("a driver that issued two requests was recorded as an observation")
	}
	if err := conf.Errors["get /tags"]; err == nil || !strings.Contains(err.Error(), "issued 2 requests") {
		t.Errorf("expected a two-request error, got %v", err)
	}
}

// TestDriverThatNeverReachesTheWireIsReported covers the other end: a call the
// client refuses before sending anything -- an option the transport rejects, say.
// Silence there would read as an operation with no parameters at all.
func TestDriverThatNeverReachesTheWireIsReported(t *testing.T) {
	spec, err := LoadSpec(filepath.Join(repoRoot, "docs", "openapi-v2.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cov, err := LoadCoverage(filepath.Join(repoRoot, "docs", "contract-coverage.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	conf, err := RunConformance(spec, cov, []Driver{{
		Op:  "post /tags",
		SDK: "TagService.Create",
		Call: func(ctx context.Context, env *Env) (any, error) {
			// WithApplication on a request that carries a body: refused by the
			// transport, so nothing is ever sent.
			return env.Client.Tags.Create(ctx, octonomy.TagCreate{Name: "n", Slug: "s", Type: "t"},
				octonomy.WithApplication("app"))
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := conf.Errors["post /tags"]; err == nil || !strings.Contains(err.Error(), "reached no request") {
		t.Errorf("expected a no-request error, got %v", err)
	}
}

// TestNullableWithNonNullableModelIsReported is the third review's repro, and the
// one a single favorable witness could never catch: the contract starts permitting
// null for a property whose Go field is a value type. The populated body still
// decodes perfectly. `encoding/json` accepts null into an `int` without an error
// too -- so neither a decode success nor a decode failure says anything here, and
// the round-tripped VALUE is what separates a *string from an int.
func TestNullableWithNonNullableModelIsReported(t *testing.T) {
	repo := stageRepo(t)
	for _, spec := range []string{"openapi-v2.yaml", "openapi.yaml"} {
		edit(t, filepath.Join(repo, "docs", spec), "\n    Tag:\n",
			"        usage_count:\n          type: integer\n",
			"        usage_count:\n          type: integer\n          nullable: true\n")
	}

	assertFinding(t, runLocal(t, repo),
		"schema `Tag` marks `usage_count` nullable and `TagService.List` decodes null as `0`")
}

// TestNullableModelsRoundTripNull is the non-vacuity guard on the witness above:
// the contract really does mark properties nullable, and the models really are
// asked to hold that state.
func TestNullableModelsRoundTripNull(t *testing.T) {
	in := load(t, repoRoot, "")
	spec := in.Vendored["v2"]

	checked := 0
	for _, model := range []string{"Tag", "Vocabulary", "TagAlias", "Assignment", "AuditLog"} {
		nullable := nullableProperties(spec, model)
		if len(nullable) == 0 {
			t.Errorf("schema %s marks nothing nullable; the null witness is asserting nothing for it", model)
		}
		checked += len(nullable)
	}
	if checked < 10 {
		t.Errorf("only %d nullable properties take part in the null witness", checked)
	}
}

// TestMissingRequestBodyPropertyIsReported is the third review's BLOCKER: a write
// that stops sending part of its payload emits the same verb, the same path and
// the same query as one that still does, so every other check stayed satisfied.
func TestMissingRequestBodyPropertyIsReported(t *testing.T) {
	in := load(t, repoRoot, "")
	observed := in.Conformance.Observations["post /tags"]
	delete(observed.Body, "slug")
	delete(observed.Body, "metadata")
	in.Conformance.Observations["post /tags"] = observed

	assertFinding(t, CheckLocal(in),
		"schema `TagWrite` documents `slug` and the client did not send it",
		"schema `TagWrite` documents `metadata` and the client did not send it")
}

// TestEmptyRequestBodyIsReported is the same hole at its widest: a write that
// sends no payload at all.
func TestEmptyRequestBodyIsReported(t *testing.T) {
	in := load(t, repoRoot, "")
	observed := in.Conformance.Observations["post /tags"]
	observed.Body = map[string]bool{}
	in.Conformance.Observations["post /tags"] = observed

	assertFinding(t, CheckLocal(in),
		"the contract documents a `TagWrite` request body and the client sent none")
}

// TestUndocumentedRequestBodyPropertyIsReported is the other direction: a
// property the server was never told to expect.
func TestUndocumentedRequestBodyPropertyIsReported(t *testing.T) {
	in := load(t, repoRoot, "")
	observed := in.Conformance.Observations["post /tags"]
	observed.Body["colour"] = true
	in.Conformance.Observations["post /tags"] = observed

	assertFinding(t, CheckLocal(in),
		"the client sends `colour` in its request body, which schema `TagWrite` does not document")
}

// TestMissingDocumentedHeaderIsReported covers the namespace axis, which lives
// entirely in headers: a method that stopped propagating X-Namespace-* looked
// exactly like one that never could, because nothing recorded headers at all.
func TestMissingDocumentedHeaderIsReported(t *testing.T) {
	in := load(t, repoRoot, "")
	observed := in.Conformance.Observations["get /tags"]
	delete(observed.Headers, "X-Namespace-Type")
	in.Conformance.Observations["get /tags"] = observed

	assertFinding(t, CheckLocal(in),
		"`get /tags` documents the header `X-Namespace-Type` and the client did not send it")
}

// TestUndocumentedHeaderIsReported: the always-sent transport headers are
// recorded once under client_headers, and anything else the client starts sending
// is a finding.
func TestUndocumentedHeaderIsReported(t *testing.T) {
	in := load(t, repoRoot, "")
	observed := in.Conformance.Observations["get /tags"]
	observed.Headers["X-Invented-Header"] = true
	in.Conformance.Observations["get /tags"] = observed

	assertFinding(t, CheckLocal(in),
		"the client sends the header `X-Invented-Header`, which no vendored contract documents")
}

// TestBodyCarriedApplicationIDIsVerified keeps the eleven write exceptions from
// being an unchecked allowlist. Each says application_id moves from the query
// string into the payload; if the payload stops carrying it, the row is asserting
// something false and the missing query key stops being excused.
func TestBodyCarriedApplicationIDIsVerified(t *testing.T) {
	in := load(t, repoRoot, "")
	observed := in.Conformance.Observations["post /tags"]
	delete(observed.Body, "application_id")
	in.Conformance.Observations["post /tags"] = observed

	assertFinding(t, CheckLocal(in),
		"`post /tags` records that application_id travels in the body, and the request body does not carry it")
}

// TestSwappedPathArgumentsAreReported: both sentinels used to collapse to `{}`, so
// a driver passing resource_type where resource_id belongs produced exactly the
// expected route. Each placeholder has its own value now.
func TestSwappedPathArgumentsAreReported(t *testing.T) {
	spec, err := LoadSpec(filepath.Join(repoRoot, "docs", "openapi-v2.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cov, err := LoadCoverage(filepath.Join(repoRoot, "docs", "contract-coverage.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	conf, err := RunConformance(spec, cov, []Driver{{
		Op:  "get /resources/{resource_type}/{resource_id}/tags",
		SDK: "ResourceService.ListTags",
		Call: func(ctx context.Context, env *Env) (any, error) {
			// The two arguments, the wrong way round.
			return env.Client.Resources.ListTags(ctx, env.Path("resource_id"), env.Path("resource_type"), nil)
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	observed := conf.Observations["get /resources/{resource_type}/{resource_id}/tags"]
	if observed.Path == "/resources/{resource_type}/{resource_id}/tags" {
		t.Error("swapping the two path arguments produced the expected route")
	}
}

// TestValueDependentRouteIsReported: one execution says what the client did with
// one input. Two inputs do not prove a route is invariant, but they catch a route
// that varies with the value -- which one cannot.
func TestValueDependentRouteIsReported(t *testing.T) {
	spec, err := LoadSpec(filepath.Join(repoRoot, "docs", "openapi-v2.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cov, err := LoadCoverage(filepath.Join(repoRoot, "docs", "contract-coverage.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	conf, err := RunConformance(spec, cov, []Driver{{
		Op:  "get /tags/{tag_id}",
		SDK: "TagService.Get",
		Call: func(ctx context.Context, env *Env) (any, error) {
			id := env.Path("tag_id")
			if id == "TAGID1" {
				return env.Client.Tags.Get(ctx, id)
			}
			return env.Client.Vocabularies.Get(ctx, id)
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := conf.Errors["get /tags/{tag_id}"]; err == nil || !strings.Contains(err.Error(), "route depends on the values passed") {
		t.Errorf("a value-dependent route was accepted: %v", err)
	}
}
