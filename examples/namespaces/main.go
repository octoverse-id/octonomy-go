// Command namespaces demonstrates merchant scoping on the v2 surface: where a
// namespaced row lives, why a namespaced read does not see the tenant-shared
// ones, and what include_global does and does not widen.
//
//	make dev-server             # boots a real Octonomy and prints these exports
//	go run ./examples/namespaces
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	octonomy "github.com/octoverse-id/octonomy-go/v2"
)

func main() {
	client := mustClient()
	appID := mustEnv("OCTONOMY_APPLICATION_ID")
	nsType := mustEnv("OCTONOMY_NAMESPACE_TYPE")
	nsID := mustEnv("OCTONOMY_NAMESPACE_ID")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// There is no namespace field on Config, deliberately: a client-level
	// default would scope every call to whichever merchant was configured at
	// startup, and every call site would still look correct. Scope is
	// per-request, where the data is asked for.
	//
	// A tag created with no namespace option is GLOBAL: it lives in the
	// tenant-shared namespace rather than in any merchant's. That is not the
	// same as being visible to every merchant -- a namespaced read excludes it
	// by default, and reaches it only when the caller opts in AND is authorized
	// for global. Both halves are below.
	//
	// It names the SAME application as the merchant row that follows, and that
	// is load-bearing rather than tidy: the server splits slug uniqueness across
	// three constraints, and a pair differing on the application axis as well
	// would land in two different ones -- so it would say nothing about
	// namespaces. Held constant, the namespace is the only thing left that can
	// explain the second create succeeding.
	slug := unique("returns-policy")
	global, err := client.Tags.Create(ctx, octonomy.TagCreate{
		Name: "Returns policy", Slug: slug, Type: "label",
		ApplicationID: octonomy.String(appID),
	})
	if err != nil {
		log.Fatalf("create the global tag: %v", err)
	}
	fmt.Printf("global tag %s: namespace=%v\n", global.Slug, global.NamespaceType)

	// The same slug, the same type, the same application -- and a merchant
	// namespace. It is not a conflict: slug uniqueness is scoped PER NAMESPACE,
	// so a merchant may carry its own row under a name the tenant already uses.
	// Creating this one in the global namespace instead would be a 409.
	//
	// A namespaced write names its application in the BODY -- the query
	// parameter is not authoritative there.
	scoped, err := client.Tags.Create(ctx, octonomy.TagCreate{
		Name: "Returns policy (merchant)", Slug: slug, Type: "label",
		ApplicationID: octonomy.String(appID),
	}, octonomy.WithNamespace(nsType, nsID))
	if err != nil {
		if octonomy.IsNamespacedWritesDisabled(err) {
			log.Fatalf("this deployment has namespaced writes off (server default): %v", err)
		}
		log.Fatalf("create the namespaced tag: %v", err)
	}
	// The pair is DECODE-ONLY: the server sets it from the X-Namespace-* headers
	// the option sends, never from the body, and it is fixed for the row's life.
	fmt.Printf("namespaced tag: namespace=%s/%s\n", *scoped.NamespaceType, *scoped.NamespaceID)

	// A namespaced read sees that namespace's rows and, BY DEFAULT, no global
	// ones. A bodyless namespaced request must also name its application.
	scopedOnly, err := client.Tags.List(ctx, &octonomy.TagListParams{Slug: octonomy.String(slug)},
		octonomy.WithNamespace(nsType, nsID), octonomy.WithApplication(appID))
	if err != nil {
		log.Fatalf("namespaced list: %v", err)
	}
	fmt.Printf("namespaced read: %d row(s) -- the global one is filtered out\n", len(scopedOnly.Data))

	// WithIncludeGlobal asks for both. It is FAIL-CLOSED on the server: it
	// widens what the request asks for, and a token holding an exact merchant
	// grant with no global authority still sees no global rows -- silently
	// absent, not an error. This example's token is a wildcard, so it sees them.
	//
	// It widens the NAMESPACE axis only. The application axis is a separate
	// filter and is unaffected -- though note that one is not narrow either:
	// application_id matches that application OR the tenant-shared rows unless
	// TagListParams.IncludeShared says otherwise.
	both, err := client.Tags.List(ctx, &octonomy.TagListParams{Slug: octonomy.String(slug)},
		octonomy.WithNamespace(nsType, nsID), octonomy.WithApplication(appID), octonomy.WithIncludeGlobal())
	if err != nil {
		log.Fatalf("namespaced list with include_global: %v", err)
	}
	fmt.Printf("with WithIncludeGlobal: %d row(s)\n", len(both.Data))

	// A global read -- no namespace headers -- sees global rows only, never a
	// merchant's. There is no way to read across namespaces in one call.
	globalOnly, err := client.Tags.List(ctx, &octonomy.TagListParams{Slug: octonomy.String(slug)},
		octonomy.WithGlobalNamespace())
	if err != nil {
		log.Fatalf("global list: %v", err)
	}
	fmt.Printf("global read: %d row(s)\n", len(globalOnly.Data))

	// include_global is a QUERY parameter and is meaningless on a write, which
	// the server ignores in silence. The SDK refuses it instead, before the
	// request leaves: silence is the failure mode this client will not produce.
	_, err = client.Tags.Create(ctx, octonomy.TagCreate{
		Name: "Never sent", Slug: unique("never-sent"), Type: "label",
	}, octonomy.WithIncludeGlobal())
	fmt.Printf("WithIncludeGlobal on a write: %v\n", err)

	// Two different namespaces on one request is a contradiction, not a
	// precedence question -- last-wins on this axis is a cross-merchant read.
	// WithGlobalNamespace is the one explicit override.
	_, err = client.Tags.List(ctx, nil,
		octonomy.WithNamespace(nsType, nsID), octonomy.WithNamespace(nsType, "someone-else"))
	fmt.Printf("two namespaces on one request: %v\n", err)
}

// --- configuration ------------------------------------------------------------
//
// Every example that calls the API reads the same three variables, plus the
// application and namespace here. `make dev-server` prints exactly this block.
//
// The block is repeated in each of them rather than shared, deliberately: an
// example is copied whole, and a helper package would move the one part a reader
// has to adapt -- how the client gets its credentials -- out of the file they
// are reading.

func mustClient() *octonomy.Client {
	client, err := octonomy.New(octonomy.Config{
		BaseURL:  env("OCTONOMY_BASE_URL", "http://127.0.0.1:8000"),
		Token:    mustEnv("OCTONOMY_TOKEN"),
		TenantID: mustEnv("OCTONOMY_TENANT_ID"),
		ActorID:  "example-namespaces",
	})
	if err != nil {
		log.Fatalf("configure client: %v", err)
	}
	return client
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("%s is not set -- run `make dev-server` and export the block it prints", key)
	}
	return v
}

// unique keeps repeat runs against one long-lived dev server from colliding on
// the server's slug uniqueness constraint.
//
// The WHOLE nanosecond timestamp, not a remainder of it: taking it modulo a
// second reduces the namespace to "which nanosecond within this second", so two
// runs a second apart at the same offset produce the same slug -- and the
// collision surfaces as the conflict this helper exists to avoid.
func unique(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}
