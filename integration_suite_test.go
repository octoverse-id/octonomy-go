//go:build integration
// +build integration

// The isolation suite (#97), run against a real Octonomy container: main's
// readProbes table, its matrix runner and the two tests that drive it, ported
// from main's integration_suite_test.go at 5e40964 and rewritten for this line.
// Every mention of main in this file means main at that commit. The rest of
// main's suite is not ported; docs/compat-test-disposition.md says why, test by
// test.
//
// WHAT THIS IS FOR, AND HOW IT DIFFERS FROM ITS NEIGHBOURS.
//
// The unit suites drive an httptest server whose fixtures this repository
// writes, so they assert the client against this SDK's own beliefs.
// integration_test.go checks the SHAPES against a real server -- the envelopes,
// the composite payloads, the namespace fields the server populates. This file
// asserts what no fixture can settle, because it is a property of the server's
// authorization and filtering rather than of any payload:
//
//	namespace isolation  -- a merchant-A client never sees a merchant-B row, on
//	                        any authenticated read endpoint this SDK exposes
//	include_global       -- fail-closed: the opt-in widens what is ASKED for, and
//	                        a token with no global authority still sees no
//	                        global rows
//
// That is the property /api/v2 exists to provide, and this line has offered
// /api/v2 since #91 with nothing asserting it.
//
// Run it against the container harness:
//
//	make dev-server
//	make test-integration
//
// With OCTONOMY_TEST_BASE_URL unset every test here skips, so `go test ./...`
// stays hermetic, fast, and offline. CI runs it in the required go1.13 smoke
// job with OCTONOMY_SMOKE_REQUIRED=1, which turns that skip into a failure.
package octonomy_test

import (
	"context"
	"testing"

	octonomy "github.com/octoverse-id/octonomy-go"
)

// readProbe is one read endpoint of this SDK, expressed as a single question:
// reading in namespace readNS, can this client see the row named by want?
//
// Phrasing every endpoint as the same question is what makes the isolation and
// include_global matrices exhaustive rather than anecdotal: one entry in this
// table buys coverage in both.
//
// Whether the row came back is the headline, but it is not the whole assertion.
// The endpoints decline an out-of-scope row differently -- some 404, some answer
// an empty page, resolution answers 400 -- and each probe declares WHICH,
// because "it errored" is not evidence of isolation when the SDK turns every
// non-2xx into an *APIError by design.
type readProbe struct {
	name string

	// filtered is what THIS endpoint does when the request is authorized but the
	// row is outside the caller's namespace. It is per-probe rather than a
	// shared allowlist because the three answers are not interchangeable, and
	// accepting any of them everywhere re-opens the hole a shared "did it
	// error?" check had: a list route that started answering 400, or an object
	// lookup that started answering 409, would read as correct filtering.
	filtered filteredOutcome

	// find answers the probe's question. extra carries per-run options -- today
	// WithIncludeGlobal -- on top of the namespace and application pair every
	// read in this suite sends.
	find func(ctx context.Context, c *octonomy.Client, readNS string, want namespaceFixture, extra ...octonomy.RequestOption) (bool, error)
}

// filteredOutcome is how one endpoint declines to show a row that is out of the
// caller's namespace. Read off the server route by route, and asserted by the
// matrix below on every run.
type filteredOutcome int

const (
	// filteredEmpty: 200 with the row simply absent. A collection or filter
	// route that does not first load a parent by id -- Tags.List, Aliases.List,
	// Vocabularies.List, Resources.ListTags -- and the audit routes, which
	// query by id without loading the row.
	filteredEmpty filteredOutcome = iota

	// filteredNotFound: 404 not_found. A row addressed by id -- or a list route
	// whose PARENT is addressed by id (a tag's aliases, a tag's resources),
	// which resolves the parent in the caller's scope first and is refused the
	// same way.
	filteredNotFound

	// filteredValidation: 400 validation_error. Resolution only, and
	// deliberately: it answers "no match" and "not authorized to see the match"
	// identically so the endpoint discloses nothing. TagService.Resolve
	// documents it, and it is the one a caller is most likely to get wrong.
	filteredValidation
)

