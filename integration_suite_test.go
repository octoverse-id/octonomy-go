//go:build integration
// +build integration

// The full integration suite (#17), run against a real Octonomy container.
//
// WHAT THIS IS FOR, AND HOW IT DIFFERS FROM ITS NEIGHBOURS.
//
// The unit suites drive an httptest server whose fixtures this repository
// writes, so they assert the client against this SDK's own beliefs. #32 is what
// that costs when a belief is wrong: every single-resource read decoded to a
// zero-valued struct with a nil error, and a complete unit suite stayed green.
// integration_test.go closed that class by checking the SHAPES against a real
// server -- the two response envelopes, the composite payloads, the namespace
// fields the server populates.
//
// This file asserts the next class down: SEMANTICS the SDK's doc comments
// promise and no fixture can settle, because they are properties of the server's
// authorization and persistence rather than of any payload.
//
//	namespace isolation      -- a merchant-A client never sees a merchant-B row,
//	                            on any read endpoint this SDK exposes
//	include_global           -- fail-closed: the opt-in widens what is ASKED for,
//	                            and a token with no global authority still sees
//	                            no global rows
//	assignment idempotence   -- 201 once, then 200 with the same row forever
//	bulk partial failure     -- atomic, and it does not leak the existence of
//	                            another merchant's rows
//	deactivation cascade     -- deleting a tag deactivates its aliases
//	tag-tree orphans         -- and NOT its children, so a live child of a
//	                            deactivated parent comes back from the default
//	                            list alone: the shape TagTree.Orphans exists for
//	tag-tree cycles          -- the server accepts A -> B -> A, so ErrTagCycle
//	                            is reachable rather than defensive -- and
//	                            clearing a parent link is the repair for one
//	nullable fields          -- which of a PATCH body's fields the server will
//	                            accept a null for, and the 400 or 409 it answers
//	                            for the rest: the table TagUpdate publishes
//	slug uniqueness          -- scoped per namespace, not per tenant
//	error envelopes          -- every Is* helper this SDK exports, against the
//	                            error the server really sends
//
// Run it against the container harness:
//
//	make dev-server
//	make test-integration
//
// With OCTONOMY_TEST_BASE_URL unset every test here skips, so `go test ./...`
// stays hermetic, fast, and offline.
package octonomy_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	octonomy "github.com/octoverse-id/octonomy-go/v2"
)

// nilUUID is a well-formed id no row will ever have. It exercises the
// "route matched, row absent" branch rather than the "malformed id" one.
const nilUUID = "00000000-0000-0000-0000-000000000000"

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
// caller's namespace. Verified against server 3.2.0, route by route.
type filteredOutcome int

const (
	// filteredEmpty: 200 with the row simply absent. Every list and filter
	// route, plus the audit routes, which query by id without loading the row.
	filteredEmpty filteredOutcome = iota

	// filteredNotFound: 404 not_found. A row addressed by id -- or a route whose
	// PARENT is addressed by id, which is refused the same way.
	filteredNotFound

	// filteredValidation: 400 validation_error. Resolution only, and
	// deliberately: it answers "no match" and "not authorized to see the match"
	// identically so the endpoint discloses nothing. TagService.Resolve
	// documents it, and it is the one a caller is most likely to get wrong.
	filteredValidation
)

// readProbes covers every authenticated read method this SDK exposes.
//
// THAT COMPLETENESS IS A CHECK RATHER THAN A CLAIM, since #73. It used to be a
// hand-maintained list in this comment, which is a reader and not a gate: the
// suite iterates whatever this table holds, so a read method that arrived
// without a probe left the cross-merchant isolation assertion covering less than
// it did, with every job green and nothing to notice.
// TestEveryReadMethodHasANamespaceProbe (readprobes_test.go) now reads the
// source of both halves and fails on the gap. It also holds the one exclusion --
// the unauthenticated health probes -- in readProbeExclusions, with the reason.
//
// Neither the list nor that reason is repeated here. A second copy of either is
// how the first one came to be wrong.
//
// TWO CONSTRAINTS THE GUARD PUTS ON THIS TABLE, worth knowing before editing it.
// It reads the []readProbe literal this function RETURNS -- build the slice with
// a helper or append to it in a loop and the guard finds nothing and fails,
// which is the safe direction but is a shape constraint all the same. And a
// probe's name must match the client method its find closure actually calls,
// since the name is what the guard counts as coverage: an entry named for one
// endpoint that exercises another reports a method as probed while a different
// one is.
//
// A new read method is still expected to arrive here alongside its resource
// file. A read endpoint nobody probed is the one a cross-merchant leak lives in;
// what changed is that something says so now.
//
// PAGINATION IS NOT AN EXHAUSTIVENESS ARGUMENT. Every list probe below narrows
// to the fixture with an exact server-side filter; a route whose params struct
// offered no such filter would have to walk the whole collection with
// octonomy.Each instead, as Vocabularies.List did until #36 gave it a slug
// filter. A single page -- even at the server's 200-row clamp -- proves only
// that the row is not on the FIRST page, and "not on page one" is not "not
// visible": a long-lived harness, or a run that leaked fixtures, pushes a
// genuinely leaked row past the boundary and turns the leak into a pass. The routes scoped to one fixture row (a tag's aliases, a
// resource's tags, one entity's audit rows) hold a handful of rows by
// construction and are read with an explicit generous limit.
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
				// This probe used to WALK the collection instead, because
				// VocabularyListParams exposed no slug filter (#36); with the
				// filter in place it narrows like every other one, and an empty
				// page means the row is genuinely not visible rather than merely
				// absent from the page we looked at.
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
	// result. The server may answer 200-with-the-row-absent (every list route)
	// or refuse the row by id (404 not_found, or 400 validation_error on
	// resolution) -- but nothing else. Accepting "any error" here is how this
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

// runProbeMatrix asks every read endpoint this SDK exposes every question in
// runs, and holds each answer to what that endpoint is supposed to do.
//
// One matrix rather than a handful of hand-written cases, because the property
// under test is about the READ SURFACE and not about any endpoint: a rule that
// holds on twelve routes and not the thirteenth is not a rule a caller can rely
// on, and the thirteenth is where the leak lives.
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
					}
				})
			}
		})
	}
}

// TestIntegration_NamespaceIsolation is the 2am-Friday test.
//
// Two merchants are seeded with a full set of rows, and every read endpoint this
// SDK exposes is asked the same question six times: three runs that must find
// the row, and three that must not. Both halves are load-bearing, and the
// negatives cover THREE distinct mechanisms, each of which has to hold on its
// own:
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
	ctx, cancel := context.WithTimeout(context.Background(), suiteTimeout)
	defer cancel()

	wildcard := h.wildcard(t)
	clientA := h.merchantClient(t, h.merchantA)
	clientB := h.merchantClient(t, h.merchantB)

	// Seeded by the wildcard client because it is the only identity that reaches
	// both merchants. That the EXACT grants can write in their own namespace is
	// asserted separately, by the harness itself and by the tests below.
	fixtureA := h.seed(t, wildcard, h.merchantA.id)
	fixtureB := h.seed(t, wildcard, h.merchantB.id)

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
// mode is silent, across the whole read surface.
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
// It runs as a matrix over every read endpoint for the same reason the isolation
// test does, and it is not a theoretical concern here: server 3.2.0 threads
// request_include_global through the tag detail, resolution, vocabulary, alias,
// resource and audit views SEPARATELY. One view can misuse the flag while
// Tags.List stays correct, and a single-endpoint test would never see it.
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
	ctx, cancel := context.WithTimeout(context.Background(), suiteTimeout)
	defer cancel()

	wildcard := h.wildcard(t)
	clientA := h.merchantClient(t, h.merchantA)

	fixtureA := h.seed(t, wildcard, h.merchantA.id)
	fixtureB := h.seed(t, wildcard, h.merchantB.id)
	// Rows with no namespace at all: the tenant-shared set the option exists to
	// reach.
	fixtureGlobal := h.seed(t, wildcard, "")

	includeGlobal := []octonomy.RequestOption{octonomy.WithIncludeGlobal()}

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

