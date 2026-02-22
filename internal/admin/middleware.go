package admin

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
)

// AdminAuth returns middleware that checks for a valid admin Bearer token
// or, as fallback, a valid session cookie via sessionAuth.
func AdminAuth(secret string, sessionAuth func(*http.Request) bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Try Bearer token first.
			auth := r.Header.Get("Authorization")
			if after, ok := strings.CutPrefix(auth, "Bearer "); ok {
				token := after
				if subtle.ConstantTimeCompare([]byte(token), []byte(secret)) == 1 {
					next.ServeHTTP(w, r)
					return
				}
			}

			// Try session cookie auth.
			if sessionAuth != nil && sessionAuth(r) {
				next.ServeHTTP(w, r)
				return
			}

			writeError(w, http.StatusUnauthorized, "missing or invalid authorization")
		})
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
