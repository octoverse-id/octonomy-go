package octonomy

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestAuditLogs_List_AllParams(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2/audit-logs" {
			t.Errorf("got %s %s, want GET /api/v2/audit-logs", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q, want Bearer test-token", got)
		}
		if got := r.Header.Get("X-Tenant-ID"); got != "tenant-1" {
			t.Errorf("X-Tenant-ID = %q, want tenant-1", got)
		}
		q := r.URL.Query()
		want := map[string]string{
			"action":         "tag.updated",
			"actor_id":       "svc-importer",
			"application_id": "commerce",
			"entity_id":      "tag_1",
			"entity_type":    "tag",
			"operation_id":   "op_1",
			"resource_id":    "ord_9",
			"resource_type":  "order",
			"tag_id":         "tag_1",
			"limit":          "25",
			"offset":         "50",
		}
		for k, v := range want {
			if got := q.Get(k); got != v {
				t.Errorf("query[%s] = %q, want %q", k, got, v)
			}
		}
		writeJSON(t, w, http.StatusOK, map[string]any{
			"data":       []AuditLog{{ID: "audit_1", Action: "tag.updated"}},
			"pagination": map[string]any{"limit": 25, "offset": 50, "count": 1},
		})
	})

	page, err := c.AuditLogs.List(context.Background(), &AuditLogListParams{
		ListOptions:   ListOptions{Limit: 25, Offset: 50},
		Action:        String("tag.updated"),
		ActorID:       String("svc-importer"),
		ApplicationID: String("commerce"),
		EntityID:      String("tag_1"),
		EntityType:    String("tag"),
		OperationID:   String("op_1"),
		ResourceID:    String("ord_9"),
		ResourceType:  String("order"),
		TagID:         String("tag_1"),
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Data) != 1 || page.Data[0].ID != "audit_1" || page.Pagination.Count != 1 {
		t.Errorf("unexpected page: %+v", page)
	}
}

func TestAuditLogs_List_NilParams(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			t.Errorf("expected no query params, got %q", r.URL.RawQuery)
		}
		writeJSON(t, w, http.StatusOK, map[string]any{
			"data":       []AuditLog{},
			"pagination": map[string]any{"limit": 50, "offset": 0, "count": 0},
		})
	})

	page, err := c.AuditLogs.List(context.Background(), nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// An empty page decodes to a non-nil slice, as everywhere.
	if page.Data == nil || len(page.Data) != 0 {
		t.Errorf("Data = %v, want an empty non-nil slice", page.Data)
	}
}

// The audit:read scope is separate from tags:read, so a token that reads tags
// perfectly well gets a 403 here. It is a token misconfiguration rather than a
// caller mistake, and it must not look like an empty history.
func TestAuditLogs_List_WithoutTheScopeIsForbidden(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusForbidden, map[string]any{
			"error": map[string]any{
				"code":       CodeForbidden,
				"message":    "Service client is not granted the required scope.",
				"details":    map[string]any{"scope": []string{"audit:read"}},
				"request_id": "req_7",
			},
		})
	})

	page, err := c.AuditLogs.List(context.Background(), nil)
	if !IsForbidden(err) {
		t.Fatalf("expected forbidden, got page %+v and err %v", page, err)
	}
	apiErr, ok := AsAPIError(err)
	if !ok {
		t.Fatalf("expected an *APIError, got %T", err)
	}
	if apiErr.StatusCode != http.StatusForbidden || apiErr.RequestID != "req_7" {
		t.Errorf("unexpected error: %+v", apiErr)
	}
	if _, named := apiErr.Details["scope"]; !named {
		t.Errorf("Details should name the missing scope, got %v", apiErr.Details)
	}
}

// The vendored contract claims these routes return a BARE ARRAY of AuditLog
// (`type: array`), as it does for bulk-assign and the resource-tag replace. The
// server sends the {data, pagination} envelope like every other list.
//
// A client written from the spec's claim decodes an empty slice and a nil error
// -- #32 in a new place -- so both spellings of it are asserted to be errors
// here rather than empty results.
func TestAuditLogs_List_TheSpecsBareArrayIsAnError(t *testing.T) {
	tests := []struct {
		name string
		body any
	}{
		{"bare array, as the spec claims", []map[string]any{{"id": "audit_1"}}},
		{"envelope without pagination", map[string]any{"data": []map[string]any{{"id": "audit_1"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, http.StatusOK, tt.body)
			})

			page, err := c.AuditLogs.List(context.Background(), nil)
			if err == nil {
				t.Fatalf("expected an error, got %+v", page)
			}
		})
	}
}

