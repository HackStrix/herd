// pool.go — Session-affine process pool with singleflight Acquire.
//
// # Core invariant
//
//	1 sessionID → 1 Worker, for the lifetime of the session.
//
// # Concurrency model (read this before touching Acquire)
//
// There are three maps protected by a single mutex (p.mu):
//
//	p.sessions  map[string]Worker[C]       — live sessionID → worker bindings
//	p.inflight  map[string]chan struct{}    — in-progress Acquire for a session
//
// And one lock-free channel:
//
//	p.available chan Worker[C]             — free workers ready to be assigned
//
// The singleflight guarantee for Acquire(ctx, sessionID):
//
//  1. Lock → check sessions → if found: unlock, return (FAST PATH).
//  2. Lock → check inflight → if pending: grab chan, unlock, wait on it,
//     then restart from step 1 when chan closes.
//  3. Lock → create inflight[sessionID] = make(chan struct{}) → unlock.
//  4. Block on <-p.available (or ctx cancel).
//  5. Call w.Healthy(ctx). If unhealthy: discard, close inflight chan, return error.
//  6. Lock → sessions[sessionID]=w, delete inflight[sid], close(ch) → unlock.
//     Closing ch broadcasts to all goroutines waiting in step 2.
//  7. Return &Session[C]{…}
//
// Why a chan struct{} instead of sync.Mutex per session?
//   - A mutex would only let one waiter in. We need ALL waiters to unblock when
//     the acquiring goroutine completes (step 6 closes ch → zero-copy broadcast).
//   - Closing a channel is safe to call exactly once and is always non-blocking.
//
// # Session.Release
//
// Session.Release removes the entry from p.sessions and pushes the worker back
// onto p.available. It does NOT close or kill the worker — the binary keeps
// running and will be assigned to the next caller.
//
// # Crash path
//
// processWorker.monitor() calls p.onCrash(sessionID) when it detects that the
// subprocess exited while holding a session. onCrash:
//  1. Removes the session from p.sessions.
//  2. If there is a pending inflight chan for the same sessionID, closes it so
//     any goroutine waiting in step 2 of Acquire unblocks (they will then get
//     an error because Acquire finds neither a session nor a valid inflight).
//  3. Calls the user-supplied crashHandler if set.
package herd

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Session[C]
// ---------------------------------------------------------------------------

// Session is a scoped handle returned by Pool.Acquire.
//
// It binds one sessionID to one worker for the duration of the session.
// Call Release when the session is done — this frees the worker so it can
// be assigned to the next sessionID. Failing to call Release leaks a worker.
//
// A Session is NOT safe for concurrent use by multiple goroutines. Multiple
// HTTP requests for the same sessionID should each call Acquire independently;
// the pool guarantees they always receive the same underlying worker.
type Session[C any] struct {
	// ID is the sessionID that was passed to Pool.Acquire.
	ID string

	// Worker is the underlying worker pinned to this session.
	// Use Worker.Client() to talk to the subprocess.
	Worker Worker[C]

	pool *Pool[C]
	once sync.Once
}

// Release removes the session from the affinity map and returns the worker to
// the available pool. After Release, the worker may be assigned to a different
// sessionID. Calling Release more than once is a no-op.
func (s *Session[C]) Release() {
	s.once.Do(func() {
		s.pool.release(s.ID, s.Worker)
	})
}

// ---------------------------------------------------------------------------
// PoolStats
// ---------------------------------------------------------------------------

// PoolStats is a point-in-time snapshot of pool state for dashboards / alerts.
type PoolStats struct {
	// TotalWorkers is the number of workers currently registered in the pool
	// (starting + healthy + busy). Does not count workers being scaled down.
	TotalWorkers int

	// AvailableWorkers is the number of idle workers ready to accept a new session.
	AvailableWorkers int

	// ActiveSessions is the number of sessionID → worker bindings currently live.
	ActiveSessions int

	// InflightAcquires is the number of Acquire calls currently in the "slow path"
	// (waiting for a worker to become available). Useful for queue-depth alerting.
	InflightAcquires int
}

// ---------------------------------------------------------------------------
// Pool[C]
// ---------------------------------------------------------------------------

