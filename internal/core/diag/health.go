package diag

import (
	"encoding/json"
	"net/http"
)

// HealthResponse is the JSON body for the basic liveness probe.
type HealthResponse struct {
	Status string `json:"status"`
}

// HealthHandler returns an http.Handler that serves GET with JSON {"status":"ok"}.
func HealthHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		b, err := json.Marshal(HealthResponse{Status: "ok"})
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write(b); err != nil {
			return
		}
	})
}
