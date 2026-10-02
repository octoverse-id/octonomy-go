package octonomy

// Tests for the transport this line gained in #91. Most are ported from main's
// octonomy_test.go at 5e40964 -- the request-id, base-URL, envelope-contents
// and identity cases -- narrowed, when #91 ported them, to the two resources
// this tree then had, and widened by #94 to the resources it ported. The
// ErrUnreachable cases are new: main asserts the sentinel and its cause only on
// a health probe, and here the wrap is a hand-written type whose two halves can
// regress separately on the versioned path too.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// writeRaw answers with body verbatim, for shapes writeJSON cannot produce.
func writeRaw(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// --- WithRequestID ----------------------------------------------------------

// Both halves: a caller-supplied correlation id reaches the wire, and NOTHING is
// sent when the caller supplies none.
//
// The absent case is asserted on the header MAP, not with Get: a
// present-but-empty header reads as "" through Get and would pass a test that
// only compared strings, while on the wire it is a header the server sees,
// treats as absent, and mints over -- the one shape this option must never
// produce. (Header.Values would say this directly, and needs Go 1.14.)
func TestDo_RequestIDHeader(t *testing.T) {
	tests := []struct {
		name      string
		version   APIVersion
		opts      []RequestOption
		wantID    string
		wantActor string
	}{
		{"absent by default", "", nil, "", ""},
		{"sent when supplied", "", []RequestOption{WithRequestID("req-abc")}, "req-abc", ""},
		{"sent on v2 too", APIV2, []RequestOption{WithRequestID("req-abc")}, "req-abc", ""},
		{"composes with WithActor", "", []RequestOption{WithActor("svc-catalog"), WithRequestID("req-abc")}, "req-abc", "svc-catalog"},
		{"composes in the other order", "", []RequestOption{WithRequestID("req-abc"), WithActor("svc-catalog")}, "req-abc", "svc-catalog"},
		// Not a scope axis: the axes that refuse last-wins are the ones where a
		// silent override reads the wrong tenant's rows. Overriding an id held in
		// a shared []RequestOption is the same act WithActor already allows.
		{"repeating it overrides", "", []RequestOption{WithRequestID("req-first"), WithRequestID("req-second")}, "req-second", ""},
		{"survives the scope options", APIV2, []RequestOption{WithRequestID("req-abc"), WithNamespace("merchant", "acme-store"), WithApplication("storefront")}, "req-abc", ""},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			c, cleanup := newVersionedTestClient(t, tt.version, func(w http.ResponseWriter, r *http.Request) {
				values := r.Header[http.CanonicalHeaderKey(requestIDHeader)]
				switch {
				case tt.wantID == "" && len(values) != 0:
					t.Errorf("%s = %q, want the header to be absent entirely so the server mints its own", requestIDHeader, values)
				case tt.wantID != "" && (len(values) != 1 || values[0] != tt.wantID):
					t.Errorf("%s = %q, want exactly [%q]", requestIDHeader, values, tt.wantID)
				}
				if got := r.Header.Get("X-Actor-ID"); got != tt.wantActor {
					t.Errorf("X-Actor-ID = %q, want %q: the two options must compose, not overwrite", got, tt.wantActor)
				}
				writeData(t, w, http.StatusOK, Tag{ID: "abc"})
			})
			defer cleanup()

			tag, err := c.Tags.Get(context.Background(), "abc", tt.opts...)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if tag.ID != "abc" {
				t.Errorf("tag.ID = %q, want abc", tag.ID)
			}
		})
	}
}