// readProbes covers every authenticated read method this SDK exposes.
//
// THAT COMPLETENESS IS A CHECK RATHER THAN A CLAIM.
// TestEveryReadMethodHasANamespaceProbe (readprobes_test.go, no build tag)
// reads the source of both halves and fails on a read method with no probe
// here, on a probe naming a method that does not exist, on a probe whose find
// closure calls a different endpoint than its name, and on a duplicate. It also
// holds the exclusions -- the unauthenticated health probes, and an accessor
// that sends no request -- in readProbeExclusions, each with its reason.
// Neither the list nor those reasons are repeated here; a second copy of either
// is how the first one comes to be wrong.
//
// TWO CONSTRAINTS THE GUARD PUTS ON THIS TABLE, worth knowing before editing it.
// It reads the []readProbe literal this function RETURNS -- build the slice with
// a helper or append to it in a loop and the guard finds nothing and fails,
// which is the safe direction but is a shape constraint all the same. And a
// probe's name must match the client method its find closure actually calls,
// since the name is what the guard counts as coverage.
//
// A new read method is still expected to arrive here alongside its resource
// file. A read endpoint nobody probed is the one a cross-merchant leak lives in;
// what changed is that something says so now.
//
// PAGINATION IS NOT AN EXHAUSTIVENESS ARGUMENT. Every list probe below narrows
// to the fixture on the server -- by an exact filter (a slug, an entity id) or
// by a route that addresses the fixture's own parent row. A collection route
// whose params struct offered no such filter would have to walk the whole
// collection and prove the walk complete, as Vocabularies.List did until #118
// gave it a slug filter on this line. A single page -- even at the server's 200-row clamp -- proves only
// that the row is not on the FIRST page, and "not on page one" is not "not
// visible": a long-lived harness, or a run that leaked fixtures, pushes a
// genuinely leaked row past the boundary and turns the leak into a pass. The
// routes scoped to one fixture row (a tag's aliases, a resource's tags, one
// entity's audit rows) hold a handful of rows by construction and are read with
// an explicit generous limit.
func readProbes(h harness) []readProbe {
	// For the routes that address ONE fixture row. Not an exhaustiveness claim:
	// these collections hold what this run just created.
	const scopedLimit = 200

	return []readProbe{
		{
			name:     "Tags.List",
			filtered: filteredEmpty,
			find: func(ctx context.Context, c *octonomy.Client, readNS string, want namespaceFixture, extra ...octonomy.RequestOption) (bool, error) {
				// Filtered by the fixture's own slug rather than paged: an empty
				// page under an exact filter means the row is genuinely not
				// visible, with no page-boundary caveat at all.
				page, err := c.Tags.List(ctx, &octonomy.TagListParams{
					Slug:        octonomy.String(want.tag.Slug),
					ListOptions: octonomy.ListOptions{Limit: scopedLimit},
				}, h.scoped(readNS, extra...)...)
				if err != nil {
					return false, err
				}
				for _, row := range page.Data {
					if row.ID == want.tag.ID {
						return true, nil
					}
				}
				return false, nil
			},
		},
		{
			name:     "Tags.Get",
			filtered: filteredNotFound,
			find: func(ctx context.Context, c *octonomy.Client, readNS string, want namespaceFixture, extra ...octonomy.RequestOption) (bool, error) {
				row, err := c.Tags.Get(ctx, want.tag.ID, h.scoped(readNS, extra...)...)
				if err != nil {
					return false, err
				}
				return row.ID == want.tag.ID, nil
			},
		},
		{
			name:     "Tags.Resolve",
			filtered: filteredValidation,
			find: func(ctx context.Context, c *octonomy.Client, readNS string, want namespaceFixture, extra ...octonomy.RequestOption) (bool, error) {
				// The slug, not the id: resolution is the one read that takes a
				// caller-chosen name, which makes it the one a caller could most
				// plausibly use to probe another merchant's vocabulary.
				resolved, err := c.Tags.Resolve(ctx, want.tag.Slug, nil, h.scoped(readNS, extra...)...)
				if err != nil {
					return false, err
				}
				return resolved.Tag.ID == want.tag.ID, nil
			},
		},
		{
			name:     "Tags.ListAliases",
			filtered: filteredNotFound,
			find: func(ctx context.Context, c *octonomy.Client, readNS string, want namespaceFixture, extra ...octonomy.RequestOption) (bool, error) {
				page, err := c.Tags.ListAliases(ctx, want.tag.ID, &octonomy.TagListAliasesParams{
					ListOptions: octonomy.ListOptions{Limit: scopedLimit},
				}, h.scoped(readNS, extra...)...)
				if err != nil {
					return false, err
				}
				for _, row := range page.Data {
					if row.ID == want.alias.ID {
						return true, nil
					}
				}
				return false, nil
			},
		},
		{
			name:     "Tags.ListResources",
			filtered: filteredNotFound,
			find: func(ctx context.Context, c *octonomy.Client, readNS string, want namespaceFixture, extra ...octonomy.RequestOption) (bool, error) {
				page, err := c.Tags.ListResources(ctx, want.tag.ID, &octonomy.TagListResourcesParams{
					ListOptions: octonomy.ListOptions{Limit: scopedLimit},
				}, h.scoped(readNS, extra...)...)
				if err != nil {
					return false, err
				}
				for _, row := range page.Data {
					if row.ResourceID == want.resourceID {
						return true, nil
					}
				}
				return false, nil
			},
		},
		{
			name:     "Tags.ListAuditLogs",
			filtered: filteredEmpty,
			find: func(ctx context.Context, c *octonomy.Client, readNS string, want namespaceFixture, extra ...octonomy.RequestOption) (bool, error) {
				page, err := c.Tags.ListAuditLogs(ctx, want.tag.ID, &octonomy.TagListAuditLogsParams{
					ListOptions: octonomy.ListOptions{Limit: scopedLimit},
				}, h.scoped(readNS, extra...)...)
				if err != nil {
					return false, err
				}
				// Any row at all: the audit table records what happened to this
				// tag, so a single row is another merchant's history leaking.
				return len(page.Data) > 0, nil
			},
		},
		{
			name:     "Aliases.List",
			filtered: filteredEmpty,
			find: func(ctx context.Context, c *octonomy.Client, readNS string, want namespaceFixture, extra ...octonomy.RequestOption) (bool, error) {
				// Exact slug filter, same reasoning as Tags.List.
				page, err := c.Aliases.List(ctx, &octonomy.TagAliasListParams{
					Slug:        octonomy.String(want.alias.Slug),
					ListOptions: octonomy.ListOptions{Limit: scopedLimit},
				}, h.scoped(readNS, extra...)...)
				if err != nil {
					return false, err
				}
				for _, row := range page.Data {
					if row.ID == want.alias.ID {
						return true, nil
					}
				}
				return false, nil
			},
		},
		{
			name:     "Aliases.Get",
			filtered: filteredNotFound,
			find: func(ctx context.Context, c *octonomy.Client, readNS string, want namespaceFixture, extra ...octonomy.RequestOption) (bool, error) {
				row, err := c.Aliases.Get(ctx, want.alias.ID, h.scoped(readNS, extra...)...)
				if err != nil {
					return false, err
				}
				return row.ID == want.alias.ID, nil
			},
		},
		{
			name:     "Vocabularies.List",
			filtered: filteredEmpty,
			find: func(ctx context.Context, c *octonomy.Client, readNS string, want namespaceFixture, extra ...octonomy.RequestOption) (bool, error) {
				// Filtered by the fixture's own slug, same reasoning as Tags.List.
				// This probe used to WALK the collection, and prove the walk
				// complete, because VocabularyListParams on this line had no slug
				// filter; #118 ported main's, and with it in place the probe
				// narrows to the fixture by slug, as main's does at 5e40964. An
				// empty page means the row is genuinely not visible rather than
				// merely absent from the page we looked at.
				page, err := c.Vocabularies.List(ctx, &octonomy.VocabularyListParams{
					Slug:        octonomy.String(want.vocabulary.Slug),
					ListOptions: octonomy.ListOptions{Limit: scopedLimit},
				}, h.scoped(readNS, extra...)...)
				if err != nil {
					return false, err
				}
				for _, row := range page.Data {
					if row.ID == want.vocabulary.ID {
						return true, nil
					}
				}
				return false, nil
			},
		},
		{
			name:     "Vocabularies.Get",
			filtered: filteredNotFound,
			find: func(ctx context.Context, c *octonomy.Client, readNS string, want namespaceFixture, extra ...octonomy.RequestOption) (bool, error) {
				row, err := c.Vocabularies.Get(ctx, want.vocabulary.ID, h.scoped(readNS, extra...)...)
				if err != nil {
					return false, err
				}
				return row.ID == want.vocabulary.ID, nil
			},
		},
		{
			name:     "Resources.ListTags",
			filtered: filteredEmpty,
			find: func(ctx context.Context, c *octonomy.Client, readNS string, want namespaceFixture, extra ...octonomy.RequestOption) (bool, error) {
				page, err := c.Resources.ListTags(ctx, want.resourceType, want.resourceID,
					&octonomy.ResourceListTagsParams{ListOptions: octonomy.ListOptions{Limit: scopedLimit}},
					h.scoped(readNS, extra...)...)
				if err != nil {
					return false, err
				}
				for _, row := range page.Data {
					if row.Tag.ID == want.tag.ID {
						return true, nil
					}
				}
				return false, nil
			},
		},
		{
			name:     "Resources.ListAuditLogs",
			filtered: filteredEmpty,
			find: func(ctx context.Context, c *octonomy.Client, readNS string, want namespaceFixture, extra ...octonomy.RequestOption) (bool, error) {
				page, err := c.Resources.ListAuditLogs(ctx, want.resourceType, want.resourceID,
					&octonomy.ResourceListAuditLogsParams{ListOptions: octonomy.ListOptions{Limit: scopedLimit}},
					h.scoped(readNS, extra...)...)
				if err != nil {
					return false, err
				}
				return len(page.Data) > 0, nil
			},
		},
		{
			name:     "AuditLogs.List",
			filtered: filteredEmpty,
			find: func(ctx context.Context, c *octonomy.Client, readNS string, want namespaceFixture, extra ...octonomy.RequestOption) (bool, error) {
				page, err := c.AuditLogs.List(ctx, &octonomy.AuditLogListParams{
					EntityType:  octonomy.String("tag"),
					EntityID:    octonomy.String(want.tag.ID),
					ListOptions: octonomy.ListOptions{Limit: scopedLimit},
				}, h.scoped(readNS, extra...)...)
				if err != nil {
					return false, err
				}
				return len(page.Data) > 0, nil
			},
		},
	}
}

