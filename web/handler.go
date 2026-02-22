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
	store       *store.Store
	adminSecret string
	logger      *slog.Logger
	templates   *template.Template
	layoutCache map[string]*template.Template
	sessions    *ttlcache.Cache[string, bool]
}

// NewHandler creates a new web UI handler.
func NewHandler(s *store.Store, adminSecret string, logger *slog.Logger) (*Handler, error) {
	tmpl, err := template.ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	sessions := ttlcache.New[string, bool](
		ttlcache.WithTTL[string, bool](24 * time.Hour),
	)
	go sessions.Start()

	// Pre-parse layout + page template combinations.
	pages := []string{"dashboard.html", "real_tokens.html", "gate_tokens.html"}
	layoutCache := make(map[string]*template.Template, len(pages))
	for _, page := range pages {
		t, err := template.ParseFS(templatesFS, "templates/layout.html", "templates/"+page)
		if err != nil {
			return nil, err
		}
		layoutCache[page] = t
	}

	return &Handler{
		store:       s,
		adminSecret: adminSecret,
		logger:      logger,
		templates:   tmpl,
		layoutCache: layoutCache,
		sessions:    sessions,
	}, nil
}

// ValidateSession checks if a request has a valid session cookie.
func (h *Handler) ValidateSession(r *http.Request) bool {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return false
	}
	return h.sessions.Get(cookie.Value) != nil
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
	mux.HandleFunc("GET /admin/gate-tokens", h.requireAuth(h.gateTokensPage))
}

// securityHeaders wraps a handler with common security headers.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'")
		next.ServeHTTP(w, r)
	})
}

// WrapMux wraps the entire mux with security headers.
func WrapMux(mux http.Handler) http.Handler {
	return securityHeaders(mux)
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

func (h *Handler) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.templates.ExecuteTemplate(w, name, data); err != nil {
		h.logger.Error("render template failed", "template", name, "error", err)
	}
}

func (h *Handler) renderLayout(w http.ResponseWriter, name string, data any) {
	tmpl, ok := h.layoutCache[name]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "layout", data); err != nil {
		h.logger.Error("render layout failed", "template", name, "error", err)
	}
}

func generateSessionToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