// The option fails LOCALLY, with its own name in the message, rather than
// letting the value reach the wire.
//
// The handler fails the test if it is ever reached, because where the failure
// happens is the point. A control byte would otherwise be refused by net/http
// inside httpClient.Do, and doRaw wraps everything from there in ErrUnreachable
// -- a sentinel that promises no server answered -- so a newline on the end of
// an id read from a file would be reported as an unreachable server. A byte
// above 0x7e would not fail at all: it would be decoded latin-1 by the server
// and stored as mojibake, correlating nothing, with a 2xx and no error.
func TestWithRequestID_RejectsUnusableValues(t *testing.T) {
	tests := []struct {
		name string
		id   string
	}{
		{"empty", ""},
		{"whitespace only", "   "},
		{"trailing newline", "req-abc\n"},
		{"embedded carriage return", "req\rabc"},
		{"embedded tab", "req\tabc"},
		{"nul byte", "req\x00abc"},
		{"del", "req\x7fabc"},
		{"non-ascii", "req-café"},
		// Printable, so the ASCII loop accepts it -- and then net/http trims it
		// while writing the header, so the server would store a string the
		// caller never logged.
		{"leading space", " req-abc"},
		{"trailing space", "req-abc "},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			c, cleanup := newUnreachableClient(t, "")
			defer cleanup()
			_, err := c.Tags.Get(context.Background(), "abc", WithRequestID(tt.id))
			if err == nil {
				t.Fatalf("WithRequestID(%q) was accepted, want a local failure", tt.id)
			}
			if !strings.Contains(err.Error(), "WithRequestID") {
				t.Errorf("error = %v, want it to name WithRequestID", err)
			}
			if errors.Is(err, ErrUnreachable) {
				t.Errorf("a refused id read as an unreachable server: %v", err)
			}
		})
	}
}

// The option sits inside requestConfig's first-failure-wins contract: an id
// that cannot be sent is still not the mistake to report when the caller
// already made an earlier one.
func TestWithRequestID_DoesNotMaskAnEarlierOptionFailure(t *testing.T) {
	c, cleanup := newUnreachableClient(t, APIV2)
	defer cleanup()
	_, err := c.Tags.Get(context.Background(), "abc",
		WithApplication(" "),
		WithRequestID("req\n"),
	)
	if err == nil {
		t.Fatal("both options were accepted, want the first failure reported")
	}
	if !strings.Contains(err.Error(), "WithApplication") {
		t.Errorf("error = %v, want the FIRST failure (WithApplication), not the request id", err)
	}
}

// The library returns errors; it does not panic. v1.0.0 called every option
// unconditionally, so a nil one -- what a caller assembling a slice
// conditionally produces -- took the process down.
func TestDoRaw_NilOptionIsAnErrorNotAPanic(t *testing.T) {
	requests := 0
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		requests++
		writeData(t, w, http.StatusOK, Tag{ID: "tag_1"})
	})
	defer cleanup()

	_, err := c.Tags.Get(context.Background(), "tag_1", WithActor("a"), nil)
	if err == nil {
		t.Fatal("expected an error for a nil RequestOption")
	}
	if !strings.Contains(err.Error(), "RequestOption 1") {
		t.Errorf("the error should name which option is nil: %v", err)
	}
	if requests != 0 {
		t.Errorf("the refusal must happen before anything is sent, got %d request(s)", requests)
	}
}

// --- ErrUnreachable ---------------------------------------------------------

// Both halves of the wrap must hold on the versioned API: the sentinel AND the
// cause. fmt.Errorf("%w: %w", ...) -- the /v2 module's spelling -- yields
// neither before Go 1.20, and "%w: %v" yields one; either regression keeps half
// of these assertions passing, which is why both are made on every case.
func TestDoRaw_TransportFailureKeepsTheSentinelAndTheCause(t *testing.T) {
	t.Run("cancelled context", func(t *testing.T) {
		c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			writeData(t, w, http.StatusOK, Tag{ID: "abc"})
		})
		defer cleanup()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := c.Tags.Get(ctx, "abc")
		assertUnreachable(t, err)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("errors.Is(err, context.Canceled) = false: %v", err)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("a cancellation also matched DeadlineExceeded: %v", err)
		}
	})

	t.Run("connection refused", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		baseURL := srv.URL
		srv.Close()

		c, err := New(Config{BaseURL: baseURL, Token: "t", TenantID: "acme", APIVersion: APIV2})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		err = c.Tags.Delete(context.Background(), "abc")
		assertUnreachable(t, err)
		var opErr *net.OpError
		if !errors.As(err, &opErr) {
			t.Errorf("errors.As(err, *net.OpError) = false: %v", err)
		}
		if errors.Is(err, context.Canceled) {
			t.Errorf("a refused connection matched context.Canceled: %v", err)
		}
	})

	// The message is the one these errors have always carried, so a caller
	// matching on text in logs is not broken by the change of type.
	t.Run("message keeps its prefix", func(t *testing.T) {
		err := error(&unreachableError{cause: errors.New("dial tcp: boom")})
		if got, want := err.Error(), "octonomy: request failed: dial tcp: boom"; got != want {
			t.Errorf("Error() = %q, want %q", got, want)
		}
		if errors.Is(err, ErrResponseTooLarge) {
			t.Error("the wrapper matched a sentinel it does not stand for")
		}
	})
}

