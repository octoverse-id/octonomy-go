package octonomy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Health probe paths. They sit at the SERVER ROOT, outside /api/<version>: the
// server mounts them ahead of the versioned include (config/urls.py), so no
// prefix and no version applies to them, and doUnversioned rather than doRaw is
// what reaches them.
const (
	healthLivePath  = "/health/live"
	healthReadyPath = "/health/ready"
)

// The two words server 3.1.x writes into HealthStatus.Status: "ok" on a 200 from
// either probe, "unavailable" on the 503 /health/ready answers when its database
// connection will not open.
//
// They are here for callers that log or compare the word. The SDK does NOT
// branch on it -- the HTTP status decides ready from not-ready -- so a future
// server word arrives verbatim in Status rather than being rejected as unknown.
const (
	HealthStatusOK          = "ok"
	HealthStatusUnavailable = "unavailable"
)

// HealthStatus is the body of a health probe: a bare {"status": "..."} object
// with NO data envelope.
//
// It is the one Octonomy response shape that carries no wrapper, which is why
// health has its own decoder instead of doData's. Loosening doData to accept a
// bare body would re-open #32 for every other resource, where an unwrapped
// decode yields a zero-valued struct and a nil error.
type HealthStatus struct {
	Status string `json:"status"`
}

// HealthService probes the server's liveness and readiness. Reach it via
// Client.Health, or via NewHealthClient when there are no credentials to hand.
//
// BOTH ENTRY POINTS RUN THE SAME CODE and send the same request: no
// Authorization, no X-Tenant-ID, no namespace headers, and no /api/<version>
// prefix. The routes are unauthenticated and unversioned on the server, so a
// probe from a fully configured Client is byte-for-byte the one a credential-free
// HealthClient sends.
//
// The methods take no RequestOption, and the option set splits in two on why.
// WithNamespace, WithApplication, WithIncludeGlobal, and WithActor are scoping
// or attribution knobs for the versioned, tenant-scoped API, and none of them
// means anything on a route that has no tenant. Accepting and ignoring them
// would be the silent no-op this SDK refuses everywhere else, so the signature
// says so instead.
//
// WITHREQUESTID IS THE ONE THAT WOULD DO SOMETHING, and it is excluded anyway.
// The server's request middleware runs on these routes too: probed against
// 3.1.0, /health/ready echoes a caller-supplied X-Request-ID back and mints one
// when there is none. But a probe mutates nothing, so it writes no audit row and
// emits no event, and its non-2xx carries {"status": ...} rather than the error
// envelope -- so of the four sinks that make a correlation id worth sending, a
// probe reaches only the server's log line. Against that thin gain: options here
// mean either accepting all of them and ignoring most, or growing a per-option
// rejection surface on the one route with nothing to scope, and either one costs
// the property above -- that both entry points send a byte-for-byte identical
// request, which is what lets them share one code path and one set of tests.
// Raised by Codex review on #5 and refused on those grounds. If a correlated
// probe is ever wanted, doUnversioned is what to revisit, not this signature.
type HealthService struct {
	client *Client
}

// HealthClient reaches the health probes with NO credentials. Build it with
// NewHealthClient.
//
// It exists because New requires a Token and a TenantID and the probes need
// neither: without a separate constructor a caller could not build a client at
// all in order to reach an endpoint that authenticates nobody. Loosening New's
// validation instead would have cost every other resource the guarantee that a
// constructed Client is tenant-scoped.
type HealthClient struct {
	// Health probes liveness and readiness. It is the same *HealthService type
	// Client.Health carries, so a call site reads identically whichever client
	// it holds.
	Health *HealthService
}

// HealthOption configures a HealthClient at construction.
//
// This is a different shape from New's Config struct, and deliberately: a health
// client has exactly ONE required input, and a positional base URL with optional
// knobs makes "no credentials are needed here" visible at the call site rather
// than something a reader has to infer from the absence of two struct fields.
type HealthOption func(*healthConfig)

type healthConfig struct {
	httpClient *http.Client
	userAgent  string
	err        error
}

