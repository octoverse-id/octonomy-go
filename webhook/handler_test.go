package webhook

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The secrets these tests sign with. handlerSecret is the one a deployment
// holds; retiringSecret is the one it is rotating away from.
const (
	handlerSecret  = "whsec_3f8a1c0d9b2e4f6a8c1d3e5f7a9b0c2d"
	retiringSecret = "whsec_0a1b2c3d4e5f60718293a4b5c6d7e8f90"
)

// tagCreatedBody is one genuine delivery, built from the same envelope helper
// the event tests use.
func tagCreatedBody() string {
	return envelope(EventTagCreated, AggregateTag, `{"after":`+tagSnapshotJSON+`}`)
}

// refusal is what an error handler recorded: everything a consumer's
// observability hook is given.
type refusal struct {
	status int
	err    error
}

// newHandler builds a handler and records what it refuses. The returned slice
// is appended to on the request's goroutine, which is the same goroutine the
// test drives it from -- these tests never serve concurrently.
func newHandler(t *testing.T, fn EventHandler, opts ...HandlerOption) (http.Handler, *[]refusal) {
	t.Helper()
	var refusals []refusal
	opts = append(opts, WithErrorHandler(func(_ *http.Request, status int, err error) {
		refusals = append(refusals, refusal{status: status, err: err})
	}))
	h, err := Handler(handlerSecret, fn, opts...)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	return h, &refusals
}

// deliver posts body with the signature the caller names and returns the
// recorder. A signature of "" sends no header at all.
func deliver(h http.Handler, method, body, signature string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "/webhooks/octonomy", strings.NewReader(body))
	if signature != "" {
		request.Header.Set(HeaderSignature, signature)
	}
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, request)
	return recorder
}

// accept is an EventHandler that acknowledges everything and records what it
// was given.
func accept(seen *[]*Event) EventHandler {
	return func(_ context.Context, event *Event) error {
		*seen = append(*seen, event)
		return nil
	}
}

// TestHandlerRefusesToBeBuiltWithoutWhatItNeeds is why Handler returns an
// error. Every one of these is a deployment mistake that is knowable where the
// handler is wired, and the alternative is an endpoint that answers 500 forever
// with the reason reachable only through an optional hook.
func TestHandlerRefusesToBeBuiltWithoutWhatItNeeds(t *testing.T) {
	noop := func(context.Context, *Event) error { return nil }

	t.Run("no secret", func(t *testing.T) {
		// The realistic case: an unset OCTONOMY_WEBHOOK_SIGNING_SECRET reaching
		// this constructor as "". HMAC under an empty key is well defined,
		// which is exactly the danger -- every forged delivery would verify.
		h, err := Handler("", noop)
		if !errors.Is(err, ErrNoSecret) {
			t.Fatalf("err = %v, want ErrNoSecret", err)
		}
		if h != nil {
			t.Error("a refused construction must return no handler")
		}
	})

	t.Run("blank additional secret", func(t *testing.T) {
		_, err := Handler(handlerSecret, noop, WithAdditionalSecrets(retiringSecret, ""))
		if !errors.Is(err, ErrNoSecret) {
			t.Fatalf("err = %v, want ErrNoSecret", err)
		}
		// Named by position, so a caller passing several knows which one is the
		// unset variable.
		if !strings.Contains(err.Error(), "secret 3 of 3") {
			t.Errorf("err = %q does not say which secret is empty", err)
		}
	})

	t.Run("nil event handler", func(t *testing.T) {
		_, err := Handler(handlerSecret, nil)
		if !errors.Is(err, ErrNoEventHandler) {
			t.Fatalf("err = %v, want ErrNoEventHandler", err)
		}
	})

	t.Run("nil option", func(t *testing.T) {
		// A nil option would be called and panic, out of a library that
		// promises never to. It is a construction error instead, and it says
		// which one of the slice it was.
		_, err := Handler(handlerSecret, noop, WithMaxBodyBytes(4096), nil)
		if err == nil {
			t.Fatal("a nil HandlerOption was accepted")
		}
		if !strings.Contains(err.Error(), "option 2 of 2") {
			t.Errorf("err = %q does not say which option is nil", err)
		}
	})

	t.Run("non-positive body limit", func(t *testing.T) {
		for _, limit := range []int64{0, -1} {
			if _, err := Handler(handlerSecret, noop, WithMaxBodyBytes(limit)); err == nil {
				t.Errorf("WithMaxBodyBytes(%d) was accepted; it refuses every delivery", limit)
			}
		}
	})
}

