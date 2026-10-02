package octonomy

// Ported from main's types_test.go at 5e40964 for #95, and every mention of main
// here means main at that commit. Of its seven tests, this
// is the one that applies to this line as it stands; docs/compat-test-disposition.md
// has the other six's verdicts -- five wait on a port of DecodeMetadata, and
// TestUpdateMetadata_OmitClearAndReplaceOnEveryPatchBody is covered by
// update_test.go.

import (
	"reflect"
	"testing"
)

// Metadata must stay a type ALIAS for map[string]interface{}, and on this line
// the reason is v1.0.0 itself. That release declared it as an alias, so a
// caller may pass a Metadata where a map[string]interface{} is expected, put
// one in a type switch case beside the unnamed map type, or name both in one
// signature. A defined type would break each of those, and this line can never
// publish a major to carry a break (AGENTS.md). main pins the same fact for its
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
