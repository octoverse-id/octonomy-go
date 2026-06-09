package octonomy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestTags_Create(t *testing.T) {
	created := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/tags" {
			t.Errorf("got %s %s, want POST /api/v1/tags", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var in map[string]any
		if err := json.Unmarshal(body, &in); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if in["name"] != "Featured" || in["slug"] != "featured" || in["type"] != "label" {
			t.Errorf("unexpected body: %v", in)
		}
		if _, ok := in["parent_id"]; ok {
			t.Errorf("nil parent_id should be omitted, got: %v", in)
		}
		writeJSON(t, w, http.StatusCreated, Tag{
			ID: "tag_1", Name: "Featured", Slug: "featured", Type: "label",
			IsActive: true, UsageCount: 0, CreatedAt: created, UpdatedAt: created,
		})
	})

	tag, err := c.Tags.Create(context.Background(), TagCreate{Name: "Featured", Slug: "featured", Type: "label"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if tag.ID != "tag_1" || !tag.CreatedAt.Equal(created) {
		t.Errorf("unexpected tag: %+v", tag)
	}
}

func TestTags_CreateConflict(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusConflict, map[string]any{
			"error": map[string]any{"code": CodeConflict, "message": "duplicate slug", "request_id": "req_9"},
		})
	})

	_, err := c.Tags.Create(context.Background(), TagCreate{Name: "Featured", Slug: "featured", Type: "label"})
	if !IsConflict(err) {
		t.Fatalf("expected conflict, got %v", err)
	}
}

func TestTags_List_AllParams(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
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
		writeJSON(t, w, http.StatusOK, map[string]any{
			"data":       []Tag{{ID: "tag_1"}},
			"pagination": map[string]any{"limit": 25, "offset": 50, "count": 1},
		})
	})

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
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			t.Errorf("expected no query params, got %q", r.URL.RawQuery)
		}
		writeJSON(t, w, http.StatusOK, map[string]any{
			"data":       []Tag{},
			"pagination": map[string]any{"limit": 50, "offset": 0, "count": 0},
		})
	})

	page, err := c.Tags.List(context.Background(), nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Data) != 0 {
		t.Errorf("len(Data) = %d, want 0", len(page.Data))
	}
}

func TestTags_GetNotFound(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusNotFound, map[string]any{
			"error": map[string]any{"code": CodeNotFound, "message": "Resource not found."},
		})
	})

	_, err := c.Tags.Get(context.Background(), "missing")
	if !IsNotFound(err) {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestTags_Update(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/api/v1/tags/tag_1" {
			t.Errorf("got %s %s, want PATCH /api/v1/tags/tag_1", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var in map[string]any
		_ = json.Unmarshal(body, &in)
		if in["is_active"] != false {
			t.Errorf("is_active = %v, want false", in["is_active"])
		}
		if _, ok := in["name"]; ok {
			t.Errorf("nil name should be omitted, got: %v", in)
		}
		writeJSON(t, w, http.StatusOK, Tag{ID: "tag_1", IsActive: false})
	})

	tag, err := c.Tags.Update(context.Background(), "tag_1", TagUpdate{IsActive: Bool(false)})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if tag.IsActive {
		t.Error("expected IsActive false")
	}
}

func TestTags_Delete(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/v1/tags/tag_1" {
			t.Errorf("got %s %s, want DELETE /api/v1/tags/tag_1", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	if err := c.Tags.Delete(context.Background(), "tag_1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}
