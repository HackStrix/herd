//go:build !linux

package herd

import (
	"errors"
	"os/exec"
	"runtime"
)

// ErrSandboxUnsupported is returned when sandbox mode is requested on a non-Linux OS.
var ErrSandboxUnsupported = errors.New(
	"\n\n##################################### WARNING ##################################################\n\n" +
		"herd: STRICT SANDBOX ENABLED BUT UNSUPPORTED ON THIS OS.\n\n" +
		"  The security sandbox relies on Linux cgroups and namespaces (CLONE_NEWUSER, etc.),\n" +
		"  which do not exist on " + runtime.GOOS + ".\n\n" +
		"  FIX: If you are developing locally on macOS or Windows and want to test your pool logic,\n" +
		"  you MUST explicitly opt-out of sandbox mode by using:\n\n" +
		"      factory.WithInsecureSandbox()\n\n" +
		"  Warning: Do not use WithInsecureSandbox() in production unless you fully trust the workloads.\n\n" +
		"###############################################################################################",
)

// applySandboxFlags applies Linux-specific sandbox isolation.
// On non-Linux systems, this returns an error if sandbox mode is enabled,
// forcing a loud failure instead of a false sense of security.
func applySandboxFlags(cmd *exec.Cmd, workerID string, cfg sandboxConfig) (sandboxHandle, error) {
	return nil, ErrSandboxUnsupported
}

func defaultNamespaceCloneFlags() uintptr {
	return 0
}
