package octonomy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// pagedTags serves a slice of tags the way the server's
// OctonomyLimitOffsetPagination does: limit defaults to 50 and is clamped at
// 200, offset and the effective limit are echoed in the envelope, and `next` is
// nil exactly when offset+limit >= count. Reproducing the clamp matters -- it
// is what makes "advance by the limit you asked for" wrong, and a fixture that
// honored an over-large limit would let that bug pass.
func pagedTags(t *testing.T, total int, requests *[]ListOptions) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		limit, offset := 50, 0
		if raw := r.URL.Query().Get("limit"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil {
				t.Errorf("limit = %q, want an integer", raw)
			}
			if n > 0 {
				limit = n
			}
		}
		if raw := r.URL.Query().Get("offset"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil {
				t.Errorf("offset = %q, want an integer", raw)
			}
			offset = n
		}
		if requests != nil {
			*requests = append(*requests, ListOptions{Limit: limit, Offset: offset})
		}
		if limit > 200 {
			limit = 200
		}

		data := []Tag{}
		for i := offset; i < offset+limit && i < total; i++ {
			data = append(data, Tag{ID: fmt.Sprintf("tag_%d", i), Slug: fmt.Sprintf("slug-%d", i)})
		}
		body := map[string]any{
			"data": data,
			"pagination": map[string]any{
				"limit": limit, "offset": offset, "count": total,
				"next": nil, "previous": nil,
			},
		}
		if offset+limit < total {
			body["pagination"].(map[string]any)["next"] = "http://example.invalid/next"
		}
		writeJSON(t, w, http.StatusOK, body)
	}
}

// walkTags is the closure shape Each's doc comment shows, kept in one place so
// the tests below differ only in what they are asserting.
func walkTags(c *Client) func(context.Context, ListOptions) (*List[Tag], error) {
	return func(ctx context.Context, o ListOptions) (*List[Tag], error) {
		return c.Tags.List(ctx, &TagListParams{ListOptions: o})
	}
}

func collectIDs(seen *[]string) func(Tag) error {
	return func(tag Tag) error {
		*seen = append(*seen, tag.ID)
		return nil
	}
}

func TestEach_WalksEveryItemExactlyOnce(t *testing.T) {
	tests := []struct {
		name  string
		total int
		limit int
		// wantPages is the number of HTTP requests the walk must cost. Each
		// promises one per page in its doc comment, so it is asserted, not
		// assumed.
		wantPages int
	}{
		{"three pages", 7, 3, 3},
		{"exact multiple still needs no extra page", 6, 3, 2},
		{"single partial page", 2, 50, 1},
		{"single exact page", 3, 3, 1},
		{"empty collection", 0, 50, 1},
		{"default page size", 120, 0, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requests []ListOptions
			c := newTestClient(t, pagedTags(t, tt.total, &requests))

			var seen []string
			offset, err := Each(context.Background(), ListOptions{Limit: tt.limit}, walkTags(c), collectIDs(&seen))
			if err != nil {
				t.Fatalf("Each: %v", err)
			}
			if offset != tt.total {
				t.Errorf("offset = %d, want %d (one past the last item)", offset, tt.total)
			}
			if len(seen) != tt.total {
				t.Fatalf("visited %d items, want %d", len(seen), tt.total)
			}
			// Exactly once, in order, with nothing skipped or repeated.
			for i, id := range seen {
				if want := fmt.Sprintf("tag_%d", i); id != want {
					t.Fatalf("item %d = %q, want %q", i, id, want)
				}
			}
			if len(requests) != tt.wantPages {
				t.Errorf("made %d requests, want %d: %v", len(requests), tt.wantPages, requests)
			}
		})
	}
}

// The server clamps limit at 200 and says so in the envelope it returns. A
// walker that advanced by the limit it ASKED for would skip 300 of every 500
// here; advancing by what actually arrived is what makes this pass.
func TestEach_SurvivesTheServersLimitClamp(t *testing.T) {
	var requests []ListOptions
	c := newTestClient(t, pagedTags(t, 450, &requests))

	var seen []string
	offset, err := Each(context.Background(), ListOptions{Limit: 500}, walkTags(c), collectIDs(&seen))
	if err != nil {
		t.Fatalf("Each: %v", err)
	}
	if offset != 450 || len(seen) != 450 {
		t.Fatalf("offset = %d, visited = %d, want 450 and 450", offset, len(seen))
	}
	for i, id := range seen {
		if want := fmt.Sprintf("tag_%d", i); id != want {
			t.Fatalf("item %d = %q, want %q", i, id, want)
		}
	}
	// 200 + 200 + 50: three clamped pages, not one 500-item page.
	if len(requests) != 3 {
		t.Errorf("made %d requests, want 3: %v", len(requests), requests)
	}
	if requests[1].Offset != 200 || requests[2].Offset != 400 {
		t.Errorf("offsets advanced by the requested limit rather than by what arrived: %v", requests)
	}
}

