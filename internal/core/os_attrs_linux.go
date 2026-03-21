//go:build linux

package core

import (
	"os/exec"
	"syscall"
)

// ApplyOSAttributes applies OS-specific attributes to the given command.
// On Linux, this ensures Pdeathsig is set to SIGKILL to prevent zombie processes.
func ApplyOSAttributes(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// Pdeathsig ensures that if the parent process (herd) dies unexpectedly,
	// the kernel will immediately send SIGKILL to the child process.
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
}
