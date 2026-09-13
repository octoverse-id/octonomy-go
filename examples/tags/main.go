// Command tags demonstrates the tag endpoints: a two-level hierarchy, the
// (type, slug) uniqueness rule that decides when a create is a conflict, and
// the free-text search that finds a tag without knowing its id.
//
//	make dev-server       # boots a real Octonomy and prints these exports
//	go run ./examples/tags
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

	vocab, err := client.Vocabularies.Create(ctx, octonomy.VocabularyCreate{
		Name: "Apparel",
		Slug: unique("apparel"),
	})
	if err != nil {
		log.Fatalf("create vocabulary: %v", err)
	}

	// A tag names its vocabulary by id. VocabularyID is optional -- a tag
	// without one is loose in the tenant rather than invalid.
	parentSlug := unique("outerwear")
	parent, err := client.Tags.Create(ctx, octonomy.TagCreate{
		Name:         "Outerwear",
		Slug:         parentSlug,
		Type:         "category",
		VocabularyID: octonomy.String(vocab.ID),
	})
	if err != nil {
		log.Fatalf("create parent tag: %v", err)
	}
	fmt.Printf("created parent %s (%s)\n", parent.Slug, parent.ID)

	// The hierarchy is one field: ParentID. There is no /tags/{id}/children
	// route -- the tree is walked by filtering the list on each parent in turn.
	child, err := client.Tags.Create(ctx, octonomy.TagCreate{
		Name:         "Rain jackets",
		Slug:         unique("rain-jackets"),
		Type:         "category",
		VocabularyID: octonomy.String(vocab.ID),
		ParentID:     octonomy.String(parent.ID),
		Metadata:     octonomy.Metadata{"season": "aw"},
	})
	if err != nil {
		log.Fatalf("create child tag: %v", err)
	}
	fmt.Printf("created child %s under %s\n", child.Slug, parent.Slug)

	children, err := client.Tags.List(ctx, &octonomy.TagListParams{
		ParentID: octonomy.String(parent.ID),
	})
	if err != nil {
		log.Fatalf("list children: %v", err)
	}
	fmt.Printf("%s has %d child tag(s)\n", parent.Slug, len(children.Data))

	// Uniqueness is on the PAIR (type, slug), not on the slug alone. The same
	// slug under the same type is a typed conflict...
	_, err = client.Tags.Create(ctx, octonomy.TagCreate{
		Name: "Outerwear again",
		Slug: parentSlug,
		Type: "category",
	})
	switch {
	case err == nil:
		log.Fatal("re-creating the same (type, slug) should have conflicted")
	case octonomy.IsConflict(err):
		fmt.Printf("same (type, slug) again: conflict, as expected\n")
	default:
		log.Fatalf("re-create: %v", err)
	}

	// ...while the same slug under a DIFFERENT type is an ordinary create. A
	// caller treating slugs as globally unique is reading half the key.
	otherType, err := client.Tags.Create(ctx, octonomy.TagCreate{
		Name: "Outerwear label",
		Slug: parentSlug,
		Type: "label",
	})
	if err != nil {
		log.Fatalf("create same slug under another type: %v", err)
	}
	fmt.Printf("same slug, type %q: created %s\n", otherType.Type, otherType.ID)

	// Query is the server's free-text `q`, matching name OR slug. Type narrows
	// it to one of the two rows the slug now names.
	hits, err := client.Tags.List(ctx, &octonomy.TagListParams{
		Query: octonomy.String(parentSlug),
		Type:  octonomy.String("category"),
	})
	if err != nil {
		log.Fatalf("search: %v", err)
	}
	for _, tag := range hits.Data {
		fmt.Printf("search hit: %s (%s) type=%s usage=%d\n", tag.Name, tag.Slug, tag.Type, tag.UsageCount)
	}
}

// --- configuration ------------------------------------------------------------
//
// Every example that calls the API reads the same three variables, so one export
// block drives all of them. `make dev-server` prints exactly this block.
//
// The block is repeated in each of them rather than shared, deliberately: an
// example is copied whole, and a helper package would move the one part a reader
// has to adapt -- how the client gets its credentials -- out of the file they
// are reading.

func mustClient() *octonomy.Client {
	client, err := octonomy.New(octonomy.Config{
		BaseURL:  env("OCTONOMY_BASE_URL", "http://127.0.0.1:8000"),
		Token:    mustEnv("OCTONOMY_TOKEN"),
		TenantID: mustEnv("OCTONOMY_TENANT_ID"),
		ActorID:  "example-tags",
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
