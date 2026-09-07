package octonomy

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestResources_ListTags(t *testing.T) {
	assigned := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2/resources/order/ord_9/tags" {
			t.Errorf("got %s %s, want GET /api/v2/resources/order/ord_9/tags", r.Method, r.URL.Path)
		}
		q := r.URL.Query()
		want := map[string]string{
			"application_id":   "commerce",
			"include_inactive": "true",
			"type":             "label",
			"limit":            "25",
			"offset":           "50",
		}
		for k, v := range want {
			if got := q.Get(k); got != v {
				t.Errorf("query[%s] = %q, want %q", k, got, v)
			}
		}
		writeJSON(t, w, http.StatusOK, map[string]any{
			"data": []ResourceTag{{
				AssignmentID: "asg_1",
				AssignedBy:   String("importer"),
				AssignedAt:   assigned,
				Tag:          Tag{ID: "tag_1", Name: "On Sale", Slug: "on-sale", Type: "label", UsageCount: 3},
			}},
			"pagination": map[string]any{"limit": 25, "offset": 50, "count": 1},
		})
	})

	page, err := c.Resources.ListTags(context.Background(), "order", "ord_9", &ResourceListTagsParams{
		ListOptions:     ListOptions{Limit: 25, Offset: 50},
		ApplicationID:   String("commerce"),
		IncludeInactive: Bool(true),
		Type:            String("label"),
	})
	if err != nil {
		t.Fatalf("ListTags: %v", err)
	}
	if len(page.Data) != 1 {
		t.Fatalf("len(Data) = %d, want 1", len(page.Data))
	}
	got := page.Data[0]
	if got.AssignmentID != "asg_1" || !got.AssignedAt.Equal(assigned) {
		t.Errorf("assignment fields did not round-trip: %+v", got)
	}
	// The nested tag is the point of this shape: it must decode whole, not as an
	// id the caller then has to fetch.
	if got.Tag.ID != "tag_1" || got.Tag.Slug != "on-sale" || got.Tag.UsageCount != 3 {
		t.Errorf("nested tag did not round-trip: %+v", got.Tag)
	}
	if got.AssignedBy == nil || *got.AssignedBy != "importer" {
		t.Errorf("AssignedBy = %v, want importer", got.AssignedBy)
	}
}

// include_inactive is absent unless set, and it is NOT is_active: nil means the
// server's own default of active-only, not "every row".
func TestResources_ListTags_OmitsUnsetFilters(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "application_id=commerce" {
			t.Errorf("query = %q, want exactly application_id=commerce", r.URL.RawQuery)
		}
		if r.URL.Query().Has("is_active") {
			t.Error("is_active is a different route's parameter and must never be sent here")
		}
		writeJSON(t, w, http.StatusOK, map[string]any{
			"data":       []ResourceTag{},
			"pagination": map[string]any{"limit": 50, "offset": 0, "count": 0},
		})
	})

	page, err := c.Resources.ListTags(context.Background(), "order", "ord_9",
		&ResourceListTagsParams{ApplicationID: String("commerce")})
	if err != nil {
		t.Fatalf("ListTags: %v", err)
	}
	if page.Data == nil || len(page.Data) != 0 {
		t.Errorf("want an empty non-nil slice, got %+v", page.Data)
	}
}

// The application can equally come from WithApplication, since this is a
// bodyless read -- and setting both to different values is a contradiction
// rather than a precedence question.
func TestResources_ListTags_ApplicationSources(t *testing.T) {
	t.Run("from the option", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if got := r.URL.Query().Get("application_id"); got != "commerce" {
				t.Errorf("application_id = %q, want commerce", got)
			}
			writeJSON(t, w, http.StatusOK, map[string]any{
				"data":       []ResourceTag{},
				"pagination": map[string]any{"limit": 50, "offset": 0, "count": 0},
			})
		})
		if _, err := c.Resources.ListTags(context.Background(), "order", "ord_9", nil,
			WithApplication("commerce")); err != nil {
			t.Fatalf("ListTags: %v", err)
		}
	})

	t.Run("contradicting the params is an error", func(t *testing.T) {
		c := newUnreachableClient(t, APIV2)
		_, err := c.Resources.ListTags(context.Background(), "order", "ord_9",
			&ResourceListTagsParams{ApplicationID: String("commerce")}, WithApplication("storefront"))
		if err == nil {
			t.Fatal("expected a contradiction error")
		}
		if !strings.Contains(err.Error(), "contradicts") {
			t.Errorf("error should say the two contradict, got: %v", err)
		}
	})
}

