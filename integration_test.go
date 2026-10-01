//go:build integration
// +build integration

// Minimal integration smoke test for the Go 1.13 line.
//
// Both build-constraint forms are present on purpose: Go 1.17+ reads
// //go:build, Go 1.13 reads only // +build, and gofmt keeps the two in sync.
//
// This is deliberately a smoke test, not a suite: what it proves on every change
// is narrow -- that the client still decodes what a real, current Octonomy server
// sends. It covers the {data, pagination} envelope (which the vendored spec does
// not describe, so only a real server can confirm it), a list of each
// implemented resource, one real error envelope, the bare {"status": ...} body
// of both health probes, an empty Metadata reaching the server as {}, the
// namespace pair decoding off a real /api/v2 response, and every method of the
// resource groups #94 ported -- the three composite bodies the vendored specs
// describe wrongly among them, decoded to their real counts. Assertions about what the
// server DOES -- isolation, authorization -- belong in a suite of their own;
// porting one is #97.
//
// Run it against the container harness:
//
//	make dev-server
//	set -a; . ./.octonomy-harness.env; set +a
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

	octonomy "github.com/octoverse-id/octonomy-go"
)

// newSmokeClient builds a client from the harness credentials, or skips.
//
// The gate is OCTONOMY_TEST_BASE_URL, matching scripts/octonomy-harness.sh. A
// missing token or tenant with a base URL present is a broken harness, not an
// absent one, so that fails rather than skips -- otherwise a misconfigured CI
// job would report a vacuous pass.
//
// OCTONOMY_SMOKE_REQUIRED=1 removes the skip entirely, and CI sets it. Skipping
// is right on a laptop with no Docker; in the required CI job it is the worst
// possible outcome, because a credential export that silently broke would leave
// this line's ONLY real-server check reporting green without running. The
// release in #29 cannot be recalled, so "green" has to mean "ran".
func newSmokeClient(t *testing.T) *octonomy.Client {
	t.Helper()

	required := os.Getenv("OCTONOMY_SMOKE_REQUIRED") == "1"
	baseURL := os.Getenv("OCTONOMY_TEST_BASE_URL")
	if baseURL == "" {
		if required {
			t.Fatal("OCTONOMY_SMOKE_REQUIRED=1 but OCTONOMY_TEST_BASE_URL is empty: the harness did not export its credentials, so this test would have skipped and reported a vacuous pass")
		}
		t.Skip("OCTONOMY_TEST_BASE_URL is empty; run `make dev-server` and source .octonomy-harness.env")
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
		ActorID:  "go113-smoke",
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

	vocabSlug := uniqueSlug("smoke-vocab")
	vocab, err := client.Vocabularies.Create(ctx, octonomy.VocabularyCreate{
		Name: "Go 1.13 smoke",
		Slug: vocabSlug,
	})
	if err != nil {
		t.Fatalf("Vocabularies.Create: %v", err)
	}
	// Deactivation, not deletion -- but it keeps a repeatedly-booted harness
	// tidy and exercises the DELETE path.
	defer func() {
		if err := client.Vocabularies.Delete(ctx, vocab.ID); err != nil {
			t.Errorf("Vocabularies.Delete: %v", err)
		}
	}()
	if vocab.ID == "" || vocab.Slug != vocabSlug {
		t.Fatalf("created vocabulary did not round-trip: %+v", vocab)
	}

	tagSlug := uniqueSlug("smoke-tag")
	tag, err := client.Tags.Create(ctx, octonomy.TagCreate{
		Name:         "Go 1.13 smoke",
		Slug:         tagSlug,
		Type:         "label",
		VocabularyID: octonomy.String(vocab.ID),
		Metadata:     octonomy.Metadata{"source": "go113-smoke"},
	})
	if err != nil {
		t.Fatalf("Tags.Create: %v", err)
	}
	defer func() {
		if err := client.Tags.Delete(ctx, tag.ID); err != nil {
			t.Errorf("Tags.Delete: %v", err)
		}
	}()
	if tag.ID == "" || tag.Slug != tagSlug {
		t.Fatalf("created tag did not round-trip: %+v", tag)
	}
	// Metadata is map[string]interface{} on this line, so a JSON string decodes
	// to interface{}("go113-smoke"), not to a typed field.
	if got := tag.Metadata["source"]; got != "go113-smoke" {
		t.Errorf("tag.Metadata[source] = %v, want go113-smoke", got)
	}

	// The {data, pagination} envelope. The vendored spec documents list
	// responses as bare arrays; the server sends the envelope. Only a real
	// server proves TagList still decodes it.
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
		t.Errorf("VocabularyList pagination did not decode: %+v", vocabs.Pagination)
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

	// A real error envelope from the real server, not a canned httptest body.
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

	// Metadata{} must reach the server as {} and empty the stored object. The
	// struct-tag encoding dropped the key, so the PATCH succeeded and changed
	// nothing; the unit tests prove what is sent, and only the server proves
	// that what is sent clears it.
	if _, err := client.Tags.Update(ctx, tag.ID, octonomy.TagUpdate{Metadata: octonomy.Metadata{}}); err != nil {
		t.Fatalf("Tags.Update(Metadata{}): %v", err)
	}
	reread, err := client.Tags.Get(ctx, tag.ID)
	if err != nil {
		t.Fatalf("Tags.Get after clearing metadata: %v", err)
	}
	if len(reread.Metadata) != 0 {
		t.Errorf("tag.Metadata = %v after Update(Metadata{}), want it emptied", reread.Metadata)
	}
	if _, err := client.Vocabularies.Update(ctx, vocab.ID, octonomy.VocabularyUpdate{Metadata: octonomy.Metadata{"k": "v"}}); err != nil {
		t.Fatalf("Vocabularies.Update(populated): %v", err)
	}
	clearedVocab, err := client.Vocabularies.Update(ctx, vocab.ID, octonomy.VocabularyUpdate{Metadata: octonomy.Metadata{}})
	if err != nil {
		t.Fatalf("Vocabularies.Update(Metadata{}): %v", err)
	}
	if len(clearedVocab.Metadata) != 0 {
		t.Errorf("vocabulary.Metadata = %v after Update(Metadata{}), want it emptied", clearedVocab.Metadata)
	}
}

// The health probes are the one response with no data envelope, and they are
// rooted outside /api/<version> and unauthenticated -- three things only a real
// server confirms at once. Both entry points are probed: the full client, whose
// credentials must not be needed, and a client built from the base URL alone.
func TestSmoke_HealthProbes(t *testing.T) {
	client := newSmokeClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for name, probe := range map[string]func(context.Context) (*octonomy.HealthStatus, error){
		"Client.Health.Live":  client.Health.Live,
		"Client.Health.Ready": client.Health.Ready,
	} {
		st, err := probe(ctx)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if st.Status != octonomy.HealthStatusOK {
			t.Errorf("%s: Status = %q, want %q", name, st.Status, octonomy.HealthStatusOK)
		}
	}

	hc, err := octonomy.NewHealthClient(os.Getenv("OCTONOMY_TEST_BASE_URL"))
	if err != nil {
		t.Fatalf("NewHealthClient: %v", err)
	}
	st, err := hc.Health.Ready(ctx)
	if err != nil {
		t.Fatalf("HealthClient.Ready: %v", err)
	}
	if st.Status != octonomy.HealthStatusOK {
		t.Errorf("HealthClient.Ready: Status = %q, want %q", st.Status, octonomy.HealthStatusOK)
	}
}

// /api/v2 is opt-in on this line. What only a real server shows is that the v2
// responses carry the namespace pair under the names the models decode, on a
// row created in a namespace and on the list that returns it, and that a v2
// not_found is still an enveloped one.
func TestSmoke_APIV2Namespace(t *testing.T) {
	newSmokeClient(t) // the skip-or-fail gate
	nsType := os.Getenv("OCTONOMY_TEST_NAMESPACE_TYPE")
	nsID := os.Getenv("OCTONOMY_TEST_NAMESPACE_ID")
	app := os.Getenv("OCTONOMY_TEST_APPLICATION_ID")
	if nsType == "" || nsID == "" || app == "" {
		t.Fatal("OCTONOMY_TEST_NAMESPACE_TYPE/_ID and OCTONOMY_TEST_APPLICATION_ID must be set: the harness exports them")
	}
	client, err := octonomy.New(octonomy.Config{
		BaseURL:    os.Getenv("OCTONOMY_TEST_BASE_URL"),
		Token:      os.Getenv("OCTONOMY_TEST_TOKEN"),
		TenantID:   os.Getenv("OCTONOMY_TEST_TENANT_ID"),
		APIVersion: octonomy.APIV2,
		ActorID:    "go113-smoke",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	scope := []octonomy.RequestOption{octonomy.WithNamespace(nsType, nsID)}
	read := append([]octonomy.RequestOption{octonomy.WithApplication(app)}, scope...)

	slug := uniqueSlug("smoke-v2")
	tag, err := client.Tags.Create(ctx, octonomy.TagCreate{
		ApplicationID: octonomy.String(app),
		Name:          "Go 1.13 v2 smoke",
		Slug:          slug,
		Type:          "label",
	}, append(scope, octonomy.WithRequestID("go113-smoke-"+slug))...)
	if err != nil {
		t.Fatalf("namespaced Tags.Create: %v", err)
	}
	defer func() {
		if err := client.Tags.Delete(ctx, tag.ID, read...); err != nil {
			t.Errorf("namespaced Tags.Delete: %v", err)
		}
	}()
	if tag.NamespaceType == nil || *tag.NamespaceType != nsType || tag.NamespaceID == nil || *tag.NamespaceID != nsID {
		t.Fatalf("created tag's namespace = (%v, %v), want (%s, %s): the pair did not decode", tag.NamespaceType, tag.NamespaceID, nsType, nsID)
	}

	got, err := client.Tags.Get(ctx, tag.ID, read...)
	if err != nil {
		t.Fatalf("namespaced Tags.Get: %v", err)
	}
	if got.NamespaceID == nil || *got.NamespaceID != nsID {
		t.Errorf("Tags.Get namespace_id = %v, want %s", got.NamespaceID, nsID)
	}

	page, err := client.Tags.List(ctx, &octonomy.TagListParams{Slug: octonomy.String(slug)}, read...)
	if err != nil {
		t.Fatalf("namespaced Tags.List: %v", err)
	}
	if len(page.Data) != 1 || page.Data[0].ID != tag.ID || page.Data[0].NamespaceID == nil || *page.Data[0].NamespaceID != nsID {
		t.Fatalf("namespaced Tags.List did not return the created row with its namespace: %+v", page.Data)
	}

	_, err = client.Tags.Get(ctx, "00000000-0000-0000-0000-000000000000", read...)
	if !octonomy.IsNotFound(err) || octonomy.IsUnexpectedStatus(err) {
		t.Errorf("v2 Tags.Get on a missing id: want an enveloped not_found, got %v", err)
	}
}

// The five resource groups #94 ported (health, the sixth, came with #91), each
// called once against a real server.
// What a fixture cannot prove and this does: that the three COMPOSITE bodies --
// bulk-assign, bulk-remove and the resource-tag replace, which both vendored
// specs describe wrongly -- decode to their real counts rather than to a
// zero-valued result with a nil error, and that every new model decodes the
// fields the server actually sends.
//
// It runs on this line's default surface, /api/v1, which is where a v1.0.0
// caller who upgrades would first call these methods; TestSmoke_APIV2ResourceGroups
// covers the namespace pair on the same models.
func TestSmoke_ResourceGroups(t *testing.T) {
	client := newSmokeClient(t)
	app := os.Getenv("OCTONOMY_TEST_APPLICATION_ID")
	if app == "" {
		t.Fatal("OCTONOMY_TEST_APPLICATION_ID must be set: the harness exports it, and assignments are always application-scoped")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	tagSlug := uniqueSlug("smoke-rg-tag")
	tag, err := client.Tags.Create(ctx, octonomy.TagCreate{Name: "Go 1.13 resource groups", Slug: tagSlug, Type: "label"})
	if err != nil {
		t.Fatalf("Tags.Create: %v", err)
	}
	defer func() {
		if err := client.Tags.Delete(ctx, tag.ID); err != nil {
			t.Errorf("Tags.Delete: %v", err)
		}
	}()

	// --- aliases ------------------------------------------------------------
	aliasSlug := uniqueSlug("smoke-rg-alias")
	alias, err := client.Aliases.Create(ctx, octonomy.TagAliasCreate{
		TagID:    tag.ID,
		Name:     "Go 1.13 alias",
		Slug:     aliasSlug,
		Metadata: octonomy.Metadata{"source": "go113-smoke"},
	})
	if err != nil {
		t.Fatalf("Aliases.Create: %v", err)
	}
	defer func() {
		if err := client.Aliases.Delete(ctx, alias.ID); err != nil {
			t.Errorf("Aliases.Delete: %v", err)
		}
	}()
	if alias.ID == "" || alias.TagID != tag.ID || alias.Slug != aliasSlug || !alias.IsActive || alias.CreatedAt.IsZero() {
		t.Fatalf("created alias did not round-trip: %+v", alias)
	}
	fetched, err := client.Aliases.Get(ctx, alias.ID)
	if err != nil {
		t.Fatalf("Aliases.Get: %v", err)
	}
	if fetched.ID != alias.ID || fetched.Metadata["source"] != "go113-smoke" {
		t.Errorf("Aliases.Get = %+v, want the created alias with its metadata", fetched)
	}
	renamed, err := client.Aliases.Update(ctx, alias.ID, octonomy.TagAliasUpdate{Name: octonomy.String("Go 1.13 alias, renamed")})
	if err != nil {
		t.Fatalf("Aliases.Update: %v", err)
	}
	if renamed.Name != "Go 1.13 alias, renamed" || renamed.Metadata["source"] != "go113-smoke" {
		t.Errorf("a name-only PATCH changed more than the name, or not the name: %+v", renamed)
	}
	// TagAliasUpdate's MarshalJSON, proven where only a server can prove it:
	// Metadata{} reaches the wire as {} AND empties the stored object.
	cleared, err := client.Aliases.Update(ctx, alias.ID, octonomy.TagAliasUpdate{Metadata: octonomy.Metadata{}})
	if err != nil {
		t.Fatalf("Aliases.Update(Metadata{}): %v", err)
	}
	if len(cleared.Metadata) != 0 {
		t.Errorf("alias.Metadata = %v after Update(Metadata{}), want it emptied", cleared.Metadata)
	}
	aliases, err := client.Aliases.List(ctx, &octonomy.TagAliasListParams{Slug: octonomy.String(aliasSlug)})
	if err != nil {
		t.Fatalf("Aliases.List: %v", err)
	}
	if len(aliases.Data) != 1 || aliases.Data[0].ID != alias.ID || aliases.Pagination.Limit < 1 {
		t.Errorf("Aliases.List did not return the created alias with a usable page: %+v", aliases)
	}
	nested, err := client.Tags.ListAliases(ctx, tag.ID, nil)
	if err != nil {
		t.Fatalf("Tags.ListAliases: %v", err)
	}
	if len(nested.Data) != 1 || nested.Data[0].ID != alias.ID {
		t.Errorf("Tags.ListAliases = %+v, want exactly the created alias", nested.Data)
	}

	// --- resolution ---------------------------------------------------------
	direct, err := client.Tags.Resolve(ctx, tagSlug, nil)
	if err != nil {
		t.Fatalf("Tags.Resolve (canonical): %v", err)
	}
	if direct.MatchedType != octonomy.MatchedTypeTag || direct.MatchedAlias != nil || direct.Tag.ID != tag.ID {
		t.Errorf("canonical resolution = %+v, want a tag match on %s", direct, tag.ID)
	}
	viaAlias, err := client.Tags.Resolve(ctx, aliasSlug, nil)
	if err != nil {
		t.Fatalf("Tags.Resolve (alias): %v", err)
	}
	if viaAlias.MatchedType != octonomy.MatchedTypeAlias || viaAlias.MatchedAlias == nil ||
		viaAlias.MatchedAlias.ID != alias.ID || viaAlias.Tag.ID != tag.ID {
		t.Errorf("alias resolution = %+v, want an alias match on %s resolving to %s", viaAlias, alias.ID, tag.ID)
	}
	_, err = client.Tags.Resolve(ctx, uniqueSlug("smoke-rg-no-such"), nil)
	if !octonomy.IsValidation(err) || octonomy.IsNotFound(err) {
		t.Errorf("an unmatched slug: want a validation_error and not a not_found, got %v", err)
	}

	// --- assignments and the two bulk composites ----------------------------
	orderID := uniqueSlug("smoke-rg-order")
	assignment, err := client.Assignments.Create(ctx, octonomy.AssignmentCreate{
		ApplicationID: app, TagID: octonomy.String(tag.ID), ResourceType: "order", ResourceID: orderID,
	})
	if err != nil {
		t.Fatalf("Assignments.Create: %v", err)
	}
	if assignment.ID == "" || assignment.TagID != tag.ID || assignment.ApplicationID != app ||
		assignment.ResourceID != orderID || assignment.AssignedAt.IsZero() {
		t.Fatalf("created assignment did not round-trip: %+v", assignment)
	}
	again, err := client.Assignments.Create(ctx, octonomy.AssignmentCreate{
		ApplicationID: app, TagID: octonomy.String(tag.ID), ResourceType: "order", ResourceID: orderID,
	})
	if err != nil || again.ID != assignment.ID {
		t.Errorf("a repeated Assignments.Create = (%+v, %v), want the same row: it is idempotent", again, err)
	}

	// The tag is already on the order, so a real decode reads existing 1 and
	// created 0. A decoder that lost the counts would read 0 and 0.
	bulk, err := client.Assignments.BulkAssign(ctx, octonomy.BulkAssign{
		ApplicationID: app, ResourceType: "order", ResourceID: orderID, TagIDs: []string{tag.ID},
	})
	if err != nil {
		t.Fatalf("Assignments.BulkAssign: %v", err)
	}
	if bulk.Created != 0 || bulk.Existing != 1 || len(bulk.Assignments) != 1 || bulk.Assignments[0].ID != assignment.ID {
		t.Errorf("BulkAssign = %+v, want created 0, existing 1, and the one assignment", bulk)
	}
	removed, err := client.Assignments.BulkRemove(ctx, octonomy.BulkRemove{
		ApplicationID: app, ResourceType: "order", ResourceID: orderID, TagIDs: []string{tag.ID},
	})
	if err != nil {
		t.Fatalf("Assignments.BulkRemove: %v", err)
	}
	if removed.Removed != 1 {
		t.Errorf("BulkRemove.Removed = %d, want 1", removed.Removed)
	}
	// A body-carrying DELETE, and idempotent: the row is already gone, so this
	// is a 204 rather than a 404.
	if err := client.Assignments.Remove(ctx, octonomy.AssignmentRemove{
		ApplicationID: app, TagID: tag.ID, ResourceType: "order", ResourceID: orderID,
	}); err != nil {
		t.Errorf("Assignments.Remove on an absent assignment: %v", err)
	}

	// A bad target reports the first server check it fails, and the order
	// depends on how it is named (AssignmentService.Create). The retired tag and
	// its alias live in app and are assigned from otherApp, so each call fails
	// two checks at once -- inactive AND another application -- and must report
	// the inactivity: inactive_tag by TagID, a validation_error through an alias
	// (deactivating the tag cascades to the alias, which the server refuses
	// before it ever compares applications).
	otherApp := app + "-other"
	retired, err := client.Tags.Create(ctx, octonomy.TagCreate{
		ApplicationID: octonomy.String(app), Name: "Go 1.13 retired", Slug: uniqueSlug("smoke-rg-retired"), Type: "label",
	})
	if err != nil {
		t.Fatalf("Tags.Create (to retire): %v", err)
	}
	retiredAlias, err := client.Aliases.Create(ctx, octonomy.TagAliasCreate{
		ApplicationID: octonomy.String(app), TagID: retired.ID, Name: "Go 1.13 retired alias", Slug: uniqueSlug("smoke-rg-retired-alias"),
	})
	if err != nil {
		t.Fatalf("Aliases.Create (to retire): %v", err)
	}
	if err := client.Tags.Delete(ctx, retired.ID, octonomy.WithApplication(app)); err != nil {
		t.Fatalf("Tags.Delete (retire): %v", err)
	}
	for _, tc := range []struct {
		name string
		in   octonomy.AssignmentCreate
		want func(error) bool
	}{
		{"by TagID", octonomy.AssignmentCreate{TagID: octonomy.String(retired.ID)}, octonomy.IsInactiveTag},
		{"by AliasID", octonomy.AssignmentCreate{AliasID: octonomy.String(retiredAlias.ID)}, octonomy.IsValidation},
		{"by AliasSlug", octonomy.AssignmentCreate{AliasSlug: octonomy.String(retiredAlias.Slug)}, octonomy.IsValidation},
	} {
		in := tc.in
		in.ApplicationID, in.ResourceType, in.ResourceID = otherApp, "order", orderID
		_, err := client.Assignments.Create(ctx, in)
		if !tc.want(err) || octonomy.IsApplicationMismatch(err) {
			t.Errorf("assigning an inactive, other-application target %s: got %v, want its inactivity reported and not the mismatch", tc.name, err)
		}
	}

	// Another application's alias: application_mismatch by AliasID, but by
	// AliasSlug it is never a candidate, so it is the not-found validation_error.
	appAlias, err := client.Aliases.Create(ctx, octonomy.TagAliasCreate{
		ApplicationID: octonomy.String(app), TagID: tag.ID, Name: "Go 1.13 app alias", Slug: uniqueSlug("smoke-rg-app-alias"),
	})
	if err != nil {
		t.Fatalf("Aliases.Create (application-scoped): %v", err)
	}
	defer func() {
		if err := client.Aliases.Delete(ctx, appAlias.ID); err != nil {
			t.Errorf("Aliases.Delete (application-scoped): %v", err)
		}
	}()
	_, err = client.Assignments.Create(ctx, octonomy.AssignmentCreate{
		ApplicationID: otherApp, AliasID: octonomy.String(appAlias.ID), ResourceType: "order", ResourceID: orderID,
	})
	if !octonomy.IsApplicationMismatch(err) {
		t.Errorf("another application's alias by AliasID: want application_mismatch, got %v", err)
	}
	_, err = client.Assignments.Create(ctx, octonomy.AssignmentCreate{
		ApplicationID: otherApp, AliasSlug: octonomy.String(appAlias.Slug), ResourceType: "order", ResourceID: orderID,
	})
	if !octonomy.IsValidation(err) || octonomy.IsApplicationMismatch(err) {
		t.Errorf("another application's alias by AliasSlug: want a validation_error, got %v", err)
	}

	// --- resource tags and the replace composite ----------------------------
	cartID := uniqueSlug("smoke-rg-cart")
	replaced, err := client.Resources.ReplaceTags(ctx, "cart", cartID, octonomy.ResourceReplace{
		ApplicationID: app, TagIDs: []string{tag.ID}, AssignedBy: octonomy.String("go113-smoke"),
	})
	if err != nil {
		t.Fatalf("Resources.ReplaceTags: %v", err)
	}
	// Tags are Tag values, not ResourceTags: a wrong element type would leave
	// Slug empty even with the envelope right.
	if replaced.Created != 1 || replaced.Removed != 0 || len(replaced.Tags) != 1 ||
		replaced.Tags[0].ID != tag.ID || replaced.Tags[0].Slug != tagSlug {
		t.Errorf("ReplaceTags = %+v, want created 1, removed 0, and the one tag", replaced)
	}
	onCart, err := client.Resources.ListTags(ctx, "cart", cartID, &octonomy.ResourceListTagsParams{ApplicationID: octonomy.String(app)})
	if err != nil {
		t.Fatalf("Resources.ListTags: %v", err)
	}
	if len(onCart.Data) != 1 {
		t.Fatalf("Resources.ListTags returned %d rows, want 1", len(onCart.Data))
	}
	if rt := onCart.Data[0]; rt.AssignmentID == "" || rt.AssignedAt.IsZero() || rt.Tag.ID != tag.ID ||
		rt.AssignedBy == nil || *rt.AssignedBy != "go113-smoke" || rt.NamespaceType != nil {
		t.Errorf("resource tag did not round-trip: %+v", rt)
	}
	carts, err := client.Tags.ListResources(ctx, tag.ID, &octonomy.TagListResourcesParams{
		ApplicationID: octonomy.String(app), ResourceType: octonomy.String("cart"),
	})
	if err != nil {
		t.Fatalf("Tags.ListResources: %v", err)
	}
	found := false
	for _, r := range carts.Data {
		if r.ResourceID == cartID && r.ResourceType == "cart" && r.ApplicationID == app && !r.AssignedAt.IsZero() {
			found = true
		}
	}
	if !found {
		t.Errorf("Tags.ListResources returned %d rows, none of them the cart %s", len(carts.Data), cartID)
	}
	// The destructive case: an empty replace is legal and clears the resource.
	emptied, err := client.Resources.ReplaceTags(ctx, "cart", cartID, octonomy.ResourceReplace{ApplicationID: app})
	if err != nil {
		t.Fatalf("Resources.ReplaceTags (empty): %v", err)
	}
	if emptied.Created != 0 || emptied.Removed != 1 || emptied.Tags == nil || len(emptied.Tags) != 0 {
		t.Errorf("an empty ReplaceTags = %+v, want created 0, removed 1, and an empty tag set", emptied)
	}

	// --- audit logs ---------------------------------------------------------
	tagRows, err := client.Tags.ListAuditLogs(ctx, tag.ID, &octonomy.TagListAuditLogsParams{Action: octonomy.String("tag.created")})
	if err != nil {
		t.Fatalf("Tags.ListAuditLogs: %v", err)
	}
	if len(tagRows.Data) != 1 || tagRows.Data[0].EntityID != tag.ID || tagRows.Data[0].EntityType != "tag" ||
		tagRows.Data[0].OperationID == "" || tagRows.Data[0].CreatedAt.IsZero() {
		t.Errorf("Tags.ListAuditLogs(tag.created) = %+v, want the one creation row", tagRows.Data)
	}
	cartRows, err := client.Resources.ListAuditLogs(ctx, "cart", cartID, nil)
	if err != nil {
		t.Fatalf("Resources.ListAuditLogs: %v", err)
	}
	// One assignment.created from the first replace, one assignment.removed
	// from the empty one, newest first.
	if len(cartRows.Data) != 2 || cartRows.Data[0].Action != "assignment.removed" || cartRows.Data[1].Action != "assignment.created" {
		t.Fatalf("Resources.ListAuditLogs = %+v, want assignment.removed then assignment.created", cartRows.Data)
	}
	if after, ok := cartRows.Data[1].Changes["after"].(map[string]interface{}); !ok || after["tag_id"] != tag.ID {
		t.Errorf("the creation row's Changes = %v, want an \"after\" object naming the tag", cartRows.Data[1].Changes)
	}
	byOperation, err := client.AuditLogs.List(ctx, &octonomy.AuditLogListParams{OperationID: octonomy.String(cartRows.Data[0].OperationID)})
	if err != nil {
		t.Fatalf("AuditLogs.List: %v", err)
	}
	if len(byOperation.Data) != 1 || byOperation.Data[0].ID != cartRows.Data[0].ID {
		t.Errorf("AuditLogs.List by operation = %+v, want exactly the removal row", byOperation.Data)
	}
}

// The namespace pair on the ported models, which only a v2 response carries and
// which a fixture marshalled from the same struct could never catch a misspelled
// tag on: a namespaced alias, a namespaced replace read back as ResourceTag and
// TagResource rows, and the audit rows those writes left.
func TestSmoke_APIV2ResourceGroups(t *testing.T) {
	newSmokeClient(t) // the skip-or-fail gate
	nsType := os.Getenv("OCTONOMY_TEST_NAMESPACE_TYPE")
	nsID := os.Getenv("OCTONOMY_TEST_NAMESPACE_ID")
	app := os.Getenv("OCTONOMY_TEST_APPLICATION_ID")
	if nsType == "" || nsID == "" || app == "" {
		t.Fatal("OCTONOMY_TEST_NAMESPACE_TYPE/_ID and OCTONOMY_TEST_APPLICATION_ID must be set: the harness exports them")
	}
	client, err := octonomy.New(octonomy.Config{
		BaseURL:    os.Getenv("OCTONOMY_TEST_BASE_URL"),
		Token:      os.Getenv("OCTONOMY_TEST_TOKEN"),
		TenantID:   os.Getenv("OCTONOMY_TEST_TENANT_ID"),
		APIVersion: octonomy.APIV2,
		ActorID:    "go113-smoke",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	scope := octonomy.WithNamespace(nsType, nsID)
	read := []octonomy.RequestOption{scope, octonomy.WithApplication(app)}
	isNS := func(gotType, gotID *string) bool {
		return gotType != nil && *gotType == nsType && gotID != nil && *gotID == nsID
	}

	tag, err := client.Tags.Create(ctx, octonomy.TagCreate{
		ApplicationID: octonomy.String(app), Name: "Go 1.13 v2 resource groups", Slug: uniqueSlug("smoke-rg-v2"), Type: "label",
	}, scope)
	if err != nil {
		t.Fatalf("namespaced Tags.Create: %v", err)
	}
	defer func() {
		if err := client.Tags.Delete(ctx, tag.ID, read...); err != nil {
			t.Errorf("namespaced Tags.Delete: %v", err)
		}
	}()

	alias, err := client.Aliases.Create(ctx, octonomy.TagAliasCreate{
		ApplicationID: octonomy.String(app), TagID: tag.ID, Name: "Go 1.13 v2 alias", Slug: uniqueSlug("smoke-rg-v2-alias"),
	}, scope)
	if err != nil {
		t.Fatalf("namespaced Aliases.Create: %v", err)
	}
	defer func() {
		if err := client.Aliases.Delete(ctx, alias.ID, read...); err != nil {
			t.Errorf("namespaced Aliases.Delete: %v", err)
		}
	}()
	if !isNS(alias.NamespaceType, alias.NamespaceID) {
		t.Errorf("namespaced alias: namespace = (%v, %v), want (%s, %s)", alias.NamespaceType, alias.NamespaceID, nsType, nsID)
	}

	cartID := uniqueSlug("smoke-rg-v2-cart")
	replaced, err := client.Resources.ReplaceTags(ctx, "cart", cartID, octonomy.ResourceReplace{
		ApplicationID: app, TagIDs: []string{tag.ID},
	}, scope)
	if err != nil {
		t.Fatalf("namespaced Resources.ReplaceTags: %v", err)
	}
	if replaced.Created != 1 || len(replaced.Tags) != 1 || !isNS(replaced.Tags[0].NamespaceType, replaced.Tags[0].NamespaceID) {
		t.Errorf("namespaced ReplaceTags = %+v, want one created tag carrying the namespace", replaced)
	}
	defer func() {
		if _, err := client.Resources.ReplaceTags(ctx, "cart", cartID, octonomy.ResourceReplace{ApplicationID: app}, scope); err != nil {
			t.Errorf("namespaced ReplaceTags (cleanup): %v", err)
		}
	}()

	onCart, err := client.Resources.ListTags(ctx, "cart", cartID, nil, read...)
	if err != nil {
		t.Fatalf("namespaced Resources.ListTags: %v", err)
	}
	if len(onCart.Data) != 1 || !isNS(onCart.Data[0].NamespaceType, onCart.Data[0].NamespaceID) {
		t.Errorf("namespaced Resources.ListTags = %+v, want one row carrying the namespace", onCart.Data)
	}
	carts, err := client.Tags.ListResources(ctx, tag.ID, nil, read...)
	if err != nil {
		t.Fatalf("namespaced Tags.ListResources: %v", err)
	}
	if len(carts.Data) != 1 || carts.Data[0].ResourceID != cartID || !isNS(carts.Data[0].NamespaceType, carts.Data[0].NamespaceID) {
		t.Errorf("namespaced Tags.ListResources = %+v, want the cart carrying the namespace", carts.Data)
	}
	rows, err := client.Resources.ListAuditLogs(ctx, "cart", cartID, nil, read...)
	if err != nil {
		t.Fatalf("namespaced Resources.ListAuditLogs: %v", err)
	}
	if len(rows.Data) != 1 || rows.Data[0].Action != "assignment.created" || !isNS(rows.Data[0].NamespaceType, rows.Data[0].NamespaceID) {
		t.Errorf("namespaced Resources.ListAuditLogs = %+v, want one creation row carrying the namespace", rows.Data)
	}
}
