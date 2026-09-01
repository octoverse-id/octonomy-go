package octonomy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// newVersionedTestClient starts an httptest server and returns a Client pinned to
// version.
func newVersionedTestClient(t *testing.T, version APIVersion, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	c, err := New(Config{
		BaseURL:    srv.URL,
		Token:      "test-token",
		TenantID:   "tenant-1",
		APIVersion: version,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// newUnreachableClient returns a Client pinned to version whose server fails the
// test if it is ever reached.
//
// Every guard in checkScopeCoherence promises to reject BEFORE issuing a request
// -- that is most of what the guards are worth, since a request that reaches the
// server has already spent the round trip the guard exists to save. Asserting
// only that an error came back would pass just as happily if the error arrived
// from the server, so the assertion is here in the handler.
func newUnreachableClient(t *testing.T, version APIVersion) *Client {
	t.Helper()
	return newVersionedTestClient(t, version, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("guard did not fire: the client issued %s %s instead of failing locally", r.Method, r.URL)
		writeData(t, w, http.StatusOK, Tag{ID: "abc"})
	})
}

func TestNew_APIVersion(t *testing.T) {
	tests := []struct {
		name    string
		version APIVersion
		want    APIVersion
		wantErr bool
	}{
		{"empty defaults to v2", "", APIV2, false},
		{"explicit v2", APIV2, APIV2, false},
		{"explicit v1", APIV1, APIV1, false},
		{"unknown version", APIVersion("v3"), "", true},
		{"prefixed value", APIVersion("/api/v2"), "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := New(Config{
				BaseURL:    "https://octonomy.example.com",
				Token:      "t",
				TenantID:   "acme",
				APIVersion: tt.version,
			})
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got client targeting %s", c.APIVersion())
				}
				return
			}
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if c.APIVersion() != tt.want {
				t.Errorf("APIVersion() = %q, want %q", c.APIVersion(), tt.want)
			}
		})
	}
}

// The default is v2, and that is a wire-level change from the 0.1.x line. Pin it
// here so flipping it back is a deliberate edit to a test that says so.
func TestAPIVersion_SelectsThePathPrefix(t *testing.T) {
	tests := []struct {
		name    string
		version APIVersion
		want    string
	}{
		{"default", "", "/api/v2/tags/abc"},
		{"v2", APIV2, "/api/v2/tags/abc"},
		{"v1", APIV1, "/api/v1/tags/abc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := tt.want
			c := newVersionedTestClient(t, tt.version, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != want {
					t.Errorf("path = %q, want %q", r.URL.Path, want)
				}
				writeData(t, w, http.StatusOK, Tag{ID: "abc"})
			})
			if _, err := c.Tags.Get(context.Background(), "abc"); err != nil {
				t.Fatalf("Get: %v", err)
			}
		})
	}
}