// Decodes a RAW body written with the wire's own field names.
//
// Every other fixture in this file is marshalled from AuditLog itself, so a
// misspelled json tag would be used for the write and the read alike and
// round-trip perfectly -- the lesson #41 paid for on Assignment. This is the
// hermetic half of that pair; the smoke test is the other.
func TestAuditLog_DecodesTheWiresFieldNames(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]any{
			"data": []any{map[string]any{
				"id":             "audit_1",
				"tenant_id":      "tenant-1",
				"application_id": "commerce",
				"namespace_type": "merchant",
				"namespace_id":   "m-42",
				"action":         "tag.updated",
				"entity_type":    "tag",
				"entity_id":      "tag_1",
				"tag_id":         "tag_1",
				"resource_type":  "order",
				"resource_id":    "ord_9",
				"actor_id":       "svc-importer",
				"request_id":     "req_7",
				"operation_id":   "op_1",
				"changes": map[string]any{
					"before": map[string]any{"name": "Featured"},
					"after":  map[string]any{"name": "Featured Content"},
				},
				"metadata":   map[string]any{"source": "import"},
				"created_at": "2026-06-08T12:00:00Z",
			}},
			"pagination": map[string]any{"limit": 50, "offset": 0, "count": 1},
		})
	})

	page, err := c.AuditLogs.List(context.Background(), nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := page.Data[0]
	if got.ID != "audit_1" || got.TenantID != "tenant-1" {
		t.Errorf("identity fields did not decode: %+v", got)
	}
	if got.Action != "tag.updated" || got.EntityType != "tag" || got.EntityID != "tag_1" {
		t.Errorf("action/entity fields did not decode: %+v", got)
	}
	if got.OperationID != "op_1" {
		t.Errorf("OperationID = %q, want op_1", got.OperationID)
	}
	for _, f := range []struct {
		name string
		got  *string
		want string
	}{
		{"ApplicationID", got.ApplicationID, "commerce"},
		{"NamespaceType", got.NamespaceType, "merchant"},
		{"NamespaceID", got.NamespaceID, "m-42"},
		{"TagID", got.TagID, "tag_1"},
		{"ResourceType", got.ResourceType, "order"},
		{"ResourceID", got.ResourceID, "ord_9"},
		{"ActorID", got.ActorID, "svc-importer"},
		{"RequestID", got.RequestID, "req_7"},
	} {
		if f.got == nil || *f.got != f.want {
			t.Errorf("%s = %v, want %q", f.name, f.got, f.want)
		}
	}
	before, ok := got.Changes["before"].(map[string]any)
	if !ok || before["name"] != "Featured" {
		t.Errorf("Changes[before] = %v, want the pre-change field set", got.Changes["before"])
	}
	after, ok := got.Changes["after"].(map[string]any)
	if !ok || after["name"] != "Featured Content" {
		t.Errorf("Changes[after] = %v, want the post-change field set", got.Changes["after"])
	}
	if got.Metadata["source"] != "import" {
		t.Errorf("Metadata[source] = %v, want import", got.Metadata["source"])
	}
	if !got.CreatedAt.Equal(time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("CreatedAt = %v, want 2026-06-08T12:00:00Z", got.CreatedAt)
	}
}