// outcome is what one probe run is entitled to.
type outcome int

const (
	// outcomeVisible: the row must come back, with no error. These runs are not
	// padding -- without them "merchant A cannot see merchant B" also passes
	// when merchant B was never written, when the probe reads the wrong field,
	// and when the server is returning empty pages to everyone.
	outcomeVisible outcome = iota

	// outcomeFiltered: the request is AUTHORIZED and the row must not be in the
	// result. The server may answer 200-with-the-row-absent (a collection or
	// filter route) or refuse the row, or its parent, by id (404 not_found, or
	// 400 validation_error on resolution) -- but only the one answer the probe
	// declares, and nothing else. Accepting "any error" here is how this
	// assertion would come to pass against a crashed container; see
	// requireFilteredOutcome.
	outcomeFiltered

	// outcomeForbidden: the request must never reach a queryset at all. This is
	// the permission layer, and it is a genuinely different mechanism from the
	// filter above -- which is why it needs its own run rather than being
	// inferred from one.
	outcomeForbidden
)

// probeRun is one row of a read matrix: who reads, where, what they are looking
// for, and what they are entitled to get.
type probeRun struct {
	name string

	// client and readNS are the reader; want is the row looked for.
	client *octonomy.Client
	readNS string
	want   namespaceFixture

	// extra carries per-run request options on top of the namespace and
	// application pair every read in this suite sends.
	extra  []octonomy.RequestOption
	expect outcome
}

