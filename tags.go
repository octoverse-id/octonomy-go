package octonomy

import (
	"context"
	"encoding/json"
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
// nil error. "id" is required on the Tag schema in both vendored contracts.
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

// TagUpdate is the PATCH body for updating a tag. Only non-nil fields are sent,
// so the server updates exactly what you set.
//
// A pointer field cannot be cleared to null: nil means "leave it alone", and
// there is no third state. The /v2 module's Optional[T] carries that state, and
// is not ported here because changing a published field's type would break
// every v1.0.0 caller that sets one.
//
// Metadata REPLACES the stored object rather than merging into it, and is the
// one field with three states, because a nil map and an empty one are different
// values in Go:
//
//	octonomy.TagUpdate{Metadata: octonomy.Metadata{"team": "growth"}} // replaces the stored object
//	octonomy.TagUpdate{Metadata: octonomy.Metadata{}}                 // sends {} -- empties it
//	octonomy.TagUpdate{}                                              // omits the key -- untouched
//
// The middle line is what v1.0.0 got wrong: encoding/json counts a zero-length
// map as empty under omitempty, so Metadata{} sent NO metadata key and a caller
// asking to clear the object got a 200 with the old object still in place and no
// error. MarshalJSON below is the fix.
type TagUpdate struct {
	ApplicationID *string  `json:"application_id,omitempty"`
	Name          *string  `json:"name,omitempty"`
	Slug          *string  `json:"slug,omitempty"`
	Type          *string  `json:"type,omitempty"`
	Description   *string  `json:"description,omitempty"`
	ParentID      *string  `json:"parent_id,omitempty"`
	VocabularyID  *string  `json:"vocabulary_id,omitempty"`
	Metadata      Metadata `json:"metadata,omitempty"`
	IsActive      *bool    `json:"is_active,omitempty"`
}

// MarshalJSON encodes a TagUpdate as encoding/json would from its tags, with one
// difference: a non-nil Metadata is always sent, so Metadata{} goes out as
// "metadata":{} and empties the stored object. A nil Metadata still omits the
// key. Every other field, and the order of all of them, is unchanged from what
// the struct tags alone produce.
//
// It is declared on the VALUE receiver, and must stay there. TagService.Update
// takes a TagUpdate by value, and a pointer-receiver MarshalJSON is not in a
// value's method set, so encoding/json would skip it without a word and send
// the omitempty encoding this method exists to replace.
//
// It lives on the struct rather than on Metadata because Metadata is a type
// alias for map[string]interface{}, and Go does not allow methods on aliases.
func (u TagUpdate) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		ApplicationID *string   `json:"application_id,omitempty"`
		Name          *string   `json:"name,omitempty"`
		Slug          *string   `json:"slug,omitempty"`
		Type          *string   `json:"type,omitempty"`
		Description   *string   `json:"description,omitempty"`
		ParentID      *string   `json:"parent_id,omitempty"`
		VocabularyID  *string   `json:"vocabulary_id,omitempty"`
		Metadata      *Metadata `json:"metadata,omitempty"`
		IsActive      *bool     `json:"is_active,omitempty"`
	}{
		ApplicationID: u.ApplicationID,
		Name:          u.Name,
		Slug:          u.Slug,
		Type:          u.Type,
		Description:   u.Description,
		ParentID:      u.ParentID,
		VocabularyID:  u.VocabularyID,
		Metadata:      sentMetadata(u.Metadata),
		IsActive:      u.IsActive,
	})
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

// TagList is the envelope GET /tags returns: {"data": [...], "pagination": {...}}.
// It is the Tag instantiation of a shape that would otherwise be one generic
// type; see pagination.go for why this line spells it out per resource.
type TagList struct {
	Data       []Tag      `json:"data"`
	Pagination Pagination `json:"pagination"`
}

// rows hands doList the decoded tags so it can check each one's identity.
func (l *TagList) rows() []identifiedResource {
	rows := make([]identifiedResource, len(l.Data))
	for i := range l.Data {
		rows[i] = l.Data[i]
	}
	return rows
}

// TagService accesses the /tags endpoints. Reach it via Client.Tags.
type TagService struct {
	client *Client
}

// Create creates a tag (POST /tags). A duplicate (type, slug) for the tenant
// returns an *APIError for which IsConflict reports true.
func (s *TagService) Create(ctx context.Context, in TagCreate, opts ...RequestOption) (*Tag, error) {
	var out Tag
	if err := s.client.doData(ctx, http.MethodPost, "/tags", nil, in, &out, opts...); err != nil {
		return nil, err
	}
	return &out, nil
}

// Get retrieves a tag by ID (GET /tags/{id}).
func (s *TagService) Get(ctx context.Context, id string, opts ...RequestOption) (*Tag, error) {
	var out Tag
	if err := s.client.doData(ctx, http.MethodGet, "/tags/"+url.PathEscape(id), nil, nil, &out, opts...); err != nil {
		return nil, err
	}
	return &out, nil
}

// List returns a page of tags (GET /tags).
func (s *TagService) List(ctx context.Context, params *TagListParams, opts ...RequestOption) (*TagList, error) {
	var out TagList
	if err := s.client.doList(ctx, http.MethodGet, "/tags", params.query(), &out, opts...); err != nil {
		return nil, err
	}
	return &out, nil
}

// Update partially updates a tag (PATCH /tags/{id}).
//
// Scope is fixed at creation: a body that changes ApplicationID, or the namespace
// the row already holds, is refused with a 409 that IsScopeImmutable matches.
// Re-create the tag in the target scope instead.
func (s *TagService) Update(ctx context.Context, id string, in TagUpdate, opts ...RequestOption) (*Tag, error) {
	var out Tag
	if err := s.client.doData(ctx, http.MethodPatch, "/tags/"+url.PathEscape(id), nil, in, &out, opts...); err != nil {
		return nil, err
	}
	return &out, nil
}

// Delete deactivates a tag (DELETE /tags/{id}). Octonomy treats deletion as
// deactivation, which cascades to the tag's aliases.
func (s *TagService) Delete(ctx context.Context, id string, opts ...RequestOption) error {
	return s.client.do(ctx, http.MethodDelete, "/tags/"+url.PathEscape(id), nil, nil, opts...)
}