// Changes is an open object, and this is the row that proves it has to be.
//
// A tag.deactivated that cascaded to aliases carries "cascaded_alias_ids" -- an
// ARRAY -- alongside the before/after pair. A Before/After struct would drop it
// silently; a map[string]Metadata would fail to decode the row and take the
// whole page with it, since one bad element fails the list.
func TestAuditLog_ChangesKeepsKeysThatAreNotBeforeOrAfter(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]any{
			"data": []any{map[string]any{
				"id":     "audit_1",
				"action": "tag.deactivated",
				"changes": map[string]any{
					"before":             map[string]any{"is_active": true},
					"after":              map[string]any{"is_active": false},
					"cascaded_alias_ids": []any{"alias_1", "alias_2"},
				},
			}},
			"pagination": map[string]any{"limit": 50, "offset": 0, "count": 1},
		})
	})

	page, err := c.AuditLogs.List(context.Background(), nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	cascaded, ok := page.Data[0].Changes["cascaded_alias_ids"].([]any)
	if !ok {
		t.Fatalf("cascaded_alias_ids = %#v, want an array", page.Data[0].Changes["cascaded_alias_ids"])
	}
	if len(cascaded) != 2 || cascaded[0] != "alias_1" {
		t.Errorf("cascaded_alias_ids = %v, want both alias ids", cascaded)
	}
	if _, ok := page.Data[0].Changes["after"].(map[string]any); !ok {
		t.Error("the before/after pair must survive alongside the cascade key")
	}
}

// Nullable columns stay nil rather than becoming "": a vocabulary edit names no
// tag and no resource, and an unattributed mutation names no actor.
func TestAuditLog_NullFieldsStayNil(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]any{
			"data": []any{map[string]any{
				"id":             "audit_1",
				"tenant_id":      "tenant-1",
				"application_id": nil,
				"namespace_type": nil,
				"namespace_id":   nil,
				"action":         "vocabulary.updated",
				"entity_type":    "vocabulary",
				"entity_id":      "voc_1",
				"tag_id":         nil,
				"resource_type":  nil,
				"resource_id":    nil,
				"actor_id":       nil,
				"request_id":     nil,
				"operation_id":   "op_1",
				"changes":        map[string]any{},
				"metadata":       map[string]any{},
				"created_at":     "2026-06-08T12:00:00Z",
			}},
			"pagination": map[string]any{"limit": 50, "offset": 0, "count": 1},
		})
	})

	page, err := c.AuditLogs.List(context.Background(), nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := page.Data[0]
	if got.ApplicationID != nil || got.NamespaceType != nil || got.NamespaceID != nil {
		t.Errorf("scope fields should be nil on a global row: %+v", got)
	}
	if got.TagID != nil || got.ResourceType != nil || got.ResourceID != nil {
		t.Errorf("tag/resource handles should be nil on a vocabulary row: %+v", got)
	}
	if got.ActorID != nil || got.RequestID != nil {
		t.Errorf("ActorID/RequestID should be nil: %+v", got)
	}
	if got.EntityID != "voc_1" {
		t.Errorf("EntityID = %q, want voc_1", got.EntityID)
	}
}

func TestTags_ListAuditLogs(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2/tags/tag_1/audit-logs" {
			t.Errorf("got %s %s, want GET /api/v2/tags/tag_1/audit-logs", r.Method, r.URL.Path)
		}
		q := r.URL.Query()
		want := map[string]string{
			"action":         "tag.updated",
			"actor_id":       "svc-importer",
			"application_id": "commerce",
			"operation_id":   "op_1",
			"limit":          "10",
		}
		for k, v := range want {
			if got := q.Get(k); got != v {
				t.Errorf("query[%s] = %q, want %q", k, got, v)
			}
		}
		// The narrow params type cannot express the collection's filters, so
		// nothing may add them behind the caller's back.
		for _, unwanted := range []string{"entity_type", "entity_id", "resource_type", "resource_id", "tag_id"} {
			if q.Has(unwanted) {
				t.Errorf("query carried %s, which this route does not document", unwanted)
			}
		}
		writeJSON(t, w, http.StatusOK, map[string]any{
			"data":       []AuditLog{{ID: "audit_1", Action: "tag.updated", TagID: String("tag_1")}},
			"pagination": map[string]any{"limit": 10, "offset": 0, "count": 1},
		})
	})

	page, err := c.Tags.ListAuditLogs(context.Background(), "tag_1", &TagListAuditLogsParams{
		ListOptions:   ListOptions{Limit: 10},
		Action:        String("tag.updated"),
		ActorID:       String("svc-importer"),
		ApplicationID: String("commerce"),
		OperationID:   String("op_1"),
	})
	if err != nil {
		t.Fatalf("ListAuditLogs: %v", err)
	}
	if len(page.Data) != 1 || page.Data[0].ID != "audit_1" {
		t.Errorf("unexpected page: %+v", page)
	}
}

