// Command vocabularies demonstrates the vocabulary endpoints: create one, find
// it again by slug without paging the collection, replace and clear its
// metadata, and watch Delete deactivate rather than remove.
//
//	make dev-server               # boots a real Octonomy and prints these exports
//	go run ./examples/vocabularies
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	octonomy "github.com/octoverse-id/octonomy-go/v2"
)

func main() {
	client := mustClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	slug := unique("catalog")
	vocab, err := client.Vocabularies.Create(ctx, octonomy.VocabularyCreate{
		Name:        "Catalog",
		Slug:        slug,
		Description: octonomy.String("created by examples/vocabularies"),
	})
	if err != nil {
		log.Fatalf("create vocabulary: %v", err)
	}
	fmt.Printf("created %s (%s)\n", vocab.Slug, vocab.ID)

	// Slug is an EXACT-match filter and the only way to look a vocabulary up by
	// slug -- there is no /vocabularies/{slug} route. Paging the collection and
	// filtering in Go instead is correct until a tenant outgrows the page you
	// happened to ask for, and then quietly wrong.
	page, err := client.Vocabularies.List(ctx, &octonomy.VocabularyListParams{
		Slug: octonomy.String(slug),
	})
	if err != nil {
		log.Fatalf("list by slug: %v", err)
	}
	fmt.Printf("list by slug returned %d row(s)\n", len(page.Data))

	// Metadata REPLACES the stored object; it never merges. The field is a
	// *Metadata so that "clear it" can be said at all:
	//
	//	&Metadata{"k": v}  replaces the object
	//	&Metadata{}        empties it
	//	nil                omits the key, leaving it untouched
	if _, err := client.Vocabularies.Update(ctx, vocab.ID, octonomy.VocabularyUpdate{
		Metadata: &octonomy.Metadata{"owner": "merchandising", "tier": "gold"},
	}); err != nil {
		log.Fatalf("set metadata: %v", err)
	}
	replaced, err := client.Vocabularies.Update(ctx, vocab.ID, octonomy.VocabularyUpdate{
		Metadata: &octonomy.Metadata{"owner": "growth"},
	})
	if err != nil {
		log.Fatalf("replace metadata: %v", err)
	}
	// "tier" is gone. A PATCH carrying only "owner" replaced the whole object.
	fmt.Printf("after replacing metadata: %v\n", replaced.Metadata)

	renamed, err := client.Vocabularies.Update(ctx, vocab.ID, octonomy.VocabularyUpdate{
		Name: octonomy.String("Catalog (renamed)"),
	})
	if err != nil {
		log.Fatalf("rename: %v", err)
	}
	// A nil Metadata sends no metadata key at all, so the rename left it alone.
	fmt.Printf("after renaming to %q: metadata %v\n", renamed.Name, renamed.Metadata)

	cleared, err := client.Vocabularies.Update(ctx, vocab.ID, octonomy.VocabularyUpdate{
		Metadata: &octonomy.Metadata{},
	})
	if err != nil {
		log.Fatalf("clear metadata: %v", err)
	}
	fmt.Printf("after &Metadata{}: metadata %v (%d key(s))\n", cleared.Metadata, len(cleared.Metadata))

	// Delete is DEACTIVATION, not removal. The row survives a Get, and the list
	// filters it out only because IsActive is unset -- which is why
	// IsActive: Bool(false) is how you find deleted rows.
	if err := client.Vocabularies.Delete(ctx, vocab.ID); err != nil {
		log.Fatalf("delete: %v", err)
	}
	deleted, err := client.Vocabularies.Get(ctx, vocab.ID)
	if err != nil {
		log.Fatalf("get after delete: %v", err)
	}
	fmt.Printf("after delete: the row is still there, IsActive=%v\n", deleted.IsActive)

	active, err := client.Vocabularies.List(ctx, &octonomy.VocabularyListParams{
		Slug: octonomy.String(slug),
	})
	if err != nil {
		log.Fatalf("list active: %v", err)
	}
	inactive, err := client.Vocabularies.List(ctx, &octonomy.VocabularyListParams{
		Slug:     octonomy.String(slug),
		IsActive: octonomy.Bool(false),
	})
	if err != nil {
		log.Fatalf("list inactive: %v", err)
	}
	fmt.Printf("default list: %d row(s); IsActive=false: %d row(s)\n", len(active.Data), len(inactive.Data))
}

// --- configuration ------------------------------------------------------------
//
// Every example reads the same three variables, so one export block drives all
// of them. `make dev-server` prints exactly this block.

func mustClient() *octonomy.Client {
	client, err := octonomy.New(octonomy.Config{
		BaseURL:  env("OCTONOMY_BASE_URL", "http://127.0.0.1:8000"),
		Token:    mustEnv("OCTONOMY_TOKEN"),
		TenantID: mustEnv("OCTONOMY_TENANT_ID"),
		ActorID:  "example-vocabularies",
	})
	if err != nil {
		log.Fatalf("configure client: %v", err)
	}
	return client
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("%s is not set -- run `make dev-server` and export the block it prints", key)
	}
	return v
}

// unique keeps repeat runs against one long-lived dev server from colliding on
// the server's slug uniqueness constraint.
//
// The WHOLE nanosecond timestamp, not a remainder of it: taking it modulo a
// second reduces the namespace to "which nanosecond within this second", so two
// runs a second apart at the same offset produce the same slug -- and the
// collision surfaces as the conflict this helper exists to avoid.
func unique(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}
