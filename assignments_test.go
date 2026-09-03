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

// decodeBody reads a request body as a generic map. Assignments put everything
// in the body -- including on DELETE -- so nearly every test here needs it.
//
// Errorf, never Fatalf: this runs on the httptest server's goroutine, and
// Fatalf there calls runtime.Goexit on the handler rather than the test,
// aborting the connection and burying the real failure under a client-side EOF.
// A nil map is safe to return for the same reason -- reads from it yield zero
// values, so the caller's own assertions still run and report against the
// failure this already named.
func decodeBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		t.Errorf("read body: %v", err)
		return nil
	}
	var in map[string]any
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Errorf("decode body %q: %v", raw, err)
		return nil
	}
	return in
}

func TestAssignments_Create(t *testing.T) {
	assigned := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v2/tag-assignments" {
			t.Errorf("got %s %s, want POST /api/v2/tag-assignments", r.Method, r.URL.Path)
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
		t.Run(http.StatusText(status), func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				writeData(t, w, status, Assignment{
					ID: "asg_1", ApplicationID: "commerce", TagID: "tag_1",
					ResourceType: "order", ResourceID: "ord_9",
				})
			})

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
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
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
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, http.StatusBadRequest, map[string]any{
					"error": map[string]any{"code": tt.code, "message": "nope"},
				})
			})

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
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusCreated, Assignment{ID: "asg_1", TagID: "tag_1"})
	})

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
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/v2/tag-assignments" {
			t.Errorf("got %s %s, want DELETE /api/v2/tag-assignments", r.Method, r.URL.Path)
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
	c := newUnreachableClient(t, APIV2)

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
}

func TestAssignments_BulkAssign(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v2/tag-assignments/bulk-assign" {
			t.Errorf("got %s %s, want POST /api/v2/tag-assignments/bulk-assign", r.Method, r.URL.Path)
		}
		in := decodeBody(t, r)
		tagIDs, _ := in["tag_ids"].([]any)
		slugs, _ := in["alias_slugs"].([]any)
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
		// claims. writeData supplies the envelope.
		writeData(t, w, http.StatusOK, BulkAssignResult{
			Created: 2, Existing: 1, Skipped: 0,
			Assignments: []Assignment{
				{ID: "asg_1", TagID: "tag_1", ApplicationID: "commerce"},
				{ID: "asg_2", TagID: "tag_2", ApplicationID: "commerce"},
				{ID: "asg_3", TagID: "tag_3", ApplicationID: "commerce"},
			},
		})
	})

	res, err := c.Assignments.BulkAssign(context.Background(), BulkAssign{
		ApplicationID: "commerce", ResourceType: "order", ResourceID: "ord_9",
		TagIDs: []string{"tag_1", "tag_2"}, AliasSlugs: []string{"sale"},
		AssignedBy: String("importer"),
	})
	if err != nil {
		t.Fatalf("BulkAssign: %v", err)
	}
	if res.Created != 2 || res.Existing != 1 || res.Skipped != 0 {
		t.Errorf("counts did not round-trip: %+v", res)
	}
	if len(res.Assignments) != 3 || res.Assignments[0].ID != "asg_1" {
		t.Errorf("assignments did not round-trip: %+v", res.Assignments)
	}
}

// THE #32 TEST FOR THIS RESOURCE. docs/openapi-v2.yaml claims bulk-assign
// returns a bare array of Assignment. It does not -- and a client written from
// the spec would decode this endpoint into a []Assignment, which against the
// real composite body yields an empty slice and a nil error. Both spellings of
// the spec's claim must be errors here, never an empty result.
func TestAssignments_BulkAssign_TheSpecsBareArrayIsAnError(t *testing.T) {
	tests := []struct {
		name string
		body any
	}{
		{
			name: "bare array, exactly as the spec describes it",
			body: []Assignment{{ID: "asg_1", TagID: "tag_1"}},
		},
		{
			name: "array under the data envelope",
			body: map[string]any{"data": []Assignment{{ID: "asg_1", TagID: "tag_1"}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, http.StatusOK, tt.body)
			})

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
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusBadRequest, map[string]any{
			"error": map[string]any{
				"code":    CodeValidation,
				"message": "Request validation failed.",
				"details": map[string]any{"tag_ids": []string{"Unknown tag ids: tag_x"}},
			},
		})
	})

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
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		// A POST, not a DELETE -- the only remove in this SDK that is.
		if r.Method != http.MethodPost || r.URL.Path != "/api/v2/tag-assignments/bulk-remove" {
			t.Errorf("got %s %s, want POST /api/v2/tag-assignments/bulk-remove", r.Method, r.URL.Path)
		}
		in := decodeBody(t, r)
		if tagIDs, _ := in["tag_ids"].([]any); len(tagIDs) != 2 {
			t.Errorf("tag_ids = %v, want two ids", in["tag_ids"])
		}
		if _, ok := in["alias_slugs"]; ok {
			t.Errorf("bulk remove has no alias form, got: %v", in)
		}
		writeData(t, w, http.StatusOK, BulkRemoveResult{Removed: 2})
	})

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

