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
	// APIV1 targets /api/v1, the original global-only surface.
	APIV1 APIVersion = "v1"

	// APIV2 targets /api/v2, the server's primary advertised surface, which adds
	// namespace scoping.
	APIV2 APIVersion = "v2"
)

// DefaultAPIVersion is the surface New selects when Config.APIVersion is empty.
//
// BREAKING: this used to be an unconditional /api/v1. A client pointed at an
// Octonomy older than 2.0 must now set Config.APIVersion = APIV1 explicitly --
// such a server has no /api/v2 route at all, so every call returns an unrouted
// 404. That failure is loud rather than silent (see errors.go,
// CodeUnexpectedStatus), but it is still a failure, and no version handshake
// exists to detect it in advance. See the README upgrade note.
//
// 2.0 is the real cutoff, not 3.0: the server's version shim landed in commit
// bd7bc62, and `git tag --contains bd7bc62` names v2.0.0 as its first release
// (server CHANGELOG 2.0.0, 2026-07-29, "Added: /api/v2 API surface via a
// version shim"). An earlier revision of this SDK said 3.0 -- inferred from the
// 3.1.x contract this client targets rather than checked -- which would have
// told every 2.x operator to turn off a namespace surface their server has.
//
// The default is deliberately set here rather than left to the zero value, and
// it is due a review at v2.0.0: a major version is where a wire-level default
// flip is legible to a reader of the CHANGELOG, and by then real-server
// integration coverage (#17) will say whether v2 is the right default for every
// deployment or only for current ones.
const DefaultAPIVersion = APIV2

const defaultTimeout = 30 * time.Second

// prefix is the URL path prefix carrying this version, e.g. "/api/v2".
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
	// is APIV2. Set APIV1 for an Octonomy server older than 2.0, which has no
	// /api/v2 route.
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

	// Vocabularies manages tenant-scoped tag groupings.
	Vocabularies *VocabularyService
	// Tags manages the core tagging units.
	Tags *TagService
	// Aliases manages the alternate identifiers that resolve to canonical tags.
	Aliases *AliasService
	// Assignments links tags to external resources.
	Assignments *AssignmentService
}

// New validates cfg and returns a ready Client.
func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return nil, fmt.Errorf("octonomy: Config.BaseURL is required")
	}
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, fmt.Errorf("octonomy: Config.Token is required")
	}
	if strings.TrimSpace(cfg.TenantID) == "" {
		return nil, fmt.Errorf("octonomy: Config.TenantID is required")
	}

	apiVersion := cfg.APIVersion
	if apiVersion == "" {
		apiVersion = DefaultAPIVersion
	}
	if !apiVersion.valid() {
		return nil, fmt.Errorf("octonomy: invalid Config.APIVersion %q, want %q or %q", cfg.APIVersion, APIV1, APIV2)
	}

	base, err := url.Parse(strings.TrimRight(cfg.BaseURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("octonomy: invalid Config.BaseURL: %w", err)
	}
	if base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("octonomy: Config.BaseURL must be an absolute URL, got %q", cfg.BaseURL)
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
	c.Aliases = &AliasService{client: c}
	c.Assignments = &AssignmentService{client: c}
	return c, nil
}

// APIVersion reports the REST surface this client targets.
func (c *Client) APIVersion() APIVersion { return c.apiVersion }
