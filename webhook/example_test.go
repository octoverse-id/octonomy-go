package webhook_test

import (
	"context"
	"errors"
	"fmt"
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

	// The merchant-namespaced tag.updated vector, for the same reason: an
	// *.updated payload is where the three states of a snapshot field are
	// actually reachable, and a merchant event is where a namespace is.
	updatedSignature = "sha256=dfcdefafeab32f2e8b591e0a01f8e0d73ddba3a7915c374f5234836fed2485f0"
)

// exampleEnvelope is an outbox event in the server's wire FORMAT -- compact
// separators, sorted keys, UTF-8 -- with an abbreviated snapshot under "after".
// A real tag.created carries the whole snapshot; the vectors deliberately do
// not, because they exist to pin the signature format and must stay valid as
// payloads evolve (see testdata/README.md). examples/webhook carries a complete
// one. A null namespace_type is the concrete global (tenant-shared) namespace
// and not a wildcard.
const exampleEnvelope = `{"actor_id":"user_42","aggregate_id":"9c2b0f41-5d33-4a6f-8b17-2e4c9a7d0b55",` +
	`"aggregate_type":"tag","application_id":"storefront",` +
	`"event_type":"tag.created","id":"0f1d7b24-2c1e-4f9a-9f3a-3a5f1c2d6e77",` +
	`"metadata":{"source":"api"},"namespace_id":null,"namespace_type":null,` +
	`"operation_id":"5f0a3d18-7b62-4c19-9e84-1d6b8f2a4c30",` +
	`"payload":{"after":{"id":"9c2b0f41-5d33-4a6f-8b17-2e4c9a7d0b55","is_active":true,"name":"Summer Sale","slug":"summer-sale","vocabulary_id":"3b7e5a90-1c48-4d2b-a6f5-8e0d9c1b4a26"}},` +
	`"request_id":null,"resource_id":null,"resource_type":null,` +
	`"tag_id":"9c2b0f41-5d33-4a6f-8b17-2e4c9a7d0b55","tenant_id":"acme"}`

// updatedEnvelope is the merchant-namespaced tag.updated vector: a rename, in a
// payload carrying ONLY the field that changed.
const updatedEnvelope = `{"actor_id":"user_42","aggregate_id":"9c2b0f41-5d33-4a6f-8b17-2e4c9a7d0b55",` +
	`"aggregate_type":"tag","application_id":"storefront","event_type":"tag.updated",` +
	`"id":"7a3c9e15-8b40-4d27-9f61-0c5e2a8d3b19","metadata":{"source":"api"},` +
	`"namespace_id":"merchant-9931","namespace_type":"merchant",` +
	`"operation_id":"5f0a3d18-7b62-4c19-9e84-1d6b8f2a4c30",` +
	`"payload":{"after":{"name":"Summer Clearance"},"before":{"name":"Summer Sale"}},` +
	`"request_id":"req_2f9c7b1a-6d38-4e05-b7a2-9c1e4f8d60a3","resource_id":null,"resource_type":null,` +
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

