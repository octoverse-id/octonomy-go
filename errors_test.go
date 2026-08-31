package octonomy

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// --- The envelope-less non-2xx rule --------------------------------------
//
// This is a REGRESSION suite, not a feature suite. Mapping an envelope-less
// non-2xx by status turned an unrouted 404 -- what a server with no /api/v2
// route returns for every call -- into CodeNotFound, so IsNotFound(err) reported
// true and a caller's ordinary "that tag doesn't exist" branch saw an empty
// taxonomy and no error at all.
//
// The old suite could not catch it: its one envelope-less test used a 502 and
// asserted StatusCode and Message but never Code, so the semantic mapping was
// unobserved. Every test below asserts Code.

// djangoUnroutedBody is Django's verbatim response for a path matching no URL
// pattern, which is exactly what /api/v2/tags is on a server predating the
// version shim. Captured from a live 3.1.0 container rather than invented, so
// this test pins the real shape and not a plausible one.
const djangoUnroutedBody = `<!doctype html>
<html lang="en">
<head>
  <title>Not Found</title>
</head>
<body>
  <h1>Not Found</h1><p>The requested resource was not found on this server.</p>
</body>
</html>`

// The trap itself: an envelope-less 404 must NOT satisfy IsNotFound.
func TestParseError_EnvelopeLess404IsNotAnOctonomyNotFound(t *testing.T) {
	c := newVersionedTestClient(t, APIV2, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(djangoUnroutedBody))
	})

	_, err := c.Tags.Get(context.Background(), "abc")
	if err == nil {
		t.Fatal("expected an error")
	}
	if IsNotFound(err) {
		t.Error("IsNotFound reported true for a 404 that carried no Octonomy error envelope: " +
			"a caller's not-found branch would read a missing /api/v2 as an empty taxonomy")
	}
	if !IsUnexpectedStatus(err) {
		t.Errorf("IsUnexpectedStatus = false, want true: %v", err)
	}
	apiErr, ok := AsAPIError(err)
	if !ok {
		t.Fatalf("error is not *APIError: %v", err)
	}
	if apiErr.Code != CodeUnexpectedStatus {
		t.Errorf("Code = %q, want %q", apiErr.Code, CodeUnexpectedStatus)
	}
	if apiErr.StatusCode != http.StatusNotFound {
		t.Errorf("StatusCode = %d, want 404", apiErr.StatusCode)
	}
	// The raw body survives -- it is the only diagnostic a non-Octonomy 404 has.
	if !strings.Contains(apiErr.Message, "Not Found") {
		t.Errorf("Message dropped the raw body: %q", apiErr.Message)
	}
	if !strings.Contains(apiErr.Message, "Config.APIVersion = APIV1") {
		t.Errorf("Message dropped the version hint: %q", apiErr.Message)
	}
}

// The converse, so the fix cannot be an over-reach: an ENVELOPED 404 is still a
// real Octonomy not_found.
func TestParseError_Enveloped404StaysANotFound(t *testing.T) {
	c := newVersionedTestClient(t, APIV2, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusNotFound, map[string]any{
			"error": map[string]any{"code": CodeNotFound, "message": "Resource not found."},
		})
	})

	_, err := c.Tags.Get(context.Background(), "missing")
	if !IsNotFound(err) {
		t.Fatalf("IsNotFound = false for an enveloped 404: %v", err)
	}
	if IsUnexpectedStatus(err) {
		t.Error("IsUnexpectedStatus reported true for an enveloped response")
	}
}

// A 503 from Octonomy's rollback gate and a 503 from a load balancer are
// different events with the same status. Only the envelope separates them, which
// is exactly what the old status-derived mapping threw away.
func TestParseError_Enveloped503IsDistinguishableFromABare503(t *testing.T) {
	t.Run("enveloped: the namespace rollback gate", func(t *testing.T) {
		c := newVersionedTestClient(t, APIV2, func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, http.StatusServiceUnavailable, map[string]any{
				"error": map[string]any{
					"code":    CodeNamespaceAPIDisabled,
					"message": "The namespaced v2 API is not enabled on this deployment.",
				},
			})
		})
		_, err := c.Tags.Get(context.Background(), "abc",
			WithNamespace("merchant", "m1"), WithApplication("shop"))
		if !IsNamespaceAPIDisabled(err) {
			t.Fatalf("IsNamespaceAPIDisabled = false: %v", err)
		}
		if IsUnexpectedStatus(err) {
			t.Error("an enveloped 503 must not read as an infrastructure failure")
		}
	})

	t.Run("bare: infrastructure", func(t *testing.T) {
		c := newVersionedTestClient(t, APIV2, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("no healthy upstream"))
		})
		_, err := c.Tags.Get(context.Background(), "abc")
		if !IsUnexpectedStatus(err) {
			t.Fatalf("IsUnexpectedStatus = false: %v", err)
		}
		if IsNamespaceAPIDisabled(err) {
			t.Error("a bare 503 must not read as the namespace rollback gate")
		}
	})
}

