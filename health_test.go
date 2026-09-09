package octonomy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestHealthClient starts an httptest server and returns a CREDENTIAL-FREE
// client pointed at it -- a base URL and nothing else, which is the constructor
// contract #13 exists for.
func newTestHealthClient(t *testing.T, handler http.HandlerFunc) *HealthClient {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	hc, err := NewHealthClient(srv.URL)
	if err != nil {
		t.Fatalf("NewHealthClient: %v", err)
	}
	return hc
}

// assertProbeRequest checks, server-side, everything a probe request must NOT
// carry. The absence of the credential headers is the point of the feature, so
// it is asserted rather than assumed -- and asserted from both entry points
// below, because "the full client also sends none" is the half that can regress.
func assertProbeRequest(t *testing.T, r *http.Request, wantPath string) {
	t.Helper()
	if r.Method != http.MethodGet {
		t.Errorf("method = %s, want GET", r.Method)
	}
	if got := r.URL.EscapedPath(); got != wantPath {
		t.Errorf("path = %q, want %q (the probes are rooted OUTSIDE /api/<version>)", got, wantPath)
	}
	for _, header := range []string{"Authorization", "X-Tenant-ID", "X-Actor-ID", namespaceTypeHeader, namespaceIDHeader} {
		if got := r.Header.Get(header); got != "" {
			t.Errorf("%s = %q, want it absent: the probes authenticate nobody and scope nothing", header, got)
		}
	}
	if got := r.Header.Get("Accept"); got != "application/json" {
		t.Errorf("Accept = %q, want application/json", got)
	}
	if got := r.Header.Get("User-Agent"); got != defaultUserAgent {
		t.Errorf("User-Agent = %q, want %q", got, defaultUserAgent)
	}
}

// The acceptance criterion of #13: a probe works with NO token and NO tenant id,
// constructed from a base URL alone. New rejects a blank Token or TenantID, so
// before this constructor existed a caller could not build a client at all in
// order to reach an endpoint that needs neither.
func TestNewHealthClient_ProbesWithNoCredentials(t *testing.T) {
	tests := []struct {
		name string
		path string
		call func(*HealthClient, context.Context) (*HealthStatus, error)
	}{
		{"live", healthLivePath, func(hc *HealthClient, ctx context.Context) (*HealthStatus, error) {
			return hc.Health.Live(ctx)
		}},
		{"ready", healthReadyPath, func(hc *HealthClient, ctx context.Context) (*HealthStatus, error) {
			return hc.Health.Ready(ctx)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hc := newTestHealthClient(t, func(w http.ResponseWriter, r *http.Request) {
				assertProbeRequest(t, r, tt.path)
				// A BARE body: no {"data": ...} envelope anywhere on this route,
				// which is why writeJSON and not writeData.
				writeJSON(t, w, http.StatusOK, map[string]any{"status": HealthStatusOK})
			})

			got, err := tt.call(hc, context.Background())
			if err != nil {
				t.Fatalf("probe: %v", err)
			}
			if got.Status != HealthStatusOK {
				t.Errorf("Status = %q, want %q", got.Status, HealthStatusOK)
			}
		})
	}
}

// The second entry point, same code and same wire request: a caller who already
// holds a full Client reaches the probes through Client.Health, and the token it
// carries still does not go to a route that authenticates nobody.
func TestClientHealth_SendsNoCredentialsEither(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assertProbeRequest(t, r, healthLivePath)
		writeJSON(t, w, http.StatusOK, map[string]any{"status": HealthStatusOK})
	})

	got, err := c.Health.Live(context.Background())
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if got.Status != HealthStatusOK {
		t.Errorf("Status = %q, want %q", got.Status, HealthStatusOK)
	}
}

