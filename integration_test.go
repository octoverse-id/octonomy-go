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
// The assertions cover the shapes: the single-resource {"data": {...}} envelope
// on both a write and a read, the {data, pagination} list envelope, all four
// resources, the composite resolution and bulk payloads, one real error
// envelope, the ENVELOPE-LESS health probes, and a namespaced round trip on
// /api/v2 -- the last of these being the only place the namespace response
// fields meet a server that actually populates them.
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

	// 0. The health probes, which are the one group a fixture can least be
	// trusted for: they are rooted OUTSIDE /api/<version>, they authenticate
	// nobody, and their body is a bare {"status": "ok"} with no data envelope.
	// Three claims a canned handler simply restates. The credential-free
	// constructor is exercised the way a caller would -- base URL alone, no
	// token, no tenant -- against a server that really does reject
	// unauthenticated traffic everywhere else.
	//
	// newSmokeClient has already gated on this variable, so it is set here.
	baseURL := os.Getenv("OCTONOMY_TEST_BASE_URL")
	probe, err := octonomy.NewHealthClient(baseURL)
	if err != nil {
		t.Fatalf("NewHealthClient: %v", err)
	}
	live, err := probe.Health.Live(ctx)
	if err != nil {
		t.Fatalf("Health.Live with no credentials: %v", err)
	}
	if live.Status != octonomy.HealthStatusOK {
		t.Errorf("Health.Live status = %q, want %q", live.Status, octonomy.HealthStatusOK)
	}
	ready, err := probe.Health.Ready(ctx)
	if err != nil {
		t.Fatalf("Health.Ready with no credentials: %v", err)
	}
	if ready.Status != octonomy.HealthStatusOK {
		t.Errorf("Health.Ready status = %q, want %q", ready.Status, octonomy.HealthStatusOK)
	}
	// The second entry point runs the same code against the same routes, and a
	// full client must not start sending its token there.
	if _, err := client.Health.Ready(ctx); err != nil {
		t.Fatalf("Client.Health.Ready: %v", err)
	}

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

	// 4. An update, the third doData write path -- and the one call in this file
	// that supplies its own X-Request-ID (#5). Only a real server can show where
	// that id lands: the audit assertions in step 12 read it back off the row the
	// SERVER wrote as a side effect of this call. The create above deliberately
	// sends none, so the two rows together prove both halves -- the caller's id
	// threads through, and the server still mints its own when there is none.
	updateRequestID := uniqueSlug("smoke-req")
	renamed, err := client.Tags.Update(ctx, tag.ID, octonomy.TagUpdate{
		Name: octonomy.String("v2 smoke renamed"),
	}, octonomy.WithRequestID(updateRequestID))
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

	// 5b. Each over a real multi-page collection (#14). The unit tests drive it
	// against a fixture reproducing four beliefs about the server's paginator:
	// count is the total rather than the page size, next goes nil at the end,
	// limit is clamped to 200, and the requested offset is echoed back. Each
	// READS two of them -- next to stop, the echoed offset to catch a page
	// function that dropped its options. The clamp is why it advances by what
	// arrived rather than by the limit it asked for, and count is what a CALLER
	// needs to detect a short walk; neither is read by the walker. All four are
	// pinned here against a real server rather than against a fixture that
	// merely agrees with the walker.
	//
	// It walks ALIASES, not tags, and via the nested route. Two reasons. The
	// nested route is the closure shape Each's doc comment advertises for
	// positional ids, and nothing else here covers it. More importantly the
	// aliases list is totally ordered by (name, slug, id), while GET /tags
	// carries no ORDER BY at all -- its usage_count annotation makes the query a
	// GROUP BY and Django drops Meta.ordering from those. A tags walk is
	// therefore allowed to repeat or miss rows between pages with no writes at
	// all, which is documented on Each and is not something to build a
	// deterministic assertion on.
	//
	// The aliases hang off a tag of their own rather than off the one from step
	// 3, which step 8 asserts holds exactly one alias. Sharing it would have
	// made this step break that one -- and a walk fixture is not worth weakening
	// an assertion elsewhere to accommodate.
	const walkAliases = 3
	walkTag, err := client.Tags.Create(ctx, octonomy.TagCreate{
		Name: "v2 smoke walk", Slug: uniqueSlug("smoke-walk-tag"), Type: "label",
	})
	if err != nil {
		t.Fatalf("Tags.Create for the walk: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cleanupCancel()
		if err := client.Tags.Delete(cleanupCtx, walkTag.ID); err != nil {
			t.Errorf("Tags.Delete for the walk: %v", err)
		}
	})

	wantAliasIDs := map[string]bool{}
	for i := 0; i < walkAliases; i++ {
		slug := uniqueSlug(fmt.Sprintf("smoke-walk-%d", i))
		created, err := client.Aliases.Create(ctx, octonomy.TagAliasCreate{
			TagID: walkTag.ID,
			Name:  fmt.Sprintf("walk %d", i),
			Slug:  slug,
		})
		if err != nil {
			t.Fatalf("Aliases.Create for the walk: %v", err)
		}
		wantAliasIDs[created.ID] = true
		id := created.ID
		t.Cleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), cleanupTimeout)
			defer cleanupCancel()
			if err := client.Aliases.Delete(cleanupCtx, id); err != nil {
				t.Errorf("Aliases.Delete for the walk: %v", err)
			}
		})
	}

	// count is the TOTAL across pages, not the size of this one, and the server
	// clamps an over-large limit and echoes the clamped value back. The CLAMP is
	// what makes Each's advance-by-what-arrived rule necessary -- advancing by
	// the limit requested would skip whatever the clamp withheld. count carries
	// no weight in the walk at all; it is asserted because the short-walk
	// detector Each documents is built on it.
	onePage, err := client.Tags.ListAliases(ctx, walkTag.ID, &octonomy.TagListAliasesParams{
		ListOptions: octonomy.ListOptions{Limit: 1},
	})
	if err != nil {
		t.Fatalf("Tags.ListAliases: %v", err)
	}
	if len(onePage.Data) != 1 {
		t.Fatalf("asked for 1 alias, got %d", len(onePage.Data))
	}
	if onePage.Pagination.Count != walkAliases {
		t.Errorf("pagination.count = %d on a 1-row page, want %d (the total, not the page size)",
			onePage.Pagination.Count, walkAliases)
	}
	clamped, err := client.Tags.ListAliases(ctx, walkTag.ID, &octonomy.TagListAliasesParams{
		ListOptions: octonomy.ListOptions{Limit: 500},
	})
	if err != nil {
		t.Fatalf("Tags.ListAliases with an over-large limit: %v", err)
	}
	if clamped.Pagination.Limit != 200 {
		t.Errorf("asked for limit 500, server echoed %d, want the 200 clamp", clamped.Pagination.Limit)
	}

	pages := 0
	visits := map[string]int{}
	offset, err := octonomy.Each(ctx, octonomy.ListOptions{Limit: 1},
		func(ctx context.Context, o octonomy.ListOptions) (*octonomy.List[octonomy.TagAlias], error) {
			pages++
			return client.Tags.ListAliases(ctx, walkTag.ID, &octonomy.TagListAliasesParams{ListOptions: o})
		},
		func(walked octonomy.TagAlias) error {
			visits[walked.ID]++
			return nil
		})
	if err != nil {
		t.Fatalf("Each over a real collection: %v", err)
	}
	if offset != walkAliases {
		t.Errorf("Each returned offset %d, want %d", offset, walkAliases)
	}
	// Exactly once each: neither a skipped row nor a redelivered one.
	if len(visits) != len(wantAliasIDs) {
		t.Errorf("walked %d distinct aliases, want %d", len(visits), len(wantAliasIDs))
	}
	for id := range wantAliasIDs {
		if visits[id] != 1 {
			t.Errorf("alias %s visited %d times, want exactly 1", id, visits[id])
		}
	}
	// One request per page, as the doc comment promises. Three items at one per
	// page is three pages -- a fourth would mean the walker only stops on an
	// empty page and never reads the server's own end-of-collection signal.
	if pages != walkAliases {
		t.Errorf("Each made %d requests for %d items at Limit 1, want %d", pages, walkAliases, walkAliases)
	}

	// DecodeMetadata against metadata the SERVER stored and returned, rather
	// than a map this test just built (#14). The tag from step 3 carries
	// {"source": "v2-smoke"}, and it reaches here having survived a real encode,
	// a real round trip, and a real decode into map[string]any.
	type smokeMeta struct {
		Source string `json:"source"`
	}
	decoded, err := octonomy.DecodeMetadata[smokeMeta](tag.Metadata)
	if err != nil {
		t.Fatalf("DecodeMetadata on server-returned metadata: %v", err)
	}
	if decoded.Source != "v2-smoke" {
		t.Errorf("DecodeMetadata gave Source = %q, want v2-smoke", decoded.Source)
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

	// 6b. The one error whose STATUS and CODE disagree, against a real server
	// (#6). scope_immutable is raised as a subclass of the server's conflict
	// error, so it arrives as a 409 whose code is not "conflict" -- and a
	// fixture asserting that is a fixture asserting what this SDK already
	// believes. Only the server settles whether IsScopeImmutable is true and
	// IsConflict is false on the same response.
	//
	// The vocabulary from step 1 is global, so naming any application at all is
	// a scope move. The target need not exist: the guard runs on the scope
	// change itself, before anything resolves the application. The row is
	// unchanged by a 409, so the step-1 cleanup still applies.
	_, err = client.Vocabularies.Update(ctx, vocab.ID, octonomy.VocabularyUpdate{
		ApplicationID: octonomy.String("smoke-scope-move"),
	})
	if err == nil {
		t.Fatal("Vocabularies.Update moving a global row into an application: expected a 409")
	}
	if !octonomy.IsScopeImmutable(err) {
		t.Errorf("scope-moving PATCH: IsScopeImmutable = false, err = %v", err)
	}
	if octonomy.IsConflict(err) {
		t.Error("scope-moving PATCH: IsConflict = true; scope_immutable must not read as a plain conflict")
	}
	scopeErr, ok := octonomy.AsAPIError(err)
	if !ok {
		t.Fatalf("scope-moving PATCH: error is not *APIError: %v", err)
	}
	if scopeErr.StatusCode != 409 || scopeErr.Code != octonomy.CodeScopeImmutable {
		t.Errorf("APIError = {status:%d code:%q}, want {409 %q}", scopeErr.StatusCode, scopeErr.Code, octonomy.CodeScopeImmutable)
	}
	// Details names the offending field. The doc comment on IsScopeImmutable
	// promises a caller can report which axis it tried to move, and only the
	// server's real payload backs that.
	if _, ok := scopeErr.Details["application_id"]; !ok {
		t.Errorf("Details did not name the offending field: %#v", scopeErr.Details)
	}
	// The row must not have moved. A 409 that mutated anyway would leave the
	// SDK reporting a refusal the server did not actually make.
	stillGlobal, err := client.Vocabularies.Get(ctx, vocab.ID)
	if err != nil {
		t.Fatalf("Vocabularies.Get after a refused scope move: %v", err)
	}
	if stillGlobal.ApplicationID != nil {
		t.Errorf("a refused scope move still changed the row: application_id = %v", *stillGlobal.ApplicationID)
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
	// 11. Resource tags: the resource's own view of tagging, and a THIRD
	// composite shape. docs/openapi-v2.yaml is wrong about the replace response
	// twice over -- it claims a bare array, and claims the elements are
	// ResourceTag, where the server sends {"created", "removed", "tags"} under
	// the envelope with Tag values in it.
	//
	// Both new models carry the namespace pair, so both are asserted against a
	// server that populates it. That is the gap #41 closed for Assignment the
	// hard way: renaming a namespace json tag left every fixture green, because
	// fixtures are marshalled from the same struct they are decoded into.
	replaceResourceID := uniqueSlug("smoke-cart")
	replaced, err := client.Resources.ReplaceTags(ctx, "cart", replaceResourceID, octonomy.ResourceReplace{
		ApplicationID: appID,
		TagIDs:        []string{tag.ID},
		AssignedBy:    octonomy.String("v2-smoke"),
	})
	if err != nil {
		t.Fatalf("Resources.ReplaceTags: %v", err)
	}
	if replaced.Created != 1 || replaced.Removed != 0 {
		t.Errorf("replace counts = created:%d removed:%d, want 1 and 0", replaced.Created, replaced.Removed)
	}
	if len(replaced.Tags) != 1 || replaced.Tags[0].ID != tag.ID {
		t.Fatalf("replace returned %+v, want the one tag", replaced.Tags)
	}
	// Tags, not ResourceTags -- the field would be empty if the element type were
	// wrong, which is the half of the spec's claim an envelope fix alone misses.
	if replaced.Tags[0].Slug != tagSlug {
		t.Errorf("replace tag slug = %q, want %q", replaced.Tags[0].Slug, tagSlug)
	}

	resourceTags, err := client.Resources.ListTags(ctx, "cart", replaceResourceID,
		&octonomy.ResourceListTagsParams{ApplicationID: octonomy.String(appID)})
	if err != nil {
		t.Fatalf("Resources.ListTags: %v", err)
	}
	if len(resourceTags.Data) != 1 {
		t.Fatalf("ListTags returned %d rows, want 1", len(resourceTags.Data))
	}
	rt := resourceTags.Data[0]
	if rt.AssignmentID == "" || rt.AssignedAt.IsZero() {
		t.Errorf("assignment fields did not decode: %+v", rt)
	}
	// The nested tag is the shape's whole point.
	if rt.Tag.ID != tag.ID || rt.Tag.Slug != tagSlug {
		t.Errorf("nested tag = %+v, want the created tag %s", rt.Tag, tag.ID)
	}
	if rt.AssignedBy == nil || *rt.AssignedBy != "v2-smoke" {
		t.Errorf("AssignedBy = %v, want v2-smoke", rt.AssignedBy)
	}
	// A global assignment reports no namespace.
	if rt.NamespaceType != nil || rt.NamespaceID != nil {
		t.Errorf("a global resource tag reported a namespace: %+v", rt)
	}

	// The mirror route, from the tag's side.
	tagResources, err := client.Tags.ListResources(ctx, tag.ID, &octonomy.TagListResourcesParams{
		ApplicationID: octonomy.String(appID),
		ResourceType:  octonomy.String("cart"),
	})
	if err != nil {
		t.Fatalf("Tags.ListResources: %v", err)
	}
	foundResource := false
	for _, row := range tagResources.Data {
		if row.ResourceID == replaceResourceID {
			foundResource = true
			if row.ResourceType != "cart" || row.ApplicationID != appID {
				t.Errorf("resource row did not round-trip: %+v", row)
			}
			if row.AssignedAt.IsZero() {
				t.Errorf("AssignedAt did not decode: %+v", row)
			}
		}
	}
	if !foundResource {
		t.Errorf("Tags.ListResources returned %d rows, none of them %s", len(tagResources.Data), replaceResourceID)
	}

	// The namespace pair on BOTH models, populated by the server from the request
	// headers -- the assertion no fixture can make honestly.
	nsCartID := uniqueSlug("smoke-ns-cart")
	if _, err := client.Resources.ReplaceTags(ctx, "cart", nsCartID, octonomy.ResourceReplace{
		ApplicationID: appID,
		TagIDs:        []string{nsTag.ID},
	}, octonomy.WithNamespace(nsType, nsID)); err != nil {
		t.Fatalf("Resources.ReplaceTags (namespaced): %v", err)
	}
	nsResourceTags, err := client.Resources.ListTags(ctx, "cart", nsCartID,
		&octonomy.ResourceListTagsParams{ApplicationID: octonomy.String(appID)},
		octonomy.WithNamespace(nsType, nsID))
	if err != nil {
		t.Fatalf("Resources.ListTags (namespaced): %v", err)
	}
	if len(nsResourceTags.Data) != 1 {
		t.Fatalf("namespaced ListTags returned %d rows, want 1", len(nsResourceTags.Data))
	}
	if got := nsResourceTags.Data[0]; got.NamespaceType == nil || *got.NamespaceType != nsType ||
		got.NamespaceID == nil || *got.NamespaceID != nsID {
		t.Errorf("namespaced resource tag: namespace = %v/%v, want %q/%q",
			got.NamespaceType, got.NamespaceID, nsType, nsID)
	}

	nsTagResources, err := client.Tags.ListResources(ctx, nsTag.ID, &octonomy.TagListResourcesParams{
		ApplicationID: octonomy.String(appID),
	}, octonomy.WithNamespace(nsType, nsID))
	if err != nil {
		t.Fatalf("Tags.ListResources (namespaced): %v", err)
	}
	if len(nsTagResources.Data) == 0 {
		t.Fatal("namespaced ListResources returned nothing")
	}
	if got := nsTagResources.Data[0]; got.NamespaceType == nil || *got.NamespaceType != nsType ||
		got.NamespaceID == nil || *got.NamespaceID != nsID {
		t.Errorf("namespaced tag resource: namespace = %v/%v, want %q/%q",
			got.NamespaceType, got.NamespaceID, nsType, nsID)
	}

	// A resource id carrying a character that needs escaping must address the
	// resource the caller named. Escaped twice -- as every path was before
	// resolvePath -- "ord 9" reached the server as the literal "ord%209", a
	// DIFFERENT resource, which on this destructive route means replacing a tag
	// set nobody asked for. Verified against the server: the two spellings really
	// do produce two rows.
	spacedResourceID := uniqueSlug("smoke order")
	if _, err := client.Resources.ReplaceTags(ctx, "cart", spacedResourceID, octonomy.ResourceReplace{
		ApplicationID: appID,
		TagIDs:        []string{tag.ID},
	}); err != nil {
		t.Fatalf("Resources.ReplaceTags (id with a space): %v", err)
	}
	spacedRows, err := client.Tags.ListResources(ctx, tag.ID, &octonomy.TagListResourcesParams{
		ApplicationID: octonomy.String(appID),
		ResourceType:  octonomy.String("cart"),
	})
	if err != nil {
		t.Fatalf("Tags.ListResources (id with a space): %v", err)
	}
	spacedFound := false
	for _, row := range spacedRows.Data {
		if row.ResourceID == spacedResourceID {
			spacedFound = true
		}
	}
	if !spacedFound {
		var got []string
		for _, row := range spacedRows.Data {
			got = append(got, row.ResourceID)
		}
		t.Errorf("the server stored %v, none of them %q: the id was escaped the wrong number of times",
			got, spacedResourceID)
	}

	// An EMPTY replace is legal and clears the resource. Proven here rather than
	// asserted in a doc comment, because it is the destructive case a caller
	// reaches by accident with a filter that matched nothing.
	cleared, err := client.Resources.ReplaceTags(ctx, "cart", replaceResourceID, octonomy.ResourceReplace{
		ApplicationID: appID,
	})
	if err != nil {
		t.Fatalf("Resources.ReplaceTags (empty): %v", err)
	}
	if cleared.Removed != 1 || cleared.Created != 0 {
		t.Errorf("empty replace counts = created:%d removed:%d, want 0 and 1", cleared.Created, cleared.Removed)
	}
	if len(cleared.Tags) != 0 {
		t.Errorf("empty replace left %d tags, want none", len(cleared.Tags))
	}
	// 12. Audit logs: the append-only history, and the one payload in this SDK
	// whose most important field has no type in the contract at all.
	//
	// Three things here need a real server. The audit rows are written by the
	// SERVER as a side effect of the mutations above, so unlike every other
	// fixture in the unit suite they are not marshalled from the struct they are
	// decoded into -- a misspelled json tag shows up here and nowhere else. The
	// `changes` object's real shape is server-authored too. And the namespace
	// filtering is a property of the query the server runs, not of anything the
	// client sends.
	tagAudit, err := client.Tags.ListAuditLogs(ctx, tag.ID, &octonomy.TagListAuditLogsParams{
		ListOptions: octonomy.ListOptions{Limit: 100},
	})
	if err != nil {
		// A 403 here means the harness token lacks audit:read, which is a harness
		// fault rather than an SDK one -- scripts/octonomy-harness.sh grants it.
		if octonomy.IsForbidden(err) {
			t.Fatalf("audit reads are forbidden for this token: the harness must grant --scope audit:read: %v", err)
		}
		t.Fatalf("Tags.ListAuditLogs: %v", err)
	}
	var created, updated *octonomy.AuditLog
	createdIdx, updatedIdx := -1, -1
	for i, row := range tagAudit.Data {
		switch row.Action {
		case "tag.created":
			created, createdIdx = &tagAudit.Data[i], i
		case "tag.updated":
			updated, updatedIdx = &tagAudit.Data[i], i
		}
	}
	if created == nil || updated == nil {
		t.Fatalf("Tags.ListAuditLogs returned %d rows, missing tag.created or tag.updated", len(tagAudit.Data))
	}
	if created.EntityType != "tag" || created.EntityID != tag.ID {
		t.Errorf("tag.created row names %s/%s, want tag/%s", created.EntityType, created.EntityID, tag.ID)
	}
	if created.TenantID == "" || created.OperationID == "" || created.CreatedAt.IsZero() {
		t.Errorf("identity fields did not decode: %+v", created)
	}
	if created.TagID == nil || *created.TagID != tag.ID {
		t.Errorf("TagID = %v, want %s", created.TagID, tag.ID)
	}
	if created.ActorID == nil || *created.ActorID != "v2-smoke" {
		t.Errorf("ActorID = %v, want v2-smoke (Config.ActorID)", created.ActorID)
	}
	// Request correlation, both halves, on rows the server wrote itself (#5).
	// The create sent no X-Request-ID, so the server minted one; the update sent
	// updateRequestID with WithRequestID, so the row must carry that exact
	// string. A unit test can assert the header leaves the client and nothing
	// more -- that it survives the middleware, reaches audit.request_id, and is
	// stored unmangled is a property of the server, and this is the only place it
	// is checked.
	switch {
	case created.RequestID == nil:
		t.Errorf("tag.created RequestID is nil, want the server's generated value")
	case *created.RequestID == "":
		t.Errorf("tag.created RequestID is empty, want the server's generated value")
	case *created.RequestID == updateRequestID:
		t.Errorf("tag.created RequestID = %q, the id sent on the UPDATE: the SDK must send no header at all when the option is absent",
			*created.RequestID)
	}
	switch {
	case updated.RequestID == nil:
		t.Errorf("tag.updated RequestID is nil, want the caller-supplied %q", updateRequestID)
	case *updated.RequestID != updateRequestID:
		t.Errorf("tag.updated RequestID = %q, want the caller-supplied %q", *updated.RequestID, updateRequestID)
	}
	// Rows arrive NEWEST FIRST, which AuditLog documents and offset paging
	// depends on: the rename happened after the create, so it comes back before
	// it. Asserted on the page order rather than only on the two timestamps,
	// since the order is what a caller actually reads.
	if updatedIdx > createdIdx {
		t.Errorf("tag.updated is at index %d and tag.created at %d: rows must arrive newest first",
			updatedIdx, createdIdx)
	}
	for i := 1; i < len(tagAudit.Data); i++ {
		if tagAudit.Data[i].CreatedAt.After(tagAudit.Data[i-1].CreatedAt) {
			t.Errorf("row %d (%v) is newer than row %d (%v): the page is not ordered newest first",
				i, tagAudit.Data[i].CreatedAt, i-1, tagAudit.Data[i-1].CreatedAt)
		}
	}

	// The untyped `changes` object, read the way a caller reads it. The contract
	// gives this field no type; the server writes {"before": ..., "after": ...}
	// with the fields the mutation touched.
	after, ok := updated.Changes["after"].(map[string]any)
	if !ok {
		t.Fatalf("tag.updated changes[after] = %#v, want an object", updated.Changes["after"])
	}
	if after["name"] != "v2 smoke renamed" {
		t.Errorf("changes[after][name] = %v, want the renamed value", after["name"])
	}
	if before, ok := updated.Changes["before"].(map[string]any); !ok || before["name"] != "v2 smoke" {
		t.Errorf("changes[before] = %#v, want the pre-rename name", updated.Changes["before"])
	}

	// OperationID is what makes a multi-row mutation reconstructable: the replace
	// in step 11 and the empty replace that cleared it are separate operations,
	// and filtering by one returns that operation's rows alone.
	resourceAudit, err := client.Resources.ListAuditLogs(ctx, "cart", replaceResourceID,
		&octonomy.ResourceListAuditLogsParams{ListOptions: octonomy.ListOptions{Limit: 100}})
	if err != nil {
		t.Fatalf("Resources.ListAuditLogs: %v", err)
	}
	if len(resourceAudit.Data) < 2 {
		t.Fatalf("Resources.ListAuditLogs returned %d rows, want the assign and the clear", len(resourceAudit.Data))
	}
	operations := map[string]int{}
	for _, row := range resourceAudit.Data {
		if row.ResourceID == nil || *row.ResourceID != replaceResourceID {
			t.Errorf("row %s names resource %v, want %s", row.ID, row.ResourceID, replaceResourceID)
		}
		// The entity is "tag_assignment" while the action is "assignment.*" --
		// an asymmetry AuditLog documents, and an exact-match filter, so it is
		// worth pinning against the server rather than against a fixture.
		if row.EntityType != "tag_assignment" {
			t.Errorf("assignment row EntityType = %q, want tag_assignment", row.EntityType)
		}
		operations[row.OperationID]++
	}
	if len(operations) < 2 {
		t.Errorf("the two replaces share %d operation id(s), want one each: %v", len(operations), operations)
	}
	oneOperation := resourceAudit.Data[0].OperationID
	byOperation, err := client.AuditLogs.List(ctx, &octonomy.AuditLogListParams{
		OperationID: octonomy.String(oneOperation),
		ListOptions: octonomy.ListOptions{Limit: 100},
	})
	if err != nil {
		t.Fatalf("AuditLogs.List (by operation): %v", err)
	}
	if len(byOperation.Data) == 0 {
		t.Fatalf("filtering by operation_id %s returned nothing", oneOperation)
	}
	for _, row := range byOperation.Data {
		if row.OperationID != oneOperation {
			t.Errorf("operation_id filter returned %s, want only %s", row.OperationID, oneOperation)
		}
	}

	// The collection's filters, and the global rows' namespace pair: a global
	// mutation records no namespace.
	entityRows, err := client.AuditLogs.List(ctx, &octonomy.AuditLogListParams{
		EntityType:  octonomy.String("tag"),
		EntityID:    octonomy.String(tag.ID),
		Action:      octonomy.String("tag.created"),
		ListOptions: octonomy.ListOptions{Limit: 10},
	})
	if err != nil {
		t.Fatalf("AuditLogs.List (filtered): %v", err)
	}
	if len(entityRows.Data) != 1 {
		t.Fatalf("filtered list returned %d rows, want exactly the tag.created row", len(entityRows.Data))
	}
	if entityRows.Data[0].NamespaceType != nil || entityRows.Data[0].NamespaceID != nil {
		t.Errorf("a global audit row reported a namespace: %+v", entityRows.Data[0])
	}

	// The namespace pair the server populates, and the fail-closed read: a
	// namespaced audit read returns that namespace's rows and excludes the global
	// ones. Without the headers the server serves the global namespace with a
	// 200, so the exclusion is what proves they arrived.
	nsAudit, err := client.AuditLogs.List(ctx, &octonomy.AuditLogListParams{
		ListOptions: octonomy.ListOptions{Limit: 100},
	}, octonomy.WithNamespace(nsType, nsID), octonomy.WithApplication(appID))
	if err != nil {
		t.Fatalf("AuditLogs.List (namespaced): %v", err)
	}
	sawNamespacedRow, sawGlobalRow := false, false
	for _, row := range nsAudit.Data {
		if row.EntityID == nsTag.ID {
			sawNamespacedRow = true
			if row.NamespaceType == nil || *row.NamespaceType != nsType ||
				row.NamespaceID == nil || *row.NamespaceID != nsID {
				t.Errorf("namespaced audit row: namespace = %v/%v, want %q/%q",
					row.NamespaceType, row.NamespaceID, nsType, nsID)
			}
		}
		if row.EntityID == tag.ID {
			sawGlobalRow = true
		}
	}
	if !sawNamespacedRow {
		t.Errorf("namespaced audit list returned no row for the namespaced tag %s", nsTag.ID)
	}
	if sawGlobalRow {
		t.Errorf("namespaced audit list returned a GLOBAL row (%s): namespaced reads exclude global rows unless include_global is set", tag.ID)
	}

	// An unknown tag is an EMPTY PAGE here, not the 404 every other /tags/{id}
	// route answers: the view filters the audit table by tag_id and never loads
	// the tag. Documented on TagService.ListAuditLogs, and only a real server can
	// confirm it.
	unknownTagAudit, err := client.Tags.ListAuditLogs(ctx, "00000000-0000-0000-0000-000000000000", nil)
	if err != nil {
		t.Fatalf("Tags.ListAuditLogs on an unknown tag: want an empty page, got %v", err)
	}
	if len(unknownTagAudit.Data) != 0 {
		t.Errorf("Tags.ListAuditLogs on an unknown tag returned %d rows", len(unknownTagAudit.Data))
	}
}
