package octonomy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const (
	namespaceTypeHeader = "X-Namespace-Type"
	namespaceIDHeader   = "X-Namespace-ID"

	// reservedNamespaceTypeGlobal is the one namespace type the server refuses:
	// "global" names the tenant-shared namespace, which is selected by sending no
	// namespace headers at all, so accepting it as a type would give one concept
	// two spellings that scope differently.
	reservedNamespaceTypeGlobal = "global"

	applicationIDParam    = "application_id"
	includeGlobalParam    = "include_global"
	scopeParam            = "scope"
	scopeMerchantValue    = "merchant"
	maxResponseBytes      = 32 << 20 // 32 MiB
	maxResponseBytesLabel = "32 MiB"
)

// ErrResponseTooLarge is returned when a response body exceeds the SDK's read
// ceiling. A caller cannot express a size limit through *http.Client -- its
// Timeout bounds duration, not bytes -- so the ceiling lives here, at the one
// chokepoint every method shares.
var ErrResponseTooLarge = errors.New("octonomy: response body exceeded the " + maxResponseBytesLabel + " read limit")

// RequestOption customizes a single request.
//
// Options contribute headers (WithActor, WithNamespace), query parameters
// (WithApplication, WithIncludeGlobal), or both, so they are resolved before the
// URL is built rather than layered on afterwards. Every option is per-call: a
// Client holds no scoping state, which is what lets goroutines sharing one
// Client target different namespaces concurrently.
type RequestOption func(*requestConfig)

type requestConfig struct {
	actorID  string
	actorSet bool

	namespaceType string
	namespaceID   string
	namespaceSet  bool

	applicationID  string
	applicationSet bool

	includeGlobal bool

	// err carries the first option-construction failure. An option is a plain
	// func with no error return, applied at call time, so an incoherent argument
	// records the problem here and doRaw reports it before any request is issued.
	// First failure wins: reporting the last one would let a later valid option
	// mask an earlier mistake.
	err error
}

func (rc *requestConfig) fail(err error) {
	if rc.err == nil {
		rc.err = err
	}
}

// WithActor sets the X-Actor-ID header for one request, overriding Config.ActorID.
// Use it to attribute a mutation to a specific user or service in the audit log.
func WithActor(actorID string) RequestOption {
	return func(rc *requestConfig) {
		rc.actorID = actorID
		rc.actorSet = true
	}
}

// WithNamespace scopes one request to a merchant or sub-tenant namespace by
// sending the X-Namespace-Type / X-Namespace-ID header pair. It requires
// Config.APIVersion = APIV2; /api/v1 is global-only.
//
// A namespaced request must also name its application, because namespace
// isolation sits below application on the server. Supply it with WithApplication
// on a read, or as the ApplicationID field of the write body on a create.
//
// Namespaced reads exclude global (tenant-shared) rows by default. Add
// WithIncludeGlobal to see both.
//
// nsType and nsID are opaque, caller-canonical strings: the server does not
// case-fold them, so "Merchant" and "merchant" are different namespaces.
func WithNamespace(nsType, nsID string) RequestOption {
	return func(rc *requestConfig) {
		switch {
		case strings.TrimSpace(nsType) == "":
			rc.fail(fmt.Errorf("octonomy: WithNamespace: namespace type is required; omit the option entirely for the global namespace"))
		case strings.TrimSpace(nsID) == "":
			rc.fail(fmt.Errorf("octonomy: WithNamespace: namespace id is required whenever a namespace type is set"))
		case nsType == reservedNamespaceTypeGlobal:
			rc.fail(fmt.Errorf("octonomy: WithNamespace: %q is a reserved namespace type; use WithGlobalNamespace, or omit the option, for the tenant-shared namespace", reservedNamespaceTypeGlobal))
		default:
			rc.namespaceType, rc.namespaceID, rc.namespaceSet = nsType, nsID, true
		}
	}
}

// WithGlobalNamespace pins one request to the global (tenant-shared) namespace
// by sending no namespace headers, which is how the server selects it.
//
// Since a Client holds no namespace default, this changes nothing on its own --
// it exists to make "global, deliberately" readable at a call site, and to
// cancel a WithNamespace held in a shared []RequestOption. Options apply in
// order, so the last of the two wins.
func WithGlobalNamespace() RequestOption {
	return func(rc *requestConfig) {
		rc.namespaceType, rc.namespaceID, rc.namespaceSet = "", "", false
	}
}

