//go:build linux

// sandbox_integration_test.go — Integration tests that spawn real processes and
// verify kernel-level cgroup enforcement.
//
// These tests require:
//   - Linux with cgroupv2 enabled (/sys/fs/cgroup must be a cgroup2 mount)
//   - Root, or a user with cgroup delegation (e.g. a systemd user slice)
//   - The HERD_CGROUP_TEST=1 environment variable to be set
//
// Run with:
//
//	HERD_CGROUP_TEST=1 go test -v -run TestSandbox -timeout 60s ./...
package herd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// requireCgroupIntegration skips the test unless HERD_CGROUP_TEST=1 is set.
func requireCgroupIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("HERD_CGROUP_TEST") != "1" {
		t.Skip("skipping cgroup integration test: set HERD_CGROUP_TEST=1 to run")
	}
}

// buildHealthWorker compiles the testdata/healthworker binary into a temp dir
// and returns the path to the compiled binary.
func buildHealthWorker(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "healthworker")
	cmd := exec.Command("go", "build", "-o", bin, "./testdata/healthworker")
	cmd.Dir = filepath.Join(os.Getenv("GOPACKAGE"), "..", ".") // codebase root
	// Use the module root (where go.mod lives) as working directory.
	cmd.Dir = "."
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build healthworker: %v\n%s", err, out)
	}
	return bin
}

// ---------------------------------------------------------------------------
// Test: born-in-cgroup verification
// ---------------------------------------------------------------------------

func TestSandbox_BornInCgroup(t *testing.T) {
	requireCgroupIntegration(t)

	bin := buildHealthWorker(t)

	factory := NewProcessFactory(bin).
		WithHealthPath("/health").
		WithStartTimeout(10 * time.Second).
		WithStartHealthCheckDelay(100 * time.Millisecond).
		WithPIDsLimit(50)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	worker, err := factory.Spawn(ctx)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer worker.Close()

	// Extract the OS-level pid from the ProcessWorker.
	pw, ok := worker.(*ProcessWorker)
	if !ok {
		t.Fatal("expected *ProcessWorker from Spawn")
	}
	pw.mu.Lock()
	pid := pw.cmd.Process.Pid
	pw.mu.Unlock()

	// Read /proc/<pid>/cgroup and verify placement.
	cgroupFile := fmt.Sprintf("/proc/%d/cgroup", pid)
	data, err := os.ReadFile(cgroupFile)
	if err != nil {
		t.Fatalf("read %s: %v", cgroupFile, err)
	}
	contents := string(data)
	t.Logf("/proc/%d/cgroup:\n%s", pid, contents)

	workerCgroupPath := "/herd/" + worker.ID()
	if !strings.Contains(contents, workerCgroupPath) {
		t.Errorf("expected cgroup path to contain %q, got:\n%s", workerCgroupPath, contents)
	}
}

// ---------------------------------------------------------------------------
// Test: cgroup directory lifecycle (exists after spawn, gone after close)
// ---------------------------------------------------------------------------

