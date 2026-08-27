package octonomy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// RequestOption customizes a single request.
type RequestOption func(*requestConfig)

type requestConfig struct {
	actorID  string
	actorSet bool
}

// WithActor sets the X-Actor-ID header for one request, overriding Config.ActorID.
// Use it to attribute a mutation to a specific user or service in the audit log.
func WithActor(actorID string) RequestOption {
	return func(rc *requestConfig) {
		rc.actorID = actorID
		rc.actorSet = true
	}
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
// for a 2xx: it builds the URL under /api/v1, sets the auth and tenant headers,
// and JSON-encodes body when non-nil. Non-2xx responses become *APIError.
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

	endpoint := *c.baseURL
	endpoint.Path += apiPrefix + path
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

	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-Tenant-ID", c.tenantID)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if actor := c.resolveActor(rc); actor != "" {
		req.Header.Set("X-Actor-ID", actor)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("octonomy: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("octonomy: read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, respBody, parseError(resp.StatusCode, respBody)
	}
	return resp.StatusCode, respBody, nil
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

// parseError converts a non-2xx response into an *APIError. It decodes the
// standard Octonomy envelope when present and otherwise falls back to the raw
// body with a status-derived code.
func parseError(status int, body []byte) error {
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
		Code:       codeFromStatus(status),
		Message:    message,
	}
}
