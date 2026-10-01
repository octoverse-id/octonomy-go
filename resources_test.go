package octonomy

// Ported from main's resources_test.go at 5e40964 for #94. What changed in the
// port, besides the dialect (defer cleanup() for t.Cleanup, interface{} for any,
// ioutil for io, a key lookup for url.Values.Has, String for Set):
//
//   - The default test client targets /api/v1, this line's default, so paths
//     assert /api/v1. The namespace case pins APIV2 with newVersionedTestClient,
//     since WithNamespace is refused on v1.
//   - TestResources_OnV1 is TestResources_OnV2 here: v1 is what every other test
//     already exercises, so the explicit-version case pins the opt-in surface.
//   - TestResources_ReplaceTags decodes a raw wire body rather than one
//     marshalled from ResourceReplaceResult, so a misspelled tag on the nested
//     Tag cannot round-trip through its own mistake (AGENTS.md).
//   - The identity cases at the end are new. main covers them through
//     identityfields_test.go, a source guard #95 rewrites for this dialect; until
//     then they are asserted here through a decode. One of them pins a deliberate
//     difference: a type error inside a replace row names the array but no index,
//     because this line decodes the whole []Tag at once instead of row by row.
//   - decodeBody is shared with assignments_test.go, where main declares it.

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
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/resources/order/ord_9/tags" {
			t.Errorf("got %s %s, want GET /api/v1/resources/order/ord_9/tags", r.Method, r.URL.Path)
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
		writeJSON(t, w, http.StatusOK, map[string]interface{}{
			"data": []ResourceTag{{
				AssignmentID: "asg_1",
				AssignedBy:   String("importer"),
				AssignedAt:   assigned,
				Tag:          Tag{ID: "tag_1", Name: "On Sale", Slug: "on-sale", Type: "label", UsageCount: 3},
			}},
			"pagination": map[string]interface{}{"limit": 25, "offset": 50, "count": 1},
		})
	})
	defer cleanup()

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
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "application_id=commerce" {
			t.Errorf("query = %q, want exactly application_id=commerce", r.URL.RawQuery)
		}
		// Key presence, not Get: url.Values.Has needs Go 1.17.
		if _, ok := r.URL.Query()["is_active"]; ok {
			t.Error("is_active is a different route's parameter and must never be sent here")
		}
		writeJSON(t, w, http.StatusOK, map[string]interface{}{
			"data":       []ResourceTag{},
			"pagination": map[string]interface{}{"limit": 50, "offset": 0, "count": 0},
		})
	})
	defer cleanup()

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
		c, cleanup := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if got := r.URL.Query().Get("application_id"); got != "commerce" {
				t.Errorf("application_id = %q, want commerce", got)
			}
			writeJSON(t, w, http.StatusOK, map[string]interface{}{
				"data":       []ResourceTag{},
				"pagination": map[string]interface{}{"limit": 50, "offset": 0, "count": 0},
			})
		})
		defer cleanup()
		if _, err := c.Resources.ListTags(context.Background(), "order", "ord_9", nil,
			WithApplication("commerce")); err != nil {
			t.Fatalf("ListTags: %v", err)
		}
	})

	t.Run("contradicting the params is an error", func(t *testing.T) {
		c, cleanup := newUnreachableClient(t, APIV2)
		defer cleanup()
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
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			t.Errorf("nil params should send no query, got %q", r.URL.RawQuery)
		}
		writeJSON(t, w, http.StatusBadRequest, map[string]interface{}{
			"error": map[string]interface{}{
				"code":    CodeValidation,
				"message": "Request validation failed.",
				"details": map[string]interface{}{"application_id": []string{"This query parameter is required."}},
			},
		})
	})
	defer cleanup()

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
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/resources/order/ord_9/tags" {
			t.Errorf("got %s %s, want POST /api/v1/resources/order/ord_9/tags", r.Method, r.URL.Path)
		}
		in := decodeBody(t, r)
		if in["application_id"] != "commerce" {
			t.Errorf("application_id = %v, want commerce", in["application_id"])
		}
		if tagIDs, _ := in["tag_ids"].([]interface{}); len(tagIDs) != 2 {
			t.Errorf("tag_ids = %v, want two ids", in["tag_ids"])
		}
		// The path names the resource and the server overwrites any body value,
		// so sending them would be noise that reads as meaningful.
		for _, field := range []string{"resource_type", "resource_id"} {
			if _, ok := in[field]; ok {
				t.Errorf("%s comes from the path and must not be in the body, got: %v", field, in)
			}
		}
		writeRaw(w, http.StatusOK, `{"data":{"created":2,"removed":1,"tags":[`+
			`{"id":"tag_1","slug":"on-sale","usage_count":4},`+
			`{"id":"tag_2","slug":"clearance","usage_count":1}]}}`)
	})
	defer cleanup()

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
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		in := decodeBody(t, r)
		for _, field := range []string{"tag_ids", "alias_slugs"} {
			if _, ok := in[field]; ok {
				t.Errorf("an empty %s should be omitted, got: %v", field, in)
			}
		}
		writeData(t, w, http.StatusOK, ResourceReplaceResult{Created: 0, Removed: 3, Tags: []Tag{}})
	})
	defer cleanup()

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
		body interface{}
	}{
		{
			name: "bare array of ResourceTag, exactly as the spec describes it",
			body: []ResourceTag{{AssignmentID: "asg_1"}},
		},
		{
			name: "array under the data envelope",
			body: map[string]interface{}{"data": []ResourceTag{{AssignmentID: "asg_1"}}},
		},
		{
			name: "composite with the counts renamed",
			body: map[string]interface{}{"data": map[string]interface{}{"added": 1, "deleted": 0, "tags": []interface{}{}}},
		},
		{
			name: "composite with no tags array",
			body: map[string]interface{}{"data": map[string]interface{}{"created": 1, "removed": 0}},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, http.StatusOK, tt.body)
			})
			defer cleanup()

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
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]interface{}{
			"data": map[string]interface{}{"created": 0, "removed": 0, "tags": nil},
		})
	})
	defer cleanup()

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
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/tags/tag_1/resources" {
			t.Errorf("got %s %s, want GET /api/v1/tags/tag_1/resources", r.Method, r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("application_id") != "commerce" || q.Get("resource_type") != "order" {
			t.Errorf("unexpected query: %v", q)
		}
		if q.Get("limit") != "10" {
			t.Errorf("limit = %q, want 10", q.Get("limit"))
		}
		writeJSON(t, w, http.StatusOK, map[string]interface{}{
			"data": []TagResource{{
				ApplicationID: "commerce",
				NamespaceType: String("merchant"),
				NamespaceID:   String("m-42"),
				ResourceType:  "order",
				ResourceID:    "ord_9",
				AssignedAt:    assigned,
			}},
			"pagination": map[string]interface{}{"limit": 10, "offset": 0, "count": 1},
		})
	})
	defer cleanup()

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
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusNotFound, map[string]interface{}{
			"error": map[string]interface{}{"code": CodeNotFound, "message": "Tag was not found."},
		})
	})
	defer cleanup()

	_, err := c.Tags.ListResources(context.Background(), "missing", nil)
	if !IsNotFound(err) {
		t.Fatalf("expected not found, got %v", err)
	}
}

