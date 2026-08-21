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
// This is the frozen Go 1.13 compatibility line. Import it as:
//
//	import octonomy "github.com/octoverse-id/octonomy-go"
//
// This repository publishes two modules, and the /v2 path suffix is what makes
// them distinct to the go command:
//
//   - github.com/octoverse-id/octonomy-go      v1.x, Go 1.13, frozen, /api/v1 only
//   - github.com/octoverse-id/octonomy-go/v2   v2.x, Go 1.24+, active development
//
// Because the paths differ, version selection cannot move a consumer between the
// two lines. This line receives security fixes only, takes no features, and has a
// published sunset date; see docs/versioning.md. If your toolchain is Go 1.24 or
// newer, use the /v2 path instead.
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
// # List responses
//
// List methods return a per-resource envelope holding the Data slice and
// Pagination metadata (limit, offset, count, next, previous): *TagList from
// Tags.List and *VocabularyList from Vocabularies.List. Page with ListOptions on
// each resource's *ListParams. (The modern line expresses these as a generic
// List[T], which needs Go 1.18.)
package octonomy
