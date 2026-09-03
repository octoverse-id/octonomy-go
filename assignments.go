package octonomy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Assignment links a tag to an external resource -- an order, a product, a
// customer -- identified by the (ResourceType, ResourceID) pair the caller
// chooses. Octonomy stores those two as opaque strings and never dereferences
// them, so a resource need not exist anywhere Octonomy can see.
//
// ApplicationID is a plain string here, not a *string as on Tag, Vocabulary, and
// TagAlias: an assignment is always application-scoped. There is no
// tenant-shared assignment, so there is no nil to represent.
type Assignment struct {
	ID            string `json:"id"`
	TenantID      string `json:"tenant_id"`
	ApplicationID string `json:"application_id"`

	// NamespaceType and NamespaceID identify the merchant or sub-tenant namespace
	// that owns this row; both are nil for a global (tenant-shared) row. They are
	// decode-only and appear on the v2 surface: the server sets them from the
	// X-Namespace-* headers at creation and never from the request body.
	//
	// Unlike the other six v2 schemas that carry this pair, Assignment does not
	// mark them required in the contract -- it emits them all the same. A drift
	// check keying on `required` will therefore count six where the runtime has
	// seven.
	NamespaceType *string `json:"namespace_type"`
	NamespaceID   *string `json:"namespace_id"`

	TagID        string    `json:"tag_id"`
	ResourceType string    `json:"resource_type"`
	ResourceID   string    `json:"resource_id"`
	AssignedBy   *string   `json:"assigned_by"`
	AssignedAt   time.Time `json:"assigned_at"`
}

// AssignmentCreate is the request body for assigning a tag to a resource.
// ApplicationID, ResourceType, and ResourceID are required.
//
// The tag is named by EXACTLY ONE of TagID, AliasID, or AliasSlug. Zero of them,
// or two, is a validation_error naming all three -- the SDK sends what you set
// and lets the server say so, rather than duplicating a rule it would then have
// to keep in step. AliasSlug is resolved with the same precedence
// Tags.Resolve uses, so an application-scoped alias beats a tenant-shared one.
type AssignmentCreate struct {
	ApplicationID string  `json:"application_id"`
	TagID         *string `json:"tag_id,omitempty"`
	AliasID       *string `json:"alias_id,omitempty"`
	AliasSlug     *string `json:"alias_slug,omitempty"`
	ResourceType  string  `json:"resource_type"`
	ResourceID    string  `json:"resource_id"`

	// AssignedBy attributes the assignment in audit logs. It is a field on the
	// row, distinct from the X-Actor-ID header WithActor sets, which attributes
	// the REQUEST. Setting this also supplies the actor when the header is absent.
	AssignedBy *string `json:"assigned_by,omitempty"`
}

// AssignmentRemove is the request body for removing one assignment. Every field
// is required, and together they identify the row: this endpoint has no id path.
//
// Note that it takes TagID only -- the alias forms AssignmentCreate accepts have
// no counterpart here, because an assignment records the canonical tag it
// resolved to and not the identifier used to create it.
type AssignmentRemove struct {
	ApplicationID string `json:"application_id"`
	TagID         string `json:"tag_id"`
	ResourceType  string `json:"resource_type"`
	ResourceID    string `json:"resource_id"`
}

// BulkAssign is the request body for assigning many tags to ONE resource. It
// names the resource once and the tags many times, so it cannot spread across
// resources; that is a loop over this call.
//
// At least one of TagIDs or AliasSlugs is required, and both may be given
// together: the server resolves the slugs to tags and unions them with TagIDs.
// The combined length is capped at the deployment's MAX_BULK_TAGS (200 by
// default), over which the whole call is a validation_error.
type BulkAssign struct {
	ApplicationID string   `json:"application_id"`
	ResourceType  string   `json:"resource_type"`
	ResourceID    string   `json:"resource_id"`
	TagIDs        []string `json:"tag_ids,omitempty"`
	AliasSlugs    []string `json:"alias_slugs,omitempty"`
	AssignedBy    *string  `json:"assigned_by,omitempty"`
}

