package octonomy

import (
	"encoding"
	"encoding/json"
	"fmt"
	"reflect"
)

// JSON nesting guards -- a hazard only Go 1.13 has, and a guard only this line
// carries.
//
// Go 1.13's encoding/json has NO cycle detection on Marshal and NO depth limit
// on Unmarshal; both arrived in later releases of the standard library. Probed
// on go1.13.15: a Metadata map that contains itself sends json.Marshal into
// unbounded recursion and the process hangs, and a 200,000-level-deep response
// decodes with a nil error. Deep enough input exhausts the goroutine's stack,
// and that is a FATAL RUNTIME ERROR, not a panic: recover cannot catch it, so it
// kills the consumer's process from inside a library that promises never to
// panic. The 32 MiB read ceiling does not help -- 400 KB of brackets is far
// under it.
//
// Both halves are bounded by the same ceiling, and depth alone is enough for
// both. A cycle is unbounded depth, so a walk that stops at a fixed depth stops
// on a cycle too, with no visited set to maintain.
//
//   - Requests: checkBodyDepth walks the caller's body before json.Marshal.
//   - Responses: decodeJSON scans the bytes before json.Unmarshal. This is the
//     half with a threat model: Metadata and an error envelope's details are
//     server-controlled free-form JSON, so a server -- or anything able to
//     answer in its place -- chooses how deep they go.
//
// The main transport.go this was ported from (5e40964) has no counterpart: a
// modern encoding/json detects encoding cycles and bounds decoding depth itself.
// So this is a deliberate divergence between the lines, not a porting gap.

// maxJSONDepth is the nesting ceiling for both halves. It is the limit the
// standard library's own decoder adopted once it had one, so a response this
// line refuses is one a modern encoding/json refuses too, and it is far beyond any
// real Octonomy payload while far below a depth that threatens the stack.
const maxJSONDepth = 10000

// errJSONTooDeep is what decodeJSON returns for a response nested past the
// ceiling. It is unexported because a caller on a modern toolchain has no
// counterpart to compare against -- its standard library rejects the same body
// with its own error -- and because a body this deep is a malformed response, not a
// condition a caller branches on. Its text is distinct from the standard
// library's ("exceeded max depth") so a test run on a modern toolchain can still
// tell which of the two refused.
var errJSONTooDeep = fmt.Errorf("JSON nests deeper than %d levels; refused before decoding, because Go 1.13's encoding/json has no depth limit and would exhaust the stack", maxJSONDepth)

