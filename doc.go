// Package octonomy is the official Go client for the Octonomy tag-management and
// taxonomy service (https://github.com/octoverse-id/octonomy).
//
// Octonomy is a multi-tenant, multi-application REST service for vocabularies,
// tags, aliases, and tag assignments. This SDK speaks the /api/v1 surface of
// server release 3.2.1. The bundled docs/openapi.yaml is the contract this
// client is written against; docs/openapi-v2.yaml, the same release's /api/v2
// surface, is vendored alongside it for the port that adds /api/v2 opt-in.
//
// # Module path and release lines
//
// This is the Go 1.13 compatibility line. Import it as:
//
//	import octonomy "github.com/octoverse-id/octonomy-go"
//
// This repository publishes two modules, and the /v2 path suffix is what makes
// them distinct to the go command:
//
//   - github.com/octoverse-id/octonomy-go      v1.x, Go 1.13, never a major
//   - github.com/octoverse-id/octonomy-go/v2   v2.x, a modern Go, where features originate
//
// Because the paths differ, version selection cannot move a consumer between the
// two lines. This line takes security fixes, bug fixes, and ports of what the
// /v2 line already has, and has a published sunset date; see docs/versioning.md.
// It never takes a breaking change -- in the sense docs/versioning.md defines,
// which is Go's own -- because an unsuffixed module path cannot publish a major,
// and it never ships a webhook receiver. If your toolchain is current, use the
// /v2 path instead -- its README on main states the minimum it requires, which is
// not a number this line can keep true.
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
// each resource's *ListParams. One envelope type per resource, rather than one
// generic envelope, because type parameters need Go 1.18.
package octonomy
