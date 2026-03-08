package herd

import (
	"testing"
)

func TestProcessFactory_WithMemoryLimit(t *testing.T) {
	factory := NewProcessFactory("echo", "hello").WithMemoryLimit(1024 * 1024)
	if factory.memoryLimitBytes != 1024*1024 {
		t.Errorf("expected 1024*1024 limit bytes, got %d", factory.memoryLimitBytes)
	}
	t.Logf("factory: %+v", factory)
}
