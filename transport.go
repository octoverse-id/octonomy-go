package octonomy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
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

// doRaw performs a single API call and returns the raw 2xx response body: it
// builds the URL under /api/v1, sets the auth and tenant headers, and
// JSON-encodes body when non-nil. Non-2xx responses become *APIError.
//
// Decoding belongs to the three callers below, because the shape depends on what
// was requested: do (no payload), doData (single resource), doList (list
// envelope). Keeping the decode out of here is what makes "a 2xx that carries
// nothing where a payload was expected" an error instead of a zero value.
func (c *Client) doRaw(ctx context.Context, method, path string, query url.Values, body interface{}, opts ...RequestOption) ([]byte, error) {
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
			return nil, fmt.Errorf("octonomy: encode request body: %w", err)
		}
		reqBody = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), reqBody)
	if err != nil {
		return nil, fmt.Errorf("octonomy: build request: %w", err)
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
		return nil, fmt.Errorf("octonomy: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("octonomy: read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseError(resp.StatusCode, respBody)
	}
	return respBody, nil
}

// do performs a call whose 2xx response carries no payload the caller needs --
// DELETE, which Octonomy answers with 204 and an empty body. Any body the server
// does send is ignored, deliberately: there is nothing to decode into.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body interface{}, opts ...RequestOption) error {
	_, err := c.doRaw(ctx, method, path, query, body, opts...)
	return err
}

// doData performs a call whose 2xx body is a SINGLE resource and unwraps the
// server's {"data": {...}} envelope into out.
//
// The server wraps every payload under "data": lists as
// {"data": [...], "pagination": {...}} and single resources as {"data": {...}}
// (octonomy/core/responses.py data_response, present since the server's first
// release). The vendored docs/openapi.yaml documents neither wrapper, so this is
// the same spec-vs-server divergence as the list envelope, and the same rule
// applies -- follow the server.
//
// Decoding a wrapped body straight into a *Tag silently yields an empty struct
// with a nil error, which is exactly how this went unnoticed until the compat
// line got a smoke test against a real server. So a body that does not carry the
// envelope is an error here, never a zero value.
func (c *Client) doData(ctx context.Context, method, path string, query url.Values, body, out interface{}, opts ...RequestOption) error {
	respBody, err := c.doRaw(ctx, method, path, query, body, opts...)
	if err != nil {
		return err
	}
	data, err := envelopeData(respBody)
	if err != nil {
		return err
	}
	if string(data) == "null" {
		return fmt.Errorf(`octonomy: response "data" envelope is null, expected a resource`)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("octonomy: decode response data: %w", err)
	}
	return nil
}

// doList performs a call whose 2xx body is a list envelope --
// {"data": [...], "pagination": {...}} -- and decodes the WHOLE body into out,
// because out (*TagList, *VocabularyList) maps both keys.
//
// The envelope is still asserted first. Decoding straight into a *TagList makes
// every unexpected shape look like an empty page with a nil error: an empty body,
// {}, or a response whose data key the server renamed all yield Data == nil and
// Count == 0, and a caller cannot tell that from "this tenant has no tags". That
// is the same silent failure doData exists to prevent, one type further out.
//
// A present-but-null data ("data": null) is accepted and decodes to a nil slice.
// The server sends [] for an empty page, and nil-versus-empty slice semantics are
// a deliberate open question on the modern line rather than something this frozen
// line should decide.
func (c *Client) doList(ctx context.Context, method, path string, query url.Values, out interface{}, opts ...RequestOption) error {
	respBody, err := c.doRaw(ctx, method, path, query, nil, opts...)
	if err != nil {
		return err
	}
	if _, err := envelopeData(respBody); err != nil {
		return err
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("octonomy: decode response body: %w", err)
	}
	return nil
}

// envelopeData returns the raw bytes under the response's "data" key.
//
// Both failure modes it reports are 2xx responses that would otherwise decode
// into a zero value with a nil error: an empty body where a payload was expected,
// and a body with no "data" key at all. Note "data": null is *present* -- callers
// decide what null means for their shape.
func envelopeData(body []byte) (json.RawMessage, error) {
	if len(body) == 0 {
		return nil, fmt.Errorf("octonomy: empty response body where a payload was expected")
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("octonomy: decode response body: %w", err)
	}
	if envelope.Data == nil {
		return nil, fmt.Errorf(`octonomy: response body has no "data" envelope`)
	}
	return envelope.Data, nil
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
			Code      string                 `json:"code"`
			Message   string                 `json:"message"`
			Details   map[string]interface{} `json:"details"`
			RequestID string                 `json:"request_id"`
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
