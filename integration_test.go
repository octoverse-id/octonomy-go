//go:build integration
// +build integration

// Minimal integration smoke test for the frozen Go 1.13 line.
//
// Both build-constraint forms are present on purpose: Go 1.17+ reads
// //go:build, Go 1.13 reads only // +build, and gofmt keeps the two in sync.
//
// This is deliberately a smoke test, not a suite. The compat line is frozen, so
// what needs proving on every change is narrow: that the client still speaks to
// a real, current Octonomy server at all. Five assertions cover it -- the
// {data, pagination} envelope (which the vendored spec does not describe, so
// only a real server can confirm it), a list of each implemented resource, and
// one real error envelope.
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
// the frozen line's ONLY real-server check reporting green without running. The
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
}
