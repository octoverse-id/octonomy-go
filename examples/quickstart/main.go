// Command quickstart demonstrates the Octonomy Go SDK end to end: configure a
// client, create a vocabulary and a tag, recover from a typed conflict, list
// tags, walk every page, and read metadata without a panicking assertion.
//
// Run it against a local Octonomy instance:
//
//	make dev-server             # boots a real Octonomy and prints these exports
//	go run ./examples/quickstart
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	octonomy "github.com/octoverse-id/octonomy-go"
)

const (
	vocabSlug = "labels"
	tagSlug   = "featured"
	tagType   = "label"
)

func main() {
	client, err := octonomy.New(octonomy.Config{
		BaseURL:  env("OCTONOMY_BASE_URL", "http://127.0.0.1:8000"),
		Token:    mustEnv("OCTONOMY_TOKEN"),
		TenantID: mustEnv("OCTONOMY_TENANT_ID"),
		ActorID:  "quickstart-example",
		// APIVersion is unset, so this targets /api/v1 -- this line's default,
		// which an upgrade never moves. Set octonomy.APIV2 for the namespace
		// axis; examples/namespaces does.
	})
	if err != nil {
		log.Fatalf("configure client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	vocab, err := client.Vocabularies.Create(ctx, octonomy.VocabularyCreate{
		Name: "Labels",
		Slug: vocabSlug,
	})
	if err != nil {
		// Creating the same slug twice is a typed CONFLICT, and the slug filter
		// is how you recover the row that is already there -- there is no
		// /vocabularies/{slug} route. A failed Create returns a nil *Vocabulary,
		// so the recovery has to produce one; this library never panics, and
		// dereferencing that nil is how a caller makes it look as if it had.
		if !octonomy.IsConflict(err) {
			log.Fatalf("create vocabulary: %v", err)
		}
		//
		// The slug is unique among the ACTIVE rows of one scope, not across the
		// tenant: an application's vocabulary may carry it too, and a list with
		// no ApplicationID returns every application's rows as well as the
		// tenant-shared ones. This one was created with no ApplicationID, so
		// the row it collided with is the shared one -- pick that, not the
		// first row.
		existing, listErr := client.Vocabularies.List(ctx, &octonomy.VocabularyListParams{
			Slug: octonomy.String(vocabSlug),
		})
		if listErr != nil {
			log.Fatalf("find the existing vocabulary: %v", listErr)
		}
		for i := range existing.Data {
			if existing.Data[i].ApplicationID == nil {
				vocab = &existing.Data[i]
				break
			}
		}
		if vocab == nil {
			log.Fatalf("vocabulary %q conflicted, but no tenant-shared row carries that slug", vocabSlug)
		}
	}
	fmt.Printf("vocabulary %s (%s)\n", vocab.Name, vocab.ID)

	tag, err := client.Tags.Create(ctx, octonomy.TagCreate{
		Name:         "Featured",
		Slug:         tagSlug,
		Type:         tagType,
		VocabularyID: octonomy.String(vocab.ID),
		Metadata:     octonomy.Metadata{"source": "quickstart"},
	})
	if err != nil {
		// The same recovery, on the pair that is actually unique: tag slugs are
		// unique per TYPE, so both filters are needed -- and, as above, per
		// scope, so the tenant-shared row is the one to pick.
		if !octonomy.IsConflict(err) {
			log.Fatalf("create tag: %v", err)
		}
		existing, listErr := client.Tags.List(ctx, &octonomy.TagListParams{
			Slug: octonomy.String(tagSlug),
			Type: octonomy.String(tagType),
		})
		if listErr != nil {
			log.Fatalf("find the existing tag: %v", listErr)
		}
		for i := range existing.Data {
			if existing.Data[i].ApplicationID == nil {
				tag = &existing.Data[i]
				break
			}
		}
		if tag == nil {
			log.Fatalf("tag %q/%q conflicted, but no tenant-shared row carries that pair", tagType, tagSlug)
		}
	}
	fmt.Printf("tag %s (%s)\n", tag.Name, tag.ID)

	page, err := client.Tags.List(ctx, &octonomy.TagListParams{
		Type:        octonomy.String(tagType),
		ListOptions: octonomy.ListOptions{Limit: 20},
	})
	if err != nil {
		log.Fatalf("list tags: %v", err)
	}
	fmt.Printf("tenant has %d label tag(s) on this page\n", len(page.Data))

	// Each is that same list call in a loop, for when one page is not enough.
	// It costs ONE REQUEST PER PAGE -- 20 at a time here -- and hands back the
	// offset it reached so a failure can resume instead of starting over.
	//
	// The page function returns an octonomy.Page, which every list type
	// implements, and the callback receives each row as an interface{} holding
	// the row's VALUE -- an octonomy.Tag, not a *Tag. Assert it with the
	// two-value form: the closures are easy to edit apart, and the one-value
	// form panics when they disagree.
	//
	// An offset walk is not a snapshot. A row written mid-walk shifts the
	// window, and on a server older than 3.2.1 GET /tags had no ORDER BY at
	// all, so its pages could repeat or miss rows with nothing writing.
	// De-duplicate on ID: it is cheap and removes the repeated half. A missed
	// row leaves no trace -- see the Each doc comment.
	walked := 0
	seen := map[string]bool{}
	offset, err := octonomy.Each(ctx, octonomy.ListOptions{Limit: 20},
		func(ctx context.Context, o octonomy.ListOptions) (octonomy.Page, error) {
			return client.Tags.List(ctx, &octonomy.TagListParams{
				Type:        octonomy.String(tagType),
				ListOptions: o,
			})
		},
		func(item interface{}) error {
			t, ok := item.(octonomy.Tag)
			if !ok {
				return fmt.Errorf("walk tags: got a %T", item)
			}
			walked++
			seen[t.ID] = true
			return nil
		},
	)
	if err != nil {
		log.Fatalf("walk tags: %v (resume from offset %d)", err, offset)
	}
	fmt.Printf("walked %d label tag row(s) across every page, %d distinct\n", walked, len(seen))

	// Metadata is map[string]interface{}, decoded by encoding/json: a string
	// arrives as a string, but every JSON number arrives as a float64 and an
	// object as a map[string]interface{}. Read a value with the two-value type
	// assertion -- the one-value form panics the day the stored shape changes.
	source, ok := tag.Metadata["source"].(string)
	fmt.Printf("tag metadata source=%q (a string: %v)\n", source, ok)
}

// --- configuration ------------------------------------------------------------
//
// Every example that calls the API reads the same three variables, so one export
// block drives all of them. `make dev-server` prints one export block carrying
// every variable the examples read.
//
// The block is repeated in each of them rather than shared, deliberately: an
// example is copied whole, and a helper package would move the one part a reader
// has to adapt -- how the client gets its credentials -- out of the file they
// are reading.

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