func TestEach_StartOffsetResumes(t *testing.T) {
	c := newTestClient(t, pagedTags(t, 10, nil))

	var seen []string
	offset, err := Each(context.Background(), ListOptions{Limit: 3, Offset: 7}, walkTags(c), collectIDs(&seen))
	if err != nil {
		t.Fatalf("Each: %v", err)
	}
	if offset != 10 {
		t.Errorf("offset = %d, want 10", offset)
	}
	if len(seen) != 3 || seen[0] != "tag_7" || seen[2] != "tag_9" {
		t.Errorf("resumed walk visited %v, want tag_7..tag_9", seen)
	}
}

// A start offset at or past the end is a legal resume -- it is what the previous
// walk returned, and a collection can shrink under it -- and must be an empty,
// successful walk rather than an error. Both are covered because the server
// reaches them by different routes: offset == count slices an empty window,
// while offset > count is its own early return.
func TestEach_StartOffsetAtOrPastTheEnd(t *testing.T) {
	for _, start := range []int{5, 7} {
		t.Run(fmt.Sprintf("offset %d of 5", start), func(t *testing.T) {
			c := newTestClient(t, pagedTags(t, 5, nil))

			var seen []string
			offset, err := Each(context.Background(), ListOptions{Limit: 3, Offset: start}, walkTags(c), collectIDs(&seen))
			if err != nil {
				t.Fatalf("Each: %v", err)
			}
			if offset != start || len(seen) != 0 {
				t.Errorf("offset = %d, visited = %v, want %d and nothing", offset, seen, start)
			}
		})
	}
}

// --- Failure mid-walk, and the offset that makes it resumable --------------

var errCallback = errors.New("callback said no")

// A failure hands back the offset of the first item NOT processed, and the two
// failure kinds disagree about where that is: a dead fetch never delivered the
// page, so the page start is unprocessed, while a callback that rejected item 4
// of the page leaves item 4 -- not item 5 -- as the first one still owed.
func TestEach_FailureOffsetIsTheFirstUnprocessedItem(t *testing.T) {
	tests := []struct {
		name string
		// failPage is the 0-based page index whose fetch fails; -1 for none.
		failPage int
		// failAt is the 0-based item index the callback rejects; -1 for none.
		failAt     int
		wantOffset int
		wantSeen   int
	}{
		{"fetch fails on the third page", 2, -1, 6, 6},
		{"fetch fails on the first page", 0, -1, 0, 0},
		{"callback fails mid-page", -1, 4, 4, 4},
		{"callback fails on the first item", -1, 0, 0, 0},
		{"callback fails on a page boundary", -1, 3, 3, 3},
		{"callback fails on the very last item", -1, 8, 8, 8},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page := 0
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if page == tt.failPage {
					writeJSON(t, w, http.StatusInternalServerError, map[string]any{
						"error": map[string]any{"code": "boom", "message": "page is down"},
					})
					page++
					return
				}
				page++
				pagedTags(t, 9, nil)(w, r)
			})

			var seen []string
			offset, err := Each(context.Background(), ListOptions{Limit: 3}, walkTags(c),
				func(tag Tag) error {
					if len(seen) == tt.failAt {
						return errCallback
					}
					seen = append(seen, tag.ID)
					return nil
				})

			if err == nil {
				t.Fatal("expected an error")
			}
			if tt.failAt >= 0 && !errors.Is(err, errCallback) {
				t.Errorf("callback error was not returned to the caller: %v", err)
			}
			if offset != tt.wantOffset {
				t.Errorf("offset = %d, want %d", offset, tt.wantOffset)
			}
			if len(seen) != tt.wantSeen {
				t.Errorf("visited %d items, want %d", len(seen), tt.wantSeen)
			}
		})
	}
}

