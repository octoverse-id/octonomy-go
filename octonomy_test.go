package octonomy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestClient starts an httptest server with handler and returns a Client
// pointed at it. The server is closed automatically when the test ends.
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	c, err := New(Config{
		BaseURL:  srv.URL,
		Token:    "test-token",
		TenantID: "tenant-1",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// writeJSON is a handler helper for canned responses.
func writeJSON(t *testing.T, w http.ResponseWriter, status int, body any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if body == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

// writeData wraps body in the server's single-resource envelope,
// {"data": {...}}, and writes it. Every Octonomy 2xx that carries a resource
// looks like this -- DELETE is the exception, answering 204 with no body at all.
// Canned handlers that returned the bare object are what let the missing unwrap
// in doData go unnoticed, so single-resource fixtures must go through here and
// not through writeJSON.
func writeData(t *testing.T, w http.ResponseWriter, status int, body any) {
	t.Helper()
	writeJSON(t, w, status, map[string]any{"data": body})
}

func TestNew_Validation(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"valid", Config{BaseURL: "https://octonomy.example.com", Token: "t", TenantID: "acme"}, false},
		{"trailing slash trimmed", Config{BaseURL: "https://octonomy.example.com/", Token: "t", TenantID: "acme"}, false},
		{"missing base url", Config{Token: "t", TenantID: "acme"}, true},
		{"missing token", Config{BaseURL: "https://octonomy.example.com", TenantID: "acme"}, true},
		{"missing tenant", Config{BaseURL: "https://octonomy.example.com", Token: "t"}, true},
		{"relative base url", Config{BaseURL: "octonomy.example.com", Token: "t", TenantID: "acme"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := New(tt.cfg)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got client %+v", c)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c.Tags == nil || c.Vocabularies == nil || c.Aliases == nil || c.Assignments == nil {
				t.Fatal("services not wired")
			}
		})
	}
}

func TestDo_SetsHeadersAndPath(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q, want %q", got, "Bearer test-token")
		}
		if got := r.Header.Get("X-Tenant-ID"); got != "tenant-1" {
			t.Errorf("X-Tenant-ID = %q, want %q", got, "tenant-1")
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q, want application/json", got)
		}
		if got := r.Header.Get("User-Agent"); got != defaultUserAgent {
			t.Errorf("User-Agent = %q, want %q", got, defaultUserAgent)
		}
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		if r.URL.Path != "/api/v2/tags/abc" {
			t.Errorf("path = %q, want /api/v2/tags/abc", r.URL.Path)
		}
		writeData(t, w, http.StatusOK, Tag{ID: "abc"})
	})

	tag, err := c.Tags.Get(context.Background(), "abc")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if tag.ID != "abc" {
		t.Errorf("tag.ID = %q, want abc", tag.ID)
	}
}

func TestDo_ActorHeader(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*Config)
		opts      []RequestOption
		want      string
	}{
		{"none", func(*Config) {}, nil, ""},
		{"config default", func(c *Config) { c.ActorID = "svc-config" }, nil, "svc-config"},
		{"per-call override", func(c *Config) { c.ActorID = "svc-config" }, []RequestOption{WithActor("svc-call")}, "svc-call"},
		{"per-call only", func(*Config) {}, []RequestOption{WithActor("svc-call")}, "svc-call"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.Header.Get("X-Actor-ID"); got != tt.want {
					t.Errorf("X-Actor-ID = %q, want %q", got, tt.want)
				}
				writeData(t, w, http.StatusOK, Tag{ID: "abc"})
			}))
			t.Cleanup(srv.Close)

			cfg := Config{BaseURL: srv.URL, Token: "t", TenantID: "acme"}
			tt.configure(&cfg)
			c, err := New(cfg)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			// Assert the decoded value, not just the absence of an error. A
			// zero-valued Tag also arrives with err == nil -- that is the defect
			// this file exists to pin -- so a test that ignores the result would
			// pass against the very behavior it is meant to exclude.
			tag, err := c.Tags.Get(context.Background(), "abc", tt.opts...)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if tag.ID != "abc" {
				t.Errorf("tag.ID = %q, want abc", tag.ID)
			}
		})
	}
}

