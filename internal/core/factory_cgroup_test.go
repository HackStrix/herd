// factory_cgroup_test.go — Unit tests for ProcessFactory cgroup configuration.
//
// No build tag: runs on all platforms (macOS, Linux, Windows).
// No processes are spawned; only field values and option validation are tested.
package core

import (
	"testing"
)

func TestNewProcessFactory_DefaultCgroupPIDs(t *testing.T) {
	f := NewProcessFactory("./fake-binary")
	if f.cgroupPIDs != 100 {
		t.Errorf("expected default cgroupPIDs=100, got %d", f.cgroupPIDs)
	}
}

func TestNewProcessFactory_DefaultMemoryCPUUnlimited(t *testing.T) {
	f := NewProcessFactory("./fake-binary")
	if f.cgroupMemory != 0 {
		t.Errorf("expected default cgroupMemory=0 (unlimited), got %d", f.cgroupMemory)
	}
	if f.cgroupCPU != 0 {
		t.Errorf("expected default cgroupCPU=0 (unlimited), got %d", f.cgroupCPU)
	}
}

func TestNewProcessFactory_DefaultNamespaceFlags(t *testing.T) {
	f := NewProcessFactory("./fake-binary")
	if f.namespaceCloneFlags != defaultNamespaceCloneFlags() {
		t.Errorf("expected default namespaceCloneFlags=%d, got %d", defaultNamespaceCloneFlags(), f.namespaceCloneFlags)
	}
}

func TestWithMemoryLimit_StoresBytes(t *testing.T) {
	const limit = 512 * 1024 * 1024 // 512 MB
	f := NewProcessFactory("./fake-binary").WithMemoryLimit(limit)
	if f.cgroupMemory != limit {
		t.Errorf("expected cgroupMemory=%d, got %d", limit, f.cgroupMemory)
	}
}

func TestWithMemoryLimit_Zero_DisablesLimit(t *testing.T) {
	f := NewProcessFactory("./fake-binary").WithMemoryLimit(512 * 1024 * 1024).WithMemoryLimit(0)
	if f.cgroupMemory != 0 {
		t.Errorf("expected cgroupMemory=0 after zeroing, got %d", f.cgroupMemory)
	}
}

func TestWithMemoryLimit_NegativePanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for negative WithMemoryLimit")
		}
	}()
	NewProcessFactory("./fake-binary").WithMemoryLimit(-1)
}

func TestWithCPULimit_HalfCore(t *testing.T) {
	f := NewProcessFactory("./fake-binary").WithCPULimit(0.5)
	if f.cgroupCPU != 50_000 {
		t.Errorf("expected cgroupCPU=50000 for 0.5 cores, got %d", f.cgroupCPU)
	}
}

func TestWithCPULimit_TwoCores(t *testing.T) {
	f := NewProcessFactory("./fake-binary").WithCPULimit(2.0)
	if f.cgroupCPU != 200_000 {
		t.Errorf("expected cgroupCPU=200000 for 2.0 cores, got %d", f.cgroupCPU)
	}
}

func TestWithCPULimit_Zero_DisablesLimit(t *testing.T) {
	f := NewProcessFactory("./fake-binary").WithCPULimit(1.0).WithCPULimit(0)
	if f.cgroupCPU != 0 {
		t.Errorf("expected cgroupCPU=0 after zeroing, got %d", f.cgroupCPU)
	}
}

func TestWithCPULimit_NegativePanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for negative WithCPULimit")
		}
	}()
	NewProcessFactory("./fake-binary").WithCPULimit(-0.5)
}

func TestWithPIDsLimit_Explicit(t *testing.T) {
	f := NewProcessFactory("./fake-binary").WithPIDsLimit(50)
	if f.cgroupPIDs != 50 {
		t.Errorf("expected cgroupPIDs=50, got %d", f.cgroupPIDs)
	}
}

func TestWithPIDsLimit_Unlimited(t *testing.T) {
	f := NewProcessFactory("./fake-binary").WithPIDsLimit(-1)
	if f.cgroupPIDs != -1 {
		t.Errorf("expected cgroupPIDs=-1 for unlimited, got %d", f.cgroupPIDs)
	}
}

func TestWithPIDsLimit_ZeroPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for WithPIDsLimit(0)")
		}
	}()
	NewProcessFactory("./fake-binary").WithPIDsLimit(0)
}

func TestWithPIDsLimit_LessThanNegativeOnePanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for WithPIDsLimit(-2)")
		}
	}()
	NewProcessFactory("./fake-binary").WithPIDsLimit(-2)
}

func TestWithPIDsLimit_Chaining(t *testing.T) {
	// Verify the builder returns the same factory pointer for fluent chaining.
	f := NewProcessFactory("./fake-binary")
	f2 := f.WithPIDsLimit(25)
	if f != f2 {
		t.Error("WithPIDsLimit should return the same *ProcessFactory for chaining")
	}
}

func TestWithMemoryLimit_Chaining(t *testing.T) {
	f := NewProcessFactory("./fake-binary")
	f2 := f.WithMemoryLimit(1024)
	if f != f2 {
		t.Error("WithMemoryLimit should return the same *ProcessFactory for chaining")
	}
}

func TestWithCPULimit_Chaining(t *testing.T) {
	f := NewProcessFactory("./fake-binary")
	f2 := f.WithCPULimit(1.0)
	if f != f2 {
		t.Error("WithCPULimit should return the same *ProcessFactory for chaining")
	}
}