// TestIntegration_AssignmentIdempotence proves the contract
// AssignmentService.Create's doc comment rests on.
//
// The server answers a first assignment 201 and every repeat 200, returning the
// existing row (octonomy/assignments/views.py:126). The SDK deliberately does not
// surface a 2xx status -- doData decodes the payload and discards the rest -- so
// the split is asserted here off the wire, once, and everything else through the
// methods a caller actually uses.
//
// It runs inside a merchant namespace under an exact grant, so the namespaced
// write path is genuinely exercised rather than standing in for the global one.
func TestIntegration_AssignmentIdempotence(t *testing.T) {
	h := loadHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), suiteTimeout)
	defer cancel()

	wildcard := h.wildcard(t)
	clientA := h.merchantClient(t, h.merchantA)
	fixtureA := h.seed(t, wildcard, h.merchantA.id)

	resourceID := uniqueSlug("int-idempotent")
	body := map[string]any{
		"application_id": h.applicationID,
		"tag_id":         fixtureA.tag.ID,
		"resource_type":  "cart",
		"resource_id":    resourceID,
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		if err := clientA.Assignments.Remove(cleanupCtx, octonomy.AssignmentRemove{
			ApplicationID: h.applicationID,
			TagID:         fixtureA.tag.ID,
			ResourceType:  "cart",
			ResourceID:    resourceID,
		}, octonomy.WithNamespace(h.namespaceType, h.merchantA.id)); err != nil {
			t.Errorf("cleanup: Assignments.Remove: %v", err)
		}
	})

	// assignmentID pulls the row id out of the server's {"data": {...}} envelope.
	assignmentID := func(t *testing.T, payload []byte) string {
		t.Helper()
		var envelope struct {
			Data struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(payload, &envelope); err != nil {
			t.Fatalf("decoding the raw assignment response: %v (body: %s)", err, payload)
		}
		if envelope.Data.ID == "" {
			t.Fatalf("the raw assignment response carried no data.id: %s", payload)
		}
		return envelope.Data.ID
	}

	status, payload := h.rawPost(ctx, t, h.merchantA.token, h.merchantA.id, "/api/v2/tag-assignments", body)
	if status != 201 {
		t.Fatalf("first assignment: HTTP %d, want 201 (body: %s)", status, payload)
	}
	firstID := assignmentID(t, payload)

	status, payload = h.rawPost(ctx, t, h.merchantA.token, h.merchantA.id, "/api/v2/tag-assignments", body)
	if status != 200 {
		t.Errorf("repeat assignment: HTTP %d, want 200 -- a 201 would mean the server created a second row, and IsConflict would mean it refused; the SDK documents neither", status)
	}
	if got := assignmentID(t, payload); got != firstID {
		t.Errorf("repeat assignment returned row %s, want the existing %s", got, firstID)
	}

	// And through the SDK, which is how a caller meets this: no error, and the
	// same row. This is the half the doc comment promises; the statuses above are
	// why it is true.
	again, err := clientA.Assignments.Create(ctx, octonomy.AssignmentCreate{
		ApplicationID: h.applicationID,
		TagID:         octonomy.String(fixtureA.tag.ID),
		ResourceType:  "cart",
		ResourceID:    resourceID,
	}, octonomy.WithNamespace(h.namespaceType, h.merchantA.id))
	if err != nil {
		t.Fatalf("Assignments.Create (repeat, through the SDK): %v", err)
	}
	if again.ID != firstID {
		t.Errorf("Assignments.Create returned row %s, want the existing %s", again.ID, firstID)
	}
	if again.NamespaceID == nil || *again.NamespaceID != h.merchantA.id {
		t.Errorf("the idempotent row lost its namespace: NamespaceID = %v, want %q", again.NamespaceID, h.merchantA.id)
	}
}

// TestIntegration_BulkPartialFailure covers two properties of one request, and
// the second is a security property rather than a correctness one.
//
//	ATOMIC              -- a bulk assign naming one good id and one bad one
//	                       assigns NEITHER. A caller who retries after a partial
//	                       success would double-assign; a caller who does not
//	                       would be left with a resource half-tagged and no way
//	                       to tell which half.
//	NO EXISTENCE ORACLE -- an id that names a real tag in ANOTHER merchant must
//	                       be reported exactly as an id that names nothing at
//	                       all. Any difference between the two -- a distinct
//	                       code, a different message, even a different field --
//	                       turns this endpoint into a way to enumerate another
//	                       merchant's tag ids one guess at a time.
//
// The second is asserted by comparing the two error messages rather than by
// pattern-matching either: with the offending id normalised out, they must be
// byte-identical. A weaker check ("both say validation_error") would pass while
// the messages differed in the detail that gives the game away.
func TestIntegration_BulkPartialFailure(t *testing.T) {
	h := loadHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), suiteTimeout)
	defer cancel()

	wildcard := h.wildcard(t)
	clientA := h.merchantClient(t, h.merchantA)
	fixtureA := h.seed(t, wildcard, h.merchantA.id)
	fixtureB := h.seed(t, wildcard, h.merchantB.id)

	resourceID := uniqueSlug("int-bulk")

	// bulkFailure runs one bulk assign expected to fail and returns the detail
	// message, with the offending id replaced by a fixed placeholder so two
	// messages about different ids can be compared directly.
	bulkFailure := func(t *testing.T, offendingID, label string) string {
		t.Helper()
		_, err := clientA.Assignments.BulkAssign(ctx, octonomy.BulkAssign{
			ApplicationID: h.applicationID,
			ResourceType:  "cart",
			ResourceID:    resourceID,
			TagIDs:        []string{fixtureA.tag.ID, offendingID},
		}, octonomy.WithNamespace(h.namespaceType, h.merchantA.id))

		apiErr := requireAPIError(t, err, label)
		if !octonomy.IsValidation(err) {
			t.Errorf("%s: IsValidation = false, code = %q", label, apiErr.Code)
		}
		messages := detailStrings(apiErr.Details, "tag_ids")
		if len(messages) == 0 {
			t.Fatalf("%s: details named no tag_ids field: %s", label, joinDetails(apiErr.Details))
		}
		joined := strings.Join(messages, "\n")
		if !strings.Contains(joined, offendingID) {
			t.Errorf("%s: the error did not name the offending id %s: %q", label, offendingID, joined)
		}
		// The GOOD id must not appear. Naming it would say "this one was fine",
		// which is a second, quieter oracle on the same request.
		if strings.Contains(joined, fixtureA.tag.ID) {
			t.Errorf("%s: the error named the caller's own valid id %s, disclosing which of the two was rejected: %q",
				label, fixtureA.tag.ID, joined)
		}

		// The WHOLE envelope, canonicalised -- status, code, message and every
		// details key, not just the tag_ids strings. An oracle does not have to
		// live in the field the test happens to read: a different status, a
		// reworded message, or one extra key such as
		// {"cross_namespace": ["..."]} would each tell the caller which of the
		// two ids was real while a tag_ids-only comparison stayed green. The
		// request id is the one field that legitimately differs per call and is
		// excluded; the offending id is substituted out so two errors about
		// different ids can be compared at all.
		canonical, err := json.Marshal(map[string]any{
			"status":  apiErr.StatusCode,
			"code":    apiErr.Code,
			"message": apiErr.Message,
			"details": apiErr.Details,
		})
		if err != nil {
			t.Fatalf("%s: canonicalising the envelope: %v", label, err)
		}
		return strings.ReplaceAll(string(canonical), offendingID, "<offending-id>")
	}

	// A real tag, in a real merchant, that this caller may not see.
	crossNamespace := bulkFailure(t, fixtureB.tag.ID, "bulk assign naming another merchant's tag")
	// An id that names nothing anywhere.
	nonexistent := bulkFailure(t, nilUUID, "bulk assign naming a nonexistent tag")

	if crossNamespace != nonexistent {
		t.Errorf("a tag in another merchant is reported differently from a tag that does not exist:\n  cross-namespace: %s\n  nonexistent:     %s\nthe difference lets a caller enumerate another merchant's tag ids",
			crossNamespace, nonexistent)
	}

	// Atomicity. The valid id in both calls above belongs to this merchant and
	// would have been assignable on its own.
	page, err := clientA.Resources.ListTags(ctx, "cart", resourceID,
		&octonomy.ResourceListTagsParams{ListOptions: octonomy.ListOptions{Limit: 200}},
		h.scoped(h.merchantA.id)...)
	if err != nil {
		t.Fatalf("Resources.ListTags after the failed bulk assigns: %v", err)
	}
	if len(page.Data) != 0 {
		t.Errorf("a failed bulk assign left %d assignment(s) behind, want none: the call is documented as atomic", len(page.Data))
	}
}

