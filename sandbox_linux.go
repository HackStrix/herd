//go:build linux

package herd

import (
	"os/exec"
)

// applySandboxFlags applies Linux-specific sandbox isolation (Namespaces, Cgroups, Seccomp)
// to the given command before it is started.
func applySandboxFlags(cmd *exec.Cmd) error {
	// TODO: Phase 1 (Namespaces) - inject syscall.SysProcAttr
	// TODO: Phase 2 (Cgroups) - setup and apply cgroup constraints
	// TODO: Phase 3 (Seccomp) - load BPF filters

	return nil
}
