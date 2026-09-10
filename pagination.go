package octonomy

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
)

// ListOptions carries the limit/offset paging controls shared by every list
// endpoint. Embed it in a resource's *ListParams. The server defaults to a limit
// of 50 and caps it at 200.
type ListOptions struct {
	Limit  int
	Offset int
}

func (o ListOptions) apply(q url.Values) {
	if o.Limit > 0 {
		q.Set("limit", strconv.Itoa(o.Limit))
	}
	if o.Offset > 0 {
		q.Set("offset", strconv.Itoa(o.Offset))
	}
}

// Pagination is the metadata block returned alongside every list response.
// Next and Previous are absolute URLs for the adjacent pages, or nil at an edge.
type Pagination struct {
	Limit    int     `json:"limit"`
	Offset   int     `json:"offset"`
	Count    int     `json:"count"`
	Next     *string `json:"next"`
	Previous *string `json:"previous"`
}

// List is the generic envelope every Octonomy list endpoint returns:
// {"data": [...], "pagination": {...}}.
type List[T any] struct {
	Data       []T        `json:"data"`
	Pagination Pagination `json:"pagination"`
}

// Each walks every page of a list endpoint and calls fn once per item.
//
// IT ISSUES ONE HTTP REQUEST PER PAGE. A walk of 4,000 tags at the server's
// default page size is 80 round trips, not one. This package promises no hidden
// behavior, so the cost is in the name and stated here rather than buried:
// Each is a loop you did not have to write, not a bulk endpoint. Raise
// start.Limit to trade requests for response size -- the server's ceiling is
// 200, and it silently clamps anything larger.
//
// start seeds the walk. A zero ListOptions starts at the beginning with the
// server's default page size; set Offset to resume (see the return value) and
// Limit to choose the page size.
//
// page fetches one page. It receives the ListOptions Each has computed for that
// page and MUST pass them through to the list method, which is what advances
// the walk:
//
//	offset, err := octonomy.Each(ctx, octonomy.ListOptions{Limit: 200},
//		func(ctx context.Context, o octonomy.ListOptions) (*octonomy.List[octonomy.Tag], error) {
//			return client.Tags.List(ctx, &octonomy.TagListParams{
//				ListOptions:   o,
//				ApplicationID: octonomy.String("commerce"),
//			})
//		},
//		func(tag octonomy.Tag) error {
//			fmt.Println(tag.Slug)
//			return nil
//		},
//	)
//
// A closure taking extra arguments is how the nested list routes are walked
// too -- Tags.ListAliases, Tags.ListResources, Resources.ListTags, and the
// ListAuditLogs pair all fit the same shape, with their positional ids captured
// from the enclosing scope.
//
// # The returned offset
//
// Each returns start.Offset plus the number of items it successfully processed
// -- equivalently, the offset of the first item it did NOT process, which is
// what makes it a resume point. On a fetch failure that is the start of the
// page that failed; on a callback failure it is the failing item, so a resumed
// walk retries it rather than skipping it: delivery is at-least-once across a
// resume, never at-most-once.
//
// The offset is meaningful even when the error is not: a walk that dies on page
// 40 of 100 hands back 39 pages of progress instead of discarding it.
//
// IT IS NOT A POLLING CURSOR. Resuming from the offset a SUCCESSFUL walk
// returned does not find what has been created since, and the drift below is
// why: a new row can sort before that offset, where a resume will never look,
// and on a collection that shrank the offset simply points past the end. To see
// new items, walk again from the beginning.
//
// # Offset drift is real and is not solvable here
//
// The server pages by limit/offset and offers no cursor, so the window shifts
// under concurrent writes. A row that sorts before the current page pushes
// every later row one place right, and Each delivers one item twice; a row
// removed behind the cursor pulls them one place left, and Each never sees one.
// Deletion counts here because Octonomy deletes by deactivating and an
// unfiltered list returns active rows only, so a delete really does remove a
// row from the walked set.
//
// The sort order is PER ENDPOINT, not one rule. Vocabularies and tag aliases
// order by (name, slug, id); audit logs by (created_at DESC, id); assignments
// and resource tags by (assigned_at DESC, id).
//
// # The tags list has no ORDER BY at all, which is worse than drift
//
// GET /tags is the exception and it is the endpoint most likely to be walked.
// Its view annotates usage_count, which makes the query a GROUP BY, and Django
// drops a model's Meta.ordering from aggregate queries -- so the SQL carries no
// ORDER BY (verified against a running 3.1.0 server: the generated statement
// ends at GROUP BY, and Django's own queryset.ordered reports false).
//
// LIMIT/OFFSET WITHOUT ORDER BY IS UNDEFINED. Each page is a separate query and
// the database is free to answer two of them in different orders, so a walk of
// the tags list can repeat or miss rows WITH NO CONCURRENT WRITES AT ALL. In
// practice the order observed is stable while the rows are unchanged, because
// it falls out of one hash-aggregate plan; nothing promises that, and a
// different plan or a changed row count is enough to alter it. Treat a tags
// walk as best-effort unless the filtered set fits in one page, where the
// question does not arise.
//
// # What a caller can actually do
//
// NO CLIENT CAN FIX EITHER PROBLEM. Re-reading a page cannot distinguish a
// shifted window from a changed one, and this package will not pretend
// otherwise by de-duplicating and calling it exactness. What does help:
//
//   - Narrow the walk with a filter that does not change while it runs
//     (ApplicationID, VocabularyID, Type), so the set is small -- and small
//     enough to fit one page is the only fully safe size on the tags list.
//   - De-duplicate on ID. That is cheap and removes the double-delivery half.
//   - DETECT the other half, which is the part that leaves no trace: keep the
//     Pagination.Count from the first page and compare it with the number of
//     distinct IDs walked. Fewer means rows were missed. That does not recover
//     them, but it turns a silent wrong answer into a known one.
//   - Walk when nothing is writing.
//
// Each never retries, and a context cancellation surfaces as the page error
// along with the offset reached.
func Each[T any](
	ctx context.Context,
	start ListOptions,
	page func(context.Context, ListOptions) (*List[T], error),
	fn func(T) error,
) (int, error) {
	if page == nil {
		return start.Offset, errors.New("octonomy: Each: page function is nil")
	}
	if fn == nil {
		return start.Offset, errors.New("octonomy: Each: callback is nil")
	}
	if start.Offset < 0 {
		return 0, fmt.Errorf("octonomy: Each: start.Offset is %d, want >= 0", start.Offset)
	}
	if start.Limit < 0 {
		return start.Offset, fmt.Errorf("octonomy: Each: start.Limit is %d, want >= 0", start.Limit)
	}

	offset := start.Offset
	for {
		pageStart := offset
		p, err := page(ctx, ListOptions{Limit: start.Limit, Offset: pageStart})
		if err != nil {
			return pageStart, err
		}
		if p == nil {
			return pageStart, errors.New("octonomy: Each: page function returned a nil *List with no error")
		}

		// The server echoes the offset it actually served. If it does not match
		// the one Each asked for, the page function did not apply the Offset it
		// was handed -- the one way to misuse this API that still compiles --
		// and every following iteration would re-fetch this same page forever,
		// delivering it again each time. Refuse instead of looping.
		//
		// This catches only the non-terminating shape, and deliberately claims
		// no more. A dropped Limit is invisible here, and a page function that
		// ignores everything still passes on a walk that fits in one page --
		// where it also happens to be correct, since there was no second page
		// to advance to.
		if p.Pagination.Offset != pageStart {
			return pageStart, fmt.Errorf(
				"octonomy: Each: page function ignored the ListOptions it was given: asked for offset %d, server served %d",
				pageStart, p.Pagination.Offset)
		}

		for _, item := range p.Data {
			if err := fn(item); err != nil {
				return offset, err
			}
			offset++
		}

		// Two independent stop conditions, and both are load-bearing. Next is
		// the server's own end-of-collection signal. An empty page is the one
		// that guarantees termination regardless: without forward progress the
		// loop could not advance even if Next kept saying otherwise.
		if len(p.Data) == 0 || p.Pagination.Next == nil {
			return offset, nil
		}
	}
}