// runProbeMatrix asks every authenticated read endpoint this SDK exposes every
// question in runs, and holds each answer to what that endpoint is supposed to
// do.
//
// One matrix rather than a handful of hand-written cases, because the property
// under test is about the READ SURFACE and not about any endpoint: a rule that
// holds on twelve routes and not the thirteenth is not a rule a caller can rely
// on, and the thirteenth is where the leak lives.
//
// The subtests are sequential -- no t.Parallel -- which is what lets the
// fixtures' teardown sit in the root test's defer: the stack seed pushes onto
// is rule 3 of the t.Cleanup replacement model, and its one defer in the root
// test's body is rule 2, which holds because a sequential t.Run returns only
// after its subtest finishes.
func runProbeMatrix(ctx context.Context, t *testing.T, h harness, runs []probeRun) {
	t.Helper()

	for _, probe := range readProbes(h) {
		t.Run(probe.name, func(t *testing.T) {
			for _, run := range runs {
				t.Run(run.name, func(t *testing.T) {
					found, err := probe.find(ctx, run.client, run.readNS, run.want, run.extra...)

					switch run.expect {
					case outcomeVisible:
						if err != nil {
							t.Fatalf("%s: %v", probe.name, err)
						}
						if !found {
							t.Fatalf("%s did not return the row it was entitled to see: the negative runs of this probe prove nothing", probe.name)
						}

					case outcomeFiltered:
						requireFilteredOutcome(t, err, probe.name, probe.filtered)
						if found {
							t.Errorf("%s returned a %s row to a client reading %s: this is a cross-scope data leak",
								probe.name, describeScope(run.want.namespaceID), describeScope(run.readNS))
						}

					case outcomeForbidden:
						apiErr := requireAPIError(t, err, probe.name)
						if !octonomy.IsForbidden(err) || apiErr.StatusCode != 403 {
							t.Errorf("%s: reaching into another merchant gave {status:%d code:%q}, want {403 %q} -- the permission layer must refuse the request, not leave it to the namespace filter",
								probe.name, apiErr.StatusCode, apiErr.Code, octonomy.CodeForbidden)
						}
						if found {
							t.Errorf("%s returned a %s row on a request that should have been refused", probe.name, describeScope(run.want.namespaceID))
						}

					default:
						t.Fatalf("%s: run %q expects outcome %d, which this matrix does not know how to assert", probe.name, run.name, run.expect)
					}
				})
			}
		})
	}
}

