package octonomy

import (
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// patchBody is the shape every *Update struct has: an Optional field carrying
// the omitzero tag. It stands in for them in the wire tests so those assert the
// TYPE's behavior rather than one resource's field list.
type patchBody struct {
	Field Optional[string] `json:"field,omitzero"`
}

// The three states must be three distinct things on the wire. Absent, null and
// a value are what a PATCH body means by "leave it alone", "clear it" and "set
// it", and a pointer could only ever say two of them (#64).
func TestOptional_ThreeStatesAreThreeDistinctBodies(t *testing.T) {
	cases := []struct {
		name string
		in   Optional[string]
		want string
	}{
		{"the zero value omits the key", Optional[string]{}, `{}`},
		{"Null sends an explicit null", Null[string](), `{"field":null}`},
		{"Set sends the value", Set("x"), `{"field":"x"}`},
		{"Set of the empty string still sends it", Set(""), `{"field":""}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(patchBody{Field: tc.in})
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("body = %s, want %s", got, tc.want)
			}
		})
	}
}

// Set("") is the case omitempty could not express either, and the reason the
// zero value of the WRAPPER rather than of T is what omits: an empty string, a
// false, and an empty map are all values a caller may legitimately send.
func TestOptional_ZeroValueOfTIsStillSent(t *testing.T) {
	type body struct {
		S Optional[string]   `json:"s,omitzero"`
		B Optional[bool]     `json:"b,omitzero"`
		M Optional[Metadata] `json:"m,omitzero"`
	}
	got, err := json.Marshal(body{S: Set(""), B: Set(false), M: Set(Metadata{})})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if want := `{"s":"","b":false,"m":{}}`; string(got) != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}

// An omitted Optional asked to encode itself is an ERROR, never a null. The
// alternative is that a field which lost its omitzero tag puts "field": null on
// every PATCH that does not touch it -- clearing columns the caller never named,
// with a 200 and no error. That is the silent-success shape this SDK refuses.
func TestOptionalMarshalJSON_OmittedIsAnErrorNotANull(t *testing.T) {
	t.Run("marshalled on its own", func(t *testing.T) {
		if _, err := json.Marshal(Optional[string]{}); !errors.Is(err, errOmittedOptional) {
			t.Errorf("Marshal(zero Optional) error = %v, want errOmittedOptional", err)
		}
	})

	// The case the error exists for: a field tagged omitempty, or not tagged at
	// all, which encoding/json will not omit for a struct.
	t.Run("a field missing the omitzero tag", func(t *testing.T) {
		type untagged struct {
			Field Optional[string] `json:"field"`
		}
		type omitempty struct {
			Field Optional[string] `json:"field,omitempty"`
		}
		if _, err := json.Marshal(untagged{}); !errors.Is(err, errOmittedOptional) {
			t.Errorf("Marshal(untagged) error = %v, want errOmittedOptional", err)
		}
		if _, err := json.Marshal(omitempty{}); !errors.Is(err, errOmittedOptional) {
			t.Errorf("Marshal(omitempty) error = %v, want errOmittedOptional", err)
		}
	})

	// ...and the message has to name the fix, since the reader is looking at a
	// struct tag, not at this file.
	if msg := errOmittedOptional.Error(); !strings.Contains(msg, "omitzero") {
		t.Errorf("errOmittedOptional = %q, want it to name the omitzero tag", msg)
	}
}

func TestOptional_Accessors(t *testing.T) {
	cases := []struct {
		name     string
		in       Optional[string]
		isZero   bool
		isNull   bool
		value    string
		hasValue bool
	}{
		{"omitted", Optional[string]{}, true, false, "", false},
		{"null", Null[string](), false, true, "", false},
		{"value", Set("x"), false, false, "x", true},
		{"empty value", Set(""), false, false, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.IsZero(); got != tc.isZero {
				t.Errorf("IsZero() = %v, want %v", got, tc.isZero)
			}
			if got := tc.in.IsNull(); got != tc.isNull {
				t.Errorf("IsNull() = %v, want %v", got, tc.isNull)
			}
			value, ok := tc.in.Get()
			if ok != tc.hasValue {
				t.Errorf("Get() ok = %v, want %v", ok, tc.hasValue)
			}
			if value != tc.value {
				t.Errorf("Get() value = %q, want %q", value, tc.value)
			}
		})
	}
}

// Decoding has to recover all three states too, or an Optional that survives a
// round trip through JSON comes back as something the caller did not write.
// Without UnmarshalJSON the unexported fields decode to nothing and report no
// error, which is the silent-zero family of #32 and #40.
func TestOptional_UnmarshalRecoversAllThreeStates(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		isZero bool
		isNull bool
		value  string
	}{
		{"an absent key stays omitted", `{}`, true, false, ""},
		{"an explicit null decodes to null", `{"field":null}`, false, true, ""},
		{"a value decodes to a value", `{"field":"x"}`, false, false, "x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got patchBody
			if err := json.Unmarshal([]byte(tc.in), &got); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if got.Field.IsZero() != tc.isZero || got.Field.IsNull() != tc.isNull {
				t.Errorf("IsZero/IsNull = %v/%v, want %v/%v",
					got.Field.IsZero(), got.Field.IsNull(), tc.isZero, tc.isNull)
			}
			if v, _ := got.Field.Get(); v != tc.value {
				t.Errorf("Get() = %q, want %q", v, tc.value)
			}
			// Re-encoding must reproduce the bytes it was given.
			out, err := json.Marshal(got)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if string(out) != tc.in {
				t.Errorf("round trip = %s, want %s", out, tc.in)
			}
		})
	}
}

// The one state that does NOT survive a round trip, pinned so the doc comment
// stays honest: a value whose own encoding is the literal null is the same bytes
// as a Null, so it comes back as one.
func TestOptional_AValueEncodingAsNullDecodesAsNull(t *testing.T) {
	type body struct {
		M Optional[Metadata] `json:"m,omitzero"`
	}
	raw, err := json.Marshal(body{M: Set(Metadata(nil))})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if want := `{"m":null}`; string(raw) != want {
		t.Fatalf("body = %s, want %s", raw, want)
	}
	var got body
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !got.M.IsNull() {
		t.Errorf("a nil map came back as %#v, want the null state", got.M)
	}
	if _, ok := got.M.Get(); ok {
		t.Error("Get reports a value; the wire carried a null and nothing else")
	}
}

// A *Update struct filled by DECODING JSON rather than by assigning its fields
// is the one migration the compiler cannot point at, and the CHANGELOG says so
// in those words -- a gateway that unmarshals an inbound patch body and forwards
// it is the realistic case.
//
// An inbound {"description": null} used to decode into a nil *string and then be
// DROPPED from the outgoing PATCH: the caller asked for a clear and silently did
// not get one, which is #64 one layer further out. It now decodes to the null
// state and goes back out as a null. Pinned because it is the one behaviour
// change in this release that no build failure announces.
func TestUpdateBody_DecodedFromJSONForwardsTheNull(t *testing.T) {
	var update TagUpdate
	inbound := `{"name":"Autumn","description":null}`
	if err := json.Unmarshal([]byte(inbound), &update); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !update.Description.IsNull() {
		t.Errorf("Description = %#v, want the null state", update.Description)
	}
	if name, ok := update.Name.Get(); !ok || name != "Autumn" {
		t.Errorf("Name = %q/%v, want Autumn/true", name, ok)
	}
	// Every key the inbound body did not carry stays omitted, so forwarding the
	// struct cannot touch a column the sender never named.
	if !update.ParentID.IsZero() || !update.Slug.IsZero() || !update.Metadata.IsZero() {
		t.Errorf("a key absent from the inbound body did not stay omitted: %#v", update)
	}
	got, err := json.Marshal(update)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(got) != inbound {
		t.Errorf("forwarded body = %s, want %s", got, inbound)
	}
}

func TestOptionalUnmarshalJSON_TypeMismatchIsAnError(t *testing.T) {
	var got patchBody
	err := json.Unmarshal([]byte(`{"field":42}`), &got)
	if err == nil {
		t.Fatal("Unmarshal of a number into Optional[string] succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "octonomy:") {
		t.Errorf("error = %v, want the octonomy: prefix this package wraps with", err)
	}
	if !got.Field.IsZero() {
		t.Error("a failed decode left the field set; it must stay omitted")
	}
}

// The doc comment claims Optional compares by value where T is comparable. The
// *string it replaced never did -- two pointers to equal strings are unequal --
// so this is a behavior change worth pinning rather than a detail.
func TestOptional_ComparesByValue(t *testing.T) {
	// Bound to variables rather than compared inline: staticcheck reads
	// Set("x") != Set("x") as two identical expressions, which is exactly the
	// claim being made -- that two independently built Optionals are equal.
	first, second := Set("x"), Set("x")
	if first != second {
		t.Error("Set(\"x\") != Set(\"x\"); Optional no longer compares by value")
	}
	if Set("x") == Set("y") {
		t.Error("Set(\"x\") == Set(\"y\")")
	}
	if Null[string]() == Set("") {
		t.Error("a null compares equal to an empty value; the states have collapsed")
	}
	if (Optional[string]{}) == Null[string]() {
		t.Error("an omitted Optional compares equal to a null one")
	}
}

// --- the source-level guard ---------------------------------------------------

// Every field of every *Update struct must be an Optional carrying omitzero.
//
// This is the compile-time half of what Optional.MarshalJSON enforces at run
// time, and it reads the SOURCE rather than reflecting over a list of three
// types, so a *Update struct added for a future resource is covered by existing
// here rather than by somebody remembering. A field left as a bare pointer is
// #64 reopened on one resource; a field tagged omitempty instead of omitzero
// never omits at all, because omitempty does not omit a struct.
func TestUpdateBodiesTagEveryOptionalOmitzero(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse the package source: %v", err)
	}
	pkg, ok := pkgs["octonomy"]
	if !ok {
		t.Fatalf("package octonomy not found in the parsed source; got %v", keys(pkgs))
	}

	seen := 0
	for path, file := range pkg.Files {
		ast.Inspect(file, func(n ast.Node) bool {
			spec, ok := n.(*ast.TypeSpec)
			if !ok || !strings.HasSuffix(spec.Name.Name, "Update") {
				return true
			}
			structType, ok := spec.Type.(*ast.StructType)
			if !ok {
				return true
			}
			seen++
			for _, field := range structType.Fields.List {
				// An EMBEDDED field has no name, and the loop below would skip
				// it in silence -- so a *Update struct that embedded something
				// would carry unchecked fields onto the wire. There is no such
				// struct today and there is no reason for one: a PATCH body is
				// a flat list of the server's own properties.
				if len(field.Names) == 0 {
					t.Errorf("%s: %s has an embedded field, which this guard cannot check "+
						"field-by-field. A PATCH body is a flat list of Optional fields",
						path, spec.Name.Name)
					continue
				}
				for _, name := range field.Names {
					where := path + ": " + spec.Name.Name + "." + name.Name
					if !isOptionalType(field.Type) {
						t.Errorf("%s is not an Optional. Every field of a PATCH body is "+
							"three-state since #64; a pointer cannot express a null, so this "+
							"field cannot be cleared at all", where)
						continue
					}
					// The json tag's OPTIONS, parsed rather than grepped: a
					// substring match over the raw tag would be satisfied by
					// `json:"field" validate:"omitzero"` or by a field whose
					// json NAME is "omitzero", neither of which omits anything.
					raw := ""
					if field.Tag != nil {
						raw = field.Tag.Value
					}
					options, tag := jsonTagOptions(raw)
					switch {
					case options["omitempty"]:
						t.Errorf("%s is tagged omitempty, which never omits a struct -- so the "+
							"field would send null on every PATCH that does not set it. Use omitzero", where)
					case !options["omitzero"]:
						t.Errorf("%s is missing the omitzero tag option, so an unset field would "+
							"encode as null rather than being left out (tag: %s)", where, tag)
					}
				}
			}
			return true
		})
	}
	if seen < 3 {
		t.Errorf("found %d *Update struct(s), want at least the 3 that exist "+
			"(TagUpdate, VocabularyUpdate, TagAliasUpdate) -- the guard is not reading the source", seen)
	}
}

// jsonTagOptions returns the set of comma-separated OPTIONS on a field's json
// struct tag, along with the tag itself for a failure message. raw is the
// quoted tag literal as it appears in the source, so it is unquoted first; a
// tag that will not unquote yields no options, which the caller reports as a
// missing omitzero rather than passing.
func jsonTagOptions(raw string) (map[string]bool, string) {
	options := map[string]bool{}
	unquoted, err := strconv.Unquote(raw)
	if err != nil {
		return options, raw
	}
	value := reflect.StructTag(unquoted).Get("json")
	parts := strings.Split(value, ",")
	for _, option := range parts[1:] {
		options[option] = true
	}
	return options, value
}

// isOptionalType reports whether expr names Optional[...]; the AST spells a
// generic instantiation as an index expression over the type name.
func isOptionalType(expr ast.Expr) bool {
	index, ok := expr.(*ast.IndexExpr)
	if !ok {
		return false
	}
	ident, ok := index.X.(*ast.Ident)
	return ok && ident.Name == "Optional"
}

func keys[K comparable, V any](m map[K]V) []K {
	out := make([]K, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// A last belt: the three structs that exist today must actually be reachable
// under those names, so a rename cannot make the source guard above vacuous by
// leaving it with nothing to find.
func TestUpdateBodiesExistUnderTheExpectedNames(t *testing.T) {
	for _, v := range []any{TagUpdate{}, VocabularyUpdate{}, TagAliasUpdate{}} {
		typ := reflect.TypeOf(v)
		if typ.NumField() == 0 {
			t.Errorf("%s has no fields", typ.Name())
		}
	}
}
