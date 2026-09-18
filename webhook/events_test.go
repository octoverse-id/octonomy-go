package webhook

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	octonomy "github.com/octoverse-id/octonomy-go/v2"
)

// envelope builds one delivery the way the server serializes it: every one of
// the sixteen documented keys present, compact separators, sorted keys. Tests
// that need a field missing remove it from the JSON explicitly, so "the server
// always sends this" and "this test happens not to set it" can never be
// confused.
func envelope(eventType EventType, aggregateType AggregateType, payload string) string {
	return fmt.Sprintf(`{"actor_id":"user_42","aggregate_id":"9c2b0f41-5d33-4a6f-8b17-2e4c9a7d0b55",`+
		`"aggregate_type":%q,"application_id":"storefront","event_type":%q,`+
		`"id":"0f1d7b24-2c1e-4f9a-9f3a-3a5f1c2d6e77","metadata":{"source":"api"},`+
		`"namespace_id":null,"namespace_type":null,`+
		`"operation_id":"5f0a3d18-7b62-4c19-9e84-1d6b8f2a4c30","payload":%s,`+
		`"request_id":null,"resource_id":null,"resource_type":null,`+
		`"tag_id":"9c2b0f41-5d33-4a6f-8b17-2e4c9a7d0b55","tenant_id":"acme"}`,
		string(aggregateType), string(eventType), payload)
}

// mustParse parses a delivery that is expected to be well formed.
func mustParse(t *testing.T, body string) *Event {
	t.Helper()
	event, err := ParseEvent([]byte(body))
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}
	return event
}

// value is the Optional read every assertion below makes: a field that carries
// a value, and a test failure when it does not.
func value[T any](t *testing.T, field octonomy.Optional[T], name string) T {
	t.Helper()
	v, ok := field.Get()
	if !ok {
		t.Fatalf("%s carries no value (absent: %t, null: %t)", name, field.IsZero(), field.IsNull())
	}
	return v
}

// The payloads below are the eleven shapes docs/events.md documents, one per
// event type, written out rather than generated so a change to the contract is
// a visible diff here.
const (
	tagSnapshotJSON = `{"id":"9c2b0f41-5d33-4a6f-8b17-2e4c9a7d0b55","tenant_id":"acme",` +
		`"application_id":"storefront","name":"Summer Sale","slug":"summer-sale","type":"campaign",` +
		`"description":"Seasonal promotion","parent_id":null,` +
		`"vocabulary_id":"3b7e5a90-1c48-4d2b-a6f5-8e0d9c1b4a26","metadata":{"team":"growth"},` +
		`"is_active":true,"created_at":"2026-09-18T10:11:12.123456+00:00",` +
		`"updated_at":"2026-09-18T10:11:12.123456+00:00"}`

	vocabularySnapshotJSON = `{"id":"3b7e5a90-1c48-4d2b-a6f5-8e0d9c1b4a26","tenant_id":"acme",` +
		`"application_id":null,"name":"Campaigns","slug":"campaigns","description":null,` +
		`"metadata":{},"is_active":true,"created_at":"2026-09-18T10:11:12+00:00",` +
		`"updated_at":"2026-09-18T10:11:12+00:00"}`

	aliasSnapshotJSON = `{"id":"7d4c1a86-9e03-4b57-8f21-6c5d0e3a2b14","tenant_id":"acme",` +
		`"application_id":"storefront","tag_id":"9c2b0f41-5d33-4a6f-8b17-2e4c9a7d0b55",` +
		`"name":"Summer Promo","slug":"summer-promo","metadata":{},"is_active":true,` +
		`"created_at":"2026-09-18T10:11:12+00:00","updated_at":"2026-09-18T10:11:12+00:00"}`

	assignmentSnapshotJSON = `{"id":"1a2b3c4d-5e6f-4071-8293-a4b5c6d7e8f9","tenant_id":"acme",` +
		`"application_id":"storefront","tag_id":"9c2b0f41-5d33-4a6f-8b17-2e4c9a7d0b55",` +
		`"resource_type":"order","resource_id":"ord_1001","assigned_by":"user_42",` +
		`"assigned_at":"2026-09-18T10:11:12+00:00"}`
)

