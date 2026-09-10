package octonomy

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
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

func TestDecodeMetadata(t *testing.T) {
	tests := []struct {
		name string
		meta Metadata
		want shipping
	}{
		{
			"every field present",
			Metadata{"carrier": "dhl", "priority": 2, "express": true, "regions": []any{"eu", "us"}},
			shipping{Carrier: "dhl", Priority: 2, Express: true, Regions: []string{"eu", "us"}},
		},
		{
			// A struct naming a subset is a legal projection: unknown keys are
			// ignored, and the keys it names but the map lacks stay zero.
			"partial and extra keys",
			Metadata{"carrier": "ups", "unrelated": map[string]any{"deep": 1}},
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
		t.Run(tt.name, func(t *testing.T) {
			got, err := DecodeMetadata[shipping](tt.meta)
			if err != nil {
				t.Fatalf("DecodeMetadata: %v", err)
			}
			if got.Carrier != tt.want.Carrier || got.Priority != tt.want.Priority || got.Express != tt.want.Express {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
			if len(got.Regions) != len(tt.want.Regions) {
				t.Fatalf("Regions = %v, want %v", got.Regions, tt.want.Regions)
			}
			for i := range got.Regions {
				if got.Regions[i] != tt.want.Regions[i] {
					t.Errorf("Regions = %v, want %v", got.Regions, tt.want.Regions)
				}
			}
		})
	}
}

// The whole point of the helper: a shape mismatch that a type assertion would
// have turned into a panic comes back as an error instead.
func TestDecodeMetadata_MismatchIsAnErrorNotAPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("DecodeMetadata panicked: %v", r)
		}
	}()

	meta := Metadata{"carrier": "dhl", "priority": "not-a-number"}
	got, err := DecodeMetadata[shipping](meta)
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
	// a non-nil error is exactly the trap this avoids.
	if !isZeroShipping(got) {
		t.Errorf("got %+v alongside the error, want the zero value", got)
	}
}

// A map holding something encoding/json cannot marshal fails on the encode leg.
// It is the caller's own map, so this is reachable, and it must not panic either.
func TestDecodeMetadata_UnmarshalableValue(t *testing.T) {
	got, err := DecodeMetadata[shipping](Metadata{"carrier": make(chan int)})
	if err == nil {
		t.Fatal("expected an error for a value json cannot encode")
	}
	if !isZeroShipping(got) {
		t.Errorf("got %+v alongside the error, want the zero value", got)
	}
}

// The precision caveat, pinned. The loss is NOT introduced by DecodeMetadata --
// it happens when the response is decoded into map[string]any, where every JSON
// number becomes a float64. This walks that exact path so the doc comment is
// backed by a test rather than by assertion.
func TestDecodeMetadata_LargeIntegerPrecision(t *testing.T) {
	type ledger struct {
		Amount   int64  `json:"amount"`
		AmountAs string `json:"amount_as_string"`
	}

	const safe = int64(1) << 53         // 9007199254740992, exactly representable
	const unsafe = (int64(1) << 53) + 1 // 9007199254740993, is not

	for _, n := range []int64{safe, unsafe} {
		t.Run(strconv.FormatInt(n, 10), func(t *testing.T) {
			// Exactly how a server response reaches Metadata: raw JSON decoded
			// into map[string]any.
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

			got, err := DecodeMetadata[ledger](meta)
			if err != nil {
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

	// The boundary itself, stated as the doc comment states it.
	if float64(safe) != math.Trunc(float64(safe)) || int64(float64(unsafe)) != safe {
		t.Errorf("2^53 boundary does not behave as documented")
	}
}

// Metadata is a type ALIAS, which is why DecodeMetadata is a function rather
// than a method. These two calls assert the alias in both directions: a
// Metadata is accepted where a plain map is wanted and the reverse. Promoting
// Metadata to a defined type -- which is what would let it carry methods --
// breaks this at compile time, which is the point of keeping it here.
func TestMetadataIsStillAnAlias(t *testing.T) {
	takesMap := func(m map[string]any) int { return len(m) }
	takesMetadata := func(m Metadata) int { return len(m) }

	if takesMap(Metadata{"k": "v"}) != 1 {
		t.Error("a Metadata is not usable as a map[string]any")
	}
	if takesMetadata(map[string]any{"k": "v"}) != 1 {
		t.Error("a map[string]any is not usable as a Metadata")
	}
	if _, err := DecodeMetadata[struct{}](map[string]any{"k": "v"}); err != nil {
		t.Fatalf("DecodeMetadata on a plain map: %v", err)
	}
}
