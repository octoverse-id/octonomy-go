//go:build integration
// +build integration

// Shared machinery for the isolation suite (#97), ported from main's
// integration_harness_test.go at 5e40964 and rewritten for this line. Every
// mention of main in this file means main at that commit.
//
// Both build-constraint forms are present on purpose: Go 1.17+ reads
// //go:build, Go 1.13 reads only // +build, and a file missing the second
// compiles into an ordinary `go test` run on this line's own toolchain.
//
// What lives here: loading the credentials scripts/octonomy-harness.sh exports,
// building clients against them, and seeding one scope's worth of rows. The
// assertions live in integration_suite_test.go. uniqueSlug comes from
// integration_test.go, which carries the same build tag.
//
// What main has here and this line does not: rawPost, the namespace-injecting
// transport and the error-details readers. Each serves a test in main's suite
// this line does not port (docs/compat-test-disposition.md), and a helper with
// no caller is a helper nothing keeps honest.
//
// THE HARNESS MINTS THREE TOKENS, AND WHICH ONE A TEST USES IS THE TEST.
//
//	wildcard  -- a --namespace-wildcard grant. Matches every partition including
//	             global, so it can seed rows in both merchants and can prove what
//	             the server's namespace FILTER does. It can never prove anything
//	             about authorization, because for this token authorization never
//	             says no.
//	merchantA -- an EXACT grant for one merchant namespace, and nothing else.
//	merchantB -- the same, for a second merchant.
//
// The exact grants are what make the isolation and fail-closed assertions mean
// something. A suite driven only by the wildcard token would pass against a
// server that had lost every authorization check, which is the failure mode
// worth the most here. TestTheIsolationSuiteRunsItsProbes (readprobes_test.go)
// refuses an isolation test whose negative runs are all on the wildcard.
package octonomy_test

import (
	"context"
	"os"
	"testing"
	"time"

	octonomy "github.com/octoverse-id/octonomy-go"
)

// suiteTimeout bounds each phase of a top-level test that talks to the server:
// every seed's writes get a context of this length, and so do all of the
// matrix's reads together, from a context the test creates once its rows
// exist. Generous: one matrix is close to a hundred reads, and a seed is four
// write requests, each of which also writes an audit row and an outbox event on
// the server. Teardown steps get cleanupTimeout each.
const suiteTimeout = 120 * time.Second

// cleanupTimeout bounds one seeded row's teardown. It is a context of its own
// rather than the test's, which may already have expired by the time the
// teardown runs -- the case in which a leaked row is likeliest.
const cleanupTimeout = 30 * time.Second

// merchant is one namespace and the token holding an exact grant for it.
type merchant struct {
	id    string
	token string
}

// harness holds everything scripts/octonomy-harness.sh exports.
type harness struct {
	baseURL       string
	tenantID      string
	applicationID string
	namespaceType string

	// wildcardToken matches every namespace partition, global included. It is
	// the seeding identity: one client can write into both merchants.
	wildcardToken string

	merchantA merchant
	merchantB merchant
}

