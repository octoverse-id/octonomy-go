package octonomy

// The wire spelling of every request body that is not a *Update (update_test.go
// covers those), pinned against literal JSON rather than against the struct's
// own encoding.
//
// A misspelled write tag compiles, vets, and passes every test that marshals
// the struct and reads it back, because both sides use the same tag. The server
// then ignores the unknown key -- and on ResourceReplace that is destructive: an
// alias_slugs key it does not recognize leaves a slug-only replace naming
// nothing, which CLEARS the resource with a 200. So each body is compared byte
// for byte with what the contract names, once with every field set and once
// with only the required ones, which pins omitempty too: a slice that lost it
// goes out as null, which the server refuses.
//
// The "every field" case fails if a field is left at its zero value, so a field
// added to one of these structs later has to be added here as well.

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestWriteBodies_WireSpelling(t *testing.T) {
	str := String
	tests := []struct {
		name string
		body interface{}
		full bool // every field set: checked by reflection below
		want string
	}{
		{"TagCreate, every field", TagCreate{
			ApplicationID: str("app"), Name: "n", Slug: "s", Type: "label", Description: str("d"),
			ParentID: str("p"), VocabularyID: str("v"), Metadata: Metadata{"k": "v"}, IsActive: Bool(true),
		}, true, `{"application_id":"app","name":"n","slug":"s","type":"label","description":"d","parent_id":"p","vocabulary_id":"v","metadata":{"k":"v"},"is_active":true}`},
		{"TagCreate, required only", TagCreate{Name: "n", Slug: "s", Type: "label"}, false,
			`{"name":"n","slug":"s","type":"label"}`},

		{"VocabularyCreate, every field", VocabularyCreate{
			ApplicationID: str("app"), Name: "n", Slug: "s", Description: str("d"), Metadata: Metadata{"k": "v"}, IsActive: Bool(false),
		}, true, `{"application_id":"app","name":"n","slug":"s","description":"d","metadata":{"k":"v"},"is_active":false}`},
		{"VocabularyCreate, required only", VocabularyCreate{Name: "n", Slug: "s"}, false,
			`{"name":"n","slug":"s"}`},

		{"TagAliasCreate, every field", TagAliasCreate{
			ApplicationID: str("app"), TagID: "t", Name: "n", Slug: "s", Metadata: Metadata{"k": "v"}, IsActive: Bool(false),
		}, true, `{"application_id":"app","tag_id":"t","name":"n","slug":"s","metadata":{"k":"v"},"is_active":false}`},
		{"TagAliasCreate, required only", TagAliasCreate{TagID: "t", Name: "n", Slug: "s"}, false,
			`{"tag_id":"t","name":"n","slug":"s"}`},

		// Exactly one of tag_id / alias_id / alias_slug is legal on the server;
		// setting all three here only checks their spelling.
		{"AssignmentCreate, every field", AssignmentCreate{
			ApplicationID: "app", TagID: str("t"), AliasID: str("a"), AliasSlug: str("as"),
			ResourceType: "order", ResourceID: "r", AssignedBy: str("me"),
		}, true, `{"application_id":"app","tag_id":"t","alias_id":"a","alias_slug":"as","resource_type":"order","resource_id":"r","assigned_by":"me"}`},
		{"AssignmentCreate, by alias slug", AssignmentCreate{
			ApplicationID: "app", AliasSlug: str("as"), ResourceType: "order", ResourceID: "r",
		}, false, `{"application_id":"app","alias_slug":"as","resource_type":"order","resource_id":"r"}`},

		{"AssignmentRemove, every field", AssignmentRemove{
			ApplicationID: "app", TagID: "t", ResourceType: "order", ResourceID: "r",
		}, true, `{"application_id":"app","tag_id":"t","resource_type":"order","resource_id":"r"}`},

		{"BulkAssign, every field", BulkAssign{
			ApplicationID: "app", ResourceType: "order", ResourceID: "r",
			TagIDs: []string{"t1", "t2"}, AliasSlugs: []string{"as"}, AssignedBy: str("me"),
		}, true, `{"application_id":"app","resource_type":"order","resource_id":"r","tag_ids":["t1","t2"],"alias_slugs":["as"],"assigned_by":"me"}`},
		{"BulkAssign, slugs only", BulkAssign{
			ApplicationID: "app", ResourceType: "order", ResourceID: "r", AliasSlugs: []string{"as"},
		}, false, `{"application_id":"app","resource_type":"order","resource_id":"r","alias_slugs":["as"]}`},
		{"BulkAssign, ids only", BulkAssign{
			ApplicationID: "app", ResourceType: "order", ResourceID: "r", TagIDs: []string{"t1"},
		}, false, `{"application_id":"app","resource_type":"order","resource_id":"r","tag_ids":["t1"]}`},

		{"BulkRemove, every field", BulkRemove{
			ApplicationID: "app", ResourceType: "order", ResourceID: "r", TagIDs: []string{"t1"},
		}, true, `{"application_id":"app","resource_type":"order","resource_id":"r","tag_ids":["t1"]}`},

		{"ResourceReplace, every field", ResourceReplace{
			ApplicationID: "app", TagIDs: []string{"t1"}, AliasSlugs: []string{"as"}, AssignedBy: str("me"),
		}, true, `{"application_id":"app","tag_ids":["t1"],"alias_slugs":["as"],"assigned_by":"me"}`},
		// The slug-only replace: the key a misspelling would silently drop.
		{"ResourceReplace, slugs only", ResourceReplace{ApplicationID: "app", AliasSlugs: []string{"as"}}, false,
			`{"application_id":"app","alias_slugs":["as"]}`},
		// The empty replace, which clears the resource on purpose: no null slices.
		{"ResourceReplace, empty", ResourceReplace{ApplicationID: "app"}, false,
			`{"application_id":"app"}`},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			if tt.full {
				v := reflect.ValueOf(tt.body)
				for i := 0; i < v.NumField(); i++ {
					if v.Field(i).IsZero() {
						t.Fatalf("%s is left unset in the every-field case; set it and add its key to want", v.Type().Field(i).Name)
					}
				}
			}
			got, err := json.Marshal(tt.body)
			if err != nil {
				t.Fatalf("json.Marshal: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("wire body\n got %s\nwant %s", got, tt.want)
			}
		})
	}
}