// WithApplication scopes one request to an application via the application_id
// query parameter.
//
// Reads take application scope from the query string only, so this is the only
// way to apply it to a Get or a Delete, which have no params struct -- and it is
// required on a namespaced read. Setting a value that contradicts one already in
// the request is an error rather than a silent overwrite.
//
// Writes may carry application scope EITHER here or as the ApplicationID field
// of the request body, and both persist. That is not an inference from the
// spec's parameter lists: a namespaced POST carrying application_id in the query
// alone was probed against 3.1.0 and returned 201 with the application persisted
// on the row (the server folds it in via create_payload_with_scope). Carrying it
// in neither place is a 403 naming the namespace grant, so the omission is loud
// -- which is why checkScopeCoherence does not try to inspect write bodies.
func WithApplication(applicationID string) RequestOption {
	return func(rc *requestConfig) {
		if strings.TrimSpace(applicationID) == "" {
			rc.fail(fmt.Errorf("octonomy: WithApplication: application id is required; omit the option for a tenant-wide request"))
			return
		}
		rc.applicationID, rc.applicationSet = applicationID, true
	}
}

// WithIncludeGlobal asks a namespaced read to also return the global
// (tenant-shared) rows the caller is authorized for, instead of the namespace's
// rows alone.
//
// It is fail-closed on the server: it widens what the request ASKS for, and a
// token holding an exact merchant grant with no global authority still sees no
// global rows.
//
// Reads only. The server takes include_global from the query string on safe
// methods and ignores it entirely on writes, so applying it to a create or an
// update is refused here rather than being sent and quietly doing nothing.
func WithIncludeGlobal() RequestOption {
	return func(rc *requestConfig) { rc.includeGlobal = true }
}

// The transport is one request path and three decoders. Pick the decoder by the
// shape of the 2xx body, never by convenience:
//
//	                      ┌─────────────────────────────────────────┐
//	resource method ─────▶│ doRaw  build URL, headers, body; send;   │
//	                      │        non-2xx → *APIError               │
//	                      │        2xx → (status, raw body)          │
//	                      └───────────────┬─────────────────────────┘
//	                                      │
//	        ┌─────────────────────────────┼─────────────────────────────┐
//	        ▼                             ▼                             ▼
//	  (c *Client) do            doData[T](ctx, c, …)          doList[T](ctx, c, …)
//	  no payload to decode      single resource               list envelope
//	  DELETE → 204, no body     {"data": {…}}                 {"data": […],
//	  asserts 204               unwraps → *T                   "pagination": {…}}
//	                                                          → *List[T]
//
// Every branch that could otherwise return a zero value with a nil error is an
// error instead. That is the whole point of the split: decoding a wrapped body
// straight into a *Tag yields an empty struct and no error, which is how the
// missing unwrap survived a full unit suite and was found only by a smoke test
// against a real server (#32).
//
// doData and doList are package-level generic functions rather than methods
// because Go does not allow type parameters on methods ("method must have no
// type parameters"). What that buys is the decoded type at each call site: a
// method on *TagService cannot return a *Vocabulary by accident, and the result
// type cannot drift from the resource.
//
// It does NOT make the choice between the three helpers a compile error -- all
// three can be pointed at any endpoint. That choice is caught at runtime, by the
// envelope assertions below, which is why they exist rather than relying on
// review to spot a mis-routed method.

// doRaw performs a single API call and returns the response status and raw body
// for a 2xx: it builds the URL under the client's /api/<version> prefix, sets the
// auth, tenant, and scoping headers, and JSON-encodes body when non-nil. Non-2xx
// responses become *APIError.
//
// Decoding belongs to the three callers above, because the shape depends on what
// was requested. Keeping the decode out of here is what makes "a 2xx that
// carries nothing where a payload was expected" an error instead of a zero
// value. Resource files must not call doRaw directly.
func (c *Client) doRaw(ctx context.Context, method, path string, query url.Values, body any, opts ...RequestOption) (int, []byte, error) {
	var rc requestConfig
	for _, opt := range opts {
		opt(&rc)
	}

	query, err := rc.mergeQuery(query)
	if err != nil {
		return 0, nil, err
	}
	if err := c.checkScopeCoherence(method, rc, query); err != nil {
		return 0, nil, err
	}

	endpoint := *c.baseURL
	endpoint.Path += c.apiVersion.prefix() + path
	if len(query) > 0 {
		endpoint.RawQuery = query.Encode()
	}

	var reqBody io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("octonomy: encode request body: %w", err)
		}
		reqBody = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), reqBody)
	if err != nil {
		return 0, nil, fmt.Errorf("octonomy: build request: %w", err)
	}
	req.Header = c.headers(rc, body != nil)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("octonomy: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := readBounded(resp.Body, c.maxResponseBytes)
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("octonomy: read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, respBody, parseError(resp.StatusCode, respBody, c.apiVersion)
	}
	return resp.StatusCode, respBody, nil
}

