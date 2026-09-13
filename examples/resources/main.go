// Command resources demonstrates tagging from the resource's side: the replace
// that is not a merge, the empty replace that clears a resource outright, and
// the two mirrored list routes.
//
//	make dev-server            # boots a real Octonomy and prints these exports
//	go run ./examples/resources
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	octonomy "github.com/octoverse-id/octonomy-go/v2"
)

func main() {
	client := mustClient()
	appID := mustEnv("OCTONOMY_APPLICATION_ID")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tags := map[string]*octonomy.Tag{}
	for _, name := range []string{"gift", "fragile", "express"} {
		tag, err := client.Tags.Create(ctx, octonomy.TagCreate{
			Name: name, Slug: unique(name), Type: "label",
		})
		if err != nil {
			log.Fatalf("create tag %s: %v", name, err)
		}
		tags[name] = tag
	}

	cartID := unique("cart")
	first, err := client.Resources.ReplaceTags(ctx, "cart", cartID, octonomy.ResourceReplace{
		ApplicationID: appID,
		TagIDs:        []string{tags["gift"].ID, tags["fragile"].ID},
	})
	if err != nil {
		log.Fatalf("first replace: %v", err)
	}
	fmt.Printf("replace #1: created=%d removed=%d -> %s\n", first.Created, first.Removed, names(first.Tags))

	// THIS REPLACES, IT DOES NOT MERGE. "fragile" survives because the request
	// names it; "gift" is removed because it does not. Sending only what you
	// want to ADD deletes everything else -- read the current set and send the
	// union if you meant to append.
	second, err := client.Resources.ReplaceTags(ctx, "cart", cartID, octonomy.ResourceReplace{
		ApplicationID: appID,
		TagIDs:        []string{tags["fragile"].ID, tags["express"].ID},
	})
	if err != nil {
		log.Fatalf("second replace: %v", err)
	}
	// One added and one removed, while the set stayed the same size: the counts
	// are assignments changed, not the size of the result.
	fmt.Printf("replace #2: created=%d removed=%d -> %s\n", second.Created, second.Removed, names(second.Tags))

	// The resource's side of the link. Each row is an ASSIGNMENT carrying the
	// tag inline -- AssignmentID, AssignedAt, and a nested Tag -- so reading a
	// resource's tags costs one request, not one per tag. ApplicationID is
	// required here; the server refuses the call without it.
	onResource, err := client.Resources.ListTags(ctx, "cart", cartID, &octonomy.ResourceListTagsParams{
		ApplicationID: octonomy.String(appID),
	})
	if err != nil {
		log.Fatalf("list the resource's tags: %v", err)
	}
	for _, row := range onResource.Data {
		fmt.Printf("  cart/%s has %s (assignment %s)\n", cartID, row.Tag.Slug, row.AssignmentID)
	}

	// The tag's side of the same links, and a different model: a TagResource
	// carries no tag (you started from it) and no assignment id (the route
	// answers "what is this tag on", not "which links exist").
	onTag, err := client.Tags.ListResources(ctx, tags["express"].ID, nil)
	if err != nil {
		log.Fatalf("list the tag's resources: %v", err)
	}
	for _, row := range onTag.Data {
		fmt.Printf("  %s is on %s/%s\n", tags["express"].Slug, row.ResourceType, row.ResourceID)
	}

	// AN EMPTY REQUEST IS LEGAL AND CLEARS THE RESOURCE. It is not a validation
	// error, so a TagIDs slice that arrived empty from a filter matching nothing
	// wipes the resource silently and successfully. BulkAssign refuses the same
	// empty body; this route does not.
	cleared, err := client.Resources.ReplaceTags(ctx, "cart", cartID, octonomy.ResourceReplace{
		ApplicationID: appID,
	})
	if err != nil {
		log.Fatalf("clearing replace: %v", err)
	}
	fmt.Printf("empty replace: created=%d removed=%d -> %d tag(s) left\n",
		cleared.Created, cleared.Removed, len(cleared.Tags))
}

func names(tags []octonomy.Tag) string {
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		out = append(out, tag.Name)
	}
	return "[" + strings.Join(out, " ") + "]"
}

// --- configuration ------------------------------------------------------------
//
// Every example reads the same three variables, plus OCTONOMY_APPLICATION_ID
// here. `make dev-server` prints exactly this block.

func mustClient() *octonomy.Client {
	client, err := octonomy.New(octonomy.Config{
		BaseURL:  env("OCTONOMY_BASE_URL", "http://127.0.0.1:8000"),
		Token:    mustEnv("OCTONOMY_TOKEN"),
		TenantID: mustEnv("OCTONOMY_TENANT_ID"),
		ActorID:  "example-resources",
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
func unique(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano()%1e9)
}