// A slash in a segment is escaped, so it cannot reach past that segment and
// address a different route -- and the request that results is one the server
// will not route at all.
//
// Both halves matter and the second is easy to miss. The SDK sends %2F, which is
// correct; the server's Django <str:resource_id> route then receives a DECODED
// slash from WSGI, splits the segment, and matches nothing. Probed against 3.1.0:
// an envelope-less 404. So the outcome is a loud failure rather than a read of
// some other resource, which is the property worth pinning -- a canned 200 here
// would assert a shape the real server never produces.
func TestResources_ASlashInAnIDIsEscapedAndThenUnroutable(t *testing.T) {
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		// Escaped once, so the id stays one segment on the wire.
		if got := r.URL.EscapedPath(); got != "/api/v1/resources/order%2Ftype/ord%2F9/tags" {
			t.Errorf("escaped path = %q, want /api/v1/resources/order%%2Ftype/ord%%2F9/tags", got)
		}
		// What the real server answers: a 404 with no Octonomy error envelope.
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusNotFound)
		if _, err := w.Write([]byte("<!doctype html><title>Not Found</title>")); err != nil {
			t.Errorf("write body: %v", err)
		}
	})
	defer cleanup()

	_, err := c.Resources.ListTags(context.Background(), "order/type", "ord/9",
		&ResourceListTagsParams{ApplicationID: String("commerce")})
	if err == nil {
		t.Fatal("expected an error for an unroutable resource id")
	}
	// Loud and correctly classified: an envelope-less 404 is infrastructure, not
	// "this resource has no tags", so IsNotFound must stay false.
	if !IsUnexpectedStatus(err) {
		t.Errorf("expected IsUnexpectedStatus, got %v", err)
	}
	if IsNotFound(err) {
		t.Error("an unrouted 404 must not read as a real not_found")
	}
}

