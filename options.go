// options.go — functional options for Pool construction.
//
// # Change log
//
//   - v0.1: Initial set: WithAutoScale, WithTTL, WithCrashHandler, WithHealthInterval.
//
// # Pattern
//
// All public options follow the functional-options pattern (Dave Cheney, 2014).
// Each option is a function that mutates an internal `config` struct.
// They are applied in order inside New[C], before any goroutines are started,
// so there are no synchronization requirements here.
//
// # Adding a new option
//
//  1. Add the field to `config`.
//  2. Set a sensible default in `defaultConfig()`.
//  3. Write a `WithXxx` function below.
//  4. Document the zero-value behaviour in the comment.
package herd

import "time"

// ---------------------------------------------------------------------------
// config — internal knobs for Pool
// ---------------------------------------------------------------------------

// config holds all tuneable parameters for a Pool.
// Defaults are set in defaultConfig(); all fields are safe to read concurrently
// after New[C] returns because they are never mutated after that point.
type config struct {
	// min is the floor: the pool always keeps at least this many workers alive.
	// Default: 1
	min int

	// max is the ceiling: the pool will never spawn more than this many workers.
	// Default: 10
	max int

	// ttl is how long an idle session is kept alive before the pool reclaims
	// its worker. "Idle" means no Acquire call has touched the session within
	// this window.
	// Default: 5 minutes
	ttl time.Duration

	// healthInterval controls how often the background health-check loop polls
	// each worker's Healthy() method.
	// Default: 5 seconds
	healthInterval time.Duration

	// crashHandler is called when a worker's subprocess exits while it holds an
	// active session. The argument is the sessionID that was lost.
	// Use this to clean up any external state tied to that session.
	// Default: nil (crash is logged but not propagated to caller)
	crashHandler func(sessionID string)
}

// defaultConfig returns the baseline configuration.
// All fields must have a valid, production-ready zero value so that
// herd.New(factory) with no extra options produces a working pool.
func defaultConfig() config {
	return config{
		min:            1,
		max:            10,
		ttl:            5 * time.Minute,
		healthInterval: 5 * time.Second,
	}
}

// ---------------------------------------------------------------------------
// Option
// ---------------------------------------------------------------------------

// Option is a functional option for New[C].
type Option func(*config)

// ---------------------------------------------------------------------------
// WithAutoScale — pool sizing
// ---------------------------------------------------------------------------

// WithAutoScale sets the minimum and maximum number of live workers.
//
//   - min: the pool always keeps at least this many workers healthy,
//     even with zero active sessions.
//   - max: hard cap on concurrent workers; Acquire blocks once this limit
//     is reached until a worker becomes available.
//
// Panics if min < 1 or max < min.
func WithAutoScale(min, max int) Option {
	if min < 1 {
		panic("herd: WithAutoScale min must be >= 1")
	}
	if max < min {
		panic("herd: WithAutoScale max must be >= min")
	}
	return func(c *config) {
		c.min = min
		c.max = max
	}
}

// ---------------------------------------------------------------------------
// WithTTL — idle session expiry
// ---------------------------------------------------------------------------

// WithTTL sets the idle-session timeout.
//
// A session is considered idle when no Acquire call has touched it within d.
// When the TTL fires, the session is removed from the affinity map and its
// worker is returned to the available pool.
//
// Set d = 0 to disable TTL (sessions live until explicitly Released or the
// pool shuts down). This is useful for REPL-style processes where the caller
// owns the session lifetime.
func WithTTL(d time.Duration) Option {
	return func(c *config) { c.ttl = d }
}

// ---------------------------------------------------------------------------
// WithCrashHandler — crash recovery callback
// ---------------------------------------------------------------------------

// WithCrashHandler registers a callback invoked when a worker's subprocess
// exits unexpectedly while it holds an active session.
//
// fn receives the sessionID that was lost. Use it to:
//   - Delete session-specific state in your database
//   - Return an error to the end user ("your session was interrupted")
//   - Trigger a re-run of the failed job
//
// fn is called from a background monitor goroutine. It must not block for
// extended periods; spawn a goroutine if you need to do heavy work.
//
// If WithCrashHandler is not set, crashes are only logged.
func WithCrashHandler(fn func(sessionID string)) Option {
	return func(c *config) { c.crashHandler = fn }
}

// ---------------------------------------------------------------------------
// WithHealthInterval — background health polling
// ---------------------------------------------------------------------------

// WithHealthInterval sets how often the pool's background health-check loop
// calls Worker.Healthy() on every live worker.
//
// Shorter intervals detect unhealthy workers faster but add more HTTP/RPC
// overhead. The default (5s) is a good balance for most workloads.
//
// Set d = 0 to disable background health checks entirely. Workers are still
// checked once during Acquire (step 6 of the singleflight protocol).
func WithHealthInterval(d time.Duration) Option {
	return func(c *config) { c.healthInterval = d }
}
