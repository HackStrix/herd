// Package proxy provides NewReverseProxy — the one-liner that turns a
// session-affine process pool into an HTTP gateway.
//
// # Why this exists
//
// Pool.Acquire gives you a *Session. But in the common case — proxying HTTP
// traffic to a subprocess — you still need to write the reverse-proxy loop
// yourself: acquire a session, build a *httputil.ReverseProxy pointing at
// session.Worker.Address(), forward the request, handle errors. That's 30
// lines every user copy-pastes. NewReverseProxy collapses it to one call.
//
// # Usage
//
//	pool, _ := herd.New(herd.NewProcessFactory("./my-binary", "--port", "{{.Port}}"))
//
//	proxy := proxy.NewReverseProxy(pool, func(r *http.Request) string {
//	    return r.Header.Get("X-Session-ID")
//	})
//
//	http.ListenAndServe(":8080", proxy)
//
// # Session lifecycle
//
// NewReverseProxy acquires a session at the START of each HTTP request and
// releases it at the END (after the response is written). This means a single
// HTTP request holds a worker exclusively for its duration — appropriate for
// request-scoped work (a browser API call, an LLM inference call, a REPL eval).
//
// For long-lived sessions where the same sessionID should stay pinned across
// many requests — e.g. a stateful REPL session that must keep the same process
// — callers should call Pool.Acquire / Session.Release directly and store the
// *Session in their own state (e.g. an HTTP session cookie → in-memory map).
//
// # Error handling
//
// If extractSessionID returns an empty string, ServeHTTP returns 400.
// If Pool.Acquire fails (timeout, all workers crashed), ServeHTTP returns 503.
// If the upstream subprocess returns a non-2xx, it is forwarded as-is — the
// proxy does not interfere with application-level error codes.
//
// # File layout
//
//	proxy/proxy.go   — NewReverseProxy + ReverseProxy.ServeHTTP (THIS FILE)
package proxy

import (
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"

	"github.com/hackstrix/herd"
)

// ---------------------------------------------------------------------------
// ReverseProxy[C]
// ---------------------------------------------------------------------------

// ReverseProxy is an http.Handler that acquires a session from pool, proxies
// the request to the worker's address, and releases the session when done.
//
// C is the worker client type — for ProcessFactory this is *http.Client.
// ReverseProxy does not use C directly; it proxies via the worker's Address().
type ReverseProxy[C any] struct {
	pool             *herd.Pool[C]
	extractSessionID func(*http.Request) string
}

// NewReverseProxy returns an http.Handler that:
//  1. Calls extractSessionID(r) to determine which session this request belongs to.
//  2. Calls pool.Acquire(ctx, sessionID) to get (or create) the pinned worker.
//  3. Reverse-proxies the request to worker.Address().
//  4. Calls session.Release() after the response is written.
//
// extractSessionID may inspect any part of the request — a header, a cookie,
// a path prefix, or a query parameter. It must return a non-empty string.
//
// Example: route by X-Session-ID header:
//
//	proxy := proxy.NewReverseProxy(pool, func(r *http.Request) string {
//	    return r.Header.Get("X-Session-ID")
//	})
func NewReverseProxy[C any](
	pool *herd.Pool[C],
	extractSessionID func(*http.Request) string,
) *ReverseProxy[C] {
	return &ReverseProxy[C]{
		pool:             pool,
		extractSessionID: extractSessionID,
	}
}

// ServeHTTP implements http.Handler.
//
// Steps:
//  1. Extract sessionID — return 400 if empty.
//  2. Acquire session — return 503 if pool is exhausted or ctx cancelled.
//  3. Parse worker address into *url.URL.
//  4. Build a per-request httputil.ReverseProxy targeting that URL.
//  5. Forward request; on response write, release the session.
func (rp *ReverseProxy[C]) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sessionID := rp.extractSessionID(r)
	if sessionID == "" {
		http.Error(w, "herd: missing session ID", http.StatusBadRequest)
		return
	}

	sess, err := rp.pool.Acquire(r.Context(), sessionID)
	if err != nil {
		log.Printf("[proxy] Acquire(%q) failed: %v", sessionID, err)
		http.Error(w, fmt.Sprintf("herd: could not acquire worker: %v", err), http.StatusServiceUnavailable)
		return
	}

	// no session release here
	// We should not be releasing the session here. session should only be release at
	// ttl or if the health checks fails and we are cleaning up

	target, err := url.Parse(sess.Worker.Address())
	if err != nil {
		http.Error(w, fmt.Sprintf("herd: bad worker address: %v", err), http.StatusInternalServerError)
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(target)

	// Custom error handler so upstream failures surface a clean 502 rather
	// than the default silent drop.
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("[proxy] upstream error (session=%q worker=%s): %v", sessionID, sess.Worker.ID(), err)
		http.Error(w, fmt.Sprintf("herd: upstream error: %v", err), http.StatusBadGateway)
	}

	proxy.ServeHTTP(w, r)
}
