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