// headers assembles the outbound header set.
//
// Extracted from doRaw, which had grown past the point where the branches were
// readable inline: auth, tenant, accept, user-agent, content-type, actor, and
// now the namespace pair. Assembling into a fresh http.Header and assigning it
// wholesale also means the header set is a value derived from (client, request
// config) rather than a sequence of mutations on a request.
func (c *Client) headers(rc requestConfig, hasBody bool) http.Header {
	h := make(http.Header, 8)
	h.Set("Authorization", "Bearer "+c.token)
	h.Set("X-Tenant-ID", c.tenantID)
	h.Set("Accept", "application/json")
	h.Set("User-Agent", c.userAgent)
	if hasBody {
		h.Set("Content-Type", "application/json")
	}
	if actor := c.resolveActor(rc); actor != "" {
		h.Set("X-Actor-ID", actor)
	}
	// The pair is all-or-nothing by construction: WithNamespace refuses to set
	// half of it, and WithGlobalNamespace clears both. The server rejects a half
	// pair with a named 400, so this invariant is worth keeping on the way out.
	if rc.namespaceSet {
		h.Set(namespaceTypeHeader, rc.namespaceType)
		h.Set(namespaceIDHeader, rc.namespaceID)
	}
	return h
}

// mergeQuery folds option-contributed parameters into the query the resource
// method built, returning a new url.Values so the caller's map is never mutated
// (params.query() hands out a fresh map, but a caller-supplied one must not be
// written to behind their back).
func (rc *requestConfig) mergeQuery(query url.Values) (url.Values, error) {
	if !rc.applicationSet && !rc.includeGlobal {
		return query, nil
	}

	merged := make(url.Values, len(query)+2)
	for key, values := range query {
		merged[key] = append([]string(nil), values...)
	}
	if rc.applicationSet {
		// A contradiction between WithApplication and a *ListParams field is a
		// mistake in the call, not a precedence question: silently picking either
		// one would scope the request to an application the caller did not
		// unambiguously ask for.
		if existing := merged.Get(applicationIDParam); existing != "" && existing != rc.applicationID {
			return nil, fmt.Errorf("octonomy: WithApplication(%q) contradicts the %s already set on this request (%q); set it in one place", rc.applicationID, applicationIDParam, existing)
		}
		merged.Set(applicationIDParam, rc.applicationID)
	}
	if rc.includeGlobal {
		merged.Set(includeGlobalParam, "true")
	}
	return merged, nil
}

