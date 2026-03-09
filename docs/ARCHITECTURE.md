# Herd Architecture

> **tl;dr** — Herd is a session-affine process pool with a built-in reverse proxy.  
> One session ID always routes to the same OS process. No container orchestration required.

---

## A. Visual Flow

The diagram below shows the full lifecycle of an HTTP request through `NewReverseProxy`.

```mermaid
sequenceDiagram
    participant Client as HTTP Client
    participant Proxy as ReverseProxy<br/>(proxy/proxy.go)
    participant Pool as Pool[C]<br/>(pool.go)
    participant Worker as processWorker<br/>(process_worker_factory.go)

    Client->>Proxy: POST /api  X-Session-ID: abc123

    Proxy->>Pool: Acquire(ctx, "abc123")

    alt Session already pinned (fast path)
        Pool-->>Proxy: &Session{Worker: w}
    else First request for this sessionID (slow path)
        Pool->>Pool: register inflight["abc123"] chan
        Pool->>Worker: pop from available channel
        Pool->>Worker: w.Healthy(ctx) — health check
        Pool->>Pool: sessions["abc123"] = w
        Pool->>Pool: close(inflight["abc123"]) — broadcast
        Pool-->>Proxy: &Session{Worker: w}
    else Concurrent duplicate session (singleflight wait)
        Pool->>Pool: <-inflight["abc123"] — wait for broadcast
        Pool-->>Proxy: &Session{Worker: w}
    end

    Proxy->>Worker: httputil.ReverseProxy → worker.Address()
    Worker-->>Proxy: HTTP response
    Proxy->>Pool: session.Release() — worker returned to available
    Proxy-->>Client: HTTP response
```

### Crash Path

```mermaid
sequenceDiagram
    participant Monitor as monitor() goroutine
    participant Pool as Pool[C]
    participant App as Your App

    Monitor->>Monitor: cmd.Wait() unblocks (process died)
    Monitor->>Pool: onCrash("abc123")
    Pool->>Pool: delete sessions["abc123"]
    Pool->>Pool: close inflight chan (unblocks any waiters)
    Pool->>Pool: maybeScaleUp() → spawn replacement worker
    Pool->>App: crashHandler("abc123") [if set]
```

---

## B. Component Breakdown

### `WorkerFactory[C]` — The Engine
Defined in [`worker.go`](../worker.go). This is the **only component that touches the OS**.  
`ProcessFactory` (the default implementation in `process_worker_factory.go`) calls `exec.Cmd`, allocates a free port via `net.Listen("tcp", "0.0.0.0:0")`, injects the port as `{{.Port}}` in args and via the `PORT` env var, then polls `GET /health` every 200ms until the process is ready.

Implement `WorkerFactory[C]` yourself only if you need custom spawn logic — Firecracker microVMs, Docker containers, remote SSH processes.

### `Pool[C]` & `Session[C]` — The Brain
Defined in [`pool.go`](../pool.go). Enforces the core invariant: **1 sessionID → 1 Worker, for the lifetime of the session**.

The singleflight lock exists to handle a specific race: if two HTTP requests for `sessionID="abc123"` arrive at exactly the same millisecond, without the lock they could both pop workers from `available` and pin *different* workers to the same session — breaking affinity. The `inflight` map + channel broadcast ensures only the *first* goroutine does the slow-path acquisition; all others wait and then receive the same result.

### `ReverseProxy[C]` — The Front Door
Defined in [`proxy/proxy.go`](../proxy/proxy.go). Intercepts HTTP requests, calls your `extractSessionID` function to determine which session this request belongs to, pauses to acquire the correct worker via `Pool.Acquire`, reverse-proxies the traffic using `httputil.ReverseProxy`, and releases the worker after the response is fully written.

A pool without a router is just a glorified map. `NewReverseProxy` is the one-liner that closes this gap.

---

## C. Design Tenets

### Why 1:1 Session Affinity?
**To isolate blast radius.** If a process dies, only *one* user's session is affected — not the entire pool. This is the session-affine model: a process is exclusively owned by a single sessionID for its lifetime, rather than being shared across requests (connection-pool style).

### Why Processes, Not Goroutines?
**Because Herd manages external stateful binaries, not Go routines.** The processes it manages — Ollama, headless Chromium, a Python REPL — carry state in OS memory, open file descriptors, and GPU contexts that cannot be represented as a Go struct. You cannot checkpoint and restore a subprocess the way you can restart a goroutine.

### Why the Built-in Proxy?
**Because a pool without a router is just a glorified map.** Every user of a process pool eventually writes the same acquire-proxy-release loop. `NewReverseProxy` collapses that into a single `http.Handler` so you spend zero lines on plumbing and all your lines on your application.

---

## File Map

| File | Responsibility |
|------|---------------|
| `worker.go` | `Worker[C]` + `WorkerFactory[C]` interfaces |
| `process_worker_factory.go` | `ProcessFactory` — default OS subprocess factory |
| `pool.go` | `Pool[C]` — singleflight session router |
| `ttl.go` | TTL sweep loop — idle session eviction |
| `options.go` | Functional options (`WithAutoScale`, `WithTTL`, …) |
| `proxy/proxy.go` | `NewReverseProxy` — HTTP gateway |
