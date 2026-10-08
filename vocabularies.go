package octonomy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Vocabulary is a tenant-scoped grouping for tags. A vocabulary with a nil
// ApplicationID is shared across all applications in the tenant; otherwise it is
// scoped to a single application.
type Vocabulary struct {
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
	Description   *string   `json:"description"`
	Metadata      Metadata  `json:"metadata,omitempty"`
	IsActive      bool      `json:"is_active"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// identityFields makes a blank id an error, as on Tag.
func (v Vocabulary) identityFields() []identityField {
	return []identityField{{name: "id", value: v.ID}}
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
//
// Metadata has three states -- a populated map replaces the stored object,
// Metadata{} sends {} and empties it, and nil omits the key -- for the reason
// TagUpdate records; MarshalJSON below is what makes Metadata{} reach the wire.
type VocabularyUpdate struct {
	ApplicationID *string  `json:"application_id,omitempty"`
	Name          *string  `json:"name,omitempty"`
	Slug          *string  `json:"slug,omitempty"`
	Description   *string  `json:"description,omitempty"`
	Metadata      Metadata `json:"metadata,omitempty"`
	IsActive      *bool    `json:"is_active,omitempty"`
}

// MarshalJSON encodes a VocabularyUpdate as its struct tags would, except that a
// non-nil Metadata is always sent, so Metadata{} goes out as "metadata":{}. It
// is on the VALUE receiver for the reason TagUpdate.MarshalJSON records.
func (u VocabularyUpdate) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		ApplicationID *string   `json:"application_id,omitempty"`
		Name          *string   `json:"name,omitempty"`
		Slug          *string   `json:"slug,omitempty"`
		Description   *string   `json:"description,omitempty"`
		Metadata      *Metadata `json:"metadata,omitempty"`
		IsActive      *bool     `json:"is_active,omitempty"`
	}{
		ApplicationID: u.ApplicationID,
		Name:          u.Name,
		Slug:          u.Slug,
		Description:   u.Description,
		Metadata:      sentMetadata(u.Metadata),
		IsActive:      u.IsActive,
	})
}

// VocabularyListParams filters and pages the vocabulary list. A nil *params lists
// with server defaults.
//
// Query and Slug are the same pair TagListParams carries, with the same
// server-side semantics: Slug is an exact match, and Query maps to the free-text
// `q` parameter, which matches name OR slug case-insensitively. Both vendored
// contracts list them on GET /vocabularies. This line gained them in #118, ported
// from main's #61 (which closed #36); until then a caller had to page the whole
// collection to find a vocabulary by slug.
type VocabularyListParams struct {
	ListOptions
	ApplicationID *string
	IncludeShared *bool
	IsActive      *bool
	Query         *string
	Slug          *string
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
	if p.Query != nil {
		q.Set("q", *p.Query)
	}
	if p.Slug != nil {
		q.Set("slug", *p.Slug)
	}
	return q
}

// VocabularyList is the envelope GET /vocabularies returns: {"data": [...],
// "pagination": {...}}. It is the Vocabulary instantiation of a shape that would
// otherwise be one generic type; see pagination.go for why this line spells it
// out per resource.
type VocabularyList struct {
	Data       []Vocabulary `json:"data"`
	Pagination Pagination   `json:"pagination"`
}

// rows hands doList the decoded vocabularies so it can check each one's
// identity.
func (l *VocabularyList) rows() []identifiedResource {
	rows := make([]identifiedResource, len(l.Data))
	for i := range l.Data {
		rows[i] = l.Data[i]
	}
	return rows
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
	if err := s.client.doList(ctx, http.MethodGet, "/vocabularies", params.query(), &out, opts...); err != nil {
		return nil, err
	}
	return &out, nil
}

// Update partially updates a vocabulary (PATCH /vocabularies/{id}).
//
// Scope is fixed at creation: a body that changes ApplicationID, or the namespace
// the row already holds, is refused with a 409 that IsScopeImmutable matches.
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
	return s.client.do(ctx, http.MethodDelete, "/vocabularies/"+url.PathEscape(id), nil, nil, opts...)
}
