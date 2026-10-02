package octonomy

// Ported from main's aliases_test.go at 5e40964 for #94. What changed in the
// port, besides the dialect (defer cleanup() for t.Cleanup, interface{} for any,
// ioutil.ReadAll for io.ReadAll, String(...) for Set(...)):
//
//   - The default client here targets APIV1, so most paths are /api/v1. The
//     full-row Get and the namespace-scoping tests pin APIV2, because the
//     namespace pair is a v2 response field and WithNamespace needs v2.
//   - The full-row Get decodes a RAW JSON fixture written in the wire spellings
//     rather than a TagAlias marshalled through its own tags, so a misspelled
//     tag fails here instead of round-tripping (AGENTS.md).
//   - TestAliases_Update_EmptyMetadataReachesTheServer is new: main fixes #37
//     with Optional[Metadata], this line with TagAliasUpdate.MarshalJSON, which
//     update_test.go covers field by field and this covers on the wire.

import (
	"context"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"testing"
	"time"
)

func TestAliases_Create(t *testing.T) {
	created := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/tag-aliases" {
			t.Errorf("got %s %s, want POST /api/v1/tag-aliases", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}
		body, _ := ioutil.ReadAll(r.Body)
		var in map[string]interface{}
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
	defer cleanup()

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
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusConflict, map[string]interface{}{
			"error": map[string]interface{}{
				"code":       CodeConflict,
				"message":    "An active tag alias with this tenant, application, and slug already exists.",
				"request_id": "req_11",
			},
		})
	})
	defer cleanup()

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
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusBadRequest, map[string]interface{}{
			"error": map[string]interface{}{
				"code":    CodeApplicationMismatch,
				"message": "App-specific tags can only use aliases in the same application.",
			},
		})
	})
	defer cleanup()

	_, err := c.Aliases.Create(context.Background(), TagAliasCreate{
		ApplicationID: String("storefront"), TagID: "tag_1", Name: "On Sale", Slug: "on-sale",
	})
	if !IsApplicationMismatch(err) {
		t.Fatalf("expected application mismatch, got %v", err)
	}
}

// Every field of a row, decoded from the wire spellings. The fixture is raw JSON
// on purpose: marshalling a TagAlias would round-trip through the struct's own
// tags, so a misspelled one would pass.
func TestAliases_Get(t *testing.T) {
	created := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	updated := created.Add(72 * time.Hour)
	c, cleanup := newVersionedTestClient(t, APIV2, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2/tag-aliases/alias_1" {
			t.Errorf("got %s %s, want GET /api/v2/tag-aliases/alias_1", r.Method, r.URL.Path)
		}
		writeRaw(w, http.StatusOK, `{"data":{
			"id": "alias_1",
			"tenant_id": "tenant-1",
			"application_id": "commerce",
			"namespace_type": "merchant",
			"namespace_id": "m-42",
			"tag_id": "tag_1",
			"name": "On Sale",
			"slug": "on-sale",
			"metadata": {"source": "import"},
			"is_active": true,
			"created_at": "2026-06-08T12:00:00Z",
			"updated_at": "2026-06-11T12:00:00Z"
		}}`)
	})
	defer cleanup()

	alias, err := c.Aliases.Get(context.Background(), "alias_1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if alias.ID != "alias_1" || alias.TenantID != "tenant-1" || alias.TagID != "tag_1" || alias.Slug != "on-sale" || alias.Name != "On Sale" {
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
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeData(t, w, http.StatusOK, TagAlias{ID: "alias_1", TagID: "tag_1", Slug: "on-sale"})
	})
	defer cleanup()

	alias, err := c.Aliases.Get(context.Background(), "alias_1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if alias.ApplicationID != nil || alias.NamespaceType != nil || alias.NamespaceID != nil {
		t.Errorf("scope fields should be nil on a global row: %+v", alias)
	}
}

// The #32 guard, asserted at this resource rather than only at the transport: a
// bare resource body -- what the vendored specs describe, and what the server
// does not send -- must be an error and never a zero-valued TagAlias with a nil
// error. It is what proves Get is routed through doData and not a bare unmarshal.
func TestAliases_Get_UnwrappedBodyIsAnError(t *testing.T) {
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, TagAlias{ID: "alias_1", TagID: "tag_1"})
	})
	defer cleanup()

	alias, err := c.Aliases.Get(context.Background(), "alias_1")
	if err == nil {
		t.Fatalf("expected an error for a body with no data envelope, got %+v", alias)
	}
}

