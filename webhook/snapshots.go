package webhook

import (
	"time"

	octonomy "github.com/octoverse-id/octonomy-go/v2"
)

// The four snapshot types below describe the entity state an event carries.
//
// # They are NOT the REST models, and must never be replaced by them
//
// octonomy.Tag, octonomy.Vocabulary, octonomy.TagAlias, and octonomy.Assignment
// describe a GET response: every documented field is present, because the
// server just serialized a whole row. A snapshot is a different thing wearing
// similar field names, and it differs in two ways that both produce silently
// wrong answers when the REST model is reused:
//
//  1. It carries FEWER fields. A tag snapshot omits usage_count,
//     namespace_type, and namespace_id -- all three of which the v2 REST Tag
//     carries and the contract marks required. The namespace is on the
//     ENVELOPE, where routing reads it ([Event.Namespace]); usage_count is
//     server-computed and simply not part of an event.
//  2. An *.updated payload carries ONLY THE FIELDS THAT CHANGED. Decoded into
//     a REST model, every field the update did not touch comes back as the zero
//     value -- "" for a name, false for is_active -- and nothing distinguishes
//     those from a name that really was cleared or a row that really was
//     deactivated.
//
// # Every field is an octonomy.Optional, and a pointer would not have done
//
// A field on a snapshot says one of THREE things, and which one matters:
//
//	octonomy.Optional[string]{}    // absent -- this event did not change it
//	octonomy.Null[string]()        // present and null -- it was CLEARED
//	octonomy.Set("summer-sale")    // present with a value -- it is now this
//
// A *string carries two of those. It would collapse "not changed" and "cleared
// to null" into one nil, and four fields on this API can really be cleared --
// TagUpdate.ParentID, TagUpdate.VocabularyID, TagUpdate.Description and
// VocabularyUpdate.Description, verified live against a running server. A
// tag.updated that un-nests a tag sends {"after":{"parent_id":null}}, and a
// consumer applying the diff off a nil pointer would leave the tag nested under
// a parent the server no longer has. That is the same wall #64 hit from the
// encoding side, which is why octonomy.Optional exists; a snapshot needs it for
// the same reason a PATCH body does.
//
// Read a field with Get, which reports whether a value arrived and never
// panics:
//
//	if slug, ok := snapshot.Slug.Get(); ok {
//		index.Rename(event.AggregateID, slug)
//	}
//
// IsZero separates "absent" from "cleared" where the difference is load-bearing:
// IsZero is true only for absent, and IsNull only for an explicit null.
//
// # Identity lives on the envelope
//
// A snapshot's ID is Optional like everything else, and on an *.updated payload
// it is ABSENT -- the changed-field set has no id in it. Use [Event.AggregateID]
// for the row's identity; it is required on every delivery and validated on the
// way in. ID here is the id the snapshot itself carried, which a *.created
// payload has and an *.updated one does not.
//
// # omitzero is required, not decorative
//
// Every field is tagged `json:",omitzero"` so a snapshot re-encodes to exactly
// the keys it arrived with -- which is what makes round-tripping one into a log
// or a queue lossless. An Optional that is asked to marshal while omitted
// returns an error rather than guessing null, so a field that lost the tag
// would fail loudly; TestSnapshotFieldsTagEveryOptionalOmitzero is the same
// guard at test time.

// TagSnapshot is the tag state a tag.* event carries. The fields are the
// server's tag snapshot exactly: no usage_count, no namespace. See the notes
// above -- every field is three-state, and an *.updated payload populates only
// what changed.
type TagSnapshot struct {
	// ID is the tag's id, on a payload that carries one. Prefer
	// Event.AggregateID, which is always present.
	ID octonomy.Optional[string] `json:"id,omitzero"`

	TenantID octonomy.Optional[string] `json:"tenant_id,omitzero"`

	// ApplicationID is null for a tenant-shared tag. It cannot CHANGE -- the
	// server answers 409 scope_immutable -- so it appears on a *.created
	// snapshot and never as a changed field.
	ApplicationID octonomy.Optional[string] `json:"application_id,omitzero"`

	Name octonomy.Optional[string] `json:"name,omitzero"`
	Slug octonomy.Optional[string] `json:"slug,omitzero"`

	// Type is the tag's own type string, not the event type.
	Type octonomy.Optional[string] `json:"type,omitzero"`

	// Description is nullable and clearable: an explicit null here means the
	// description was removed, which IsNull reports and a nil pointer could not.
	Description octonomy.Optional[string] `json:"description,omitzero"`

	// ParentID and VocabularyID are the tag's links, reported under their _id
	// spellings on a changed-field payload as well as on a full snapshot. Both
	// are clearable, so both reach the null state.
	ParentID     octonomy.Optional[string] `json:"parent_id,omitzero"`
	VocabularyID octonomy.Optional[string] `json:"vocabulary_id,omitzero"`

	// Metadata is the tag's own free-form object, distinct from the envelope's
	// Event.Metadata. A PATCH REPLACES it rather than merging, so when it is
	// present it is the whole new object.
	Metadata octonomy.Optional[octonomy.Metadata] `json:"metadata,omitzero"`

	// IsActive is the soft-deletion flag. It is the only field a
	// tag.deactivated payload carries, and a reactivation arrives as an
	// ordinary tag.updated flipping it back to true.
	IsActive octonomy.Optional[bool] `json:"is_active,omitzero"`

	// CreatedAt and UpdatedAt are RFC 3339 timestamps. They are on a full
	// snapshot; updated_at is not reported as a changed field.
	CreatedAt octonomy.Optional[time.Time] `json:"created_at,omitzero"`
	UpdatedAt octonomy.Optional[time.Time] `json:"updated_at,omitzero"`
}