// --- Paths ------------------------------------------------------------------

// A base URL with a path prefix, which a deployment behind a reverse proxy
// has, and an id that needs escaping. Client.joinPath keeps url.URL's decoded
// Path and escaped RawPath in step; the first release assigned the escaped
// path to Path alone, so String() escaped it twice and "tag 1" reached the
// server as the literal "tag%201".
func TestBaseURL_WithPathPrefix(t *testing.T) {
	tests := []struct {
		name        string
		version     APIVersion
		suffix      string
		id          string
		wantEscaped string
	}{
		{"plain id", "", "/gateway", "tag_1", "/gateway/api/v1/tags/tag_1"},
		{"id needing escaping", "", "/gateway", "tag 1", "/gateway/api/v1/tags/tag%201"},
		{"id carrying a percent", "", "", "50%off", "/api/v1/tags/50%25off"},
		{"id carrying a slash", "", "", "a/b", "/api/v1/tags/a%2Fb"},
		{"nested prefix", APIV2, "/a/b", "tag_1", "/a/b/api/v2/tags/tag_1"},
		{"trailing slash on the base", "", "/gateway/", "tag_1", "/gateway/api/v1/tags/tag_1"},
		{"no prefix", APIV2, "", "tag 1", "/api/v2/tags/tag%201"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.URL.EscapedPath(); got != tt.wantEscaped {
					t.Errorf("escaped path = %q, want %q", got, tt.wantEscaped)
				}
				writeData(t, w, http.StatusOK, Tag{ID: "tag_1"})
			}))
			defer srv.Close()

			c, err := New(Config{BaseURL: srv.URL + tt.suffix, Token: "t", TenantID: "tenant-1", APIVersion: tt.version})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if _, err := c.Tags.Get(context.Background(), tt.id); err != nil {
				t.Fatalf("Get: %v", err)
			}
		})
	}
}

// --- Envelope CONTENTS and identity -----------------------------------------
//
// One level in from the envelope tests in octonomy_test.go. {"data": {}} is well
// formed and carries the key, so it passes decodeEnvelope; encoding/json then
// fills in nothing and the call returns a zero-valued struct with a nil error.
// The id makes that obvious to a caller who reads it, but an empty Slug or a nil
// Metadata is a plausible-looking blank instead.