// TestHandlerAcceptsAGenuineDelivery is the whole path: bound, read, verify,
// parse, dispatch, acknowledge.
func TestHandlerAcceptsAGenuineDelivery(t *testing.T) {
	var seen []*Event
	h, refusals := newHandler(t, accept(&seen))

	body := tagCreatedBody()
	recorder := deliver(h, http.MethodPost, body, sign(handlerSecret, []byte(body)))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body)
	}
	if len(*refusals) != 0 {
		t.Errorf("an accepted delivery reported %d refusals", len(*refusals))
	}
	if len(seen) != 1 {
		t.Fatalf("the event handler saw %d events, want 1", len(seen))
	}
	event := seen[0]
	if event.Type != EventTagCreated {
		t.Errorf("Type = %q", event.Type)
	}
	if event.Tag == nil || event.Tag.After == nil {
		t.Fatal("the typed payload did not reach the event handler")
	}
	if slug, ok := event.Tag.After.Slug.Get(); !ok || slug != "summer-sale" {
		t.Errorf("slug = %q (present %t)", slug, ok)
	}
}

// TestHandlerRefusesEveryDeliveryItCannotVerify is the property the package
// exists for, through the handler: nothing reaches the EventHandler until the
// signature has checked out.
func TestHandlerRefusesEveryDeliveryItCannotVerify(t *testing.T) {
	body := tagCreatedBody()
	tampered := strings.Replace(body, `"tenant_id":"acme"`, `"tenant_id":"evil"`, 1)

	cases := []struct {
		name      string
		body      string
		signature string
		want      error
	}{
		{
			name: "no signature at all",
			body: body, signature: "",
			want: ErrMissingSignature,
		},
		{
			name: "signature of a different body",
			body: tampered, signature: sign(handlerSecret, []byte(body)),
			want: ErrSignatureMismatch,
		},
		{
			name: "signed with the wrong secret",
			body: body, signature: sign("whsec_not_the_deployment_secret", []byte(body)),
			want: ErrSignatureMismatch,
		},
		{
			name: "bare hex digest",
			body: body, signature: strings.TrimPrefix(sign(handlerSecret, []byte(body)), signaturePrefix),
			want: ErrUnsupportedAlgorithm,
		},
		{
			name: "truncated digest",
			body: body, signature: signaturePrefix + "abc123",
			want: ErrMalformedSignature,
		},
		{
			name: "empty body",
			body: "", signature: sign(handlerSecret, nil),
			want: ErrEmptyBody,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var seen []*Event
			h, refusals := newHandler(t, accept(&seen))

			recorder := deliver(h, http.MethodPost, tc.body, tc.signature)

			if recorder.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", recorder.Code)
			}
			if len(seen) != 0 {
				t.Error("an unverified delivery reached the event handler")
			}
			if len(*refusals) != 1 {
				t.Fatalf("recorded %d refusals, want 1", len(*refusals))
			}
			if !errors.Is((*refusals)[0].err, tc.want) {
				t.Errorf("err = %v, want %v", (*refusals)[0].err, tc.want)
			}
			if (*refusals)[0].status != http.StatusUnauthorized {
				t.Errorf("reported status = %d, want 401", (*refusals)[0].status)
			}
		})
	}
}

// TestHandlerVerifiesBeforeItParses pins the order. A handler that parsed first
// would answer 400 for a forged delivery whose JSON is broken, telling an
// attacker their body was read before it was authenticated.
func TestHandlerVerifiesBeforeItParses(t *testing.T) {
	var seen []*Event
	h, refusals := newHandler(t, accept(&seen))

	const broken = `{"id":"x",`
	recorder := deliver(h, http.MethodPost, broken, signaturePrefix+strings.Repeat("0", 64))

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: a body is parsed only after it verifies", recorder.Code)
	}
	if !errors.Is((*refusals)[0].err, ErrSignatureMismatch) {
		t.Errorf("err = %v, want ErrSignatureMismatch", (*refusals)[0].err)
	}

	// The same broken body, correctly signed, is the 400: the refusal moves to
	// the parse step only once the signature is genuine.
	h, refusals = newHandler(t, accept(&seen))
	recorder = deliver(h, http.MethodPost, broken, sign(handlerSecret, []byte(broken)))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
	if !errors.Is((*refusals)[0].err, ErrMalformedEvent) {
		t.Errorf("err = %v, want ErrMalformedEvent", (*refusals)[0].err)
	}
	if len(seen) != 0 {
		t.Error("a body that does not decode reached the event handler")
	}
}

