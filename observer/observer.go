// Package observer provides lightweight, OS-level resource sampling.
//
// # Overview
//
// PollNodeStats returns a point-in-time snapshot of host memory and CPU
// availability. It is designed to be called from Pool.Stats so that
// dashboards and alerting systems can see not just pool-level metrics
// (workers, sessions) but also whether the *host* is under pressure.
//
// # Platform support
//
//   - Linux: reads /proc/meminfo and /proc/stat (two samples, 100 ms apart).
//   - All other platforms: returns a zero-valued NodeStats with no error.
//     This allows the observer package to compile and be imported on macOS and
//     Windows without any build-tag gymnastics in the caller.
//
// # CPU measurement latency
//
// On Linux, PollNodeStats blocks for ~100 ms because computing CPU idle
// requires two /proc/stat snapshots separated by a measurement window.
// If you call Pool.Stats() in a hot path, cache the result or call
// PollNodeStats from a background goroutine.
package observer

// NodeStats is a point-in-time snapshot of host resource availability.
// All fields are zero on non-Linux platforms.
type NodeStats struct {
	// TotalMemoryBytes is the total physical RAM on the host, in bytes.
	TotalMemoryBytes int64

	// AvailableMemoryBytes is the amount of memory available for new
	// processes without swapping (corresponds to /proc/meminfo MemAvailable).
	AvailableMemoryBytes int64

	// CPUIdle is the fraction of CPU time that was idle during the
	// measurement window (0.0 = fully busy, 1.0 = fully idle).
	// Averaged across all logical cores.
	CPUIdle float64
}

// PollNodeStats returns a current snapshot of host resources.
// On non-Linux platforms it returns a zero NodeStats and nil error.
// On Linux it blocks for ~100 ms to measure CPU idle.
func PollNodeStats() (NodeStats, error) {
	return pollNodeStats()
}