// checkScopeCoherence rejects a request whose own scoping options contradict
// each other, or the API version the client targets, before anything is sent.
//
// AGENTS.md EXEMPTION, recorded deliberately. AGENTS.md says "Do not encode
// server-side validation or invariants here", and that rule governs validating
// request DATA against server business rules -- whether a slug is taken, whether
// a tag may be assigned in an application. Nothing here does that. Every check
// below is about the COHERENCE OF THE CALLER'S OWN REQUEST: an option that
// cannot mean anything on the surface this client targets, or that the transport
// would otherwise drop on the floor. None consults resource state, and none can
// disagree with the server about a row.
//
// The distinction is load-bearing rather than a formality, because the server
// already rejects every one of these loudly and by name (probed against 3.1.0:
// namespace headers on v1 are a 400 namespace_not_supported; the reserved
// "global" type and a half pair are a 400 namespace_invalid; a namespaced read
// with no application is refused at the permission layer). These guards
// therefore buy a saved round trip and an error naming the SDK-level fix -- they
// are never the only thing standing between a caller and a bad write. That is
// why they may be this narrow and no wider: each one names an SDK symbol in its
// remediation, and a check that could only cite a server rule does not belong
// here.
//
// The one exception is WithIncludeGlobal on a write, which the server does not
// reject -- it ignores it. Silence is exactly the failure mode this SDK refuses
// elsewhere (#32), so the SDK makes it loud.
func (c *Client) checkScopeCoherence(method string, rc requestConfig, query url.Values) error {
	if rc.err != nil {
		return rc.err
	}

	if rc.namespaceSet && c.apiVersion != APIV2 {
		return fmt.Errorf("octonomy: WithNamespace requires the v2 API surface, but this client targets %s: set Config.APIVersion = APIV2", c.apiVersion)
	}

	if rc.includeGlobal {
		if c.apiVersion != APIV2 {
			return fmt.Errorf("octonomy: WithIncludeGlobal requires the v2 API surface, but this client targets %s: %s is meaningful only where a namespace axis exists", c.apiVersion, includeGlobalParam)
		}
		if !isReadMethod(method) {
			return fmt.Errorf("octonomy: WithIncludeGlobal applies to reads only, and this request is a %s: the server reads %s from the query string on safe methods and ignores it on writes, so sending it here would silently do nothing", method, includeGlobalParam)
		}
	}

	// Namespace isolation sits below application on the server, so a namespaced
	// request that names no application is refused. Reads take application scope
	// from the query string ALONE, which the transport can see in full -- so the
	// check is exact here and is made.
	//
	// Writes are deliberately left to the server. They may carry application_id
	// in the query or in the body (the server unions both), and the body is an
	// arbitrary caller type: reaching into it would mean either reflecting over
	// every write struct or an interface each future resource must remember to
	// implement, where forgetting silently disables the guard. A guard that can
	// fail open by omission is worse than no guard, and the server's rejection is
	// unambiguous, so this one stops at the query string.
	if rc.namespaceSet && isReadMethod(method) && query.Get(applicationIDParam) == "" {
		return fmt.Errorf("octonomy: a namespaced read must also name its application: add WithApplication(...), because namespace isolation sits below application on the server")
	}

	// scope=merchant (tag resolution) asks the server to resolve within the
	// request's namespace, so it contradicts a request that has none. Note the
	// asymmetry with the header rule above: scope=global is a LEGAL explicit pin
	// on the same parameter, even though "global" is a reserved namespace TYPE.
	// A flat "reject global everywhere" rule would break a valid call.
	if query.Get(scopeParam) == scopeMerchantValue && !rc.namespaceSet {
		return fmt.Errorf("octonomy: %s=%s resolves within the request's namespace, but this request has none: add WithNamespace(...), or use %s=global to pin the tenant-shared namespace", scopeParam, scopeMerchantValue, scopeParam)
	}

	return nil
}

// isReadMethod reports whether method is safe, i.e. carries no state change and
// takes all of its scoping from the URL.
func isReadMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

// readBounded reads at most limit bytes and reports ErrResponseTooLarge rather
// than truncating. Truncation would hand the decoders a body that is invalid
// JSON for an accidental reason, producing a decode error that names the wrong
// problem.
//
// io.LimitReader with a limit+1 probe rather than http.MaxBytesReader: the
// latter is server-side machinery. It takes an http.ResponseWriter in order to
// close the connection on overflow, and its error type is addressed to a request
// handler. Passing a nil writer works but reads as borrowing a server tool for a
// client job, and its sentinel is not one a caller of this package should have
// to know about.
func readBounded(r io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, ErrResponseTooLarge
	}
	return body, nil
}

// do performs a call whose 2xx response carries no payload the caller needs --
// DELETE, which Octonomy answers with 204 and an empty body on every resource
// (verified across tags, vocabularies, aliases, and assignments on server
// 3.1.0).
//
// It asserts that shape rather than ignoring the response. A 200 carrying
// {"data": ...} routed through here would otherwise succeed while discarding
// the payload, which is the same silent-success failure doData exists to
// prevent: the wrong helper for a method must not compile-and-pass. That makes
// this the mechanical form of a rule AGENTS.md already stated in prose on the
// compat line -- prose did not prevent the defect this fix exists for.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any, opts ...RequestOption) error {
	status, respBody, err := c.doRaw(ctx, method, path, query, body, opts...)
	if err != nil {
		return err
	}
	if status != http.StatusNoContent {
		return fmt.Errorf("octonomy: expected 204 No Content, got %d: this call returned a payload, so it needs doData or doList", status)
	}
	// Belt and braces: net/http discards a body on a 204, so this is a guard on
	// the contract rather than a branch a real server can reach today.
	if len(bytes.TrimSpace(respBody)) > 0 {
		return fmt.Errorf("octonomy: 204 response carried a %d-byte body, expected none", len(respBody))
	}
	return nil
}

