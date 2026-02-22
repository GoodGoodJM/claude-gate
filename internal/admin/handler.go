package admin

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
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
func (h *AdminHandler) Register(mux *http.ServeMux, secret string) {
	auth := AdminAuth(secret)
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" || req.AccessToken == "" {
		writeError(w, http.StatusBadRequest, "name and access_token are required")
		return
	}
	t, err := h.store.CreateRealToken(r.Context(), req.Name, req.AccessToken, req.RefreshToken)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create real token")
		return
	}
	h.logger.Info("real token created", "id", t.ID, "name", req.Name)
	h.onPoolChanged()
	writeJSON(w, http.StatusCreated, sanitizeRealToken(t))
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
	id := r.PathValue("id")
	err := h.store.DeleteRealToken(r.Context(), id)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "real token not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete real token")
		return
	}
	h.logger.Info("real token deleted", "id", id)
	h.onPoolChanged()
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

func (h *AdminHandler) activateRealToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := h.store.SetRealTokenActive(r.Context(), id, true)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "real token not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to activate real token")
		return
	}
	h.logger.Info("real token activated", "id", id)
	h.onPoolChanged()
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "status": "active"})
}

func (h *AdminHandler) deactivateRealToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := h.store.SetRealTokenActive(r.Context(), id, false)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "real token not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to deactivate real token")
		return
	}
	h.logger.Info("real token deactivated", "id", id)
	h.onPoolChanged()
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "status": "inactive"})
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	t, err := h.store.CreateGateToken(r.Context(), req.Name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create gate token")
		return
	}
	h.logger.Info("gate token created", "id", t.ID, "name", req.Name)
	writeJSON(w, http.StatusCreated, t)
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
	id := r.PathValue("id")
	err := h.store.DeleteGateToken(r.Context(), id)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "gate token not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete gate token")
		return
	}
	h.logger.Info("gate token deleted", "id", id)
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

func (h *AdminHandler) activateGateToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := h.store.SetGateTokenActive(r.Context(), id, true)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "gate token not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to activate gate token")
		return
	}
	h.logger.Info("gate token activated", "id", id)
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "status": "active"})
}

func (h *AdminHandler) deactivateGateToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := h.store.SetGateTokenActive(r.Context(), id, false)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "gate token not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to deactivate gate token")
		return
	}
	h.logger.Info("gate token deactivated", "id", id)
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "status": "inactive"})
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
	since := parseSince(r)
	stats, err := h.store.GetUsageStats(r.Context(), since)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get usage stats")
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (h *AdminHandler) getUsageByRealToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	since := parseSince(r)
	stats, err := h.store.GetUsageStatsByRealToken(r.Context(), id, since)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get usage stats")
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (h *AdminHandler) getUsageByGateToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	since := parseSince(r)
	stats, err := h.store.GetUsageStatsByGateToken(r.Context(), id, since)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get usage stats")
		return
	}
	writeJSON(w, http.StatusOK, stats)
}