func singleResourceRoutes() []struct {
	name string
	call func(*Client) error
} {
	return []struct {
		name string
		call func(*Client) error
	}{
		{"tags.get", func(c *Client) error {
			_, err := c.Tags.Get(context.Background(), "tag_1")
			return err
		}},
		{"tags.create", func(c *Client) error {
			_, err := c.Tags.Create(context.Background(), TagCreate{Name: "N", Slug: "n", Type: "topic"})
			return err
		}},
		{"tags.update", func(c *Client) error {
			_, err := c.Tags.Update(context.Background(), "tag_1", TagUpdate{Name: String("N")})
			return err
		}},
		{"vocabularies.get", func(c *Client) error {
			_, err := c.Vocabularies.Get(context.Background(), "voc_1")
			return err
		}},
		{"vocabularies.create", func(c *Client) error {
			_, err := c.Vocabularies.Create(context.Background(), VocabularyCreate{Name: "N", Slug: "n"})
			return err
		}},
		{"vocabularies.update", func(c *Client) error {
			_, err := c.Vocabularies.Update(context.Background(), "voc_1", VocabularyUpdate{Name: String("N")})
			return err
		}},
		{"aliases.get", func(c *Client) error {
			_, err := c.Aliases.Get(context.Background(), "alias_1")
			return err
		}},
		{"aliases.create", func(c *Client) error {
			_, err := c.Aliases.Create(context.Background(), TagAliasCreate{TagID: "tag_1", Name: "N", Slug: "n"})
			return err
		}},
		{"aliases.update", func(c *Client) error {
			_, err := c.Aliases.Update(context.Background(), "alias_1", TagAliasUpdate{Name: String("N")})
			return err
		}},
		{"assignments.create", func(c *Client) error {
			_, err := c.Assignments.Create(context.Background(), AssignmentCreate{
				ApplicationID: "commerce", TagID: String("tag_1"), ResourceType: "order", ResourceID: "ord_1",
			})
			return err
		}},
	}
}

// listRoutes is every list method, each paired with the wire name of the field
// that identifies its rows -- "id" on most, but a ResourceTag has no id of its
// own and a TagResource names its resource instead.
func listRoutes() []struct {
	name     string
	identity string
	call     func(*Client) error
} {
	return []struct {
		name     string
		identity string
		call     func(*Client) error
	}{
		{"tags", "id", func(c *Client) error {
			_, err := c.Tags.List(context.Background(), nil)
			return err
		}},
		{"vocabularies", "id", func(c *Client) error {
			_, err := c.Vocabularies.List(context.Background(), nil)
			return err
		}},
		{"aliases", "id", func(c *Client) error {
			_, err := c.Aliases.List(context.Background(), nil)
			return err
		}},
		{"tags.aliases", "id", func(c *Client) error {
			_, err := c.Tags.ListAliases(context.Background(), "tag_1", nil)
			return err
		}},
		{"resources.tags", "assignment_id", func(c *Client) error {
			_, err := c.Resources.ListTags(context.Background(), "order", "ord_1", &ResourceListTagsParams{ApplicationID: String("commerce")})
			return err
		}},
		{"tags.resources", "resource_id", func(c *Client) error {
			_, err := c.Tags.ListResources(context.Background(), "tag_1", nil)
			return err
		}},
		{"audit-logs", "id", func(c *Client) error {
			_, err := c.AuditLogs.List(context.Background(), nil)
			return err
		}},
		{"tags.audit-logs", "id", func(c *Client) error {
			_, err := c.Tags.ListAuditLogs(context.Background(), "tag_1", nil)
			return err
		}},
		{"resources.audit-logs", "id", func(c *Client) error {
			_, err := c.Resources.ListAuditLogs(context.Background(), "order", "ord_1", nil)
			return err
		}},
	}
}

func TestDoData_ContentsThatWouldDecodeToAZeroValue(t *testing.T) {
	bodies := []struct {
		name string
		body string
		want string // a fragment the message must name
	}{
		{"empty data object", `{"data":{}}`, "empty object"},
		{"data is null", `{"data":null}`, "null"},
		{"data is a string", `{"data":"tag_1"}`, "not a resource object"},
		{"data is a number", `{"data":0}`, "not a resource object"},
		{"data is an array", `{"data":[{"id":"tag_1"}]}`, "is an array"},
		// Whitespace must not smuggle an empty object past a byte check.
		{"empty data object with whitespace", `{"data": {  }  }`, "empty object"},
	}
	for _, route := range singleResourceRoutes() {
		for _, body := range bodies {
			route, body := route, body
			t.Run(route.name+"/"+body.name, func(t *testing.T) {
				c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
					writeRaw(w, http.StatusOK, body.body)
				})
				defer cleanup()

				err := route.call(c)
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
				if !strings.Contains(err.Error(), body.want) {
					t.Errorf("error should name the shape (%q), got: %v", body.want, err)
				}
				if !strings.Contains(err.Error(), "data") {
					t.Errorf("error should name the position, got: %v", err)
				}
			})
		}
	}
}