// WithHealthHTTPClient sets the *http.Client used for the probes. The default is
// an http.Client with the same 30s timeout New uses.
//
// This is the knob a probe loop usually wants, and a much shorter timeout is
// normally right for one: a readiness check that hangs for 30 seconds has
// already failed as far as the caller polling it is concerned.
//
//	hc, err := octonomy.NewHealthClient(baseURL,
//		octonomy.WithHealthHTTPClient(&http.Client{Timeout: 2 * time.Second}))
func WithHealthHTTPClient(httpClient *http.Client) HealthOption {
	return func(hc *healthConfig) {
		if httpClient == nil {
			// Silently falling back to the default would leave a caller who
			// passed a deliberately-short-timeout client that turned out to be
			// nil probing on the 30s default with no sign of it.
			hc.fail(fmt.Errorf("octonomy: WithHealthHTTPClient(nil): pass an *http.Client or omit the option"))
			return
		}
		hc.httpClient = httpClient
	}
}

// WithHealthUserAgent overrides the default "octonomy-go/<version>" User-Agent,
// as Config.UserAgent does for a full client.
func WithHealthUserAgent(userAgent string) HealthOption {
	return func(hc *healthConfig) { hc.userAgent = userAgent }
}

// fail records the first option error, mirroring requestConfig.fail: the first
// mistake is the one whose remediation points at the right argument.
func (hc *healthConfig) fail(err error) {
	if hc.err == nil {
		hc.err = err
	}
}

// NewHealthClient returns a client that can reach the health probes and nothing
// else, from a base URL alone.
//
//	hc, err := octonomy.NewHealthClient("https://octonomy.example.com")
//	if err != nil {
//		return err
//	}
//	if _, err := hc.Health.Ready(ctx); err != nil {
//		// see IsNotReady and ErrUnreachable -- they mean different things
//	}
//
// baseURL is the Octonomy origin, exactly as Config.BaseURL: no /api prefix, and
// a path prefix on it (a gateway mount such as /octonomy) is preserved, so the
// probe goes to <baseURL>/health/live rather than to the host root.
//
// The returned client holds no token and no tenant, so it can reach no
// tenant-scoped route -- HealthClient exposes only Health, and the API transport
// refuses a credential-free client outright rather than sending a blank
// Authorization header.
func NewHealthClient(baseURL string, opts ...HealthOption) (*HealthClient, error) {
	base, err := parseBaseURL(baseURL, "baseURL")
	if err != nil {
		return nil, err
	}

	var cfg healthConfig
	for i, opt := range opts {
		// A nil option is a caller mistake, not a reason to panic -- and
		// conditionally assembled option slices are exactly where one comes from.
		if opt == nil {
			return nil, fmt.Errorf("octonomy: HealthOption %d is nil; omit it rather than passing a nil option", i)
		}
		opt(&cfg)
	}
	if cfg.err != nil {
		return nil, cfg.err
	}

	httpClient := cfg.httpClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultTimeout}
	}
	userAgent := cfg.userAgent
	if userAgent == "" {
		userAgent = defaultUserAgent
	}

	// A *Client with no credentials and no API version. Nothing versioned is
	// reachable from it: probeOnly makes doRaw refuse, and HealthClient exposes
	// no service that would call it.
	c := &Client{
		httpClient:       httpClient,
		baseURL:          base,
		userAgent:        userAgent,
		maxResponseBytes: maxResponseBytes,
		probeOnly:        true,
	}
	return &HealthClient{Health: &HealthService{client: c}}, nil
}

// Live reports that the server process is up and serving HTTP
// (GET /health/live).
//
// It checks nothing beyond the process itself -- the server answers 200
// {"status": "ok"} unconditionally -- so it is the restart signal, not the
// traffic signal. Use Ready for the latter.
func (s *HealthService) Live(ctx context.Context) (*HealthStatus, error) {
	return s.probe(ctx, healthLivePath)
}

// Ready reports that the server can serve traffic (GET /health/ready), which on
// server 3.1.x means its database connection opens.
//
// A SERVER THAT ANSWERS "NOT READY" AND ONE THAT DOES NOT ANSWER AT ALL ARE
// DIFFERENT FAILURES, and this method keeps them apart:
//
//	st, err := hc.Health.Ready(ctx)
//	switch {
//	case err == nil:
//		// ready; st.Status is the server's word, "ok" today
//	case octonomy.IsNotReady(err):
//		// reachable and answering, but not serving -- back off and re-probe
//	case errors.Is(err, octonomy.ErrUnreachable):
//		// no response at all -- wrong URL, process gone, or the deadline passed
//	default:
//		// answered, but not with a health payload: a proxy or gateway spoke
//	}
//
// Collapsing the first two into one error would hide the distinction an operator
// most needs: a 503 from the application means "wait", while no response at all
// means "look somewhere other than the database".
func (s *HealthService) Ready(ctx context.Context) (*HealthStatus, error) {
	return s.probe(ctx, healthReadyPath)
}