// BulkAssignResult is what a bulk assign returns: counts plus the resulting
// rows. It is a composite payload rather than a resource or a page -- it carries
// no pagination block, because it is not one.
//
// Created and Existing split the assignments by whether this call made them,
// which is how a caller learns that a tag was already on the resource.
//
// SKIPPED IS ALWAYS ZERO on server 3.1.x, and the field exists only because the
// server emits it. Nothing is skipped because nothing is tolerated: an unknown
// tag id fails the entire call rather than being passed over (see BulkAssign on
// AssignmentService). Do not build logic on it being non-zero.
type BulkAssignResult struct {
	Created     int          `json:"created"`
	Existing    int          `json:"existing"`
	Skipped     int          `json:"skipped"`
	Assignments []Assignment `json:"assignments"`
}

// UnmarshalJSON requires the keys a caller acts on, rather than letting an
// unexpected object shape decode to a zero-valued result with a nil error.
//
// doData stops at the data envelope, which is the right line for a RESOURCE: a
// zero-valued Assignment has an empty ID, and no caller mistakes that for an
// answer. A composite of counters is different, and that is the whole reason
// this method exists -- Created 0, Existing 0, Assignments empty is a perfectly
// ordinary result, so a body whose keys the server renamed would be read as "the
// tags were all already there" instead of as the contract break it is. The zero
// value being indistinguishable from a real answer is exactly the #32 shape,
// one level in from where doData catches it.
//
// Skipped is deliberately NOT required: it is vestigial (always 0 on 3.1.x, see
// above), so demanding it would turn the server dropping a dead field into a
// client error.
//
// Assignments uses json.RawMessage rather than a *[]Assignment because those two
// spellings of nil have to be told apart, and a pointer-to-slice cannot: an
// ABSENT key is the contract break this method exists to catch, while a
// present-but-null one means "no rows" and normalizes to an empty non-nil slice.
// That is the same distinction decodeEnvelope draws with the same idiom, and the
// same null handling doList already gives an empty page.
func (r *BulkAssignResult) UnmarshalJSON(data []byte) error {
	var wire struct {
		Created     *int            `json:"created"`
		Existing    *int            `json:"existing"`
		Skipped     *int            `json:"skipped"`
		Assignments json.RawMessage `json:"assignments"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	switch {
	case wire.Created == nil:
		return fmt.Errorf(`octonomy: bulk assign response has no "created" count`)
	case wire.Existing == nil:
		return fmt.Errorf(`octonomy: bulk assign response has no "existing" count`)
	case wire.Assignments == nil:
		return fmt.Errorf(`octonomy: bulk assign response has no "assignments" array`)
	}
	// Built whole and assigned in one go, rather than field by field onto the
	// receiver. UnmarshalJSON is exported, so it must fully define what it
	// decodes into: assigning Skipped only when the key is present would leave a
	// REUSED result carrying its previous count on a response that omits it.
	// doData always passes a fresh value, so nothing in this package could reach
	// that -- which is exactly why it is worth closing here rather than relying
	// on every future caller to know.
	out := BulkAssignResult{Created: *wire.Created, Existing: *wire.Existing}
	if err := json.Unmarshal(wire.Assignments, &out.Assignments); err != nil {
		return fmt.Errorf("octonomy: decode bulk assign assignments: %w", err)
	}
	if out.Assignments == nil {
		out.Assignments = []Assignment{}
	}
	if wire.Skipped != nil {
		out.Skipped = *wire.Skipped
	}
	*r = out
	return nil
}

// BulkRemove is the request body for removing many tags from ONE resource.
// TagIDs is required and takes canonical tag ids only, with no alias form -- the
// asymmetry with BulkAssign, which also accepts AliasSlugs.
type BulkRemove struct {
	ApplicationID string   `json:"application_id"`
	ResourceType  string   `json:"resource_type"`
	ResourceID    string   `json:"resource_id"`
	TagIDs        []string `json:"tag_ids"`
}

// BulkRemoveResult reports how many assignments a bulk remove deleted. Ids that
// named no assignment are not an error, so Removed is routinely smaller than the
// number of ids sent.
type BulkRemoveResult struct {
	Removed int `json:"removed"`
}

// UnmarshalJSON requires the removed count, for the reason given on
// BulkAssignResult -- and more sharply here, because this payload is a single
// counter. A missing or renamed "removed" would otherwise decode to 0 with a nil
// error, which is not merely a plausible answer but the single most common one:
// "none of those tags were on that resource".
func (r *BulkRemoveResult) UnmarshalJSON(data []byte) error {
	var wire struct {
		Removed *int `json:"removed"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if wire.Removed == nil {
		return fmt.Errorf(`octonomy: bulk remove response has no "removed" count`)
	}
	*r = BulkRemoveResult{Removed: *wire.Removed}
	return nil
}

// AssignmentService accesses the /tag-assignments endpoints. Reach it via
// Client.Assignments.
type AssignmentService struct {
	client *Client
}

// Create assigns a tag to a resource (POST /tag-assignments).
//
// IT IS IDEMPOTENT. Re-assigning a tag already on the resource returns the
// existing row with a 200 rather than a 201, and is not an error. The SDK
// returns the same *Assignment either way: doData decodes the payload and does
// not surface the status, so this call cannot tell you which happened. When that
// distinction matters, BulkAssign with a single tag id answers it directly --
// its Created and Existing counts are the same fact, in a form the response body
// carries.
//
// The target tag must be active and assignable in the named application:
// an inactive tag is an inactive_tag error (IsInactiveTag) and a tag belonging to
// a different application is an application_mismatch (IsApplicationMismatch).
func (s *AssignmentService) Create(ctx context.Context, in AssignmentCreate, opts ...RequestOption) (*Assignment, error) {
	return doData[Assignment](ctx, s.client, http.MethodPost, "/tag-assignments", nil, in, opts...)
}

// Remove deletes one assignment (DELETE /tag-assignments), answering 204.
//
// THIS DELETE CARRIES A BODY, which is unusual and is how the row is identified:
// there is no /tag-assignments/{id} route. One consequence reaches the caller --
// WithApplication is refused here, as on any body-carrying request, because
// AssignmentRemove.ApplicationID is the authoritative one.
//
// It is idempotent in the other direction too: removing an assignment that does
// not exist is a 204, not a 404. A caller wanting to know whether anything was
// actually deleted should use BulkRemove, whose Removed count reports it.
//
// Removal deletes the assignment row outright. That is a real delete, and the
// exception to Octonomy's usual rule -- tags, vocabularies, and aliases are
// deactivated rather than deleted, but an assignment is a link, and an inactive
// link is just an absent one.
func (s *AssignmentService) Remove(ctx context.Context, in AssignmentRemove, opts ...RequestOption) error {
	return s.client.do(ctx, http.MethodDelete, "/tag-assignments", nil, in, opts...)
}

// BulkAssign assigns many tags to one resource
// (POST /tag-assignments/bulk-assign).
//
// IT IS ALL OR NOTHING. An id that names no tag the caller may see fails the
// entire call with a validation_error listing the offending ids -- nothing is
// partially applied. An id outside the request's namespace reports identically
// to one that does not exist anywhere, deliberately: distinguishing them would
// tell a caller that an id names a real tag in a namespace they cannot read.
//
// The response is a COMPOSITE under the data envelope, not a bare array as
// docs/openapi-v2.yaml claims. Decoding that body into a []Assignment yields an
// empty slice and a nil error -- #32 in a new place -- so this goes through
// doData with a result struct, and the envelope assertion is what catches a
// mis-routed decode.
func (s *AssignmentService) BulkAssign(ctx context.Context, in BulkAssign, opts ...RequestOption) (*BulkAssignResult, error) {
	return doData[BulkAssignResult](ctx, s.client, http.MethodPost, "/tag-assignments/bulk-assign", nil, in, opts...)
}

// BulkRemove removes many tags from one resource
// (POST /tag-assignments/bulk-remove). It is a POST, not a DELETE.
//
// Unlike BulkAssign it tolerates ids that match nothing: they are counted out of
// Removed rather than raising. The size cap still applies.
//
// The response is {"data": {"removed": N}}, a shape docs/openapi-v2.yaml does
// not describe at all -- it documents the 200 with no schema whatsoever. Same
// treatment as BulkAssign, and the same reason.
func (s *AssignmentService) BulkRemove(ctx context.Context, in BulkRemove, opts ...RequestOption) (*BulkRemoveResult, error) {
	return doData[BulkRemoveResult](ctx, s.client, http.MethodPost, "/tag-assignments/bulk-remove", nil, in, opts...)
}