// Scoping is inherited from the transport. The read carries its application in
// the query; the replace carries it in the body, so WithApplication is refused.
func TestResources_NamespaceScoping(t *testing.T) {
	t.Run("list carries the headers and the application query", func(t *testing.T) {
		c, cleanup := newVersionedTestClient(t, APIV2, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v2/resources/order/ord_9/tags" {
				t.Errorf("path = %q, want /api/v2/resources/order/ord_9/tags", r.URL.Path)
			}
			if got := r.Header.Get(namespaceTypeHeader); got != "merchant" {
				t.Errorf("%s = %q, want merchant", namespaceTypeHeader, got)
			}
			if got := r.URL.Query().Get("application_id"); got != "commerce" {
				t.Errorf("application_id = %q, want commerce", got)
			}
			writeJSON(t, w, http.StatusOK, map[string]interface{}{
				"data":       []ResourceTag{},
				"pagination": map[string]interface{}{"limit": 50, "offset": 0, "count": 0},
			})
		})
		defer cleanup()
		_, err := c.Resources.ListTags(context.Background(), "order", "ord_9",
			&ResourceListTagsParams{ApplicationID: String("commerce")}, WithNamespace("merchant", "m-42"))
		if err != nil {
			t.Fatalf("ListTags: %v", err)
		}
	})

	t.Run("replace refuses WithApplication", func(t *testing.T) {
		c, cleanup := newUnreachableClient(t, APIV2)
		defer cleanup()
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

// The opt-in surface: the same route under /api/v2, decoding a v2 row that
// carries the namespace pair.
func TestResources_OnV2(t *testing.T) {
	c, cleanup := newVersionedTestClient(t, APIV2, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/resources/order/ord_9/tags" {
			t.Errorf("path = %q, want /api/v2/resources/order/ord_9/tags", r.URL.Path)
		}
		writeRaw(w, http.StatusOK, `{"data":[{"assignment_id":"asg_1","namespace_type":"merchant",`+
			`"namespace_id":"m-42","tag":{"id":"tag_1"}}],"pagination":{"limit":50,"offset":0,"count":1}}`)
	})
	defer cleanup()

	page, err := c.Resources.ListTags(context.Background(), "order", "ord_9",
		&ResourceListTagsParams{ApplicationID: String("commerce")})
	if err != nil {
		t.Fatalf("ListTags on v2: %v", err)
	}
	if got := page.Data[0]; got.NamespaceType == nil || *got.NamespaceType != "merchant" ||
		got.NamespaceID == nil || *got.NamespaceID != "m-42" {
		t.Errorf("v2 resource tag lost its namespace: %+v", got)
	}
}

// v1 responses carry no namespace fields, so they stay nil rather than "".
func TestResources_V1RowsKeepNilNamespace(t *testing.T) {
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]interface{}{
			"data":       []interface{}{map[string]interface{}{"assignment_id": "asg_1", "tag": map[string]interface{}{"id": "tag_1"}}},
			"pagination": map[string]interface{}{"limit": 50, "offset": 0, "count": 1},
		})
	})
	defer cleanup()

	page, err := c.Resources.ListTags(context.Background(), "order", "ord_9",
		&ResourceListTagsParams{ApplicationID: String("commerce")})
	if err != nil {
		t.Fatalf("ListTags on v1: %v", err)
	}
	if page.Data[0].NamespaceType != nil || page.Data[0].NamespaceID != nil {
		t.Errorf("v1 resource tag reported a namespace: %+v", page.Data[0])
	}
}