func TestWithNamespace_SendsTheHeaderPair(t *testing.T) {
	c := newVersionedTestClient(t, APIV2, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get(namespaceTypeHeader); got != "merchant" {
			t.Errorf("%s = %q, want merchant", namespaceTypeHeader, got)
		}
		if got := r.Header.Get(namespaceIDHeader); got != "acme-store" {
			t.Errorf("%s = %q, want acme-store", namespaceIDHeader, got)
		}
		if got := r.URL.Query().Get(applicationIDParam); got != "shop" {
			t.Errorf("%s = %q, want shop", applicationIDParam, got)
		}
		writeData(t, w, http.StatusOK, Tag{
			ID:            "abc",
			NamespaceType: String("merchant"),
			NamespaceID:   String("acme-store"),
		})
	})

	tag, err := c.Tags.Get(context.Background(), "abc",
		WithNamespace("merchant", "acme-store"), WithApplication("shop"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// The namespace fields are new on the response models; a decode that dropped
	// them would leave nil here and look exactly like a global row.
	if tag.NamespaceType == nil || *tag.NamespaceType != "merchant" {
		t.Errorf("tag.NamespaceType = %v, want merchant", tag.NamespaceType)
	}
	if tag.NamespaceID == nil || *tag.NamespaceID != "acme-store" {
		t.Errorf("tag.NamespaceID = %v, want acme-store", tag.NamespaceID)
	}
}

// A global request sends NO namespace headers -- that absence is how the server
// selects the tenant-shared namespace, so sending an empty pair would be a
// different (and rejected) request.
func TestNamespaceHeaders_AbsentWhenNotScoped(t *testing.T) {
	tests := []struct {
		name string
		opts []RequestOption
	}{
		{"no option", nil},
		{"explicit global", []RequestOption{WithGlobalNamespace()}},
		{"global cancels an earlier namespace", []RequestOption{
			WithNamespace("merchant", "acme-store"), WithGlobalNamespace(),
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newVersionedTestClient(t, APIV2, func(w http.ResponseWriter, r *http.Request) {
				for _, header := range []string{namespaceTypeHeader, namespaceIDHeader} {
					if _, ok := r.Header[http.CanonicalHeaderKey(header)]; ok {
						t.Errorf("%s was sent on a global request: %q", header, r.Header.Get(header))
					}
				}
				writeData(t, w, http.StatusOK, Tag{ID: "abc"})
			})
			if _, err := c.Tags.Get(context.Background(), "abc", tt.opts...); err != nil {
				t.Fatalf("Get: %v", err)
			}
		})
	}
}

// Every guard case fails locally, with no HTTP request issued.
func TestScopeGuards_RejectBeforeSendingAnything(t *testing.T) {
	tests := []struct {
		name    string
		version APIVersion
		call    func(*Client) error
		wantIn  string
	}{
		{
			name:    "namespace on v1",
			version: APIV1,
			call: func(c *Client) error {
				_, err := c.Tags.Get(context.Background(), "abc", WithNamespace("merchant", "m1"), WithApplication("shop"))
				return err
			},
			wantIn: "Config.APIVersion = APIV2",
		},
		{
			// The scope param's own contradiction. It lives with the other guards
			// rather than only in resolution_test.go because checkScopeCoherence
			// owns it, and because the server resolves this one silently in favor
			// of the scope instead of rejecting it.
			name:    "include_global alongside scope=merchant",
			version: APIV2,
			call: func(c *Client) error {
				_, err := c.Tags.Resolve(context.Background(), "sale",
					&TagResolveParams{Scope: ResolutionScopeMerchant, ApplicationID: String("shop")},
					WithNamespace("merchant", "m1"), WithIncludeGlobal())
				return err
			},
			wantIn: "contradicts scope=merchant",
		},
		{
			name:    "reserved global namespace type",
			version: APIV2,
			call: func(c *Client) error {
				_, err := c.Tags.Get(context.Background(), "abc", WithNamespace("global", "anything"))
				return err
			},
			wantIn: "reserved namespace type",
		},
		{
			name:    "namespace type without id",
			version: APIV2,
			call: func(c *Client) error {
				_, err := c.Tags.Get(context.Background(), "abc", WithNamespace("merchant", ""))
				return err
			},
			wantIn: "namespace id is required",
		},
		{
			name:    "namespace id without type",
			version: APIV2,
			call: func(c *Client) error {
				_, err := c.Tags.Get(context.Background(), "abc", WithNamespace("", "m1"))
				return err
			},
			wantIn: "namespace type is required",
		},
		{
			name:    "blank application id",
			version: APIV2,
			call: func(c *Client) error {
				_, err := c.Tags.Get(context.Background(), "abc", WithApplication("  "))
				return err
			},
			wantIn: "application id is required",
		},
		{
			name:    "namespaced read without an application",
			version: APIV2,
			call: func(c *Client) error {
				_, err := c.Tags.Get(context.Background(), "abc", WithNamespace("merchant", "m1"))
				return err
			},
			wantIn: "WithApplication",
		},
		{
			name:    "namespaced list without an application",
			version: APIV2,
			call: func(c *Client) error {
				_, err := c.Tags.List(context.Background(), nil, WithNamespace("merchant", "m1"))
				return err
			},
			wantIn: "WithApplication",
		},
		{
			// A bodyless DELETE is the case where the query string is the WHOLE
			// request: Tags.Delete has no params struct and no body, so
			// WithApplication is the only way to name an application. An earlier
			// version of this guard keyed on "is it a read" and let this through
			// to a server 403 -- found by Codex review of #7.
			name:    "namespaced delete without an application",
			version: APIV2,
			call: func(c *Client) error {
				return c.Tags.Delete(context.Background(), "abc", WithNamespace("merchant", "m1"))
			},
			wantIn: "WithApplication",
		},
		{
			// The query application_id is not authoritative on a write: probed
			// against 3.1.0, it persists on a namespaced create and is DROPPED
			// on a global one, so honoring this option would silently produce a
			// tenant-shared row for a caller who asked for application scope.
			// Found by Codex review of #7 (its only P1).
			name:    "WithApplication on a create",
			version: APIV2,
			call: func(c *Client) error {
				_, err := c.Tags.Create(context.Background(), TagCreate{Name: "N", Slug: "n", Type: "topic"}, WithApplication("shop"))
				return err
			},
			wantIn: "ApplicationID field of the request body",
		},
		{
			name:    "WithApplication on an update",
			version: APIV2,
			call: func(c *Client) error {
				_, err := c.Tags.Update(context.Background(), "abc", TagUpdate{Name: String("N")}, WithApplication("shop"))
				return err
			},
			wantIn: "ApplicationID field of the request body",
		},
		{
			name:    "include_global on a write",
			version: APIV2,
			call: func(c *Client) error {
				_, err := c.Tags.Create(context.Background(), TagCreate{Name: "N", Slug: "n", Type: "topic"}, WithIncludeGlobal())
				return err
			},
			wantIn: "reads only",
		},
		{
			name:    "include_global on a delete",
			version: APIV2,
			call: func(c *Client) error {
				return c.Tags.Delete(context.Background(), "abc", WithIncludeGlobal())
			},
			wantIn: "reads only",
		},
		{
			name:    "include_global on v1",
			version: APIV1,
			call: func(c *Client) error {
				_, err := c.Tags.Get(context.Background(), "abc", WithIncludeGlobal())
				return err
			},
			wantIn: "requires the v2 API surface",
		},
		{
			name:    "contradictory application ids",
			version: APIV2,
			call: func(c *Client) error {
				_, err := c.Tags.List(context.Background(),
					&TagListParams{ApplicationID: String("shop")}, WithApplication("warehouse"))
				return err
			},
			wantIn: "contradicts",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call(newUnreachableClient(t, tt.version))
			if err == nil {
				t.Fatal("expected a client-side error")
			}
			if !strings.Contains(err.Error(), tt.wantIn) {
				t.Errorf("error should mention %q, got: %v", tt.wantIn, err)
			}
			// A guard failure is local, so it must not masquerade as a server
			// response a caller could branch on with IsValidation and friends.
			if apiErr, ok := AsAPIError(err); ok {
				t.Errorf("guard error surfaced as *APIError (%v); it never reached the server", apiErr)
			}
		})
	}
}

