package octonomy

// Ported from main's errors_test.go at 5e40964 for #91. What changed in the port,
// besides the dialect:
//
//   - The envelope-less 404 rule is asserted on BOTH API versions. On main it
//     was only ever a v2 question; here v1 is the default surface and is where
//     v1.0.0's codeFromStatus actually ran, so v1 is the case that matters most.
//   - TestIsScopeImmutable walks the two PATCH routes this tree has, and both
//     surfaces; main's also covers tag aliases, which are not ported yet.

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// --- The envelope-less non-2xx rule --------------------------------------
//
// This is a REGRESSION suite, not a feature suite. This line's first release
// mapped an envelope-less non-2xx by status (codeFromStatus), so a bare 404 --
// a wrong BaseURL, a path-stripping proxy, or a server with no /api/v2 route --
// became CodeNotFound, IsNotFound(err) reported true, and a caller's ordinary "that tag
// doesn't exist" branch saw an empty taxonomy and no error at all.
//
// v1.0.0's own suite could not catch it: its one envelope-less test used a 502
// and asserted StatusCode and Message but never Code, so the semantic mapping
// was unobserved. Every test below asserts Code.

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

// The acceptance table for removing codeFromStatus: on EITHER surface, a bare
// 404 is not an Octonomy not_found, and an enveloped one still is.
func TestParseError_Bare404IsNotANotFoundOnEitherSurface(t *testing.T) {
	tests := []struct {
		name         string
		version      APIVersion
		enveloped    bool
		wantNotFound bool
		wantCode     string
	}{
		{"bare 404 on the default (v1)", "", false, false, CodeUnexpectedStatus},
		{"bare 404 on v1", APIV1, false, false, CodeUnexpectedStatus},
		{"bare 404 on v2", APIV2, false, false, CodeUnexpectedStatus},
		{"enveloped not_found on the default (v1)", "", true, true, CodeNotFound},
		{"enveloped not_found on v1", APIV1, true, true, CodeNotFound},
		{"enveloped not_found on v2", APIV2, true, true, CodeNotFound},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			c, cleanup := newVersionedTestClient(t, tt.version, func(w http.ResponseWriter, _ *http.Request) {
				if tt.enveloped {
					writeJSON(t, w, http.StatusNotFound, map[string]interface{}{
						"error": map[string]interface{}{"code": CodeNotFound, "message": "Resource not found."},
					})
					return
				}
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(djangoUnroutedBody))
			})
			defer cleanup()

			_, err := c.Tags.Get(context.Background(), "abc")
			if err == nil {
				t.Fatal("expected an error")
			}
			if got := IsNotFound(err); got != tt.wantNotFound {
				t.Errorf("IsNotFound = %v, want %v: %v", got, tt.wantNotFound, err)
			}
			if got := IsUnexpectedStatus(err); got == tt.enveloped {
				t.Errorf("IsUnexpectedStatus = %v, want %v: %v", got, !tt.enveloped, err)
			}
			apiErr, ok := AsAPIError(err)
			if !ok {
				t.Fatalf("error is not *APIError: %v", err)
			}
			if apiErr.Code != tt.wantCode {
				t.Errorf("Code = %q, want %q", apiErr.Code, tt.wantCode)
			}
			if apiErr.StatusCode != http.StatusNotFound {
				t.Errorf("StatusCode = %d, want 404", apiErr.StatusCode)
			}
			// The raw body survives -- it is the only diagnostic a non-Octonomy
			// 404 has.
			if !tt.enveloped && !strings.Contains(apiErr.Message, "Not Found") {
				t.Errorf("Message dropped the raw body: %q", apiErr.Message)
			}
		})
	}
}

