package main

import (
	"context"

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
// EVERY OPTIONAL PARAMETER IS SET, on purpose. The request the client emits is
// what the gate compares against the contract's documented query parameters, so a
// field left unset here would read as a parameter the SDK cannot send. Where the
// client genuinely cannot send a documented parameter -- `q` and `slug` on
// vocabularies, which VocabularyListParams has no field for (#36) -- that IS the
// finding, and it belongs in the YAML's unsent_query_parameters with its reason.
//
// The scope options need care rather than uniformity, and the reasons are the
// SDK's own rules (AGENTS.md): WithApplication is refused on a request with a
// body, because the body's ApplicationID is authoritative there; WithIncludeGlobal
// is refused on writes, because the server reads it only on safe methods; and it
// contradicts an explicit merchant scope on /tag-resolution. A driver that
// ignored those would fail before reaching the wire.

// Sentinel path segments. The stub reduces these to {} so an observed path
// compares with a documented one.
const (
	seg1 = "SEG1"
	seg2 = "SEG2"
)

func str(s string) *string { return &s }
func boolp(b bool) *bool   { return &b }

func listOptions() octonomy.ListOptions {
	return octonomy.ListOptions{Limit: 1, Offset: 2}
}

// readScope is the option pair every bodyless read can carry: an application, and
// the opt-in that widens a namespaced read back to global rows.
func readScope() []octonomy.RequestOption {
	return []octonomy.RequestOption{
		octonomy.WithApplication("app"),
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
				ApplicationID: str("app"),
				IncludeShared: boolp(true),
				IsActive:      boolp(true),
				ParentID:      str("parent"),
				Query:         str("q"),
				Slug:          str("slug"),
				Type:          str("type"),
				VocabularyID:  str("vocab"),
			}, octonomy.WithIncludeGlobal())
		}},
		{Op: "post /tags", SDK: "TagService.Create", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Tags.Create(ctx, octonomy.TagCreate{
				ApplicationID: str("app"), Name: "n", Slug: "s", Type: "t",
				Description: str("d"), ParentID: str("p"), VocabularyID: str("v"),
				Metadata: octonomy.Metadata{"k": "v"}, IsActive: boolp(true),
			})
		}},
		{Op: "get /tags/{tag_id}", SDK: "TagService.Get", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Tags.Get(ctx, seg1, readScope()...)
		}},
		{Op: "patch /tags/{tag_id}", SDK: "TagService.Update", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Tags.Update(ctx, seg1, octonomy.TagUpdate{Name: str("n")})
		}},
		{Op: "delete /tags/{tag_id}", SDK: "TagService.Delete", Call: func(ctx context.Context, env *Env) (any, error) {
			return nil, env.Client.Tags.Delete(ctx, seg1, octonomy.WithApplication("app"))
		}},

		// --- Vocabularies -----------------------------------------------------
		{Op: "get /vocabularies", SDK: "VocabularyService.List", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Vocabularies.List(ctx, &octonomy.VocabularyListParams{
				ListOptions:   listOptions(),
				ApplicationID: str("app"),
				IncludeShared: boolp(true),
				IsActive:      boolp(true),
			}, octonomy.WithIncludeGlobal())
		}},
		{Op: "post /vocabularies", SDK: "VocabularyService.Create", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Vocabularies.Create(ctx, octonomy.VocabularyCreate{
				ApplicationID: str("app"), Name: "n", Slug: "s", Description: str("d"),
				Metadata: octonomy.Metadata{"k": "v"}, IsActive: boolp(true),
			})
		}},
		{Op: "get /vocabularies/{vocabulary_id}", SDK: "VocabularyService.Get", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Vocabularies.Get(ctx, seg1, readScope()...)
		}},
		{Op: "patch /vocabularies/{vocabulary_id}", SDK: "VocabularyService.Update", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Vocabularies.Update(ctx, seg1, octonomy.VocabularyUpdate{Name: str("n")})
		}},
		{Op: "delete /vocabularies/{vocabulary_id}", SDK: "VocabularyService.Delete", Call: func(ctx context.Context, env *Env) (any, error) {
			return nil, env.Client.Vocabularies.Delete(ctx, seg1, octonomy.WithApplication("app"))
		}},

		// --- Tag aliases ------------------------------------------------------
		{Op: "get /tag-aliases", SDK: "AliasService.List", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Aliases.List(ctx, &octonomy.TagAliasListParams{
				ListOptions:   listOptions(),
				ApplicationID: str("app"),
				IncludeShared: boolp(true),
				IsActive:      boolp(true),
				Query:         str("q"),
				Slug:          str("slug"),
				TagID:         str("tag"),
			}, octonomy.WithIncludeGlobal())
		}},
		{Op: "post /tag-aliases", SDK: "AliasService.Create", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Aliases.Create(ctx, octonomy.TagAliasCreate{
				ApplicationID: str("app"), TagID: "tag", Name: "n", Slug: "s",
				Metadata: octonomy.Metadata{"k": "v"}, IsActive: boolp(true),
			})
		}},
		{Op: "get /tag-aliases/{alias_id}", SDK: "AliasService.Get", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Aliases.Get(ctx, seg1, readScope()...)
		}},
		{Op: "patch /tag-aliases/{alias_id}", SDK: "AliasService.Update", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Aliases.Update(ctx, seg1, octonomy.TagAliasUpdate{Name: str("n")})
		}},
		{Op: "delete /tag-aliases/{alias_id}", SDK: "AliasService.Delete", Call: func(ctx context.Context, env *Env) (any, error) {
			return nil, env.Client.Aliases.Delete(ctx, seg1, octonomy.WithApplication("app"))
		}},
		{Op: "get /tags/{tag_id}/aliases", SDK: "TagService.ListAliases", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Tags.ListAliases(ctx, seg1, &octonomy.TagListAliasesParams{
				ListOptions:   listOptions(),
				ApplicationID: str("app"),
				IncludeShared: boolp(true),
				IsActive:      boolp(true),
			}, octonomy.WithIncludeGlobal())
		}},

		// --- Tag resolution ---------------------------------------------------
		//
		// `scope` is the parameter this whole gate exists because of. It is pinned
		// to global rather than merchant so WithIncludeGlobal can ride along: the
		// SDK refuses that pair against a MERCHANT scope, which resolves inside the
		// namespace and excludes global rows by definition, and the server would
		// drop one of the two in silence.
		{Op: "get /tag-resolution", SDK: "TagService.Resolve", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Tags.Resolve(ctx, "slug", &octonomy.TagResolveParams{
				ApplicationID: str("app"),
				Type:          str("type"),
				Scope:         octonomy.ResolutionScopeGlobal,
			}, octonomy.WithIncludeGlobal())
		}},

		// --- Assignments ------------------------------------------------------
		{Op: "post /tag-assignments", SDK: "AssignmentService.Create", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Assignments.Create(ctx, octonomy.AssignmentCreate{
				ApplicationID: "app", TagID: str("tag"),
				ResourceType: "rt", ResourceID: "ri", AssignedBy: str("who"),
			})
		}},
		{Op: "delete /tag-assignments", SDK: "AssignmentService.Remove", Call: func(ctx context.Context, env *Env) (any, error) {
			return nil, env.Client.Assignments.Remove(ctx, octonomy.AssignmentRemove{
				ApplicationID: "app", TagID: "tag", ResourceType: "rt", ResourceID: "ri",
			})
		}},
		{Op: "post /tag-assignments/bulk-assign", SDK: "AssignmentService.BulkAssign", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Assignments.BulkAssign(ctx, octonomy.BulkAssign{
				ApplicationID: "app", ResourceType: "rt", ResourceID: "ri",
				TagIDs: []string{"tag"}, AssignedBy: str("who"),
			})
		}},
		{Op: "post /tag-assignments/bulk-remove", SDK: "AssignmentService.BulkRemove", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Assignments.BulkRemove(ctx, octonomy.BulkRemove{
				ApplicationID: "app", ResourceType: "rt", ResourceID: "ri",
				TagIDs: []string{"tag"},
			})
		}},

		// --- Resource tags ----------------------------------------------------
		{Op: "get /resources/{resource_type}/{resource_id}/tags", SDK: "ResourceService.ListTags", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Resources.ListTags(ctx, seg1, seg2, &octonomy.ResourceListTagsParams{
				ListOptions:     listOptions(),
				ApplicationID:   str("app"),
				IncludeInactive: boolp(true),
				Type:            str("type"),
			}, octonomy.WithIncludeGlobal())
		}},
		{Op: "post /resources/{resource_type}/{resource_id}/tags", SDK: "ResourceService.ReplaceTags", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Resources.ReplaceTags(ctx, seg1, seg2, octonomy.ResourceReplace{
				ApplicationID: "app", TagIDs: []string{"tag"}, AssignedBy: str("who"),
			})
		}},
		{Op: "get /tags/{tag_id}/resources", SDK: "TagService.ListResources", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Tags.ListResources(ctx, seg1, &octonomy.TagListResourcesParams{
				ListOptions:   listOptions(),
				ApplicationID: str("app"),
				ResourceType:  str("rt"),
			}, octonomy.WithIncludeGlobal())
		}},

		// --- Audit logs -------------------------------------------------------
		{Op: "get /audit-logs", SDK: "AuditLogService.List", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.AuditLogs.List(ctx, &octonomy.AuditLogListParams{
				ListOptions:   listOptions(),
				Action:        str("a"),
				ActorID:       str("actor"),
				ApplicationID: str("app"),
				EntityID:      str("e"),
				EntityType:    str("et"),
				OperationID:   str("op"),
				ResourceID:    str("ri"),
				ResourceType:  str("rt"),
				TagID:         str("tag"),
			}, octonomy.WithIncludeGlobal())
		}},
		{Op: "get /tags/{tag_id}/audit-logs", SDK: "TagService.ListAuditLogs", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Tags.ListAuditLogs(ctx, seg1, &octonomy.TagListAuditLogsParams{
				ListOptions:   listOptions(),
				Action:        str("a"),
				ActorID:       str("actor"),
				ApplicationID: str("app"),
				OperationID:   str("op"),
			}, octonomy.WithIncludeGlobal())
		}},
		{Op: "get /resources/{resource_type}/{resource_id}/audit-logs", SDK: "ResourceService.ListAuditLogs", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Resources.ListAuditLogs(ctx, seg1, seg2, &octonomy.ResourceListAuditLogsParams{
				ListOptions:   listOptions(),
				Action:        str("a"),
				ActorID:       str("actor"),
				ApplicationID: str("app"),
				OperationID:   str("op"),
			}, octonomy.WithIncludeGlobal())
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
