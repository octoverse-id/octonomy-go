package webhook

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	octonomy "github.com/octoverse-id/octonomy-go/v2"
)

// EventType is the "event_type" field of a delivery: what happened.
//
// It is a defined string rather than a bare one so the eleven types the server
// documents can be named as constants, and it is a STRING rather than an
// enumerated integer so that an event type this package has never heard of
// still round-trips intact. A twelfth type added by a future server arrives
// here as itself, not as a zero value -- see [Event] on why that case must not
// be fatal.
type EventType string

// The eleven event types Octonomy emits, from the server's docs/events.md.
//
// Events are emitted only for REAL changes: an idempotent no-op write, a
// repeated delete, and an update that changes nothing emit nothing at all. A
// consumer therefore never sees an event whose before and after are equal.
//
// There is no "removed" for a tag, vocabulary, or alias and no "updated" for an
// assignment, and neither is an oversight. Deletion on this server is
// DEACTIVATION -- the row survives with is_active false -- and an assignment is
// a link that is created or removed rather than edited.
const (
	EventTagCreated     EventType = "tag.created"
	EventTagUpdated     EventType = "tag.updated"
	EventTagDeactivated EventType = "tag.deactivated"

	EventVocabularyCreated     EventType = "vocabulary.created"
	EventVocabularyUpdated     EventType = "vocabulary.updated"
	EventVocabularyDeactivated EventType = "vocabulary.deactivated"

	EventTagAliasCreated     EventType = "tag_alias.created"
	EventTagAliasUpdated     EventType = "tag_alias.updated"
	EventTagAliasDeactivated EventType = "tag_alias.deactivated"

	EventAssignmentCreated EventType = "assignment.created"
	EventAssignmentRemoved EventType = "assignment.removed"
)

// AggregateType is the "aggregate_type" field: which kind of row the event is
// about. It is carried verbatim from the delivery and is redundant with
// [EventType] -- every tag.* event names the tag aggregate -- so route on the
// event type and read this one for logging.
type AggregateType string

// The four aggregate types. Note that the assignment aggregate is spelled
// "tag_assignment" while its events are spelled "assignment.*"; both spellings
// are the server's and neither is normalized here.
const (
	AggregateTag           AggregateType = "tag"
	AggregateVocabulary    AggregateType = "vocabulary"
	AggregateTagAlias      AggregateType = "tag_alias"
	AggregateTagAssignment AggregateType = "tag_assignment"
)

// eventShape is what this package knows about one event type: which aggregate's
// snapshot its payload carries, and which sides that payload must have.
type eventShape struct {
	aggregate AggregateType
	// before and after are the sides docs/events.md documents for this event
	// type. They are REQUIRED, not optional: a tag.created whose payload has no
	// "after" carries no tag, and handing that back as a TagPayload with a nil
	// After is how a consumer's event.Tag.After.Slug becomes a nil dereference
	// on a delivery that decoded without error.
	before bool
	after  bool
}

// eventShapes is the single list of what this package knows. [EventType.Known]
// reads it, and [Event.UnmarshalJSON] uses it to pick the payload type to
// decode into and the sides to insist on.
//
// A created event carries only "after" and a removed assignment only "before";
// updated and deactivated carry both. There is no assignment.updated, which is
// why the assignment rows are the only asymmetric pair.
var eventShapes = map[EventType]eventShape{
	EventTagCreated:            {aggregate: AggregateTag, after: true},
	EventTagUpdated:            {aggregate: AggregateTag, before: true, after: true},
	EventTagDeactivated:        {aggregate: AggregateTag, before: true, after: true},
	EventVocabularyCreated:     {aggregate: AggregateVocabulary, after: true},
	EventVocabularyUpdated:     {aggregate: AggregateVocabulary, before: true, after: true},
	EventVocabularyDeactivated: {aggregate: AggregateVocabulary, before: true, after: true},
	EventTagAliasCreated:       {aggregate: AggregateTagAlias, after: true},
	EventTagAliasUpdated:       {aggregate: AggregateTagAlias, before: true, after: true},
	EventTagAliasDeactivated:   {aggregate: AggregateTagAlias, before: true, after: true},
	EventAssignmentCreated:     {aggregate: AggregateTagAssignment, after: true},
	EventAssignmentRemoved:     {aggregate: AggregateTagAssignment, before: true},
}

// Known reports whether this package has a typed payload for t.
//
// It is false for an event type added to the server after this SDK was built,
// and that is an ordinary condition rather than an error. See [Event].
func (t EventType) Known() bool {
	_, ok := eventShapes[t]
	return ok
}

