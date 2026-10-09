package octonomy

// Ported from main's types_test.go at 5e40964, and every mention of main here
// means main at that commit. Of its seven tests, TestMetadataIsStillAnAlias came
// with #95, the five DecodeMetadata tests with the function's port in #100, and
// TestUpdateMetadata_OmitClearAndReplaceOnEveryPatchBody is covered by
// update_test.go; docs/compat-test-disposition.md has each verdict.
//
// The DecodeMetadata tests are main's, rewritten for this line's signature:
// where main's returns a value, this line's writes through a pointer. Each
// target therefore starts NON-zero, so "the zero value" is something the call
// wrote rather than what the variable already held -- a test of a returned
// value needed no such care, and a straight port of one would pass against a
// DecodeMetadata that wrote nothing. The tests below main's are this line's
// own: the pointer contract, the replace-never-merge rule, and the cycle guard.

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

type shipping struct {
	Carrier  string   `json:"carrier"`
	Priority int      `json:"priority"`
	Express  bool     `json:"express"`
	Regions  []string `json:"regions"`
}

// shipping holds a slice, so it is not comparable with ==.
func isZeroShipping(s shipping) bool {
	return s.Carrier == "" && s.Priority == 0 && !s.Express && s.Regions == nil
}

// stale is what every DecodeMetadata target starts as: every field set, so a
// field the call failed to write is visible.
func stale() shipping {
	return shipping{Carrier: "stale", Priority: 99, Express: true, Regions: []string{"stale"}}
}

// Metadata must stay a type ALIAS for map[string]interface{}, and on this line
// the reason is v1.0.0 itself. That release declared it as an alias, so to a
// caller the two are ONE type: a Metadata held in an interface{} matches
// `case map[string]interface{}` in a type switch, and a []Metadata or a
// func(Metadata) is a []map[string]interface{} or a func(map[string]interface{}).
// A defined type would break each of those -- while direct assignment between
// the two kept compiling, which is the trap described below -- and this line can
// never publish a major to carry a break (AGENTS.md). main pins the same fact for its
// own reason: DecodeMetadata is a function rather than a method because
// Metadata is an alias, and Go allows no methods on one.
//
// Assignability proves nothing here, and that is the trap this test exists to
// avoid. Go already permits assignment between a defined map type and an
// unnamed map[string]interface{}, so `var m map[string]interface{} = Metadata{}`
// compiles whether Metadata is an alias or `type Metadata map[string]interface{}`.
// The discriminator is type IDENTITY: an alias resolves to the unnamed map type
// and so has no name of its own, while a defined type is named.
func TestMetadataIsStillAnAlias(t *testing.T) {
	got := reflect.TypeOf(Metadata{})
	if name := got.Name(); name != "" {
		t.Errorf("Metadata resolves to the named type %q; it is no longer an alias, so every "+
			"type switch and signature naming it has changed identity -- a break for v1.0.0 callers", name)
	}
	if want := reflect.TypeOf(map[string]interface{}{}); got != want {
		t.Errorf("Metadata is %v, want the identical type %v", got, want)
	}
}

