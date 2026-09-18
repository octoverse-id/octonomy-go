package webhook

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime/debug"
	"strings"
)

// DefaultMaxBodyBytes is the ceiling [Handler] puts on one delivery when
// [WithMaxBodyBytes] is not used.
//
// An Octonomy event is a small envelope plus one snapshot, so 1 MiB is roughly
// a thousand times a typical delivery. The headroom is for metadata: both the
// envelope and the aggregate carry a free-form JSON object whose size is the
// caller's own choice, and a consumer who stores large objects there should
// raise this rather than discover the ceiling as a 413 in production.
const DefaultMaxBodyBytes int64 = 1 << 20

// EventHandler processes one verified delivery.
//
// It is called only after the signature has been checked and the envelope
// decoded, so event is authentic and complete. Return nil to ACKNOWLEDGE the
// event -- the handler answers 200, and the server marks it published and never
// sends it again. Return an error to refuse it -- the handler answers 500, and
// the server retries with exponential backoff and eventually dead-letters it.
//
// Two rules follow from that, and both are ways a consumer loses events:
//
//   - Acknowledge an event type you do not recognize. A default branch that
//     returns an error makes every deployed consumer start dead-lettering the
//     day Octonomy adds a twelfth event type. See [Event].
//   - Do the work idempotently, keyed on [Event.ID]. Delivery is at-least-once
//     and the same event can arrive twice; nothing here can prevent that.
//
// ctx is the request's context, so it is cancelled when the sender hangs up or
// the server shuts down. Do not retain it past the return.
type EventHandler func(ctx context.Context, event *Event) error

// Errors [Handler] reports through [WithErrorHandler] for a refusal that is
// about the REQUEST rather than about the signature or the event body. The
// signature sentinels in verify.go and the envelope sentinels in events.go
// reach that hook unchanged.
var (
	// ErrMethodNotAllowed reports a delivery that was not a POST. Octonomy only
	// ever POSTs, so this is something else that found the URL -- a browser, a
	// health check, a scanner.
	ErrMethodNotAllowed = errors.New("octonomy: webhook delivery must be a POST")

	// ErrBodyTooLarge reports a body over the handler's ceiling; it wraps
	// net/http's own *MaxBytesError. Nothing was verified, because the bytes
	// the signature covers were never all read.
	//
	// Raise the ceiling with [WithMaxBodyBytes] if genuine deliveries are
	// hitting it. A refusal here is permanent on the consumer's side but not on
	// the sender's: the dispatcher retries it with backoff like any other
	// failure and dead-letters it at the attempt limit, so the event is lost to
	// a configuration number rather than to anything about the event.
	ErrBodyTooLarge = errors.New("octonomy: webhook delivery body exceeds the handler's limit")

	// ErrUnreadableBody reports a body that ended early or could not be read:
	// a hung-up sender, a cancelled request, a truncated upload.
	ErrUnreadableBody = errors.New("octonomy: webhook delivery body could not be read")

	// ErrNoEventHandler reports that [Handler] was given a nil [EventHandler].
	// It is a construction error, raised where the handler is wired rather than
	// on the first delivery.
	ErrNoEventHandler = errors.New("octonomy: webhook event handler is nil")

	// ErrHandlerPanic reports that the [EventHandler] panicked. The panic value
	// and the stack it was raised on are wrapped in.
	//
	// The handler RECOVERS and answers 500 rather than letting the panic reach
	// the consumer's http.Server, and that choice is deliberate. net/http
	// recovers a handler panic per connection, logs the stack, and closes the
	// connection WITHOUT writing a response -- so the sender records a
	// transport failure, retries, and the consumer's own error path never sees
	// it. Answering 500 gives the same retry with a response the sender can
	// classify, keeps the connection usable, and routes the stack to
	// [WithErrorHandler].
	//
	// The cost is real and worth naming: without an error handler configured,
	// the stack net/http would have logged is not logged by anything, because
	// this library does not log. Configure one.
	ErrHandlerPanic = errors.New("octonomy: webhook event handler panicked")
)