// UNREADY: the server answered, with its own status body, and said it cannot
// serve. This must not collapse into the unreachable case below -- one says
// "back off and re-probe", the other says "look for the process".
func TestHealth_UnreadyIsAnAnsweredFailure(t *testing.T) {
	hc := newTestHealthClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusServiceUnavailable, map[string]any{"status": HealthStatusUnavailable})
	})

	got, err := hc.Health.Ready(context.Background())
	if err == nil {
		t.Fatalf("expected an error, got %+v", got)
	}
	if !IsNotReady(err) {
		t.Fatalf("IsNotReady = false for a 503 carrying the probe's own status body: %v", err)
	}
	if IsUnexpectedStatus(err) {
		t.Error("IsUnexpectedStatus = true: the health view answered, so the status is not unexpected")
	}
	if errors.Is(err, ErrUnreachable) {
		t.Error("errors.Is(err, ErrUnreachable) = true: the server answered, so it was reachable")
	}
	apiErr, ok := AsAPIError(err)
	if !ok {
		t.Fatalf("an answered probe failure must be an *APIError: %v", err)
	}
	if apiErr.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("StatusCode = %d, want 503", apiErr.StatusCode)
	}
	if apiErr.Details["status"] != HealthStatusUnavailable {
		t.Errorf(`Details["status"] = %v, want %q: the server's own word is the operator's evidence`, apiErr.Details["status"], HealthStatusUnavailable)
	}
	if !strings.Contains(apiErr.Message, HealthStatusUnavailable) {
		t.Errorf("Message should quote the server's status word: %q", apiErr.Message)
	}
}

// UNREACHABLE: nothing answered, so there is no status to classify and no
// *APIError at all. The two halves of the distinction are asserted against each
// other on purpose -- a regression that collapsed them would keep each test's
// positive assertion passing.
func TestHealth_UnreachableIsNotUnready(t *testing.T) {
	t.Run("connection refused", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		baseURL := srv.URL
		srv.Close() // nothing is listening on that port any more

		hc, err := NewHealthClient(baseURL)
		if err != nil {
			t.Fatalf("NewHealthClient: %v", err)
		}
		_, err = hc.Health.Ready(context.Background())
		assertUnreachable(t, err)
	})

	// A deadline that has already passed reaches the same branch, deterministically.
	t.Run("cancelled context", func(t *testing.T) {
		hc := newTestHealthClient(t, func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, http.StatusOK, map[string]any{"status": HealthStatusOK})
		})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := hc.Health.Live(ctx)
		assertUnreachable(t, err)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("the cause must survive the wrap so a caller can tell a cancellation from a refused connection: %v", err)
		}
	})
}

func assertUnreachable(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("errors.Is(err, ErrUnreachable) = false: %v", err)
	}
	if IsNotReady(err) {
		t.Error("IsNotReady = true for a server that never answered: unreachable and unready must not collapse")
	}
	if apiErr, ok := AsAPIError(err); ok {
		t.Errorf("a request that got no response became an *APIError (%v); there is no status to carry", apiErr)
	}
}

