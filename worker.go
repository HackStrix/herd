// Package herd provides a session-affine process pool for Go.
//
// # Design
//
// herd manages a pool of OS subprocess "workers" and routes incoming requests
// to the correct worker by an arbitrary string sessionID. The key invariant is:
//
//	1 session → 1 worker, for the lifetime of the session.
//
// This means a session always arrives at the same process — enabling stateful
// binaries (browsers, LLMs, REPLs) to be turned into multi-tenant services
// without any external coordination layer.
//

package herd

import (
	"context"
	"io"
)

// ---------------------------------------------------------------------------
// Worker[C]
// ---------------------------------------------------------------------------

// Worker represents one running subprocess managed by the pool.
//
// C is the typed client the caller uses to talk to the subprocess — for
// example *http.Client, a gRPC connection, or a custom struct.
// The type parameter is constrained to "any" so the pool is fully generic.
type Worker[C any] interface {
	// ID returns a stable, unique identifier for this worker (e.g. "worker-3").
	// Never reused — not even after a crash and restart.
	ID() string

	// Address returns the internal network URI the worker
	// is listening on (e.g., '127.0.0.1:54321').
	Address() string

	// Client returns the typed connection to the worker process.
	// For most users this is *http.Client; gRPC users return their stub here.
	Client() C

	// Healthy performs a liveness check against the subprocess.
	// Returns nil if the worker is accepting requests; non-nil otherwise.
	// Pool.Acquire calls this before handing a worker to a new session,
	// so a stale or crashed worker is never returned to a caller.
	Healthy(ctx context.Context) error

	// Close performs graceful shutdown of the worker process.
	// Called by the pool during scale-down or Pool.Shutdown.
	io.Closer
}

// ---------------------------------------------------------------------------
// WorkerFactory[C]
// ---------------------------------------------------------------------------

// WorkerFactory knows how to spawn one worker process and return a typed
// Worker[C] that is ready to accept requests (i.e. Healthy returns nil).
//
// Most users never implement this interface — they use NewProcessFactory
// instead. Implement WorkerFactory only if you need custom spawn logic
// (e.g. Firecracker microVM, Docker container, remote SSH process).
type WorkerFactory[C any] interface {
	// Spawn starts one new worker and blocks until it is healthy.
	// If ctx is cancelled before the worker becomes healthy, Spawn must
	// kill the process and return a non-nil error.
	Spawn(ctx context.Context) (Worker[C], error)
}