// HandlerOption configures [Handler].
type HandlerOption func(*handler)

// WithMaxBodyBytes sets the ceiling on one delivery, replacing
// [DefaultMaxBodyBytes]. A body over it is refused with 413 and
// [ErrBodyTooLarge]; it is never truncated and then verified, which would fail
// the signature check for a reason that has nothing to do with the signature.
//
// n must be positive. Zero would refuse every delivery, and a negative value is
// a caller's arithmetic that went wrong, so both are construction errors rather
// than a handler that rejects everything it is sent.
func WithMaxBodyBytes(n int64) HandlerOption {
	return func(h *handler) { h.maxBodyBytes = n }
}

// WithAdditionalSecrets accepts deliveries signed with any of these secrets as
// well as the one passed to [Handler]. It exists for ROTATION, which is a
// window rather than an instant: the server signs with one secret at a time, and
// until every dispatcher has the new one, deliveries arrive under both.
//
// Pass the INCOMING secret to [Handler] and the retiring one here, so the steady
// state costs one HMAC and only a delivery still signed with the old secret pays
// for a second. Remove the retired secret once the server no longer sends it --
// a secret left in this list is a live credential.
//
// The order the secrets are tried is not a security property (each is a full
// constant-time comparison), and only a MISMATCH moves on to the next one: a
// missing, misprefixed, or malformed signature header fails identically under
// every secret, so it is reported once, as itself.
func WithAdditionalSecrets(secrets ...string) HandlerOption {
	return func(h *handler) { h.secrets = append(h.secrets, secrets...) }
}

// WithErrorHandler reports every refusal to fn: the *http.Request it was
// refusing, the status it answered with, and why.
//
// Without it a refusal is visible only as a status code on the sender's side.
// That is not nothing -- Octonomy retries and dead-letters, so a persistent
// failure does surface eventually -- but it cannot tell an operator whether the
// deployment is misconfigured or under attack, which is the entire reason the
// refusals are distinct sentinels. Classify with errors.Is:
//
//	webhook.WithErrorHandler(func(r *http.Request, status int, err error) {
//		switch {
//		case errors.Is(err, webhook.ErrSignatureMismatch):
//			// someone sent a well-formed digest that did not match
//		case errors.Is(err, webhook.ErrMalformedEvent), errors.Is(err, webhook.ErrIncompleteEvent):
//			// a signed delivery this SDK could not decode: the contract moved
//		default:
//			log.Printf("octonomy webhook %d: %v", status, err)
//		}
//	})
//
// fn is called synchronously, on the request's goroutine, BEFORE the response is
// written -- so a slow fn slows the response and a panicking one panics the
// request. Hand the work to something else if it is not cheap. It is never
// called for a delivery the [EventHandler] accepted.
//
// This is the library's only outward channel: the SDK never logs on its own.
func WithErrorHandler(fn func(r *http.Request, status int, err error)) HandlerOption {
	return func(h *handler) { h.onError = fn }
}

// handler is the http.Handler [Handler] returns. It is unexported because every
// field of it is set through an option or the constructor, and because a
// consumer holding one could otherwise change the secret under a serving
// handler with no synchronization.
type handler struct {
	secrets      []string
	fn           EventHandler
	maxBodyBytes int64
	onError      func(r *http.Request, status int, err error)
}

