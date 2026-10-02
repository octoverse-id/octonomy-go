package octonomy

// A REWRITE of main's identityfields_test.go at 5e40964, not a port (#95; the
// disposition table is docs/compat-test-disposition.md). Every mention of main
// in this file means main at that commit. main's guard derives
// the response types from the TYPE ARGUMENT of doData[T] and doList[T], and a
// mechanical transform of it would compile here and find nothing to read:
// this line hands the transport an untyped `&out`. The derivation is
// responseTypes in sourceguard_test.go; what this file keeps from main is the
// rule it checks and the fixtures that hold the checker to it.
//
// One half is new, because one mechanism is: a list's rows() (see
// TestTheRuntimeIdentityTablesMatchTheSource).

import (
	"reflect"
	"strings"
	"testing"
)

// compositeTypes are the response types with no row identity of their own, which
// require their KEYS in UnmarshalJSON instead. The value says whether the
// composite also delivers a resource and therefore carries identityFields too.
//
// This is declared rather than inferred, and that is the point. An earlier
// revision on main let ANY UnmarshalJSON excuse a type from identityFields, so a
// resource that grew a custom decoder for an unrelated reason would have been
// excused from the check that matters -- which is the defect this guard exists
// to prevent, wearing the guard's own clothes. A composite is a fact about the
// contract, so it is written down and verified, not guessed from a method set.
var compositeTypes = map[string]bool{
	"BulkAssignResult":      false,
	"BulkRemoveResult":      false,
	"ResourceReplaceResult": false,
	"TagResolution":         true, // the tag it exists to deliver is a resource
}

// knownResponseTypes and knownListTypes are the floors: a derivation that
// silently stopped finding response types would satisfy every loop below by
// finding nothing at all. The row count is main's, because the set is the same.
const (
	knownResponseTypes = 11
	knownListTypes     = 6
)

