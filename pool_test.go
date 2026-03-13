// pool_test.go — Race-detector tests for Pool[C].Acquire.
//
// # What is tested
//
//   - TestSameSessionSingleflight: 20 concurrent goroutines calling
//     Acquire("same-session") must all get back the SAME worker. No second
//     worker should be popped from the pool. Verifies the singleflight guard.
//
//   - TestDifferentSessionsIsolated: N goroutines each acquiring a unique
//     sessionID must each get a DIFFERENT worker. Verifies session-to-worker
//     1:1 mapping under concurrency.
//
//   - TestCrashDuringAcquire: if the worker fails Healthy() at the moment
//     of acquisition, Acquire must return an error and not hand a dead handle
//     to the caller. No workers leak.
//
// # Running
//
//	go test -race -count=1 -v -timeout=30s ./herd/
//
// All tests use stubWorker and stubFactory — no real binary is required.
// The race detector is the hard pass/fail criterion.
package herd

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Stub types — no real binary, fully deterministic
// ---------------------------------------------------------------------------

// stubWorker is a fake Worker[*stubClient] for testing.
// All methods are safe for concurrent use.
type stubWorker struct {
	id        string
	healthErr error // if non-nil, Healthy() returns this error
	closed    bool
	mu        sync.Mutex
}

type stubClient struct{}

func (w *stubWorker) ID() string          { return w.id }
func (w *stubWorker) Address() string     { return "http://127.0.0.1:9999" }
func (w *stubWorker) Client() *stubClient { return &stubClient{} }
func (w *stubWorker) Healthy(_ context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.healthErr
}
func (w *stubWorker) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	return nil
}

// stubFactory spawns exactly the workers provided in the workers slice, in
// order. Panics if Spawn is called more times than there are workers.
type stubFactory struct {
	mu      sync.Mutex
	workers []*stubWorker
	index   int
}

func newStubFactory(workers ...*stubWorker) *stubFactory {
	return &stubFactory{workers: workers}
}

func (f *stubFactory) Spawn(_ context.Context) (Worker[*stubClient], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.index >= len(f.workers) {
		return nil, fmt.Errorf("stub: no more workers to spawn")
	}
	w := f.workers[f.index]
	f.index++
	return w, nil
}

// ---------------------------------------------------------------------------
// Helper: build pool with pre-wired stub workers, bypassing Spawn health poll
// ---------------------------------------------------------------------------

// newTestPool builds a Pool[*stubClient] with the given stub workers already
// in the available channel. This avoids the need for a real binary or health
// endpoint while still exercising the full Acquire / Release logic.
func newTestPool(t *testing.T, workers ...*stubWorker) *Pool[*stubClient] {
	t.Helper()
	factory := newStubFactory(workers...)

	// Build pool with min=0 so New() doesn't call Spawn at startup.
	// ctx/cancel must be wired so that addWorker (called by maybeScaleUp)
	// can derive its spawn timeout via context.WithTimeout(p.ctx, ...).
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel) // ensure no goroutine leaks after the test exits

	p := &Pool[*stubClient]{
		factory:      factory,
		cfg:          defaultConfig(),
		sessions:     make(map[string]Worker[*stubClient]),
		inflight:     make(map[string]chan struct{}),
		lastAccessed: make(map[string]time.Time),
		activeConns:  make(map[string]int32),
		workers:      make([]Worker[*stubClient], 0, len(workers)),
		available:    make(chan Worker[*stubClient], len(workers)),
		done:         make(chan struct{}),
		ctx:          ctx,
		cancel:       cancel,
	}

	// Manually wire workers (same logic as New → wireWorker, minus crash hookup)
	for _, w := range workers {
		p.workers = append(p.workers, w)
		p.available <- w
	}
	return p
}

// ---------------------------------------------------------------------------
// Test 1 — Singleflight: same sessionID, 20 concurrent Acquires
// ---------------------------------------------------------------------------

