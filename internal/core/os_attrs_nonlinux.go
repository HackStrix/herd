//go:build !linux

package core

import "os/exec"

// ApplyOSAttributes applies OS-specific attributes to the given command.
// On non-Linux systems, this is a no-op as Pdeathsig or equivalent is not universally available.
func ApplyOSAttributes(cmd *exec.Cmd) {
	// No-op for macOS/Windows. Graceful shutdown must rely on explicit tracking.
}
