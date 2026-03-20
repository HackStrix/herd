//go:build linux

// sandbox_linux_test.go — Unit tests for applySandboxFlags and cgroupHandle.
//
// These tests redirect activeCgroupRoot to t.TempDir() so they work
// without real cgroup privileges — all file writes go to a temp directory.
// The SysProcAttr wiring is verified on an uncommitted exec.Cmd (never started).
package core

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// withTempCgroupRoot points activeCgroupRoot to a temp dir for the duration
// of the test and resets it afterwards.
func withTempCgroupRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	// Pre-create the subtree_control file so writeCgroupFile can write to it.
	// (Real cgroupfs has this; our temp dir does not.)
	if err := os.WriteFile(filepath.Join(root, "cgroup.subtree_control"), []byte(""), 0o644); err != nil {
		t.Fatalf("setup: create subtree_control: %v", err)
	}
	old := activeCgroupRoot
	activeCgroupRoot = root
	t.Cleanup(func() { activeCgroupRoot = old })
	return root
}

func newFakeCmd() *exec.Cmd {
	// "true" exists on all Linux systems and is harmless — the Cmd is never started.
	return exec.Command("true")
}

func readCgroupFile(t *testing.T, cgroupPath, filename string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(cgroupPath, filename))
	if err != nil {
		t.Fatalf("read cgroup file %s: %v", filename, err)
	}
	return strings.TrimSpace(string(data))
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestApplySandboxFlags_DefaultPIDs(t *testing.T) {
	root := withTempCgroupRoot(t)
	cmd := newFakeCmd()

	h, err := applySandboxFlags(cmd, "worker-1", sandboxConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h == nil {
		t.Fatal("expected non-nil handle, got nil (soft-fail triggered unexpectedly)")
	}

	cgroupPath := filepath.Join(root, "worker-1")
	got := readCgroupFile(t, cgroupPath, "pids.max")
	if got != "100" {
		t.Errorf("pids.max: expected '100' (default), got %q", got)
	}
}

func TestApplySandboxFlags_MemoryLimit(t *testing.T) {
	root := withTempCgroupRoot(t)
	cmd := newFakeCmd()
	const limit int64 = 64 * 1024 * 1024 // 64 MB

	h, err := applySandboxFlags(cmd, "worker-mem", sandboxConfig{memoryMaxBytes: limit})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h == nil {
		t.Fatal("expected non-nil handle")
	}

	cgroupPath := filepath.Join(root, "worker-mem")
	if got := readCgroupFile(t, cgroupPath, "memory.max"); got != "67108864" {
		t.Errorf("memory.max: expected '67108864', got %q", got)
	}
	if got := readCgroupFile(t, cgroupPath, "memory.swap.max"); got != "0" {
		t.Errorf("memory.swap.max: expected '0', got %q", got)
	}
}

func TestApplySandboxFlags_CPULimit(t *testing.T) {
	root := withTempCgroupRoot(t)
	cmd := newFakeCmd()

	h, err := applySandboxFlags(cmd, "worker-cpu", sandboxConfig{cpuMaxMicros: 50_000})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h == nil {
		t.Fatal("expected non-nil handle")
	}

	cgroupPath := filepath.Join(root, "worker-cpu")
	if got := readCgroupFile(t, cgroupPath, "cpu.max"); got != "50000 100000" {
		t.Errorf("cpu.max: expected '50000 100000', got %q", got)
	}
}

func TestApplySandboxFlags_UnlimitedPIDs(t *testing.T) {
	root := withTempCgroupRoot(t)
	cmd := newFakeCmd()

	h, err := applySandboxFlags(cmd, "worker-nopid", sandboxConfig{pidsMax: -1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h == nil {
		t.Fatal("expected non-nil handle")
	}

	cgroupPath := filepath.Join(root, "worker-nopid")
	if got := readCgroupFile(t, cgroupPath, "pids.max"); got != "max" {
		t.Errorf("pids.max: expected 'max' for -1, got %q", got)
	}
}

func TestApplySandboxFlags_NoCPULimitFileWhenZero(t *testing.T) {
	root := withTempCgroupRoot(t)
	cmd := newFakeCmd()

	_, err := applySandboxFlags(cmd, "worker-nocpu", sandboxConfig{cpuMaxMicros: 0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cgroupPath := filepath.Join(root, "worker-nocpu")
	if _, err := os.Stat(filepath.Join(cgroupPath, "cpu.max")); err == nil {
		t.Error("cpu.max should not be written when cpuMaxMicros=0")
	}
}

func TestApplySandboxFlags_SysProcAttrWired(t *testing.T) {
	withTempCgroupRoot(t)
	cmd := newFakeCmd()

	h, err := applySandboxFlags(cmd, "worker-attr", sandboxConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h == nil {
		t.Fatal("expected non-nil handle")
	}
	if cmd.SysProcAttr == nil {
		t.Fatal("SysProcAttr should be set after applySandboxFlags")
	}
	if !cmd.SysProcAttr.UseCgroupFD {
		t.Error("UseCgroupFD should be true")
	}
	if cmd.SysProcAttr.CgroupFD <= 0 {
		t.Errorf("CgroupFD should be a valid fd (>0), got %d", cmd.SysProcAttr.CgroupFD)
	}
}

func TestApplySandboxFlags_CloneFlagsMergedWithCgroup(t *testing.T) {
	withTempCgroupRoot(t)
	cmd := newFakeCmd()

	flags := uintptr(syscall.CLONE_NEWPID | syscall.CLONE_NEWNS | syscall.CLONE_NEWIPC)
	h, err := applySandboxFlags(cmd, "worker-ns", sandboxConfig{cloneFlags: flags})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h == nil {
		t.Fatal("expected non-nil handle")
	}
	if cmd.SysProcAttr == nil {
		t.Fatal("SysProcAttr should be set after applySandboxFlags")
	}
	if cmd.SysProcAttr.Cloneflags&flags != flags {
		t.Errorf("expected Cloneflags to include %#x, got %#x", flags, cmd.SysProcAttr.Cloneflags)
	}
	if !cmd.SysProcAttr.UseCgroupFD {
		t.Error("UseCgroupFD should remain true after namespace merge")
	}
	if cmd.SysProcAttr.CgroupFD <= 0 {
		t.Errorf("CgroupFD should remain set after namespace merge, got %d", cmd.SysProcAttr.CgroupFD)
	}
}

func TestApplySandboxFlags_SoftFailOnBadRoot(t *testing.T) {
	// Point to a path that cannot be created (inside /proc which is read-only).
	old := activeCgroupRoot
	activeCgroupRoot = "/proc/herd_test_unreachable_path"
	defer func() { activeCgroupRoot = old }()

	cmd := newFakeCmd()
	h, err := applySandboxFlags(cmd, "worker-fail", sandboxConfig{})
	if err != nil {
		t.Fatalf("expected soft fail (nil, nil) but got error: %v", err)
	}
	if h != nil {
		t.Errorf("expected nil handle on soft fail, got %v", h)
	}
}

func TestApplySandboxFlags_ExistingCgroupDirIsReused(t *testing.T) {
	root := withTempCgroupRoot(t)
	// Pre-create the leaf dir to simulate a stale entry.
	cgroupPath := filepath.Join(root, "worker-exist")
	if err := os.Mkdir(cgroupPath, 0o755); err != nil {
		t.Fatalf("pre-create cgroup dir: %v", err)
	}
	cmd := newFakeCmd()

	h, err := applySandboxFlags(cmd, "worker-exist", sandboxConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should not soft-fail just because the dir already exists.
	if h == nil {
		t.Error("expected non-nil handle when cgroup dir already exists")
	}
}

func TestCgroupHandle_PostStart_ClosesFile(t *testing.T) {
	// Create a real file to wrap as the fd.
	tmp, err := os.CreateTemp(t.TempDir(), "cgfd-*")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	h := &cgroupHandle{path: t.TempDir(), fd: tmp}
	h.PostStart()
	if h.fd != nil {
		t.Error("expected fd to be nil after PostStart")
	}
	// Calling PostStart again should be a no-op (fd already nil).
	h.PostStart()
}

func TestCgroupHandle_Cleanup_RemovesDir(t *testing.T) {
	dir := t.TempDir()
	leaf := filepath.Join(dir, "leaf")
	if err := os.Mkdir(leaf, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	h := &cgroupHandle{path: leaf}
	h.Cleanup()
	if _, err := os.Stat(leaf); !os.IsNotExist(err) {
		t.Error("expected cgroup leaf dir to be removed after Cleanup")
	}
}

func TestCgroupHandle_Cleanup_Idempotent(t *testing.T) {
	dir := t.TempDir()
	leaf := filepath.Join(dir, "stale")
	if err := os.Mkdir(leaf, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	h := &cgroupHandle{path: leaf}
	h.Cleanup() // removes dir
	h.Cleanup() // dir already gone — should not panic or log error as warning
}

func TestCgroupHandle_Cleanup_NilSafe(t *testing.T) {
	var h *cgroupHandle
	h.Cleanup() // must not panic
}
