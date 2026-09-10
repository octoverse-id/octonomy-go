package octonomy

import (
	"encoding/json"
	"fmt"
)

// Metadata is an arbitrary JSON object attached to Octonomy resources. It maps to
// the `metadata` field on vocabularies, tags, aliases, and audit logs.
type Metadata = map[string]any

// String returns a pointer to v. It is a convenience for setting optional or
// nullable request fields such as TagCreate.Description.
func String(v string) *string { return &v }

// Bool returns a pointer to v, for optional boolean request fields such as
// TagCreate.IsActive.
func Bool(v bool) *bool { return &v }

// Int returns a pointer to v, for optional integer fields.
func Int(v int) *int { return &v }

// DecodeMetadata decodes a resource's free-form Metadata into a struct of the
// caller's own shape.
//
//	type shipping struct {
//		Carrier  string `json:"carrier"`
//		Priority int    `json:"priority"`
//	}
//	cfg, err := octonomy.DecodeMetadata[shipping](tag.Metadata)
//
// It exists because the alternative is a type assertion at every read --
// meta["priority"].(int) -- which compiles, is wrong (JSON numbers arrive as
// float64), and PANICS when the stored shape changes. This package never
// panics, and that promise is worth little if the shape it hands callers makes
// them write the panic themselves. A mismatch here is an error return.
//
// It is a FUNCTION rather than a method on Metadata because Metadata is a type
// ALIAS for map[string]any (see above) and Go does not allow methods on
// aliases. Making it a defined type instead would break every caller already
// passing a plain map, so the alias stays and this takes an argument.
//
// A nil or empty Metadata yields the zero value of T and a nil error: absent
// metadata is not a failure. On any error the ZERO value is returned, never a
// half-populated T -- encoding/json fills fields as it goes and stops at the
// first type mismatch, and handing back that partial struct beside an error is
// how a caller ends up trusting three of five fields. Unknown keys are ignored
// and missing keys are left zero, exactly as encoding/json does elsewhere, so a
// struct naming a subset of the stored keys is a legal projection.
//
// # Large integers are already imprecise before this is called
//
// The loss is real but it does not happen here. Metadata is decoded from the
// server's response into map[string]any by encoding/json, which represents
// every JSON number as float64. Integers above 2^53 (9007199254740992) are
// therefore already rounded by the time Metadata exists, and re-encoding them
// into an int64 field cannot recover what was discarded: 9007199254740993
// arrives as 9007199254740992 and no decoder gets the 3 back.
//
// The usual advice -- decode the raw JSON yourself -- is NOT available here,
// because this SDK exposes no raw-response hook. So the workaround is on the
// writing side: store an identifier or amount that must survive exactly as a
// STRING in metadata, and parse it with strconv.ParseInt on the way out.
// Snowflake ids, ledger amounts in minor units, and anything else that outgrows
// float64 belong in a string field. Values within 2^53 round-trip exactly and
// need none of this.
func DecodeMetadata[T any](m Metadata) (T, error) {
	var out T
	raw, err := json.Marshal(m)
	if err != nil {
		var zero T
		return zero, fmt.Errorf("octonomy: encode metadata: %w", err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		var zero T
		return zero, fmt.Errorf("octonomy: decode metadata: %w", err)
	}
	return out, nil
}
