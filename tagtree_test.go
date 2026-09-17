package octonomy

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// tag builds a Tag with just the fields the tree cares about. parent is the
// parent's id, or "" for none.
func tag(id, parent string) Tag {
	t := Tag{ID: id, Slug: id, Name: strings.ToUpper(id), Type: "category", IsActive: true}
	if parent != "" {
		t.ParentID = String(parent)
	}
	return t
}

// ids renders nodes as their ids, for comparing shapes without writing struct
// literals.
func ids(nodes []*TagNode) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.Tag.ID)
	}
	return out
}

// label is how cycleError renders one tag: "id (slug)". The tag helper gives
// every tag a slug equal to its id.
func label(id string) string { return id + " (" + id + ")" }

// walkIDs is the pre-order walk as a flat list of ids.
func walkIDs(t *testing.T, tree *TagTree) []string {
	t.Helper()
	var out []string
	if err := tree.Walk(func(n *TagNode) error {
		out = append(out, n.Tag.ID)
		return nil
	}); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	return out
}

func TestBuildTagTree_Shape(t *testing.T) {
	tests := []struct {
		name  string
		tags  []Tag
		roots []string
		// walk is the expected pre-order traversal: parents before children,
		// siblings in input order.
		walk    []string
		orphans []string
		depths  map[string]int
	}{
		{
			name:    "empty input builds an empty tree",
			tags:    nil,
			roots:   nil,
			walk:    nil,
			orphans: nil,
		},
		{
			name:    "a flat list is all roots, in input order",
			tags:    []Tag{tag("b", ""), tag("a", ""), tag("c", "")},
			roots:   []string{"b", "a", "c"},
			walk:    []string{"b", "a", "c"},
			orphans: nil,
			depths:  map[string]int{"b": 0, "a": 0, "c": 0},
		},
		{
			name:    "two levels",
			tags:    []Tag{tag("root", ""), tag("kid1", "root"), tag("kid2", "root")},
			roots:   []string{"root"},
			walk:    []string{"root", "kid1", "kid2"},
			orphans: nil,
			depths:  map[string]int{"root": 0, "kid1": 1, "kid2": 1},
		},
		{
			name: "three levels, depth counts from the root",
			tags: []Tag{tag("a", ""), tag("b", "a"), tag("c", "b"), tag("d", "c")},
			// One chain, so the walk is the chain.
			roots:   []string{"a"},
			walk:    []string{"a", "b", "c", "d"},
			orphans: nil,
			depths:  map[string]int{"a": 0, "b": 1, "c": 2, "d": 3},
		},
		{
			name: "a child BEFORE its parent in the input still links",
			// No server ordering GUARANTEES parents before children -- (name,
			// slug, id) sorts on the name, not the parent chain, so it may
			// happen to and is never obliged to. "Parents first" is a shape the
			// assembly must never assume.
			tags:    []Tag{tag("kid", "root"), tag("root", "")},
			roots:   []string{"root"},
			walk:    []string{"root", "kid"},
			orphans: nil,
			depths:  map[string]int{"root": 0, "kid": 1},
		},
		{
			name: "a tag whose parent is absent becomes a root AND an orphan",
			tags: []Tag{tag("real-root", ""), tag("orphan", "not-in-this-page")},
			// Both are roots -- the orphan is not dropped -- and the orphan is
			// additionally indexed.
			roots:   []string{"real-root", "orphan"},
			walk:    []string{"real-root", "orphan"},
			orphans: []string{"orphan"},
			depths:  map[string]int{"real-root": 0, "orphan": 0},
		},
		{
			name: "an orphan keeps its own subtree",
			tags: []Tag{tag("orphan", "absent"), tag("kid", "orphan")},
			// The missing ancestor does not cost the orphan its children.
			roots:   []string{"orphan"},
			walk:    []string{"orphan", "kid"},
			orphans: []string{"orphan"},
			depths:  map[string]int{"orphan": 0, "kid": 1},
		},
		{
			name: "interleaved siblings keep input order per parent",
			tags: []Tag{
				tag("a", ""), tag("b", ""),
				tag("a2", "a"), tag("b1", "b"), tag("a1", "a"),
			},
			roots:   []string{"a", "b"},
			walk:    []string{"a", "a2", "a1", "b", "b1"},
			orphans: nil,
			depths:  map[string]int{"a": 0, "b": 0, "a1": 1, "a2": 1, "b1": 1},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tree, err := BuildTagTree(tc.tags)
			if err != nil {
				t.Fatalf("BuildTagTree: %v", err)
			}
			if got := ids(tree.Roots); !slices.Equal(got, tc.roots) {
				t.Errorf("Roots = %v, want %v", got, tc.roots)
			}
			if got := ids(tree.Orphans); !slices.Equal(got, tc.orphans) {
				t.Errorf("Orphans = %v, want %v", got, tc.orphans)
			}
			if got := walkIDs(t, tree); !slices.Equal(got, tc.walk) {
				t.Errorf("Walk = %v, want %v", got, tc.walk)
			}
			// The invariant: nothing is dropped, nothing is duplicated.
			if tree.Len() != len(tc.tags) {
				t.Errorf("Len = %d, want %d", tree.Len(), len(tc.tags))
			}
			if len(tc.walk) != len(tc.tags) {
				t.Errorf("the case itself expects %d walked nodes for %d tags", len(tc.walk), len(tc.tags))
			}
			for id, want := range tc.depths {
				node := tree.Node(id)
				if node == nil {
					t.Fatalf("Node(%q) = nil", id)
				}
				if node.Depth != want {
					t.Errorf("Node(%q).Depth = %d, want %d", id, node.Depth, want)
				}
			}
		})
	}
}

