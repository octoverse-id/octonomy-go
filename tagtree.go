package octonomy

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ErrTagCycle reports that the tags handed to BuildTagTree contain a parent
// cycle -- a chain of ParentID links that returns to where it started -- and
// therefore do not describe a tree.
//
// The server does not prevent this. The database forbids only the one-hop case
// (the tag_parent_cannot_be_self check constraint, parent_id != id), and
// validate_tag_parent checks tenant, application and namespace compatibility
// without ever walking the ancestry, so A -> B -> A is reachable through two
// ordinary PATCHes.
//
// Match it with errors.Is. The message names the cycle, id and slug, in
// parent-to-child order and ending where it began.
var ErrTagCycle = errors.New("octonomy: tags contain a parent cycle")

// ErrDuplicateTagID reports that the same tag id appeared more than once in the
// input.
//
// Match it with errors.Is; see BuildTagTree for why this is refused rather than
// resolved, and for the two-line de-duplication a walk needs.
var ErrDuplicateTagID = errors.New("octonomy: duplicate tag id")

// TagNode is one tag's position in an assembled tree.
//
// Tag is a SHALLOW copy of the input value: the struct is copied, so replacing
// an element of the slice you passed does not reach the tree, but its POINTER
// and MAP fields still point where they did. Tag carries six *string fields and
// a Metadata map, so a caller who kept a pointer it stored in one can still
// change what a node reports through it. That is only reachable for a
// hand-built slice -- a Tag decoded from a response owns pointers nothing else
// holds -- and cloning them here would be an allocation per node per field to
// defend against it. Build the tree from tags you are not still editing.
//
// Parent, Children and Depth are a SNAPSHOT computed by BuildTagTree, exported
// to be read and walked rather than rewired. Rewiring them is not defended
// against and cannot be in Go: Walk and Path follow the links they find, so a
// Parent or Children edited into a loop does not terminate, and emptying Roots
// leaves Len reporting tags that Walk no longer reaches. Appending to Children
// also leaves Parent and Depth describing the tree as it was built. Edit the
// slice and build a new tree instead.
type TagNode struct {
	// Tag is the tag this node carries.
	Tag Tag

	// Parent is the node for Tag.ParentID, or nil. Nil has TWO meanings and
	// they are worth keeping apart: the tag has no parent (Tag.ParentID is
	// nil), or it has one that was not in the input (IsOrphan reports true).
	Parent *TagNode

	// Children holds this node's child nodes in input order. It is nil for a
	// leaf.
	Children []*TagNode

	// Depth is 0 for a root and one more than the parent's otherwise. It counts
	// position WITHIN THIS TREE: an orphan is a root here and has depth 0 even
	// though the server knows it to be nested under a tag you did not fetch.
	Depth int
}

// TagTree is a client-side assembly of tags into the hierarchy their ParentID
// fields describe. Build it with BuildTagTree.
type TagTree struct {
	// Roots holds every node with no parent IN THIS TREE, in input order:
	// genuine roots (ParentID nil) and orphans alike. Walking Roots therefore
	// reaches every tag in the input exactly once.
	Roots []*TagNode

	// Orphans holds the subset of Roots whose ParentID names a tag that was not
	// in the input, in input order. It is an index into Roots, not a second
	// collection: these nodes appear in both.
	//
	// A non-empty Orphans is NOT a corruption report. It is the ordinary result
	// of asking the server for less than the whole taxonomy -- see BuildTagTree
	// for the three ways a perfectly healthy Octonomy produces one.
	Orphans []*TagNode

	index map[string]*TagNode
}

