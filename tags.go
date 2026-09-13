package octonomy

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Tag is the core tagging unit. A tag with a nil ApplicationID is shared across
// the tenant; otherwise it is scoped to a single application. ParentID and
// VocabularyID are set when the tag is nested or grouped. UsageCount is
// server-computed and read-only.
type Tag struct {
	ID            string  `json:"id"`
	TenantID      string  `json:"tenant_id"`
	ApplicationID *string `json:"application_id"`

	// NamespaceType and NamespaceID identify the merchant or sub-tenant namespace
	// that owns this row; both are nil for a global (tenant-shared) row. They are
	// decode-only and appear on the v2 surface: the server sets them from the
	// X-Namespace-* headers at creation and never from the request body, and they
	// are fixed for the row's lifetime (attempting to change them is a 409
	// scope_immutable). /api/v1 responses omit them, so they decode to nil there.
	NamespaceType *string   `json:"namespace_type"`
	NamespaceID   *string   `json:"namespace_id"`
	Name          string    `json:"name"`
	Slug          string    `json:"slug"`
	Type          string    `json:"type"`
	Description   *string   `json:"description"`
	ParentID      *string   `json:"parent_id"`
	VocabularyID  *string   `json:"vocabulary_id"`
	Metadata      Metadata  `json:"metadata,omitempty"`
	IsActive      bool      `json:"is_active"`
	UsageCount    int       `json:"usage_count"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// identityFields makes a blank id an error rather than a zero-valued Tag with a
// nil error (#40). "id" is required on the Tag schema in both vendored
// contracts.
func (t Tag) identityFields() []identityField {
	return []identityField{{name: "id", value: t.ID}}
}

// TagCreate is the request body for creating a tag. Name, Slug, and Type are
// required; the remaining fields are optional.
type TagCreate struct {
	ApplicationID *string  `json:"application_id,omitempty"`
	Name          string   `json:"name"`
	Slug          string   `json:"slug"`
	Type          string   `json:"type"`
	Description   *string  `json:"description,omitempty"`
	ParentID      *string  `json:"parent_id,omitempty"`
	VocabularyID  *string  `json:"vocabulary_id,omitempty"`
	Metadata      Metadata `json:"metadata,omitempty"`
	IsActive      *bool    `json:"is_active,omitempty"`
}

// TagUpdate is the PATCH body for updating a tag. Every field is an Optional,
// so each one says exactly one of three things:
//
//	octonomy.TagUpdate{Name: octonomy.Set("Autumn")}       // {"name":"Autumn"}
//	octonomy.TagUpdate{ParentID: octonomy.Null[string]()}  // {"parent_id":null} -- un-nests the tag
//	octonomy.TagUpdate{}                                   // {} -- touches nothing
//
// A PATCH replaces rather than merges, field by field: the server leaves every
// column whose key is absent alone, and overwrites the ones that arrive.
//
// # Which nulls the server accepts, and which it refuses
//
// The vendored v2 contract marks seven properties nullable across the three
// patch schemas, three of which are application_id -- refused on every resource
// as a scope change -- so the REACHABLE set is four, three of them here.
// Verified live against a running server, one PATCH per row, re-reading the row
// after each:
//
//	{"parent_id": null}       200  link cleared
//	{"vocabulary_id": null}   200  link cleared
//	{"description": null}     200  cleared to null
//	{"application_id": null}  409  scope_immutable -- when the row HAS an application
//	{"name": null}            400  validation_error: "This field may not be null."
//	{"slug": null}            400  likewise; also type, metadata and is_active
//
// **A null ApplicationID on a row that is already tenant-shared answers 200.**
// The server refuses a scope CHANGE, not the literal null, so the no-op case
// passes. That is not permission to clear one -- see IsScopeImmutable.
//
// This package refuses none of the 400s locally. Which fields are nullable is a
// server rule, and re-running server validation in the client is out of bounds
// here; the server names the offending field in APIError.Details.
//
// # Clearing a parent is how a cycle gets broken
//
// A parent cycle is reachable on the server -- the database forbids only
// parent_id = id, and nothing walks the ancestry -- so BuildTagTree refuses one
// with ErrTagCycle. Null[string]() on ParentID is the repair: before #64 the
// request could not be expressed at all, and the ring had to be broken by
// re-pointing a link at a third tag or deactivating a row instead.
type TagUpdate struct {
	ApplicationID Optional[string] `json:"application_id,omitzero"`
	Name          Optional[string] `json:"name,omitzero"`
	Slug          Optional[string] `json:"slug,omitzero"`
	Type          Optional[string] `json:"type,omitzero"`
	Description   Optional[string] `json:"description,omitzero"`
	ParentID      Optional[string] `json:"parent_id,omitzero"`
	VocabularyID  Optional[string] `json:"vocabulary_id,omitzero"`

	// Metadata REPLACES the stored object rather than merging into it:
	//
	//	octonomy.Set(octonomy.Metadata{"team": "growth"})  // replaces the stored object
	//	octonomy.Set(octonomy.Metadata{})                  // sends {} -- empties it
	//	octonomy.Optional[octonomy.Metadata]{}             // omits the key -- untouched
	//
	// Emptying it is Set of an EMPTY MAP, never Null: the server answers
	// "metadata": null with a 400 ("This field may not be null"), so
	// Null[Metadata]() compiles and is always refused. Set of a NIL map
	// (var m Metadata; Set(m)) encodes as null and is refused the same way --
	// write Set(Metadata{}).
	//
	// This field is what #37 was about. While it was a plain Metadata,
	// encoding/json counted a zero-length map as empty under omitempty, so
	// Metadata{} sent NO metadata key and a caller asking to clear the object
	// got a 200 with the old object still in place and no error. omitzero
	// cannot repeat that: it consults Optional.IsZero, which reports which of
	// the three states the field is in and never inspects the payload, so an
	// empty map that was Set explicitly still goes out.
	//
	// VocabularyUpdate and TagAliasUpdate carry the same field for the same
	// reason and point here; all three moved together so that no resource is
	// the outlier.
	Metadata Optional[Metadata] `json:"metadata,omitzero"`

	IsActive Optional[bool] `json:"is_active,omitzero"`
}

// TagListParams filters and pages the tag list. A nil *params lists with server
// defaults. Query maps to the server's free-text `q` parameter.
type TagListParams struct {
	ListOptions
	ApplicationID *string
	IncludeShared *bool
	IsActive      *bool
	ParentID      *string
	Query         *string
	Slug          *string
	Type          *string
	VocabularyID  *string
}

func (p *TagListParams) query() url.Values {
	q := url.Values{}
	if p == nil {
		return q
	}
	p.apply(q)
	if p.ApplicationID != nil {
		q.Set("application_id", *p.ApplicationID)
	}
	if p.IncludeShared != nil {
		q.Set("include_shared", strconv.FormatBool(*p.IncludeShared))
	}
	if p.IsActive != nil {
		q.Set("is_active", strconv.FormatBool(*p.IsActive))
	}
	if p.ParentID != nil {
		q.Set("parent_id", *p.ParentID)
	}
	if p.Query != nil {
		q.Set("q", *p.Query)
	}
	if p.Slug != nil {
		q.Set("slug", *p.Slug)
	}
	if p.Type != nil {
		q.Set("type", *p.Type)
	}
	if p.VocabularyID != nil {
		q.Set("vocabulary_id", *p.VocabularyID)
	}
	return q
}

// TagService accesses the /tags endpoints. Reach it via Client.Tags.
type TagService struct {
	client *Client
}

// Create creates a tag (POST /tags). A duplicate (type, slug) for the tenant
// returns an *APIError for which IsConflict reports true.
func (s *TagService) Create(ctx context.Context, in TagCreate, opts ...RequestOption) (*Tag, error) {
	return doData[Tag](ctx, s.client, http.MethodPost, "/tags", nil, in, opts...)
}

// Get retrieves a tag by ID (GET /tags/{id}).
func (s *TagService) Get(ctx context.Context, id string, opts ...RequestOption) (*Tag, error) {
	return doData[Tag](ctx, s.client, http.MethodGet, "/tags/"+url.PathEscape(id), nil, nil, opts...)
}

// List returns a page of tags (GET /tags).
func (s *TagService) List(ctx context.Context, params *TagListParams, opts ...RequestOption) (*List[Tag], error) {
	return doList[Tag](ctx, s.client, http.MethodGet, "/tags", params.query(), opts...)
}

// Update partially updates a tag (PATCH /tags/{id}).
//
// Scope is fixed at creation: a body that changes ApplicationID, or the namespace
// the row already holds, is refused with a 409 that IsScopeImmutable matches.
// Re-create the tag in the target scope instead.
func (s *TagService) Update(ctx context.Context, id string, in TagUpdate, opts ...RequestOption) (*Tag, error) {
	return doData[Tag](ctx, s.client, http.MethodPatch, "/tags/"+url.PathEscape(id), nil, in, opts...)
}

// Delete deactivates a tag (DELETE /tags/{id}). Octonomy treats deletion as
// deactivation, which cascades to the tag's aliases.
func (s *TagService) Delete(ctx context.Context, id string, opts ...RequestOption) error {
	return s.client.do(ctx, http.MethodDelete, "/tags/"+url.PathEscape(id), nil, nil, opts...)
}