func TestBuildTagTree_ParentAndChildrenAreWiredBothWays(t *testing.T) {
	tree, err := BuildTagTree([]Tag{tag("root", ""), tag("kid", "root")})
	if err != nil {
		t.Fatalf("BuildTagTree: %v", err)
	}
	root, kid := tree.Node("root"), tree.Node("kid")
	if root == nil || kid == nil {
		t.Fatalf("Node: root=%v kid=%v", root, kid)
	}
	if kid.Parent != root {
		t.Errorf("kid.Parent = %v, want the root node", kid.Parent)
	}
	if len(root.Children) != 1 || root.Children[0] != kid {
		t.Errorf("root.Children = %v, want [kid]", ids(root.Children))
	}
	if root.Parent != nil {
		t.Errorf("root.Parent = %v, want nil", root.Parent)
	}
	if len(kid.Children) != 0 {
		t.Errorf("kid.Children = %v, want empty", ids(kid.Children))
	}
}

func TestBuildTagTree_IsOrphanSeparatesTheTwoMeaningsOfANilParent(t *testing.T) {
	tree, err := BuildTagTree([]Tag{tag("root", ""), tag("orphan", "absent")})
	if err != nil {
		t.Fatalf("BuildTagTree: %v", err)
	}
	if tree.Node("root").IsOrphan() {
		t.Error("a tag with no ParentID reports IsOrphan")
	}
	if !tree.Node("orphan").IsOrphan() {
		t.Error("a tag whose parent is absent does not report IsOrphan")
	}
	// Both have a nil Parent; only the data tells them apart.
	if tree.Node("orphan").Parent != nil {
		t.Error("an orphan's Parent should be nil")
	}
	var nilNode *TagNode
	if nilNode.IsOrphan() {
		t.Error("a nil node reports IsOrphan")
	}
}

