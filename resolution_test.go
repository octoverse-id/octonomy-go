package octonomy

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestTags_Resolve_MatchedTag(t *testing.T) {
	created := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2/tag-resolution" {
			t.Errorf("got %s %s, want GET /api/v2/tag-resolution", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("slug"); got != "on-sale" {
			t.Errorf("slug = %q, want on-sale", got)
		}
		writeData(t, w, http.StatusOK, TagResolution{
			MatchedType: MatchedTypeTag,
			Tag: Tag{
				ID: "tag_1", TenantID: "tenant-1", Name: "On Sale", Slug: "on-sale",
				Type: "label", IsActive: true, CreatedAt: created, UpdatedAt: created,
			},
		})
	})

	res, err := c.Tags.Resolve(context.Background(), "on-sale", nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.MatchedType != MatchedTypeTag {
		t.Errorf("MatchedType = %q, want %q", res.MatchedType, MatchedTypeTag)
	}
	// Nil is the documented contract for a canonical match, and it is what a
	// caller branches on to know the slug was not an alias.
	if res.MatchedAlias != nil {
		t.Errorf("MatchedAlias = %+v, want nil on a canonical match", res.MatchedAlias)
	}
	if res.Tag.ID != "tag_1" || res.Tag.Slug != "on-sale" || res.Tag.Type != "label" {
		t.Errorf("tag did not round-trip: %+v", res.Tag)
	}
	if !res.Tag.CreatedAt.Equal(created) {
		t.Errorf("Tag.CreatedAt = %v, want %v", res.Tag.CreatedAt, created)
	}
}

// The alias branch is the one that needs the nested decode to work: matched_alias
// is a full TagAlias, and tag is the CANONICAL tag rather than the alias's own
// row, so the two must not be confused.
func TestTags_Resolve_MatchedAlias(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("slug"); got != "sale" {
			t.Errorf("slug = %q, want sale", got)
		}
		writeData(t, w, http.StatusOK, TagResolution{
			MatchedType: MatchedTypeAlias,
			MatchedAlias: &TagAlias{
				ID: "alias_1", TenantID: "tenant-1", TagID: "tag_1",
				Name: "Sale", Slug: "sale",
				NamespaceType: String("merchant"), NamespaceID: String("m-42"),
				Metadata: Metadata{"source": "import"}, IsActive: true,
			},
			Tag: Tag{ID: "tag_1", Name: "On Sale", Slug: "on-sale", Type: "label"},
		})
	})

	res, err := c.Tags.Resolve(context.Background(), "sale", nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.MatchedType != MatchedTypeAlias {
		t.Errorf("MatchedType = %q, want %q", res.MatchedType, MatchedTypeAlias)
	}
	if res.MatchedAlias == nil {
		t.Fatal("MatchedAlias = nil, want the alias that matched")
	}
	if res.MatchedAlias.ID != "alias_1" || res.MatchedAlias.Slug != "sale" || res.MatchedAlias.TagID != "tag_1" {
		t.Errorf("alias did not round-trip: %+v", res.MatchedAlias)
	}
	if res.MatchedAlias.NamespaceType == nil || *res.MatchedAlias.NamespaceType != "merchant" {
		t.Errorf("alias NamespaceType = %v, want merchant", res.MatchedAlias.NamespaceType)
	}
	if res.MatchedAlias.Metadata["source"] != "import" {
		t.Errorf("alias Metadata[source] = %v, want import", res.MatchedAlias.Metadata["source"])
	}
	// The canonical tag, not the alias -- the slug asked for was "sale" and the
	// tag it resolves to is "on-sale".
	if res.Tag.ID != "tag_1" || res.Tag.Slug != "on-sale" {
		t.Errorf("resolved tag should be the canonical one, got: %+v", res.Tag)
	}
}

