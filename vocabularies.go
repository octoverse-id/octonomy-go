package octonomy

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Vocabulary is a tenant-scoped grouping for tags. A vocabulary with a nil
// ApplicationID is shared across all applications in the tenant; otherwise it is
// scoped to a single application.
type Vocabulary struct {
	ID            string    `json:"id"`
	TenantID      string    `json:"tenant_id"`
	ApplicationID *string   `json:"application_id"`
	Name          string    `json:"name"`
	Slug          string    `json:"slug"`
	Description   *string   `json:"description"`
	Metadata      Metadata  `json:"metadata,omitempty"`
	IsActive      bool      `json:"is_active"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// VocabularyCreate is the request body for creating a vocabulary. Name and Slug
// are required; the remaining fields are optional.
type VocabularyCreate struct {
	ApplicationID *string  `json:"application_id,omitempty"`
	Name          string   `json:"name"`
	Slug          string   `json:"slug"`
	Description   *string  `json:"description,omitempty"`
	Metadata      Metadata `json:"metadata,omitempty"`
	IsActive      *bool    `json:"is_active,omitempty"`
}

// VocabularyUpdate is the PATCH body for updating a vocabulary. Only non-nil
// fields are sent, so the server updates exactly what you set.
type VocabularyUpdate struct {
	ApplicationID *string  `json:"application_id,omitempty"`
	Name          *string  `json:"name,omitempty"`
	Slug          *string  `json:"slug,omitempty"`
	Description   *string  `json:"description,omitempty"`
	Metadata      Metadata `json:"metadata,omitempty"`
	IsActive      *bool    `json:"is_active,omitempty"`
}

// VocabularyListParams filters and pages the vocabulary list. A nil *params lists
// with server defaults.
type VocabularyListParams struct {
	ListOptions
	ApplicationID *string
	IncludeShared *bool
	IsActive      *bool
}

func (p *VocabularyListParams) query() url.Values {
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
	return q
}

// VocabularyList is the envelope GET /vocabularies returns: {"data": [...],
// "pagination": {...}}. It is the Vocabulary instantiation of what the modern
// line expresses as List[Vocabulary]; see pagination.go for why this line spells
// it out per resource.
type VocabularyList struct {
	Data       []Vocabulary `json:"data"`
	Pagination Pagination   `json:"pagination"`
}

// VocabularyService accesses the /vocabularies endpoints. Reach it via
// Client.Vocabularies.
type VocabularyService struct {
	client *Client
}

// Create creates a vocabulary (POST /vocabularies).
func (s *VocabularyService) Create(ctx context.Context, in VocabularyCreate, opts ...RequestOption) (*Vocabulary, error) {
	var out Vocabulary
	if err := s.client.doData(ctx, http.MethodPost, "/vocabularies", nil, in, &out, opts...); err != nil {
		return nil, err
	}
	return &out, nil
}

// Get retrieves a vocabulary by ID (GET /vocabularies/{id}).
func (s *VocabularyService) Get(ctx context.Context, id string, opts ...RequestOption) (*Vocabulary, error) {
	var out Vocabulary
	if err := s.client.doData(ctx, http.MethodGet, "/vocabularies/"+url.PathEscape(id), nil, nil, &out, opts...); err != nil {
		return nil, err
	}
	return &out, nil
}

// List returns a page of vocabularies (GET /vocabularies).
func (s *VocabularyService) List(ctx context.Context, params *VocabularyListParams, opts ...RequestOption) (*VocabularyList, error) {
	var out VocabularyList
	if err := s.client.do(ctx, http.MethodGet, "/vocabularies", params.query(), nil, &out, opts...); err != nil {
		return nil, err
	}
	return &out, nil
}

// Update partially updates a vocabulary (PATCH /vocabularies/{id}).
func (s *VocabularyService) Update(ctx context.Context, id string, in VocabularyUpdate, opts ...RequestOption) (*Vocabulary, error) {
	var out Vocabulary
	if err := s.client.doData(ctx, http.MethodPatch, "/vocabularies/"+url.PathEscape(id), nil, in, &out, opts...); err != nil {
		return nil, err
	}
	return &out, nil
}

// Delete deactivates a vocabulary (DELETE /vocabularies/{id}). Octonomy treats
// deletion as deactivation; the record and its history are retained.
func (s *VocabularyService) Delete(ctx context.Context, id string, opts ...RequestOption) error {
	return s.client.do(ctx, http.MethodDelete, "/vocabularies/"+url.PathEscape(id), nil, nil, nil, opts...)
}
