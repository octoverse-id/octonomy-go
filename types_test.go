package octonomy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"reflect"
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

// The zero-value promise has to hold for every T, not just a value struct.
// Decoding "{}" would allocate a non-nil pointer and a non-nil empty map, so
// absent metadata is short-circuited instead of round-tripped.
func TestDecodeMetadata_AbsentMetadataIsTheZeroValueForEveryT(t *testing.T) {
	for _, meta := range []Metadata{nil, {}} {
		name := "nil"
		if meta != nil {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			ptr, err := DecodeMetadata[*shipping](meta)
			if err != nil {
				t.Fatalf("pointer T: %v", err)
			}
			if ptr != nil {
				t.Errorf("pointer T = %+v, want nil", ptr)
			}

			m, err := DecodeMetadata[map[string]any](meta)
			if err != nil {
				t.Fatalf("map T: %v", err)
			}
			if m != nil {
				t.Errorf("map T = %#v, want a nil map", m)
			}

			sl, err := DecodeMetadata[[]string](meta)
			if err != nil {
				t.Fatalf("slice T: %v", err)
			}
			if sl != nil {
				t.Errorf("slice T = %#v, want a nil slice", sl)
			}

			st, err := DecodeMetadata[shipping](meta)
			if err != nil {
				t.Fatalf("struct T: %v", err)
			}
			if !isZeroShipping(st) {
				t.Errorf("struct T = %+v, want the zero value", st)
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
	built, err := DecodeMetadata[ledger](Metadata{"amount": unsafe})
	if err != nil {
		t.Fatalf("DecodeMetadata on a caller-built map: %v", err)
	}
	if built.Amount != unsafe {
		t.Errorf("caller-built int64 = %d, want %d exactly", built.Amount, unsafe)
	}
}

// Metadata is a type ALIAS, which is why DecodeMetadata is a function rather
// than a method: Go does not allow methods on an alias.
//
// Assignability proves nothing here, and that is the trap this test exists to
// avoid. Go already permits assignment between a defined map type and an
// unnamed map[string]any, so `var m map[string]any = Metadata{}` compiles
// whether Metadata is an alias or `type Metadata map[string]any`. The
// discriminator is type IDENTITY: an alias resolves to the unnamed map type and
// so has no name of its own, while a defined type is named.
func TestMetadataIsStillAnAlias(t *testing.T) {
	got := reflect.TypeOf(Metadata{})
	if name := got.Name(); name != "" {
		t.Errorf("Metadata resolves to the named type %q; it is no longer an alias, "+
			"so DecodeMetadata could be a method -- and every type switch and "+
			"signature naming it has changed identity", name)
	}
	if want := reflect.TypeOf(map[string]any{}); got != want {
		t.Errorf("Metadata is %v, want the identical type %v", got, want)
	}
}

// --- Metadata on PATCH bodies ---------------------------------------------

// nilMetadata returns a pointer to a nil Metadata, the one spelling that is
// neither "clear it" nor "leave it alone". It exists as a helper because
// &Metadata(nil) is not addressable.
func nilMetadata() *Metadata {
	var m Metadata
	return &m
}

// The three PATCH bodies that carry Metadata must agree on what each spelling
// of the field puts on the wire.
//
// The pointer exists for exactly this. While Metadata was a plain map,
// encoding/json counted a zero-length one as empty under omitempty, so
// Metadata{} sent NO metadata key: "clear the stored object" was
// indistinguishable from "leave it alone", and the caller got a 200 with the old
// object still in place and no error (#37). That is the silent-success shape
// this SDK refuses everywhere else.
//
// All three resources are walked rather than one sampled, because what the fix
// promises is that none of them is the outlier -- and the assertion is on the
// RAW bytes of the key, since the absent / {} / null distinction is precisely
// what a decoded map[string]any would flatten.
func TestUpdateMetadata_OmitClearAndReplaceOnEveryPatchBody(t *testing.T) {
	resources := []struct {
		name string
		path string
		resp any
		call func(*Client, *Metadata) error
	}{
		{"tags", "/api/v2/tags/tag_1", Tag{ID: "tag_1"}, func(c *Client, m *Metadata) error {
			_, err := c.Tags.Update(context.Background(), "tag_1", TagUpdate{Metadata: m})
			return err
		}},
		{"vocabularies", "/api/v2/vocabularies/voc_1", Vocabulary{ID: "voc_1"}, func(c *Client, m *Metadata) error {
			_, err := c.Vocabularies.Update(context.Background(), "voc_1", VocabularyUpdate{Metadata: m})
			return err
		}},
		{"tag-aliases", "/api/v2/tag-aliases/alias_1", TagAlias{ID: "alias_1"}, func(c *Client, m *Metadata) error {
			_, err := c.Aliases.Update(context.Background(), "alias_1", TagAliasUpdate{Metadata: m})
			return err
		}},
	}

	bodies := []struct {
		name string
		meta *Metadata
		// want is the raw JSON expected under "metadata"; empty means the key
		// must not be on the wire at all.
		want string
	}{
		{"nil omits the key", nil, ""},
		{"empty map clears the stored object", &Metadata{}, `{}`},
		{"populated map replaces it", &Metadata{"team": "growth"}, `{"team":"growth"}`},
		// Documented on TagUpdate.Metadata: a pointer to a NIL map is neither
		// intent and marshals as null. Pinned so the doc comment stays true.
		{"pointer to a nil map is null", nilMetadata(), `null`},
	}

	for _, res := range resources {
		for _, body := range bodies {
			t.Run(res.name+"/"+body.name, func(t *testing.T) {
				c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodPatch || r.URL.Path != res.path {
						t.Errorf("got %s %s, want PATCH %s", r.Method, r.URL.Path, res.path)
					}
					raw, err := io.ReadAll(r.Body)
					if err != nil {
						// Errorf, not Fatalf: this runs on the server's goroutine.
						t.Errorf("read body: %v", err)
						return
					}
					var in map[string]json.RawMessage
					if err := json.Unmarshal(raw, &in); err != nil {
						t.Errorf("decode body %s: %v", raw, err)
						return
					}
					got, present := in["metadata"]
					switch {
					case body.want == "" && present:
						t.Errorf("metadata = %s, want the key absent (body: %s)", got, raw)
					case body.want != "" && !present:
						t.Errorf("metadata key absent, want %s (body: %s)", body.want, raw)
					case body.want != "" && string(got) != body.want:
						t.Errorf("metadata = %s, want %s", got, body.want)
					}
					writeData(t, w, http.StatusOK, res.resp)
				})

				if err := res.call(c, body.meta); err != nil {
					t.Fatalf("Update: %v", err)
				}
			})
		}
	}
}