// BuildTagTree assembles a flat slice of tags into the hierarchy their ParentID
// fields describe.
//
// The server has no tree endpoint and no /tags/{id}/children route. A caller
// rendering a category browser or a tag picker either walks the list once per
// parent -- one request per node -- or fetches the set once and assembles it
// locally. This is that assembly, and nothing more: it makes no request, takes
// no context, and never consults the server.
//
//	page, err := client.Tags.List(ctx, &octonomy.TagListParams{
//		VocabularyID: octonomy.String(vocab.ID),
//		ListOptions:  octonomy.ListOptions{Limit: 200},
//	})
//	if err != nil {
//		return err
//	}
//	tree, err := octonomy.BuildTagTree(page.Data)
//	if err != nil {
//		return err
//	}
//	// Walk, not a loop over Roots: a root's Depth is always 0, and its
//	// children are not in that slice.
//	err = tree.Walk(func(node *octonomy.TagNode) error {
//		fmt.Println(strings.Repeat("  ", node.Depth) + node.Tag.Name)
//		return nil
//	})
//
// # NO TAG IS EVER DROPPED
//
// That is the one invariant worth remembering, and everything below follows
// from it: on success, tree.Len() == len(tags) and every input tag appears
// exactly once, reachable from Roots. Ambiguity is an ERROR rather than a quiet
// choice, because a helper that is ALMOST right is worse than none -- consumers
// work around it, and the workaround outlives the bug.
//
// This helper was deferred once for exactly that reason (#20): the four
// questions below have no answer that suits everybody. They are answered here
// by Octonomy's own behavior rather than by taste, which is what made it
// implementable.
//
// # A missing parent is normal, so its tag becomes a root and is also listed in Orphans
//
// A tag whose ParentID names a tag absent from the input is attached to Roots
// -- nothing is dropped -- and recorded in Orphans, so a caller that cares can
// tell it from a genuine root. The distinction is in the data either way:
// Tag.ParentID is set while Parent is nil (IsOrphan).
//
// THREE ROUTINE THINGS PRODUCE ONE, none of them a data defect:
//
//   - A deactivated parent. Delete is deactivation on the server, the cascade
//     reaches the tag's ALIASES ONLY and never its children, and an unfiltered
//     list returns active rows only (is_active defaults to true in the server's
//     filter). So a live child of a deactivated parent is returned while its
//     parent is not. NO SINGLE LIST CALL RECOVERS BOTH: IsActive is a *bool
//     that selects one side or the other, so fetch the missing parent by id
//     (Tags.Get reads deactivated rows) or list the inactive ones separately,
//     then rebuild from the combined slice.
//   - Namespace scope. A namespaced tag may name a GLOBAL parent -- the server
//     permits exactly that -- and a namespaced read without WithIncludeGlobal
//     does not return global rows.
//   - Any filter or page. VocabularyID, Type, ApplicationID and a limit each
//     cut the set; a parent outside the cut is a parent outside the input.
//
// # Inactive tags are kept, untouched
//
// Pruning them is a filter (TagListParams.IsActive), applied when you fetch,
// and it belongs there rather than here: the server allows an ACTIVE tag under
// an INACTIVE parent, so dropping inactive nodes during assembly would orphan
// live children of a deactivated category -- a shape change this package has no
// business making on your behalf. The SDK adds ergonomics, not behavior.
//
// # A cycle is an error
//
// A cycle cannot be rendered as a tree, and the failure mode of ignoring one is
// invisible: every tag in the cycle, and everything beneath it, has a parent in
// the set and therefore never becomes a root, so a naive assembly SILENTLY
// returns fewer nodes than it was given. Refusing with ErrTagCycle, naming the
// chain, is the only outcome that does not lose rows without saying so.
//
// # Depth is reported, not limited
//
// There is no maximum depth and no recursion here to overflow -- the assembly,
// the cycle check and Walk all use explicit stacks, so a 50,000-deep chain
// costs memory rather than a stack overflow. Read TagNode.Depth if you want to
// stop rendering at a level.
//
// # A duplicate id is refused
//
// Two rows sharing an id mean the input is not a set, and choosing between them
// is a guess about which is current -- the one place assembly could silently
// produce a DIFFERENT tree (the copies may disagree about ParentID) with no
// indication it did. It returns ErrDuplicateTagID naming the id.
//
// This is reachable from an ordinary Each walk, whose doc comment already
// warns that offset drift can deliver a row twice and says to de-duplicate on
// id. That is the fix, and it is two lines:
//
//	seen := make(map[string]bool, len(tags))
//	tags = slices.DeleteFunc(tags, func(t octonomy.Tag) bool {
//		dup := seen[t.ID]
//		seen[t.ID] = true
//		return dup
//	})
//
// Note that de-duplicating removes the double-delivery half of drift only. A
// walk can also MISS rows, and a missed parent turns up here as an orphan
// rather than as an error.
//
// # Order
//
// Roots, Orphans and every Children slice are in INPUT ORDER, and no sort is
// applied. GET /tags has no ORDER BY at all (its usage_count annotation makes
// the query a GROUP BY, and Django drops Meta.ordering from aggregate queries),
// so the order you get is the order the server happened to answer with. Sort
// the input, or sort Children yourself, if you need a stable rendering:
//
//	tree.Walk(func(n *octonomy.TagNode) error {
//		slices.SortFunc(n.Children, func(a, b *octonomy.TagNode) int {
//			return strings.Compare(a.Tag.Name, b.Tag.Name)
//		})
//		return nil
//	})
//
// A nil or empty slice builds an empty tree and no error: no tags is not a
// failure.
func BuildTagTree(tags []Tag) (*TagTree, error) {
	tree := &TagTree{index: make(map[string]*TagNode, len(tags))}
	nodes := make([]*TagNode, 0, len(tags))

	// Pass one indexes every tag. It is separate from the linking pass because
	// a parent may appear AFTER its child in the input -- the list has no order
	// at all, so "parents first" is not a shape this can assume.
	for i := range tags {
		if tags[i].ID == "" {
			return nil, fmt.Errorf("octonomy: BuildTagTree: tags[%d] has a blank id; a tag decoded by this SDK always carries one, so this slice was not built from a list response", i)
		}
		if _, dup := tree.index[tags[i].ID]; dup {
			return nil, fmt.Errorf("octonomy: BuildTagTree: tags[%d] repeats id %q; de-duplicate on id before assembling (an Each walk can deliver a row twice): %w",
				i, tags[i].ID, ErrDuplicateTagID)
		}
		node := &TagNode{Tag: tags[i]}
		tree.index[tags[i].ID] = node
		nodes = append(nodes, node)
	}

	for _, node := range nodes {
		if node.Tag.ParentID == nil {
			tree.Roots = append(tree.Roots, node)
			continue
		}
		parent, ok := tree.index[*node.Tag.ParentID]
		if !ok {
			// The parent exists on the server; it is just not in this slice.
			// Promote rather than drop, and say so in Orphans.
			tree.Roots = append(tree.Roots, node)
			tree.Orphans = append(tree.Orphans, node)
			continue
		}
		node.Parent = parent
		parent.Children = append(parent.Children, node)
	}

	// Depth is assigned by descending from the roots, which doubles as the
	// cycle check: a node in a cycle has a parent in the set, so it is not a
	// root, and nothing above it is either -- the descent cannot reach it. Any
	// node left unreached is in a cycle or hangs beneath one, and the count is
	// what detects it.
	reached := make(map[*TagNode]bool, len(nodes))
	stack := make([]*TagNode, len(tree.Roots))
	copy(stack, tree.Roots)
	for len(stack) > 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		reached[node] = true
		for _, child := range node.Children {
			child.Depth = node.Depth + 1
			stack = append(stack, child)
		}
	}
	if len(reached) != len(nodes) {
		return nil, cycleError(nodes, reached)
	}

	return tree, nil
}