// Everything that is neither a healthy probe nor the probe's own refusal. The
// unifying rule: a body that is not a status payload was not written by the
// health view, whatever its status code, and never reads as either health or
// unreadiness.
func TestHealth_ResponsesThatAreNotAProbeAnswer(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		contentType string
		body        string
		wantErrPart string
		wantAPIErr  bool
	}{
		{
			name:        "503 from a gateway, no status body",
			status:      http.StatusServiceUnavailable,
			contentType: "text/html",
			body:        "<h1>503 Service Unavailable</h1>",
			wantErrPart: "503",
			wantAPIErr:  true,
		},
		{
			name:        "404 from the wrong origin",
			status:      http.StatusNotFound,
			contentType: "text/html",
			body:        "<h1>Not Found</h1>",
			wantErrPart: "outside /api/<version>",
			wantAPIErr:  true,
		},
		{
			name:        "200 with no status field",
			status:      http.StatusOK,
			contentType: "application/json",
			body:        `{}`,
			wantErrPart: `no "status" field`,
		},
		{
			name:        "200 with a null status",
			status:      http.StatusOK,
			contentType: "application/json",
			body:        `{"status": null}`,
			wantErrPart: `no "status" field`,
		},
		{
			name:        "200 that is not JSON at all",
			status:      http.StatusOK,
			contentType: "text/html",
			body:        "<html>the load balancer's splash page</html>",
			wantErrPart: "decode health response",
		},
		{
			name:        "200 with an empty body",
			status:      http.StatusOK,
			contentType: "application/json",
			body:        "",
			wantErrPart: "empty health response body",
		},
		{
			name:        "200 carrying the data envelope this route does not use",
			status:      http.StatusOK,
			contentType: "application/json",
			body:        `{"data": {"status": "ok"}}`,
			wantErrPart: `no "status" field`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hc := newTestHealthClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tt.contentType)
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})

			got, err := hc.Health.Ready(context.Background())
			if err == nil {
				t.Fatalf("expected an error, got %+v", got)
			}
			if IsNotReady(err) {
				t.Errorf("IsNotReady = true for a response the health view did not write: %v", err)
			}
			if !strings.Contains(err.Error(), tt.wantErrPart) {
				t.Errorf("error = %q, want it to mention %q", err.Error(), tt.wantErrPart)
			}
			if _, ok := AsAPIError(err); ok != tt.wantAPIErr {
				t.Errorf("AsAPIError = %v, want %v", ok, tt.wantAPIErr)
			}
			if tt.wantAPIErr && !IsUnexpectedStatus(err) {
				t.Errorf("a non-2xx with no probe body gets CodeUnexpectedStatus like any other envelope-less response: %v", err)
			}
		})
	}
}

// A non-2xx whose body could not be read keeps its *APIError classification, as
// it does on the versioned surface -- the health path must not be the one place
// where a large error page falls out of AsAPIError.
func TestHealth_OversizedNon2xxKeepsItsAPIErrorClassification(t *testing.T) {
	hc := newTestHealthClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(strings.Repeat("x", 512)))
	})
	hc.Health.client.maxResponseBytes = 32

	_, err := hc.Health.Ready(context.Background())
	apiErr, ok := AsAPIError(err)
	if !ok {
		t.Fatalf("want an *APIError, got %v", err)
	}
	if apiErr.StatusCode != http.StatusServiceUnavailable || apiErr.Code != CodeUnexpectedStatus {
		t.Errorf("got status=%d code=%q, want 503 / %s", apiErr.StatusCode, apiErr.Code, CodeUnexpectedStatus)
	}
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Errorf("the cause must survive the wrap: %v", err)
	}
	if IsNotReady(err) {
		t.Error("IsNotReady = true for a body that was never read: no status payload was established")
	}
}

// The 2xx side is not symmetric, as on the versioned surface: a success status
// with an unusable payload has no classification worth preserving, so it stays a
// plain read error rather than being dressed up as a probe answer.
func TestHealth_Oversized2xxStaysAPlainReadError(t *testing.T) {
	hc := newTestHealthClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]any{"status": strings.Repeat("o", 512)})
	})
	hc.Health.client.maxResponseBytes = 32

	got, err := hc.Health.Live(context.Background())
	if err == nil {
		t.Fatalf("expected an error, got %+v", got)
	}
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("err = %v, want ErrResponseTooLarge", err)
	}
	if apiErr, ok := AsAPIError(err); ok {
		t.Errorf("a 2xx read failure became an *APIError (%v); there is no status classification to preserve", apiErr)
	}
}