// TestIntegration_DeactivationCascade proves what Delete means on this server,
// and what it means for the rows hanging off the one deleted.
//
// Two claims this SDK's doc comments make, neither of which a fixture can
// settle, because both are consequences of a write the server performs on rows
// the request never named:
//
//	Delete is DEACTIVATION, not deletion -- the tag is still there, readable,
//	with IsActive false. A caller who believed the row was gone would report a
//	missing tag where the server has a disabled one.
//	Deleting a tag DEACTIVATES ITS ALIASES -- an alias resolving to a disabled
//	tag would otherwise be a live pointer into a decommissioned vocabulary.
//
// It runs under an exact merchant grant, so the whole cascade happens inside one
// merchant namespace: the aliases the server deactivates are namespaced rows,
// reached by a server-side query rather than by anything this client sent.
func TestIntegration_DeactivationCascade(t *testing.T) {
	h := loadHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), suiteTimeout)
	defer cancel()

	clientA := h.merchantClient(t, h.merchantA)
	ns := octonomy.WithNamespace(h.namespaceType, h.merchantA.id)
	scoped := h.scoped(h.merchantA.id)

	// Written by the merchant's OWN token rather than the wildcard one: this is
	// the suite's proof that an exact grant performs real namespaced writes, not
	// only reads.
	tag, err := clientA.Tags.Create(ctx, octonomy.TagCreate{
		ApplicationID: octonomy.String(h.applicationID),
		Name:          "integration cascade",
		Slug:          uniqueSlug("int-cascade"),
		Type:          "label",
	}, ns)
	if err != nil {
		t.Fatalf("Tags.Create: %v", err)
	}
	// Registered immediately, not after the aliases below, and deliberately
	// tolerant: the test itself deletes this tag, so on the happy path this
	// cleanup deletes an already-deactivated row -- which the server accepts.
	// Its job is the UNHAPPY path, where an alias create fails and t.Fatalf
	// would otherwise leave an active tag behind in a namespace this suite
	// reads on every subsequent run against the same harness.
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		if err := clientA.Tags.Delete(cleanupCtx, tag.ID, scoped...); err != nil {
			t.Errorf("cleanup: Tags.Delete: %v", err)
		}
	})

	// Two aliases, not one. A cascade that deactivated only the first alias it
	// found would pass a single-alias test.
	//
	// Each gets its OWN cleanup, and leaning on the tag deletion to cascade to
	// them would be exactly wrong: the cascade is the thing under test, so on the
	// failure path this cleanup has to run it is by definition not working. Worse,
	// it could not run even in principle -- deactivate_tag returns early for an
	// already-inactive tag (octonomy/tags/services.py:348-354) and never reaches
	// the alias sweep, so the tag cleanup below cannot repair an alias the server
	// left active. LIFO puts these before it.
	aliasIDs := make([]string, 0, 2)
	for i := 0; i < 2; i++ {
		alias, err := clientA.Aliases.Create(ctx, octonomy.TagAliasCreate{
			ApplicationID: octonomy.String(h.applicationID),
			TagID:         tag.ID,
			Name:          "integration cascade alias",
			Slug:          uniqueSlug("int-cascade-alias"),
		}, ns)
		if err != nil {
			t.Fatalf("Aliases.Create: %v", err)
		}
		aliasIDs = append(aliasIDs, alias.ID)
		id := alias.ID
		t.Cleanup(func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
			defer cancel()
			if err := clientA.Aliases.Delete(cleanupCtx, id, scoped...); err != nil {
				t.Errorf("cleanup: Aliases.Delete: %v", err)
			}
		})
	}

	// activeAliases counts what the default list returns: the server filters to
	// active rows when is_active is absent, which is the behaviour
	// TagAliasListParams.IsActive documents.
	activeAliases := func(t *testing.T) int {
		t.Helper()
		page, err := clientA.Tags.ListAliases(ctx, tag.ID, &octonomy.TagListAliasesParams{
			ListOptions: octonomy.ListOptions{Limit: 200},
		}, scoped...)
		if err != nil {
			t.Fatalf("Tags.ListAliases: %v", err)
		}
		return len(page.Data)
	}

	if got := activeAliases(t); got != len(aliasIDs) {
		t.Fatalf("before the delete the tag has %d active alias(es), want %d: the cascade assertion below would prove nothing", got, len(aliasIDs))
	}

	if err := clientA.Tags.Delete(ctx, tag.ID, scoped...); err != nil {
		t.Fatalf("Tags.Delete: %v", err)
	}

	// The tag itself: still readable, no longer active.
	deleted, err := clientA.Tags.Get(ctx, tag.ID, scoped...)
	if err != nil {
		t.Fatalf("Tags.Get after Delete: the row must still be readable, because Delete is deactivation: %v", err)
	}
	if deleted.IsActive {
		t.Error("Tags.Get after Delete reports IsActive = true: the deactivation did not happen")
	}

	// The cascade.
	if got := activeAliases(t); got != 0 {
		t.Errorf("%d alias(es) are still active after their tag was deleted, want 0: an alias pointing at a deactivated tag is a live pointer into a decommissioned vocabulary", got)
	}

	// Deactivated, not deleted: the rows are still there under the inactive
	// filter, which is how a caller finds what a cascade touched.
	inactive, err := clientA.Tags.ListAliases(ctx, tag.ID, &octonomy.TagListAliasesParams{
		IsActive:    octonomy.Bool(false),
		ListOptions: octonomy.ListOptions{Limit: 200},
	}, scoped...)
	if err != nil {
		t.Fatalf("Tags.ListAliases (inactive): %v", err)
	}
	seen := map[string]bool{}
	for _, row := range inactive.Data {
		if row.IsActive {
			t.Errorf("alias %s came back from an is_active=false filter reporting IsActive = true", row.ID)
		}
		seen[row.ID] = true
	}
	for _, id := range aliasIDs {
		if !seen[id] {
			t.Errorf("alias %s is neither active nor inactive after the cascade: it was hard-deleted, and alias history is supposed to survive", id)
		}
	}

	// Reachable by id too, which is what a caller holding a stored alias id does.
	row, err := clientA.Aliases.Get(ctx, aliasIDs[0], scoped...)
	if err != nil {
		t.Fatalf("Aliases.Get after the cascade: %v", err)
	}
	if row.IsActive {
		t.Error("Aliases.Get reports IsActive = true after the cascade deactivated it")
	}
}

// TestIntegration_DuplicateSlugScopedPerNamespace pins the uniqueness rule a
// multi-merchant caller most needs to know.
//
// Slug uniqueness is NOT tenant-wide. The server enforces it per
// (tenant, application, namespace, type, slug) through three split partial
// indexes (octonomy/tags/models.py:156-190), so two merchants may each own a tag
// called "priority" and neither blocks the other. A caller who assumed otherwise
// would prefix every slug with a merchant id forever, and one who assumed no
// uniqueness at all would meet a 409 in production.
//
// Only a real server settles which: the rule lives in database constraints, and
// a fixture asserting a 409 is a fixture asserting what this test already
// believes.
func TestIntegration_DuplicateSlugScopedPerNamespace(t *testing.T) {
	h := loadHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), suiteTimeout)
	defer cancel()

	wildcard := h.wildcard(t)
	slug := uniqueSlug("int-dup")

	// create makes one tag carrying the shared slug inside namespaceID, or in the
	// global namespace when it is empty.
	//
	// The write and the cleanup delete take DIFFERENT option sets, which is the
	// whole reason this takes a namespace id rather than a ready-made []Option.
	// A create carries its application in the body (WithApplication is refused
	// there), while the DELETE is bodyless and the query is the only place its
	// application can come from -- and a namespaced one without it is refused by
	// checkScopeCoherence before it is sent.
	create := func(t *testing.T, label, namespaceID string) (*octonomy.Tag, error) {
		t.Helper()

		var writeOpts []octonomy.RequestOption
		deleteOpts := []octonomy.RequestOption{octonomy.WithApplication(h.applicationID)}
		if namespaceID != "" {
			ns := octonomy.WithNamespace(h.namespaceType, namespaceID)
			writeOpts = append(writeOpts, ns)
			deleteOpts = append(deleteOpts, ns)
		}

		tag, err := wildcard.Tags.Create(ctx, octonomy.TagCreate{
			ApplicationID: octonomy.String(h.applicationID),
			Name:          "integration duplicate " + label,
			Slug:          slug,
			Type:          "label",
		}, writeOpts...)
		if err == nil {
			id := tag.ID
			t.Cleanup(func() {
				cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
				defer cancel()
				if err := wildcard.Tags.Delete(cleanupCtx, id, deleteOpts...); err != nil {
					t.Errorf("cleanup %s: Tags.Delete: %v", label, err)
				}
			})
		}
		return tag, err
	}

	inA, err := create(t, "merchant A", h.merchantA.id)
	if err != nil {
		t.Fatalf("creating the first tag in merchant A: %v", err)
	}

	// requireDuplicateConflict asserts BOTH halves of what #17 asks for: the code
	// a caller branches on and the status a proxy, a log line or a retry policy
	// sees. IsConflict reads the code alone, so a server that started answering
	// `conflict` with a 400 would keep every IsConflict branch working while
	// silently changing what every layer between the two saw.
	requireDuplicateConflict := func(t *testing.T, err error, label string) {
		t.Helper()
		apiErr := requireAPIError(t, err, label)
		if !octonomy.IsConflict(err) {
			t.Errorf("%s: IsConflict = false, code = %q", label, apiErr.Code)
		}
		if apiErr.StatusCode != http.StatusConflict {
			t.Errorf("%s: status = %d, want 409", label, apiErr.StatusCode)
		}
	}

	// The same slug, the same merchant, the same type: refused.
	if _, err := create(t, "merchant A again", h.merchantA.id); err == nil {
		t.Error("a duplicate slug inside one merchant was accepted: the (tenant, application, namespace, type, slug) constraint is not being enforced")
	} else {
		requireDuplicateConflict(t, err, "duplicate slug inside one merchant")
	}

	// The same slug in a DIFFERENT merchant: accepted, and a distinct row.
	inB, err := create(t, "merchant B", h.merchantB.id)
	if err != nil {
		t.Fatalf("the same slug was refused in a second merchant, so slug uniqueness is tenant-wide rather than per-namespace: %v", err)
	}
	if inB.ID == inA.ID {
		t.Fatalf("both merchants resolved to the same row %s: the create returned an existing tag rather than making one", inA.ID)
	}
	if inB.NamespaceID == nil || *inB.NamespaceID != h.merchantB.id {
		t.Errorf("the merchant-B row reports namespace %v, want %q", inB.NamespaceID, h.merchantB.id)
	}

	// And once more on the global rung, which is a third constraint again: an
	// application-scoped row with no namespace.
	global, err := create(t, "global", "")
	if err != nil {
		t.Fatalf("the same slug was refused in the global namespace: %v", err)
	}
	if global.NamespaceType != nil {
		t.Errorf("the global row reports namespace type %v, want none", global.NamespaceType)
	}
	if _, err := create(t, "global again", ""); err == nil {
		t.Error("a duplicate slug in the global namespace was accepted")
	} else {
		requireDuplicateConflict(t, err, "duplicate slug in the global namespace")
	}
}

