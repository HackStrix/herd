// examples/playwright/main.go — one-process-per-session Playwright gateway built on herd.
//
// # What this does
//
// Starts a pool of `npx playwright run-server` processes — each pinned to one X-Session-ID.
// Any connection carrying X-Session-ID: <id> is always routed to the SAME
// Playwright instance, giving each user their own browser session.
//
// # Run
//
//	go run . --port 8080 --min 1 --max 5
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hackstrix/herd"
	"github.com/hackstrix/herd/proxy"
)

func main() {
	minWorkers := flag.Int("min", 1, "minimum playwright workers kept alive")
	maxWorkers := flag.Int("max", 5, "maximum concurrent playwright workers")
	port := flag.Int("port", 8080, "gateway listen port")
	ttl := flag.Duration("ttl", 10*time.Second, "idle session TTL before the worker is reclaimed")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	// ── Factory ────────────────────────────────────────────────────────────
	// Each worker is a `npx playwright run-server` process listening on its own port.
	//
	// We use the CLI directly inside ProcessFactory:
	// npx playwright run-server --port {{.Port}} --host 127.0.0.1
	factory := herd.NewProcessFactory(
		"npx", "playwright", "run-server",
		"--port", "{{.Port}}",
		"--host", "127.0.0.1",
		).
		WithHealthPath("/").
		WithStartTimeout(1 * time.Minute).
		WithStartHealthCheckDelay(500 * time.Millisecond)

	// ── Pool ───────────────────────────────────────────────────────────────
	// To make a bulletproof multi-tenant tool and avoid shared fate, state leaks,
	// and resource throttling, Herd spawns a fresh Playwright server per Session ID.
	pool, err := herd.New(factory,
		herd.WithAutoScale(*minWorkers, *maxWorkers),
		herd.WithTTL(*ttl),
		herd.WithCrashHandler(func(sessionID string) {
			log.Printf("[ALERT] playwright worker for session %q crashed", sessionID)
		}),
		herd.WithWorkerReuse(false), // Never reuse workers for browsers to prevent state leaks
	)
	if err != nil {
		log.Fatalf("failed to start pool: %v", err)
	}

	// ── Routes ─────────────────────────────────────────────────────────────
	mux := http.NewServeMux()

	// Main proxy — Reverse proxy for Playwright websocket connections and HTTP endpoints.
	// When a user hits this proxy with 'X-Session-ID: user-1', Herd routes them
	// to a dedicated Playwright instance just for them.
	mux.Handle("/", proxy.NewReverseProxy(pool, func(r *http.Request) string {
		sessionID := r.Header.Get("X-Session-ID")
		if sessionID == "" {
			// Fallback session if none provided
			sessionID = "default"
		}
		return sessionID
	}))

	// /health — simple liveness probe for load-balancers
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	})

	// /status — pool utilisation snapshot for dashboards / alerting
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		s := pool.Stats()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"total":%d,"available":%d,"active_sessions":%d,"inflight":%d}`+"\n",
			s.TotalWorkers, s.AvailableWorkers, s.ActiveSessions, s.InflightAcquires)
	})

	// ── Graceful shutdown ──────────────────────────────────────────────────
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
		sig := <-ch
		log.Printf("received %s — shutting down", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = pool.Shutdown(ctx)
		os.Exit(0)
	}()

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("[playwright-gateway] listening on %s  (min=%d max=%d ttl=%s)",
		addr, *minWorkers, *maxWorkers, *ttl)
	log.Fatal(http.ListenAndServe(addr, mux))
}