// The status-to-code mapping is gone for EVERY status it used to cover, not
// only 404: a gateway's bare 400, 401, 403, or 409 is no more Octonomy's
// validation_error, authentication_required, forbidden, or conflict than a bare
// 404 is its not_found.
func TestParseError_NoEnvelopeMeansNoSemanticCode(t *testing.T) {
	statuses := []struct {
		status int
		was    func(error) bool
	}{
		{http.StatusBadRequest, IsValidation},
		{http.StatusUnauthorized, IsAuthError},
		{http.StatusForbidden, IsForbidden},
		{http.StatusNotFound, IsNotFound},
		{http.StatusConflict, IsConflict},
	}
	for _, version := range []APIVersion{APIV1, APIV2} {
		for _, s := range statuses {
			version, s := version, s
			t.Run(string(version)+"/"+http.StatusText(s.status), func(t *testing.T) {
				c, cleanup := newVersionedTestClient(t, version, func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(s.status)
					_, _ = w.Write([]byte("from a proxy"))
				})
				defer cleanup()

				_, err := c.Tags.Get(context.Background(), "abc")
				if s.was(err) {
					t.Errorf("a bare %d still satisfies its old semantic helper: %v", s.status, err)
				}
				if !IsUnexpectedStatus(err) {
					t.Errorf("IsUnexpectedStatus = false for a bare %d: %v", s.status, err)
				}
			})
		}
	}
}

// The v2 hint names the one cause the SDK can act on, and only where it applies.
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
		{"envelope-less 404 on the default", "", http.StatusNotFound, false, false},
		{"envelope-less 502 on v2", APIV2, http.StatusBadGateway, false, false},
		{"enveloped 404 on v2", APIV2, http.StatusNotFound, true, false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			c, cleanup := newVersionedTestClient(t, tt.version, func(w http.ResponseWriter, _ *http.Request) {
				if tt.envelope {
					writeJSON(t, w, tt.status, map[string]interface{}{
						"error": map[string]interface{}{"code": CodeNotFound, "message": "Resource not found."},
					})
					return
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte("boom"))
			})
			defer cleanup()

			_, err := c.Tags.Get(context.Background(), "abc")
			if err == nil {
				t.Fatal("expected an error")
			}
			// The cutoff is a fact about the SERVER's history: /api/v2 shipped
			// in server 2.0. Naming a later release would tell every operator in
			// between to disable a namespace surface their server has.
			if strings.Contains(err.Error(), "predates Octonomy") &&
				!strings.Contains(err.Error(), "predates Octonomy 2.0") {
				t.Errorf("version hint names the wrong cutoff; /api/v2 shipped in server 2.0: %v", err)
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

// A 503 from Octonomy's rollback gate and a 503 from a load balancer are
// different events with the same status. Only the envelope separates them, which
// is exactly what a status-derived mapping throws away.
func TestParseError_Enveloped503IsDistinguishableFromABare503(t *testing.T) {
	t.Run("enveloped: the namespace rollback gate", func(t *testing.T) {
		c, cleanup := newVersionedTestClient(t, APIV2, func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, http.StatusServiceUnavailable, map[string]interface{}{
				"error": map[string]interface{}{
					"code":    CodeNamespaceAPIDisabled,
					"message": "The namespaced v2 API is not enabled on this deployment.",
				},
			})
		})
		defer cleanup()
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
		c, cleanup := newVersionedTestClient(t, APIV2, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("no healthy upstream"))
		})
		defer cleanup()
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
// flattened, and a server ahead of this SDK will send others.
func TestParseError_PreservesAnUnknownEnvelopeCode(t *testing.T) {
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusConflict, map[string]interface{}{
			"error": map[string]interface{}{"code": "some_future_code", "message": "nope"},
		})
	})
	defer cleanup()

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