// TestParseEventDecodesEveryEventType walks all eleven types the server
// documents and asserts that each one lands in the right typed payload -- and,
// just as importantly, in only that one. A decoder that filled two of them
// would let a consumer's switch read a tag out of a vocabulary event.
func TestParseEventDecodesEveryEventType(t *testing.T) {
	cases := []struct {
		eventType EventType
		aggregate AggregateType
		payload   string
		// assert checks the one typed field this event type fills.
		assert func(t *testing.T, event *Event)
	}{
		{
			eventType: EventTagCreated,
			aggregate: AggregateTag,
			payload:   `{"after":` + tagSnapshotJSON + `}`,
			assert: func(t *testing.T, event *Event) {
				if event.Tag.Before != nil {
					t.Error("a created event has no before side")
				}
				after := event.Tag.After
				if got := value(t, after.Slug, "slug"); got != "summer-sale" {
					t.Errorf("slug = %q", got)
				}
				if got := value(t, after.Type, "type"); got != "campaign" {
					t.Errorf("type = %q", got)
				}
				if !after.ParentID.IsNull() {
					t.Error("parent_id arrived as null and must read as null")
				}
				metadata := value(t, after.Metadata, "metadata")
				if metadata["team"] != "growth" {
					t.Errorf("metadata = %v", metadata)
				}
				created := value(t, after.CreatedAt, "created_at")
				if !created.Equal(time.Date(2026, 9, 18, 10, 11, 12, 123456000, time.UTC)) {
					t.Errorf("created_at = %s", created)
				}
			},
		},
		{
			eventType: EventTagUpdated,
			aggregate: AggregateTag,
			payload:   `{"before":{"name":"Summer Sale"},"after":{"name":"Autumn Sale"}}`,
			assert: func(t *testing.T, event *Event) {
				if got := value(t, event.Tag.Before.Name, "before.name"); got != "Summer Sale" {
					t.Errorf("before.name = %q", got)
				}
				if got := value(t, event.Tag.After.Name, "after.name"); got != "Autumn Sale" {
					t.Errorf("after.name = %q", got)
				}
				// The changed-field set carries no id; identity is on the
				// envelope, which is the whole reason AggregateID is required.
				if !event.Tag.After.ID.IsZero() {
					t.Error("an updated payload carries no id and must read as absent")
				}
			},
		},
		{
			eventType: EventTagDeactivated,
			aggregate: AggregateTag,
			payload: `{"before":{"is_active":true},"after":{"is_active":false},` +
				`"cascaded_alias_ids":["7d4c1a86-9e03-4b57-8f21-6c5d0e3a2b14"]}`,
			assert: func(t *testing.T, event *Event) {
				if value(t, event.Tag.Before.IsActive, "before.is_active") != true {
					t.Error("before.is_active must be true")
				}
				if value(t, event.Tag.After.IsActive, "after.is_active") != false {
					t.Error("after.is_active must be false")
				}
				want := []string{"7d4c1a86-9e03-4b57-8f21-6c5d0e3a2b14"}
				if !slices.Equal(event.Tag.CascadedAliasIDs, want) {
					t.Errorf("cascaded_alias_ids = %v, want %v", event.Tag.CascadedAliasIDs, want)
				}
			},
		},
		{
			eventType: EventVocabularyCreated,
			aggregate: AggregateVocabulary,
			payload:   `{"after":` + vocabularySnapshotJSON + `}`,
			assert: func(t *testing.T, event *Event) {
				after := event.Vocabulary.After
				if got := value(t, after.Slug, "slug"); got != "campaigns" {
					t.Errorf("slug = %q", got)
				}
				// A tenant-shared vocabulary: present, and null.
				if !after.ApplicationID.IsNull() {
					t.Error("application_id arrived as null and must read as null")
				}
				if metadata := value(t, after.Metadata, "metadata"); len(metadata) != 0 {
					t.Errorf("metadata = %v, want an empty object", metadata)
				}
			},
		},
		{
			eventType: EventVocabularyUpdated,
			aggregate: AggregateVocabulary,
			payload:   `{"before":{"description":"Old"},"after":{"description":null}}`,
			assert: func(t *testing.T, event *Event) {
				if got := value(t, event.Vocabulary.Before.Description, "before.description"); got != "Old" {
					t.Errorf("before.description = %q", got)
				}
				if !event.Vocabulary.After.Description.IsNull() {
					t.Error("a cleared description must read as null, not as absent")
				}
			},
		},
		{
			eventType: EventVocabularyDeactivated,
			aggregate: AggregateVocabulary,
			payload:   `{"before":{"is_active":true},"after":{"is_active":false}}`,
			assert: func(t *testing.T, event *Event) {
				if value(t, event.Vocabulary.After.IsActive, "after.is_active") != false {
					t.Error("after.is_active must be false")
				}
			},
		},
		{
			eventType: EventTagAliasCreated,
			aggregate: AggregateTagAlias,
			payload:   `{"after":` + aliasSnapshotJSON + `}`,
			assert: func(t *testing.T, event *Event) {
				after := event.TagAlias.After
				if got := value(t, after.Slug, "slug"); got != "summer-promo" {
					t.Errorf("slug = %q", got)
				}
				if got := value(t, after.TagID, "tag_id"); got != "9c2b0f41-5d33-4a6f-8b17-2e4c9a7d0b55" {
					t.Errorf("tag_id = %q", got)
				}
				if event.TagAlias.Cascade != nil {
					t.Error("a direct create carries no cascade")
				}
			},
		},
		{
			eventType: EventTagAliasUpdated,
			aggregate: AggregateTagAlias,
			payload:   `{"before":{"tag_id":"9c2b0f41-5d33-4a6f-8b17-2e4c9a7d0b55"},"after":{"tag_id":"3b7e5a90-1c48-4d2b-a6f5-8e0d9c1b4a26"}}`,
			assert: func(t *testing.T, event *Event) {
				if got := value(t, event.TagAlias.After.TagID, "after.tag_id"); got != "3b7e5a90-1c48-4d2b-a6f5-8e0d9c1b4a26" {
					t.Errorf("after.tag_id = %q", got)
				}
			},
		},
		{
			eventType: EventTagAliasDeactivated,
			aggregate: AggregateTagAlias,
			payload: `{"before":{"is_active":true},"after":{"is_active":false},` +
				`"cascade":{"source_event_type":"tag.deactivated","source_tag_id":"9c2b0f41-5d33-4a6f-8b17-2e4c9a7d0b55"}}`,
			assert: func(t *testing.T, event *Event) {
				cascade := event.TagAlias.Cascade
				if cascade == nil {
					t.Fatal("a cascaded deactivation must carry its cascade")
				}
				if cascade.SourceEventType != EventTagDeactivated {
					t.Errorf("source_event_type = %q", cascade.SourceEventType)
				}
				if cascade.SourceTagID != "9c2b0f41-5d33-4a6f-8b17-2e4c9a7d0b55" {
					t.Errorf("source_tag_id = %q", cascade.SourceTagID)
				}
			},
		},
		{
			eventType: EventAssignmentCreated,
			aggregate: AggregateTagAssignment,
			payload:   `{"after":` + assignmentSnapshotJSON + `}`,
			assert: func(t *testing.T, event *Event) {
				after := event.Assignment.After
				if got := value(t, after.ResourceID, "resource_id"); got != "ord_1001" {
					t.Errorf("resource_id = %q", got)
				}
				if got := value(t, after.AssignedBy, "assigned_by"); got != "user_42" {
					t.Errorf("assigned_by = %q", got)
				}
				if event.Assignment.Before != nil {
					t.Error("a created assignment has no before side")
				}
			},
		},
		{
			eventType: EventAssignmentRemoved,
			aggregate: AggregateTagAssignment,
			payload:   `{"before":` + assignmentSnapshotJSON + `}`,
			assert: func(t *testing.T, event *Event) {
				if event.Assignment.After != nil {
					t.Error("a removed assignment has no after side")
				}
				if got := value(t, event.Assignment.Before.ResourceType, "resource_type"); got != "order" {
					t.Errorf("resource_type = %q", got)
				}
			},
		},
	}

	// Every known event type must appear above. A twelfth type added to
	// eventAggregates and not to this table would ship a payload nothing
	// decodes in anger.
	covered := make(map[EventType]bool, len(cases))
	for _, tc := range cases {
		covered[tc.eventType] = true
	}
	for _, eventType := range slices.Sorted(maps.Keys(eventAggregates)) {
		if !covered[eventType] {
			t.Errorf("%s is a known event type with no case in this table", eventType)
		}
	}

	for _, tc := range cases {
		t.Run(string(tc.eventType), func(t *testing.T) {
			event := mustParse(t, envelope(tc.eventType, tc.aggregate, tc.payload))

			if !event.Known() {
				t.Fatalf("%s must be a known event type", tc.eventType)
			}
			if event.Type != tc.eventType {
				t.Errorf("Type = %q, want %q", event.Type, tc.eventType)
			}
			if event.AggregateType != tc.aggregate {
				t.Errorf("AggregateType = %q, want %q", event.AggregateType, tc.aggregate)
			}

			// Exactly one typed payload, and it is the aggregate's.
			filled := map[AggregateType]bool{
				AggregateTag:           event.Tag != nil,
				AggregateVocabulary:    event.Vocabulary != nil,
				AggregateTagAlias:      event.TagAlias != nil,
				AggregateTagAssignment: event.Assignment != nil,
			}
			for aggregate, isSet := range filled {
				if want := aggregate == tc.aggregate; isSet != want {
					t.Errorf("%s payload set = %t, want %t", aggregate, isSet, want)
				}
			}

			// The raw payload survives decoding, for every event type and not
			// only the unknown ones.
			if !json.Valid(event.Payload) {
				t.Errorf("Payload is not valid JSON: %s", event.Payload)
			}

			tc.assert(t, event)
		})
	}
}