// requireSides reports the side this event type documents but did not carry.
func (s eventShape) requireSides(eventType EventType, hasBefore, hasAfter bool) error {
	if s.before && !hasBefore {
		return fmt.Errorf("%w: payload.before on %s", ErrIncompleteEvent, eventType)
	}
	if s.after && !hasAfter {
		return fmt.Errorf("%w: payload.after on %s", ErrIncompleteEvent, eventType)
	}
	return nil
}

// String returns the raw wire value.
func (t EventType) String() string { return string(t) }

// Errors returned when a delivery cannot be turned into an [Event]. They are
// separate from the signature sentinels in verify.go because they answer a
// different question: those say whether the bytes came from Octonomy, these say
// whether the bytes Octonomy sent are an event this package can describe.
//
// All three are reached only AFTER the signature has been verified, so none of
// them describes an attacker. They describe a delivery this SDK does not
// understand, which in practice means the contract moved.
var (
	// ErrMalformedEvent reports a body that is not a JSON object at all, or
	// whose envelope fields do not have the documented types. The underlying
	// encoding/json error is wrapped in.
	ErrMalformedEvent = errors.New("octonomy: webhook event is not a well-formed envelope")

	// ErrIncompleteEvent reports a delivery missing something the event cannot
	// be processed without. The error names it. Three groups:
	//
	//   - An identity field: id, tenant_id, event_type, aggregate_type, or
	//     aggregate_id. The server marks every one non-null, so a blank one is
	//     not a delivery with less information in it -- it is one this SDK
	//     would otherwise hand back as a zero-valued Event with a nil error,
	//     which is the silent-zero failure the root package refuses at #32 and
	//     #40. Deduplication depends on a stable id, and a consumer that
	//     dedupes on "" drops every event after the first.
	//   - An INCOHERENT namespace pair: one half set and the other null, or a
	//     half present but blank. Both halves null is the global namespace and
	//     both absent is a pre-namespace server, so neither of those is
	//     refused; a half-set pair is neither, and reading one half alone would
	//     report a merchant's event as global.
	//   - A payload side its event type documents: payload itself, or the
	//     before/after a known event type must carry. See [Event] on why a
	//     missing side cannot be handed back as a nil field.
	ErrIncompleteEvent = errors.New("octonomy: webhook event is missing a required field")

	// ErrMalformedPayload reports a payload that does not fit the shape its own
	// event type documents -- a tag.created whose "after" is an array, a
	// timestamp that is not RFC 3339.
	//
	// It is raised only for a KNOWN event type. An unknown one is never decoded
	// into a typed payload at all, so it cannot fail this way: see [Event].
	ErrMalformedPayload = errors.New("octonomy: webhook event payload does not fit its event type")
)