// TestIntegration_ErrorEnvelopes drives every Is* helper this SDK exports
// against the error the server really sends.
//
// The unit suite asserts these against canned envelopes this repository writes,
// which proves the helper reads the Code field and nothing more. What it cannot
// prove is that the server ever sends that code, on that status, from that call
// -- and getting that wrong sends a caller's error handling down a branch the
// server never takes. Two live examples this pins:
//
//	an unmatched slug is a 400 validation_error, NOT a 404
//	scope_immutable arrives on a 409 whose code is not "conflict"
//
// FOUR HELPERS ARE STRUCTURALLY OUT OF REACH HERE, and are listed rather than
// quietly omitted:
//
//	IsNamespacedWritesDisabled
//	IsNamespaceAPIDisabled   Both are deployment kill-switches
//	                         (OCTONOMY_NAMESPACE_WRITE_ENABLED,
//	                         NAMESPACE_V2_API_ENABLED). This harness must have
//	                         them ON -- with writes disabled every namespaced
//	                         assertion in this file would pass vacuously -- and a
//	                         second container booted the other way costs a full
//	                         Postgres + migrate cycle to reach two error codes.
//	                         The harness asserts the ON state explicitly instead.
//	IsNotReady               Needs a server that answers /health/ready with a
//	                         non-2xx and its own {"status": ...} body, i.e. a
//	                         broken database under a live app. Producing it means
//	                         killing Postgres mid-suite, which ends every other
//	                         test in the file.
//	IsAmbiguousResolution    Not reachable through the HTTP surface on this
//	                         server: with an application named each resolution
//	                         rung holds at most one active row per type, and a
//	                         namespaced request without an application is refused
//	                         at auth. The server's own note says so
//	                         (octonomy/tags/alias_services.py:380-387); the
//	                         remaining multi-match is the type axis, which is
//	                         raised as validation_error. The helper stays for
//	                         direct/internal callers and for a future server that
//	                         relaxes the rule.
func TestIntegration_ErrorEnvelopes(t *testing.T) {
	h := loadHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), suiteTimeout)
	defer cancel()

	wildcard := h.wildcard(t)
	clientA := h.merchantClient(t, h.merchantA)

	// A shared global tag, deactivated, for the inactive_tag case. Shared rather
	// than application-scoped on purpose: validate_tag_for_assignment checks
	// is_active BEFORE the application, and an application-scoped tag would let
	// application_mismatch fire first and hide the case under test.
	inactiveTag, err := wildcard.Tags.Create(ctx, octonomy.TagCreate{
		Name: "integration inactive",
		Slug: uniqueSlug("int-inactive"),
		Type: "label",
	})
	if err != nil {
		t.Fatalf("Tags.Create (to be deactivated): %v", err)
	}
	if err := wildcard.Tags.Delete(ctx, inactiveTag.ID); err != nil {
		t.Fatalf("Tags.Delete (to deactivate): %v", err)
	}

	// A tag belonging to a DIFFERENT application, for application_mismatch. The
	// wildcard grant is tenant-wide, so it may name an application the harness
	// never configured -- applications are opaque caller-chosen ids here.
	otherApplication := h.applicationID + "-other"
	foreignTag, err := wildcard.Tags.Create(ctx, octonomy.TagCreate{
		ApplicationID: octonomy.String(otherApplication),
		Name:          "integration foreign",
		Slug:          uniqueSlug("int-foreign"),
		Type:          "label",
	})
	if err != nil {
		t.Fatalf("Tags.Create (foreign application): %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		if err := wildcard.Tags.Delete(cleanupCtx, foreignTag.ID, octonomy.WithApplication(otherApplication)); err != nil {
			t.Errorf("cleanup: Tags.Delete (foreign application): %v", err)
		}
	})

	// A row whose slug is already taken, for conflict.
	takenSlug := uniqueSlug("int-taken")
	taken, err := wildcard.Tags.Create(ctx, octonomy.TagCreate{
		Name: "integration taken",
		Slug: takenSlug,
		Type: "label",
	})
	if err != nil {
		t.Fatalf("Tags.Create (taken slug): %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		if err := wildcard.Tags.Delete(cleanupCtx, taken.ID); err != nil {
			t.Errorf("cleanup: Tags.Delete (taken slug): %v", err)
		}
	})

	cases := []struct {
		name string
		// code and status are what the SERVER must send. Both are asserted:
		// the code is what the helper reads, and the status is what a caller
		// sees in a log or a proxy, so a change in either is a change in the
		// contract.
		code   string
		status int
		is     func(error) bool
		call   func(ctx context.Context) error
	}{
		{
			name:   "IsNotFound",
			code:   octonomy.CodeNotFound,
			status: 404,
			is:     octonomy.IsNotFound,
			call: func(ctx context.Context) error {
				_, err := wildcard.Tags.Get(ctx, nilUUID)
				return err
			},
		},
		{
			name:   "IsValidation",
			code:   octonomy.CodeValidation,
			status: 400,
			is:     octonomy.IsValidation,
			call: func(ctx context.Context) error {
				// An unmatched slug. Worth pinning precisely because the
				// intuitive branch is the wrong one: this is a 400, not a 404.
				_, err := wildcard.Tags.Resolve(ctx, uniqueSlug("int-no-such"), nil)
				return err
			},
		},
		{
			name:   "IsConflict",
			code:   octonomy.CodeConflict,
			status: 409,
			is:     octonomy.IsConflict,
			call: func(ctx context.Context) error {
				_, err := wildcard.Tags.Create(ctx, octonomy.TagCreate{
					Name: "integration taken again",
					Slug: takenSlug,
					Type: "label",
				})
				return err
			},
		},
		{
			name: "IsAuthError",
			code: octonomy.CodeAuthRequired,
			// 403, NOT 401. DRF answers a rejected bearer token with 403 when the
			// authenticator advertises no WWW-Authenticate challenge, so the
			// status alone cannot tell authentication from authorization here --
			// the CODE can, which is what IsAuthError reads and why it exists
			// separately from IsForbidden.
			status: 403,
			is:     octonomy.IsAuthError,
			call: func(ctx context.Context) error {
				bad := h.client(t, "not-a-real-token", "v2-integration-badtoken")
				_, err := bad.Tags.List(ctx, nil)
				return err
			},
		},
		{
			name:   "IsForbidden",
			code:   octonomy.CodeForbidden,
			status: 403,
			is:     octonomy.IsForbidden,
			call: func(ctx context.Context) error {
				// An exact merchant grant reaching for another merchant. Same
				// status as IsAuthError above and a different code: the token is
				// valid, the partition is not its own.
				_, err := clientA.Tags.List(ctx, nil, h.scoped(h.merchantB.id)...)
				return err
			},
		},
		{
			name:   "IsTenantMismatch",
			code:   octonomy.CodeTenantMismatch,
			status: 400,
			is:     octonomy.IsTenantMismatch,
			call: func(ctx context.Context) error {
				// A real token, a tenant it holds no grant for. 400 rather than
				// 403: the server treats an ungranted tenant header as a
				// malformed request rather than a denial, which is not what the
				// name suggests and is exactly why it is pinned here.
				stranger, err := octonomy.New(octonomy.Config{
					BaseURL:  h.baseURL,
					Token:    h.wildcardToken,
					TenantID: "integration-not-a-granted-tenant",
				})
				if err != nil {
					return err
				}
				_, err = stranger.Tags.List(ctx, nil)
				return err
			},
		},
		{
			name:   "IsApplicationMismatch",
			code:   octonomy.CodeApplicationMismatch,
			status: 400,
			is:     octonomy.IsApplicationMismatch,
			call: func(ctx context.Context) error {
				_, err := wildcard.Assignments.Create(ctx, octonomy.AssignmentCreate{
					ApplicationID: h.applicationID,
					TagID:         octonomy.String(foreignTag.ID),
					ResourceType:  "order",
					ResourceID:    uniqueSlug("int-foreign-order"),
				})
				return err
			},
		},
		{
			name:   "IsInactiveTag",
			code:   octonomy.CodeInactiveTag,
			status: 400,
			is:     octonomy.IsInactiveTag,
			call: func(ctx context.Context) error {
				_, err := wildcard.Assignments.Create(ctx, octonomy.AssignmentCreate{
					ApplicationID: h.applicationID,
					TagID:         octonomy.String(inactiveTag.ID),
					ResourceType:  "order",
					ResourceID:    uniqueSlug("int-inactive-order"),
				})
				return err
			},
		},
		{
			name:   "IsScopeImmutable",
			code:   octonomy.CodeScopeImmutable,
			status: 409,
			is:     octonomy.IsScopeImmutable,
			call: func(ctx context.Context) error {
				// On a TAG. integration_test.go asserts the same refusal on a
				// vocabulary, and the two go through different serializers on the
				// server, so neither stands in for the other.
				_, err := wildcard.Tags.Update(ctx, foreignTag.ID, octonomy.TagUpdate{
					ApplicationID: octonomy.Set(h.applicationID),
				})
				return err
			},
		},
		{
			name:   "IsNamespaceInvalid",
			code:   octonomy.CodeNamespaceInvalid,
			status: 400,
			is:     octonomy.IsNamespaceInvalid,
			call: func(ctx context.Context) error {
				// An overlong namespace id. WithNamespace rejects a blank pair
				// and the reserved "global" type, but deliberately does not
				// enforce the server's column width -- that is a server rule, and
				// AGENTS.md keeps server rules out of this package. So this one
				// namespace_invalid variant does reach the wire, which is what
				// makes the helper testable at all.
				_, err := wildcard.Tags.List(ctx, nil,
					octonomy.WithNamespace(h.namespaceType, strings.Repeat("x", 300)),
					octonomy.WithApplication(h.applicationID))
				return err
			},
		},
		{
			name:   "IsNamespaceNotSupported",
			code:   octonomy.CodeNamespaceNotSupported,
			status: 400,
			is:     octonomy.IsNamespaceNotSupported,
			call: func(ctx context.Context) error {
				// /api/v1 is global-only and rejects X-Namespace-* by name.
				// WithNamespace on a v1 client never gets that far --
				// checkScopeCoherence refuses it before anything is sent, which
				// is the point of that guard. But Config.HTTPClient is exported
				// and an http.RoundTripper is this SDK's sanctioned extension
				// point (see architecture.md), so a wrapper transport can add
				// the headers AFTER the guard has run, and a v1 client built by
				// New then receives the server's real envelope. That is exactly
				// the case IsNamespaceNotSupported's own doc comment describes:
				// reaching it means the namespace came from something other
				// than WithNamespace.
				//
				// It is contrived as a way to CALL Octonomy and nobody should
				// write it. It is the honest way to check the claim this suite
				// exists to check -- that the server really sends this code, on
				// this status, from this route -- and the alternative was to
				// list the helper as unreachable, which would have been untrue.
				v1, err := octonomy.New(octonomy.Config{
					BaseURL:    h.baseURL,
					Token:      h.wildcardToken,
					TenantID:   h.tenantID,
					APIVersion: octonomy.APIV1,
					HTTPClient: &http.Client{
						Timeout:   30 * time.Second,
						Transport: namespaceInjectingTransport(h.namespaceType, h.merchantA.id),
					},
				})
				if err != nil {
					return err
				}
				_, err = v1.Tags.List(ctx, nil)
				return err
			},
		},
		{
			name: "IsUnexpectedStatus",
			code: octonomy.CodeUnexpectedStatus,
			// A route the server does not serve. Django answers with an HTML 404
			// carrying no error envelope, and the SDK must NOT infer not_found
			// from the bare status: that is #7, where an unrouted /api/v2 made a
			// caller's not-found branch read a missing deployment as an empty
			// taxonomy.
			status: 404,
			is:     octonomy.IsUnexpectedStatus,
			call: func(ctx context.Context) error {
				unmounted, err := octonomy.New(octonomy.Config{
					BaseURL:  h.baseURL + "/not-a-mounted-prefix",
					Token:    h.wildcardToken,
					TenantID: h.tenantID,
				})
				if err != nil {
					return err
				}
				_, err = unmounted.Tags.List(ctx, nil)
				return err
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call(ctx)
			apiErr := requireAPIError(t, err, tc.name)

			if !tc.is(err) {
				t.Errorf("%s(err) = false for the server's own error: code = %q", tc.name, apiErr.Code)
			}
			if apiErr.Code != tc.code {
				t.Errorf("code = %q, want %q", apiErr.Code, tc.code)
			}
			if apiErr.StatusCode != tc.status {
				t.Errorf("status = %d, want %d", apiErr.StatusCode, tc.status)
			}
			// Every envelope this SDK decodes carries a message, and callers
			// surface it. An empty one means the SDK found the code but dropped
			// the human-readable half.
			if strings.TrimSpace(apiErr.Message) == "" {
				t.Error("the decoded error carried no message")
			}
			// CodeUnexpectedStatus is the one case with no envelope to carry a
			// request id, since the response never reached Octonomy's own error
			// handler.
			if tc.code != octonomy.CodeUnexpectedStatus && apiErr.RequestID == "" {
				t.Error("the decoded error carried no request id: the server stamps one on every envelope, and it is how a caller correlates a failure with the server's logs")
			}
		})
	}
}

