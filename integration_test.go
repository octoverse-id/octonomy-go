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
// Seven assertions cover the shapes: the single-resource {"data": {...}} envelope
// on both a write and a read, the {data, pagination} list envelope, both
// resources, one real error envelope, and a namespaced round trip on /api/v2 --
// the last of these being the only place the namespace response fields meet a
// server that actually populates them.
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
}
