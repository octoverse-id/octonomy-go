package main

import (
	"context"
	"strings"

	octonomy "github.com/octoverse-id/octonomy-go/v2"
)

// One call per operation the contract publishes, with every parameter the SDK
// offers populated.
//
// This table is the executable half of docs/contract-coverage.yaml. The YAML says
// which operations exist and what is deliberately not implemented; this says what
// the client does about each one, in the only form that cannot drift from the
// truth -- it calls the method. A driver that names the wrong method reports the
// wrong route on the first run; a driver that stops compiling is a build failure;
// and an operation with no driver is a finding, exactly as an operation with no
// coverage row is.
//
// EVERY FIELD IS SET, on purpose, and it is CHECKED rather than promised. The
// request the client emits is what the gate compares against the contract, in
// both directions -- documented query parameters, documented headers, and the
// properties of the documented request schema -- so a field left unset here shows
// up as something the contract documents and the client did not send. An earlier
// version of this comment claimed completeness while three PATCH drivers set only
// `Name`; that is the shape of promise a review should not have to take on trust,
// and now it does not.
//
// Where the client genuinely cannot send a documented parameter -- `q` and `slug`
// on vocabularies, which VocabularyListParams has no field for (#36) -- that IS
// the finding, and it belongs in the YAML's unsent_query_parameters with a reason.
//
// The scope options need care rather than uniformity, and the reasons are the
// SDK's own rules (AGENTS.md): WithApplication is refused on a request with a
// body, because the body's ApplicationID is authoritative there; WithIncludeGlobal
// is refused on writes, because the server reads it only on safe methods; and it
// contradicts an explicit merchant scope on /tag-resolution. A driver that
// ignored those would fail before reaching the wire.

func str(s string) *string    { return &s }
func boolp(b bool) *bool      { return &b }
func meta() octonomy.Metadata { return octonomy.Metadata{"contractdrift": "value"} }
func metap() *octonomy.Metadata {
	m := meta()
	return &m
}

func listOptions() octonomy.ListOptions {
	return octonomy.ListOptions{Limit: 1, Offset: 2}
}

// namespaced is the option every operation carries, so the X-Namespace-* headers
// the v2 contract documents on all of them are actually on the wire and can be
// compared. Without it those headers are simply absent, and a method that stopped
// propagating them would look the same as one that never could.
func namespaced() octonomy.RequestOption {
	return octonomy.WithNamespace(driverValue("x-namespace-type"), driverValue("x-namespace-id"))
}

// readScope is what every bodyless read carries: an application (required on a
// namespaced bodyless request), the namespace pair, and the opt-in that widens a
// namespaced read back to global rows.
func readScope() []octonomy.RequestOption {
	return []octonomy.RequestOption{
		octonomy.WithApplication(driverValue("application_id")),
		namespaced(),
		octonomy.WithIncludeGlobal(),
	}
}