// The server requires application_id here, which the SDK does not duplicate --
// it sends what you set and lets the server say so.
func TestResources_ListTags_MissingApplicationIsTheServersError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			t.Errorf("nil params should send no query, got %q", r.URL.RawQuery)
		}
		writeJSON(t, w, http.StatusBadRequest, map[string]any{
			"error": map[string]any{
				"code":    CodeValidation,
				"message": "Request validation failed.",
				"details": map[string]any{"application_id": []string{"This query parameter is required."}},
			},
		})
	})

	_, err := c.Resources.ListTags(context.Background(), "order", "ord_9", nil)
	if !IsValidation(err) {
		t.Fatalf("expected a validation error, got %v", err)
	}
	apiErr, _ := AsAPIError(err)
	if _, ok := apiErr.Details["application_id"]; !ok {
		t.Errorf("Details should name application_id, got %v", apiErr.Details)
	}
}

func TestResources_ReplaceTags(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v2/resources/order/ord_9/tags" {
			t.Errorf("got %s %s, want POST /api/v2/resources/order/ord_9/tags", r.Method, r.URL.Path)
		}
		in := decodeBody(t, r)
		if in["application_id"] != "commerce" {
			t.Errorf("application_id = %v, want commerce", in["application_id"])
		}
		if tagIDs, _ := in["tag_ids"].([]any); len(tagIDs) != 2 {
			t.Errorf("tag_ids = %v, want two ids", in["tag_ids"])
		}
		// The path names the resource and the server overwrites any body value,
		// so sending them would be noise that reads as meaningful.
		for _, field := range []string{"resource_type", "resource_id"} {
			if _, ok := in[field]; ok {
				t.Errorf("%s comes from the path and must not be in the body, got: %v", field, in)
			}
		}
		writeData(t, w, http.StatusOK, ResourceReplaceResult{
			Created: 2, Removed: 1,
			Tags: []Tag{
				{ID: "tag_1", Slug: "on-sale", UsageCount: 4},
				{ID: "tag_2", Slug: "clearance", UsageCount: 1},
			},
		})
	})

	res, err := c.Resources.ReplaceTags(context.Background(), "order", "ord_9", ResourceReplace{
		ApplicationID: "commerce",
		TagIDs:        []string{"tag_1", "tag_2"},
		AssignedBy:    String("importer"),
	})
	if err != nil {
		t.Fatalf("ReplaceTags: %v", err)
	}
	if res.Created != 2 || res.Removed != 1 {
		t.Errorf("counts did not round-trip: %+v", res)
	}
	// Tags, not ResourceTags: the replace answers with the tags themselves.
	if len(res.Tags) != 2 || res.Tags[0].Slug != "on-sale" || res.Tags[1].UsageCount != 1 {
		t.Errorf("tags did not round-trip: %+v", res.Tags)
	}
}

// An empty replace is legal and clears the resource -- the difference from
// BulkAssign, which refuses one. The SDK must send it rather than second-guess.
func TestResources_ReplaceTags_EmptyClearsTheResource(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		in := decodeBody(t, r)
		for _, field := range []string{"tag_ids", "alias_slugs"} {
			if _, ok := in[field]; ok {
				t.Errorf("an empty %s should be omitted, got: %v", field, in)
			}
		}
		writeData(t, w, http.StatusOK, ResourceReplaceResult{Created: 0, Removed: 3, Tags: []Tag{}})
	})

	res, err := c.Resources.ReplaceTags(context.Background(), "order", "ord_9", ResourceReplace{
		ApplicationID: "commerce",
	})
	if err != nil {
		t.Fatalf("ReplaceTags: %v", err)
	}
	if res.Removed != 3 || len(res.Tags) != 0 {
		t.Errorf("an empty replace should clear the resource: %+v", res)
	}
}

