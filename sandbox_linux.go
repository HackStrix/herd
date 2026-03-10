//go:build linux

package herd

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
)

const (
	herdCgroupRoot  = "/sys/fs/cgroup/herd"
	cpuPeriodMicros = 100_000
)

// activeCgroupRoot is the base directory used for all cgroup operations.
// It defaults to herdCgroupRoot but can be overridden in tests to redirect
// cgroup file writes to a temp dir without needing real cgroup privileges.
var activeCgroupRoot = herdCgroupRoot

type cgroupHandle struct {
	path string
	fd   *os.File
}

func (h *cgroupHandle) PostStart() {
	if h == nil || h.fd == nil {
		return
	}
	if err := h.fd.Close(); err != nil {
		log.Printf("[sandbox] warning: close cgroup fd for %s: %v", h.path, err)
	}
	h.fd = nil
}

func (h *cgroupHandle) Cleanup() {
	if h == nil || h.path == "" {
		return
	}
	if err := syscall.Rmdir(h.path); err != nil && !errors.Is(err, syscall.ENOENT) {
		log.Printf("[sandbox] warning: cleanup cgroup %s: %v", h.path, err)
	}
}

// applySandboxFlags applies Linux cgroup v2 constraints to the command.
// If cgroup provisioning is unavailable (for example due to permissions), it
// soft-fails and allows the worker to start without constraints.
func applySandboxFlags(cmd *exec.Cmd, workerID string, cfg sandboxConfig) (sandboxHandle, error) {

	if cfg.pidsMax == 0 {
		cfg.pidsMax = 100
	}

	if err := os.MkdirAll(activeCgroupRoot, 0o755); err != nil {
		log.Printf("[sandbox:%s] WARNING: cgroup root mkdir failed: %v; continuing without cgroup constraints", workerID, err)
		return nil, nil
	}

	if err := writeCgroupFile(activeCgroupRoot, "cgroup.subtree_control", "+memory +cpu +pids"); err != nil {
		log.Printf("[sandbox:%s] WARNING: cgroup controller enable failed: %v; continuing without cgroup constraints", workerID, err)
		return nil, nil
	}

	cgroupPath := filepath.Join(activeCgroupRoot, workerID)
	if err := os.Mkdir(cgroupPath, 0o755); err != nil {
		if !errors.Is(err, os.ErrExist) {
			log.Printf("[sandbox:%s] WARNING: cgroup leaf mkdir failed: %v; continuing without cgroup constraints", workerID, err)
			return nil, nil
		}
	}

	if cfg.memoryMaxBytes > 0 {
		if err := writeCgroupFile(cgroupPath, "memory.max", strconv.FormatInt(cfg.memoryMaxBytes, 10)); err != nil {
			log.Printf("[sandbox:%s] WARNING: memory.max write failed: %v; continuing without cgroup constraints", workerID, err)
			return nil, nil
		}
		if err := writeCgroupFile(cgroupPath, "memory.swap.max", "0"); err != nil {
			log.Printf("[sandbox:%s] WARNING: memory.swap.max write failed: %v; continuing without cgroup constraints", workerID, err)
			return nil, nil
		}
	}

	if cfg.cpuMaxMicros > 0 {
		cpuMax := fmt.Sprintf("%d %d", cfg.cpuMaxMicros, cpuPeriodMicros)
		if err := writeCgroupFile(cgroupPath, "cpu.max", cpuMax); err != nil {
			log.Printf("[sandbox:%s] WARNING: cpu.max write failed: %v; continuing without cgroup constraints", workerID, err)
			return nil, nil
		}
	}

	pidsValue := "max"
	if cfg.pidsMax > 0 {
		pidsValue = strconv.FormatInt(cfg.pidsMax, 10)
	}
	if err := writeCgroupFile(cgroupPath, "pids.max", pidsValue); err != nil {
		log.Printf("[sandbox:%s] WARNING: pids.max write failed: %v; continuing without cgroup constraints", workerID, err)
		return nil, nil
	}

	dir, err := os.Open(cgroupPath)
	if err != nil {
		log.Printf("[sandbox:%s] WARNING: open cgroup directory failed: %v; continuing without cgroup constraints", workerID, err)
		return nil, nil
	}

	sys := cmd.SysProcAttr
	if sys == nil {
		sys = &syscall.SysProcAttr{}
	}
	sys.CgroupFD = int(dir.Fd())
	sys.UseCgroupFD = true
	cmd.SysProcAttr = sys

	return &cgroupHandle{path: cgroupPath, fd: dir}, nil
}

func writeCgroupFile(cgroupPath, filename, value string) error {
	path := filepath.Join(cgroupPath, filename)
	return os.WriteFile(path, []byte(value), 0o644)
}