// TestIntegration_NamespaceIsolation is the 2am-Friday test.
//
// Two merchants are seeded with a full set of rows, and every authenticated
// read endpoint this SDK exposes is asked the same question six times: three
// runs that must find the row, and three that must not. Both halves are
// load-bearing, and the negatives cover THREE distinct mechanisms, each of
// which has to hold on its own:
//
//	the FILTER, under a merchant token   merchant A reads its own namespace and
//	                                     merchant B's rows are simply not in the
//	                                     result
//	the FILTER, under a wildcard token   the same read by a token authorized for
//	                                     every partition, so nothing refuses it
//	                                     and only the server's namespace filter
//	                                     stands between it and merchant B
//	AUTHORIZATION                        merchant A ASKS FOR merchant B and is
//	                                     refused 403 before any queryset runs
//	                                     (BearerTokenPermission, core/auth.py)
//
// The third is the one an attacker actually performs, and it is not implied by
// either of the first two: a token scoped to its own namespace exercises the
// filter no matter what the permission layer does. A suite with only the filter
// runs would stay green through a permission regression on any individual route,
// and one with only the authorization run would stay green through a lost
// namespace filter. The harness's single generic `GET /tags` denial covers one
// route out of thirteen, which is why this is asserted per endpoint here.
func TestIntegration_NamespaceIsolation(t *testing.T) {
	h := loadHarness(t)
	var rows teardown
	defer rows.run()

	wildcard := h.wildcard(t)
	clientA := h.merchantClient(t, h.merchantA)
	clientB := h.merchantClient(t, h.merchantB)

	// Seeded by the wildcard client because it is the only identity that reaches
	// both merchants. That the EXACT grants can write in their own namespace is
	// asserted separately, by the harness itself.
	fixtureA := h.seed(t, &rows, wildcard, h.merchantA.id)
	fixtureB := h.seed(t, &rows, wildcard, h.merchantB.id)

	// The matrix's budget starts here, once the rows exist: each seed had its
	// own (suiteTimeout).
	ctx, cancel := context.WithTimeout(context.Background(), suiteTimeout)
	defer cancel()
	runProbeMatrix(ctx, t, h, []probeRun{
		{
			name:   "a wildcard token reading merchant B sees merchant B",
			client: wildcard,
			readNS: h.merchantB.id,
			want:   fixtureB,
			expect: outcomeVisible,
		},
		{
			name:   "an exact merchant-B grant sees merchant B",
			client: clientB,
			readNS: h.merchantB.id,
			want:   fixtureB,
			expect: outcomeVisible,
		},
		{
			name:   "an exact merchant-A grant sees merchant A",
			client: clientA,
			readNS: h.merchantA.id,
			want:   fixtureA,
			expect: outcomeVisible,
		},
		{
			name:   "merchant A, reading its own namespace, does not get merchant B",
			client: clientA,
			readNS: h.merchantA.id,
			want:   fixtureB,
			expect: outcomeFiltered,
		},
		{
			// The same read by a token that IS authorized for merchant B. The
			// permission layer cannot be what hides the row here, so a pass
			// means the namespace filter is doing the work.
			name:   "a wildcard token scoped to merchant A does not get merchant B",
			client: wildcard,
			readNS: h.merchantA.id,
			want:   fixtureB,
			expect: outcomeFiltered,
		},
		{
			// The request an attacker actually makes: merchant A ASKING FOR
			// merchant B. Neither run above performs it -- both keep asking for
			// a namespace the caller is entitled to -- so without this, a
			// permission regression on any one of the thirteen routes stays
			// green while every filter assertion above still passes.
			name:   "merchant A asking for merchant B is refused outright",
			client: clientA,
			readNS: h.merchantB.id,
			want:   fixtureB,
			expect: outcomeForbidden,
		},
	})
}