func TestDo_ErrorEnvelope(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		code      string
		predicate func(error) bool
	}{
		{"not found", http.StatusNotFound, CodeNotFound, IsNotFound},
		{"conflict", http.StatusConflict, CodeConflict, IsConflict},
		{"validation", http.StatusBadRequest, CodeValidation, IsValidation},
		{"auth", http.StatusUnauthorized, CodeAuthRequired, IsAuthError},
		{"forbidden", http.StatusForbidden, CodeForbidden, IsForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, tt.status, map[string]any{
					"error": map[string]any{
						"code":       tt.code,
						"message":    "boom",
						"details":    map[string]any{"field": "slug"},
						"request_id": "req_123",
					},
				})
			})

			_, err := c.Tags.Get(context.Background(), "missing")
			if err == nil {
				t.Fatal("expected error")
			}
			if !tt.predicate(err) {
				t.Errorf("predicate did not match for code %q: %v", tt.code, err)
			}
			apiErr, ok := AsAPIError(err)
			if !ok {
				t.Fatalf("error is not *APIError: %v", err)
			}
			if apiErr.StatusCode != tt.status {
				t.Errorf("StatusCode = %d, want %d", apiErr.StatusCode, tt.status)
			}
			if apiErr.Code != tt.code {
				t.Errorf("Code = %q, want %q", apiErr.Code, tt.code)
			}
			if apiErr.RequestID != "req_123" {
				t.Errorf("RequestID = %q, want req_123", apiErr.RequestID)
			}
			if apiErr.Details["field"] != "slug" {
				t.Errorf("Details[field] = %v, want slug", apiErr.Details["field"])
			}
		})
	}
}

func TestDo_ErrorFallbackNonEnvelope(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream down"))
	})

	_, err := c.Tags.Get(context.Background(), "abc")
	apiErr, ok := AsAPIError(err)
	if !ok {
		t.Fatalf("error is not *APIError: %v", err)
	}
	if apiErr.StatusCode != http.StatusBadGateway {
		t.Errorf("StatusCode = %d, want 502", apiErr.StatusCode)
	}
	if apiErr.Message != "upstream down" {
		t.Errorf("Message = %q, want %q", apiErr.Message, "upstream down")
	}
	// Asserting Code closes the hole that hid the 404 trap: this test existed
	// throughout, but checked only StatusCode and Message, so the status-derived
	// semantic code it was producing went unobserved. errors_test.go covers the
	// rule; this line stops the original blind spot from reopening.
	if apiErr.Code != CodeUnexpectedStatus {
		t.Errorf("Code = %q, want %q", apiErr.Code, CodeUnexpectedStatus)
	}
}

// --- Response envelope decoding -------------------------------------------
//
// The server wraps every payload under "data", including single resources. The
// vendored spec documents bare objects, every canned fixture matched the spec,
// and so `Create`/`Get`/`Update` returned a zero-valued struct with a nil error
// against a real server for the whole life of the SDK (#32). These tests pin the
// server's real shape and make each silent-zero path an error.

// The bare object the vendored spec describes must FAIL, not pass. This is the
// regression: it is the exact body every fixture used to send.
func TestDoData_MissingEnvelopeIsAnError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, Tag{ID: "abc", Name: "Featured"})
	})

	tag, err := c.Tags.Get(context.Background(), "abc")
	if err == nil {
		t.Fatalf("expected an error for an unwrapped body, got tag %+v", tag)
	}
	if tag != nil {
		t.Errorf("tag = %+v, want nil alongside the error", tag)
	}
	if !strings.Contains(err.Error(), `"data"`) {
		t.Errorf("error should name the missing envelope, got: %v", err)
	}
}

// An explicit JSON null under "data" is the same failure as an absent key: there
// is no resource to return, so returning a zero-valued one would lie.
func TestDoData_NullEnvelopeIsAnError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]any{"data": nil})
	})

	if _, err := c.Tags.Get(context.Background(), "abc"); err == nil {
		t.Fatal("expected an error for a null data envelope")
	}
}

// A 2xx with no body at all, where a resource was expected.
func TestDoData_EmptyBodyIsAnError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	if _, err := c.Tags.Get(context.Background(), "abc"); err == nil {
		t.Fatal("expected an error for an empty 2xx body")
	}
}

