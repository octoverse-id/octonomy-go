package octonomy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
			if c.Tags == nil || c.Vocabularies == nil {
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
		if r.URL.Path != "/api/v1/tags/abc" {
			t.Errorf("path = %q, want /api/v1/tags/abc", r.URL.Path)
		}
		writeJSON(t, w, http.StatusOK, Tag{ID: "abc"})
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
				writeJSON(t, w, http.StatusOK, Tag{ID: "abc"})
			}))
			t.Cleanup(srv.Close)

			cfg := Config{BaseURL: srv.URL, Token: "t", TenantID: "acme"}
			tt.configure(&cfg)
			c, err := New(cfg)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if _, err := c.Tags.Get(context.Background(), "abc", tt.opts...); err != nil {
				t.Fatalf("Get: %v", err)
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
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
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
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
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
}
