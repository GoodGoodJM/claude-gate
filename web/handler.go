package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/ggmolly/claude-gate/internal/store"
	"github.com/jellydator/ttlcache/v3"
)

const sessionCookieName = "claude_gate_session"

// Handler serves the admin web UI.
type Handler struct {
	store         *store.Store
	adminSecret   string
	logger        *slog.Logger
	templates     *template.Template
	sessions      *ttlcache.Cache[string, bool]
	onPoolChanged func()
}

// NewHandler creates a new web UI handler.
func NewHandler(s *store.Store, adminSecret string, onPoolChanged func(), logger *slog.Logger) (*Handler, error) {
	tmpl, err := template.ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	sessions := ttlcache.New[string, bool](
		ttlcache.WithTTL[string, bool](24 * time.Hour),
	)
	go sessions.Start()

	return &Handler{
		store:         s,
		adminSecret:   adminSecret,
		logger:        logger,
		templates:     tmpl,
		sessions:      sessions,
		onPoolChanged: onPoolChanged,
	}, nil
}

// RegisterRoutes registers all web UI routes on the given mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	staticSub, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /admin/static/", http.StripPrefix("/admin/static/", http.FileServer(http.FS(staticSub))))

	mux.HandleFunc("GET /admin/login", h.loginPage)
	mux.HandleFunc("POST /admin/login", h.loginSubmit)
	mux.HandleFunc("GET /admin/logout", h.logout)

	mux.HandleFunc("GET /admin/", h.requireAuth(h.dashboard))
	mux.HandleFunc("GET /admin/real-tokens", h.requireAuth(h.realTokensPage))
	mux.HandleFunc("POST /admin/real-tokens/create", h.requireAuth(h.realTokenCreate))
	mux.HandleFunc("POST /admin/real-tokens/{id}/activate", h.requireAuth(h.realTokenActivate))
	mux.HandleFunc("POST /admin/real-tokens/{id}/deactivate", h.requireAuth(h.realTokenDeactivate))
	mux.HandleFunc("POST /admin/real-tokens/{id}/delete", h.requireAuth(h.realTokenDelete))

	mux.HandleFunc("GET /admin/gate-tokens", h.requireAuth(h.gateTokensPage))
	mux.HandleFunc("POST /admin/gate-tokens/create", h.requireAuth(h.gateTokenCreate))
	mux.HandleFunc("POST /admin/gate-tokens/{id}/activate", h.requireAuth(h.gateTokenActivate))
	mux.HandleFunc("POST /admin/gate-tokens/{id}/deactivate", h.requireAuth(h.gateTokenDeactivate))
	mux.HandleFunc("POST /admin/gate-tokens/{id}/delete", h.requireAuth(h.gateTokenDelete))
}

func (h *Handler) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		item := h.sessions.Get(cookie.Value)
		if item == nil {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

func (h *Handler) loginPage(w http.ResponseWriter, r *http.Request) {
	h.render(w, "login", map[string]any{"Error": ""})
}

func (h *Handler) loginSubmit(w http.ResponseWriter, r *http.Request) {
	secret := r.FormValue("secret")
	if subtle.ConstantTimeCompare([]byte(secret), []byte(h.adminSecret)) != 1 {
		h.render(w, "login", map[string]any{"Error": "Invalid admin secret"})
		return
	}

	token := generateSessionToken()
	h.sessions.Set(token, true, ttlcache.DefaultTTL)

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/admin",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   86400,
	})
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		h.sessions.Delete(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:   sessionCookieName,
		Value:  "",
		Path:   "/admin",
		MaxAge: -1,
	})
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