// Every response type must be able to refuse a payload that decoded to nothing.
//
// `requireIdentity` (transport.go) type-asserts, and returns nil for a model that
// does not implement the interface:
//
//	resource, ok := v.(identifiedResource)
//	if !ok {
//		return nil
//	}
//
// So a model that skips identityFields() is not checked, and is not checked
// SILENTLY -- `{"data": {"id": null}}` or a renamed id then decodes to a
// zero-valued resource behind a nil error, which is #40.
//
// RECEIVER KIND IS PART OF THE RULE, and on this line for a narrower reason than
// on main. doData passes requireIdentity its `out` -- a *T -- whose method set
// includes a pointer-receiver identityFields, so a single-resource decode would
// find one. The paths that hand requireIdentity a VALUE are the ones that would
// not: a composite's rows (`requireIdentity(out.Assignments[i], …)` in
// assignments.go, `out.Tags[i]` in resources.go), TagResolution's own decoder,
// and a list's rows(), which assigns `rows[i] = l.Data[i]` -- that last one does
// not compile with a pointer receiver, and the first two compile and skip the
// check in silence. A value receiver is in both method sets, so it is the one
// shape every path sees. json.Unmarshal is handed `&out`, so UnmarshalJSON is the
// mirror image and must be on the POINTER.
//
// It carries no build tag: it has to run in the pull request that adds a
// resource.
//
// WHERE THIS CHECK ENDS, since a check whose edge nobody knows is worse than none:
//
//   - It proves a mechanism EXISTS with the right shape, never that it is RIGHT.
//     A model naming the wrong field in identityFields satisfies this and still
//     decodes a zero-valued row; a composite whose UnmarshalJSON requires none of
//     its keys satisfies it too. Both are a reader's job.
//   - It does not reach NESTED required resources. ResourceTag.Tag and
//     TagResolution.Tag count only where the contract marks them `required` AND
//     the route delivers them -- a fact about openapi-v2.yaml, not about Go
//     source.
//   - compositeTypes is a declared list. It is verified against the source in
//     both directions, but nothing proves a type listed there is genuinely a
//     composite rather than a resource someone wanted to excuse.
//   - It reads DIRECT methods and CANONICAL type spellings. A promoted method, a
//     type alias for a response type, or an alias for []byte or error is
//     rejected rather than resolved. That fails closed, no model uses those
//     shapes, and resolving them properly means go/types.
//   - It resolves a destination only in the shapes responseTypes names -- `&out`
//     after ONE declaration of out with a named type, or `&T{}` -- and reports
//     every other as unresolved. A wrapper that takes `out interface{}` and
//     passes it on is the shape that matters, and is reported, not followed.
//   - It matches on BARE type names within this one package, which Go makes
//     unique at package level. A file with a dot import is refused outright.
func TestEveryResponseTypeCanRefuseAnEmptyDecode(t *testing.T) {
	files := parsePackageSource(t)
	methods := declaredMethods(files)
	found, lists, unresolved := responseTypes(files)

	for _, where := range unresolved {
		t.Errorf("%s hands a transport helper a destination this guard cannot resolve to a "+
			"named type. The type it really decodes is invisible here, so it would be checked "+
			"by nothing. Decode into `var out T` and pass &out, or teach responseTypes the shape",
			where)
	}

	for _, name := range stringKeys(found) {
		alsoResource, isComposite := compositeTypes[name]
		if isComposite {
			if why := hasUnmarshalJSON(name, methods); why != "" {
				t.Errorf("%s is declared a composite and %s. A composite has no row identity and "+
					"must refuse a payload missing its keys, in UnmarshalJSON on the POINTER -- "+
					"json.Unmarshal is handed &out", name, why)
			}
			if !alsoResource {
				continue
			}
		}
		if why := hasIdentityFields(name, methods); why != "" {
			kind := "is decoded from a response"
			if isComposite {
				kind = "is a composite that also delivers a resource"
			}
			t.Errorf("%s %s (%s) and %s. requireIdentity is handed a VALUE of the type on every "+
				"path but doData's, so the method must be on the value receiver and return "+
				"[]identityField; anything else is skipped in silence somewhere, and a payload that "+
				"decoded to nothing returns a zero value with a nil error (#40)",
				name, kind, found[name], why)
		}
	}

	for _, name := range sortedKeys(compositeTypes) {
		if _, live := found[name]; !live {
			t.Errorf("compositeTypes lists %q, which is not a response type. Drop it -- an entry "+
				"for a type that does not exist would also excuse a future one that takes the name",
				name)
		}
	}

	if len(found) < knownResponseTypes {
		t.Errorf("found %d response type(s), want at least the %d that exist -- the guard is not "+
			"reading the source. Found: %v", len(found), knownResponseTypes, stringKeys(found))
	}
	if len(lists) < knownListTypes {
		t.Errorf("found %d list type(s), want at least the %d that exist -- the guard is not "+
			"reading doList's destinations. Found: %v", len(lists), knownListTypes, stringKeys(lists))
	}
}

// The derivation reads the destination by POSITION, so the position has to be
// the one the helpers declare. Reading doData's argument 4 instead of 5 would
// resolve the request body -- a TagCreate -- as the response type.
func TestTransportOutMatchesTheHelpersSignatures(t *testing.T) {
	methods := declaredMethods(parsePackageSource(t))
	for _, helper := range []string{"doData", "doList"} {
		fn, ok := methods["Client."+helper]
		if !ok {
			t.Errorf("Client.%s is not declared; transportOut names a helper that no longer exists", helper)
			continue
		}
		if !fn.pointer {
			t.Errorf("Client.%s is not on the pointer receiver the call sites use", helper)
		}
		names := paramNames(fn.decl.Type.Params)
		idx := transportOut[helper]
		if idx >= len(names) || names[idx] != "out" {
			t.Errorf("transportOut[%q] = %d, but Client.%s's parameters are %v: the destination "+
				"is the parameter named out", helper, idx, helper, names)
		}
	}
}

// hasIdentityFields returns "" if the type carries the method in the shape the
// runtime consults, or a description of what is wrong.
func hasIdentityFields(name string, methods methodSet) string {
	fn, ok := methods[name+".identityFields"]
	if !ok {
		return "has no identityFields() method"
	}
	if fn.pointer {
		return "declares identityFields() on the POINTER receiver, which is not in the value's " +
			"method set -- a composite's rows and a list's rows are handed to requireIdentity as " +
			"values, so there this method is never seen"
	}
	if n := len(paramNames(fn.decl.Type.Params)); n != 0 {
		return "declares identityFields() taking arguments, so it does not satisfy identifiedResource"
	}
	if got := resultTypes(fn.decl.Type); len(got) != 1 || got[0] != "[]identityField" {
		return "declares identityFields() returning " + strings.Join(got, ", ") +
			", not []identityField, so it does not satisfy identifiedResource"
	}
	return ""
}

