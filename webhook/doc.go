// Package webhook verifies the HMAC signature Octonomy puts on a webhook
// delivery.
//
// It is one function. [Verify] takes the signing secret, the value of the
// X-Octonomy-Signature header, and the RAW REQUEST BODY, and reports whether
// those bytes were signed with that secret. Nothing here parses an event and
// nothing here depends on a payload shape, which is why this package can be
// correct today: the typed event surface and an http.Handler adapter are
// deferred until an Octonomy deployment actually emits webhooks
// (OUTBOX_TRANSPORT defaults to "logging"), while the signature contract is
// already fixed and already dangerous to get wrong.
//
// This package may import the root octonomy package; the root never imports it.
// Today it imports neither -- verification needs only the standard library.
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
// # Writing the handler
//
// See the package examples for the whole shape. Three things about it matter,
// and each one is a way webhook verification is commonly broken:
//
//  1. BOUND THE BODY YOURSELF, before reading it. This package is handed bytes
//     that are already in memory, so it cannot impose a ceiling -- by the time
//     Verify is called, an attacker's 8 GiB POST has already been read. Wrap
//     the body in [net/http.MaxBytesReader] first. This SDK ships no handler,
//     so nothing else will do it for you.
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
// # What a valid signature proves, and what it does not
//
// A nil error from Verify means these exact bytes were signed by a holder of
// the secret. That is authenticity, and it is all of it.
//
// REPLAY IS NOT PREVENTED, AND THIS PACKAGE CANNOT PREVENT IT. The server sends
// no timestamp header, so there is no signed freshness claim to check and no
// window to enforce; a captured delivery replayed a week later carries a
// signature that is still perfectly valid. Nor is replay hypothetical:
// Octonomy's outbox is at-least-once, so it redelivers on its own after a crash
// between publish and mark, or when a stale processing claim is recovered.
//
// Deduplicate on the envelope's "id" field, which is stable across
// redeliveries, and make the handler idempotent. "operation_id" groups the
// events one request emitted, which is the other half of reconstructing what
// happened. Ordering is best-effort by creation time and is not guaranteed
// under retries.
//
// A valid signature also says nothing about WHICH tenant the event is for.
// Read that from the parsed body -- after verifying -- and never from
// X-Octonomy-Tenant-ID alone, which is as attacker-controlled as the rest of
// the request until the signature checks out. Routing lives in the JSON body:
// partition on (tenant_id, application_id, namespace_type, namespace_id), where
// a null namespace_type is the concrete global namespace and not a wildcard.
//
// # Secret rotation
//
// A secret is a shared secret, so rotating it is a window, not an instant: the
// server sends one secret's signature and the consumer must already accept the
// other. Verify takes one secret per call, so hold a slice during the window
// and accept a body that any of them signs:
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
// delivery was never signed properly at all. Example_secretRotation is the
// whole thing. Drop the old secret once the server no longer sends it; leaving
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
