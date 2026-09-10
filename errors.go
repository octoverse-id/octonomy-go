package octonomy

import (
	"errors"
	"fmt"
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
	CodeScopeImmutable      = "scope_immutable"

	// Namespace codes. Every one of these is reachable only on /api/v2 (see
	// APIVersion); v1 has no namespace axis.
	CodeNamespaceNotSupported    = "namespace_not_supported"
	CodeNamespaceInvalid         = "namespace_invalid"
	CodeNamespacedWritesDisabled = "namespaced_writes_disabled"
	CodeNamespaceAPIDisabled     = "namespace_api_disabled"

	CodeAmbiguousResolution = "ambiguous_resolution"
)

// CodeUnexpectedStatus is the code carried by an *APIError built from a non-2xx
// response that arrived WITHOUT the Octonomy error envelope: a proxy 502, a load
// balancer 503, an unrouted 404 from a server that does not serve the requested
// API version.
//
// It is deliberately not a semantic code, and that is the whole point. This
// package used to map such a response by status -- a bare 404 became
// CodeNotFound -- which made IsNotFound(err) report true for "this deployment
// has no /api/v2 route at all". A caller whose ordinary not-found branch treats
// that as "the tag does not exist" then sees an empty taxonomy and no error: a
// wrong answer, silently, from an infrastructure failure. Mapping by status
// cannot tell the two apart, so it no longer tries.
//
// An enveloped response keeps its real code, so a 503 namespace_api_disabled
// stays distinguishable from an infrastructure 503 that never reached Octonomy.
const CodeUnexpectedStatus = "unexpected_status"

// CodeNotReady is the code carried by an *APIError from a HEALTH PROBE that the
// server answered with a non-2xx AND its own {"status": ...} body: reachable,
// up, and reporting that it cannot serve. /health/ready answers
// 503 {"status": "unavailable"} when its database connection will not open.
//
// THIS IS NOT THE STATUS-TO-CODE MAPPING CodeUnexpectedStatus EXISTS TO FORBID.
// That rule bans INFERRING a semantic code from an HTTP status this SDK did not
// generate. Nothing is inferred here: the code is established by a
// server-authored payload, the health view's own {"status": ...} envelope, which
// is as much a response body as {"error": {...}} is. A non-2xx on a probe route
// whose body is NOT that shape gets CodeUnexpectedStatus like every other
// envelope-less response -- which is exactly what keeps "the application says it
// is unready" apart from "a load balancer answered 503 because nothing is
// behind it".
//
// The server never sends this string; it is the SDK's name for a state the
// server expresses as a status plus a body.
const CodeNotReady = "not_ready"

// APIError is returned for any non-2xx response from the Octonomy API. It carries
// the HTTP status alongside the server's error envelope so callers can branch on
// Code, inspect Details, and correlate logs via RequestID.
type APIError struct {
	StatusCode int
	Code       string
	Message    string
	Details    map[string]any
	RequestID  string

	// err is an underlying cause, set when the failure was detected before the
	// error envelope could be read -- today, only a response body the SDK
	// refused to read to completion. It keeps errors.Is working for that cause
	// without adding a second error type callers would have to know about.
	err error
}

func (e *APIError) Error() string {
	if e.RequestID != "" {
		return fmt.Sprintf("octonomy: %s (code=%s, status=%d, request_id=%s)",
			e.Message, e.Code, e.StatusCode, e.RequestID)
	}
	return fmt.Sprintf("octonomy: %s (code=%s, status=%d)", e.Message, e.Code, e.StatusCode)
}

// Unwrap returns the underlying cause, if any, so errors.Is reaches it. Most
// *APIError values wrap nothing: a decoded error envelope IS the error.
func (e *APIError) Unwrap() error { return e.err }

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
//
// It reports true only for a response that carried the Octonomy error envelope.
// A bare 404 from a proxy, or from a server with no route for the requested API
// version, yields CodeUnexpectedStatus instead -- see IsUnexpectedStatus.
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

// IsTenantMismatch reports whether err is a tenant_mismatch error: the requested
// row belongs to a different tenant than Config.TenantID.
func IsTenantMismatch(err error) bool { return hasCode(err, CodeTenantMismatch) }

// IsApplicationMismatch reports whether err is an application_mismatch error: a
// tag cannot be assigned in the application the request names.
func IsApplicationMismatch(err error) bool { return hasCode(err, CodeApplicationMismatch) }

// IsInactiveTag reports whether err is an inactive_tag error. Octonomy deletes
// tags by deactivating them, and an inactive tag cannot be assigned.
func IsInactiveTag(err error) bool { return hasCode(err, CodeInactiveTag) }

