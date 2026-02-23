package httputil

import (
	"encoding/json"
	"net/http"
)

// WriteJSONError writes a JSON error response with the given HTTP status code.
func WriteJSONError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