// Event is one delivered outbox event: the envelope every delivery carries,
// plus the typed payload for its event type.
//
// # Read routing from here, never from the headers
//
// The signature covers the BODY, so these fields are the authenticated ones and
// the X-Octonomy-* headers are not -- a wholly genuine delivery replayed with
// X-Octonomy-Tenant-ID rewritten verifies exactly as it did the first time.
// Partition on (TenantID, ApplicationID, NamespaceType, NamespaceID). A nil
// NamespaceType is the CONCRETE GLOBAL namespace and never a wildcard: a global
// event is tenant-shared and visible to every merchant under the application,
// while a namespaced event belongs to exactly one merchant and must never be
// fanned out to the others.
//
// # An unknown event type is not an error
//
// Delivery is at-least-once with exponential backoff and dead-lettering. If
// this package refused an event type it did not recognize, the day Octonomy
// adds a twelfth type every deployed Go consumer would start retrying and
// dead-lettering it -- consumers who shipped no code and did nothing wrong.
//
// So an unrecognized event type parses successfully: [Event.Type] holds the raw
// string, [Event.Payload] holds the undecoded JSON, [EventType.Known] reports
// false, and all four typed payload fields are nil. The case is visible without
// being fatal. A handler's default branch must ACKNOWLEDGE it -- return nil
// from an [EventHandler], or answer 2xx from a handler of your own.
//
// Decoding is deliberately lenient about fields it has never seen, for the same
// reason: a snapshot field added by a later server is ignored rather than
// refused, and [Event.Payload] still holds the bytes it arrived in.
//
// # Deduplicate on ID
//
// The outbox publishes outside the row-locking transaction and marks the row
// afterwards, so a crash in between -- or a recovered processing claim --
// redelivers. ID is stable across redeliveries and is the dedupe key;
// OperationID groups every event one request emitted, and is how a bulk or
// replace operation is reconstructed. Ordering is best-effort by creation time
// and is not guaranteed under retries, so two events from one operation may
// arrive interleaved.
type Event struct {
	// ID is the event's unique id, stable across redeliveries. Dedupe on it.
	ID string `json:"id"`

	// TenantID is the owning tenant. Always present.
	TenantID string `json:"tenant_id"`

	// ApplicationID is the owning application, or nil for a tenant-shared row.
	ApplicationID *string `json:"application_id"`

	// NamespaceType and NamespaceID are the merchant or sub-tenant namespace
	// axis. Both nil is the concrete GLOBAL namespace -- not a wildcard, and not
	// "unknown". Values are opaque, caller-canonical strings: match them
	// exactly, with no case folding or normalization.
	//
	// They are set together or not at all; a delivery with one half set is
	// refused rather than decoded, so no value of this pair can be read as
	// global when it is not. [Event.Namespace] is the read that says so.
	NamespaceType *string `json:"namespace_type"`
	NamespaceID   *string `json:"namespace_id"`

	// Type is what happened. Compare it against the Event* constants, and
	// handle the unknown case by acknowledging it.
	Type EventType `json:"event_type"`

	// AggregateType and AggregateID identify the row the event is about.
	// AggregateID is the identity of that row: the partial payloads on an
	// *.updated event carry no "id" of their own, so this is where it lives.
	AggregateType AggregateType `json:"aggregate_type"`
	AggregateID   string        `json:"aggregate_id"`

	// OperationID correlates every event and audit row one request emitted. A
	// bulk or replace operation shares one across all of its events.
	OperationID *string `json:"operation_id"`

	// RequestID is the inbound X-Request-ID, when the caller sent one. It is
	// also the only X-Octonomy-* header the server sends CONDITIONALLY, so a
	// delivery without it is ordinary rather than malformed.
	RequestID *string `json:"request_id"`

	// ActorID is the resolved actor: X-Actor-ID, or the service client's name.
	ActorID *string `json:"actor_id"`

	// TagID is the related tag, when the event has one.
	TagID *string `json:"tag_id"`

	// ResourceType and ResourceID name the external resource an assignment
	// event is about. They are nil on every other event type.
	ResourceType *string `json:"resource_type"`
	ResourceID   *string `json:"resource_id"`

	// Metadata is the envelope's free-form object, {} by default. It is NOT the
	// aggregate's own metadata -- that lives on the snapshot in the payload.
	// Decode it into a struct of your own with octonomy.DecodeMetadata.
	Metadata octonomy.Metadata `json:"metadata"`

	// Payload is the raw, undecoded payload exactly as it arrived. It is always
	// populated, including for an event type this package does not know, which
	// is the only place that case can be inspected.
	Payload json.RawMessage `json:"payload"`

	// Tag, Vocabulary, TagAlias, and Assignment are the typed payload. AT MOST
	// ONE is non-nil, chosen by Type, and ALL are nil when Type is unknown.
	//
	// They are populated by decoding Payload rather than by the JSON decoder,
	// which is why they are tagged "-": a delivery carrying a top-level "tag"
	// key must not be able to reach them.
	Tag        *TagPayload        `json:"-"`
	Vocabulary *VocabularyPayload `json:"-"`
	TagAlias   *TagAliasPayload   `json:"-"`
	Assignment *AssignmentPayload `json:"-"`
}

// Known reports whether this package has a typed payload for the event's type.
// When it is false, every typed payload field is nil and [Event.Payload] is the
// only way to see what arrived.
func (e *Event) Known() bool { return e.Type.Known() }

// Namespace reports the routing namespace: the type, the id, and whether the
// event is namespaced at all.
//
// The third return is what keeps the global namespace from being mistaken for
// a missing one. It is false for a global (tenant-shared) event, where both
// strings are empty and the event is visible to every merchant under the
// application; it is true for a merchant event, which belongs to exactly one
// namespace and must not be fanned out.
//
// There is no third case: a half-set pair is refused at decode time, so false
// here always means the global namespace and never "half of one arrived".
func (e *Event) Namespace() (namespaceType, namespaceID string, namespaced bool) {
	if e.NamespaceType == nil || e.NamespaceID == nil {
		return "", "", false
	}
	return *e.NamespaceType, *e.NamespaceID, true
}

