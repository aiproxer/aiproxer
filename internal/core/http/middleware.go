package http

import (
	"net/http"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// HeaderTraceID is the default inbound/outbound trace header.
const HeaderTraceID = aipsdk.HeaderTraceID

// TraceMiddleware injects a trace ID into the request context when a configured
// inbound trace header is present.
func TraceMiddleware(next http.Handler) http.Handler {
	return TraceMiddlewareHeaders(nil, next)
}

// TraceMiddlewareHeaders is [TraceMiddleware] with explicit inbound header names.
// Empty names use [HeaderTraceID].
func TraceMiddlewareHeaders(names []string, next http.Handler) http.Handler {
	if len(names) == 0 {
		names = []string{HeaderTraceID}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if traceID := aipsdk.FirstHeader(r.Header, names); traceID != "" {
			r = r.WithContext(diag.WithTraceID(r.Context(), traceID))
		}
		next.ServeHTTP(w, r)
	})
}

// RequestIDMiddleware generates and injects a new trace ID when one is not
// already present in the request context. gen must be non-nil (typically
// [diag.NewTraceIDGenerator] from the composition root).
func RequestIDMiddleware(gen *diag.TraceIDGenerator, next http.Handler) http.Handler {
	return RequestIDMiddlewareHeaders(gen, nil, next)
}

// RequestIDMiddlewareHeaders is [RequestIDMiddleware] and echoes the trace ID
// on each configured outbound header name (defaults to [HeaderTraceID]).
func RequestIDMiddlewareHeaders(gen *diag.TraceIDGenerator, names []string, next http.Handler) http.Handler {
	if gen == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "trace id generator not configured", http.StatusInternalServerError)
		})
	}
	if len(names) == 0 {
		names = []string{HeaderTraceID}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if diag.TraceID(r.Context()) == "" {
			r = r.WithContext(diag.WithTraceID(r.Context(), gen.Next()))
		}
		if tid := diag.TraceID(r.Context()); tid != "" {
			for _, name := range names {
				w.Header().Set(name, tid)
			}
		}
		next.ServeHTTP(w, r)
	})
}

// SecurityHeadersMiddleware adds standard security headers to every response.
func SecurityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
		next.ServeHTTP(w, r)
	})
}
