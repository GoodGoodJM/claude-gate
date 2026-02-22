package proxy

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"

	"github.com/ggmolly/claude-gate/internal/store"
	"github.com/ggmolly/claude-gate/internal/token"
)

// usageEntry holds data needed to log a completed request.
type usageEntry struct {
	GateTokenID string
	RealTokenID string
	Usage       UsageData
	RequestPath string
	StatusCode  int
}

// ProxyHandler is an HTTP handler that reverse-proxies requests to the
// Claude API upstream, replacing gate tokens with real tokens and
// tapping SSE streams to extract usage information.
type ProxyHandler struct {
	store        *store.Store
	tokenMgr     *token.Manager
	upstreamURL  *url.URL
	usageCh      chan<- usageEntry
	logger       *slog.Logger
	droppedCount atomic.Int64
}

// NewProxyHandler creates a new ProxyHandler.
func NewProxyHandler(s *store.Store, mgr *token.Manager, upstream string, usageCh chan<- usageEntry, logger *slog.Logger) (*ProxyHandler, error) {
	u, err := url.Parse(upstream)
	if err != nil {
		return nil, err
	}
	return &ProxyHandler{
		store:       s,
		tokenMgr:    mgr,
		upstreamURL: u,
		usageCh:     usageCh,
		logger:      logger,
	}, nil
}

func (h *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Extract Bearer token.
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		writeJSONError(w, http.StatusUnauthorized, "missing or invalid Authorization header")
		return
	}
	gateTokenStr := strings.TrimPrefix(authHeader, "Bearer ")

	// Look up gate token.
	gt, err := h.store.GetGateTokenByToken(r.Context(), gateTokenStr)
	if err != nil || !gt.IsActive {
		writeJSONError(w, http.StatusUnauthorized, "invalid or inactive gate token")
		return
	}

	// Resolve real token.
	realToken, err := h.tokenMgr.ResolveToken(r.Context(), gt.ID)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "no available upstream tokens")
		return
	}

	// Build the reverse proxy.
	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = h.upstreamURL.Scheme
			req.URL.Host = h.upstreamURL.Host
			req.Host = h.upstreamURL.Host
			req.Header.Set("Authorization", "Bearer "+realToken.AccessToken)
		},
		FlushInterval: -1, // immediate flush for SSE
		ModifyResponse: func(resp *http.Response) error {
			ct := resp.Header.Get("Content-Type")
			if strings.HasPrefix(ct, "text/event-stream") {
				// Wrap body with tapping reader to extract usage from SSE.
				resp.Body = newTappingReader(resp.Body, func(usage UsageData) {
					h.sendUsage(gt.ID, realToken.ID, usage, r.URL.Path, resp.StatusCode)
				})
			} else if strings.HasPrefix(ct, "application/json") && resp.StatusCode >= 200 && resp.StatusCode < 300 {
				// Non-streaming: read body, extract usage, re-wrap.
				body, readErr := io.ReadAll(resp.Body)
				resp.Body.Close()
				if readErr != nil {
					return readErr
				}
				usage := extractJSONUsage(body)
				h.sendUsage(gt.ID, realToken.ID, usage, r.URL.Path, resp.StatusCode)
				resp.Body = io.NopCloser(strings.NewReader(string(body)))
			}

			// Record failure for upstream 5xx responses.
			if resp.StatusCode >= 500 {
				if rfErr := h.tokenMgr.RecordFailure(r.Context(), realToken.ID); rfErr != nil {
					h.logger.Error("failed to record failure", "real_token", realToken.ID, "error", rfErr)
				}
			}

			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			writeJSONError(w, http.StatusBadGateway, "upstream error: "+err.Error())
			if rfErr := h.tokenMgr.RecordFailure(r.Context(), realToken.ID); rfErr != nil {
				h.logger.Error("failed to record failure", "real_token", realToken.ID, "error", rfErr)
			}
		},
	}

	proxy.ServeHTTP(w, r)
}

func (h *ProxyHandler) sendUsage(gateTokenID, realTokenID string, usage UsageData, path string, statusCode int) {
	select {
	case h.usageCh <- usageEntry{
		GateTokenID: gateTokenID,
		RealTokenID: realTokenID,
		Usage:       usage,
		RequestPath: path,
		StatusCode:  statusCode,
	}:
	default:
		h.droppedCount.Add(1)
		h.logger.Warn("usage channel full, dropping entry", "gate", gateTokenID)
	}
}

// jsonUsageEnvelope extracts usage from a non-streaming JSON response.
type jsonUsageEnvelope struct {
	Model string `json:"model"`
	Usage struct {
		InputTokens              int64 `json:"input_tokens"`
		OutputTokens             int64 `json:"output_tokens"`
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

func extractJSONUsage(body []byte) UsageData {
	var env jsonUsageEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return UsageData{}
	}
	return UsageData{
		InputTokens:              env.Usage.InputTokens,
		OutputTokens:             env.Usage.OutputTokens,
		CacheCreationInputTokens: env.Usage.CacheCreationInputTokens,
		CacheReadInputTokens:     env.Usage.CacheReadInputTokens,
		Model:                    env.Model,
	}
}

func writeJSONError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