// The first bad option wins, so a later valid one cannot mask it.
func TestScopeGuards_ReportTheFirstFailure(t *testing.T) {
	c := newUnreachableClient(t, APIV2)
	_, err := c.Tags.Get(context.Background(), "abc",
		WithNamespace("global", "m1"),       // reserved type
		WithApplication(""),                 // also invalid
		WithNamespace("merchant", "m-good"), // valid, applied after both
	)
	if err == nil {
		t.Fatal("expected a client-side error")
	}
	if !strings.Contains(err.Error(), "reserved namespace type") {
		t.Errorf("want the first failure reported, got: %v", err)
	}
}

// ...and a LATER stage cannot mask it either. mergeQuery raises contradictions
// of its own, and running before the recorded failure was reported meant the
// caller got remediation aimed at the wrong argument: here, "B contradicts A"
// instead of "the blank id is not an application". Found by Codex review of #7.
func TestScopeGuards_MergeContradictionDoesNotMaskAnOptionFailure(t *testing.T) {
	c := newUnreachableClient(t, APIV2)
	_, err := c.Tags.List(context.Background(),
		&TagListParams{ApplicationID: String("A")},
		WithApplication(""),  // first failure: a blank id is not an application
		WithApplication("B"), // valid, and contradicts the params value at merge time
	)
	if err == nil {
		t.Fatal("expected a client-side error")
	}
	if !strings.Contains(err.Error(), "application id is required") {
		t.Errorf("want the first recorded failure, got the merge-stage one: %v", err)
	}
}