// TestParseEventDecodesTheWholeEnvelope covers the sixteen envelope fields
// themselves, which every event type carries identically.
func TestParseEventDecodesTheWholeEnvelope(t *testing.T) {
	event := mustParse(t, envelope(EventTagCreated, AggregateTag, `{"after":`+tagSnapshotJSON+`}`))

	if event.ID != "0f1d7b24-2c1e-4f9a-9f3a-3a5f1c2d6e77" {
		t.Errorf("ID = %q", event.ID)
	}
	if event.TenantID != "acme" {
		t.Errorf("TenantID = %q", event.TenantID)
	}
	if event.AggregateID != "9c2b0f41-5d33-4a6f-8b17-2e4c9a7d0b55" {
		t.Errorf("AggregateID = %q", event.AggregateID)
	}
	if event.ApplicationID == nil || *event.ApplicationID != "storefront" {
		t.Errorf("ApplicationID = %v", event.ApplicationID)
	}
	if event.OperationID == nil || *event.OperationID != "5f0a3d18-7b62-4c19-9e84-1d6b8f2a4c30" {
		t.Errorf("OperationID = %v", event.OperationID)
	}
	if event.ActorID == nil || *event.ActorID != "user_42" {
		t.Errorf("ActorID = %v", event.ActorID)
	}
	if event.TagID == nil || *event.TagID != "9c2b0f41-5d33-4a6f-8b17-2e4c9a7d0b55" {
		t.Errorf("TagID = %v", event.TagID)
	}
	// request_id is the CONDITIONAL header's body counterpart and is null here.
	if event.RequestID != nil {
		t.Errorf("RequestID = %v, want nil", event.RequestID)
	}
	if event.ResourceType != nil || event.ResourceID != nil {
		t.Errorf("resource fields = %v/%v, want nil on a tag event", event.ResourceType, event.ResourceID)
	}
	if event.Metadata["source"] != "api" {
		t.Errorf("Metadata = %v", event.Metadata)
	}
}

