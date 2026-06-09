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
	ID            string    `json:"id"`
	TenantID      string    `json:"tenant_id"`
	ApplicationID *string   `json:"application_id"`
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

// TagUpdate is the PATCH body for updating a tag. Only non-nil fields are sent.
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
	p.ListOptions.apply(q)
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
	var out Tag
	if err := s.client.do(ctx, http.MethodPost, "/tags", nil, in, &out, opts...); err != nil {
		return nil, err
	}
	return &out, nil
}

// Get retrieves a tag by ID (GET /tags/{id}).
func (s *TagService) Get(ctx context.Context, id string, opts ...RequestOption) (*Tag, error) {
	var out Tag
	if err := s.client.do(ctx, http.MethodGet, "/tags/"+url.PathEscape(id), nil, nil, &out, opts...); err != nil {
		return nil, err
	}
	return &out, nil
}

// List returns a page of tags (GET /tags).
func (s *TagService) List(ctx context.Context, params *TagListParams, opts ...RequestOption) (*List[Tag], error) {
	var out List[Tag]
	if err := s.client.do(ctx, http.MethodGet, "/tags", params.query(), nil, &out, opts...); err != nil {
		return nil, err
	}
	return &out, nil
}

// Update partially updates a tag (PATCH /tags/{id}).
func (s *TagService) Update(ctx context.Context, id string, in TagUpdate, opts ...RequestOption) (*Tag, error) {
	var out Tag
	if err := s.client.do(ctx, http.MethodPatch, "/tags/"+url.PathEscape(id), nil, in, &out, opts...); err != nil {
		return nil, err
	}
	return &out, nil
}

// Delete deactivates a tag (DELETE /tags/{id}). Octonomy treats deletion as
// deactivation, which cascades to the tag's aliases.
func (s *TagService) Delete(ctx context.Context, id string, opts ...RequestOption) error {
	return s.client.do(ctx, http.MethodDelete, "/tags/"+url.PathEscape(id), nil, nil, nil, opts...)
}
