package octonomy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestVocabularies_Create(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/api/v2/vocabularies" {
			t.Errorf("path = %q, want /api/v2/vocabularies", r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		body, _ := io.ReadAll(r.Body)
		var in map[string]any
		if err := json.Unmarshal(body, &in); err != nil {
			// Errorf, not Fatalf: this runs on the server's goroutine, where
			// FailNow aborts the connection instead of the test.
			t.Errorf("decode body: %v", err)
			return
		}
		if in["name"] != "Labels" || in["slug"] != "labels" {
			t.Errorf("unexpected body: %v", in)
		}
		writeData(t, w, http.StatusCreated, Vocabulary{ID: "voc_1", Name: "Labels", Slug: "labels", IsActive: true})
	})

	voc, err := c.Vocabularies.Create(context.Background(), VocabularyCreate{Name: "Labels", Slug: "labels"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if voc.ID != "voc_1" || voc.Name != "Labels" {
		t.Errorf("unexpected vocabulary: %+v", voc)
	}
}

func TestVocabularies_List(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/vocabularies" {
			t.Errorf("path = %q, want /api/v2/vocabularies", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("limit") != "10" || q.Get("offset") != "20" {
			t.Errorf("paging params = %v, want limit=10 offset=20", q)
		}
		if q.Get("include_shared") != "true" {
			t.Errorf("include_shared = %q, want true", q.Get("include_shared"))
		}
		writeJSON(t, w, http.StatusOK, map[string]any{
			"data": []Vocabulary{{ID: "voc_1"}, {ID: "voc_2"}},
			"pagination": map[string]any{
				"limit": 10, "offset": 20, "count": 2, "next": nil, "previous": nil,
			},
		})
	})

	page, err := c.Vocabularies.List(context.Background(), &VocabularyListParams{
		ListOptions:   ListOptions{Limit: 10, Offset: 20},
		IncludeShared: Bool(true),
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Data) != 2 {
		t.Fatalf("len(Data) = %d, want 2", len(page.Data))
	}
	if page.Pagination.Count != 2 || page.Pagination.Limit != 10 || page.Pagination.Offset != 20 {
		t.Errorf("unexpected pagination: %+v", page.Pagination)
	}
}

// The other previously untested doData route -- see TestTags_Get.
func TestVocabularies_Get(t *testing.T) {
	created := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2/vocabularies/voc_1" {
			t.Errorf("got %s %s, want GET /api/v2/vocabularies/voc_1", r.Method, r.URL.Path)
		}
		writeData(t, w, http.StatusOK, Vocabulary{
			ID:          "voc_1",
			TenantID:    "tenant-1",
			Name:        "Labels",
			Slug:        "labels",
			Description: String("Shared label vocabulary"),
			Metadata:    Metadata{"owner": "platform"},
			IsActive:    true,
			CreatedAt:   created,
			UpdatedAt:   created,
		})
	})

	voc, err := c.Vocabularies.Get(context.Background(), "voc_1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if voc.ID != "voc_1" || voc.TenantID != "tenant-1" || voc.Name != "Labels" || voc.Slug != "labels" {
		t.Errorf("scalar fields did not round-trip: %+v", voc)
	}
	// A nil ApplicationID means "shared across the tenant", so the distinction
	// between nil and "" is load-bearing here.
	if voc.ApplicationID != nil {
		t.Errorf("ApplicationID = %v, want nil (shared)", voc.ApplicationID)
	}
	if voc.Description == nil || *voc.Description != "Shared label vocabulary" {
		t.Errorf("Description = %v, want the shared-vocabulary text", voc.Description)
	}
	if voc.Metadata["owner"] != "platform" {
		t.Errorf("Metadata[owner] = %v, want platform", voc.Metadata["owner"])
	}
	if !voc.IsActive || !voc.CreatedAt.Equal(created) {
		t.Errorf("IsActive/CreatedAt did not round-trip: %+v", voc)
	}
}

func TestVocabularies_Update(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("method = %q, want PATCH", r.Method)
		}
		if r.URL.Path != "/api/v2/vocabularies/voc_1" {
			t.Errorf("path = %q, want /api/v2/vocabularies/voc_1", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var in map[string]any
		_ = json.Unmarshal(body, &in)
		if _, ok := in["slug"]; ok {
			t.Errorf("nil fields should be omitted, got slug in body: %v", in)
		}
		if in["name"] != "Renamed" {
			t.Errorf("name = %v, want Renamed", in["name"])
		}
		writeData(t, w, http.StatusOK, Vocabulary{ID: "voc_1", Name: "Renamed"})
	})

	voc, err := c.Vocabularies.Update(context.Background(), "voc_1", VocabularyUpdate{Name: String("Renamed")})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if voc.Name != "Renamed" {
		t.Errorf("name = %q, want Renamed", voc.Name)
	}
}

func TestVocabularies_Delete(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %q, want DELETE", r.Method)
		}
		if r.URL.Path != "/api/v2/vocabularies/voc_1" {
			t.Errorf("path = %q, want /api/v2/vocabularies/voc_1", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	if err := c.Vocabularies.Delete(context.Background(), "voc_1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}
