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
// Phrasing every endpoint as the same question is what makes the isolation test
// exhaustive rather than anecdotal. The endpoints answer differently when a row
// is out of scope -- some 404, some return an empty page, resolution returns a
// 400 -- and none of those differences is the property under test. What matters
// is only whether the row came back.
type readProbe struct {
	name string
	find func(ctx context.Context, c *octonomy.Client, readNS string, want namespaceFixture) (bool, error)
}

// readProbes covers every authenticated read method this SDK exposes.
//
// The complete list, and why the one exclusion is not a gap:
//
//	Vocabularies  Get, List
//	Tags          Get, List, Resolve, ListAliases, ListResources, ListAuditLogs
//	Aliases       Get, List
//	Resources     ListTags, ListAuditLogs
//	AuditLogs     List
//	Health        Live, Ready  -- EXCLUDED: unauthenticated, unversioned, and
//	                              outside the namespace axis entirely. They send
//	                              no X-Namespace-* headers and read no rows, so
//	                              "can merchant A see merchant B" is not a
//	                              question they can be asked.
//
// A new read method is expected to arrive here alongside its resource file. A
// read endpoint nobody probed is the one a cross-merchant leak lives in.
//
// PAGINATION IS NOT AN EXHAUSTIVENESS ARGUMENT. Every list probe below either
// narrows to the fixture with an exact server-side filter, or walks the whole
// collection with octonomy.Each. A single page -- even at the server's 200-row
// clamp -- proves only that the row is not on the FIRST page, and "not on page
// one" is not "not visible": a long-lived harness, or a run that leaked
// fixtures, pushes a genuinely leaked row past the boundary and turns the leak
// into a pass. The routes scoped to one fixture row (a tag's aliases, a
// resource's tags, one entity's audit rows) hold a handful of rows by
// construction and are read with an explicit generous limit.
func readProbes(h harness) []readProbe {
	// For the routes that address ONE fixture row. Not an exhaustiveness claim:
	// these collections hold what this run just created.
	const scopedLimit = 200

	return []readProbe{
		{
			name: "Tags.List",
			find: func(ctx context.Context, c *octonomy.Client, readNS string, want namespaceFixture) (bool, error) {
				// Filtered by the fixture's own slug rather than paged: an empty
				// page under an exact filter means the row is genuinely not
				// visible, with no page-boundary caveat at all.
				page, err := c.Tags.List(ctx, &octonomy.TagListParams{
					Slug:        octonomy.String(want.tag.Slug),
					ListOptions: octonomy.ListOptions{Limit: scopedLimit},
				}, h.scoped(readNS)...)
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
			name: "Tags.Get",
			find: func(ctx context.Context, c *octonomy.Client, readNS string, want namespaceFixture) (bool, error) {
				row, err := c.Tags.Get(ctx, want.tag.ID, h.scoped(readNS)...)
				if err != nil {
					return false, err
				}
				return row.ID == want.tag.ID, nil
			},
		},
		{
			name: "Tags.Resolve",
			find: func(ctx context.Context, c *octonomy.Client, readNS string, want namespaceFixture) (bool, error) {
				// The slug, not the id: resolution is the one read that takes a
				// caller-chosen name, which makes it the one a caller could most
				// plausibly use to probe another merchant's vocabulary.
				resolved, err := c.Tags.Resolve(ctx, want.tag.Slug, nil, h.scoped(readNS)...)
				if err != nil {
					return false, err
				}
				return resolved.Tag.ID == want.tag.ID, nil
			},
		},
		{
			name: "Tags.ListAliases",
			find: func(ctx context.Context, c *octonomy.Client, readNS string, want namespaceFixture) (bool, error) {
				page, err := c.Tags.ListAliases(ctx, want.tag.ID, &octonomy.TagListAliasesParams{
					ListOptions: octonomy.ListOptions{Limit: scopedLimit},
				}, h.scoped(readNS)...)
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
			name: "Tags.ListResources",
			find: func(ctx context.Context, c *octonomy.Client, readNS string, want namespaceFixture) (bool, error) {
				page, err := c.Tags.ListResources(ctx, want.tag.ID, &octonomy.TagListResourcesParams{
					ListOptions: octonomy.ListOptions{Limit: scopedLimit},
				}, h.scoped(readNS)...)
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
			name: "Tags.ListAuditLogs",
			find: func(ctx context.Context, c *octonomy.Client, readNS string, want namespaceFixture) (bool, error) {
				page, err := c.Tags.ListAuditLogs(ctx, want.tag.ID, &octonomy.TagListAuditLogsParams{
					ListOptions: octonomy.ListOptions{Limit: scopedLimit},
				}, h.scoped(readNS)...)
				if err != nil {
					return false, err
				}
				// Any row at all: the audit table records what happened to this
				// tag, so a single row is another merchant's history leaking.
				return len(page.Data) > 0, nil
			},
		},
		{
			name: "Aliases.List",
			find: func(ctx context.Context, c *octonomy.Client, readNS string, want namespaceFixture) (bool, error) {
				// Exact slug filter, same reasoning as Tags.List.
				page, err := c.Aliases.List(ctx, &octonomy.TagAliasListParams{
					Slug:        octonomy.String(want.alias.Slug),
					ListOptions: octonomy.ListOptions{Limit: scopedLimit},
				}, h.scoped(readNS)...)
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
			name: "Aliases.Get",
			find: func(ctx context.Context, c *octonomy.Client, readNS string, want namespaceFixture) (bool, error) {
				row, err := c.Aliases.Get(ctx, want.alias.ID, h.scoped(readNS)...)
				if err != nil {
					return false, err
				}
				return row.ID == want.alias.ID, nil
			},
		},
		{
			name: "Vocabularies.List",
			find: func(ctx context.Context, c *octonomy.Client, readNS string, want namespaceFixture) (bool, error) {
				// VocabularyListParams exposes no slug or free-text filter (#36
				// covers the gap), so this is the one probe that must WALK the
				// collection rather than narrow it. Each stops on the server's
				// own end-of-collection signal, so "absent" here means absent
				// from the endpoint, not merely from page one.
				found := false
				_, err := octonomy.Each(ctx, octonomy.ListOptions{Limit: scopedLimit},
					func(ctx context.Context, o octonomy.ListOptions) (*octonomy.List[octonomy.Vocabulary], error) {
						return c.Vocabularies.List(ctx, &octonomy.VocabularyListParams{ListOptions: o}, h.scoped(readNS)...)
					},
					func(row octonomy.Vocabulary) error {
						if row.ID == want.vocabulary.ID {
							found = true
						}
						return nil
					})
				if err != nil {
					return false, err
				}
				return found, nil
			},
		},
		{
			name: "Vocabularies.Get",
			find: func(ctx context.Context, c *octonomy.Client, readNS string, want namespaceFixture) (bool, error) {
				row, err := c.Vocabularies.Get(ctx, want.vocabulary.ID, h.scoped(readNS)...)
				if err != nil {
					return false, err
				}
				return row.ID == want.vocabulary.ID, nil
			},
		},
		{
			name: "Resources.ListTags",
			find: func(ctx context.Context, c *octonomy.Client, readNS string, want namespaceFixture) (bool, error) {
				page, err := c.Resources.ListTags(ctx, want.resourceType, want.resourceID,
					&octonomy.ResourceListTagsParams{ListOptions: octonomy.ListOptions{Limit: scopedLimit}},
					h.scoped(readNS)...)
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
			name: "Resources.ListAuditLogs",
			find: func(ctx context.Context, c *octonomy.Client, readNS string, want namespaceFixture) (bool, error) {
				page, err := c.Resources.ListAuditLogs(ctx, want.resourceType, want.resourceID,
					&octonomy.ResourceListAuditLogsParams{ListOptions: octonomy.ListOptions{Limit: scopedLimit}},
					h.scoped(readNS)...)
				if err != nil {
					return false, err
				}
				return len(page.Data) > 0, nil
			},
		},
		{
			name: "AuditLogs.List",
			find: func(ctx context.Context, c *octonomy.Client, readNS string, want namespaceFixture) (bool, error) {
				page, err := c.AuditLogs.List(ctx, &octonomy.AuditLogListParams{
					EntityType:  octonomy.String("tag"),
					EntityID:    octonomy.String(want.tag.ID),
					ListOptions: octonomy.ListOptions{Limit: scopedLimit},
				}, h.scoped(readNS)...)
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
	// requireFilteredRefusal.
	outcomeFiltered

	// outcomeForbidden: the request must never reach a queryset at all. This is
	// the permission layer, and it is a genuinely different mechanism from the
	// filter above -- which is why it needs its own run rather than being
	// inferred from one.
	outcomeForbidden
)

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

	runs := []struct {
		name string
		// client and readNS are the reader; want is the row looked for.
		client *octonomy.Client
		readNS string
		want   namespaceFixture
		expect outcome
	}{
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
	}

	for _, probe := range readProbes(h) {
		t.Run(probe.name, func(t *testing.T) {
			for _, run := range runs {
				t.Run(run.name, func(t *testing.T) {
					found, err := probe.find(ctx, run.client, run.readNS, run.want)

					switch run.expect {
					case outcomeVisible:
						if err != nil {
							t.Fatalf("%s: %v", probe.name, err)
						}
						if !found {
							t.Fatalf("%s did not return the row it was entitled to see: the negative runs of this probe prove nothing", probe.name)
						}

					case outcomeFiltered:
						if err != nil {
							requireFilteredRefusal(t, err, probe.name)
						}
						if found {
							t.Errorf("%s returned a merchant-%s row to a client reading merchant %s: this is a cross-merchant data leak",
								probe.name, run.want.namespaceID, run.readNS)
						}

					case outcomeForbidden:
						apiErr := requireAPIError(t, err, probe.name)
						if !octonomy.IsForbidden(err) || apiErr.StatusCode != 403 {
							t.Errorf("%s: reaching into another merchant gave {status:%d code:%q}, want {403 %q} -- the permission layer must refuse the request, not leave it to the namespace filter",
								probe.name, apiErr.StatusCode, apiErr.Code, octonomy.CodeForbidden)
						}
						if found {
							t.Errorf("%s returned a merchant-%s row on a request that should have been refused", probe.name, run.want.namespaceID)
						}
					}
				})
			}
		})
	}
}

// TestIntegration_IncludeGlobalFailsClosed pins the one option whose failure
// mode is silent.
//
// WithIncludeGlobal widens what a namespaced read ASKS for. Whether the global
// rows actually come back is decided separately, by whether the token holds
// global authority (request_include_global, octonomy/core/auth.py:53-67). So a
// token with an exact merchant grant that asks for global rows gets a 200 and
// its own rows -- no error, no warning, and nothing in the response that says
// the opt-in was declined.
//
// That is unfalsifiable from a fixture: a canned server returns whatever the
// fixture says, and a wildcard token makes the opt-in always succeed, so the
// fail-closed branch would never execute. It needs a token that genuinely cannot
// see global rows, which is why the harness mints exact grants.
func TestIntegration_IncludeGlobalFailsClosed(t *testing.T) {
	h := loadHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), suiteTimeout)
	defer cancel()

	wildcard := h.wildcard(t)
	clientA := h.merchantClient(t, h.merchantA)
	fixtureA := h.seed(t, wildcard, h.merchantA.id)

	// Merchant B exists here for one reason: `include_global` must widen a read
	// to the GLOBAL rows, never to every namespace. Without a second merchant's
	// row in the database, a server that had come to read the parameter as "all
	// partitions" would satisfy every other assertion in this test -- the default
	// read still excludes global, the wildcard control still finds it, and the
	// merchant token is still clamped back to its own namespace by
	// authorization. Nothing would notice.
	fixtureB := h.seed(t, wildcard, h.merchantB.id)

	// A tenant-shared global row: no namespace, no application. It is assignable
	// and visible tenant-wide, which is exactly what makes "can this merchant
	// opt into seeing it" a real question.
	globalTag, err := wildcard.Tags.Create(ctx, octonomy.TagCreate{
		Name: "integration global",
		Slug: uniqueSlug("int-global-tag"),
		Type: "label",
	})
	if err != nil {
		t.Fatalf("Tags.Create (global): %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		if err := wildcard.Tags.Delete(cleanupCtx, globalTag.ID); err != nil {
			t.Errorf("Tags.Delete (global): %v", err)
		}
	})

	// visibleTags runs ONE namespaced list in merchant A and returns the set of
	// tag ids it yielded.
	//
	// One request, one set, every membership question answered from it. The
	// alternative -- a `sees(id)` helper called once per id -- makes three
	// separate requests while the assertions read as if they were describing a
	// single response, so "the SAME request returned A but not global" would be
	// a claim the code never actually checked.
	//
	// It WALKS rather than taking one page: merchant A's namespace is small, but
	// "the leaked row was on page two" must not be able to read as "the leaked
	// row was absent", and TagListParams has no filter that covers three
	// different slugs at once.
	visibleTags := func(t *testing.T, c *octonomy.Client, opts ...octonomy.RequestOption) map[string]bool {
		t.Helper()
		seen := map[string]bool{}
		scoped := append(h.scoped(h.merchantA.id), opts...)
		if _, err := octonomy.Each(ctx, octonomy.ListOptions{Limit: 200},
			func(ctx context.Context, o octonomy.ListOptions) (*octonomy.List[octonomy.Tag], error) {
				return c.Tags.List(ctx, &octonomy.TagListParams{ListOptions: o}, scoped...)
			},
			func(row octonomy.Tag) error {
				seen[row.ID] = true
				return nil
			}); err != nil {
			t.Fatalf("Tags.List (merchant A): %v", err)
		}
		return seen
	}

	t.Run("a namespaced read excludes global rows by default", func(t *testing.T) {
		seen := visibleTags(t, wildcard)
		if seen[globalTag.ID] {
			t.Error("a namespaced list returned a global row without include_global: the parameter would then be meaningless")
		}
		if !seen[fixtureA.tag.ID] {
			t.Error("the same read returned none of merchant A's own rows, so it proves nothing about what it excluded")
		}
	})

	t.Run("an authorized token can opt into global rows", func(t *testing.T) {
		// The control for the assertion below. Without it, "merchant A sees no
		// global row" also passes on a server that ignores include_global
		// entirely, and the fail-closed claim would be resting on a broken
		// feature rather than a working guard.
		seen := visibleTags(t, wildcard, octonomy.WithIncludeGlobal())
		if !seen[globalTag.ID] {
			t.Error("a wildcard token asking for include_global did not get the global row: the opt-in is not working at all")
		}
		// GLOBAL, not every namespace -- and this token is authorized for
		// merchant B, so authorization cannot be what withholds it. Only the
		// meaning of the parameter can.
		if seen[fixtureB.tag.ID] {
			t.Error("include_global returned a row from ANOTHER MERCHANT: the parameter widens a read to the tenant-shared rows, never across the namespace axis")
		}
	})

	t.Run("an exact merchant grant cannot opt into global rows", func(t *testing.T) {
		// One request; three things asserted about it.
		seen := visibleTags(t, clientA, octonomy.WithIncludeGlobal())
		if seen[globalTag.ID] {
			t.Error("an exact merchant grant saw a global row via include_global: the opt-in must be fail-closed, and a merchant token has no global authority to widen with")
		}
		if seen[fixtureB.tag.ID] {
			t.Error("an exact merchant grant saw another merchant's row via include_global")
		}
		// The same response must still have carried merchant A's OWN rows.
		// Without this the two assertions above pass whenever the request
		// failed, was refused, or returned an empty page -- none of which is the
		// fail-closed behaviour being claimed, which is that the global rows are
		// withheld and the read otherwise works.
		if !seen[fixtureA.tag.ID] {
			t.Error("the same include_global response carried none of merchant A's own rows: fail-closed means the global rows are withheld, not that the read fails")
		}
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
	// They need no cleanup of their own: the tag deletion above cascades to
	// them, which is the very property under test.
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
					ApplicationID: octonomy.String(h.applicationID),
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
