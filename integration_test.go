//go:build integration

// Narrow integration smoke test for the modern line.
//
// Deliberately a smoke test, not a suite. #17 builds the full integration suite;
// what this proves is the one thing httptest structurally cannot: that the
// canned fixtures match the server. They did not, and that is #32 -- every
// single-resource read decoded to a zero-valued struct with a nil error, and a
// complete unit suite stayed green through it, because the fixtures encoded the
// vendored spec rather than the running server.
//
// Ten assertions cover the shapes: the single-resource {"data": {...}} envelope
// on both a write and a read, the {data, pagination} list envelope, all four
// resources, the composite resolution and bulk payloads, one real error
// envelope, and a namespaced round trip on /api/v2 -- the last of these being the
// only place the namespace response fields meet a server that actually populates
// them.
//
// The bulk assertions matter most of the set. docs/openapi-v2.yaml claims
// bulk-assign returns a bare array and documents no schema at all for
// bulk-remove; the server returns composites under `data` for both. A client
// written from the spec decodes an empty slice and a nil error, and only a real
// server can tell you which of the two is true.
//
// Run it against the container harness:
//
//	make dev-server
//	make smoke
//
// With OCTONOMY_TEST_BASE_URL unset the test skips, so `go test ./...` stays
// hermetic.
package octonomy_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	octonomy "github.com/octoverse-id/octonomy-go/v2"
)

// cleanupTimeout bounds the deferred deletes. They get their own context on
// purpose: sharing the test's context means a timeout mid-test cancels the
// cleanup too, so the rows leak AND the delete error masks the real failure in
// the output.
const cleanupTimeout = 15 * time.Second