// TestHandlerAnswersTheDocumentedStatuses walks the table in Handler's doc
// comment. The statuses are a contract with the sender: every non-2xx is a
// retry, and the one 2xx is the acknowledgement that ends the event's life.
func TestHandlerAnswersTheDocumentedStatuses(t *testing.T) {
	body := tagCreatedBody()

	t.Run("405 on a non-POST", func(t *testing.T) {
		var seen []*Event
		h, refusals := newHandler(t, accept(&seen))
		for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
			recorder := deliver(h, method, body, sign(handlerSecret, []byte(body)))
			if recorder.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s: status = %d, want 405", method, recorder.Code)
			}
			if got := recorder.Header().Get("Allow"); got != http.MethodPost {
				t.Errorf("%s: Allow = %q, want POST", method, got)
			}
		}
		if len(seen) != 0 {
			t.Error("a non-POST reached the event handler")
		}
		for _, r := range *refusals {
			if !errors.Is(r.err, ErrMethodNotAllowed) {
				t.Errorf("err = %v, want ErrMethodNotAllowed", r.err)
			}
		}
	})

	t.Run("413 over the ceiling", func(t *testing.T) {
		var seen []*Event
		h, refusals := newHandler(t, accept(&seen), WithMaxBodyBytes(64))

		recorder := deliver(h, http.MethodPost, body, sign(handlerSecret, []byte(body)))
		if recorder.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want 413", recorder.Code)
		}
		if len(seen) != 0 {
			t.Error("an oversized delivery reached the event handler")
		}
		if !errors.Is((*refusals)[0].err, ErrBodyTooLarge) {
			t.Fatalf("err = %v, want ErrBodyTooLarge", (*refusals)[0].err)
		}
		// The ceiling is in the message, because the fix is to raise it.
		if !strings.Contains((*refusals)[0].err.Error(), "64") {
			t.Errorf("err = %q does not name the limit", (*refusals)[0].err)
		}
	})

	t.Run("400 on an unreadable body", func(t *testing.T) {
		var seen []*Event
		h, refusals := newHandler(t, accept(&seen))

		request := httptest.NewRequest(http.MethodPost, "/webhooks/octonomy", io.NopCloser(errorReader{}))
		request.Header.Set(HeaderSignature, sign(handlerSecret, []byte(body)))
		recorder := httptest.NewRecorder()
		h.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", recorder.Code)
		}
		if !errors.Is((*refusals)[0].err, ErrUnreadableBody) {
			t.Errorf("err = %v, want ErrUnreadableBody", (*refusals)[0].err)
		}
		if len(seen) != 0 {
			t.Error("a body that could not be read reached the event handler")
		}
	})

	t.Run("400 on a signed body that is not an event", func(t *testing.T) {
		var seen []*Event
		h, refusals := newHandler(t, accept(&seen))

		// A perfectly genuine delivery whose envelope has no id. It must NOT be
		// acknowledged: any 2xx marks it published and it is gone.
		blank := strings.Replace(body, `"id":"0f1d7b24-2c1e-4f9a-9f3a-3a5f1c2d6e77",`, `"id":"",`, 1)
		recorder := deliver(h, http.MethodPost, blank, sign(handlerSecret, []byte(blank)))

		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", recorder.Code)
		}
		if !errors.Is((*refusals)[0].err, ErrIncompleteEvent) {
			t.Errorf("err = %v, want ErrIncompleteEvent", (*refusals)[0].err)
		}
		if len(seen) != 0 {
			t.Error("an incomplete envelope reached the event handler")
		}
	})

	t.Run("500 when the event handler refuses", func(t *testing.T) {
		boom := errors.New("the index is down")
		h, refusals := newHandler(t, func(context.Context, *Event) error { return boom })

		recorder := deliver(h, http.MethodPost, body, sign(handlerSecret, []byte(body)))
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", recorder.Code)
		}
		if !errors.Is((*refusals)[0].err, boom) {
			t.Errorf("err = %v, want the event handler's own error", (*refusals)[0].err)
		}
		// The sender is not the operator: the consumer's error goes to the
		// error handler and never into the response.
		if strings.Contains(recorder.Body.String(), boom.Error()) {
			t.Errorf("response body %q echoes the event handler's error", recorder.Body)
		}
	})
}