// TestIntegration_DeactivatedParentOrphansItsLiveChildren proves the shape
// TagTree.Orphans exists for, against the server that produces it (#20).
//
// BuildTagTree's central claim is that a missing parent is ORDINARY rather than
// a data defect, and the commonest way a healthy Octonomy produces one is its
// own delete: deletion is deactivation, the cascade reaches the tag's ALIASES
// ONLY and never its children (octonomy/tags/services.py -- deactivate_tag
// sweeps TagAlias and nothing else), and an unfiltered list returns active rows
// only (filter_tags applies is_active=True when the parameter is absent). Put
// together, a live child comes back from the default list carrying a ParentID
// that the same list does not contain.
//
// No fixture can settle that: it is two server behaviours interacting, and a
// canned response asserting it would only assert what this SDK already
// believes. The design DECISION it grounds -- promote the orphan to a root and
// name it in Orphans, never drop it -- is what makes the helper safe to render
// a category browser from.
func TestIntegration_DeactivatedParentOrphansItsLiveChildren(t *testing.T) {
	h := loadHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), suiteTimeout)
	defer cancel()

	clientA := h.merchantClient(t, h.merchantA)
	ns := octonomy.WithNamespace(h.namespaceType, h.merchantA.id)
	scoped := h.scoped(h.merchantA.id)

	// One prefix shared by both slugs, so a single q filter fetches exactly the
	// rows this test wrote and nothing another run left behind.
	prefix := uniqueSlug("int-orphan")
	newTag := func(t *testing.T, name, suffix string, parentID *string) octonomy.Tag {
		t.Helper()
		tag, err := clientA.Tags.Create(ctx, octonomy.TagCreate{
			ApplicationID: octonomy.String(h.applicationID),
			Name:          name,
			Slug:          prefix + suffix,
			Type:          "category",
			ParentID:      parentID,
		}, ns)
		if err != nil {
			t.Fatalf("Tags.Create(%s): %v", name, err)
		}
		t.Cleanup(func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
			defer cancel()
			// Tolerant of an already-deactivated row: this test deletes the
			// parent itself, and the server accepts a repeat delete.
			if err := clientA.Tags.Delete(cleanupCtx, tag.ID, scoped...); err != nil {
				t.Errorf("cleanup: Tags.Delete(%s): %v", name, err)
			}
		})
		return *tag
	}

	parent := newTag(t, "integration orphan parent", "-parent", nil)
	child := newTag(t, "integration orphan child", "-child", octonomy.String(parent.ID))

	// fetch is the default list -- no IsActive -- which is the call a consumer
	// renders a browser from and the one whose filtering this test is about.
	fetch := func(t *testing.T) []octonomy.Tag {
		t.Helper()
		page, err := clientA.Tags.List(ctx, &octonomy.TagListParams{
			Query:       octonomy.String(prefix),
			ListOptions: octonomy.ListOptions{Limit: 200},
		}, scoped...)
		if err != nil {
			t.Fatalf("Tags.List: %v", err)
		}
		return page.Data
	}

	before := fetch(t)
	if len(before) != 2 {
		t.Fatalf("the default list returned %d rows before the delete, want 2: the assertion below would prove nothing", len(before))
	}
	intact, err := octonomy.BuildTagTree(before)
	if err != nil {
		t.Fatalf("BuildTagTree on two live rows: %v", err)
	}
	if len(intact.Orphans) != 0 {
		t.Errorf("Orphans = %d on a complete fetch, want 0", len(intact.Orphans))
	}
	if len(intact.Roots) != 1 || intact.Roots[0].Tag.ID != parent.ID {
		t.Fatalf("Roots = %v, want just the parent", intact.Roots)
	}
	if node := intact.Node(child.ID); node == nil || node.Parent == nil || node.Parent.Tag.ID != parent.ID {
		t.Errorf("the child is not attached to its parent: %#v", node)
	}

	if err := clientA.Tags.Delete(ctx, parent.ID, scoped...); err != nil {
		t.Fatalf("Tags.Delete(parent): %v", err)
	}

	// The server's half of the claim: the child survives its parent's
	// deactivation, still carries the ParentID, and the default list no longer
	// contains the parent.
	after := fetch(t)
	if len(after) != 1 {
		t.Fatalf("the default list returned %d rows after deleting the parent, want 1 (the live child): %v", len(after), after)
	}
	if after[0].ID != child.ID {
		t.Fatalf("the surviving row is %s, want the child %s", after[0].ID, child.ID)
	}
	if !after[0].IsActive {
		t.Error("the child is inactive: the deactivation cascaded to a child, which the server is not supposed to do")
	}
	if after[0].ParentID == nil || *after[0].ParentID != parent.ID {
		t.Fatalf("the child's ParentID is %v, want the deactivated parent %s: without it there is no orphan to assemble", after[0].ParentID, parent.ID)
	}

	// The SDK's half: kept, promoted, and named -- not dropped.
	orphaned, err := octonomy.BuildTagTree(after)
	if err != nil {
		t.Fatalf("BuildTagTree on a row whose parent was deactivated: %v", err)
	}
	if orphaned.Len() != len(after) {
		t.Errorf("Len = %d for %d fetched rows: a tag was dropped", orphaned.Len(), len(after))
	}
	if len(orphaned.Orphans) != 1 || orphaned.Orphans[0].Tag.ID != child.ID {
		t.Fatalf("Orphans = %v, want just the child", orphaned.Orphans)
	}
	if !orphaned.Node(child.ID).IsOrphan() {
		t.Error("IsOrphan reports false for a tag whose parent the server has deactivated")
	}
	if len(orphaned.Roots) != 1 || orphaned.Roots[0].Tag.ID != child.ID {
		t.Errorf("Roots = %v, want the orphan promoted so that walking Roots still reaches it", orphaned.Roots)
	}

	// And the documented remedy: fetch the missing parent and rebuild. Get
	// reads deactivated rows, which is what makes this recoverable at all.
	deactivated, err := clientA.Tags.Get(ctx, parent.ID, scoped...)
	if err != nil {
		t.Fatalf("Tags.Get(deactivated parent): %v", err)
	}
	if deactivated.IsActive {
		t.Fatal("the parent is still active: the delete did not deactivate it")
	}
	repaired, err := octonomy.BuildTagTree(append(after, *deactivated))
	if err != nil {
		t.Fatalf("BuildTagTree after refetching the parent: %v", err)
	}
	if len(repaired.Orphans) != 0 {
		t.Errorf("Orphans = %v after refetching the missing parent, want none", repaired.Orphans)
	}
	if node := repaired.Node(child.ID); node == nil || node.Depth != 1 {
		t.Errorf("the child did not reattach under the refetched parent: %#v", node)
	}
}

