package octonomy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// ResourceTag is one tag as seen FROM a resource: the tag itself plus the
// assignment that links it. It is the read side of Assignments.Create, projected
// so a caller listing a resource's tags gets the tag inline rather than a tag id
// to look up.
//
// AssignmentID is the id of the underlying Assignment, which is what
// Assignments.Remove and BulkRemove act on -- though both identify the row by
// (application, tag, resource) rather than by this id.
type ResourceTag struct {
	AssignmentID string `json:"assignment_id"`

	// NamespaceType and NamespaceID identify the merchant or sub-tenant namespace
	// that owns the assignment; both are nil for a global row, and on every
	// /api/v1 response. Decode-only, as everywhere.
	NamespaceType *string   `json:"namespace_type"`
	NamespaceID   *string   `json:"namespace_id"`
	AssignedBy    *string   `json:"assigned_by"`
	AssignedAt    time.Time `json:"assigned_at"`
	Tag           Tag       `json:"tag"`
}

// TagResource is one resource as seen FROM a tag -- the mirror of ResourceTag,
// and the reason the two exist separately. It carries no tag, because the tag is
// what you started from, and no assignment id, because the route answers "what
// is this tag on" rather than "which links exist".
type TagResource struct {
	ApplicationID string `json:"application_id"`

	// NamespaceType and NamespaceID identify the merchant or sub-tenant namespace
	// that owns the assignment; both are nil for a global row.
	NamespaceType *string   `json:"namespace_type"`
	NamespaceID   *string   `json:"namespace_id"`
	ResourceType  string    `json:"resource_type"`
	ResourceID    string    `json:"resource_id"`
	AssignedBy    *string   `json:"assigned_by"`
	AssignedAt    time.Time `json:"assigned_at"`
}

// ResourceReplace is the request body for replacing a resource's whole tag set.
//
// It names no resource: ResourceType and ResourceID come from the PATH, and the
// server overwrites whatever a body carries for them with the path values. The
// contract lists them on this schema all the same, which is why they are absent
// here -- a field that cannot affect the request does not belong on it.
//
// TagIDs and AliasSlugs are unioned, exactly as on BulkAssign. Unlike BulkAssign,
// though, BOTH MAY BE EMPTY, and that is not a validation error -- see
// ResourceService.ReplaceTags, because it is the destructive case.
type ResourceReplace struct {
	ApplicationID string   `json:"application_id"`
	TagIDs        []string `json:"tag_ids,omitempty"`
	AliasSlugs    []string `json:"alias_slugs,omitempty"`
	AssignedBy    *string  `json:"assigned_by,omitempty"`
}

// ResourceReplaceResult reports what a replace changed: how many assignments it
// added, how many it deleted, and the resource's resulting tag set.
//
// Tags is []Tag and NOT []ResourceTag, which is worth reading twice -- the
// replace answers with the tags themselves, not with the assignments that link
// them, so there is no AssignmentID or AssignedAt here. UsageCount on each Tag is
// populated by the server for this response.
//
// Created and Removed are counts of assignments, so a replace that swaps one tag
// for another reports 1 and 1 while Tags has the same length as before.
type ResourceReplaceResult struct {
	Created int   `json:"created"`
	Removed int   `json:"removed"`
	Tags    []Tag `json:"tags"`
}