// Handler returns an http.Handler that verifies, decodes, and dispatches
// Octonomy webhook deliveries.
//
// It owns the order the steps have to happen in, which is the reason it exists.
// A hand-written receiver has to bound the body, read it to bytes, verify those
// bytes, and only then parse them -- and the failure when it does not is silent:
// a body that json.NewDecoder, a logging middleware, or a tracer read first
// reaches [Verify] as empty, so the check appears to run, always fails, and gets
// "fixed" by deleting it. Here the body cannot be consumed before it is
// verified, because nothing else is given a chance to touch it.
//
//	h, err := webhook.Handler(os.Getenv("OCTONOMY_WEBHOOK_SIGNING_SECRET"),
//		func(ctx context.Context, event *webhook.Event) error {
//			switch event.Type {
//			case webhook.EventTagCreated:
//				return index(ctx, event)
//			default:
//				return nil // acknowledge what this consumer does not handle
//			}
//		},
//		webhook.WithErrorHandler(report),
//	)
//	if err != nil {
//		log.Fatalf("octonomy webhook: %v", err)
//	}
//	mux.Handle("POST /webhooks/octonomy", h)
//
// # What it answers
//
//	200  the EventHandler returned nil -- the event is acknowledged and never resent
//	401  the signature was missing, malformed, or wrong
//	405  the request was not a POST
//	413  the body exceeded the ceiling (see WithMaxBodyBytes)
//	400  the body could not be read, or is not an event this SDK can decode
//	500  the EventHandler returned an error or panicked
//
// EVERY 2xx IS AN ACKNOWLEDGEMENT. The dispatcher marks the event published and
// never retries it, so the only path here that answers 2xx is the one where the
// [EventHandler] returned nil. Every refusal above is a non-2xx on purpose, so
// the sender's retry and dead-letter machinery makes it visible.
//
// The response body is a short fixed string. The [EventHandler]'s error is never
// echoed to the sender -- it goes to [WithErrorHandler] -- because the sender is
// not the operator and a message written for one is a leak to the other.
//
// # Why it returns an error
//
// An empty signing secret, a nil [EventHandler], and a non-positive ceiling are
// all deployment mistakes, and all three are knowable when the handler is wired
// rather than at 3am on the first delivery. The alternative -- an http.Handler
// that answers 500 forever with the reason reachable only through an optional
// hook -- is exactly the check-that-appears-to-run failure this package exists
// to refuse. octonomy.New refuses a blank token the same way and for the same
// reason. An unset OCTONOMY_WEBHOOK_SIGNING_SECRET arriving as "" is the
// realistic case: it returns [ErrNoSecret], so log.Fatal at startup beats a
// silently unverified endpoint.
//
// The secret is also handed to the runtime here, so a key the runtime itself
// refuses -- under GODEBUG=fips140=only, where crypto/hmac rejects anything
// shorter than 112 bits -- is reported as [ErrUnusableSecret] at construction
// instead of on every delivery.
func Handler(secret string, fn EventHandler, opts ...HandlerOption) (http.Handler, error) {
	h := &handler{
		secrets:      []string{secret},
		fn:           fn,
		maxBodyBytes: DefaultMaxBodyBytes,
	}
	for i, opt := range opts {
		// A nil option would panic on the call, and this library does not
		// panic -- least of all at wiring time, where the caller has a typed
		// nil in a slice and no idea which one.
		if opt == nil {
			return nil, fmt.Errorf("octonomy: webhook handler option %d of %d is nil", i+1, len(opts))
		}
		opt(h)
	}

	if fn == nil {
		return nil, ErrNoEventHandler
	}
	if h.maxBodyBytes <= 0 {
		return nil, fmt.Errorf("octonomy: webhook handler body limit must be positive, got %d", h.maxBodyBytes)
	}
	for i, s := range h.secrets {
		if s == "" {
			// Named by position so a caller passing several knows which one is
			// the unset environment variable.
			return nil, fmt.Errorf("%w (secret %d of %d)", ErrNoSecret, i+1, len(h.secrets))
		}
		// Reaches the runtime's own refusal now rather than per delivery.
		if _, err := newMAC(s); err != nil {
			return nil, fmt.Errorf("%w (secret %d of %d)", err, i+1, len(h.secrets))
		}
	}
	return h, nil
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	refuse := func(status int, message string, err error) {
		if h.onError != nil {
			h.onError(r, status, err)
		}
		http.Error(w, message, status)
	}

	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		refuse(http.StatusMethodNotAllowed, "method not allowed",
			fmt.Errorf("%w, got %s", ErrMethodNotAllowed, r.Method))
		return
	}

	// The ceiling goes on BEFORE the read, which is the only place it can go:
	// once io.ReadAll has returned, an unbounded body is already in memory.
	r.Body = http.MaxBytesReader(w, r.Body, h.maxBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			refuse(http.StatusRequestEntityTooLarge, "request body too large",
				fmt.Errorf("%w of %d bytes: %w", ErrBodyTooLarge, h.maxBodyBytes, err))
			return
		}
		refuse(http.StatusBadRequest, "unreadable body", fmt.Errorf("%w: %w", ErrUnreadableBody, err))
		return
	}

	// VERIFY BEFORE PARSING. Until this returns nil the request is an anonymous
	// POST claiming to be from Octonomy, and every header on it -- the tenant
	// included -- says whatever the sender wanted it to say.
	if err := h.verify(r.Header.Get(HeaderSignature), body); err != nil {
		// Construction already refused an empty or runtime-rejected secret, so
		// neither of these is reachable today. They are mapped anyway, and to
		// 500 rather than 401, because the status is the only account of the
		// failure that reaches the sender: those two mean the verification
		// never RAN, which is this deployment's fault, and a 401 records a
		// forged signature against a delivery that was probably genuine. The
		// dispatcher treats every non-2xx alike -- backoff, then dead-letter at
		// the attempt limit -- so nothing about the retry changes; what changes
		// is what the dead-letter row says happened.
		status := http.StatusUnauthorized
		if errors.Is(err, ErrNoSecret) || errors.Is(err, ErrUnusableSecret) {
			status = http.StatusInternalServerError
		}
		refuse(status, "invalid signature", err)
		return
	}

	event, err := ParseEvent(body)
	if err != nil {
		// A signed body this SDK cannot decode is not an attack -- it is the
		// contract having moved -- and it must not be acknowledged: any 2xx
		// here marks it published and it is gone. Refuse, and let it be retried
		// and dead-lettered where someone will find it.
		refuse(http.StatusBadRequest, "malformed event", err)
		return
	}

	if err := h.invoke(r.Context(), event); err != nil {
		refuse(http.StatusInternalServerError, "handler error", err)
		return
	}

	// Explicit, because falling off the end sends the same 200 without saying
	// so -- and this 200 is the acknowledgement that ends the event's life.
	w.WriteHeader(http.StatusOK)
}