// IsScopeImmutable reports whether err is a scope_immutable error (409): a PATCH
// tried to change a row's scope -- application_id, namespace_type, or
// namespace_id.
//
// REMEDIATION IS TO RE-CREATE, NOT TO RETRY. Scope is fixed at creation. Moving
// a row between scopes would orphan the assignments, child tags, aliases, and
// vocabulary references attached to it under the old scope, and could hand
// merchant-private data to a different merchant, so the server refuses the move
// outright rather than cascading it. Create the row in the target scope and
// delete the original; no payload adjustment makes the PATCH succeed.
//
// APIError.Details names the offending fields, each mapped to its own message,
// so a caller can report which part of the scope it tried to move.
//
// IsConflict reports FALSE for it, and that is the trap. The server raises it
// as a subclass of its conflict error, so it carries 409 while its code is
// scope_immutable rather than conflict. A caller that keys on the STATUS --
// "409 means duplicate slug, pick another" -- retries a request that can never
// succeed; one that keys on IsConflict alone drops it into a generic error path
// with the remediation unread. Branch ORDER is not the issue: IsConflict
// compares the exact code and never matches this one whichever runs first.
//
// From this SDK only ApplicationID can raise it, on EITHER surface. The
// server's rule covers all three scope fields and its detail-PATCH view is
// shared by /api/v1 and /api/v2, so the raw REST API can reject any of them on
// either one. Namespace, though, is set by the X-Namespace-* headers rather
// than by the body, so TagUpdate, VocabularyUpdate and TagAliasUpdate carry no
// namespace field for a PATCH to move.
func IsScopeImmutable(err error) bool { return hasCode(err, CodeScopeImmutable) }

// IsNamespaceNotSupported reports whether err is a namespace_not_supported
// error: namespace headers were sent to /api/v1, which is global-only.
//
// Remediation is the caller's: set Config.APIVersion = APIV2. The SDK refuses
// this combination before issuing the request, so reaching this error means the
// namespace was applied by something other than WithNamespace.
func IsNamespaceNotSupported(err error) bool { return hasCode(err, CodeNamespaceNotSupported) }

// IsNamespaceInvalid reports whether err is a namespace_invalid error: the
// X-Namespace-* header pair is structurally unusable (blank, half a pair, the
// reserved "global" type, a folded/repeated header, or a value wider than the
// server's 100-character namespace column).
func IsNamespaceInvalid(err error) bool { return hasCode(err, CodeNamespaceInvalid) }

// IsNamespacedWritesDisabled reports whether err is a namespaced_writes_disabled
// error (403).
//
// This is an OPERATOR state, not a caller error: the deployment has
// NAMESPACE_WRITE_ENABLED off, which is its default, so it accepts namespaced
// reads and refuses namespaced writes. Retrying, changing the payload, or
// dropping the namespace will not help -- the fix is to enable the flag on the
// server (OCTONOMY_NAMESPACE_WRITE_ENABLED=true) or to write in the global
// namespace instead.
func IsNamespacedWritesDisabled(err error) bool { return hasCode(err, CodeNamespacedWritesDisabled) }

// IsNamespaceAPIDisabled reports whether err is a namespace_api_disabled error
// (503).
//
// Also an OPERATOR state rather than a caller error: the deployment has
// NAMESPACE_V2_API_ENABLED off, the first rung of the server's namespace
// rollback ladder, so the namespaced v2 surface is withdrawn while global v1 and
// v2 traffic keeps flowing. Treat it as "come back later", not as a bad request:
// a namespaced call cannot succeed until an operator re-enables the flag. Global
// (namespace-less) calls on the same client are unaffected.
func IsNamespaceAPIDisabled(err error) bool { return hasCode(err, CodeNamespaceAPIDisabled) }

// IsAmbiguousResolution reports whether err is an ambiguous_resolution error:
// two or more equally specific tags or aliases matched at the same resolution
// scope, so the server cannot deterministically pick one. Narrow the call with
// application_id, type, or an explicit scope.
func IsAmbiguousResolution(err error) bool { return hasCode(err, CodeAmbiguousResolution) }

// IsNotReady reports whether err is a health probe the server ANSWERED with a
// non-2xx and its own status payload: it is reachable and up, but not serving.
// APIError.Details carries the server's own word under "status".
//
// It is one half of a distinction health callers need and this SDK never
// collapses. The other half is an UNREACHABLE server, which produces no
// *APIError at all and matches errors.Is(err, ErrUnreachable). Not ready means
// back off and re-probe; unreachable means the process, the URL, or the network
// is the problem, and no amount of waiting on the database will fix it.
//
// It reports false for a 503 that did NOT come from the probe -- an HTML error
// page from a gateway is IsUnexpectedStatus -- so a true here is the
// application's own answer.
func IsNotReady(err error) bool { return hasCode(err, CodeNotReady) }

// IsUnexpectedStatus reports whether err is a non-2xx response that carried no
// Octonomy error envelope -- see CodeUnexpectedStatus.
//
// It means the failure did not come from Octonomy's application layer: a proxy,
// a load balancer, or a server that does not serve the API version this client
// targets. Inspect APIError.StatusCode and Message for the raw response.
func IsUnexpectedStatus(err error) bool { return hasCode(err, CodeUnexpectedStatus) }
