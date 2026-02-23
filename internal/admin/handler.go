package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ggmolly/claude-gate/internal/store"
)

// RealTokenResponse is a sanitized version of RealToken that omits secrets.
type RealTokenResponse struct {
	ID                string     `json:"id"`
	Name              string     `json:"name"`
	IsActive          bool       `json:"is_active"`
	FailureCount      int        `json:"failure_count"`
	LastFailureAt     *time.Time `json:"last_failure_at,omitempty"`
	LastUsedAt        *time.Time `json:"last_used_at,omitempty"`
	TotalInputTokens  int64      `json:"total_input_tokens"`
	TotalOutputTokens int64      `json:"total_output_tokens"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

func sanitizeRealToken(t *store.RealToken) RealTokenResponse {
	return RealTokenResponse{
		ID:                t.ID,
		Name:              t.Name,
		IsActive:          t.IsActive,
		FailureCount:      t.FailureCount,
		LastFailureAt:     t.LastFailureAt,
		LastUsedAt:        t.LastUsedAt,
		TotalInputTokens:  t.TotalInputTokens,
		TotalOutputTokens: t.TotalOutputTokens,
		CreatedAt:         t.CreatedAt,
		UpdatedAt:         t.UpdatedAt,
	}
}

// AdminHandler handles all admin API endpoints.
type AdminHandler struct {
	store         *store.Store
	logger        *slog.Logger
	onPoolChanged func() // called after real token CRUD to refresh the pool
}

// NewAdminHandler creates a new AdminHandler.
// onPoolChanged is an optional callback invoked after real token mutations.
func NewAdminHandler(s *store.Store, onPoolChanged func(), logger *slog.Logger) *AdminHandler {
	if onPoolChanged == nil {
		onPoolChanged = func() {}
	}
	return &AdminHandler{store: s, onPoolChanged: onPoolChanged, logger: logger}
}

// Register registers all admin API routes on the given mux.
// The secret parameter is used to apply AdminAuth middleware to each route.
// sessionAuth is an optional function to validate session cookies (for Web UI integration).
func (h *AdminHandler) Register(mux *http.ServeMux, secret string, sessionAuth func(*http.Request) bool) {
	auth := AdminAuth(secret, sessionAuth)
	wrap := func(handler http.HandlerFunc) http.Handler {
		return auth(handler)
	}

	// Real tokens
	mux.Handle("GET /admin/api/real-tokens", wrap(h.listRealTokens))
	mux.Handle("POST /admin/api/real-tokens", wrap(h.createRealToken))
	mux.Handle("PUT /admin/api/real-tokens/{id}", wrap(h.updateRealToken))
	mux.Handle("DELETE /admin/api/real-tokens/{id}", wrap(h.deleteRealToken))
	mux.Handle("POST /admin/api/real-tokens/{id}/activate", wrap(h.activateRealToken))
	mux.Handle("POST /admin/api/real-tokens/{id}/deactivate", wrap(h.deactivateRealToken))

	// Gate tokens
	mux.Handle("GET /admin/api/gate-tokens", wrap(h.listGateTokens))
	mux.Handle("POST /admin/api/gate-tokens", wrap(h.createGateToken))
	mux.Handle("PUT /admin/api/gate-tokens/{id}", wrap(h.updateGateToken))
	mux.Handle("DELETE /admin/api/gate-tokens/{id}", wrap(h.deleteGateToken))
	mux.Handle("POST /admin/api/gate-tokens/{id}/activate", wrap(h.activateGateToken))
	mux.Handle("POST /admin/api/gate-tokens/{id}/deactivate", wrap(h.deactivateGateToken))

	// Usage
	mux.Handle("GET /admin/api/usage", wrap(h.getUsage))
	mux.Handle("GET /admin/api/usage/real-tokens/{id}", wrap(h.getUsageByRealToken))
	mux.Handle("GET /admin/api/usage/gate-tokens/{id}", wrap(h.getUsageByGateToken))
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

// isFormRequest returns true if the request has form-encoded content type.
func isFormRequest(r *http.Request) bool {
	ct := r.Header.Get("Content-Type")
	return strings.HasPrefix(ct, "application/x-www-form-urlencoded") ||
		strings.HasPrefix(ct, "multipart/form-data")
}

// respondSuccess sends JSON or HX-Redirect depending on the request origin.
func respondSuccess(w http.ResponseWriter, r *http.Request, status int, data any, redirectURL string) {
	if r.Header.Get("HX-Request") == "true" || isFormRequest(r) {
		w.Header().Set("HX-Redirect", redirectURL)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, status, data)
}

// respondError sends JSON error or HX-Redirect to error page.
func respondError(w http.ResponseWriter, r *http.Request, status int, msg, redirectURL string) {
	if r.Header.Get("HX-Request") == "true" || isFormRequest(r) {
		w.Header().Set("HX-Redirect", redirectURL)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeError(w, status, msg)
}

// adminRedirect builds a safe redirect URL with flash query parameter using net/url.
func adminRedirect(base, flash string) string {
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	q := u.Query()
	q.Set("flash", flash)
	u.RawQuery = q.Encode()
	return u.String()
}

// setTokenActive consolidates activate/deactivate for both real and gate tokens.
func (h *AdminHandler) setTokenActive(w http.ResponseWriter, r *http.Request, tokenType string, active bool) {
	id := r.PathValue("id")
	basePath := "/admin/" + tokenType + "-tokens"

	var setActive func(ctx context.Context, id string, active bool) error
	if tokenType == "real" {
		setActive = h.store.SetRealTokenActive
	} else {
		setActive = h.store.SetGateTokenActive
	}

	err := setActive(r.Context(), id, active)
	if err == sql.ErrNoRows {
		respondError(w, r, http.StatusNotFound, tokenType+" token not found", adminRedirect(basePath, "Token not found"))
		return
	}
	if err != nil {
		action := "activate"
		if !active {
			action = "deactivate"
		}
		respondError(w, r, http.StatusInternalServerError, "failed to "+action+" "+tokenType+" token", adminRedirect(basePath, "Failed to "+action+" token"))
		return
	}

	action := "activated"
	status := "active"
	if !active {
		action = "deactivated"
		status = "inactive"
	}
	h.logger.Info(tokenType+" token "+action, "id", id)
	if tokenType == "real" {
		h.onPoolChanged()
	}
	respondSuccess(w, r, http.StatusOK, map[string]string{"id": id, "status": status}, adminRedirect(basePath, "Token "+action))
}

// deleteToken consolidates delete for both real and gate tokens.
func (h *AdminHandler) deleteToken(w http.ResponseWriter, r *http.Request, tokenType string) {
	id := r.PathValue("id")
	basePath := "/admin/" + tokenType + "-tokens"

	var deleteFn func(ctx context.Context, id string) error
	if tokenType == "real" {
		deleteFn = h.store.DeleteRealToken
	} else {
		deleteFn = h.store.DeleteGateToken
	}

	err := deleteFn(r.Context(), id)
	if err == sql.ErrNoRows {
		respondError(w, r, http.StatusNotFound, tokenType+" token not found", adminRedirect(basePath, "Token not found"))
		return
	}
	if err != nil {
		respondError(w, r, http.StatusInternalServerError, "failed to delete "+tokenType+" token", adminRedirect(basePath, "Failed to delete token"))
		return
	}
	h.logger.Info(tokenType+" token deleted", "id", id)
	if tokenType == "real" {
		h.onPoolChanged()
	}
	respondSuccess(w, r, http.StatusOK, map[string]string{"id": id}, adminRedirect(basePath, "Token deleted"))
}

// handleUsage consolidates the three usage handler functions.
func (h *AdminHandler) handleUsage(w http.ResponseWriter, r *http.Request, queryFn func(context.Context, time.Time) (*store.UsageStats, error)) {
	since := parseSince(r)
	stats, err := queryFn(r.Context(), since)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get usage stats")
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

// --- Real Token handlers ---

func (h *AdminHandler) listRealTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := h.store.ListRealTokens(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list real tokens")
		return
	}
	resp := make([]RealTokenResponse, 0, len(tokens))
	for i := range tokens {
		resp = append(resp, sanitizeRealToken(&tokens[i]))
	}
	writeJSON(w, http.StatusOK, resp)
}

type createRealTokenRequest struct {
	Name         string `json:"name"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

func (h *AdminHandler) createRealToken(w http.ResponseWriter, r *http.Request) {
	var req createRealTokenRequest
	if isFormRequest(r) {
		_ = r.ParseForm()
		req = createRealTokenRequest{
			Name:         r.FormValue("name"),
			AccessToken:  r.FormValue("access_token"),
			RefreshToken: r.FormValue("refresh_token"),
		}
	} else if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" || req.AccessToken == "" {
		respondError(w, r, http.StatusBadRequest, "name and access_token are required", adminRedirect("/admin/real-tokens", "Name and access_token are required"))
		return
	}
	t, err := h.store.CreateRealToken(r.Context(), req.Name, req.AccessToken, req.RefreshToken)
	if err != nil {
		respondError(w, r, http.StatusInternalServerError, "failed to create real token", adminRedirect("/admin/real-tokens", "Failed to create token"))
		return
	}
	h.logger.Info("real token created", "id", t.ID, "name", req.Name)
	h.onPoolChanged()
	respondSuccess(w, r, http.StatusCreated, sanitizeRealToken(t), adminRedirect("/admin/real-tokens", "Token created"))
}