// The other half, and the half the shape check cannot do. A NON-empty object is
// still a well-formed object: {"id": null} and {"wrong": true} both decode to a
// zero-valued resource with a nil error, because encoding/json ignores a null
// for a string field and skips unknown keys. Each model names the field that
// identifies its row, and a blank one after a successful decode means the bytes
// did not carry it.
func TestDoData_ContentsThatDecodeToABlankIdentity(t *testing.T) {
	bodies := []struct {
		name string
		body string
	}{
		{"id is null", `{"data":{"id":null,"slug":"featured"}}`},
		{"id is empty", `{"data":{"id":"","slug":"featured"}}`},
		{"id renamed", `{"data":{"identifier":"tag_1","slug":"featured"}}`},
		{"only unknown fields", `{"data":{"wrong":true}}`},
	}
	for _, route := range singleResourceRoutes() {
		for _, body := range bodies {
			route, body := route, body
			t.Run(route.name+"/"+body.name, func(t *testing.T) {
				c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
					writeRaw(w, http.StatusOK, body.body)
				})
				defer cleanup()

				err := route.call(c)
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
				if !strings.Contains(err.Error(), `no "id"`) {
					t.Errorf("error should name the missing identity, got: %v", err)
				}
			})
		}
	}
}

// A returned resource is nil alongside the error, never a half-filled one.
func TestDoData_RefusalReturnsNoResource(t *testing.T) {
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeRaw(w, http.StatusOK, `{"data":{"id":null,"slug":"featured"}}`)
	})
	defer cleanup()

	tag, err := c.Tags.Get(context.Background(), "tag_1")
	if err == nil || tag != nil {
		t.Errorf("Get = (%+v, %v), want (nil, error)", tag, err)
	}
	vocab, err := c.Vocabularies.Get(context.Background(), "voc_1")
	if err == nil || vocab != nil {
		t.Errorf("Vocabularies.Get = (%+v, %v), want (nil, error)", vocab, err)
	}
	alias, err := c.Aliases.Get(context.Background(), "alias_1")
	if err == nil || alias != nil {
		t.Errorf("Aliases.Get = (%+v, %v), want (nil, error)", alias, err)
	}
}

// A blank row inside an otherwise good page is worse than a blank single
// resource, not better: the length is right, the pagination is right, and one
// row among fifty is not something a caller inspects. The message has to name
// the index, because nothing else in the response points at the bad row.
//
// Each list names its own identity field, so the bodies are built per list: a
// good row has to carry the right field to be good, and a ResourceTag row is
// only good with its nested tag's id too.
func TestDoList_RejectsARowThatWouldBeZeroValued(t *testing.T) {
	for _, list := range listRoutes() {
		good := fmt.Sprintf(`{%q:"row_1","tag":{"id":"tag_1"}}`, list.identity)
		renamed := `{"identifier":"row_2","tag":{"id":"tag_1"}}`
		bodies := []struct {
			name string
			body string
			want string
		}{
			{"null element", `{"data":[null],"pagination":{"limit":50}}`, "element 0 is null"},
			{"empty object element", `{"data":[{}],"pagination":{"limit":50}}`, "element 0 is an empty object"},
			{
				"a good row does not excuse a bad one",
				`{"data":[` + good + `,{}],"pagination":{"limit":50}}`,
				"element 1 is an empty object",
			},
			{"nested array element", `{"data":[[` + good + `]],"pagination":{"limit":50}}`, "element 0 is an array"},
			{"scalar element", `{"data":["row_1"],"pagination":{"limit":50}}`, "element 0 is not a resource object"},
			{"null identity", fmt.Sprintf(`{"data":[{%q:null,"tag":{"id":"tag_1"}}],"pagination":{"limit":50}}`, list.identity), fmt.Sprintf(`element 0 decoded with no %q`, list.identity)},
			{
				"good row then a renamed identity",
				`{"data":[` + good + `,` + renamed + `],"pagination":{"limit":50}}`,
				fmt.Sprintf(`element 1 decoded with no %q`, list.identity),
			},
		}
		for _, body := range bodies {
			list, body := list, body
			t.Run(list.name+"/"+body.name, func(t *testing.T) {
				c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
					writeRaw(w, http.StatusOK, body.body)
				})
				defer cleanup()

				err := list.call(c)
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
				if !strings.Contains(err.Error(), body.want) {
					t.Errorf("error should say %q, got: %v", body.want, err)
				}
			})
		}
	}
}

