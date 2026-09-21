//go:build integration
// +build integration

// Narrow integration smoke test for the modern line.
//
// Deliberately a smoke test, not a suite. #17 builds the full integration suite;
// what this proves is the one thing httptest structurally cannot: that the
// canned fixtures match the server. They did not, and that is #32 -- every
// single-resource read decoded to a zero-valued struct with a nil error, and a
// complete unit suite stayed green through it, because the fixtures encoded the
// vendored spec rather than the running server.
//
// The assertions cover the shapes: the single-resource {"data": {...}} envelope
// on both a write and a read, the {data, pagination} list envelope, all four
// resources, the composite resolution and bulk payloads, one real error
// envelope, the ENVELOPE-LESS health probes, and a namespaced round trip on
// /api/v2 -- the last of these being the only place the namespace response
// fields meet a server that actually populates them.
//
// THE WALK IS A REGISTRY, ONE ENTRY PER RESPONSE TYPE (#77). It used to be one
// long function whose completeness was a claim in this comment, which is a
// reader and not a gate: a resource added without an assertion here left #32's
// class unguarded for itself with every job green, and the recipe in
// docs/roadmap.md said as much. smokeProbes below is keyed by the type each
// entry asserts, and TestEveryResponseTypeHasASmokeProbe (smokeprobes_test.go)
// compares those keys against the response types derived from this package's own
// source -- the same derivation #76's guard uses -- and binds each entry to a
// client method that really decodes its key. It cannot prove an assertion is
// meaningful; it can make a silent omission impossible, which is the failure
// that actually happens.
//
// TWO CONSTRAINTS THE GUARD PUTS ON THAT TABLE, worth knowing before editing it.
// It reads the []smokeProbe literal smokeProbes RETURNS -- build the slice with
// a helper or append to it in a loop and the guard finds nothing and fails,
// which is the safe direction but is a shape constraint all the same. And an
// entry's assert closure must call a client method that decodes the entry's own
// key, since the key is what the guard counts as coverage.
//
// THE ORDER OF THE TABLE IS LOAD-BEARING and is not checked by anything. Entries
// run in slice order against one server, sharing the rows they create through
// smokeState: the Vocabulary entry creates the vocabulary the Tag entry hangs
// its tag off, and the AuditLog entry reads the history every entry before it
// wrote. Reordering it is a behavioural change, not a cosmetic one.
//
// The bulk assertions matter most of the set. docs/openapi-v2.yaml claims
// bulk-assign returns a bare array and documents no schema at all for
// bulk-remove; the server returns composites under `data` for both. A client
// written from the spec decodes an empty slice and a nil error, and only a real
// server can tell you which of the two is true.
//
// Run it against the container harness:
//
//	make dev-server
//	make smoke
//
// With OCTONOMY_TEST_BASE_URL unset the test skips, so `go test ./...` stays
// hermetic.
package octonomy_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	octonomy "github.com/octoverse-id/octonomy-go/v2"
)

// cleanupTimeout bounds the deferred deletes. They get their own context on
// purpose: sharing the test's context means a timeout mid-test cancels the
// cleanup too, so the rows leak AND the delete error masks the real failure in
// the output.
const cleanupTimeout = 15 * time.Second

// newSmokeClient builds a client from the harness credentials, or skips.
//
// The gate is OCTONOMY_TEST_BASE_URL, matching scripts/octonomy-harness.sh. A
// missing token or tenant with a base URL present is a broken harness, not an
// absent one, so that fails rather than skips -- otherwise a misconfigured CI
// job would report a vacuous pass.
//
// OCTONOMY_SMOKE_REQUIRED=1 removes the skip entirely, and CI sets it. Skipping
// is right on a laptop with no Docker; in the CI job it is the worst available
// outcome, because this is the only check that sees the server's real response
// shapes -- the exact blind spot that produced #32.
func newSmokeClient(t *testing.T) *octonomy.Client {
	t.Helper()

	required := os.Getenv("OCTONOMY_SMOKE_REQUIRED") == "1"
	baseURL := os.Getenv("OCTONOMY_TEST_BASE_URL")
	if baseURL == "" {
		if required {
			t.Fatal("OCTONOMY_SMOKE_REQUIRED=1 but OCTONOMY_TEST_BASE_URL is empty: the harness did not export its credentials, so this test would have skipped and reported a vacuous pass")
		}
		t.Skip("OCTONOMY_TEST_BASE_URL is empty; run `make dev-server` and then `make smoke`")
	}
	token := os.Getenv("OCTONOMY_TEST_TOKEN")
	tenantID := os.Getenv("OCTONOMY_TEST_TENANT_ID")
	if token == "" || tenantID == "" {
		t.Fatal("OCTONOMY_TEST_BASE_URL is set but OCTONOMY_TEST_TOKEN/OCTONOMY_TEST_TENANT_ID are not: the harness env is incomplete")
	}

	client, err := octonomy.New(octonomy.Config{
		BaseURL:  baseURL,
		Token:    token,
		TenantID: tenantID,
		ActorID:  "v2-smoke",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
}

// slugSeq distinguishes two slugs the clock cannot.
//
// It is not decoration. The timestamp half used to be `UnixNano() % 1e6`, which
// is the low six digits of the nanosecond count and therefore REPEATS EVERY
// MILLISECOND: two calls exactly 1ms apart in one process produce the same
// string, and a rerun that inherits a recycled pid can collide with the run
// before it. Slugs are unique per (type, slug) on the server, so a collision is
// a 409 on a create -- or worse, a list filter that matches a row this run did
// not make and an exact-count assertion that fails for a reason nobody can see
// from the output. The counter makes two calls in one process distinct by
// construction; the full nanosecond stamp and the pid separate one process from
// the next.
var slugSeq atomic.Uint64

// uniqueSlug keeps repeat runs against one long-lived harness from colliding on
// the server's (type, slug) uniqueness constraint.
//
// LENGTH IS BOUNDED BY THE TIGHTEST CONSUMER, not by `slug`. A value is its
// prefix, three separators, the pid, a nineteen-digit nanosecond stamp (nineteen
// until the year 2262, where int64 nanoseconds run out) and the counter: under
// 50 characters for the longest prefix either integration file uses, on a Linux
// pid_max of 4194304, and one suite appends a suffix of its own to that. All of
// it is far inside the contract's 255 on `slug` and `resource_id`.
//
// But one of these values is not a slug at all. The "smoke-req" one goes to
// WithRequestID, and the server stores a request id in a 100-CHARACTER column,
// where an over-long id fails the row insert and comes back as a bare 500 --
// transport.go records what that costs a caller. That one runs to about 40
// characters, so it has room today; a longer prefix or a wider stamp is measured
// against 100 rather than 255. This walk is where that matters, because it is
// this change that grew the value: the stamp used to be truncated to six digits,
// which is what let two calls a millisecond apart collide.
func uniqueSlug(prefix string) string {
	return fmt.Sprintf("%s-%d-%d-%d", prefix, os.Getpid(), time.Now().UnixNano(), slugSeq.Add(1))
}

// smokeState is what one probe leaves behind for the ones after it.
//
// The walk creates real rows on a real server, and most of the shapes here can
// only be asked for in terms of a row something else created: an alias needs a
// tag, a resolution needs an alias, the audit history needs every mutation above
// it. Splitting the walk into entries did not remove those dependencies, so they
// are carried in one explicit struct rather than in the closure scope of a
// single enormous function, where they used to be implicit.
//
// A field is written by exactly one probe and read by the ones after it. A nil
// pointer here means an entry ran out of order, which the walk's fail-fast
// prevents by aborting on the first failed entry rather than carrying on into a
// panic.
type smokeState struct {
	// root is the TEST ITSELF rather than the subtest a probe runs in. Every row
	// this walk creates has to outlive the entry that created it -- the tag from
	// the Tag entry is read by six later ones -- so deletions are registered here
	// and run once the whole walk is done. Registering them on a subtest's own
	// *testing.T would delete the tag before the alias entry could use it.
	root *testing.T

	// The harness scope triple. Required rather than optional: the harness
	// exports all three, so their absence means it booted differently than this
	// walk assumes rather than "no namespace support here".
	nsType, nsID, appID string

	// Written by the Vocabulary entry.
	vocab     *octonomy.Vocabulary
	vocabSlug string

	// Written by the Tag entry. nsTag is the namespaced one; every later
	// namespaced write targets it, since a namespaced row may only point at a
	// global or same-namespace one.
	tag             *octonomy.Tag
	tagSlug         string
	nsTag           *octonomy.Tag
	updateRequestID string

	// Written by the TagAlias entry.
	alias     *octonomy.TagAlias
	aliasSlug string

	// Written by the Assignment entry: the resource the assignment and both bulk
	// entries operate on, in that order.
	assignResourceID string

	// Written by the ResourceReplaceResult entry. replaceResourceID keeps its
	// tag so the two list shapes below have a row to read; clearedResourceID is
	// assigned and then cleared, which is the destructive case, and leaves
	// exactly the two operations the AuditLog entry reconstructs. nsCartID is
	// the namespaced one.
	replaceResourceID string
	clearedResourceID string
	nsCartID          string
}

// deleteLater registers a deletion that runs when the whole walk is done.
//
// Deactivation, not deletion -- but it keeps a repeatedly-booted harness tidy
// and exercises the DELETE path's 204 assertion against a real server.
func (s *smokeState) deleteLater(what string, del func(ctx context.Context) error) {
	s.root.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		if err := del(ctx); err != nil {
			s.root.Errorf("%s: %v", what, err)
		}
	})
}

