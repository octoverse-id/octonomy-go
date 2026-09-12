package webhook_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/octoverse-id/octonomy-go/v2/webhook"
)

// The signing secret a deployment sets as OCTONOMY_WEBHOOK_SIGNING_SECRET, the
// secret it is being rotated to, and one real tag.created delivery signed under
// each. Every value here is lifted from testdata/signature_vectors.json, so
// these examples run against signatures the SERVER's signing code produced --
// they fail if this package ever stops agreeing with the emitter.
const (
	exampleSecret = "whsec_3f8a1c0d9b2e4f6a8c1d3e5f7a9b0c2d"
	rotatedSecret = "whsec_0a1b2c3d4e5f60718293a4b5c6d7e8f90"

	exampleSignature = "sha256=20b8b4239fcae9785de2ac6d6de1abf2c0824fa5f0442032d9c57cc808c96b04"
	rotatedSignature = "sha256=47cc334e6fce09c9a8b161c39d2482029411781be73c68196281e9c7d94d14ff"
)

// exampleEnvelope is an outbox event exactly as the server puts it on the wire:
// compact separators, sorted keys, UTF-8. A null namespace_type is the concrete
// global (tenant-shared) namespace and not a wildcard.
const exampleEnvelope = `{"actor_id":"user_42","aggregate_id":"9c2b0f41-5d33-4a6f-8b17-2e4c9a7d0b55",` +
	`"aggregate_type":"tag","application_id":"storefront",` +
	`"event_type":"tag.created","id":"0f1d7b24-2c1e-4f9a-9f3a-3a5f1c2d6e77",` +
	`"metadata":{"source":"api"},"namespace_id":null,"namespace_type":null,` +
	`"operation_id":"5f0a3d18-7b62-4c19-9e84-1d6b8f2a4c30",` +
	`"payload":{"after":{"id":"9c2b0f41-5d33-4a6f-8b17-2e4c9a7d0b55","is_active":true,"name":"Summer Sale","slug":"summer-sale","vocabulary_id":"3b7e5a90-1c48-4d2b-a6f5-8e0d9c1b4a26"}},` +
	`"request_id":null,"resource_id":null,"resource_type":null,` +
	`"tag_id":"9c2b0f41-5d33-4a6f-8b17-2e4c9a7d0b55","tenant_id":"acme"}`

func ExampleVerify() {
	err := webhook.Verify(exampleSecret, exampleSignature, []byte(exampleEnvelope))
	fmt.Println("delivery verified:", err == nil)

	// The same delivery, with the tenant switched after it was signed. The
	// mutation is length-preserving and leaves valid JSON behind, so nothing
	// downstream of the signature check would have noticed.
	tampered := strings.Replace(exampleEnvelope, `"tenant_id":"acme"`, `"tenant_id":"evil"`, 1)
	err = webhook.Verify(exampleSecret, exampleSignature, []byte(tampered))
	fmt.Println("tampered accepted:", err == nil)
	fmt.Println("mismatch:", errors.Is(err, webhook.ErrSignatureMismatch))

	// A body something else already read -- middleware, a logger, a
	// json.Decoder -- reaches Verify as zero bytes and is named as such rather
	// than surfacing as a permanent, mysterious mismatch.
	err = webhook.Verify(exampleSecret, exampleSignature, nil)
	fmt.Println("drained body:", err)

	// Output:
	// delivery verified: true
	// tampered accepted: false
	// mismatch: true
	// drained body: octonomy: webhook body is empty
}