func TestWithIncludeGlobal_ReachesTheQueryString(t *testing.T) {
	// The server documents include_global on every safe method, detail reads
	// included -- which is why it is a RequestOption rather than a *ListParams
	// field: Get has no params struct to put it in.
	t.Run("detail read", func(t *testing.T) {
		c := newVersionedTestClient(t, APIV2, func(w http.ResponseWriter, r *http.Request) {
			if got := r.URL.Query().Get(includeGlobalParam); got != "true" {
				t.Errorf("%s = %q, want true", includeGlobalParam, got)
			}
			writeData(t, w, http.StatusOK, Tag{ID: "abc"})
		})
		if _, err := c.Tags.Get(context.Background(), "abc",
			WithNamespace("merchant", "m1"), WithApplication("shop"), WithIncludeGlobal()); err != nil {
			t.Fatalf("Get: %v", err)
		}
	})

	t.Run("list read keeps the params", func(t *testing.T) {
		c := newVersionedTestClient(t, APIV2, func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			if got := q.Get(includeGlobalParam); got != "true" {
				t.Errorf("%s = %q, want true", includeGlobalParam, got)
			}
			if got := q.Get("type"); got != "topic" {
				t.Errorf("type = %q, want topic (option merge dropped a params field)", got)
			}
			if got := q.Get(applicationIDParam); got != "shop" {
				t.Errorf("%s = %q, want shop", applicationIDParam, got)
			}
			writeJSON(t, w, http.StatusOK, map[string]any{
				"data":       []Tag{},
				"pagination": map[string]any{"limit": 50},
			})
		})
		params := &TagListParams{ApplicationID: String("shop"), Type: String("topic")}
		if _, err := c.Tags.List(context.Background(), params,
			WithNamespace("merchant", "m1"), WithIncludeGlobal()); err != nil {
			t.Fatalf("List: %v", err)
		}
	})
}

// Merging option-contributed params must not write into the caller's map.
func TestMergeQuery_DoesNotMutateTheCallersParams(t *testing.T) {
	c := newVersionedTestClient(t, APIV2, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]any{
			"data":       []Tag{},
			"pagination": map[string]any{"limit": 50},
		})
	})

	params := &TagListParams{ApplicationID: String("shop")}
	before := params.query()
	if _, err := c.Tags.List(context.Background(), params,
		WithNamespace("merchant", "m1"), WithIncludeGlobal()); err != nil {
		t.Fatalf("List: %v", err)
	}
	after := params.query()
	if before.Encode() != after.Encode() {
		t.Errorf("params.query() changed across a call: %q then %q", before.Encode(), after.Encode())
	}
	if after.Has(includeGlobalParam) {
		t.Errorf("option-contributed %s leaked into the caller's params", includeGlobalParam)
	}
}

// scope=global is a LEGAL explicit pin on tag resolution, even though "global"
// is a reserved namespace TYPE. The two rules live on different parameters and a
// flat "reject global" would break a valid call.
//
// Tag resolution itself is #9; these exercise the transport rule through a query
// the resource layer will build there.
func TestScopeParam_MerchantNeedsANamespaceButGlobalDoesNot(t *testing.T) {
	t.Run("scope=merchant without a namespace is refused locally", func(t *testing.T) {
		c := newUnreachableClient(t, APIV2)
		_, _, err := c.doRaw(context.Background(), http.MethodGet, "/tag-resolution",
			mustQuery("slug", "sale", scopeParam, scopeMerchantValue), nil)
		if err == nil {
			t.Fatal("expected a client-side error")
		}
		if !strings.Contains(err.Error(), "WithNamespace") {
			t.Errorf("error should name the fix, got: %v", err)
		}
	})

	t.Run("scope=global is sent unchanged", func(t *testing.T) {
		c := newVersionedTestClient(t, APIV2, func(w http.ResponseWriter, r *http.Request) {
			if got := r.URL.Query().Get(scopeParam); got != "global" {
				t.Errorf("%s = %q, want global", scopeParam, got)
			}
			writeData(t, w, http.StatusOK, map[string]any{"matched_type": "tag"})
		})
		_, _, err := c.doRaw(context.Background(), http.MethodGet, "/tag-resolution",
			mustQuery("slug", "sale", scopeParam, "global"), nil)
		if err != nil {
			t.Fatalf("doRaw: %v", err)
		}
	})

	t.Run("scope=merchant with a namespace is sent unchanged", func(t *testing.T) {
		c := newVersionedTestClient(t, APIV2, func(w http.ResponseWriter, r *http.Request) {
			if got := r.URL.Query().Get(scopeParam); got != scopeMerchantValue {
				t.Errorf("%s = %q, want %s", scopeParam, got, scopeMerchantValue)
			}
			writeData(t, w, http.StatusOK, map[string]any{"matched_type": "tag"})
		})
		_, _, err := c.doRaw(context.Background(), http.MethodGet, "/tag-resolution",
			mustQuery("slug", "sale", scopeParam, scopeMerchantValue), nil,
			WithNamespace("merchant", "m1"), WithApplication("shop"))
		if err != nil {
			t.Fatalf("doRaw: %v", err)
		}
	})
}