func TestAliases_GetNotFound(t *testing.T) {
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusNotFound, map[string]interface{}{
			"error": map[string]interface{}{"code": CodeNotFound, "message": "Tag alias was not found."},
		})
	})
	defer cleanup()

	_, err := c.Aliases.Get(context.Background(), "missing")
	if !IsNotFound(err) {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestAliases_List_AllParams(t *testing.T) {
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/tag-aliases" {
			t.Errorf("got %s %s, want GET /api/v1/tag-aliases", r.Method, r.URL.Path)
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
		writeJSON(t, w, http.StatusOK, map[string]interface{}{
			"data":       []TagAlias{{ID: "alias_1", TagID: "tag_1", Slug: "on-sale"}},
			"pagination": map[string]interface{}{"limit": 25, "offset": 50, "count": 1},
		})
	})
	defer cleanup()

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
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			t.Errorf("expected no query params, got %q", r.URL.RawQuery)
		}
		writeJSON(t, w, http.StatusOK, map[string]interface{}{
			"data":       []TagAlias{},
			"pagination": map[string]interface{}{"limit": 50, "offset": 0, "count": 0},
		})
	})
	defer cleanup()

	page, err := c.Aliases.List(context.Background(), nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if page.Data == nil || len(page.Data) != 0 {
		t.Errorf("want an empty non-nil slice, got %+v", page.Data)
	}
}

func TestAliases_Update(t *testing.T) {
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/api/v1/tag-aliases/alias_1" {
			t.Errorf("got %s %s, want PATCH /api/v1/tag-aliases/alias_1", r.Method, r.URL.Path)
		}
		body, _ := ioutil.ReadAll(r.Body)
		var in map[string]interface{}
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
	defer cleanup()

	alias, err := c.Aliases.Update(context.Background(), "alias_1", TagAliasUpdate{TagID: String("tag_2")})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if alias.ID != "alias_1" || alias.TagID != "tag_2" {
		t.Errorf("unexpected alias: %+v", alias)
	}
}

// Metadata{} must reach the server as {} and empty the stored object (#37). The
// struct-tag encoding drops a zero-length map under omitempty, so without
// TagAliasUpdate.MarshalJSON this PATCH would carry no metadata key at all and
// succeed while changing nothing.
func TestAliases_Update_EmptyMetadataReachesTheServer(t *testing.T) {
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, err := ioutil.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		if string(body) != `{"metadata":{}}` {
			t.Errorf("PATCH body = %s, want {\"metadata\":{}}", body)
		}
		writeData(t, w, http.StatusOK, TagAlias{ID: "alias_1", TagID: "tag_1"})
	})
	defer cleanup()

	if _, err := c.Aliases.Update(context.Background(), "alias_1", TagAliasUpdate{Metadata: Metadata{}}); err != nil {
		t.Fatalf("Update: %v", err)
	}
}

// IsConflict is deliberately false for scope_immutable -- it keys on "conflict",
// and reading a fixed-scope refusal as a duplicate slug would send a caller down
// a retry path that cannot work. The helper is exercised across the PATCH routes
// in TestIsScopeImmutable_OnEveryDocumentedPatch; this keeps the alias-specific
// call site covered from the resource's own suite.
func TestAliases_UpdateScopeImmutable(t *testing.T) {
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusConflict, map[string]interface{}{
			"error": map[string]interface{}{
				"code":    CodeScopeImmutable,
				"message": "Scope is fixed at creation.",
			},
		})
	})
	defer cleanup()

	_, err := c.Aliases.Update(context.Background(), "alias_1", TagAliasUpdate{ApplicationID: String("other")})
	apiErr, ok := AsAPIError(err)
	if !ok {
		t.Fatalf("expected *APIError, got %v", err)
	}
	if apiErr.Code != CodeScopeImmutable || apiErr.StatusCode != http.StatusConflict {
		t.Errorf("unexpected error: %+v", apiErr)
	}
	if !IsScopeImmutable(err) {
		t.Error("IsScopeImmutable did not match a scope_immutable envelope")
	}
	if IsConflict(err) {
		t.Error("scope_immutable must not read as a plain conflict")
	}
}

