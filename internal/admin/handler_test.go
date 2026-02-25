package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/ggmolly/claude-gate/internal/logging"
	"github.com/ggmolly/claude-gate/internal/store"
)

const testAdminSecret = "test-admin-secret"

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("create test store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func setupTestHandler(t *testing.T) (*AdminHandler, *http.ServeMux, *store.Store) {
	t.Helper()
	s := newTestStore(t)
	h := NewAdminHandler(s, nil, logging.Discard())
	mux := http.NewServeMux()
	h.Register(mux, testAdminSecret, nil)
	return h, mux, s
}

func authedRequest(method, path string, body any) *http.Request {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Authorization", "Bearer "+testAdminSecret)
	req.Header.Set("Content-Type", "application/json")
	return req
}

func doRequest(mux http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

func parseResponse(t *testing.T, rr *httptest.ResponseRecorder) map[string]json.RawMessage {
	t.Helper()
	var resp map[string]json.RawMessage
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	return resp
}

// --- Middleware tests ---

func TestAdminAuth_ValidToken(t *testing.T) {
	handler := AdminAuth(testAdminSecret, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/admin/api/test", nil)
	req.Header.Set("Authorization", "Bearer "+testAdminSecret)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
}

func TestAdminAuth_MissingToken(t *testing.T) {
	handler := AdminAuth(testAdminSecret, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/admin/api/test", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
}

func TestAdminAuth_InvalidToken(t *testing.T) {
	handler := AdminAuth(testAdminSecret, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/admin/api/test", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
}

// --- Real Token tests ---

func TestListRealTokens_Empty(t *testing.T) {
	_, mux, _ := setupTestHandler(t)
	req := authedRequest("GET", "/admin/api/real-tokens", nil)
	rr := doRequest(mux, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	resp := parseResponse(t, rr)
	var tokens []RealTokenResponse
	if err := json.Unmarshal(resp["data"], &tokens); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if len(tokens) != 0 {
		t.Errorf("expected 0 tokens, got %d", len(tokens))
	}
}

func TestCreateRealToken(t *testing.T) {
	_, mux, _ := setupTestHandler(t)
	body := createRealTokenRequest{
		Name:         "test-token",
		AccessToken:  "access-123",
		RefreshToken: "refresh-456",
	}
	req := authedRequest("POST", "/admin/api/real-tokens", body)
	rr := doRequest(mux, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}
	resp := parseResponse(t, rr)
	var token RealTokenResponse
	if err := json.Unmarshal(resp["data"], &token); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if token.Name != "test-token" {
		t.Errorf("expected name 'test-token', got %q", token.Name)
	}
	if token.ID == "" {
		t.Error("expected non-empty ID")
	}
}

func TestCreateRealToken_MissingFields(t *testing.T) {
	_, mux, _ := setupTestHandler(t)
	body := map[string]string{"name": "test"}
	req := authedRequest("POST", "/admin/api/real-tokens", body)
	rr := doRequest(mux, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

func TestListRealTokens_OmitsSecrets(t *testing.T) {
	_, mux, s := setupTestHandler(t)
	ctx := context.Background()
	_, _ = s.CreateRealToken(ctx, "test", "secret-access", "secret-refresh")

	req := authedRequest("GET", "/admin/api/real-tokens", nil)
	rr := doRequest(mux, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	// Check raw JSON does not contain access_token or refresh_token
	body := rr.Body.String()
	if bytes.Contains([]byte(body), []byte("secret-access")) {
		t.Error("response contains access_token value")
	}
	if bytes.Contains([]byte(body), []byte("secret-refresh")) {
		t.Error("response contains refresh_token value")
	}
}

func TestUpdateRealToken(t *testing.T) {
	_, mux, s := setupTestHandler(t)
	ctx := context.Background()
	tok, _ := s.CreateRealToken(ctx, "old-name", "a", "r")

	body := updateTokenRequest{Name: "new-name"}
	req := authedRequest("PUT", "/admin/api/real-tokens/"+tok.ID, body)
	rr := doRequest(mux, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestUpdateRealToken_NotFound(t *testing.T) {
	_, mux, _ := setupTestHandler(t)
	body := updateTokenRequest{Name: "new-name"}
	req := authedRequest("PUT", "/admin/api/real-tokens/nonexistent", body)
	rr := doRequest(mux, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rr.Code)
	}
}

func TestDeleteRealToken(t *testing.T) {
	_, mux, s := setupTestHandler(t)
	ctx := context.Background()
	tok, _ := s.CreateRealToken(ctx, "to-delete", "a", "r")

	req := authedRequest("DELETE", "/admin/api/real-tokens/"+tok.ID, nil)
	rr := doRequest(mux, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestDeleteRealToken_NotFound(t *testing.T) {
	_, mux, _ := setupTestHandler(t)
	req := authedRequest("DELETE", "/admin/api/real-tokens/nonexistent", nil)
	rr := doRequest(mux, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rr.Code)
	}
}

func TestActivateRealToken(t *testing.T) {
	_, mux, s := setupTestHandler(t)
	ctx := context.Background()
	tok, _ := s.CreateRealToken(ctx, "tok", "a", "r")
	_ = s.SetRealTokenActive(ctx, tok.ID, false)

	req := authedRequest("POST", "/admin/api/real-tokens/"+tok.ID+"/activate", nil)
	rr := doRequest(mux, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestDeactivateRealToken(t *testing.T) {
	_, mux, s := setupTestHandler(t)
	ctx := context.Background()
	tok, _ := s.CreateRealToken(ctx, "tok", "a", "r")

	req := authedRequest("POST", "/admin/api/real-tokens/"+tok.ID+"/deactivate", nil)
	rr := doRequest(mux, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

// --- Gate Token tests ---

func TestListGateTokens_Empty(t *testing.T) {
	_, mux, _ := setupTestHandler(t)
	req := authedRequest("GET", "/admin/api/gate-tokens", nil)
	rr := doRequest(mux, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	resp := parseResponse(t, rr)
	var tokens []store.GateToken
	if err := json.Unmarshal(resp["data"], &tokens); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if len(tokens) != 0 {
		t.Errorf("expected 0 tokens, got %d", len(tokens))
	}
}

func TestCreateGateToken(t *testing.T) {
	_, mux, _ := setupTestHandler(t)
	body := createGateTokenRequest{Name: "my-gate"}
	req := authedRequest("POST", "/admin/api/gate-tokens", body)
	rr := doRequest(mux, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}
	resp := parseResponse(t, rr)
	var token store.GateToken
	if err := json.Unmarshal(resp["data"], &token); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if token.Name != "my-gate" {
		t.Errorf("expected name 'my-gate', got %q", token.Name)
	}
	if token.Token == "" {
		t.Error("expected token to be returned on create")
	}
}

func TestCreateGateToken_MissingName(t *testing.T) {
	_, mux, _ := setupTestHandler(t)
	body := map[string]string{}
	req := authedRequest("POST", "/admin/api/gate-tokens", body)
	rr := doRequest(mux, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

func TestUpdateGateToken(t *testing.T) {
	_, mux, s := setupTestHandler(t)
	ctx := context.Background()
	tok, _ := s.CreateGateToken(ctx, "old-name", "")

	body := updateTokenRequest{Name: "new-name"}
	req := authedRequest("PUT", "/admin/api/gate-tokens/"+tok.ID, body)
	rr := doRequest(mux, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestUpdateGateToken_NotFound(t *testing.T) {
	_, mux, _ := setupTestHandler(t)
	body := updateTokenRequest{Name: "new-name"}
	req := authedRequest("PUT", "/admin/api/gate-tokens/nonexistent", body)
	rr := doRequest(mux, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rr.Code)
	}
}

func TestDeleteGateToken(t *testing.T) {
	_, mux, s := setupTestHandler(t)
	ctx := context.Background()
	tok, _ := s.CreateGateToken(ctx, "to-delete", "")

	req := authedRequest("DELETE", "/admin/api/gate-tokens/"+tok.ID, nil)
	rr := doRequest(mux, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestDeleteGateToken_NotFound(t *testing.T) {
	_, mux, _ := setupTestHandler(t)
	req := authedRequest("DELETE", "/admin/api/gate-tokens/nonexistent", nil)
	rr := doRequest(mux, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rr.Code)
	}
}

func TestActivateGateToken(t *testing.T) {
	_, mux, s := setupTestHandler(t)
	ctx := context.Background()
	tok, _ := s.CreateGateToken(ctx, "tok", "")
	_ = s.SetGateTokenActive(ctx, tok.ID, false)

	req := authedRequest("POST", "/admin/api/gate-tokens/"+tok.ID+"/activate", nil)
	rr := doRequest(mux, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestDeactivateGateToken(t *testing.T) {
	_, mux, s := setupTestHandler(t)
	ctx := context.Background()
	tok, _ := s.CreateGateToken(ctx, "tok", "")

	req := authedRequest("POST", "/admin/api/gate-tokens/"+tok.ID+"/deactivate", nil)
	rr := doRequest(mux, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

// --- Usage tests ---

func TestGetUsage(t *testing.T) {
	_, mux, s := setupTestHandler(t)
	ctx := context.Background()
	rt, _ := s.CreateRealToken(ctx, "rt", "a", "r")
	gt, _ := s.CreateGateToken(ctx, "gt", "")
	_ = s.InsertUsageLog(ctx, &store.UsageLog{
		GateTokenID:  gt.ID,
		RealTokenID:  rt.ID,
		Model:        "claude-sonnet-4-20250514",
		InputTokens:  100,
		OutputTokens: 50,
		RequestPath:  "/v1/messages",
		StatusCode:   200,
	})

	req := authedRequest("GET", "/admin/api/usage", nil)
	rr := doRequest(mux, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	resp := parseResponse(t, rr)
	var stats store.UsageStats
	if err := json.Unmarshal(resp["data"], &stats); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if stats.RequestCount != 1 {
		t.Errorf("expected 1 request, got %d", stats.RequestCount)
	}
	if stats.TotalInputTokens != 100 {
		t.Errorf("expected 100 input tokens, got %d", stats.TotalInputTokens)
	}
}

func TestGetUsageByRealToken(t *testing.T) {
	_, mux, s := setupTestHandler(t)
	ctx := context.Background()
	rt, _ := s.CreateRealToken(ctx, "rt", "a", "r")
	gt, _ := s.CreateGateToken(ctx, "gt", "")
	_ = s.InsertUsageLog(ctx, &store.UsageLog{
		GateTokenID:  gt.ID,
		RealTokenID:  rt.ID,
		Model:        "claude-sonnet-4-20250514",
		InputTokens:  200,
		OutputTokens: 100,
		RequestPath:  "/v1/messages",
		StatusCode:   200,
	})

	req := authedRequest("GET", "/admin/api/usage/real-tokens/"+rt.ID, nil)
	rr := doRequest(mux, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	resp := parseResponse(t, rr)
	var stats store.UsageStats
	if err := json.Unmarshal(resp["data"], &stats); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if stats.TotalInputTokens != 200 {
		t.Errorf("expected 200 input tokens, got %d", stats.TotalInputTokens)
	}
}

func TestGetUsageByGateToken(t *testing.T) {
	_, mux, s := setupTestHandler(t)
	ctx := context.Background()
	rt, _ := s.CreateRealToken(ctx, "rt", "a", "r")
	gt, _ := s.CreateGateToken(ctx, "gt", "")
	_ = s.InsertUsageLog(ctx, &store.UsageLog{
		GateTokenID:  gt.ID,
		RealTokenID:  rt.ID,
		Model:        "claude-sonnet-4-20250514",
		InputTokens:  300,
		OutputTokens: 150,
		RequestPath:  "/v1/messages",
		StatusCode:   200,
	})

	req := authedRequest("GET", "/admin/api/usage/gate-tokens/"+gt.ID, nil)
	rr := doRequest(mux, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	resp := parseResponse(t, rr)
	var stats store.UsageStats
	if err := json.Unmarshal(resp["data"], &stats); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if stats.TotalInputTokens != 300 {
		t.Errorf("expected 300 input tokens, got %d", stats.TotalInputTokens)
	}
}

func TestGetUsage_WithSinceParam(t *testing.T) {
	_, mux, _ := setupTestHandler(t)

	req := authedRequest("GET", "/admin/api/usage?since=2024-01-01T00:00:00Z", nil)
	rr := doRequest(mux, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}
