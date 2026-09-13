package octonomy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// optionalState is which of the three things an Optional says. The zero value
// is optionalOmit on purpose: an Optional nobody assigned omits its key, so a
// partially filled *Update struct sends exactly the fields the caller named.
type optionalState uint8

const (
	optionalOmit optionalState = iota
	optionalNull
	optionalValue
)

// errOmittedOptional is returned by Optional.MarshalJSON when an omitted
// Optional is asked to encode itself. See the type's doc comment for why that
// is an error rather than a null.
var errOmittedOptional = errors.New("octonomy: an omitted Optional has no JSON encoding; tag the field `json:\",omitzero\"` so the key is left out instead")

// Optional carries one of three things for a PATCH body field: nothing at all,
// an explicit JSON null, or a value. A plain pointer can carry only two of
// them, which is the whole reason this type exists.
//
//	octonomy.Set("x")                 // sends "field": "x"
//	octonomy.Null[string]()           // sends "field": null
//	octonomy.Optional[string]{}       // the zero value -- omits the key entirely
//
// # Why a pointer was not enough
//
// Every optional field on the *Update structs used to be a *T with omitempty,
// where nil meant "leave this one alone". That spends the pointer's one spare
// state on absent-versus-set and leaves nothing to say null with, so a caller
// could not un-nest a tag, detach it from a vocabulary, or remove a description
// through this SDK at all: the request was inexpressible, not merely awkward
// (#64). It is the sibling of #37, which hit the same wall on Metadata and
// could still solve it by ADDING a pointer.
//
// Nothing about the old shape was a lie -- nil did leave the field alone, and
// no call silently did the wrong thing. What was missing was a way to say the
// third thing.
//
// # Not every field the server will let you null
//
// The type is uniform across the three *Update structs so that one spelling
// fills any field; the SERVER decides which nulls CLEAR something, and four do
// (verified live, see TagUpdate). A null anywhere else is a 400 that
// IsValidation matches, except ApplicationID: that one is a 409 IsScopeImmutable
// matches when the row HAS an application, and an ordinary 200 no-op when the
// row is already tenant-shared -- the server refuses a scope CHANGE rather than
// the literal null. This package does not pre-empt any of it; rejecting a null
// here would be re-running the server's validation, and the server already names
// the field.
//
// # The zero value omits, and that is load-bearing
//
// A field of this type MUST be tagged `json:",omitzero"`, never `omitempty`:
// omitempty never omits a struct, which is why this shape was unavailable until
// Go 1.24 added omitzero and the IsZero method it consults.
//
// An omitted Optional asked to encode itself returns an error rather than
// guessing, because the only other answer is null -- so a field that lost its
// tag would put "field": null on every PATCH that does not touch it, rewriting
// columns the caller never named and doing it silently. That is the failure
// this SDK refuses everywhere else. TestUpdateBodiesTagEveryOptionalOmitzero is
// the same guard at test time, over every field of the three *Update structs;
// this is the half that still holds for a struct of the caller's own.
//
// # Comparability
//
// Optional[T] is comparable exactly when T is, and compares by VALUE rather
// than by address -- octonomy.Set("x") == octonomy.Set("x") is true, which the
// *string it replaced never was. Optional[Metadata] is the exception, since a
// map is not comparable; a struct carrying one cannot be compared with ==
// either, which is a compile error rather than a silently wrong answer.
type Optional[T any] struct {
	value T
	state optionalState
}

// Set returns an Optional that sends v.
func Set[T any](v T) Optional[T] { return Optional[T]{value: v, state: optionalValue} }

// Null returns an Optional that sends an explicit JSON null, asking the server
// to clear the field. The type argument cannot be inferred from an empty
// argument list, so it is written out: octonomy.Null[string]().
//
// Four fields on this API are cleared by it -- TagUpdate.ParentID,
// TagUpdate.VocabularyID, TagUpdate.Description and VocabularyUpdate.Description.
// Everywhere else it is refused, with one exception that clears nothing:
// ApplicationID on a row that is ALREADY tenant-shared answers 200, because that
// null changes no scope. See Optional and TagUpdate.
func Null[T any]() Optional[T] { return Optional[T]{state: optionalNull} }

// IsZero reports whether this Optional is the omitted one, and is what
// encoding/json calls for the omitzero tag option. It is false for a null: an
// explicit null is something to send, not the absence of one.
func (o Optional[T]) IsZero() bool { return o.state == optionalOmit }

// IsNull reports whether this Optional sends an explicit JSON null.
func (o Optional[T]) IsNull() bool { return o.state == optionalNull }

// Get returns the value and whether this Optional carries one. It reports false
// for both of the other states; a caller that must tell an omitted field from a
// null one separates them with IsZero and IsNull.
func (o Optional[T]) Get() (T, bool) {
	if o.state != optionalValue {
		var zero T
		return zero, false
	}
	return o.value, true
}

// MarshalJSON implements json.Marshaler.
//
// The omitted state has no encoding and returns errOmittedOptional rather than
// falling back to null -- see the type's doc comment. Reaching it means a field
// is missing its omitzero tag, and the error says so.
func (o Optional[T]) MarshalJSON() ([]byte, error) {
	switch o.state {
	case optionalValue:
		return json.Marshal(o.value)
	case optionalNull:
		return []byte("null"), nil
	default:
		return nil, errOmittedOptional
	}
}

// UnmarshalJSON implements json.Unmarshaler, so an Optional survives a round
// trip through JSON.
//
// encoding/json calls this for an explicit null too, which is what makes the
// null state recoverable; a key that is absent from the JSON leaves the field
// untouched, so a zero Optional stays omitted. Without this method the
// unexported fields would decode to nothing at all and report no error, which
// is the silent-zero failure this package refuses (#32, #40).
//
// # One case does not survive, and JSON is why rather than this type
//
// A VALUE whose own encoding is the literal null is indistinguishable on the
// wire from a Null, so it decodes to the null state: Set of a nil Metadata, and
// the same for any nil-able T. What round-trips exactly is an absent key, an
// explicit null, and any value that does not encode as null. The collapse is in
// the right direction for the one field it can reach -- Set of a nil map was
// already the spelling that means nothing useful, and both halves are refused
// by the server for the same reason (see TagUpdate.Metadata).
func (o *Optional[T]) UnmarshalJSON(data []byte) error {
	if bytes.Equal(data, []byte("null")) {
		var zero T
		o.value, o.state = zero, optionalNull
		return nil
	}
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return fmt.Errorf("octonomy: decode optional value: %w", err)
	}
	o.value, o.state = v, optionalValue
	return nil
}