// UnmarshalJSON implements json.Unmarshaler.
//
// It exists so that json.Unmarshal(body, &event) and [ParseEvent] cannot
// disagree. Without it, the ordinary decoder would fill the envelope, leave
// every typed payload nil, and report no error -- an Event that looks decoded,
// has no payload, and names nothing that went wrong.
func (e *Event) UnmarshalJSON(data []byte) error {
	// envelope drops the methods, so the decoder below cannot recurse into this
	// one. The typed payload fields come along but are tagged "-", so they stay
	// nil here and are filled from Payload afterwards.
	type envelope Event
	var decoded envelope
	if err := json.Unmarshal(data, &decoded); err != nil {
		return fmt.Errorf("%w: %w", ErrMalformedEvent, err)
	}

	// Ordered as the server serializes them, so a truncated or hand-built
	// delivery names the first field it is missing rather than an arbitrary one.
	for _, field := range []struct {
		name  string
		value string
	}{
		{"id", decoded.ID},
		{"tenant_id", decoded.TenantID},
		{"event_type", string(decoded.Type)},
		{"aggregate_type", string(decoded.AggregateType)},
		{"aggregate_id", decoded.AggregateID},
	} {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("%w: %s", ErrIncompleteEvent, field.name)
		}
	}

	// The namespace pair is set TOGETHER or not at all. The server emits both
	// null for a global row, both populated for a namespaced one, and a
	// pre-namespace server emits neither -- so absent and null are the same
	// fact and neither is refused here. A HALF-SET pair is none of the three,
	// and reading either half alone would report it as global, which routes a
	// merchant's event into the tenant-shared partition and acknowledges it.
	if (decoded.NamespaceType == nil) != (decoded.NamespaceID == nil) {
		return fmt.Errorf("%w: namespace_type and namespace_id are set together or not at all", ErrIncompleteEvent)
	}
	for _, field := range []struct {
		name  string
		value *string
	}{
		{"namespace_type", decoded.NamespaceType},
		{"namespace_id", decoded.NamespaceID},
	} {
		// A blank one is the same hazard wearing a different spelling: it is
		// neither the global namespace nor a namespace anything can match.
		if field.value != nil && strings.TrimSpace(*field.value) == "" {
			return fmt.Errorf("%w: %s is present but blank", ErrIncompleteEvent, field.name)
		}
	}

	*e = Event(decoded)

	// An unknown event type stops here: Payload keeps the bytes, every typed
	// field stays nil, and nothing about the delivery is refused. Decoding an
	// unrecognized payload into whichever struct its aggregate suggested is
	// exactly how a future event type would become a permanent 5xx.
	shape, known := eventShapes[e.Type]
	if !known {
		return nil
	}
	if len(e.Payload) == 0 || bytes.Equal(e.Payload, []byte("null")) {
		return fmt.Errorf("%w: payload", ErrIncompleteEvent)
	}

	// json.Unmarshal ignores keys the target does not name, which is the
	// forward-compatible direction: a snapshot field a later server adds is
	// dropped here and still readable in Payload. DisallowUnknownFields would
	// turn that addition into a dead-letter for every deployed consumer.
	//
	// The sides, though, are checked: this SDK claims to understand these
	// eleven types, and a payload missing the side its type documents is not
	// one of them. Without that check a tag.created with an empty payload
	// decodes without error into a TagPayload whose After is nil, and the
	// documented event.Tag.After.Slug is a nil dereference on a delivery
	// nothing reported as wrong.
	var err error
	switch shape.aggregate {
	case AggregateTag:
		payload := new(TagPayload)
		if err = json.Unmarshal(e.Payload, payload); err == nil {
			e.Tag = payload
			err = shape.requireSides(e.Type, payload.Before != nil, payload.After != nil)
		}
	case AggregateVocabulary:
		payload := new(VocabularyPayload)
		if err = json.Unmarshal(e.Payload, payload); err == nil {
			e.Vocabulary = payload
			err = shape.requireSides(e.Type, payload.Before != nil, payload.After != nil)
		}
	case AggregateTagAlias:
		payload := new(TagAliasPayload)
		if err = json.Unmarshal(e.Payload, payload); err == nil {
			e.TagAlias = payload
			err = shape.requireSides(e.Type, payload.Before != nil, payload.After != nil)
		}
	case AggregateTagAssignment:
		payload := new(AssignmentPayload)
		if err = json.Unmarshal(e.Payload, payload); err == nil {
			e.Assignment = payload
			err = shape.requireSides(e.Type, payload.Before != nil, payload.After != nil)
		}
	}
	if err != nil {
		// Leave nothing half-decoded behind: a caller who ignored the error
		// would otherwise find a payload struct with some fields set.
		e.Tag, e.Vocabulary, e.TagAlias, e.Assignment = nil, nil, nil, nil
		if errors.Is(err, ErrIncompleteEvent) {
			return err
		}
		return fmt.Errorf("%w on %s: %w", ErrMalformedPayload, e.Type, err)
	}
	return nil
}