// Drivers is the table. Order is the report's order.
func Drivers() []Driver {
	return []Driver{
		// --- Tags -------------------------------------------------------------
		{Op: "get /tags", SDK: "TagService.List", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Tags.List(ctx, &octonomy.TagListParams{
				ListOptions:   listOptions(),
				ApplicationID: str(driverValue("application_id")),
				IncludeShared: boolp(true),
				IsActive:      boolp(true),
				ParentID:      str(driverValue("parent_id")),
				Query:         str(driverValue("q")),
				Slug:          str(driverValue("slug")),
				Type:          str(driverValue("type")),
				VocabularyID:  str(driverValue("vocabulary_id")),
			}, namespaced(), octonomy.WithIncludeGlobal())
		}},
		{Op: "post /tags", SDK: "TagService.Create", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Tags.Create(ctx, octonomy.TagCreate{
				ApplicationID: str(driverValue("application_id")), Name: driverValue("name"), Slug: driverValue("slug"), Type: driverValue("type"),
				Description: str(driverValue("description")), ParentID: str(driverValue("parent_id")),
				VocabularyID: str(driverValue("vocabulary_id")),
				Metadata:     meta(), IsActive: boolp(true),
			}, namespaced())
		}},
		{Op: "get /tags/{tag_id}", SDK: "TagService.Get", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Tags.Get(ctx, env.Path("tag_id"), readScope()...)
		}},
		{Op: "patch /tags/{tag_id}", SDK: "TagService.Update", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Tags.Update(ctx, env.Path("tag_id"), octonomy.TagUpdate{
				ApplicationID: str(driverValue("application_id")), Name: str(driverValue("name")), Slug: str(driverValue("slug")), Type: str(driverValue("type")),
				Description: str(driverValue("description")), ParentID: str(driverValue("parent_id")),
				VocabularyID: str(driverValue("vocabulary_id")),
				Metadata:     metap(), IsActive: boolp(true),
			}, namespaced())
		}},
		{Op: "delete /tags/{tag_id}", SDK: "TagService.Delete", Call: func(ctx context.Context, env *Env) (any, error) {
			return nil, env.Client.Tags.Delete(ctx, env.Path("tag_id"), octonomy.WithApplication(driverValue("application_id")), namespaced())
		}},

		// --- Vocabularies -----------------------------------------------------
		{Op: "get /vocabularies", SDK: "VocabularyService.List", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Vocabularies.List(ctx, &octonomy.VocabularyListParams{
				ListOptions:   listOptions(),
				ApplicationID: str(driverValue("application_id")),
				IncludeShared: boolp(true),
				IsActive:      boolp(true),
			}, namespaced(), octonomy.WithIncludeGlobal())
		}},
		{Op: "post /vocabularies", SDK: "VocabularyService.Create", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Vocabularies.Create(ctx, octonomy.VocabularyCreate{
				ApplicationID: str(driverValue("application_id")), Name: driverValue("name"), Slug: driverValue("slug"), Description: str(driverValue("description")),
				Metadata: meta(), IsActive: boolp(true),
			}, namespaced())
		}},
		{Op: "get /vocabularies/{vocabulary_id}", SDK: "VocabularyService.Get", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Vocabularies.Get(ctx, env.Path("vocabulary_id"), readScope()...)
		}},
		{Op: "patch /vocabularies/{vocabulary_id}", SDK: "VocabularyService.Update", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Vocabularies.Update(ctx, env.Path("vocabulary_id"), octonomy.VocabularyUpdate{
				ApplicationID: str(driverValue("application_id")), Name: str(driverValue("name")), Slug: str(driverValue("slug")), Description: str(driverValue("description")),
				Metadata: metap(), IsActive: boolp(true),
			}, namespaced())
		}},
		{Op: "delete /vocabularies/{vocabulary_id}", SDK: "VocabularyService.Delete", Call: func(ctx context.Context, env *Env) (any, error) {
			return nil, env.Client.Vocabularies.Delete(ctx, env.Path("vocabulary_id"), octonomy.WithApplication(driverValue("application_id")), namespaced())
		}},

		// --- Tag aliases ------------------------------------------------------
		{Op: "get /tag-aliases", SDK: "AliasService.List", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Aliases.List(ctx, &octonomy.TagAliasListParams{
				ListOptions:   listOptions(),
				ApplicationID: str(driverValue("application_id")),
				IncludeShared: boolp(true),
				IsActive:      boolp(true),
				Query:         str(driverValue("q")),
				Slug:          str(driverValue("slug")),
				TagID:         str(driverValue("tag_id")),
			}, namespaced(), octonomy.WithIncludeGlobal())
		}},
		{Op: "post /tag-aliases", SDK: "AliasService.Create", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Aliases.Create(ctx, octonomy.TagAliasCreate{
				ApplicationID: str(driverValue("application_id")), TagID: driverValue("tag_id"), Name: driverValue("name"), Slug: driverValue("slug"),
				Metadata: meta(), IsActive: boolp(true),
			}, namespaced())
		}},
		{Op: "get /tag-aliases/{alias_id}", SDK: "AliasService.Get", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Aliases.Get(ctx, env.Path("alias_id"), readScope()...)
		}},
		{Op: "patch /tag-aliases/{alias_id}", SDK: "AliasService.Update", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Aliases.Update(ctx, env.Path("alias_id"), octonomy.TagAliasUpdate{
				ApplicationID: str(driverValue("application_id")), TagID: str(driverValue("tag_id")), Name: str(driverValue("name")), Slug: str(driverValue("slug")),
				Metadata: metap(), IsActive: boolp(true),
			}, namespaced())
		}},
		{Op: "delete /tag-aliases/{alias_id}", SDK: "AliasService.Delete", Call: func(ctx context.Context, env *Env) (any, error) {
			return nil, env.Client.Aliases.Delete(ctx, env.Path("alias_id"), octonomy.WithApplication(driverValue("application_id")), namespaced())
		}},
		{Op: "get /tags/{tag_id}/aliases", SDK: "TagService.ListAliases", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Tags.ListAliases(ctx, env.Path("tag_id"), &octonomy.TagListAliasesParams{
				ListOptions:   listOptions(),
				ApplicationID: str(driverValue("application_id")),
				IncludeShared: boolp(true),
				IsActive:      boolp(true),
			}, namespaced(), octonomy.WithIncludeGlobal())
		}},

		// --- Tag resolution ---------------------------------------------------
		//
		// `scope` is the parameter this whole gate exists because of. It is pinned
		// to global rather than merchant so WithIncludeGlobal can ride along: the
		// SDK refuses that pair against a MERCHANT scope, which resolves inside the
		// namespace and excludes global rows by definition, and the server would
		// drop one of the two in silence.
		{Op: "get /tag-resolution", SDK: "TagService.Resolve", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Tags.Resolve(ctx, driverValue("slug"), &octonomy.TagResolveParams{
				ApplicationID: str(driverValue("application_id")),
				Type:          str(driverValue("type")),
				Scope:         octonomy.ResolutionScopeGlobal,
			}, namespaced(), octonomy.WithIncludeGlobal())
		}},

		// --- Assignments ------------------------------------------------------
		{Op: "post /tag-assignments", SDK: "AssignmentService.Create", Call: func(ctx context.Context, env *Env) (any, error) {
			// TagID, AliasID and AliasSlug are three ways to name the same tag and
			// the server takes one; the contract documents all three, so all three
			// are sent and the server's own mutual-exclusion rule is its business,
			// not this SDK's (AGENTS.md: the SDK adds ergonomics, not validation).
			return env.Client.Assignments.Create(ctx, octonomy.AssignmentCreate{
				ApplicationID: driverValue("application_id"), TagID: str(driverValue("tag_id")), AliasID: str(driverValue("alias_id")),
				AliasSlug:    str(driverValue("alias_slug")),
				ResourceType: driverValue("resource_type"), ResourceID: driverValue("resource_id"),
				AssignedBy: str(driverValue("assigned_by")),
			}, namespaced())
		}},
		{Op: "delete /tag-assignments", SDK: "AssignmentService.Remove", Call: func(ctx context.Context, env *Env) (any, error) {
			return nil, env.Client.Assignments.Remove(ctx, octonomy.AssignmentRemove{
				ApplicationID: driverValue("application_id"), TagID: driverValue("tag_id"),
				ResourceType: driverValue("resource_type"), ResourceID: driverValue("resource_id"),
			}, namespaced())
		}},
		{Op: "post /tag-assignments/bulk-assign", SDK: "AssignmentService.BulkAssign", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Assignments.BulkAssign(ctx, octonomy.BulkAssign{
				ApplicationID: driverValue("application_id"), ResourceType: driverValue("resource_type"), ResourceID: driverValue("resource_id"),
				TagIDs: []string{driverValue("tag_ids")}, AliasSlugs: []string{driverValue("alias_slugs")},
				AssignedBy: str(driverValue("assigned_by")),
			}, namespaced())
		}},
		{Op: "post /tag-assignments/bulk-remove", SDK: "AssignmentService.BulkRemove", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Assignments.BulkRemove(ctx, octonomy.BulkRemove{
				ApplicationID: driverValue("application_id"), ResourceType: driverValue("resource_type"), ResourceID: driverValue("resource_id"),
				TagIDs: []string{driverValue("tag_ids")},
			}, namespaced())
		}},

		// --- Resource tags ----------------------------------------------------
		{Op: "get /resources/{resource_type}/{resource_id}/tags", SDK: "ResourceService.ListTags", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Resources.ListTags(ctx, env.Path("resource_type"), env.Path("resource_id"), &octonomy.ResourceListTagsParams{
				ListOptions:     listOptions(),
				ApplicationID:   str("app"),
				IncludeInactive: boolp(true),
				Type:            str("type"),
			}, namespaced(), octonomy.WithIncludeGlobal())
		}},
		{Op: "post /resources/{resource_type}/{resource_id}/tags", SDK: "ResourceService.ReplaceTags", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Resources.ReplaceTags(ctx, env.Path("resource_type"), env.Path("resource_id"), octonomy.ResourceReplace{
				ApplicationID: driverValue("application_id"), TagIDs: []string{driverValue("tag_ids")}, AliasSlugs: []string{driverValue("alias_slugs")},
				AssignedBy: str(driverValue("assigned_by")),
			}, namespaced())
		}},
		{Op: "get /tags/{tag_id}/resources", SDK: "TagService.ListResources", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Tags.ListResources(ctx, env.Path("tag_id"), &octonomy.TagListResourcesParams{
				ListOptions:   listOptions(),
				ApplicationID: str(driverValue("application_id")),
				ResourceType:  str(driverValue("resource_type")),
			}, namespaced(), octonomy.WithIncludeGlobal())
		}},

		// --- Audit logs -------------------------------------------------------
		{Op: "get /audit-logs", SDK: "AuditLogService.List", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.AuditLogs.List(ctx, &octonomy.AuditLogListParams{
				ListOptions:   listOptions(),
				Action:        str(driverValue("action")),
				ActorID:       str(driverValue("actor_id")),
				ApplicationID: str(driverValue("application_id")),
				EntityID:      str(driverValue("entity_id")),
				EntityType:    str(driverValue("entity_type")),
				OperationID:   str(driverValue("operation_id")),
				ResourceID:    str(driverValue("resource_id")),
				ResourceType:  str(driverValue("resource_type")),
				TagID:         str(driverValue("tag_id")),
			}, namespaced(), octonomy.WithIncludeGlobal())
		}},
		{Op: "get /tags/{tag_id}/audit-logs", SDK: "TagService.ListAuditLogs", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Tags.ListAuditLogs(ctx, env.Path("tag_id"), &octonomy.TagListAuditLogsParams{
				ListOptions:   listOptions(),
				Action:        str(driverValue("action")),
				ActorID:       str(driverValue("actor_id")),
				ApplicationID: str(driverValue("application_id")),
				OperationID:   str(driverValue("operation_id")),
			}, namespaced(), octonomy.WithIncludeGlobal())
		}},
		{Op: "get /resources/{resource_type}/{resource_id}/audit-logs", SDK: "ResourceService.ListAuditLogs", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Resources.ListAuditLogs(ctx, env.Path("resource_type"), env.Path("resource_id"), &octonomy.ResourceListAuditLogsParams{
				ListOptions:   listOptions(),
				Action:        str(driverValue("action")),
				ActorID:       str(driverValue("actor_id")),
				ApplicationID: str(driverValue("application_id")),
				OperationID:   str(driverValue("operation_id")),
			}, namespaced(), octonomy.WithIncludeGlobal())
		}},

		// --- Health -----------------------------------------------------------
		//
		// The credential-free client, because these routes authenticate nobody and
		// sit outside /api/<version> entirely.
		{Op: "get /health/live", SDK: "HealthService.Live", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Health.Health.Live(ctx)
		}},
		{Op: "get /health/ready", SDK: "HealthService.Ready", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Health.Health.Ready(ctx)
		}},
	}
}

// --- self-identifying values ------------------------------------------------------
//
// Every string a driver supplies names the WIRE FIELD it is supposed to arrive as.
// `driverValue("slug")` is what goes into the field that should reach the server as
// `slug`, wherever that is -- a query parameter, a body property, a header.
//
// That one convention is what lets the gate check values without a schema
// validator, and it closes four mutations a name-only comparison waved through: a
// params struct wiring `q` to the Slug field, two JSON tags swapped on a write
// model, the two namespace headers crossed, and a parameter retyped in the contract
// while the client kept sending a string. Under each of those, a value turns up
// under a name that is not its own and says so.
//
// Values that cannot be arbitrary strings -- limit, offset, the booleans -- carry no
// sentinel and are checked against the documented type instead.
const driverValuePrefix = "cd~"

func driverValue(wireName string) string { return driverValuePrefix + wireName }

// sentinelOrigin reports the wire field a driver-supplied value was meant for.
func sentinelOrigin(value string) (string, bool) {
	return strings.CutPrefix(value, driverValuePrefix)
}