// The two remaining decode failures: a body that is not JSON at all, and a
// "data" whose contents do not fit the resource type.
func TestDoData_UndecodableBodies(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"not json", `<html>proxy error</html>`},
		{"data is not an object", `{"data": 42}`},
		{"data is an array", `{"data": [{"id":"abc"}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := tt.body
			c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(body))
			})

			tag, err := c.Tags.Get(context.Background(), "abc")
			if err == nil {
				t.Fatalf("expected an error, got tag %+v", tag)
			}
			if tag != nil {
				t.Errorf("tag = %+v, want nil alongside the error", tag)
			}
		})
	}
}

// The list path has the same silent-zero trap one type further out: decoding
// straight into a *List[Tag] turns an empty body, a {}, a renamed data key, or an
// unusable pagination block into "this tenant has no tags" with a nil error. A
// caller cannot tell any of those from a real empty page, so each is an error.
func TestDoList_RejectsBodiesThatWouldLookLikeAnEmptyPage(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"empty body", ""},
		{"not json", `<html>proxy error</html>`},
		{"empty object", `{}`},
		{"pagination without data", `{"pagination":{"limit":50,"offset":0,"count":0}}`},
		{"data key renamed", `{"items":[],"pagination":{"limit":50}}`},
		// A zero-valued Pagination reads as "one page, nothing after it" to a
		// caller looping on Count, so the block is required -- and required to be
		// usable, not merely present. Limit is the field that can carry that
		// check: the server's paginator never emits Limit < 1, while Count and
		// Offset are legitimately 0 on an empty first page.
		{"data without pagination", `{"data":[]}`},
		{"null pagination", `{"data":[],"pagination":null}`},
		{"empty pagination block", `{"data":[],"pagination":{}}`},
		{"pagination limit zero", `{"data":[],"pagination":{"limit":0,"offset":0,"count":0}}`},
		{"pagination is not an object", `{"data":[],"pagination":7}`},
		{"data is not an array", `{"data":{"id":"tag_1"},"pagination":{"limit":50}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := tt.body
			c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(body))
			})

			page, err := c.Tags.List(context.Background(), nil)
			if err == nil {
				t.Fatalf("expected an error, got page %+v", page)
			}
			if page != nil {
				t.Errorf("page = %+v, want nil alongside the error", page)
			}
		})
	}
}

// The converse: a real empty page must still succeed. An over-eager envelope
// check that rejected "data": [] would break every caller paging past the end.
//
// Both wire forms of an empty page normalize to the same value. The server sends
// [], but "data": null is reachable, and a caller should not have to handle two
// spellings of "no rows" -- so both yield an empty non-nil slice.
func TestDoList_EmptyPageIsNotAnError(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"data is an empty array", `{"data":[],"pagination":{"limit":50,"offset":0,"count":0}}`},
		{"data is null", `{"data":null,"pagination":{"limit":50,"offset":0,"count":0}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := tt.body
			c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(body))
			})

			page, err := c.Tags.List(context.Background(), nil)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if page == nil {
				t.Fatal("page = nil, want an empty page")
			}
			if page.Data == nil {
				t.Error("page.Data = nil, want an empty non-nil slice")
			}
			if len(page.Data) != 0 {
				t.Errorf("len(page.Data) = %d, want 0", len(page.Data))
			}
			if page.Pagination.Limit != 50 {
				t.Errorf("pagination did not decode: %+v", page.Pagination)
			}
		})
	}
}

// DELETE is the one path that legitimately answers 204 with no body, so it must
// stay working while the decoding paths tightened around it.
func TestDo_DeleteAcceptsEmpty204(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	if err := c.Tags.Delete(context.Background(), "tag_1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

// ...and only that shape. A 2xx carrying a payload means either the server
// changed its DELETE contract or the method was routed through the wrong helper;
// both must be loud, because `do` discards whatever it is handed.
func TestDo_RejectsA2xxThatCarriesAPayload(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeData(t, w, http.StatusOK, Tag{ID: "tag_1"})
	})

	err := c.Tags.Delete(context.Background(), "tag_1")
	if err == nil {
		t.Fatal("expected an error for a 200 with a payload")
	}
	if !strings.Contains(err.Error(), "204") {
		t.Errorf("error should name the expected status, got: %v", err)
	}
}

// Every helper propagates a non-2xx as *APIError rather than swallowing it into
// its own decode failure. doData is covered by TestDo_ErrorEnvelope above; these
// are the other two.
func TestTransport_ErrorsPropagateFromEveryHelper(t *testing.T) {
	handler := func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusForbidden, map[string]any{
			"error": map[string]any{"code": CodeForbidden, "message": "insufficient scope"},
		})
	}

	t.Run("doList", func(t *testing.T) {
		c := newTestClient(t, handler)
		page, err := c.Tags.List(context.Background(), nil)
		if !IsForbidden(err) {
			t.Fatalf("expected forbidden, got %v", err)
		}
		if page != nil {
			t.Errorf("page = %+v, want nil", page)
		}
	})

	t.Run("do", func(t *testing.T) {
		c := newTestClient(t, handler)
		if err := c.Tags.Delete(context.Background(), "tag_1"); !IsForbidden(err) {
			t.Fatalf("expected forbidden, got %v", err)
		}
	})
}