// The contract is only worth anything if the offset actually resumes. This
// fails a walk, restarts from what it handed back, and asserts the two halves
// join up with nothing lost, the failed item included.
//
// The fixture is a stable, totally ordered collection with nothing writing to
// it, which is the ONLY condition under which redelivery is guaranteed -- an
// offset is a position, not an identity, so against a collection that moved
// (or against the unordered tags list) the same offset may address a different
// row. The doc comment states it conditionally for that reason, and this test
// establishes the condition rather than the general claim.
func TestEach_ReturnedOffsetActuallyResumes(t *testing.T) {
	c := newTestClient(t, pagedTags(t, 10, nil))

	var first []string
	offset, err := Each(context.Background(), ListOptions{Limit: 4}, walkTags(c),
		func(tag Tag) error {
			if len(first) == 6 {
				return errCallback
			}
			first = append(first, tag.ID)
			return nil
		})
	if !errors.Is(err, errCallback) {
		t.Fatalf("first walk err = %v, want errCallback", err)
	}
	if offset != 6 {
		t.Fatalf("offset = %d, want 6", offset)
	}

	var second []string
	final, err := Each(context.Background(), ListOptions{Limit: 4, Offset: offset}, walkTags(c), collectIDs(&second))
	if err != nil {
		t.Fatalf("resumed walk: %v", err)
	}
	if final != 10 {
		t.Errorf("final offset = %d, want 10", final)
	}

	joined := append(append([]string{}, first...), second...)
	if len(joined) != 10 {
		t.Fatalf("the two halves cover %d items, want 10: %v", len(joined), joined)
	}
	for i, id := range joined {
		if want := fmt.Sprintf("tag_%d", i); id != want {
			t.Fatalf("item %d = %q, want %q -- the resume did not join up", i, id, want)
		}
	}
	// tag_6 is the item the callback rejected. Against this stable collection it
	// must appear in the second half: the resume redelivers it rather than
	// stepping over it.
	if second[0] != "tag_6" {
		t.Errorf("resumed walk began at %q, want tag_6 (the item that failed)", second[0])
	}
}