// TestHandlerAcknowledgesAnUnknownEventType is the forward-compatibility
// contract at the HTTP layer. The default branch of a consumer's switch returns
// nil, and the delivery is acknowledged rather than retried and dead-lettered.
func TestHandlerAcknowledgesAnUnknownEventType(t *testing.T) {
	var seen []*Event
	h, refusals := newHandler(t, accept(&seen))

	body := envelope("tag.merged", AggregateTag, `{"source_tag_id":"a","target_tag_id":"b"}`)
	recorder := deliver(h, http.MethodPost, body, sign(handlerSecret, []byte(body)))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: an unknown event type must be acknowledged", recorder.Code)
	}
	if len(*refusals) != 0 {
		t.Errorf("an unknown event type was reported as a refusal: %v", *refusals)
	}
	if len(seen) != 1 {
		t.Fatalf("the event handler saw %d events, want 1", len(seen))
	}
	// It is visible without being fatal: the raw type and the undecoded payload
	// both reach the consumer.
	if seen[0].Known() {
		t.Error("tag.merged must report Known() == false")
	}
	if seen[0].Type != "tag.merged" {
		t.Errorf("Type = %q", seen[0].Type)
	}
	if !strings.Contains(string(seen[0].Payload), "target_tag_id") {
		t.Errorf("Payload = %s", seen[0].Payload)
	}
}

// TestHandlerRecoversAPanickingEventHandler pins the decision #22 asked for.
// Left to escape, the panic is recovered by net/http per connection with NO
// response written -- so the sender records a transport failure and the
// consumer's own error path never sees it.
func TestHandlerRecoversAPanickingEventHandler(t *testing.T) {
	h, refusals := newHandler(t, func(context.Context, *Event) error {
		panic("index client was nil")
	})

	body := tagCreatedBody()
	recorder := deliver(h, http.MethodPost, body, sign(handlerSecret, []byte(body)))

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}
	if len(*refusals) != 1 {
		t.Fatalf("recorded %d refusals, want 1", len(*refusals))
	}
	err := (*refusals)[0].err
	if !errors.Is(err, ErrHandlerPanic) {
		t.Fatalf("err = %v, want ErrHandlerPanic", err)
	}
	if !strings.Contains(err.Error(), "index client was nil") {
		t.Errorf("err = %q does not carry the panic value", err)
	}
	// The stack is the only copy: net/http would have logged one, and this
	// package does not log.
	if !strings.Contains(err.Error(), "webhook.(*handler).invoke") {
		t.Errorf("err = %q does not carry a stack", err)
	}
}

// TestHandlerWrapsAPanickedError keeps the diagnosis reachable. panic(err) is
// the common spelling, and an error panicked with is still an error: collapsing
// it to text would leave an error handler able to see THAT something panicked
// and never what.
func TestHandlerWrapsAPanickedError(t *testing.T) {
	cause := errors.New("the index client was nil")
	h, refusals := newHandler(t, func(context.Context, *Event) error {
		panic(cause)
	})

	body := tagCreatedBody()
	if code := deliver(h, http.MethodPost, body, sign(handlerSecret, []byte(body))).Code; code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", code)
	}
	err := (*refusals)[0].err
	if !errors.Is(err, ErrHandlerPanic) {
		t.Errorf("err = %v, want ErrHandlerPanic", err)
	}
	if !errors.Is(err, cause) {
		t.Errorf("err = %v does not wrap the panicked error", err)
	}
}

// TestHandlerPropagatesErrAbortHandler keeps net/http's own escape hatch
// working. ErrAbortHandler is the documented way to abandon a connection
// deliberately, and swallowing it would silently disable something the consumer
// reached for on purpose.
func TestHandlerPropagatesErrAbortHandler(t *testing.T) {
	h, refusals := newHandler(t, func(context.Context, *Event) error {
		panic(http.ErrAbortHandler)
	})

	body := tagCreatedBody()
	defer func() {
		recovered := recover()
		if recovered != http.ErrAbortHandler {
			t.Errorf("recovered %v, want http.ErrAbortHandler", recovered)
		}
		if len(*refusals) != 0 {
			t.Errorf("an aborted connection was reported as a refusal: %v", *refusals)
		}
	}()
	deliver(h, http.MethodPost, body, sign(handlerSecret, []byte(body)))
	t.Fatal("ServeHTTP returned; http.ErrAbortHandler must reach net/http")
}