// The converse, so the identity check cannot be an over-reach: rows that carry
// their id decode, and "data": null still decodes to a nil slice, as it did in
// v1.0.0 -- a caller may be relying on which spelling of an empty page it gets.
func TestDoList_GoodRowsAndANullPageStillDecode(t *testing.T) {
	t.Run("rows with ids", func(t *testing.T) {
		c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			writeRaw(w, http.StatusOK, `{"data":[{"id":"t1"},{"id":"t2"}],"pagination":{"limit":50,"count":2}}`)
		})
		defer cleanup()
		page, err := c.Tags.List(context.Background(), nil)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(page.Data) != 2 || page.Data[0].ID != "t1" || page.Data[1].ID != "t2" {
			t.Errorf("unexpected page: %+v", page.Data)
		}
	})

	t.Run("null data", func(t *testing.T) {
		c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			writeRaw(w, http.StatusOK, `{"data":null,"pagination":{"limit":50,"count":0}}`)
		})
		defer cleanup()
		page, err := c.Vocabularies.List(context.Background(), nil)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if page.Data != nil {
			t.Errorf("Data = %#v, want nil: a null page decodes as it did in v1.0.0", page.Data)
		}
	})
}

// Every model the transport decodes carries an identity, and every list type
// hands its rows back. A model that stopped implementing identifiedResource
// would be skipped by requireIdentity in silence, so this asserts the
// interfaces directly rather than through a decode.
func TestEveryDecodedModelCarriesAnIdentity(t *testing.T) {
	models := []interface{}{
		Tag{}, &Tag{}, Vocabulary{}, &Vocabulary{}, TagAlias{}, &TagAlias{},
		Assignment{}, &Assignment{}, ResourceTag{}, &ResourceTag{}, TagResource{}, &TagResource{},
		AuditLog{}, &AuditLog{}, TagResolution{}, &TagResolution{},
	}
	for _, m := range models {
		if _, ok := m.(identifiedResource); !ok {
			t.Errorf("%T does not implement identifiedResource, so a blank id would decode with a nil error", m)
		}
	}
	// The composites are the converse: they carry no identity of their own and
	// require their keys in UnmarshalJSON instead, so requireIdentity must skip
	// them. One that grew an identityFields() naming a field it does not have
	// would turn every real answer into an error.
	composites := []interface{}{&BulkAssignResult{}, &BulkRemoveResult{}, &ResourceReplaceResult{}}
	for _, m := range composites {
		if _, ok := m.(identifiedResource); ok {
			t.Errorf("%T implements identifiedResource; a composite requires its keys in its own decoder", m)
		}
	}
	lists := []struct {
		list identifiedList
		want []string
	}{
		{&TagList{Data: []Tag{{ID: "a"}, {ID: "b"}}}, []string{"a", "b"}},
		{&VocabularyList{Data: []Vocabulary{{ID: "c"}}}, []string{"c"}},
		{&TagAliasList{Data: []TagAlias{{ID: "d"}}}, []string{"d"}},
		{&ResourceTagList{Data: []ResourceTag{{AssignmentID: "e"}}}, []string{"e"}},
		{&TagResourceList{Data: []TagResource{{ResourceID: "f"}}}, []string{"f"}},
		{&AuditLogList{Data: []AuditLog{{ID: "g"}, {ID: "h"}}}, []string{"g", "h"}},
	}
	for _, l := range lists {
		var got []string
		for _, row := range l.list.rows() {
			got = append(got, row.identityFields()[0].value)
		}
		if fmt.Sprint(got) != fmt.Sprint(l.want) {
			t.Errorf("%T.rows() identities = %v, want %v", l.list, got, l.want)
		}
	}
}

