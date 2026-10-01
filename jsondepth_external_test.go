package octonomy_test

// The one body-walk case that needs a type declared OUTSIDE package octonomy:
// a caller's own json.Marshaler. checkBodyDepth treats this package's types as
// walkable and everyone else's Marshaler as opaque, so the fixture has to live
// in a different package to be the second kind.

import (
	"context"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	octonomy "github.com/octoverse-id/octonomy-go"
)

// selfRef refers to itself through an exported field, and encodes as a fixed
// string. encoding/json calls MarshalJSON and never sees the field.
type selfRef struct {
	Self *selfRef
}

func (s *selfRef) MarshalJSON() ([]byte, error) { return []byte(`"opaque"`), nil }

func TestCheckBodyDepth_ACallersMarshalerIsOpaque(t *testing.T) {
	var sent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := ioutil.ReadAll(r.Body)
		sent = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"id":"t1"}}`))
	}))
	defer srv.Close()
	c, err := octonomy.New(octonomy.Config{BaseURL: srv.URL, Token: "t", TenantID: "acme"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	s := &selfRef{}
	s.Self = s
	if _, err := c.Tags.Create(context.Background(), octonomy.TagCreate{
		Name: "N", Slug: "n", Type: "t", Metadata: octonomy.Metadata{"s": s},
	}); err != nil {
		t.Fatalf("Create refused a body encoding/json encodes fine: %v", err)
	}
	if !strings.Contains(sent, `"s":"opaque"`) {
		t.Errorf("body = %s, want the Marshaler's own encoding", sent)
	}
}
