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
// aliases. Promoting it to a defined type is what would let it carry methods,
// and that is not blocked by assignment -- Go still accepts a plain
// map[string]any where a defined map type is wanted. What it would change is
// type IDENTITY: type switches, reflection, and every signature naming the
// type. That is disruption without a matching gain, so the alias stays and this
// takes an argument.
//
// A nil or empty Metadata yields the zero value of T and a nil error: absent
// metadata is not a failure. On any error the ZERO value is returned, never a
// half-populated T -- encoding/json fills fields as it goes and stops at the
// first type mismatch, and handing back that partial struct beside an error is
// how a caller ends up trusting three of five fields. Unknown keys are ignored
// and missing keys are left zero, exactly as encoding/json does elsewhere, so a
// struct naming a subset of the stored keys is a legal projection.
//
// # Large integers MAY lose precision, and not here
//
// Where they do, the loss has already happened. Metadata is decoded from the
// server's response into map[string]any by encoding/json, which represents
// every JSON number as float64. A value the server sent that float64 cannot
// represent is therefore rounded before Metadata exists, and re-encoding it
// into an int64 field cannot recover what was discarded: 9007199254740993
// (2^53+1) arrives as 9007199254740992 and no decoder gets the 3 back.
//
// "Above 2^53" is the wrong rule of thumb, though. What float64 loses above it
// is RESOLUTION, in doubling steps: every integer is exact below 2^53; between
// 2^53 and 2^54 only the even ones are, so 2^53+2 survives while 2^53+1 does
// not; between 2^54 and 2^55 only multiples of four, so even 2^54+2 is gone;
// and so on. The honest statement is that integers beyond +/-2^53 MAY lose
// precision depending on the value, which is worse than a clean cutoff because
// it holds in testing and fails on one production id.
//
// Two things this does NOT apply to. A Metadata the CALLER built holding a real
// int64 marshals exactly -- nothing rounded it, so nothing is lost. And a
// caller who must have the raw bytes is not without recourse: Config.HTTPClient
// accepts an *http.Client, so a custom http.RoundTripper can copy the response
// body before this SDK decodes it. That is a deliberate escape hatch rather
// than a supported API, and it is a lot of machinery for one field.
//
// The simple fix is on the writing side: store a value that must survive
// exactly as a STRING in metadata and parse it with strconv.ParseInt on the way
// out. Snowflake ids and ledger amounts in minor units belong in a string
// field. Values within 2^53 round-trip exactly and need none of this.
func DecodeMetadata[T any](m Metadata) (T, error) {
	var out T
	// Short-circuit rather than decode "{}" or "null". T is not constrained to
	// a struct, and for a pointer or map T the decoders disagree about what
	// nothing means: "null" leaves a *T nil while "{}" allocates one, and "{}"
	// gives a map T an empty non-nil map. Returning the zero value directly is
	// the only way the sentence above is true for every T.
	if len(m) == 0 {
		return out, nil
	}
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