// An active child of a DEACTIVATED parent is the commonest orphan there is: the
// server's delete is a deactivation, it cascades to aliases and never to
// children, and an unfiltered list returns active rows only -- so the child
// comes back and its parent does not. Assembly must neither drop the child nor
// prune the inactive parent when it IS fetched.
func TestBuildTagTree_KeepsInactiveTagsAndTheirLiveChildren(t *testing.T) {
	deactivated := tag("parent", "")
	deactivated.IsActive = false
	child := tag("child", "parent")

	t.Run("both fetched: the hierarchy is intact and nothing is pruned", func(t *testing.T) {
		tree, err := BuildTagTree([]Tag{deactivated, child})
		if err != nil {
			t.Fatalf("BuildTagTree: %v", err)
		}
		if tree.Len() != 2 {
			t.Fatalf("Len = %d, want 2 -- an inactive tag must not be pruned", tree.Len())
		}
		if got := ids(tree.Roots); !slices.Equal(got, []string{"parent"}) {
			t.Errorf("Roots = %v, want [parent]", got)
		}
		if tree.Node("child").Parent != tree.Node("parent") {
			t.Error("the live child lost its deactivated parent")
		}
		if tree.Node("parent").Tag.IsActive {
			t.Error("IsActive was rewritten")
		}
	})

	t.Run("active-only list: the child survives as an orphan", func(t *testing.T) {
		tree, err := BuildTagTree([]Tag{child})
		if err != nil {
			t.Fatalf("BuildTagTree: %v", err)
		}
		if got := ids(tree.Orphans); !slices.Equal(got, []string{"child"}) {
			t.Errorf("Orphans = %v, want [child]", got)
		}
		if tree.Len() != 1 {
			t.Errorf("Len = %d, want 1 -- the child must not be dropped", tree.Len())
		}
	})
}

func TestBuildTagTree_RefusesACycle(t *testing.T) {
	tests := []struct {
		name string
		tags []Tag
		// wantIDs are the ids the message must name; wantAbsent must not appear.
		wantIDs    []string
		wantAbsent []string
	}{
		{
			// The database's tag_parent_cannot_be_self constraint forbids this
			// one, so it cannot arrive from a live server -- it can from a
			// hand-built slice, and it must not loop forever.
			name:    "self-parent",
			tags:    []Tag{tag("a", "a")},
			wantIDs: []string{"a"},
		},
		{
			// Two PATCHes reach this: the server validates tenant, application
			// and namespace compatibility on a parent, never the ancestry.
			name:    "two-node cycle",
			tags:    []Tag{tag("a", "b"), tag("b", "a")},
			wantIDs: []string{"a", "b"},
		},
		{
			name:    "three-node cycle alongside a healthy tree",
			tags:    []Tag{tag("root", ""), tag("kid", "root"), tag("a", "c"), tag("b", "a"), tag("c", "b")},
			wantIDs: []string{"a", "b", "c"},
			// The healthy half is not implicated.
			wantAbsent: []string{"root", "kid"},
		},
		{
			// The node that hangs BELOW the cycle is unreachable too. The
			// message must name the cycle itself, not the innocent descendant.
			//
			// The descendant is FIRST in the input on purpose: the walk up
			// starts from the first unreached tag, so this is the ordering that
			// exercises the prefix trim in cycleError. With the cycle first the
			// walk begins inside it and trims nothing, and a regression that
			// dropped the trim would pass.
			name:       "a descendant of a cycle is not named",
			tags:       []Tag{tag("hanger", "a"), tag("a", "b"), tag("b", "a")},
			wantIDs:    []string{"a", "b"},
			wantAbsent: []string{"hanger"},
		},
		{
			// Two independent cycles: the report names the one the first
			// unreached tag leads to, and nothing from the other.
			name:       "two independent cycles report the first",
			tags:       []Tag{tag("x", "y"), tag("y", "x"), tag("p", "q"), tag("q", "p")},
			wantIDs:    []string{"x", "y"},
			wantAbsent: []string{"p", "q"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tree, err := BuildTagTree(tc.tags)
			if err == nil {
				t.Fatalf("BuildTagTree returned a tree for a cycle: %v", ids(tree.Roots))
			}
			if tree != nil {
				t.Errorf("BuildTagTree returned a non-nil tree alongside the error")
			}
			if !errors.Is(err, ErrTagCycle) {
				t.Fatalf("error does not match ErrTagCycle: %v", err)
			}
			// Matched as the whole LABEL ("id (slug)") rather than as a bare
			// substring: a one-character id matches half the sentence
			// otherwise -- "p" appears in the word "parent" -- and an absence
			// assertion that cannot fail is worse than none.
			for _, id := range tc.wantIDs {
				if !strings.Contains(err.Error(), label(id)) {
					t.Errorf("message does not name %q: %v", id, err)
				}
			}
			for _, id := range tc.wantAbsent {
				if strings.Contains(err.Error(), label(id)) {
					t.Errorf("message names %q, which is not in the cycle: %v", id, err)
				}
			}
		})
	}
}