// decodeJSON is json.Unmarshal behind the response-side depth guard. EVERY
// decode of response bytes in this package goes through it --
// TestEveryResponseDecodeIsDepthBounded enforces that over the source -- so the
// guard cannot be bypassed by a decoder written later that forgot it.
//
// The scan is repeated on sub-slices of a body already scanned (the envelope,
// then its data), which is redundant but cheap: it is one pass over bytes the
// decoder is about to walk anyway, and keeping every call site identical is
// what lets the source guard be a one-line rule.
func decodeJSON(data []byte, v interface{}) error {
	if err := checkJSONDepth(data); err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// checkJSONDepth reports errJSONTooDeep when data nests objects and arrays past
// maxJSONDepth. It is one O(n) pass that tracks only whether it is inside a
// string -- a bracket inside a string is content, not structure -- and the
// current depth.
//
// It does not validate the JSON. It does not need to: json.Unmarshal checks the
// whole document with its iterative scanner before its recursive decoder runs,
// so malformed input never reaches the recursion this bounds. What it must get
// right is the depth of a VALID document, and for one it is exact.
func checkJSONDepth(data []byte) error {
	depth := 0
	inString, escaped := false, false
	for _, b := range data {
		if inString {
			switch {
			case escaped:
				escaped = false
			case b == '\\':
				escaped = true
			case b == '"':
				inString = false
			}
			continue
		}
		switch b {
		case '"':
			inString = true
		case '{', '[':
			depth++
			if depth > maxJSONDepth {
				return errJSONTooDeep
			}
		case '}', ']':
			depth--
		}
	}
	return nil
}

// checkBodyDepth bounds how deep json.Marshal would have to recurse to encode
// body, refusing a cycle as the unbounded depth it is.
//
// It walks what encoding/json walks, and only that: through pointers and
// interfaces, into maps, slices, arrays, and the struct fields encoding/json
// encodes. A field it skips -- unexported, tagged json:"-", or an unexported
// embedded type that is not a struct -- is skipped here too, so a back-reference
// a caller excludes from the wire with json:"-" is no reason to refuse the
// request. Every map, slice, array, struct, and pointer it enters counts one
// level; interfaces count none, since one cannot hold itself without a pointer
// or a container in between. Counting pointers is what stops a cycle that runs
// through no container at all -- a *interface{} that points at itself -- and it
// makes the request ceiling at most, not exactly, the response one. The bodies
// this package defines carry pointers only at their leaves.
//
// A value whose type implements json.Marshaler or encoding.TextMarshaler, and
// is declared outside this package, is opaque: encoding/json calls its method
// instead of walking its fields, so the walk stops there too. That covers
// json.RawMessage, whose bytes are emitted through the iterative scanner and so
// cannot exhaust the stack however deep they nest. What such a method does is
// the caller's code, not this package's. This package's own *Update types do
// implement json.Marshaler, and are walked: their MarshalJSON encodes exactly
// their fields, Metadata included.
func checkBodyDepth(body interface{}) error {
	return walkBodyDepth(reflect.ValueOf(body), 0)
}

var (
	jsonMarshalerType = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	textMarshalerType = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
	thisPackage       = reflect.TypeOf(Client{}).PkgPath()
)

// opaqueToTheEncoder reports whether encoding/json would hand v to a method of
// the caller's rather than walk it -- the rule checkBodyDepth's comment states.
func opaqueToTheEncoder(v reflect.Value) bool {
	t := v.Type()
	named := t
	if named.Kind() == reflect.Ptr {
		named = named.Elem()
	}
	if named.PkgPath() == thisPackage {
		return false
	}
	if t.Implements(jsonMarshalerType) || t.Implements(textMarshalerType) {
		return true
	}
	// encoding/json also uses a pointer-receiver method when the value is
	// addressable -- a struct field reached through a pointer, a slice element.
	if t.Kind() != reflect.Ptr && v.CanAddr() {
		pt := reflect.PtrTo(t)
		return pt.Implements(jsonMarshalerType) || pt.Implements(textMarshalerType)
	}
	return false
}

// skippedByTheEncoder reports whether encoding/json leaves struct field f off
// the wire entirely, so the walk need not follow it.
func skippedByTheEncoder(f reflect.StructField) bool {
	if f.Tag.Get("json") == "-" {
		return true
	}
	if f.PkgPath == "" {
		return false
	}
	if !f.Anonymous {
		return true
	}
	// An unexported embedded field is encoded only when it is a struct, or a
	// pointer to one, whose exported fields are promoted.
	t := f.Type
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t.Kind() != reflect.Struct
}

func walkBodyDepth(v reflect.Value, depth int) error {
	enter := func() error {
		depth++
		if depth > maxJSONDepth {
			return fmt.Errorf("request body nests deeper than %d levels; a Metadata map that contains itself, directly or through another, does so without end, and Go 1.13's encoding/json has no cycle detection", maxJSONDepth)
		}
		return nil
	}

	if !v.IsValid() {
		return nil
	}
	if v.Kind() != reflect.Interface && opaqueToTheEncoder(v) {
		return nil
	}
	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			return nil
		}
		return walkBodyDepth(v.Elem(), depth)
	case reflect.Ptr:
		if v.IsNil() {
			return nil
		}
		if err := enter(); err != nil {
			return err
		}
		return walkBodyDepth(v.Elem(), depth)
	case reflect.Map:
		if v.IsNil() {
			return nil
		}
		if err := enter(); err != nil {
			return err
		}
		iter := v.MapRange()
		for iter.Next() {
			if err := walkBodyDepth(iter.Value(), depth); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() {
			return nil
		}
		// A []byte is encoded as one base64 string, not as an array.
		if v.Kind() == reflect.Slice && v.Type().Elem().Kind() == reflect.Uint8 {
			return nil
		}
		if err := enter(); err != nil {
			return err
		}
		for i := 0; i < v.Len(); i++ {
			if err := walkBodyDepth(v.Index(i), depth); err != nil {
				return err
			}
		}
	case reflect.Struct:
		if err := enter(); err != nil {
			return err
		}
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			if skippedByTheEncoder(t.Field(i)) {
				continue
			}
			if err := walkBodyDepth(v.Field(i), depth); err != nil {
				return err
			}
		}
	}
	return nil
}
