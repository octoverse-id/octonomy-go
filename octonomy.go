package octonomy

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// APIVersion selects which Octonomy REST surface a Client targets.
//
// Both surfaces are live and fully supported by the server; neither is
// deprecated. What separates them is the namespace axis: v2 carries
// merchant/sub-tenant scoping (see WithNamespace), and v1 is global-only and
// rejects namespace headers with a named 400.
type APIVersion string

const (
	// APIV1 targets /api/v1, the original global-only surface. It is this
	// line's default; see DefaultAPIVersion.
	APIV1 APIVersion = "v1"

	// APIV2 targets /api/v2, the server's primary advertised surface, which adds
	// namespace scoping. On this line it is opt-in: set Config.APIVersion.
	APIV2 APIVersion = "v2"
)

// DefaultAPIVersion is the surface New selects when Config.APIVersion is empty.
//
// IT IS APIV1 ON THIS LINE, which is not the value the /v2 module had when this
// selector was ported from it (main at 5e40964, APIV2). v1.0.0 sent every request
// to /api/v1 unconditionally, and this line can never publish a major, so it can
// never change a default under a caller: copying the /v2 module's value would
// move every existing caller's requests to a different REST surface on an
// in-range `go get -u`, with nothing in their code saying so. /api/v2 therefore
// arrives opt-in, behind Config.APIVersion = APIV2.
//
// What the opt-in buys is the namespace axis -- WithNamespace and
// WithIncludeGlobal require it -- and nothing else changes shape: both surfaces
// publish the same paths, and on a global (namespace-less) request they behave
// alike. A server older than Octonomy 2.0 has no /api/v2 route at all, so a
// client opting in against one gets an unrouted 404 on every call; that failure
// is loud rather than silent (see CodeUnexpectedStatus and the hint the error
// message carries).
const DefaultAPIVersion = APIV1

const defaultTimeout = 30 * time.Second

// prefix is the URL path prefix carrying this version, e.g. "/api/v1".
func (v APIVersion) prefix() string { return "/api/" + string(v) }

func (v APIVersion) valid() bool { return v == APIV1 || v == APIV2 }

// Config configures a Client. BaseURL, Token, and TenantID are required.
type Config struct {
	// BaseURL is the Octonomy origin, e.g. "https://octonomy.example.com". The
	// SDK appends the /api/<version> prefix; do not include it here.
	BaseURL string

	// Token is the service token sent as "Authorization: Bearer <token>".
	Token string

	// TenantID is sent as the X-Tenant-ID header and scopes every request.
	TenantID string

	// APIVersion selects the REST surface. Empty means DefaultAPIVersion, which
	// on this line is APIV1 -- the surface every v1.0.0 request went to, so an
	// existing caller's requests do not move on an upgrade. Set APIV2 to reach
	// /api/v2 and its namespace axis (WithNamespace); it needs an Octonomy server
	// of 2.0 or later.
	APIVersion APIVersion

	// ActorID, when set, is sent as X-Actor-ID to attribute mutations in audit
	// logs. It can be overridden per call with WithActor.
	ActorID string

	// HTTPClient is used for all requests. When nil, an http.Client with a 30s
	// timeout is used.
	HTTPClient *http.Client

	// UserAgent overrides the default "octonomy-go/<version>" User-Agent.
	UserAgent string

	// There is deliberately NO namespace field here. A client-level namespace
	// default is a cross-merchant data-leak footgun: one shared *Client would
	// silently scope every read to whichever merchant was configured at startup,
	// and every call site would still look correct. It is not a failure the
	// server can catch either -- omitting the headers is a legal request that
	// returns the GLOBAL namespace with a 200 (probed against 3.1.0), so a
	// mis-scoped read is wrong rows, never an error. Namespace is therefore
	// per-request only, via WithNamespace, where the scope is visible at the
	// point the data is requested.
}

// Client is the entry point to the Octonomy API. Construct it with New and reach
// resources through the service fields. A Client is safe for concurrent use.
//
// Concurrency note: every per-request scoping option (namespace, application,
// include_global) is resolved into a value local to the call, so N goroutines
// sharing one Client may each target a different namespace without cross-talk.
type Client struct {
	httpClient *http.Client
	baseURL    *url.URL
	token      string
	tenantID   string
	actorID    string
	userAgent  string
	apiVersion APIVersion

	// maxResponseBytes bounds a single response body. It is set from the
	// package default in New and is a field rather than a constant read at the
	// call site so the limit can be lowered in tests without allocating the
	// default ceiling to prove the check fires.
	maxResponseBytes int64

	// probeOnly marks a Client built by NewHealthClient: no token, no tenant, no
	// API version, and no service wired but Health. doRaw refuses such a client
	// outright, so the credential-free constructor cannot become a way to reach
	// a tenant-scoped route with blank credentials.
	probeOnly bool

	// Vocabularies manages tenant-scoped tag groupings.
	Vocabularies *VocabularyService
	// Tags manages the core tagging units.
	Tags *TagService
	// Health probes liveness and readiness. Unauthenticated and unversioned, so
	// it needs no credentials at all -- see NewHealthClient for the entry point
	// that requires none.
	Health *HealthService
}

// parseBaseURL validates and normalizes an Octonomy origin for both
// constructors. label names the caller's field so the error points at the
// argument the caller actually wrote ("Config.BaseURL" or "baseURL").
func parseBaseURL(raw, label string) (*url.URL, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("octonomy: %s is required", label)
	}
	base, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil {
		return nil, fmt.Errorf("octonomy: invalid %s: %w", label, err)
	}
	if base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("octonomy: %s must be an absolute URL, got %q", label, raw)
	}
	return base, nil
}

// New validates cfg and returns a ready Client.
func New(cfg Config) (*Client, error) {
	// The three presence checks run before the base URL is parsed, the order
	// v1.0.0 reported them in, so a Config missing several fields still names
	// the same one first.
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return nil, fmt.Errorf("octonomy: Config.BaseURL is required")
	}
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, fmt.Errorf("octonomy: Config.Token is required")
	}
	if strings.TrimSpace(cfg.TenantID) == "" {
		return nil, fmt.Errorf("octonomy: Config.TenantID is required")
	}
	base, err := parseBaseURL(cfg.BaseURL, "Config.BaseURL")
	if err != nil {
		return nil, err
	}

	apiVersion := cfg.APIVersion
	if apiVersion == "" {
		apiVersion = DefaultAPIVersion
	}
	if !apiVersion.valid() {
		return nil, fmt.Errorf("octonomy: invalid Config.APIVersion %q, want %q or %q", cfg.APIVersion, APIV1, APIV2)
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultTimeout}
	}
	userAgent := cfg.UserAgent
	if userAgent == "" {
		userAgent = defaultUserAgent
	}

	c := &Client{
		httpClient: httpClient,
		baseURL:    base,
		token:      cfg.Token,
		tenantID:   cfg.TenantID,
		actorID:    cfg.ActorID,
		userAgent:  userAgent,
		apiVersion: apiVersion,

		maxResponseBytes: maxResponseBytes,
	}
	c.Vocabularies = &VocabularyService{client: c}
	c.Tags = &TagService{client: c}
	c.Health = &HealthService{client: c}
	return c, nil
}

// APIVersion reports the REST surface this client targets.
func (c *Client) APIVersion() APIVersion { return c.apiVersion }