// The reported cycle must not depend on map iteration order: the same input has
// to produce the same message every time, or a log line is unreproducible.
func TestBuildTagTree_CycleMessageIsDeterministic(t *testing.T) {
	tags := []Tag{tag("a", "c"), tag("b", "a"), tag("c", "b"), tag("d", "c")}
	_, first := BuildTagTree(tags)
	if first == nil {
		t.Fatal("BuildTagTree accepted a cycle")
	}
	for i := range 50 {
		_, err := BuildTagTree(tags)
		if err == nil || err.Error() != first.Error() {
			t.Fatalf("run %d produced %v, want %v", i, err, first)
		}
	}

	// The chain is written parent-to-child and closes where it opened, so the
	// first and last labels are the same tag.
	chain := strings.TrimSuffix(first.Error(), ": "+ErrTagCycle.Error())
	chain = strings.TrimPrefix(chain, "octonomy: BuildTagTree: ")
	links := strings.Split(chain, " -> ")
	if len(links) != 4 {
		t.Fatalf("chain %q has %d links, want 4 for a three-tag cycle", chain, len(links))
	}
	if links[0] != links[len(links)-1] {
		t.Errorf("chain %q does not close where it opened", chain)
	}
	// d hangs below the cycle and is not part of it.
	if strings.Contains(chain, "d (d)") {
		t.Errorf("chain %q names a descendant of the cycle", chain)
	}
}

func TestBuildTagTree_RefusesADuplicateID(t *testing.T) {
	tree, err := BuildTagTree([]Tag{tag("a", ""), tag("b", "a"), tag("a", "")})
	if err == nil {
		t.Fatal("BuildTagTree accepted a repeated id")
	}
	if tree != nil {
		t.Error("BuildTagTree returned a non-nil tree alongside the error")
	}
	if !errors.Is(err, ErrDuplicateTagID) {
		t.Fatalf("error does not match ErrDuplicateTagID: %v", err)
	}
	if !strings.Contains(err.Error(), `"a"`) {
		t.Errorf("message does not name the repeated id: %v", err)
	}
	// The index, so the caller can find the second copy in a large slice.
	if !strings.Contains(err.Error(), "tags[2]") {
		t.Errorf("message does not name the position: %v", err)
	}
}

// The de-duplication in BuildTagTree's doc comment has to actually work: a walk
// that delivered a row twice must build after it, and the surviving copy must
// be the first one seen.
func TestBuildTagTree_DocumentedDeduplicationBuilds(t *testing.T) {
	first := tag("a", "")
	first.Name = "FIRST"
	again := tag("a", "")
	again.Name = "SECOND"
	tags := []Tag{first, tag("b", "a"), again}

	seen := make(map[string]bool, len(tags))
	tags = slices.DeleteFunc(tags, func(t Tag) bool {
		dup := seen[t.ID]
		seen[t.ID] = true
		return dup
	})

	tree, err := BuildTagTree(tags)
	if err != nil {
		t.Fatalf("BuildTagTree after de-duplication: %v", err)
	}
	if tree.Len() != 2 {
		t.Fatalf("Len = %d, want 2", tree.Len())
	}
	if got := tree.Node("a").Tag.Name; got != "FIRST" {
		t.Errorf("kept copy is %q, want the first one", got)
	}
}