// Example_httpHandler is the whole shape of a webhook endpoint: bound, read,
// verify, and only then parse.
func Example_httpHandler() {
	// The ceiling is the caller's job. This package is handed bytes that are
	// already in memory, so it cannot impose one -- without the MaxBytesReader
	// below, an unbounded POST is read into the process before anything gets a
	// chance to refuse it. Size it to the largest payload your events carry.
	const maxBodyBytes = 1 << 20 // 1 MiB

	handler := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "unreadable body", http.StatusBadRequest)
			return
		}

		// Verify BEFORE parsing. Until this returns nil the request is an
		// anonymous POST claiming to be from Octonomy, and every header on it
		// -- the tenant included -- claims whatever the sender wanted.
		if err := webhook.Verify(exampleSecret, r.Header.Get(webhook.HeaderSignature), body); err != nil {
			// Refuse. Answering 200 here is the failure that never surfaces:
			// the sender records a successful delivery and stops retrying,
			// while nothing was ever verified.
			log.Printf("octonomy webhook rejected: %v", err)
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}

		var event struct {
			ID            string          `json:"id"`
			TenantID      string          `json:"tenant_id"`
			ApplicationID string          `json:"application_id"`
			EventType     string          `json:"event_type"`
			NamespaceType *string         `json:"namespace_type"`
			NamespaceID   *string         `json:"namespace_id"`
			Payload       json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(body, &event); err != nil {
			http.Error(w, "malformed event", http.StatusBadRequest)
			return
		}

		// Routing comes from the VERIFIED body, never from the headers: the
		// signature covers the body and nothing else. A null namespace_type is
		// the global, tenant-shared namespace.
		namespace := "global"
		if event.NamespaceType != nil {
			namespace = *event.NamespaceType + "/" + *event.NamespaceID
		}

		// Delivery is at-least-once and carries no timestamp to bound replay,
		// so the handler dedupes on the envelope's stable id -- unchanged
		// across redeliveries -- and does its work idempotently.
		fmt.Printf("%s %s tenant=%s app=%s namespace=%s\n",
			event.EventType, event.ID, event.TenantID, event.ApplicationID, namespace)

		w.WriteHeader(http.StatusNoContent)
	}

	// Driven with a recorder rather than a live listener: the handler is the
	// subject, and an example that needs a socket cannot run in a sandboxed CI.
	post := func(body string, signature string) int {
		request := httptest.NewRequest(http.MethodPost, "/webhooks/octonomy", strings.NewReader(body))
		request.Header.Set(webhook.HeaderSignature, signature)
		recorder := httptest.NewRecorder()
		handler(recorder, request)
		return recorder.Code
	}

	fmt.Println("genuine delivery:", post(exampleEnvelope, exampleSignature))

	// The same signature over a body altered after signing. The handler refuses
	// it, and the sender's retry and dead-letter machinery makes that visible.
	tampered := strings.Replace(exampleEnvelope, `"tenant_id":"acme"`, `"tenant_id":"evil"`, 1)
	fmt.Println("tampered delivery:", post(tampered, exampleSignature))

	// Output:
	// tag.created 0f1d7b24-2c1e-4f9a-9f3a-3a5f1c2d6e77 tenant=acme app=storefront namespace=global
	// genuine delivery: 204
	// tampered delivery: 401
}

// Example_secretRotation accepts either the outgoing or the incoming secret for
// the length of a rotation window. Rotation is a window and not an instant: the
// server signs with one secret at a time, and until every dispatcher has the
// new one, deliveries arrive under both.
func Example_secretRotation() {
	// Newest first, so the steady state costs one HMAC and only a delivery
	// still signed with the retiring secret pays for the second.
	secrets := []string{
		rotatedSecret, // incoming
		exampleSecret, // outgoing -- drop it once the server no longer sends it
	}

	verify := func(signature string, body []byte) error {
		// Seeded with a refusal rather than with nil, so an empty or
		// misconfigured list fails CLOSED. A bare `var err error` here returns nil
		// -- accepted -- for a list that was never populated, which is the same
		// silent-acceptance failure the package exists to prevent.
		err := webhook.ErrNoSecret
		for _, secret := range secrets {
			err = webhook.Verify(secret, signature, body)
			if err == nil {
				return nil
			}
			// Only a MISMATCH is worth trying another secret. A missing,
			// misprefixed, or malformed header fails identically under every
			// secret, so retrying it would turn one clear refusal into N and
			// report the last -- this way the specific reason survives.
			if !errors.Is(err, webhook.ErrSignatureMismatch) {
				return err
			}
		}
		return err
	}

	body := []byte(exampleEnvelope)
	fmt.Println("signed with the incoming secret:", verify(rotatedSignature, body) == nil)
	fmt.Println("signed with the outgoing secret:", verify(exampleSignature, body) == nil)

	// Well-formed, and not a digest either secret produces.
	forged := "sha256=" + strings.Repeat("0", 64)
	fmt.Println("signed with neither:", verify(forged, body))

	// Not a mismatch, so the loop stops at the first secret and says what is
	// actually wrong instead of blaming the last one it tried.
	fmt.Println("not signed at all:", verify("", body))

	// Output:
	// signed with the incoming secret: true
	// signed with the outgoing secret: true
	// signed with neither: octonomy: webhook signature does not match the body
	// not signed at all: octonomy: webhook signature header is missing
}