// Pool manages a set of workers and routes requests by sessionID.
// Create one with New[C].
type Pool[C any] struct {
	factory WorkerFactory[C]
	cfg     config

	mu           sync.Mutex
	pendingAdds  int
	sessions     map[string]Worker[C]     // sessionID → pinned worker
	inflight     map[string]chan struct{} // sessionID → broadcast channel
	lastAccessed map[string]time.Time     // sessionID → last Acquire time (for TTL)

	workers   []Worker[C]    // all known workers (for Stats / Shutdown)
	available chan Worker[C] // free workers

	wg   sync.WaitGroup
	done chan struct{} // closed when the pool is shutting down, all the background loops will listen for this

	// pool context for canceling all the background loops
	// must be canceled when the pool is shutting down
	ctx    context.Context 
    cancel context.CancelFunc
}

// New creates a pool backed by factory, applies opts, and starts min workers.
// Returns an error if any of the initial workers fail to start.
func New[C any](factory WorkerFactory[C], opts ...Option) (*Pool[C], error) {
	cfg := defaultConfig()
	
	// Using functional options pattern
	for _, apply_options := range opts {
		apply_options(&cfg)
	}

	ctx, cancel := context.WithCancel(context.Background())

	p := &Pool[C]{
		factory:      factory,
		cfg:          cfg,
		sessions:     make(map[string]Worker[C]),
		inflight:     make(map[string]chan struct{}),
		lastAccessed: make(map[string]time.Time),
		workers:      make([]Worker[C], 0, cfg.max),
		available:    make(chan Worker[C], cfg.max),
		done:         make(chan struct{}),
		ctx:          ctx,
		cancel:       cancel,
	}

	// Start the minimum number of workers synchronously.
	// All min workers must be healthy before New returns.
	for i := 0; i < cfg.min; i++ {
		w, err := factory.Spawn(context.Background())
		if err != nil {
			// Best-effort cleanup of already-started workers
			for _, started := range p.workers {
				_ = started.Close()
			}
			return nil, fmt.Errorf("herd: New: failed to start initial worker %d: %w", i, err)
		}
		p.wireWorker(w)
	}

	// Background loops
	if cfg.healthInterval > 0 {
		go p.healthCheckLoop()
	}

	if cfg.ttl > 0 {
		go p.runTTLSweep()
	}

	// TODO: check if we need crash monitoring loop here

	return p, nil
}

// wireWorker registers w in the pool and wires its crash callback.
// Must be called with p.mu NOT held.
func (p *Pool[C]) wireWorker(w Worker[C]) {
	// Wire crash callback if the underlying worker supports it
	// (i.e. it is a *processWorker from ProcessFactory).
	if pw, ok := any(w).(*processWorker); ok {
		pw.onCrash = func(sessionID string) {
			p.onCrash(sessionID)
		}
	}
	p.mu.Lock()
	p.workers = append(p.workers, w)
	p.mu.Unlock()

	// Push onto available immediately — the worker is already healthy
	p.available <- w
}

// ---------------------------------------------------------------------------
// Acquire — the core primitive
// ---------------------------------------------------------------------------