// TestIntegration_ParentCycleIsReachableAndRefused proves ErrTagCycle is not
// defensive programming (#20).
//
// The database forbids the one-hop case only -- the tag_parent_cannot_be_self
// check constraint, parent_id != id -- and validate_tag_parent checks tenant,
// application and namespace compatibility on a parent without ever walking the
// ancestry. So A -> B -> A is two ordinary, individually valid PATCHes, and the
// server answers 200 to the one that closes the ring.
//
// That is the whole justification for refusing a cycle rather than breaking it
// quietly: the tags really can arrive this way, and every tag in a cycle has a
// parent inside the set, so a naive assembler finds NO ROOT for any of them and
// returns a tree silently missing rows.
func TestIntegration_ParentCycleIsReachableAndRefused(t *testing.T) {
	h := loadHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), suiteTimeout)
	defer cancel()

	clientA := h.merchantClient(t, h.merchantA)
	ns := octonomy.WithNamespace(h.namespaceType, h.merchantA.id)
	scoped := h.scoped(h.merchantA.id)

	prefix := uniqueSlug("int-cycle")
	newTag := func(t *testing.T, name, suffix string, parentID *string) octonomy.Tag {
		t.Helper()
		tag, err := clientA.Tags.Create(ctx, octonomy.TagCreate{
			ApplicationID: octonomy.String(h.applicationID),
			Name:          name,
			Slug:          prefix + suffix,
			Type:          "category",
			ParentID:      parentID,
		}, ns)
		if err != nil {
			t.Fatalf("Tags.Create(%s): %v", name, err)
		}
		t.Cleanup(func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
			defer cancel()
			if err := clientA.Tags.Delete(cleanupCtx, tag.ID, scoped...); err != nil {
				t.Errorf("cleanup: Tags.Delete(%s): %v", name, err)
			}
		})
		return *tag
	}

	first := newTag(t, "integration cycle first", "-first", nil)
	second := newTag(t, "integration cycle second", "-second", octonomy.String(first.ID))

	// The move the server does not stop. If this ever starts failing, the
	// server grew an ancestry check and ErrTagCycle became unreachable through
	// the API -- read the change before relaxing anything here, because the
	// helper still has to survive a hand-built slice.
	//
	// ApplicationID is RESTATED rather than omitted, and it is load-bearing
	// under an exact merchant grant: a write takes its application scope from
	// the body (WithApplication is refused on a request that carries one), and
	// the grant is checked against that pair, so a PATCH naming no application
	// asks for tenant-wide authority and is answered 403 forbidden. Restating
	// the value the row already holds is not a scope CHANGE, so it does not
	// trip scope_immutable.
	closed, err := clientA.Tags.Update(ctx, first.ID, octonomy.TagUpdate{
		ApplicationID: octonomy.Set(h.applicationID),
		ParentID:      octonomy.Set(second.ID),
	}, ns)
	if err != nil {
		t.Fatalf("Tags.Update closing the cycle: %v -- if the server now rejects this, ErrTagCycle's justification has changed", err)
	}
	if closed.ParentID == nil || *closed.ParentID != second.ID {
		t.Fatalf("the server echoed ParentID %v, want %s: the cycle was not stored", closed.ParentID, second.ID)
	}

	page, err := clientA.Tags.List(ctx, &octonomy.TagListParams{
		Query:       octonomy.String(prefix),
		ListOptions: octonomy.ListOptions{Limit: 200},
	}, scoped...)
	if err != nil {
		t.Fatalf("Tags.List: %v", err)
	}
	if len(page.Data) != 2 {
		t.Fatalf("the list returned %d rows, want the 2 this test wrote", len(page.Data))
	}
	// Neither row is a root: that is what a naive assembler loses silently.
	for _, row := range page.Data {
		if row.ParentID == nil {
			t.Fatalf("tag %s came back with no parent: the cycle is not what this test is asserting on", row.Slug)
		}
	}

	tree, err := octonomy.BuildTagTree(page.Data)
	if err == nil {
		t.Fatalf("BuildTagTree assembled a cycle into a tree with %d root(s)", len(tree.Roots))
	}
	if tree != nil {
		t.Error("BuildTagTree returned a non-nil tree alongside the cycle error")
	}
	if !errors.Is(err, octonomy.ErrTagCycle) {
		t.Fatalf("error does not match ErrTagCycle: %v", err)
	}
	for _, id := range []string{first.ID, second.ID} {
		if !strings.Contains(err.Error(), id) {
			t.Errorf("the message does not name %s, so an operator cannot find the rows to fix: %v", id, err)
		}
	}

	// Breaking the ring makes the same two rows assemble, and CLEARING the
	// offending link is how an operator does it: ParentID is an Optional, so
	// Null[string]() sends "parent_id": null and the row becomes a root (#64).
	//
	// That was inexpressible until #64. The field was a *string with omitempty
	// where nil already meant "leave it alone", so the repair had to be a
	// re-point at some third tag or a deactivation -- the obvious move, on data
	// an operator is holding precisely because it is wrong, was the one the SDK
	// could not make. ApplicationID is restated for the same reason as above.
	if _, err := clientA.Tags.Update(ctx, first.ID, octonomy.TagUpdate{
		ApplicationID: octonomy.Set(h.applicationID),
		ParentID:      octonomy.Null[string](),
	}, ns); err != nil {
		t.Fatalf("Tags.Update clearing the cycle's parent link: %v", err)
	}

	repaired, err := clientA.Tags.List(ctx, &octonomy.TagListParams{
		Query:       octonomy.String(prefix),
		ListOptions: octonomy.ListOptions{Limit: 200},
	}, scoped...)
	if err != nil {
		t.Fatalf("Tags.List after breaking the cycle: %v", err)
	}
	tree, err = octonomy.BuildTagTree(repaired.Data)
	if err != nil {
		t.Fatalf("BuildTagTree after breaking the cycle: %v", err)
	}
	if tree.Len() != len(repaired.Data) {
		t.Errorf("Len = %d for %d rows: a tag was dropped", tree.Len(), len(repaired.Data))
	}
	if len(tree.Roots) != 1 || tree.Roots[0].Tag.ID != first.ID {
		t.Errorf("Roots = %v, want the tag whose parent link was cleared", tree.Roots)
	}
	if node := tree.Node(second.ID); node == nil || node.Depth != 1 {
		t.Errorf("the chain did not reassemble as first -> second: %#v", node)
	}
}

