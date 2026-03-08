package proxy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hackstrix/herd"
	"github.com/hackstrix/herd/proxy"
)

// ---------------------------------------------------------------------------
// Mock Pool
// ---------------------------------------------------------------------------

type mockClient struct{}

type mockWorker struct {
	id      string
	address string
}

func (m *mockWorker) ID() string                        { return m.id }
func (m *mockWorker) Address() string                   { return m.address }
func (m *mockWorker) Client() *mockClient               { return &mockClient{} }
func (m *mockWorker) Healthy(ctx context.Context) error { return nil }
func (m *mockWorker) Close() error                      { return nil }

type mockFactory struct {
	worker *mockWorker
}

func (m *mockFactory) Spawn(ctx context.Context) (herd.Worker[*mockClient], error) {
	return m.worker, nil
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestReverseProxy_SuccessRoutesRequest(t *testing.T) {
	// 1. Create a dummy downstream server that the worker "represents"
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend-Handled", "true")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("hello from worker"))
	}))
	defer targetServer.Close()

	// 2. Build the pool with our mock setup
	factory := &mockFactory{
		worker: &mockWorker{
			id:      "worker-1",
			address: targetServer.URL,
		},
	}
	pool, err := herd.New(factory, herd.WithAutoScale(1, 1))
	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}

	// 3. Create the proxy
	p := proxy.NewReverseProxy(pool, func(r *http.Request) string {
		return r.Header.Get("X-Session-ID")
	})

	proxyServer := httptest.NewServer(p)
	defer proxyServer.Close()

	// 4. Send a request hitting the proxy with session ID
	req, _ := http.NewRequest("GET", proxyServer.URL+"/", nil)
	req.Header.Set("X-Session-ID", "session-123")

	client := proxyServer.Client()
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("proxy request failed: %v", err)
	}
	defer resp.Body.Close()

	// 5. Verify the route occurred
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Backend-Handled"); got != "true" {
		t.Errorf("expected header X-Backend-Handled=true, got %q", got)
	}

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello from worker" {
		t.Errorf("expected body 'hello from worker', got %q", string(body))
	}
}

func TestReverseProxy_MissingSessionIDReturns400(t *testing.T) {
	pool, _ := herd.New(&mockFactory{}, herd.WithAutoScale(1, 1))
	p := proxy.NewReverseProxy(pool, func(r *http.Request) string {
		return "" // empty session ID
	})

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	p.ServeHTTP(w, req)

	res := w.Result()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request, got %d", res.StatusCode)
	}
}

func TestReverseProxy_AcquireFailureReturns503(t *testing.T) {
	pool, _ := herd.New(&mockFactory{worker: &mockWorker{address: "http://127.0.0.1"}}, herd.WithAutoScale(1, 1))

	// Exhaust the pool by acquiring the only worker
	_, err := pool.Acquire(context.Background(), "other-session")
	if err != nil {
		t.Fatalf("failed to exhaust pool: %v", err)
	}

	p := proxy.NewReverseProxy(pool, func(r *http.Request) string {
		return "session-wait"
	})

	req := httptest.NewRequest("GET", "/", nil)
	// Extremely short timeout so it fails to acquire fast
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()

	p.ServeHTTP(w, req)

	res := w.Result()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503 Service Unavailable, got %d", res.StatusCode)
	}
}
