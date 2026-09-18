// Package webhook receives Octonomy webhook deliveries: it verifies the HMAC
// signature, decodes the event, and hands it to your code.
//
// There are three entry points, and which one you use depends on how much of
// the HTTP layer you own.
//
//	[Handler]     an http.Handler that does the whole thing. Use this.
//	[ParseEvent]  the envelope and its typed payload, from bytes you verified.
//	[Verify]      the signature check alone, over the raw body.
//
// [Handler] exists because the ORDER of those steps is the part that is easy to
// get wrong and silent when it is: bound the body, read it to bytes, verify
// those bytes, and only then parse them. See "Writing it yourself" below for
// what that order costs when a handler does not own it.
//
// This package may import the root octonomy package; the root never imports it.
// It imports it for two types -- octonomy.Metadata and octonomy.Optional, both
// of which a payload is made of -- and for nothing else.
//
// # The delivery contract
//
// The server signs the exact bytes of the request body with HMAC-SHA256 under
// the shared signing secret, hex-encodes the digest in lowercase, and sends it
// prefixed with "sha256=":
//
//	X-Octonomy-Signature:  sha256=<hex hmac-sha256(secret, raw_body)>
//	X-Octonomy-Event-ID:   <uuid>
//	X-Octonomy-Event-Type: <event type, e.g. tag.created>
//	X-Octonomy-Tenant-ID:  <tenant>
//	X-Octonomy-Request-ID: <request id>   -- CONDITIONAL
//
// X-Octonomy-Request-ID is sent only for an event that carries a request id, so
// a handler must treat its absence as ordinary rather than as a malformed
// delivery. The other four are always present. [HeaderSignature] and its
// siblings name them.
//
// # Receiving a delivery
//
// [Handler] takes the signing secret and one function. The function gets a
// decoded, authenticated [Event]; returning nil acknowledges it and returning
// an error refuses it:
//
//	h, err := webhook.Handler(os.Getenv("OCTONOMY_WEBHOOK_SIGNING_SECRET"),
//		func(ctx context.Context, event *webhook.Event) error {
//			switch event.Type {
//			case webhook.EventTagCreated:
//				slug, _ := event.Tag.After.Slug.Get()
//				return index.Add(ctx, event.AggregateID, slug)
//			case webhook.EventTagDeactivated:
//				return index.Remove(ctx, event.AggregateID)
//			default:
//				return nil // acknowledge what this consumer does not handle
//			}
//		},
//		webhook.WithErrorHandler(func(r *http.Request, status int, err error) {
//			log.Printf("octonomy webhook %d: %v", status, err)
//		}),
//	)
//	if err != nil {
//		log.Fatalf("octonomy webhook: %v", err)
//	}
//	mux.Handle("POST /webhooks/octonomy", h)
//
// The statuses it answers, and why each one, are on [Handler]. The two rules
// that outlive it are on [EventHandler]: acknowledge an event type you do not
// recognize, and do the work idempotently on [Event.ID].
//
// # Writing it yourself
//
// A consumer whose HTTP layer belongs to a framework, or who is replaying
// bodies out of a queue, calls [Verify] and then [ParseEvent]. Three things
// about that order matter, and each one is a way webhook verification is
// commonly broken:
//
//  1. BOUND THE BODY YOURSELF, before reading it. [Verify] is handed bytes that
//     are already in memory, so it cannot impose a ceiling -- by the time it is
//     called, an attacker's 8 GiB POST has already been read. Wrap the body in
//     [net/http.MaxBytesReader] first. [Handler] does this for you and nothing
//     else will.
//
//  2. READ THE BODY TO BYTES AND VERIFY BEFORE PARSING IT. Not after, and not
//     "while" -- a json.Decoder over the request body consumes it, and a body
//     consumed before Verify sees it is empty or partial by then. That is why
//     Verify takes a []byte and not an *http.Request: an unread stream cannot
//     be handed to it by accident. Parse the body only once the signature has
//     checked out; until then it is attacker-controlled input that merely
//     claims to be from Octonomy.
//
//  3. REFUSE THE REQUEST ON ANY ERROR. Every failure mode of a signature check
//     is silent -- a handler that logs the error and returns 200 looks healthy
//     on both ends forever. Return 401 and let the sender's retry and
//     dead-letter machinery make the failure visible.
//
//     This outlives the signature check. EVERY 2xx is an acknowledgement: the
//     dispatcher marks the event published and never retries it, so any path
//     that answers 200 without actually processing the delivery -- a discarded
//     json.Unmarshal error, a handler that just falls off the end -- is how one
//     disappears for good.
//
// # The events
//
// [Event] is the envelope every delivery carries. Its typed payload is one of
// [TagPayload], [VocabularyPayload], [TagAliasPayload], or [AssignmentPayload],
// chosen by [Event.Type]; each holds the before and after sides as one of the
// four snapshot types. [Event.Payload] is the raw JSON, always.
//
// Two properties of that surface are load-bearing and neither is obvious.
//
// AN UNKNOWN EVENT TYPE IS NOT AN ERROR. It parses, [EventType.Known] reports
// false, the typed payloads are nil, and the raw type and payload are there to
// look at. Anything else would mean that the day Octonomy adds a twelfth event
// type, every deployed Go consumer starts retrying and dead-lettering it --
// consumers who shipped no code and did nothing wrong.
//
// A SNAPSHOT IS NOT A REST MODEL, and octonomy.Tag must not be used in its
// place. Snapshots omit usage_count and the namespace pair, and an *.updated
// payload carries ONLY THE FIELDS THAT CHANGED -- so every field is an
// octonomy.Optional that says which of absent, null, and a value arrived. The
// snapshot types' own documentation has the reasoning.
//
// # What a valid signature proves, and what it does not
//
// A nil error from [Verify] means these exact bytes were signed by a holder of
// the secret. That is authenticity, and it is all of it.
//
// REPLAY IS NOT PREVENTED, AND THIS PACKAGE CANNOT PREVENT IT. The server sends
// no timestamp header, so there is no signed freshness claim to check and no
// window to enforce; a captured delivery replayed a week later carries a
// signature that is still perfectly valid. Nor is replay hypothetical:
// Octonomy's outbox is at-least-once, so it redelivers on its own after a crash
// between publish and mark, or when a stale processing claim is recovered.
//
// Deduplicate on [Event.ID], which is stable across redeliveries, and make the
// handler idempotent. [Event.OperationID] groups the events one request emitted,
// which is the other half of reconstructing what happened. Ordering is
// best-effort by creation time and is not guaranteed under retries.
//
// A valid signature also says nothing about WHICH tenant the event is for.
// Read that from the parsed body -- after verifying -- and never from
// X-Octonomy-Tenant-ID, which the signature does not cover at all: a wholly
// genuine delivery replayed with that header rewritten verifies exactly as it
// did the first time. The same goes for every other X-Octonomy-* header; none
// of them becomes trustworthy because Verify returned nil. Routing lives in the
// JSON body: partition on (TenantID, ApplicationID, NamespaceType,
// NamespaceID), where a null namespace_type is the concrete global namespace
// and not a wildcard. [Event.Namespace] is that distinction as a method.
//
// # Secret rotation
//
// A secret is a shared secret, so rotating it is a window, not an instant: the
// server sends one secret's signature and the consumer must already accept the
// other. [Handler] takes the incoming secret and [WithAdditionalSecrets] the
// retiring one, and that is the whole of it.
//
// Doing it by hand, [Verify] takes one secret per call, so hold a slice during
// the window and accept a body that any of them signs:
//
//	for _, secret := range secrets {
//		if err := webhook.Verify(secret, sig, body); err == nil {
//			// accepted
//		}
//	}
//
// Stopping early on anything except [ErrSignatureMismatch] is what makes that
// loop correct rather than merely short -- a malformed header is not a reason
// to try the next secret, and a failure to reject it is a failure to notice the
// delivery was never signed properly at all. Seed the loop's error with a
// refusal rather than with nil, too, so a list that was never populated fails
// CLOSED instead of returning "accepted". Example_secretRotation is the whole
// thing. Drop the old secret once the server no longer sends it; leaving
// a retired secret in the list keeps it live.
//
// Trying N secrets costs N HMACs over the body, which is nothing next to the
// network round trip that delivered it, so there is no reason to shorten the
// window for performance.
//
// # Test vectors
//
// testdata/signature_vectors.json holds known-good vectors -- secret, body,
// and the correct signature -- plus the cases that must be rejected and why.
// They are computed from the server's own signing code rather than from this
// package, they are format- and not payload-dependent, and they are portable:
// an SDK in any language can drive its verifier from that one file. See
// testdata/README.md.
package webhook