// THE #32 TEST FOR THIS RESOURCE, and the spec is wrong twice over: it claims a
// bare array AND claims the elements are ResourceTag. Every shape a client
// written from that spec would accept must be an error here.
func TestResources_ReplaceTags_TheSpecsShapeIsAnError(t *testing.T) {
	tests := []struct {
		name string
		body any
	}{
		{
			name: "bare array of ResourceTag, exactly as the spec describes it",
			body: []ResourceTag{{AssignmentID: "asg_1"}},
		},
		{
			name: "array under the data envelope",
			body: map[string]any{"data": []ResourceTag{{AssignmentID: "asg_1"}}},
		},
		{
			name: "composite with the counts renamed",
			body: map[string]any{"data": map[string]any{"added": 1, "deleted": 0, "tags": []any{}}},
		},
		{
			name: "composite with no tags array",
			body: map[string]any{"data": map[string]any{"created": 1, "removed": 0}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, http.StatusOK, tt.body)
			})

			res, err := c.Resources.ReplaceTags(context.Background(), "order", "ord_9", ResourceReplace{
				ApplicationID: "commerce", TagIDs: []string{"tag_1"},
			})
			if err == nil {
				t.Fatalf("expected an error, got %+v -- a wrong shape must never decode to zeros", res)
			}
		})
	}
}

// Created 0 / Removed 0 is what a replace reports when the set already matched,
// so it must decode cleanly rather than being mistaken for the failures above.
func TestResources_ReplaceTags_NoChangeIsLegal(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]any{
			"data": map[string]any{"created": 0, "removed": 0, "tags": nil},
		})
	})

	res, err := c.Resources.ReplaceTags(context.Background(), "order", "ord_9", ResourceReplace{
		ApplicationID: "commerce", TagIDs: []string{"tag_1"},
	})
	if err != nil {
		t.Fatalf("ReplaceTags: %v", err)
	}
	if res.Created != 0 || res.Removed != 0 {
		t.Errorf("counts = %+v, want zeros", res)
	}
	if res.Tags == nil || len(res.Tags) != 0 {
		t.Errorf("a null tags array should normalize to empty non-nil, got %+v", res.Tags)
	}
}

// UnmarshalJSON is exported, so it must fully define what it decodes into.
func TestResourceReplaceResult_UnmarshalIntoAReusedValueIsTotal(t *testing.T) {
	res := ResourceReplaceResult{Created: 9, Removed: 9, Tags: []Tag{{ID: "stale"}}}

	if err := json.Unmarshal([]byte(`{"created":1,"removed":2,"tags":[]}`), &res); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if res.Created != 1 || res.Removed != 2 || len(res.Tags) != 0 {
		t.Errorf("decoded fields did not replace the stale ones: %+v", res)
	}
}

func TestTags_ListResources(t *testing.T) {
	assigned := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2/tags/tag_1/resources" {
			t.Errorf("got %s %s, want GET /api/v2/tags/tag_1/resources", r.Method, r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("application_id") != "commerce" || q.Get("resource_type") != "order" {
			t.Errorf("unexpected query: %v", q)
		}
		if q.Get("limit") != "10" {
			t.Errorf("limit = %q, want 10", q.Get("limit"))
		}
		writeJSON(t, w, http.StatusOK, map[string]any{
			"data": []TagResource{{
				ApplicationID: "commerce",
				NamespaceType: String("merchant"),
				NamespaceID:   String("m-42"),
				ResourceType:  "order",
				ResourceID:    "ord_9",
				AssignedAt:    assigned,
			}},
			"pagination": map[string]any{"limit": 10, "offset": 0, "count": 1},
		})
	})

	page, err := c.Tags.ListResources(context.Background(), "tag_1", &TagListResourcesParams{
		ListOptions:   ListOptions{Limit: 10},
		ApplicationID: String("commerce"),
		ResourceType:  String("order"),
	})
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	if len(page.Data) != 1 {
		t.Fatalf("len(Data) = %d, want 1", len(page.Data))
	}
	got := page.Data[0]
	if got.ResourceType != "order" || got.ResourceID != "ord_9" || got.ApplicationID != "commerce" {
		t.Errorf("resource identity did not round-trip: %+v", got)
	}
	if got.NamespaceType == nil || *got.NamespaceType != "merchant" {
		t.Errorf("NamespaceType = %v, want merchant", got.NamespaceType)
	}
	if !got.AssignedAt.Equal(assigned) {
		t.Errorf("AssignedAt = %v, want %v", got.AssignedAt, assigned)
	}
}

