// pool_ttl.go — Idle session TTL sweeper for Pool[C].
//
// # Why a separate file?
//
// pool.go owns the concurrency model (Acquire, Release, onCrash).
// This file owns time-based session lifecycle — kept separate so each file
// has one job and can be read/reviewed in isolation.
//
// # How it works
//
// Every session that enters p.sessions also gets an entry in p.lastAccessed
// (a map[string]time.Time guarded by the same p.mu lock). Every call to
// Acquire that hits the fast path "touches" the session by updating its
// timestamp. The sweeper goroutine wakes up every ttl/2 and evicts sessions
// whose timestamp is older than cfg.ttl, calling release() on each so the
// worker is returned to the available channel.
//
// # Concurrency
//
// p.lastAccessed is always read/written under p.mu — the same lock that
// guards p.sessions and p.inflight. No extra synchronization is needed.
//
// # Disabling TTL
//
// Pass WithTTL(0) to New[C]. The ttlSweepLoop in pool.go returns immediately
// when cfg.ttl == 0, so this file's sweeper is never started.
package herd

import (
	"log"
	"time"
)

// ---------------------------------------------------------------------------
// lastAccessed tracking — hoisted alongside Pool fields
// ---------------------------------------------------------------------------
// The Pool struct in pool.go contains:
//
//   lastAccessed map[string]time.Time  // sessionID → last Acquire time
//
// These methods operate on that field.

// touchSession updates the last-accessed timestamp for a session.
// Must be called with p.mu held.
func (p *Pool[C]) touchSession(sessionID string) {
	p.lastAccessed[sessionID] = time.Now()
}

// ---------------------------------------------------------------------------
// TTL sweep
// ---------------------------------------------------------------------------

// sweepExpired scans p.lastAccessed under the lock, collects sessions that
// have been idle longer than cfg.ttl, removes them from both maps, and then
// calls release() on each worker outside the lock.
func (p *Pool[C]) sweepExpired() {
	now := time.Now()

	p.mu.Lock()
	var expired []struct {
		sessionID string
		worker    Worker[C]
	}
	for sid, lastSeen := range p.lastAccessed {
		if now.Sub(lastSeen) < p.cfg.ttl {
			continue
		}
		w, ok := p.sessions[sid]
		if !ok {
			// Session already released — clean up orphaned timestamp
			delete(p.lastAccessed, sid)
			continue
		}
		expired = append(expired, struct {
			sessionID string
			worker    Worker[C]
		}{sid, w})
		delete(p.sessions, sid)
		delete(p.lastAccessed, sid)
	}
	p.mu.Unlock()

	// Release or close workers outside the lock so we don't hold mu while blocking
	for _, e := range expired {
		if p.cfg.reuseWorkers {
			log.Printf("[pool] TTL expired: session %q → worker %s returned to pool", e.sessionID, e.worker.ID())
			// Push directly to available — bypass release() logging duplication
			select {
			case p.available <- e.worker:
			default:
				log.Printf("[pool] TTL sweep: available channel full releasing worker %s — this is a bug", e.worker.ID())
			}
		} else {
			log.Printf("[pool] TTL expired: session %q → worker %s killed (reuse=false)", e.sessionID, e.worker.ID())
			go func(w Worker[C]) {
				if err := w.Close(); err != nil {
					log.Printf("[pool] TTL sweep: error closing worker %s: %v", w.ID(), err)
				}
				p.removeWorker(w)
				p.maybeScaleUp()
			}(e.worker)
		}
	}

}

// runTTLSweep is the real implementation wired into the ttlSweepLoop stub in pool.go.
// Call this from Pool.ttlSweepLoop replacing the TODO stub.
func (p *Pool[C]) runTTLSweep() {
	if p.cfg.ttl == 0 {
		return
	}
	ticker := time.NewTicker(p.cfg.ttl / 2)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			p.sweepExpired()
		case <-p.done:
			return
		}
	}
}
