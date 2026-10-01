package octonomy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
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

	// requestIDHeader carries the caller's OWN correlation id, and only when the
	// caller supplied one -- see WithRequestID for where the server puts it and
	// why the SDK never mints one itself.
	requestIDHeader = "X-Request-ID"

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

// ErrUnreachable marks a failure in which NO USABLE HTTP RESPONSE WAS RECEIVED:
// the request never completed, so there is no status and no body to classify.
// Connection refused, DNS failure, a TLS handshake failure, a client timeout, and
// a cancelled context all land here.
//
// So does a redirect chain the client refused to follow, which is the one case
// where net/http returns a response alongside its error -- and it has already
// closed that body, and documents the response as ignorable. A 302 the SDK
// declined to follow is not the server answering this request, so it is not
// given a status classification either; the cause names itself in the message
// ("stopped after 10 redirects").
//
// It is what separates "the server is gone" from "the server answered and said
// no". The latter is always an *APIError -- AsAPIError finds it, and on a health
// probe IsNotReady names it exactly -- while this sentinel is reachable only when
// nothing answered at all. The two call for different operator responses, so the
// SDK never collapses them.
//
// Wrapping preserves the cause, so the narrower checks still work alongside it:
// errors.Is(err, context.DeadlineExceeded) and errors.As into *net.OpError both
// reach through. Its own message is the "octonomy: request failed" prefix these
// errors have always carried.
var ErrUnreachable = errors.New("octonomy: request failed")

// unreachableError is how a transport failure carries BOTH ErrUnreachable and
// its cause. errors.Is(err, ErrUnreachable) is answered by Is, and
// errors.Is(err, context.Canceled) -- or errors.As into *net.OpError -- by
// following Unwrap to the cause.
//
// It is a type rather than fmt.Errorf("%w: %w", ErrUnreachable, err), which is
// how the /v2 module spells it, because that spelling needs Go 1.20. Before
// 1.20, fmt.Errorf with two %w verbs returns an error with NO Unwrap method at
// all, so errors.Is finds neither the sentinel nor the cause -- and it compiles
// and vets clean on Go 1.13, so nothing but a test notices. "%w: %v" is no fix
// either: it keeps one of the two and flattens the other into text, and a
// caller needs both to tell a cancellation from a refused connection.
type unreachableError struct {
	cause error
}

func (e *unreachableError) Error() string { return ErrUnreachable.Error() + ": " + e.cause.Error() }

// Unwrap returns the transport failure, so errors.Is and errors.As reach it.
func (e *unreachableError) Unwrap() error { return e.cause }

// Is reports the sentinel this error stands for. errors.Is consults it before
// unwrapping, so the cause does not have to be ErrUnreachable for the sentinel
// to match.
func (e *unreachableError) Is(target error) bool { return target == ErrUnreachable }

// RequestOption customizes a single request.
//
// Options contribute headers (WithActor, WithRequestID, WithNamespace), query
// parameters (WithApplication, WithIncludeGlobal), or both, so they are resolved
// before the URL is built rather than layered on afterwards. Every option is
// per-call: a Client holds no scoping state, which is what lets goroutines
// sharing one Client target different namespaces concurrently.
type RequestOption func(*requestConfig)