func TestTags_ListAuditLogs_NilParams(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			t.Errorf("expected no query params, got %q", r.URL.RawQuery)
		}
		writeJSON(t, w, http.StatusOK, map[string]any{
			"data":       []AuditLog{},
			"pagination": map[string]any{"limit": 50, "offset": 0, "count": 0},
		})
	})

	if _, err := c.Tags.ListAuditLogs(context.Background(), "tag_1", nil); err != nil {
		t.Fatalf("ListAuditLogs: %v", err)
	}
}

// Unlike every other /tags/{id} route, an unknown tag is an EMPTY PAGE here.
//
// The view filters the audit table by tag_id and never loads the tag, so a tag
// that never existed, one that was deactivated, and one outside the request's
// namespace all answer 200 with no rows. A caller expecting IsNotFound to tell
// those apart would wait for an error that does not come.
func TestTags_ListAuditLogs_UnknownTagIsAnEmptyPage(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]any{
			"data":       []AuditLog{},
			"pagination": map[string]any{"limit": 50, "offset": 0, "count": 0},
		})
	})

	page, err := c.Tags.ListAuditLogs(context.Background(), "00000000-0000-0000-0000-000000000000", nil)
	if err != nil {
		t.Fatalf("ListAuditLogs: %v", err)
	}
	if len(page.Data) != 0 {
		t.Errorf("len(Data) = %d, want 0", len(page.Data))
	}
}

func TestResources_ListAuditLogs(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2/resources/order/ord_9/audit-logs" {
			t.Errorf("got %s %s, want GET /api/v2/resources/order/ord_9/audit-logs", r.Method, r.URL.Path)
		}
		q := r.URL.Query()
		want := map[string]string{
			"action":         "assignment.created",
			"actor_id":       "svc-importer",
			"application_id": "commerce",
			"operation_id":   "op_1",
			"offset":         "20",
		}
		for k, v := range want {
			if got := q.Get(k); got != v {
				t.Errorf("query[%s] = %q, want %q", k, got, v)
			}
		}
		writeJSON(t, w, http.StatusOK, map[string]any{
			"data": []AuditLog{{
				ID:           "audit_1",
				Action:       "assignment.created",
				ResourceType: String("order"),
				ResourceID:   String("ord_9"),
			}},
			"pagination": map[string]any{"limit": 50, "offset": 20, "count": 1},
		})
	})

	page, err := c.Resources.ListAuditLogs(context.Background(), "order", "ord_9", &ResourceListAuditLogsParams{
		ListOptions:   ListOptions{Offset: 20},
		Action:        String("assignment.created"),
		ActorID:       String("svc-importer"),
		ApplicationID: String("commerce"),
		OperationID:   String("op_1"),
	})
	if err != nil {
		t.Fatalf("ListAuditLogs: %v", err)
	}
	if len(page.Data) != 1 || page.Data[0].ResourceID == nil || *page.Data[0].ResourceID != "ord_9" {
		t.Errorf("unexpected page: %+v", page)
	}
}

// The audit route shares resourcePath, so it inherits the escape-exactly-once
// guarantee -- and needs its own assertion, because a route that escapes twice
// reads the history of a resource the caller never named.
func TestResources_ListAuditLogs_EscapesTheIDExactlyOnce(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.EscapedPath(); got != "/api/v2/resources/order/ord%209/audit-logs" {
			t.Errorf("escaped path = %q, want /api/v2/resources/order/ord%%209/audit-logs", got)
		}
		if got := r.URL.Path; got != "/api/v2/resources/order/ord 9/audit-logs" {
			t.Errorf("decoded path = %q, want the literal id \"ord 9\"", got)
		}
		writeJSON(t, w, http.StatusOK, map[string]any{
			"data":       []AuditLog{},
			"pagination": map[string]any{"limit": 50, "offset": 0, "count": 0},
		})
	})

	if _, err := c.Resources.ListAuditLogs(context.Background(), "order", "ord 9", nil); err != nil {
		t.Fatalf("ListAuditLogs: %v", err)
	}
}

