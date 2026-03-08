package herd

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"
)

// TestHelperProcess isn't a real test. It's used as a dummy worker process
// for ProcessFactory tests.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}

	mode := os.Getenv("HELPER_MODE")
	port := os.Getenv("PORT")

	if mode == "immediate_exit" {
		os.Exit(1)
	}

	if mode == "hang" {
		// Just sleep forever, never start HTTP server
		time.Sleep(1 * time.Hour)
		os.Exit(0)
	}

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	// Start server
	if err := http.ListenAndServe("127.0.0.1:"+port, nil); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestProcessFactory_SpawnSuccess(t *testing.T) {
	factory := NewProcessFactory(os.Args[0], "-test.run=TestHelperProcess")
	factory.WithEnv("GO_WANT_HELPER_PROCESS=1")
	factory.WithEnv("HELPER_MODE=success")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	worker, err := factory.Spawn(ctx)
	if err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}
	defer worker.Close()

	if worker.ID() == "" {
		t.Errorf("worker ID is empty")
	}
	if worker.Address() == "" {
		t.Errorf("worker Address is empty")
	}

	// Double check health passes
	err = worker.Healthy(ctx)
	if err != nil {
		t.Errorf("worker.Healthy failed: %v", err)
	}
}

func TestProcessFactory_SpawnImmediateExit(t *testing.T) {
	factory := NewProcessFactory(os.Args[0], "-test.run=TestHelperProcess")
	factory.WithEnv("GO_WANT_HELPER_PROCESS=1")
	factory.WithEnv("HELPER_MODE=immediate_exit")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := factory.Spawn(ctx)
	if err == nil {
		t.Fatalf("expected Spawn to fail when process exits immediately, got nil")
	}
}

func TestProcessFactory_SpawnTimeout(t *testing.T) {
	factory := NewProcessFactory(os.Args[0], "-test.run=TestHelperProcess")
	factory.WithEnv("GO_WANT_HELPER_PROCESS=1")
	factory.WithEnv("HELPER_MODE=hang")
	factory.WithStartTimeout(500 * time.Millisecond) // Short timeout

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := factory.Spawn(ctx)
	if err == nil {
		t.Fatalf("expected Spawn to fail due to timeout, got nil")
	}
}

func TestProcessFactory_WithMemoryLimit(t *testing.T) {
	factory := NewProcessFactory("echo", "hello").WithMemoryLimit(1024 * 1024)
	if factory.memoryLimitBytes != 1024*1024 {
		t.Errorf("expected 1024*1024 limit bytes, got %d", factory.memoryLimitBytes)
	}
}