// One Client, N goroutines, N different namespaces. The scoping options are
// per-call values rather than client state, and this is what says so: a
// namespace stored on the Client would leak across these goroutines, and the
// race detector would not necessarily catch it -- the wrong header is not a data
// race, just a wrong answer.
func TestNamespace_IsPerRequestUnderConcurrency(t *testing.T) {
	const goroutines = 24

	// The handler correlates without shared state: each request carries
	// application_id "app-<n>" and namespace id "ns-<n>", so a crossed pair is
	// visible from the request alone.
	c := newVersionedTestClient(t, APIV2, func(w http.ResponseWriter, r *http.Request) {
		app := r.URL.Query().Get(applicationIDParam)
		wantNS := strings.Replace(app, "app-", "ns-", 1)
		if got := r.Header.Get(namespaceIDHeader); got != wantNS {
			t.Errorf("%s = %q for %s=%q, want %q: scope crossed between requests",
				namespaceIDHeader, got, applicationIDParam, app, wantNS)
		}
		writeData(t, w, http.StatusOK, Tag{ID: "abc", NamespaceID: String(r.Header.Get(namespaceIDHeader))})
	})

	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			wantNS := fmt.Sprintf("ns-%d", i)
			tag, err := c.Tags.Get(context.Background(), "abc",
				WithNamespace("merchant", wantNS),
				WithApplication(fmt.Sprintf("app-%d", i)))
			if err != nil {
				t.Errorf("Get: %v", err)
				return
			}
			if tag.NamespaceID == nil || *tag.NamespaceID != wantNS {
				t.Errorf("round-tripped namespace = %v, want %q", tag.NamespaceID, wantNS)
			}
		}()
	}
	wg.Wait()
}

// mustQuery builds url.Values from alternating key/value pairs.
func mustQuery(pairs ...string) url.Values {
	q := url.Values{}
	for i := 0; i+1 < len(pairs); i += 2 {
		q[pairs[i]] = []string{pairs[i+1]}
	}
	return q
}

// --- Repeated scope options ----------------------------------------------
//
// Codex review of #7 caught this: WithApplication documented that a
// contradictory scope is refused, but only enforced it against a *ListParams
// field. Option-versus-option fell through to last-wins -- and on Get and
// Delete, which have no params struct, that was the ONLY way to set application
// scope, so the documented promise held nowhere those methods could reach.
//
// The namespace axis had the same hole and is the one that matters more: a
// silent last-wins there is a cross-merchant read, which is the exact failure
// the no-Config-namespace design exists to prevent.

func TestRepeatedScopeOptions_ContradictionIsAnError(t *testing.T) {
	tests := []struct {
		name   string
		opts   []RequestOption
		wantIn string
	}{
		{
			name:   "two different applications",
			opts:   []RequestOption{WithApplication("catalog"), WithApplication("billing")},
			wantIn: "contradicts the application already set",
		},
		{
			name:   "two different namespace ids",
			opts:   []RequestOption{WithNamespace("merchant", "acme"), WithNamespace("merchant", "globex")},
			wantIn: "contradicts the namespace already set",
		},
		{
			name:   "two different namespace types",
			opts:   []RequestOption{WithNamespace("merchant", "acme"), WithNamespace("reseller", "acme")},
			wantIn: "contradicts the namespace already set",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newUnreachableClient(t, APIV2)
			_, err := c.Tags.Get(context.Background(), "abc", tt.opts...)
			if err == nil {
				t.Fatal("expected a client-side error")
			}
			if !strings.Contains(err.Error(), tt.wantIn) {
				t.Errorf("error should mention %q, got: %v", tt.wantIn, err)
			}
		})
	}
}

