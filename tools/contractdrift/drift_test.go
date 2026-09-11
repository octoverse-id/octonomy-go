package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		"documents the query parameter `scope_hint`",
		"`TagService.Resolve` never sends it")
}

// TestAllowlistedUnsentQueryParameterPasses is that allowlist working.
func TestAllowlistedUnsentQueryParameterPasses(t *testing.T) {
	repo := stageRepo(t)
	edit(t, filepath.Join(repo, "docs", "openapi-v2.yaml"),
		"operationId: api_v2_tag_resolution_retrieve",
		"      parameters:\n",
		"      parameters:\n      - in: query\n        name: scope_hint\n        schema:\n          type: string\n")
	edit(t, filepath.Join(repo, "docs", "contract-coverage.yaml"),
		"\nunsent_query_parameters:\n",
		"\nunsent_query_parameters:\n",
		"\nunsent_query_parameters:\n"+`  - path: /tag-resolution
    method: get
    name: scope_hint
    reason: server-side ranking hint, not a client concern
`)

	assertClean(t, runLocal(t, repo))
}

// TestStaleUnsentAllowlistFails keeps the allowlist from outliving its parameter.
func TestStaleUnsentAllowlistFails(t *testing.T) {
	repo := stageRepo(t)
	edit(t, filepath.Join(repo, "docs", "contract-coverage.yaml"),
		"\nunsent_query_parameters:\n",
		"\nunsent_query_parameters:\n",
		"\nunsent_query_parameters:\n"+`  - path: /tag-resolution
    method: get
    name: nothing_documents_this
    reason: left behind by an earlier refresh
`)

	assertFinding(t, runLocal(t, repo),
		"`get /tag-resolution nothing_documents_this` is listed under unsent_query_parameters",
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

// TestQueryParametersResolveThroughConstants covers the transport's own spelling:
// it sets the scope parameters through constants rather than inline literals, and
// an extractor that only matched the inline form would report them as never sent.
func TestQueryParametersResolveThroughConstants(t *testing.T) {
	sdk, err := LoadSDKPackage(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	sent, err := sdk.QueryParams("TagService.Resolve")
	if err != nil {
		t.Fatal(err)
	}
	// `scope` is set through a constant declared in transport.go and used in
	// resolution.go; `application_id` and `include_global` are set by the
	// transport itself, for every call.
	for _, name := range []string{"scope", "slug", "type", "application_id", "include_global"} {
		if !sent[name] {
			t.Errorf("TagService.Resolve sends %q and the analysis did not find it", name)
		}
	}
	// And the point of doing this per operation: the resolution route does NOT
	// page, even though pagination.go sets limit and offset for the list routes.
	for _, name := range []string{"limit", "offset"} {
		if sent[name] {
			t.Errorf("TagService.Resolve does not send %q; the analysis leaked it from another route", name)
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

// --- the SDK side, which is where the first review found the hole ---------------
//
// The gate shipped its first draft comparing YAML with YAML and asserting only
// that a function with the right name existed. An outside review reproduced the
// consequence exactly: add a property to a vendored schema, change no Go, and the
// blocking gate was green -- so a contract refresh could land while the SDK still
// implemented the old contract, which is the one thing this gate is for. The
// tests below are that reproduction, kept.

// TestDetectsFieldAddedToVendoredSchema is the review's own repro: the contract
// grows a field, the Go model does not, and the OFFLINE gate says so. The
// upstream comparison cannot: once the YAML is refreshed, both sides of it agree.
func TestDetectsFieldAddedToVendoredSchema(t *testing.T) {
	repo := stageRepo(t)
	for _, spec := range []string{"openapi-v2.yaml", "openapi.yaml"} {
		edit(t, filepath.Join(repo, "docs", spec), "\n    Tag:\n",
			"      properties:\n",
			"      properties:\n        colour:\n          type: string\n")
	}

	assertFinding(t, runLocal(t, repo),
		"schema `Tag` documents `colour` and the Go model `Tag` has no field for it")
}

// TestDetectsFieldWithdrawnFromVendoredSchema is the other direction: the SDK
// keeps decoding something the contract no longer documents.
func TestDetectsFieldWithdrawnFromVendoredSchema(t *testing.T) {
	repo := stageRepo(t)
	for _, spec := range []string{"openapi-v2.yaml", "openapi.yaml"} {
		edit(t, filepath.Join(repo, "docs", spec), "\n    Tag:\n",
			"        usage_count:\n", "        usage_count_renamed:\n")
	}

	assertFinding(t, runLocal(t, repo),
		"the Go model `Tag` decodes `usage_count`, which schema `Tag` does not document")
}

// TestResponseModelCheckIsNotVacuous guards the check above from quietly
// comparing nothing -- an empty property set on either side would make every
// model "match".
func TestResponseModelCheckIsNotVacuous(t *testing.T) {
	in := load(t, repoRoot, "")
	spec := in.Vendored["v2"]

	compared := 0
	for _, model := range []string{"Tag", "Vocabulary", "TagAlias", "Assignment", "AuditLog", "ResourceTag", "TagResource", "TagResolution"} {
		documented, ok := spec.SchemaProperties(model)
		if !ok || len(documented) == 0 {
			t.Errorf("schema %s documents no properties", model)
			continue
		}
		decoded, ok := in.SDK.JSONFields(model)
		if !ok || len(decoded) == 0 {
			t.Errorf("the Go model %s decodes no JSON fields", model)
			continue
		}
		compared += len(documented)
	}
	if compared < 50 {
		t.Errorf("only %d properties take part in the model comparison; it is close to vacuous", compared)
	}
}

// TestQueryParameterCheckIsPerOperation is the review's second repro. `limit` is
// set by pagination.go for every list route, so a check that asked whether the
// NAME appeared anywhere in the package called it implemented on /tag-resolution
// too -- which does not page at all.
func TestQueryParameterCheckIsPerOperation(t *testing.T) {
	repo := stageRepo(t)
	for _, spec := range []string{"openapi-v2.yaml", "openapi.yaml"} {
		edit(t, filepath.Join(repo, "docs", spec),
			"operationId: api_v"+surfaceDigit(spec)+"_tag_resolution_retrieve",
			"      parameters:\n",
			"      parameters:\n      - in: query\n        name: limit\n        schema:\n          type: integer\n")
	}

	assertFinding(t, runLocal(t, repo),
		"`get /tag-resolution` documents the query parameter `limit` and `TagService.Resolve` never sends it")
}

// surfaceDigit picks the operationId prefix for a vendored spec file.
func surfaceDigit(spec string) string {
	if strings.Contains(spec, "-v2") {
		return "2"
	}
	return "1"
}

// TestUnsentAllowlistDoesNotLeakAcrossOperations pins the reason that allowlist
// is keyed by operation. `q` is recorded as unsent on /vocabularies; that must
// not excuse it anywhere else.
func TestUnsentAllowlistDoesNotLeakAcrossOperations(t *testing.T) {
	repo := stageRepo(t)
	for _, spec := range []string{"openapi-v2.yaml", "openapi.yaml"} {
		edit(t, filepath.Join(repo, "docs", spec),
			"operationId: api_v"+surfaceDigit(spec)+"_tag_resolution_retrieve",
			"      parameters:\n",
			"      parameters:\n      - in: query\n        name: q\n        schema:\n          type: string\n")
	}

	assertFinding(t, runLocal(t, repo),
		"`get /tag-resolution` documents the query parameter `q` and `TagService.Resolve` never sends it")
}

// TestRoutesAreDerivedFromTheCode is what turns the inventory from a claim into
// an assertion: the row says `get /tags`, and the method really does issue a GET
// to /tags through doList.
func TestRoutesAreDerivedFromTheCode(t *testing.T) {
	sdk, err := LoadSDKPackage(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		symbol string
		want   Route
	}{
		{"TagService.List", Route{Method: "get", Path: "/tags", Helper: "doList", Model: "Tag"}},
		{"TagService.Get", Route{Method: "get", Path: "/tags/{}", Helper: "doData", Model: "Tag"}},
		{"TagService.Delete", Route{Method: "delete", Path: "/tags/{}", Helper: "do"}},
		{"ResourceService.ListTags", Route{Method: "get", Path: "/resources/{}/{}/tags", Helper: "doList", Model: "ResourceTag"}},
		// The health probes reach the transport through a shared helper that takes
		// the path as a parameter and fixes the method itself; both indirections
		// have to resolve or the row cannot be checked at all.
		{"HealthService.Live", Route{Method: "get", Path: "/health/live", Helper: "doUnversioned"}},
	} {
		got, err := sdk.Route(tc.symbol)
		if err != nil {
			t.Errorf("%s: %v", tc.symbol, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: got %+v, want %+v", tc.symbol, got, tc.want)
		}
	}
}

// TestRepointedMethodFailsInventory: the named method exists and does something
// else. The name-only check this replaced agreed the operation was covered.
func TestRepointedMethodFailsInventory(t *testing.T) {
	repo := stageRepo(t)
	edit(t, filepath.Join(repo, "tags.go"),
		"func (s *TagService) Get(",
		`"/tags/"+url.PathEscape(id), nil, nil, opts...)`,
		`"/vocabularies/"+url.PathEscape(id), nil, nil, opts...)`)

	assertFinding(t, runLocal(t, repo),
		"`TagService.Get` requests `/vocabularies/{}`, not `/tags/{}`")
}

// TestWrongActualResponseFails ties the recorded envelope to the transport helper
// the method actually calls, rather than to whatever someone typed.
func TestWrongActualResponseFails(t *testing.T) {
	repo := stageRepo(t)
	edit(t, filepath.Join(repo, "docs", "contract-coverage.yaml"),
		"  - path: /tags\n    method: get\n",
		"    actual_response: list-envelope\n",
		"    actual_response: data-envelope\n")

	assertFinding(t, runLocal(t, repo),
		"`TagService.List` decodes through doList, which yields `list-envelope`, but the row records `data-envelope`")
}

// TestTypedConstantsResolve covers the spelling that broke the regexp this
// replaced: `scopeParam string = "scope"` is the same declaration to a reader and
// must be the same to the gate.
func TestTypedConstantsResolve(t *testing.T) {
	repo := stageRepo(t)
	edit(t, filepath.Join(repo, "transport.go"),
		"\tscopeParam ",
		`scopeParam            = "scope"`,
		`scopeParam     string = "scope"`)

	assertClean(t, runLocal(t, repo))
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

// TestReorderedSequenceIsNotDrift keeps a generator's incidental ordering out of
// the report. A scheduled job that goes red for a reshuffle is a job people learn
// to close unread.
func TestReorderedSequenceIsNotDrift(t *testing.T) {
	upstream := stageUpstream(t)
	edit(t, filepath.Join(upstream, "openapi-v2.yaml"),
		"operationId: api_v2_tags_list",
		"      - in: query\n        name: is_active\n        schema:\n          type: boolean\n      - in: query\n        name: limit\n",
		"      - in: query\n        name: limit\n")
	edit(t, filepath.Join(upstream, "openapi-v2.yaml"),
		"operationId: api_v2_tags_list",
		"      tags:\n",
		"      - in: query\n        name: is_active\n        schema:\n          type: boolean\n      tags:\n")

	assertClean(t, runFull(t, repoRoot, upstream))
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

// TestAmbiguousRouteFailsLoudly: a method that issues two different requests has
// no single route, and answering the inventory with whichever the walk reached
// first would be true only sometimes.
func TestAmbiguousRouteFailsLoudly(t *testing.T) {
	repo := stageRepo(t)
	edit(t, filepath.Join(repo, "tags.go"),
		"func (s *TagService) Get(",
		`return doData[Tag](ctx, s.client, http.MethodGet, "/tags/"+url.PathEscape(id), nil, nil, opts...)`,
		`if id == "" {
		return doData[Tag](ctx, s.client, http.MethodPost, "/vocabularies", nil, nil, opts...)
	}
	return doData[Tag](ctx, s.client, http.MethodGet, "/tags/"+url.PathEscape(id), nil, nil, opts...)`)

	assertFinding(t, runLocal(t, repo), "issues more than one request")
}

// TestUntaggedExportedFieldIsCompared covers what encoding/json really does with
// a field carrying no tag: it decodes under the Go name. Skipping it would hide a
// field the SDK genuinely decodes from the check that exists to notice fields.
func TestUntaggedExportedFieldIsCompared(t *testing.T) {
	repo := stageRepo(t)
	edit(t, filepath.Join(repo, "tags.go"),
		"type Tag struct {",
		"type Tag struct {\n",
		"type Tag struct {\n\tColour string\n")

	assertFinding(t, runLocal(t, repo),
		"the Go model `Tag` decodes `Colour`, which schema `Tag` does not document")
}

// TestPathHelpersAreRenderedFromTheirBody: the /resources/{}/{} prefix is read out
// of resourcePath rather than hardcoded, so a change to the helper moves the
// derived route with it instead of leaving the gate agreeing with a stale string.
func TestPathHelpersAreRenderedFromTheirBody(t *testing.T) {
	repo := stageRepo(t)
	edit(t, filepath.Join(repo, "resources.go"),
		"func resourcePath(",
		`return "/resources/" + url.PathEscape(resourceType)`,
		`return "/things/" + url.PathEscape(resourceType)`)

	assertFinding(t, runLocal(t, repo),
		"requests `/things/{}/{}/tags`, not `/resources/{}/{}/tags`")
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
		{"allowlist for a missing operation", valid + "unsent_query_parameters:\n  - path: /nowhere\n    method: get\n    name: q\n    reason: x\n", "is not an operation in this file"},
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