// The known limit stated on versionHint, pinned so it is documented behavior
// rather than a surprise: a server WITH the version shim that rejects the
// requested version answers through DRF, with an enveloped not_found. The SDK
// preserves an envelope code verbatim, so this one does satisfy IsNotFound.
func TestParseError_EnvelopedVersionRejectIsStillANotFound(t *testing.T) {
	c, cleanup := newVersionedTestClient(t, APIV2, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusNotFound, map[string]interface{}{
			"error": map[string]interface{}{
				"code":    CodeNotFound,
				"message": "Resource not found.",
				"details": map[string]interface{}{"detail": "Invalid version in URL path."},
			},
		})
	})
	defer cleanup()

	_, err := c.Tags.Get(context.Background(), "abc")
	if !IsNotFound(err) {
		t.Fatalf("an enveloped not_found must stay a not_found whatever caused it: %v", err)
	}
	if IsUnexpectedStatus(err) {
		t.Error("IsUnexpectedStatus reported true for an enveloped response")
	}
}

// Every Is* helper matches its own code and nothing else.
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
		{CodeScopeImmutable, IsScopeImmutable},
		{CodeNamespaceNotSupported, IsNamespaceNotSupported},
		{CodeNamespaceInvalid, IsNamespaceInvalid},
		{CodeNamespacedWritesDisabled, IsNamespacedWritesDisabled},
		{CodeNamespaceAPIDisabled, IsNamespaceAPIDisabled},
		{CodeAmbiguousResolution, IsAmbiguousResolution},
		{CodeUnexpectedStatus, IsUnexpectedStatus},
		{CodeNotReady, IsNotReady},
	}
	if len(helpers) != 16 {
		t.Fatalf("%d helpers in the table, want all 16 codes", len(helpers))
	}
	for _, subject := range helpers {
		subject := subject
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
		tt := tt
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

// countingReader is an endless source that records how much was taken from it.
type countingReader struct{ n int64 }

func (r *countingReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	r.n += int64(len(p))
	return len(p), nil
}

// The ceiling bounds what is READ, not only what is returned. Reading the whole
// body and then comparing its length would report ErrResponseTooLarge just the
// same -- after holding every byte of it in memory, which is the failure the
// ceiling exists to prevent. Against an endless body that read never returns.
func TestReadBounded_StopsReadingAtTheCeiling(t *testing.T) {
	src := &io.LimitedReader{R: &countingReader{}, N: 1 << 20}
	if _, err := readBounded(src, 8); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("err = %v, want ErrResponseTooLarge", err)
	}
	if read := (1 << 20) - src.N; read > 8+512 {
		t.Errorf("readBounded consumed %d bytes of a 1 MiB body with an 8-byte ceiling; it must stop just past the ceiling", read)
	}
}

// The ceiling is the documented 32 MiB, and New wires it into every client.
func TestNew_WiresTheResponseCeiling(t *testing.T) {
	if maxResponseBytes != 32*1024*1024 {
		t.Errorf("maxResponseBytes = %d, want 32 MiB", maxResponseBytes)
	}
	c, err := New(Config{BaseURL: "https://octonomy.example.com", Token: "t", TenantID: "acme"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.maxResponseBytes != maxResponseBytes {
		t.Errorf("client ceiling = %d, want %d", c.maxResponseBytes, maxResponseBytes)
	}
	hc, err := NewHealthClient("https://octonomy.example.com")
	if err != nil {
		t.Fatalf("NewHealthClient: %v", err)
	}
	if hc.Health.client.maxResponseBytes != maxResponseBytes {
		t.Errorf("health client ceiling = %d, want %d", hc.Health.client.maxResponseBytes, maxResponseBytes)
	}
}

// ...and the ceiling is wired at the transport chokepoint, so it covers every
// method on the client rather than the ones that remembered to ask.
func TestDoRaw_BoundsTheResponseBody(t *testing.T) {
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeData(t, w, http.StatusOK, Tag{ID: "abc", Name: strings.Repeat("x", 512)})
	})
	defer cleanup()
	c.maxResponseBytes = 32

	_, err := c.Tags.Get(context.Background(), "abc")
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("err = %v, want ErrResponseTooLarge", err)
	}
	if !strings.Contains(err.Error(), "octonomy:") {
		t.Errorf("error lost the package prefix: %v", err)
	}

	// The list path shares the chokepoint.
	if _, err := c.Tags.List(context.Background(), nil); !errors.Is(err, ErrResponseTooLarge) {
		t.Errorf("List err = %v, want ErrResponseTooLarge", err)
	}
}