// A gateway mount on the base URL is preserved, and NO /api/<version> is added.
// Both halves matter: the probes live beside the API, not under it.
func TestHealth_BaseURLPathPrefix(t *testing.T) {
	tests := []struct {
		name        string
		suffix      string
		wantEscaped string
	}{
		{"no prefix", "", "/health/live"},
		{"gateway prefix", "/gateway", "/gateway/health/live"},
		{"nested prefix", "/a/b", "/a/b/health/live"},
		{"trailing slash on the base", "/gateway/", "/gateway/health/live"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.URL.EscapedPath(); got != tt.wantEscaped {
					t.Errorf("escaped path = %q, want %q", got, tt.wantEscaped)
				}
				writeJSON(t, w, http.StatusOK, map[string]any{"status": HealthStatusOK})
			}))
			t.Cleanup(srv.Close)

			hc, err := NewHealthClient(srv.URL + tt.suffix)
			if err != nil {
				t.Fatalf("NewHealthClient: %v", err)
			}
			if _, err := hc.Health.Live(context.Background()); err != nil {
				t.Fatalf("Live: %v", err)
			}
		})
	}
}

func TestNewHealthClient_Validation(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		opts    []HealthOption
		wantErr bool
	}{
		{"base url alone", "https://octonomy.example.com", nil, false},
		{"trailing slash trimmed", "https://octonomy.example.com/", nil, false},
		{"missing base url", "", nil, true},
		{"blank base url", "   ", nil, true},
		{"relative base url", "octonomy.example.com", nil, true},
		{"custom http client", "https://octonomy.example.com", []HealthOption{WithHealthHTTPClient(&http.Client{})}, false},
		{"custom user agent", "https://octonomy.example.com", []HealthOption{WithHealthUserAgent("probe/1")}, false},
		// Falling back to the default silently would leave a caller who meant to
		// set a 2s timeout probing on the 30s one with no sign of it.
		{"nil http client", "https://octonomy.example.com", []HealthOption{WithHealthHTTPClient(nil)}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hc, err := NewHealthClient(tt.baseURL, tt.opts...)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %+v", hc)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if hc.Health == nil {
				t.Fatal("Health service not wired")
			}
		})
	}
}

func TestNewHealthClient_OptionsApply(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		writeJSON(t, w, http.StatusOK, map[string]any{"status": HealthStatusOK})
	}))
	t.Cleanup(srv.Close)

	custom := &http.Client{}
	hc, err := NewHealthClient(srv.URL, WithHealthHTTPClient(custom), WithHealthUserAgent("probe/1"))
	if err != nil {
		t.Fatalf("NewHealthClient: %v", err)
	}
	if hc.Health.client.httpClient != custom {
		t.Error("WithHealthHTTPClient did not take effect")
	}
	if _, err := hc.Health.Live(context.Background()); err != nil {
		t.Fatalf("Live: %v", err)
	}
	if gotUA != "probe/1" {
		t.Errorf("User-Agent = %q, want %q", gotUA, "probe/1")
	}
}

// The invariant guard: a credential-free client cannot reach a tenant-scoped
// route. Nothing exported routes there today -- HealthClient exposes only
// Health -- so this asserts the guard rather than a reachable path, and what it
// prevents is silent: a blank "Authorization: Bearer " and an empty X-Tenant-ID
// are a well-formed request that no server can attribute to anyone.
func TestHealthClient_RefusesAPICalls(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		writeData(t, w, http.StatusOK, Tag{ID: "tag_1"})
	}))
	t.Cleanup(srv.Close)

	hc, err := NewHealthClient(srv.URL)
	if err != nil {
		t.Fatalf("NewHealthClient: %v", err)
	}

	_, _, err = hc.Health.client.doRaw(context.Background(), http.MethodGet, "/tags", nil, nil)
	if err == nil {
		t.Fatal("a probe-only client must refuse an API call")
	}
	if !strings.Contains(err.Error(), "NewHealthClient") {
		t.Errorf("the error should name the constructor that produced this client: %v", err)
	}
	if requests != 0 {
		t.Errorf("the refusal must happen before anything is sent, got %d request(s)", requests)
	}
}
