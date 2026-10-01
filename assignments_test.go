package octonomy

// Ported from main's assignments_test.go at 5e40964 for #94. What changed in the
// port, besides the dialect (defer cleanup() for t.Cleanup, interface{} for any,
// ioutil.ReadAll for io.ReadAll):
//
//   - The default test client targets APIV1 on this line, so most paths read
//     /api/v1. The namespace test pins APIV2 explicitly, and TestAssignments_OnV1
//     became TestAssignments_OnEitherSurface, so the opt-in surface is still
//     covered without a namespace.
//   - The BulkAssign and BulkRemove success cases answer with raw wire JSON
//     rather than a marshalled result struct, with three distinct non-zero
//     counters, so a decoder that crosses two of them or never assigns one
//     fails instead of round-tripping the fixture's own mistake.
//   - The zero-valued-row test gained the cases this line's hand-port of
//     decodeResourceArray has to carry itself: a row with a blank or renamed id,
//     a row whose field has the wrong type, and an "assignments" that is not an
//     array. A type error now names the array rather than an element index,
//     because the rows are decoded as one typed slice.
//   - decodeBody stays defined here, as on main; resources_test.go uses it too.

import (
	"context"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"strings"
	"testing"
	"time"
)

// decodeBody reads a request body as a generic map. Assignments put everything
// in the body -- including on DELETE -- so nearly every test here needs it.
//
// Errorf, never Fatalf: this runs on the httptest server's goroutine, and
// Fatalf there calls runtime.Goexit on the handler rather than the test,
// aborting the connection and burying the real failure under a client-side EOF.
// A nil map is safe to return for the same reason -- reads from it yield zero
// values, so the caller's own assertions still run and report against the
// failure this already named.
func decodeBody(t *testing.T, r *http.Request) map[string]interface{} {
	t.Helper()
	raw, err := ioutil.ReadAll(r.Body)
	if err != nil {
		t.Errorf("read body: %v", err)
		return nil
	}
	var in map[string]interface{}
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Errorf("decode body %q: %v", raw, err)
		return nil
	}
	return in
}

func TestAssignments_Create(t *testing.T) {
	assigned := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/tag-assignments" {
			t.Errorf("got %s %s, want POST /api/v1/tag-assignments", r.Method, r.URL.Path)
		}
		in := decodeBody(t, r)
		if in["application_id"] != "commerce" || in["tag_id"] != "tag_1" ||
			in["resource_type"] != "order" || in["resource_id"] != "ord_9" {
			t.Errorf("unexpected body: %v", in)
		}
		for _, field := range []string{"alias_id", "alias_slug", "assigned_by"} {
			if _, ok := in[field]; ok {
				t.Errorf("nil %s should be omitted, got: %v", field, in)
			}
		}
		writeData(t, w, http.StatusCreated, Assignment{
			ID: "asg_1", TenantID: "tenant-1", ApplicationID: "commerce",
			TagID: "tag_1", ResourceType: "order", ResourceID: "ord_9",
			AssignedAt: assigned,
		})
	})
	defer cleanup()

	got, err := c.Assignments.Create(context.Background(), AssignmentCreate{
		ApplicationID: "commerce", TagID: String("tag_1"),
		ResourceType: "order", ResourceID: "ord_9",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.ID != "asg_1" || got.TagID != "tag_1" || got.ApplicationID != "commerce" {
		t.Errorf("assignment did not round-trip: %+v", got)
	}
	if got.ResourceType != "order" || got.ResourceID != "ord_9" {
		t.Errorf("resource identity did not round-trip: %+v", got)
	}
	if !got.AssignedAt.Equal(assigned) {
		t.Errorf("AssignedAt = %v, want %v", got.AssignedAt, assigned)
	}
	// A global assignment reports no namespace, and AssignedBy stays nil rather
	// than decoding to "".
	if got.NamespaceType != nil || got.NamespaceID != nil || got.AssignedBy != nil {
		t.Errorf("nullable fields should be nil: %+v", got)
	}
}

// Re-assigning is a 200 rather than a 201, and is NOT an error. The SDK returns
// the same *Assignment for both, which is the documented limitation: doData
// decodes the payload and does not surface the status.
func TestAssignments_Create_IsIdempotent(t *testing.T) {
	for _, status := range []int{http.StatusCreated, http.StatusOK} {
		status := status
		t.Run(http.StatusText(status), func(t *testing.T) {
			c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				writeData(t, w, status, Assignment{
					ID: "asg_1", ApplicationID: "commerce", TagID: "tag_1",
					ResourceType: "order", ResourceID: "ord_9",
				})
			})
			defer cleanup()

			got, err := c.Assignments.Create(context.Background(), AssignmentCreate{
				ApplicationID: "commerce", TagID: String("tag_1"),
				ResourceType: "order", ResourceID: "ord_9",
			})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			if got.ID != "asg_1" {
				t.Errorf("ID = %q, want asg_1 (a zero-valued Assignment would reach here too)", got.ID)
			}
		})
	}
}