// TestEventNamespaceDoesNotTreatGlobalAsMissing is the routing hazard the
// server's own documentation calls out: null/null is the CONCRETE global
// namespace, not a wildcard and not an unknown.
func TestEventNamespaceDoesNotTreatGlobalAsMissing(t *testing.T) {
	global := mustParse(t, envelope(EventTagCreated, AggregateTag, `{"after":`+tagSnapshotJSON+`}`))
	namespaceType, namespaceID, namespaced := global.Namespace()
	if namespaced {
		t.Error("a null namespace_type is the global namespace, so namespaced must be false")
	}
	if namespaceType != "" || namespaceID != "" {
		t.Errorf("global namespace = %q/%q, want empty", namespaceType, namespaceID)
	}

	body := strings.Replace(
		envelope(EventTagCreated, AggregateTag, `{"after":`+tagSnapshotJSON+`}`),
		`"namespace_id":null,"namespace_type":null`,
		`"namespace_id":"m_42","namespace_type":"merchant"`, 1)
	merchant := mustParse(t, body)
	namespaceType, namespaceID, namespaced = merchant.Namespace()
	if !namespaced || namespaceType != "merchant" || namespaceID != "m_42" {
		t.Errorf("merchant namespace = %q/%q (namespaced %t)", namespaceType, namespaceID, namespaced)
	}
}

