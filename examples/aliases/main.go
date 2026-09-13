// Command aliases demonstrates tag aliases: the two routes that return the same
// rows, re-pointing an alias at a different tag, and the deactivation that
// cascades from a canonical tag to every alias on it.
//
//	make dev-server          # boots a real Octonomy and prints these exports
//	go run ./examples/aliases
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

	sneakers, err := client.Tags.Create(ctx, octonomy.TagCreate{
		Name: "Sneakers",
		Slug: unique("sneakers"),
		Type: "label",
	})
	if err != nil {
		log.Fatalf("create canonical tag: %v", err)
	}
	trainers, err := client.Tags.Create(ctx, octonomy.TagCreate{
		Name: "Trainers",
		Slug: unique("trainers"),
		Type: "label",
	})
	if err != nil {
		log.Fatalf("create second tag: %v", err)
	}

	// An alias is an alternate identifier that resolves to a canonical tag. It
	// is a row of its own, with its own id and slug -- not a field on the tag.
	alias, err := client.Aliases.Create(ctx, octonomy.TagAliasCreate{
		TagID: sneakers.ID,
		Name:  "Kicks",
		Slug:  unique("kicks"),
	})
	if err != nil {
		log.Fatalf("create alias: %v", err)
	}
	fmt.Printf("alias %s -> tag %s\n", alias.Slug, sneakers.Slug)

	// Two routes, the same rows. The nested one is pre-filtered by the path and
	// carries a deliberately NARROWER params type: the contract documents fewer
	// filters on it, so the SDK exposes fewer.
	nested, err := client.Tags.ListAliases(ctx, sneakers.ID, nil)
	if err != nil {
		log.Fatalf("list aliases of the tag: %v", err)
	}
	collection, err := client.Aliases.List(ctx, &octonomy.TagAliasListParams{
		TagID: octonomy.String(sneakers.ID),
	})
	if err != nil {
		log.Fatalf("list the alias collection: %v", err)
	}
	fmt.Printf("via /tags/{id}/aliases: %d, via /tag-aliases?tag_id=: %d\n",
		len(nested.Data), len(collection.Data))

	// An alias is not pinned to the tag it was created against. Re-pointing it
	// through TagID is an ordinary edit -- what PATCH refuses is a change of
	// SCOPE (application or namespace), which is a 409 IsScopeImmutable.
	repointed, err := client.Aliases.Update(ctx, alias.ID, octonomy.TagAliasUpdate{
		TagID: octonomy.String(trainers.ID),
	})
	if err != nil {
		log.Fatalf("re-point alias: %v", err)
	}
	fmt.Printf("alias %s now points at %s (was %s)\n", repointed.Slug, trainers.Slug, sneakers.Slug)

	// Deactivating a canonical tag CASCADES to its active aliases. Nothing here
	// touched the alias row, and yet it stops resolving -- which is why an alias
	// that vanished from a list is worth checking against its tag.
	if err := client.Tags.Delete(ctx, trainers.ID); err != nil {
		log.Fatalf("deactivate the canonical tag: %v", err)
	}
	after, err := client.Aliases.Get(ctx, alias.ID)
	if err != nil {
		log.Fatalf("get alias after the cascade: %v", err)
	}
	fmt.Printf("after deactivating %s: alias IsActive=%v\n", trainers.Slug, after.IsActive)

	// Delete being deactivation is why an absent IsActive lists active rows
	// only: the deactivated alias is found by asking for it.
	deactivated, err := client.Aliases.List(ctx, &octonomy.TagAliasListParams{
		Slug:     octonomy.String(alias.Slug),
		IsActive: octonomy.Bool(false),
	})
	if err != nil {
		log.Fatalf("list deactivated aliases: %v", err)
	}
	fmt.Printf("IsActive=false finds it again: %d row(s)\n", len(deactivated.Data))
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
		ActorID:  "example-aliases",
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