type updateTokenRequest struct {
	Name string `json:"name"`
}

func (h *AdminHandler) updateRealToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req updateTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	err := h.store.UpdateRealToken(r.Context(), id, req.Name)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "real token not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update real token")
		return
	}
	h.logger.Info("real token updated", "id", id, "name", req.Name)
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "name": req.Name})
}

func (h *AdminHandler) deleteRealToken(w http.ResponseWriter, r *http.Request) {
	h.deleteToken(w, r, "real")
}

func (h *AdminHandler) activateRealToken(w http.ResponseWriter, r *http.Request) {
	h.setTokenActive(w, r, "real", true)
}

func (h *AdminHandler) deactivateRealToken(w http.ResponseWriter, r *http.Request) {
	h.setTokenActive(w, r, "real", false)
}

// --- Gate Token handlers ---

func (h *AdminHandler) listGateTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := h.store.ListGateTokens(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list gate tokens")
		return
	}
	if tokens == nil {
		tokens = []store.GateToken{}
	}
	writeJSON(w, http.StatusOK, tokens)
}

type createGateTokenRequest struct {
	Name string `json:"name"`
}

func (h *AdminHandler) createGateToken(w http.ResponseWriter, r *http.Request) {
	var req createGateTokenRequest
	if isFormRequest(r) {
		_ = r.ParseForm()
		req = createGateTokenRequest{Name: r.FormValue("name")}
	} else if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		respondError(w, r, http.StatusBadRequest, "name is required", adminRedirect("/admin/gate-tokens", "Name is required"))
		return
	}
	t, err := h.store.CreateGateToken(r.Context(), req.Name)
	if err != nil {
		respondError(w, r, http.StatusInternalServerError, "failed to create gate token", adminRedirect("/admin/gate-tokens", "Failed to create token"))
		return
	}
	h.logger.Info("gate token created", "id", t.ID, "name", req.Name)
	u, _ := url.Parse("/admin/gate-tokens")
	q := u.Query()
	q.Set("flash", "Token created")
	q.Set("new_token", t.Token)
	u.RawQuery = q.Encode()
	respondSuccess(w, r, http.StatusCreated, t, u.String())
}