// TestParseEventAcceptsAnUnknownEventType is the forward-compatibility
// requirement, and it is a delivery contract rather than a nicety: an SDK that
// errored here would make every deployed consumer retry and dead-letter the day
// Octonomy adds a twelfth event type.
func TestParseEventAcceptsAnUnknownEventType(t *testing.T) {
	const payload = `{"source_tag_id":"a","target_tag_id":"b"}`
	event := mustParse(t, envelope("tag.merged", AggregateTag, payload))

	if event.Known() {
		t.Fatal("tag.merged must not be a known event type")
	}
	if event.Type != "tag.merged" {
		t.Errorf("Type = %q; the raw event type must survive", event.Type)
	}
	if event.Tag != nil || event.Vocabulary != nil || event.TagAlias != nil || event.Assignment != nil {
		t.Error("an unknown event type must fill no typed payload")
	}
	if string(event.Payload) != payload {
		t.Errorf("Payload = %s, want the undecoded payload %s", event.Payload, payload)
	}

	// A shape that would be a hard decode error if this package guessed at it
	// from the aggregate type alone. It must not be an error.
	if _, err := ParseEvent([]byte(envelope("tag.merged", AggregateTag, `{"after":[1,2,3]}`))); err != nil {
		t.Errorf("an unknown event type must not be decoded into a typed payload: %v", err)
	}

	// And an unknown type with no payload at all is still not fatal: the
	// payload requirement applies only where this SDK claims to understand it.
	missing := strings.Replace(envelope("tag.merged", AggregateTag, payload), `"payload":`+payload+`,`, "", 1)
	if _, err := ParseEvent([]byte(missing)); err != nil {
		t.Errorf("an unknown event type with no payload must parse: %v", err)
	}
}