// Decodes RAW bodies written with the wire's own field names, for both models.
//
// A fixture built by marshalling the struct uses a misspelled json tag for the
// write and the read alike and round-trips perfectly -- the fixture and the
// client share the same mistake. This is the hermetic half of the real-server
// smoke step, so a typo fails in milliseconds rather than needing a container.
func TestResourceModels_DecodeTheWiresFieldNames(t *testing.T) {
	t.Run("ResourceTag", func(t *testing.T) {
		c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, http.StatusOK, map[string]interface{}{
				"data": []interface{}{map[string]interface{}{
					"assignment_id":  "asg_1",
					"namespace_type": "merchant",
					"namespace_id":   "m-42",
					"assigned_by":    "importer",
					"assigned_at":    "2026-06-08T12:00:00Z",
					"tag":            map[string]interface{}{"id": "tag_1", "slug": "on-sale"},
				}},
				"pagination": map[string]interface{}{"limit": 50, "offset": 0, "count": 1},
			})
		})
		defer cleanup()

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
		c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, http.StatusOK, map[string]interface{}{
				"data": []interface{}{map[string]interface{}{
					"application_id": "commerce",
					"namespace_type": "merchant",
					"namespace_id":   "m-42",
					"resource_type":  "order",
					"resource_id":    "ord_9",
					"assigned_by":    "importer",
					"assigned_at":    "2026-06-08T12:00:00Z",
				}},
				"pagination": map[string]interface{}{"limit": 50, "offset": 0, "count": 1},
			})
		})
		defer cleanup()

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

// A resource id containing a character that needs escaping must reach the
// server as the id the caller named.
//
// resource_id is a caller-chosen external identifier the server validates only
// as non-blank, so a space is legal in one. Escaped twice -- as every path was
// before resolvePath -- "ord 9" went out as "ord%25209" and arrived as the
// literal "ord%209": a different resource. On ListTags that reads nothing; on
// ReplaceTags, which is destructive, it writes a tag set against a resource the
// caller never named.
func TestResources_EscapesSpecialCharactersExactlyOnce(t *testing.T) {
	tests := []struct {
		name        string
		resourceID  string
		wantEscaped string
		wantDecoded string
	}{
		{"space", "ord 9", "/api/v1/resources/order/ord%209/tags", "/api/v1/resources/order/ord 9/tags"},
		{"percent", "ord%9", "/api/v1/resources/order/ord%259/tags", "/api/v1/resources/order/ord%9/tags"},
		{"hash", "ord#9", "/api/v1/resources/order/ord%239/tags", "/api/v1/resources/order/ord#9/tags"},
		{"plain", "ord_9", "/api/v1/resources/order/ord_9/tags", "/api/v1/resources/order/ord_9/tags"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			c, cleanup := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if got := r.URL.EscapedPath(); got != tt.wantEscaped {
					t.Errorf("escaped path = %q, want %q", got, tt.wantEscaped)
				}
				// What the server decodes back out is the id the caller passed.
				if got := r.URL.Path; got != tt.wantDecoded {
					t.Errorf("decoded path = %q, want %q", got, tt.wantDecoded)
				}
				writeJSON(t, w, http.StatusOK, map[string]interface{}{
					"data":       []ResourceTag{},
					"pagination": map[string]interface{}{"limit": 50, "offset": 0, "count": 0},
				})
			})
			defer cleanup()

			_, err := c.Resources.ListTags(context.Background(), "order", tt.resourceID,
				&ResourceListTagsParams{ApplicationID: String("commerce")})
			if err != nil {
				t.Fatalf("ListTags: %v", err)
			}
		})
	}
}

// The same guarantee on the destructive route, which is the one that matters:
// a replace must act on the resource the caller named or fail, never on another.
func TestResources_ReplaceTagsAddressesTheNamedResource(t *testing.T) {
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/api/v1/resources/order/ord 9/tags" {
			t.Errorf("decoded path = %q, want the literal id \"ord 9\"", got)
		}
		writeData(t, w, http.StatusOK, ResourceReplaceResult{Created: 1, Tags: []Tag{{ID: "tag_1"}}})
	})
	defer cleanup()

	_, err := c.Resources.ReplaceTags(context.Background(), "order", "ord 9", ResourceReplace{
		ApplicationID: "commerce", TagIDs: []string{"tag_1"},
	})
	if err != nil {
		t.Fatalf("ReplaceTags: %v", err)
	}
}