func TestTags_ListResources_UnknownTag(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusNotFound, map[string]any{
			"error": map[string]any{"code": CodeNotFound, "message": "Tag was not found."},
		})
	})

	_, err := c.Tags.ListResources(context.Background(), "missing", nil)
	if !IsNotFound(err) {
		t.Fatalf("expected not found, got %v", err)
	}
}

// Both path segments are escaped, so neither can reach past its own segment and
// address a different route. Same invariant the alias tests pin, two segments in.
func TestResources_PathKeepsSegmentsSeparate(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v2/resources/") || !strings.HasSuffix(r.URL.Path, "/tags") {
			t.Errorf("path = %q, want the /api/v2/resources/{type}/{id}/tags route", r.URL.Path)
		}
		if got := strings.Count(r.URL.Path, "/"); got != 6 {
			t.Errorf("path = %q has %d separators, want 6: a segment escaped", r.URL.Path, got)
		}
		writeJSON(t, w, http.StatusOK, map[string]any{
			"data":       []ResourceTag{},
			"pagination": map[string]any{"limit": 50, "offset": 0, "count": 0},
		})
	})

	if _, err := c.Resources.ListTags(context.Background(), "order/type", "ord/9",
		&ResourceListTagsParams{ApplicationID: String("commerce")}); err != nil {
		t.Fatalf("ListTags: %v", err)
	}
}

// Scoping is inherited from the transport. The read carries its application in
// the query; the replace carries it in the body, so WithApplication is refused.
func TestResources_NamespaceScoping(t *testing.T) {
	t.Run("list carries the headers and the application query", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if got := r.Header.Get(namespaceTypeHeader); got != "merchant" {
				t.Errorf("%s = %q, want merchant", namespaceTypeHeader, got)
			}
			if got := r.URL.Query().Get("application_id"); got != "commerce" {
				t.Errorf("application_id = %q, want commerce", got)
			}
			writeJSON(t, w, http.StatusOK, map[string]any{
				"data":       []ResourceTag{},
				"pagination": map[string]any{"limit": 50, "offset": 0, "count": 0},
			})
		})
		_, err := c.Resources.ListTags(context.Background(), "order", "ord_9",
			&ResourceListTagsParams{ApplicationID: String("commerce")}, WithNamespace("merchant", "m-42"))
		if err != nil {
			t.Fatalf("ListTags: %v", err)
		}
	})

	t.Run("replace refuses WithApplication", func(t *testing.T) {
		c := newUnreachableClient(t, APIV2)
		_, err := c.Resources.ReplaceTags(context.Background(), "order", "ord_9",
			ResourceReplace{ApplicationID: "commerce"}, WithApplication("commerce"))
		if err == nil {
			t.Fatal("expected WithApplication to be refused on a body-carrying write")
		}
		if !strings.Contains(err.Error(), "ApplicationID field") {
			t.Errorf("error should point at the body field, got: %v", err)
		}
	})
}