// hasUnmarshalJSON returns "" if the type carries the decoder in the shape
// encoding/json consults, or a description of what is wrong.
func hasUnmarshalJSON(name string, methods methodSet) string {
	fn, ok := methods[name+".UnmarshalJSON"]
	if !ok {
		return "has no UnmarshalJSON method"
	}
	if !fn.pointer {
		return "declares UnmarshalJSON on the VALUE receiver. json.Unmarshal is handed &out, and " +
			"*T's method set does include T's value methods -- so this IS called, on a copy, and " +
			"cannot populate the value being decoded"
	}
	if got := paramTypes(fn.decl.Type); len(got) != 1 || got[0] != "[]byte" {
		return "declares UnmarshalJSON taking " + strings.Join(got, ", ") + ", not []byte"
	}
	if got := resultTypes(fn.decl.Type); len(got) != 1 || got[0] != "error" {
		return "declares UnmarshalJSON returning " + strings.Join(got, ", ") + ", not error"
	}
	return ""
}

// --- the runtime half: rows(), which main does not have ----------------------

// The source check above proves each row type carries identityFields(). On this
// line a LIST also has a mechanism of its own: doList ranges over out.rows(),
// so a list type whose rows() handed back fewer rows than Data holds -- `return
// nil`, say -- would compile, satisfy identifiedList, and exempt every row of
// every page from the identity check. main has no such method to get wrong;
// its doList[T] ranges over the slice itself.
//
// No source reading can prove what rows() returns, so
// TestEveryDecodedModelCarriesAnIdentity (transport_test.go) proves it by
// calling it, over hand-written tables. A hand-written table is the shape that
// rots -- a list type added later with no row in it asserts nothing -- so this
// test holds those tables to the source-derived sets, in both directions.
func TestTheRuntimeIdentityTablesMatchTheSource(t *testing.T) {
	found, lists, _ := responseTypes(parsePackageSource(t))

	wantModels := map[string]bool{}
	wantComposites := map[string]bool{}
	for name := range found {
		if alsoResource, isComposite := compositeTypes[name]; isComposite && !alsoResource {
			wantComposites[name] = true
			continue
		}
		wantModels[name] = true
	}
	wantLists := map[string]bool{}
	for name := range lists {
		wantLists[name] = true
	}

	var models, composites, listTypes []interface{}
	models = identityModels()
	composites = compositeResults()
	for _, l := range identityLists() {
		listTypes = append(listTypes, l.list)
	}

	// Every model is listed as a value AND as a pointer, because the two paths
	// that reach requireIdentity hand it one each.
	values, pointers := typeNames(models, false), typeNames(models, true)
	compareNames(t, "identityModels() values", values, wantModels)
	compareNames(t, "identityModels() pointers", pointers, wantModels)
	compareNames(t, "compositeResults()", typeNames(composites, true), wantComposites)
	compareNames(t, "identityLists()", typeNames(listTypes, true), wantLists)
}

// typeNames returns the named types in values: those that are pointers when
// pointer is set, or those that are not.
func typeNames(values []interface{}, pointer bool) map[string]bool {
	out := map[string]bool{}
	for _, v := range values {
		typ := reflect.TypeOf(v)
		if (typ.Kind() == reflect.Ptr) != pointer {
			continue
		}
		if pointer {
			typ = typ.Elem()
		}
		out[typ.Name()] = true
	}
	return out
}

func compareNames(t *testing.T, table string, got, want map[string]bool) {
	t.Helper()
	for _, name := range sortedKeys(want) {
		if !got[name] {
			t.Errorf("%s has no entry for %s, which the source decodes a response into: nothing "+
				"calls its identity mechanism at runtime", table, name)
		}
	}
	for _, name := range sortedKeys(got) {
		if !want[name] {
			t.Errorf("%s has an entry for %s, which is not a response type the source decodes "+
				"into. A stale row tests a type nothing reaches", table, name)
		}
	}
}

