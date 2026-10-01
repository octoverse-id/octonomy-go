package octonomy

// Metadata is an arbitrary JSON object attached to Octonomy resources. It maps to
// the `metadata` field on vocabularies, tags, aliases, and audit logs.
type Metadata = map[string]interface{}

// String returns a pointer to v. It is a convenience for setting optional or
// nullable request fields such as TagCreate.Description.
func String(v string) *string { return &v }

// Bool returns a pointer to v, for optional boolean request fields such as
// TagCreate.IsActive.
func Bool(v bool) *bool { return &v }

// Int returns a pointer to v, for optional integer fields.
func Int(v int) *int { return &v }

// sentMetadata is how a *Update's MarshalJSON keeps Metadata's third state. A
// pointer tagged omitempty is omitted only when it is nil, so pointing at a
// non-nil map -- empty or not -- sends it, while a nil map stays a nil pointer
// and omits the key, exactly as before.
func sentMetadata(m Metadata) *Metadata {
	if m == nil {
		return nil
	}
	return &m
}