// loadHarness reads the harness credentials, or skips.
//
// The gate is OCTONOMY_TEST_BASE_URL, exactly as in newSmokeClient: with no
// harness up, `go test -tags=integration` skips rather than fails, because an
// absent container is a developer's normal case.
//
// OCTONOMY_SMOKE_REQUIRED=1 removes the skip, and the pinned CI step sets it.
// The name predates this suite -- it was introduced for the smoke test -- and is
// reused rather than duplicated, as on main, so that CI and a laptop have
// exactly one knob for "a harness must be up". Two spellings would mean two ways
// to be half-configured. The skip is held to newSmokeClient's shape -- one Skip,
// straight after `if required { t.Fatal(…) }` -- by
// TestTheIsolationSuiteRunsItsProbes.
//
// Every other variable is a HARD failure when the base URL is present. A partial
// environment is a broken harness, not an absent one, and the difference matters:
// silently skipping the namespace assertions because one variable did not survive
// $GITHUB_ENV is precisely the vacuous green this suite exists to close.
func loadHarness(t *testing.T) harness {
	t.Helper()

	required := os.Getenv("OCTONOMY_SMOKE_REQUIRED") == "1"
	baseURL := os.Getenv("OCTONOMY_TEST_BASE_URL")
	if baseURL == "" {
		if required {
			t.Fatal("OCTONOMY_SMOKE_REQUIRED=1 but OCTONOMY_TEST_BASE_URL is empty: the harness did not export its credentials, so this suite would have skipped and reported a vacuous pass")
		}
		t.Skip("OCTONOMY_TEST_BASE_URL is empty; run `make dev-server` and then `make test-integration`")
	}

	h := harness{
		baseURL:       baseURL,
		tenantID:      os.Getenv("OCTONOMY_TEST_TENANT_ID"),
		applicationID: os.Getenv("OCTONOMY_TEST_APPLICATION_ID"),
		namespaceType: os.Getenv("OCTONOMY_TEST_NAMESPACE_TYPE"),
		wildcardToken: os.Getenv("OCTONOMY_TEST_TOKEN"),
		merchantA: merchant{
			id:    os.Getenv("OCTONOMY_TEST_NAMESPACE_A_ID"),
			token: os.Getenv("OCTONOMY_TEST_NAMESPACE_A_TOKEN"),
		},
		merchantB: merchant{
			id:    os.Getenv("OCTONOMY_TEST_NAMESPACE_B_ID"),
			token: os.Getenv("OCTONOMY_TEST_NAMESPACE_B_TOKEN"),
		},
	}

	// Named individually rather than as one "the environment is incomplete":
	// the failure a developer actually hits here is an OLD .octonomy-harness.env
	// from before the per-merchant tokens existed, and "which variable" is the
	// whole remediation. A slice, not a map, so the message names them in a
	// stable order.
	var missing []string
	for _, v := range []struct{ name, value string }{
		{"OCTONOMY_TEST_TOKEN", h.wildcardToken},
		{"OCTONOMY_TEST_TENANT_ID", h.tenantID},
		{"OCTONOMY_TEST_APPLICATION_ID", h.applicationID},
		{"OCTONOMY_TEST_NAMESPACE_TYPE", h.namespaceType},
		{"OCTONOMY_TEST_NAMESPACE_A_ID", h.merchantA.id},
		{"OCTONOMY_TEST_NAMESPACE_A_TOKEN", h.merchantA.token},
		{"OCTONOMY_TEST_NAMESPACE_B_ID", h.merchantB.id},
		{"OCTONOMY_TEST_NAMESPACE_B_TOKEN", h.merchantB.token},
	} {
		if v.value == "" {
			missing = append(missing, v.name)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("OCTONOMY_TEST_BASE_URL is set but %v are not: re-run `make dev-server` to refresh .octonomy-harness.env", missing)
	}

	// The two merchants must be genuinely different namespaces. Equal ids would
	// leave every isolation assertion comparing a namespace with itself and
	// passing for the wrong reason.
	if h.merchantA.id == h.merchantB.id {
		t.Fatalf("the two merchant namespaces are both %q: the isolation assertions would compare a namespace with itself", h.merchantA.id)
	}
	// And three different credentials. An exact grant equal to the wildcard
	// token is the wildcard, under which authorization never refuses anything.
	if h.merchantA.token == h.merchantB.token || h.merchantA.token == h.wildcardToken || h.merchantB.token == h.wildcardToken {
		t.Fatal("the harness tokens are not three distinct grants: the harness did not mint separate exact grants, so no cross-merchant assertion here means anything")
	}
	return h
}

// client builds an /api/v2 client for one token. actorID lands in the audit
// rows the server writes, which is how a failed run is traced back to this
// suite.
//
// APIV2 is explicit, and must be. This line's default surface is /api/v1, which
// has no namespace axis, and checkScopeCoherence refuses a namespace option on a
// v1 client before the request is sent -- so a client built without it would
// turn every probe into "the SDK refused my call", which requireAPIError rejects
// as not a server answer.
func (h harness) client(t *testing.T, token, actorID string) *octonomy.Client {
	t.Helper()
	client, err := octonomy.New(octonomy.Config{
		BaseURL:    h.baseURL,
		Token:      token,
		TenantID:   h.tenantID,
		ActorID:    actorID,
		APIVersion: octonomy.APIV2,
	})
	if err != nil {
		t.Fatalf("New(%s): %v", actorID, err)
	}
	return client
}

// wildcard returns the seeding client: the one identity that can write into both
// merchants and into the global namespace.
func (h harness) wildcard(t *testing.T) *octonomy.Client {
	t.Helper()
	return h.client(t, h.wildcardToken, "go113-isolation")
}

// merchantClient returns a client holding an exact grant for m and nothing else.
func (h harness) merchantClient(t *testing.T, m merchant) *octonomy.Client {
	t.Helper()
	return h.client(t, m.token, "go113-isolation-"+m.id)
}

// scoped is the option set every read in this suite carries.
//
// Namespace AND application, always, on a namespaced read: namespace isolation
// sits below application on the server, so a namespaced bodyless request that
// names no application is refused -- by the SDK's own checkScopeCoherence before
// it is even sent. Spelling the pair once keeps a probe from accidentally
// asserting "the SDK refused my call" instead of "the server refused my read".
//
// An EMPTY namespaceID means the global namespace, which the server selects by
// the absence of the headers -- so the option is omitted rather than sent blank
// (WithNamespace rejects a blank id outright, and rightly). The application is
// still named, because the global fixture's rows are application-scoped.
//
// It returns a fresh slice every call, so a caller may append to the result
// without reaching into anything shared.
func (h harness) scoped(namespaceID string, extra ...octonomy.RequestOption) []octonomy.RequestOption {
	opts := make([]octonomy.RequestOption, 0, 2+len(extra))
	if namespaceID != "" {
		opts = append(opts, octonomy.WithNamespace(h.namespaceType, namespaceID))
	}
	opts = append(opts, octonomy.WithApplication(h.applicationID))
	return append(opts, extra...)
}

// teardown is rule 3 of the t.Cleanup replacement model
// (docs/compat-test-disposition.md): a teardown that must outlive the helper
// registering it belongs to the ROOT test. main's seed registers each row's
// delete with t.Cleanup, which runs when the test finishes; t.Cleanup needs Go
// 1.14, and a defer inside seed would delete the rows before the probes read
// them. Deletion is deactivation here, and a deactivated row is still served by
// id -- 200, is_active false -- so most of the matrix would go on passing
// against rows no list shows: only the list and resolution probes, which leave
// inactive rows out, would fail. That is quiet in the wrong direction, which is
// why the teardown waits for the root test.
//
// So the root test owns the stack and drains it with one defer at its top:
//
//	var rows teardown
//	defer rows.run()
//
// and seed pushes onto it. run drains last-in-first-out, as t.Cleanup does, and
// it runs on a t.Fatalf inside seed too: Fatalf ends the test goroutine with
// runtime.Goexit, which runs the root test's deferred calls.
type teardown struct {
	steps []func()
}

// add registers one teardown step.
func (td *teardown) add(step func()) { td.steps = append(td.steps, step) }

// run drains the stack, newest first.
func (td *teardown) run() {
	for i := len(td.steps) - 1; i >= 0; i-- {
		td.steps[i]()
	}
	td.steps = nil
}

// namespaceFixture is one SCOPE's worth of rows -- one merchant's, or the
// global namespace's -- enough that every authenticated read endpoint the SDK
// exposes has something of that scope's to find.
//
// The resource carries the tag, so the assignment, resource-tag, and audit
// routes all resolve to the same underlying write, which is what makes a single
// fixture sufficient for the whole authenticated read surface.
//
// A blank namespaceID means the global namespace. describeScope renders it for
// failure messages, where "merchant " would be wrong.
type namespaceFixture struct {
	namespaceID string

	vocabulary *octonomy.Vocabulary
	tag        *octonomy.Tag
	alias      *octonomy.TagAlias

	resourceType string
	resourceID   string
}

// seed creates one fixture inside namespaceID using c, which must be a client
// whose grant reaches that scope -- in practice the wildcard one, since a single
// caller has to populate both merchants and the global namespace. Each row's
// teardown goes onto td, which the calling root test drains.
//
// An EMPTY namespaceID seeds the global namespace; see the comment on the option
// list below for why that mode exists.
//
// For a NAMESPACED seed every write goes through the namespace axis, so a run
// against a deployment with OCTONOMY_NAMESPACE_WRITE_ENABLED unset fails on the
// first create with a named message rather than seeding nothing and leaving the
// assertions to report "isolation holds" about rows that were never written. A
// global seed does not touch that kill-switch, which is correct: global writes
// are not what it gates.
func (h harness) seed(t *testing.T, td *teardown, c *octonomy.Client, namespaceID string) namespaceFixture {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), suiteTimeout)
	defer cancel()

	// An empty namespaceID seeds the GLOBAL namespace, which the server selects
	// by the absence of the headers. The include_global runs in the isolation
	// matrix need a global counterpart of every fixture row, and duplicating this
	// function to make three global rows would be three more places to forget a
	// cleanup.
	var ns []octonomy.RequestOption
	if namespaceID != "" {
		ns = append(ns, octonomy.WithNamespace(h.namespaceType, namespaceID))
	}

	// EACH TEARDOWN IS REGISTERED THE MOMENT ITS ROW EXISTS, never in one block
	// at the end. Every create below is followed by t.Fatalf on failure, and a
	// Fatalf skips whatever has not been registered yet -- so a single trailing
	// registration leaks the vocabulary when the tag create fails, and the
	// vocabulary and the tag when the alias create fails. Those leaks land in the
	// long-lived harness a developer is explicitly invited to reuse, where they
	// accumulate as active rows in the very namespaces this suite reads.
	//
	// The stack drains newest-first, which gives the order this needs for free:
	// alias, then tag, then vocabulary. That matters because deleting a tag
	// cascades deactivation to its aliases, and the reverse order would leave the
	// alias teardown deleting a row the tag teardown had already deactivated --
	// not an error on this server, but it would make a real teardown failure
	// unreadable.
	register := func(label string, del func(context.Context) error) {
		td.add(func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
			defer cancel()
			if err := del(cleanupCtx); err != nil {
				t.Errorf("cleanup %s: %s: %v", describeScope(namespaceID), label, err)
			}
		})
	}

	vocab, err := c.Vocabularies.Create(ctx, octonomy.VocabularyCreate{
		ApplicationID: octonomy.String(h.applicationID),
		Name:          "go113 isolation " + describeScope(namespaceID),
		Slug:          uniqueSlug("iso-vocab"),
	}, ns...)
	if err != nil {
		if octonomy.IsNamespacedWritesDisabled(err) {
			t.Fatalf("namespaced writes are disabled on this deployment: the harness must set OCTONOMY_NAMESPACE_WRITE_ENABLED=true (the server default is false): %v", err)
		}
		t.Fatalf("seed %s: Vocabularies.Create: %v", describeScope(namespaceID), err)
	}
	register("Vocabularies.Delete", func(ctx context.Context) error {
		return c.Vocabularies.Delete(ctx, vocab.ID, h.scoped(namespaceID)...)
	})

	tag, err := c.Tags.Create(ctx, octonomy.TagCreate{
		ApplicationID: octonomy.String(h.applicationID),
		Name:          "go113 isolation " + describeScope(namespaceID),
		Slug:          uniqueSlug("iso-tag"),
		Type:          "label",
	}, ns...)
	if err != nil {
		t.Fatalf("seed %s: Tags.Create: %v", describeScope(namespaceID), err)
	}
	register("Tags.Delete", func(ctx context.Context) error {
		return c.Tags.Delete(ctx, tag.ID, h.scoped(namespaceID)...)
	})

	alias, err := c.Aliases.Create(ctx, octonomy.TagAliasCreate{
		ApplicationID: octonomy.String(h.applicationID),
		TagID:         tag.ID,
		Name:          "go113 isolation alias " + describeScope(namespaceID),
		Slug:          uniqueSlug("iso-alias"),
	}, ns...)
	if err != nil {
		t.Fatalf("seed %s: Aliases.Create: %v", describeScope(namespaceID), err)
	}
	register("Aliases.Delete", func(ctx context.Context) error {
		return c.Aliases.Delete(ctx, alias.ID, h.scoped(namespaceID)...)
	})

	fixture := namespaceFixture{
		namespaceID:  namespaceID,
		vocabulary:   vocab,
		tag:          tag,
		alias:        alias,
		resourceType: "cart",
		resourceID:   uniqueSlug("iso-cart"),
	}

	// Assignments are deliberately left behind rather than cleaned up, as on
	// main. Deleting the tag deactivates it, which is what the server means by
	// delete, and every assertion in this suite is keyed on ids minted by this
	// run -- so a leftover row cannot reach the next one.
	if _, err := c.Resources.ReplaceTags(ctx, fixture.resourceType, fixture.resourceID, octonomy.ResourceReplace{
		ApplicationID: h.applicationID,
		TagIDs:        []string{tag.ID},
		AssignedBy:    octonomy.String("go113-isolation"),
	}, ns...); err != nil {
		t.Fatalf("seed %s: Resources.ReplaceTags: %v", describeScope(namespaceID), err)
	}

	return fixture
}