func TestTags_Resolve_AllParams(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		want := map[string]string{
			"slug":           "on-sale",
			"application_id": "commerce",
			"type":           "label",
			"scope":          "global",
		}
		for k, v := range want {
			if got := q.Get(k); got != v {
				t.Errorf("query[%s] = %q, want %q", k, got, v)
			}
		}
		if len(q) != len(want) {
			t.Errorf("query = %v, want exactly %d params", q, len(want))
		}
		writeData(t, w, http.StatusOK, TagResolution{MatchedType: MatchedTypeTag, Tag: Tag{ID: "tag_1"}})
	})

	res, err := c.Tags.Resolve(context.Background(), "on-sale", &TagResolveParams{
		ApplicationID: String("commerce"),
		Type:          String("label"),
		Scope:         ResolutionScopeGlobal,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Tag.ID != "tag_1" {
		t.Errorf("Tag.ID = %q, want tag_1 (a zero-valued TagResolution would reach here too)", res.Tag.ID)
	}
}

func TestTags_Resolve_NilParamsSendsOnlyTheSlug(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.RawQuery; got != "slug=on-sale" {
			t.Errorf("query = %q, want exactly slug=on-sale", got)
		}
		writeData(t, w, http.StatusOK, TagResolution{MatchedType: MatchedTypeTag, Tag: Tag{ID: "tag_1"}})
	})

	if _, err := c.Tags.Resolve(context.Background(), "on-sale", nil); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
}

// An empty Scope is omitted rather than sent as scope="", which the server would
// reject with "Use 'global' or 'merchant'".
func TestTags_Resolve_EmptyScopeIsOmitted(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("scope") {
			t.Errorf("scope should be absent, got query %q", r.URL.RawQuery)
		}
		writeData(t, w, http.StatusOK, TagResolution{MatchedType: MatchedTypeTag, Tag: Tag{ID: "tag_1"}})
	})

	if _, err := c.Tags.Resolve(context.Background(), "on-sale", &TagResolveParams{Type: String("label")}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
}

// The headline surprise of this endpoint: an unmatched slug is a 400
// validation_error, not a 404. A caller reaching for IsNotFound here gets false
// and falls through to its generic error branch.
func TestTags_Resolve_NoMatchIsValidationNotNotFound(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusBadRequest, map[string]any{
			"error": map[string]any{
				"code":    CodeValidation,
				"message": "Request validation failed.",
				"details": map[string]any{"slug": []string{"No active tag or alias matched this slug."}},
			},
		})
	})

	_, err := c.Tags.Resolve(context.Background(), "nothing-called-this", nil)
	if !IsValidation(err) {
		t.Fatalf("expected a validation error, got %v", err)
	}
	if IsNotFound(err) {
		t.Error("an unmatched slug must not read as not_found: the server answers 400, not 404")
	}
	apiErr, ok := AsAPIError(err)
	if !ok || apiErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("unexpected error: %+v", apiErr)
	}
	if _, ok := apiErr.Details["slug"]; !ok {
		t.Errorf("Details should name the slug axis, got %v", apiErr.Details)
	}
}

// The two ambiguity axes arrive under DIFFERENT codes, which is the trap: a
// caller handling only IsAmbiguousResolution misses the type-axis tie entirely.
func TestTags_Resolve_AmbiguityAxes(t *testing.T) {
	tests := []struct {
		name              string
		code              string
		detailKey         string
		wantAmbiguous     bool
		wantValidationToo bool
	}{
		{
			name:          "across applications is ambiguous_resolution",
			code:          CodeAmbiguousResolution,
			detailKey:     "application_id",
			wantAmbiguous: true,
		},
		{
			name:              "across types is a plain validation_error",
			code:              CodeValidation,
			detailKey:         "type",
			wantValidationToo: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, http.StatusBadRequest, map[string]any{
					"error": map[string]any{
						"code":    tt.code,
						"message": "Multiple matches.",
						"details": map[string]any{tt.detailKey: []string{"provide it"}},
					},
				})
			})

			_, err := c.Tags.Resolve(context.Background(), "sale", nil)
			if got := IsAmbiguousResolution(err); got != tt.wantAmbiguous {
				t.Errorf("IsAmbiguousResolution = %v, want %v", got, tt.wantAmbiguous)
			}
			if got := IsValidation(err); got != tt.wantValidationToo {
				t.Errorf("IsValidation = %v, want %v", got, tt.wantValidationToo)
			}
			apiErr, ok := AsAPIError(err)
			if !ok {
				t.Fatalf("expected *APIError, got %v", err)
			}
			if _, ok := apiErr.Details[tt.detailKey]; !ok {
				t.Errorf("Details should name the %q axis, got %v", tt.detailKey, apiErr.Details)
			}
		})
	}
}

