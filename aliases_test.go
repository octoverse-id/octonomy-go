package octonomy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestAliases_Create(t *testing.T) {
	created := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v2/tag-aliases" {
			t.Errorf("got %s %s, want POST /api/v2/tag-aliases", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}
		body, _ := io.ReadAll(r.Body)
		var in map[string]any
		if err := json.Unmarshal(body, &in); err != nil {
			// Errorf, not Fatalf: this runs on the server's goroutine, where
			// FailNow aborts the connection instead of the test.
			t.Errorf("decode body: %v", err)
			return
		}
		if in["tag_id"] != "tag_1" || in["name"] != "On Sale" || in["slug"] != "on-sale" {
			t.Errorf("unexpected body: %v", in)
		}
		if _, ok := in["application_id"]; ok {
			t.Errorf("nil application_id should be omitted, got: %v", in)
		}
		if _, ok := in["is_active"]; ok {
			t.Errorf("nil is_active should be omitted, got: %v", in)
		}
		writeData(t, w, http.StatusCreated, TagAlias{
			ID: "alias_1", TagID: "tag_1", Name: "On Sale", Slug: "on-sale",
			IsActive: true, CreatedAt: created, UpdatedAt: created,
		})
	})

	alias, err := c.Aliases.Create(context.Background(), TagAliasCreate{
		TagID: "tag_1", Name: "On Sale", Slug: "on-sale",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if alias.ID != "alias_1" || alias.TagID != "tag_1" || alias.Slug != "on-sale" {
		t.Errorf("unexpected alias: %+v", alias)
	}
	if !alias.CreatedAt.Equal(created) {
		t.Errorf("CreatedAt = %v, want %v", alias.CreatedAt, created)
	}
}

func TestAliases_CreateConflict(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusConflict, map[string]any{
			"error": map[string]any{
				"code":       CodeConflict,
				"message":    "An active tag alias with this tenant, application, and slug already exists.",
				"request_id": "req_11",
			},
		})
	})

	_, err := c.Aliases.Create(context.Background(), TagAliasCreate{
		TagID: "tag_1", Name: "On Sale", Slug: "on-sale",
	})
	if !IsConflict(err) {
		t.Fatalf("expected conflict, got %v", err)
	}
	apiErr, ok := AsAPIError(err)
	if !ok || apiErr.RequestID != "req_11" {
		t.Errorf("request id did not survive: %+v", apiErr)
	}
}

// An alias whose target tag lives in another application is application_mismatch,
// not a generic validation error: the server refuses it so an alias cannot be a
// way around the tag's own application boundary.
func TestAliases_CreateApplicationMismatch(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusBadRequest, map[string]any{
			"error": map[string]any{
				"code":    CodeApplicationMismatch,
				"message": "App-specific tags can only use aliases in the same application.",
			},
		})
	})

	_, err := c.Aliases.Create(context.Background(), TagAliasCreate{
		ApplicationID: String("storefront"), TagID: "tag_1", Name: "On Sale", Slug: "on-sale",
	})
	if !IsApplicationMismatch(err) {
		t.Fatalf("expected application mismatch, got %v", err)
	}
}

func TestAliases_Get(t *testing.T) {
	created := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	updated := created.Add(72 * time.Hour)
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2/tag-aliases/alias_1" {
			t.Errorf("got %s %s, want GET /api/v2/tag-aliases/alias_1", r.Method, r.URL.Path)
		}
		writeData(t, w, http.StatusOK, TagAlias{
			ID:            "alias_1",
			TenantID:      "tenant-1",
			ApplicationID: String("commerce"),
			NamespaceType: String("merchant"),
			NamespaceID:   String("m-42"),
			TagID:         "tag_1",
			Name:          "On Sale",
			Slug:          "on-sale",
			Metadata:      Metadata{"source": "import"},
			IsActive:      true,
			CreatedAt:     created,
			UpdatedAt:     updated,
		})
	})

	alias, err := c.Aliases.Get(context.Background(), "alias_1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if alias.ID != "alias_1" || alias.TenantID != "tenant-1" || alias.TagID != "tag_1" || alias.Slug != "on-sale" {
		t.Errorf("scalar fields did not round-trip: %+v", alias)
	}
	if alias.ApplicationID == nil || *alias.ApplicationID != "commerce" {
		t.Errorf("ApplicationID = %v, want commerce", alias.ApplicationID)
	}
	if alias.NamespaceType == nil || *alias.NamespaceType != "merchant" {
		t.Errorf("NamespaceType = %v, want merchant", alias.NamespaceType)
	}
	if alias.NamespaceID == nil || *alias.NamespaceID != "m-42" {
		t.Errorf("NamespaceID = %v, want m-42", alias.NamespaceID)
	}
	if alias.Metadata["source"] != "import" {
		t.Errorf("Metadata[source] = %v, want import", alias.Metadata["source"])
	}
	if !alias.IsActive {
		t.Error("expected IsActive true")
	}
	if !alias.CreatedAt.Equal(created) || !alias.UpdatedAt.Equal(updated) {
		t.Errorf("timestamps did not round-trip: %+v", alias)
	}
}