func TestBuildTagTree_RefusesABlankID(t *testing.T) {
	tree, err := BuildTagTree([]Tag{tag("a", ""), {Slug: "no-id"}})
	if err == nil {
		t.Fatal("BuildTagTree accepted a blank id")
	}
	if tree != nil {
		t.Error("BuildTagTree returned a non-nil tree alongside the error")
	}
	if !strings.Contains(err.Error(), "tags[1]") {
		t.Errorf("message does not name the position: %v", err)
	}
	// A blank id is not a duplicate and not a cycle; it must not borrow either
	// sentinel, or a caller's de-duplicate-and-retry branch runs forever.
	if errors.Is(err, ErrDuplicateTagID) || errors.Is(err, ErrTagCycle) {
		t.Errorf("a blank id matched another refusal's sentinel: %v", err)
	}
}

func TestBuildTagTree_CopySemantics(t *testing.T) {
	t.Run("an edit to the input slice does not reach the tree", func(t *testing.T) {
		tags := []Tag{tag("root", ""), tag("kid", "root")}
		tree, err := BuildTagTree(tags)
		if err != nil {
			t.Fatalf("BuildTagTree: %v", err)
		}
		tags[0].Name = "rewritten after the build"
		if got := tree.Node("root").Tag.Name; got != "ROOT" {
			t.Errorf("the tree followed an edit to the input slice: Name = %q", got)
		}
	})

	// The other half, pinned because the doc comment states it rather than
	// defending against it: the copy is SHALLOW, so the pointer and map fields
	// stay shared with the input. This is NOT a hand-built-input curiosity --
	// a caller holds the slice a list response decoded into, and can write
	// through page.Data[i].ParentID or page.Data[i].Metadata just as this test
	// does. Cloning six pointers per node, plus a faithful clone of an
	// arbitrarily nested map[string]any, is a larger contract than the helper
	// should take on, so the behaviour is documented and pinned instead.
	t.Run("pointer and map fields stay shared with the input", func(t *testing.T) {
		parentID := "parent"
		description := "a description"
		tags := []Tag{
			{ID: "parent", Slug: "parent"},
			{
				ID: "child", Slug: "child",
				ParentID:    &parentID,
				Description: &description,
				Metadata:    Metadata{"label": "original"},
			},
		}
		tree, err := BuildTagTree(tags)
		if err != nil {
			t.Fatalf("BuildTagTree: %v", err)
		}
		// Written the way a caller holding a decoded page would write them:
		// through the pointer, and into the map.
		*tags[1].ParentID = "somewhere else"
		description = "rewritten"
		tags[1].Metadata["label"] = "changed"

		child := tree.Node("child")
		if got := *child.Tag.ParentID; got != "somewhere else" {
			t.Errorf("Tag.ParentID = %q: the pointer was cloned after all, and the doc comment now says it is not", got)
		}
		if got := *child.Tag.Description; got != "rewritten" {
			t.Errorf("Tag.Description = %q: the pointer was cloned after all", got)
		}
		if got := child.Tag.Metadata["label"]; got != "changed" {
			t.Errorf("Tag.Metadata[\"label\"] = %v: the map was cloned after all", got)
		}
		// The ASSEMBLED shape was computed at build time and does not move with
		// the pointer, which is the part that would otherwise be a silent lie.
		if child.Parent == nil || child.Parent.Tag.ID != "parent" {
			t.Errorf("the link moved with the caller's pointer: %#v", child.Parent)
		}
	})
}