// cycleError names one cycle for the message. It walks UP from the first
// unreached node in input order -- deterministic, so the same input always
// reports the same cycle -- until it revisits a node, which is the cycle's
// entry point. The unreached node itself may hang below the cycle rather than
// sit in it, so the ancestors walked before that entry point are dropped.
func cycleError(nodes []*TagNode, reached map[*TagNode]bool) error {
	var start *TagNode
	for _, node := range nodes {
		if !reached[node] {
			start = node
			break
		}
	}
	if start == nil {
		// Unreachable: the caller only gets here when a node was not reached.
		// Stated as an error rather than assumed, because the alternative when
		// the assumption is wrong is an index panic, and this package does not
		// panic.
		return fmt.Errorf("octonomy: BuildTagTree: %w", ErrTagCycle)
	}

	seen := make(map[*TagNode]int, len(nodes))
	var path []*TagNode
	for node := start; node != nil; node = node.Parent {
		if at, ok := seen[node]; ok {
			path = path[at:]
			break
		}
		seen[node] = len(path)
		path = append(path, node)
	}

	// Walked child-to-parent; read it back the way the links point.
	slices.Reverse(path)
	labels := make([]string, 0, len(path)+1)
	for _, node := range path {
		labels = append(labels, fmt.Sprintf("%s (%s)", node.Tag.ID, node.Tag.Slug))
	}
	labels = append(labels, labels[0])
	return fmt.Errorf("octonomy: BuildTagTree: %s: %w", strings.Join(labels, " -> "), ErrTagCycle)
}