// verify accepts the body if any configured secret signed it.
//
// The loop is seeded with a refusal rather than with nil so it fails CLOSED: a
// secrets slice that somehow arrived empty returns "no secret" instead of
// "accepted". Construction guarantees at least one, which is what makes that
// unreachable rather than merely unlikely.
func (h *handler) verify(signatureHeader string, body []byte) error {
	err := ErrNoSecret
	for _, secret := range h.secrets {
		err = Verify(secret, signatureHeader, body)
		if err == nil {
			return nil
		}
		// Only a mismatch is worth trying another secret. Every other refusal
		// is about the header or the body and fails identically under all of
		// them, so retrying would turn one clear reason into N and report the
		// last one tried.
		if !errors.Is(err, ErrSignatureMismatch) {
			return err
		}
	}
	return err
}

// invoke calls the consumer's EventHandler and turns a panic into an error.
//
// See [ErrHandlerPanic] for why this recovers rather than propagating.
// http.ErrAbortHandler is re-panicked untouched: it is net/http's documented way
// to abandon a connection deliberately, and swallowing it here would silently
// disable a mechanism the consumer reached for on purpose.
func (h *handler) invoke(ctx context.Context, event *Event) (err error) {
	defer func() {
		recovered := recover()
		if recovered == nil {
			return
		}
		if recovered == http.ErrAbortHandler {
			panic(recovered)
		}
		err = fmt.Errorf("%w: %v\n%s", ErrHandlerPanic, recovered, strings.TrimRight(string(debug.Stack()), "\n"))
	}()
	return h.fn(ctx, event)
}