// TestHandlerAcceptsEitherSecretDuringRotation covers the window in which the
// server has been reconfigured but some dispatchers still sign with the old
// secret. Without this the handler is unusable for the length of a rotation,
// which is how a consumer ends up hand-rolling the thing again.
func TestHandlerAcceptsEitherSecretDuringRotation(t *testing.T) {
	var seen []*Event
	h, refusals := newHandler(t, accept(&seen), WithAdditionalSecrets(retiringSecret))

	body := tagCreatedBody()
	for name, signature := range map[string]string{
		"incoming secret": sign(handlerSecret, []byte(body)),
		"retiring secret": sign(retiringSecret, []byte(body)),
	} {
		if code := deliver(h, http.MethodPost, body, signature).Code; code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", name, code)
		}
	}
	if len(seen) != 2 {
		t.Errorf("the event handler saw %d events, want 2", len(seen))
	}

	// A third secret is still refused, and a malformed header still reports
	// what is actually wrong rather than the last secret's mismatch.
	if code := deliver(h, http.MethodPost, body, sign("whsec_neither", []byte(body))).Code; code != http.StatusUnauthorized {
		t.Errorf("an unknown secret: status = %d, want 401", code)
	}
	if code := deliver(h, http.MethodPost, body, "").Code; code != http.StatusUnauthorized {
		t.Errorf("no signature: status = %d, want 401", code)
	}
	last := (*refusals)[len(*refusals)-1].err
	if !errors.Is(last, ErrMissingSignature) {
		t.Errorf("err = %v, want ErrMissingSignature rather than a mismatch from the last secret tried", last)
	}
}

// TestHandlerPassesTheRequestContext proves the EventHandler can observe the
// sender hanging up or the server shutting down, rather than working on against
// a connection that is gone.
func TestHandlerPassesTheRequestContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var observed error
	h, _ := newHandler(t, func(ctx context.Context, _ *Event) error {
		observed = ctx.Err()
		return nil
	})

	body := tagCreatedBody()
	request := httptest.NewRequest(http.MethodPost, "/webhooks/octonomy", strings.NewReader(body)).WithContext(ctx)
	request.Header.Set(HeaderSignature, sign(handlerSecret, []byte(body)))
	h.ServeHTTP(httptest.NewRecorder(), request)

	if !errors.Is(observed, context.Canceled) {
		t.Errorf("the event handler saw ctx.Err() = %v, want context.Canceled", observed)
	}
}

// TestHandlerWorksWithoutAnErrorHandler keeps the option optional. A refusal is
// then visible only as a status code, which is documented -- but it must not
// become a nil-func panic.
func TestHandlerWorksWithoutAnErrorHandler(t *testing.T) {
	h, err := Handler(handlerSecret, func(context.Context, *Event) error { return nil })
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	body := tagCreatedBody()
	if code := deliver(h, http.MethodPost, body, sign(handlerSecret, []byte(body))).Code; code != http.StatusOK {
		t.Errorf("status = %d, want 200", code)
	}
	if code := deliver(h, http.MethodPost, body, "").Code; code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", code)
	}
}

// TestHandlerDefaultCeilingIsTheDocumentedOne guards the constant against a
// silent change, since a consumer sizes their metadata against it.
func TestHandlerDefaultCeilingIsTheDocumentedOne(t *testing.T) {
	if DefaultMaxBodyBytes != 1<<20 {
		t.Errorf("DefaultMaxBodyBytes = %d, want 1 MiB", DefaultMaxBodyBytes)
	}

	var seen []*Event
	h, _ := newHandler(t, accept(&seen))

	// One byte over the default, with a correct signature, so the only thing
	// that can refuse it is the ceiling.
	oversized := `{"padding":"` + strings.Repeat("x", int(DefaultMaxBodyBytes)) + `"}`
	if code := deliver(h, http.MethodPost, oversized, sign(handlerSecret, []byte(oversized))).Code; code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", code)
	}
	if len(seen) != 0 {
		t.Error("an oversized delivery reached the event handler")
	}
}