// Len returns the number of tags in the tree, which on a successful build is
// the number of tags it was given.
//
// A nil tree has length 0. Every method here tolerates a nil receiver, because
// the natural spelling at a call site is the one that reaches them with one:
// BuildTagTree returns (nil, err) on a refusal, and a caller who logged the
// error and carried on would otherwise take a panic out of a package that
// promises never to raise one.
func (t *TagTree) Len() int {
	if t == nil {
		return 0
	}
	return len(t.index)
}

// Node returns the node for a tag id, or nil if the tree does not hold it.
//
// It is the lookup a breadcrumb starts from: find the node you are rendering,
// then call Path.
func (t *TagTree) Node(id string) *TagNode {
	if t == nil {
		return nil
	}
	return t.index[id]
}

// Walk calls fn once for every node in the tree, parents before children
// (pre-order), roots and siblings in input order.
//
// It stops at the first error fn returns and returns it unchanged, so a
// sentinel of your own is how a walk says "found it, stop":
//
//	var errFound = errors.New("found")
//
// A nil fn is an error rather than a panic, and a nil tree walks nothing.
//
// The walk reads Children, so fn may sort them -- a node's children are visited
// after fn has run on it. It may not safely ADD or REMOVE nodes mid-walk.
func (t *TagTree) Walk(fn func(*TagNode) error) error {
	// Refused here as well as in TagNode.Walk, so an empty or nil tree answers
	// the same way a populated one does. A nil callback is caller misuse, and
	// Each refuses it in the same words rather than dereferencing it: the
	// library never panics, and "it only panicked on an empty tree" is the
	// shape that reaches production.
	if fn == nil {
		return errors.New("octonomy: TagTree.Walk: callback is nil")
	}
	if t == nil {
		return nil
	}
	for _, root := range t.Roots {
		if err := root.Walk(fn); err != nil {
			return err
		}
	}
	return nil
}

// Walk calls fn on this node and then, pre-order, on everything beneath it.
// It stops at the first error fn returns and returns it unchanged. A nil fn is
// an error rather than a panic, and a nil node walks nothing.
func (n *TagNode) Walk(fn func(*TagNode) error) error {
	if fn == nil {
		return errors.New("octonomy: TagNode.Walk: callback is nil")
	}
	if n == nil {
		return nil
	}
	// An explicit stack rather than recursion: nothing bounds a tag hierarchy's
	// depth, and this package does not get to crash the caller's program.
	stack := []*TagNode{n}
	for len(stack) > 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if err := fn(node); err != nil {
			return err
		}
		// Pushed in reverse so the first child is popped first and siblings are
		// visited in input order.
		for i := len(node.Children) - 1; i >= 0; i-- {
			stack = append(stack, node.Children[i])
		}
	}
	return nil
}

// Path returns the chain from this node's root down to and including this node
// -- the breadcrumb. A root's Path is a single element, and a nil node's is
// nil.
//
// It is the path WITHIN THIS TREE. An orphan is a root here, so its Path starts
// at itself and says nothing about the ancestors the server holds above it;
// fetch those tags and rebuild if you need the full chain.
func (n *TagNode) Path() []Tag {
	if n == nil {
		return nil
	}
	var path []Tag
	for node := n; node != nil; node = node.Parent {
		path = append(path, node.Tag)
	}
	slices.Reverse(path)
	return path
}

// IsOrphan reports whether this node's tag names a parent that was not in the
// input: it is a root of this tree but not a root of the taxonomy.
//
// See BuildTagTree for the three ordinary ways a healthy server produces one.
func (n *TagNode) IsOrphan() bool {
	return n != nil && n.Tag.ParentID != nil && n.Parent == nil
}