// VocabularySnapshot is the vocabulary state a vocabulary.* event carries.
// Vocabularies have no parent and no type, and Description is the one field on
// them the server lets a caller clear.
type VocabularySnapshot struct {
	ID            octonomy.Optional[string]            `json:"id,omitzero"`
	TenantID      octonomy.Optional[string]            `json:"tenant_id,omitzero"`
	ApplicationID octonomy.Optional[string]            `json:"application_id,omitzero"`
	Name          octonomy.Optional[string]            `json:"name,omitzero"`
	Slug          octonomy.Optional[string]            `json:"slug,omitzero"`
	Description   octonomy.Optional[string]            `json:"description,omitzero"`
	Metadata      octonomy.Optional[octonomy.Metadata] `json:"metadata,omitzero"`
	IsActive      octonomy.Optional[bool]              `json:"is_active,omitzero"`
	CreatedAt     octonomy.Optional[time.Time]         `json:"created_at,omitzero"`
	UpdatedAt     octonomy.Optional[time.Time]         `json:"updated_at,omitzero"`
}

// TagAliasSnapshot is the alias state a tag_alias.* event carries.
//
// TagID is the canonical tag the alias resolves to. It is not immutable -- an
// alias can be re-pointed, which is an ordinary tag_alias.updated -- so it does
// appear as a changed field.
type TagAliasSnapshot struct {
	ID            octonomy.Optional[string]            `json:"id,omitzero"`
	TenantID      octonomy.Optional[string]            `json:"tenant_id,omitzero"`
	ApplicationID octonomy.Optional[string]            `json:"application_id,omitzero"`
	TagID         octonomy.Optional[string]            `json:"tag_id,omitzero"`
	Name          octonomy.Optional[string]            `json:"name,omitzero"`
	Slug          octonomy.Optional[string]            `json:"slug,omitzero"`
	Metadata      octonomy.Optional[octonomy.Metadata] `json:"metadata,omitzero"`
	IsActive      octonomy.Optional[bool]              `json:"is_active,omitzero"`
	CreatedAt     octonomy.Optional[time.Time]         `json:"created_at,omitzero"`
	UpdatedAt     octonomy.Optional[time.Time]         `json:"updated_at,omitzero"`
}

// AssignmentSnapshot is the assignment state an assignment.* event carries.
//
// It has no is_active and no updated_at: an assignment is created or removed,
// never edited, so both sides of an assignment event are full snapshots and
// neither is a changed-field set. ApplicationID is Optional here for uniformity
// with the other three, though an assignment is always application-scoped and
// the server always sends it.
type AssignmentSnapshot struct {
	ID            octonomy.Optional[string] `json:"id,omitzero"`
	TenantID      octonomy.Optional[string] `json:"tenant_id,omitzero"`
	ApplicationID octonomy.Optional[string] `json:"application_id,omitzero"`
	TagID         octonomy.Optional[string] `json:"tag_id,omitzero"`

	// ResourceType and ResourceID are the caller's own opaque identifiers for
	// the external row. Octonomy never dereferences them.
	ResourceType octonomy.Optional[string] `json:"resource_type,omitzero"`
	ResourceID   octonomy.Optional[string] `json:"resource_id,omitzero"`

	// AssignedBy is the row's own attribution field, distinct from the
	// envelope's ActorID, which attributes the REQUEST.
	AssignedBy octonomy.Optional[string]    `json:"assigned_by,omitzero"`
	AssignedAt octonomy.Optional[time.Time] `json:"assigned_at,omitzero"`
}