// errorReader is a request body that fails partway through, the way a hung-up
// sender or a truncated upload does.
type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, fmt.Errorf("connection reset") }

// TestHandlerFailsClosedWithNoSecrets reaches the branch construction makes
// unreachable, which is the only way to prove it is the right branch.
//
// Two properties at once. The verify loop is seeded with a refusal rather than
// with nil, so a secrets list that somehow arrived empty answers "no secret"
// instead of "accepted" -- the difference between an endpoint that refuses
// everything and one that accepts everything. And the status is 500 rather than
// 401: the verification never RAN, which is this deployment's fault and not the
// sender's, and a 401 would tell Octonomy to give up on a delivery that was
// probably genuine.
func TestHandlerFailsClosedWithNoSecrets(t *testing.T) {
	var refusals []refusal
	var seen []*Event
	h := &handler{
		secrets:      nil,
		fn:           accept(&seen),
		maxBodyBytes: DefaultMaxBodyBytes,
		onError: func(_ *http.Request, status int, err error) {
			refusals = append(refusals, refusal{status: status, err: err})
		},
	}

	body := tagCreatedBody()
	recorder := deliver(h, http.MethodPost, body, sign(handlerSecret, []byte(body)))

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}
	if len(seen) != 0 {
		t.Error("a delivery nothing verified reached the event handler")
	}
	if len(refusals) != 1 || !errors.Is(refusals[0].err, ErrNoSecret) {
		t.Fatalf("refusals = %v, want one ErrNoSecret", refusals)
	}
}

// TestHandlerIsOnlyCorrectWhenItIsFirstOnTheRequest pins the two ways
// middleware in front of this handler changes what it can promise. They are
// pinned rather than fixed because one of them is unfixable from inside a
// handler, and a guarantee with an unstated precondition is worse than one
// written down.
func TestHandlerIsOnlyCorrectWhenItIsFirstOnTheRequest(t *testing.T) {
	body := tagCreatedBody()
	signature := sign(handlerSecret, []byte(body))

	t.Run("a drained body fails safe", func(t *testing.T) {
		// Middleware that read the body first -- a logger, a tracer, a
		// json.Decoder. The signature check then runs over zero bytes.
		var seen []*Event
		h, refusals := newHandler(t, accept(&seen))

		request := httptest.NewRequest(http.MethodPost, "/webhooks/octonomy", strings.NewReader(body))
		request.Header.Set(HeaderSignature, signature)
		if _, err := io.ReadAll(request.Body); err != nil {
			t.Fatalf("drain the body: %v", err)
		}
		recorder := httptest.NewRecorder()
		h.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", recorder.Code)
		}
		if len(seen) != 0 {
			t.Error("a delivery whose body was already read reached the event handler")
		}
		// Named as itself, not as a mismatch: that sentinel is the whole
		// diagnosis of a drained stream.
		if !errors.Is((*refusals)[0].err, ErrEmptyBody) {
			t.Errorf("err = %v, want ErrEmptyBody", (*refusals)[0].err)
		}
	})

	t.Run("a committed status cannot be taken back", func(t *testing.T) {
		// Middleware that wrote the response before delegating. net/http
		// ignores the second WriteHeader, so the 200 stands even though the
		// event handler refused the event -- and the dispatcher acknowledges
		// work that never happened. Nothing here can detect or repair it.
		h, refusals := newHandler(t, func(context.Context, *Event) error {
			return errors.New("the index is down")
		})

		request := httptest.NewRequest(http.MethodPost, "/webhooks/octonomy", strings.NewReader(body))
		request.Header.Set(HeaderSignature, signature)
		recorder := httptest.NewRecorder()
		recorder.WriteHeader(http.StatusOK)
		h.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d; this test exists because the committed 200 stands", recorder.Code)
		}
		// The error handler is the only signal left, and it still reports the
		// status this handler INTENDED.
		if len(*refusals) != 1 {
			t.Fatalf("recorded %d refusals, want 1", len(*refusals))
		}
		if (*refusals)[0].status != http.StatusInternalServerError {
			t.Errorf("reported status = %d, want the 500 it intended", (*refusals)[0].status)
		}
	})
}