// The spec documents bulk-remove's 200 with NO schema at all, so there is
// nothing to write a client against. An unenveloped body must be an error rather
// than a Removed of 0, which a caller would read as "nothing matched".
func TestAssignments_BulkRemove_UnenvelopedBodyIsAnError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]any{"removed": 2})
	})

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
// application stays in the body.
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
				// The application lives in the body on every one of these, so
				// nothing should be adding it to the query.
				if r.URL.RawQuery != "" {
					t.Errorf("expected no query params, got %q", r.URL.RawQuery)
				}
				tt.reply(t, w)
			})
			if err := tt.call(c); err != nil {
				t.Fatalf("%s: %v", tt.name, err)
			}
		})
	}
}

func TestAssignments_OnV1(t *testing.T) {
	c := newVersionedTestClient(t, APIV1, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/tag-assignments" {
			t.Errorf("path = %q, want /api/v1/tag-assignments", r.URL.Path)
		}
		writeData(t, w, http.StatusCreated, Assignment{
			ID: "asg_1", ApplicationID: "commerce", TagID: "tag_1",
			ResourceType: "order", ResourceID: "ord_9",
		})
	})

	got, err := c.Assignments.Create(context.Background(), AssignmentCreate{
		ApplicationID: "commerce", TagID: String("tag_1"),
		ResourceType: "order", ResourceID: "ord_9",
	})
	if err != nil {
		t.Fatalf("Create on v1: %v", err)
	}
	// v1 responses carry no namespace fields, so they stay nil rather than "".
	if got.NamespaceType != nil || got.NamespaceID != nil {
		t.Errorf("v1 assignment reported a namespace: %+v", got)
	}
}

// doData stops at the data envelope, which is right for a resource: a
// zero-valued Assignment has an empty ID and no caller mistakes it for an
// answer. A composite of counters is different -- created:0 existing:0 with no
// rows is an ordinary result -- so a renamed or missing key must be an error
// rather than "the tags were all already there".
func TestAssignments_BulkAssign_MissingKeysAreErrors(t *testing.T) {
	tests := []struct {
		name string
		data any
	}{
		{"empty object", map[string]any{}},
		{"renamed keys", map[string]any{"results": []any{}, "count": 1}},
		{"no assignments array", map[string]any{"created": 1, "existing": 0, "skipped": 0}},
		{"no created count", map[string]any{"existing": 0, "skipped": 0, "assignments": []any{}}},
		{"no existing count", map[string]any{"created": 1, "skipped": 0, "assignments": []any{}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, http.StatusOK, map[string]any{"data": tt.data})
			})

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
		data map[string]any
	}{
		{"nothing assigned", map[string]any{"created": 0, "existing": 0, "skipped": 0, "assignments": []any{}}},
		{"skipped absent", map[string]any{"created": 0, "existing": 0, "assignments": []any{}}},
		{"assignments null", map[string]any{"created": 0, "existing": 0, "assignments": nil}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, http.StatusOK, map[string]any{"data": tt.data})
			})

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
	for _, data := range []map[string]any{{}, {"deleted": 2}, {"count": 2}} {
		c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, http.StatusOK, map[string]any{"data": data})
		})

		res, err := c.Assignments.BulkRemove(context.Background(), BulkRemove{
			ApplicationID: "commerce", ResourceType: "order", ResourceID: "ord_9",
			TagIDs: []string{"tag_1"},
		})
		if err == nil {
			t.Fatalf("body %v: expected an error, got Removed=%d -- 0 is the most common real answer", data, res.Removed)
		}
	}
}

// A removed count of zero is a real answer and must decode cleanly.
func TestAssignments_BulkRemove_ZeroIsLegal(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]any{"data": map[string]any{"removed": 0}})
	})

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