// Acquire returns the Worker pinned to sessionID.
//
// If sessionID already has a worker, it is returned immediately (fast path).
// If sessionID is new, a free worker is popped from the available channel,
// health-checked, and pinned to the session.
//
// If another goroutine is currently acquiring the same sessionID, this call
// blocks until that acquisition completes and then returns the same worker
// (singleflight guarantee — no two goroutines can pin different workers to
// the same sessionID simultaneously).
//
// Blocks until a worker is available or ctx is cancelled.
func (p *Pool[C]) Acquire(ctx context.Context, sessionID string) (*Session[C], error) {
	for {
		p.mu.Lock()

		// ── FAST PATH ──────────────────────────────────────────────────────
		// Session already pinned: return the existing worker immediately.
		// Also update the last-accessed time so the TTL sweeper doesn't
		// evict an actively-used session.
		if w, ok := p.sessions[sessionID]; ok {
			p.touchSession(sessionID)
			p.mu.Unlock()
			return &Session[C]{ID: sessionID, Worker: w, pool: p}, nil
		}

		// ── SINGLEFLIGHT WAIT ──────────────────────────────────────────────
		// Another goroutine is already acquiring this sessionID.
		// Grab its broadcast channel, unlock, and wait for it to finish.
		if ch, pending := p.inflight[sessionID]; pending {
			p.mu.Unlock()
			select {
			case <-ch:
				// The acquiring goroutine finished (it closed ch).
				// Loop back to check sessions — we will hit the fast path
				// if it succeeded, or find no entry if it crashed/errored.
				continue
			case <-ctx.Done():
				return nil, fmt.Errorf("herd: Acquire(%q): context cancelled while waiting for inflight: %w", sessionID, ctx.Err())
			}
		}

		// ── SLOW PATH ──────────────────────────────────────────────────────
		// We are the first goroutine for this sessionID.
		// Register an inflight channel so concurrent callers wait on us.
		ch := make(chan struct{})
		p.inflight[sessionID] = ch
		p.mu.Unlock()

		// Try to scale up if pool is exhausted but below ceiling.
		p.maybeScaleUp()

		// Block until a free worker arrives or we time out.
		var w Worker[C]
		select {
		case w = <-p.available:
		case <-ctx.Done():
			// We failed to get a worker before the context expired.
			// Close the broadcast channel so any goroutines waiting on
			// this sessionID unblock and return their own error.
			p.mu.Lock()
			delete(p.inflight, sessionID)
			p.mu.Unlock()
			close(ch)
			return nil, fmt.Errorf("herd: Acquire(%q): timed out waiting for available worker: %w", sessionID, ctx.Err())
		}

		// ── HEALTH CHECK ───────────────────────────────────────────────────
		// Verify the worker is still alive before handing it to a session.
		// This prevents giving a dead handle to the caller (and to any
		// goroutines waiting on the inflight channel).
		hCtx, hCancel := context.WithTimeout(ctx, 3*time.Second)
		err := w.Healthy(hCtx)
		hCancel()

		if err != nil {
			log.Printf("[pool] Acquire(%q): worker %s failed health check: %v — discarding", sessionID, w.ID(), err)
			_ = w.Close()
			p.removeWorker(w)
			// Unblock any waiters — they will loop and receive the same error
			p.mu.Lock()
			delete(p.inflight, sessionID)
			p.mu.Unlock()
			close(ch)
			return nil, fmt.Errorf("herd: Acquire(%q): worker %s unhealthy: %w", sessionID, w.ID(), err)
		}

		// Pin the worker to this session, record access time, and broadcast.
		p.mu.Lock()
		p.sessions[sessionID] = w
		p.lastAccessed[sessionID] = time.Now()
		delete(p.inflight, sessionID)
		p.mu.Unlock()

		close(ch) // broadcast: all goroutines blocked in SINGLEFLIGHT WAIT unblock

		log.Printf("[pool] Acquire(%q): pinned to worker %s", sessionID, w.ID())
		return &Session[C]{ID: sessionID, Worker: w, pool: p}, nil
	}
}

// ---------------------------------------------------------------------------
// release — called by Session.Release
// ---------------------------------------------------------------------------

// release removes the session → worker binding and returns the worker to the
// available channel. Internal; external callers use Session.Release().
func (p *Pool[C]) release(sessionID string, w Worker[C]) {
	p.mu.Lock()
	delete(p.sessions, sessionID)
	delete(p.lastAccessed, sessionID)
	// Validate the worker wasn't evicted by a crash or health check
	isValid := false
	for _, existing := range p.workers {
		if existing.ID() == w.ID() {
			isValid = true
            break
        }
    }
	p.mu.Unlock()

	if !isValid {
		log.Printf("[pool] release(%q): worker %s returned to pool", sessionID, w.ID())
		return
	}

	log.Printf("[pool] release(%q): worker %s returned to pool", sessionID, w.ID())

	// Non-blocking push: if the channel is somehow already full (shouldn't
	// happen with 1-session-per-worker) log and drop rather than deadlock.
	select {
	case p.available <- w:
	default:
		log.Printf("[pool] release(%q): available channel full — this is a bug", sessionID)
	}
}

// ---------------------------------------------------------------------------
// onCrash — called by processWorker.monitor on unexpected exit
// ---------------------------------------------------------------------------

// onCrash handles a worker process exiting unexpectedly while holding a session.
// It cleans up the session map and any pending inflight channel for the same
// sessionID, then calls the user-supplied crash handler.
func (p *Pool[C]) onCrash(sessionID string) {
	p.mu.Lock()
	w, hadSession := p.sessions[sessionID]
	delete(p.sessions, sessionID)

	// If another Acquire is in-flight for this sessionID, close its channel
	// so the waiting goroutine unblocks and returns an error rather than
	// hanging indefinitely.
	if ch, pending := p.inflight[sessionID]; pending {
		delete(p.inflight, sessionID)
		close(ch)
	}
	p.mu.Unlock()

	if hadSession {
		log.Printf("[pool] onCrash(%q): worker %s crashed — session lost", sessionID, w.ID())
		p.removeWorker(w)
	}

	if p.cfg.crashHandler != nil {
		p.cfg.crashHandler(sessionID)
	}

	// Replace the lost worker to keep the pool at min capacity
	p.maybeScaleUp()
}

