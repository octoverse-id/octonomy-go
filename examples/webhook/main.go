// Command webhook is a webhook receiver built on webhook.Handler: the handler
// owns the order the steps have to happen in -- bound the body, read it, verify
// the signature, and only then parse and route -- so this file is the part that
// is actually yours, which is the switch.
//
//	go run ./examples/webhook
//
// It prints three curl commands: a genuine delivery, one altered after signing,
// and one carrying an event type this SDK has never heard of. No dev server is
// needed -- nothing here talks to Octonomy.
//
// The third one is the one worth running twice. Delivery is at-least-once with
// backoff and dead-lettering, so a consumer that ERRORED on an unrecognized
// event type would start dead-lettering the day Octonomy adds a twelfth one.
// The default branch below acknowledges it instead, and answers 200.
package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/octoverse-id/octonomy-go/v2/webhook"
)

// One real tag.created envelope, exactly as the server puts it on the wire:
// compact separators, sorted keys. A null namespace_type is the global
// (tenant-shared) namespace, not a wildcard.
const sampleEnvelope = `{"actor_id":"user_42","aggregate_id":"9c2b0f41-5d33-4a6f-8b17-2e4c9a7d0b55",` +
	`"aggregate_type":"tag","application_id":"storefront","event_type":"tag.created",` +
	`"id":"0f1d7b24-2c1e-4f9a-9f3a-3a5f1c2d6e77","metadata":{},"namespace_id":null,"namespace_type":null,` +
	`"operation_id":"5f0a3d18-7b62-4c19-9e84-1d6b8f2a4c30",` +
	`"payload":{"after":{"id":"9c2b0f41-5d33-4a6f-8b17-2e4c9a7d0b55","is_active":true,` +
	`"name":"Summer Sale","slug":"summer-sale","type":"campaign"}},` +
	`"request_id":null,"resource_id":null,"resource_type":null,` +
	`"tag_id":"9c2b0f41-5d33-4a6f-8b17-2e4c9a7d0b55","tenant_id":"acme"}`

// The same envelope carrying an event type that does not exist yet. This is
// what a future server release looks like to a binary built today.
var futureEnvelope = strings.NewReplacer(
	`"event_type":"tag.created"`, `"event_type":"tag.merged"`,
	`"id":"0f1d7b24-2c1e-4f9a-9f3a-3a5f1c2d6e77"`, `"id":"4e8a2c50-7f19-4d63-b0a8-5c9e1f7d3b26"`,
).Replace(sampleEnvelope)

func main() {
	// The secret the DEPLOYMENT signs with (OCTONOMY_WEBHOOK_SIGNING_SECRET on
	// the server). The fallback exists so this example runs with nothing set up;
	// a real receiver has no default, and webhook.Handler refuses to be built
	// without one rather than answering 500 to every delivery.
	secret := env("OCTONOMY_WEBHOOK_SIGNING_SECRET", "whsec_example_not_a_real_secret")
	addr := env("OCTONOMY_WEBHOOK_ADDR", "127.0.0.1:8088")

	handler, err := webhook.Handler(secret, handleEvent, webhook.WithErrorHandler(report))
	if err != nil {
		// An unset signing secret lands here, at startup, instead of becoming
		// an endpoint that looks healthy and verifies nothing.
		log.Fatalf("octonomy webhook: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("POST /webhooks/octonomy", handler)

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Bind BEFORE announcing. ListenAndServe in a goroutine would let this
	// program print "listening" and a curl command while the socket is not yet
	// accepting -- so the first delivery gets connection refused -- and would
	// report a bind failure (a port already in use) only after claiming to have
	// succeeded. net.Listen here makes both impossible: the address is held by
	// the time anything is printed, and a failure is the program's exit status.
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("listen on %s: %v", addr, err)
	}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("serve: %v", err)
		}
	}()
	printTryItCommands(listener.Addr().String(), secret)

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