// UnmarshalJSON requires the keys a caller acts on, for the reason given on
// BulkAssignResult: this is a composite of counters, where the zero value is a
// perfectly ordinary answer. Created 0, Removed 0 is what a replace reports when
// the requested set already matched, so a body whose keys the server renamed
// would read as "nothing needed changing" rather than as the contract break it
// is. A present-but-null tags array normalizes to an empty non-nil slice, as
// doList does for a null page.
func (r *ResourceReplaceResult) UnmarshalJSON(data []byte) error {
	var wire struct {
		Created *int            `json:"created"`
		Removed *int            `json:"removed"`
		Tags    json.RawMessage `json:"tags"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	switch {
	case wire.Created == nil:
		return fmt.Errorf(`octonomy: replace response has no "created" count`)
	case wire.Removed == nil:
		return fmt.Errorf(`octonomy: replace response has no "removed" count`)
	case wire.Tags == nil:
		return fmt.Errorf(`octonomy: replace response has no "tags" array`)
	}
	out := ResourceReplaceResult{Created: *wire.Created, Removed: *wire.Removed}
	if err := json.Unmarshal(wire.Tags, &out.Tags); err != nil {
		return fmt.Errorf("octonomy: decode replace tags: %w", err)
	}
	if out.Tags == nil {
		out.Tags = []Tag{}
	}
	*r = out
	return nil
}

// ResourceListTagsParams filters and pages a resource's tag list.
//
// ApplicationID is REQUIRED on this route -- the only list in this SDK where that
// is true -- and a request without it is a validation_error naming the parameter.
// Set it here, or with WithApplication; setting both to different values is a
// contradiction rather than a precedence question. A nil *params therefore always
// fails, which is deliberate: the alternative is inventing a default application,
// and there is no safe one to invent.
//
// IncludeInactive is NOT the is_active filter the tag and alias lists take. It is
// a different parameter with different polarity: nil or false returns only
// assignments whose tag is active, and true widens to include deactivated tags.
// There is no way to ask for deactivated tags ALONE.
type ResourceListTagsParams struct {
	ListOptions
	ApplicationID   *string
	IncludeInactive *bool
	Type            *string
}

func (p *ResourceListTagsParams) query() url.Values {
	q := url.Values{}
	if p == nil {
		return q
	}
	p.apply(q)
	if p.ApplicationID != nil {
		q.Set(applicationIDParam, *p.ApplicationID)
	}
	if p.IncludeInactive != nil {
		q.Set("include_inactive", strconv.FormatBool(*p.IncludeInactive))
	}
	if p.Type != nil {
		q.Set("type", *p.Type)
	}
	return q
}

// TagListResourcesParams filters and pages the resources a tag is on. Unlike
// ResourceListTagsParams, ApplicationID is optional here: with it unset the list
// spans every application the caller can see.
type TagListResourcesParams struct {
	ListOptions
	ApplicationID *string
	ResourceType  *string
}

func (p *TagListResourcesParams) query() url.Values {
	q := url.Values{}
	if p == nil {
		return q
	}
	p.apply(q)
	if p.ApplicationID != nil {
		q.Set(applicationIDParam, *p.ApplicationID)
	}
	if p.ResourceType != nil {
		q.Set("resource_type", *p.ResourceType)
	}
	return q
}

// ResourceService accesses the /resources endpoints, which look at tagging from
// the resource's side. Reach it via Client.Resources.
type ResourceService struct {
	client *Client
}

func resourcePath(resourceType, resourceID, suffix string) string {
	return "/resources/" + url.PathEscape(resourceType) + "/" + url.PathEscape(resourceID) + suffix
}

// ListTags returns a page of the tags on one resource
// (GET /resources/{resource_type}/{resource_id}/tags).
//
// params must supply ApplicationID, or the call must carry WithApplication: the
// server requires it here and answers a validation_error without it.
//
// Deactivated tags are excluded unless ResourceListTagsParams.IncludeInactive is
// true. Since Tags.Delete is deactivation, a tag "deleted" after being assigned
// keeps its assignment and simply stops appearing here.
func (s *ResourceService) ListTags(ctx context.Context, resourceType, resourceID string, params *ResourceListTagsParams, opts ...RequestOption) (*List[ResourceTag], error) {
	return doList[ResourceTag](ctx, s.client, http.MethodGet, resourcePath(resourceType, resourceID, "/tags"), params.query(), opts...)
}

// ReplaceTags sets a resource's tag set to exactly what the body names
// (POST /resources/{resource_type}/{resource_id}/tags).
//
// THIS REPLACES, IT DOES NOT MERGE. Every tag currently on the resource and
// absent from the request is REMOVED. Reading the current set, appending to it,
// and sending the result is the merge; sending only the additions deletes
// everything else.
//
// AN EMPTY REQUEST IS LEGAL AND CLEARS THE RESOURCE. ResourceReplace with no
// TagIDs and no AliasSlugs removes every tag and returns Created 0 with Removed
// equal to what was there. That is a deliberate difference from BulkAssign, which
// refuses an empty request -- so an empty slice that reached this call by
// accident, from a filter that matched nothing, wipes the resource silently and
// successfully.
//
// The whole operation is one atomic unit and shares a single operation_id across
// every audit and outbox event it emits, so the removals and additions can be
// correlated as one act rather than read as unrelated churn.
//
// The response is a COMPOSITE under the data envelope, and docs/openapi-v2.yaml
// is wrong about it twice over: it claims a bare array, and it claims the
// elements are ResourceTag. Neither holds -- the server sends
// {"data": {"created": N, "removed": N, "tags": [...]}}, and those tags are Tag
// values. A client written from the spec decodes an empty slice and a nil error,
// which is #32; one that guessed the envelope but kept the element type would
// decode Tags with every field empty.
func (s *ResourceService) ReplaceTags(ctx context.Context, resourceType, resourceID string, in ResourceReplace, opts ...RequestOption) (*ResourceReplaceResult, error) {
	return doData[ResourceReplaceResult](ctx, s.client, http.MethodPost, resourcePath(resourceType, resourceID, "/tags"), nil, in, opts...)
}

// ListResources returns a page of the resources one tag is assigned to
// (GET /tags/{tag_id}/resources).
//
// It lives here rather than in tags.go because everything it decodes belongs to
// the resource side. A tag the request's scope cannot see is a not_found for the
// TAG, not an empty page.
func (s *TagService) ListResources(ctx context.Context, tagID string, params *TagListResourcesParams, opts ...RequestOption) (*List[TagResource], error) {
	return doList[TagResource](ctx, s.client, http.MethodGet, "/tags/"+url.PathEscape(tagID)+"/resources", params.query(), opts...)
}