// The row-level half of the replace composite: the counts can be right while a
// row is blank, so each row is held to the resource standard.
func TestResources_ReplaceTags_ARowThatWouldBeZeroValuedIsAnError(t *testing.T) {
	tests := []struct {
		name string
		rows interface{}
		want string
	}{
		{"null row", []interface{}{nil}, "element 0 is null"},
		{"empty row", []interface{}{map[string]interface{}{}}, "element 0 is an empty object"},
		{"a good row does not excuse a blank id", []interface{}{
			map[string]interface{}{"id": "tag_1"},
			map[string]interface{}{"id": nil, "slug": "on-sale"},
		}, `element 1 decoded with no "id"`},
		{"renamed id", []interface{}{map[string]interface{}{"identifier": "tag_1"}}, `element 0 decoded with no "id"`},
		// A deliberate difference from main, which decodes row by row and names
		// the index here too. This line decodes the whole []Tag in one call, so a
		// type error names the array and encoding/json's own message names the
		// field; the shape and identity checks above still name the index.
		{"type error in a row", []interface{}{map[string]interface{}{"id": 5}}, `decode replace "tags"`},
		{"tags is not an array", map[string]interface{}{"id": "tag_1"}, `replace "tags"`},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, http.StatusOK, map[string]interface{}{"data": map[string]interface{}{
					"created": 1, "removed": 0, "tags": tt.rows,
				}})
			})
			defer cleanup()

			res, err := c.Resources.ReplaceTags(context.Background(), "order", "ord_9", ResourceReplace{
				ApplicationID: "commerce", TagIDs: []string{"tag_1"},
			})
			if err == nil {
				t.Fatalf("expected an error, got %+v", res)
			}
			if res != nil {
				t.Errorf("a refused replace returned a result alongside the error: %+v", res)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error should say %q, got: %v", tt.want, err)
			}
			if !strings.Contains(err.Error(), "tags") {
				t.Errorf("error should name the array, got: %v", err)
			}
		})
	}
}

// The identity of a list row. ResourceTag has no "id": its identity is
// assignment_id, and the embedded tag's id with it, because both vendored
// contracts mark "tag" required and the inline tag is the route's whole point.
// TagResource has neither an id nor a tag: its identity is resource_id. Each
// blank one must fail the page, naming the row, rather than decode to a
// plausible-looking blank.
func TestResources_ListRowsCarryTheirIdentity(t *testing.T) {
	type listCall func(*Client) error
	listTags := func(c *Client) error {
		_, err := c.Resources.ListTags(context.Background(), "order", "ord_9",
			&ResourceListTagsParams{ApplicationID: String("commerce")})
		return err
	}
	listResources := func(c *Client) error {
		_, err := c.Tags.ListResources(context.Background(), "tag_1", nil)
		return err
	}
	tests := []struct {
		name string
		call listCall
		rows string
		want string
	}{
		{"ResourceTag with a null assignment_id", listTags,
			`[{"assignment_id":null,"tag":{"id":"tag_1"}}]`, `element 0 decoded with no "assignment_id"`},
		{"ResourceTag with an id but no assignment_id", listTags,
			`[{"id":"asg_1","tag":{"id":"tag_1"}}]`, `element 0 decoded with no "assignment_id"`},
		{"ResourceTag with no tag", listTags,
			`[{"assignment_id":"asg_1"}]`, `element 0 decoded with no "tag.id"`},
		{"ResourceTag with an empty tag", listTags,
			`[{"assignment_id":"asg_1","tag":{}}]`, `element 0 decoded with no "tag.id"`},
		{"ResourceTag: a good row does not excuse a bad one", listTags,
			`[{"assignment_id":"asg_1","tag":{"id":"tag_1"}},{"assignment_id":"asg_2","tag":{"slug":"x"}}]`,
			`element 1 decoded with no "tag.id"`},
		{"TagResource with a null resource_id", listResources,
			`[{"application_id":"commerce","resource_type":"order","resource_id":null}]`, `element 0 decoded with no "resource_id"`},
		{"TagResource with the id renamed", listResources,
			`[{"application_id":"commerce","resource_type":"order","resourceId":"ord_9"}]`, `element 0 decoded with no "resource_id"`},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				writeRaw(w, http.StatusOK, `{"data":`+tt.rows+`,"pagination":{"limit":50,"offset":0,"count":1}}`)
			})
			defer cleanup()

			err := tt.call(c)
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error should say %q, got: %v", tt.want, err)
			}
		})
	}
}

