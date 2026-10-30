// Package stdhttp provides HTTP server wiring; recovery middleware isolates request panics.
package stdhttp

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	corehttp "github.com/aiproxer/aiproxer/internal/core/http"
	"github.com/aiproxer/aiproxer/internal/core/safety"
)

// internalErrorMessage is the client-safe body for pre-commit panic recovery (match frontend
// "internal error" wire copy; do not import protocol packages from stdhttp).
const internalErrorMessage = "internal error"

// RecoveryMiddleware recovers panics from inner handlers, maps them to [safety.PanicError],
// and returns a safe 500 when the response is not yet committed. After [http.ResponseWriter]
// headers are committed, it logs only and does not write a second error body.
func RecoveryMiddleware(log *slog.Logger, next http.Handler) http.Handler {
	return recoveryMiddleware(log, "http_handler", "stdhttp: isolated panic in request handler", next)
}

func outerRecoveryMiddleware(log *slog.Logger, next http.Handler) http.Handler {
	return recoveryMiddleware(log, "http_outer_handler", "stdhttp: isolated panic in outer HTTP handler", next)
}

func recoveryMiddleware(log *slog.Logger, operation string, isolatedLogMsg string, next http.Handler) http.Handler {
	if next == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, internalErrorMessage, http.StatusInternalServerError)
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rr := &corehttp.ResponseStatusRecorder{ResponseWriter: w}
		defer func() {
			p := recover()
			if p == nil {
				return
			}
			pe := safety.Capture(safety.BoundaryHTTP, operation, p)
			ctx := context.Background()
			if r != nil {
				ctx = r.Context()
			}
			if log != nil {
				attrs := diag.IsolatedCrashAttrs(ctx, pe, diag.CrashAttrOpts{})
				attrs = diag.AppendIsolatedCrashStack(attrs, pe)
				log.LogAttrs(ctx, slog.LevelError, isolatedLogMsg, attrs...)
			}
			if rr.Status != 0 {
				return
			}
			http.Error(rr, internalErrorMessage, http.StatusInternalServerError)
		}()
		next.ServeHTTP(rr, r)
	})
}
