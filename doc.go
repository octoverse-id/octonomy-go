// Package octonomy is the official Go client for the Octonomy tag-management and
// taxonomy service (https://github.com/octoverse-id/octonomy).
//
// Octonomy is a multi-tenant, multi-application REST service for vocabularies,
// tags, aliases, and tag assignments. This SDK targets the server's primary
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
// two lines. If you are on Go 1.13, use the unsuffixed path; it receives security
// fixes only and has a published sunset date. See docs/versioning.md.
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
// # Errors
//
// Non-2xx responses are returned as *APIError, which exposes the Octonomy error
// envelope (Code, Message, Details, RequestID) plus the HTTP StatusCode. Use the
// IsNotFound, IsConflict, and IsValidation helpers to branch on common cases.
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
// # List responses
//
// List methods return a *List[T] holding the Data slice and Pagination metadata
// (limit, offset, count, next, previous). Page with ListOptions on each resource's
// *ListParams.
package octonomy
