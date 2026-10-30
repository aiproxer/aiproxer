package admin

import (
	"log/slog"
	"net/http"

	"github.com/aiproxer/aiproxer/internal/core/diag"
)

// DiagnosticsHandler serves the admin diagnostics surface combining health
// and attempts endpoints under a common mux.
func DiagnosticsHandler(attemptLoader diag.AttemptLoader) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/health", diag.HealthHandler())
	if attemptLoader != nil {
		h, err := diag.AttemptsHandler(attemptLoader)
		if err != nil {
			slog.Default().Error("attempts handler construction failed", "error", err)
			h = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "attempts endpoint unavailable", http.StatusInternalServerError)
			})
		}
		mux.Handle("/attempts", h)
	}
	return mux
}
