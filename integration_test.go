package herd_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/hackstrix/herd"
	"github.com/hackstrix/herd/proxy"
)

// In integration test, we use the same TestHelperProcess defined in factory_test.go
// as our dummy target. The TestMain func or factory_test setup handles it.
func TestIntegration_EndToEnd(t *testing.T) {
	// Stand up a ProcessFactory that spawns our dummy HTTP server
	factory := herd.NewProcessFactory(os.Args[0], "-test.run=TestHelperProcess")
	factory.WithEnv("GO_WANT_HELPER_PROCESS=1")
	factory.WithEnv("HELPER_MODE=success")

	// Create pool: 1 worker min, 1 max.
	pool, err := herd.New(factory, herd.WithAutoScale(1, 1), herd.WithTTL(500*time.Millisecond))
	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}
	defer pool.Shutdown(context.Background())

	// Wait briefly for min workers to be spawned
	time.Sleep(200 * time.Millisecond)

	// Build the reverse proxy using a custom session ID extractor
	p := proxy.NewReverseProxy(pool, func(r *http.Request) string {
		return r.Header.Get("X-Session-ID")
	})

	proxyServer := httptest.NewServer(p)
	defer proxyServer.Close()

	// ----------------------------------------------------
	// Request 1: Should successfully hit the dummy worker
	// ----------------------------------------------------
	req1, _ := http.NewRequest("GET", proxyServer.URL+"/", nil)
	req1.Header.Set("X-Session-ID", "integration-session-1")

	client := proxyServer.Client()
	resp1, err := client.Do(req1)
	if err != nil {
		t.Fatalf("proxy request 1 failed: %v", err)
	}
	defer resp1.Body.Close()

	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("expected proxy to return 200 OK, got %d", resp1.StatusCode)
	}

	body1, _ := io.ReadAll(resp1.Body)
	if string(body1) != "OK" {
		t.Errorf("expected standard 'OK' from dummy worker, got %q", string(body1))
	}

	// ----------------------------------------------------
	// Pool Stats Check
	// ----------------------------------------------------
	// After the proxy request finishes, the session is released and worker returned to pool.
	stats := pool.Stats()
	if stats.ActiveSessions != 0 {
		t.Errorf("expected 0 active sessions after request, got %d", stats.ActiveSessions)
	}
	if stats.AvailableWorkers != 1 {
		t.Errorf("expected 1 available worker, got %d", stats.AvailableWorkers)
	}

	// ----------------------------------------------------
	// TTL Expiry Check
	// ----------------------------------------------------
	// Wait out the TTL logic (500ms)
	time.Sleep(1 * time.Second)

	// Since we set TTL=500ms, the session should be evicted, though with min=1, the worker is kept!
	// Actually, TTL sweeper drops the worker completely, then the background maintenance
	// loop will immediately notice we are < min and spawn a new one.
	statsAfterTTL := pool.Stats()
	if statsAfterTTL.ActiveSessions != 0 {
		t.Errorf("expected 0 active sessions, got %d", statsAfterTTL.ActiveSessions)
	}
}