// ---------------------------------------------------------------------------
// Scale helpers
// ---------------------------------------------------------------------------

// maybeScaleUp fires addWorker in a goroutine if the pool is below its ceiling
// and there are no free workers. Uses the workers slice length + a pendingAdds
// counter to avoid overshooting max under concurrent scale-up pressure.
func (p *Pool[C]) maybeScaleUp() {
	p.mu.Lock()
	// p.available should be read with lock to avoid race conditions
	if len(p.available) > 0 {
		p.mu.Unlock()
		return
	}
	total := len(p.workers) + p.pendingAdds
	if total < p.cfg.max {
		p.pendingAdds++
		p.mu.Unlock()
		go p.addWorker()
		return
	}
	p.mu.Unlock()
}

// addWorker spawns one new worker and registers it. Runs in its own goroutine.
func (p *Pool[C]) addWorker() {
	defer func() {
		p.mu.Lock()
		p.pendingAdds--
		p.mu.Unlock()
	}()
	
	// if the factory spawn is over the network or some other external call it will hang and leak memory
	// if it cant be resolved within 60 sec cancel the operation and reclaim resources
	ctx, cancel := context.WithTimeout(p.ctx, 60*time.Second)
	defer cancel()	

	w, err := p.factory.Spawn(ctx)
	if err != nil {
		log.Printf("[pool] scale-up failed: %v", err)
		return
	}
	p.wireWorker(w)
	log.Printf("[pool] scale-up: worker %s added", w.ID())
}

// removeWorker evicts w from the p.workers slice (called after a crash or
// health-check failure). Linear scan is fine — pools are small (< 100 workers).
// TODO: eventually have a lookup to avoid holding the lock while we remove the worker
func (p *Pool[C]) removeWorker(w Worker[C]) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, existing := range p.workers {
		if existing.ID() == w.ID() {
			p.workers = append(p.workers[:i], p.workers[i+1:]...)
			return
		}
	}
}

// ---------------------------------------------------------------------------
// Background loops
// ---------------------------------------------------------------------------

// healthCheckLoop periodically calls w.Healthy on every worker and kills
// unhealthy ones. The pool's monitor goroutine (via wireWorker) handles restart.
func (p *Pool[C]) healthCheckLoop() {
	if p.cfg.healthInterval == 0 {
		return
	}
	ticker := time.NewTicker(p.cfg.healthInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			p.mu.Lock()
			workers := make([]Worker[C], len(p.workers))
			copy(workers, p.workers)
			p.mu.Unlock()

			for _, w := range workers {
				hCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				err := w.Healthy(hCtx)
				cancel()
				if err != nil {
					log.Printf("[pool] health-check: worker %s unhealthy (%v) — closing", w.ID(), err)
					_ = w.Close()
					p.removeWorker(w)
					p.maybeScaleUp()
				}
			}
		case <-p.done:
			return
		}
	}
}


// ---------------------------------------------------------------------------
// Stats & Shutdown
// ---------------------------------------------------------------------------

// Stats returns a point-in-time snapshot of pool state.
// Safe to call concurrently.
func (p *Pool[C]) Stats() PoolStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return PoolStats{
		TotalWorkers:     len(p.workers),
		AvailableWorkers: len(p.available),
		ActiveSessions:   len(p.sessions),
		InflightAcquires: len(p.inflight),
	}
}

// Shutdown gracefully stops the pool.
// It closes all background goroutines and then kills every worker.
// In-flight Acquire calls will receive a context cancellation error if
// the caller's ctx is tied to the application lifetime.
func (p *Pool[C]) Shutdown(ctx context.Context) error {
	p.cancel()
	close(p.done)
	p.mu.Lock()
	workers := make([]Worker[C], len(p.workers))
	copy(workers, p.workers)
	p.mu.Unlock()

	for _, w := range workers {
		if err := w.Close(); err != nil {
			log.Printf("[pool] Shutdown: error closing worker %s: %v", w.ID(), err)
		}
	}
	log.Println("[pool] shutdown complete")
	return nil
}