// newSmokeClient builds a client from the harness credentials, or skips.
//
// The gate is OCTONOMY_TEST_BASE_URL, matching scripts/octonomy-harness.sh. A
// missing token or tenant with a base URL present is a broken harness, not an
// absent one, so that fails rather than skips -- otherwise a misconfigured CI
// job would report a vacuous pass.
//
// OCTONOMY_SMOKE_REQUIRED=1 removes the skip entirely, and CI sets it. Skipping
// is right on a laptop with no Docker; in the CI job it is the worst available
// outcome, because this is the only check that sees the server's real response
// shapes -- the exact blind spot that produced #32.
func newSmokeClient(t *testing.T) *octonomy.Client {
	t.Helper()

	required := os.Getenv("OCTONOMY_SMOKE_REQUIRED") == "1"
	baseURL := os.Getenv("OCTONOMY_TEST_BASE_URL")
	if baseURL == "" {
		if required {
			t.Fatal("OCTONOMY_SMOKE_REQUIRED=1 but OCTONOMY_TEST_BASE_URL is empty: the harness did not export its credentials, so this test would have skipped and reported a vacuous pass")
		}
		t.Skip("OCTONOMY_TEST_BASE_URL is empty; run `make dev-server` and then `make smoke`")
	}
	token := os.Getenv("OCTONOMY_TEST_TOKEN")
	tenantID := os.Getenv("OCTONOMY_TEST_TENANT_ID")
	if token == "" || tenantID == "" {
		t.Fatal("OCTONOMY_TEST_BASE_URL is set but OCTONOMY_TEST_TOKEN/OCTONOMY_TEST_TENANT_ID are not: the harness env is incomplete")
	}

	client, err := octonomy.New(octonomy.Config{
		BaseURL:  baseURL,
		Token:    token,
		TenantID: tenantID,
		ActorID:  "v2-smoke",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
}

// uniqueSlug keeps repeat runs against one long-lived harness from colliding on
// the server's (type, slug) uniqueness constraint.
func uniqueSlug(prefix string) string {
	return fmt.Sprintf("%s-%d-%d", prefix, os.Getpid(), time.Now().UnixNano()%1e6)
}

func TestSmoke_RealServer(t *testing.T) {
	client := newSmokeClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// 1. A write that returns a resource. The server answers 201 with
	// {"data": {...}}; before #32 this decoded to an empty Vocabulary and a nil
	// error, and this assertion is what caught it on the compat line.
	vocabSlug := uniqueSlug("smoke-vocab")
	vocab, err := client.Vocabularies.Create(ctx, octonomy.VocabularyCreate{
		Name:        "v2 smoke",
		Slug:        vocabSlug,
		Description: octonomy.String("created by the integration smoke test"),
	})
	if err != nil {
		t.Fatalf("Vocabularies.Create: %v", err)
	}
	// Deactivation, not deletion -- but it keeps a repeatedly-booted harness
	// tidy and exercises the DELETE path's 204 assertion against a real server.
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cleanupCancel()
		if err := client.Vocabularies.Delete(cleanupCtx, vocab.ID); err != nil {
			t.Errorf("Vocabularies.Delete: %v", err)
		}
	})
	if vocab.ID == "" || vocab.Slug != vocabSlug {
		t.Fatalf("created vocabulary did not round-trip: %+v", vocab)
	}

	// 2. A read that returns a resource. Create and Get are separate call sites
	// through doData, and only one of them was covered by a fixture before this
	// change.
	fetched, err := client.Vocabularies.Get(ctx, vocab.ID)
	if err != nil {
		t.Fatalf("Vocabularies.Get: %v", err)
	}
	if fetched.ID != vocab.ID || fetched.Slug != vocabSlug {
		t.Fatalf("fetched vocabulary did not round-trip: %+v", fetched)
	}
	if fetched.Description == nil || *fetched.Description != "created by the integration smoke test" {
		t.Errorf("Description did not round-trip: %v", fetched.Description)
	}

	// 3. The same on the other resource, with metadata, which is the field most
	// likely to be silently dropped by a wrong envelope assumption.
	tagSlug := uniqueSlug("smoke-tag")
	tag, err := client.Tags.Create(ctx, octonomy.TagCreate{
		Name:         "v2 smoke",
		Slug:         tagSlug,
		Type:         "label",
		VocabularyID: octonomy.String(vocab.ID),
		Metadata:     octonomy.Metadata{"source": "v2-smoke"},
	})
	if err != nil {
		t.Fatalf("Tags.Create: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cleanupCancel()
		if err := client.Tags.Delete(cleanupCtx, tag.ID); err != nil {
			t.Errorf("Tags.Delete: %v", err)
		}
	})
	if tag.ID == "" || tag.Slug != tagSlug {
		t.Fatalf("created tag did not round-trip: %+v", tag)
	}
	if got := tag.Metadata["source"]; got != "v2-smoke" {
		t.Errorf("tag.Metadata[source] = %v, want v2-smoke", got)
	}

	// 4. An update, the third doData write path.
	renamed, err := client.Tags.Update(ctx, tag.ID, octonomy.TagUpdate{
		Name: octonomy.String("v2 smoke renamed"),
	})
	if err != nil {
		t.Fatalf("Tags.Update: %v", err)
	}
	if renamed.Name != "v2 smoke renamed" || renamed.ID != tag.ID {
		t.Fatalf("updated tag did not round-trip: %+v", renamed)
	}

	// 5. The {data, pagination} list envelope. The vendored spec documents list
	// responses as bare arrays; the server sends the envelope, and doList now
	// requires a usable pagination block, so only a real server proves the
	// requirement matches what the server actually emits.
	tags, err := client.Tags.List(ctx, &octonomy.TagListParams{
		Slug:        octonomy.String(tagSlug),
		ListOptions: octonomy.ListOptions{Limit: 10},
	})
	if err != nil {
		t.Fatalf("Tags.List: %v", err)
	}
	if len(tags.Data) != 1 || tags.Data[0].ID != tag.ID {
		t.Fatalf("Tags.List did not return the created tag: %+v", tags.Data)
	}
	if tags.Pagination.Limit != 10 || tags.Pagination.Count < 1 {
		t.Errorf("pagination block did not decode: %+v", tags.Pagination)
	}

	vocabs, err := client.Vocabularies.List(ctx, &octonomy.VocabularyListParams{
		ListOptions: octonomy.ListOptions{Limit: 50},
	})
	if err != nil {
		t.Fatalf("Vocabularies.List: %v", err)
	}
	if vocabs.Pagination.Limit != 50 {
		t.Errorf("vocabulary pagination did not decode: %+v", vocabs.Pagination)
	}
	// Look for the row we created, not merely for a non-empty page. "some
	// vocabulary came back" still passes if a tenancy, filter, or visibility
	// regression is returning the wrong tenant's rows.
	found := false
	for _, v := range vocabs.Data {
		if v.ID == vocab.ID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Vocabularies.List returned %d rows, none of them the created %s", len(vocabs.Data), vocab.ID)
	}

	// 6. A real error envelope from the real server, not a canned httptest body.
	_, err = client.Tags.Get(ctx, "00000000-0000-0000-0000-000000000000")
	if err == nil {
		t.Fatal("Tags.Get on a missing id: expected an error")
	}
	if !octonomy.IsNotFound(err) {
		t.Errorf("Tags.Get on a missing id: IsNotFound = false, err = %v", err)
	}
	apiErr, ok := octonomy.AsAPIError(err)
	if !ok {
		t.Fatalf("error is not *APIError: %v", err)
	}
	if apiErr.StatusCode != 404 || apiErr.Code != octonomy.CodeNotFound {
		t.Errorf("APIError = {status:%d code:%q}, want {404 %q}", apiErr.StatusCode, apiErr.Code, octonomy.CodeNotFound)
	}

	// 7. The namespace axis, which exists only on /api/v2.
	//
	// A unit test cannot reach this: the namespace response fields are populated
	// by the server from the request headers, so a canned fixture asserts only
	// that the SDK can echo a value it wrote itself. Here the round trip is real
	// -- headers out, persisted scope back -- which is the same class of gap that
	// hid #32.
	//
	// The client above targets APIV2 by default, so this needs no second client.
	nsType := os.Getenv("OCTONOMY_TEST_NAMESPACE_TYPE")
	nsID := os.Getenv("OCTONOMY_TEST_NAMESPACE_ID")
	appID := os.Getenv("OCTONOMY_TEST_APPLICATION_ID")
	if nsType == "" || nsID == "" || appID == "" {
		// Not a skip: the harness exports all three, so their absence means it
		// booted differently than this test assumes rather than "no namespace
		// support here".
		t.Fatal("OCTONOMY_TEST_NAMESPACE_TYPE/_NAMESPACE_ID/_APPLICATION_ID are not all set: the harness env is incomplete for the namespace assertions")
	}

	// The global rows created above must report no namespace. This is the
	// assertion that would fail if the SDK ever grew a client-level namespace
	// default and started scoping every call.
	if tag.NamespaceType != nil || tag.NamespaceID != nil {
		t.Errorf("a global tag reported a namespace: type=%v id=%v", tag.NamespaceType, tag.NamespaceID)
	}

	nsSlug := uniqueSlug("smoke-ns-tag")
	nsTag, err := client.Tags.Create(ctx, octonomy.TagCreate{
		Name:          "v2 smoke namespaced",
		Slug:          nsSlug,
		Type:          "label",
		ApplicationID: octonomy.String(appID),
	}, octonomy.WithNamespace(nsType, nsID))
	if err != nil {
		// A 403 namespaced_writes_disabled here means the harness did not pass
		// OCTONOMY_NAMESPACE_WRITE_ENABLED=true through to the container, which
		// is a harness fault rather than an SDK one -- say which.
		if octonomy.IsNamespacedWritesDisabled(err) {
			t.Fatalf("namespaced writes are disabled on this deployment: the harness must set OCTONOMY_NAMESPACE_WRITE_ENABLED=true (server default is false): %v", err)
		}
		t.Fatalf("Tags.Create (namespaced): %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cleanupCancel()
		if err := client.Tags.Delete(cleanupCtx, nsTag.ID,
			octonomy.WithNamespace(nsType, nsID), octonomy.WithApplication(appID)); err != nil {
			t.Errorf("Tags.Delete (namespaced): %v", err)
		}
	})
	if nsTag.NamespaceType == nil || *nsTag.NamespaceType != nsType {
		t.Errorf("created namespaced tag: NamespaceType = %v, want %q", nsTag.NamespaceType, nsType)
	}
	if nsTag.NamespaceID == nil || *nsTag.NamespaceID != nsID {
		t.Errorf("created namespaced tag: NamespaceID = %v, want %q", nsTag.NamespaceID, nsID)
	}

	// The read path carries the headers too, and the persisted scope survives it.
	nsFetched, err := client.Tags.Get(ctx, nsTag.ID,
		octonomy.WithNamespace(nsType, nsID), octonomy.WithApplication(appID))
	if err != nil {
		t.Fatalf("Tags.Get (namespaced): %v", err)
	}
	if nsFetched.NamespaceID == nil || *nsFetched.NamespaceID != nsID {
		t.Errorf("fetched namespaced tag: NamespaceID = %v, want %q", nsFetched.NamespaceID, nsID)
	}

	// A namespaced list excludes global rows by default, and the tag created in
	// step 3 is global -- so it must not appear here. This is the assertion that
	// proves the headers actually reached the server: without them the server
	// serves the global namespace with a 200 and this list would contain it.
	nsList, err := client.Tags.List(ctx, &octonomy.TagListParams{
		ListOptions: octonomy.ListOptions{Limit: 100},
	}, octonomy.WithNamespace(nsType, nsID), octonomy.WithApplication(appID))
	if err != nil {
		t.Fatalf("Tags.List (namespaced): %v", err)
	}
	sawNamespaced, sawGlobal := false, false
	for _, row := range nsList.Data {
		switch row.ID {
		case nsTag.ID:
			sawNamespaced = true
		case tag.ID:
			sawGlobal = true
		}
	}
	if !sawNamespaced {
		t.Errorf("namespaced list did not return the namespaced tag %s", nsTag.ID)
	}
	if sawGlobal {
		t.Errorf("namespaced list returned the GLOBAL tag %s: a namespaced read must exclude global rows unless include_global is set", tag.ID)
	}
	// 8. Tag aliases: a third response shape, and the third schema carrying
	// namespace identity. AGENTS.md requires a new shape to be asserted here and
	// not only against handcrafted fixtures, for the reason the file's header
	// gives -- a fixture written from the vendored spec passes against a client
	// that is wrong, which is exactly how #32 survived a full unit suite. The
	// spec describes both alias list routes as bare arrays.
	aliasSlug := uniqueSlug("smoke-alias")
	alias, err := client.Aliases.Create(ctx, octonomy.TagAliasCreate{
		TagID:    tag.ID,
		Name:     "v2 smoke alias",
		Slug:     aliasSlug,
		Metadata: octonomy.Metadata{"source": "v2-smoke"},
	})
	if err != nil {
		t.Fatalf("Aliases.Create: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cleanupCancel()
		if err := client.Aliases.Delete(cleanupCtx, alias.ID); err != nil {
			t.Errorf("Aliases.Delete: %v", err)
		}
	})
	if alias.ID == "" || alias.Slug != aliasSlug || alias.TagID != tag.ID {
		t.Fatalf("created alias did not round-trip: %+v", alias)
	}
	if got := alias.Metadata["source"]; got != "v2-smoke" {
		t.Errorf("alias.Metadata[source] = %v, want v2-smoke", got)
	}
	// A global alias reports no namespace, the same invariant asserted for the
	// global tag in step 7.
	if alias.NamespaceType != nil || alias.NamespaceID != nil {
		t.Errorf("a global alias reported a namespace: type=%v id=%v", alias.NamespaceType, alias.NamespaceID)
	}

	aliasFetched, err := client.Aliases.Get(ctx, alias.ID)
	if err != nil {
		t.Fatalf("Aliases.Get: %v", err)
	}
	if aliasFetched.ID != alias.ID || aliasFetched.TagID != tag.ID {
		t.Fatalf("fetched alias did not round-trip: %+v", aliasFetched)
	}

	aliasRenamed, err := client.Aliases.Update(ctx, alias.ID, octonomy.TagAliasUpdate{
		Name: octonomy.String("v2 smoke alias renamed"),
	})
	if err != nil {
		t.Fatalf("Aliases.Update: %v", err)
	}
	if aliasRenamed.Name != "v2 smoke alias renamed" || aliasRenamed.ID != alias.ID {
		t.Fatalf("updated alias did not round-trip: %+v", aliasRenamed)
	}

	// Both list routes, because they are separate doList call sites reaching
	// different server views of the same rows.
	aliasPage, err := client.Aliases.List(ctx, &octonomy.TagAliasListParams{
		TagID:       octonomy.String(tag.ID),
		ListOptions: octonomy.ListOptions{Limit: 10},
	})
	if err != nil {
		t.Fatalf("Aliases.List: %v", err)
	}
	if len(aliasPage.Data) != 1 || aliasPage.Data[0].ID != alias.ID {
		t.Fatalf("Aliases.List did not return the created alias: %+v", aliasPage.Data)
	}
	if aliasPage.Pagination.Limit != 10 || aliasPage.Pagination.Count < 1 {
		t.Errorf("alias pagination block did not decode: %+v", aliasPage.Pagination)
	}

	nested, err := client.Tags.ListAliases(ctx, tag.ID, &octonomy.TagListAliasesParams{
		ListOptions: octonomy.ListOptions{Limit: 10},
	})
	if err != nil {
		t.Fatalf("Tags.ListAliases: %v", err)
	}
	nestedFound := false
	for _, row := range nested.Data {
		if row.ID == alias.ID {
			nestedFound = true
			break
		}
	}
	if !nestedFound {
		t.Errorf("Tags.ListAliases returned %d rows, none of them the created alias %s", len(nested.Data), alias.ID)
	}

	// The namespace fields on TagAlias are server-set from the request headers,
	// so only a real server can populate them -- a fixture would assert that the
	// SDK echoes a value it wrote itself. The target is the namespaced tag from
	// step 7, because a namespaced alias may only point at a global or
	// same-namespace tag.
	nsAlias, err := client.Aliases.Create(ctx, octonomy.TagAliasCreate{
		TagID:         nsTag.ID,
		Name:          "v2 smoke alias namespaced",
		Slug:          uniqueSlug("smoke-ns-alias"),
		ApplicationID: octonomy.String(appID),
	}, octonomy.WithNamespace(nsType, nsID))
	if err != nil {
		t.Fatalf("Aliases.Create (namespaced): %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cleanupCancel()
		if err := client.Aliases.Delete(cleanupCtx, nsAlias.ID,
			octonomy.WithNamespace(nsType, nsID), octonomy.WithApplication(appID)); err != nil {
			t.Errorf("Aliases.Delete (namespaced): %v", err)
		}
	})
	if nsAlias.NamespaceType == nil || *nsAlias.NamespaceType != nsType {
		t.Errorf("created namespaced alias: NamespaceType = %v, want %q", nsAlias.NamespaceType, nsType)
	}
	if nsAlias.NamespaceID == nil || *nsAlias.NamespaceID != nsID {
		t.Errorf("created namespaced alias: NamespaceID = %v, want %q", nsAlias.NamespaceID, nsID)
	}
	// 9. Tag resolution: a composite payload rather than a resource, and the
	// only response in this SDK that nests one model inside another. A fixture
	// proves nothing about which of the two tags the server puts in `tag` --
	// only a real alias resolving to a real canonical tag does.
	resolvedTag, err := client.Tags.Resolve(ctx, tagSlug, nil)
	if err != nil {
		t.Fatalf("Tags.Resolve (canonical): %v", err)
	}
	if resolvedTag.MatchedType != octonomy.MatchedTypeTag {
		t.Errorf("MatchedType = %q, want %q", resolvedTag.MatchedType, octonomy.MatchedTypeTag)
	}
	if resolvedTag.MatchedAlias != nil {
		t.Errorf("a canonical match carried an alias: %+v", resolvedTag.MatchedAlias)
	}
	if resolvedTag.Tag.ID != tag.ID {
		t.Errorf("resolved tag = %s, want the created tag %s", resolvedTag.Tag.ID, tag.ID)
	}

	// The alias branch, resolving the alias slug created in step 8. `tag` must
	// be the CANONICAL tag, not the alias's own row -- the assertion a canned
	// fixture cannot make honestly, because it would be asserting a value the
	// test itself chose.
	resolvedAlias, err := client.Tags.Resolve(ctx, aliasSlug, nil)
	if err != nil {
		t.Fatalf("Tags.Resolve (via alias): %v", err)
	}
	if resolvedAlias.MatchedType != octonomy.MatchedTypeAlias {
		t.Errorf("MatchedType = %q, want %q", resolvedAlias.MatchedType, octonomy.MatchedTypeAlias)
	}
	if resolvedAlias.MatchedAlias == nil {
		t.Fatal("MatchedAlias = nil on an alias match")
	}
	if resolvedAlias.MatchedAlias.ID != alias.ID {
		t.Errorf("MatchedAlias = %s, want the created alias %s", resolvedAlias.MatchedAlias.ID, alias.ID)
	}
	if resolvedAlias.Tag.ID != tag.ID {
		t.Errorf("alias resolved to tag %s, want the canonical %s", resolvedAlias.Tag.ID, tag.ID)
	}
	if resolvedAlias.Tag.Slug != tagSlug {
		t.Errorf("resolved tag slug = %q, want the canonical %q (not the alias slug)", resolvedAlias.Tag.Slug, tagSlug)
	}

	// An unmatched slug is a 400 validation_error, NOT a 404 -- the one piece of
	// this endpoint's contract a caller is most likely to get wrong, and the one
	// most worth pinning against the real server rather than a fixture that
	// simply restates the claim.
	_, err = client.Tags.Resolve(ctx, uniqueSlug("smoke-no-such"), nil)
	if err == nil {
		t.Fatal("Tags.Resolve on an unmatched slug: expected an error")
	}
	if !octonomy.IsValidation(err) {
		t.Errorf("Tags.Resolve on an unmatched slug: IsValidation = false, err = %v", err)
	}
	if octonomy.IsNotFound(err) {
		t.Error("Tags.Resolve on an unmatched slug reported not_found: the server answers 400, and the SDK documents IsValidation as the branch")
	}
	// 10. Tag assignments, the resource whose documented response shapes are
	// furthest from the server's. Assignments are always application-scoped, so
	// these use the harness application; the tag from step 3 is global, which is
	// assignable in any application.
	resourceID := uniqueSlug("smoke-order")
	assignment, err := client.Assignments.Create(ctx, octonomy.AssignmentCreate{
		ApplicationID: appID,
		TagID:         octonomy.String(tag.ID),
		ResourceType:  "order",
		ResourceID:    resourceID,
		AssignedBy:    octonomy.String("v2-smoke"),
	})
	if err != nil {
		t.Fatalf("Assignments.Create: %v", err)
	}
	if assignment.ID == "" || assignment.TagID != tag.ID || assignment.ResourceID != resourceID {
		t.Fatalf("created assignment did not round-trip: %+v", assignment)
	}
	if assignment.ApplicationID != appID {
		t.Errorf("ApplicationID = %q, want %q", assignment.ApplicationID, appID)
	}
	if assignment.AssignedBy == nil || *assignment.AssignedBy != "v2-smoke" {
		t.Errorf("AssignedBy = %v, want v2-smoke", assignment.AssignedBy)
	}
	if assignment.AssignedAt.IsZero() {
		t.Error("AssignedAt did not decode")
	}

	// Idempotent: the same assignment again returns the SAME row rather than a
	// duplicate or an error. Asserting the id is stronger than asserting the
	// status, which the SDK deliberately does not surface.
	again, err := client.Assignments.Create(ctx, octonomy.AssignmentCreate{
		ApplicationID: appID,
		TagID:         octonomy.String(tag.ID),
		ResourceType:  "order",
		ResourceID:    resourceID,
	})
	if err != nil {
		t.Fatalf("Assignments.Create (repeat): %v", err)
	}
	if again.ID != assignment.ID {
		t.Errorf("re-assigning produced a new row %s, want the existing %s", again.ID, assignment.ID)
	}

	// The alias form resolves to the canonical tag, which is the assertion a
	// fixture cannot make: the server does the resolving.
	viaAlias, err := client.Assignments.Create(ctx, octonomy.AssignmentCreate{
		ApplicationID: appID,
		AliasSlug:     octonomy.String(aliasSlug),
		ResourceType:  "order",
		ResourceID:    resourceID,
	})
	if err != nil {
		t.Fatalf("Assignments.Create (by alias slug): %v", err)
	}
	if viaAlias.TagID != tag.ID {
		t.Errorf("alias slug assigned tag %s, want the canonical %s", viaAlias.TagID, tag.ID)
	}

	// BULK ASSIGN: the composite the spec calls a bare array. A []Assignment
	// decoder against this body yields an empty slice and a nil error, so this
	// is the assertion that says which shape the server really sends.
	bulk, err := client.Assignments.BulkAssign(ctx, octonomy.BulkAssign{
		ApplicationID: appID,
		ResourceType:  "order",
		ResourceID:    resourceID,
		TagIDs:        []string{tag.ID},
	})
	if err != nil {
		t.Fatalf("Assignments.BulkAssign: %v", err)
	}
	// The tag is already assigned from the calls above, so this must count as
	// existing rather than created -- which also proves the counts are real and
	// not zero-valued.
	if bulk.Existing != 1 || bulk.Created != 0 {
		t.Errorf("bulk counts = created:%d existing:%d, want created:0 existing:1", bulk.Created, bulk.Existing)
	}
	if len(bulk.Assignments) != 1 || bulk.Assignments[0].TagID != tag.ID {
		t.Errorf("bulk assignments did not round-trip: %+v", bulk.Assignments)
	}

	// BULK REMOVE: the shape the spec does not describe at all.
	removed, err := client.Assignments.BulkRemove(ctx, octonomy.BulkRemove{
		ApplicationID: appID,
		ResourceType:  "order",
		ResourceID:    resourceID,
		TagIDs:        []string{tag.ID},
	})
	if err != nil {
		t.Fatalf("Assignments.BulkRemove: %v", err)
	}
	if removed.Removed != 1 {
		t.Errorf("Removed = %d, want 1", removed.Removed)
	}

	// Removing what is no longer there is a 204, not a 404.
	if err := client.Assignments.Remove(ctx, octonomy.AssignmentRemove{
		ApplicationID: appID,
		TagID:         tag.ID,
		ResourceType:  "order",
		ResourceID:    resourceID,
	}); err != nil {
		t.Fatalf("Assignments.Remove on an already-removed assignment: %v", err)
	}

	// A NAMESPACED assignment, whose namespace pair the server populates from the
	// request headers. Only a real server can assert this: every canned fixture
	// is marshalled from Assignment itself, so a misspelled json tag is used for
	// both the write and the read and round-trips perfectly. Verified by breaking
	// the tags -- the whole unit suite and this smoke test stayed green until
	// these assertions existed.
	//
	// The target is the namespaced tag from step 7, since a namespaced write must
	// stay inside its own scope.
	nsResourceID := uniqueSlug("smoke-ns-order")
	nsAssignment, err := client.Assignments.Create(ctx, octonomy.AssignmentCreate{
		ApplicationID: appID,
		TagID:         octonomy.String(nsTag.ID),
		ResourceType:  "order",
		ResourceID:    nsResourceID,
	}, octonomy.WithNamespace(nsType, nsID))
	if err != nil {
		t.Fatalf("Assignments.Create (namespaced): %v", err)
	}
	if nsAssignment.NamespaceType == nil || *nsAssignment.NamespaceType != nsType {
		t.Errorf("namespaced assignment: NamespaceType = %v, want %q", nsAssignment.NamespaceType, nsType)
	}
	if nsAssignment.NamespaceID == nil || *nsAssignment.NamespaceID != nsID {
		t.Errorf("namespaced assignment: NamespaceID = %v, want %q", nsAssignment.NamespaceID, nsID)
	}
	if nsAssignment.TagID != nsTag.ID {
		t.Errorf("namespaced assignment: TagID = %s, want %s", nsAssignment.TagID, nsTag.ID)
	}

	// And the namespaced body-carrying DELETE, which no other resource exercises:
	// the headers scope the request while the body identifies the row.
	if err := client.Assignments.Remove(ctx, octonomy.AssignmentRemove{
		ApplicationID: appID,
		TagID:         nsTag.ID,
		ResourceType:  "order",
		ResourceID:    nsResourceID,
	}, octonomy.WithNamespace(nsType, nsID)); err != nil {
		t.Fatalf("Assignments.Remove (namespaced): %v", err)
	}
}