// --- Shapes the first release accepted, and main refuses ---------------------

// A list whose pagination block is present but unusable reads as "one page,
// nothing after it" to a caller paging on Count. Limit is never below 1 in a
// real response, so it is the field that tells the two apart.
func TestDoList_RefusesAnUnusablePaginationBlock(t *testing.T) {
	lists := listRoutes()
	bodies := []struct {
		name string
		body string
		want string
	}{
		{"empty pagination object", `{"data":[],"pagination":{}}`, "limit=0"},
		{"null data and empty pagination", `{"data":null,"pagination":{}}`, "limit=0"},
		{"zero limit", `{"data":[],"pagination":{"limit":0,"count":0}}`, "limit=0"},
		{"negative limit", `{"data":[],"pagination":{"limit":-1}}`, "limit=-1"},
		{"pagination is not an object", `{"data":[],"pagination":"50"}`, "pagination"},
	}
	for _, list := range lists {
		for _, body := range bodies {
			list, body := list, body
			t.Run(list.name+"/"+body.name, func(t *testing.T) {
				c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
					writeRaw(w, http.StatusOK, body.body)
				})
				defer cleanup()
				err := list.call(c)
				if err == nil {
					t.Fatal("expected an error, got a page")
				}
				if !strings.Contains(err.Error(), body.want) {
					t.Errorf("error should mention %q, got: %v", body.want, err)
				}
			})
		}
	}
}

// DELETE is answered with 204 and no body on every resource. Anything else --
// a payload, or a 2xx that is not Octonomy's answer -- is not evidence that the
// row was deactivated, and the first release reported it as success.
func TestDo_RefusesA2xxThatIsNotTheDeleteAnswer(t *testing.T) {
	deletes := []struct {
		name string
		call func(*Client) error
	}{
		{"tags", func(c *Client) error { return c.Tags.Delete(context.Background(), "tag_1") }},
		{"vocabularies", func(c *Client) error { return c.Vocabularies.Delete(context.Background(), "voc_1") }},
		{"aliases", func(c *Client) error { return c.Aliases.Delete(context.Background(), "alias_1") }},
		// A DELETE that carries a body, and the one real delete among them: the
		// 204 assertion has to hold on it as well.
		{"assignments.remove", func(c *Client) error {
			return c.Assignments.Remove(context.Background(), AssignmentRemove{
				ApplicationID: "commerce", TagID: "tag_1", ResourceType: "order", ResourceID: "ord_1",
			})
		}},
	}
	answers := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"200 carrying a resource", http.StatusOK, `{"data":{"id":"still-active"}}`, "expected 204"},
		{"200 with no body", http.StatusOK, "", "expected 204"},
		{"202 accepted", http.StatusAccepted, "", "expected 204"},
	}
	for _, del := range deletes {
		for _, answer := range answers {
			del, answer := del, answer
			t.Run(del.name+"/"+answer.name, func(t *testing.T) {
				c, cleanup := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodDelete {
						t.Errorf("method = %s, want DELETE", r.Method)
					}
					w.WriteHeader(answer.status)
					_, _ = w.Write([]byte(answer.body))
				})
				defer cleanup()
				err := del.call(c)
				if err == nil {
					t.Fatal("a DELETE that was not answered with 204 reported success")
				}
				if !strings.Contains(err.Error(), answer.want) {
					t.Errorf("error should mention %q, got: %v", answer.want, err)
				}
				if _, ok := AsAPIError(err); ok {
					t.Errorf("a 2xx became an *APIError (%v); the server did not say no", err)
				}
			})
		}
	}

	// The body check, reached directly: net/http drops a body on a real 204, so
	// no server can trip it, and only the helper's contract can be asserted.
	t.Run("204 is still a success", func(t *testing.T) {
		c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})
		defer cleanup()
		for _, del := range deletes {
			if err := del.call(c); err != nil {
				t.Errorf("%s: %v", del.name, err)
			}
		}
	})
}
