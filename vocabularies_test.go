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

// Every filter VocabularyListParams offers, on the wire, one case at a time.
//
// Table-driven rather than a single set-everything call, for two reasons. A
// full-house case cannot tell a parameter emitted under its OWN name from one
// emitted under a neighbour's -- swap the q and slug lines in query() and a
// combined assertion still passes -- so `q` and `slug` each get a row where they
// are the only filter set. And each case asserts the query string EXACTLY: a
// parameter nobody asked for is as wrong as a missing one, and the nil-params row
// is what proves an unset filter stays off the wire rather than going out empty.
//
// TestVocabularies_List above covers the decode; this covers the request (#36).
func TestVocabularies_List_Params(t *testing.T) {
	tests := []struct {
		name   string
		params *VocabularyListParams
		want   map[string]string
	}{
		{
			name: "every filter",
			params: &VocabularyListParams{
				ListOptions:   ListOptions{Limit: 25, Offset: 50},
				ApplicationID: String("commerce"),
				IncludeShared: Bool(true),
				IsActive:      Bool(false),
				Query:         String("promo"),
				Slug:          String("labels"),
			},
			want: map[string]string{
				"limit":          "25",
				"offset":         "50",
				"application_id": "commerce",
				"include_shared": "true",
				"is_active":      "false",
				"q":              "promo",
				"slug":           "labels",
			},
		},
		{
			name:   "q alone",
			params: &VocabularyListParams{Query: String("promo")},
			want:   map[string]string{"q": "promo"},
		},
		{
			name:   "slug alone",
			params: &VocabularyListParams{Slug: String("labels")},
			want:   map[string]string{"slug": "labels"},
		},
		{
			// nil and &"" are different requests, and which one the caller meant
			// is not this package's call to make: nil omits the parameter, &""
			// sends it empty. The server happens to read a blank value as no
			// filter on both (`if value:` / `if q:` in vocabulary_selectors.py),
			// but applying that rule HERE -- dropping the key because the value
			// looks empty -- would be the SDK re-implementing server validation,
			// which AGENTS.md rules out.
			name:   "empty strings are still sent",
			params: &VocabularyListParams{Query: String(""), Slug: String("")},
			want:   map[string]string{"q": "", "slug": ""},
		},
		{
			name:   "nil params",
			params: nil,
			want:   map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/v2/vocabularies" {
					t.Errorf("got %s %s, want GET /api/v2/vocabularies", r.Method, r.URL.Path)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
					t.Errorf("Authorization = %q, want Bearer test-token", got)
				}
				if got := r.Header.Get("X-Tenant-ID"); got != "tenant-1" {
					t.Errorf("X-Tenant-ID = %q, want tenant-1", got)
				}
				q := r.URL.Query()
				for k, v := range tt.want {
					got, ok := q[k]
					if !ok {
						t.Errorf("query is missing %s (want %q); raw query = %q", k, v, r.URL.RawQuery)
						continue
					}
					if len(got) != 1 || got[0] != v {
						t.Errorf("query[%s] = %v, want [%q]", k, got, v)
					}
				}
				for k := range q {
					if _, ok := tt.want[k]; !ok {
						t.Errorf("unexpected query param %s=%q", k, q.Get(k))
					}
				}
				writeJSON(t, w, http.StatusOK, map[string]any{
					"data":       []Vocabulary{{ID: "voc_1"}},
					"pagination": map[string]any{"limit": 25, "offset": 50, "count": 1},
				})
			})

			page, err := c.Vocabularies.List(context.Background(), tt.params)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(page.Data) != 1 || page.Data[0].ID != "voc_1" {
				t.Errorf("unexpected page: %+v", page.Data)
			}
		})
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

	voc, err := c.Vocabularies.Update(context.Background(), "voc_1", VocabularyUpdate{Name: Set("Renamed")})
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