// The tag can be named three ways, and only the one set reaches the wire.
func TestAssignments_Create_TagIdentifiers(t *testing.T) {
	tests := []struct {
		name    string
		in      AssignmentCreate
		wantKey string
		wantVal string
	}{
		{
			name:    "by tag id",
			in:      AssignmentCreate{TagID: String("tag_1")},
			wantKey: "tag_id", wantVal: "tag_1",
		},
		{
			name:    "by alias id",
			in:      AssignmentCreate{AliasID: String("alias_1")},
			wantKey: "alias_id", wantVal: "alias_1",
		},
		{
			name:    "by alias slug",
			in:      AssignmentCreate{AliasSlug: String("sale")},
			wantKey: "alias_slug", wantVal: "sale",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			c, cleanup := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				in := decodeBody(t, r)
				if in[tt.wantKey] != tt.wantVal {
					t.Errorf("%s = %v, want %q", tt.wantKey, in[tt.wantKey], tt.wantVal)
				}
				for _, other := range []string{"tag_id", "alias_id", "alias_slug"} {
					if other == tt.wantKey {
						continue
					}
					if _, ok := in[other]; ok {
						t.Errorf("%s should be omitted when unset, got: %v", other, in)
					}
				}
				writeData(t, w, http.StatusCreated, Assignment{ID: "asg_1", TagID: "tag_1"})
			})
			defer cleanup()

			in := tt.in
			in.ApplicationID, in.ResourceType, in.ResourceID = "commerce", "order", "ord_9"
			if _, err := c.Assignments.Create(context.Background(), in); err != nil {
				t.Fatalf("Create: %v", err)
			}
		})
	}
}

func TestAssignments_Create_Errors(t *testing.T) {
	tests := []struct {
		name  string
		code  string
		check func(error) bool
	}{
		{"inactive tag", CodeInactiveTag, IsInactiveTag},
		{"application mismatch", CodeApplicationMismatch, IsApplicationMismatch},
		{"no identifier", CodeValidation, IsValidation},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, http.StatusBadRequest, map[string]interface{}{
					"error": map[string]interface{}{"code": tt.code, "message": "nope"},
				})
			})
			defer cleanup()

			_, err := c.Assignments.Create(context.Background(), AssignmentCreate{
				ApplicationID: "commerce", ResourceType: "order", ResourceID: "ord_9",
			})
			if !tt.check(err) {
				t.Fatalf("expected %s, got %v", tt.code, err)
			}
		})
	}
}

func TestAssignments_Create_UnwrappedBodyIsAnError(t *testing.T) {
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusCreated, Assignment{ID: "asg_1", TagID: "tag_1"})
	})
	defer cleanup()

	got, err := c.Assignments.Create(context.Background(), AssignmentCreate{
		ApplicationID: "commerce", TagID: String("tag_1"),
		ResourceType: "order", ResourceID: "ord_9",
	})
	if err == nil {
		t.Fatalf("expected an error for a body with no data envelope, got %+v", got)
	}
}

