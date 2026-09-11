package octonomy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// ResolutionScope pins the namespace a tag resolution searches. It is the one
// place in this SDK where the literal "global" is a legal value: as a SCOPE it
// names the tenant-shared namespace explicitly, while as an X-Namespace-Type it
// is reserved and rejected (see WithNamespace).
//
// It is sent on BOTH surfaces, and docs/openapi.yaml now documents it on
// /api/v1/tag-resolution as well. It suggested otherwise only while that file
// was pinned at server 1.0.0, which predates the parameter; sending it on v1 was
// already right then -- probed against 3.1.0, GET /api/v1/tag-resolution
// validates it by name rather than ignoring it -- and the 3.1.1 refresh says so.
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
// ignore the other two fields. MatchedAlias is non-nil WHENEVER MatchedType is
// MatchedTypeAlias -- required on decode, so the branch that reads it cannot
// nil-deref -- and it is what says the slug the caller passed is an alternate
// identifier rather than the tag's own slug, useful for nudging a caller's
// stored slug towards the canonical one.
//
// Test for MatchedType rather than for a non-nil MatchedAlias. The server sends
// an alias only on an alias match, but the decoder does not enforce the converse:
// a matched_type this SDK has no constant for is preserved rather than rejected
// (see UnmarshalJSON), so a future match kind could in principle carry one.
//
// Canonical tags win over aliases for the same slug, and within an application a
// tag scoped to that application wins over a tenant-shared one, so local
// vocabulary can override tenant-wide defaults.
type TagResolution struct {
	MatchedType  MatchedType `json:"matched_type"`
	MatchedAlias *TagAlias   `json:"matched_alias"`
	Tag          Tag         `json:"tag"`
}

// UnmarshalJSON requires the keys a caller acts on, for the reason given on
// BulkAssignResult: this is a COMPOSITE, not a resource, so it carries no id
// whose blankness would give the problem away.
//
// A resolution whose "tag" key the server renamed would otherwise decode to a
// zero-valued Tag with a nil error -- an empty ID, an empty Slug, and
// IsActive false -- delivered from the one call whose entire purpose is to hand
// back that tag (#40). MatchedType is required for the same reason one step
// down: "" is not one of the two legal values, and a caller branching on
// MatchedTypeAlias reads it as "the slug named a tag directly", which is a
// plausible answer rather than a visible failure.
//
// Both vendored contracts mark all THREE keys required, "matched_alias"
// included -- required as a key, whose value is nullable -- so an absent one is
// a contract break and is reported as such.
//
// An UNKNOWN matched_type is preserved verbatim rather than rejected, exactly as
// an error code this SDK has no constant for is (AGENTS.md). A third match type
// the server adds later must not turn every resolution into a client error. What
// is rejected is "", which is not a value at all.
//
// A matched_type of "alias" with a null matched_alias IS rejected, because
// TagResolution documents MatchedAlias as non-nil whenever the match is an alias
// -- and a caller who writes the obvious res.MatchedAlias.Slug against that
// documented invariant would panic. This library never panics, and that promise is worth little if
// what it hands back makes the caller do it. The converse (a "tag" match
// carrying an alias) is left alone: it breaks nothing a caller does, so
// rejecting it would be strictness with no failure mode behind it.
func (r *TagResolution) UnmarshalJSON(data []byte) error {
	var wire struct {
		MatchedType  *MatchedType    `json:"matched_type"`
		MatchedAlias json.RawMessage `json:"matched_alias"`
		Tag          json.RawMessage `json:"tag"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	switch {
	case wire.MatchedType == nil:
		return fmt.Errorf(`octonomy: resolution response has no "matched_type"`)
	case *wire.MatchedType == "":
		return fmt.Errorf(`octonomy: resolution response has an empty "matched_type"`)
	case wire.Tag == nil:
		return fmt.Errorf(`octonomy: resolution response has no "tag"`)
	case wire.MatchedAlias == nil:
		return fmt.Errorf(`octonomy: resolution response has no "matched_alias" key (null is how a tag match reports one)`)
	}
	if err := requireResourceObject(wire.Tag, `resolution response "tag"`); err != nil {
		return err
	}

	// Built whole and assigned in one go, as BulkAssignResult is and for the
	// same reason: UnmarshalJSON is exported, so it must fully define what it
	// decodes into rather than leaving a reused value's old alias in place.
	out := TagResolution{MatchedType: *wire.MatchedType}
	if err := json.Unmarshal(wire.Tag, &out.Tag); err != nil {
		return fmt.Errorf("octonomy: decode resolution tag: %w", err)
	}
	if string(wire.MatchedAlias) != "null" {
		if err := requireResourceObject(wire.MatchedAlias, `resolution response "matched_alias"`); err != nil {
			return err
		}
		if err := json.Unmarshal(wire.MatchedAlias, &out.MatchedAlias); err != nil {
			return fmt.Errorf("octonomy: decode resolution matched_alias: %w", err)
		}
	} else if out.MatchedType == MatchedTypeAlias {
		return fmt.Errorf(`octonomy: resolution response matched an alias but its "matched_alias" is null`)
	}
	*r = out
	return nil
}

// identityFields requires the resolution to carry a real tag, and a real alias
// when it reports one. TagResolution has no id of its own, so these are the
// fields whose blankness would otherwise pass for an answer (#40).
func (r TagResolution) identityFields() []identityField {
	fields := []identityField{{name: "tag.id", value: r.Tag.ID}}
	if r.MatchedAlias != nil {
		fields = append(fields, identityField{name: "matched_alias.id", value: r.MatchedAlias.ID})
	}
	return fields
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
