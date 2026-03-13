package herd

import (
	"context"
	"sync"
)

// SessionRegistry tracks which workers are pinned to which session IDs.
// In a distributed setup (Enterprise), this registry is shared across multiple nodes.
type SessionRegistry[C any] interface {
	// Get returns the worker pinned to sessionID.
	// Returns (nil, nil) if no session exists for this ID.
	Get(ctx context.Context, sessionID string) (Worker[C], error)

	// Put pins a worker to a sessionID.
	Put(ctx context.Context, sessionID string, w Worker[C]) error

	// Delete removes the pinning for sessionID.
	Delete(ctx context.Context, sessionID string) error

	// List returns a snapshot of all currently active sessions.
	// Primarily used for background health checks and cleanup.
	List(ctx context.Context) (map[string]Worker[C], error)

	// Len returns the number of active sessions.
	Len() int
}

// localRegistry is the default in-memory implementation for OSS.
// It is not thread-safe by itself; it expects the Pool to hold p.mu
// when calling these methods for now, but includes its own RWMutex
// to future-proof it.
type localRegistry[C any] struct {
	mu       sync.RWMutex
	sessions map[string]Worker[C]
}

func newLocalRegistry[C any]() *localRegistry[C] {
	return &localRegistry[C]{
		sessions: make(map[string]Worker[C]),
	}
}

func (r *localRegistry[C]) Get(_ context.Context, sessionID string) (Worker[C], error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.sessions[sessionID], nil
}

func (r *localRegistry[C]) Put(_ context.Context, sessionID string, w Worker[C]) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions[sessionID] = w
	return nil
}

func (r *localRegistry[C]) Delete(_ context.Context, sessionID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sessions, sessionID)
	return nil
}

func (r *localRegistry[C]) List(_ context.Context) (map[string]Worker[C], error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	// Return a copy to avoid external mutation of the internal map
	copyMap := make(map[string]Worker[C], len(r.sessions))
	for k, v := range r.sessions {
		copyMap[k] = v
	}
	return copyMap, nil
}

func (r *localRegistry[C]) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.sessions)
}