// TestIntegration_IncludeGlobalFailsClosed pins the one option whose failure
// mode is silent, across the whole authenticated read surface.
//
// WithIncludeGlobal widens what a namespaced read ASKS for. Whether the global
// rows actually come back is decided separately, by whether the token holds
// global authority (request_include_global, octonomy/core/auth.py:53-67). So a
// token with an exact merchant grant that asks for global rows gets a 200 and
// its own rows -- no error, no warning, and nothing in the response that says
// the opt-in was declined.
//
// That is unfalsifiable from a fixture: a canned server returns whatever the
// fixture says, and a WILDCARD token makes the opt-in always succeed, so the
// fail-closed branch would never execute. It needs a token that genuinely cannot
// see global rows, which is why the harness mints exact grants.
//
// It runs as a matrix over every authenticated read endpoint for the same
// reason the isolation test does: the server threads request_include_global
// through the tag detail, resolution, vocabulary, alias, resource and audit
// views SEPARATELY. One view can misuse the flag while Tags.List stays correct,
// and a single-endpoint test would never see it.
//
// The five runs, and why each is needed:
//
//	default             a namespaced read excludes global rows with no option at
//	                    all -- otherwise the parameter means nothing
//	authorized opt-in   the CONTROL. Without it "the merchant saw no global row"
//	                    also passes on a route that ignores the option outright
//	fail-closed         the assertion: an exact merchant grant asking for global
//	                    rows still gets none
//	no axis widening    global, never "every namespace". Asserted with the
//	                    WILDCARD token, which is authorized for merchant B, so
//	                    authorization cannot be what withholds that row -- only
//	                    the meaning of the parameter can
//	own rows still come the read WORKS. Without it the three negatives above pass
//	                    whenever the request failed or came back empty, none of
//	                    which is the fail-closed behaviour being claimed
func TestIntegration_IncludeGlobalFailsClosed(t *testing.T) {
	h := loadHarness(t)
	var rows teardown
	defer rows.run()

	wildcard := h.wildcard(t)
	clientA := h.merchantClient(t, h.merchantA)

	fixtureA := h.seed(t, &rows, wildcard, h.merchantA.id)
	fixtureB := h.seed(t, &rows, wildcard, h.merchantB.id)
	// Rows with no namespace at all: the tenant-shared set the option exists to
	// reach.
	fixtureGlobal := h.seed(t, &rows, wildcard, "")

	includeGlobal := []octonomy.RequestOption{octonomy.WithIncludeGlobal()}

	// As in TestIntegration_NamespaceIsolation: the matrix's budget starts once
	// the rows exist.
	ctx, cancel := context.WithTimeout(context.Background(), suiteTimeout)
	defer cancel()
	runProbeMatrix(ctx, t, h, []probeRun{
		{
			name:   "a namespaced read excludes the global rows by default",
			client: wildcard,
			readNS: h.merchantA.id,
			want:   fixtureGlobal,
			expect: outcomeFiltered,
		},
		{
			name:   "an authorized token can opt into the global rows",
			client: wildcard,
			readNS: h.merchantA.id,
			want:   fixtureGlobal,
			extra:  includeGlobal,
			expect: outcomeVisible,
		},
		{
			name:   "an exact merchant grant cannot opt into the global rows",
			client: clientA,
			readNS: h.merchantA.id,
			want:   fixtureGlobal,
			extra:  includeGlobal,
			expect: outcomeFiltered,
		},
		{
			name:   "include_global does not widen a merchant-A read to merchant B",
			client: wildcard,
			readNS: h.merchantA.id,
			want:   fixtureB,
			extra:  includeGlobal,
			expect: outcomeFiltered,
		},
		{
			// The read still works. Without this the three negatives above pass
			// whenever the request failed or came back empty -- none of which is
			// the fail-closed behaviour being claimed, which is that the global
			// rows are withheld and the read is otherwise normal.
			name:   "the same opt-in read still returns merchant A's own rows",
			client: clientA,
			readNS: h.merchantA.id,
			want:   fixtureA,
			extra:  includeGlobal,
			expect: outcomeVisible,
		},
	})
}