// ExampleHandler is a whole webhook endpoint. The handler owns the order --
// bound, read, verify, parse -- so what is left is the switch.
func ExampleHandler() {
	handler, err := webhook.Handler(exampleSecret,
		func(_ context.Context, event *webhook.Event) error {
			// Routing comes from the VERIFIED body, never from an X-Octonomy-*
			// header: the signature covers the body and nothing else. A global
			// event is tenant-shared, which is a namespace and not a missing one.
			namespace := "the global namespace"
			if namespaceType, namespaceID, namespaced := event.Namespace(); namespaced {
				namespace = namespaceType + "/" + namespaceID
			}

			switch event.Type {
			case webhook.EventTagCreated:
				// A snapshot field says one of absent, null, or a value. Get
				// reports the third and never panics.
				slug, _ := event.Tag.After.Slug.Get()
				fmt.Printf("created tag %s (%s) in %s\n", event.AggregateID, slug, namespace)
			default:
				// ACKNOWLEDGE what this consumer does not handle. Returning an
				// error here is how a consumer starts dead-lettering the day
				// Octonomy adds an event type this binary predates.
				fmt.Printf("acknowledged %s\n", event.Type)
			}

			// nil acknowledges: the server marks the event published and never
			// sends it again. An error refuses it, and it is retried.
			return nil
		},
		// The library never logs, so this hook is the only place a refusal is
		// visible as anything but a status code.
		webhook.WithErrorHandler(func(_ *http.Request, status int, err error) {
			fmt.Printf("refused with %d: %v\n", status, err)
		}),
	)
	if err != nil {
		// An unset OCTONOMY_WEBHOOK_SIGNING_SECRET lands here, at startup,
		// rather than becoming an endpoint that verifies nothing.
		log.Fatalf("octonomy webhook: %v", err)
	}

	// Driven with a recorder rather than a live listener: the handler is the
	// subject, and an example that needs a socket cannot run in a sandboxed CI.
	post := func(body string, signature string) int {
		request := httptest.NewRequest(http.MethodPost, "/webhooks/octonomy", strings.NewReader(body))
		request.Header.Set(webhook.HeaderSignature, signature)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder.Code
	}

	fmt.Println("genuine delivery:", post(exampleEnvelope, exampleSignature))

	// The same signature over a body altered after signing. The handler refuses
	// it, and the sender's retry and dead-letter machinery makes that visible.
	tampered := strings.Replace(exampleEnvelope, `"tenant_id":"acme"`, `"tenant_id":"evil"`, 1)
	fmt.Println("tampered delivery:", post(tampered, exampleSignature))

	// Output:
	// created tag 9c2b0f41-5d33-4a6f-8b17-2e4c9a7d0b55 (summer-sale) in the global namespace
	// genuine delivery: 200
	// refused with 401: octonomy: webhook signature does not match the body
	// tampered delivery: 401
}

// ExampleParseEvent is the same decode without the handler, for a consumer
// whose HTTP layer belongs to a framework. Verify FIRST -- until it returns
// nil the bytes are an anonymous POST claiming to be from Octonomy.
func ExampleParseEvent() {
	parse := func(body, signature string) *webhook.Event {
		if err := webhook.Verify(exampleSecret, signature, []byte(body)); err != nil {
			log.Fatalf("refused: %v", err)
		}
		event, err := webhook.ParseEvent([]byte(body))
		if err != nil {
			log.Fatalf("malformed: %v", err)
		}
		return event
	}

	created := parse(exampleEnvelope, exampleSignature)
	fmt.Println("type:", created.Type)
	fmt.Println("aggregate:", created.AggregateID)
	slug, _ := created.Tag.After.Slug.Get()
	fmt.Println("slug:", slug)
	_, _, namespaced := created.Namespace()
	fmt.Println("namespaced:", namespaced)

	// An *.updated payload carries only what changed, which is where the three
	// states of a snapshot field are reachable: a value for the field that
	// moved, and ABSENT for every field this update did not touch. A *string
	// would report the second as nil -- the same thing it would report for a
	// field that had been cleared to null.
	updated := parse(updatedEnvelope, updatedSignature)
	name, _ := updated.Tag.After.Name.Get()
	fmt.Println("renamed to:", name)
	fmt.Println("slug untouched:", updated.Tag.After.Slug.IsZero())
	fmt.Println("slug cleared:", updated.Tag.After.Slug.IsNull())
	namespaceType, namespaceID, namespaced := updated.Namespace()
	fmt.Printf("namespace: %s/%s (namespaced %t)\n", namespaceType, namespaceID, namespaced)

	// An event type this SDK predates is not an error. The raw type and the
	// undecoded payload are both there; the typed payload is nil.
	future := strings.Replace(exampleEnvelope, `"event_type":"tag.created"`, `"event_type":"tag.merged"`, 1)
	unknown, err := webhook.ParseEvent([]byte(future))
	fmt.Printf("future type %q: err=%v known=%t typed=%t\n",
		unknown.Type, err, unknown.Known(), unknown.Tag != nil)

	// Output:
	// type: tag.created
	// aggregate: 9c2b0f41-5d33-4a6f-8b17-2e4c9a7d0b55
	// slug: summer-sale
	// namespaced: false
	// renamed to: Summer Clearance
	// slug untouched: true
	// slug cleared: false
	// namespace: merchant/merchant-9931 (namespaced true)
	// future type "tag.merged": err=<nil> known=false typed=false
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
