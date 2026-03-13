//go:build !linux

package observer

// pollNodeStats returns a zeroed NodeStats on non-Linux platforms.
// This is intentional — there is no /proc filesystem on macOS or Windows.
// Callers should treat a zero NodeStats as "metrics unavailable" rather
// than "machine has zero memory."
func pollNodeStats() (NodeStats, error) {
	return NodeStats{}, nil
}
