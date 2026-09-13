// Command assignments demonstrates linking tags to external resources: the
// idempotent create, the alias form, the bulk counters, and the bulk call's
// all-or-nothing failure.
//
//	make dev-server              # boots a real Octonomy and prints these exports
//	go run ./examples/assignments
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
	// Assignments are ALWAYS application-scoped: ApplicationID is required on
	// every call below, unlike tags and vocabularies where it is optional.
	appID := mustEnv("OCTONOMY_APPLICATION_ID")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	featured, err := client.Tags.Create(ctx, octonomy.TagCreate{
		Name: "Featured", Slug: unique("featured"), Type: "label",
	})
	if err != nil {
		log.Fatalf("create tag: %v", err)
	}
	clearance, err := client.Tags.Create(ctx, octonomy.TagCreate{
		Name: "Clearance", Slug: unique("clearance"), Type: "label",
	})
	if err != nil {
		log.Fatalf("create second tag: %v", err)
	}
	aliasSlug := unique("promo")
	if _, err := client.Aliases.Create(ctx, octonomy.TagAliasCreate{
		TagID: featured.ID, Name: "Promo", Slug: aliasSlug,
	}); err != nil {
		log.Fatalf("create alias: %v", err)
	}

	orderID := unique("order")
	assignment, err := client.Assignments.Create(ctx, octonomy.AssignmentCreate{
		ApplicationID: appID,
		TagID:         octonomy.String(featured.ID),
		ResourceType:  "order",
		ResourceID:    orderID,
		// AssignedBy is a field on the ROW, distinct from the X-Actor-ID header
		// WithActor sets, which attributes the request.
		AssignedBy: octonomy.String("examples/assignments"),
	})
	if err != nil {
		log.Fatalf("assign: %v", err)
	}
	fmt.Printf("assigned %s to order/%s (%s)\n", featured.Slug, orderID, assignment.ID)

	// IT IS IDEMPOTENT. Assigning the same tag again returns the EXISTING row --
	// not a duplicate, and not a conflict. There is no need to check first.
	again, err := client.Assignments.Create(ctx, octonomy.AssignmentCreate{
		ApplicationID: appID,
		TagID:         octonomy.String(featured.ID),
		ResourceType:  "order",
		ResourceID:    orderID,
	})
	if err != nil {
		log.Fatalf("re-assign: %v", err)
	}
	fmt.Printf("re-assigning returned the same row: %v\n", again.ID == assignment.ID)

	// The alias form: name the tag by an alias slug and the SERVER resolves it,
	// with the same precedence Tags.Resolve uses. The row records the canonical
	// tag, which is why AssignmentRemove takes a TagID and no alias form.
	viaAlias, err := client.Assignments.Create(ctx, octonomy.AssignmentCreate{
		ApplicationID: appID,
		AliasSlug:     octonomy.String(aliasSlug),
		ResourceType:  "order",
		ResourceID:    orderID,
	})
	if err != nil {
		log.Fatalf("assign by alias: %v", err)
	}
	fmt.Printf("alias %s resolved to the canonical tag: %v\n", aliasSlug, viaAlias.TagID == featured.ID)

	// Bulk assigns MANY TAGS TO ONE RESOURCE -- it names the resource once, so
	// spreading across resources is a loop over this call. The counters split
	// the result by what this call actually did.
	bulk, err := client.Assignments.BulkAssign(ctx, octonomy.BulkAssign{
		ApplicationID: appID,
		ResourceType:  "order",
		ResourceID:    orderID,
		TagIDs:        []string{featured.ID, clearance.ID},
	})
	if err != nil {
		log.Fatalf("bulk assign: %v", err)
	}
	fmt.Printf("bulk: created=%d existing=%d rows=%d\n", bulk.Created, bulk.Existing, len(bulk.Assignments))

	// ALL OR NOTHING. One unknown tag id fails the whole call; nothing is
	// partially applied and nothing is "skipped" (the Skipped counter is
	// vestigial and always 0). A caller cannot treat a bulk error as "some of
	// them worked".
	_, err = client.Assignments.BulkAssign(ctx, octonomy.BulkAssign{
		ApplicationID: appID,
		ResourceType:  "order",
		ResourceID:    unique("order-atomic"),
		TagIDs:        []string{featured.ID, "00000000-0000-0000-0000-000000000000"},
	})
	fmt.Printf("bulk with one bad id: IsValidation=%v\n", octonomy.IsValidation(err))

	// Bulk remove reports a count, and ids that named no assignment are not an
	// error -- so Removed is routinely smaller than the number of ids sent.
	removed, err := client.Assignments.BulkRemove(ctx, octonomy.BulkRemove{
		ApplicationID: appID,
		ResourceType:  "order",
		ResourceID:    orderID,
		TagIDs:        []string{featured.ID, clearance.ID},
	})
	if err != nil {
		log.Fatalf("bulk remove: %v", err)
	}
	fmt.Printf("bulk remove: removed=%d\n", removed.Removed)

	// Remove is a DELETE of the link, not a deactivation -- an assignment is a
	// link, and an inactive link is an absent one. Removing what is already gone
	// is a 204, not a 404.
	if err := client.Assignments.Remove(ctx, octonomy.AssignmentRemove{
		ApplicationID: appID,
		TagID:         featured.ID,
		ResourceType:  "order",
		ResourceID:    orderID,
	}); err != nil {
		log.Fatalf("remove an already-removed assignment: %v", err)
	}
	fmt.Println("removing an already-removed assignment: no error")
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
		ActorID:  "example-assignments",
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