type requestConfig struct {
	actorID  string
	actorSet bool

	requestID    string
	requestIDSet bool

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

// WithRequestID sets the X-Request-ID header for one request, threading the
// caller's own correlation id through everything the server records about that
// request.
//
// The server reads the header when it is present and mints req_<uuid> when it is
// not (core/middleware.py), then carries whichever id it holds into the places
// it records the request:
//
//   - the audit row written for the mutation
//   - the outbox / webhook event envelope -- its request_id field, and the
//     X-Octonomy-Request-ID header on the delivered webhook
//   - the server's structured request log
//   - the error envelope of a failed call -- APIError.RequestID
//
// Supplying the id is what joins those to the caller's own logs. Without it
// they are internally consistent and unreachable from outside: Octonomy holds
// one id, the calling service holds another, and nothing joins them.
//
// THE SDK NEVER MINTS ONE. No header is sent unless this option is used, which
// leaves the server's own minting intact. A client-side id would replace a
// server-generated one the caller can at least find in an error envelope with
// one that was never surfaced anywhere -- strictly worse than sending none.
//
// For the same reason there is no Config field. A request id names ONE request,
// so a client-level default would stamp every call the process makes with a
// single value and correlate nothing, while looking exactly like it worked.
//
// It composes with WithActor rather than replacing it -- actor is WHO, request
// id is WHICH CALL, and a mutation usually wants both -- and applies to every
// method that takes a RequestOption, since it is set at the one chokepoint they
// all share. The health probes are the exception: they take no options at all,
// and HealthService records why this one is excluded with the rest. Order does
// not matter.
//
// Repeating it overrides, unlike the scope options: the axes those guard are
// tenant, namespace, and application, where last-wins is a silent wrong-scope
// read; the worst a wrong id can do is mislabel a log line, and overriding one
// held in a shared []RequestOption is the same act WithActor already allows.
//
// ON THE SUCCESS PATH THE SERVER'S ID IS NOT SURFACED. Methods return
// (*T, error), the transport discards the response headers, and a Client holds
// no per-call state to park one in. That is not a gap this option leaves open:
// a caller who passes an id already has it. Generate one per outbound call
// (a UUID, or the trace id you already carry) and log it on your side.
//
// The id must be non-blank printable ASCII with no leading or trailing
// whitespace; see the guard below for what each of those prevents.
//
// KEEP IT SHORT. The server stores it in a 100-character column (audit/models.py
// and events/models.py), and a longer id fails the row insert: probed against
// 3.1.0 on Postgres, a 150-character id answers 500 with a bare HTML body -- no
// error envelope, so it reaches a caller as CodeUnexpectedStatus naming nothing
// -- and the mutation is rolled back whole rather than committed without its
// audit row. A uuid is 36 characters and a W3C traceparent is 55.
//
// That width is a SERVER rule -- a column, which a later release may widen --
// so this client does not enforce it. A cap here would become a false rejection
// of a legal id the moment the server changed, which is the drift AGENTS.md
// keeps server invariants out of this package to avoid. The guards below are a
// different thing: they are the wire grammar of an HTTP header, which no server
// release can widen.
func WithRequestID(id string) RequestOption {
	return func(rc *requestConfig) {
		if strings.TrimSpace(id) == "" {
			rc.fail(fmt.Errorf("octonomy: WithRequestID: request id is required; omit the option to let the server mint one"))
			return
		}
		// NOT a server-rule check. This is the wire grammar of a header value,
		// plus the one property a correlation id has to keep: being matched by
		// string EQUALITY at the far end.
		//
		// A control byte is refused by net/http itself, at write time, inside
		// httpClient.Do -- so doRaw would wrap it in ErrUnreachable, a sentinel
		// documented to mean nothing answered, for a request that was never sent.
		// The most likely way to get one is not exotic: an id read from a file or
		// an environment variable with its trailing newline still attached.
		//
		// A byte above 0x7e is worse, because it is ACCEPTED. The header goes out
		// verbatim and the server's WSGI layer decodes it as latin-1 (PEP 3333),
		// so a UTF-8 id lands in the audit row as mojibake that no longer equals
		// the one the caller logged: probed against 3.1.0, "req-café" is stored
		// as "req-cafÃ©". Correlation is lost with a 201 and no error anywhere,
		// and silence is the failure mode this SDK refuses.
		for i := 0; i < len(id); i++ {
			if b := id[i]; b < 0x20 || b > 0x7e {
				rc.fail(fmt.Errorf("octonomy: WithRequestID(%q): byte %#02x at offset %d is not printable ASCII; a request id travels in an HTTP header and is matched by string equality in the audit log, the event envelope, and the server's logs, so it must be printable ASCII (a UUID, or your own trace id)", id, b, i))
				return
			}
		}
		// Outer whitespace is the third way the id the caller logs and the id the
		// server stores can differ, and the only one that is not visibly wrong:
		// a space IS printable ASCII, so the loop above accepts it, and then
		// net/http TRIMS it while writing the header. Trimming it here instead
		// would send a legal request and still break equality, since the caller's
		// own logs keep the untrimmed string. So it is refused, and the message
		// names the fix.
		if trimmed := strings.TrimSpace(id); trimmed != id {
			rc.fail(fmt.Errorf("octonomy: WithRequestID(%q): a request id may not begin or end with whitespace; net/http trims it on the way out, so the server would record %q and no longer match the id you logged -- pass the trimmed value", id, trimmed))
			return
		}
		rc.requestID, rc.requestIDSet = id, true
	}
}

// WithNamespace scopes one request to a merchant or sub-tenant namespace by
// sending the X-Namespace-Type / X-Namespace-ID header pair. It requires
// Config.APIVersion = APIV2; /api/v1 is global-only, and on this line APIV1 is
// the default, so a namespaced client has to opt in explicitly.
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
		case rc.namespaceSet && (rc.namespaceType != nsType || rc.namespaceID != nsID):
			// Two different namespaces on one request is a contradiction, not a
			// precedence question. Last-wins would silently send whichever the
			// caller wrote second -- and on THIS axis that is a cross-merchant
			// read, the exact failure the no-Config-field design exists to
			// prevent. WithGlobalNamespace still clears: an explicit cancel is a
			// different act from naming two merchants and hoping.
			rc.fail(fmt.Errorf("octonomy: WithNamespace(%q, %q) contradicts the namespace already set on this request (%q, %q); set it in one place, or clear it with WithGlobalNamespace first", nsType, nsID, rc.namespaceType, rc.namespaceID))
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
// order, so a WithGlobalNamespace after a WithNamespace clears it, and a
// WithNamespace after a WithGlobalNamespace sets it.
//
// This is the ONE way to change an already-set namespace. Naming a second,
// different namespace with WithNamespace is an error rather than an override --
// cancelling deliberately and contradicting yourself are different acts, and
// only the first is legible at the call site.
//
// It is valid on either API version: sending no namespace headers is a legal
// request on /api/v1 too.
func WithGlobalNamespace() RequestOption {
	return func(rc *requestConfig) {
		rc.namespaceType, rc.namespaceID, rc.namespaceSet = "", "", false
	}
}

// WithApplication scopes one request to an application via the application_id
// query parameter.
//
// BODYLESS REQUESTS ONLY -- Get, Delete, and any List. Those take application
// scope from the query string alone, so this is the only way to supply it, and
// it is required on a namespaced one. Setting a value that contradicts one
// already in the request is an error rather than a silent overwrite.
//
// On a POST or PATCH it is REFUSED, because the query is not authoritative
// there. Probed against 3.1.0, the query value persists on a namespaced create
// but is DROPPED on a global one (core/selectors.py create_payload_with_scope
// returns early for global scope: "NULL = shared is valid there"), and a body
// application_id beats the query in both. So on a body-carrying write this
// option would be honored, ignored, or overridden depending on request scope --
// and the ignored case is the dangerous one: a caller asking for application
// scope silently gets a tenant-SHARED row. Write bodies name their own
// application through the ApplicationID field, which is authoritative in every
// case.
func WithApplication(applicationID string) RequestOption {
	return func(rc *requestConfig) {
		if strings.TrimSpace(applicationID) == "" {
			rc.fail(fmt.Errorf("octonomy: WithApplication: application id is required; omit the option for a tenant-wide request"))
			return
		}
		// Same rule as the *ListParams conflict in mergeQuery: a method with no
		// params struct (Get, Delete) has no query value to contradict, so
		// option-versus-option is the only remaining way to change application
		// scope silently.
		if rc.applicationSet && rc.applicationID != applicationID {
			rc.fail(fmt.Errorf("octonomy: WithApplication(%q) contradicts the application already set on this request (%q); set it in one place", applicationID, rc.applicationID))
			return
		}
		rc.applicationID, rc.applicationSet = applicationID, true
	}
}

// WithIncludeGlobal asks a namespaced read to also return the global
// (tenant-shared) rows the caller is authorized for, instead of the namespace's
// rows alone. It requires Config.APIVersion = APIV2, the only surface with a
// namespace axis.
//
// It is fail-closed on the server: it widens what the request ASKS for, and a
// token holding an exact merchant grant with no global authority still sees no
// global rows.
//
// Reads only. The server takes include_global from the query string on safe
// methods and ignores it entirely on writes, so applying it to a create or an
// update is refused here rather than being sent and quietly doing nothing.
//
// It is refused for the same reason alongside a scope=merchant query, which the
// tag-resolution route reads as "resolve within the request's namespace" and
// which makes the server discard include_global outright. Pairing it with
// scope=global is allowed but redundant: that scope is itself the authorization
// opt-in on the resolution route.
func WithIncludeGlobal() RequestOption {
	return func(rc *requestConfig) { rc.includeGlobal = true }
}

// The transport is one request path and three decoders. Pick the decoder by the
// shape of the 2xx body, never by convenience:
//
//	                      ┌─────────────────────────────────────────┐
//	resource method ─────▶│ doRaw  build URL, headers, body; send;   │
//	                      │        non-2xx → *APIError               │
//	                      │        2xx → raw body                    │
//	                      └───────────────┬─────────────────────────┘
//	                                      │
//	        ┌─────────────────────────────┼─────────────────────────────┐
//	        ▼                             ▼                             ▼
//	  (c *Client) do            (c *Client) doData            (c *Client) doList
//	  no payload to decode      single resource               list envelope
//	  DELETE → 204, no body     {"data": {…}}                 {"data": […],
//	                            unwraps into out               "pagination": {…}}
//	                                                          into a *TagList, …
//
// Every branch that could otherwise return a zero value with a nil error is an
// error instead. That is the whole point of the split: decoding a wrapped body
// straight into a *Tag yields an empty struct and no error, which is how the
// missing unwrap survived a full unit suite and was found only by a smoke test
// against a real server.
//
// The decoders take an `out` argument rather than returning a typed value
// because this line has no type parameters (Go 1.18). That costs the compile-time
// tie between a method and the type it decodes, so the envelope and identity
// assertions below are what catch a mis-routed method, at runtime.
//
// Every JSON decode of a response body goes through decodeJSON (jsondepth.go),
// which bounds nesting depth before encoding/json sees the bytes. Go 1.13's
// decoder has no depth limit of its own.

// doRaw performs a single API call and returns the raw 2xx response body: it
// builds the URL under the client's /api/<version> prefix, sets the auth, tenant,
// and scoping headers, and JSON-encodes body when non-nil. Non-2xx responses
// become *APIError.
//
// Decoding belongs to the three callers above, because the shape depends on what
// was requested. Keeping the decode out of here is what makes "a 2xx that
// carries nothing where a payload was expected" an error instead of a zero
// value. Resource files must not call doRaw directly.
func (c *Client) doRaw(ctx context.Context, method, path string, query url.Values, body interface{}, opts ...RequestOption) ([]byte, error) {
	// A probe client (NewHealthClient) holds no token and no tenant. Nothing
	// exported can route an API call through one -- HealthClient exposes only
	// Health, and every service field lives on a Client that New built -- so
	// this is an invariant guard rather than a branch a caller can reach. It is
	// here because the failure it prevents is silent: the header assembly below
	// would send "Authorization: Bearer " and an empty X-Tenant-ID, a well-formed
	// request that no server can attribute to anyone.
	if c.probeOnly {
		return nil, fmt.Errorf("octonomy: this client was built by NewHealthClient and carries no credentials, so it can reach only the health probes; use New for API calls")
	}

	var rc requestConfig
	for i, opt := range opts {
		// The library never panics; it returns errors. A nil option reaches here
		// from a caller assembling a slice conditionally, and calling it would
		// take the process down over a mistake in one argument.
		if opt == nil {
			return nil, fmt.Errorf("octonomy: RequestOption %d is nil; omit it rather than passing a nil option", i)
		}
		opt(&rc)
	}

	// An option that failed to construct is reported before anything derived
	// from the options is used. Order matters: mergeQuery can raise a
	// contradiction of its own, and running it first would report THAT while the
	// caller's actual first mistake -- the one requestConfig recorded and
	// promises to surface -- stayed hidden, pointing the remediation at the wrong
	// argument.
	if rc.err != nil {
		return nil, rc.err
	}

	query, err := rc.mergeQuery(query)
	if err != nil {
		return nil, err
	}
	if err := c.checkScopeCoherence(method, rc, query, body != nil); err != nil {
		return nil, err
	}

	endpoint, err := c.resolvePath(path)
	if err != nil {
		return nil, err
	}
	if len(query) > 0 {
		endpoint.RawQuery = query.Encode()
	}

	var reqBody io.Reader
	if body != nil {
		// Before json.Marshal, which on Go 1.13 has no cycle detection: a
		// Metadata map that contains itself recurses until the goroutine's stack
		// is exhausted, which is a fatal runtime error rather than a recoverable
		// panic. See checkBodyDepth.
		if err := checkBodyDepth(body); err != nil {
			return nil, fmt.Errorf("octonomy: encode request body: %w", err)
		}
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
	req.Header = c.headers(rc, body != nil)

	// The response is ignored on error, per net/http's contract -- see the note
	// on the same call in doUnversioned.
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &unreachableError{cause: err}
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := readBounded(resp.Body, c.maxResponseBytes)
	if err != nil {
		// A non-2xx whose body could not be read is still a non-2xx, and the
		// status is the part the caller branches on. Returning a bare read error
		// here would drop an entire class of failures out of *APIError -- a 503
		// behind a proxy that dumps a 40 MiB error page would stop satisfying
		// AsAPIError and IsUnexpectedStatus, silently, while every other 503 kept
		// working. The read failure is not lost: it is wrapped, so errors.Is
		// still finds ErrResponseTooLarge.
		//
		// A 2xx is different and stays a plain read error: there is no status
		// classification worth preserving when the status said success and the
		// payload is the thing that failed.
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, unreadableBodyError(resp.StatusCode, err)
		}
		return nil, fmt.Errorf("octonomy: read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseError(resp.StatusCode, respBody, versionHint(resp.StatusCode, c.apiVersion))
	}
	return respBody, nil
}

// doUnversioned performs a GET against a route that sits OUTSIDE
// /api/<version> and authenticates nobody -- the two health probes, and nothing
// else.
//
// IT IS A SEPARATE FUNCTION RATHER THAN A FLAG ON doRaw. Threading a skipAuth
// or skipPrefix bool through doRaw would put a credential-suppressing branch in
// the middle of the one path every tenant-scoped call takes, on top of the
// version and scope logic already there. Here the absence of auth is the whole
// function and cannot be reached by accident.
//
// It sends NO Authorization, NO X-Tenant-ID, no namespace headers, and no actor,
// from EITHER entry point. A probe from a fully configured Client is byte-for-byte
// the one a credential-free HealthClient sends, which is what lets Client.Health
// and NewHealthClient share this code and one set of tests. Request options are
// not accepted at all -- see HealthService.
//
// IT RETURNS THE STATUS AND BODY FOR EVERY RESPONSE, converting only a transport
// or read failure into an error, and leaves the non-2xx classification to its
// caller. On /health/ready a 503 is a documented ANSWER -- reachable but not
// serving -- rather than a failure of the request, and routing it through
// parseError here would flatten it into CodeUnexpectedStatus alongside the 503 a
// load balancer emits when nothing is behind it. HealthService.probe makes that
// split on the body.
func (c *Client) doUnversioned(ctx context.Context, path string) (int, []byte, error) {
	endpoint, err := c.joinPath(path)
	if err != nil {
		return 0, nil, err
	}

	// USERINFO ON THE BASE URL WOULD BECOME AN Authorization HEADER. net/http
	// adds "Authorization: Basic ..." itself for any request whose URL carries
	// userinfo and whose Authorization header is empty (net/http.Client.send) --
	// and empty is exactly what this function promises. So a caller who wrote
	// https://user:pass@octonomy.example.com would send credentials to the one
	// route in this package documented to carry none.
	//
	// Stripping it costs nothing elsewhere: on the versioned path c.headers
	// always sets Authorization: Bearer, so net/http never reaches its userinfo
	// branch and a userinfo base URL has never authenticated anything here.
	endpoint.User = nil

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return 0, nil, fmt.Errorf("octonomy: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)

	// resp is ignored on error by net/http's own contract: "On error, any
	// Response can be ignored. A non-nil Response with a non-nil error only
	// occurs when CheckRedirect fails, and even then the returned
	// Response.Body is already closed." There is nothing left to read and no
	// final status to classify -- a redirect chain the client refused to follow
	// is not an answer to this request -- and the cause names itself in the
	// message ("stopped after 10 redirects").
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, nil, &unreachableError{cause: err}
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := readBounded(resp.Body, c.maxResponseBytes)
	if err != nil {
		// Same rule as doRaw: a non-2xx whose body could not be read is still a
		// non-2xx, and the status is the part the caller branches on.
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return resp.StatusCode, nil, unreadableBodyError(resp.StatusCode, err)
		}
		return resp.StatusCode, nil, fmt.Errorf("octonomy: read response body: %w", err)
	}
	return resp.StatusCode, body, nil
}

// resolvePath joins the client's base URL, the /api/<version> prefix, and an
// already-escaped resource path, setting BOTH halves of url.URL's path pair.
//
// Resource methods hand in segments escaped with url.PathEscape, and url.URL
// keeps the path twice: Path holds the DECODED form and RawPath the escaped one.
// Assigning an escaped string to Path alone -- which this line's first release
// did -- means String() escapes it a second time, so an id of "ord 9" left as "ord%209" went
// out as "ord%25209" and reached the server as the literal "ord%209". Every id
// was addressed correctly right up until one contained a character that needed
// escaping. Tag and vocabulary ids are server-minted uuids, which never do, so
// this was latent here; the resource groups still to be ported address rows by
// caller-chosen external identifiers, where it would not be.
//
// Setting both fields consistently makes EscapedPath() return RawPath verbatim
// -- it does so whenever RawPath is a valid encoding of Path -- so each segment
// is escaped exactly once.
func (c *Client) resolvePath(path string) (*url.URL, error) {
	return c.joinPath(c.apiVersion.prefix() + path)
}

// joinPath appends an already-escaped, absolute path to the client's base URL,
// keeping url.URL's Path/RawPath pair consistent for the reason resolvePath
// records above.
//
// It is shared with doUnversioned, which appends a root-level path with no
// version prefix. The escaping rule is the same for both and belongs in one
// place: the health probes are constant paths, but a second copy of this is a
// second chance to set only one half of the pair.
func (c *Client) joinPath(escaped string) (*url.URL, error) {
	decoded, err := url.PathUnescape(escaped)
	if err != nil {
		return nil, fmt.Errorf("octonomy: invalid request path %q: %w", escaped, err)
	}
	endpoint := *c.baseURL
	endpoint.RawPath = endpoint.EscapedPath() + escaped
	endpoint.Path += decoded
	return &endpoint, nil
}

// headers assembles the outbound header set.
//
// Assembling into a fresh http.Header and assigning it wholesale means the
// header set is a value derived from (client, request config) rather than a
// sequence of mutations on a request.
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
	// ONLY when the caller supplied one. Sending a client-minted id here would
	// bypass the server's own minting for no benefit, and sending an empty header
	// would be worse than sending none: the server treats a blank value as
	// absent and mints anyway, so the request would carry a header that means
	// nothing. WithRequestID refuses a blank id for that reason.
	if rc.requestIDSet {
		h.Set(requestIDHeader, rc.requestID)
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
		//
		// Key presence, not Get() != "": a params field explicitly set to "" is
		// PRESENT, and comparing against the empty string reads that as absent --
		// so the option would overwrite it instead of reporting the
		// contradiction. (url.Values.Has would say this, and needs Go 1.17.)
		if _, present := merged[applicationIDParam]; present {
			if existing := merged.Get(applicationIDParam); existing != rc.applicationID {
				return nil, fmt.Errorf("octonomy: WithApplication(%q) contradicts the %s already set on this request (%q); set it in one place", rc.applicationID, applicationIDParam, existing)
			}
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
// elsewhere, so the SDK makes it loud.
//
// Option-construction failures (rc.err) are NOT checked here: doRaw reports them
// before it calls mergeQuery, because a merge-time contradiction would otherwise
// mask the earlier one. By the time this runs, rc holds no recorded failure.
func (c *Client) checkScopeCoherence(method string, rc requestConfig, query url.Values, hasBody bool) error {
	if rc.namespaceSet && c.apiVersion != APIV2 {
		return fmt.Errorf("octonomy: WithNamespace requires the v2 API surface, but this client targets %s: set Config.APIVersion = APIV2", c.apiVersion)
	}

	// Application scope on a body-carrying write belongs to the body. The query
	// value is not authoritative there: the server drops it on a global create
	// and lets a body value beat it, so honoring this option would sometimes
	// scope the row, sometimes silently leave it tenant-shared. Same family as
	// WithIncludeGlobal below -- an option the server may quietly ignore is
	// refused here rather than sent to do nothing.
	if rc.applicationSet && hasBody {
		return fmt.Errorf("octonomy: WithApplication does not apply to a %s, which carries a body: set the ApplicationID field of the request body instead, because the server takes a write's application scope from the body and may ignore the query parameter entirely", method)
	}

	if rc.includeGlobal {
		if c.apiVersion != APIV2 {
			return fmt.Errorf("octonomy: WithIncludeGlobal requires the v2 API surface, but this client targets %s: %s is meaningful only where a namespace axis exists, so set Config.APIVersion = APIV2", c.apiVersion, includeGlobalParam)
		}
		if !isReadMethod(method) {
			return fmt.Errorf("octonomy: WithIncludeGlobal applies to reads only, and this request is a %s: the server reads %s from the query string on safe methods and ignores it on writes, so sending it here would silently do nothing", method, includeGlobalParam)
		}
	}

	// Namespace isolation sits below application on the server, so a namespaced
	// request that names no application is refused.
	//
	// The criterion for checking it here is whether the transport can see ALL of
	// the request's application scope, which is exactly "this request carries no
	// body": then the query string is the whole story and the check is complete.
	// That covers GET and HEAD, and it covers DELETE -- a bodyless DELETE is
	// precisely the case where the query is all there is, and Tags.Delete offers
	// no other way to supply an application.
	//
	// Body-carrying writes are deliberately left to the server. Their
	// application lives in the body -- WithApplication is refused on them above,
	// precisely because the query is not authoritative there -- and the body is
	// an arbitrary caller type: reaching into it would mean reflecting over
	// every write struct, or an interface each future resource must remember to
	// implement, where forgetting silently disables the guard. A guard that
	// fails open by omission is worse than no guard, and the server's rejection
	// here is an unambiguous 403 naming the namespace grant (probed).
	//
	// Non-blank, not merely present: the two application checks in this file ask
	// different questions and must not share a test. The contradiction check in
	// mergeQuery asks "was a value SET?", so key presence is exactly right there
	// -- a params field set to "" is a value the caller chose. This one asks "did
	// the caller name a usable application?", and "" or "   " names none.
	if rc.namespaceSet && !hasBody && strings.TrimSpace(query.Get(applicationIDParam)) == "" {
		return fmt.Errorf("octonomy: a namespaced %s must also name its application: add WithApplication(...), because namespace isolation sits below application on the server", method)
	}

	// scope=merchant (tag resolution) asks the server to resolve within the
	// request's namespace, so it contradicts a request that has none. Note the
	// asymmetry with the header rule above: scope=global is a LEGAL explicit pin
	// on the same parameter, even though "global" is a reserved namespace TYPE.
	// A flat "reject global everywhere" rule would break a valid call.
	if query.Get(scopeParam) == scopeMerchantValue && !rc.namespaceSet {
		return fmt.Errorf("octonomy: %s=%s resolves within the request's namespace, but this request has none: add WithNamespace(...), or use %s=global to pin the tenant-shared namespace", scopeParam, scopeMerchantValue, scopeParam)
	}

	// scope=merchant and WithIncludeGlobal ask for opposite things: one pins
	// resolution to the request's namespace, the other widens it to the global
	// rows too. The server does not report the contradiction -- it resolves it
	// silently in favor of the scope, discarding include_global outright. So a
	// caller who wrote WithIncludeGlobal would get merchant-only results and no
	// indication the option did nothing.
	//
	// Same rule and same reason as WithIncludeGlobal on a write, above: an
	// option the server quietly ignores is refused here rather than sent to do
	// nothing. Ordered AFTER the namespace check so a request missing both gets
	// told about the more fundamental problem first.
	if rc.includeGlobal && query.Get(scopeParam) == scopeMerchantValue {
		return fmt.Errorf("octonomy: WithIncludeGlobal contradicts %s=%s, which resolves within the request's namespace and excludes global rows by definition: the server would drop the option silently, so drop one of the two -- omit WithIncludeGlobal to stay inside the namespace, or omit the merchant scope to let global rows back in", scopeParam, scopeMerchantValue)
	}

	return nil
}

// isReadMethod reports whether method is safe. It gates include_global, which
// the server reads from the query only on safe methods -- a narrower question
// than "can the transport see the whole request", which the application guard
// above answers with hasBody instead.
func isReadMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

// readBounded reads at most limit bytes and reports ErrResponseTooLarge rather
// than truncating. Truncation would hand the decoders a body that is invalid
// JSON for an accidental reason, producing a decode error that names the wrong
// problem.
//
// It replaces an unbounded ioutil.ReadAll, which let one response grow the
// client's memory without limit.
//
// io.LimitReader with a limit+1 probe rather than http.MaxBytesReader: the
// latter is server-side machinery. It takes an http.ResponseWriter in order to
// close the connection on overflow, and its error is addressed to a request
// handler. ioutil.ReadAll rather than io.ReadAll, which needs Go 1.16.
func readBounded(r io.Reader, limit int64) ([]byte, error) {
	body, err := ioutil.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, ErrResponseTooLarge
	}
	return body, nil
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
// release). Neither vendored spec documents either wrapper, so this is
// the same spec-vs-server divergence as the list envelope, and the same rule
// applies -- follow the server.
//
// Decoding a wrapped body straight into a *Tag silently yields an empty struct
// with a nil error, which is exactly how this went unnoticed until the compat
// line got a smoke test against a real server. So a body that does not carry the
// envelope is an error here, never a zero value -- and so is an envelope whose
// contents are not a resource (requireResourceObject), or decode to one with no
// identity (requireIdentity).
func (c *Client) doData(ctx context.Context, method, path string, query url.Values, body, out interface{}, opts ...RequestOption) error {
	respBody, err := c.doRaw(ctx, method, path, query, body, opts...)
	if err != nil {
		return err
	}
	data, _, err := decodeEnvelope(respBody)
	if err != nil {
		return err
	}
	if err := requireResourceObject(data, `response "data" envelope`); err != nil {
		return err
	}
	if err := decodeJSON(data, out); err != nil {
		return fmt.Errorf("octonomy: decode response data: %w", err)
	}
	return requireIdentity(out, `response "data" envelope`)
}

// identifiedList is implemented by every list envelope doList decodes into --
// TagList and VocabularyList -- and hands back the decoded rows so their
// identity can be checked.
//
// It is the doList half of what a type parameter would have given for free:
// without one, the transport cannot range over an out it knows only as
// interface{}. Making it the parameter TYPE, rather than asserting it at
// runtime, is what keeps a new list type from skipping the check silently --
// one that does not implement it does not compile as doList's argument.
type identifiedList interface {
	rows() []identifiedResource
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
// BOTH keys are required, not just data. A missing or null pagination block
// decodes to a zero-valued Pagination -- Count 0, Limit 0, nil Next -- and a
// caller paging on Count reads that as "one page, nothing after it". Same
// indistinguishable-empty-page failure, one field over.
//
// Each ROW is held to the same standard as a single resource: it must be an
// object (requireResourceArray) and must decode with its identity
// (requireIdentity). A blank row among fifty is worse than a blank single
// resource, not better -- the page length and the pagination are right, and
// nothing points a caller at the bad row except the index the error names.
//
// A present-but-null data ("data": null) is accepted and decodes to a nil slice.
// The server sends [] for an empty page. Both spellings mean "no rows", so treat
// nil and empty as the same thing; this line does not normalize between them,
// and a v1.0.0 caller may be relying on which one it gets.
func (c *Client) doList(ctx context.Context, method, path string, query url.Values, out identifiedList, opts ...RequestOption) error {
	respBody, err := c.doRaw(ctx, method, path, query, nil, opts...)
	if err != nil {
		return err
	}
	data, pagination, err := decodeEnvelope(respBody)
	if err != nil {
		return err
	}
	if pagination == nil || string(pagination) == "null" {
		return fmt.Errorf(`octonomy: list response has no "pagination" block`)
	}
	if err := requireResourceArray(data, `list response "data"`); err != nil {
		return err
	}
	if err := decodeJSON(respBody, out); err != nil {
		return fmt.Errorf("octonomy: decode response body: %w", err)
	}
	for i, row := range out.rows() {
		if err := requireIdentity(row, fmt.Sprintf(`list response "data" element %d`, i)); err != nil {
			return err
		}
	}
	return nil
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
	if err := decodeJSON(body, &envelope); err != nil {
		return nil, nil, fmt.Errorf("octonomy: decode response body: %w", err)
	}
	if envelope.Data == nil {
		return nil, nil, fmt.Errorf(`octonomy: response body has no "data" envelope`)
	}
	return envelope.Data, envelope.Pagination, nil
}

// requireResourceObject rejects a payload position that must hold a resource but
// holds something that would decode to a zero value with a nil error.
//
// It is the rule decodeEnvelope enforces, moved one level in. decodeEnvelope
// catches a body with no "data" key at all; this catches a well-formed envelope
// whose contents are not a resource -- {} above all, which fills in nothing and
// yields a Tag with an empty ID or a Vocabulary with no Slug. The id makes that
// self-evident; every OTHER field makes it a plausible-looking blank instead,
// and a caller reading one cannot tell it from a real value.
//
// It deliberately knows NOTHING about the decoded type, so every resource is
// checked by the same lines. What it cannot see is a NON-empty object carrying
// the wrong thing, since {"id": null} is as well formed as any other;
// requireIdentity below is the half that closes that, and the two are
// deliberately separate because only the second needs to know the model.
//
// The what argument names the position -- `response "data" envelope`, or
// `list response "data" element 2` -- because by the time this fails a caller
// has no other way to learn which part of the body was wrong.
func requireResourceObject(raw json.RawMessage, what string) error {
	trimmed := bytes.TrimSpace(raw)
	switch {
	case len(trimmed) == 0 || string(trimmed) == "null":
		return fmt.Errorf("octonomy: %s is null, expected a resource object", what)
	case trimmed[0] == '[':
		return fmt.Errorf("octonomy: %s is an array, expected a single resource object", what)
	case trimmed[0] != '{':
		return fmt.Errorf("octonomy: %s is not a resource object", what)
	}
	// An object carrying any key has a quote after the brace; only an empty one
	// has '}' there. That is exact for well-formed JSON, and the decode that
	// follows every call to this is what establishes the body IS well-formed --
	// so this stays a byte check rather than a second full parse of every
	// element of every page.
	if rest := bytes.TrimSpace(trimmed[1:]); len(rest) > 0 && rest[0] == '}' {
		return fmt.Errorf("octonomy: %s is an empty object, which would decode to a zero-valued resource", what)
	}
	return nil
}

// requireResourceArray requires every element of a JSON array of resources to
// be one, by requireResourceObject.
//
// A zero-valued element is the same defect as a zero-valued resource, one level
// in: "data": [null] gives a page whose length is right and whose row is blank.
// A null array is accepted -- see doList for why it is not normalized here.
func requireResourceArray(raw json.RawMessage, what string) error {
	var elements []json.RawMessage
	if err := decodeJSON(raw, &elements); err != nil {
		return fmt.Errorf("octonomy: decode %s: %w", what, err)
	}
	for i, element := range elements {
		if err := requireResourceObject(element, fmt.Sprintf("%s element %d", what, i)); err != nil {
			return err
		}
	}
	return nil
}

// identityField is one field a decoded model requires to be non-blank, paired
// with its name on the wire so a failure can say which one was missing.
type identityField struct {
	name  string
	value string
}

// identifiedResource is implemented by every model this SDK decodes from a
// resource position. It is the second half of the zero-value guard, and the
// half that requireResourceObject cannot do on its own.
//
// The shape check rejects a "data" that is empty, null, or not an object. It
// cannot reject a NON-empty object carrying the wrong thing: {"id": null} and
// {"wrong": true} are both well-formed objects that encoding/json fills nothing
// in from, so both decoded to a zero-valued resource with a nil error.
//
// Each model names only the field(s) that identify the ROW, which is what the
// vendored contracts mark required, and not every field they document. This
// stays a decode guarantee about identity rather than a client-side re-run of
// the server's validation, which AGENTS.md puts out of bounds.
//
// The method is unexported, so implementing it adds no public API. It is
// declared on the VALUE receiver, so both a Tag held in a list and the *Tag
// doData decodes into satisfy it.
type identifiedResource interface {
	identityFields() []identityField
}

// requireIdentity rejects a decoded value whose identity did not survive the
// decode. A blank id after a successful unmarshal means the bytes did not carry
// one, whatever else they carried.
//
// A value that does not implement identifiedResource is skipped: that is a
// composite result, which carries no identity of its own and requires its keys
// in its own decoder instead.
func requireIdentity(v interface{}, what string) error {
	resource, ok := v.(identifiedResource)
	if !ok {
		return nil
	}
	for _, field := range resource.identityFields() {
		if field.value == "" {
			return fmt.Errorf("octonomy: %s decoded with no %q, so it would be a zero-valued resource", what, field.name)
		}
	}
	return nil
}

func (c *Client) resolveActor(rc requestConfig) string {
	if rc.actorSet {
		return rc.actorID
	}
	return c.actorID
}

// parseError converts a non-2xx response into an *APIError.
//
// It takes a hint -- rather than only (status, body) -- so an envelope-less
// response can name the most likely cause, which differs by surface: the
// versioned API's is a server with no /api/v2 route (versionHint), and the
// probes' is a base URL that does not point at the Octonomy origin (healthHint).
// The hint is built by the caller because only the caller knows which surface it
// is on; the mapping rule below stays the same for both.
//
// The mapping rule has exactly two branches:
//
//   - WITH the Octonomy envelope, the server's own code is preserved verbatim,
//     including codes this SDK has no constant for.
//   - WITHOUT it, the response did not come from Octonomy's application layer at
//     all, and every such response gets CodeUnexpectedStatus.
//
// The second branch used to derive a semantic code from the status (the first
// release's codeFromStatus), which made an envelope-less 404 satisfy IsNotFound -- a wrong
// BaseURL, a path-stripping proxy, or a server with no /api/v2 route read as
// "that tag does not exist". Deriving semantics from a status this SDK did not
// generate cannot be made safe, so it is gone, on both API versions -- see
// CodeUnexpectedStatus.
func parseError(status int, body []byte, hint string) error {
	var envelope struct {
		Error struct {
			Code      string                 `json:"code"`
			Message   string                 `json:"message"`
			Details   map[string]interface{} `json:"details"`
			RequestID string                 `json:"request_id"`
		} `json:"error"`
	}
	if err := decodeJSON(body, &envelope); err == nil && envelope.Error.Code != "" {
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
		Message:    message + hint,
	}
}

// unreadableBodyError builds the *APIError for a non-2xx whose body could not be
// read to completion.
//
// The code is CodeUnexpectedStatus by the same rule parseError applies: no
// envelope was decoded, so no semantic code was established. That is not a
// fallback here but the accurate answer -- an unread body cannot have carried a
// code, and guessing one from the status is precisely what this package stopped
// doing.
func unreadableBodyError(status int, cause error) error {
	return &APIError{
		StatusCode: status,
		Code:       CodeUnexpectedStatus,
		Message:    fmt.Sprintf("%s (response body could not be read: %v)", http.StatusText(status), cause),
		err:        cause,
	}
}

// versionHint appends a diagnosis to an envelope-less 404 on the v2 surface,
// which is the shape a server predating /api/v2 returns for every call.
//
// That shape was verified rather than assumed. Before the server's version shim
// landed, the URLconf hardcoded path("api/v1/", ...) with no <version> capture,
// so /api/v2/tags matches no pattern at all and Django's resolver answers before
// any DRF view or exception handler runs: 404 text/html, "<h1>Not Found</h1>",
// no envelope. Probed against a live 3.1.0 container via a path outside the
// URLconf, which reproduces it exactly.
//
// KNOWN LIMIT, worth stating because it bounds the guarantee. A server that DOES
// have the version shim but rejects the requested version answers through DRF
// instead, with an ENVELOPED 404 (code not_found, details "Invalid version in
// URL path."). The envelope branch of parseError preserves that code verbatim,
// so IsNotFound would report true for it. The SDK never requests a version
// outside {v1, v2}, both of which every shimmed server allows, so it cannot
// reach that case; it is not a hole the SDK can close, because a server sending
// code=not_found means not_found.
//
// Phrased as a possibility, never an assertion: an envelope-less 404 is also
// what a misconfigured BaseURL, a path-stripping proxy, or a genuinely absent
// resource behind a gateway produces. The hint names the one cause the SDK can
// do something about. On v1 there is no such cause to name -- every server
// serves /api/v1 -- so the hint is v2-only.
func versionHint(status int, version APIVersion) string {
	if status != http.StatusNotFound || version != APIV2 {
		return ""
	}
	return fmt.Sprintf(" [octonomy-go: this response carried no Octonomy error envelope, which is what a server that does not serve %s returns for every call; if this deployment predates Octonomy 2.0, set Config.APIVersion = APIV1]", APIV2.prefix())
}