func (h *Handler) dashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	realTokens, err := h.store.ListRealTokens(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	gateTokens, err := h.store.ListGateTokens(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	usage24h, err := h.store.GetUsageStats(ctx, time.Now().Add(-24*time.Hour))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	activeReal := 0
	for _, t := range realTokens {
		if t.IsActive {
			activeReal++
		}
	}

	activeGate := 0
	for _, t := range gateTokens {
		if t.IsActive {
			activeGate++
		}
	}

	h.renderLayout(w, "dashboard.html", map[string]any{
		"Title":                "Dashboard",
		"Nav":                  "dashboard",
		"RealTokenCount":       len(realTokens),
		"ActiveRealTokenCount": activeReal,
		"GateTokenCount":       len(gateTokens),
		"ActiveGateTokenCount": activeGate,
		"Usage24h":             usage24h,
	})
}

func (h *Handler) realTokensPage(w http.ResponseWriter, r *http.Request) {
	tokens, err := h.store.ListRealTokens(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	h.renderLayout(w, "real_tokens.html", map[string]any{
		"Title":  "Real Tokens",
		"Nav":    "real-tokens",
		"Tokens": tokens,
		"Flash":  r.URL.Query().Get("flash"),
	})
}

func (h *Handler) realTokenCreate(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	accessToken := r.FormValue("access_token")
	refreshToken := r.FormValue("refresh_token")

	_, err := h.store.CreateRealToken(r.Context(), name, accessToken, refreshToken)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.notifyPoolChanged()
	http.Redirect(w, r, "/admin/real-tokens?flash=Token+created", http.StatusSeeOther)
}

func (h *Handler) realTokenActivate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.store.SetRealTokenActive(r.Context(), id, true); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.notifyPoolChanged()
	http.Redirect(w, r, "/admin/real-tokens?flash=Token+activated", http.StatusSeeOther)
}

func (h *Handler) realTokenDeactivate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.store.SetRealTokenActive(r.Context(), id, false); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.notifyPoolChanged()
	http.Redirect(w, r, "/admin/real-tokens?flash=Token+deactivated", http.StatusSeeOther)
}

func (h *Handler) realTokenDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.store.DeleteRealToken(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.notifyPoolChanged()
	http.Redirect(w, r, "/admin/real-tokens?flash=Token+deleted", http.StatusSeeOther)
}

func (h *Handler) gateTokensPage(w http.ResponseWriter, r *http.Request) {
	tokens, err := h.store.ListGateTokens(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	h.renderLayout(w, "gate_tokens.html", map[string]any{
		"Title":    "Gate Tokens",
		"Nav":      "gate-tokens",
		"Tokens":   tokens,
		"Flash":    r.URL.Query().Get("flash"),
		"NewToken": r.URL.Query().Get("new_token"),
	})
}

func (h *Handler) gateTokenCreate(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	gt, err := h.store.CreateGateToken(r.Context(), name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/gate-tokens?flash=Token+created&new_token="+gt.Token, http.StatusSeeOther)
}

func (h *Handler) gateTokenActivate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.store.SetGateTokenActive(r.Context(), id, true); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/gate-tokens?flash=Token+activated", http.StatusSeeOther)
}

func (h *Handler) gateTokenDeactivate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.store.SetGateTokenActive(r.Context(), id, false); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/gate-tokens?flash=Token+deactivated", http.StatusSeeOther)
}

func (h *Handler) gateTokenDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.store.DeleteGateToken(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/gate-tokens?flash=Token+deleted", http.StatusSeeOther)
}

func (h *Handler) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.templates.ExecuteTemplate(w, name, data); err != nil {
		h.logger.Error("render template failed", "template", name, "error", err)
	}
}

func (h *Handler) renderLayout(w http.ResponseWriter, name string, data any) {
	// Parse layout + specific template together
	tmpl, err := template.ParseFS(templatesFS, "templates/layout.html", "templates/"+name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "layout", data); err != nil {
		h.logger.Error("render layout failed", "template", name, "error", err)
	}
}

func (h *Handler) notifyPoolChanged() {
	if h.onPoolChanged != nil {
		h.onPoolChanged()
	}
}

func generateSessionToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