// The DELETE carries a body, which is how the row is identified -- there is no
// /tag-assignments/{id} route. Worth asserting explicitly, because a DELETE with
// a body is unusual enough that a future refactor might drop it.
func TestAssignments_Remove(t *testing.T) {
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/v1/tag-assignments" {
			t.Errorf("got %s %s, want DELETE /api/v1/tag-assignments", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json on a body-carrying DELETE", got)
		}
		in := decodeBody(t, r)
		if in["application_id"] != "commerce" || in["tag_id"] != "tag_1" ||
			in["resource_type"] != "order" || in["resource_id"] != "ord_9" {
			t.Errorf("unexpected body: %v", in)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	defer cleanup()

	err := c.Assignments.Remove(context.Background(), AssignmentRemove{
		ApplicationID: "commerce", TagID: "tag_1",
		ResourceType: "order", ResourceID: "ord_9",
	})
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
}

// Because the DELETE carries a body, WithApplication is refused on it like any
// other write -- the body's ApplicationID is authoritative. This is the one
// caller-visible consequence of the unusual shape, so it gets its own test.
func TestAssignments_Remove_RefusesWithApplication(t *testing.T) {
	for _, version := range []APIVersion{APIV1, APIV2} {
		version := version
		t.Run(string(version), func(t *testing.T) {
			c, cleanup := newUnreachableClient(t, version)
			defer cleanup()

			err := c.Assignments.Remove(context.Background(), AssignmentRemove{
				ApplicationID: "commerce", TagID: "tag_1",
				ResourceType: "order", ResourceID: "ord_9",
			}, WithApplication("commerce"))
			if err == nil {
				t.Fatal("expected WithApplication to be refused on a body-carrying DELETE")
			}
			if !strings.Contains(err.Error(), "ApplicationID field") {
				t.Errorf("error should point at the body field, got: %v", err)
			}
		})
	}
}

func TestAssignments_BulkAssign(t *testing.T) {
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/tag-assignments/bulk-assign" {
			t.Errorf("got %s %s, want POST /api/v1/tag-assignments/bulk-assign", r.Method, r.URL.Path)
		}
		in := decodeBody(t, r)
		tagIDs, _ := in["tag_ids"].([]interface{})
		slugs, _ := in["alias_slugs"].([]interface{})
		if len(tagIDs) != 2 || tagIDs[0] != "tag_1" {
			t.Errorf("tag_ids = %v, want [tag_1 tag_2]", in["tag_ids"])
		}
		if len(slugs) != 1 || slugs[0] != "sale" {
			t.Errorf("alias_slugs = %v, want [sale]", in["alias_slugs"])
		}
		if in["assigned_by"] != "importer" {
			t.Errorf("assigned_by = %v, want importer", in["assigned_by"])
		}
		// The composite the server really sends, NOT the bare array the spec
		// claims -- written with the wire's own key names rather than marshalled
		// from BulkAssignResult, so the decoder's spellings are what is tested.
		// Three distinct non-zero counters: a decoder that crossed two of them, or
		// never assigned one, would return something other than what was sent.
		writeRaw(w, http.StatusOK, `{"data": {"created": 2, "existing": 1, "skipped": 3, "assignments": [
			{"id": "asg_1", "tag_id": "tag_1", "application_id": "commerce"},
			{"id": "asg_2", "tag_id": "tag_2", "application_id": "commerce"},
			{"id": "asg_3", "tag_id": "tag_3", "application_id": "commerce"}
		]}}`)
	})
	defer cleanup()

	res, err := c.Assignments.BulkAssign(context.Background(), BulkAssign{
		ApplicationID: "commerce", ResourceType: "order", ResourceID: "ord_9",
		TagIDs: []string{"tag_1", "tag_2"}, AliasSlugs: []string{"sale"},
		AssignedBy: String("importer"),
	})
	if err != nil {
		t.Fatalf("BulkAssign: %v", err)
	}
	if res.Created != 2 || res.Existing != 1 || res.Skipped != 3 {
		t.Errorf("counts did not round-trip: %+v", res)
	}
	if len(res.Assignments) != 3 || res.Assignments[0].ID != "asg_1" || res.Assignments[2].TagID != "tag_3" {
		t.Errorf("assignments did not round-trip: %+v", res.Assignments)
	}
}

