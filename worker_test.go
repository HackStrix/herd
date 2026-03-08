package herd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"testing"
	"time"
)

// roundTripFunc implements http.RoundTripper
type roundTripFunc func(req *http.Request) *http.Response

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req), nil
}

func TestWorkerHealthy(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		wantErr    bool
	}{
		{"200 OK", http.StatusOK, false},
		{"500 Internal Error", http.StatusInternalServerError, true},
		{"404 Not Found", http.StatusNotFound, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{
				Transport: roundTripFunc(func(req *http.Request) *http.Response {
					return &http.Response{
						StatusCode: tc.statusCode,
						Body:       io.NopCloser(bytes.NewBufferString("dummy body")),
						Header:     make(http.Header),
					}
				}),
			}

			w := &processWorker{
				id:         "test-worker",
				address:    "http://127.0.0.1:9999",
				healthPath: "/health",
				client:     client,
			}

			ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
			defer cancel()

			err := w.Healthy(ctx)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Healthy() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestWorkerHealthy_RequestError(t *testing.T) {
	// A transport that returns an error

	client := &http.Client{
		Transport: &errorRoundTripper{err: fmt.Errorf("network reset")},
	}

	w := &processWorker{
		id:         "test-worker",
		address:    "http://127.0.0.1:9999",
		healthPath: "/health",
		client:     client,
	}

	ctx := context.Background()
	err := w.Healthy(ctx)
	if err == nil {
		t.Fatalf("expected error on Healthy due to transport failure, got nil")
	}
}

type errorRoundTripper struct {
	err error
}

func (e *errorRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return nil, e.err
}

func TestWorkerClose_KillsProcess(t *testing.T) {
	// Start a long running process that we will kill
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start sleep command: %v", err)
	}

	w := &processWorker{
		id:  "test-worker-kill",
		cmd: cmd,
	}

	err := w.Close()
	if err != nil {
		t.Fatalf("Close() failed: %v", err)
	}

	// Wait for process to exit
	err = cmd.Wait()
	if err == nil {
		t.Fatalf("expected process to have an error from being killed, but it exited cleanly")
	}

	// Double check that it's drained
	if w.draining.Load() != 1 {
		t.Errorf("expected worker to be marked as draining, got %d", w.draining.Load())
	}
}
