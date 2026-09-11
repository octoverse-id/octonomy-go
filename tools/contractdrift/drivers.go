package main

import (
	"context"
	"strconv"
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

func meta() octonomy.Metadata { return octonomy.Metadata{"contractdrift": "value"} }

// strFor, boolFor and intFor read the ONE table of expected values, so a driver
// cannot disagree with what the gate will require. Written by hand, they did: two
// fields kept plain literals and every boolean was `true` while the table said
// otherwise, and the gate reported all of it.
func strFor(wireName string) *string {
	value := ExpectedValue(wireName)
	return &value
}

func boolFor(wireName string) *bool {
	value, err := strconv.ParseBool(ExpectedValue(wireName))
	if err != nil {
		panic("contractdrift: " + wireName + " is not a boolean in the expected-value table")
	}
	return &value
}

func intFor(wireName string) int {
	value, err := strconv.Atoi(ExpectedValue(wireName))
	if err != nil {
		panic("contractdrift: " + wireName + " is not an integer in the expected-value table")
	}
	return value
}
func metap() *octonomy.Metadata {
	m := meta()
	return &m
}

func listOptions() octonomy.ListOptions {
	return octonomy.ListOptions{Limit: intFor("limit"), Offset: intFor("offset")}
}

// Drivers is the table. Order is the report's order.
func Drivers() []Driver {
	return []Driver{
		// --- Tags -------------------------------------------------------------
		{Op: "get /tags", SDK: "TagService.List", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Tags.List(ctx, &octonomy.TagListParams{
				ListOptions:   listOptions(),
				ApplicationID: strFor("application_id"),
				IncludeShared: boolFor("include_shared"),
				IsActive:      boolFor("is_active"),
				ParentID:      strFor("parent_id"),
				Query:         strFor("q"),
				Slug:          strFor("slug"),
				Type:          strFor("type"),
				VocabularyID:  strFor("vocabulary_id"),
			}, env.ReadScope()...)
		}},
		{Op: "post /tags", SDK: "TagService.Create", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Tags.Create(ctx, octonomy.TagCreate{
				ApplicationID: strFor("application_id"), Name: driverValue("name"), Slug: driverValue("slug"), Type: driverValue("type"),
				Description: strFor("description"), ParentID: strFor("parent_id"),
				VocabularyID: strFor("vocabulary_id"),
				Metadata:     meta(), IsActive: boolFor("is_active"),
			}, env.Scope()...)
		}},
		{Op: "get /tags/{tag_id}", SDK: "TagService.Get", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Tags.Get(ctx, env.Path("tag_id"), env.ReadScope()...)
		}},
		{Op: "patch /tags/{tag_id}", SDK: "TagService.Update", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Tags.Update(ctx, env.Path("tag_id"), octonomy.TagUpdate{
				ApplicationID: strFor("application_id"), Name: strFor("name"), Slug: strFor("slug"), Type: strFor("type"),
				Description: strFor("description"), ParentID: strFor("parent_id"),
				VocabularyID: strFor("vocabulary_id"),
				Metadata:     metap(), IsActive: boolFor("is_active"),
			}, env.Scope()...)
		}},
		{Op: "delete /tags/{tag_id}", SDK: "TagService.Delete", Call: func(ctx context.Context, env *Env) (any, error) {
			return nil, env.Client.Tags.Delete(ctx, env.Path("tag_id"), env.DeleteScope()...)
		}},

		// --- Vocabularies -----------------------------------------------------
		{Op: "get /vocabularies", SDK: "VocabularyService.List", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Vocabularies.List(ctx, &octonomy.VocabularyListParams{
				ListOptions:   listOptions(),
				ApplicationID: strFor("application_id"),
				IncludeShared: boolFor("include_shared"),
				IsActive:      boolFor("is_active"),
			}, env.ReadScope()...)
		}},
		{Op: "post /vocabularies", SDK: "VocabularyService.Create", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Vocabularies.Create(ctx, octonomy.VocabularyCreate{
				ApplicationID: strFor("application_id"), Name: driverValue("name"), Slug: driverValue("slug"), Description: strFor("description"),
				Metadata: meta(), IsActive: boolFor("is_active"),
			}, env.Scope()...)
		}},
		{Op: "get /vocabularies/{vocabulary_id}", SDK: "VocabularyService.Get", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Vocabularies.Get(ctx, env.Path("vocabulary_id"), env.ReadScope()...)
		}},
		{Op: "patch /vocabularies/{vocabulary_id}", SDK: "VocabularyService.Update", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Vocabularies.Update(ctx, env.Path("vocabulary_id"), octonomy.VocabularyUpdate{
				ApplicationID: strFor("application_id"), Name: strFor("name"), Slug: strFor("slug"), Description: strFor("description"),
				Metadata: metap(), IsActive: boolFor("is_active"),
			}, env.Scope()...)
		}},
		{Op: "delete /vocabularies/{vocabulary_id}", SDK: "VocabularyService.Delete", Call: func(ctx context.Context, env *Env) (any, error) {
			return nil, env.Client.Vocabularies.Delete(ctx, env.Path("vocabulary_id"), env.DeleteScope()...)
		}},

		// --- Tag aliases ------------------------------------------------------
		{Op: "get /tag-aliases", SDK: "AliasService.List", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Aliases.List(ctx, &octonomy.TagAliasListParams{
				ListOptions:   listOptions(),
				ApplicationID: strFor("application_id"),
				IncludeShared: boolFor("include_shared"),
				IsActive:      boolFor("is_active"),
				Query:         strFor("q"),
				Slug:          strFor("slug"),
				TagID:         strFor("tag_id"),
			}, env.ReadScope()...)
		}},
		{Op: "post /tag-aliases", SDK: "AliasService.Create", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Aliases.Create(ctx, octonomy.TagAliasCreate{
				ApplicationID: strFor("application_id"), TagID: driverValue("tag_id"), Name: driverValue("name"), Slug: driverValue("slug"),
				Metadata: meta(), IsActive: boolFor("is_active"),
			}, env.Scope()...)
		}},
		{Op: "get /tag-aliases/{alias_id}", SDK: "AliasService.Get", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Aliases.Get(ctx, env.Path("alias_id"), env.ReadScope()...)
		}},
		{Op: "patch /tag-aliases/{alias_id}", SDK: "AliasService.Update", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Aliases.Update(ctx, env.Path("alias_id"), octonomy.TagAliasUpdate{
				ApplicationID: strFor("application_id"), TagID: strFor("tag_id"), Name: strFor("name"), Slug: strFor("slug"),
				Metadata: metap(), IsActive: boolFor("is_active"),
			}, env.Scope()...)
		}},
		{Op: "delete /tag-aliases/{alias_id}", SDK: "AliasService.Delete", Call: func(ctx context.Context, env *Env) (any, error) {
			return nil, env.Client.Aliases.Delete(ctx, env.Path("alias_id"), env.DeleteScope()...)
		}},
		{Op: "get /tags/{tag_id}/aliases", SDK: "TagService.ListAliases", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Tags.ListAliases(ctx, env.Path("tag_id"), &octonomy.TagListAliasesParams{
				ListOptions:   listOptions(),
				ApplicationID: strFor("application_id"),
				IncludeShared: boolFor("include_shared"),
				IsActive:      boolFor("is_active"),
			}, env.ReadScope()...)
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
				ApplicationID: strFor("application_id"),
				Type:          strFor("type"),
				Scope:         octonomy.ResolutionScopeGlobal,
			}, env.ReadScope()...)
		}},

		// --- Assignments ------------------------------------------------------
		{Op: "post /tag-assignments", SDK: "AssignmentService.Create", Call: func(ctx context.Context, env *Env) (any, error) {
			// TagID, AliasID and AliasSlug are three ways to name the same tag and
			// the server takes one; the contract documents all three, so all three
			// are sent and the server's own mutual-exclusion rule is its business,
			// not this SDK's (AGENTS.md: the SDK adds ergonomics, not validation).
			return env.Client.Assignments.Create(ctx, octonomy.AssignmentCreate{
				ApplicationID: driverValue("application_id"), TagID: strFor("tag_id"), AliasID: strFor("alias_id"),
				AliasSlug:    strFor("alias_slug"),
				ResourceType: driverValue("resource_type"), ResourceID: driverValue("resource_id"),
				AssignedBy: strFor("assigned_by"),
			}, env.Scope()...)
		}},
		{Op: "delete /tag-assignments", SDK: "AssignmentService.Remove", Call: func(ctx context.Context, env *Env) (any, error) {
			return nil, env.Client.Assignments.Remove(ctx, octonomy.AssignmentRemove{
				ApplicationID: driverValue("application_id"), TagID: driverValue("tag_id"),
				ResourceType: driverValue("resource_type"), ResourceID: driverValue("resource_id"),
			}, env.Scope()...)
		}},
		{Op: "post /tag-assignments/bulk-assign", SDK: "AssignmentService.BulkAssign", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Assignments.BulkAssign(ctx, octonomy.BulkAssign{
				ApplicationID: driverValue("application_id"), ResourceType: driverValue("resource_type"), ResourceID: driverValue("resource_id"),
				TagIDs: []string{driverValue("tag_ids")}, AliasSlugs: []string{driverValue("alias_slugs")},
				AssignedBy: strFor("assigned_by"),
			}, env.Scope()...)
		}},
		{Op: "post /tag-assignments/bulk-remove", SDK: "AssignmentService.BulkRemove", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Assignments.BulkRemove(ctx, octonomy.BulkRemove{
				ApplicationID: driverValue("application_id"), ResourceType: driverValue("resource_type"), ResourceID: driverValue("resource_id"),
				TagIDs: []string{driverValue("tag_ids")},
			}, env.Scope()...)
		}},

		// --- Resource tags ----------------------------------------------------
		{Op: "get /resources/{resource_type}/{resource_id}/tags", SDK: "ResourceService.ListTags", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Resources.ListTags(ctx, env.Path("resource_type"), env.Path("resource_id"), &octonomy.ResourceListTagsParams{
				ListOptions:     listOptions(),
				ApplicationID:   strFor("application_id"),
				IncludeInactive: boolFor("include_inactive"),
				Type:            strFor("type"),
			}, env.ReadScope()...)
		}},
		{Op: "post /resources/{resource_type}/{resource_id}/tags", SDK: "ResourceService.ReplaceTags", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Resources.ReplaceTags(ctx, env.Path("resource_type"), env.Path("resource_id"), octonomy.ResourceReplace{
				ApplicationID: driverValue("application_id"), TagIDs: []string{driverValue("tag_ids")}, AliasSlugs: []string{driverValue("alias_slugs")},
				AssignedBy: strFor("assigned_by"),
			}, env.Scope()...)
		}},
		{Op: "get /tags/{tag_id}/resources", SDK: "TagService.ListResources", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Tags.ListResources(ctx, env.Path("tag_id"), &octonomy.TagListResourcesParams{
				ListOptions:   listOptions(),
				ApplicationID: strFor("application_id"),
				ResourceType:  strFor("resource_type"),
			}, env.ReadScope()...)
		}},

		// --- Audit logs -------------------------------------------------------
		{Op: "get /audit-logs", SDK: "AuditLogService.List", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.AuditLogs.List(ctx, &octonomy.AuditLogListParams{
				ListOptions:   listOptions(),
				Action:        strFor("action"),
				ActorID:       strFor("actor_id"),
				ApplicationID: strFor("application_id"),
				EntityID:      strFor("entity_id"),
				EntityType:    strFor("entity_type"),
				OperationID:   strFor("operation_id"),
				ResourceID:    strFor("resource_id"),
				ResourceType:  strFor("resource_type"),
				TagID:         strFor("tag_id"),
			}, env.ReadScope()...)
		}},
		{Op: "get /tags/{tag_id}/audit-logs", SDK: "TagService.ListAuditLogs", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Tags.ListAuditLogs(ctx, env.Path("tag_id"), &octonomy.TagListAuditLogsParams{
				ListOptions:   listOptions(),
				Action:        strFor("action"),
				ActorID:       strFor("actor_id"),
				ApplicationID: strFor("application_id"),
				OperationID:   strFor("operation_id"),
			}, env.ReadScope()...)
		}},
		{Op: "get /resources/{resource_type}/{resource_id}/audit-logs", SDK: "ResourceService.ListAuditLogs", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Resources.ListAuditLogs(ctx, env.Path("resource_type"), env.Path("resource_id"), &octonomy.ResourceListAuditLogsParams{
				ListOptions:   listOptions(),
				Action:        strFor("action"),
				ActorID:       strFor("actor_id"),
				ApplicationID: strFor("application_id"),
				OperationID:   strFor("operation_id"),
			}, env.ReadScope()...)
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

// --- expected values ---------------------------------------------------------------
//
// Every input a driver supplies has ONE canonical value, and the gate requires the
// wire to carry exactly it. Not "a value that looks like a sentinel for something
// else" -- that was the previous rule, and a review walked through it six ways: a
// hard-coded string passed because it no longer looked like a sentinel at all;
// `limit` and `offset` swapped because both parse as integers; two booleans
// swapped because both are `true`; a path argument reused as `slug`; an array
// emptied; and the credentials, which carried no expectation at all.
//
// Exact equality removes the whole class. A field carries what the driver gave it
// or it does not.
//
// Most values are the wire field's own name, which makes a misrouted value
// self-describing in the report. The exceptions are values the contract or the
// SDK constrains -- an enum, a number, a boolean -- and those are listed
// explicitly below, chosen so that any two that can appear on the same request
// differ from each other. Two booleans that were both `true` would be swappable
// without either changing.
const driverValuePrefix = "cd~"

// constrainedValues are the inputs that cannot carry a name-shaped value.
var constrainedValues = map[string]string{
	// Paging. Distinct numbers, so swapping them changes both.
	"limit":  "1",
	"offset": "2",

	// Booleans, deliberately not all true: `include_shared` and `is_active` ride
	// the same request, and two `true`s are interchangeable without either
	// changing.
	"include_shared":   "true",
	"is_active":        "false",
	"include_inactive": "true",
	// include_global is set by WithIncludeGlobal, which sends only "true".
	"include_global": "true",

	// An enum the server validates. `global` pins the tenant-shared namespace and
	// is the one value that can ride with WithIncludeGlobal.
	"scope": "global",

	// The client-wide credentials, which client_headers records and the gate now
	// checks the values of -- an arbitrary token under Authorization used to pass.
	"authorization": "Bearer " + driverValuePrefix + "token",
	"x-tenant-id":   driverValuePrefix + "tenant",

	// The namespace type may not be the reserved literal `global`, and both halves
	// must differ from each other or crossing them would be invisible.
	"x-namespace-type": driverValuePrefix + "x-namespace-type",
	"x-namespace-id":   driverValuePrefix + "x-namespace-id",
}

// ExpectedValue is the value the wire must carry for a documented input.
func ExpectedValue(wireName string) string {
	if value, ok := constrainedValues[strings.ToLower(wireName)]; ok {
		return value
	}
	return driverValuePrefix + wireName
}

// driverValue is what a driver passes. Same function, named for the reading side.
func driverValue(wireName string) string { return ExpectedValue(wireName) }

// sentinelOrigin reports the wire field a name-shaped value was meant for, which
// turns "this value is wrong" into "this value belongs to that field".
func sentinelOrigin(value string) (string, bool) {
	return strings.CutPrefix(value, driverValuePrefix)
}