// THE #32 TEST FOR THIS RESOURCE. Both vendored specs claim bulk-assign returns
// a bare array of Assignment. It does not -- and a client written from the spec
// would decode this endpoint into a []Assignment, which against the real
// composite body yields an empty slice and a nil error. Both spellings of the
// spec's claim must be errors here, never an empty result.
func TestAssignments_BulkAssign_TheSpecsBareArrayIsAnError(t *testing.T) {
	tests := []struct {
		name string
		body interface{}
	}{
		{
			name: "bare array, exactly as the spec describes it",
			body: []Assignment{{ID: "asg_1", TagID: "tag_1"}},
		},
		{
			name: "array under the data envelope",
			body: map[string]interface{}{"data": []Assignment{{ID: "asg_1", TagID: "tag_1"}}},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, http.StatusOK, tt.body)
			})
			defer cleanup()

			res, err := c.Assignments.BulkAssign(context.Background(), BulkAssign{
				ApplicationID: "commerce", ResourceType: "order", ResourceID: "ord_9",
				TagIDs: []string{"tag_1"},
			})
			if err == nil {
				t.Fatalf("expected an error, got %+v -- a bare array must never decode to an empty result", res)
			}
		})
	}
}

// All or nothing: one unknown id fails the whole call, and an out-of-scope tag
// is reported identically to a nonexistent one so the response cannot be used to
// probe for tags in other namespaces.
func TestAssignments_BulkAssign_UnknownIDsFailTheWholeCall(t *testing.T) {
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusBadRequest, map[string]interface{}{
			"error": map[string]interface{}{
				"code":    CodeValidation,
				"message": "Request validation failed.",
				"details": map[string]interface{}{"tag_ids": []string{"Unknown tag ids: tag_x"}},
			},
		})
	})
	defer cleanup()

	_, err := c.Assignments.BulkAssign(context.Background(), BulkAssign{
		ApplicationID: "commerce", ResourceType: "order", ResourceID: "ord_9",
		TagIDs: []string{"tag_1", "tag_x"},
	})
	if !IsValidation(err) {
		t.Fatalf("expected a validation error, got %v", err)
	}
	apiErr, _ := AsAPIError(err)
	if _, ok := apiErr.Details["tag_ids"]; !ok {
		t.Errorf("Details should name the tag_ids axis, got %v", apiErr.Details)
	}
}

func TestAssignments_BulkRemove(t *testing.T) {
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		// A POST, not a DELETE -- the only remove in this SDK that is.
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/tag-assignments/bulk-remove" {
			t.Errorf("got %s %s, want POST /api/v1/tag-assignments/bulk-remove", r.Method, r.URL.Path)
		}
		in := decodeBody(t, r)
		if tagIDs, _ := in["tag_ids"].([]interface{}); len(tagIDs) != 2 {
			t.Errorf("tag_ids = %v, want two ids", in["tag_ids"])
		}
		if _, ok := in["alias_slugs"]; ok {
			t.Errorf("bulk remove has no alias form, got: %v", in)
		}
		// Wire spelling, not a marshalled BulkRemoveResult, for the reason
		// TestAssignments_BulkAssign gives.
		writeRaw(w, http.StatusOK, `{"data": {"removed": 2}}`)
	})
	defer cleanup()

	res, err := c.Assignments.BulkRemove(context.Background(), BulkRemove{
		ApplicationID: "commerce", ResourceType: "order", ResourceID: "ord_9",
		TagIDs: []string{"tag_1", "tag_2"},
	})
	if err != nil {
		t.Fatalf("BulkRemove: %v", err)
	}
	if res.Removed != 2 {
		t.Errorf("Removed = %d, want 2", res.Removed)
	}
}