// A global (tenant-shared) alias carries null for all three scope fields, and
// they must stay nil rather than decoding to "".
func TestAliases_Get_GlobalRowKeepsNilScope(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeData(t, w, http.StatusOK, TagAlias{ID: "alias_1", TagID: "tag_1", Slug: "on-sale"})
	})

	alias, err := c.Aliases.Get(context.Background(), "alias_1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if alias.ApplicationID != nil || alias.NamespaceType != nil || alias.NamespaceID != nil {
		t.Errorf("scope fields should be nil on a global row: %+v", alias)
	}
}

// The #32 guard, asserted at this resource rather than only at the transport: a
// bare resource body -- what docs/openapi-v2.yaml describes, and what the server
// does not send -- must be an error and never a zero-valued TagAlias with a nil
// error. It is what proves Get is routed through doData and not a bare unmarshal.
func TestAliases_Get_UnwrappedBodyIsAnError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, TagAlias{ID: "alias_1", TagID: "tag_1"})
	})

	alias, err := c.Aliases.Get(context.Background(), "alias_1")
	if err == nil {
		t.Fatalf("expected an error for a body with no data envelope, got %+v", alias)
	}
}

func TestAliases_GetNotFound(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusNotFound, map[string]any{
			"error": map[string]any{"code": CodeNotFound, "message": "Tag alias was not found."},
		})
	})

	_, err := c.Aliases.Get(context.Background(), "missing")
	if !IsNotFound(err) {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestAliases_List_AllParams(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2/tag-aliases" {
			t.Errorf("got %s %s, want GET /api/v2/tag-aliases", r.Method, r.URL.Path)
		}
		q := r.URL.Query()
		want := map[string]string{
			"application_id": "commerce",
			"include_shared": "true",
			"is_active":      "false",
			"q":              "sale",
			"slug":           "on-sale",
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
			"data":       []TagAlias{{ID: "alias_1", TagID: "tag_1", Slug: "on-sale"}},
			"pagination": map[string]any{"limit": 25, "offset": 50, "count": 1},
		})
	})

	page, err := c.Aliases.List(context.Background(), &TagAliasListParams{
		ListOptions:   ListOptions{Limit: 25, Offset: 50},
		ApplicationID: String("commerce"),
		IncludeShared: Bool(true),
		IsActive:      Bool(false),
		Query:         String("sale"),
		Slug:          String("on-sale"),
		TagID:         String("tag_1"),
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Data) != 1 || page.Data[0].ID != "alias_1" || page.Pagination.Count != 1 {
		t.Errorf("unexpected page: %+v", page)
	}
}

func TestAliases_List_NilParams(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			t.Errorf("expected no query params, got %q", r.URL.RawQuery)
		}
		writeJSON(t, w, http.StatusOK, map[string]any{
			"data":       []TagAlias{},
			"pagination": map[string]any{"limit": 50, "offset": 0, "count": 0},
		})
	})

	page, err := c.Aliases.List(context.Background(), nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if page.Data == nil || len(page.Data) != 0 {
		t.Errorf("want an empty non-nil slice, got %+v", page.Data)
	}
}

func TestAliases_Update(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/api/v2/tag-aliases/alias_1" {
			t.Errorf("got %s %s, want PATCH /api/v2/tag-aliases/alias_1", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var in map[string]any
		if err := json.Unmarshal(body, &in); err != nil {
			// Errorf, not Fatalf: this runs on the server's goroutine, where
			// FailNow aborts the connection instead of the test.
			t.Errorf("decode body: %v", err)
			return
		}
		// Re-pointing an alias at another tag is a normal edit, not the scope
		// change PATCH refuses, so tag_id has to reach the wire.
		if in["tag_id"] != "tag_2" {
			t.Errorf("tag_id = %v, want tag_2", in["tag_id"])
		}
		for _, field := range []string{"name", "slug", "application_id", "is_active", "metadata"} {
			if _, ok := in[field]; ok {
				t.Errorf("nil %s should be omitted, got: %v", field, in)
			}
		}
		writeData(t, w, http.StatusOK, TagAlias{ID: "alias_1", TagID: "tag_2", Slug: "on-sale"})
	})

	alias, err := c.Aliases.Update(context.Background(), "alias_1", TagAliasUpdate{TagID: String("tag_2")})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if alias.ID != "alias_1" || alias.TagID != "tag_2" {
		t.Errorf("unexpected alias: %+v", alias)
	}
}