// smokeProbe is one response shape, asserted against the real server.
//
// name is the RESPONSE TYPE the entry covers -- the type handed to doData or
// doList, which is what the guard derives its required set from. It is not a
// method name and not a prose label: TestEveryResponseTypeHasASmokeProbe matches
// it against the package's own source in both directions, so a typo, a renamed
// model, or an entry for a type that no longer exists fails rather than
// quietly covering nothing.
//
// assert takes the client as a PARAMETER rather than reading it off smokeState,
// because that is what lets the guard bind an entry to the endpoint it really
// calls: it looks for calls on this parameter and nothing else, so a call on
// some other value cannot stand in as coverage.
type smokeProbe struct {
	name   string
	assert func(ctx context.Context, t *testing.T, c *octonomy.Client, s *smokeState)
}

func TestSmoke_RealServer(t *testing.T) {
	client := newSmokeClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// The health probes, which are the one group a fixture can least be trusted
	// for: they are rooted OUTSIDE /api/<version>, they authenticate nobody, and
	// their body is a bare {"status": "ok"} with no data envelope. Three claims a
	// canned handler simply restates. The credential-free constructor is
	// exercised the way a caller would -- base URL alone, no token, no tenant --
	// against a server that really does reject unauthenticated traffic
	// everywhere else.
	//
	// It is a PROLOGUE rather than a registry entry on purpose, and the guard
	// says the same from its side: HealthStatus is decoded by health.go's own
	// probe helper and never reaches doData or doList, so it is not a response
	// type by the definition the registry is checked against. Giving it an entry
	// would put a key in the table that the required set does not contain.
	//
	// newSmokeClient has already gated on this variable, so it is set here.
	baseURL := os.Getenv("OCTONOMY_TEST_BASE_URL")
	probe, err := octonomy.NewHealthClient(baseURL)
	if err != nil {
		t.Fatalf("NewHealthClient: %v", err)
	}
	live, err := probe.Health.Live(ctx)
	if err != nil {
		t.Fatalf("Health.Live with no credentials: %v", err)
	}
	if live.Status != octonomy.HealthStatusOK {
		t.Errorf("Health.Live status = %q, want %q", live.Status, octonomy.HealthStatusOK)
	}
	ready, err := probe.Health.Ready(ctx)
	if err != nil {
		t.Fatalf("Health.Ready with no credentials: %v", err)
	}
	if ready.Status != octonomy.HealthStatusOK {
		t.Errorf("Health.Ready status = %q, want %q", ready.Status, octonomy.HealthStatusOK)
	}
	// The second entry point runs the same code against the same routes, and a
	// full client must not start sending its token there.
	if _, err := client.Health.Ready(ctx); err != nil {
		t.Fatalf("Client.Health.Ready: %v", err)
	}

	// The namespace axis exists only on /api/v2, and the harness exports the
	// triple every namespaced assertion below needs. Read once, here, rather
	// than in each entry that needs it: their absence is a broken harness, and
	// finding that out on the eighth entry wastes the seven before it.
	state := &smokeState{
		root:   t,
		nsType: os.Getenv("OCTONOMY_TEST_NAMESPACE_TYPE"),
		nsID:   os.Getenv("OCTONOMY_TEST_NAMESPACE_ID"),
		appID:  os.Getenv("OCTONOMY_TEST_APPLICATION_ID"),
	}
	if state.nsType == "" || state.nsID == "" || state.appID == "" {
		// Not a skip: the harness exports all three, so their absence means it
		// booted differently than this test assumes rather than "no namespace
		// support here".
		t.Fatal("OCTONOMY_TEST_NAMESPACE_TYPE/_NAMESPACE_ID/_APPLICATION_ID are not all set: the harness env is incomplete for the namespace assertions")
	}

	// The walk. Ordered, sequential, and FAIL-FAST: an entry reads the rows the
	// entries before it created, so carrying on past a failure would report a
	// cascade of consequences and bury the one cause -- or dereference a row
	// that was never created and panic. t.Run reports whether its subtest
	// passed, which is what makes that check possible at all.
	//
	// A SKIPPED ENTRY STOPS THE WALK TOO, and that is not the same check. t.Run
	// returns TRUE for a subtest that skipped, so an entry that skipped itself
	// would leave the walk carrying on into rows it never created while the run
	// reported PASS -- the vacuous pass this file's header refuses in the large,
	// reproduced one entry at a time. It is a hazard the single-function walk did
	// not have: a step could not skip without skipping everything. The whole test
	// still skips on a missing harness, in newSmokeClient and nowhere else.
	for _, probe := range smokeProbes() {
		skipped := false
		passed := t.Run(probe.name, func(t *testing.T) {
			// Deferred, so it also runs when the closure exits through SkipNow.
			defer func() { skipped = t.Skipped() }()
			probe.assert(ctx, t, client, state)
		})
		if !passed {
			t.Fatalf("the %s smoke probe failed; every entry after it reads rows it was supposed to create, so the walk stops here rather than reporting their consequences as separate failures", probe.name)
		}
		if skipped {
			t.Fatalf("the %s smoke probe skipped, so it asserted nothing and created none of the rows the entries after it read. A missing harness skips the whole test in newSmokeClient; an entry that skips itself is a green run that checked one shape fewer than it says", probe.name)
		}
	}
}

