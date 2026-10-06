package octonomy

// The *Update MarshalJSON added for #37 (#91). It has no counterpart in main's
// tests to port: main fixed #37 by changing the field to Optional[Metadata],
// which this line cannot do without breaking v1.0.0 callers, so the method and
// these tests are this line's own.
//
// The tests are driven by reflection over the struct, not by a list of fields,
// so a field added to TagUpdate, VocabularyUpdate or TagAliasUpdate later is
// covered the day it is added -- and one the hand-written MarshalJSON forgets to carry fails here
// instead of being silently dropped from every PATCH. A new *Update TYPE is
// covered once it has a row in updateBodies, and TestUpdateBodiesNamesEveryUpdateType
// (updateguard_test.go, #96) fails until it does. That file also reads the tags
// from source, which catches what marshalling here cannot on a modern
// toolchain: a leftover omitzero, which only Go 1.13's encoding/json ignores.

import (
	"bytes"
	"context"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// The struct-tag encoding, which is exactly what v1.0.0 sent: converting to a
// defined type with the same fields drops the methods, MarshalJSON included.
type (
	plainTagUpdate        TagUpdate
	plainVocabularyUpdate VocabularyUpdate
	plainTagAliasUpdate   TagAliasUpdate
)

// updateBodies pairs each *Update type with its tag-only encoding, and with one
// value of it, every field set, whose wire body is written out by hand.
//
// The hand-written body is the one check here that does not read the struct's
// own tags. A key misspelled the same way on the struct and in its MarshalJSON
// passes every comparison against the tag encoding -- both sides agree -- and
// the server ignores the unknown key, so the PATCH changes nothing and answers
// 200. wire is spelled from the contract's PATCH schema (PatchedTagPatch,
// PatchedVocabularyPatch, PatchedTagAliasPatch in docs/openapi-v2.yaml), as
// writebodies_test.go does for every other request body.
var updateBodies = []struct {
	name  string
	typ   reflect.Type
	plain func(reflect.Value) interface{}
	full  interface{}
	wire  string
}{
	{
		name: "TagUpdate", typ: reflect.TypeOf(TagUpdate{}),
		plain: func(v reflect.Value) interface{} { return plainTagUpdate(v.Interface().(TagUpdate)) },
		full: TagUpdate{
			ApplicationID: String("app"), Name: String("n"), Slug: String("s"), Type: String("label"),
			Description: String("d"), ParentID: String("p"), VocabularyID: String("v"),
			Metadata: Metadata{"k": "v"}, IsActive: Bool(false),
		},
		wire: `{"application_id":"app","name":"n","slug":"s","type":"label","description":"d","parent_id":"p","vocabulary_id":"v","metadata":{"k":"v"},"is_active":false}`,
	},
	{
		name: "VocabularyUpdate", typ: reflect.TypeOf(VocabularyUpdate{}),
		plain: func(v reflect.Value) interface{} { return plainVocabularyUpdate(v.Interface().(VocabularyUpdate)) },
		full: VocabularyUpdate{
			ApplicationID: String("app"), Name: String("n"), Slug: String("s"), Description: String("d"),
			Metadata: Metadata{"k": "v"}, IsActive: Bool(false),
		},
		wire: `{"application_id":"app","name":"n","slug":"s","description":"d","metadata":{"k":"v"},"is_active":false}`,
	},
	{
		name: "TagAliasUpdate", typ: reflect.TypeOf(TagAliasUpdate{}),
		plain: func(v reflect.Value) interface{} { return plainTagAliasUpdate(v.Interface().(TagAliasUpdate)) },
		full: TagAliasUpdate{
			ApplicationID: String("app"), TagID: String("t"), Name: String("n"), Slug: String("s"),
			Metadata: Metadata{"k": "v"}, IsActive: Bool(false),
		},
		wire: `{"application_id":"app","tag_id":"t","name":"n","slug":"s","metadata":{"k":"v"},"is_active":false}`,
	},
}

// sampleFor returns a non-zero value for one *Update field, or fails the test
// for a field type it does not know -- so a new kind of field cannot slip past
// the per-field checks by being unassignable.
func sampleFor(t *testing.T, f reflect.StructField) reflect.Value {
	t.Helper()
	switch f.Type {
	case reflect.TypeOf((*string)(nil)):
		return reflect.ValueOf(String("value-of-" + f.Name))
	case reflect.TypeOf((*bool)(nil)):
		return reflect.ValueOf(Bool(true))
	case reflect.TypeOf(Metadata(nil)):
		return reflect.ValueOf(Metadata{"k": "v"})
	}
	t.Fatalf("%s has type %s, which this test does not know how to populate; extend sampleFor", f.Name, f.Type)
	return reflect.Value{}
}

// pointeeSamples returns the values one *Update field is set to on its own: the
// sample above and, for a pointer, a pointer to the ZERO value. Both must be
// sent. omitempty tests a pointer for nil only, so String("") and Bool(false)
// are values a caller sets -- Bool(false) is how IsActive deactivates -- and a
// MarshalJSON that dereferenced the pointer into an omitempty value would drop
// them while passing every non-zero sample.
func pointeeSamples(t *testing.T, f reflect.StructField) []fieldSample {
	t.Helper()
	out := []fieldSample{{"set", sampleFor(t, f)}}
	if f.Type.Kind() == reflect.Ptr {
		out = append(out, fieldSample{"zero pointee", reflect.New(f.Type.Elem())})
	}
	return out
}

type fieldSample struct {
	label string
	value reflect.Value
}

func jsonKey(f reflect.StructField) string {
	return strings.Split(f.Tag.Get("json"), ",")[0]
}

func marshal(t *testing.T, v interface{}) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal(%T): %v", v, err)
	}
	return b
}