func TestSameSessionSingleflight(t *testing.T) {
	// Two workers in the pool. Regardless of concurrency, all callers for
	// "session-x" must receive the SAME worker.
	w1 := &stubWorker{id: "worker-1"}
	w2 := &stubWorker{id: "worker-2"}
	pool := newTestPool(t, w1, w2)

	const goroutines = 20
	results := make([]*Session[*stubClient], goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for i := 0; i < goroutines; i++ {
		i := i
		go func() {
			defer wg.Done()
			sess, err := pool.Acquire(ctx, "session-x")
			if err != nil {
				t.Errorf("goroutine %d: Acquire returned unexpected error: %v", i, err)
				return
			}
			results[i] = sess
		}()
	}
	wg.Wait()

	// All non-nil results must point to the same worker ID
	var firstID string
	for i, sess := range results {
		if sess == nil {
			continue // error already reported above
		}
		if firstID == "" {
			firstID = sess.Worker.ID()
		}
		if sess.Worker.ID() != firstID {
			t.Errorf("goroutine %d: got worker %q, expected %q (singleflight violated)", i, sess.Worker.ID(), firstID)
		}
	}
	t.Logf("All %d goroutines received worker %q", goroutines, firstID)

	// The second worker must still be in the available pool (untouched)
	stats := pool.Stats()
	if stats.AvailableWorkers != 1 {
		t.Errorf("expected 1 available worker (w2 untouched), got %d", stats.AvailableWorkers)
	}
}

// ---------------------------------------------------------------------------
// Test 2 — Isolation: different sessionIDs → different workers
// ---------------------------------------------------------------------------

func TestDifferentSessionsIsolated(t *testing.T) {
	w1 := &stubWorker{id: "worker-1"}
	w2 := &stubWorker{id: "worker-2"}
	w3 := &stubWorker{id: "worker-3"}
	pool := newTestPool(t, w1, w2, w3)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sessions := make([]*Session[*stubClient], 3)
	sessionIDs := []string{"session-alpha", "session-beta", "session-gamma"}

	var wg sync.WaitGroup
	wg.Add(3)
	for i, sid := range sessionIDs {
		i, sid := i, sid
		go func() {
			defer wg.Done()
			sess, err := pool.Acquire(ctx, sid)
			if err != nil {
				t.Errorf("Acquire(%q): %v", sid, err)
				return
			}
			sessions[i] = sess
		}()
	}
	wg.Wait()

	// Each session must use a unique worker
	seen := map[string]string{} // workerID → sessionID
	for i, sess := range sessions {
		if sess == nil {
			continue
		}
		wid := sess.Worker.ID()
		if prev, dup := seen[wid]; dup {
			t.Errorf("worker %q is shared between sessions %q and %q — isolation violated", wid, prev, sessionIDs[i])
		}
		seen[wid] = sessionIDs[i]
	}
	t.Logf("Sessions isolated: %v", seen)

	// No workers should be available (all 3 are pinned)
	stats := pool.Stats()
	if stats.AvailableWorkers != 0 {
		t.Errorf("expected 0 available workers (all pinned), got %d", stats.AvailableWorkers)
	}
	if stats.ActiveSessions != 3 {
		t.Errorf("expected 3 active sessions, got %d", stats.ActiveSessions)
	}
}

// ---------------------------------------------------------------------------
// Test 3 — Crash during Acquire: unhealthy worker is discarded, not returned
// ---------------------------------------------------------------------------

func TestCrashDuringAcquire(t *testing.T) {
	// Worker always fails health check
	w := &stubWorker{id: "worker-dead", healthErr: fmt.Errorf("process exited")}
	pool := newTestPool(t, w)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := pool.Acquire(ctx, "session-y")
	if err == nil {
		t.Fatal("expected Acquire to return error for unhealthy worker, got nil")
	}
	t.Logf("Acquire correctly returned error: %v", err)

	// The dead worker must NOT be back in the available channel (it was discarded)
	stats := pool.Stats()
	if stats.AvailableWorkers != 0 {
		t.Errorf("expected 0 available workers after dead worker discarded, got %d", stats.AvailableWorkers)
	}

	// The session must not exist in the map
	pool.mu.Lock()
	_, exists := pool.sessions["session-y"]
	pool.mu.Unlock()
	if exists {
		t.Error("session-y should not exist in session map after failed Acquire")
	}
}

// ---------------------------------------------------------------------------
// Test 4 — Release: worker is returned to available pool after Session.Release
// ---------------------------------------------------------------------------

func TestReleaseReturnsWorkerToPool(t *testing.T) {
	w := &stubWorker{id: "worker-1"}
	pool := newTestPool(t, w)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sess, err := pool.Acquire(ctx, "session-z")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	// After Acquire, pool should have 0 available workers
	if got := pool.Stats().AvailableWorkers; got != 0 {
		t.Fatalf("expected 0 available after Acquire, got %d", got)
	}

	sess.Release()

	// After Release, worker should be back
	if got := pool.Stats().AvailableWorkers; got != 1 {
		t.Errorf("expected 1 available after Release, got %d", got)
	}

	// And the session should be gone from the map
	pool.mu.Lock()
	_, exists := pool.sessions["session-z"]
	pool.mu.Unlock()
	if exists {
		t.Error("session-z should not exist in session map after Release")
	}
}

// ---------------------------------------------------------------------------
// Test 5 — TTL sweep: idle session is evicted and worker returned to pool
// ---------------------------------------------------------------------------

func TestTTLSweepExpiresSessions(t *testing.T) {
	w := &stubWorker{id: "worker-1"}
	pool := newTestPool(t, w)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sess, err := pool.Acquire(ctx, "session-ttl")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	// Don't call Release — simulate the TTL sweeper doing it.
	_ = sess // intentionally held open

	// Release connection so activeConns becomes 0.
	// This will reset lastAccessed to time.Now().
	sess.ConnRelease()

	// Backdate lastAccessed so the session looks stale (1 hour ago).
	pool.mu.Lock()
	pool.lastAccessed["session-ttl"] = time.Now().Add(-1 * time.Hour)
	pool.mu.Unlock()

	// Manually trigger the sweep (avoids waiting for the real ticker).
	pool.sweepExpired()

	// Session should be gone from the affinity map.
	pool.mu.Lock()
	_, stillExists := pool.sessions["session-ttl"]
	pool.mu.Unlock()
	if stillExists {
		t.Error("expected session-ttl to be evicted by TTL sweeper")
	}

	// Worker should be back in the available channel.
	if got := pool.Stats().AvailableWorkers; got != 1 {
		t.Errorf("expected 1 available worker after TTL eviction, got %d", got)
	}
}