func TestDecodeMetadata(t *testing.T) {
	tests := []struct {
		name string
		meta Metadata
		want shipping
	}{
		{
			"every field present",
			Metadata{"carrier": "dhl", "priority": 2, "express": true, "regions": []interface{}{"eu", "us"}},
			shipping{Carrier: "dhl", Priority: 2, Express: true, Regions: []string{"eu", "us"}},
		},
		{
			// A struct naming a subset is a legal projection: unknown keys are
			// ignored, and the keys it names but the map lacks stay zero.
			"partial and extra keys",
			Metadata{"carrier": "ups", "unrelated": map[string]interface{}{"deep": 1}},
			shipping{Carrier: "ups"},
		},
		{"nil metadata is not a failure", nil, shipping{}},
		{"empty metadata is not a failure", Metadata{}, shipping{}},
		{
			// The map arrives from encoding/json, so numbers are float64 rather
			// than int. Decoding must accept that, since it is the only shape a
			// server-sourced Metadata ever has.
			"json-shaped numbers",
			Metadata{"carrier": "fedex", "priority": float64(3)},
			shipping{Carrier: "fedex", Priority: 3},
		},
	}
	for _, tt := range tests {
		tt := tt // Go 1.13: the loop variable is shared across iterations
		t.Run(tt.name, func(t *testing.T) {
			got := stale()
			if err := DecodeMetadata(tt.meta, &got); err != nil {
				t.Fatalf("DecodeMetadata: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestDecodeMetadata_AbsentMetadataIsTheZeroValueForEveryT(t *testing.T) {
	for _, meta := range []Metadata{nil, {}} {
		name := "nil"
		if meta != nil {
			name = "empty"
		}
		meta := meta
		t.Run(name, func(t *testing.T) {
			ptr := &shipping{Carrier: "stale"}
			if err := DecodeMetadata(meta, &ptr); err != nil {
				t.Fatalf("pointer target: %v", err)
			}
			if ptr != nil {
				t.Errorf("pointer target = %+v, want nil", ptr)
			}

			m := map[string]interface{}{"stale": true}
			if err := DecodeMetadata(meta, &m); err != nil {
				t.Fatalf("map target: %v", err)
			}
			if m != nil {
				t.Errorf("map target = %#v, want a nil map", m)
			}

			sl := []string{"stale"}
			if err := DecodeMetadata(meta, &sl); err != nil {
				t.Fatalf("slice target: %v", err)
			}
			if sl != nil {
				t.Errorf("slice target = %#v, want a nil slice", sl)
			}

			st := stale()
			if err := DecodeMetadata(meta, &st); err != nil {
				t.Fatalf("struct target: %v", err)
			}
			if !isZeroShipping(st) {
				t.Errorf("struct target = %+v, want the zero value", st)
			}
		})
	}
}

func TestDecodeMetadata_MismatchIsAnErrorNotAPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("DecodeMetadata panicked: %v", r)
		}
	}()

	meta := Metadata{"carrier": "dhl", "priority": "not-a-number"}
	got := stale()
	err := DecodeMetadata(meta, &got)
	if err == nil {
		t.Fatal("expected an error for a type mismatch")
	}
	var typeErr *json.UnmarshalTypeError
	if !errors.As(err, &typeErr) {
		t.Errorf("error does not unwrap to *json.UnmarshalTypeError: %v", err)
	}

	// The zero value, NOT a half-populated struct. encoding/json fills fields as
	// it goes and reports the type error at the end, so Carrier would otherwise
	// come back set -- and a caller reading three good fields out of five beside
	// a non-nil error is exactly the trap this avoids. Nor what the target held
	// before: the call wrote it.
	if !isZeroShipping(got) {
		t.Errorf("got %+v alongside the error, want the zero value", got)
	}
}

func TestDecodeMetadata_UnmarshalableValue(t *testing.T) {
	got := stale()
	err := DecodeMetadata(Metadata{"carrier": make(chan int)}, &got)
	if err == nil {
		t.Fatal("expected an error for a value json cannot encode")
	}
	if !isZeroShipping(got) {
		t.Errorf("got %+v alongside the error, want the zero value", got)
	}
}

func TestDecodeMetadata_LargeIntegerPrecision(t *testing.T) {
	type ledger struct {
		Amount   int64  `json:"amount"`
		AmountAs string `json:"amount_as_string"`
	}

	const safe = int64(1) << 53         // 9007199254740992, exactly representable
	const unsafe = (int64(1) << 53) + 1 // 9007199254740993, is not

	for _, n := range []int64{safe, unsafe} {
		n := n
		t.Run(strconv.FormatInt(n, 10), func(t *testing.T) {
			// Exactly how a server response reaches Metadata: raw JSON decoded
			// into map[string]interface{}.
			raw := []byte(`{"amount":` + strconv.FormatInt(n, 10) +
				`,"amount_as_string":"` + strconv.FormatInt(n, 10) + `"}`)
			var meta Metadata
			if err := json.Unmarshal(raw, &meta); err != nil {
				t.Fatalf("decode the response: %v", err)
			}
			// Precision is already gone at THIS point, before DecodeMetadata runs.
			if _, ok := meta["amount"].(float64); !ok {
				t.Fatalf("amount is %T, want float64 -- the premise of the caveat", meta["amount"])
			}

			var got ledger
			if err := DecodeMetadata(meta, &got); err != nil {
				t.Fatalf("DecodeMetadata: %v", err)
			}
			if n == safe && got.Amount != n {
				t.Errorf("Amount = %d, want %d: values within 2^53 must round-trip exactly", got.Amount, n)
			}
			if n == unsafe {
				if got.Amount == n {
					t.Errorf("Amount = %d survived, but float64 cannot represent it -- the caveat would be wrong", got.Amount)
				}
				if got.Amount != safe {
					t.Errorf("Amount = %d, want it rounded to %d", got.Amount, safe)
				}
			}
			// The documented workaround: the same number stored as a string
			// survives both legs untouched.
			if got.AmountAs != strconv.FormatInt(n, 10) {
				t.Errorf("string form = %q, want %q -- the recommended workaround must actually work",
					got.AmountAs, strconv.FormatInt(n, 10))
			}
			parsed, err := strconv.ParseInt(got.AmountAs, 10, 64)
			if err != nil || parsed != n {
				t.Errorf("ParseInt(%q) = %d, %v; want %d", got.AmountAs, parsed, err, n)
			}
		})
	}

	// "Above 2^53 is lost" would be the tidy rule and it is wrong; so is the
	// tempting repair, "but every even integer survives". float64 loses
	// resolution in DOUBLING steps, so the even ones survive only up to 2^54,
	// after which it takes multiples of four. Both rungs are pinned, because
	// each one is a rule someone would otherwise write down as the whole story.
	const evenBelow54 = (int64(1) << 53) + 2 // in [2^53, 2^54): even is enough
	if int64(float64(evenBelow54)) != evenBelow54 {
		t.Errorf("%d should survive float64: it is even and below 2^54", evenBelow54)
	}
	const evenAbove54 = (int64(1) << 54) + 2 // in [2^54, 2^55): even is NOT enough
	if int64(float64(evenAbove54)) == evenAbove54 {
		t.Errorf("%d survived float64, but past 2^54 only multiples of four do", evenAbove54)
	}
	if got := int64(float64(evenAbove54)); got != int64(1)<<54 {
		t.Errorf("%d rounded to %d, want %d", evenAbove54, got, int64(1)<<54)
	}
	if float64(safe) != math.Trunc(float64(safe)) || int64(float64(unsafe)) != safe {
		t.Errorf("2^53 boundary does not behave as documented")
	}

	// A Metadata the CALLER built is not on the lossy path at all: nothing
	// rounded it, so an int64 marshals and decodes exactly.
	var built ledger
	if err := DecodeMetadata(Metadata{"amount": unsafe}, &built); err != nil {
		t.Fatalf("DecodeMetadata on a caller-built map: %v", err)
	}
	if built.Amount != unsafe {
		t.Errorf("caller-built int64 = %d, want %d exactly", built.Amount, unsafe)
	}
}

// The pointer is this line's own contract, so its failure is too: anything but
// a non-nil pointer is an error -- the same *json.InvalidUnmarshalError
// json.Unmarshal returns, so a caller can tell it apart -- and never a panic,
// with or without metadata to decode.
func TestDecodeMetadata_RefusesAnythingButANonNilPointer(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("DecodeMetadata panicked: %v", r)
		}
	}()
	var nilShipping *shipping
	for _, meta := range []Metadata{nil, {"carrier": "dhl"}} {
		for _, tc := range []struct {
			name string
			v    interface{}
		}{
			{"nil", nil},
			{"a typed nil pointer", nilShipping},
			{"a struct value", shipping{}},
			{"a map value", map[string]interface{}{}},
		} {
			err := DecodeMetadata(meta, tc.v)
			var invalid *json.InvalidUnmarshalError
			if !errors.As(err, &invalid) {
				t.Errorf("metadata %v into %s: err = %v, want a *json.InvalidUnmarshalError", meta, tc.name, err)
			}
			if err != nil && !strings.HasPrefix(err.Error(), "octonomy: ") {
				t.Errorf("metadata %v into %s: %q lacks the package prefix", meta, tc.name, err)
			}
		}
	}
}

// What the target held is replaced, never merged into: main's version returns a
// fresh value, and a caller reusing one variable across tags must not see the
// previous tag's fields where this tag's metadata has none -- the merge
// json.Unmarshal would do.
func TestDecodeMetadata_ReplacesTheTargetRatherThanMergingIntoIt(t *testing.T) {
	got := stale()
	if err := DecodeMetadata(Metadata{"carrier": "dhl"}, &got); err != nil {
		t.Fatalf("DecodeMetadata: %v", err)
	}
	if want := (shipping{Carrier: "dhl"}); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v: a field the metadata lacks kept the target's old value", got, want)
	}

	m := map[string]interface{}{"stale": true}
	if err := DecodeMetadata(Metadata{"carrier": "dhl"}, &m); err != nil {
		t.Fatalf("DecodeMetadata into a map: %v", err)
	}
	if want := (map[string]interface{}{"carrier": "dhl"}); !reflect.DeepEqual(m, want) {
		t.Errorf("map target = %#v, want %#v: the old key survived", m, want)
	}
}
