// Command webhook is a webhook receiver: the whole shape of an Octonomy
// endpoint, in the order the steps have to happen -- bound the body, read it,
// verify the signature, and only then parse and route.
//
// The SDK ships no http.Handler on purpose. A handler would have to read the
// body for you, and the HMAC is over the raw bytes: a body that middleware, a
// logger, or json.NewDecoder read first verifies as empty or partial. With no
// handler, bounding and reading the body is visibly the caller's job -- which is
// what this file is.
//
//	go run ./examples/webhook
//
// It prints two curl commands: one genuine delivery and one altered after
// signing. No dev server is needed -- nothing here talks to Octonomy.
package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/octoverse-id/octonomy-go/v2/webhook"
)

// maxBodyBytes bounds one delivery. The ceiling is the caller's job: this
// package is handed bytes that are already in memory, so it cannot impose one,
// and without the MaxBytesReader below an unbounded POST is read into the
// process before anything gets a chance to refuse it.
const maxBodyBytes = 1 << 20 // 1 MiB

// One real tag.created envelope, exactly as the server puts it on the wire:
// compact separators, sorted keys. A null namespace_type is the global
// (tenant-shared) namespace, not a wildcard.
const sampleEnvelope = `{"actor_id":"user_42","aggregate_id":"9c2b0f41-5d33-4a6f-8b17-2e4c9a7d0b55",` +
	`"aggregate_type":"tag","application_id":"storefront","event_type":"tag.created",` +
	`"id":"0f1d7b24-2c1e-4f9a-9f3a-3a5f1c2d6e77","namespace_id":null,"namespace_type":null,` +
	`"payload":{"after":{"id":"9c2b0f41-5d33-4a6f-8b17-2e4c9a7d0b55","slug":"summer-sale"}},` +
	`"tenant_id":"acme"}`

func main() {
	// The secret the DEPLOYMENT signs with (OCTONOMY_WEBHOOK_SIGNING_SECRET on
	// the server). The fallback exists so this example runs with nothing set up;
	// a real receiver has no default and refuses to start without one.
	secret := env("OCTONOMY_WEBHOOK_SIGNING_SECRET", "whsec_example_not_a_real_secret")
	addr := env("OCTONOMY_WEBHOOK_ADDR", "127.0.0.1:8088")

	mux := http.NewServeMux()
	mux.HandleFunc("/webhooks/octonomy", func(w http.ResponseWriter, r *http.Request) {
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

		// VERIFY BEFORE PARSING. Until this returns nil the request is an
		// anonymous POST claiming to be from Octonomy, and every header on it --
		// the tenant included -- says whatever the sender wanted it to say.
		//
		// Answering 200 to a bad signature is the failure that never surfaces:
		// the sender records a successful delivery and stops retrying, while
		// nothing was ever verified.
		if err := webhook.Verify(secret, r.Header.Get(webhook.HeaderSignature), body); err != nil {
			// The sentinels are distinct so an operator can tell a
			// misconfiguration from an attack. Only the last one means someone
			// sent a well-formed digest that did not match.
			switch {
			case errors.Is(err, webhook.ErrNoSecret), errors.Is(err, webhook.ErrUnusableSecret):
				log.Printf("octonomy webhook: THIS DEPLOYMENT is misconfigured: %v", err)
			case errors.Is(err, webhook.ErrSignatureMismatch):
				log.Printf("octonomy webhook: signature did not match the body: %v", err)
			default:
				log.Printf("octonomy webhook: refused: %v", err)
			}
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}

		var event struct {
			ID            string          `json:"id"`
			TenantID      string          `json:"tenant_id"`
			EventType     string          `json:"event_type"`
			NamespaceType *string         `json:"namespace_type"`
			NamespaceID   *string         `json:"namespace_id"`
			Payload       json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(body, &event); err != nil {
			http.Error(w, "malformed event", http.StatusBadRequest)
			return
		}

		// Routing comes from the VERIFIED body, never from an X-Octonomy-*
		// header: the signature covers the body and nothing else, so a genuine
		// delivery replayed with the tenant header rewritten verifies exactly as
		// it did the first time. A consumer that routed on that header would
		// send a real event to the wrong tenant.
		namespace := "global"
		if event.NamespaceType != nil && event.NamespaceID != nil {
			namespace = *event.NamespaceType + "/" + *event.NamespaceID
		}

		// Delivery is at-least-once and carries no timestamp, so there is no
		// signed freshness claim to check and replay is not preventable here.
		// Dedupe on the envelope's stable id and do the work idempotently.
		log.Printf("accepted %s id=%s tenant=%s namespace=%s", event.EventType, event.ID, event.TenantID, namespace)

		w.WriteHeader(http.StatusNoContent)
	})

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()
	printTryItCommands(addr, secret)

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

// printTryItCommands signs the sample envelope so the endpoint can be exercised
// without a running Octonomy.
//
// THE SERVER DOES THIS, NOT A CONSUMER. It is here only because no Octonomy
// deployment emits webhooks by default (OUTBOX_TRANSPORT is "logging"), so
// without it there would be nothing to receive. The SDK deliberately ships no
// signing function: a consumer that can sign can forge.
func printTryItCommands(addr, secret string) {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(sampleEnvelope))
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	url := "http://" + addr + "/webhooks/octonomy"
	fmt.Printf("listening on %s\n\n", url)
	fmt.Printf("genuine delivery (expect 204):\n  curl -si -X POST %s \\\n    -H '%s: %s' \\\n    -d '%s'\n\n",
		url, webhook.HeaderSignature, signature, sampleEnvelope)

	// The same signature over a body altered after signing. The change is
	// length-preserving and leaves valid JSON behind, so nothing downstream of
	// the signature check would have noticed it.
	tampered := strings.Replace(sampleEnvelope, `"tenant_id":"acme"`, `"tenant_id":"evil"`, 1)
	fmt.Printf("tampered delivery (expect 401):\n  curl -si -X POST %s \\\n    -H '%s: %s' \\\n    -d '%s'\n\n",
		url, webhook.HeaderSignature, signature, tampered)
	fmt.Println("Ctrl-C to stop.")
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