func TestAliases_Delete(t *testing.T) {
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/v1/tag-aliases/alias_1" {
			t.Errorf("got %s %s, want DELETE /api/v1/tag-aliases/alias_1", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	defer cleanup()

	if err := c.Aliases.Delete(context.Background(), "alias_1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

// An id is escaped rather than interpolated: a slash inside one stays inside its
// segment and cannot address a different route.
//
// Asserted on EscapedPath, which is what goes on the wire and what the server
// routes on -- URL.Path is the DECODED form, where that slash is a real slash
// again and counting separators there proves nothing. The escaping is applied
// exactly once; this line's first release applied it twice (%252F), which is
// the defect resolvePath fixes in transport.go.
func TestAliases_PathKeepsTheIDInOneSegment(t *testing.T) {
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.EscapedPath(); got != "/api/v1/tag-aliases/alias%2F1" {
			t.Errorf("escaped path = %q, want /api/v1/tag-aliases/alias%%2F1", got)
		}
		writeData(t, w, http.StatusOK, TagAlias{ID: "alias/1", TagID: "tag_1"})
	})
	defer cleanup()

	if _, err := c.Aliases.Get(context.Background(), "alias/1"); err != nil {
		t.Fatalf("Get: %v", err)
	}
}

func TestTags_ListAliases(t *testing.T) {
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/tags/tag_1/aliases" {
			t.Errorf("got %s %s, want GET /api/v1/tags/tag_1/aliases", r.Method, r.URL.Path)
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
		writeJSON(t, w, http.StatusOK, map[string]interface{}{
			"data":       []TagAlias{{ID: "alias_1", TagID: "tag_1", Slug: "on-sale"}},
			"pagination": map[string]interface{}{"limit": 10, "offset": 20, "count": 1},
		})
	})
	defer cleanup()

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
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		// Same invariant as TestAliases_PathKeepsTheIDInOneSegment, one level in:
		// the tag id cannot reach past its own segment and rename the /aliases
		// sub-route.
		if got := r.URL.EscapedPath(); got != "/api/v1/tags/tag%2F1/aliases" {
			t.Errorf("escaped path = %q, want /api/v1/tags/tag%%2F1/aliases", got)
		}
		if r.URL.RawQuery != "" {
			t.Errorf("expected no query params, got %q", r.URL.RawQuery)
		}
		writeJSON(t, w, http.StatusOK, map[string]interface{}{
			"data":       []TagAlias{},
			"pagination": map[string]interface{}{"limit": 50, "offset": 0, "count": 0},
		})
	})
	defer cleanup()

	if _, err := c.Tags.ListAliases(context.Background(), "tag/1", nil); err != nil {
		t.Fatalf("ListAliases: %v", err)
	}
}

// A tag the request's scope cannot see is a not_found for the TAG, not an empty
// page: the server looks the tag up before it filters aliases.
func TestTags_ListAliases_UnknownTag(t *testing.T) {
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusNotFound, map[string]interface{}{
			"error": map[string]interface{}{"code": CodeNotFound, "message": "Tag was not found."},
		})
	})
	defer cleanup()

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
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			c, cleanup := newVersionedTestClient(t, APIV2, func(w http.ResponseWriter, r *http.Request) {
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
				writeJSON(t, w, http.StatusOK, map[string]interface{}{
					"data":       []TagAlias{},
					"pagination": map[string]interface{}{"limit": 50, "offset": 0, "count": 0},
				})
			})
			defer cleanup()
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
	c, cleanup := newUnreachableClient(t, APIV2)
	defer cleanup()

	if _, err := c.Aliases.List(context.Background(), nil, WithNamespace("merchant", "m-42")); err == nil {
		t.Fatal("expected a local error for a namespaced list with no application")
	}
	if err := c.Aliases.Delete(context.Background(), "alias_1", WithNamespace("merchant", "m-42")); err == nil {
		t.Fatal("expected a local error for a namespaced delete with no application")
	}
}