// A non-2xx whose body cannot be read is still a non-2xx. Returning before
// parseError would drop an oversized error body out of *APIError entirely -- a
// caller branching on IsUnexpectedStatus or reading StatusCode would see
// nothing, silently, and only for the failures big enough to trip the limit,
// which is the worst possible distribution for a bug.
func TestDoRaw_OversizedNon2xxKeepsItsAPIErrorClassification(t *testing.T) {
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		// A proxy dumping a large HTML error page over a real 503.
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(strings.Repeat("x", 512)))
	})
	defer cleanup()
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
	c, cleanup := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		writeData(t, w, http.StatusOK, Tag{ID: "abc", Name: strings.Repeat("x", 512)})
	})
	defer cleanup()
	c.maxResponseBytes = 32

	_, err := c.Tags.Get(context.Background(), "abc")
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("err = %v, want ErrResponseTooLarge", err)
	}
	if apiErr, ok := AsAPIError(err); ok {
		t.Errorf("a 2xx read failure became an *APIError (%v); there is no status classification to preserve", apiErr)
	}
}

// --- scope_immutable ------------------------------------------------------

// The contract documents 409 scope_immutable on the detail PATCH of tags,
// vocabularies and tag aliases, on both surfaces, so the test walks six call
// sites, and pins the three properties a caller depends on: the helper matches,
// the code survives, and the 409 does NOT read as a plain conflict. The server
// raises scope_immutable as a subclass of its conflict error, so the status
// alone says "conflict" while the code does not.
func TestIsScopeImmutable_OnEveryDocumentedPatch(t *testing.T) {
	patches := []struct {
		name string
		call func(*Client) error
	}{
		{"tags", func(c *Client) error {
			_, err := c.Tags.Update(context.Background(), "tag_1", TagUpdate{ApplicationID: String("other")})
			return err
		}},
		{"vocabularies", func(c *Client) error {
			_, err := c.Vocabularies.Update(context.Background(), "voc_1", VocabularyUpdate{ApplicationID: String("other")})
			return err
		}},
		{"tag aliases", func(c *Client) error {
			_, err := c.Aliases.Update(context.Background(), "alias_1", TagAliasUpdate{ApplicationID: String("other")})
			return err
		}},
	}
	for _, version := range []APIVersion{APIV1, APIV2} {
		for _, patch := range patches {
			version, patch := version, patch
			t.Run(string(version)+"/"+patch.name, func(t *testing.T) {
				c, cleanup := newVersionedTestClient(t, version, func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodPatch {
						t.Errorf("method = %s, want PATCH", r.Method)
					}
					writeJSON(t, w, http.StatusConflict, map[string]interface{}{
						"error": map[string]interface{}{
							"code":    CodeScopeImmutable,
							"message": "Scope fields cannot be changed after creation.",
							"details": map[string]interface{}{"application_id": "Scope is immutable."},
						},
					})
				})
				defer cleanup()

				err := patch.call(c)
				if !IsScopeImmutable(err) {
					t.Fatalf("IsScopeImmutable = false: %v", err)
				}
				if IsConflict(err) {
					t.Error("IsConflict = true for scope_immutable; a caller would retry a PATCH that can never succeed")
				}
				apiErr, _ := AsAPIError(err)
				if apiErr.StatusCode != http.StatusConflict {
					t.Errorf("StatusCode = %d, want 409", apiErr.StatusCode)
				}
				if _, ok := apiErr.Details["application_id"]; !ok {
					t.Errorf("Details lost the offending field: %v", apiErr.Details)
				}
			})
		}
	}
}