func (h *AdminHandler) updateGateToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req updateTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	err := h.store.UpdateGateToken(r.Context(), id, req.Name)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "gate token not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update gate token")
		return
	}
	h.logger.Info("gate token updated", "id", id, "name", req.Name)
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "name": req.Name})
}

func (h *AdminHandler) deleteGateToken(w http.ResponseWriter, r *http.Request) {
	h.deleteToken(w, r, "gate")
}

func (h *AdminHandler) activateGateToken(w http.ResponseWriter, r *http.Request) {
	h.setTokenActive(w, r, "gate", true)
}

func (h *AdminHandler) deactivateGateToken(w http.ResponseWriter, r *http.Request) {
	h.setTokenActive(w, r, "gate", false)
}

// --- Usage handlers ---

func parseSince(r *http.Request) time.Time {
	sinceStr := r.URL.Query().Get("since")
	if sinceStr == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, sinceStr)
	if err != nil {
		return time.Time{}
	}
	return t
}

func (h *AdminHandler) getUsage(w http.ResponseWriter, r *http.Request) {
	h.handleUsage(w, r, func(ctx context.Context, since time.Time) (*store.UsageStats, error) {
		return h.store.GetUsageStats(ctx, since)
	})
}

func (h *AdminHandler) getUsageByRealToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	h.handleUsage(w, r, func(ctx context.Context, since time.Time) (*store.UsageStats, error) {
		return h.store.GetUsageStatsByRealToken(ctx, id, since)
	})
}

func (h *AdminHandler) getUsageByGateToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	h.handleUsage(w, r, func(ctx context.Context, since time.Time) (*store.UsageStats, error) {
		return h.store.GetUsageStatsByGateToken(ctx, id, since)
	})
}
