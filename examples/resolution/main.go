// Command resolution demonstrates turning a slug into a tag: the alias branch,
// the error that means "nothing is called that" (it is not a 404), and the one
// tie this route can actually report -- two canonical tags under different
// types.
//
//	make dev-server             # boots a real Octonomy and prints these exports
//	go run ./examples/resolution
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tagSlug := unique("summer-sale")
	tag, err := client.Tags.Create(ctx, octonomy.TagCreate{
		Name: "Summer sale",
		Slug: tagSlug,
		Type: "label",
	})
	if err != nil {
		log.Fatalf("create tag: %v", err)
	}
	aliasSlug := unique("sale")
	if _, err := client.Aliases.Create(ctx, octonomy.TagAliasCreate{
		TagID: tag.ID,
		Name:  "Sale",
		Slug:  aliasSlug,
	}); err != nil {
		log.Fatalf("create alias: %v", err)
	}

	// A canonical slug matches the tag directly and carries no alias.
	direct, err := client.Tags.Resolve(ctx, tagSlug, nil)
	if err != nil {
		log.Fatalf("resolve the tag slug: %v", err)
	}
	fmt.Printf("%s -> matched_type=%s tag=%s alias=%v\n",
		tagSlug, direct.MatchedType, direct.Tag.Slug, direct.MatchedAlias)

	// An alias slug resolves THROUGH the alias to the canonical tag: Tag is the
	// tag, never the alias's own row. Branch on MatchedType rather than on
	// MatchedAlias being non-nil -- an unknown match kind the server adds later
	// is preserved rather than rejected, so the nil check is not the converse.
	viaAlias, err := client.Tags.Resolve(ctx, aliasSlug, nil)
	if err != nil {
		log.Fatalf("resolve the alias slug: %v", err)
	}
	if viaAlias.MatchedType == octonomy.MatchedTypeAlias {
		fmt.Printf("%s -> matched alias %s, canonical tag %s\n",
			aliasSlug, viaAlias.MatchedAlias.Slug, viaAlias.Tag.Slug)
	}

	// NO MATCH IS NOT A 404. The server answers an unmatched slug with a 400
	// validation_error, so the branch meaning "nothing is called that" is
	// IsValidation. A caller reaching for IsNotFound here never takes it.
	_, err = client.Tags.Resolve(ctx, unique("no-such"), nil)
	fmt.Printf("unmatched slug: IsValidation=%v IsNotFound=%v\n",
		octonomy.IsValidation(err), octonomy.IsNotFound(err))

	// Two canonical tags of equal specificity are refused rather than broken
	// arbitrarily, and Details names the axis to disambiguate on. Here the slug
	// names one tag per type, so the axis is "type".
	tiedSlug := unique("tied")
	for _, tagType := range []string{"label", "state"} {
		if _, err := client.Tags.Create(ctx, octonomy.TagCreate{
			Name: "Tied " + tagType,
			Slug: tiedSlug,
			Type: tagType,
		}); err != nil {
			log.Fatalf("create %s tag: %v", tagType, err)
		}
	}
	_, err = client.Tags.Resolve(ctx, tiedSlug, nil)
	if apiErr, ok := octonomy.AsAPIError(err); ok {
		fmt.Printf("tied slug: code=%s details=%v\n", apiErr.Code, apiErr.Details)
	}

	// Type breaks that tie. ApplicationID is the other narrowing knob, and it
	// both filters and ORDERS: resolution without one searches tenant-shared
	// rows alone, and with one an application-scoped row outranks the shared row
	// carrying the same slug -- which is how local vocabulary overrides a
	// tenant-wide default.
	picked, err := client.Tags.Resolve(ctx, tiedSlug, &octonomy.TagResolveParams{
		Type: octonomy.String("state"),
	})
	if err != nil {
		log.Fatalf("resolve with a type: %v", err)
	}
	fmt.Printf("tied slug, type=state: %s\n", picked.Tag.Name)
}

// --- configuration ------------------------------------------------------------
//
// Every example that calls the API reads the same three variables, so one export
// block drives all of them. `make dev-server` prints exactly this block.
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
		ActorID:  "example-resolution",
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
