package observer

import (
	"runtime"
	"testing"
)

func TestPollNodeStats(t *testing.T) {
	stats, err := PollNodeStats()
	if err != nil {
		t.Fatalf("PollNodeStats() returned error: %v", err)
	}

	if runtime.GOOS != "linux" {
		// On macOS / Windows the stub returns zeroes — that's expected.
		t.Logf("non-Linux platform (%s): NodeStats is zero-valued by design", runtime.GOOS)
		if stats.TotalMemoryBytes != 0 || stats.AvailableMemoryBytes != 0 || stats.CPUIdle != 0 {
			t.Errorf("expected zero NodeStats on non-Linux, got %+v", stats)
		}
		return
	}

	// On Linux all fields must be populated.
	if stats.TotalMemoryBytes <= 0 {
		t.Errorf("TotalMemoryBytes should be > 0 on Linux, got %d", stats.TotalMemoryBytes)
	}
	if stats.AvailableMemoryBytes <= 0 {
		t.Errorf("AvailableMemoryBytes should be > 0 on Linux, got %d", stats.AvailableMemoryBytes)
	}
	if stats.AvailableMemoryBytes > stats.TotalMemoryBytes {
		t.Errorf("AvailableMemoryBytes (%d) > TotalMemoryBytes (%d) — not possible",
			stats.AvailableMemoryBytes, stats.TotalMemoryBytes)
	}
	if stats.CPUIdle < 0.0 || stats.CPUIdle > 1.0 {
		t.Errorf("CPUIdle must be in [0, 1], got %f", stats.CPUIdle)
	}

	t.Logf("NodeStats: total=%d MB available=%d MB cpuIdle=%.2f%%",
		stats.TotalMemoryBytes/1024/1024,
		stats.AvailableMemoryBytes/1024/1024,
		stats.CPUIdle*100,
	)
}
