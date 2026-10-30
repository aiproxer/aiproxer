package http_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	corehttp "github.com/aiproxer/aiproxer/internal/core/http"
)

func TestTraceMiddleware_propagatesHeader(t *testing.T) {
	t.Parallel()

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := diag.TraceID(r.Context())
		if got != "trace-123" {
			t.Fatalf("expected trace-123, got %q", got)
		}
	})

	h := corehttp.TraceMiddleware(inner)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Trace-ID", "trace-123")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
}

func TestRequestIDMiddleware_generatesWhenMissing(t *testing.T) {
	t.Parallel()

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := diag.TraceID(r.Context())
		if got == "" {
			t.Fatal("expected trace ID")
		}
	})

	gen := diag.NewTraceIDGenerator()
	h := corehttp.RequestIDMiddleware(gen, inner)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Trace-ID"); got != "t_00000001" {
		t.Fatalf("X-Trace-ID = %q, want t_00000001", got)
	}
}

func TestTraceMiddleware_propagatesAliasHeader(t *testing.T) {
	t.Parallel()
	inner := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if got := diag.TraceID(r.Context()); got != "from-alias" {
			t.Fatalf("got %q", got)
		}
	})
	h := corehttp.TraceMiddlewareHeaders([]string{corehttp.HeaderTraceID, "X-Request-ID"}, inner)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-ID", "from-alias")
	h.ServeHTTP(httptest.NewRecorder(), req)
}

func TestRequestIDMiddleware_preservesExisting(t *testing.T) {
	t.Parallel()

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := diag.TraceID(r.Context())
		if got != "existing-id" {
			t.Fatalf("expected existing-id, got %q", got)
		}
	})

	ctx := diag.WithTraceID(context.Background(), "existing-id")
	h := corehttp.RequestIDMiddleware(diag.NewTraceIDGenerator(), inner)
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
}

func TestSecurityHeadersMiddleware_addsHeaders(t *testing.T) {
	t.Parallel()

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	h := corehttp.SecurityHeadersMiddleware(inner)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	res := rec.Result()
	if got := res.Header.Get("Content-Security-Policy"); got != "default-src 'self'; script-src 'self'" {
		t.Errorf("CSP = %q, want default-src 'self'; script-src 'self'", got)
	}
	if got := res.Header.Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q, want DENY", got)
	}
	if got := res.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := res.Header.Get("Strict-Transport-Security"); got != "max-age=31536000; includeSubDomains" {
		t.Errorf("Strict-Transport-Security = %q, want max-age=31536000; includeSubDomains", got)
	}
	if got := res.Header.Get("Referrer-Policy"); got != "strict-origin-when-cross-origin" {
		t.Errorf("Referrer-Policy = %q, want strict-origin-when-cross-origin", got)
	}
	if got := res.Header.Get("Permissions-Policy"); got != "geolocation=(), microphone=(), camera=()" {
		t.Errorf("Permissions-Policy = %q, want geolocation=(), microphone=(), camera=()", got)
	}
}
