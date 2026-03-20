package core

// sandboxConfig contains per-worker sandbox resource constraints.
// A value of 0 means "unlimited" for memory and CPU.
type sandboxConfig struct {
	memoryMaxBytes int64
	cpuMaxMicros   int64
	pidsMax        int64
	cloneFlags     uintptr
}

// sandboxHandle owns post-start and cleanup hooks for sandbox resources.
// Implementations may be no-op on unsupported or soft-fail paths.
type sandboxHandle interface {
	PostStart()
	Cleanup()
}