// scope=merchant resolves within the request's namespace, so a request with none
// is refused locally -- #7's guard, which this endpoint is the first to reach
// from a resource method rather than a raw doRaw call.
func TestTags_Resolve_MerchantScopeNeedsANamespace(t *testing.T) {
	c := newUnreachableClient(t, APIV2)

	_, err := c.Tags.Resolve(context.Background(), "on-sale", &TagResolveParams{Scope: ResolutionScopeMerchant})
	if err == nil {
		t.Fatal("expected a local error for scope=merchant with no namespace")
	}
	if !strings.Contains(err.Error(), "WithNamespace") {
		t.Errorf("error should name the fix, got: %v", err)
	}
}

// The asymmetry that makes the guard subtle: "global" is a reserved namespace
// TYPE but a legal SCOPE, so this must NOT be refused.
func TestTags_Resolve_GlobalScopeNeedsNoNamespace(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("scope"); got != "global" {
			t.Errorf("scope = %q, want global", got)
		}
		if r.Header.Get(namespaceTypeHeader) != "" {
			t.Errorf("no namespace header should be sent, got %q", r.Header.Get(namespaceTypeHeader))
		}
		writeData(t, w, http.StatusOK, TagResolution{MatchedType: MatchedTypeTag, Tag: Tag{ID: "tag_1"}})
	})

	if _, err := c.Tags.Resolve(context.Background(), "on-sale", &TagResolveParams{Scope: ResolutionScopeGlobal}); err != nil {
		t.Fatalf("scope=global must be accepted without a namespace: %v", err)
	}
}

func TestTags_Resolve_MerchantScopeWithANamespace(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get(namespaceTypeHeader); got != "merchant" {
			t.Errorf("%s = %q, want merchant", namespaceTypeHeader, got)
		}
		if got := r.Header.Get(namespaceIDHeader); got != "m-42" {
			t.Errorf("%s = %q, want m-42", namespaceIDHeader, got)
		}
		q := r.URL.Query()
		if q.Get("scope") != "merchant" || q.Get("application_id") != "commerce" {
			t.Errorf("unexpected query: %v", q)
		}
		// Merchant scope excludes global rows by definition, so nothing should
		// be asking for them here.
		if q.Has("include_global") {
			t.Errorf("include_global should be absent, got query %q", r.URL.RawQuery)
		}
		writeData(t, w, http.StatusOK, TagResolution{MatchedType: MatchedTypeTag, Tag: Tag{ID: "tag_1"}})
	})

	_, err := c.Tags.Resolve(context.Background(), "on-sale",
		&TagResolveParams{Scope: ResolutionScopeMerchant, ApplicationID: String("commerce")},
		WithNamespace("merchant", "m-42"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
}

// Merchant scope and WithIncludeGlobal ask for opposite things, and the server
// does not report the contradiction -- effective_resolution_scope discards
// include_global outright on the merchant branch, so the caller would get
// merchant-only results with no sign the option did nothing. Refused locally,
// the same rule as WithIncludeGlobal on a write.
func TestTags_Resolve_MerchantScopeRefusesIncludeGlobal(t *testing.T) {
	c := newUnreachableClient(t, APIV2)

	_, err := c.Tags.Resolve(context.Background(), "on-sale",
		&TagResolveParams{Scope: ResolutionScopeMerchant, ApplicationID: String("commerce")},
		WithNamespace("merchant", "m-42"), WithIncludeGlobal())
	if err == nil {
		t.Fatal("expected a local error for WithIncludeGlobal alongside merchant scope")
	}
	if !strings.Contains(err.Error(), "WithIncludeGlobal") || !strings.Contains(err.Error(), "merchant") {
		t.Errorf("error should name both halves of the contradiction, got: %v", err)
	}
}

// The global scope does NOT collide with it: the pairing is redundant, since
// scope=global is itself the authorization opt-in on this route, but redundant
// is not contradictory and the SDK does not refuse it.
func TestTags_Resolve_GlobalScopeAllowsIncludeGlobal(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("scope") != "global" || q.Get("include_global") != "true" {
			t.Errorf("unexpected query: %v", q)
		}
		writeData(t, w, http.StatusOK, TagResolution{MatchedType: MatchedTypeTag, Tag: Tag{ID: "tag_1"}})
	})

	_, err := c.Tags.Resolve(context.Background(), "on-sale",
		&TagResolveParams{Scope: ResolutionScopeGlobal, ApplicationID: String("commerce")},
		WithNamespace("merchant", "m-42"), WithIncludeGlobal())
	if err != nil {
		t.Fatalf("scope=global with WithIncludeGlobal must be accepted: %v", err)
	}
}

