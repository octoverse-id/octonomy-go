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

// do performs a single API call: it builds the URL under /api/v1, sets the auth
// and tenant headers, JSON-encodes body (when non-nil), and decodes a 2xx
// response into out (when non-nil). Non-2xx responses become *APIError.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any, opts ...RequestOption) error {
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
			return fmt.Errorf("octonomy: encode request body: %w", err)
		}
		reqBody = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), reqBody)
	if err != nil {
		return fmt.Errorf("octonomy: build request: %w", err)
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
		return fmt.Errorf("octonomy: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("octonomy: read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return parseError(resp.StatusCode, respBody)
	}

	if out == nil || len(respBody) == 0 {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("octonomy: decode response body: %w", err)
	}
	return nil
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