// ...but a repeat of the SAME value is not a contradiction, and clearing then
// setting is the documented way to change a namespace. Neither may be caught by
// the rule above: a shared []RequestOption that repeats itself is harmless, and
// breaking WithGlobalNamespace's cancel would remove the only legitimate
// override.
func TestRepeatedScopeOptions_IdempotentAndCancelStillWork(t *testing.T) {
	tests := []struct {
		name    string
		opts    []RequestOption
		wantNS  string
		wantApp string
	}{
		{
			name:    "identical repeats",
			opts:    []RequestOption{WithNamespace("merchant", "acme"), WithNamespace("merchant", "acme"), WithApplication("shop"), WithApplication("shop")},
			wantNS:  "acme",
			wantApp: "shop",
		},
		{
			name:    "cancel then set a different namespace",
			opts:    []RequestOption{WithNamespace("merchant", "acme"), WithGlobalNamespace(), WithNamespace("merchant", "globex"), WithApplication("shop")},
			wantNS:  "globex",
			wantApp: "shop",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wantNS, wantApp := tt.wantNS, tt.wantApp
			c := newVersionedTestClient(t, APIV2, func(w http.ResponseWriter, r *http.Request) {
				if got := r.Header.Get(namespaceIDHeader); got != wantNS {
					t.Errorf("%s = %q, want %q", namespaceIDHeader, got, wantNS)
				}
				if got := r.URL.Query().Get(applicationIDParam); got != wantApp {
					t.Errorf("%s = %q, want %q", applicationIDParam, got, wantApp)
				}
				writeData(t, w, http.StatusOK, Tag{ID: "abc"})
			})
			if _, err := c.Tags.Get(context.Background(), "abc", tt.opts...); err != nil {
				t.Fatalf("Get: %v", err)
			}
		})
	}
}

// The guard's boundary is "can the transport see the whole request", not "is it
// a read". These are the two sides of that line, and getting either wrong is a
// user-visible bug: too narrow sends a request the server will refuse, too wide
// blocks a legal call the server would accept.
func TestNamespacedApplicationGuard_BoundaryIsTheBody(t *testing.T) {
	t.Run("bodyless methods are checked locally", func(t *testing.T) {
		// GET and DELETE both carry their application scope in the query alone.
		c := newUnreachableClient(t, APIV2)
		if _, err := c.Tags.Get(context.Background(), "abc", WithNamespace("merchant", "m1")); err == nil {
			t.Error("namespaced Get without an application was not refused")
		}
		if err := c.Tags.Delete(context.Background(), "abc", WithNamespace("merchant", "m1")); err == nil {
			t.Error("namespaced Delete without an application was not refused")
		}
	})

	t.Run("body-carrying writes are left to the server", func(t *testing.T) {
		// TagCreate.ApplicationID is a body field the transport cannot see
		// without reflection, so a namespaced create must still be SENT -- the
		// server answers 403 if the application really is missing. Refusing it
		// here would break every correct namespaced create.
		reached := false
		c := newVersionedTestClient(t, APIV2, func(w http.ResponseWriter, _ *http.Request) {
			reached = true
			writeData(t, w, http.StatusCreated, Tag{ID: "abc"})
		})
		_, err := c.Tags.Create(context.Background(), TagCreate{
			Name: "N", Slug: "n", Type: "topic", ApplicationID: String("shop"),
		}, WithNamespace("merchant", "m1"))
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if !reached {
			t.Error("a namespaced create with a body application was refused locally; the transport cannot see the body and must not guess")
		}
	})

	t.Run("a supplied application satisfies the guard on a delete", func(t *testing.T) {
		c := newVersionedTestClient(t, APIV2, func(w http.ResponseWriter, r *http.Request) {
			if got := r.URL.Query().Get(applicationIDParam); got != "shop" {
				t.Errorf("%s = %q, want shop", applicationIDParam, got)
			}
			w.WriteHeader(http.StatusNoContent)
		})
		if err := c.Tags.Delete(context.Background(), "abc",
			WithNamespace("merchant", "m1"), WithApplication("shop")); err != nil {
			t.Fatalf("Delete: %v", err)
		}
	})
}

