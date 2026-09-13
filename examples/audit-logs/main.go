// Command audit-logs demonstrates the append-only history: rows arrive newest
// first, a caller-supplied request id comes back on them, and one operation id
// reconstructs a multi-row mutation as the single act it was.
//
//	make dev-server              # boots a real Octonomy and prints these exports
//	go run ./examples/audit-logs
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
	appID := mustEnv("OCTONOMY_APPLICATION_ID")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tag, err := client.Tags.Create(ctx, octonomy.TagCreate{
		Name: "Seasonal", Slug: unique("seasonal"), Type: "label",
	})
	if err != nil {
		log.Fatalf("create tag: %v", err)
	}

	// A request id is per call and never minted by the SDK: supply your own and
	// the server threads it into the audit row, so a log line in your service
	// and a row here name the same string.
	requestID := unique("req-example")
	if _, err := client.Tags.Update(ctx, tag.ID, octonomy.TagUpdate{
		Name: octonomy.String("Seasonal (renamed)"),
	}, octonomy.WithRequestID(requestID)); err != nil {
		log.Fatalf("rename tag: %v", err)
	}

	// Audit logs are LIST-ONLY: server-written history, so there is no Get and
	// no writes. A token without the audit:read scope gets a 403 here
	// (IsForbidden), not an empty page.
	rows, err := client.Tags.ListAuditLogs(ctx, tag.ID, &octonomy.TagListAuditLogsParams{
		ListOptions: octonomy.ListOptions{Limit: 50},
	})
	if err != nil {
		if octonomy.IsForbidden(err) {
			log.Fatalf("this token lacks the audit:read scope: %v", err)
		}
		log.Fatalf("list the tag's audit rows: %v", err)
	}
	// Newest first, so the rename is row 0 and the create is behind it.
	for _, row := range rows.Data {
		fmt.Printf("%s %s/%s actor=%s\n", row.Action, row.EntityType, row.EntityID, deref(row.ActorID))
	}

	for _, row := range rows.Data {
		if row.Action != "tag.updated" {
			continue
		}
		// Changes is an untyped object -- {"before": ..., "after": ...} today,
		// plus "cascaded_alias_ids" (an ARRAY) on a deactivation that cascaded.
		// Read it with a checked assertion; a struct here would drop keys.
		if after, ok := row.Changes["after"].(map[string]any); ok {
			fmt.Printf("renamed to %v, request_id matches ours: %v\n",
				after["name"], deref(row.RequestID) == requestID)
		}
		break
	}

	// One replace, several rows, one OperationID. Filtering on it is how a bulk
	// call or a replace is read back as a single act rather than as unrelated
	// churn -- the removals and the additions carry the same id.
	gift, err := client.Tags.Create(ctx, octonomy.TagCreate{Name: "Gift", Slug: unique("gift"), Type: "label"})
	if err != nil {
		log.Fatalf("create second tag: %v", err)
	}
	cartID := unique("cart")
	if _, err := client.Resources.ReplaceTags(ctx, "cart", cartID, octonomy.ResourceReplace{
		ApplicationID: appID,
		TagIDs:        []string{tag.ID, gift.ID},
	}); err != nil {
		log.Fatalf("replace tags: %v", err)
	}
	resourceRows, err := client.Resources.ListAuditLogs(ctx, "cart", cartID, nil)
	if err != nil {
		log.Fatalf("list the resource's audit rows: %v", err)
	}
	if len(resourceRows.Data) == 0 {
		log.Fatal("the replace recorded no audit rows")
	}
	operationID := resourceRows.Data[0].OperationID

	// Every filter is an EXACT match, and the entity is spelled differently from
	// the action: the rows are entity_type "tag_assignment" while their actions
	// are "assignment.created" and "assignment.removed". EntityType "assignment"
	// returns an empty page rather than an error.
	sameOperation, err := client.AuditLogs.List(ctx, &octonomy.AuditLogListParams{
		OperationID: octonomy.String(operationID),
		EntityType:  octonomy.String("tag_assignment"),
	})
	if err != nil {
		log.Fatalf("list by operation: %v", err)
	}
	fmt.Printf("operation %s wrote %d assignment row(s)\n", operationID, len(sameOperation.Data))

	misspelled, err := client.AuditLogs.List(ctx, &octonomy.AuditLogListParams{
		OperationID: octonomy.String(operationID),
		EntityType:  octonomy.String("assignment"),
	})
	if err != nil {
		log.Fatalf("list by the wrong entity type: %v", err)
	}
	fmt.Printf("entity_type=\"assignment\" (the action's spelling): %d row(s), no error\n", len(misspelled.Data))
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
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
		ActorID:  "example-audit-logs",
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