// --- fixtures: the guard's own behaviour ---------------------------------------

// On a correct tree the guard passes whether or not it works, so these drive it
// over synthetic source. Each case is a shape it has to get right, and the ones
// that matter most are where a wrong answer is silent: a response type it fails
// to collect, and a method it credits that the runtime would never call.

func TestResponseTypesCollectsWhatTheTransportDecodes(t *testing.T) {
	const header = "package octonomy\n" +
		"type TagList struct { Data []Tag; Pagination Pagination }\n" +
		"type PtrList struct { Data []*Tag }\n" +
		"type NoData struct { Rows []Tag }\n" +
		"type AliasList = TagList\n"

	for _, tc := range []struct {
		name       string
		src        string // appended to header
		full       string // a whole file, for a case the header cannot precede
		want       []string
		wantLists  []string
		unresolved int
	}{
		{
			name: "doData into a declared local",
			src: `func (s *S) Get(ctx context.Context) (*Tag, error) {
				var out Tag
				if err := s.client.doData(ctx, http.MethodGet, "/t", nil, nil, &out); err != nil { return nil, err }
				return &out, nil
			}`,
			want: []string{"Tag"},
		},
		{
			// The ROW is what counts, not the envelope: main's doList[Tag] names
			// the row, and a guard that collected TagList here would demand an
			// identityFields() of the envelope and never look at Tag.
			name: "doList into a list envelope names the row",
			src: `func (s *S) List(ctx context.Context) (*TagList, error) {
				var out TagList
				err := s.client.doList(ctx, http.MethodGet, "/t", nil, &out)
				return &out, err
			}`,
			want: []string{"Tag"}, wantLists: []string{"TagList"},
		},
		{
			// The argument POSITION is the rule. doData's request body sits one
			// place before its destination; reading it would resolve TagCreate.
			name: "the body is not the destination",
			src: `func (s *S) Create(ctx context.Context, in TagCreate) (*Tag, error) {
				var out Tag
				err := s.client.doData(ctx, http.MethodPost, "/t", nil, &in, &out)
				return &out, err
			}`,
			want: []string{"Tag"},
		},
		{
			name: "a short variable declaration from a literal",
			src:  `func (s *S) Get(ctx context.Context) error { out := Vocabulary{}; return s.client.doData(ctx, "GET", "/v", nil, nil, &out) }`,
			want: []string{"Vocabulary"},
		},
		{
			name: "the address of a literal",
			src:  `func (s *S) Get(ctx context.Context) error { return s.client.doData(ctx, "GET", "/v", nil, nil, &Vocabulary{}) }`,
			want: []string{"Vocabulary"},
		},
		{
			name: "parentheses do not hide the call or the destination",
			src:  `func (s *S) Get(ctx context.Context) error { var out Tag; return (s.client).doData(ctx, "GET", "/t", nil, nil, (&out)) }`,
			want: []string{"Tag"},
		},
		{
			// A WRAPPER. The concrete type reaches the helper from the wrapper's
			// own call sites, which this guard does not follow -- so it must be
			// reported, not skipped. This is main's "a wrapper generic over T" in
			// this dialect: interface{} where main has a type parameter.
			name: "a destination handed through from a parameter",
			src: `func (c *Client) get(ctx context.Context, path string, out interface{}) error {
				return c.doData(ctx, http.MethodGet, path, nil, nil, out)
			}`,
			unresolved: 1,
		},
		{
			name:       "the address of a parameter",
			src:        `func (c *Client) get(ctx context.Context, out Tag) error { return c.doData(ctx, "GET", "/t", nil, nil, &out) }`,
			unresolved: 1,
		},
		{
			// Two declarations of the destination's name. Which one `&out`
			// refers to is a scope question this guard does not answer, and
			// answering it wrong credits a type the runtime never decodes.
			name: "a shadowed destination is refused",
			src: `func (s *S) Get(ctx context.Context) error {
				var out Tag
				if true {
					var out Vocabulary
					return s.client.doData(ctx, "GET", "/v", nil, nil, &out)
				}
				return nil
			}`,
			unresolved: 1,
		},
		{
			name: "a closure's own parameter of that name counts as a second declaration",
			src: `func (s *S) Get(ctx context.Context) error {
				var out Tag
				f := func(out *Vocabulary) {}
				_ = f
				return s.client.doData(ctx, "GET", "/v", nil, nil, &out)
			}`,
			unresolved: 1,
		},
		{
			// A method VALUE hands the helper on without this guard seeing what
			// it decodes. Matching only `x.doData(…)` in callee position lost the
			// type entirely; a reference anywhere else is reported.
			name:       "a transport helper bound to a variable",
			src:        `func (s *S) Get(ctx context.Context) error { decode := s.client.doData; var out Tag; return decode(ctx, "GET", "/t", nil, nil, &out) }`,
			unresolved: 1,
		},
		{
			name:       "a method expression at package level",
			src:        `var decode = (*Client).doList`,
			unresolved: 1,
		},
		{
			// A method expression CALLED takes the client as argument 0, so the
			// destination is one place later than transportOut says. Reading the
			// usual position credited the request body -- &request, a Tag --
			// while the runtime decoded a Widget, with nothing reported.
			name: "an invoked method expression is refused",
			src: `func (s *S) Create(ctx context.Context) error {
				var request Tag
				var out Widget
				return (*Client).doData(s.client, ctx, http.MethodPost, "/w", nil, &request, &out)
			}`,
			unresolved: 1,
		},
		{
			name: "a method expression through a type alias is refused",
			src: `type C = *Client
			func (s *S) List(ctx context.Context) error { var out TagList; return C.doList(s.client, ctx, "GET", "/t", nil, &out) }`,
			unresolved: 1,
		},
		{
			name:       "a destination that is not an address",
			src:        `func (s *S) Get(ctx context.Context) error { out := new(Tag); return s.client.doData(ctx, "GET", "/t", nil, nil, out) }`,
			unresolved: 1,
		},
		{
			name:       "too few arguments to have a destination",
			src:        `func (s *S) Get(ctx context.Context) error { return s.client.doList(ctx, "GET", "/t") }`,
			unresolved: 1,
		},
		{
			name:       "a list whose rows are pointers",
			src:        `func (s *S) List(ctx context.Context) error { var out PtrList; return s.client.doList(ctx, "GET", "/t", nil, &out) }`,
			unresolved: 1,
		},
		{
			name:       "a list with no Data field",
			src:        `func (s *S) List(ctx context.Context) error { var out NoData; return s.client.doList(ctx, "GET", "/t", nil, &out) }`,
			unresolved: 1,
		},
		{
			name:       "a list type this package does not declare",
			src:        `func (s *S) List(ctx context.Context) error { var out Elsewhere; return s.client.doList(ctx, "GET", "/t", nil, &out) }`,
			unresolved: 1,
		},
		{
			name:       "an alias for a list envelope",
			src:        `func (s *S) List(ctx context.Context) error { var out AliasList; return s.client.doList(ctx, "GET", "/t", nil, &out) }`,
			unresolved: 1,
		},
		{
			// A dot import puts another package's exported names into scope under
			// their bare names -- `. "debug/dwarf"` brings a Tag -- which is
			// exactly what this guard resolves by. The file is refused rather
			// than guessed at.
			name:       "a dot-imported file is refused",
			full:       "package octonomy\n" + `import . "debug/dwarf"` + "\n" + `func (s *S) Get(ctx context.Context) error { var out Tag; return s.client.doData(ctx, "GET", "/t", nil, nil, &out) }`,
			unresolved: 1,
		},
		{
			name: "an unrelated call is not a transport call",
			src:  `func (s *S) Get(ctx context.Context) error { var out Tag; return decodeJSON(nil, &out) }`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := header + tc.src
			if tc.full != "" {
				src = tc.full
			}
			found, lists, unresolved := responseTypes(parseFixture(t, src))
			if len(unresolved) != tc.unresolved {
				t.Errorf("unresolved = %d, want %d (%v)", len(unresolved), tc.unresolved, unresolved)
			}
			if got := stringKeys(found); strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("collected %v, want %v", got, tc.want)
			}
			if got := stringKeys(lists); strings.Join(got, ",") != strings.Join(tc.wantLists, ",") {
				t.Errorf("lists %v, want %v", got, tc.wantLists)
			}
		})
	}
}