// smokeProbes is the walk: one entry per response type this SDK decodes.
//
// THE GUARD READS THIS FUNCTION'S SOURCE, so it has a shape as well as a
// content. It must have exactly one return, declared in the function body,
// returning a []smokeProbe literal whose entries carry a literal name and an
// assert closure -- see the file header and smokeprobes_test.go for why each of
// those is a requirement rather than a style.
//
// Neither the list of response types nor the reason an entry exists is repeated
// here. A second copy of either is how the first one came to be wrong.
func smokeProbes() []smokeProbe {
	return []smokeProbe{
		{
			// The first write and the first read, and the shape #32 was about:
			// the server answers 201 and 200 with {"data": {...}}, and before
			// #32 both decoded to an empty Vocabulary behind a nil error. This
			// entry is first because every other row in the walk hangs off the
			// vocabulary it creates.
			name: "Vocabulary",
			assert: func(ctx context.Context, t *testing.T, c *octonomy.Client, s *smokeState) {
				s.vocabSlug = uniqueSlug("smoke-vocab")
				vocab, err := c.Vocabularies.Create(ctx, octonomy.VocabularyCreate{
					Name:        "v2 smoke",
					Slug:        s.vocabSlug,
					Description: octonomy.String("created by the integration smoke test"),
				})
				if err != nil {
					t.Fatalf("Vocabularies.Create: %v", err)
				}
				s.vocab = vocab
				s.deleteLater("Vocabularies.Delete", func(ctx context.Context) error {
					return c.Vocabularies.Delete(ctx, vocab.ID)
				})
				if vocab.ID == "" || vocab.Slug != s.vocabSlug {
					t.Fatalf("created vocabulary did not round-trip: %+v", vocab)
				}

				// Create and Get are separate call sites through doData, and only
				// one of them was covered by a fixture before #32.
				fetched, err := c.Vocabularies.Get(ctx, vocab.ID)
				if err != nil {
					t.Fatalf("Vocabularies.Get: %v", err)
				}
				if fetched.ID != vocab.ID || fetched.Slug != s.vocabSlug {
					t.Fatalf("fetched vocabulary did not round-trip: %+v", fetched)
				}
				if fetched.Description == nil || *fetched.Description != "created by the integration smoke test" {
					t.Errorf("Description did not round-trip: %v", fetched.Description)
				}

				// VocabularyUpdate's metadata field, set and then emptied. The
				// three *Update structs were changed together (#37), so all three
				// are proved against a real server rather than one standing in
				// for the others -- the server's patch serializers are separate
				// code paths, and the docs claim all three accept {}. The
				// vocabulary was created with no metadata, so this exercises the
				// replace half from empty as well.
				vocabMeta, err := c.Vocabularies.Update(ctx, vocab.ID, octonomy.VocabularyUpdate{
					Metadata: octonomy.Set(octonomy.Metadata{"source": "v2-smoke"}),
				})
				if err != nil {
					t.Fatalf("Vocabularies.Update setting metadata: %v", err)
				}
				if got := vocabMeta.Metadata["source"]; got != "v2-smoke" {
					t.Errorf("vocabulary Metadata[source] = %v, want v2-smoke", got)
				}
				vocabCleared, err := c.Vocabularies.Update(ctx, vocab.ID, octonomy.VocabularyUpdate{
					Metadata: octonomy.Set(octonomy.Metadata{}),
				})
				if err != nil {
					t.Fatalf("Vocabularies.Update clearing metadata: %v", err)
				}
				if len(vocabCleared.Metadata) != 0 {
					t.Errorf("vocabulary Metadata = %v after Set(Metadata{}), want it cleared", vocabCleared.Metadata)
				}

				// The {data, pagination} list envelope. The vendored spec
				// documents list responses as bare arrays; the server sends the
				// envelope, and doList now requires a usable pagination block, so
				// only a real server proves the requirement matches what the
				// server actually emits.
				vocabs, err := c.Vocabularies.List(ctx, &octonomy.VocabularyListParams{
					ListOptions: octonomy.ListOptions{Limit: 50},
				})
				if err != nil {
					t.Fatalf("Vocabularies.List: %v", err)
				}
				if vocabs.Pagination.Limit != 50 {
					t.Errorf("vocabulary pagination did not decode: %+v", vocabs.Pagination)
				}

				// The two filters #36 added. Only a real server can prove either
				// one is READ rather than merely sent: an unknown query parameter
				// is dropped in silence, so a name the SDK got wrong -- or one
				// this route never supported -- comes back as a full, plausible
				// page that looks exactly like a filter that worked. The unit
				// test asserts the wire; this asserts the effect.
				//
				// A SECOND ROW IS WHAT MAKES THAT ASSERTION MEAN ANYTHING, and it
				// is the whole reason this decoy exists. A freshly booted harness
				// holds exactly one globally visible active vocabulary -- the one
				// created above, since the harness's own probe rows are
				// namespaced and invisible to this global client -- so "the
				// filtered page holds only our row" is equally true of a server
				// that ignored the parameter and returned the entire collection.
				// With a second visible row present, an ignored filter returns
				// two and every assertion below fails, which is the point.
				//
				// Its slug shares no substring with vocabSlug (`smoke-decoy-`
				// against `smoke-vocab-`, each with its own pid/nanos suffix), so
				// it cannot be swept in by the free-text lookup either.
				decoySlug := uniqueSlug("smoke-decoy")
				decoy, err := c.Vocabularies.Create(ctx, octonomy.VocabularyCreate{
					Name:        "v2 smoke decoy",
					Slug:        decoySlug,
					Description: octonomy.String("a second visible vocabulary, so a filtered lookup has something to exclude"),
				})
				if err != nil {
					t.Fatalf("Vocabularies.Create decoy: %v", err)
				}
				s.deleteLater("Vocabularies.Delete decoy", func(ctx context.Context) error {
					return c.Vocabularies.Delete(ctx, decoy.ID)
				})

				// Each slug returns its OWN row, which is two assertions in one:
				// the filter includes the match, and it excludes the other row
				// that is provably visible to this same client -- provably,
				// because the other lookup just returned it.
				for _, tc := range []struct {
					slug string
					want string
				}{
					{slug: s.vocabSlug, want: vocab.ID},
					{slug: decoySlug, want: decoy.ID},
				} {
					page, err := c.Vocabularies.List(ctx, &octonomy.VocabularyListParams{
						Slug:        octonomy.String(tc.slug),
						ListOptions: octonomy.ListOptions{Limit: 50},
					})
					if err != nil {
						t.Fatalf("Vocabularies.List(slug=%s): %v", tc.slug, err)
					}
					if len(page.Data) != 1 || page.Data[0].ID != tc.want {
						t.Fatalf("Vocabularies.List(slug=%s) returned %d rows, want only %s: %+v",
							tc.slug, len(page.Data), tc.want, page.Data)
					}
				}

				// `q` is a case-insensitive substring of the name OR the slug on
				// the server, so the slug in a different case is a match the
				// exact filter above would miss -- which is what tells the two
				// filters apart from here, rather than leaving `q` proved by a
				// value `slug` would have matched anyway.
				byQuery, err := c.Vocabularies.List(ctx, &octonomy.VocabularyListParams{
					Query:       octonomy.String(strings.ToUpper(s.vocabSlug)),
					ListOptions: octonomy.ListOptions{Limit: 50},
				})
				if err != nil {
					t.Fatalf("Vocabularies.List by q: %v", err)
				}
				if len(byQuery.Data) != 1 || byQuery.Data[0].ID != vocab.ID {
					t.Fatalf("Vocabularies.List(q=%s) returned %d rows, want only %s: %+v",
						strings.ToUpper(s.vocabSlug), len(byQuery.Data), vocab.ID, byQuery.Data)
				}

				// The one error whose STATUS and CODE disagree, against a real
				// server (#6). scope_immutable is raised as a subclass of the
				// server's conflict error, so it arrives as a 409 whose code is
				// not "conflict" -- and a fixture asserting that is a fixture
				// asserting what this SDK already believes. Only the server
				// settles whether IsScopeImmutable is true and IsConflict is
				// false on the same response.
				//
				// The vocabulary created above is global, so naming any
				// application at all is a scope move. The target need not exist:
				// the guard runs on the scope change itself, before anything
				// resolves the application. The row is unchanged by a 409, so the
				// cleanup registered above still applies.
				_, err = c.Vocabularies.Update(ctx, vocab.ID, octonomy.VocabularyUpdate{
					ApplicationID: octonomy.Set("smoke-scope-move"),
				})
				if err == nil {
					t.Fatal("Vocabularies.Update moving a global row into an application: expected a 409")
				}
				if !octonomy.IsScopeImmutable(err) {
					t.Errorf("scope-moving PATCH: IsScopeImmutable = false, err = %v", err)
				}
				if octonomy.IsConflict(err) {
					t.Error("scope-moving PATCH: IsConflict = true; scope_immutable must not read as a plain conflict")
				}
				scopeErr, ok := octonomy.AsAPIError(err)
				if !ok {
					t.Fatalf("scope-moving PATCH: error is not *APIError: %v", err)
				}
				if scopeErr.StatusCode != 409 || scopeErr.Code != octonomy.CodeScopeImmutable {
					t.Errorf("APIError = {status:%d code:%q}, want {409 %q}", scopeErr.StatusCode, scopeErr.Code, octonomy.CodeScopeImmutable)
				}
				// Details names the offending field. The doc comment on
				// IsScopeImmutable promises a caller can report which axis it
				// tried to move, and only the server's real payload backs that.
				if _, ok := scopeErr.Details["application_id"]; !ok {
					t.Errorf("Details did not name the offending field: %#v", scopeErr.Details)
				}
				// The row must not have moved. A 409 that mutated anyway would
				// leave the SDK reporting a refusal the server did not actually
				// make.
				stillGlobal, err := c.Vocabularies.Get(ctx, vocab.ID)
				if err != nil {
					t.Fatalf("Vocabularies.Get after a refused scope move: %v", err)
				}
				if stillGlobal.ApplicationID != nil {
					t.Errorf("a refused scope move still changed the row: application_id = %v", *stillGlobal.ApplicationID)
				}
			},
		},
		{
			// The same envelope on the other resource, plus metadata -- the
			// field most likely to be silently dropped by a wrong envelope
			// assumption -- the real error envelope, and the namespace axis.
			name: "Tag",
			assert: func(ctx context.Context, t *testing.T, c *octonomy.Client, s *smokeState) {
				s.tagSlug = uniqueSlug("smoke-tag")
				tag, err := c.Tags.Create(ctx, octonomy.TagCreate{
					Name:         "v2 smoke",
					Slug:         s.tagSlug,
					Type:         "label",
					VocabularyID: octonomy.String(s.vocab.ID),
					Metadata:     octonomy.Metadata{"source": "v2-smoke"},
				})
				if err != nil {
					t.Fatalf("Tags.Create: %v", err)
				}
				s.tag = tag
				s.deleteLater("Tags.Delete", func(ctx context.Context) error {
					return c.Tags.Delete(ctx, tag.ID)
				})
				if tag.ID == "" || tag.Slug != s.tagSlug {
					t.Fatalf("created tag did not round-trip: %+v", tag)
				}
				if got := tag.Metadata["source"]; got != "v2-smoke" {
					t.Errorf("tag.Metadata[source] = %v, want v2-smoke", got)
				}

				// Clearing a metadata object, which only a real server can
				// demonstrate (#37). A canned test can prove that "metadata": {}
				// leaves the client; that the server then EMPTIES the stored
				// object is its behavior, not the SDK's -- and the defect this
				// fixes was precisely a call that looked like it worked: while
				// the field was a plain map, encoding/json omitted an empty one
				// under omitempty, so the request carried no metadata key and the
				// server answered 200 with the old object still in place and no
				// error.
				//
				// It runs BEFORE the rename below deliberately. The AuditLog
				// entry reads the NEWEST tag.updated row and requires it to carry
				// the caller-supplied request id and the rename in its changes,
				// so the rename has to stay the last update on this tag.
				metaCleared, err := c.Tags.Update(ctx, tag.ID, octonomy.TagUpdate{
					Metadata: octonomy.Set(octonomy.Metadata{}),
				})
				if err != nil {
					t.Fatalf("Tags.Update clearing metadata: %v", err)
				}
				if len(metaCleared.Metadata) != 0 {
					t.Errorf("Metadata = %v after Set(Metadata{}), want the stored object cleared", metaCleared.Metadata)
				}
				// Put it back, so the rest of the walk sees the tag it was
				// written against. That also exercises the replace half through
				// the same field.
				metaRestored, err := c.Tags.Update(ctx, tag.ID, octonomy.TagUpdate{
					Metadata: octonomy.Set(octonomy.Metadata{"source": "v2-smoke"}),
				})
				if err != nil {
					t.Fatalf("Tags.Update restoring metadata: %v", err)
				}
				if got := metaRestored.Metadata["source"]; got != "v2-smoke" {
					t.Errorf("restored Metadata[source] = %v, want v2-smoke", got)
				}

				// An update, the third doData write path -- and the one call in
				// this file that supplies its own X-Request-ID (#5). Only a real
				// server can show where that id lands: the AuditLog entry reads
				// it back off the row the SERVER wrote as a side effect of this
				// call. The create above deliberately sends none, so the two rows
				// together prove both halves -- the caller's id threads through,
				// and the server still mints its own when there is none.
				s.updateRequestID = uniqueSlug("smoke-req")
				renamed, err := c.Tags.Update(ctx, tag.ID, octonomy.TagUpdate{
					Name: octonomy.Set("v2 smoke renamed"),
				}, octonomy.WithRequestID(s.updateRequestID))
				if err != nil {
					t.Fatalf("Tags.Update: %v", err)
				}
				if renamed.Name != "v2 smoke renamed" || renamed.ID != tag.ID {
					t.Fatalf("updated tag did not round-trip: %+v", renamed)
				}

				// The {data, pagination} list envelope on this resource.
				tags, err := c.Tags.List(ctx, &octonomy.TagListParams{
					Slug:        octonomy.String(s.tagSlug),
					ListOptions: octonomy.ListOptions{Limit: 10},
				})
				if err != nil {
					t.Fatalf("Tags.List: %v", err)
				}
				if len(tags.Data) != 1 || tags.Data[0].ID != tag.ID {
					t.Fatalf("Tags.List did not return the created tag: %+v", tags.Data)
				}
				if tags.Pagination.Limit != 10 || tags.Pagination.Count < 1 {
					t.Errorf("pagination block did not decode: %+v", tags.Pagination)
				}

				// DecodeMetadata against metadata the SERVER stored and returned,
				// rather than a map this test just built (#14). The tag carries
				// {"source": "v2-smoke"}, and it reaches here having survived a
				// real encode, a real round trip, and a real decode into
				// map[string]any.
				type smokeMeta struct {
					Source string `json:"source"`
				}
				decoded, err := octonomy.DecodeMetadata[smokeMeta](tag.Metadata)
				if err != nil {
					t.Fatalf("DecodeMetadata on server-returned metadata: %v", err)
				}
				if decoded.Source != "v2-smoke" {
					t.Errorf("DecodeMetadata gave Source = %q, want v2-smoke", decoded.Source)
				}

				// A real error envelope from the real server, not a canned
				// httptest body.
				_, err = c.Tags.Get(ctx, "00000000-0000-0000-0000-000000000000")
				if err == nil {
					t.Fatal("Tags.Get on a missing id: expected an error")
				}
				if !octonomy.IsNotFound(err) {
					t.Errorf("Tags.Get on a missing id: IsNotFound = false, err = %v", err)
				}
				apiErr, ok := octonomy.AsAPIError(err)
				if !ok {
					t.Fatalf("error is not *APIError: %v", err)
				}
				if apiErr.StatusCode != 404 || apiErr.Code != octonomy.CodeNotFound {
					t.Errorf("APIError = {status:%d code:%q}, want {404 %q}", apiErr.StatusCode, apiErr.Code, octonomy.CodeNotFound)
				}

				// The namespace axis, which exists only on /api/v2.
				//
				// A unit test cannot reach this: the namespace response fields
				// are populated by the server from the request headers, so a
				// canned fixture asserts only that the SDK can echo a value it
				// wrote itself. Here the round trip is real -- headers out,
				// persisted scope back -- which is the same class of gap that hid
				// #32.
				//
				// The client targets APIV2 by default, so this needs no second
				// client.
				//
				// The global rows created above must report no namespace. This is
				// the assertion that would fail if the SDK ever grew a
				// client-level namespace default and started scoping every call.
				if tag.NamespaceType != nil || tag.NamespaceID != nil {
					t.Errorf("a global tag reported a namespace: type=%v id=%v", tag.NamespaceType, tag.NamespaceID)
				}

				nsSlug := uniqueSlug("smoke-ns-tag")
				nsTag, err := c.Tags.Create(ctx, octonomy.TagCreate{
					Name:          "v2 smoke namespaced",
					Slug:          nsSlug,
					Type:          "label",
					ApplicationID: octonomy.String(s.appID),
				}, octonomy.WithNamespace(s.nsType, s.nsID))
				if err != nil {
					// A 403 namespaced_writes_disabled here means the harness did
					// not pass OCTONOMY_NAMESPACE_WRITE_ENABLED=true through to
					// the container, which is a harness fault rather than an SDK
					// one -- say which.
					if octonomy.IsNamespacedWritesDisabled(err) {
						t.Fatalf("namespaced writes are disabled on this deployment: the harness must set OCTONOMY_NAMESPACE_WRITE_ENABLED=true (server default is false): %v", err)
					}
					t.Fatalf("Tags.Create (namespaced): %v", err)
				}
				s.nsTag = nsTag
				s.deleteLater("Tags.Delete (namespaced)", func(ctx context.Context) error {
					return c.Tags.Delete(ctx, nsTag.ID,
						octonomy.WithNamespace(s.nsType, s.nsID), octonomy.WithApplication(s.appID))
				})
				if nsTag.NamespaceType == nil || *nsTag.NamespaceType != s.nsType {
					t.Errorf("created namespaced tag: NamespaceType = %v, want %q", nsTag.NamespaceType, s.nsType)
				}
				if nsTag.NamespaceID == nil || *nsTag.NamespaceID != s.nsID {
					t.Errorf("created namespaced tag: NamespaceID = %v, want %q", nsTag.NamespaceID, s.nsID)
				}

				// The read path carries the headers too, and the persisted scope
				// survives it.
				nsFetched, err := c.Tags.Get(ctx, nsTag.ID,
					octonomy.WithNamespace(s.nsType, s.nsID), octonomy.WithApplication(s.appID))
				if err != nil {
					t.Fatalf("Tags.Get (namespaced): %v", err)
				}
				if nsFetched.NamespaceID == nil || *nsFetched.NamespaceID != s.nsID {
					t.Errorf("fetched namespaced tag: NamespaceID = %v, want %q", nsFetched.NamespaceID, s.nsID)
				}

				// A namespaced list excludes global rows by default, and the tag
				// created above is global -- so it must not appear here. This is
				// the assertion that proves the headers actually reached the
				// server: without them the server serves the global namespace
				// with a 200 and this list would contain it.
				nsList, err := c.Tags.List(ctx, &octonomy.TagListParams{
					ListOptions: octonomy.ListOptions{Limit: 100},
				}, octonomy.WithNamespace(s.nsType, s.nsID), octonomy.WithApplication(s.appID))
				if err != nil {
					t.Fatalf("Tags.List (namespaced): %v", err)
				}
				sawNamespaced, sawGlobal := false, false
				for _, row := range nsList.Data {
					switch row.ID {
					case nsTag.ID:
						sawNamespaced = true
					case tag.ID:
						sawGlobal = true
					}
				}
				if !sawNamespaced {
					t.Errorf("namespaced list did not return the namespaced tag %s", nsTag.ID)
				}
				if sawGlobal {
					t.Errorf("namespaced list returned the GLOBAL tag %s: a namespaced read must exclude global rows unless include_global is set", tag.ID)
				}
			},
		},
		{
			// A third response shape, and the third schema carrying namespace
			// identity. The recipe requires a new shape to be asserted here and
			// not only against handcrafted fixtures, for the reason the file's
			// header gives -- a fixture written from the vendored spec passes
			// against a client that is wrong, which is exactly how #32 survived a
			// full unit suite. The spec describes both alias list routes as bare
			// arrays.
			//
			// This entry also carries the only real-collection pagination walk in
			// the file, on aliases of its own, for the reasons recorded at the
			// bottom of it.
			name: "TagAlias",
			assert: func(ctx context.Context, t *testing.T, c *octonomy.Client, s *smokeState) {
				s.aliasSlug = uniqueSlug("smoke-alias")
				alias, err := c.Aliases.Create(ctx, octonomy.TagAliasCreate{
					TagID:    s.tag.ID,
					Name:     "v2 smoke alias",
					Slug:     s.aliasSlug,
					Metadata: octonomy.Metadata{"source": "v2-smoke"},
				})
				if err != nil {
					t.Fatalf("Aliases.Create: %v", err)
				}
				s.alias = alias
				s.deleteLater("Aliases.Delete", func(ctx context.Context) error {
					return c.Aliases.Delete(ctx, alias.ID)
				})
				if alias.ID == "" || alias.Slug != s.aliasSlug || alias.TagID != s.tag.ID {
					t.Fatalf("created alias did not round-trip: %+v", alias)
				}
				if got := alias.Metadata["source"]; got != "v2-smoke" {
					t.Errorf("alias.Metadata[source] = %v, want v2-smoke", got)
				}
				// A global alias reports no namespace, the same invariant
				// asserted for the global tag in the Tag entry.
				if alias.NamespaceType != nil || alias.NamespaceID != nil {
					t.Errorf("a global alias reported a namespace: type=%v id=%v", alias.NamespaceType, alias.NamespaceID)
				}

				aliasFetched, err := c.Aliases.Get(ctx, alias.ID)
				if err != nil {
					t.Fatalf("Aliases.Get: %v", err)
				}
				if aliasFetched.ID != alias.ID || aliasFetched.TagID != s.tag.ID {
					t.Fatalf("fetched alias did not round-trip: %+v", aliasFetched)
				}

				aliasRenamed, err := c.Aliases.Update(ctx, alias.ID, octonomy.TagAliasUpdate{
					Name: octonomy.Set("v2 smoke alias renamed"),
				})
				if err != nil {
					t.Fatalf("Aliases.Update: %v", err)
				}
				if aliasRenamed.Name != "v2 smoke alias renamed" || aliasRenamed.ID != alias.ID {
					t.Fatalf("updated alias did not round-trip: %+v", aliasRenamed)
				}

				// Both list routes, because they are separate doList call sites
				// reaching different server views of the same rows.
				aliasPage, err := c.Aliases.List(ctx, &octonomy.TagAliasListParams{
					TagID:       octonomy.String(s.tag.ID),
					ListOptions: octonomy.ListOptions{Limit: 10},
				})
				if err != nil {
					t.Fatalf("Aliases.List: %v", err)
				}
				if len(aliasPage.Data) != 1 || aliasPage.Data[0].ID != alias.ID {
					t.Fatalf("Aliases.List did not return the created alias: %+v", aliasPage.Data)
				}
				if aliasPage.Pagination.Limit != 10 || aliasPage.Pagination.Count < 1 {
					t.Errorf("alias pagination block did not decode: %+v", aliasPage.Pagination)
				}

				nested, err := c.Tags.ListAliases(ctx, s.tag.ID, &octonomy.TagListAliasesParams{
					ListOptions: octonomy.ListOptions{Limit: 10},
				})
				if err != nil {
					t.Fatalf("Tags.ListAliases: %v", err)
				}
				nestedFound := false
				for _, row := range nested.Data {
					if row.ID == alias.ID {
						nestedFound = true
						break
					}
				}
				if !nestedFound {
					t.Errorf("Tags.ListAliases returned %d rows, none of them the created alias %s", len(nested.Data), alias.ID)
				}

				// TagAliasUpdate's metadata field, the third of the three (#37).
				// The alias was created carrying {"source": "v2-smoke"}, so there
				// is a real stored object here for {} to clear -- see the Tag
				// entry for why only a real server settles this.
				aliasCleared, err := c.Aliases.Update(ctx, alias.ID, octonomy.TagAliasUpdate{
					Metadata: octonomy.Set(octonomy.Metadata{}),
				})
				if err != nil {
					t.Fatalf("Aliases.Update clearing metadata: %v", err)
				}
				if len(aliasCleared.Metadata) != 0 {
					t.Errorf("alias Metadata = %v after Set(Metadata{}), want it cleared", aliasCleared.Metadata)
				}
				aliasRestored, err := c.Aliases.Update(ctx, alias.ID, octonomy.TagAliasUpdate{
					Metadata: octonomy.Set(octonomy.Metadata{"source": "v2-smoke"}),
				})
				if err != nil {
					t.Fatalf("Aliases.Update restoring metadata: %v", err)
				}
				if got := aliasRestored.Metadata["source"]; got != "v2-smoke" {
					t.Errorf("restored alias Metadata[source] = %v, want v2-smoke", got)
				}

				// The namespace fields on TagAlias are server-set from the
				// request headers, so only a real server can populate them -- a
				// fixture would assert that the SDK echoes a value it wrote
				// itself. The target is the namespaced tag from the Tag entry,
				// because a namespaced alias may only point at a global or
				// same-namespace tag.
				nsAlias, err := c.Aliases.Create(ctx, octonomy.TagAliasCreate{
					TagID:         s.nsTag.ID,
					Name:          "v2 smoke alias namespaced",
					Slug:          uniqueSlug("smoke-ns-alias"),
					ApplicationID: octonomy.String(s.appID),
				}, octonomy.WithNamespace(s.nsType, s.nsID))
				if err != nil {
					t.Fatalf("Aliases.Create (namespaced): %v", err)
				}
				s.deleteLater("Aliases.Delete (namespaced)", func(ctx context.Context) error {
					return c.Aliases.Delete(ctx, nsAlias.ID,
						octonomy.WithNamespace(s.nsType, s.nsID), octonomy.WithApplication(s.appID))
				})
				if nsAlias.NamespaceType == nil || *nsAlias.NamespaceType != s.nsType {
					t.Errorf("created namespaced alias: NamespaceType = %v, want %q", nsAlias.NamespaceType, s.nsType)
				}
				if nsAlias.NamespaceID == nil || *nsAlias.NamespaceID != s.nsID {
					t.Errorf("created namespaced alias: NamespaceID = %v, want %q", nsAlias.NamespaceID, s.nsID)
				}

				// Each over a real multi-page collection (#14). The unit tests
				// drive it against a fixture reproducing four beliefs about the
				// server's paginator: count is the total rather than the page
				// size, next goes nil at the end, limit is clamped to 200, and
				// the requested offset is echoed back. Each READS two of them --
				// next to stop, the echoed offset to catch a page function that
				// dropped its options. The clamp is why it advances by what
				// arrived rather than by the limit it asked for, and count is
				// what a CALLER needs to detect a short walk; neither is read by
				// the walker. All four are pinned here against a real server
				// rather than against a fixture that merely agrees with the
				// walker.
				//
				// It walks ALIASES, not tags, and via the nested route. The
				// nested route is the closure shape Each's doc comment advertises
				// for positional ids, and nothing else here covers it.
				//
				// It STAYS on aliases now that server 3.2.1 has ordered the tags
				// list (octonomy#162, octonomy-go#49). Moving it would have
				// traded the only coverage of that nested shape for coverage of a
				// list that is now walked separately --
				// TestIntegration_TagsListPagesInATotalOrder, in
				// integration_suite_test.go, was ADDED rather than swapped in for
				// exactly that reason. It lives there rather than here because
				// tags ordering is a SEMANTIC property of the server, which is
				// that file's remit, while this one covers response SHAPES.
				//
				// The second reason this walk was on aliases is gone, though:
				// both lists are totally ordered by (name, slug, id) against the
				// pinned harness, so it is no longer the case that only one of
				// them can carry a deterministic assertion.
				//
				// The aliases hang off a tag of their own rather than off the one
				// this entry asserted above to hold exactly one alias. Sharing it
				// would have made this walk break that assertion -- and a walk
				// fixture is not worth weakening an assertion elsewhere to
				// accommodate.
				const walkAliases = 3
				walkTag, err := c.Tags.Create(ctx, octonomy.TagCreate{
					Name: "v2 smoke walk", Slug: uniqueSlug("smoke-walk-tag"), Type: "label",
				})
				if err != nil {
					t.Fatalf("Tags.Create for the walk: %v", err)
				}
				s.deleteLater("Tags.Delete for the walk", func(ctx context.Context) error {
					return c.Tags.Delete(ctx, walkTag.ID)
				})

				wantAliasIDs := map[string]bool{}
				for i := 0; i < walkAliases; i++ {
					slug := uniqueSlug(fmt.Sprintf("smoke-walk-%d", i))
					created, err := c.Aliases.Create(ctx, octonomy.TagAliasCreate{
						TagID: walkTag.ID,
						Name:  fmt.Sprintf("walk %d", i),
						Slug:  slug,
					})
					if err != nil {
						t.Fatalf("Aliases.Create for the walk: %v", err)
					}
					wantAliasIDs[created.ID] = true
					id := created.ID
					s.deleteLater("Aliases.Delete for the walk", func(ctx context.Context) error {
						return c.Aliases.Delete(ctx, id)
					})
				}

				// count is the TOTAL across pages, not the size of this one, and
				// the server clamps an over-large limit and echoes the clamped
				// value back. The CLAMP is what makes Each's advance-by-what-
				// arrived rule necessary -- advancing by the limit requested
				// would skip whatever the clamp withheld. count carries no weight
				// in the walk at all; it is asserted because the short-walk
				// detector Each documents is built on it.
				onePage, err := c.Tags.ListAliases(ctx, walkTag.ID, &octonomy.TagListAliasesParams{
					ListOptions: octonomy.ListOptions{Limit: 1},
				})
				if err != nil {
					t.Fatalf("Tags.ListAliases: %v", err)
				}
				if len(onePage.Data) != 1 {
					t.Fatalf("asked for 1 alias, got %d", len(onePage.Data))
				}
				if onePage.Pagination.Count != walkAliases {
					t.Errorf("pagination.count = %d on a 1-row page, want %d (the total, not the page size)",
						onePage.Pagination.Count, walkAliases)
				}
				clamped, err := c.Tags.ListAliases(ctx, walkTag.ID, &octonomy.TagListAliasesParams{
					ListOptions: octonomy.ListOptions{Limit: 500},
				})
				if err != nil {
					t.Fatalf("Tags.ListAliases with an over-large limit: %v", err)
				}
				if clamped.Pagination.Limit != 200 {
					t.Errorf("asked for limit 500, server echoed %d, want the 200 clamp", clamped.Pagination.Limit)
				}

				pages := 0
				visits := map[string]int{}
				offset, err := octonomy.Each(ctx, octonomy.ListOptions{Limit: 1},
					func(ctx context.Context, o octonomy.ListOptions) (*octonomy.List[octonomy.TagAlias], error) {
						pages++
						return c.Tags.ListAliases(ctx, walkTag.ID, &octonomy.TagListAliasesParams{ListOptions: o})
					},
					func(walked octonomy.TagAlias) error {
						visits[walked.ID]++
						return nil
					})
				if err != nil {
					t.Fatalf("Each over a real collection: %v", err)
				}
				if offset != walkAliases {
					t.Errorf("Each returned offset %d, want %d", offset, walkAliases)
				}
				// Exactly once each: neither a skipped row nor a redelivered one.
				if len(visits) != len(wantAliasIDs) {
					t.Errorf("walked %d distinct aliases, want %d", len(visits), len(wantAliasIDs))
				}
				for id := range wantAliasIDs {
					if visits[id] != 1 {
						t.Errorf("alias %s visited %d times, want exactly 1", id, visits[id])
					}
				}
				// One request per page, as the doc comment promises. Three items
				// at one per page is three pages -- a fourth would mean the
				// walker only stops on an empty page and never reads the server's
				// own end-of-collection signal.
				if pages != walkAliases {
					t.Errorf("Each made %d requests for %d items at Limit 1, want %d", pages, walkAliases, walkAliases)
				}
			},
		},
		{
			// A composite payload rather than a resource, and the only response
			// in this SDK that nests one model inside another. A fixture proves
			// nothing about which of the two tags the server puts in `tag` --
			// only a real alias resolving to a real canonical tag does.
			name: "TagResolution",
			assert: func(ctx context.Context, t *testing.T, c *octonomy.Client, s *smokeState) {
				resolvedTag, err := c.Tags.Resolve(ctx, s.tagSlug, nil)
				if err != nil {
					t.Fatalf("Tags.Resolve (canonical): %v", err)
				}
				if resolvedTag.MatchedType != octonomy.MatchedTypeTag {
					t.Errorf("MatchedType = %q, want %q", resolvedTag.MatchedType, octonomy.MatchedTypeTag)
				}
				if resolvedTag.MatchedAlias != nil {
					t.Errorf("a canonical match carried an alias: %+v", resolvedTag.MatchedAlias)
				}
				if resolvedTag.Tag.ID != s.tag.ID {
					t.Errorf("resolved tag = %s, want the created tag %s", resolvedTag.Tag.ID, s.tag.ID)
				}

				// The alias branch, resolving the alias slug the TagAlias entry
				// created. `tag` must be the CANONICAL tag, not the alias's own
				// row -- the assertion a canned fixture cannot make honestly,
				// because it would be asserting a value the test itself chose.
				resolvedAlias, err := c.Tags.Resolve(ctx, s.aliasSlug, nil)
				if err != nil {
					t.Fatalf("Tags.Resolve (via alias): %v", err)
				}
				if resolvedAlias.MatchedType != octonomy.MatchedTypeAlias {
					t.Errorf("MatchedType = %q, want %q", resolvedAlias.MatchedType, octonomy.MatchedTypeAlias)
				}
				if resolvedAlias.MatchedAlias == nil {
					t.Fatal("MatchedAlias = nil on an alias match")
				}
				if resolvedAlias.MatchedAlias.ID != s.alias.ID {
					t.Errorf("MatchedAlias = %s, want the created alias %s", resolvedAlias.MatchedAlias.ID, s.alias.ID)
				}
				if resolvedAlias.Tag.ID != s.tag.ID {
					t.Errorf("alias resolved to tag %s, want the canonical %s", resolvedAlias.Tag.ID, s.tag.ID)
				}
				if resolvedAlias.Tag.Slug != s.tagSlug {
					t.Errorf("resolved tag slug = %q, want the canonical %q (not the alias slug)", resolvedAlias.Tag.Slug, s.tagSlug)
				}

				// An unmatched slug is a 400 validation_error, NOT a 404 -- the
				// one piece of this endpoint's contract a caller is most likely
				// to get wrong, and the one most worth pinning against the real
				// server rather than a fixture that simply restates the claim.
				_, err = c.Tags.Resolve(ctx, uniqueSlug("smoke-no-such"), nil)
				if err == nil {
					t.Fatal("Tags.Resolve on an unmatched slug: expected an error")
				}
				if !octonomy.IsValidation(err) {
					t.Errorf("Tags.Resolve on an unmatched slug: IsValidation = false, err = %v", err)
				}
				if octonomy.IsNotFound(err) {
					t.Error("Tags.Resolve on an unmatched slug reported not_found: the server answers 400, and the SDK documents IsValidation as the branch")
				}
			},
		},
		{
			// The resource whose documented response shapes are furthest from
			// the server's. Assignments are always application-scoped, so these
			// use the harness application; the tag from the Tag entry is global,
			// which is assignable in any application.
			//
			// It leaves the assignment IN PLACE: the two bulk entries after it
			// operate on the same resource, and their counts are what prove the
			// server distinguishes an existing assignment from a new one.
			name: "Assignment",
			assert: func(ctx context.Context, t *testing.T, c *octonomy.Client, s *smokeState) {
				s.assignResourceID = uniqueSlug("smoke-order")
				assignment, err := c.Assignments.Create(ctx, octonomy.AssignmentCreate{
					ApplicationID: s.appID,
					TagID:         octonomy.String(s.tag.ID),
					ResourceType:  "order",
					ResourceID:    s.assignResourceID,
					AssignedBy:    octonomy.String("v2-smoke"),
				})
				if err != nil {
					t.Fatalf("Assignments.Create: %v", err)
				}
				if assignment.ID == "" || assignment.TagID != s.tag.ID || assignment.ResourceID != s.assignResourceID {
					t.Fatalf("created assignment did not round-trip: %+v", assignment)
				}
				if assignment.ApplicationID != s.appID {
					t.Errorf("ApplicationID = %q, want %q", assignment.ApplicationID, s.appID)
				}
				if assignment.AssignedBy == nil || *assignment.AssignedBy != "v2-smoke" {
					t.Errorf("AssignedBy = %v, want v2-smoke", assignment.AssignedBy)
				}
				if assignment.AssignedAt.IsZero() {
					t.Error("AssignedAt did not decode")
				}

				// Idempotent: the same assignment again returns the SAME row
				// rather than a duplicate or an error. Asserting the id is
				// stronger than asserting the status, which the SDK deliberately
				// does not surface.
				again, err := c.Assignments.Create(ctx, octonomy.AssignmentCreate{
					ApplicationID: s.appID,
					TagID:         octonomy.String(s.tag.ID),
					ResourceType:  "order",
					ResourceID:    s.assignResourceID,
				})
				if err != nil {
					t.Fatalf("Assignments.Create (repeat): %v", err)
				}
				if again.ID != assignment.ID {
					t.Errorf("re-assigning produced a new row %s, want the existing %s", again.ID, assignment.ID)
				}

				// The alias form resolves to the canonical tag, which is the
				// assertion a fixture cannot make: the server does the resolving.
				viaAlias, err := c.Assignments.Create(ctx, octonomy.AssignmentCreate{
					ApplicationID: s.appID,
					AliasSlug:     octonomy.String(s.aliasSlug),
					ResourceType:  "order",
					ResourceID:    s.assignResourceID,
				})
				if err != nil {
					t.Fatalf("Assignments.Create (by alias slug): %v", err)
				}
				if viaAlias.TagID != s.tag.ID {
					t.Errorf("alias slug assigned tag %s, want the canonical %s", viaAlias.TagID, s.tag.ID)
				}

				// A NAMESPACED assignment, whose namespace pair the server
				// populates from the request headers. Only a real server can
				// assert this: every canned fixture is marshalled from Assignment
				// itself, so a misspelled json tag is used for both the write and
				// the read and round-trips perfectly. Verified by breaking the
				// tags -- the whole unit suite and this smoke test stayed green
				// until these assertions existed.
				//
				// The target is the namespaced tag from the Tag entry, since a
				// namespaced write must stay inside its own scope.
				nsResourceID := uniqueSlug("smoke-ns-order")
				nsAssignment, err := c.Assignments.Create(ctx, octonomy.AssignmentCreate{
					ApplicationID: s.appID,
					TagID:         octonomy.String(s.nsTag.ID),
					ResourceType:  "order",
					ResourceID:    nsResourceID,
				}, octonomy.WithNamespace(s.nsType, s.nsID))
				if err != nil {
					t.Fatalf("Assignments.Create (namespaced): %v", err)
				}
				if nsAssignment.NamespaceType == nil || *nsAssignment.NamespaceType != s.nsType {
					t.Errorf("namespaced assignment: NamespaceType = %v, want %q", nsAssignment.NamespaceType, s.nsType)
				}
				if nsAssignment.NamespaceID == nil || *nsAssignment.NamespaceID != s.nsID {
					t.Errorf("namespaced assignment: NamespaceID = %v, want %q", nsAssignment.NamespaceID, s.nsID)
				}
				if nsAssignment.TagID != s.nsTag.ID {
					t.Errorf("namespaced assignment: TagID = %s, want %s", nsAssignment.TagID, s.nsTag.ID)
				}

				// And the namespaced body-carrying DELETE, which no other
				// resource exercises: the headers scope the request while the
				// body identifies the row.
				if err := c.Assignments.Remove(ctx, octonomy.AssignmentRemove{
					ApplicationID: s.appID,
					TagID:         s.nsTag.ID,
					ResourceType:  "order",
					ResourceID:    nsResourceID,
				}, octonomy.WithNamespace(s.nsType, s.nsID)); err != nil {
					t.Fatalf("Assignments.Remove (namespaced): %v", err)
				}
			},
		},
		{
			// The composite the spec calls a bare array. A []Assignment decoder
			// against this body yields an empty slice and a nil error, so this is
			// the assertion that says which shape the server really sends.
			name: "BulkAssignResult",
			assert: func(ctx context.Context, t *testing.T, c *octonomy.Client, s *smokeState) {
				bulk, err := c.Assignments.BulkAssign(ctx, octonomy.BulkAssign{
					ApplicationID: s.appID,
					ResourceType:  "order",
					ResourceID:    s.assignResourceID,
					TagIDs:        []string{s.tag.ID},
				})
				if err != nil {
					t.Fatalf("Assignments.BulkAssign: %v", err)
				}
				// The tag is already assigned by the Assignment entry, so this
				// must count as existing rather than created -- which also proves
				// the counts are real and not zero-valued.
				if bulk.Existing != 1 || bulk.Created != 0 {
					t.Errorf("bulk counts = created:%d existing:%d, want created:0 existing:1", bulk.Created, bulk.Existing)
				}
				if len(bulk.Assignments) != 1 || bulk.Assignments[0].TagID != s.tag.ID {
					t.Errorf("bulk assignments did not round-trip: %+v", bulk.Assignments)
				}
			},
		},
		{
			// The shape the spec does not describe at all.
			name: "BulkRemoveResult",
			assert: func(ctx context.Context, t *testing.T, c *octonomy.Client, s *smokeState) {
				removed, err := c.Assignments.BulkRemove(ctx, octonomy.BulkRemove{
					ApplicationID: s.appID,
					ResourceType:  "order",
					ResourceID:    s.assignResourceID,
					TagIDs:        []string{s.tag.ID},
				})
				if err != nil {
					t.Fatalf("Assignments.BulkRemove: %v", err)
				}
				if removed.Removed != 1 {
					t.Errorf("Removed = %d, want 1", removed.Removed)
				}

				// Removing what is no longer there is a 204, not a 404 -- which
				// is asserted here because the bulk remove above is what makes
				// the row absent.
				if err := c.Assignments.Remove(ctx, octonomy.AssignmentRemove{
					ApplicationID: s.appID,
					TagID:         s.tag.ID,
					ResourceType:  "order",
					ResourceID:    s.assignResourceID,
				}); err != nil {
					t.Fatalf("Assignments.Remove on an already-removed assignment: %v", err)
				}
			},
		},
		{
			// A THIRD composite shape, and the resource's own view of tagging.
			// docs/openapi-v2.yaml is wrong about the replace response twice over
			// -- it claims a bare array, and claims the elements are ResourceTag,
			// where the server sends {"created", "removed", "tags"} under the
			// envelope with Tag values in it.
			//
			// It leaves two resources behind on purpose. replaceResourceID keeps
			// its tag, because the two list entries after this one need a row to
			// read; clearedResourceID is assigned and then cleared here, which
			// keeps the destructive case away from the rows those entries read
			// and leaves exactly the two operations the AuditLog entry
			// reconstructs.
			name: "ResourceReplaceResult",
			assert: func(ctx context.Context, t *testing.T, c *octonomy.Client, s *smokeState) {
				s.replaceResourceID = uniqueSlug("smoke-cart")
				replaced, err := c.Resources.ReplaceTags(ctx, "cart", s.replaceResourceID, octonomy.ResourceReplace{
					ApplicationID: s.appID,
					TagIDs:        []string{s.tag.ID},
					AssignedBy:    octonomy.String("v2-smoke"),
				})
				if err != nil {
					t.Fatalf("Resources.ReplaceTags: %v", err)
				}
				if replaced.Created != 1 || replaced.Removed != 0 {
					t.Errorf("replace counts = created:%d removed:%d, want 1 and 0", replaced.Created, replaced.Removed)
				}
				if len(replaced.Tags) != 1 || replaced.Tags[0].ID != s.tag.ID {
					t.Fatalf("replace returned %+v, want the one tag", replaced.Tags)
				}
				// Tags, not ResourceTags -- the field would be empty if the
				// element type were wrong, which is the half of the spec's claim
				// an envelope fix alone misses.
				if replaced.Tags[0].Slug != s.tagSlug {
					t.Errorf("replace tag slug = %q, want %q", replaced.Tags[0].Slug, s.tagSlug)
				}

				// The namespaced replace, whose rows the two list entries read
				// back for the namespace pair the server populated.
				s.nsCartID = uniqueSlug("smoke-ns-cart")
				if _, err := c.Resources.ReplaceTags(ctx, "cart", s.nsCartID, octonomy.ResourceReplace{
					ApplicationID: s.appID,
					TagIDs:        []string{s.nsTag.ID},
				}, octonomy.WithNamespace(s.nsType, s.nsID)); err != nil {
					t.Fatalf("Resources.ReplaceTags (namespaced): %v", err)
				}

				// An EMPTY replace is legal and clears the resource. Proven here
				// rather than asserted in a doc comment, because it is the
				// destructive case a caller reaches by accident with a filter
				// that matched nothing.
				s.clearedResourceID = uniqueSlug("smoke-cleared-cart")
				assigned, err := c.Resources.ReplaceTags(ctx, "cart", s.clearedResourceID, octonomy.ResourceReplace{
					ApplicationID: s.appID,
					TagIDs:        []string{s.tag.ID},
				})
				if err != nil {
					t.Fatalf("Resources.ReplaceTags (the row to clear): %v", err)
				}
				if assigned.Created != 1 || assigned.Removed != 0 {
					t.Errorf("replace counts = created:%d removed:%d, want 1 and 0", assigned.Created, assigned.Removed)
				}
				cleared, err := c.Resources.ReplaceTags(ctx, "cart", s.clearedResourceID, octonomy.ResourceReplace{
					ApplicationID: s.appID,
				})
				if err != nil {
					t.Fatalf("Resources.ReplaceTags (empty): %v", err)
				}
				if cleared.Removed != 1 || cleared.Created != 0 {
					t.Errorf("empty replace counts = created:%d removed:%d, want 0 and 1", cleared.Created, cleared.Removed)
				}
				if len(cleared.Tags) != 0 {
					t.Errorf("empty replace left %d tags, want none", len(cleared.Tags))
				}
			},
		},
		{
			// The resource's view: an assignment row with the tag nested in it.
			// Both this model and TagResource carry the namespace pair, so both
			// are asserted against a server that populates it. That is the gap
			// #41 closed for Assignment the hard way: renaming a namespace json
			// tag left every fixture green, because fixtures are marshalled from
			// the same struct they are decoded into.
			name: "ResourceTag",
			assert: func(ctx context.Context, t *testing.T, c *octonomy.Client, s *smokeState) {
				resourceTags, err := c.Resources.ListTags(ctx, "cart", s.replaceResourceID,
					&octonomy.ResourceListTagsParams{ApplicationID: octonomy.String(s.appID)})
				if err != nil {
					t.Fatalf("Resources.ListTags: %v", err)
				}
				if len(resourceTags.Data) != 1 {
					t.Fatalf("ListTags returned %d rows, want 1", len(resourceTags.Data))
				}
				rt := resourceTags.Data[0]
				if rt.AssignmentID == "" || rt.AssignedAt.IsZero() {
					t.Errorf("assignment fields did not decode: %+v", rt)
				}
				// The nested tag is the shape's whole point.
				if rt.Tag.ID != s.tag.ID || rt.Tag.Slug != s.tagSlug {
					t.Errorf("nested tag = %+v, want the created tag %s", rt.Tag, s.tag.ID)
				}
				if rt.AssignedBy == nil || *rt.AssignedBy != "v2-smoke" {
					t.Errorf("AssignedBy = %v, want v2-smoke", rt.AssignedBy)
				}
				// A global assignment reports no namespace.
				if rt.NamespaceType != nil || rt.NamespaceID != nil {
					t.Errorf("a global resource tag reported a namespace: %+v", rt)
				}

				// The namespace pair the server populated on the namespaced
				// replace -- the assertion no fixture can make honestly.
				nsResourceTags, err := c.Resources.ListTags(ctx, "cart", s.nsCartID,
					&octonomy.ResourceListTagsParams{ApplicationID: octonomy.String(s.appID)},
					octonomy.WithNamespace(s.nsType, s.nsID))
				if err != nil {
					t.Fatalf("Resources.ListTags (namespaced): %v", err)
				}
				if len(nsResourceTags.Data) != 1 {
					t.Fatalf("namespaced ListTags returned %d rows, want 1", len(nsResourceTags.Data))
				}
				if got := nsResourceTags.Data[0]; got.NamespaceType == nil || *got.NamespaceType != s.nsType ||
					got.NamespaceID == nil || *got.NamespaceID != s.nsID {
					t.Errorf("namespaced resource tag: namespace = %v/%v, want %q/%q",
						got.NamespaceType, got.NamespaceID, s.nsType, s.nsID)
				}
			},
		},
		{
			// The mirror route, from the tag's side.
			name: "TagResource",
			assert: func(ctx context.Context, t *testing.T, c *octonomy.Client, s *smokeState) {
				tagResources, err := c.Tags.ListResources(ctx, s.tag.ID, &octonomy.TagListResourcesParams{
					ApplicationID: octonomy.String(s.appID),
					ResourceType:  octonomy.String("cart"),
				})
				if err != nil {
					t.Fatalf("Tags.ListResources: %v", err)
				}
				foundResource := false
				for _, row := range tagResources.Data {
					if row.ResourceID == s.replaceResourceID {
						foundResource = true
						if row.ResourceType != "cart" || row.ApplicationID != s.appID {
							t.Errorf("resource row did not round-trip: %+v", row)
						}
						if row.AssignedAt.IsZero() {
							t.Errorf("AssignedAt did not decode: %+v", row)
						}
					}
				}
				if !foundResource {
					t.Errorf("Tags.ListResources returned %d rows, none of them %s", len(tagResources.Data), s.replaceResourceID)
				}

				nsTagResources, err := c.Tags.ListResources(ctx, s.nsTag.ID, &octonomy.TagListResourcesParams{
					ApplicationID: octonomy.String(s.appID),
				}, octonomy.WithNamespace(s.nsType, s.nsID))
				if err != nil {
					t.Fatalf("Tags.ListResources (namespaced): %v", err)
				}
				if len(nsTagResources.Data) == 0 {
					t.Fatal("namespaced ListResources returned nothing")
				}
				if got := nsTagResources.Data[0]; got.NamespaceType == nil || *got.NamespaceType != s.nsType ||
					got.NamespaceID == nil || *got.NamespaceID != s.nsID {
					t.Errorf("namespaced tag resource: namespace = %v/%v, want %q/%q",
						got.NamespaceType, got.NamespaceID, s.nsType, s.nsID)
				}

				// A resource id carrying a character that needs escaping must
				// address the resource the caller named. Escaped twice -- as
				// every path was before resolvePath -- "ord 9" reached the server
				// as the literal "ord%209", a DIFFERENT resource, which on the
				// destructive replace route means replacing a tag set nobody
				// asked for. Verified against the server: the two spellings
				// really do produce two rows.
				//
				// The write is here rather than in the ResourceReplaceResult
				// entry because this list is what settles the question: the
				// replace succeeds either way, and only reading the row back
				// under the name the caller used can tell the two spellings
				// apart.
				spacedResourceID := uniqueSlug("smoke order")
				if _, err := c.Resources.ReplaceTags(ctx, "cart", spacedResourceID, octonomy.ResourceReplace{
					ApplicationID: s.appID,
					TagIDs:        []string{s.tag.ID},
				}); err != nil {
					t.Fatalf("Resources.ReplaceTags (id with a space): %v", err)
				}
				spacedRows, err := c.Tags.ListResources(ctx, s.tag.ID, &octonomy.TagListResourcesParams{
					ApplicationID: octonomy.String(s.appID),
					ResourceType:  octonomy.String("cart"),
				})
				if err != nil {
					t.Fatalf("Tags.ListResources (id with a space): %v", err)
				}
				spacedFound := false
				for _, row := range spacedRows.Data {
					if row.ResourceID == spacedResourceID {
						spacedFound = true
					}
				}
				if !spacedFound {
					var got []string
					for _, row := range spacedRows.Data {
						got = append(got, row.ResourceID)
					}
					t.Errorf("the server stored %v, none of them %q: the id was escaped the wrong number of times",
						got, spacedResourceID)
				}
			},
		},
		{
			// The append-only history, and the one payload in this SDK whose most
			// important field has no type in the contract at all.
			//
			// Three things here need a real server. The audit rows are written by
			// the SERVER as a side effect of the mutations every entry above
			// made, so unlike every other fixture in the unit suite they are not
			// marshalled from the struct they are decoded into -- a misspelled
			// json tag shows up here and nowhere else. The `changes` object's
			// real shape is server-authored too. And the namespace filtering is a
			// property of the query the server runs, not of anything the client
			// sends.
			//
			// It is LAST for that reason: it reads what the walk wrote.
			name: "AuditLog",
			assert: func(ctx context.Context, t *testing.T, c *octonomy.Client, s *smokeState) {
				tagAudit, err := c.Tags.ListAuditLogs(ctx, s.tag.ID, &octonomy.TagListAuditLogsParams{
					ListOptions: octonomy.ListOptions{Limit: 100},
				})
				if err != nil {
					// A 403 here means the harness token lacks audit:read, which
					// is a harness fault rather than an SDK one --
					// scripts/octonomy-harness.sh grants it.
					if octonomy.IsForbidden(err) {
						t.Fatalf("audit reads are forbidden for this token: the harness must grant --scope audit:read: %v", err)
					}
					t.Fatalf("Tags.ListAuditLogs: %v", err)
				}
				// Rows arrive NEWEST FIRST, so the first match of each action is
				// the most recent one -- for tag.updated that is the rename in
				// the Tag entry, which is the call that carried the
				// caller-supplied request id. Taking the LAST match instead would
				// read that entry's metadata writes, which happen to the same tag
				// and would be indistinguishable here; it worked only while
				// exactly one update existed, which is not a property this walk
				// can promise.
				var created, updated *octonomy.AuditLog
				createdIdx, updatedIdx := -1, -1
				for i, row := range tagAudit.Data {
					switch {
					case row.Action == "tag.created" && created == nil:
						created, createdIdx = &tagAudit.Data[i], i
					case row.Action == "tag.updated" && updated == nil:
						updated, updatedIdx = &tagAudit.Data[i], i
					}
				}
				if created == nil || updated == nil {
					t.Fatalf("Tags.ListAuditLogs returned %d rows, missing tag.created or tag.updated", len(tagAudit.Data))
				}
				if created.EntityType != "tag" || created.EntityID != s.tag.ID {
					t.Errorf("tag.created row names %s/%s, want tag/%s", created.EntityType, created.EntityID, s.tag.ID)
				}
				if created.TenantID == "" || created.OperationID == "" || created.CreatedAt.IsZero() {
					t.Errorf("identity fields did not decode: %+v", created)
				}
				if created.TagID == nil || *created.TagID != s.tag.ID {
					t.Errorf("TagID = %v, want %s", created.TagID, s.tag.ID)
				}
				if created.ActorID == nil || *created.ActorID != "v2-smoke" {
					t.Errorf("ActorID = %v, want v2-smoke (Config.ActorID)", created.ActorID)
				}
				// Request correlation, both halves, on rows the server wrote
				// itself (#5). The create sent no X-Request-ID, so the server
				// minted one; the update sent updateRequestID with WithRequestID,
				// so the row must carry that exact string. A unit test can assert
				// the header leaves the client and nothing more -- that it
				// survives the middleware, reaches audit.request_id, and is stored
				// unmangled is a property of the server, and this is the only
				// place it is checked.
				switch {
				case created.RequestID == nil:
					t.Errorf("tag.created RequestID is nil, want the server's generated value")
				case *created.RequestID == "":
					t.Errorf("tag.created RequestID is empty, want the server's generated value")
				case *created.RequestID == s.updateRequestID:
					t.Errorf("tag.created RequestID = %q, the id sent on the UPDATE: the SDK must send no header at all when the option is absent",
						*created.RequestID)
				}
				switch {
				case updated.RequestID == nil:
					t.Errorf("tag.updated RequestID is nil, want the caller-supplied %q", s.updateRequestID)
				case *updated.RequestID != s.updateRequestID:
					t.Errorf("tag.updated RequestID = %q, want the caller-supplied %q", *updated.RequestID, s.updateRequestID)
				}
				// Rows arrive NEWEST FIRST, which AuditLog documents and offset
				// paging depends on: the rename happened after the create, so it
				// comes back before it. Asserted on the page order rather than
				// only on the two timestamps, since the order is what a caller
				// actually reads.
				if updatedIdx > createdIdx {
					t.Errorf("tag.updated is at index %d and tag.created at %d: rows must arrive newest first",
						updatedIdx, createdIdx)
				}
				for i := 1; i < len(tagAudit.Data); i++ {
					if tagAudit.Data[i].CreatedAt.After(tagAudit.Data[i-1].CreatedAt) {
						t.Errorf("row %d (%v) is newer than row %d (%v): the page is not ordered newest first",
							i, tagAudit.Data[i].CreatedAt, i-1, tagAudit.Data[i-1].CreatedAt)
					}
				}

				// The untyped `changes` object, read the way a caller reads it.
				// The contract gives this field no type; the server writes
				// {"before": ..., "after": ...} with the fields the mutation
				// touched.
				after, ok := updated.Changes["after"].(map[string]any)
				if !ok {
					t.Fatalf("tag.updated changes[after] = %#v, want an object", updated.Changes["after"])
				}
				if after["name"] != "v2 smoke renamed" {
					t.Errorf("changes[after][name] = %v, want the renamed value", after["name"])
				}
				if before, ok := updated.Changes["before"].(map[string]any); !ok || before["name"] != "v2 smoke" {
					t.Errorf("changes[before] = %#v, want the pre-rename name", updated.Changes["before"])
				}

				// OperationID is what makes a multi-row mutation reconstructable:
				// the assign and the empty replace that cleared it, both made by
				// the ResourceReplaceResult entry on this one resource, are
				// separate operations, and filtering by one returns that
				// operation's rows alone.
				resourceAudit, err := c.Resources.ListAuditLogs(ctx, "cart", s.clearedResourceID,
					&octonomy.ResourceListAuditLogsParams{ListOptions: octonomy.ListOptions{Limit: 100}})
				if err != nil {
					t.Fatalf("Resources.ListAuditLogs: %v", err)
				}
				if len(resourceAudit.Data) < 2 {
					t.Fatalf("Resources.ListAuditLogs returned %d rows, want the assign and the clear", len(resourceAudit.Data))
				}
				operations := map[string]int{}
				for _, row := range resourceAudit.Data {
					if row.ResourceID == nil || *row.ResourceID != s.clearedResourceID {
						t.Errorf("row %s names resource %v, want %s", row.ID, row.ResourceID, s.clearedResourceID)
					}
					// The entity is "tag_assignment" while the action is
					// "assignment.*" -- an asymmetry AuditLog documents, and an
					// exact-match filter, so it is worth pinning against the
					// server rather than against a fixture.
					if row.EntityType != "tag_assignment" {
						t.Errorf("assignment row EntityType = %q, want tag_assignment", row.EntityType)
					}
					operations[row.OperationID]++
				}
				if len(operations) < 2 {
					t.Errorf("the two replaces share %d operation id(s), want one each: %v", len(operations), operations)
				}
				oneOperation := resourceAudit.Data[0].OperationID
				byOperation, err := c.AuditLogs.List(ctx, &octonomy.AuditLogListParams{
					OperationID: octonomy.String(oneOperation),
					ListOptions: octonomy.ListOptions{Limit: 100},
				})
				if err != nil {
					t.Fatalf("AuditLogs.List (by operation): %v", err)
				}
				if len(byOperation.Data) == 0 {
					t.Fatalf("filtering by operation_id %s returned nothing", oneOperation)
				}
				for _, row := range byOperation.Data {
					if row.OperationID != oneOperation {
						t.Errorf("operation_id filter returned %s, want only %s", row.OperationID, oneOperation)
					}
				}

				// The collection's filters, and the global rows' namespace pair:
				// a global mutation records no namespace.
				entityRows, err := c.AuditLogs.List(ctx, &octonomy.AuditLogListParams{
					EntityType:  octonomy.String("tag"),
					EntityID:    octonomy.String(s.tag.ID),
					Action:      octonomy.String("tag.created"),
					ListOptions: octonomy.ListOptions{Limit: 10},
				})
				if err != nil {
					t.Fatalf("AuditLogs.List (filtered): %v", err)
				}
				if len(entityRows.Data) != 1 {
					t.Fatalf("filtered list returned %d rows, want exactly the tag.created row", len(entityRows.Data))
				}
				if entityRows.Data[0].NamespaceType != nil || entityRows.Data[0].NamespaceID != nil {
					t.Errorf("a global audit row reported a namespace: %+v", entityRows.Data[0])
				}

				// The namespace pair the server populates, and the fail-closed
				// read: a namespaced audit read returns that namespace's rows and
				// excludes the global ones. Without the headers the server serves
				// the global namespace with a 200, so the exclusion is what
				// proves they arrived.
				nsAudit, err := c.AuditLogs.List(ctx, &octonomy.AuditLogListParams{
					ListOptions: octonomy.ListOptions{Limit: 100},
				}, octonomy.WithNamespace(s.nsType, s.nsID), octonomy.WithApplication(s.appID))
				if err != nil {
					t.Fatalf("AuditLogs.List (namespaced): %v", err)
				}
				sawNamespacedRow, sawGlobalRow := false, false
				for _, row := range nsAudit.Data {
					if row.EntityID == s.nsTag.ID {
						sawNamespacedRow = true
						if row.NamespaceType == nil || *row.NamespaceType != s.nsType ||
							row.NamespaceID == nil || *row.NamespaceID != s.nsID {
							t.Errorf("namespaced audit row: namespace = %v/%v, want %q/%q",
								row.NamespaceType, row.NamespaceID, s.nsType, s.nsID)
						}
					}
					if row.EntityID == s.tag.ID {
						sawGlobalRow = true
					}
				}
				if !sawNamespacedRow {
					t.Errorf("namespaced audit list returned no row for the namespaced tag %s", s.nsTag.ID)
				}
				if sawGlobalRow {
					t.Errorf("namespaced audit list returned a GLOBAL row (%s): namespaced reads exclude global rows unless include_global is set", s.tag.ID)
				}

				// An unknown tag is an EMPTY PAGE here, not the 404 every other
				// /tags/{id} route answers: the view filters the audit table by
				// tag_id and never loads the tag. Documented on
				// TagService.ListAuditLogs, and only a real server can confirm it.
				unknownTagAudit, err := c.Tags.ListAuditLogs(ctx, "00000000-0000-0000-0000-000000000000", nil)
				if err != nil {
					t.Fatalf("Tags.ListAuditLogs on an unknown tag: want an empty page, got %v", err)
				}
				if len(unknownTagAudit.Data) != 0 {
					t.Errorf("Tags.ListAuditLogs on an unknown tag returned %d rows", len(unknownTagAudit.Data))
				}
			},
		},
	}
}