// Which fields the server will accept a NULL for, and which it refuses (#64).
//
// This is the claim the three-state Optional rests on, and only a real server
// can settle it. A canned test proves that "parent_id": null leaves the client;
// that the server then CLEARS the column, and answers 400 for the fields it does
// not mark nullable, is its behavior rather than the SDK's -- and the doc
// comments on TagUpdate, VocabularyUpdate and TagAliasUpdate publish a table of
// exactly that, which nothing else would catch drifting.
//
// The vendored v2 contract marks seven properties nullable across the three
// patch schemas. Three of the seven are application_id, refused on every
// resource as a scope change, so the REACHABLE set is four -- and this walks all
// four plus a representative refusal on every resource.
//
// It uses the WILDCARD token on purpose. The question here is which fields the
// server will null, not who may write them; an exact merchant grant would add an
// authorization failure mode to every row of the table and to none of the
// assertions.
func TestIntegration_NullClearsOnlyTheNullableFields(t *testing.T) {
	h := loadHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), suiteTimeout)
	defer cancel()

	client := h.wildcard(t)
	prefix := uniqueSlug("int-null")

	cleanup := func(what string, del func(context.Context) error) {
		t.Cleanup(func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
			defer cancel()
			if err := del(cleanupCtx); err != nil {
				t.Errorf("cleanup: delete %s: %v", what, err)
			}
		})
	}

	vocab, err := client.Vocabularies.Create(ctx, octonomy.VocabularyCreate{
		Name:        "null clears vocabulary",
		Slug:        prefix + "-vocab",
		Description: octonomy.String("set so that clearing it is visible"),
	})
	if err != nil {
		t.Fatalf("Vocabularies.Create: %v", err)
	}
	cleanup("vocabulary", func(ctx context.Context) error { return client.Vocabularies.Delete(ctx, vocab.ID) })

	parent, err := client.Tags.Create(ctx, octonomy.TagCreate{
		Name: "null clears parent", Slug: prefix + "-parent", Type: "category",
	})
	if err != nil {
		t.Fatalf("Tags.Create(parent): %v", err)
	}
	cleanup("parent tag", func(ctx context.Context) error { return client.Tags.Delete(ctx, parent.ID) })

	tag, err := client.Tags.Create(ctx, octonomy.TagCreate{
		Name: "null clears child", Slug: prefix + "-child", Type: "category",
		Description:  octonomy.String("set so that clearing it is visible"),
		ParentID:     octonomy.String(parent.ID),
		VocabularyID: octonomy.String(vocab.ID),
		Metadata:     octonomy.Metadata{"source": "int-null"},
	})
	if err != nil {
		t.Fatalf("Tags.Create(child): %v", err)
	}
	cleanup("child tag", func(ctx context.Context) error { return client.Tags.Delete(ctx, tag.ID) })

	alias, err := client.Aliases.Create(ctx, octonomy.TagAliasCreate{
		TagID: tag.ID, Name: "null clears alias", Slug: prefix + "-alias",
	})
	if err != nil {
		t.Fatalf("Aliases.Create: %v", err)
	}
	cleanup("alias", func(ctx context.Context) error { return client.Aliases.Delete(ctx, alias.ID) })

	// The row starts out fully linked, or the clears below would pass against a
	// server that ignores the null entirely.
	if tag.ParentID == nil || tag.VocabularyID == nil || tag.Description == nil {
		t.Fatalf("the fixture tag is not fully linked, so clearing proves nothing: %+v", tag)
	}

	// --- the four reachable clears ------------------------------------------

	// Every clear is RE-READ rather than believed. What an Update returns is the
	// serializer's view of the row it thinks it wrote, so a server that cleared
	// an in-memory instance without persisting would answer with the field gone
	// and still hand the next request the old value. Persistence is exactly the
	// class of claim this suite exists for, and the PATCH response cannot settle
	// it.
	readTag := func(t *testing.T) *octonomy.Tag {
		t.Helper()
		got, err := client.Tags.Get(ctx, tag.ID)
		if err != nil {
			t.Fatalf("Tags.Get re-reading the row: %v", err)
		}
		return got
	}
	readVocab := func(t *testing.T) *octonomy.Vocabulary {
		t.Helper()
		got, err := client.Vocabularies.Get(ctx, vocab.ID)
		if err != nil {
			t.Fatalf("Vocabularies.Get re-reading the row: %v", err)
		}
		return got
	}

	t.Run("tag parent_id", func(t *testing.T) {
		got, err := client.Tags.Update(ctx, tag.ID, octonomy.TagUpdate{ParentID: octonomy.Null[string]()})
		if err != nil {
			t.Fatalf("Tags.Update clearing ParentID: %v -- the field is nullable in PatchedTagPatch", err)
		}
		if got.ParentID != nil {
			t.Errorf("ParentID = %q after Null[string](), want it cleared", *got.ParentID)
		}
		if reread := readTag(t); reread.ParentID != nil {
			t.Errorf("ParentID = %q on a re-read: the PATCH response cleared it, the stored row did not",
				*reread.ParentID)
		}
	})

	t.Run("tag vocabulary_id", func(t *testing.T) {
		got, err := client.Tags.Update(ctx, tag.ID, octonomy.TagUpdate{VocabularyID: octonomy.Null[string]()})
		if err != nil {
			t.Fatalf("Tags.Update clearing VocabularyID: %v", err)
		}
		if got.VocabularyID != nil {
			t.Errorf("VocabularyID = %q after Null[string](), want it cleared", *got.VocabularyID)
		}
		if reread := readTag(t); reread.VocabularyID != nil {
			t.Errorf("VocabularyID = %q on a re-read: the PATCH response cleared it, the stored row did not",
				*reread.VocabularyID)
		}
	})

	t.Run("tag description", func(t *testing.T) {
		got, err := client.Tags.Update(ctx, tag.ID, octonomy.TagUpdate{Description: octonomy.Null[string]()})
		if err != nil {
			t.Fatalf("Tags.Update clearing Description: %v", err)
		}
		if got.Description != nil {
			t.Errorf("Description = %q after Null[string](), want it cleared", *got.Description)
		}
		reread := readTag(t)
		if reread.Description != nil {
			t.Errorf("Description = %q on a re-read: the PATCH response cleared it, the stored row did not",
				*reread.Description)
		}
		// The neighbouring columns are untouched: a PATCH clears the field it
		// names and nothing else.
		if reread.Name != tag.Name || reread.Slug != tag.Slug || reread.Type != tag.Type {
			t.Errorf("clearing Description also moved another column: %+v", reread)
		}
		if got := reread.Metadata["source"]; got != "int-null" {
			t.Errorf("Metadata[source] = %v after clearing Description, want it untouched", got)
		}
	})

	t.Run("vocabulary description", func(t *testing.T) {
		got, err := client.Vocabularies.Update(ctx, vocab.ID, octonomy.VocabularyUpdate{
			Description: octonomy.Null[string](),
		})
		if err != nil {
			t.Fatalf("Vocabularies.Update clearing Description: %v", err)
		}
		if got.Description != nil {
			t.Errorf("Description = %q after Null[string](), want it cleared", *got.Description)
		}
		if reread := readVocab(t); reread.Description != nil {
			t.Errorf("Description = %q on a re-read: the PATCH response cleared it, the stored row did not",
				*reread.Description)
		}
	})

	// A clear is not a one-way door: the same field takes a value again, which
	// is what makes ParentID a usable repair for a cycle rather than a way to
	// strand a row.
	t.Run("a cleared link can be set again", func(t *testing.T) {
		got, err := client.Tags.Update(ctx, tag.ID, octonomy.TagUpdate{ParentID: octonomy.Set(parent.ID)})
		if err != nil {
			t.Fatalf("Tags.Update re-pointing ParentID: %v", err)
		}
		if got.ParentID == nil || *got.ParentID != parent.ID {
			t.Errorf("ParentID = %v, want %s", got.ParentID, parent.ID)
		}
		if reread := readTag(t); reread.ParentID == nil || *reread.ParentID != parent.ID {
			t.Errorf("ParentID = %v on a re-read, want %s", reread.ParentID, parent.ID)
		}
	})

	// --- the refusals --------------------------------------------------------

	// Every one of these is expressible in the type system and answered by the
	// SERVER, which is deliberate: rejecting them in the client would be
	// re-running server validation here, and the server names the field.
	refusals := []struct {
		name  string
		field string
		call  func() error
	}{
		{"tag name", "name", func() error {
			_, err := client.Tags.Update(ctx, tag.ID, octonomy.TagUpdate{Name: octonomy.Null[string]()})
			return err
		}},
		{"tag slug", "slug", func() error {
			_, err := client.Tags.Update(ctx, tag.ID, octonomy.TagUpdate{Slug: octonomy.Null[string]()})
			return err
		}},
		{"tag type", "type", func() error {
			_, err := client.Tags.Update(ctx, tag.ID, octonomy.TagUpdate{Type: octonomy.Null[string]()})
			return err
		}},
		{"tag is_active", "is_active", func() error {
			_, err := client.Tags.Update(ctx, tag.ID, octonomy.TagUpdate{IsActive: octonomy.Null[bool]()})
			return err
		}},
		// The one a caller is most likely to reach for by analogy: metadata is
		// emptied with Set(Metadata{}), never with Null. Both doc comments say
		// so; this is why.
		{"tag metadata", "metadata", func() error {
			_, err := client.Tags.Update(ctx, tag.ID, octonomy.TagUpdate{Metadata: octonomy.Null[octonomy.Metadata]()})
			return err
		}},
		{"vocabulary name", "name", func() error {
			_, err := client.Vocabularies.Update(ctx, vocab.ID, octonomy.VocabularyUpdate{Name: octonomy.Null[string]()})
			return err
		}},
		{"vocabulary metadata", "metadata", func() error {
			_, err := client.Vocabularies.Update(ctx, vocab.ID, octonomy.VocabularyUpdate{
				Metadata: octonomy.Null[octonomy.Metadata](),
			})
			return err
		}},
		// TagAliasUpdate has NO clearable field at all -- application_id is its
		// only nullable property and the server refuses that one as a scope
		// change, which is what the struct's doc comment claims.
		{"alias tag_id", "tag_id", func() error {
			_, err := client.Aliases.Update(ctx, alias.ID, octonomy.TagAliasUpdate{TagID: octonomy.Null[string]()})
			return err
		}},
		{"alias name", "name", func() error {
			_, err := client.Aliases.Update(ctx, alias.ID, octonomy.TagAliasUpdate{Name: octonomy.Null[string]()})
			return err
		}},
	}
	for _, tc := range refusals {
		t.Run("null "+tc.name+" is refused", func(t *testing.T) {
			err := tc.call()
			if !octonomy.IsValidation(err) {
				t.Fatalf("error = %v, want a validation_error: the field is not nullable in the contract", err)
			}
			apiErr := requireAPIError(t, err, "null "+tc.name)
			if got := detailStrings(apiErr.Details, tc.field); len(got) == 0 {
				t.Errorf("details name no %q key, so a caller cannot tell which field was refused: %s",
					tc.field, joinDetails(apiErr.Details))
			}
		})
	}

	// --- application_id, the nullable property that is refused anyway --------

	// Three of the contract's seven nullable properties are application_id, and
	// the server treats it as scope rather than data. The refusal is on the
	// CHANGE, not on the literal null, which is why both halves are asserted:
	// nulling it on a row that has one is a 409, and nulling it on a row that is
	// already tenant-shared is an ordinary 200 no-op. Reading only the first
	// would leave the doc comment claiming a refusal the server does not always
	// make.
	t.Run("application_id", func(t *testing.T) {
		// All three resources, not one standing in for the others: the doc
		// comment on every *Update struct claims this refusal, the server's
		// patch serializers are separate code paths, and TagAliasUpdate's claim
		// that it has NO clearable field rests entirely on this one.
		scopedTag, err := client.Tags.Create(ctx, octonomy.TagCreate{
			ApplicationID: octonomy.String(h.applicationID),
			Name:          "null clears scoped", Slug: prefix + "-scoped", Type: "category",
		})
		if err != nil {
			t.Fatalf("Tags.Create(application-scoped): %v", err)
		}
		cleanup("application-scoped tag", func(ctx context.Context) error {
			return client.Tags.Delete(ctx, scopedTag.ID)
		})

		scopedVocab, err := client.Vocabularies.Create(ctx, octonomy.VocabularyCreate{
			ApplicationID: octonomy.String(h.applicationID),
			Name:          "null clears scoped vocabulary", Slug: prefix + "-scoped-vocab",
		})
		if err != nil {
			t.Fatalf("Vocabularies.Create(application-scoped): %v", err)
		}
		cleanup("application-scoped vocabulary", func(ctx context.Context) error {
			return client.Vocabularies.Delete(ctx, scopedVocab.ID)
		})

		// The alias has to hang off the application-scoped TAG: a tenant-shared
		// target would not accept an application-scoped alias.
		scopedAlias, err := client.Aliases.Create(ctx, octonomy.TagAliasCreate{
			ApplicationID: octonomy.String(h.applicationID),
			TagID:         scopedTag.ID,
			Name:          "null clears scoped alias", Slug: prefix + "-scoped-alias",
		})
		if err != nil {
			t.Fatalf("Aliases.Create(application-scoped): %v", err)
		}
		cleanup("application-scoped alias", func(ctx context.Context) error {
			return client.Aliases.Delete(ctx, scopedAlias.ID)
		})

		refusals := []struct {
			name string
			call func() error
		}{
			{"tag", func() error {
				_, err := client.Tags.Update(ctx, scopedTag.ID, octonomy.TagUpdate{
					ApplicationID: octonomy.Null[string](),
				})
				return err
			}},
			{"vocabulary", func() error {
				_, err := client.Vocabularies.Update(ctx, scopedVocab.ID, octonomy.VocabularyUpdate{
					ApplicationID: octonomy.Null[string](),
				})
				return err
			}},
			{"alias", func() error {
				_, err := client.Aliases.Update(ctx, scopedAlias.ID, octonomy.TagAliasUpdate{
					ApplicationID: octonomy.Null[string](),
				})
				return err
			}},
		}
		for _, tc := range refusals {
			t.Run(tc.name+" with an application is 409", func(t *testing.T) {
				if err := tc.call(); !octonomy.IsScopeImmutable(err) {
					t.Errorf("error = %v, want scope_immutable", err)
				}
			})
		}

		// The other half, and the reason this is not a one-line assertion: the
		// server refuses a scope CHANGE, not the literal null, so the same null
		// on a row that is ALREADY tenant-shared is an ordinary 200 no-op.
		// Asserting only the refusal would leave every doc comment claiming one
		// the server does not always make.
		t.Run("a row that has none is a 200 no-op", func(t *testing.T) {
			shared, err := client.Tags.Update(ctx, tag.ID, octonomy.TagUpdate{
				ApplicationID: octonomy.Null[string](),
			})
			if err != nil {
				t.Fatalf("clearing ApplicationID on a row that has none: %v -- "+
					"the server refuses a scope CHANGE, not the null", err)
			}
			if shared.ApplicationID != nil {
				t.Errorf("ApplicationID = %q, want it still unset", *shared.ApplicationID)
			}
			if reread := readTag(t); reread.ApplicationID != nil {
				t.Errorf("ApplicationID = %q on a re-read, want it still unset", *reread.ApplicationID)
			}
		})
	})
}
