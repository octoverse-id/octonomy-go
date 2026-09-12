package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
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
	if in.Conformance, err = RunConformance(in.Vendored, in.Coverage, Drivers()); err != nil {
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

// loadSpecs reads both vendored surfaces, which is what the conformance runner
// drives -- one client per surface, each against its own contract.
func loadSpecs(t *testing.T) map[string]*Spec {
	t.Helper()
	out := map[string]*Spec{}
	var err error
	if out["v1"], err = LoadSpec(filepath.Join(repoRoot, "docs", "openapi.yaml")); err != nil {
		t.Fatal(err)
	}
	if out["v2"], err = LoadSpec(filepath.Join(repoRoot, "docs", "openapi-v2.yaml")); err != nil {
		t.Fatal(err)
	}
	return out
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
		observed, ok := in.Conformance.Observations["v2 "+row.Key()]
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
	resolution, ok := in.Conformance.Observations["v2 get /tag-resolution"]
	if !ok {
		t.Fatal("no observation for get /tag-resolution")
	}
	for _, name := range []string{"slug", "type", "scope", "application_id", "include_global"} {
		if _, sent := resolution.Query[name]; !sent {
			t.Errorf("TagService.Resolve did not send %q", name)
		}
	}
	for _, name := range []string{"limit", "offset"} {
		if _, sent := resolution.Query[name]; sent {
			t.Errorf("TagService.Resolve sent %q; it does not page", name)
		}
	}

	list, ok := in.Conformance.Observations["v2 get /tags"]
	if !ok {
		t.Fatal("no observation for get /tags")
	}
	for _, name := range []string{"limit", "offset", "q", "slug", "vocabulary_id"} {
		if _, sent := list.Query[name]; !sent {
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
	observed := in.Conformance.Observations["v2 get /tags"]
	delete(observed.Query, "vocabulary_id")
	delete(observed.Query, "parent_id")
	in.Conformance.Observations["v2 get /tags"] = observed

	assertFinding(t, CheckLocal(in),
		"`get /tags` documents the query parameter `vocabulary_id` and the client did not send it",
		"`get /tags` documents the query parameter `parent_id` and the client did not send it")
}

// TestUndocumentedQueryParameterIsReported is the other direction, which the first
// version of this check did not have at all: a parameter the client sends that
// nothing documents is a filter the server ignores in silence.
func TestUndocumentedQueryParameterIsReported(t *testing.T) {
	in := load(t, repoRoot, "")
	observed := in.Conformance.Observations["v2 get /tags"]
	observed.Query["colour"] = "x"
	in.Conformance.Observations["v2 get /tags"] = observed

	assertFinding(t, CheckLocal(in),
		"`get /tags` (v2): the client sends the query parameter `colour`, which that surface's contract does not document")
}

// TestWrongRouteIsReported: the row names a method, the method requests something
// else. Under the analyzer this needed a mutated source file and a derivation that
// could read it; here it is the request line.
func TestWrongRouteIsReported(t *testing.T) {
	in := load(t, repoRoot, "")
	observed := in.Conformance.Observations["v2 get /tags/{tag_id}"]
	observed.Path = "/vocabularies/{}"
	in.Conformance.Observations["v2 get /tags/{tag_id}"] = observed

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
		observed := in.Conformance.Observations["v2 "+row.Key()]
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
    documented_response: array
    actual_response: list-envelope
server_error_codes: [a, b, c, d, e, f, g, h, i, j]
`
	for _, tc := range []struct{ name, yaml, want string }{
		{"prefixed path", strings.Replace(valid, "path: /tags", "path: /api/v2/tags", 1), "must not carry the /api/<version> prefix"},
		{"unknown method", strings.Replace(valid, "method: get", "method: GET", 1), "is not a lowercase HTTP method"},
		{"bare sdk name", strings.Replace(valid, "sdk: TagService.List", "sdk: List", 1), "must name the method as Receiver.Method"},
		{"neither implemented nor not", strings.Replace(valid, "    sdk: TagService.List\n", "", 1), "needs either an sdk method or an unimplemented reason"},
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
	spec := loadSpecs(t)
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
	if _, ok := conf.Observations["v2 get /tags"]; ok {
		t.Error("a driver that issued two requests was recorded as an observation")
	}
	if err := conf.Errors["v2 get /tags"]; err == nil || !strings.Contains(err.Error(), "issued 2 requests") {
		t.Errorf("expected a two-request error, got %v", err)
	}
}

// TestDriverThatNeverReachesTheWireIsReported covers the other end: a call the
// client refuses before sending anything -- an option the transport rejects, say.
// Silence there would read as an operation with no parameters at all.
func TestDriverThatNeverReachesTheWireIsReported(t *testing.T) {
	spec := loadSpecs(t)
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
	if err := conf.Errors["v2 post /tags"]; err == nil || !strings.Contains(err.Error(), "reached no request") {
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
	observed := in.Conformance.Observations["v2 post /tags"]
	delete(observed.Body, "slug")
	delete(observed.Body, "metadata")
	in.Conformance.Observations["v2 post /tags"] = observed

	assertFinding(t, CheckLocal(in),
		"schema `TagWrite` documents `slug` and the client did not send it",
		"schema `TagWrite` documents `metadata` and the client did not send it")
}

// TestEmptyRequestBodyIsReported is the same hole at its widest: a write that
// sends no payload at all.
func TestEmptyRequestBodyIsReported(t *testing.T) {
	in := load(t, repoRoot, "")
	observed := in.Conformance.Observations["v2 post /tags"]
	observed.Body = map[string]json.RawMessage{}
	in.Conformance.Observations["v2 post /tags"] = observed

	assertFinding(t, CheckLocal(in),
		"the contract documents a `TagWrite` request body and the client sent none")
}

// TestUndocumentedRequestBodyPropertyIsReported is the other direction: a
// property the server was never told to expect.
func TestUndocumentedRequestBodyPropertyIsReported(t *testing.T) {
	in := load(t, repoRoot, "")
	observed := in.Conformance.Observations["v2 post /tags"]
	observed.Body["colour"] = json.RawMessage(`"x"`)
	in.Conformance.Observations["v2 post /tags"] = observed

	assertFinding(t, CheckLocal(in),
		"the client sends `colour` in its request body, which schema `TagWrite` does not document")
}

// TestMissingDocumentedHeaderIsReported covers the namespace axis, which lives
// entirely in headers: a method that stopped propagating X-Namespace-* looked
// exactly like one that never could, because nothing recorded headers at all.
func TestMissingDocumentedHeaderIsReported(t *testing.T) {
	in := load(t, repoRoot, "")
	observed := in.Conformance.Observations["v2 get /tags"]
	delete(observed.Headers, "X-Namespace-Type")
	in.Conformance.Observations["v2 get /tags"] = observed

	assertFinding(t, CheckLocal(in),
		"`get /tags` documents the header `X-Namespace-Type` and the client did not send it")
}

// TestUndocumentedHeaderIsReported: the always-sent transport headers are
// recorded once under client_headers, and anything else the client starts sending
// is a finding.
func TestUndocumentedHeaderIsReported(t *testing.T) {
	in := load(t, repoRoot, "")
	observed := in.Conformance.Observations["v2 get /tags"]
	observed.Headers["X-Invented-Header"] = "x"
	in.Conformance.Observations["v2 get /tags"] = observed

	assertFinding(t, CheckLocal(in),
		"the client sends the header `X-Invented-Header`, which no vendored contract documents")
}

// TestBodyCarriedApplicationIDIsVerified keeps the eleven write exceptions from
// being an unchecked allowlist. Each declares `carried_in: body`, so the gate
// requires application_id to really be in that payload; if it stops being there,
// the row is asserting something false and the missing query key stops being
// excused.
func TestBodyCarriedApplicationIDIsVerified(t *testing.T) {
	in := load(t, repoRoot, "")
	observed := in.Conformance.Observations["v2 post /tags"]
	delete(observed.Body, "application_id")
	in.Conformance.Observations["v2 post /tags"] = observed

	assertFinding(t, CheckLocal(in),
		"`post /tags` records that `application_id` travels in the body instead, and the request's body does not carry it")
}

// TestSwappedPathArgumentsAreReported: both sentinels used to collapse to `{}`, so
// a driver passing resource_type where resource_id belongs produced exactly the
// expected route. Each placeholder has its own value now.
func TestSwappedPathArgumentsAreReported(t *testing.T) {
	spec := loadSpecs(t)
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
	observed := conf.Observations["v2 get /resources/{resource_type}/{resource_id}/tags"]
	if observed.Path == "/resources/{resource_type}/{resource_id}/tags" {
		t.Error("swapping the two path arguments produced the expected route")
	}
}

// TestValueDependentRouteIsReported: one execution says what the client did with
// one input. Two inputs do not prove a route is invariant, but they catch a route
// that varies with the value -- which one cannot. The comparison covers the whole
// request, not just its route: a driver that passed its options on one execution
// and not the other used to pass, because the first supplied all the evidence.
func TestValueDependentRouteIsReported(t *testing.T) {
	spec := loadSpecs(t)
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
	if err := conf.Errors["v2 get /tags/{tag_id}"]; err == nil || !strings.Contains(err.Error(), "the two executions sent different requests") {
		t.Errorf("a value-dependent route was accepted: %v", err)
	}
}

// TestNullableContainerRoundTripsAsAbsent: a nullable property whose Go field is a
// map or a slice carrying `omitempty` vanishes from the round trip when it is
// null -- and that is correct, because nil is exactly the absent state for those.
// The null witness must not report it.
//
// Found by a review's own experiment, which marked `Tag.metadata` nullable and
// walked straight into the skip that used to cover BOTH this case and the one
// below.
func TestNullableContainerRoundTripsAsAbsent(t *testing.T) {
	repo := stageRepo(t)
	for _, spec := range []string{"openapi-v2.yaml", "openapi.yaml"} {
		editTagSchema(t, filepath.Join(repo, "docs", spec),
			"        metadata: {}\n",
			"        metadata:\n          nullable: true\n")
	}

	assertClean(t, runLocal(t, repo))
}

// editTagSchema applies a replacement inside the Tag schema only. Several schemas
// share property spellings, and an unanchored edit lands on whichever comes first.
func editTagSchema(t *testing.T, path, old, replacement string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	start := strings.Index(src, "\n    Tag:\n")
	end := strings.Index(src, "\n    TagAlias:\n")
	if start < 0 || end < start {
		t.Fatal("the Tag schema is no longer where this fixture looks for it")
	}
	segment := strings.Replace(src[start:end], old, replacement, 1)
	if segment == src[start:end] {
		t.Fatalf("%q not found in the Tag schema", old)
	}
	write(t, path, src[:start]+segment+src[end:])
}

// TestUnsynthesizableSchemaBlamesTheGate: a schema shape the stub cannot build a
// body from must be reported as the GATE's gap, not as the SDK failing to handle
// a response. The two reach the client identically -- as a transport error -- and
// telling them apart is the difference between "teach the tool" and "fix the
// client".
func TestUnsynthesizableSchemaBlamesTheGate(t *testing.T) {
	repo := stageRepo(t)
	for _, spec := range []string{"openapi-v2.yaml", "openapi.yaml"} {
		editTagSchema(t, filepath.Join(repo, "docs", spec),
			"        usage_count:\n          type: integer\n",
			"        usage_count:\n          allOf:\n          - type: integer\n          - type: string\n")
	}

	assertFinding(t, runLocal(t, repo),
		"the response stub could not build a body",
		"allOf of 2 members")
}

// TestCarriedInMustNotRepeatIn: a row saying an input is unsent in the body AND
// carried in the body says the client both does and does not send it there.
func TestCarriedInMustNotRepeatIn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "contract-coverage.yaml")
	write(t, path, `
operations:
  - path: /tags
    method: get
    sdk: TagService.List
    documented_response: array
    actual_response: list-envelope
server_error_codes: [a, b, c, d, e, f, g, h, i, j]
unsent_inputs:
  - path: /tags
    method: get
    in: query
    name: q
    carried_in: query
    reason: x
`)
	_, err := LoadCoverage(path)
	if err == nil || !strings.Contains(err.Error(), "repeats `in`") {
		t.Errorf("expected a carried_in/in conflict, got %v", err)
	}
}

// TestStaleClientHeaderIsReported: every allowlist in the coverage file gets the
// same staleness check, this one included. A header recorded as always-sent that
// the client no longer sends suppresses nothing -- and would suppress the next
// header to go missing just as quietly.
func TestStaleClientHeaderIsReported(t *testing.T) {
	repo := stageRepo(t)
	edit(t, filepath.Join(repo, "docs", "contract-coverage.yaml"),
		"\nclient_headers:\n",
		"\nclient_headers:\n",
		"\nclient_headers:\n  - name: X-Gone\n    reason: left behind by an earlier refactor\n")

	assertFinding(t, runLocal(t, repo),
		"`X-Gone` is listed under client_headers and the client sends it on no operation")
}

// TestStaleUndocumentedRequestBodyIsReported: the same, for the row that records a
// payload the contract does not describe.
func TestStaleUndocumentedRequestBodyIsReported(t *testing.T) {
	repo := stageRepo(t)
	edit(t, filepath.Join(repo, "docs", "contract-coverage.yaml"),
		"  - path: /tags/{tag_id}\n    method: delete\n",
		"    documented_response: none\n",
		"    undocumented_request_body: this DELETE sends no body at all\n    documented_response: none\n")

	assertFinding(t, runLocal(t, repo),
		"`delete /tags/{tag_id}` records an undocumented request body and the client sends no body at all")
}

// --- values, not just names ------------------------------------------------------
//
// A review sent four requests past this gate with every documented name in place
// and the wrong thing under each: a parameter the contract had retyped, a params
// struct wiring `q` to the Slug field, two JSON tags swapped on a write model, and
// the two namespace headers crossed. These are those four, plus the two response
// cases in the same family.

// TestRetypedQueryParameterIsReported: the contract retypes a parameter and the
// client keeps sending a string. Both names are present; only the value differs.
func TestRetypedQueryParameterIsReported(t *testing.T) {
	repo := stageRepo(t)
	for _, spec := range []string{"openapi-v2.yaml", "openapi.yaml"} {
		surface := "2"
		if !strings.Contains(spec, "-v2") {
			surface = "1"
		}
		edit(t, filepath.Join(repo, "docs", spec),
			"operationId: api_v"+surface+"_tags_list",
			"      - in: query\n        name: q\n        schema:\n          type: string\n",
			"      - in: query\n        name: q\n        schema:\n          type: integer\n")
	}

	assertFinding(t, runLocal(t, repo),
		"the query parameter `q` is documented as `integer` and the client sends")
}

// TestMiswiredQueryValueIsReported: `q` carries the value the driver supplied for
// `slug`, so the public Query input is ignored. Every name is still right.
func TestMiswiredQueryValueIsReported(t *testing.T) {
	in := load(t, repoRoot, "")
	observed := in.Conformance.Observations["v2 get /tags"]
	observed.Query["q"] = observed.Query["slug"]
	in.Conformance.Observations["v2 get /tags"] = observed

	assertFinding(t, CheckLocal(in),
		"the query parameter `q` carries the value the driver supplied for `slug`")
}

// TestMiswiredBodyValueIsReported is the same on a write: two JSON tags swapped.
func TestMiswiredBodyValueIsReported(t *testing.T) {
	in := load(t, repoRoot, "")
	observed := in.Conformance.Observations["v2 post /tags"]
	observed.Body["name"], observed.Body["slug"] = observed.Body["slug"], observed.Body["name"]
	in.Conformance.Observations["v2 post /tags"] = observed

	assertFinding(t, CheckLocal(in),
		"the body property `name` carries the value the driver supplied for `slug`",
		"the body property `slug` carries the value the driver supplied for `name`")
}

// TestCrossedNamespaceHeadersAreReported: both header names present, each carrying
// the other's value, so every namespaced call targets the wrong scope pair.
func TestCrossedNamespaceHeadersAreReported(t *testing.T) {
	in := load(t, repoRoot, "")
	observed := in.Conformance.Observations["v2 get /tags"]
	observed.Headers["X-Namespace-Type"], observed.Headers["X-Namespace-Id"] =
		observed.Headers["X-Namespace-Id"], observed.Headers["X-Namespace-Type"]
	in.Conformance.Observations["v2 get /tags"] = observed

	assertFinding(t, CheckLocal(in), "carries the value the driver supplied for `x-namespace-")
}

// TestMissingClientHeaderOnOneOperationIsReported: client_headers means EVERY
// versioned request. Requiring each name to have been seen somewhere let a client
// drop X-Tenant-ID from every request carrying a body while the reads kept it --
// every write losing its tenant scope, reported as nothing at all.
func TestMissingClientHeaderOnOneOperationIsReported(t *testing.T) {
	in := load(t, repoRoot, "")
	observed := in.Conformance.Observations["v2 post /tags"]
	delete(observed.Headers, "X-Tenant-Id")
	in.Conformance.Observations["v2 post /tags"] = observed

	assertFinding(t, CheckLocal(in),
		"`post /tags` does not carry `X-Tenant-Id`, which client_headers records as sent on every versioned request")
}

// TestClientHeaderOnHealthIsReported is the other half of that rule: the probes
// authenticate nobody, so a credential leaking onto them is a finding.
func TestClientHeaderOnHealthIsReported(t *testing.T) {
	in := load(t, repoRoot, "")
	observed := in.Conformance.Observations["v2 get /health/live"]
	observed.Headers["Authorization"] = "Bearer leaked"
	in.Conformance.Observations["v2 get /health/live"] = observed

	assertFinding(t, CheckLocal(in),
		"carries `Authorization`, and the unversioned probes authenticate nobody")
}

// TestSwappedResponseTagsAreReported is the defect a round trip structurally
// cannot see: the same tags decode and re-encode, so identical bytes come back
// while the caller reads the server's name out of Tag.Slug.
//
// It mutates the staged model and runs the real check. The version of this test a
// review found only asserted that SnakeCase("Slug") != "name" -- removing the
// checkModelFieldNames call from CheckLocal would have left it green.
func TestSwappedResponseTagsAreReported(t *testing.T) {
	repo := stageRepo(t)
	edit(t, filepath.Join(repo, "tags.go"), "type Tag struct {",
		"\tName          string    `json:\"name\"`\n\tSlug          string    `json:\"slug\"`\n",
		"\tName          string    `json:\"slug\"`\n\tSlug          string    `json:\"name\"`\n")

	assertFinding(t, runLocal(t, repo),
		"model `Tag`: the field `Name` decodes `slug`",
		"model `Tag`: the field `Slug` decodes `name`")
}

// TestModelFieldNamesAreNotVacuous: the convention really is checked, over real
// fields, including the acronym spellings it has to get right.
func TestModelFieldNamesAreNotVacuous(t *testing.T) {
	in := load(t, repoRoot, "")
	checked := 0
	for _, model := range []string{"Tag", "Vocabulary", "TagAlias", "Assignment", "AuditLog"} {
		fields, ok := in.SDK.ModelFields(model)
		if !ok || len(fields) == 0 {
			t.Errorf("%s has no fields to check", model)
			continue
		}
		checked += len(fields)
	}
	if checked < 50 {
		t.Errorf("only %d fields take part in the name check", checked)
	}
	for name, want := range map[string]string{
		"TagID": "tag_id", "ApplicationID": "application_id", "ID": "id",
		"UsageCount": "usage_count", "NamespaceType": "namespace_type",
	} {
		if got := SnakeCase(name); got != want {
			t.Errorf("SnakeCase(%q) = %q, want %q", name, got, want)
		}
	}
}

// TestDuplicateDriverIsRejected: indexing drivers by operation let a wrong-route
// duplicate be overwritten by a correct one, and the gate saw only the correct
// evidence.
func TestDuplicateDriverIsRejected(t *testing.T) {
	spec := loadSpecs(t)
	cov, err := LoadCoverage(filepath.Join(repoRoot, "docs", "contract-coverage.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	driver := Driver{
		Op:  "get /tags",
		SDK: "TagService.List",
		Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Tags.List(ctx, nil)
		},
	}

	conf, err := RunConformance(spec, cov, []Driver{driver, driver})
	if err != nil {
		t.Fatal(err)
	}
	if err := conf.Errors["v2 get /tags"]; err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Errorf("a duplicate driver was accepted: %v", err)
	}
}

// TestDriverWithInconsistentOptionsIsReported: the second execution's request used
// to be discarded except for its decoded body, so a driver that passed its options
// once and not the other time was invisible.
func TestDriverWithInconsistentOptionsIsReported(t *testing.T) {
	spec := loadSpecs(t)
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
				return env.Client.Tags.Get(ctx, id, env.ReadScope()...)
			}
			return env.Client.Tags.Get(ctx, id)
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := conf.Errors["v2 get /tags/{tag_id}"]; err == nil || !strings.Contains(err.Error(), "different requests") {
		t.Errorf("a driver whose two executions differed was accepted: %v", err)
	}
}

// --- exact values ----------------------------------------------------------------
//
// The sentinel convention only ever reported a value that still LOOKED like a
// sentinel for some other field. A review walked through that six ways, and these
// are those six: a value has to be what the driver sent, not merely plausible.

// TestHardCodedValueIsReported: the value stops looking like a sentinel at all.
func TestHardCodedValueIsReported(t *testing.T) {
	in := load(t, repoRoot, "")
	observed := in.Conformance.Observations["v2 get /tags"]
	observed.Query["q"] = "hard-coded-wrong-value"
	in.Conformance.Observations["v2 get /tags"] = observed

	assertFinding(t, CheckLocal(in),
		`the driver sent "cd~q" for the query parameter `+"`q`"+` and the client put "hard-coded-wrong-value" on the wire`)
}

// TestSwappedIntegerValuesAreReported: limit and offset both parse as integers, so
// a type check alone could never tell them apart. The table gives them different
// numbers for exactly that reason.
func TestSwappedIntegerValuesAreReported(t *testing.T) {
	in := load(t, repoRoot, "")
	observed := in.Conformance.Observations["v2 get /tags"]
	observed.Query["limit"], observed.Query["offset"] = observed.Query["offset"], observed.Query["limit"]
	in.Conformance.Observations["v2 get /tags"] = observed

	assertFinding(t, CheckLocal(in),
		"for the query parameter `limit`", "for the query parameter `offset`")
}

// TestSwappedBooleanValuesAreReported: two booleans on one request would be
// interchangeable if both were `true`. The table makes them differ.
func TestSwappedBooleanValuesAreReported(t *testing.T) {
	in := load(t, repoRoot, "")
	observed := in.Conformance.Observations["v2 get /tags"]
	observed.Query["include_shared"], observed.Query["is_active"] =
		observed.Query["is_active"], observed.Query["include_shared"]
	in.Conformance.Observations["v2 get /tags"] = observed

	assertFinding(t, CheckLocal(in), "for the query parameter `include_shared`")
}

// TestWrongCredentialValueIsReported: client_headers used to check presence only,
// so an arbitrary token under Authorization passed.
func TestWrongCredentialValueIsReported(t *testing.T) {
	in := load(t, repoRoot, "")
	observed := in.Conformance.Observations["v2 get /tags"]
	observed.Headers["Authorization"] = "Bearer wrong"
	in.Conformance.Observations["v2 get /tags"] = observed

	assertFinding(t, CheckLocal(in), "and sent `Authorization: Bearer wrong`")
}

// TestEmptiedArrayIsReported: recursing over the elements that remain let an
// emptied array through, because there was nothing left to disagree with.
func TestEmptiedArrayIsReported(t *testing.T) {
	in := load(t, repoRoot, "")
	observed := in.Conformance.Observations["v2 post /tag-assignments/bulk-assign"]
	observed.Body["tag_ids"] = json.RawMessage(`[]`)
	in.Conformance.Observations["v2 post /tag-assignments/bulk-assign"] = observed

	assertFinding(t, CheckLocal(in),
		"the driver sent one element for the body property `tag_ids` and the client put 0 on the wire")
}

// TestReusedPathValueIsReported: a path witness reused as a query value. The
// two-execution comparison used to suppress any difference where either side was a
// path witness, which accepted it across both runs.
func TestReusedPathValueIsReported(t *testing.T) {
	in := load(t, repoRoot, "")
	observed := in.Conformance.Observations["v2 get /tag-resolution"]
	observed.Query["slug"] = "TAGID1"
	in.Conformance.Observations["v2 get /tag-resolution"] = observed

	assertFinding(t, CheckLocal(in), "for the query parameter `slug`")
}

// TestSwappedResponseValuesAreReported: every key present, each carrying the
// other's contents. The populated witness kept its values and never compared them.
func TestSwappedResponseValuesAreReported(t *testing.T) {
	in := load(t, repoRoot, "")
	observed := in.Conformance.Observations["v2 get /tags"]
	observed.Decoded["name"], observed.Decoded["slug"] = observed.Decoded["slug"], observed.Decoded["name"]
	in.Conformance.Observations["v2 get /tags"] = observed

	assertFinding(t, CheckLocal(in), "the model is not putting it where it belongs")
}

// TestSwappedCompositeCountersAreReported: composites sat outside every value
// comparison, so swapping two counters left the bytes intact and was invisible.
// checkDecodedValues owns this now -- the composite-only check it replaced was
// pinned to v2 and execution 1, and so was strictly weaker.
func TestSwappedCompositeCountersAreReported(t *testing.T) {
	in := load(t, repoRoot, "")
	observed := in.Conformance.Observations["v2 post /tag-assignments/bulk-assign"]
	observed.Decoded["created"], observed.Decoded["existing"] =
		observed.Decoded["existing"], observed.Decoded["created"]
	in.Conformance.Observations["v2 post /tag-assignments/bulk-assign"] = observed

	assertFinding(t, CheckLocal(in), "the response sent `created` as 1")
}

// --- both surfaces ----------------------------------------------------------------

// TestV1OnlyParameterIsReported: the gate drove v2 only, so a v1 contract change
// could land with no client follow-through and the offline gate stayed clean --
// the hole it exists to close, on the half of the API it was not looking at.
func TestV1OnlyParameterIsReported(t *testing.T) {
	repo := stageRepo(t)
	edit(t, filepath.Join(repo, "docs", "openapi.yaml"),
		"operationId: api_v1_tag_resolution_retrieve",
		"      parameters:\n",
		"      parameters:\n      - in: query\n        name: v1_only_hint\n        schema:\n          type: string\n")

	assertFinding(t, runLocal(t, repo),
		"`get /tag-resolution` documents the query parameter `v1_only_hint` and the client did not send it")
}

// TestBothSurfacesAreDriven is the non-vacuity guard on that: every implemented
// operation is exercised on each surface, and v1 really is reached at its own
// prefix.
func TestBothSurfacesAreDriven(t *testing.T) {
	in := load(t, repoRoot, "")
	for _, surface := range []string{"v1", "v2"} {
		seen := 0
		for _, row := range in.Coverage.Operations {
			if row.Unimplemented != "" {
				continue
			}
			if _, ok := in.Conformance.Observations[surface+" "+row.Key()]; ok {
				seen++
			}
		}
		if seen < 25 {
			t.Errorf("%s: only %d operations were observed", surface, seen)
		}
	}
	// And the namespace axis is v2 only: a v1 client refuses the headers, so the
	// drivers must not be sending them there.
	v1 := in.Conformance.Observations["v1 get /tags"]
	if _, sent := v1.Headers["X-Namespace-Type"]; sent {
		t.Error("the v1 client sent a namespace header, which v1 rejects")
	}
	v2 := in.Conformance.Observations["v2 get /tags"]
	if _, sent := v2.Headers["X-Namespace-Type"]; !sent {
		t.Error("the v2 client did not send a namespace header")
	}
}

// --- the sixth pass ---------------------------------------------------------------

// TestWrongVersionPrefixIsReported is the review's own repro, and the bug it
// found: the APIVersion field simply was not set, so both passes built a default
// v2 client and the "v1" run was a v2 run compared against the v1 document. The
// prefix was erased by the suffix normalization, so nothing could see it either.
func TestWrongVersionPrefixIsReported(t *testing.T) {
	in := load(t, repoRoot, "")
	observed := in.Conformance.Observations["v1 get /tags"]
	observed.Prefix = "/api/v2"
	in.Conformance.Observations["v1 get /tags"] = observed

	assertFinding(t, CheckLocal(in), "`get /tags` (v1): the client sent it to `/api/v2/tags`")
}

// TestSurfacesReachTheirOwnPrefix is the non-vacuity guard on that: the real runs
// go where their configured surface says, and the health probes go outside both.
func TestSurfacesReachTheirOwnPrefix(t *testing.T) {
	in := load(t, repoRoot, "")
	for surface, want := range map[string]string{"v1": "/api/v1", "v2": "/api/v2"} {
		observed, ok := in.Conformance.Observations[surface+" get /tags"]
		if !ok {
			t.Fatalf("no observation for %s get /tags", surface)
		}
		if observed.Prefix != want {
			t.Errorf("%s: the client reached %q, want %q", surface, observed.Prefix, want)
		}
	}
	probe := in.Conformance.Observations["v2 get /health/live"]
	if probe.Prefix != "" {
		t.Errorf("the health probe was sent to %q; it is outside the versioned API", probe.Prefix)
	}
}

// TestBooleansAreDistinguishableAcrossExecutions: two values cannot tell three
// boolean axes apart in one execution, so each carries a distinct PAIR across the
// two -- but only among the booleans that can ride the SAME request. The groups
// come from the contract rather than from a list here, so a new boolean parameter
// on an existing operation is checked the day it arrives.
func TestBooleansAreDistinguishableAcrossExecutions(t *testing.T) {
	in := load(t, repoRoot, "")
	spec := in.Vendored["v2"]

	groups := 0
	for _, key := range sortedKeys(spec.Operations) {
		op := spec.Operations[key]
		patterns := map[[2]string]string{}
		booleans := 0
		for _, name := range op.QueryParams() {
			if op.ParamSchema("query", name)["type"] != "boolean" {
				continue
			}
			booleans++
			pattern := [2]string{ExpectedValue(name, 0), ExpectedValue(name, 1)}
			if other, clash := patterns[pattern]; clash {
				t.Errorf("%s: %q and %q both carry %v, so swapping them would change nothing",
					key, name, other, pattern)
			}
			patterns[pattern] = name
		}
		if booleans > 1 {
			groups++
		}
	}
	if groups == 0 {
		t.Fatal("no operation documents more than one boolean; this test is asserting nothing")
	}
}

// TestSameTypedResponseValuesDiffer: every date-time was one timestamp and every
// integer was 1, so a decoder crossing two same-typed properties preserved every
// compared byte.
func TestSameTypedResponseValuesDiffer(t *testing.T) {
	in := load(t, repoRoot, "")
	observed := in.Conformance.Observations["v2 get /tags"]
	if string(observed.Sent["created_at"]) == string(observed.Sent["updated_at"]) {
		t.Errorf("created_at and updated_at carry the same witness %s", observed.Sent["created_at"])
	}
	pagination := map[string]bool{}
	for _, field := range []string{"pagination.limit", "pagination.offset", "pagination.count"} {
		value := string(observed.Sent[field])
		if value == "" {
			t.Errorf("%s was not sent", field)
		}
		if pagination[value] {
			t.Errorf("%s reuses the witness %s", field, value)
		}
		pagination[value] = true
	}
}

// TestCrossedPaginationValuesAreReported: the pagination block was dropped from
// the comparison entirely, so a client crossing two of its fields after decoding
// was invisible.
func TestCrossedPaginationValuesAreReported(t *testing.T) {
	in := load(t, repoRoot, "")
	observed := in.Conformance.Observations["v2 get /tags"]
	observed.Decoded["pagination.offset"], observed.Decoded["pagination.count"] =
		observed.Decoded["pagination.count"], observed.Decoded["pagination.offset"]
	in.Conformance.Observations["v2 get /tags"] = observed

	assertFinding(t, CheckLocal(in), "the list envelope sent `offset` as")
}

// TestDiscardedCompositeRowsAreReported: both composite arrays were empty, so a
// decoder that dropped every returned row returned exactly what was sent.
func TestDiscardedCompositeRowsAreReported(t *testing.T) {
	in := load(t, repoRoot, "")
	observed := in.Conformance.Observations["v2 post /tag-assignments/bulk-assign"]
	observed.Decoded["assignments"] = json.RawMessage(`[]`)
	in.Conformance.Observations["v2 post /tag-assignments/bulk-assign"] = observed

	assertFinding(t, CheckLocal(in), "the response sent `assignments` as")
}

// TestCompositeWitnessesAreDistinct guards the numbers the check above rests on:
// a decoder crossing two counters has to change both.
func TestCompositeWitnessesAreDistinct(t *testing.T) {
	in := load(t, repoRoot, "")
	for _, row := range in.Coverage.Operations {
		if row.CompositeBody == "" {
			continue
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal([]byte(row.CompositeBody), &body); err != nil {
			t.Fatalf("%s: %v", row.Key(), err)
		}
		seen := map[string]string{}
		for name, raw := range body {
			var number float64
			if err := json.Unmarshal(raw, &number); err != nil {
				continue // an array or an object, not a counter
			}
			if other, clash := seen[string(raw)]; clash {
				t.Errorf("%s: %q and %q are both %s; crossing them would change nothing",
					row.Key(), name, other, string(raw))
			}
			seen[string(raw)] = name
		}
		for name, raw := range body {
			var rows []json.RawMessage
			if err := json.Unmarshal(raw, &rows); err == nil && len(rows) == 0 {
				t.Errorf("%s: %q is an empty array; a decoder that discards every row would return it unchanged",
					row.Key(), name)
			}
		}
	}
}

// TestFreeFormObjectValueIsCompared: `metadata` is unconstrained by the contract,
// but the gate controls both sides, so a driver whose object stopped arriving
// intact should not be invisible.
func TestFreeFormObjectValueIsCompared(t *testing.T) {
	in := load(t, repoRoot, "")
	observed := in.Conformance.Observations["v2 post /tags"]
	observed.Body["metadata"] = json.RawMessage(`{"wrong":"value"}`)
	in.Conformance.Observations["v2 post /tags"] = observed

	assertFinding(t, CheckLocal(in), "for the body property `metadata`")
}

// TestListScopeContributesNoApplication is the one-source rule, tested where it
// lives. The version of this a review found deleted `application_id` from a
// recorded observation and asserted the ordinary missing-query checker reported
// it -- which is a different test that stays green if ListScope regresses and
// re-adds WithApplication, recreating the duplicate source it exists to prevent.
func TestListScopeContributesNoApplication(t *testing.T) {
	env := &Env{surface: "v2"}
	if len(env.ListScope()) == 0 {
		t.Fatal("ListScope contributes nothing at all")
	}
	// The option list is opaque, so the check is behavioural: a client given only
	// ListScope must put no application_id on the wire.
	spec := loadSpecs(t)
	cov, err := LoadCoverage(filepath.Join(repoRoot, "docs", "contract-coverage.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	conf, err := RunConformance(spec, cov, []Driver{{
		Op:  "get /tags",
		SDK: "TagService.List",
		Call: func(ctx context.Context, env *Env) (any, error) {
			// No params struct, so nothing else can supply it either.
			return env.Client.Tags.List(ctx, nil, env.ListScope()...)
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	observed := conf.Observations["v2 get /tags"]
	if _, sent := observed.Query["application_id"]; sent {
		t.Error("ListScope put application_id on the wire; the params struct is supposed to be its only source")
	}
	// And ReadScope, which is the one that should.
	readEnv := &Env{surface: "v2"}
	if len(readEnv.ReadScope()) <= len(env.ListScope()) {
		t.Error("ReadScope does not add the application option ListScope omits")
	}
}

// --- the seventh pass -------------------------------------------------------------

// TestHardCodedPassValueIsReported is the review's BLOCKER: comparing the two
// executions and ALLOWING the declared difference is an exemption, not an
// assertion, so a client hard-coding one pass's value satisfied it on both.
func TestHardCodedPassValueIsReported(t *testing.T) {
	in := load(t, repoRoot, "")
	observed := in.Conformance.Observations["v2 get /tags"]
	second := *observed.Second
	second.Query = map[string]string{}
	for name, value := range observed.Second.Query {
		second.Query[name] = value
	}
	// Both executions now carry the FIRST pass's value.
	second.Query["include_shared"] = observed.Query["include_shared"]
	observed.Second = &second
	in.Conformance.Observations["v2 get /tags"] = observed

	assertFinding(t, CheckLocal(in),
		"on execution 2 the driver sent \"false\" for the query parameter `include_shared` and the client put \"true\" on the wire")
}

// TestSecondExecutionPrefixIsCompared: the prefix assertion downstream sees the
// first pass only, so a client sending just its second execution to the wrong
// /api/<version> was invisible.
func TestSecondExecutionPrefixIsCompared(t *testing.T) {
	a := Observation{Method: "get", Path: "/tags", Prefix: "/api/v2"}
	b := Observation{Method: "get", Path: "/tags", Prefix: "/api/v1"}
	if diff := requestDiff(a, b); !strings.Contains(diff, "prefixes") {
		t.Errorf("a differing prefix was not reported: %q", diff)
	}
}

// TestSecondExecutionFailureIsCompared: a second-pass error was consulted only on
// ordinary model rows, so a composite or no-content operation could fail on its
// second witness with nothing said.
func TestSecondExecutionFailureIsCompared(t *testing.T) {
	a := Observation{Method: "post", Path: "/tags"}
	b := Observation{Method: "post", Path: "/tags", CallErr: errSecondPass}
	if diff := requestDiff(a, b); !strings.Contains(diff, "one execution failed") {
		t.Errorf("a second-execution failure was not reported: %q", diff)
	}
}

var errSecondPass = errors.New("the second execution failed")

// TestEveryBooleanExercisesBothValues: `include_inactive` had the pattern
// {false,false}, so the only value it ever sent was the one it defaults to and
// the opt-in was never exercised at all.
func TestEveryBooleanExercisesBothValues(t *testing.T) {
	for _, name := range []string{"include_shared", "is_active", "include_inactive"} {
		if ExpectedValue(name, 0) == ExpectedValue(name, 1) {
			t.Errorf("%q carries %q on both executions; one of its two values is never sent",
				name, ExpectedValue(name, 0))
		}
	}
}

// TestDroppedPaginationFieldIsReported: the pagination comparison walked the
// DECODED side, which can never notice a field the model dropped -- a dropped
// field is not there to walk.
func TestDroppedPaginationFieldIsReported(t *testing.T) {
	in := load(t, repoRoot, "")
	observed := in.Conformance.Observations["v2 get /tags"]
	delete(observed.Decoded, "pagination.count")
	in.Conformance.Observations["v2 get /tags"] = observed

	assertFinding(t, CheckLocal(in),
		"the list envelope carries `count` and `TagService.List` decodes it away")
}

// TestV1OnlyResponsePropertyIsReported: an unknown JSON property decodes without
// error, so merely calling the v1 method proved nothing about its fields. The
// forward comparison runs on both surfaces now.
func TestV1OnlyResponsePropertyIsReported(t *testing.T) {
	repo := stageRepo(t)
	editTagSchema(t, filepath.Join(repo, "docs", "openapi.yaml"),
		"      properties:\n",
		"      properties:\n        v1_only_colour:\n          type: string\n")

	assertFinding(t, runLocal(t, repo), "v1_only_colour")
}

// TestCompositeModelsReachTheFieldNameCheck: the composite result structs are
// named by no success schema, so the check that catches a round-trip-invariant tag
// swap did not reach them.
//
// It mutates a staged model and asserts through the real run. The version a review
// found called fieldNameFindings directly, so removing BulkAssignResult from the
// production list left it green -- which is the same disconnected-test mistake
// this file made once before.
func TestCompositeModelsReachTheFieldNameCheck(t *testing.T) {
	repo := stageRepo(t)
	edit(t, filepath.Join(repo, "assignments.go"), "type BulkAssignResult struct {",
		"\tCreated     int          `json:\"created\"`\n\tExisting    int          `json:\"existing\"`\n",
		"\tCreated     int          `json:\"existing\"`\n\tExisting    int          `json:\"created\"`\n")

	assertFinding(t, runLocal(t, repo),
		"model `BulkAssignResult`: the field `Created` decodes `existing`",
		"model `BulkAssignResult`: the field `Existing` decodes `created`")
}

// TestUnreachableModelsAreNamed guards the list those models are named in: every
// one has to resolve, or the check above silently covers nothing.
func TestUnreachableModelsAreNamed(t *testing.T) {
	in := load(t, repoRoot, "")
	for _, model := range []string{"Pagination", "BulkAssignResult", "BulkRemoveResult", "ResourceReplaceResult", "HealthStatus"} {
		fields, ok := in.SDK.ModelFields(model)
		if !ok || len(fields) == 0 {
			t.Errorf("%s is not reachable by the field-name check", model)
		}
	}
}

// --- the eighth pass --------------------------------------------------------------

// TestHardCodedBodyBooleanIsReported is the review's BLOCKER: JSON booleans and
// numbers reached only a TYPE check, so a request-body boolean hard-coded to one
// execution's value passed on both -- the seventh pass's defect again, through a
// different channel.
func TestHardCodedBodyBooleanIsReported(t *testing.T) {
	in := load(t, repoRoot, "")
	observed := in.Conformance.Observations["v2 post /tags"]
	second := *observed.Second
	second.Body = map[string]json.RawMessage{}
	for name, value := range observed.Second.Body {
		second.Body[name] = value
	}
	second.Body["is_active"] = observed.Body["is_active"] // execution 1's value
	observed.Second = &second
	in.Conformance.Observations["v2 post /tags"] = observed

	assertFinding(t, CheckLocal(in), "for the body property `is_active`")
}

// TestUndocumentedInputValueIsChecked: a recorded divergence means the CONTRACT
// has nothing to compare against. It never meant the DRIVER had nothing to compare
// against, and the exception used to skip the value entirely.
func TestUndocumentedInputValueIsChecked(t *testing.T) {
	in := load(t, repoRoot, "")
	observed := in.Conformance.Observations["v1 get /tags/{tag_id}"]
	observed.Query["application_id"] = "wrong-application"
	in.Conformance.Observations["v1 get /tags/{tag_id}"] = observed

	assertFinding(t, CheckLocal(in), `the client put "wrong-application" on the wire`)
}

// TestUndocumentedRequestBodyValuesAreChecked: the body-carrying DELETE took an
// early continue, so neither its field names nor their values were checked.
func TestUndocumentedRequestBodyValuesAreChecked(t *testing.T) {
	in := load(t, repoRoot, "")
	observed := in.Conformance.Observations["v2 delete /tag-assignments"]
	observed.Body["resource_type"] = observed.Body["tag_id"]
	in.Conformance.Observations["v2 delete /tag-assignments"] = observed

	assertFinding(t, CheckLocal(in),
		"the body property `resource_type` carries the value the driver supplied for `tag_id`")
}

// TestSecondExecutionResponseIsChecked: the forward response comparison read
// execution 1 only, so a value corrupted on the second witness alone was invisible.
func TestSecondExecutionResponseIsChecked(t *testing.T) {
	in := load(t, repoRoot, "")
	observed := in.Conformance.Observations["v2 get /tags/{tag_id}"]
	second := *observed.Second
	second.Decoded = map[string]json.RawMessage{}
	for name, value := range observed.Second.Decoded {
		second.Decoded[name] = value
	}
	second.Decoded["name"] = json.RawMessage(`""`)
	observed.Second = &second
	in.Conformance.Observations["v2 get /tags/{tag_id}"] = observed

	assertFinding(t, CheckLocal(in), "execution 2): the response sent `name`")
}

// TestV1CompositeResponseIsChecked: the composite comparison was pinned to v2, so
// a counter cleared on the v1 client alone was invisible.
func TestV1CompositeResponseIsChecked(t *testing.T) {
	in := load(t, repoRoot, "")
	observed := in.Conformance.Observations["v1 post /tag-assignments/bulk-assign"]
	observed.Decoded["created"] = json.RawMessage(`0`)
	in.Conformance.Observations["v1 post /tag-assignments/bulk-assign"] = observed

	assertFinding(t, CheckLocal(in), "(v1, execution 1): the response sent `created`")
}

// TestHealthResponseValueIsChecked: the probes sit outside the API surface, so
// every schema-driven check stepped around them and the returned word was never
// compared at all.
func TestHealthResponseValueIsChecked(t *testing.T) {
	in := load(t, repoRoot, "")
	observed := in.Conformance.Observations["v2 get /health/live"]
	observed.Decoded["status"] = json.RawMessage(`"wrong-status"`)
	in.Conformance.Observations["v2 get /health/live"] = observed

	assertFinding(t, CheckLocal(in), "the response sent `status` as \"ok\"")
}

// --- the ninth pass ---------------------------------------------------------------

// TestDetectsAddedResponseStatus pins checkResponseDrift, which was the one check
// of nineteen that no test held.
//
// Both workflows run `make contract-test` under a step whose stated purpose is to
// prove the gate can still fail; that guarantee was 18/19 true. Deleting the check
// left a green suite, and it is not redundant -- it is the only vendored-versus-
// upstream comparison of request bodies in the tool.
func TestDetectsAddedResponseStatus(t *testing.T) {
	upstream := stageUpstream(t)
	edit(t, filepath.Join(upstream, "openapi-v2.yaml"),
		"operationId: api_v2_audit_logs_list",
		"      responses:\n",
		"      responses:\n        '429':\n          description: ''\n")

	assertFinding(t, runFull(t, repoRoot, upstream),
		"gained a documented `429` response")
}

// TestDetectsChangedRequestBody is the other half of the same check, and the part
// nothing else covers at all.
func TestDetectsChangedRequestBody(t *testing.T) {
	upstream := stageUpstream(t)
	edit(t, filepath.Join(upstream, "openapi-v2.yaml"),
		"operationId: api_v2_tags_create",
		"$ref: '#/components/schemas/TagWrite'",
		"$ref: '#/components/schemas/TagRewrite'")

	assertFinding(t, runFull(t, repoRoot, upstream), "request body:")
}

// TestEveryCheckIsPinnedByATest is the guard that would have found the gap above.
//
// It is deliberately coarse: it asserts only that each check function is dispatched
// from CheckLocal or CheckUpstream, so a check that is written and never wired is
// caught. Whether each one is exercised is what the rest of this file is for.
func TestEveryCheckIsPinnedByATest(t *testing.T) {
	source, err := os.ReadFile("checks.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)

	declared := regexp.MustCompile(`(?m)^func (check[A-Za-z]+)\(in Inputs, r \*Report\)`).FindAllStringSubmatch(text, -1)
	if len(declared) < 15 {
		t.Fatalf("found only %d check functions; the pattern stopped matching", len(declared))
	}
	for _, match := range declared {
		name := match[1]
		if !strings.Contains(text, "\t"+name+"(in, r)") {
			t.Errorf("%s is declared and never dispatched from CheckLocal or CheckUpstream", name)
		}
	}
}

// TestDescriptionPropertyIsCompared: `flatten` dropped every mapping key called
// `description` as prose. Six schemas on both surfaces have a PROPERTY of that
// name, and the whole subtree went with it -- so retyping it, or withdrawing it,
// was no upstream drift at all.
func TestDescriptionPropertyIsCompared(t *testing.T) {
	upstream := stageUpstream(t)
	editTagSchema(t, filepath.Join(upstream, "openapi-v2.yaml"),
		"        description:\n          type: string\n",
		"        description:\n          type: integer\n")

	assertFinding(t, runFull(t, repoRoot, upstream),
		"schema `Tag`: ~ properties.description.type: string -> integer")
}

// TestAnnotationDescriptionsAreStillIgnored is the other side of that rule: the
// server rewords its prose freely, and reporting that would teach everyone to
// close this job unread.
func TestAnnotationDescriptionsAreStillIgnored(t *testing.T) {
	upstream := stageUpstream(t)
	edit(t, filepath.Join(upstream, "openapi-v2.yaml"),
		"operationId: api_v2_tags_list",
		"        description: Namespace type for merchant/sub-tenant isolation.",
		"        description: Reworded entirely by the server's documentation pass.")

	assertClean(t, runFull(t, repoRoot, upstream))
}

// TestUnusualErrorCodeSpellingIsRead: the extractor captured `[a-z0-9_]+` while
// the unreadable-form detector accepted any string literal, so the two predicates
// disagreed and a code spelled otherwise was neither extracted nor reported.
func TestUnusualErrorCodeSpellingIsRead(t *testing.T) {
	upstream := stageUpstream(t)
	write(t, filepath.Join(upstream, "errors.py"), syntheticErrorsPy+`

class BrandNewError(DomainError):
    code = "Brand_New_Thing"

class DashedError(DomainError):
    code = "rate-limited"
`)

	assertFinding(t, runFull(t, repoRoot, upstream),
		"the server can return `Brand_New_Thing`",
		"the server can return `rate-limited`")
}

// TestUUIDWitnessesDiffer: every `format: uuid` property carried one witness, so
// crossing two of them on a model was invisible -- the same defect the timestamps
// and integers had already been fixed for.
func TestUUIDWitnessesDiffer(t *testing.T) {
	in := load(t, repoRoot, "")
	observed := in.Conformance.Observations["v2 get /tags"]
	seen := map[string]string{}
	for _, property := range []string{"id", "parent_id", "vocabulary_id"} {
		value := string(observed.Sent[property])
		if value == "" {
			t.Errorf("%s was not sent", property)
			continue
		}
		if other, clash := seen[value]; clash {
			t.Errorf("%s and %s share the witness %s; crossing them would change nothing",
				property, other, value)
		}
		seen[value] = property
	}
}

// TestPaginationCursorsAreNotOnlyNull: `next` and `previous` were sent only as
// null, so the type behind the pointer was never exercised -- and `Each`
// terminates on `Pagination.Next == nil`, so a mistyped Next breaks every walk.
func TestPaginationCursorsAreNotOnlyNull(t *testing.T) {
	in := load(t, repoRoot, "")
	observed := in.Conformance.Observations["v2 get /tags"]
	for _, field := range []string{"pagination.next", "pagination.previous"} {
		if value := string(observed.Sent[field]); value == "" || value == "null" {
			t.Errorf("%s was sent as %q; the type behind the pointer is unexercised", field, value)
		}
	}
}

// TestRenamedUnreachableModelIsReported: the five models named by no success
// schema are hard-coded strings, so renaming the Go type switched off the only
// check that can see a swapped pair of tags, in silence.
func TestRenamedUnreachableModelIsReported(t *testing.T) {
	repo := stageRepo(t)
	edit(t, filepath.Join(repo, "pagination.go"), "type Pagination struct {",
		"type Pagination struct {", "type PageInfo struct {")

	assertFinding(t, runLocal(t, repo),
		"`Pagination` is named here as a model no success schema reaches, and this package no longer declares it")
}

// --- the error envelope ---------------------------------------------------------
//
// `ErrorResponse` is referenced by every failure response in both contracts and
// was, until these tests, the one schema nothing here exercised: the stub only
// ever answered 200 or 204, so no drive ever reached parseError. The four tests
// below are the four ways that mattered.

// TestRenamedErrorCodeIsCaught is the reproduction that opened the hole. Renaming
// `error.code` in both vendored contracts reported no drift at all, while in the
// SDK it means parseError never finds a code: every error comes back stamped
// CodeUnexpectedStatus, and IsNotFound, IsConflict and IsValidation each answer
// false for the error they are named after.
func TestRenamedErrorCodeIsCaught(t *testing.T) {
	repo := stageRepo(t)
	spec := filepath.Join(repo, "docs", "openapi-v2.yaml")
	edit(t, spec, "    ErrorResponse:", "            code:\n", "            error_code:\n")
	edit(t, spec, "    ErrorResponse:", "          - code\n", "          - error_code\n")

	assertFinding(t, runLocal(t, repo),
		"`v2`: `ErrorResponse.error.code` is gone from the contract",
		"`ErrorResponse.error.error_code` is new in the contract")
}

// TestRenamedErrorRequestIDIsCaught: the request id is how a caller correlates a
// failure with the server's logs, and it is decoded by name like the code.
func TestRenamedErrorRequestIDIsCaught(t *testing.T) {
	repo := stageRepo(t)
	edit(t, filepath.Join(repo, "docs", "openapi-v2.yaml"), "    ErrorResponse:",
		"            request_id:\n", "            correlation_id:\n")

	assertFinding(t, runLocal(t, repo),
		"`v2`: `ErrorResponse.error.request_id` is gone from the contract")
}

// TestRetypedErrorDetailsIsCaught: `details` carries the field-level validation
// errors, and it is the one envelope property that is not a string. Retyping it
// makes the whole envelope undecodable -- parseError's json.Unmarshal fails, and
// the fallback branch answers with a code no server sent.
func TestRetypedErrorDetailsIsCaught(t *testing.T) {
	repo := stageRepo(t)
	edit(t, filepath.Join(repo, "docs", "openapi-v2.yaml"), "    ErrorResponse:",
		"            details:\n              type: object\n              additionalProperties: true\n",
		"            details:\n              type: string\n")

	assertFinding(t, runLocal(t, repo),
		"`v2`: the envelope carried \"conflict\" in `error.code` and the returned `*APIError` reports \"unexpected_status\"")
}

// TestErrorEnvelopeIsDrivenOnBothSurfaces: v1 and v2 document the envelope
// separately, and only v2 was mutated above. A check that ran on one surface and
// silently skipped the other would pass every test in this section.
func TestErrorEnvelopeIsDrivenOnBothSurfaces(t *testing.T) {
	repo := stageRepo(t)
	for _, surface := range []string{"openapi.yaml", "openapi-v2.yaml"} {
		edit(t, filepath.Join(repo, "docs", surface), "    ErrorResponse:",
			"            message:\n", "            detail:\n")
	}

	assertFinding(t, runLocal(t, repo),
		"`v1`: `ErrorResponse.error.message` is gone from the contract",
		"`v2`: `ErrorResponse.error.message` is gone from the contract")
}

// envelopeObservation is a clean error drive for one surface: what the stub sends
// and what a working client makes of it.
func envelopeObservation(surface string) ErrorObservation {
	return ErrorObservation{
		Sent: map[string]json.RawMessage{
			"code":       json.RawMessage(`"conflict"`),
			"message":    json.RawMessage(`"cd~message"`),
			"request_id": json.RawMessage(`"cd~request_id"`),
			"details":    json.RawMessage(`{"contractdrift":"value"}`),
		},
		Code:      "conflict",
		Message:   "cd~message",
		RequestID: "cd~request_id",
		Details:   map[string]any{"contractdrift": "value"},
		Prefix:    "/api/" + surface,
		Status:    409,
		Helpers:   cleanHelpers(),

		HelperPrefix:       cleanHelperPrefixes(surface),
		FallbackPrefix:     "/api/" + surface,
		FallbackCode:       unexpectedStatusCode,
		FallbackUnexpected: true,

		TruncatedPrefix:     "/api/" + surface,
		TruncatedCode:       unexpectedStatusCode,
		TruncatedUnexpected: true,
	}
}

// cleanHelperPrefixes is every helper's drive having reached this surface.
func cleanHelperPrefixes(surface string) map[string]string {
	out := map[string]string{}
	for _, helper := range semanticHelpers {
		out[helper.Name] = "/api/" + surface
	}
	return out
}

// cleanHelpers is every helper answering for itself and nothing else.
func cleanHelpers() map[string][]string {
	out := map[string][]string{}
	for _, helper := range semanticHelpers {
		out[helper.Name] = []string{helper.Name}
	}
	return out
}

func envelopeReport(t *testing.T, mutate func(o *ErrorObservation)) *Report {
	t.Helper()
	envelope := map[string]ErrorObservation{}
	for _, surface := range surfaces {
		observation := envelopeObservation(surface)
		mutate(&observation)
		envelope[surface] = observation
	}
	sdk, err := LoadSDKPackage(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	r := &Report{}
	checkErrorEnvelope(Inputs{
		SDK:         sdk,
		Conformance: &Conformance{ErrorEnvelope: envelope},
	}, r)
	return r
}

// TestCleanErrorEnvelopeIsSilent anchors the three tests below: without it they
// would all pass against a check that reports everything.
func TestCleanErrorEnvelopeIsSilent(t *testing.T) {
	assertClean(t, envelopeReport(t, func(*ErrorObservation) {}))
}

// TestErrorDriveAttestsItsSurface: the v1 drive is a v1 drive only by intention
// until the wire says so. Changing the surface test in runErrorEnvelope to
// something that never matches left BOTH drives on /api/v2 and nothing said so --
// the same shape as the Config.APIVersion that was never set, recurring in new
// code.
func TestErrorDriveAttestsItsSurface(t *testing.T) {
	assertFinding(t, envelopeReport(t, func(o *ErrorObservation) { o.Prefix = "/api/v2" }),
		"`v1`: the error drive went to \"/api/v2\", not \"/api/v1\"")
}

// TestErrorDriveAttestsItsStatus: `StatusCode: status` in parseError replaced with
// a zero passed clean, and StatusCode is on the exported *APIError -- a caller
// switching on it would read 0 for every error the server sends.
func TestErrorDriveAttestsItsStatus(t *testing.T) {
	assertFinding(t, envelopeReport(t, func(o *ErrorObservation) { o.Status = 0 }),
		"the error drive answers 409 and the returned `*APIError` reports status 0")
}

// TestErrorDriveAssertsTheSemanticHelper: a caller writes IsConflict, not
// `err.(*APIError).Code == "conflict"`. Rewiring IsConflict to CodeValidation
// passed clean while the drive's own comment cited that helper as its reason for
// answering 409 -- an unasserted claim is not a claim.
func TestErrorDriveAssertsTheSemanticHelper(t *testing.T) {
	assertFinding(t, envelopeReport(t, func(o *ErrorObservation) {
		o.Helpers["IsConflict"] = nil
	}), "makes `IsConflict` answer false")
}

// TestDetectsWithdrawnRequiredOnErrorEnvelope: requiredness is not exercised by a
// stub that populates every property, so the OFFLINE half cannot see a withdrawn
// `required` -- docs/development.md says so. The cross-repository half is where it
// is compared, and this pins that, because "covered elsewhere" is a claim like any
// other.
func TestDetectsWithdrawnRequiredOnErrorEnvelope(t *testing.T) {
	upstream := stageUpstream(t)
	edit(t, filepath.Join(upstream, "openapi-v2.yaml"), "    ErrorResponse:",
		"      required:\n      - error\n", "")

	assertFinding(t, runFull(t, repoRoot, upstream),
		"schema `ErrorResponse`: - required: [error]")
}

// TestWitnessesDoNotCollide walks every schema in both contracts and fails on any
// two properties of one schema that would receive the same witness.
//
// Two did. `nameOffset` was `sum%97 + 1`, and `id` and `operation_id` both landed
// on 58, so `AuditLog.id` and `AuditLog.operation_id` carried one uuid and
// crossing them changed nothing compared — the exact defect per-property
// witnesses were introduced to close, surviving inside the fix for it. A fixed
// modulus is a birthday problem, so the property is asserted over the real
// contracts rather than argued about.
func TestWitnessesDoNotCollide(t *testing.T) {
	for surface, spec := range loadSpecs(t) {
		for name := range spec.Schemas {
			documented, ok := spec.SchemaProperties(name)
			if !ok {
				continue
			}
			seen := map[int]string{}
			for _, property := range documented {
				offset := nameOffset(property)
				if other, clash := seen[offset]; clash {
					t.Errorf("%s: schema %s: %q and %q both receive witness offset %d -- crossing those two fields is invisible",
						surface, name, other, property, offset)
				}
				seen[offset] = property
			}
		}
	}
}

// TestCrossedFreeFormObjectsAreCaught: `AuditLog.changes` and `AuditLog.metadata`
// are both free-form, and both used to synthesize the same constant, so a decoder
// that swapped them changed nothing compared. The contract constrains neither, so
// the witness is all there is to tell them apart.
func TestCrossedFreeFormObjectsAreCaught(t *testing.T) {
	specs := loadSpecs(t)
	changes, err := synthesizeSchema(specs["v2"], "AuditLog", 0, witnessPopulated)
	if err != nil {
		t.Fatal(err)
	}
	left, right := changes["changes"], changes["metadata"]
	if reflect.DeepEqual(left, right) {
		t.Fatalf("AuditLog.changes and AuditLog.metadata synthesize the same value %v -- crossing them is invisible", left)
	}
}

// TestCompositeNullIsNotDeferredToNothing pins the one residual difference between
// checkDecodedValues and the checkCompositeResponses it replaced. The deferral for
// a decoded-away `null` hands the question to the null witness — which runs only
// where the operation has an OKModel, and a composite has none. So for a composite
// the deferral used to hand it to nothing at all. Not reachable as the coverage
// file stands, since no composite_body carries a null; pinned anyway, because the
// subsumption claim has to hold for the shapes that file can take.
func TestCompositeNullIsNotDeferredToNothing(t *testing.T) {
	repo := stageRepo(t)
	edit(t, filepath.Join(repo, "docs", "contract-coverage.yaml"),
		"AssignmentService.BulkRemove",
		`composite_body: '{"removed": 4}'`,
		`composite_body: '{"removed": 4, "note": null}'`)

	assertFinding(t, runLocal(t, repo), "decoded it away")
}

// TestCommentedOutServerCodeIsNotRead: a `#`-commented `code = "..."` used to be
// read as a live server code, and a phantom code demands a `Code*` constant that
// must not exist. A weekly job reporting a line the server deleted is the nag
// this gate was told not to become.
func TestCommentedOutServerCodeIsNotRead(t *testing.T) {
	upstream := stageUpstream(t)
	path := filepath.Join(upstream, "errors.py")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	write(t, path, string(raw)+"\n\n# class RetiredError(DomainError):\n#     code = \"retired_last_release\"\n")

	codes, _, err := ServerErrorCodes(path)
	if err != nil {
		t.Fatal(err)
	}
	if codes["retired_last_release"] {
		t.Error("a commented-out code was read as a live one")
	}
}

// TestHashInsideAStringLiteralIsNotAComment is the other side of that rule: the
// stripper must not truncate a value that merely contains a `#`.
func TestHashInsideAStringLiteralIsNotAComment(t *testing.T) {
	upstream := stageUpstream(t)
	path := filepath.Join(upstream, "errors.py")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	write(t, path, string(raw)+"\n\nclass HashError(DomainError):\n    code = \"tag#collision\"\n")

	codes, _, err := ServerErrorCodes(path)
	if err != nil {
		t.Fatal(err)
	}
	if !codes["tag#collision"] {
		t.Errorf("a `#` inside a literal was treated as a comment; got %v", sortedCodes(codes))
	}
}

func sortedCodes(codes map[string]bool) []string {
	out := make([]string, 0, len(codes))
	for code := range codes {
		out = append(out, code)
	}
	sort.Strings(out)
	return out
}

// TestSwappedErrorCodeConstantsAreCaught is the symmetric swap the set
// comparisons structurally cannot see. Exchange the values of CodeNotFound and
// CodeForbidden and the registry still holds exactly the same codes, every one
// still has a constant and every constant still has a code -- while IsNotFound
// answers true for a forbidden and IsForbidden answers true for a missing row.
func TestSwappedErrorCodeConstantsAreCaught(t *testing.T) {
	repo := stageRepo(t)
	path := filepath.Join(repo, "errors.go")
	edit(t, path, "CodeForbidden", `CodeForbidden           = "forbidden"`, `CodeForbidden           = "not_found"`)
	edit(t, path, "CodeNotFound", `CodeNotFound            = "not_found"`, `CodeNotFound            = "forbidden"`)

	assertFinding(t, runLocal(t, repo),
		"`CodeForbidden` carries `not_found`, and its name spells `forbidden`",
		"`CodeNotFound` carries `forbidden`, and its name spells `not_found`")
}

// TestAbbreviatedConstantIsHeldToItsRecordedCode: the two recorded abbreviations
// are exemptions from the naming rule, not from the comparison. A recorded
// constant that starts carrying a different code is still a swap.
func TestAbbreviatedConstantIsHeldToItsRecordedCode(t *testing.T) {
	repo := stageRepo(t)
	edit(t, filepath.Join(repo, "errors.go"), "CodeValidation",
		`CodeValidation          = "validation_error"`, `CodeValidation          = "conflict"`)

	assertFinding(t, runLocal(t, repo),
		"`CodeValidation` is recorded as abbreviating `validation_error` and now carries `conflict`")
}

// TestStaleAbbreviationRowIsReported: a row whose constant is renamed or deleted
// would otherwise sit in the file exempting nothing.
func TestStaleAbbreviationRowIsReported(t *testing.T) {
	repo := stageRepo(t)
	edit(t, filepath.Join(repo, "docs", "contract-coverage.yaml"), "abbreviated_error_constants",
		"  - constant: CodeAuthRequired\n", "  - constant: CodeRetired\n")

	assertFinding(t, runLocal(t, repo),
		"`CodeRetired` is recorded in abbreviated_error_constants and errors.go declares no such constant")
}

// TestVacuousAbbreviationRowIsRefused: a row for a constant whose name already
// spells its code exempts nothing, so the loader refuses it rather than letting
// it accumulate.
func TestVacuousAbbreviationRowIsRefused(t *testing.T) {
	repo := stageRepo(t)
	edit(t, filepath.Join(repo, "docs", "contract-coverage.yaml"), "abbreviated_error_constants",
		"  - constant: CodeAuthRequired\n    code: authentication_required\n",
		"  - constant: CodeNotFound\n    code: not_found\n")

	if _, err := LoadCoverage(filepath.Join(repo, "docs", "contract-coverage.yaml")); err == nil {
		t.Fatal("a row that exempts nothing was accepted")
	}
}

// TestEscapedQuoteInAServerCodeIsReportedNotGuessed: the capture stops at the
// first quote, so `code = "tag\"#collision"` yielded `tag\` -- a code the server
// does not have, demanding a constant that must not exist. Reported as an
// unreadable spelling and NOT entered into the comparison.
func TestEscapedQuoteInAServerCodeIsReportedNotGuessed(t *testing.T) {
	upstream := stageUpstream(t)
	path := filepath.Join(upstream, "errors.py")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	write(t, path, string(raw)+"\n\nclass EscapedHashError(DomainError):\n    code = \"tag\\\"#collision\"\n")

	codes, unreadable, err := ServerErrorCodes(path)
	if err != nil {
		t.Fatal(err)
	}
	for code := range codes {
		if strings.Contains(code, `\`) {
			t.Errorf("a truncated code %q was read as a real one", code)
		}
	}
	if len(unreadable) == 0 {
		t.Error("the escaped-quote spelling was neither read nor reported -- it vanished")
	}
}

// TestTwoConstantsForOneCodeAreReported: ErrorCodes is keyed by value, so two
// constants carrying one code collapse to a single entry and which name survives
// depends on map order. Every set comparison stays green through it -- the
// registry has every code, every code has a constant -- while one of the two is
// dead weight a caller may be switching on.
func TestTwoConstantsForOneCodeAreReported(t *testing.T) {
	repo := stageRepo(t)
	edit(t, filepath.Join(repo, "errors.go"), "CodeConflict",
		`CodeConflict            = "conflict"`,
		"CodeConflict            = \"conflict\"\n\tCodeClash               = \"conflict\"")

	assertFinding(t, runLocal(t, repo),
		"`conflict` is carried by `CodeClash` and `CodeConflict`")
}

// TestEscapedPythonCodeIsReportedNotDropped: `code = "brand\x5fnew"` is a VALID
// Python literal for `brand_new`. The extractor refuses the backslash, and
// isPyStringLiteral calls the same expression perfectly readable — no embedded
// quote — so before this the code was neither read nor reported. It vanished.
func TestEscapedPythonCodeIsReportedNotDropped(t *testing.T) {
	upstream := stageUpstream(t)
	path := filepath.Join(upstream, "errors.py")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	write(t, path, string(raw)+"\n\nclass EscapedUnderscoreError(DomainError):\n    code = \"brand\\x5fnew\"\n")

	codes, unreadable, err := ServerErrorCodes(path)
	if err != nil {
		t.Fatal(err)
	}
	if codes[`brand\x5fnew`] {
		t.Error("an unresolved escape was read as a literal code")
	}
	found := false
	for _, form := range unreadable {
		if strings.Contains(form, `brand\x5fnew`) {
			found = true
		}
	}
	if !found {
		t.Errorf("the escaped spelling was neither read nor reported; unreadable=%v", unreadable)
	}
}

// TestDuplicateAbbreviationRowIsRefused: the rows are read into a map, so two for
// one constant means the later silently wins — and the loser can be the row that
// was actually true.
func TestDuplicateAbbreviationRowIsRefused(t *testing.T) {
	repo := stageRepo(t)
	edit(t, filepath.Join(repo, "docs", "contract-coverage.yaml"), "abbreviated_error_constants",
		"  - constant: CodeValidation\n",
		"  - constant: CodeValidation\n    code: authentication_required\n    reason: A duplicate row.\n  - constant: CodeValidation\n")

	if _, err := LoadCoverage(filepath.Join(repo, "docs", "contract-coverage.yaml")); err == nil {
		t.Fatal("two rows for one constant were accepted")
	}
}

// --- the Python extractor's one predicate ----------------------------------------
//
// Three review rounds running found a code that fell between a narrow reader and
// a wide unreadable-detector that had to agree with it. They now share one
// predicate, and these pin the four shapes that found the gap.

// pyRegistry runs the extractor over the synthetic registry plus one addition.
func pyRegistry(t *testing.T, extra string) (map[string]bool, []string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "errors.py")
	write(t, path, syntheticErrorsPy+extra)
	codes, unreadable, err := ServerErrorCodes(path)
	if err != nil {
		t.Fatal(err)
	}
	return codes, unreadable
}

// TestSpacedCallIsRead: `error_response ("spaced_call", ...)` is valid Python and
// was matched by neither the old reader nor the old detector, so a code written
// that way disappeared while the count floor stayed healthy.
func TestSpacedCallIsRead(t *testing.T) {
	codes, unreadable := pyRegistry(t, "\ndef handler(exc):\n    return error_response (\"spaced_call\", \"x\", {}, None, 400)\n")
	if !codes["spaced_call"] {
		t.Errorf("a code written with a space before the paren was not read; unreadable=%v", unreadable)
	}
}

// TestAdjacentLiteralsAreReportedNotHalfRead: `"joined" "_code"` is ONE Python
// string, `joined_code`. Reading the first half invents a code the server does
// not have.
func TestAdjacentLiteralsAreReportedNotHalfRead(t *testing.T) {
	codes, unreadable := pyRegistry(t, "\nclass JoinedError(DomainError):\n    code = \"joined\" \"_code\"\n")
	if codes["joined"] {
		t.Error("half of a concatenated literal was read as a code")
	}
	if len(unreadable) == 0 {
		t.Error("the concatenation was neither read nor reported")
	}
}

// TestAliasesDoNotFloodTheUnreadableList: `code = exc.code` is the DRF handler
// reading a code already extracted from its class. One line per variable spelling
// is a flood, not a diagnostic, and a scheduled job nobody reads catches nothing.
func TestAliasesDoNotFloodTheUnreadableList(t *testing.T) {
	_, unreadable := pyRegistry(t, "\ndef handler(exc):\n    code = exc.code\n    code = error.code\n    return code\n")
	if len(unreadable) != 0 {
		t.Errorf("ordinary aliases were reported as unreadable: %v", unreadable)
	}
}

// TestUnrecognisedAliasIsReported is the other half of that rule. The suppression
// used to be `<any identifier>.code`, which silently covered `response.code` --
// an object that is not a DomainError, carrying a code this reader cannot see, so
// an unreadable call was treated as one already accounted for. The list is
// explicit now, and a spelling not on it is reported.
func TestUnrecognisedAliasIsReported(t *testing.T) {
	_, unreadable := pyRegistry(t, "\ndef handler(response):\n    return error_response(response.code, \"x\", {}, None, 400)\n")
	if len(unreadable) == 0 {
		t.Error("`response.code` was suppressed as if it were a DomainError's own code")
	}
}

// TestEqualityIsNotAnAssignment: `if code == "first":` matched the first `=` of
// `==`, so three ordinary comparisons in a handler produced three findings.
func TestEqualityIsNotAnAssignment(t *testing.T) {
	_, unreadable := pyRegistry(t, "\ndef handler(code):\n    if code == \"first\":\n        pass\n    if code == \"second\":\n        pass\n    return code\n")
	if len(unreadable) != 0 {
		t.Errorf("equality comparisons were read as assignments: %v", unreadable)
	}
}

// TestContinuedCallIsRead: a backslash line continuation before the paren is
// valid Python that matched nothing at all, so the code vanished.
func TestContinuedCallIsRead(t *testing.T) {
	codes, unreadable := pyRegistry(t, "\ndef handler(exc):\n    return error_response \\\n        (\"continued_call\", \"x\", {}, None, 400)\n")
	if !codes["continued_call"] {
		t.Errorf("a continued call was not read; unreadable=%v", unreadable)
	}
}

// TestRealCallOnALineMentioningDefIsRead: the definition filter matched "def "
// ANYWHERE in the text before the call, so a real call on any line that happened
// to mention it was suppressed.
func TestRealCallOnALineMentioningDefIsRead(t *testing.T) {
	codes, unreadable := pyRegistry(t, "\ndef handler(exc):\n    if \"def \" in exc.message: return error_response(\"mentions_def\", \"x\", {}, None, 400)\n")
	if !codes["mentions_def"] {
		t.Errorf("a real call was suppressed as a definition; unreadable=%v", unreadable)
	}
}

// TestTheRealServerRegistryReadsCleanly is the anti-nag guard for all of the
// above: the extractor must read the actual file without inventing work. A
// predicate tightened until it is safe is worthless if it reports every line.
func TestTheRealServerRegistryReadsCleanly(t *testing.T) {
	codes, unreadable := pyRegistry(t, "")
	if len(codes) < minServerErrorCodes {
		t.Errorf("read only %d codes from the synthetic registry", len(codes))
	}
	if len(unreadable) != 0 {
		t.Errorf("an ordinary registry produced %d unreadable findings: %v", len(unreadable), unreadable)
	}
}

// TestAttributeAssignmentIsNotACode: `\bcode` matched `response.code = "x"` --
// an attribute on an unrelated object, read as a server error code that does not
// exist. A phantom code demands a Code* constant that must not exist.
func TestAttributeAssignmentIsNotACode(t *testing.T) {
	codes, _ := pyRegistry(t, "\ndef handler(response):\n    response.code = \"phantom_attribute\"\n    return response\n")
	if codes["phantom_attribute"] {
		t.Error("an attribute assignment on an unrelated object was read as a server code")
	}
}

// TestOneLineClassBodyStillReads is the other side of that scoping: a code that
// follows the colon of a one-line class body is still the first thing in its
// statement, and tightening the pattern must not lose it.
func TestOneLineClassBodyStillReads(t *testing.T) {
	codes, unreadable := pyRegistry(t, "\nclass InlineError(DomainError): code = \"inline_oneliner\"\n")
	if !codes["inline_oneliner"] {
		t.Errorf("a one-line class body was lost; unreadable=%v", unreadable)
	}
}

// TestSuppressedAliasesAreExactlyThese pins the extractor's one remaining silent
// spot, so that it stays one and stays deliberate.
//
// `error_response(code, ...)` has to be suppressed: the server's handler assigns
// `code = exc.code` and passes it, and reporting that every run is a flood. The
// cost is that a `code` this reader cannot follow to a literal -- a loop
// variable, say -- is suppressed with it, and a code introduced that way is
// neither read nor reported. Distinguishing them needs dataflow, which a regexp
// over Python does not have. It is bounded rather than closed: the list is
// explicit, and this test fails if it grows.
func TestSuppressedAliasesAreExactlyThese(t *testing.T) {
	want := []string{"code", "error.code", "exc.code"}
	var got []string
	for spelling := range pyResolvedCodes {
		got = append(got, spelling)
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the suppressed spellings are %v, not %v -- each one is a place a code can go unseen, so growing the list is a decision, not a detail", got, want)
	}
}

// TestEveryHelperIsDriven: the helper table is a list of claims, and a claim
// nobody makes is the one that goes wrong. errors.go declares sixteen exported
// Is* predicates; if it grows a seventeenth, this fails until the table covers
// it, so a new helper cannot arrive unexercised the way fifteen of them did.
func TestEveryHelperIsDriven(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot, "errors.go"))
	if err != nil {
		t.Fatal(err)
	}
	// `\w+ error` and not `err error`: the parameter's NAME is the author's choice,
	// and `func IsConflictAlias(cause error) bool` was an exported helper this scan
	// did not see at all.
	declared := regexp.MustCompile(`(?m)^func (Is[A-Za-z]+)\(\w+ error\) bool`).FindAllStringSubmatch(string(raw), -1)
	if len(declared) < 10 {
		t.Fatalf("found only %d Is* helpers; the pattern stopped matching", len(declared))
	}
	driven := map[string]bool{}
	for _, helper := range semanticHelpers {
		driven[helper.Name] = true
	}
	for _, match := range declared {
		if !driven[match[1]] {
			t.Errorf("errors.go declares %s and semanticHelpers does not drive it -- a helper nobody exercises is one that can be rewired in silence", match[1])
		}
		delete(driven, match[1])
	}
	// And the other way, so the two lists cannot drift apart in either direction.
	for name := range driven {
		t.Errorf("semanticHelpers drives %s and errors.go does not declare it -- the two lists have come apart", name)
	}
}

// TestRewiredHelperIsCaught is round 15's reproduction: IsNotFound pointed at
// CodeForbidden. Every set stays intact through it, the constants are all still
// correctly named, and a caller asking "was that a 404?" gets the wrong answer.
func TestRewiredHelperIsCaught(t *testing.T) {
	repo := stageRepo(t)
	edit(t, filepath.Join(repo, "errors.go"), "func IsNotFound",
		"func IsNotFound(err error) bool { return hasCode(err, CodeNotFound) }",
		"func IsNotFound(err error) bool { return hasCode(err, CodeForbidden) }")

	// The conformance run drives the SDK compiled into this binary, so a staged
	// source edit cannot change what runs. Drive the table directly instead: this
	// is what checkErrorEnvelope compares, and the rewiring is what it must see.
	answered := map[string][]string{}
	for _, helper := range semanticHelpers {
		if helper.Name == "IsNotFound" {
			answered[helper.Name] = []string{"IsForbidden"}
			continue
		}
		answered[helper.Name] = []string{helper.Name}
	}
	sdk, err := LoadSDKPackage(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	r := &Report{}
	checkErrorEnvelope(Inputs{SDK: sdk, Conformance: &Conformance{ErrorEnvelope: map[string]ErrorObservation{
		"v1": withHelpers(envelopeObservation("v1"), answered),
		"v2": withHelpers(envelopeObservation("v2"), answered),
	}}}, r)

	assertFinding(t, r, "makes `IsForbidden` answer true, and only `IsNotFound` should")
}

func withHelpers(o ErrorObservation, helpers map[string][]string) ErrorObservation {
	o.Helpers = helpers
	return o
}

// TestSemicolonClassBodyIsRead: a statement can begin after a semicolon as well
// as after a colon. `class E(DomainError): description = "x"; code = "lost"` is
// valid Python whose code matched nothing at all.
func TestSemicolonClassBodyIsRead(t *testing.T) {
	codes, unreadable := pyRegistry(t, "\nclass SemicolonError(DomainError): description = \"x\"; code = \"after_semicolon\"\n")
	if !codes["after_semicolon"] {
		t.Errorf("a code after a semicolon was lost; unreadable=%v", unreadable)
	}
}

// TestParenthesizedCallIsRead: `(error_response)("x", ...)` is the same call.
func TestParenthesizedCallIsRead(t *testing.T) {
	codes, unreadable := pyRegistry(t, "\ndef handler(exc):\n    return (error_response)(\"parenthesized\", \"x\", {}, None, 400)\n")
	if !codes["parenthesized"] {
		t.Errorf("a parenthesized call was lost; unreadable=%v", unreadable)
	}
}

// TestAsyncDefinitionIsNotACall: matching only "def " reported the server's own
// signature as an unreadable code the moment it became async.
func TestAsyncDefinitionIsNotACall(t *testing.T) {
	_, unreadable := pyRegistry(t, "\nasync def error_response(code: str, message: str):\n    return None\n")
	if len(unreadable) != 0 {
		t.Errorf("an async definition was reported as an unreadable call: %v", unreadable)
	}
}

// TestSimilarlyNamedFunctionIsNotACall: `custom_error_response` ends in this
// function's name, and the underscore is a word character, so only a word
// boundary tells them apart.
func TestSimilarlyNamedFunctionIsNotACall(t *testing.T) {
	_, unreadable := pyRegistry(t, "\ndef custom_error_response(code: str):\n    return code\n")
	if len(unreadable) != 0 {
		t.Errorf("an unrelated function was reported as an unreadable call: %v", unreadable)
	}
}

// TestAnExtraHelperAnsweringIsReported pins the "AND NOTHING ELSE" half, which no
// test covered: every case exercised a helper that answered FALSE, so loosening
// the comparison from `len(answered) == 1` to `len(answered) > 0` was clean, and
// a helper answering true for its own code AND someone else's went unreported.
func TestAnExtraHelperAnsweringIsReported(t *testing.T) {
	assertFinding(t, envelopeReport(t, func(o *ErrorObservation) {
		o.Helpers["IsTenantMismatch"] = []string{"IsTenantMismatch", "IsApplicationMismatch"}
	}), "answer true, and only `IsTenantMismatch` should")
}

// TestUndrivenHelperDriveIsReported: a drive that returns a fabricated error
// without issuing a request answers every question correctly about something no
// client produced.
func TestUndrivenHelperDriveIsReported(t *testing.T) {
	assertFinding(t, envelopeReport(t, func(o *ErrorObservation) {
		o.HelperPrefix["IsConflict"] = ""
	}), "the drive for `IsConflict` went to")
}

// TestEnvelopelessFallbackIsAsserted: CodeUnexpectedStatus is manufactured by the
// branch of parseError that no envelope reaches, and driving IsUnexpectedStatus
// through a synthesized envelope carrying `unexpected_status` proved it for a
// response no server sends. A status-to-code mapping bolted into that branch --
// the one thing the constant exists to forbid -- was clean.
func TestEnvelopelessFallbackIsAsserted(t *testing.T) {
	assertFinding(t, envelopeReport(t, func(o *ErrorObservation) {
		o.FallbackCode = "conflict"
		o.FallbackUnexpected = false
	}), "a 502 carrying no envelope produced code `conflict`")
}

// TestHelperTablePairingIsDerived: swapping the Code AND the Is of two rows
// together redefines what the table asserts, so two correspondingly broken
// helpers agreed with it and passed. The constant carrying a code has to be the
// one its helper is named after, and that is derived from errors.go rather than
// read off the table.
func TestHelperTablePairingIsDerived(t *testing.T) {
	in := load(t, stageRepo(t), "")
	saved := semanticHelpers[0].Code
	semanticHelpers[0].Code = octonomy.CodeConflict
	defer func() { semanticHelpers[0].Code = saved }()

	r := &Report{}
	checkErrorEnvelope(in, r)
	assertFinding(t, r, "the helper table pairs `"+semanticHelpers[0].Name+"` with `conflict`")
}

// TestUnreadableBodyFallbackIsAsserted: a body that starts arriving and stops
// reaches unreadableBodyError, not parseError, so a code invented there is
// invisible to the envelope-less drive. An unread body cannot have carried a
// code, and guessing one from the status is what CodeUnexpectedStatus forbids.
func TestUnreadableBodyFallbackIsAsserted(t *testing.T) {
	assertFinding(t, envelopeReport(t, func(o *ErrorObservation) {
		o.TruncatedCode = "conflict"
		o.TruncatedUnexpected = false
	}), "whose body could not be read produced code `conflict`")
}
