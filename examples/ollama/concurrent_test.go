// concurrent_test.go — integration test: 10 concurrent chat requests,
// each pinned to a unique X-Agent-ID, against a live herd-ollama gateway.
//
// # Prerequisites
//
//  1. Gateway must be running:  go run . --port 8080 --min 1 --max 5
//  2. Model must be pulled:     ollama pull qwen2.5:0.5b
//
// # Run
//
//	go test -v -run TestConcurrentChat -timeout 120s
//
// Flags (via -args):
//
//	-gateway  http address of the gateway  (default: http://localhost:8080)
//	-model    ollama model name            (default: qwen2.5:0.5b)
//	-agents   number of concurrent agents  (default: 10)
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"
)

// ── flags ────────────────────────────────────────────────────────────────────

var (
	flagGateway = flag.String("gateway", "http://127.0.0.1:8080", "herd-ollama gateway address")
	flagModel   = flag.String("model", "Qwen3:0.6B", "ollama model to use")
	flagAgents  = flag.Int("agents", 3, "number of concurrent agents")
)

// ── types ────────────────────────────────────────────────────────────────────

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Message chatMessage `json:"message"`
	Done    bool        `json:"done"`
}

type result struct {
	agentID  string
	reply    string
	duration time.Duration
	err      error
}

// ── test ─────────────────────────────────────────────────────────────────────

// TestConcurrentChat fires -agents concurrent chat requests to the gateway.
// Each request uses a unique X-Agent-ID so herd pins it to a distinct worker.
// The test passes if every agent gets a non-empty reply.
func TestConcurrentChat(t *testing.T) {
	gateway := *flagGateway
	model := *flagModel
	n := *flagAgents

	t.Logf("gateway=%s  model=%s  agents=%d", gateway, model, n)

	// Verify gateway is reachable before spinning up goroutines.
	hCtx, hCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer hCancel()
	req, _ := http.NewRequestWithContext(hCtx, http.MethodGet, gateway+"/health", nil)
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusOK {
		if err != nil {
			t.Fatalf("gateway unreachable at %s: %v — is `go run . --port 8080` running?", gateway, err)
		}
		resp.Body.Close()
		t.Fatalf("gateway health check returned %d — is the gateway running?", resp.StatusCode)
	}

	results := make([]result, n)
	var wg sync.WaitGroup

	for i := range n {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			agentID := fmt.Sprintf("agent-%02d", idx)
			prompt := fmt.Sprintf("Reply in exactly 5 words. Agent number %d checking in.", idx)
			results[idx] = sendChat(agentID, gateway, model, prompt)
		}(i)
	}

	wg.Wait()

	// ── Report ────────────────────────────────────────────────────────────
	t.Log("\n── Results ──────────────────────────────────────────────────")
	var failed int
	var totalDuration time.Duration
	for _, r := range results {
		if r.err != nil {
			t.Errorf("[%s] ERROR: %v", r.agentID, r.err)
			failed++
			continue
		}
		t.Logf("[%s] (%s) %q", r.agentID, r.duration.Round(time.Millisecond), r.reply)
		totalDuration += r.duration
	}

	passed := n - failed
	t.Logf("\n── Summary ──────────────────────────────────────────────────")
	t.Logf("  passed:   %d / %d", passed, n)
	t.Logf("  failed:   %d / %d", failed, n)
	if passed > 0 {
		t.Logf("  avg time: %s", (totalDuration / time.Duration(passed)).Round(time.Millisecond))
	}

	if failed > 0 {
		t.Fatalf("%d/%d agents failed", failed, n)
	}
}

// sendChat sends one non-streaming POST /api/chat to the gateway.
func sendChat(agentID, gateway, model, prompt string) result {
	start := time.Now()

	body, _ := json.Marshal(chatRequest{
		Model:    model,
		Messages: []chatMessage{{Role: "user", Content: prompt}},
		Stream:   false, // single JSON response — easier to parse in a test
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, gateway+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return result{agentID: agentID, err: fmt.Errorf("build request: %w", err)}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agent-ID", agentID)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return result{agentID: agentID, err: fmt.Errorf("POST /api/chat: %w", err), duration: time.Since(start)}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return result{
			agentID:  agentID,
			err:      fmt.Errorf("unexpected status %d: %s", resp.StatusCode, raw),
			duration: time.Since(start),
		}
	}

	var cr chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return result{agentID: agentID, err: fmt.Errorf("decode response: %w", err), duration: time.Since(start)}
	}
	if cr.Message.Content == "" {
		return result{agentID: agentID, err: fmt.Errorf("empty reply from model"), duration: time.Since(start)}
	}

	return result{
		agentID:  agentID,
		reply:    cr.Message.Content,
		duration: time.Since(start),
	}
}