// doData performs a call whose 2xx body is a SINGLE resource and unwraps the
// server's {"data": {...}} envelope into a *T.
//
// The server wraps every payload under "data": lists as
// {"data": [...], "pagination": {...}} and single resources as {"data": {...}}
// (octonomy/core/responses.py data_response, present since the server's first
// commit). The vendored docs/openapi.yaml documents neither wrapper, so this is
// the same spec-vs-server divergence as the list envelope, and the same rule
// applies -- follow the server.
//
// Decoding a wrapped body straight into a *Tag silently yields an empty struct
// with a nil error. So a body that does not carry the envelope is an error here,
// never a zero value.
func doData[T any](ctx context.Context, c *Client, method, path string, query url.Values, body any, opts ...RequestOption) (*T, error) {
	_, respBody, err := c.doRaw(ctx, method, path, query, body, opts...)
	if err != nil {
		return nil, err
	}
	data, _, err := decodeEnvelope(respBody)
	if err != nil {
		return nil, err
	}
	if string(data) == "null" {
		return nil, fmt.Errorf(`octonomy: response "data" envelope is null, expected a resource`)
	}
	var out T
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("octonomy: decode response data: %w", err)
	}
	return &out, nil
}

// doList performs a call whose 2xx body is a list envelope --
// {"data": [...], "pagination": {...}} -- and decodes it into a *List[T].
//
// The envelope is asserted before either key is decoded. Unmarshalling straight
// into a *List[T] makes every unexpected shape look like an empty page with a
// nil error: an empty body, {}, or a response whose data key the server renamed
// all yield Data == nil and Count == 0, and a caller cannot tell that from "this
// tenant has no tags". That is the same silent failure doData prevents, one type
// further out.
//
// BOTH keys are required, and pagination has to be usable rather than merely
// present. A missing, null, or {} pagination block decodes to a zero-valued
// Pagination -- Limit 0, Count 0, nil Next -- which a caller paging on Count
// reads as "one page, nothing after it". Limit is the field that tells those
// apart: the server's paginator resolves it through DRF's strict positive-int
// parse and falls back to default_limit 50, so a real response never carries
// Limit < 1 (octonomy/core/pagination.py). Count and Offset are legitimately 0
// on an empty first page and cannot carry this check.
//
// A present-but-null data ("data": null) normalizes to an empty non-nil slice,
// identical to "data": []. Both wire forms mean "no rows on this page", and
// callers should not have to handle two spellings of an empty page; range and
// len behave the same either way, so the distinction buys nothing.
func doList[T any](ctx context.Context, c *Client, method, path string, query url.Values, opts ...RequestOption) (*List[T], error) {
	_, respBody, err := c.doRaw(ctx, method, path, query, nil, opts...)
	if err != nil {
		return nil, err
	}
	data, pagination, err := decodeEnvelope(respBody)
	if err != nil {
		return nil, err
	}
	if pagination == nil || string(pagination) == "null" {
		return nil, fmt.Errorf(`octonomy: list response has no "pagination" block`)
	}

	out := &List[T]{}
	if err := json.Unmarshal(data, &out.Data); err != nil {
		return nil, fmt.Errorf("octonomy: decode response data: %w", err)
	}
	if out.Data == nil {
		out.Data = []T{}
	}
	if err := json.Unmarshal(pagination, &out.Pagination); err != nil {
		return nil, fmt.Errorf("octonomy: decode response pagination: %w", err)
	}
	if out.Pagination.Limit < 1 {
		return nil, fmt.Errorf(`octonomy: list response has an unusable "pagination" block (limit=%d), which would read as a single complete page`, out.Pagination.Limit)
	}
	return out, nil
}