// The specs document bulk-remove's 200 with NO schema at all, so there is
// nothing to write a client against. An unenveloped body must be an error rather
// than a Removed of 0, which a caller would read as "nothing matched".
func TestAssignments_BulkRemove_UnenvelopedBodyIsAnError(t *testing.T) {
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]interface{}{"removed": 2})
	})
	defer cleanup()

	res, err := c.Assignments.BulkRemove(context.Background(), BulkRemove{
		ApplicationID: "commerce", ResourceType: "order", ResourceID: "ord_9",
		TagIDs: []string{"tag_1"},
	})
	if err == nil {
		t.Fatalf("expected an error, got %+v -- a missing envelope must not read as 'nothing removed'", res)
	}
}

// Scoping is inherited from the transport with no per-resource work. These are
// all body-carrying writes, so the namespace headers go out while the
// application stays in the body. A namespace needs the opt-in v2 surface.
func TestAssignments_NamespaceScopingReachesTheWire(t *testing.T) {
	tests := []struct {
		name string
		path string
		// reply writes the shape THIS call expects. A single superset body
		// covering all four would pass while proving nothing about any of them,
		// and would lean on the permissive decode the composites now refuse.
		reply func(*testing.T, http.ResponseWriter)
		call  func(*Client) error
	}{
		{
			name: "create", path: "/api/v2/tag-assignments",
			reply: func(t *testing.T, w http.ResponseWriter) {
				writeData(t, w, http.StatusCreated, Assignment{ID: "asg_1", TagID: "tag_1"})
			},
			call: func(c *Client) error {
				_, err := c.Assignments.Create(context.Background(), AssignmentCreate{
					ApplicationID: "commerce", TagID: String("tag_1"),
					ResourceType: "order", ResourceID: "ord_9",
				}, WithNamespace("merchant", "m-42"))
				return err
			},
		},
		{
			name: "remove", path: "/api/v2/tag-assignments",
			reply: func(_ *testing.T, w http.ResponseWriter) {
				w.WriteHeader(http.StatusNoContent)
			},
			call: func(c *Client) error {
				return c.Assignments.Remove(context.Background(), AssignmentRemove{
					ApplicationID: "commerce", TagID: "tag_1",
					ResourceType: "order", ResourceID: "ord_9",
				}, WithNamespace("merchant", "m-42"))
			},
		},
		{
			name: "bulk assign", path: "/api/v2/tag-assignments/bulk-assign",
			reply: func(t *testing.T, w http.ResponseWriter) {
				writeData(t, w, http.StatusOK, BulkAssignResult{
					Created: 1, Assignments: []Assignment{{ID: "asg_1", TagID: "tag_1"}},
				})
			},
			call: func(c *Client) error {
				_, err := c.Assignments.BulkAssign(context.Background(), BulkAssign{
					ApplicationID: "commerce", ResourceType: "order", ResourceID: "ord_9",
					TagIDs: []string{"tag_1"},
				}, WithNamespace("merchant", "m-42"))
				return err
			},
		},
		{
			name: "bulk remove", path: "/api/v2/tag-assignments/bulk-remove",
			reply: func(t *testing.T, w http.ResponseWriter) {
				writeData(t, w, http.StatusOK, BulkRemoveResult{Removed: 1})
			},
			call: func(c *Client) error {
				_, err := c.Assignments.BulkRemove(context.Background(), BulkRemove{
					ApplicationID: "commerce", ResourceType: "order", ResourceID: "ord_9",
					TagIDs: []string{"tag_1"},
				}, WithNamespace("merchant", "m-42"))
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
				// The application lives in the body on every one of these, so
				// nothing should be adding it to the query.
				if r.URL.RawQuery != "" {
					t.Errorf("expected no query params, got %q", r.URL.RawQuery)
				}
				tt.reply(t, w)
			})
			defer cleanup()
			if err := tt.call(c); err != nil {
				t.Fatalf("%s: %v", tt.name, err)
			}
		})
	}
}

