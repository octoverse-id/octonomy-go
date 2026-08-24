package octonomy

import (
	"errors"
	"fmt"
	"net/http"
)

// Error codes returned by the Octonomy API in the error envelope
// ({"error": {"code": ...}}). They mirror octonomy/core/errors.py on the server.
const (
	CodeValidation          = "validation_error"
	CodeAuthRequired        = "authentication_required"
	CodeForbidden           = "forbidden"
	CodeNotFound            = "not_found"
	CodeConflict            = "conflict"
	CodeTenantMismatch      = "tenant_mismatch"
	CodeApplicationMismatch = "application_mismatch"
	CodeInactiveTag         = "inactive_tag"
)

// APIError is returned for any non-2xx response from the Octonomy API. It carries
// the HTTP status alongside the server's error envelope so callers can branch on
// Code, inspect Details, and correlate logs via RequestID.
type APIError struct {
	StatusCode int
	Code       string
	Message    string
	Details    map[string]interface{}
	RequestID  string
}

func (e *APIError) Error() string {
	if e.RequestID != "" {
		return fmt.Sprintf("octonomy: %s (code=%s, status=%d, request_id=%s)",
			e.Message, e.Code, e.StatusCode, e.RequestID)
	}
	return fmt.Sprintf("octonomy: %s (code=%s, status=%d)", e.Message, e.Code, e.StatusCode)
}

// AsAPIError reports whether err wraps an *APIError and returns it when it does.
func AsAPIError(err error) (*APIError, bool) {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr, true
	}
	return nil, false
}

func hasCode(err error, code string) bool {
	apiErr, ok := AsAPIError(err)
	return ok && apiErr.Code == code
}

// IsNotFound reports whether err is an Octonomy not_found error.
func IsNotFound(err error) bool { return hasCode(err, CodeNotFound) }

// IsConflict reports whether err is an Octonomy conflict error (e.g. a duplicate
// slug or an idempotency clash).
func IsConflict(err error) bool { return hasCode(err, CodeConflict) }

// IsValidation reports whether err is an Octonomy validation_error.
func IsValidation(err error) bool { return hasCode(err, CodeValidation) }

// IsAuthError reports whether err is an authentication_required error.
func IsAuthError(err error) bool { return hasCode(err, CodeAuthRequired) }

// IsForbidden reports whether err is a forbidden (insufficient scope) error.
func IsForbidden(err error) bool { return hasCode(err, CodeForbidden) }

// codeFromStatus provides a best-effort error code when a non-2xx response does
// not carry the standard Octonomy error envelope (e.g. a gateway 502).
func codeFromStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return CodeValidation
	case http.StatusUnauthorized:
		return CodeAuthRequired
	case http.StatusForbidden:
		return CodeForbidden
	case http.StatusNotFound:
		return CodeNotFound
	case http.StatusConflict:
		return CodeConflict
	default:
		return ""
	}
}