// The two application checks ask different questions, so they need different
// tests: presence for the contradiction check, usability for the missing-
// application guard. Sharing one test broke the other -- found by Codex review
// of #7, as fallout from the previous round's fix.
func TestNamespacedApplicationGuard_BlankIsNotAnApplication(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{"empty string", ""},
		{"whitespace only", "   "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newUnreachableClient(t, APIV2)
			_, err := c.Tags.List(context.Background(),
				&TagListParams{ApplicationID: String(tt.value)}, WithNamespace("merchant", "m1"))
			if err == nil {
				t.Fatal("a blank application did not satisfy the guard, but the request was sent anyway")
			}
			if !strings.Contains(err.Error(), "WithApplication") {
				t.Errorf("error should name the fix, got: %v", err)
			}
		})
	}
}

// An application_id explicitly set to "" is PRESENT, not absent. Comparing the
// merged value against the empty string read it as absent and let the option
// overwrite it -- found by Codex review of #7.
func TestMergeQuery_EmptyParamsApplicationStillContradicts(t *testing.T) {
	c := newUnreachableClient(t, APIV2)
	_, err := c.Tags.List(context.Background(),
		&TagListParams{ApplicationID: String("")}, WithApplication("shop"))
	if err == nil {
		t.Fatal("expected a contradiction error")
	}
	if !strings.Contains(err.Error(), "contradicts") {
		t.Errorf("error should name the contradiction, got: %v", err)
	}
}

// WithApplication's domain is exactly the bodyless requests -- the same hasBody
// axis the missing-application guard uses. One concept, two consequences:
// where the query is the whole request the option is the way to scope it, and
// where a body exists the body is authoritative and the option is refused.
func TestWithApplication_AppliesToBodylessRequestsOnly(t *testing.T) {
	t.Run("delete accepts it", func(t *testing.T) {
		c := newVersionedTestClient(t, APIV2, func(w http.ResponseWriter, r *http.Request) {
			if got := r.URL.Query().Get(applicationIDParam); got != "shop" {
				t.Errorf("%s = %q, want shop", applicationIDParam, got)
			}
			w.WriteHeader(http.StatusNoContent)
		})
		if err := c.Tags.Delete(context.Background(), "abc", WithApplication("shop")); err != nil {
			t.Fatalf("Delete: %v", err)
		}
	})

	t.Run("list accepts it", func(t *testing.T) {
		c := newVersionedTestClient(t, APIV2, func(w http.ResponseWriter, r *http.Request) {
			if got := r.URL.Query().Get(applicationIDParam); got != "shop" {
				t.Errorf("%s = %q, want shop", applicationIDParam, got)
			}
			writeJSON(t, w, http.StatusOK, map[string]any{
				"data": []Tag{}, "pagination": map[string]any{"limit": 50},
			})
		})
		if _, err := c.Tags.List(context.Background(), nil, WithApplication("shop")); err != nil {
			t.Fatalf("List: %v", err)
		}
	})

	t.Run("create refuses it and names the body field", func(t *testing.T) {
		c := newUnreachableClient(t, APIV2)
		_, err := c.Vocabularies.Create(context.Background(),
			VocabularyCreate{Name: "N", Slug: "n"}, WithApplication("shop"))
		if err == nil {
			t.Fatal("expected a client-side error")
		}
		if !strings.Contains(err.Error(), "ApplicationID") {
			t.Errorf("error must name the field that actually works, got: %v", err)
		}
	})

	// The body field is untouched by any of this and remains the way to scope a
	// write -- including a namespaced one.
	t.Run("the body field still scopes a create", func(t *testing.T) {
		c := newVersionedTestClient(t, APIV2, func(w http.ResponseWriter, r *http.Request) {
			var got TagCreate
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Errorf("decode body: %v", err)
			}
			if got.ApplicationID == nil || *got.ApplicationID != "shop" {
				t.Errorf("body application_id = %v, want shop", got.ApplicationID)
			}
			writeData(t, w, http.StatusCreated, Tag{ID: "abc", ApplicationID: String("shop")})
		})
		tag, err := c.Tags.Create(context.Background(), TagCreate{
			Name: "N", Slug: "n", Type: "topic", ApplicationID: String("shop"),
		}, WithNamespace("merchant", "m1"))
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if tag.ApplicationID == nil || *tag.ApplicationID != "shop" {
			t.Errorf("tag.ApplicationID = %v, want shop", tag.ApplicationID)
		}
	})
}