// TestParseEventRequiresTheIdentityFields refuses the silent-zero decode: a
// blank id would leave a consumer deduplicating on "" and dropping every event
// after the first.
func TestParseEventRequiresTheIdentityFields(t *testing.T) {
	full := envelope(EventTagCreated, AggregateTag, `{"after":`+tagSnapshotJSON+`}`)

	// Each fixture names the exact envelope spelling it rewrites. The snapshot
	// in the payload repeats "id" and "tenant_id" with the same shape, so a
	// pattern that is not anchored to the envelope silently rewrites the
	// payload instead and the test asserts nothing.
	for _, field := range []struct {
		name   string
		from   string
		absent string
		blank  string
	}{
		{
			name:   "id",
			from:   `"id":"0f1d7b24-2c1e-4f9a-9f3a-3a5f1c2d6e77",`,
			absent: ``,
			blank:  `"id":"",`,
		},
		{
			name:   "tenant_id",
			from:   `,"tenant_id":"acme"}`,
			absent: `}`,
			blank:  `,"tenant_id":""}`,
		},
		{
			name:   "event_type",
			from:   `"event_type":"tag.created",`,
			absent: ``,
			blank:  `"event_type":"",`,
		},
		{
			name:   "aggregate_type",
			from:   `"aggregate_type":"tag",`,
			absent: ``,
			blank:  `"aggregate_type":"",`,
		},
		{
			name:   "aggregate_id",
			from:   `"aggregate_id":"9c2b0f41-5d33-4a6f-8b17-2e4c9a7d0b55",`,
			absent: ``,
			blank:  `"aggregate_id":"",`,
		},
	} {
		t.Run(field.name, func(t *testing.T) {
			for label, body := range map[string]string{
				"absent": strings.Replace(full, field.from, field.absent, 1),
				"blank":  strings.Replace(full, field.from, field.blank, 1),
			} {
				if body == full {
					t.Fatalf("%s fixture did not change the envelope; the test is asserting nothing", label)
				}
				_, err := ParseEvent([]byte(body))
				if !errors.Is(err, ErrIncompleteEvent) {
					t.Errorf("%s %s: err = %v, want ErrIncompleteEvent", field.name, label, err)
				}
				if err != nil && !strings.Contains(err.Error(), field.name) {
					t.Errorf("%s %s: err = %q does not name the field", field.name, label, err)
				}
			}
		})
	}

	// A known event type with no payload is the same class of defect: the
	// typed payload would be a zero-valued struct with nothing in it.
	missing := strings.Replace(full, `"payload":{"after":`+tagSnapshotJSON+`},`, "", 1)
	if _, err := ParseEvent([]byte(missing)); !errors.Is(err, ErrIncompleteEvent) {
		t.Errorf("missing payload: err = %v, want ErrIncompleteEvent", err)
	}
	null := strings.Replace(full, `"payload":{"after":`+tagSnapshotJSON+`}`, `"payload":null`, 1)
	if _, err := ParseEvent([]byte(null)); !errors.Is(err, ErrIncompleteEvent) {
		t.Errorf("null payload: err = %v, want ErrIncompleteEvent", err)
	}
}