func TestResources_OnV1(t *testing.T) {
	c := newVersionedTestClient(t, APIV1, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/resources/order/ord_9/tags" {
			t.Errorf("path = %q, want /api/v1/resources/order/ord_9/tags", r.URL.Path)
		}
		writeJSON(t, w, http.StatusOK, map[string]any{
			"data":       []ResourceTag{{AssignmentID: "asg_1", Tag: Tag{ID: "tag_1"}}},
			"pagination": map[string]any{"limit": 50, "offset": 0, "count": 1},
		})
	})

	page, err := c.Resources.ListTags(context.Background(), "order", "ord_9",
		&ResourceListTagsParams{ApplicationID: String("commerce")})
	if err != nil {
		t.Fatalf("ListTags on v1: %v", err)
	}
	// v1 responses carry no namespace fields, so they stay nil rather than "".
	if page.Data[0].NamespaceType != nil || page.Data[0].NamespaceID != nil {
		t.Errorf("v1 resource tag reported a namespace: %+v", page.Data[0])
	}
}

// Decodes RAW bodies written with the wire's own field names, for both models.
//
// Every other fixture here is built by marshalling the struct, so a misspelled
// json tag is used for the write and the read alike and round-trips perfectly --
// the fixture and the client share the same mistake. Verified: renaming these
// tags left the whole unit suite green, and only the real-server smoke step
// caught it. This is the hermetic half of that pair, so a typo fails in
// milliseconds rather than needing a container.
func TestResourceModels_DecodeTheWiresFieldNames(t *testing.T) {
	t.Run("ResourceTag", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, http.StatusOK, map[string]any{
				"data": []any{map[string]any{
					"assignment_id":  "asg_1",
					"namespace_type": "merchant",
					"namespace_id":   "m-42",
					"assigned_by":    "importer",
					"assigned_at":    "2026-06-08T12:00:00Z",
					"tag":            map[string]any{"id": "tag_1", "slug": "on-sale"},
				}},
				"pagination": map[string]any{"limit": 50, "offset": 0, "count": 1},
			})
		})

		page, err := c.Resources.ListTags(context.Background(), "order", "ord_9",
			&ResourceListTagsParams{ApplicationID: String("commerce")})
		if err != nil {
			t.Fatalf("ListTags: %v", err)
		}
		got := page.Data[0]
		if got.AssignmentID != "asg_1" {
			t.Errorf("AssignmentID = %q, want asg_1", got.AssignmentID)
		}
		if got.NamespaceType == nil || *got.NamespaceType != "merchant" {
			t.Errorf("NamespaceType = %v, want merchant", got.NamespaceType)
		}
		if got.NamespaceID == nil || *got.NamespaceID != "m-42" {
			t.Errorf("NamespaceID = %v, want m-42", got.NamespaceID)
		}
		if got.AssignedBy == nil || *got.AssignedBy != "importer" {
			t.Errorf("AssignedBy = %v, want importer", got.AssignedBy)
		}
		if got.AssignedAt.IsZero() || got.Tag.Slug != "on-sale" {
			t.Errorf("assigned_at / nested tag did not decode: %+v", got)
		}
	})

	t.Run("TagResource", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, http.StatusOK, map[string]any{
				"data": []any{map[string]any{
					"application_id": "commerce",
					"namespace_type": "merchant",
					"namespace_id":   "m-42",
					"resource_type":  "order",
					"resource_id":    "ord_9",
					"assigned_by":    "importer",
					"assigned_at":    "2026-06-08T12:00:00Z",
				}},
				"pagination": map[string]any{"limit": 50, "offset": 0, "count": 1},
			})
		})

		page, err := c.Tags.ListResources(context.Background(), "tag_1", nil)
		if err != nil {
			t.Fatalf("ListResources: %v", err)
		}
		got := page.Data[0]
		if got.ApplicationID != "commerce" || got.ResourceType != "order" || got.ResourceID != "ord_9" {
			t.Errorf("identity fields did not decode: %+v", got)
		}
		if got.NamespaceType == nil || *got.NamespaceType != "merchant" {
			t.Errorf("NamespaceType = %v, want merchant", got.NamespaceType)
		}
		if got.NamespaceID == nil || *got.NamespaceID != "m-42" {
			t.Errorf("NamespaceID = %v, want m-42", got.NamespaceID)
		}
		if got.AssignedBy == nil || *got.AssignedBy != "importer" {
			t.Errorf("AssignedBy = %v, want importer", got.AssignedBy)
		}
		if got.AssignedAt.IsZero() {
			t.Error("assigned_at did not decode")
		}
	})
}