// Both surfaces route the same path suffix, and a response that carries no
// namespace fields -- every v1 response, and a global v2 one -- leaves them nil
// rather than "".
func TestAssignments_OnEitherSurface(t *testing.T) {
	for _, version := range []APIVersion{APIV1, APIV2} {
		version := version
		t.Run(string(version), func(t *testing.T) {
			want := "/api/" + string(version) + "/tag-assignments"
			c, cleanup := newVersionedTestClient(t, version, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != want {
					t.Errorf("path = %q, want %s", r.URL.Path, want)
				}
				writeData(t, w, http.StatusCreated, Assignment{
					ID: "asg_1", ApplicationID: "commerce", TagID: "tag_1",
					ResourceType: "order", ResourceID: "ord_9",
				})
			})
			defer cleanup()

			got, err := c.Assignments.Create(context.Background(), AssignmentCreate{
				ApplicationID: "commerce", TagID: String("tag_1"),
				ResourceType: "order", ResourceID: "ord_9",
			})
			if err != nil {
				t.Fatalf("Create on %s: %v", version, err)
			}
			if got.NamespaceType != nil || got.NamespaceID != nil {
				t.Errorf("a global assignment reported a namespace: %+v", got)
			}
		})
	}
}

// doData validates the data envelope's shape and the decoded row's identity,
// which is right for a resource: past that, a zero-valued Assignment has an
// empty ID and no caller mistakes it for an answer. A composite of counters is
// different -- created:0 existing:0 with no rows is an ordinary result -- so a
// renamed or missing key must be an error rather than "the tags were all
// already there".
func TestAssignments_BulkAssign_MissingKeysAreErrors(t *testing.T) {
	tests := []struct {
		name string
		data interface{}
	}{
		{"empty object", map[string]interface{}{}},
		{"renamed keys", map[string]interface{}{"results": []interface{}{}, "count": 1}},
		{"no assignments array", map[string]interface{}{"created": 1, "existing": 0, "skipped": 0}},
		{"no created count", map[string]interface{}{"existing": 0, "skipped": 0, "assignments": []interface{}{}}},
		{"no existing count", map[string]interface{}{"created": 1, "skipped": 0, "assignments": []interface{}{}}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, http.StatusOK, map[string]interface{}{"data": tt.data})
			})
			defer cleanup()

			res, err := c.Assignments.BulkAssign(context.Background(), BulkAssign{
				ApplicationID: "commerce", ResourceType: "order", ResourceID: "ord_9",
				TagIDs: []string{"tag_1"},
			})
			if err == nil {
				t.Fatalf("expected an error, got %+v", res)
			}
		})
	}
}

// The two shapes that must NOT be errors: an empty page of assignments is a real
// answer, and Skipped is vestigial, so the server dropping a dead field is not a
// client failure.
func TestAssignments_BulkAssign_LegalMinimalShapes(t *testing.T) {
	tests := []struct {
		name string
		data map[string]interface{}
	}{
		{"nothing assigned", map[string]interface{}{"created": 0, "existing": 0, "skipped": 0, "assignments": []interface{}{}}},
		{"skipped absent", map[string]interface{}{"created": 0, "existing": 0, "assignments": []interface{}{}}},
		{"assignments null", map[string]interface{}{"created": 0, "existing": 0, "assignments": nil}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, http.StatusOK, map[string]interface{}{"data": tt.data})
			})
			defer cleanup()

			res, err := c.Assignments.BulkAssign(context.Background(), BulkAssign{
				ApplicationID: "commerce", ResourceType: "order", ResourceID: "ord_9",
				TagIDs: []string{"tag_1"},
			})
			if err != nil {
				t.Fatalf("BulkAssign: %v", err)
			}
			// Empty and non-nil, so a caller can range over it without a nil check.
			if res.Assignments == nil || len(res.Assignments) != 0 {
				t.Errorf("Assignments = %+v, want an empty non-nil slice", res.Assignments)
			}
		})
	}
}