// probe runs one health request and classifies the answer.
//
// The classification is the whole point of the function, and it turns on the
// BODY rather than on the status:
//
//   - 2xx with a status body -> the probe passed; the body is returned.
//   - 2xx without one -> an error. A 200 that carries no readable status is not
//     evidence of health, and returning a zero-valued *HealthStatus with a nil
//     error is the failure mode this SDK refuses everywhere (#32).
//   - non-2xx WITH a status body -> *APIError carrying CodeNotReady. The
//     response came from the health view itself, so the server is up and saying
//     it cannot serve.
//   - non-2xx WITHOUT one -> parseError, i.e. CodeUnexpectedStatus. Something
//     that is not the health view answered: a proxy, a gateway, or a host that
//     is not Octonomy.
//
// That last split is why CodeNotReady is not the status-to-code mapping
// CodeUnexpectedStatus exists to forbid. Nothing here infers a meaning from an
// HTTP status; the code is read off a server-authored payload, and a 503 whose
// body is an HTML error page is classified exactly as it would be anywhere else
// in this package.
func (s *HealthService) probe(ctx context.Context, path string) (*HealthStatus, error) {
	status, body, err := s.client.doUnversioned(ctx, path)
	if err != nil {
		return nil, err
	}

	health, decodeErr := decodeHealthStatus(body)
	if status >= 200 && status < 300 {
		if decodeErr != nil {
			return nil, decodeErr
		}
		return health, nil
	}
	if decodeErr != nil {
		return nil, parseError(status, body, healthHint(status, path))
	}
	return nil, &APIError{
		StatusCode: status,
		Code:       CodeNotReady,
		Message:    fmt.Sprintf("the server answered %s with status %q", path, health.Status),
		Details:    map[string]any{"status": health.Status},
	}
}

// decodeHealthStatus decodes a bare {"status": "..."} body, reporting anything
// that would otherwise pass as a zero-valued HealthStatus.
//
// A MISSING OR BLANK status IS AN ERROR. The probes exist to be believed, and a
// status carrying no word is what a caller would get from {}, from a JSON body
// with a renamed key, and from a 200 served by something that is not Octonomy at
// all -- three different problems that must not read as a healthy server.
//
// Blank is measured after trimming, as every other emptiness test in this
// package is (Config.Token, the namespace pair, an application id, and the body
// check right above). "   " carries exactly as much health information as "",
// and a rule that rejects one while accepting the other is a rule with a hole in
// it rather than a narrower rule.
//
// What is NOT trimmed is the value itself: whatever word the server sent reaches
// the caller verbatim in Status. The check decides whether a payload arrived at
// all; it does not get to rewrite the server's answer.
func decodeHealthStatus(body []byte) (*HealthStatus, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, fmt.Errorf("octonomy: empty health response body, expected a status payload")
	}
	var health HealthStatus
	if err := json.Unmarshal(body, &health); err != nil {
		return nil, fmt.Errorf("octonomy: decode health response: %w", err)
	}
	if strings.TrimSpace(health.Status) == "" {
		return nil, fmt.Errorf(`octonomy: health response carried no "status" field, so it did not come from an Octonomy health probe`)
	}
	return &health, nil
}

// healthHint diagnoses an envelope-less 404 on a probe route, as versionHint
// does for the versioned surface -- and it names a different cause, which is why
// it is a separate function: the probes are unversioned, so an API version can
// never be the explanation here.
//
// Phrased as a likelihood rather than an assertion: a gateway that routes /api
// but not /health produces the same response.
func healthHint(status int, path string) string {
	if status != http.StatusNotFound {
		return ""
	}
	return fmt.Sprintf(" [octonomy-go: %s is served at the server root, outside /api/<version>; a 404 here usually means the base URL does not point at the Octonomy origin]", path)
}
