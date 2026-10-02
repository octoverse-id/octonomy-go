package octonomy

// The t.Cleanup replacement model, held to its one claim (#95; the model itself
// is written down in docs/compat-test-disposition.md).
//
// t.Cleanup needs Go 1.14. On main at 5e40964 a helper registers its teardown
// with it, and the teardown runs when the TEST finishes. The tempting rewrite --
// `defer srv.Close()` inside the helper -- runs when the HELPER returns, which
// is before the caller has issued a single request. So on this line a helper that
// owns a resource RETURNS its teardown, and the caller defers it at the call
// site, which runs where t.Cleanup would have.
//
// These tests prove that of each helper that owns a server: the server is alive
// after the helper returns, it is still alive when the caller's assertions run,
// and it is gone once the returned cleanup has run. A helper that slipped back
// to a deferred close would fail the first half; one whose cleanup closed
// nothing would fail the second.

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestTestHelpers_TheirResourceOutlivesTheHelperAndDiesWithTheCleanup(t *testing.T) {
	ok := func(w http.ResponseWriter, _ *http.Request) {
		writeRaw(w, http.StatusOK, `{"data": {"id": "tag_1"}}`)
	}
	healthy := func(w http.ResponseWriter, _ *http.Request) {
		writeRaw(w, http.StatusOK, `{"status": "ok"}`)
	}

	for _, tc := range []struct {
		name string
		// build calls the helper and returns a call against its server, plus
		// the cleanup the helper handed back.
		build func(t *testing.T) (call func() error, cleanup func())
	}{
		{"newTestClient", func(t *testing.T) (func() error, func()) {
			c, cleanup := newTestClient(t, ok)
			return func() error { _, err := c.Tags.Get(context.Background(), "tag_1"); return err }, cleanup
		}},
		{"newVersionedTestClient", func(t *testing.T) (func() error, func()) {
			c, cleanup := newVersionedTestClient(t, APIV2, ok)
			return func() error { _, err := c.Tags.Get(context.Background(), "tag_1"); return err }, cleanup
		}},
		{"newTestHealthClient", func(t *testing.T) (func() error, func()) {
			hc, cleanup := newTestHealthClient(t, healthy)
			return func() error { _, err := hc.Health.Live(context.Background()); return err }, cleanup
		}},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			call, cleanup := tc.build(t)
			cleaned := false
			defer func() {
				if !cleaned {
					cleanup()
				}
			}()

			// The helper has returned. Its server must still answer -- this is
			// the call a helper-local `defer srv.Close()` would have broken.
			if err := call(); err != nil {
				t.Fatalf("the helper's server is gone before the caller used it: %v", err)
			}
			// And again, after assertions of the caller's own, which is where
			// t.Cleanup would still have kept it alive.
			if err := call(); err != nil {
				t.Fatalf("the helper's server died between the caller's calls: %v", err)
			}

			cleanup()
			cleaned = true
			if err := call(); !errors.Is(err, ErrUnreachable) {
				t.Errorf("after the returned cleanup ran, a call = %v, want ErrUnreachable: the cleanup "+
					"did not tear down what the helper built", err)
			}
		})
	}
}
