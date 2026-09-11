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

// The free-form objects. Their contents are arbitrary as far as the contract is
// concerned, so the gate compares them exactly -- it controls both sides, and a
// driver whose metadata stopped arriving would otherwise be invisible.
func metaValue() octonomy.Metadata {
	return octonomy.Metadata{ExpectedValue("metadata", 0): "value"}
}

func meta() octonomy.Metadata { return metaValue() }

// Str, Bool and Int read the ONE table of expected values for THIS execution, so
// a driver cannot disagree with what the gate will require. Written by hand, they
// did: two fields kept plain literals and every boolean was `true` while the table
// said otherwise, and the gate reported all of it.
func (e *Env) Str(wireName string) *string {
	value := ExpectedValue(wireName, e.pass)
	return &value
}

// Val is Str without the pointer, for a field that takes a bare string.
func (e *Env) Val(wireName string) string { return ExpectedValue(wireName, e.pass) }

func (e *Env) Bool(wireName string) *bool {
	value, err := strconv.ParseBool(ExpectedValue(wireName, e.pass))
	if err != nil {
		panic("contractdrift: " + wireName + " is not a boolean in the expected-value table")
	}
	return &value
}

func (e *Env) Int(wireName string) int {
	value, err := strconv.Atoi(ExpectedValue(wireName, e.pass))
	if err != nil {
		panic("contractdrift: " + wireName + " is not an integer in the expected-value table")
	}
	return value
}
func metap() *octonomy.Metadata {
	m := meta()
	return &m
}

func listOptions(env *Env) octonomy.ListOptions {
	return octonomy.ListOptions{Limit: env.Int("limit"), Offset: env.Int("offset")}
}