// Codes the SDK has no constant for are preserved verbatim rather than
// flattened -- #6 adds scope_immutable, and a server ahead of this SDK will send
// others.
func TestParseError_PreservesAnUnknownEnvelopeCode(t *testing.T) {
	c := newVersionedTestClient(t, APIV2, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusConflict, map[string]any{
			"error": map[string]any{"code": "some_future_code", "message": "nope"},
		})
	})

	_, err := c.Tags.Get(context.Background(), "abc")
	apiErr, ok := AsAPIError(err)
	if !ok {
		t.Fatalf("error is not *APIError: %v", err)
	}
	if apiErr.Code != "some_future_code" {
		t.Errorf("Code = %q, want the server's code verbatim", apiErr.Code)
	}
	if IsUnexpectedStatus(err) {
		t.Error("an enveloped response must not read as envelope-less")
	}
}

// The hint names the one cause the SDK can act on, and only where it applies.
func TestParseError_VersionHint(t *testing.T) {
	tests := []struct {
		name     string
		version  APIVersion
		status   int
		envelope bool
		wantHint bool
	}{
		{"envelope-less 404 on v2", APIV2, http.StatusNotFound, false, true},
		{"envelope-less 404 on v1", APIV1, http.StatusNotFound, false, false},
		{"envelope-less 502 on v2", APIV2, http.StatusBadGateway, false, false},
		{"enveloped 404 on v2", APIV2, http.StatusNotFound, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newVersionedTestClient(t, tt.version, func(w http.ResponseWriter, _ *http.Request) {
				if tt.envelope {
					writeJSON(t, w, tt.status, map[string]any{
						"error": map[string]any{"code": CodeNotFound, "message": "Resource not found."},
					})
					return
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte("boom"))
			})

			_, err := c.Tags.Get(context.Background(), "abc")
			if err == nil {
				t.Fatal("expected an error")
			}
			gotHint := strings.Contains(err.Error(), "Config.APIVersion = APIV1")
			if gotHint != tt.wantHint {
				t.Errorf("hint present = %v, want %v: %v", gotHint, tt.wantHint, err)
			}
			// Phrased as a possibility, never a diagnosis: an envelope-less 404
			// is also what a wrong BaseURL or a path-stripping proxy produces.
			if gotHint && !strings.Contains(err.Error(), "if this deployment predates") {
				t.Errorf("hint should be conditional, got: %v", err)
			}
		})
	}
}

// Every Is* helper matches its own code and nothing else. The three at the end
// were pre-existing gaps: the constants shipped without helpers, and two of them
// are exactly what assignment writes raise.
func TestErrorHelpers_MatchOnlyTheirOwnCode(t *testing.T) {
	helpers := []struct {
		code string
		fn   func(error) bool
	}{
		{CodeValidation, IsValidation},
		{CodeAuthRequired, IsAuthError},
		{CodeForbidden, IsForbidden},
		{CodeNotFound, IsNotFound},
		{CodeConflict, IsConflict},
		{CodeTenantMismatch, IsTenantMismatch},
		{CodeApplicationMismatch, IsApplicationMismatch},
		{CodeInactiveTag, IsInactiveTag},
		{CodeNamespaceNotSupported, IsNamespaceNotSupported},
		{CodeNamespaceInvalid, IsNamespaceInvalid},
		{CodeNamespacedWritesDisabled, IsNamespacedWritesDisabled},
		{CodeNamespaceAPIDisabled, IsNamespaceAPIDisabled},
		{CodeAmbiguousResolution, IsAmbiguousResolution},
		{CodeUnexpectedStatus, IsUnexpectedStatus},
	}
	for _, subject := range helpers {
		t.Run(subject.code, func(t *testing.T) {
			err := error(&APIError{StatusCode: 400, Code: subject.code, Message: "boom"})
			if !subject.fn(err) {
				t.Errorf("%s helper did not match its own code", subject.code)
			}
			for _, other := range helpers {
				if other.code == subject.code {
					continue
				}
				if other.fn(err) {
					t.Errorf("%s helper also matched %s", other.code, subject.code)
				}
			}
			if subject.fn(errors.New("not an api error")) {
				t.Errorf("%s helper matched a non-APIError", subject.code)
			}
			if subject.fn(nil) {
				t.Errorf("%s helper matched nil", subject.code)
			}
		})
	}
}

// --- Bounded response reads ----------------------------------------------

func TestReadBounded(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		limit   int64
		wantErr bool
	}{
		{"under the limit", "1234567", 8, false},
		{"exactly at the limit", "12345678", 8, false},
		{"one byte over", "123456789", 8, true},
		{"empty", "", 8, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := readBounded(strings.NewReader(tt.body), tt.limit)
			if tt.wantErr {
				if !errors.Is(err, ErrResponseTooLarge) {
					t.Fatalf("err = %v, want ErrResponseTooLarge", err)
				}
				// Truncated content must never be returned: it would reach the
				// decoders as invalid JSON and report the wrong problem.
				if got != nil {
					t.Errorf("body = %q, want nil alongside the error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("readBounded: %v", err)
			}
			if string(got) != tt.body {
				t.Errorf("body = %q, want %q", got, tt.body)
			}
		})
	}
}