// The converse, so the identity rule cannot be an over-reach: the rows above
// with their identity present decode. assignment_id + tag.id is enough for a
// ResourceTag, and resource_id alone for a TagResource -- nothing else the
// contracts document is required by this client.
func TestResources_ListRowsWithTheirIdentityDecode(t *testing.T) {
	t.Run("ResourceTag", func(t *testing.T) {
		c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			writeRaw(w, http.StatusOK, `{"data":[{"assignment_id":"asg_1","tag":{"id":"tag_1"}}],"pagination":{"limit":50}}`)
		})
		defer cleanup()
		page, err := c.Resources.ListTags(context.Background(), "order", "ord_9",
			&ResourceListTagsParams{ApplicationID: String("commerce")})
		if err != nil {
			t.Fatalf("ListTags: %v", err)
		}
		if len(page.Data) != 1 || page.Data[0].AssignmentID != "asg_1" || page.Data[0].Tag.ID != "tag_1" {
			t.Errorf("unexpected page: %+v", page.Data)
		}
	})
	t.Run("TagResource", func(t *testing.T) {
		c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			writeRaw(w, http.StatusOK, `{"data":[{"resource_id":"ord_9"}],"pagination":{"limit":50}}`)
		})
		defer cleanup()
		page, err := c.Tags.ListResources(context.Background(), "tag_1", nil)
		if err != nil {
			t.Fatalf("ListResources: %v", err)
		}
		if len(page.Data) != 1 || page.Data[0].ResourceID != "ord_9" {
			t.Errorf("unexpected page: %+v", page.Data)
		}
	})
	// A null page stays nil on this line, as on every list (see doList): the
	// composite normalization above is the replace's, not the list's.
	t.Run("null page", func(t *testing.T) {
		c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			writeRaw(w, http.StatusOK, `{"data":null,"pagination":{"limit":50,"count":0}}`)
		})
		defer cleanup()
		page, err := c.Tags.ListResources(context.Background(), "tag_1", nil)
		if err != nil {
			t.Fatalf("ListResources: %v", err)
		}
		if page.Data != nil {
			t.Errorf("Data = %#v, want nil: a null page decodes as it did in v1.0.0", page.Data)
		}
	})
}

// Both list types hand their rows back to doList, with the identity each names
// -- a list type whose rows() dropped or reordered rows would check the wrong
// ones in silence.
func TestResourceLists_RowsReportEachIdentity(t *testing.T) {
	tags := &ResourceTagList{Data: []ResourceTag{
		{AssignmentID: "asg_1", Tag: Tag{ID: "tag_1"}},
		{AssignmentID: "asg_2", Tag: Tag{ID: "tag_2"}},
	}}
	var got []string
	for _, row := range tags.rows() {
		for _, f := range row.identityFields() {
			got = append(got, f.name+"="+f.value)
		}
	}
	if want := "assignment_id=asg_1 tag.id=tag_1 assignment_id=asg_2 tag.id=tag_2"; strings.Join(got, " ") != want {
		t.Errorf("ResourceTagList identities = %v, want %s", got, want)
	}

	resources := &TagResourceList{Data: []TagResource{{ResourceID: "ord_9"}, {ResourceID: "ord_10"}}}
	got = nil
	for _, row := range resources.rows() {
		for _, f := range row.identityFields() {
			got = append(got, f.name+"="+f.value)
		}
	}
	if want := "resource_id=ord_9 resource_id=ord_10"; strings.Join(got, " ") != want {
		t.Errorf("TagResourceList identities = %v, want %s", got, want)
	}
}