// scope_immutable has no constant in this package yet, which is exactly what
// makes it worth a test: parseError preserves whatever code the envelope carries,
// so a caller can branch on APIError.Code today. IsConflict is deliberately false
// -- it keys on "conflict", and reading a scope change as a duplicate slug would
// send a caller down a retry path that cannot work.
func TestAliases_UpdateScopeImmutable(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusConflict, map[string]any{
			"error": map[string]any{
				"code":    "scope_immutable",
				"message": "Scope is fixed at creation.",
			},
		})
	})

	_, err := c.Aliases.Update(context.Background(), "alias_1", TagAliasUpdate{ApplicationID: String("other")})
	apiErr, ok := AsAPIError(err)
	if !ok {
		t.Fatalf("expected *APIError, got %v", err)
	}
	if apiErr.Code != "scope_immutable" || apiErr.StatusCode != http.StatusConflict {
		t.Errorf("unexpected error: %+v", apiErr)
	}
	if IsConflict(err) {
		t.Error("scope_immutable must not read as a plain conflict")
	}
}

func TestAliases_Delete(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/v2/tag-aliases/alias_1" {
			t.Errorf("got %s %s, want DELETE /api/v2/tag-aliases/alias_1", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	if err := c.Aliases.Delete(context.Background(), "alias_1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

// An id is escaped rather than interpolated: a slash inside one must stay inside
// its segment and must not address a different route. Asserted as a segment
// count, not as an exact byte sequence, because the escaping is currently
// applied twice -- url.PathEscape here, and again when net/url renders the
// url.URL.Path this is assigned into, so a slash reaches the wire as %252F. That
// double encoding predates this resource (Tags.Get and Vocabularies.Get do the
// same) and is not what this test is about; the segment boundary is, and it holds
// either way. Alias ids are uuids on every route here, so no real id reaches it.
func TestAliases_PathKeepsTheIDInOneSegment(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v2/tag-aliases/") {
			t.Errorf("path = %q, want the /api/v2/tag-aliases/{id} route", r.URL.Path)
		}
		if got := strings.Count(r.URL.Path, "/"); got != 4 {
			t.Errorf("path = %q has %d separators, want 4: the id escaped its segment", r.URL.Path, got)
		}
		writeData(t, w, http.StatusOK, TagAlias{ID: "alias/1", TagID: "tag_1"})
	})

	if _, err := c.Aliases.Get(context.Background(), "alias/1"); err != nil {
		t.Fatalf("Get: %v", err)
	}
}

func TestTags_ListAliases(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2/tags/tag_1/aliases" {
			t.Errorf("got %s %s, want GET /api/v2/tags/tag_1/aliases", r.Method, r.URL.Path)
		}
		q := r.URL.Query()
		want := map[string]string{
			"application_id": "commerce",
			"include_shared": "false",
			"is_active":      "true",
			"limit":          "10",
			"offset":         "20",
		}
		for k, v := range want {
			if got := q.Get(k); got != v {
				t.Errorf("query[%s] = %q, want %q", k, got, v)
			}
		}
		writeJSON(t, w, http.StatusOK, map[string]any{
			"data":       []TagAlias{{ID: "alias_1", TagID: "tag_1", Slug: "on-sale"}},
			"pagination": map[string]any{"limit": 10, "offset": 20, "count": 1},
		})
	})

	page, err := c.Tags.ListAliases(context.Background(), "tag_1", &TagListAliasesParams{
		ListOptions:   ListOptions{Limit: 10, Offset: 20},
		ApplicationID: String("commerce"),
		IncludeShared: Bool(false),
		IsActive:      Bool(true),
	})
	if err != nil {
		t.Fatalf("ListAliases: %v", err)
	}
	if len(page.Data) != 1 || page.Data[0].TagID != "tag_1" || page.Pagination.Limit != 10 {
		t.Errorf("unexpected page: %+v", page)
	}
}