func TestSandbox_CgroupDirLifecycle(t *testing.T) {
	requireCgroupIntegration(t)

	bin := buildHealthWorker(t)

	factory := NewProcessFactory(bin).
		WithHealthPath("/health").
		WithStartTimeout(10 * time.Second).
		WithStartHealthCheckDelay(100 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	worker, err := factory.Spawn(ctx)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	cgroupPath := filepath.Join("/sys/fs/cgroup/herd", worker.ID())

	// Directory must exist while the worker is alive.
	if _, err := os.Stat(cgroupPath); os.IsNotExist(err) {
		t.Errorf("expected cgroup dir %q to exist while worker is alive", cgroupPath)
	} else {
		t.Logf("cgroup dir present: %s", cgroupPath)
	}

	// Close the worker and wait for monitor() to run Cleanup().
	pw := worker.(*ProcessWorker)
	_ = worker.Close()
	select {
	case <-pw.dead:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for worker to die after Close")
	}

	// Give monitor() a moment to run Cleanup.
	time.Sleep(100 * time.Millisecond)

	if _, err := os.Stat(cgroupPath); !os.IsNotExist(err) {
		t.Errorf("expected cgroup dir %q to be removed after Close, but it still exists", cgroupPath)
	}
}

// ---------------------------------------------------------------------------
// Test: pids.max limit file verification
// ---------------------------------------------------------------------------

func TestSandbox_PIDEnforcement(t *testing.T) {
	requireCgroupIntegration(t)

	bin := buildHealthWorker(t)

	// Set a reasonable PID limit that still allows the healthworker to start.
	// A Go HTTP server needs ~5-10 PIDs for the runtime + main goroutine + HTTP handling.
	// Setting 30 leaves room but still demonstrates the limit is enforced at the kernel level.
	factory := NewProcessFactory(bin).
		WithHealthPath("/health").
		WithStartTimeout(10 * time.Second).
		WithStartHealthCheckDelay(100 * time.Millisecond).
		WithPIDsLimit(30)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	worker, err := factory.Spawn(ctx)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer worker.Close()

	// Read pids.max from the actual cgroup dir to confirm the limit was written.
	// This verifies that the cgroup limit file is set correctly at the kernel level.
	cgroupPath := filepath.Join("/sys/fs/cgroup/herd", worker.ID())
	data, err := os.ReadFile(filepath.Join(cgroupPath, "pids.max"))
	if err != nil {
		t.Fatalf("read pids.max: %v", err)
	}
	got := strings.TrimSpace(string(data))
	if got != "30" {
		t.Errorf("pids.max: expected '30', got %q", got)
	}
	t.Logf("pids.max confirmed at kernel level: %s", got)

	// Check pids.current to see how many PIDs the worker is actually using.
	currentData, err := os.ReadFile(filepath.Join(cgroupPath, "pids.current"))
	if err != nil {
		t.Logf("note: could not read pids.current: %v", err)
	} else {
		current := strings.TrimSpace(string(currentData))
		t.Logf("pids.current (actual usage): %s / 30", current)
	}
}

// ---------------------------------------------------------------------------
// Test: memory.max enforcement — confirm limit file is written correctly
// ---------------------------------------------------------------------------

func TestSandbox_MemoryLimitFileWritten(t *testing.T) {
	requireCgroupIntegration(t)

	bin := buildHealthWorker(t)

	const memLimit int64 = 64 * 1024 * 1024 // 64 MB

	factory := NewProcessFactory(bin).
		WithHealthPath("/health").
		WithStartTimeout(10 * time.Second).
		WithStartHealthCheckDelay(100 * time.Millisecond).
		WithMemoryLimit(memLimit)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	worker, err := factory.Spawn(ctx)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer worker.Close()

	cgroupPath := filepath.Join("/sys/fs/cgroup/herd", worker.ID())

	memMax, err := os.ReadFile(filepath.Join(cgroupPath, "memory.max"))
	if err != nil {
		t.Fatalf("read memory.max: %v", err)
	}
	if got := strings.TrimSpace(string(memMax)); got != "67108864" {
		t.Errorf("memory.max: expected '67108864', got %q", got)
	}

	swapMax, err := os.ReadFile(filepath.Join(cgroupPath, "memory.swap.max"))
	if err != nil {
		t.Fatalf("read memory.swap.max: %v", err)
	}
	if got := strings.TrimSpace(string(swapMax)); got != "0" {
		t.Errorf("memory.swap.max: expected '0', got %q", got)
	}
	t.Logf("memory limits confirmed: max=%s swap=%s",
		strings.TrimSpace(string(memMax)), strings.TrimSpace(string(swapMax)))
}

func TestNamespace_PIDIsolation(t *testing.T) {
	requireCgroupIntegration(t)

	bin := buildHealthWorker(t)

	factory := NewProcessFactory(bin).
		WithHealthPath("/health").
		WithStartTimeout(10 * time.Second).
		WithStartHealthCheckDelay(100 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	worker, err := factory.Spawn(ctx)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer worker.Close()

	pw, ok := worker.(*ProcessWorker)
	if !ok {
		t.Fatal("expected *ProcessWorker from Spawn")
	}
	pw.mu.Lock()
	hostPID := pw.cmd.Process.Pid
	pw.mu.Unlock()

	statusFile := fmt.Sprintf("/proc/%d/status", hostPID)
	status, err := os.ReadFile(statusFile)
	if err != nil {
		t.Fatalf("read %s: %v", statusFile, err)
	}

	insidePID, ok := parseInnermostNSpid(string(status))
	if !ok {
		t.Fatalf("NSpid line not found in %s:\n%s", statusFile, string(status))
	}
	if insidePID != 1 {
		t.Fatalf("expected worker to be PID 1 inside its namespace, got %d", insidePID)
	}
	t.Logf("NSpid verified: host pid=%d, namespace pid=%d", hostPID, insidePID)
}

func parseInnermostNSpid(status string) (int, bool) {
	for _, line := range strings.Split(status, "\n") {
		if !strings.HasPrefix(line, "NSpid:") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "NSpid:"))
		if len(fields) == 0 {
			return 0, false
		}
		last := fields[len(fields)-1]
		pid, err := strconv.Atoi(last)
		if err != nil {
			return 0, false
		}
		return pid, true
	}
	return 0, false
}