func TestTagTree_Walk(t *testing.T) {
	tree, err := BuildTagTree([]Tag{
		tag("a", ""), tag("b", ""),
		tag("a1", "a"), tag("a2", "a"), tag("a1x", "a1"),
	})
	if err != nil {
		t.Fatalf("BuildTagTree: %v", err)
	}

	t.Run("pre-order, siblings in input order", func(t *testing.T) {
		want := []string{"a", "a1", "a1x", "a2", "b"}
		if got := walkIDs(t, tree); !slices.Equal(got, want) {
			t.Errorf("Walk = %v, want %v", got, want)
		}
	})

	t.Run("a subtree walk starts at the node", func(t *testing.T) {
		var got []string
		if err := tree.Node("a1").Walk(func(n *TagNode) error {
			got = append(got, n.Tag.ID)
			return nil
		}); err != nil {
			t.Fatalf("Walk: %v", err)
		}
		if want := []string{"a1", "a1x"}; !slices.Equal(got, want) {
			t.Errorf("subtree Walk = %v, want %v", got, want)
		}
	})

	t.Run("the first error stops the walk and is returned unchanged", func(t *testing.T) {
		stop := errors.New("stop")
		var visited []string
		err := tree.Walk(func(n *TagNode) error {
			visited = append(visited, n.Tag.ID)
			if n.Tag.ID == "a1" {
				return stop
			}
			return nil
		})
		if !errors.Is(err, stop) {
			t.Fatalf("Walk = %v, want the sentinel back", err)
		}
		if want := []string{"a", "a1"}; !slices.Equal(visited, want) {
			t.Errorf("visited %v after the error, want %v", visited, want)
		}
	})

	t.Run("sorting children mid-walk is honored for nodes not yet visited", func(t *testing.T) {
		// Documented: fn runs on a node BEFORE its children are pushed, so a
		// sort applied in fn decides the order they are visited in.
		sorted, err := BuildTagTree([]Tag{tag("r", ""), tag("z", "r"), tag("a", "r")})
		if err != nil {
			t.Fatalf("BuildTagTree: %v", err)
		}
		var got []string
		err = sorted.Walk(func(n *TagNode) error {
			slices.SortFunc(n.Children, func(x, y *TagNode) int {
				return strings.Compare(x.Tag.ID, y.Tag.ID)
			})
			got = append(got, n.Tag.ID)
			return nil
		})
		if err != nil {
			t.Fatalf("Walk: %v", err)
		}
		if want := []string{"r", "a", "z"}; !slices.Equal(got, want) {
			t.Errorf("Walk = %v, want %v", got, want)
		}
	})

	t.Run("a nil callback is an error rather than a panic", func(t *testing.T) {
		// Each refuses a nil callback in the same words. A Walk that
		// dereferenced it would panic out of a library that promises not to,
		// and only on a tree with at least one node -- the shape that survives
		// a test suite and fails in production.
		empty, err := BuildTagTree(nil)
		if err != nil {
			t.Fatalf("BuildTagTree(nil): %v", err)
		}
		for _, tc := range []struct {
			name string
			call func() error
		}{
			{"populated tree", func() error { return tree.Walk(nil) }},
			{"empty tree", func() error { return empty.Walk(nil) }},
			{"nil tree", func() error { var nilTree *TagTree; return nilTree.Walk(nil) }},
			{"node", func() error { return tree.Roots[0].Walk(nil) }},
			{"nil node", func() error { var nilNode *TagNode; return nilNode.Walk(nil) }},
		} {
			err := tc.call()
			if err == nil {
				t.Errorf("%s: Walk(nil) returned no error", tc.name)
				continue
			}
			if !strings.Contains(err.Error(), "callback is nil") {
				t.Errorf("%s: Walk(nil) = %v, want a nil-callback error", tc.name, err)
			}
		}
	})

	t.Run("nil receivers walk nothing rather than panicking", func(t *testing.T) {
		var nilTree *TagTree
		if err := nilTree.Walk(func(*TagNode) error { return errors.New("called") }); err != nil {
			t.Errorf("nil tree Walk = %v, want nil", err)
		}
		var nilNode *TagNode
		if err := nilNode.Walk(func(*TagNode) error { return errors.New("called") }); err != nil {
			t.Errorf("nil node Walk = %v, want nil", err)
		}
	})
}

