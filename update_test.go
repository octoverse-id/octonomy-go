package octonomy

// The *Update MarshalJSON added for #37 (#91). It has no counterpart in main's
// tests to port: main fixed #37 by changing the field to Optional[Metadata],
// which this line cannot do without breaking v1.0.0 callers, so the method and
// these tests are this line's own.
//
// The tests are driven by reflection over the struct, not by a list of fields,
// so a field added to TagUpdate, VocabularyUpdate or TagAliasUpdate later is
// covered the day it is added -- and one the hand-written MarshalJSON forgets to carry fails here
// instead of being silently dropped from every PATCH.

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

// updateBodies pairs each *Update type with its tag-only encoding.
var updateBodies = []struct {
	name  string
	typ   reflect.Type
	plain func(reflect.Value) interface{}
}{
	{"TagUpdate", reflect.TypeOf(TagUpdate{}), func(v reflect.Value) interface{} {
		return plainTagUpdate(v.Interface().(TagUpdate))
	}},
	{"VocabularyUpdate", reflect.TypeOf(VocabularyUpdate{}), func(v reflect.Value) interface{} {
		return plainVocabularyUpdate(v.Interface().(VocabularyUpdate))
	}},
	{"TagAliasUpdate", reflect.TypeOf(TagAliasUpdate{}), func(v reflect.Value) interface{} {
		return plainTagAliasUpdate(v.Interface().(TagAliasUpdate))
	}},
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

// The method is in the VALUE's method set. Update takes the struct by value, so
// a pointer-receiver MarshalJSON would be skipped by encoding/json with no error
// and every PATCH would fall back to the omitempty encoding.
func TestUpdateMarshalJSON_IsOnTheValueReceiver(t *testing.T) {
	marshaler := reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	for _, body := range updateBodies {
		if !body.typ.Implements(marshaler) {
			t.Errorf("%s (the value type) does not implement json.Marshaler; Update passes it by value", body.name)
		}
	}
}

// Set exactly one field and the body carries exactly that key, with the same
// bytes the struct tags alone produce -- so the method drops no field, renames
// none, and changes nothing about the fields it was not written for.
func TestUpdateMarshalJSON_EachFieldAloneIsOneKey(t *testing.T) {
	for _, body := range updateBodies {
		for i := 0; i < body.typ.NumField(); i++ {
			i, f, body := i, body.typ.Field(i), body
			t.Run(body.name+"."+f.Name, func(t *testing.T) {
				v := reflect.New(body.typ).Elem()
				v.Field(i).Set(sampleFor(t, f))

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

// The fix itself, #37: the three states of Metadata, on each *Update type.
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
