package octonomy

import (
	"context"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"testing"
	"time"
)

func TestVocabularies_Create(t *testing.T) {
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/api/v1/vocabularies" {
			t.Errorf("path = %q, want /api/v1/vocabularies", r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		body, _ := ioutil.ReadAll(r.Body)
		var in map[string]interface{}
		if err := json.Unmarshal(body, &in); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if in["name"] != "Labels" || in["slug"] != "labels" {
			t.Errorf("unexpected body: %v", in)
		}
		writeData(t, w, http.StatusCreated, Vocabulary{ID: "voc_1", Name: "Labels", Slug: "labels", IsActive: true})
	})
	defer cleanup()

	voc, err := c.Vocabularies.Create(context.Background(), VocabularyCreate{Name: "Labels", Slug: "labels"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if voc.ID != "voc_1" || voc.Name != "Labels" {
		t.Errorf("unexpected vocabulary: %+v", voc)
	}
}

func TestVocabularies_List(t *testing.T) {
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/vocabularies" {
			t.Errorf("path = %q, want /api/v1/vocabularies", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("limit") != "10" || q.Get("offset") != "20" {
			t.Errorf("paging params = %v, want limit=10 offset=20", q)
		}
		if q.Get("include_shared") != "true" {
			t.Errorf("include_shared = %q, want true", q.Get("include_shared"))
		}
		writeJSON(t, w, http.StatusOK, map[string]interface{}{
			"data": []Vocabulary{{ID: "voc_1"}, {ID: "voc_2"}},
			"pagination": map[string]interface{}{
				"limit": 10, "offset": 20, "count": 2, "next": nil, "previous": nil,
			},
		})
	})
	defer cleanup()

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

func TestVocabularies_Update(t *testing.T) {
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("method = %q, want PATCH", r.Method)
		}
		if r.URL.Path != "/api/v1/vocabularies/voc_1" {
			t.Errorf("path = %q, want /api/v1/vocabularies/voc_1", r.URL.Path)
		}
		body, _ := ioutil.ReadAll(r.Body)
		var in map[string]interface{}
		_ = json.Unmarshal(body, &in)
		if _, ok := in["slug"]; ok {
			t.Errorf("nil fields should be omitted, got slug in body: %v", in)
		}
		if in["name"] != "Renamed" {
			t.Errorf("name = %v, want Renamed", in["name"])
		}
		writeData(t, w, http.StatusOK, Vocabulary{ID: "voc_1", Name: "Renamed"})
	})
	defer cleanup()

	voc, err := c.Vocabularies.Update(context.Background(), "voc_1", VocabularyUpdate{Name: String("Renamed")})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if voc.Name != "Renamed" {
		t.Errorf("name = %q, want Renamed", voc.Name)
	}
}

func TestVocabularies_Delete(t *testing.T) {
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %q, want DELETE", r.Method)
		}
		if r.URL.Path != "/api/v1/vocabularies/voc_1" {
			t.Errorf("path = %q, want /api/v1/vocabularies/voc_1", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	defer cleanup()

	if err := c.Vocabularies.Delete(context.Background(), "voc_1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

// Ported from main's vocabularies_test.go at 5e40964 for #95, without two of its
// cases: main's VocabularyListParams has Query and Slug (the q and slug filters,
// main's #61), and this line's does not yet, although docs/openapi-v2.yaml and
// docs/openapi.yaml both list them. docs/compat-test-disposition.md records the
// gap. Every filter this line does have is asserted, and so is the absence of
// any other parameter.
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
			},
			want: map[string]string{
				"limit":          "25",
				"offset":         "50",
				"application_id": "commerce",
				"include_shared": "true",
				"is_active":      "false",
			},
		},
		{
			// nil and &"" are different requests, and which one the caller meant
			// is not this package's call to make: nil omits the parameter, &""
			// sends it empty. Dropping the key because the value looks empty
			// would be the SDK re-implementing server validation, which AGENTS.md
			// rules out.
			name:   "an empty string is still sent",
			params: &VocabularyListParams{ApplicationID: String("")},
			want:   map[string]string{"application_id": ""},
		},
		{
			name:   "nil params",
			params: nil,
			want:   map[string]string{},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			c, cleanup := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/v1/vocabularies" {
					t.Errorf("got %s %s, want GET /api/v1/vocabularies", r.Method, r.URL.Path)
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
				writeRaw(w, http.StatusOK, `{"data": [{"id": "voc_1"}], "pagination": {"limit": 25, "offset": 50, "count": 1}}`)
			})
			defer cleanup()

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

// Ported from main's vocabularies_test.go at 5e40964 for #95, with the fixture
// as raw wire JSON for the reason TestTags_Get gives. It was main's other
// previously untested doData route, and it is this line's too.
func TestVocabularies_Get(t *testing.T) {
	created := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/vocabularies/voc_1" {
			t.Errorf("got %s %s, want GET /api/v1/vocabularies/voc_1", r.Method, r.URL.Path)
		}
		writeRaw(w, http.StatusOK, `{"data": {
			"id": "voc_1",
			"tenant_id": "tenant-1",
			"application_id": null,
			"name": "Labels",
			"slug": "labels",
			"description": "Shared label vocabulary",
			"metadata": {"owner": "platform"},
			"is_active": true,
			"created_at": "2026-06-08T12:00:00Z",
			"updated_at": "2026-06-08T12:00:00Z"
		}}`)
	})
	defer cleanup()

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
	if !voc.IsActive || !voc.CreatedAt.Equal(created) || !voc.UpdatedAt.Equal(created) {
		t.Errorf("IsActive/timestamps did not round-trip: %+v", voc)
	}
}
