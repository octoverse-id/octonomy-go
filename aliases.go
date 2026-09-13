package octonomy

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// TagAlias is an alternate identifier that resolves to a canonical tag. A tenant
// can reach one tag through several aliases -- an old slug kept alive after a
// rename, a partner's vocabulary, a localized spelling -- without duplicating the
// tag itself.
//
// An alias with a nil ApplicationID is shared across the tenant; otherwise it is
// scoped to a single application. The scope it may occupy is constrained by its
// target: an application-specific tag accepts aliases only in that same
// application (the server answers application_mismatch otherwise), because an
// alias in a second application would be a way around the tag's own application
// boundary.
type TagAlias struct {
	ID            string  `json:"id"`
	TenantID      string  `json:"tenant_id"`
	ApplicationID *string `json:"application_id"`

	// NamespaceType and NamespaceID identify the merchant or sub-tenant namespace
	// that owns this row; both are nil for a global (tenant-shared) row. They are
	// decode-only and appear on the v2 surface: the server sets them from the
	// X-Namespace-* headers at creation and never from the request body, and they
	// are fixed for the row's lifetime (attempting to change them is a 409
	// scope_immutable). /api/v1 responses omit them, so they decode to nil there.
	NamespaceType *string `json:"namespace_type"`
	NamespaceID   *string `json:"namespace_id"`

	// TagID is the canonical tag this alias resolves to. It is decode-only on the
	// response, but it is not immutable: TagAliasUpdate.TagID re-points an alias
	// at a different tag, which is a normal edit and not the scope change that
	// PATCH refuses.
	TagID     string    `json:"tag_id"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	Metadata  Metadata  `json:"metadata,omitempty"`
	IsActive  bool      `json:"is_active"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// identityFields makes a blank id an error, as on Tag (#40).
func (a TagAlias) identityFields() []identityField {
	return []identityField{{name: "id", value: a.ID}}
}

// TagAliasCreate is the request body for creating an alias. TagID, Name, and Slug
// are required; the remaining fields are optional.
//
// ApplicationID is authoritative here, as on every write -- WithApplication is
// refused on a body-carrying request. Leaving it nil creates a tenant-shared
// alias, which the server accepts only for a tenant-shared target tag.
type TagAliasCreate struct {
	ApplicationID *string  `json:"application_id,omitempty"`
	TagID         string   `json:"tag_id"`
	Name          string   `json:"name"`
	Slug          string   `json:"slug"`
	Metadata      Metadata `json:"metadata,omitempty"`
	IsActive      *bool    `json:"is_active,omitempty"`
}

// TagAliasUpdate is the PATCH body for updating an alias. Every field is an
// Optional: the zero value omits the key, Set sends a value, and Null sends an
// explicit JSON null. See TagUpdate for the three states and for the table of
// which nulls the server accepts.
//
// TagID re-points the alias at a different tag; the new target must satisfy the
// same compatibility rules a create does. ApplicationID is present because the
// server's patch schema carries it, but changing it is a 409 scope_immutable
// (IsScopeImmutable) -- see AliasService.Update.
//
// **No field on this struct is clearable.** ApplicationID is the only nullable
// property in PatchedTagAliasPatch, and nulling it clears nothing: on a row that
// HAS an application it is a 409 scope_immutable, and on one that is already
// tenant-shared it is a 200 no-op, because the server refuses a scope CHANGE
// rather than the literal null. Every other field answers 400 on a null, so Null
// belongs on none of them.
type TagAliasUpdate struct {
	ApplicationID Optional[string] `json:"application_id,omitzero"`
	TagID         Optional[string] `json:"tag_id,omitzero"`
	Name          Optional[string] `json:"name,omitzero"`
	Slug          Optional[string] `json:"slug,omitzero"`

	// Metadata REPLACES the stored object; it does not merge. Set(Metadata{})
	// empties it and the zero Optional omits the key. See TagUpdate.Metadata
	// for why Null is not the way to empty it (#37, #64).
	Metadata Optional[Metadata] `json:"metadata,omitzero"`

	IsActive Optional[bool] `json:"is_active,omitzero"`
}

// TagAliasListParams filters and pages the alias list. A nil *params lists with
// server defaults. Query maps to the server's free-text `q` parameter, which
// matches Name or Slug.
//
// IsActive is worth setting explicitly: the server filters to active aliases when
// the parameter is absent, so a nil IsActive lists active rows only, not every
// row. Since Delete is deactivation, IsActive: Bool(false) is how you find
// deleted aliases.
type TagAliasListParams struct {
	ListOptions
	ApplicationID *string
	IncludeShared *bool
	IsActive      *bool
	Query         *string
	Slug          *string
	TagID         *string
}

func (p *TagAliasListParams) query() url.Values {
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
	if p.TagID != nil {
		q.Set("tag_id", *p.TagID)
	}
	return q
}

// TagListAliasesParams filters and pages TagService.ListAliases. A nil *params
// lists with server defaults, and IsActive behaves as it does on
// TagAliasListParams: absent means active rows only.
//
// It is deliberately NARROWER than TagAliasListParams, which is the params type
// for the same rows reached through /tag-aliases. The contract documents five
// parameters on GET /tags/{tag_id}/aliases and eight on the collection; the
// server happens to run one filter function behind both routes, so `q` and `slug`
// would be honored here too. Exposing them anyway would put the SDK ahead of the
// published contract on a route the server is free to narrow, and it is the kind
// of divergence the contract drift gate exists to catch. TagID has no meaning
// here at all: the path names the tag.
type TagListAliasesParams struct {
	ListOptions
	ApplicationID *string
	IncludeShared *bool
	IsActive      *bool
}

func (p *TagListAliasesParams) query() url.Values {
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

// AliasService accesses the /tag-aliases endpoints. Reach it via Client.Aliases.
type AliasService struct {
	client *Client
}

// Create creates an alias (POST /tag-aliases).
//
// The target tag must exist, be active, and be scope-compatible with the alias:
// an application-specific tag takes aliases only in its own application
// (application_mismatch, IsApplicationMismatch), and a namespaced alias may
// target only a global or same-namespace tag. An inactive target is a plain
// validation_error (IsValidation) rather than inactive_tag, which the server
// reserves for assignment. A second active alias with the same tenant,
// application, and slug is a conflict (IsConflict).
func (s *AliasService) Create(ctx context.Context, in TagAliasCreate, opts ...RequestOption) (*TagAlias, error) {
	return doData[TagAlias](ctx, s.client, http.MethodPost, "/tag-aliases", nil, in, opts...)
}

// Get retrieves an alias by ID (GET /tag-aliases/{id}).
func (s *AliasService) Get(ctx context.Context, id string, opts ...RequestOption) (*TagAlias, error) {
	return doData[TagAlias](ctx, s.client, http.MethodGet, "/tag-aliases/"+url.PathEscape(id), nil, nil, opts...)
}

// List returns a page of aliases (GET /tag-aliases).
func (s *AliasService) List(ctx context.Context, params *TagAliasListParams, opts ...RequestOption) (*List[TagAlias], error) {
	return doList[TagAlias](ctx, s.client, http.MethodGet, "/tag-aliases", params.query(), opts...)
}

// Update partially updates an alias (PATCH /tag-aliases/{id}).
//
// Scope is fixed at creation: a body that changes ApplicationID, or the namespace
// the row already holds, is refused with a 409 that IsScopeImmutable matches.
// Re-create the alias in the target scope instead. Re-pointing the alias to a
// different tag through TagID is not a scope change and is allowed, subject to
// the same target rules as Create.
func (s *AliasService) Update(ctx context.Context, id string, in TagAliasUpdate, opts ...RequestOption) (*TagAlias, error) {
	return doData[TagAlias](ctx, s.client, http.MethodPatch, "/tag-aliases/"+url.PathEscape(id), nil, in, opts...)
}

// Delete deactivates an alias (DELETE /tag-aliases/{id}). Octonomy treats deletion
// as deactivation; the row and its history are retained, and the alias stops
// resolving. Deactivating the canonical tag cascades to its active aliases.
func (s *AliasService) Delete(ctx context.Context, id string, opts ...RequestOption) error {
	return s.client.do(ctx, http.MethodDelete, "/tag-aliases/"+url.PathEscape(id), nil, nil, opts...)
}

// ListAliases returns a page of the aliases pointing at one tag
// (GET /tags/{tag_id}/aliases).
//
// It lives here rather than in tags.go because everything it decodes is an alias;
// the route is the same rows as AliasService.List, pre-filtered by the path. A
// tag that does not exist -- or that the request's scope cannot see -- is a
// not_found for the TAG, not an empty page.
func (s *TagService) ListAliases(ctx context.Context, tagID string, params *TagListAliasesParams, opts ...RequestOption) (*List[TagAlias], error) {
	return doList[TagAlias](ctx, s.client, http.MethodGet, "/tags/"+url.PathEscape(tagID)+"/aliases", params.query(), opts...)
}
