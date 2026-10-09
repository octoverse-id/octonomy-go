// Command tags demonstrates the tag endpoints: a two-level hierarchy, the
// pointer fields that leave a link alone when nil, the (type, slug) uniqueness
// rule that decides when a create is a conflict, the free-text search that finds
// a tag without knowing its id, and assembling the hierarchy client-side with
// BuildTagTree -- including the parent cycle the server lets through and the
// orphan a deactivated parent leaves behind.
//
//	make dev-server       # boots a real Octonomy and prints these exports
//	go run ./examples/tags
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	octonomy "github.com/octoverse-id/octonomy-go"
)

func main() {
	client := mustClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	vocab, err := client.Vocabularies.Create(ctx, octonomy.VocabularyCreate{
		Name: "Apparel",
		Slug: unique("apparel"),
	})
	if err != nil {
		log.Fatalf("create vocabulary: %v", err)
	}

	// A tag names its vocabulary by id. VocabularyID is optional -- a tag
	// without one is loose in the tenant rather than invalid.
	root, err := client.Tags.Create(ctx, octonomy.TagCreate{
		Name:         "Clothing",
		Slug:         unique("clothing"),
		Type:         "category",
		VocabularyID: octonomy.String(vocab.ID),
	})
	if err != nil {
		log.Fatalf("create root tag: %v", err)
	}
	parentSlug := unique("outerwear")
	parent, err := client.Tags.Create(ctx, octonomy.TagCreate{
		Name:         "Outerwear",
		Slug:         parentSlug,
		Type:         "category",
		VocabularyID: octonomy.String(vocab.ID),
	})
	if err != nil {
		log.Fatalf("create parent tag: %v", err)
	}
	fmt.Printf("created parent %s (%s)\n", parent.Slug, parent.ID)

	// The hierarchy is one field: ParentID. There is no /tags/{id}/children
	// route and no tree endpoint, so a browser either filters the list once per
	// parent -- one request per node -- or fetches the set and assembles it
	// locally, which is what BuildTagTree does further down.
	child, err := client.Tags.Create(ctx, octonomy.TagCreate{
		Name:         "Rain jackets",
		Slug:         unique("rain-jackets"),
		Type:         "category",
		VocabularyID: octonomy.String(vocab.ID),
		ParentID:     octonomy.String(parent.ID),
		Metadata:     octonomy.Metadata{"season": "aw"},
	})
	if err != nil {
		log.Fatalf("create child tag: %v", err)
	}
	fmt.Printf("created child %s under %s\n", child.Slug, parent.Slug)

	children, err := client.Tags.List(ctx, &octonomy.TagListParams{
		ParentID: octonomy.String(parent.ID),
	})
	if err != nil {
		log.Fatalf("list children: %v", err)
	}
	fmt.Printf("%s has %d child tag(s)\n", parent.Slug, len(children.Data))

	// A nil field in an *Update is OMITTED, so it leaves the column alone: this
	// rename sends {"name": ...} and nothing else, and the child keeps its
	// parent. The converse is the trap. No TagUpdate value sends
	// "parent_id": null -- the fields are pointers, where nil already means
	// "leave it alone" -- so this SDK cannot un-nest a tag. Re-point it instead,
	// which is also how the cycle below is repaired.
	renamed, err := client.Tags.Update(ctx, child.ID, octonomy.TagUpdate{
		Name: octonomy.String("Rain jackets (renamed)"),
	})
	if err != nil {
		log.Fatalf("rename child tag: %v", err)
	}
	fmt.Printf("after a rename with ParentID nil: %s still has a parent = %v\n",
		renamed.Slug, renamed.ParentID != nil)

	// Uniqueness is on the PAIR (type, slug), not on the slug alone. The same
	// slug under the same type is a typed conflict...
	_, err = client.Tags.Create(ctx, octonomy.TagCreate{
		Name: "Outerwear again",
		Slug: parentSlug,
		Type: "category",
	})
	switch {
	case err == nil:
		log.Fatal("re-creating the same (type, slug) should have conflicted")
	case octonomy.IsConflict(err):
		fmt.Printf("same (type, slug) again: conflict, as expected\n")
	default:
		log.Fatalf("re-create: %v", err)
	}

	// ...while the same slug under a DIFFERENT type is an ordinary create. A
	// caller treating slugs as globally unique is reading half the key.
	otherType, err := client.Tags.Create(ctx, octonomy.TagCreate{
		Name: "Outerwear label",
		Slug: parentSlug,
		Type: "label",
	})
	if err != nil {
		log.Fatalf("create same slug under another type: %v", err)
	}
	fmt.Printf("same slug, type %q: created %s\n", otherType.Type, otherType.ID)

	// Query is the server's free-text `q`, matching name OR slug. Type narrows
	// it to one of the two rows the slug now names.
	hits, err := client.Tags.List(ctx, &octonomy.TagListParams{
		Query: octonomy.String(parentSlug),
		Type:  octonomy.String("category"),
	})
	if err != nil {
		log.Fatalf("search: %v", err)
	}
	for _, tag := range hits.Data {
		fmt.Printf("search hit: %s (%s) type=%s usage=%d\n", tag.Name, tag.Slug, tag.Type, tag.UsageCount)
	}

	// The tags this run created, in ONE request. The vocabulary filter is what
	// makes that possible: it narrows the list to a set small enough for a
	// single page, which is the only size that cannot drift at all, since one
	// request cannot shift under itself. Paging it is fine against a 3.2.1+
	// server, where GET /tags orders by (name, slug, id); older servers left
	// that list unordered, so a walk there could repeat or miss rows with no
	// concurrent writes. See the Each doc comment.
	fetch := func(what string) []octonomy.Tag {
		page, err := client.Tags.List(ctx, &octonomy.TagListParams{
			VocabularyID: octonomy.String(vocab.ID),
			ListOptions:  octonomy.ListOptions{Limit: 200},
		})
		if err != nil {
			log.Fatalf("list %s: %v", what, err)
		}
		return page.Data
	}

	tree, err := octonomy.BuildTagTree(fetch("the vocabulary's tags"))
	if err != nil {
		log.Fatalf("build tree: %v", err)
	}
	fmt.Printf("assembled %d tag(s) from one list call:\n", tree.Len())
	printTree(tree)

	// A parent CYCLE is reachable on the server: the database forbids only
	// parent_id = id, and nothing walks the ancestry, so pointing the parent at
	// its own child is an ordinary PATCH. A naive assembler would silently
	// return fewer tags than it was given -- every tag in the cycle has a parent
	// in the set, so none of them is a root -- and BuildTagTree refuses instead.
	if _, err := client.Tags.Update(ctx, parent.ID, octonomy.TagUpdate{
		ParentID: octonomy.String(child.ID),
	}); err != nil {
		log.Fatalf("point the parent at its own child: %v", err)
	}
	if _, err := octonomy.BuildTagTree(fetch("the tags, now in a cycle")); errors.Is(err, octonomy.ErrTagCycle) {
		fmt.Printf("after pointing %s at its child: %v\n", parent.Slug, err)
	} else {
		log.Fatalf("build tree over a cycle: err = %v, want ErrTagCycle", err)
	}

	// The repair is a re-point, since no TagUpdate sends null: hang the parent
	// under a tag outside the cycle.
	if _, err := client.Tags.Update(ctx, parent.ID, octonomy.TagUpdate{
		ParentID: octonomy.String(root.ID),
	}); err != nil {
		log.Fatalf("re-point the parent: %v", err)
	}
	repointed, err := octonomy.BuildTagTree(fetch("the repaired tags"))
	if err != nil {
		log.Fatalf("build tree after the repair: %v", err)
	}
	fmt.Printf("after re-pointing %s under %s:\n", parent.Slug, root.Slug)
	printTree(repointed)

	// Now the case that sends a hand-written assembler wrong. Deleting the
	// parent DEACTIVATES it; the cascade reaches the tag's aliases and never
	// its children, and an unfiltered list returns active rows only. So the
	// child comes back without it, still naming a parent this page does not
	// hold.
	if err := client.Tags.Delete(ctx, parent.ID); err != nil {
		log.Fatalf("delete parent: %v", err)
	}
	orphaned, err := octonomy.BuildTagTree(fetch("the tags that are still active"))
	if err != nil {
		log.Fatalf("build tree after the delete: %v", err)
	}
	// Kept and reported, never dropped: an orphan is promoted to a root, so
	// walking Roots still reaches every tag you fetched.
	fmt.Printf("after deactivating %s: %d tag(s), %d orphan(s)\n",
		parent.Slug, orphaned.Len(), len(orphaned.Orphans))
	for _, node := range orphaned.Orphans {
		fmt.Printf("  orphan %s names parent %s, which is not in this page\n", node.Tag.Slug, *node.Tag.ParentID)
	}

	// The fix is a fetch, not a guess: Get reads deactivated rows.
	deactivated, err := client.Tags.Get(ctx, parent.ID)
	if err != nil {
		log.Fatalf("get the deactivated parent: %v", err)
	}
	repaired, err := octonomy.BuildTagTree(append(fetch("the active tags again"), *deactivated))
	if err != nil {
		log.Fatalf("rebuild: %v", err)
	}
	fmt.Printf("refetched the parent (is_active=%t): %d orphan(s)\n",
		deactivated.IsActive, len(repaired.Orphans))
	if node := repaired.Node(child.ID); node != nil {
		for _, step := range node.Path() {
			fmt.Printf("  breadcrumb: %s\n", step.Name)
		}
	}
}

// printTree walks the tree rather than looping over Roots: a root's Depth is
// always 0, and its children are not in that slice.
func printTree(tree *octonomy.TagTree) {
	if err := tree.Walk(func(n *octonomy.TagNode) error {
		fmt.Printf("  %s%s\n", strings.Repeat("  ", n.Depth), n.Tag.Name)
		return nil
	}); err != nil {
		log.Fatalf("walk: %v", err)
	}
}

// --- configuration ------------------------------------------------------------
//
// Every example that calls the API reads the same three variables, so one export
// block drives all of them. `make dev-server` prints one export block carrying
// every variable the examples read.
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
		ActorID:  "example-tags",
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