// Any MarshalJSON is in the VALUE's method set. Update takes the struct by
// value, so a pointer-receiver MarshalJSON would be skipped by encoding/json
// with no error and every PATCH would fall back to the omitempty encoding. A
// type carrying Metadata must have one, for #37; a type of pointer fields alone
// needs none, since the tag encoding already sends exactly what was set.
func TestUpdateMarshalJSON_IsOnTheValueReceiver(t *testing.T) {
	marshaler := reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	metadata := reflect.TypeOf(Metadata(nil))
	for _, body := range updateBodies {
		if reflect.PtrTo(body.typ).Implements(marshaler) && !body.typ.Implements(marshaler) {
			t.Errorf("%s implements json.Marshaler on the pointer only; Update passes it by value", body.name)
		}
		for i := 0; i < body.typ.NumField(); i++ {
			if body.typ.Field(i).Type == metadata && !body.typ.Implements(marshaler) {
				t.Errorf("%s carries Metadata in %s and does not implement json.Marshaler on the value, "+
					"so Metadata{} is dropped by omitempty (#37)", body.name, body.typ.Field(i).Name)
			}
		}
	}
}

// Every field set, against the body written out by hand: the key names come
// from the contract, not from the tags under test.
func TestUpdateBodies_WireSpelling(t *testing.T) {
	for _, body := range updateBodies {
		v := reflect.ValueOf(body.full)
		if v.Type() != body.typ {
			t.Errorf("updateBodies row %s holds a %s in full", body.name, v.Type())
			continue
		}
		for i := 0; i < v.NumField(); i++ {
			if v.Field(i).IsZero() {
				t.Errorf("%s.%s is left unset in full; set it and add its key to wire", body.name, v.Type().Field(i).Name)
			}
		}
		if got := marshal(t, body.full); string(got) != body.wire {
			t.Errorf("%s wire body\n got %s\nwant %s", body.name, got, body.wire)
		}
	}
}

// Set exactly one field and the body carries exactly that key, with the same
// bytes the struct tags alone produce -- so the method drops no field, renames
// none, and changes nothing about the fields it was not written for. A pointer
// field is set twice, once to a pointer to its zero value (pointeeSamples).
func TestUpdateMarshalJSON_EachFieldAloneIsOneKey(t *testing.T) {
	for _, body := range updateBodies {
		for i := 0; i < body.typ.NumField(); i++ {
			i, f, body := i, body.typ.Field(i), body
			for _, sample := range pointeeSamples(t, f) {
				sample := sample
				t.Run(body.name+"."+f.Name+"/"+sample.label, func(t *testing.T) {
					v := reflect.New(body.typ).Elem()
					v.Field(i).Set(sample.value)

					got := marshal(t, v.Interface())
					var keys map[string]json.RawMessage
					if err := json.Unmarshal(got, &keys); err != nil {
						t.Fatalf("decode %s: %v", got, err)
					}
					if len(keys) != 1 {
						t.Fatalf("setting only %s sent %d keys: %s", f.Name, len(keys), got)
					}
					if _, ok := keys[jsonKey(f)]; !ok {
						t.Errorf("setting only %s sent %s, want the key %q", f.Name, got, jsonKey(f))
					}
					if want := marshal(t, body.plain(v)); !bytes.Equal(got, want) {
						t.Errorf("%s alone: got %s, want the v1.0.0 encoding %s", f.Name, got, want)
					}
				})
			}
		}
	}
}

// Every field set at once: the keys come out in declaration order, matching
// encoding/json, byte for byte against the struct-tag encoding.
func TestUpdateMarshalJSON_AllFieldsMatchTheTagEncodingInOrder(t *testing.T) {
	for _, body := range updateBodies {
		v := reflect.New(body.typ).Elem()
		var order []string
		for i := 0; i < body.typ.NumField(); i++ {
			v.Field(i).Set(sampleFor(t, body.typ.Field(i)))
			order = append(order, `"`+jsonKey(body.typ.Field(i))+`":`)
		}
		got := marshal(t, v.Interface())
		if want := marshal(t, body.plain(v)); !bytes.Equal(got, want) {
			t.Errorf("%s: got %s, want %s", body.name, got, want)
		}
		at := 0
		for _, key := range order {
			idx := bytes.Index(got[at:], []byte(key))
			if idx < 0 {
				t.Errorf("%s: key %s missing or out of declaration order in %s", body.name, key, got)
				break
			}
			at += idx + len(key)
		}
	}
}

