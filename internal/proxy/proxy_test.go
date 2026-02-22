package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ggmolly/claude-gate/internal/token"
	"github.com/ggmolly/claude-gate/testutil"
)

func setupProxy(t *testing.T, upstream *httptest.Server) (*ProxyHandler, <-chan usageEntry) {
	t.Helper()

	s := testutil.NewTestStore(t)

	// Create a real token.
	_, err := s.CreateRealToken("test-real", "sk-real-token-123", "")
	if err != nil {
		t.Fatalf("create real token: %v", err)
	}

	// Create a gate token.
	_, err = s.CreateGateToken("test-gate")
	if err != nil {
		t.Fatalf("create gate token: %v", err)
	}

	// Set up token manager.
	mgr := token.NewManager(s, 5, 10*time.Minute)
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("start token manager: %v", err)
	}
	t.Cleanup(mgr.Stop)

	ch := make(chan usageEntry, 100)
	handler, err := NewProxyHandler(s, mgr, upstream.URL, ch)
	if err != nil {
		t.Fatalf("create proxy handler: %v", err)
	}

	return handler, ch
}

func TestProxyHandler_SSEStream(t *testing.T) {
	upstream := testutil.NewMockUpstream(t)
	defer upstream.Close()

	handler, usageCh := setupProxy(t, upstream)
	s := testutil.NewTestStore(t)

	// We need the gate token value. Create one in the same store the handler uses.
	// Reuse the handler's store by getting gate tokens through the proxy's store.
	_ = s // unused, we use the handler's store directly

	// Get the gate token from the handler's store.
	gateTokens, err := handler.store.ListGateTokens()
	if err != nil || len(gateTokens) == 0 {
		t.Fatalf("list gate tokens: %v", err)
	}
	gateToken := gateTokens[0]

	// Build request.
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-20250514","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	req.Header.Set("Authorization", "Bearer "+gateToken.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	// Body should contain SSE events.
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "message_start") {
		t.Error("response body does not contain message_start event")
	}

	// Check usage was sent.
	select {
	case entry := <-usageCh:
		if entry.Usage.InputTokens != 25 {
			t.Errorf("InputTokens = %d, want 25", entry.Usage.InputTokens)
		}
		if entry.Usage.OutputTokens != 15 {
			t.Errorf("OutputTokens = %d, want 15", entry.Usage.OutputTokens)
		}
		if entry.GateTokenID != gateToken.ID {
			t.Errorf("GateTokenID = %q, want %q", entry.GateTokenID, gateToken.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for usage entry")
	}
}

func TestProxyHandler_JSONResponse(t *testing.T) {
	upstream := testutil.NewMockUpstreamJSON(t)
	defer upstream.Close()

	handler, usageCh := setupProxy(t, upstream)

	gateTokens, err := handler.store.ListGateTokens()
	if err != nil || len(gateTokens) == 0 {
		t.Fatalf("list gate tokens: %v", err)
	}
	gateToken := gateTokens[0]

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-20250514","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+gateToken.Token)
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	select {
	case entry := <-usageCh:
		if entry.Usage.InputTokens != 25 {
			t.Errorf("InputTokens = %d, want 25", entry.Usage.InputTokens)
		}
		if entry.Usage.OutputTokens != 15 {
			t.Errorf("OutputTokens = %d, want 15", entry.Usage.OutputTokens)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for usage entry")
	}
}

func TestProxyHandler_InvalidToken(t *testing.T) {
	upstream := testutil.NewMockUpstream(t)
	defer upstream.Close()

	handler, _ := setupProxy(t, upstream)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer invalid-token")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}

	var resp map[string]string
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp["error"] == "" {
		t.Error("expected error message in response")
	}
}

func TestProxyHandler_MissingAuth(t *testing.T) {
	upstream := testutil.NewMockUpstream(t)
	defer upstream.Close()

	handler, _ := setupProxy(t, upstream)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	// No Authorization header.

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestProxyHandler_TokenReplacement(t *testing.T) {
	// Verify that the upstream receives the real token, not the gate token.
	var receivedAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"model":"claude-sonnet-4-20250514","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer upstream.Close()

	handler, _ := setupProxy(t, upstream)

	gateTokens, _ := handler.store.ListGateTokens()
	gateToken := gateTokens[0]

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+gateToken.Token)
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if receivedAuth != "Bearer sk-real-token-123" {
		t.Errorf("upstream received auth %q, want %q", receivedAuth, "Bearer sk-real-token-123")
	}
}

func TestProxyHandler_InactiveGateToken(t *testing.T) {
	upstream := testutil.NewMockUpstream(t)
	defer upstream.Close()

	handler, _ := setupProxy(t, upstream)

	gateTokens, _ := handler.store.ListGateTokens()
	gateToken := gateTokens[0]

	// Deactivate the gate token.
	if err := handler.store.SetGateTokenActive(gateToken.ID, false); err != nil {
		t.Fatalf("deactivate gate token: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer "+gateToken.Token)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for inactive token, got %d", rec.Code)
	}
}

func TestUsageWriter_BatchFlush(t *testing.T) {
	s := testutil.NewTestStore(t)

	// Create tokens for usage logging.
	rt, _ := s.CreateRealToken("real", "sk-test", "")
	gt, _ := s.CreateGateToken("gate")

	ch := make(chan usageEntry, 10)
	writer := NewUsageWriter(ch, s)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		writer.Run(ctx)
		close(done)
	}()

	// Send a usage entry.
	ch <- usageEntry{
		GateTokenID: gt.ID,
		RealTokenID: rt.ID,
		Usage: UsageData{
			InputTokens:  100,
			OutputTokens: 50,
			Model:        "claude-sonnet-4-20250514",
		},
		RequestPath: "/v1/messages",
		StatusCode:  200,
	}

	// Wait for flush (1s timer + some margin).
	time.Sleep(2 * time.Second)
	cancel()
	<-done

	// Check that usage was logged.
	logs, err := s.ListUsageLogs(10, 0)
	if err != nil {
		t.Fatalf("list usage logs: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 usage log, got %d", len(logs))
	}
	if logs[0].InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100", logs[0].InputTokens)
	}
	if logs[0].OutputTokens != 50 {
		t.Errorf("OutputTokens = %d, want 50", logs[0].OutputTokens)
	}

	// Check cumulative counters were updated.
	updatedGT, _ := s.GetGateToken(gt.ID)
	if updatedGT.TotalInputTokens != 100 {
		t.Errorf("gate token TotalInputTokens = %d, want 100", updatedGT.TotalInputTokens)
	}
	updatedRT, _ := s.GetRealToken(rt.ID)
	if updatedRT.TotalInputTokens != 100 {
		t.Errorf("real token TotalInputTokens = %d, want 100", updatedRT.TotalInputTokens)
	}
}