// handleEvent is the only part of a receiver that is yours. It is reached only
// for a delivery whose signature verified and whose envelope decoded, so
// everything on the event is authentic.
//
// Returning nil ACKNOWLEDGES the event: the server marks it published and never
// sends it again. Returning an error refuses it, and the server retries with
// backoff and eventually dead-letters it. There is no third answer, which is
// why the default branch below matters.
func handleEvent(_ context.Context, event *webhook.Event) error {
	// Routing comes from the VERIFIED body, never from an X-Octonomy-* header:
	// the signature covers the body and nothing else, so a genuine delivery
	// replayed with the tenant header rewritten verifies exactly as it did the
	// first time. Event.Namespace says whether the event is namespaced at all;
	// a global event is tenant-shared, not "namespace unknown".
	namespace := "global"
	if namespaceType, namespaceID, namespaced := event.Namespace(); namespaced {
		namespace = namespaceType + "/" + namespaceID
	}

	// Delivery is at-least-once and carries no timestamp, so there is no signed
	// freshness claim to check and replay is not preventable here. Dedupe on
	// event.ID -- stable across redeliveries -- and do the work idempotently.
	log.Printf("accepted %s id=%s tenant=%s namespace=%s", event.Type, event.ID, event.TenantID, namespace)

	switch event.Type {
	case webhook.EventTagCreated:
		// A snapshot field says one of three things: absent (this event did not
		// carry it), null (it was cleared), or a value. Get reports the third
		// and never panics -- which a *string field would have invited.
		slug, ok := event.Tag.After.Slug.Get()
		if !ok {
			// A tag.created always carries a slug, so this is the contract
			// having moved. Refusing it is right: the delivery is retried and
			// dead-lettered where someone will find it.
			return fmt.Errorf("tag.created %s carried no slug", event.AggregateID)
		}
		log.Printf("  index tag %s as %q", event.AggregateID, slug)

	case webhook.EventTagDeactivated:
		// The cascade is reported twice by design: here as a summary, and once
		// per alias as its own tag_alias.deactivated. Handle either, not both.
		log.Printf("  drop tag %s (%d aliases cascaded)", event.AggregateID, len(event.Tag.CascadedAliasIDs))

	default:
		// ACKNOWLEDGE what this consumer does not handle, including an event
		// type this SDK has never heard of. Known() is false for the latter,
		// and event.Payload still holds the undecoded JSON.
		if !event.Known() {
			log.Printf("  unrecognized event type %q, acknowledged: %s", event.Type, event.Payload)
		}
	}
	return nil
}

// report is where refusals go. The library never logs, so without a hook like
// this one a refusal is visible only as a status code on the sender's side.
func report(_ *http.Request, status int, err error) {
	switch {
	case errors.Is(err, webhook.ErrNoSecret), errors.Is(err, webhook.ErrUnusableSecret):
		log.Printf("octonomy webhook %d: THIS DEPLOYMENT is misconfigured: %v", status, err)
	case errors.Is(err, webhook.ErrSignatureMismatch):
		log.Printf("octonomy webhook %d: signature did not match the body: %v", status, err)
	case errors.Is(err, webhook.ErrMalformedEvent), errors.Is(err, webhook.ErrIncompleteEvent),
		errors.Is(err, webhook.ErrMalformedPayload):
		log.Printf("octonomy webhook %d: a signed delivery this SDK could not decode: %v", status, err)
	default:
		log.Printf("octonomy webhook %d: refused: %v", status, err)
	}
}

// printTryItCommands signs the sample envelopes so the endpoint can be
// exercised without a running Octonomy.
//
// THE SERVER DOES THIS, NOT A CONSUMER. It is here only because no Octonomy
// deployment emits webhooks by default (OUTBOX_TRANSPORT is "logging"), so
// without it there would be nothing to receive. The SDK deliberately ships no
// signing function: a consumer that can sign can forge.
func printTryItCommands(addr, secret string) {
	sign := func(body string) string {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(body))
		return "sha256=" + hex.EncodeToString(mac.Sum(nil))
	}

	url := "http://" + addr + "/webhooks/octonomy"
	fmt.Printf("listening on %s\n\n", url)

	post := func(label, body, signature string) {
		fmt.Printf("%s:\n  curl -si -X POST %s \\\n    -H '%s: %s' \\\n    -d '%s'\n\n",
			label, url, webhook.HeaderSignature, signature, body)
	}

	post("genuine delivery (expect 200)", sampleEnvelope, sign(sampleEnvelope))

	// The same signature over a body altered after signing. The change is
	// length-preserving and leaves valid JSON behind, so nothing downstream of
	// the signature check would have noticed it.
	tampered := strings.Replace(sampleEnvelope, `"tenant_id":"acme"`, `"tenant_id":"evil"`, 1)
	post("tampered delivery (expect 401)", tampered, sign(sampleEnvelope))

	post("event type from a future server (expect 200)", futureEnvelope, sign(futureEnvelope))

	fmt.Println("Ctrl-C to stop.")
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