// Chaos: the server dies mid-walk rather than answering with an error envelope.
// The transport failure is a different code path from a 500, and the offset
// promise has to hold on it too -- this is the shape a real outage takes.
func TestEach_ServerKilledMidWalk(t *testing.T) {
	var srv *httptest.Server
	pages := 0
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if pages == 2 {
			// Close from another goroutine: Close blocks on outstanding
			// handlers, so closing inline would deadlock against this one.
			go srv.Close()
			// Hijack and drop the connection so the client sees a transport
			// failure now rather than racing the shutdown.
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			_ = conn.Close()
			return
		}
		pages++
		pagedTags(t, 12, nil)(w, r)
	}))
	t.Cleanup(srv.Close)

	c, err := New(Config{BaseURL: srv.URL, Token: "test-token", TenantID: "tenant-1"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var seen []string
	offset, err := Each(context.Background(), ListOptions{Limit: 4}, walkTags(c), collectIDs(&seen))
	if err == nil {
		t.Fatal("expected a transport error once the server died")
	}
	// Two pages of four were delivered before the kill; the third never
	// arrived, so offset 8 is the first unprocessed item.
	if offset != 8 {
		t.Errorf("offset = %d, want 8 -- a dead server must not lose the pages that did arrive", offset)
	}
	if len(seen) != 8 {
		t.Errorf("visited %d items, want 8", len(seen))
	}
}

func TestEach_ContextCancellationKeepsTheOffset(t *testing.T) {
	c := newTestClient(t, pagedTags(t, 12, nil))
	ctx, cancel := context.WithCancel(context.Background())

	var seen []string
	offset, err := Each(ctx, ListOptions{Limit: 4}, walkTags(c), func(tag Tag) error {
		seen = append(seen, tag.ID)
		if len(seen) == 8 {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if offset != 8 {
		t.Errorf("offset = %d, want 8", offset)
	}
}

// --- Termination guards ---------------------------------------------------

// The one misuse of this API that compiles: a page function that builds its own
// params and drops the ListOptions it was handed. Every iteration would then
// re-fetch page one and redeliver it, forever. Each refuses instead.
func TestEach_RefusesAPageFunctionThatIgnoresItsOptions(t *testing.T) {
	c := newTestClient(t, pagedTags(t, 100, nil))

	calls := 0
	offset, err := Each(context.Background(), ListOptions{Limit: 10},
		func(ctx context.Context, _ ListOptions) (*List[Tag], error) {
			calls++
			if calls > 5 {
				t.Fatal("Each kept looping on a page function that ignores its offset")
			}
			// Note the dropped ListOptions -- always page one.
			return c.Tags.List(ctx, &TagListParams{ListOptions: ListOptions{Limit: 10}})
		},
		func(Tag) error { return nil })

	if err == nil {
		t.Fatal("expected an error naming the dropped ListOptions")
	}
	if offset != 10 {
		t.Errorf("offset = %d, want 10 (the page start it could not advance past)", offset)
	}
	if calls != 2 {
		t.Errorf("page function called %d times, want 2 (one good, one caught)", calls)
	}
}

// The guard's limit, asserted so the doc comment cannot quietly outgrow it. A
// page function that ignores everything still succeeds when the walk fits in a
// single page -- there is no second page to advance to, so the offset it echoes
// is the one that was asked for, and the answer is right anyway.
func TestEach_GuardDoesNotFireOnASinglePageWalk(t *testing.T) {
	c := newTestClient(t, pagedTags(t, 3, nil))

	var seen []string
	offset, err := Each(context.Background(), ListOptions{Limit: 50},
		func(ctx context.Context, _ ListOptions) (*List[Tag], error) {
			// Every option dropped, and it does not matter here.
			return c.Tags.List(ctx, &TagListParams{})
		}, collectIDs(&seen))
	if err != nil {
		t.Fatalf("Each: %v", err)
	}
	if offset != 3 || len(seen) != 3 {
		t.Errorf("offset = %d, visited = %d, want 3 and 3", offset, len(seen))
	}
}

// A page claiming there is more while returning nothing cannot be advanced past.
// Trusting `next` alone here would spin forever.
func TestEach_EmptyPageTerminatesEvenWhenNextSaysOtherwise(t *testing.T) {
	calls := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls > 3 {
			// Errorf, not Fatalf: this runs on the server's goroutine, where
			// FailNow is not valid and would Goexit the handler -- turning the
			// assertion into a transport error that reports the wrong problem.
			t.Errorf("Each looped on an empty page that advertised a next link")
			writeJSON(t, w, http.StatusInternalServerError, nil)
			return
		}
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		writeJSON(t, w, http.StatusOK, map[string]any{
			"data": []Tag{},
			"pagination": map[string]any{
				"limit": 10, "offset": offset, "count": 999,
				"next": "http://example.invalid/next", "previous": nil,
			},
		})
	})

	offset, err := Each(context.Background(), ListOptions{Limit: 10}, walkTags(c), func(Tag) error { return nil })
	if err != nil {
		t.Fatalf("Each: %v", err)
	}
	if offset != 0 || calls != 1 {
		t.Errorf("offset = %d after %d calls, want 0 after 1", offset, calls)
	}
}

func TestEach_ArgumentValidation(t *testing.T) {
	ctx := context.Background()
	okPage := func(context.Context, ListOptions) (*List[Tag], error) {
		return &List[Tag]{}, nil
	}
	tests := []struct {
		name       string
		start      ListOptions
		page       func(context.Context, ListOptions) (*List[Tag], error)
		fn         func(Tag) error
		wantOffset int
	}{
		{"nil page function", ListOptions{Offset: 5}, nil, func(Tag) error { return nil }, 5},
		{"nil callback", ListOptions{Offset: 5}, okPage, nil, 5},
		{"negative offset", ListOptions{Offset: -1}, okPage, func(Tag) error { return nil }, 0},
		{"negative limit", ListOptions{Limit: -1, Offset: 3}, okPage, func(Tag) error { return nil }, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			offset, err := Each(ctx, tt.start, tt.page, tt.fn)
			if err == nil {
				t.Fatal("expected an error")
			}
			if offset != tt.wantOffset {
				t.Errorf("offset = %d, want %d", offset, tt.wantOffset)
			}
		})
	}
}

// A page function of the caller's own making can return (nil, nil). Dereferencing
// that would panic, and this library never panics.
func TestEach_NilListWithNoError(t *testing.T) {
	offset, err := Each(context.Background(), ListOptions{Offset: 4},
		func(context.Context, ListOptions) (*List[Tag], error) { return nil, nil },
		func(Tag) error { return nil })
	if err == nil {
		t.Fatal("expected an error for a nil *List")
	}
	if offset != 4 {
		t.Errorf("offset = %d, want 4", offset)
	}
}

// Cancelling during the LAST page is the case a per-page check misses: there is
// no next fetch to notice, so the walk would finish the page and report success.
func TestEach_CancellationDuringTheFinalPage(t *testing.T) {
	c := newTestClient(t, pagedTags(t, 5, nil))
	ctx, cancel := context.WithCancel(context.Background())

	var seen []string
	// One page of five, so nothing fetches again after the cancel.
	offset, err := Each(ctx, ListOptions{Limit: 50}, walkTags(c), func(tag Tag) error {
		seen = append(seen, tag.ID)
		if len(seen) == 2 {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled -- a cancel on the final page was swallowed", err)
	}
	if offset != 2 {
		t.Errorf("offset = %d, want 2", offset)
	}
	// The remaining three items of the page must not have been delivered.
	if len(seen) != 2 {
		t.Errorf("delivered %d items after cancellation, want 2", len(seen))
	}
}