// Drivers is the table. Order is the report's order.
func Drivers() []Driver {
	return []Driver{
		// --- Tags -------------------------------------------------------------
		{Op: "get /tags", SDK: "TagService.List", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Tags.List(ctx, &octonomy.TagListParams{
				ListOptions:   listOptions(env),
				ApplicationID: env.Str("application_id"),
				IncludeShared: env.Bool("include_shared"),
				IsActive:      env.Bool("is_active"),
				ParentID:      env.Str("parent_id"),
				Query:         env.Str("q"),
				Slug:          env.Str("slug"),
				Type:          env.Str("type"),
				VocabularyID:  env.Str("vocabulary_id"),
			}, env.ListScope()...)
		}},
		{Op: "post /tags", SDK: "TagService.Create", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Tags.Create(ctx, octonomy.TagCreate{
				ApplicationID: env.Str("application_id"), Name: env.Val("name"), Slug: env.Val("slug"), Type: env.Val("type"),
				Description: env.Str("description"), ParentID: env.Str("parent_id"),
				VocabularyID: env.Str("vocabulary_id"),
				Metadata:     meta(), IsActive: env.Bool("is_active"),
			}, env.Scope()...)
		}},
		{Op: "get /tags/{tag_id}", SDK: "TagService.Get", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Tags.Get(ctx, env.Path("tag_id"), env.ReadScope()...)
		}},
		{Op: "patch /tags/{tag_id}", SDK: "TagService.Update", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Tags.Update(ctx, env.Path("tag_id"), octonomy.TagUpdate{
				ApplicationID: env.Str("application_id"), Name: env.Str("name"), Slug: env.Str("slug"), Type: env.Str("type"),
				Description: env.Str("description"), ParentID: env.Str("parent_id"),
				VocabularyID: env.Str("vocabulary_id"),
				Metadata:     metap(), IsActive: env.Bool("is_active"),
			}, env.Scope()...)
		}},
		{Op: "delete /tags/{tag_id}", SDK: "TagService.Delete", Call: func(ctx context.Context, env *Env) (any, error) {
			return nil, env.Client.Tags.Delete(ctx, env.Path("tag_id"), env.DeleteScope()...)
		}},

		// --- Vocabularies -----------------------------------------------------
		{Op: "get /vocabularies", SDK: "VocabularyService.List", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Vocabularies.List(ctx, &octonomy.VocabularyListParams{
				ListOptions:   listOptions(env),
				ApplicationID: env.Str("application_id"),
				IncludeShared: env.Bool("include_shared"),
				IsActive:      env.Bool("is_active"),
			}, env.ListScope()...)
		}},
		{Op: "post /vocabularies", SDK: "VocabularyService.Create", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Vocabularies.Create(ctx, octonomy.VocabularyCreate{
				ApplicationID: env.Str("application_id"), Name: env.Val("name"), Slug: env.Val("slug"), Description: env.Str("description"),
				Metadata: meta(), IsActive: env.Bool("is_active"),
			}, env.Scope()...)
		}},
		{Op: "get /vocabularies/{vocabulary_id}", SDK: "VocabularyService.Get", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Vocabularies.Get(ctx, env.Path("vocabulary_id"), env.ReadScope()...)
		}},
		{Op: "patch /vocabularies/{vocabulary_id}", SDK: "VocabularyService.Update", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Vocabularies.Update(ctx, env.Path("vocabulary_id"), octonomy.VocabularyUpdate{
				ApplicationID: env.Str("application_id"), Name: env.Str("name"), Slug: env.Str("slug"), Description: env.Str("description"),
				Metadata: metap(), IsActive: env.Bool("is_active"),
			}, env.Scope()...)
		}},
		{Op: "delete /vocabularies/{vocabulary_id}", SDK: "VocabularyService.Delete", Call: func(ctx context.Context, env *Env) (any, error) {
			return nil, env.Client.Vocabularies.Delete(ctx, env.Path("vocabulary_id"), env.DeleteScope()...)
		}},

		// --- Tag aliases ------------------------------------------------------
		{Op: "get /tag-aliases", SDK: "AliasService.List", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Aliases.List(ctx, &octonomy.TagAliasListParams{
				ListOptions:   listOptions(env),
				ApplicationID: env.Str("application_id"),
				IncludeShared: env.Bool("include_shared"),
				IsActive:      env.Bool("is_active"),
				Query:         env.Str("q"),
				Slug:          env.Str("slug"),
				TagID:         env.Str("tag_id"),
			}, env.ListScope()...)
		}},
		{Op: "post /tag-aliases", SDK: "AliasService.Create", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Aliases.Create(ctx, octonomy.TagAliasCreate{
				ApplicationID: env.Str("application_id"), TagID: env.Val("tag_id"), Name: env.Val("name"), Slug: env.Val("slug"),
				Metadata: meta(), IsActive: env.Bool("is_active"),
			}, env.Scope()...)
		}},
		{Op: "get /tag-aliases/{alias_id}", SDK: "AliasService.Get", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Aliases.Get(ctx, env.Path("alias_id"), env.ReadScope()...)
		}},
		{Op: "patch /tag-aliases/{alias_id}", SDK: "AliasService.Update", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Aliases.Update(ctx, env.Path("alias_id"), octonomy.TagAliasUpdate{
				ApplicationID: env.Str("application_id"), TagID: env.Str("tag_id"), Name: env.Str("name"), Slug: env.Str("slug"),
				Metadata: metap(), IsActive: env.Bool("is_active"),
			}, env.Scope()...)
		}},
		{Op: "delete /tag-aliases/{alias_id}", SDK: "AliasService.Delete", Call: func(ctx context.Context, env *Env) (any, error) {
			return nil, env.Client.Aliases.Delete(ctx, env.Path("alias_id"), env.DeleteScope()...)
		}},
		{Op: "get /tags/{tag_id}/aliases", SDK: "TagService.ListAliases", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Tags.ListAliases(ctx, env.Path("tag_id"), &octonomy.TagListAliasesParams{
				ListOptions:   listOptions(env),
				ApplicationID: env.Str("application_id"),
				IncludeShared: env.Bool("include_shared"),
				IsActive:      env.Bool("is_active"),
			}, env.ListScope()...)
		}},

		// --- Tag resolution ---------------------------------------------------
		//
		// `scope` is the parameter this whole gate exists because of. It is pinned
		// to global rather than merchant so WithIncludeGlobal can ride along: the
		// SDK refuses that pair against a MERCHANT scope, which resolves inside the
		// namespace and excludes global rows by definition, and the server would
		// drop one of the two in silence.
		{Op: "get /tag-resolution", SDK: "TagService.Resolve", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Tags.Resolve(ctx, env.Val("slug"), &octonomy.TagResolveParams{
				ApplicationID: env.Str("application_id"),
				Type:          env.Str("type"),
				Scope:         octonomy.ResolutionScope(env.Val("scope")),
			}, env.ListScope()...)
		}},

		// --- Assignments ------------------------------------------------------
		{Op: "post /tag-assignments", SDK: "AssignmentService.Create", Call: func(ctx context.Context, env *Env) (any, error) {
			// TagID, AliasID and AliasSlug are three ways to name the same tag and
			// the server takes one; the contract documents all three, so all three
			// are sent and the server's own mutual-exclusion rule is its business,
			// not this SDK's (AGENTS.md: the SDK adds ergonomics, not validation).
			return env.Client.Assignments.Create(ctx, octonomy.AssignmentCreate{
				ApplicationID: env.Val("application_id"), TagID: env.Str("tag_id"), AliasID: env.Str("alias_id"),
				AliasSlug:    env.Str("alias_slug"),
				ResourceType: env.Val("resource_type"), ResourceID: env.Val("resource_id"),
				AssignedBy: env.Str("assigned_by"),
			}, env.Scope()...)
		}},
		{Op: "delete /tag-assignments", SDK: "AssignmentService.Remove", Call: func(ctx context.Context, env *Env) (any, error) {
			return nil, env.Client.Assignments.Remove(ctx, octonomy.AssignmentRemove{
				ApplicationID: env.Val("application_id"), TagID: env.Val("tag_id"),
				ResourceType: env.Val("resource_type"), ResourceID: env.Val("resource_id"),
			}, env.Scope()...)
		}},
		{Op: "post /tag-assignments/bulk-assign", SDK: "AssignmentService.BulkAssign", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Assignments.BulkAssign(ctx, octonomy.BulkAssign{
				ApplicationID: env.Val("application_id"), ResourceType: env.Val("resource_type"), ResourceID: env.Val("resource_id"),
				TagIDs: []string{env.Val("tag_ids")}, AliasSlugs: []string{env.Val("alias_slugs")},
				AssignedBy: env.Str("assigned_by"),
			}, env.Scope()...)
		}},
		{Op: "post /tag-assignments/bulk-remove", SDK: "AssignmentService.BulkRemove", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Assignments.BulkRemove(ctx, octonomy.BulkRemove{
				ApplicationID: env.Val("application_id"), ResourceType: env.Val("resource_type"), ResourceID: env.Val("resource_id"),
				TagIDs: []string{env.Val("tag_ids")},
			}, env.Scope()...)
		}},

		// --- Resource tags ----------------------------------------------------
		{Op: "get /resources/{resource_type}/{resource_id}/tags", SDK: "ResourceService.ListTags", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Resources.ListTags(ctx, env.Path("resource_type"), env.Path("resource_id"), &octonomy.ResourceListTagsParams{
				ListOptions:     listOptions(env),
				ApplicationID:   env.Str("application_id"),
				IncludeInactive: env.Bool("include_inactive"),
				Type:            env.Str("type"),
			}, env.ListScope()...)
		}},
		{Op: "post /resources/{resource_type}/{resource_id}/tags", SDK: "ResourceService.ReplaceTags", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Resources.ReplaceTags(ctx, env.Path("resource_type"), env.Path("resource_id"), octonomy.ResourceReplace{
				ApplicationID: env.Val("application_id"), TagIDs: []string{env.Val("tag_ids")}, AliasSlugs: []string{env.Val("alias_slugs")},
				AssignedBy: env.Str("assigned_by"),
			}, env.Scope()...)
		}},
		{Op: "get /tags/{tag_id}/resources", SDK: "TagService.ListResources", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Tags.ListResources(ctx, env.Path("tag_id"), &octonomy.TagListResourcesParams{
				ListOptions:   listOptions(env),
				ApplicationID: env.Str("application_id"),
				ResourceType:  env.Str("resource_type"),
			}, env.ListScope()...)
		}},

		// --- Audit logs -------------------------------------------------------
		{Op: "get /audit-logs", SDK: "AuditLogService.List", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.AuditLogs.List(ctx, &octonomy.AuditLogListParams{
				ListOptions:   listOptions(env),
				Action:        env.Str("action"),
				ActorID:       env.Str("actor_id"),
				ApplicationID: env.Str("application_id"),
				EntityID:      env.Str("entity_id"),
				EntityType:    env.Str("entity_type"),
				OperationID:   env.Str("operation_id"),
				ResourceID:    env.Str("resource_id"),
				ResourceType:  env.Str("resource_type"),
				TagID:         env.Str("tag_id"),
			}, env.ListScope()...)
		}},
		{Op: "get /tags/{tag_id}/audit-logs", SDK: "TagService.ListAuditLogs", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Tags.ListAuditLogs(ctx, env.Path("tag_id"), &octonomy.TagListAuditLogsParams{
				ListOptions:   listOptions(env),
				Action:        env.Str("action"),
				ActorID:       env.Str("actor_id"),
				ApplicationID: env.Str("application_id"),
				OperationID:   env.Str("operation_id"),
			}, env.ListScope()...)
		}},
		{Op: "get /resources/{resource_type}/{resource_id}/audit-logs", SDK: "ResourceService.ListAuditLogs", Call: func(ctx context.Context, env *Env) (any, error) {
			return env.Client.Resources.ListAuditLogs(ctx, env.Path("resource_type"), env.Path("resource_id"), &octonomy.ResourceListAuditLogsParams{
				ListOptions:   listOptions(env),
				Action:        env.Str("action"),
				ActorID:       env.Str("actor_id"),
				ApplicationID: env.Str("application_id"),
				OperationID:   env.Str("operation_id"),
			}, env.ListScope()...)
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

// booleanPatterns give each boolean input a distinct pair of values across the two
// executions.
//
// One execution cannot tell three booleans apart: there are two values and three
// axes on a tag list (`include_shared`, `is_active`, `include_global`), so some
// pair always matches and swapping that pair changes nothing. The comment here
// used to claim any two sharing a request differ, and a review showed it false.
//
// Two executions give four patterns, which is enough. `include_global` is fixed:
// WithIncludeGlobal sends only "true", so it takes TT and the others take the
// three remaining patterns.
var booleanPatterns = map[string][2]bool{
	"include_global":   {true, true},
	"include_shared":   {true, false},
	"is_active":        {false, true},
	"include_inactive": {false, false},
}

// ExpectedValue is the value the wire must carry for a documented input, on a
// given execution.
func ExpectedValue(wireName string, pass int) string {
	name := strings.ToLower(wireName)
	if pattern, ok := booleanPatterns[name]; ok {
		return strconv.FormatBool(pattern[pass%len(pattern)])
	}
	if value, ok := constrainedValues[name]; ok {
		return value
	}
	return driverValuePrefix + wireName
}

// sentinelOrigin reports the wire field a name-shaped value was meant for, which
// turns "this value is wrong" into "this value belongs to that field".
func sentinelOrigin(value string) (string, bool) {
	return strings.CutPrefix(value, driverValuePrefix)
}
