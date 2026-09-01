package octonomy

import (
	"context"
	"net/http"
	"net/url"
)

// ResolutionScope pins the namespace a tag resolution searches. It is the one
// place in this SDK where the literal "global" is a legal value: as a SCOPE it
// names the tenant-shared namespace explicitly, while as an X-Namespace-Type it
// is reserved and rejected (see WithNamespace).
//
// It is sent on BOTH surfaces, and the vendored docs/openapi.yaml will suggest
// otherwise: that file is pinned at server 1.0.0, which predates the parameter.
// The running server carries it on /api/v1 too -- probed against 3.1.0, where
// GET /api/v1/tag-resolution validates it by name rather than ignoring it -- so
// the stale spec loses to the server, as it does for the response envelopes.
// Gating this to APIV2 would refuse a call every current deployment answers, and
// the SDK has no version handshake with which to tell an old v1 server from a
// current one. Against a server predating the parameter it is dropped like any
// unknown query parameter.
type ResolutionScope string

const (
	// ResolutionScopeGlobal pins resolution to the tenant-shared namespace.
	//
	// From a namespaced request it is also the authorization opt-in, and pairing
	// it with WithIncludeGlobal is unnecessary: the server adds the global
	// namespace to the request's authorized set for THIS ROUTE ONLY when it sees
	// scope=global (core/versioning.py), deliberately not treating the parameter
	// as a general alias for include_global on other endpoints.
	ResolutionScopeGlobal ResolutionScope = "global"

	// ResolutionScopeMerchant pins resolution to the request's own namespace, so
	// it requires one: the SDK refuses the combination locally rather than
	// spending a round trip on a request the server answers with "Merchant scope
	// requires a namespaced request."
	ResolutionScopeMerchant ResolutionScope = "merchant"
)

// MatchedType reports which kind of row satisfied a resolution.
type MatchedType string

const (
	// MatchedTypeTag means the slug named a canonical tag directly.
	// TagResolution.MatchedAlias is nil.
	MatchedTypeTag MatchedType = "tag"

	// MatchedTypeAlias means the slug named an alias, which resolved to the
	// canonical tag in TagResolution.Tag. MatchedAlias carries the alias itself.
	MatchedTypeAlias MatchedType = "alias"
)

// TagResolution is the result of resolving a slug (GET /tag-resolution).
//
// Tag is the canonical tag either way, so a caller that only wants the tag can
// ignore the other two fields. MatchedAlias is non-nil exactly when MatchedType
// is MatchedTypeAlias, and it is what says the slug the caller passed is an
// alternate identifier rather than the tag's own slug -- useful for nudging a
// caller's stored slug towards the canonical one.
//
// Canonical tags win over aliases for the same slug, and within an application a
// tag scoped to that application wins over a tenant-shared one, so local
// vocabulary can override tenant-wide defaults.
type TagResolution struct {
	MatchedType  MatchedType `json:"matched_type"`
	MatchedAlias *TagAlias   `json:"matched_alias"`
	Tag          Tag         `json:"tag"`
}

// TagResolveParams narrows a resolution. A nil *params resolves the slug alone,
// with server defaults.
//
// ApplicationID both filters and ORDERS: with it set, a tag or alias in that
// application outranks a tenant-shared one carrying the same slug. Without it,
// two rows in different applications are a tie the server refuses to break.
type TagResolveParams struct {
	ApplicationID *string
	Type          *string
	Scope         ResolutionScope
}

// query builds the resolution query. slug is a positional argument on Resolve
// rather than a params field because the server requires it, so this takes it
// instead of following the no-argument query() shape the list params use.
func (p *TagResolveParams) query(slug string) url.Values {
	q := url.Values{}
	q.Set("slug", slug)
	if p == nil {
		return q
	}
	if p.ApplicationID != nil {
		q.Set(applicationIDParam, *p.ApplicationID)
	}
	if p.Type != nil {
		q.Set("type", *p.Type)
	}
	if p.Scope != "" {
		q.Set(scopeParam, string(p.Scope))
	}
	return q
}

// Resolve resolves a slug to a tag, possibly by way of an alias
// (GET /tag-resolution). It is a single specialized read: there is no list form
// and no write.
//
// NO MATCH IS NOT A 404. The server answers an unmatched slug with a 400
// validation_error whose Details carry {"slug": [...]}, so the branch that means
// "nothing is called that" is IsValidation, NOT IsNotFound. A resolution that
// asks for scope=global without the authority to see global rows returns that
// same error, indistinguishable on purpose: telling the two apart would disclose
// the existence of rows the caller may not read.
//
// Two matches of equal specificity are refused rather than broken arbitrarily,
// and the axis that disambiguates them arrives in Details -- but under two
// different codes, so a caller handling only one of them misses half the cases:
//
//   - Rows differing by APPLICATION are an ambiguous_resolution
//     (IsAmbiguousResolution), with Details {"application_id": [...]}. Set
//     TagResolveParams.ApplicationID.
//   - Canonical tags differing by TYPE are a plain validation_error
//     (IsValidation), with Details {"type": [...]}. Set TagResolveParams.Type.
func (s *TagService) Resolve(ctx context.Context, slug string, params *TagResolveParams, opts ...RequestOption) (*TagResolution, error) {
	return doData[TagResolution](ctx, s.client, http.MethodGet, "/tag-resolution", params.query(slug), nil, opts...)
}