// Sharper here than on bulk assign: this payload is a single counter, so a
// renamed key decodes to the most common legitimate answer there is.
func TestAssignments_BulkRemove_MissingRemovedIsAnError(t *testing.T) {
	for _, data := range []map[string]interface{}{{}, {"deleted": 2}, {"count": 2}} {
		data := data
		func() {
			c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, http.StatusOK, map[string]interface{}{"data": data})
			})
			defer cleanup()

			res, err := c.Assignments.BulkRemove(context.Background(), BulkRemove{
				ApplicationID: "commerce", ResourceType: "order", ResourceID: "ord_9",
				TagIDs: []string{"tag_1"},
			})
			if err == nil {
				t.Fatalf("body %v: expected an error, got Removed=%d -- 0 is the most common real answer", data, res.Removed)
			}
		}()
	}
}

// A removed count of zero is a real answer and must decode cleanly.
func TestAssignments_BulkRemove_ZeroIsLegal(t *testing.T) {
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]interface{}{"data": map[string]interface{}{"removed": 0}})
	})
	defer cleanup()

	res, err := c.Assignments.BulkRemove(context.Background(), BulkRemove{
		ApplicationID: "commerce", ResourceType: "order", ResourceID: "ord_9",
		TagIDs: []string{"tag_1"},
	})
	if err != nil {
		t.Fatalf("BulkRemove: %v", err)
	}
	if res.Removed != 0 {
		t.Errorf("Removed = %d, want 0", res.Removed)
	}
}

// UnmarshalJSON is exported, so it must fully define the value it decodes into.
// Skipped is the only optional key, which makes it the one field a response
// omitting it could leave carrying a previous count. doData always passes a
// fresh value, so this is unreachable through the service methods -- which is
// the reason to pin it here rather than trust every future caller to know.
func TestBulkAssignResult_UnmarshalIntoAReusedValueIsTotal(t *testing.T) {
	res := BulkAssignResult{Created: 9, Existing: 9, Skipped: 9, Assignments: []Assignment{{ID: "stale"}}}

	if err := json.Unmarshal([]byte(`{"created":1,"existing":2,"assignments":[]}`), &res); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if res.Skipped != 0 {
		t.Errorf("Skipped = %d, want 0: an omitted key must not leave the previous value", res.Skipped)
	}
	if res.Created != 1 || res.Existing != 2 || len(res.Assignments) != 0 {
		t.Errorf("decoded fields did not replace the stale ones: %+v", res)
	}
}