func TestTagNode_Path(t *testing.T) {
	tree, err := BuildTagTree([]Tag{
		tag("root", ""), tag("mid", "root"), tag("leaf", "mid"), tag("orphan", "absent"),
	})
	if err != nil {
		t.Fatalf("BuildTagTree: %v", err)
	}

	tests := []struct {
		name string
		node *TagNode
		want []string
	}{
		{name: "a leaf's path is the breadcrumb, root first", node: tree.Node("leaf"), want: []string{"root", "mid", "leaf"}},
		{name: "a root's path is itself", node: tree.Node("root"), want: []string{"root"}},
		{name: "an orphan's path starts at itself", node: tree.Node("orphan"), want: []string{"orphan"}},
		{name: "a nil node has no path", node: nil, want: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := make([]string, 0, len(tc.want))
			for _, tg := range tc.node.Path() {
				got = append(got, tg.ID)
			}
			if len(tc.want) == 0 && len(got) == 0 {
				return
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("Path = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTagTree_Node(t *testing.T) {
	tree, err := BuildTagTree([]Tag{tag("a", "")})
	if err != nil {
		t.Fatalf("BuildTagTree: %v", err)
	}
	if node := tree.Node("a"); node == nil || node.Tag.ID != "a" {
		t.Errorf("Node(\"a\") = %v, want the node for a", node)
	}
	if node := tree.Node("nope"); node != nil {
		t.Errorf("Node of an absent id = %v, want nil", node)
	}
	var nilTree *TagTree
	if node := nilTree.Node("a"); node != nil {
		t.Errorf("Node on a nil tree = %v, want nil", node)
	}
	if nilTree.Len() != 0 {
		t.Errorf("Len on a nil tree = %d, want 0", nilTree.Len())
	}
}

// The doc comment promises explicit stacks rather than recursion, and a
// hierarchy has no server-imposed depth limit. A chain far deeper than the
// default goroutine stack would tolerate proves it: with recursion this
// overflows and takes the process with it, which is exactly what a library that
// "never panics" must not do.
func TestBuildTagTree_DeepChainDoesNotOverflowTheStack(t *testing.T) {
	const depth = 50_000
	tags := make([]Tag, 0, depth)
	tags = append(tags, tag("n0", ""))
	for i := 1; i < depth; i++ {
		tags = append(tags, tag(fmt.Sprintf("n%d", i), fmt.Sprintf("n%d", i-1)))
	}

	tree, err := BuildTagTree(tags)
	if err != nil {
		t.Fatalf("BuildTagTree: %v", err)
	}
	if tree.Len() != depth {
		t.Fatalf("Len = %d, want %d", tree.Len(), depth)
	}
	deepest := tree.Node(fmt.Sprintf("n%d", depth-1))
	if deepest.Depth != depth-1 {
		t.Errorf("deepest node Depth = %d, want %d", deepest.Depth, depth-1)
	}
	if got := len(deepest.Path()); got != depth {
		t.Errorf("Path length = %d, want %d", got, depth)
	}
	walked := 0
	if err := tree.Walk(func(*TagNode) error { walked++; return nil }); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if walked != depth {
		t.Errorf("Walk visited %d nodes, want %d", walked, depth)
	}
}

// The invariant, stated as a test over a messy input: every tag in, every tag
// out, exactly once.
func TestBuildTagTree_NoTagIsEverDropped(t *testing.T) {
	tags := []Tag{
		tag("orphan-a", "gone"), tag("root", ""), tag("kid", "root"),
		tag("orphan-b", "also-gone"), tag("orphan-kid", "orphan-a"), tag("lonely", ""),
	}
	tree, err := BuildTagTree(tags)
	if err != nil {
		t.Fatalf("BuildTagTree: %v", err)
	}
	if tree.Len() != len(tags) {
		t.Errorf("Len = %d, want %d", tree.Len(), len(tags))
	}
	seen := map[string]int{}
	if err := tree.Walk(func(n *TagNode) error {
		seen[n.Tag.ID]++
		return nil
	}); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	for _, tg := range tags {
		if seen[tg.ID] != 1 {
			t.Errorf("tag %q was walked %d times, want exactly 1", tg.ID, seen[tg.ID])
		}
	}
	if len(seen) != len(tags) {
		t.Errorf("walked %d distinct tags, want %d", len(seen), len(tags))
	}
	// Every orphan is also a root, not a second collection.
	for _, orphan := range tree.Orphans {
		if !slices.Contains(tree.Roots, orphan) {
			t.Errorf("orphan %q is not in Roots", orphan.Tag.ID)
		}
	}
}
