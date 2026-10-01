// Package octonomy is the official Go client for the Octonomy tag-management and
// taxonomy service (https://github.com/octoverse-id/octonomy).
//
// Octonomy is a multi-tenant, multi-application REST service for vocabularies,
// tags, aliases, and tag assignments. This SDK speaks both REST surfaces of
// server release 3.2.1: /api/v1 by default, and /api/v2 -- which adds the
// namespace axis -- when Config.APIVersion is APIV2. The bundled
// docs/openapi.yaml and docs/openapi-v2.yaml are the contracts this client is
// written against.
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
// populate X-Actor-ID for audit trails, and pass WithRequestID to correlate one
// call with your own logs. Tokens are scoped to tags:read, tags:write, and
// audit:read on the server side.
//
// # API version and namespaces
//
// Config.APIVersion defaults to APIV1 on this line -- the surface every v1.0.0
// request reached -- because this line never changes a default under a caller.
// The /v2 module defaults to APIV2 instead. Set APIV2 here to reach /api/v2, and
// scope a request to a merchant or sub-tenant namespace per call:
//
//	tags, err := client.Tags.List(ctx, nil,
//		octonomy.WithNamespace("merchant", "acme-store"),
//		octonomy.WithApplication("storefront"))
//
// There is no client-level namespace, on purpose. A request whose scope options
// contradict each other, or the client's API version, is refused before it is
// sent, with an error naming the fix; WithNamespace, WithApplication, and
// WithIncludeGlobal each document what they require.
//
// # Errors
//
// Non-2xx responses are returned as *APIError, which exposes the Octonomy error
// envelope (Code, Message, Details, RequestID) plus the HTTP StatusCode. Use the
// IsNotFound, IsConflict, and IsValidation helpers to branch on common cases.
//
// The helpers match the code in the envelope, never a bare status: a non-2xx
// that Octonomy did not write -- a proxy's 502, a wrong BaseURL's 404 -- is
// IsUnexpectedStatus, and a request that got no response at all matches
// errors.Is(err, ErrUnreachable).
//
// # Health probes
//
// Client.Health probes /health/live and /health/ready, which are unauthenticated
// and outside /api/<version>. NewHealthClient builds a client for them from a
// base URL alone, with no token and no tenant. IsNotReady (the server answered
// and is not serving) and ErrUnreachable (nothing answered) stay distinct.
//
// # List responses
//
// List methods return a per-resource envelope holding the Data slice and
// Pagination metadata (limit, offset, count, next, previous): *TagList from
// Tags.List and *VocabularyList from Vocabularies.List. Page with ListOptions on
// each resource's *ListParams. One envelope type per resource, rather than one
// generic envelope, because type parameters need Go 1.18.
package octonomy
