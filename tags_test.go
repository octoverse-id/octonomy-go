package octonomy

import (
	"context"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"testing"
	"time"
)

func TestTags_Create(t *testing.T) {
	created := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/tags" {
			t.Errorf("got %s %s, want POST /api/v1/tags", r.Method, r.URL.Path)
		}
		body, _ := ioutil.ReadAll(r.Body)
		var in map[string]interface{}
		if err := json.Unmarshal(body, &in); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if in["name"] != "Featured" || in["slug"] != "featured" || in["type"] != "label" {
			t.Errorf("unexpected body: %v", in)
		}
		if _, ok := in["parent_id"]; ok {
			t.Errorf("nil parent_id should be omitted, got: %v", in)
		}
		writeData(t, w, http.StatusCreated, Tag{
			ID: "tag_1", Name: "Featured", Slug: "featured", Type: "label",
			IsActive: true, UsageCount: 0, CreatedAt: created, UpdatedAt: created,
		})
	})
	defer cleanup()

	tag, err := c.Tags.Create(context.Background(), TagCreate{Name: "Featured", Slug: "featured", Type: "label"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if tag.ID != "tag_1" || !tag.CreatedAt.Equal(created) {
		t.Errorf("unexpected tag: %+v", tag)
	}
}

func TestTags_CreateConflict(t *testing.T) {
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusConflict, map[string]interface{}{
			"error": map[string]interface{}{"code": CodeConflict, "message": "duplicate slug", "request_id": "req_9"},
		})
	})
	defer cleanup()

	_, err := c.Tags.Create(context.Background(), TagCreate{Name: "Featured", Slug: "featured", Type: "label"})
	if !IsConflict(err) {
		t.Fatalf("expected conflict, got %v", err)
	}
}

func TestTags_List_AllParams(t *testing.T) {
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/tags" {
			t.Errorf("path = %q, want /api/v1/tags", r.URL.Path)
		}
		q := r.URL.Query()
		want := map[string]string{
			"application_id": "commerce",
			"include_shared": "true",
			"is_active":      "false",
			"parent_id":      "tag_parent",
			"q":              "promo",
			"slug":           "sale",
			"type":           "label",
			"vocabulary_id":  "voc_1",
			"limit":          "25",
			"offset":         "50",
		}
		for k, v := range want {
			if got := q.Get(k); got != v {
				t.Errorf("query[%s] = %q, want %q", k, got, v)
			}
		}
		writeJSON(t, w, http.StatusOK, map[string]interface{}{
			"data":       []Tag{{ID: "tag_1"}},
			"pagination": map[string]interface{}{"limit": 25, "offset": 50, "count": 1},
		})
	})
	defer cleanup()

	page, err := c.Tags.List(context.Background(), &TagListParams{
		ListOptions:   ListOptions{Limit: 25, Offset: 50},
		ApplicationID: String("commerce"),
		IncludeShared: Bool(true),
		IsActive:      Bool(false),
		ParentID:      String("tag_parent"),
		Query:         String("promo"),
		Slug:          String("sale"),
		Type:          String("label"),
		VocabularyID:  String("voc_1"),
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Data) != 1 || page.Pagination.Count != 1 {
		t.Errorf("unexpected page: %+v", page)
	}
}

func TestTags_List_NilParams(t *testing.T) {
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			t.Errorf("expected no query params, got %q", r.URL.RawQuery)
		}
		writeJSON(t, w, http.StatusOK, map[string]interface{}{
			"data":       []Tag{},
			"pagination": map[string]interface{}{"limit": 50, "offset": 0, "count": 0},
		})
	})
	defer cleanup()

	page, err := c.Tags.List(context.Background(), nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Data) != 0 {
		t.Errorf("len(Data) = %d, want 0", len(page.Data))
	}
}

func TestTags_GetNotFound(t *testing.T) {
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusNotFound, map[string]interface{}{
			"error": map[string]interface{}{"code": CodeNotFound, "message": "Resource not found."},
		})
	})
	defer cleanup()

	_, err := c.Tags.Get(context.Background(), "missing")
	if !IsNotFound(err) {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestTags_Update(t *testing.T) {
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/api/v1/tags/tag_1" {
			t.Errorf("got %s %s, want PATCH /api/v1/tags/tag_1", r.Method, r.URL.Path)
		}
		body, _ := ioutil.ReadAll(r.Body)
		var in map[string]interface{}
		_ = json.Unmarshal(body, &in)
		if in["is_active"] != false {
			t.Errorf("is_active = %v, want false", in["is_active"])
		}
		if _, ok := in["name"]; ok {
			t.Errorf("nil name should be omitted, got: %v", in)
		}
		writeData(t, w, http.StatusOK, Tag{ID: "tag_1", IsActive: false})
	})
	defer cleanup()

	tag, err := c.Tags.Update(context.Background(), "tag_1", TagUpdate{IsActive: Bool(false)})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if tag.IsActive {
		t.Error("expected IsActive false")
	}
}

