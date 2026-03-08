// examples/ollama/main.go — one-process-per-agent Ollama gateway built on herd.
//
// # What this does
//
// Starts a pool of `ollama serve` processes — each pinned to one X-Agent-ID.
// Any HTTP request carrying X-Agent-ID: <id> is always routed to the SAME
// Ollama instance, giving each agent its own LLM context/conversation state.
//
// Useful for multi-agent setups where:
//   - each agent needs isolated model state (KV cache, loaded adapters, etc.)
//   - you don't want to run ollama pull on every request
//   - you want scale-to-zero when no agents are active
//
// # Run
//
//	go run . --port 8080 --min 1 --max 5
//
// # Example request
//
//	curl -X POST http://localhost:8080/api/chat \
//	  -H "X-Agent-ID: agent-42" \
//	  -H "Content-Type: application/json" \
//	  -d '{"model":"llama3","messages":[{"role":"user","content":"Hello!"}]}'
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
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
	minWorkers := flag.Int("min", 1, "minimum ollama workers kept alive")
	maxWorkers := flag.Int("max", 5, "maximum concurrent ollama workers")
	port := flag.Int("port", 8080, "gateway listen port")
	modelsDir := flag.String("models", "/Users/sankalpnarula/.ollama/models", "shared models directory (read-only mount)")
	ttl := flag.Duration("ttl", 10*time.Minute, "idle agent TTL before its worker is reclaimed")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	// ── Factory ────────────────────────────────────────────────────────────
	// Each worker is an `ollama serve` process listening on its own port.
	//
	// OLLAMA_HOST  tells ollama which address to bind — {{.Port}} is replaced
	//              by herd with the OS-allocated free port at spawn time.
	// OLLAMA_MODELS points all instances at a shared read-only model cache so
	//              you only ever pull a model once.
	// CUDA_VISIBLE_DEVICES is intentionally left empty here; set it if you
	//              want to pin different workers to different GPUs.
	factory := herd.NewProcessFactory("ollama", "serve").
		WithEnv("OLLAMA_HOST=127.0.0.1:{{.Port}}").
		WithEnv("OLLAMA_MODELS=" + *modelsDir).
		WithHealthPath("/"). // ollama: GET / → 200 "Ollama is running"
		WithStartTimeout(2 * time.Minute).
		WithMemoryLimit(1024 * 1024 * 512) // 512MB
		

	// ── Pool ───────────────────────────────────────────────────────────────
	pool, err := herd.New(factory,
		herd.WithAutoScale(*minWorkers, *maxWorkers),
		herd.WithTTL(*ttl),
		herd.WithCrashHandler(func(agentID string) {
			// An ollama worker crashed while serving an agent.
			// Log it — in production you might also notify the agent or
			// clear any external state tied to that agent's conversation.
			log.Printf("[ALERT] ollama worker for agent %q crashed — conversation state lost", agentID)
		}),
	)
	if err != nil {
		log.Fatalf("failed to start pool: %v", err)
	}

	// ── Routes ─────────────────────────────────────────────────────────────
	mux := http.NewServeMux()

	// Main proxy — all Ollama API requests (POST /api/chat, POST /api/generate,
	// GET /api/tags, etc.) are forwarded to the worker pinned to X-Agent-ID.
	//
	// proxy.NewReverseProxy handles the full pipe: it calls pool.Acquire,
	// proxies the request byte-for-byte (including streaming responses), and
	// releases the worker back to the pool when the response is done.
	mux.Handle("/api/", proxy.NewReverseProxy(pool, func(r *http.Request) string {
		agentID := r.Header.Get("X-Agent-ID")
		if agentID == "" {
			// Fall back to a default agent so bare requests still work.
			// In production you might reject missing headers instead.
			agentID = "default"
		}
		return agentID
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
	log.Printf("[ollama-gateway] listening on %s  (min=%d max=%d ttl=%s models=%s)",
		addr, *minWorkers, *maxWorkers, *ttl, *modelsDir)
	log.Fatal(http.ListenAndServe(addr, mux))
}

// ---------------------------------------------------------------------------
// proxyAll — fallback if proxy.NewReverseProxy is unavailable
// ---------------------------------------------------------------------------
// (kept as a reference; the ReverseProxy from herd/proxy handles streaming
//  responses, which a naïve io.ReadAll-based helper cannot.)

func proxyAll(w http.ResponseWriter, r *http.Request, target string) {
	url := target + r.URL.RequestURI()

	outReq, err := http.NewRequestWithContext(r.Context(), r.Method, url, r.Body)
	if err != nil {
		http.Error(w, "bad gateway: "+err.Error(), http.StatusBadGateway)
		return
	}
	outReq.Header = r.Header.Clone()

	resp, err := http.DefaultClient.Do(outReq)
	if err != nil {
		http.Error(w, "bad gateway: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body) //nolint:errcheck
}