func TestAuditLogs_NamespaceScoping(t *testing.T) {
	t.Run("headers and include_global travel with the read", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if got := r.Header.Get(namespaceTypeHeader); got != "merchant" {
				t.Errorf("%s = %q, want merchant", namespaceTypeHeader, got)
			}
			if got := r.Header.Get(namespaceIDHeader); got != "m-42" {
				t.Errorf("%s = %q, want m-42", namespaceIDHeader, got)
			}
			q := r.URL.Query()
			if got := q.Get(includeGlobalParam); got != "true" {
				t.Errorf("%s = %q, want true", includeGlobalParam, got)
			}
			if got := q.Get(applicationIDParam); got != "commerce" {
				t.Errorf("application_id = %q, want commerce", got)
			}
			writeJSON(t, w, http.StatusOK, map[string]any{
				"data":       []AuditLog{},
				"pagination": map[string]any{"limit": 50, "offset": 0, "count": 0},
			})
		})

		_, err := c.AuditLogs.List(context.Background(), &AuditLogListParams{ApplicationID: String("commerce")},
			WithNamespace("merchant", "m-42"), WithIncludeGlobal())
		if err != nil {
			t.Fatalf("List: %v", err)
		}
	})

	// The transport's chokepoint guards cover this resource by doing nothing:
	// namespace isolation sits below application, so a namespaced read naming no
	// application is refused before the round trip.
	t.Run("a namespaced read must name its application", func(t *testing.T) {
		c := newUnreachableClient(t, APIV2)
		_, err := c.AuditLogs.List(context.Background(), nil, WithNamespace("merchant", "m-42"))
		if err == nil {
			t.Fatal("expected a namespaced read with no application to be refused")
		}
		if !strings.Contains(err.Error(), "WithApplication") {
			t.Errorf("error should name the remediation, got: %v", err)
		}
	})

	t.Run("the nested routes carry the pair too", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if got := r.Header.Get(namespaceTypeHeader); got != "merchant" {
				t.Errorf("%s = %q, want merchant", namespaceTypeHeader, got)
			}
			writeJSON(t, w, http.StatusOK, map[string]any{
				"data":       []AuditLog{},
				"pagination": map[string]any{"limit": 50, "offset": 0, "count": 0},
			})
		})

		if _, err := c.Tags.ListAuditLogs(context.Background(), "tag_1",
			&TagListAuditLogsParams{ApplicationID: String("commerce")},
			WithNamespace("merchant", "m-42")); err != nil {
			t.Fatalf("Tags.ListAuditLogs: %v", err)
		}
		if _, err := c.Resources.ListAuditLogs(context.Background(), "order", "ord_9",
			&ResourceListAuditLogsParams{ApplicationID: String("commerce")},
			WithNamespace("merchant", "m-42")); err != nil {
			t.Fatalf("Resources.ListAuditLogs: %v", err)
		}
	})
}

// All three routes exist on /api/v1 as well -- one view tree serves both
// surfaces -- and a v1 response carries no namespace fields, so they stay nil.
func TestAuditLogs_OnV1(t *testing.T) {
	paths := map[string]bool{
		"/api/v1/audit-logs":                       false,
		"/api/v1/tags/tag_1/audit-logs":            false,
		"/api/v1/resources/order/ord_9/audit-logs": false,
	}
	c := newVersionedTestClient(t, APIV1, func(w http.ResponseWriter, r *http.Request) {
		if _, known := paths[r.URL.Path]; !known {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		paths[r.URL.Path] = true
		writeJSON(t, w, http.StatusOK, map[string]any{
			"data":       []AuditLog{{ID: "audit_1", Action: "tag.created"}},
			"pagination": map[string]any{"limit": 50, "offset": 0, "count": 1},
		})
	})

	page, err := c.AuditLogs.List(context.Background(), nil)
	if err != nil {
		t.Fatalf("List on v1: %v", err)
	}
	if page.Data[0].NamespaceType != nil || page.Data[0].NamespaceID != nil {
		t.Errorf("a v1 audit row reported a namespace: %+v", page.Data[0])
	}
	if _, err := c.Tags.ListAuditLogs(context.Background(), "tag_1", nil); err != nil {
		t.Fatalf("Tags.ListAuditLogs on v1: %v", err)
	}
	if _, err := c.Resources.ListAuditLogs(context.Background(), "order", "ord_9", nil); err != nil {
		t.Fatalf("Resources.ListAuditLogs on v1: %v", err)
	}
	for path, called := range paths {
		if !called {
			t.Errorf("%s was never requested", path)
		}
	}
}