// requireFilteredOutcome asserts that a read which should not have produced the
// row declined in exactly the way THIS endpoint declines -- not merely that
// something went wrong.
//
// "Any error counts as isolation" is the trap this closes, and it is a wide one,
// because the SDK turns EVERY non-2xx into an *APIError by design. Under a bare
// AsAPIError check a crashed container's 500, a proxy's 502, an unrouted HTML
// 404 (CodeUnexpectedStatus), an expired token's 401 and an unrelated validation
// failure all read as "merchant A could not see merchant B" -- so the isolation
// assertions would go green against a server that had stopped serving
// altogether.
//
// A shared two-code allowlist is not enough either, which is the second half of
// the same lesson: it would accept a list route that began answering 400, or an
// object lookup that began answering 409 not_found, both of which are the server
// changing behaviour rather than filtering correctly. The expected shape is
// therefore a property of the probe, and both halves of it -- status AND code --
// are asserted.
func requireFilteredOutcome(t *testing.T, err error, what string, want filteredOutcome) {
	t.Helper()

	if want == filteredEmpty {
		// No error at all: the route answers 200 with the row simply absent, and
		// the caller has already checked that it was absent.
		if err != nil {
			t.Errorf("%s: a filtered read failed with %v, want a 200 whose page does not contain the row", what, err)
		}
		return
	}

	wantCode, wantStatus := octonomy.CodeNotFound, 404
	if want == filteredValidation {
		wantCode, wantStatus = octonomy.CodeValidation, 400
	}

	apiErr := requireAPIError(t, err, what)
	if apiErr.Code != wantCode || apiErr.StatusCode != wantStatus {
		t.Errorf("%s: a filtered read failed with {status:%d code:%q}, want {%d %q} -- this endpoint declines an out-of-namespace row one specific way, and anything else is the server doing something other than filtering",
			what, apiErr.StatusCode, apiErr.Code, wantStatus, wantCode)
	}
}

// requireAPIError asserts that err is a refusal the SERVER made, and returns the
// decoded envelope.
//
// The distinction from a transport-level failure is load-bearing wherever a test
// accepts an error as evidence of anything. A wrong port, a torn-down container
// or a client-side option guard is also an error, and treating one as a server
// response would make these assertions pass against a harness that is not even
// running. An *APIError means a real Octonomy response came back carrying a real
// error envelope.
func requireAPIError(t *testing.T, err error, what string) *octonomy.APIError {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected an error, got none", what)
	}
	apiErr, ok := octonomy.AsAPIError(err)
	if !ok {
		t.Fatalf("%s: expected a server error envelope, got a transport-level failure: %v", what, err)
	}
	return apiErr
}

// describeScope names a fixture's partition for a failure message. seed("")
// creates rows in the global namespace, where "merchant " would be wrong.
func describeScope(namespaceID string) string {
	if namespaceID == "" {
		return "global"
	}
	return "merchant-" + namespaceID
}