// ParseEvent decodes one verified delivery body into an [Event].
//
// # Verify first, always
//
// body must be a body [Verify] has already accepted. Until it has, the bytes
// are an anonymous POST claiming to be from Octonomy, and everything this
// function returns is whatever the sender chose to send -- including the tenant
// and the namespace a consumer routes on. [Handler] owns that order so it
// cannot be got wrong; this function is for a consumer whose HTTP layer is
// someone else's (a framework's router, a queue that stored the raw body), and
// it takes bytes for the same reason Verify does.
//
// An event type this package does not know is NOT an error -- see [Event].
func ParseEvent(body []byte) (*Event, error) {
	var event Event
	if err := json.Unmarshal(body, &event); err != nil {
		// A body that is not JSON at all fails before UnmarshalJSON is reached,
		// so it arrives here unwrapped; anything this package raised is already
		// carrying its sentinel and must not be wrapped in a second one.
		if errors.Is(err, ErrMalformedEvent) || errors.Is(err, ErrIncompleteEvent) || errors.Is(err, ErrMalformedPayload) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %w", ErrMalformedEvent, err)
	}
	return &event, nil
}

// TagPayload is the payload of a tag.* event.
//
// Which sides are populated depends on the event type, and the sides that
// event type documents are GUARANTEED NON-NIL -- a delivery without them is
// refused as [ErrIncompleteEvent] rather than handed over with a nil field, so
// event.Tag.After is safe to dereference on a tag.created:
//
//   - tag.created -- After only, a full snapshot
//   - tag.updated -- Before and After, carrying ONLY the fields that changed
//   - tag.deactivated -- Before and After, carrying is_active alone
//
// A side the event type does not document is nil rather than an empty snapshot.
type TagPayload struct {
	// Before is the state that changed, and After is what it changed to. On an
	// *.updated event each holds only the fields that actually changed; see
	// [TagSnapshot].
	Before *TagSnapshot `json:"before,omitempty"`
	After  *TagSnapshot `json:"after,omitempty"`

	// CascadedAliasIDs lists the aliases a tag.deactivated also deactivated,
	// and is present only when the cascade touched something. Each of those
	// aliases ALSO gets its own tag_alias.deactivated event carrying a
	// [TagAliasPayload.Cascade] pointing back here, so the same fact arrives
	// twice by design: once as a summary on the tag and once per alias. Handle
	// whichever suits you and ignore the other -- but idempotently, because
	// both may be redelivered.
	CascadedAliasIDs []string `json:"cascaded_alias_ids,omitempty"`
}

// VocabularyPayload is the payload of a vocabulary.* event. The sides follow
// the same rule as [TagPayload]; vocabularies have no cascade.
type VocabularyPayload struct {
	Before *VocabularySnapshot `json:"before,omitempty"`
	After  *VocabularySnapshot `json:"after,omitempty"`
}

// TagAliasPayload is the payload of a tag_alias.* event. The sides follow the
// same rule as [TagPayload].
type TagAliasPayload struct {
	Before *TagAliasSnapshot `json:"before,omitempty"`
	After  *TagAliasSnapshot `json:"after,omitempty"`

	// Cascade is set on a tag_alias.deactivated that was caused by its tag's
	// deactivation rather than by a direct call, and names the tag. A nil
	// Cascade means the alias was deactivated on its own.
	Cascade *Cascade `json:"cascade,omitempty"`
}

// AssignmentPayload is the payload of an assignment.* event.
//
// Both sides are FULL snapshots here, because an assignment is a link that is
// created or removed rather than edited: assignment.created carries After and
// assignment.removed carries Before -- each guaranteed non-nil for its event
// type, as on [TagPayload]. There is no assignment.updated.
type AssignmentPayload struct {
	Before *AssignmentSnapshot `json:"before,omitempty"`
	After  *AssignmentSnapshot `json:"after,omitempty"`
}

// Cascade records that an event was caused by another event rather than by a
// direct call. Today the server emits exactly one cascade: a tag.deactivated
// deactivating the tag's active aliases.
type Cascade struct {
	// SourceEventType is the event that caused this one -- tag.deactivated.
	SourceEventType EventType `json:"source_event_type"`
	// SourceTagID is the tag that was deactivated.
	SourceTagID string `json:"source_tag_id"`
}