func TestTags_ListAliases_NilParamsAndEscaping(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		// Same invariant as TestAliases_PathKeepsTheIDInOneSegment, one level in:
		// the tag id must not be able to reach past its own segment and rename the
		// /aliases sub-route.
		if !strings.HasPrefix(r.URL.Path, "/api/v2/tags/") || !strings.HasSuffix(r.URL.Path, "/aliases") {
			t.Errorf("path = %q, want the /api/v2/tags/{id}/aliases route", r.URL.Path)
		}
		if got := strings.Count(r.URL.Path, "/"); got != 5 {
			t.Errorf("path = %q has %d separators, want 5: the tag id escaped its segment", r.URL.Path, got)
		}
		if r.URL.RawQuery != "" {
			t.Errorf("expected no query params, got %q", r.URL.RawQuery)
		}
		writeJSON(t, w, http.StatusOK, map[string]any{
			"data":       []TagAlias{},
			"pagination": map[string]any{"limit": 50, "offset": 0, "count": 0},
		})
	})

	if _, err := c.Tags.ListAliases(context.Background(), "tag/1", nil); err != nil {
		t.Fatalf("ListAliases: %v", err)
	}
}

// A tag the request's scope cannot see is a not_found for the TAG, not an empty
// page: the server looks the tag up before it filters aliases.
func TestTags_ListAliases_UnknownTag(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusNotFound, map[string]any{
			"error": map[string]any{"code": CodeNotFound, "message": "Tag was not found."},
		})
	})

	_, err := c.Tags.ListAliases(context.Background(), "missing", nil)
	if !IsNotFound(err) {
		t.Fatalf("expected not found, got %v", err)
	}
}

// Scoping is the transport's job and is covered exhaustively in scope_test.go.
// What this asserts is that a new resource inherits it by doing nothing: the
// namespace pair and the application it requires reach the wire on both alias
// routes, including the nested one.
func TestAliases_NamespaceScopingReachesTheWire(t *testing.T) {
	tests := []struct {
		name string
		call func(*Client) error
		path string
	}{
		{
			name: "collection list",
			path: "/api/v2/tag-aliases",
			call: func(c *Client) error {
				_, err := c.Aliases.List(context.Background(), nil,
					WithNamespace("merchant", "m-42"), WithApplication("commerce"), WithIncludeGlobal())
				return err
			},
		},
		{
			name: "aliases of a tag",
			path: "/api/v2/tags/tag_1/aliases",
			call: func(c *Client) error {
				_, err := c.Tags.ListAliases(context.Background(), "tag_1", nil,
					WithNamespace("merchant", "m-42"), WithApplication("commerce"), WithIncludeGlobal())
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tt.path {
					t.Errorf("path = %q, want %q", r.URL.Path, tt.path)
				}
				if got := r.Header.Get(namespaceTypeHeader); got != "merchant" {
					t.Errorf("%s = %q, want merchant", namespaceTypeHeader, got)
				}
				if got := r.Header.Get(namespaceIDHeader); got != "m-42" {
					t.Errorf("%s = %q, want m-42", namespaceIDHeader, got)
				}
				q := r.URL.Query()
				if q.Get("application_id") != "commerce" {
					t.Errorf("application_id = %q, want commerce", q.Get("application_id"))
				}
				if q.Get("include_global") != "true" {
					t.Errorf("include_global = %q, want true", q.Get("include_global"))
				}
				writeJSON(t, w, http.StatusOK, map[string]any{
					"data":       []TagAlias{},
					"pagination": map[string]any{"limit": 50, "offset": 0, "count": 0},
				})
			})
			if err := tt.call(c); err != nil {
				t.Fatalf("%s: %v", tt.name, err)
			}
		})
	}
}

// A namespaced bodyless request with no application is refused locally, and that
// holds for a resource that added no guard of its own -- the point of putting
// scope at the chokepoint.
func TestAliases_NamespacedReadNeedsAnApplication(t *testing.T) {
	c := newUnreachableClient(t, APIV2)

	if _, err := c.Aliases.List(context.Background(), nil, WithNamespace("merchant", "m-42")); err == nil {
		t.Fatal("expected a local error for a namespaced list with no application")
	}
	if err := c.Aliases.Delete(context.Background(), "alias_1", WithNamespace("merchant", "m-42")); err == nil {
		t.Fatal("expected a local error for a namespaced delete with no application")
	}
}
