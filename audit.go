package octonomy

import (
	"context"
	"net/http"
	"net/url"
	"time"
)

// AuditLog is one row of Octonomy's append-only mutation history: what changed,
// which entity it changed, who asked for it, and under which request and
// operation.
//
// It is READ-ONLY IN EVERY SENSE. The server writes these rows itself as a side
// effect of the mutation they describe, so there is no create, no update, no
// delete, and no Get -- the three list routes below are the whole surface. That
// is why this file carries no *Create / *Update structs: there is no write body
// to model.
//
// Reading them needs the audit:read scope, which is separate from tags:read -- a
// token without it gets a 403 (IsForbidden) from every route here while its
// ordinary reads keep working. See AuditLogService.
//
// Rows arrive NEWEST FIRST (the server orders by created_at descending, with id
// as a stable tiebreak), so offset paging walks backwards through history.
type AuditLog struct {
	ID            string  `json:"id"`
	TenantID      string  `json:"tenant_id"`
	ApplicationID *string `json:"application_id"`

	// NamespaceType and NamespaceID identify the merchant or sub-tenant namespace
	// the audited row belonged to; both are nil for a global (tenant-shared) row,
	// and on every /api/v1 response. Decode-only, as everywhere.
	//
	// They are also what makes an audit read namespace-filtered rather than
	// tenant-wide: see AuditLogService for the fail-closed rule.
	NamespaceType *string `json:"namespace_type"`
	NamespaceID   *string `json:"namespace_id"`

	// Action names the mutation, e.g. "tag.created", "tag.updated",
	// "tag.deactivated", "tag_alias.created", "vocabulary.updated",
	// "assignment.created", "assignment.removed". EntityType and EntityID
	// identify the row it happened to: EntityType is one of "tag", "tag_alias",
	// "vocabulary", or "tag_assignment", and EntityID is that row's id.
	//
	// EntityType and Action are spelled DIFFERENTLY for assignments -- the
	// entity is "tag_assignment" while its actions are "assignment.created" and
	// "assignment.removed" -- and both filters are exact matches, so
	// EntityType: String("assignment") returns an empty page rather than an
	// error.
	Action     string `json:"action"`
	EntityType string `json:"entity_type"`
	EntityID   string `json:"entity_id"`

	// TagID, ResourceType, and ResourceID are the denormalized handles the
	// server records so the tag and resource routes can filter without a join.
	// They are nil on rows where they do not apply: a vocabulary edit names no
	// tag, and only assignment rows name a resource.
	TagID        *string `json:"tag_id"`
	ResourceType *string `json:"resource_type"`
	ResourceID   *string `json:"resource_id"`

	// ActorID is who the mutation was attributed to -- Config.ActorID or
	// WithActor on the mutating call, falling back to the service client's own
	// name. It is nil only when neither was available.
	ActorID *string `json:"actor_id"`

	// RequestID correlates this row with the single HTTP request that produced
	// it, and appears on APIError for a failed one. The server takes it from an
	// inbound X-Request-ID header and generates a "req_..." value when there is
	// none -- and this SDK does not send one yet (#5), so today it is
	// server-generated and correlates rows to each other rather than to a
	// caller's own log line.
	RequestID *string `json:"request_id"`

	// OperationID groups every row one logical operation emitted, which is the
	// field that makes a multi-row mutation reconstructable. Resources.ReplaceTags
	// and both bulk calls emit one row per assignment they touch, all sharing an
	// OperationID, so the removals and additions read as one act rather than as
	// unrelated churn. Filter on it with AuditLogListParams.OperationID.
	OperationID string `json:"operation_id"`

	// Changes is the before/after record of the mutation, and it is deliberately
	// an untyped object rather than a Before/After struct.
	//
	// The shape the server writes today is {"before": {...}, "after": {...}}: a
	// create carries "after" alone, a deactivation "before" and "after" holding
	// just the changed fields, an update the changed fields on both sides. So
	// most rows read as Changes["after"].(map[string]any).
	//
	// BUT NOT ALL KEYS ARE OBJECTS, AND NOT ALL ROWS HAVE ONLY TWO. A
	// tag.deactivated row that cascaded to aliases adds "cascaded_alias_ids", an
	// ARRAY of alias ids, alongside the pair (octonomy/tags/services.py). That
	// single fact rules out both tidier types: a struct with Before and After
	// would drop the cascade silently, and a map[string]Metadata would fail to
	// decode the row outright -- taking the whole page down with it, since one
	// bad element fails the list. The contract types the field as nothing at all,
	// which is the honest description, so the SDK keeps it open.
	Changes Metadata `json:"changes"`

	// Metadata is an open object the server reserves for extra context. It is
	// empty on every row server 3.1.x writes today.
	Metadata  Metadata  `json:"metadata,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// identityFields makes a blank id an error, as on Tag (#40). Audit rows arrive
// only in list pages, where a blank row is least likely to be noticed.
func (a AuditLog) identityFields() []identityField {
	return []identityField{{name: "id", value: a.ID}}
}

// AuditLogListParams filters and pages the audit log collection. A nil *params
// lists with server defaults.
//
// Every filter is an EXACT match, not a search: Action is "tag.updated" and not
// "tag", EntityID is a full id. They combine with AND, and the server ignores a
// filter set to the empty string rather than matching rows whose value is empty.
//
// OperationID is the one to reach for when reconstructing a past mutation: read
// any row of it, then list by its OperationID to get every row the same
// operation wrote.
type AuditLogListParams struct {
	ListOptions
	Action        *string
	ActorID       *string
	ApplicationID *string
	EntityID      *string
	EntityType    *string
	OperationID   *string
	ResourceID    *string
	ResourceType  *string
	TagID         *string
}

func (p *AuditLogListParams) query() url.Values {
	q := url.Values{}
	if p == nil {
		return q
	}
	p.apply(q)
	if p.Action != nil {
		q.Set("action", *p.Action)
	}
	if p.ActorID != nil {
		q.Set("actor_id", *p.ActorID)
	}
	if p.ApplicationID != nil {
		q.Set(applicationIDParam, *p.ApplicationID)
	}
	if p.EntityID != nil {
		q.Set("entity_id", *p.EntityID)
	}
	if p.EntityType != nil {
		q.Set("entity_type", *p.EntityType)
	}
	if p.OperationID != nil {
		q.Set("operation_id", *p.OperationID)
	}
	if p.ResourceID != nil {
		q.Set("resource_id", *p.ResourceID)
	}
	if p.ResourceType != nil {
		q.Set("resource_type", *p.ResourceType)
	}
	if p.TagID != nil {
		q.Set("tag_id", *p.TagID)
	}
	return q
}

// TagListAuditLogsParams filters and pages TagService.ListAuditLogs. A nil
// *params lists with server defaults.
//
// It is NARROWER than AuditLogListParams, for the reason TagListAliasesParams is
// narrower than TagAliasListParams: the contract documents four filters on
// GET /tags/{tag_id}/audit-logs and nine on the collection. One filter function
// happens to serve every audit route today, so entity_type or resource_id would
// be honored here too -- but exposing them would put the SDK ahead of the
// published contract on a route the server is free to narrow, which is what the
// drift gate (#18) exists to catch. TagID has no meaning here at all: the path
// names the tag.
//
// ApplicationID is documented on the v2 route as the application scope a
// namespaced read must carry, and it filters as well. WithApplication supplies
// the same parameter; setting both to different values is a contradiction the
// transport reports rather than resolving.
type TagListAuditLogsParams struct {
	ListOptions
	Action        *string
	ActorID       *string
	ApplicationID *string
	OperationID   *string
}

func (p *TagListAuditLogsParams) query() url.Values {
	q := url.Values{}
	if p == nil {
		return q
	}
	p.apply(q)
	if p.Action != nil {
		q.Set("action", *p.Action)
	}
	if p.ActorID != nil {
		q.Set("actor_id", *p.ActorID)
	}
	if p.ApplicationID != nil {
		q.Set(applicationIDParam, *p.ApplicationID)
	}
	if p.OperationID != nil {
		q.Set("operation_id", *p.OperationID)
	}
	return q
}

// ResourceListAuditLogsParams filters and pages ResourceService.ListAuditLogs. A
// nil *params lists with server defaults.
//
// It carries the same four filters as TagListAuditLogsParams and is a separate
// type rather than a shared one: the two routes are specified separately in the
// contract and are free to diverge -- application_id is a plain documented
// filter here and the namespace scope parameter there -- and merging them now
// would make any future divergence a breaking change to a shared type.
// ResourceType and ResourceID are absent for the same reason TagID is absent
// there: the path names both.
type ResourceListAuditLogsParams struct {
	ListOptions
	Action        *string
	ActorID       *string
	ApplicationID *string
	OperationID   *string
}

func (p *ResourceListAuditLogsParams) query() url.Values {
	q := url.Values{}
	if p == nil {
		return q
	}
	p.apply(q)
	if p.Action != nil {
		q.Set("action", *p.Action)
	}
	if p.ActorID != nil {
		q.Set("actor_id", *p.ActorID)
	}
	if p.ApplicationID != nil {
		q.Set(applicationIDParam, *p.ApplicationID)
	}
	if p.OperationID != nil {
		q.Set("operation_id", *p.OperationID)
	}
	return q
}

// AuditLogService reads the append-only mutation history. Reach it via
// Client.AuditLogs.
//
// IT IS LIST-ONLY -- three routes, no Get and no writes. A single row is reached
// by filtering the collection, because the server publishes no /audit-logs/{id}.
//
// EVERY ROUTE REQUIRES THE audit:read SCOPE, which service tokens carry
// separately from tags:read and tags:write. A token without it gets a 403
// carrying the code forbidden (IsForbidden) -- not an empty page -- while the
// same token's tag and vocabulary reads keep working. It is a token
// misconfiguration rather than a caller mistake, so retrying or narrowing the
// filters will not help.
//
// ON /api/v2 AUDIT READS ARE NAMESPACE-FILTERED, AND GLOBAL ROWS FAIL CLOSED. A
// namespaced request (WithNamespace) sees that namespace's rows and, by default,
// no global ones; WithIncludeGlobal asks for both. It only widens what the
// request ASKS for: a token holding an exact merchant grant with no global
// authority still sees no global rows, and gets them silently absent rather than
// as an error. A global request -- no namespace headers -- sees global rows
// only, never a merchant's. There is no way to read across namespaces in one
// call, which is the point.
type AuditLogService struct {
	client *Client
}

// List returns a page of audit log rows (GET /audit-logs), newest first.
//
// This is the only route with the full filter set -- entity, resource, and tag
// handles as well as action, actor, application, and operation. Needs the
// audit:read scope; see AuditLogService for that and for the namespace rules.
func (s *AuditLogService) List(ctx context.Context, params *AuditLogListParams, opts ...RequestOption) (*List[AuditLog], error) {
	return doList[AuditLog](ctx, s.client, http.MethodGet, "/audit-logs", params.query(), opts...)
}

// ListAuditLogs returns a page of the audit rows recorded against one tag
// (GET /tags/{tag_id}/audit-logs), newest first.
//
// It lives here rather than in tags.go because everything it decodes is an audit
// row; the route is the same rows as AuditLogs.List, pre-filtered by the path.
//
// The rows it returns are those carrying this tag_id, which is broader than
// "edits to the tag itself": an assignment of the tag to a resource records the
// tag id too, so it appears here alongside the tag's own create and update rows.
// Filter by Action to separate them.
//
// UNLIKE THE TAG ROUTES IN tags.go, AN UNKNOWN TAG IS AN EMPTY PAGE, NOT A 404.
// The route filters the audit table by tag_id and never loads the tag, so a tag
// that does not exist, one that was deactivated, and one outside the request's
// namespace are all reported the same way: a 200 with no rows. Only a tagID that
// is not a uuid fails, and it fails at the server's router -- an envelope-less
// 404 that surfaces as IsUnexpectedStatus, never IsNotFound.
func (s *TagService) ListAuditLogs(ctx context.Context, tagID string, params *TagListAuditLogsParams, opts ...RequestOption) (*List[AuditLog], error) {
	return doList[AuditLog](ctx, s.client, http.MethodGet, "/tags/"+url.PathEscape(tagID)+"/audit-logs", params.query(), opts...)
}

// ListAuditLogs returns a page of the audit rows recorded against one external
// resource (GET /resources/{resource_type}/{resource_id}/audit-logs), newest
// first.
//
// These are assignment rows: what was tagged onto the resource and what was
// taken off, including every row a Resources.ReplaceTags emitted under one
// OperationID.
//
// As on the tag route, an unknown resource is an empty page rather than a 404 --
// nothing is loaded but the audit rows. A resource id containing a slash is
// unaddressable here for the reason resourcePath records, and for the same
// reason it is on the tag routes.
func (s *ResourceService) ListAuditLogs(ctx context.Context, resourceType, resourceID string, params *ResourceListAuditLogsParams, opts ...RequestOption) (*List[AuditLog], error) {
	return doList[AuditLog](ctx, s.client, http.MethodGet, resourcePath(resourceType, resourceID, "/audit-logs"), params.query(), opts...)
}
