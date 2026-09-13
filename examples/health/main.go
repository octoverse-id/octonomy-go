// Command health probes liveness and readiness with no credentials at all, and
// shows the distinction a probe loop is built on: a server that did not answer
// is not the same as one that answered "not ready".
//
//	make dev-server         # boots a real Octonomy and prints these exports
//	go run ./examples/health
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	octonomy "github.com/octoverse-id/octonomy-go/v2"
)

func main() {
	baseURL := env("OCTONOMY_BASE_URL", "http://127.0.0.1:8000")

	// The probes sit at the SERVER ROOT, outside /api/<version>, and
	// authenticate nobody -- so there is a constructor that takes no token and
	// no tenant. A base URL is the whole configuration.
	probe, err := octonomy.NewHealthClient(baseURL,
		// A probe loop wants a much shorter timeout than the 30s default: a
		// readiness check that hangs for half a minute has already answered the
		// question it was asked.
		octonomy.WithHealthHTTPClient(&http.Client{Timeout: 2 * time.Second}),
	)
	if err != nil {
		log.Fatalf("configure probe: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Live says the process is up. Ready says it can serve -- it opens a
	// database cursor, so it is the one a load balancer should ask.
	live, err := probe.Health.Live(ctx)
	if err != nil {
		log.Fatalf("live: %v", err)
	}
	ready, err := probe.Health.Ready(ctx)
	if err != nil {
		log.Fatalf("ready: %v", err)
	}
	fmt.Printf("live=%s ready=%s\n", live.Status, ready.Status)

	// UNREACHABLE AND UNREADY MUST NOT COLLAPSE. Nothing answered here, so the
	// error wraps ErrUnreachable and there is no *APIError to inspect: the
	// server may be starting, gone, or behind a broken route. A server that
	// ANSWERS a probe with a non-2xx and its own {"status": ...} body is an
	// *APIError carrying CodeNotReady instead -- it is up, and it is telling you
	// it cannot serve. Only the second is worth waiting out.
	closed, err := octonomy.NewHealthClient("http://127.0.0.1:1",
		octonomy.WithHealthHTTPClient(&http.Client{Timeout: 2 * time.Second}))
	if err != nil {
		log.Fatalf("configure the unreachable probe: %v", err)
	}
	_, err = closed.Health.Ready(ctx)
	apiErr, isAPI := octonomy.AsAPIError(err)
	fmt.Printf("nothing listening: ErrUnreachable=%v APIError=%v IsNotReady=%v\n",
		errors.Is(err, octonomy.ErrUnreachable), isAPI, octonomy.IsNotReady(err))

	// The branch a probe loop actually writes.
	switch {
	case err == nil:
		fmt.Println("would report: serving")
	case octonomy.IsNotReady(err):
		fmt.Printf("would report: up but not serving (%s)\n", apiErr.Message)
	case errors.Is(err, octonomy.ErrUnreachable):
		fmt.Println("would report: no answer -- starting, gone, or misrouted")
	default:
		fmt.Printf("would report: unexpected -- %v\n", err)
	}

	// A fully configured Client reaches the same routes through Client.Health,
	// and sends a byte-for-byte identical request: no Authorization header, no
	// X-Tenant-ID, no version prefix. The credentials it holds go nowhere here.
	if token := os.Getenv("OCTONOMY_TOKEN"); token != "" {
		client, err := octonomy.New(octonomy.Config{
			BaseURL:  baseURL,
			Token:    token,
			TenantID: mustEnv("OCTONOMY_TENANT_ID"),
		})
		if err != nil {
			log.Fatalf("configure client: %v", err)
		}
		status, err := client.Health.Ready(ctx)
		if err != nil {
			log.Fatalf("ready via the full client: %v", err)
		}
		fmt.Printf("via the full client: ready=%s\n", status.Status)
	}
}

// --- configuration ------------------------------------------------------------
//
// The probes need no credentials, so this example needs only OCTONOMY_BASE_URL.
// `make dev-server` prints it along with the rest.

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("%s is not set -- run `make dev-server` and export the block it prints", key)
	}
	return v
}