// The runtime consults a VALUE's method set for identityFields on every path but
// doData's, and a POINTER's for UnmarshalJSON. A guard that credited either
// receiver for either method certified shapes that are never called -- which is
// #40 reintroduced by the check written to prevent it.
func TestIdentityMechanismsMatchWhatTheRuntimeCalls(t *testing.T) {
	const header = "package octonomy\n"

	for _, tc := range []struct {
		name, typ, src string
		wantIdentityOK bool
		wantDecoderOK  bool
	}{
		{
			name: "a value receiver is what requireIdentity sees", typ: "Tag",
			src:            `func (t Tag) identityFields() []identityField { return nil }`,
			wantIdentityOK: true,
		},
		{
			// A composite's rows and a list's rows are handed over as values, so
			// this method is not in the method set asserted against there. It
			// compiles and reads correctly.
			name: "a pointer receiver is skipped at runtime", typ: "Tag",
			src:            `func (t *Tag) identityFields() []identityField { return nil }`,
			wantIdentityOK: false,
		},
		{
			name: "a parenthesized pointer receiver is still a pointer", typ: "Tag",
			src:            `func (t (*Tag)) identityFields() []identityField { return nil }`,
			wantIdentityOK: false,
		},
		{
			name: "the wrong return type does not satisfy the interface", typ: "Widget",
			src:            `func (w Widget) identityFields() string { return "" }`,
			wantIdentityOK: false,
		},
		{
			name: "a method taking arguments does not satisfy it either", typ: "Widget",
			src:            `func (w Widget) identityFields(deep bool) []identityField { return nil }`,
			wantIdentityOK: false,
		},
		{
			name: "a pointer receiver is what json.Unmarshal calls", typ: "BulkAssignResult",
			src:           `func (r *BulkAssignResult) UnmarshalJSON(data []byte) error { return nil }`,
			wantDecoderOK: true,
		},
		{
			// *T's method set DOES include T's value methods, so json.Unmarshal
			// calls this one -- on a copy. It cannot populate the value being
			// decoded, which is the same silent nothing as not having it.
			name: "a value-receiver decoder cannot populate the value", typ: "BulkAssignResult",
			src:           `func (r BulkAssignResult) UnmarshalJSON(data []byte) error { return nil }`,
			wantDecoderOK: false,
		},
		{
			name: "a decoder with the wrong result is not json.Unmarshaler", typ: "Result",
			src:           `func (r *Result) UnmarshalJSON(data []byte) bool { return true }`,
			wantDecoderOK: false,
		},
		{
			name: "a method of the right name on another type does not count", typ: "Widget",
			src: `func (t Tag) identityFields() []identityField { return nil }`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			methods := declaredMethods(parseFixture(t, header+tc.src))
			if got := hasIdentityFields(tc.typ, methods) == ""; got != tc.wantIdentityOK {
				t.Errorf("hasIdentityFields(%s) ok = %v, want %v (%s)",
					tc.typ, got, tc.wantIdentityOK, hasIdentityFields(tc.typ, methods))
			}
			if got := hasUnmarshalJSON(tc.typ, methods) == ""; got != tc.wantDecoderOK {
				t.Errorf("hasUnmarshalJSON(%s) ok = %v, want %v (%s)",
					tc.typ, got, tc.wantDecoderOK, hasUnmarshalJSON(tc.typ, methods))
			}
		})
	}
}

// compareNames is only as good as typeNames, which has to tell a pointer entry
// from a value entry and name each by its element type.
func TestTypeNamesSplitsValuesFromPointers(t *testing.T) {
	values := []interface{}{Tag{}, &Tag{}, Vocabulary{}, &TagList{}}
	if got := sortedKeys(typeNames(values, false)); strings.Join(got, ",") != "Tag,Vocabulary" {
		t.Errorf("values = %v, want [Tag Vocabulary]", got)
	}
	if got := sortedKeys(typeNames(values, true)); strings.Join(got, ",") != "Tag,TagList" {
		t.Errorf("pointers = %v, want [Tag TagList]", got)
	}
}