// decodeEnvelope returns the raw bytes under the response's "data" and
// "pagination" keys. pagination is nil for a single resource, which carries none.
//
// The failures it reports are 2xx responses that would otherwise decode into a
// zero value with a nil error: an empty body where a payload was expected, and a
// body with no "data" key at all. Note "data": null is *present* -- each caller
// decides what null means for its shape.
func decodeEnvelope(body []byte) (data, pagination json.RawMessage, err error) {
	if len(body) == 0 {
		return nil, nil, fmt.Errorf("octonomy: empty response body where a payload was expected")
	}
	var envelope struct {
		Data       json.RawMessage `json:"data"`
		Pagination json.RawMessage `json:"pagination"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, nil, fmt.Errorf("octonomy: decode response body: %w", err)
	}
	if envelope.Data == nil {
		return nil, nil, fmt.Errorf(`octonomy: response body has no "data" envelope`)
	}
	return envelope.Data, envelope.Pagination, nil
}

func (c *Client) resolveActor(rc requestConfig) string {
	if rc.actorSet {
		return rc.actorID
	}
	return c.actorID
}

// parseError converts a non-2xx response into an *APIError.
//
// It takes the requested API version -- rather than only (status, body) -- so an
// envelope-less 404 can name the most likely cause. That is a signature the
// caller has to thread through, and it is deliberate: the alternative is a
// second guess at the call site, where the version is equally available but the
// mapping rule is not.
//
// The mapping rule has exactly two branches:
//
//   - WITH the Octonomy envelope, the server's own code is preserved verbatim,
//     including codes this SDK has no constant for.
//   - WITHOUT it, the response did not come from Octonomy's application layer at
//     all, and every such response gets CodeUnexpectedStatus.
//
// The second branch used to derive a semantic code from the status, which is
// what made an unrouted 404 satisfy IsNotFound and let a missing /api/v2 read as
// an empty taxonomy. Deriving semantics from a status this SDK did not generate
// cannot be made safe, so it is gone -- see CodeUnexpectedStatus.
func parseError(status int, body []byte, version APIVersion) error {
	var envelope struct {
		Error struct {
			Code      string         `json:"code"`
			Message   string         `json:"message"`
			Details   map[string]any `json:"details"`
			RequestID string         `json:"request_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Error.Code != "" {
		return &APIError{
			StatusCode: status,
			Code:       envelope.Error.Code,
			Message:    envelope.Error.Message,
			Details:    envelope.Error.Details,
			RequestID:  envelope.Error.RequestID,
		}
	}

	message := string(bytes.TrimSpace(body))
	if message == "" {
		message = http.StatusText(status)
	}
	return &APIError{
		StatusCode: status,
		Code:       CodeUnexpectedStatus,
		Message:    message + versionHint(status, version),
	}
}

// versionHint appends a diagnosis to an envelope-less 404 on the v2 surface,
// which is the shape a server predating /api/v2 returns for every call.
//
// That shape was verified rather than assumed. Before the version shim landed
// (server commit bd7bc62), the URLconf hardcoded path("api/v1/", ...) with no
// <version> capture and no ALLOWED_VERSIONS, so /api/v2/tags matches no pattern
// at all and Django's resolver answers before any DRF view or exception handler
// runs: 404 text/html, "<h1>Not Found</h1>", no envelope. Probed against a live
// 3.1.0 container via a path outside the URLconf, which reproduces it exactly.
//
// KNOWN LIMIT, worth stating because it bounds the guarantee. A server that DOES
// have the version shim but rejects the requested version answers through DRF
// instead, with an ENVELOPED 404 (code not_found, details "Invalid version in
// URL path.") -- also probed, via /api/v3 on 3.1.0. The envelope branch of
// parseError preserves that code verbatim, so IsNotFound would report true for
// it. The SDK never requests a version outside {v1, v2}, both of which every
// shimmed server allows, so it cannot reach that case today; it is not a hole
// the SDK can close, because a server sending code=not_found means not_found.
//
// Phrased as a possibility, never an assertion: an envelope-less 404 is also
// what a misconfigured BaseURL, a path-stripping proxy, or a genuinely absent
// resource behind a gateway produces. The hint names the one cause the SDK can
// do something about.
func versionHint(status int, version APIVersion) string {
	if status != http.StatusNotFound || version != APIV2 {
		return ""
	}
	return fmt.Sprintf(" [octonomy-go: this response carried no Octonomy error envelope, which is what a server that does not serve %s returns for every call; if this deployment predates Octonomy 3.0, set Config.APIVersion = APIV1]", APIV2.prefix())
}