func TestTags_Delete(t *testing.T) {
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/v1/tags/tag_1" {
			t.Errorf("got %s %s, want DELETE /api/v1/tags/tag_1", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	defer cleanup()

	if err := c.Tags.Delete(context.Background(), "tag_1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

// Ported from main's tags_test.go at 5e40964 for #95, with two changes. The
// fixture is the wire spelling as raw JSON rather than a Tag run through
// writeData: a marshalled Tag round-trips through its own JSON tags, so a
// misspelled tag would pass (AGENTS.md, Testing Expectations). And every field
// it expects a value in is sent a NON-ZERO one, parent_id included, because a
// misspelled tag decodes to the zero value -- main's fixture sent "parent_id":
// null, which a tag the decoder ignores also turns into nil. The namespace pair
// is asserted absent, as a v1 response has it; the namespaced tests in
// scope_test.go are what hold its spelling. The null case, which is what
// main's assertion was for, is TestTags_Get_NullParentStaysNil below. Every
// other test of Tags.Get here is a failure path.
func TestTags_Get(t *testing.T) {
	created := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	updated := created.Add(48 * time.Hour)
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/tags/tag_1" {
			t.Errorf("got %s %s, want GET /api/v1/tags/tag_1", r.Method, r.URL.Path)
		}
		writeRaw(w, http.StatusOK, `{"data": {
			"id": "tag_1",
			"tenant_id": "tenant-1",
			"application_id": "commerce",
			"name": "Featured",
			"slug": "featured",
			"type": "label",
			"description": "Front page picks",
			"parent_id": "tag_0",
			"vocabulary_id": "voc_1",
			"metadata": {"source": "import"},
			"is_active": true,
			"usage_count": 7,
			"created_at": "2026-06-08T12:00:00Z",
			"updated_at": "2026-06-10T12:00:00Z"
		}}`)
	})
	defer cleanup()

	tag, err := c.Tags.Get(context.Background(), "tag_1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if tag.ID != "tag_1" || tag.TenantID != "tenant-1" || tag.Name != "Featured" || tag.Slug != "featured" || tag.Type != "label" {
		t.Errorf("scalar fields did not round-trip: %+v", tag)
	}
	if tag.ApplicationID == nil || *tag.ApplicationID != "commerce" {
		t.Errorf("ApplicationID = %v, want commerce", tag.ApplicationID)
	}
	if tag.Description == nil || *tag.Description != "Front page picks" {
		t.Errorf("Description = %v, want \"Front page picks\"", tag.Description)
	}
	if tag.VocabularyID == nil || *tag.VocabularyID != "voc_1" {
		t.Errorf("VocabularyID = %v, want voc_1", tag.VocabularyID)
	}
	if tag.ParentID == nil || *tag.ParentID != "tag_0" {
		t.Errorf("ParentID = %v, want tag_0", tag.ParentID)
	}
	// A v1 response carries no namespace pair, so it stays nil rather than "".
	if tag.NamespaceType != nil || tag.NamespaceID != nil {
		t.Errorf("namespace = (%v, %v), want nil on a v1 response", tag.NamespaceType, tag.NamespaceID)
	}
	if tag.Metadata["source"] != "import" {
		t.Errorf("Metadata[source] = %v, want import", tag.Metadata["source"])
	}
	if tag.UsageCount != 7 || !tag.IsActive {
		t.Errorf("UsageCount/IsActive did not round-trip: %+v", tag)
	}
	if !tag.CreatedAt.Equal(created) || !tag.UpdatedAt.Equal(updated) {
		t.Errorf("timestamps did not round-trip: %+v", tag)
	}
}

// A null on the wire stays nil rather than becoming "", which is the distinction
// a nullable field's pointer exists for. It cannot say anything about the tag's
// spelling -- an ignored key is nil too -- which is why TestTags_Get sends a
// value.
func TestTags_Get_NullParentStaysNil(t *testing.T) {
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeRaw(w, http.StatusOK, `{"data": {"id": "tag_1", "parent_id": null, "description": null}}`)
	})
	defer cleanup()

	tag, err := c.Tags.Get(context.Background(), "tag_1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if tag.ParentID != nil || tag.Description != nil {
		t.Errorf("ParentID = %v, Description = %v, want both nil", tag.ParentID, tag.Description)
	}
}