// Decodes a RAW body written with the wire's own field names, rather than one
// marshalled from Assignment itself.
//
// That distinction is the entire point. Every canned fixture built by
// marshalling the struct uses a misspelled json tag for both the write and the
// read, so it round-trips perfectly -- the fixture and the client share the same
// mistake. Verified on main: renaming these tags to
// "namespaceType"/"namespaceID" left the whole unit suite AND the real-server
// smoke test green before this test existed, because nothing anywhere asserted
// the pair was populated.
func TestAssignment_DecodesTheWiresFieldNames(t *testing.T) {
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusCreated, map[string]interface{}{"data": map[string]interface{}{
			"id":             "asg_1",
			"tenant_id":      "tenant-1",
			"application_id": "commerce",
			"namespace_type": "merchant",
			"namespace_id":   "m-42",
			"tag_id":         "tag_1",
			"resource_type":  "order",
			"resource_id":    "ord_9",
			"assigned_by":    "importer",
			"assigned_at":    "2026-06-08T12:00:00Z",
		}})
	})
	defer cleanup()

	got, err := c.Assignments.Create(context.Background(), AssignmentCreate{
		ApplicationID: "commerce", TagID: String("tag_1"),
		ResourceType: "order", ResourceID: "ord_9",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.NamespaceType == nil || *got.NamespaceType != "merchant" {
		t.Errorf("NamespaceType = %v, want merchant", got.NamespaceType)
	}
	if got.NamespaceID == nil || *got.NamespaceID != "m-42" {
		t.Errorf("NamespaceID = %v, want m-42", got.NamespaceID)
	}
	if got.ID != "asg_1" || got.TenantID != "tenant-1" || got.ApplicationID != "commerce" {
		t.Errorf("identity fields did not decode: %+v", got)
	}
	if got.TagID != "tag_1" || got.ResourceType != "order" || got.ResourceID != "ord_9" {
		t.Errorf("link fields did not decode: %+v", got)
	}
	if got.AssignedBy == nil || *got.AssignedBy != "importer" {
		t.Errorf("AssignedBy = %v, want importer", got.AssignedBy)
	}
	if got.AssignedAt.IsZero() {
		t.Error("AssignedAt did not decode")
	}
}

// The composite guards the counters; the row checks guard the ROWS. A null or
// empty row decodes to a zero-valued Assignment inside a result whose counts all
// look right, so nothing a caller checks would give it away.
//
// On this line the row checks are a hand-port of main's decodeResourceArray --
// requireResourceArray, one decode of the typed slice, requireIdentity per row --
// so the identity, type and not-an-array cases are pinned here as well: each is a
// step of that hand-port that could be dropped and still compile.
func TestAssignments_BulkAssign_ARowThatWouldBeZeroValuedIsAnError(t *testing.T) {
	tests := []struct {
		name string
		rows interface{}
		want string
	}{
		{"null row", []interface{}{nil}, "element 0 is null"},
		{"empty row", []interface{}{map[string]interface{}{}}, "element 0 is an empty object"},
		{
			"one good row does not excuse the next",
			[]interface{}{map[string]interface{}{"id": "asg_1"}, nil},
			"element 1 is null",
		},
		{
			"null id",
			[]interface{}{map[string]interface{}{"id": nil, "tag_id": "tag_1"}},
			`element 0 decoded with no "id"`,
		},
		{
			"good row then a renamed id",
			[]interface{}{map[string]interface{}{"id": "asg_1"}, map[string]interface{}{"identifier": "asg_2"}},
			`element 1 decoded with no "id"`,
		},
		// The rows decode as one typed slice, so a type error names the array
		// rather than an element index (main's per-element decode named both).
		{
			"a field of the wrong type",
			[]interface{}{map[string]interface{}{"id": 5}},
			`decode bulk assign "assignments"`,
		},
		{"not an array", map[string]interface{}{"id": "asg_1"}, `decode bulk assign "assignments"`},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, http.StatusOK, map[string]interface{}{"data": map[string]interface{}{
					"created": 1, "existing": 0, "skipped": 0, "assignments": tt.rows,
				}})
			})
			defer cleanup()

			res, err := c.Assignments.BulkAssign(context.Background(), BulkAssign{
				ApplicationID: "commerce", ResourceType: "order", ResourceID: "ord_9",
				TagIDs: []string{"tag_1"},
			})
			if err == nil {
				t.Fatalf("expected an error, got %+v", res)
			}
			if res != nil {
				t.Errorf("BulkAssign returned %+v alongside the error, want nil", res)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error should say %q, got: %v", tt.want, err)
			}
			if !strings.Contains(err.Error(), "assignments") {
				t.Errorf("error should name the array, got: %v", err)
			}
		})
	}
}
