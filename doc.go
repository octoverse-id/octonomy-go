// Package octonomy is the official Go client for the Octonomy tag-management and
// taxonomy service (https://github.com/octoverse-id/octonomy).
//
// Octonomy is a multi-tenant, multi-application REST service for vocabularies,
// tags, aliases, tag assignments, and the audit history of every mutation to
// them. This SDK targets the server's primary
// surface, /api/v2 (server release 3.1.1), and can be pointed at /api/v1
// instead. The bundled docs/openapi-v2.yaml and docs/openapi.yaml are the
// contracts this client is written against.
//
// # Module path and release lines
//
// Import this package as:
//
//	import octonomy "github.com/octoverse-id/octonomy-go/v2"
//
// The /v2 suffix is not decoration: this repository publishes two modules, and
// the suffix is what makes them distinct to the go command.
//
//   - github.com/octoverse-id/octonomy-go      v1.x, Go 1.13, frozen, /api/v1 only
//   - github.com/octoverse-id/octonomy-go/v2   v2.x, Go 1.24+, active development
//
// Because the paths differ, version selection cannot move a consumer between the
// two lines, and a consumer needs no exclude, pin, or build tag of their own. If
// you are on Go 1.13, use the unsuffixed path: it carries Vocabularies and Tags
// on /api/v1 only, receives security fixes and nothing else, and sunsets on
// 2027-08-31, after which it receives nothing at all. Go 1.13 is itself unpatched
// (go1.13.15, August 2020, was its last release), so pinning there is an informed
// trade rather than a safe harbour. See docs/versioning.md and SECURITY.md.
//
// This line is not yet tagged: v2.0.0-alpha.1 is unreleased, so go get on the /v2
// path resolves a pseudo-version. The compat line is released at v1.0.0. No v0.x
// of either line was ever published -- proxy.golang.org lists v1.0.0 alone for
// the unsuffixed path and nothing at all for /v2.
//
// Note that the Version constant still reads "0.1.0" on this branch, so the
// default User-Agent is octonomy-go/0.1.0. It is bumped to the release version in
// the dedicated release PR (docs/release.md), not here, which is why the source
// constant and the published tags do not yet agree.
//
// # Quickstart
//
//	client, err := octonomy.New(octonomy.Config{
//		BaseURL:  "https://octonomy.example.com",
//		Token:    "svc_live_...", // service token -> Authorization: Bearer
//		TenantID: "acme",         // -> X-Tenant-ID
//		// APIVersion defaults to APIV2; set APIV1 for a server older than 2.0.
//	})
//	if err != nil {
//		log.Fatal(err)
//	}
//
//	ctx := context.Background()
//	tag, err := client.Tags.Create(ctx, octonomy.TagCreate{
//		Name: "Featured",
//		Slug: "featured",
//		Type: "label",
//	})
//	if err != nil {
//		if octonomy.IsConflict(err) {
//			// a tag with this (type, slug) already exists for the tenant
//		}
//		log.Fatal(err)
//	}
//	fmt.Println(tag.ID)
//
// # API version
//
// Config.APIVersion selects the REST surface and defaults to APIV2, the server's
// primary advertised one. Both surfaces are live and neither is deprecated; what
// separates them is the namespace axis below, which exists only on v2.
//
// If your Octonomy server predates 2.0 it has no /api/v2 route at all and will
// answer every call with an unrouted 404. Set Config.APIVersion = APIV1 for such
// a deployment. There is no version handshake, so the SDK cannot detect this in
// advance -- but the failure is loud: an unrouted 404 carries no Octonomy error
// envelope, so it surfaces as CodeUnexpectedStatus with a hint, and never as a
// not_found a caller might mistake for "the tag does not exist".
//
// # Namespaces
//
// On v2, a request may be scoped to a merchant or sub-tenant namespace, which
// partitions rows below the application level:
//
//	page, err := client.Tags.List(ctx, params,
//		octonomy.WithNamespace("merchant", "acme-store"),
//		octonomy.WithApplication("storefront"),
//	)
//
// Namespace is per-request and deliberately has no Config field. Omitting it is
// not an error -- the server serves the global (tenant-shared) namespace with a
// 200 -- so a client-level default would silently scope every read to whichever
// merchant was configured at startup, at call sites that all still look correct.
//
// A namespaced request must also name its application (WithApplication on a read,
// or the ApplicationID field of a write body), because namespace isolation sits
// below application. Namespaced reads exclude global rows by default; add
// WithIncludeGlobal to see both. WithIncludeGlobal is fail-closed on the server:
// it widens what the request asks for, not what the token is authorized to see.
//
// # Authentication and scope
//
// Every request carries the service token (Authorization: Bearer) and the tenant
// (X-Tenant-ID) from Config. Set Config.ActorID (or pass WithActor per call) to
// populate X-Actor-ID for audit trails. Tokens are scoped to tags:read,
// tags:write, and audit:read on the server side.
//
// audit:read is granted separately and gates only Client.AuditLogs and the two
// nested audit routes. A token without it reads and writes tags perfectly well
// and gets a 403 (IsForbidden) from those three -- never an empty page.
//
// # Request correlation
//
// WithRequestID sends X-Request-ID for one call, threading the caller's own
// correlation id into the audit row, the outbox/webhook event, the server's
// structured log, and the error envelope. It composes with WithActor: actor is
// who, request id is which call.
//
// The SDK never mints an id -- no header is sent unless the option is used, so
// the server's own minting stays intact -- and there is no Config field for one,
// since a request id names a single request and a client-level default would
// stamp every call with one value. Generate one per outbound call and log it on
// your side; on a success the SDK returns (*T, error) and does not hand back the
// server's id, while on a failure APIError.RequestID carries it.
//
// # Errors
//
// Non-2xx responses are returned as *APIError, which exposes the Octonomy error
// envelope (Code, Message, Details, RequestID) plus the HTTP StatusCode. Use the
// IsNotFound, IsConflict, and IsValidation helpers to branch on common cases.
//
// The helpers match on code, never on status, so a 409 is not automatically an
// IsConflict: a PATCH that tries to move a row between application or namespace
// scopes answers 409 scope_immutable, which IsScopeImmutable matches and
// IsConflict does not. Scope is fixed at creation -- re-create the row in the
// target scope rather than retrying.
//
// A non-2xx that arrives WITHOUT that envelope did not come from Octonomy's
// application layer -- a proxy, a load balancer, a server with no route for the
// requested API version -- and carries CodeUnexpectedStatus rather than a code
// derived from its status. IsNotFound is therefore true only for a real Octonomy
// not_found, never for a bare 404.
//
// A 2xx whose body does not match the expected shape is an error too. The server
// wraps single resources in {"data": {...}} and lists in
// {"data": [...], "pagination": {...}}; a body missing that envelope would
// otherwise decode into a zero-valued struct, or an empty-looking page, with no
// error at all. A genuinely empty page is not an error and yields an empty
// non-nil Data slice.
//
// # Health probes
//
// The liveness and readiness routes sit at the SERVER ROOT, outside
// /api/<version>, authenticate nobody, and answer with a bare {"status": "ok"}
// carrying no data envelope. Because New requires a Token and a TenantID,
// reaching them with neither needs its own constructor:
//
//	probe, err := octonomy.NewHealthClient("https://octonomy.example.com")
//	if err != nil {
//		log.Fatal(err)
//	}
//
//	st, err := probe.Health.Ready(ctx)
//	switch {
//	case err == nil:
//		// ready; st.Status is the server's own word
//	case octonomy.IsNotReady(err):
//		// it answered and said it cannot serve: back off and re-probe
//	case errors.Is(err, octonomy.ErrUnreachable):
//		// no response at all -- refused, DNS, TLS, timeout, cancelled context
//	}
//
// A caller holding a full Client uses client.Health instead, which runs the same
// code and sends the same request: no Authorization, no X-Tenant-ID, and no
// /api prefix, from either entry point.
//
// Unreachable and unready are deliberately never collapsed into one error. They
// call for different responses -- wait, versus go looking for the process -- so a
// 503 carrying the probe's own body is an *APIError (IsNotReady) while a request
// that got no response produces no *APIError at all and matches
// errors.Is(err, ErrUnreachable). ErrUnreachable is not health-specific: every
// method in this package wraps it around a request that got no response.
//
// # List responses
//
// List methods return a *List[T] holding the Data slice and Pagination metadata
// (limit, offset, count, next, previous). Page with ListOptions on each resource's
// *ListParams.
//
// Each walks every page for you. It issues ONE REQUEST PER PAGE -- said plainly
// because this package promises no hidden behavior -- and returns the offset of
// the first item it did not process, so a failed walk resumes instead of
// starting over. Its doc comment covers the offset drift that limit/offset
// paging cannot avoid.
//
// # Transport, observability, and connection reuse
//
// This package never logs, never mutates global state, and adds no retry loop of
// its own, so the sanctioned extension point for metrics, tracing, request
// logging, retries, and rate limiting is an http.RoundTripper on the *http.Client
// you supply through Config.HTTPClient (or WithHealthHTTPClient). A RoundTripper
// sees the fully assembled request and the raw response; read X-Request-ID off
// the request to join your span to the server's record of the same call.
// (net/http's own transport already retries a request it failed to write on a
// REUSED connection -- recovery from a half-closed idle socket, not a retry
// policy, and not something this package adds to.)
//
// Config.HTTPClient REPLACES the default (&http.Client{Timeout: 30 * time.Second})
// rather than decorating it, so set a Timeout on any client you pass.
//
// ON HTTP/1.1 ONLY, the idle-connection pool is the first scaling bottleneck.
// http.DefaultTransport sets ForceAttemptHTTP2, so against an HTTPS endpoint that
// negotiates h2 every request multiplexes over one connection and none of this
// applies. Where the connection really is HTTP/1.1 -- plaintext, a proxy that
// terminates at 1.1, or a transport that did not opt into h2 -- DefaultTransport
// leaves MaxIdleConnsPerHost unset and it falls back to
// http.DefaultMaxIdleConnsPerHost, which is 2. Octonomy is a single host, so past
// two CONCURRENT calls each additional one opens and then discards its own
// connection instead of returning it to the pool. It caps pooled connections
// rather than in-flight ones, so nothing blocks -- the handshakes simply stop
// being amortized.
//
// Sequential work never meets it on either protocol: one goroutine in a loop,
// Each included, reuses a single connection whatever the setting. It is a
// fan-out across goroutines sharing one Client that hits it.
//
// This package does not tune it: transport configuration belongs to the caller.
// Raise it by cloning DefaultTransport (Clone keeps its proxy, dialer, and HTTP/2
// settings, which a bare &http.Transport{} drops) and setting MaxIdleConnsPerHost
// at or above your peak concurrency. See the README for a worked example.
//
// A Client is safe for concurrent use, and sharing one is the simplest way to get
// this right. The pool belongs to the TRANSPORT, not the Client, so a per-request
// Client defeats pooling only when it also builds a new *http.Transport each time.
//
// # Typed metadata
//
// DecodeMetadata decodes a resource's Metadata into a struct of the caller's own
// shape, replacing the type assertion that would otherwise panic when the stored
// shape changes. It is a function rather than a method because Metadata is a
// type alias. Integers beyond +/-2^53 MAY already have been rounded by the time
// Metadata exists -- float64 loses resolution in doubling steps rather than at
// a clean cutoff; see its doc comment.
package octonomy
