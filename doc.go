// Package octonomy is the official Go client for the Octonomy tag-management and
// taxonomy service (https://github.com/octoverse-id/octonomy).
//
// Octonomy is a multi-tenant, multi-application REST service for vocabularies,
// tags, aliases, and tag assignments. This SDK targets the stable v1 API
// (server release 1.0.0) served under /api/v1. The bundled docs/openapi.yaml is
// the contract this client is written against.
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