// The fix itself, #37: the three states of Metadata, on every Metadata field of
// every *Update type in updateBodies -- which TestUpdateBodiesNamesEveryUpdateType
// holds to the source -- asserted on the raw bytes. Decoded back into a struct,
// "metadata":null and an absent key both leave a nil map, and a null is not what
// any of the three states asks the server for. Each other field is left nil, so
// the bytes are the Metadata key alone.
func TestUpdateMetadataHasThreeStates(t *testing.T) {
	tests := []struct {
		name     string
		metadata Metadata
		want     string // the key's encoding; "" means the key is absent
	}{
		{"nil omits the key", nil, ""},
		{"empty map sends {} and empties the object", Metadata{}, `{}`},
		{"populated map replaces the object", Metadata{"team": "growth"}, `{"team":"growth"}`},
	}
	metadata := reflect.TypeOf(Metadata(nil))
	fields := 0
	for _, body := range updateBodies {
		for i := 0; i < body.typ.NumField(); i++ {
			i, f, body := i, body.typ.Field(i), body
			if f.Type != metadata {
				continue
			}
			fields++
			for _, tt := range tests {
				tt := tt
				t.Run(body.name+"."+f.Name+"/"+tt.name, func(t *testing.T) {
					v := reflect.New(body.typ).Elem()
					v.Field(i).Set(reflect.ValueOf(tt.metadata))
					want := `{}`
					if tt.want != "" {
						want = `{"` + jsonKey(f) + `":` + tt.want + `}`
					}
					if got := marshal(t, v.Interface()); string(got) != want {
						t.Errorf("got %s, want %s", got, want)
					}
				})
			}
		}
	}
	if fields < knownUpdateTypes {
		t.Errorf("found %d Metadata fields across updateBodies, want at least %d", fields, knownUpdateTypes)
	}
}

// ...and beside the other fields, which the method must leave as they were.
func TestUpdateMarshalJSON_MetadataHasThreeStates(t *testing.T) {
	tests := []struct {
		name     string
		metadata Metadata
		want     string
	}{
		{"nil omits the key", nil, `{"name":"N"}`},
		{"empty map sends {} and empties the object", Metadata{}, `{"name":"N","metadata":{}}`},
		{"populated map replaces the object", Metadata{"team": "growth"}, `{"name":"N","metadata":{"team":"growth"}}`},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			tag := marshal(t, TagUpdate{Name: String("N"), Metadata: tt.metadata})
			if string(tag) != tt.want {
				t.Errorf("TagUpdate = %s, want %s", tag, tt.want)
			}
			vocab := marshal(t, VocabularyUpdate{Name: String("N"), Metadata: tt.metadata})
			if string(vocab) != tt.want {
				t.Errorf("VocabularyUpdate = %s, want %s", vocab, tt.want)
			}
			alias := marshal(t, TagAliasUpdate{Name: String("N"), Metadata: tt.metadata})
			if string(alias) != tt.want {
				t.Errorf("TagAliasUpdate = %s, want %s", alias, tt.want)
			}
		})
	}

	// Metadata{} is the ONE input whose encoding the fix changes. Pin that the
	// tag-only encoding drops it, so this test fails if it is ever "fixed" in a
	// way that also changes what the other inputs send.
	if got := marshal(t, plainTagUpdate(TagUpdate{Metadata: Metadata{}})); string(got) != `{}` {
		t.Errorf("the struct-tag encoding of Metadata{} = %s, want {} (the v1.0.0 behaviour this fixes)", got)
	}
}

// ...and on the wire, through Update itself: what the server receives.
func TestUpdate_EmptyMetadataReachesTheServer(t *testing.T) {
	routes := []struct {
		name string
		call func(*Client) error
	}{
		{"tags", func(c *Client) error {
			_, err := c.Tags.Update(context.Background(), "tag_1", TagUpdate{Metadata: Metadata{}})
			return err
		}},
		{"vocabularies", func(c *Client) error {
			_, err := c.Vocabularies.Update(context.Background(), "voc_1", VocabularyUpdate{Metadata: Metadata{}})
			return err
		}},
		{"tag aliases", func(c *Client) error {
			_, err := c.Aliases.Update(context.Background(), "alias_1", TagAliasUpdate{Metadata: Metadata{}})
			return err
		}},
	}
	for _, route := range routes {
		route := route
		t.Run(route.name, func(t *testing.T) {
			c, cleanup := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				body, err := ioutil.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read body: %v", err)
				}
				if string(body) != `{"metadata":{}}` {
					t.Errorf("PATCH body = %s, want {\"metadata\":{}}", body)
				}
				writeData(t, w, http.StatusOK, Tag{ID: "row_1"})
			})
			defer cleanup()
			if err := route.call(c); err != nil {
				t.Fatalf("Update: %v", err)
			}
		})
	}
}
