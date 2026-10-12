package octonomy

import (
	"encoding/json"
	"fmt"
	"reflect"
)

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

// DecodeMetadata decodes a resource's free-form Metadata into v, which must be
// a non-nil pointer -- usually to a struct of the caller's own shape:
//
//	type shipping struct {
//		Carrier  string `json:"carrier"`
//		Priority int    `json:"priority"`
//	}
//	var cfg shipping
//	err := octonomy.DecodeMetadata(tag.Metadata, &cfg)
//
// It exists because the alternative is a type assertion at every read --
// meta["priority"].(int) -- which compiles, is wrong (JSON numbers arrive as
// float64), and PANICS when the stored shape changes. This package never
// panics, and that promise is worth little if the shape it hands callers makes
// them write the panic themselves. A mismatch here is an error return.
//
// It takes a pointer to decode into, as json.Unmarshal does, where the /v2
// module's DecodeMetadata takes a type argument: Go 1.13 has no type
// parameters. It is a FUNCTION rather than a method on Metadata because Metadata
// is a type ALIAS for map[string]interface{}, and Go allows no methods on an
// alias; making it a defined type would change its identity under every type
// switch a caller of this line's first release wrote.
//
// What v points at is REPLACED, never merged into, and always with a whole
// value -- unlike json.Unmarshal, which decodes over whatever is already there.
// A nil or empty Metadata sets it to its type's zero value with a nil error:
// absent metadata is not a failure. On any error it is set to the zero value
// too, never left half-populated. encoding/json does not stop at a bad field:
// it skips that one, KEEPS DECODING the rest, and returns the earliest type
// error at the end -- so decoding straight into v would leave a struct with
// most fields set and one silently zero, and handing that back beside an error
// is how a caller ends up trusting three of five fields. Unknown keys are
// ignored and missing keys are left zero, exactly as encoding/json does
// elsewhere, so a struct naming a subset of the stored keys is a legal
// projection. A v that is not a non-nil pointer is an error wrapping
// *json.InvalidUnmarshalError, and nothing is written.
//
// # Large integers MAY lose precision, and not here
//
// Where they do, the loss has already happened. Metadata is decoded from the
// server's response into map[string]interface{} by encoding/json, which
// represents every JSON number as float64. A value the server sent that float64
// cannot represent is therefore rounded before Metadata exists, and re-encoding
// it into an int64 field cannot recover what was discarded: 9007199254740993
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
func DecodeMetadata(m Metadata, v interface{}) error {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		return fmt.Errorf("octonomy: decode metadata: %w", &json.InvalidUnmarshalError{Type: reflect.TypeOf(v)})
	}
	target := rv.Elem()
	// Short-circuit rather than decode "{}" or "null". v need not point at a
	// struct, and for a pointer or map the decoders disagree about what nothing
	// means: "null" leaves a pointer nil while "{}" allocates one, and "{}"
	// gives a map an empty non-nil map. Writing the zero value directly is the
	// only way the sentence above is true for every type.
	if len(m) == 0 {
		target.Set(reflect.Zero(target.Type()))
		return nil
	}
	// Before json.Marshal, which on Go 1.13 has no cycle detection: a Metadata
	// the caller built that contains itself would hang the process. See
	// checkBodyDepth.
	if err := checkBodyDepth(m); err != nil {
		target.Set(reflect.Zero(target.Type()))
		return fmt.Errorf("octonomy: encode metadata: %w", err)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		target.Set(reflect.Zero(target.Type()))
		return fmt.Errorf("octonomy: encode metadata: %w", err)
	}
	// Into a fresh value, so a type error part-way through never reaches v.
	fresh := reflect.New(target.Type())
	if err := decodeJSON(raw, fresh.Interface()); err != nil {
		target.Set(reflect.Zero(target.Type()))
		return fmt.Errorf("octonomy: decode metadata: %w", err)
	}
	target.Set(fresh.Elem())
	return nil
}

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