// WithApplication and the params field set the same query parameter, so
// disagreeing about it is an error rather than a silent precedence decision.
func TestTags_Resolve_ContradictoryApplicationIsAnError(t *testing.T) {
	c := newUnreachableClient(t, APIV2)

	_, err := c.Tags.Resolve(context.Background(), "on-sale",
		&TagResolveParams{ApplicationID: String("commerce")}, WithApplication("storefront"))
	if err == nil {
		t.Fatal("expected an error for contradictory application scope")
	}
	if !strings.Contains(err.Error(), "contradicts") {
		t.Errorf("error should say the two contradict, got: %v", err)
	}
}

// The #32 guard at this resource: a bare, unenveloped body -- which is what the
// vendored spec describes -- must be an error, never a zero-valued
// TagResolution whose MatchedType is "" and whose Tag is empty.
func TestTags_Resolve_UnwrappedBodyIsAnError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, TagResolution{MatchedType: MatchedTypeTag, Tag: Tag{ID: "tag_1"}})
	})

	res, err := c.Tags.Resolve(context.Background(), "on-sale", nil)
	if err == nil {
		t.Fatalf("expected an error for a body with no data envelope, got %+v", res)
	}
}

// Resolution exists on both surfaces with the same four parameters; only the
// namespace headers and include_global are v2-only.
func TestTags_Resolve_OnV1(t *testing.T) {
	c := newVersionedTestClient(t, APIV1, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/tag-resolution" {
			t.Errorf("path = %q, want /api/v1/tag-resolution", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("slug") != "on-sale" || q.Get("scope") != "global" {
			t.Errorf("unexpected query: %v", q)
		}
		writeData(t, w, http.StatusOK, TagResolution{MatchedType: MatchedTypeTag, Tag: Tag{ID: "tag_1"}})
	})

	res, err := c.Tags.Resolve(context.Background(), "on-sale", &TagResolveParams{Scope: ResolutionScopeGlobal})
	if err != nil {
		t.Fatalf("Resolve on v1: %v", err)
	}
	// v1 responses carry no namespace fields, so they decode to nil rather than "".
	if res.Tag.NamespaceType != nil || res.Tag.NamespaceID != nil {
		t.Errorf("v1 tag reported a namespace: %+v", res.Tag)
	}
}

// scope=merchant cannot mean anything on v1, and the namespace it requires is
// itself v2-only -- so the refusal comes from the namespace guard, before the
// request is built.
func TestTags_Resolve_MerchantScopeIsUnreachableOnV1(t *testing.T) {
	c := newUnreachableClient(t, APIV1)

	_, err := c.Tags.Resolve(context.Background(), "on-sale",
		&TagResolveParams{Scope: ResolutionScopeMerchant, ApplicationID: String("commerce")},
		WithNamespace("merchant", "m-42"))
	if err == nil {
		t.Fatal("expected a local error for a namespaced v1 request")
	}
	if !strings.Contains(err.Error(), "APIV2") {
		t.Errorf("error should name the fix, got: %v", err)
	}
}