func TestParseEventRejectsABodyThatIsNotAnEnvelope(t *testing.T) {
	for name, body := range map[string]string{
		"empty":            ``,
		"truncated":        `{"id":"x"`,
		"array":            `[{"id":"x"}]`,
		"string":           `"tag.created"`,
		"null":             `null`,
		"wrongly typed id": `{"id":42,"tenant_id":"acme","event_type":"tag.created","aggregate_type":"tag","aggregate_id":"a","payload":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			event, err := ParseEvent([]byte(body))
			if err == nil {
				t.Fatalf("ParseEvent(%s) = %+v, want an error", body, event)
			}
			// "null" decodes to a no-op rather than a syntax error, so it fails
			// the identity check instead; either refusal is correct and both
			// must be a refusal.
			if !errors.Is(err, ErrMalformedEvent) && !errors.Is(err, ErrIncompleteEvent) {
				t.Errorf("err = %v, want ErrMalformedEvent or ErrIncompleteEvent", err)
			}
		})
	}
}

// TestParseEventRejectsAPayloadThatDoesNotFitAKnownEventType covers the other
// direction from the unknown-type case: this SDK claims to understand
// tag.created, so a tag.created it cannot decode is a refusal.
func TestParseEventRejectsAPayloadThatDoesNotFitAKnownEventType(t *testing.T) {
	for name, payload := range map[string]string{
		"after is an array":       `{"after":[1,2,3]}`,
		"snapshot field is wrong": `{"after":{"slug":42}}`,
		"timestamp is not a time": `{"after":{"created_at":"last tuesday"}}`,
		"payload is a string":     `"created"`,
	} {
		t.Run(name, func(t *testing.T) {
			event, err := ParseEvent([]byte(envelope(EventTagCreated, AggregateTag, payload)))
			if !errors.Is(err, ErrMalformedPayload) {
				t.Fatalf("err = %v, want ErrMalformedPayload", err)
			}
			if event != nil {
				t.Errorf("event = %+v, want nil on a refusal", event)
			}
			if !strings.Contains(err.Error(), string(EventTagCreated)) {
				t.Errorf("err = %q does not name the event type", err)
			}
		})
	}

	// The same through the plain decoder, where a caller who ignored the error
	// must not find a half-filled payload behind it.
	var event Event
	err := json.Unmarshal([]byte(envelope(EventTagCreated, AggregateTag, `{"after":{"slug":"ok","name":42}}`)), &event)
	if !errors.Is(err, ErrMalformedPayload) {
		t.Fatalf("err = %v, want ErrMalformedPayload", err)
	}
	if event.Tag != nil {
		t.Errorf("Tag = %+v, want nil: a refused payload must leave nothing half-decoded", event.Tag)
	}
}

// TestSnapshotDistinguishesAbsentFromNullFromValue is the reason the snapshot
// fields are octonomy.Optional and not pointers. A *string collapses the first
// two, and a consumer applying a changed-field set off a nil pointer leaves a
// tag nested under a parent the server no longer has.
func TestSnapshotDistinguishesAbsentFromNullFromValue(t *testing.T) {
	event := mustParse(t, envelope(EventTagUpdated, AggregateTag,
		`{"before":{"parent_id":"9c2b0f41-5d33-4a6f-8b17-2e4c9a7d0b55","description":"Seasonal"},`+
			`"after":{"parent_id":null,"description":"Seasonal promotion"}}`))

	after := event.Tag.After

	// Present and null: the tag was un-nested.
	if !after.ParentID.IsNull() {
		t.Error("parent_id: a cleared link must report IsNull")
	}
	if after.ParentID.IsZero() {
		t.Error("parent_id: a cleared link is not an absent one")
	}
	if _, ok := after.ParentID.Get(); ok {
		t.Error("parent_id: a null carries no value")
	}

	// Present with a value.
	if got := value(t, after.Description, "description"); got != "Seasonal promotion" {
		t.Errorf("description = %q", got)
	}

	// Absent: this update did not touch the slug, which is a different fact
	// from "the slug was cleared" and must not read the same way.
	if !after.Slug.IsZero() {
		t.Error("slug: a field the update did not touch must report IsZero")
	}
	if after.Slug.IsNull() {
		t.Error("slug: an absent field is not a null one")
	}

	// And the same distinction on the before side, so a diff can be applied in
	// either direction.
	if got := value(t, event.Tag.Before.ParentID, "before.parent_id"); got != "9c2b0f41-5d33-4a6f-8b17-2e4c9a7d0b55" {
		t.Errorf("before.parent_id = %q", got)
	}
}

// TestSnapshotRoundTripsToTheKeysItArrivedWith is what the omitzero tags buy: a
// snapshot re-encodes to exactly the keys it was sent with, so one can be
// logged or forwarded without inventing fields the event never carried.
func TestSnapshotRoundTripsToTheKeysItArrivedWith(t *testing.T) {
	const payload = `{"after":{"description":null,"parent_id":"9c2b0f41-5d33-4a6f-8b17-2e4c9a7d0b55"}}`
	event := mustParse(t, envelope(EventTagUpdated, AggregateTag, payload))

	encoded, err := json.Marshal(event.Tag)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got, want map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("decode re-encoded payload: %v", err)
	}
	if err := json.Unmarshal([]byte(payload), &want); err != nil {
		t.Fatalf("decode original payload: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip = %s, want %s", encoded, payload)
	}
}

// TestParseEventIgnoresUnknownSnapshotFields is the other half of forward
// compatibility. A field a later server adds to a snapshot must not turn every
// delivery into a dead letter; the bytes stay readable in Payload.
func TestParseEventIgnoresUnknownSnapshotFields(t *testing.T) {
	const payload = `{"after":{"slug":"summer-sale","colour":"amber","usage_count":7}}`
	event := mustParse(t, envelope(EventTagCreated, AggregateTag, payload))

	if got := value(t, event.Tag.After.Slug, "slug"); got != "summer-sale" {
		t.Errorf("slug = %q", got)
	}
	if !strings.Contains(string(event.Payload), `"colour":"amber"`) {
		t.Errorf("Payload = %s; a field this SDK dropped must still be readable there", event.Payload)
	}
}

// TestUnmarshalJSONAndParseEventAgree keeps the two entry points from drifting.
// Without Event.UnmarshalJSON, json.Unmarshal would fill the envelope, leave
// every typed payload nil, and report no error -- an Event that looks decoded
// and names nothing that went wrong.
func TestUnmarshalJSONAndParseEventAgree(t *testing.T) {
	body := envelope(EventTagCreated, AggregateTag, `{"after":`+tagSnapshotJSON+`}`)

	parsed := mustParse(t, body)

	var direct Event
	if err := json.Unmarshal([]byte(body), &direct); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if direct.Tag == nil {
		t.Fatal("json.Unmarshal must fill the typed payload too")
	}
	if !reflect.DeepEqual(&direct, parsed) {
		t.Error("json.Unmarshal and ParseEvent produced different events")
	}

	// A reused Event must not keep the previous delivery's typed payload.
	if err := json.Unmarshal([]byte(envelope("tag.merged", AggregateTag, `{}`)), &direct); err != nil {
		t.Fatalf("json.Unmarshal of an unknown type: %v", err)
	}
	if direct.Tag != nil {
		t.Error("decoding an unknown event type over a decoded one must clear the typed payload")
	}
}

// TestEventTypeKnownCoversExactlyTheDocumentedTypes pins the set. Adding a type
// to eventAggregates without adding it to the server's contract -- or the other
// way round -- is a decoder that claims to understand something it has not seen.
func TestEventTypeKnownCoversExactlyTheDocumentedTypes(t *testing.T) {
	want := map[EventType]AggregateType{
		"tag.created":            AggregateTag,
		"tag.updated":            AggregateTag,
		"tag.deactivated":        AggregateTag,
		"vocabulary.created":     AggregateVocabulary,
		"vocabulary.updated":     AggregateVocabulary,
		"vocabulary.deactivated": AggregateVocabulary,
		"tag_alias.created":      AggregateTagAlias,
		"tag_alias.updated":      AggregateTagAlias,
		"tag_alias.deactivated":  AggregateTagAlias,
		"assignment.created":     AggregateTagAssignment,
		"assignment.removed":     AggregateTagAssignment,
	}
	if !maps.Equal(want, eventAggregates) {
		t.Errorf("eventAggregates = %v, want %v", eventAggregates, want)
	}

	for eventType := range want {
		if !eventType.Known() {
			t.Errorf("%s must be Known", eventType)
		}
		if eventType.String() != string(eventType) {
			t.Errorf("%s does not round-trip through String", eventType)
		}
	}
	for _, unknown := range []EventType{"", "tag.merged", "Tag.Created", "tag.created "} {
		if unknown.Known() {
			t.Errorf("%q must not be Known", unknown)
		}
	}
}

// TestSnapshotFieldsTagEveryOptionalOmitzero mirrors the root package's guard
// on the *Update structs. An octonomy.Optional without the tag is a field that
// fails to marshal once it is absent -- which is every field of every
// changed-field payload.
func TestSnapshotFieldsTagEveryOptionalOmitzero(t *testing.T) {
	packages, err := parser.ParseDir(token.NewFileSet(), ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}

	checked := 0
	for _, pkg := range packages {
		for _, file := range pkg.Files {
			ast.Inspect(file, func(node ast.Node) bool {
				structType, ok := node.(*ast.StructType)
				if !ok {
					return true
				}
				for _, field := range structType.Fields.List {
					if !isOptionalType(field.Type) {
						continue
					}
					checked++
					name := "<embedded>"
					if len(field.Names) > 0 {
						name = field.Names[0].Name
					}
					if field.Tag == nil || !strings.Contains(field.Tag.Value, ",omitzero") {
						t.Errorf("%s is an octonomy.Optional without a `,omitzero` json tag", name)
					}
				}
				return true
			})
		}
	}
	if checked == 0 {
		t.Fatal("found no octonomy.Optional fields; this guard is guarding nothing")
	}
}

// isOptionalType reports whether an expression spells octonomy.Optional[...].
func isOptionalType(expr ast.Expr) bool {
	index, ok := expr.(*ast.IndexExpr)
	if !ok {
		return false
	}
	selector, ok := index.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := selector.X.(*ast.Ident)
	return ok && pkg.Name == "octonomy" && selector.Sel.Name == "Optional"
}
