// Command quickstart demonstrates the Octonomy Go SDK end to end: configure a
// client, create a vocabulary and a tag, list tags, and handle a typed error.
//
// Run it against a local Octonomy instance:
//
//	OCTONOMY_BASE_URL=http://localhost:8000 \
//	OCTONOMY_TOKEN=svc_... \
//	OCTONOMY_TENANT_ID=acme \
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

func main() {
	client, err := octonomy.New(octonomy.Config{
		BaseURL:  env("OCTONOMY_BASE_URL", "http://localhost:8000"),
		Token:    os.Getenv("OCTONOMY_TOKEN"),
		TenantID: env("OCTONOMY_TENANT_ID", "acme"),
		ActorID:  "quickstart-example",
	})
	if err != nil {
		log.Fatalf("configure client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	vocab, err := client.Vocabularies.Create(ctx, octonomy.VocabularyCreate{
		Name: "Labels",
		Slug: "labels",
	})
	if err != nil {
		log.Fatalf("create vocabulary: %v", err)
	}
	fmt.Printf("created vocabulary %s (%s)\n", vocab.Name, vocab.ID)

	tag, err := client.Tags.Create(ctx, octonomy.TagCreate{
		Name:         "Featured",
		Slug:         "featured",
		Type:         "label",
		VocabularyID: octonomy.String(vocab.ID),
		Metadata:     octonomy.Metadata{"source": "quickstart"},
	})
	if err != nil {
		// Creating the same (type, slug) twice returns a typed conflict.
		if octonomy.IsConflict(err) {
			fmt.Println("tag already exists; continuing")
		} else {
			log.Fatalf("create tag: %v", err)
		}
	} else {
		fmt.Printf("created tag %s (%s)\n", tag.Name, tag.ID)
	}

	page, err := client.Tags.List(ctx, &octonomy.TagListParams{
		Type:        octonomy.String("label"),
		ListOptions: octonomy.ListOptions{Limit: 20},
	})
	if err != nil {
		log.Fatalf("list tags: %v", err)
	}
	fmt.Printf("tenant has %d label tag(s) on this page\n", len(page.Data))
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