// ...and the ceiling is wired at the transport chokepoint, so it covers every
// method on the client rather than the ones that remembered to ask.
func TestDoRaw_BoundsTheResponseBody(t *testing.T) {
	c := newVersionedTestClient(t, APIV2, func(w http.ResponseWriter, _ *http.Request) {
		writeData(t, w, http.StatusOK, Tag{ID: "abc", Name: strings.Repeat("x", 512)})
	})
	c.maxResponseBytes = 32

	_, err := c.Tags.Get(context.Background(), "abc")
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("err = %v, want ErrResponseTooLarge", err)
	}
	if !strings.Contains(err.Error(), "octonomy:") {
		t.Errorf("error lost the package prefix: %v", err)
	}
}

// The known limit stated on versionHint, pinned so it is documented behavior
// rather than a surprise: a server WITH the version shim that rejects the
// requested version answers through DRF, with an enveloped not_found. The SDK
// preserves an envelope code verbatim, so this one does satisfy IsNotFound.
//
// The SDK never requests a version outside {v1, v2} -- Config.APIVersion is
// validated in New -- so it cannot reach this. The test exists because the
// alternative to stating the limit is implying there isn't one.
func TestParseError_EnvelopedVersionRejectIsStillANotFound(t *testing.T) {
	c := newVersionedTestClient(t, APIV2, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusNotFound, map[string]any{
			"error": map[string]any{
				"code":    CodeNotFound,
				"message": "Resource not found.",
				"details": map[string]any{"detail": "Invalid version in URL path."},
			},
		})
	})

	_, err := c.Tags.Get(context.Background(), "abc")
	if !IsNotFound(err) {
		t.Fatalf("an enveloped not_found must stay a not_found whatever caused it: %v", err)
	}
	if IsUnexpectedStatus(err) {
		t.Error("IsUnexpectedStatus reported true for an enveloped response")
	}
}

// A non-2xx whose body cannot be read is still a non-2xx.
//
// Codex review of #7 caught this: the read-error branch returned before
// parseError, so an oversized error body dropped the response out of *APIError
// entirely. A caller branching on IsUnexpectedStatus or reading StatusCode would
// see nothing -- silently, and only for the failures big enough to trip the
// limit, which is the worst possible distribution for a bug.
func TestDoRaw_OversizedNon2xxKeepsItsAPIErrorClassification(t *testing.T) {
	c := newVersionedTestClient(t, APIV2, func(w http.ResponseWriter, _ *http.Request) {
		// A proxy dumping a large HTML error page over a real 503.
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(strings.Repeat("x", 512)))
	})
	c.maxResponseBytes = 32

	_, err := c.Tags.Get(context.Background(), "abc")
	if err == nil {
		t.Fatal("expected an error")
	}
	apiErr, ok := AsAPIError(err)
	if !ok {
		t.Fatalf("a non-2xx must stay an *APIError even when its body is unreadable: %v", err)
	}
	if apiErr.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("StatusCode = %d, want 503", apiErr.StatusCode)
	}
	if apiErr.Code != CodeUnexpectedStatus {
		t.Errorf("Code = %q, want %q: no envelope was read, so no semantic code was established", apiErr.Code, CodeUnexpectedStatus)
	}
	if !IsUnexpectedStatus(err) {
		t.Error("IsUnexpectedStatus = false")
	}
	// The cause survives the wrap, so a caller can still tell "too big" from
	// "connection died mid-body".
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Errorf("errors.Is(err, ErrResponseTooLarge) = false: %v", err)
	}
	if !strings.Contains(apiErr.Message, "could not be read") {
		t.Errorf("Message should say why the body is missing: %q", apiErr.Message)
	}
}

// The 2xx side is deliberately NOT symmetric: a success status with an unusable
// payload has no classification worth preserving, so it stays a plain read
// error rather than being dressed up as an API error.
func TestDoRaw_Oversized2xxStaysAPlainReadError(t *testing.T) {
	c := newVersionedTestClient(t, APIV2, func(w http.ResponseWriter, _ *http.Request) {
		writeData(t, w, http.StatusOK, Tag{ID: "abc", Name: strings.Repeat("x", 512)})
	})
	c.maxResponseBytes = 32

	_, err := c.Tags.Get(context.Background(), "abc")
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("err = %v, want ErrResponseTooLarge", err)
	}
	if apiErr, ok := AsAPIError(err); ok {
		t.Errorf("a 2xx read failure became an *APIError (%v); there is no status classification to preserve", apiErr)
	}
}
