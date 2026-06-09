package octonomy

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// apiPrefix is the stable v1 URL contract. It stays at v1 for the entire 1.x
	// line of the Octonomy server (see docs/versioning.md).
	apiPrefix = "/api/v1"

	defaultTimeout = 30 * time.Second
)

// Config configures a Client. BaseURL, Token, and TenantID are required.
type Config struct {
	// BaseURL is the Octonomy origin, e.g. "https://octonomy.example.com". The
	// SDK appends the /api/v1 prefix; do not include it here.
	BaseURL string

	// Token is the service token sent as "Authorization: Bearer <token>".
	Token string

	// TenantID is sent as the X-Tenant-ID header and scopes every request.
	TenantID string

	// ActorID, when set, is sent as X-Actor-ID to attribute mutations in audit
	// logs. It can be overridden per call with WithActor.
	ActorID string

	// HTTPClient is used for all requests. When nil, an http.Client with a 30s
	// timeout is used.
	HTTPClient *http.Client

	// UserAgent overrides the default "octonomy-go/<version>" User-Agent.
	UserAgent string
}

// Client is the entry point to the Octonomy API. Construct it with New and reach
// resources through the service fields. A Client is safe for concurrent use.
type Client struct {
	httpClient *http.Client
	baseURL    *url.URL
	token      string
	tenantID   string
	actorID    string
	userAgent  string

	// Vocabularies manages tenant-scoped tag groupings.
	Vocabularies *VocabularyService
	// Tags manages the core tagging units.
	Tags *TagService
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
	}
	c.Vocabularies = &VocabularyService{client: c}
	c.Tags = &TagService{client: c}
	return c, nil
}
