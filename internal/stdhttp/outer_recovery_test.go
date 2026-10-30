package stdhttp

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	coreconfig "github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/testkit"
)

func TestOuterRecoveryMiddleware_nilNext_returns500SafeBody(t *testing.T) {
	t.Parallel()
	h := outerRecoveryMiddleware(testkit.DiscardLogger(), nil)
	if h == nil {
		t.Fatal("outerRecoveryMiddleware(nil next) must not return nil")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d want %d", rec.Code, http.StatusInternalServerError)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "internal error") {
		t.Fatalf("body=%q want internal error text", body)
	}
}

func TestOuterRecoveryMiddleware_panicBeforeCommit_returns500SafeBodyAndOperation(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError}))
	h := outerRecoveryMiddleware(log, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("do not leak outer")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d want %d", rec.Code, http.StatusInternalServerError)
	}
	body := rec.Body.String()
	if strings.Contains(body, "do not leak") {
		t.Fatalf("body leaked panic value: %q", body)
	}
	if !strings.Contains(body, "internal error") {
		t.Fatalf("body=%q want internal error text", body)
	}
	s := buf.String()
	if !strings.Contains(s, `"operation":"http_outer_handler"`) {
		t.Fatalf("want operation=http_outer_handler in log, got %q", s)
	}
	if !strings.Contains(s, `"panic_boundary"`) || !strings.Contains(s, `"panic_value_type"`) {
		t.Fatalf("want panic_boundary and panic_value_type in log, got %q", s)
	}
	if strings.Contains(s, `"panic_message"`) {
		t.Fatalf("must not log panic_message, got %q", s)
	}
	if !strings.Contains(s, "stdhttp: isolated panic in outer HTTP handler") {
		t.Fatalf("want outer isolated log msg, got %q", s)
	}
}

func TestOuterRecoveryMiddleware_panicAfterWriteHeader_doesNotWriteSecondError(t *testing.T) {
	t.Parallel()
	hc := &headerCountWriter{ResponseWriter: httptest.NewRecorder()}
	h := outerRecoveryMiddleware(testkit.DiscardLogger(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		panic("after commit")
	}))
	h.ServeHTTP(hc, httptest.NewRequest(http.MethodGet, "/", nil))
	if hc.n != 1 {
		t.Fatalf("WriteHeader calls=%d want 1 (no second error response after commit)", hc.n)
	}
}

func TestOuterRecoveryMiddleware_panicAfterWrite_doesNotAppendInternalError(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	h := outerRecoveryMiddleware(testkit.DiscardLogger(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
		panic("after first body bytes")
	}))
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.HasPrefix(body, "ok") {
		t.Fatalf("body=%q want leading bytes from first write", body)
	}
	if strings.Contains(body, "internal error") {
		t.Fatalf("recovery must not append a second error body, got: %q", body)
	}
}

func TestStackHTTPHandler_testOuterWrap_panicBeforeCommit_outerOperationInLog(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := &coreconfig.Config{
		Logging:       coreconfig.LoggingConfig{AccessLog: false},
		Observability: coreconfig.ObservabilityConfig{Tracing: coreconfig.TracingConfig{Enabled: false}},
	}
	h := stackHTTPHandler(stackHTTPInput{
		Cfg:      cfg,
		Log:      log,
		Security: HTTPSecurityInput{},
		TraceGen: diag.NewTraceIDGenerator(),
		Inner:    http.NewServeMux(),
		HTTPProm: nil,
		testOuterWrap: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				panic("synthetic outer before next")
			})
		},
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	res, err := http.Get(srv.URL + "/any")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500", res.StatusCode)
	}
	s := buf.String()
	if !strings.Contains(s, `"operation":"http_outer_handler"`) {
		t.Fatalf("want operation=http_outer_handler in log, got %q", s)
	}
}

func TestStackHTTPHandler_testOuterWrap_panicBeforeCommit_carriesSecurityHeaders(t *testing.T) {
	t.Parallel()
	cfg := &coreconfig.Config{
		Logging:       coreconfig.LoggingConfig{AccessLog: false},
		Observability: coreconfig.ObservabilityConfig{Tracing: coreconfig.TracingConfig{Enabled: false}},
	}
	h := stackHTTPHandler(stackHTTPInput{
		Cfg:      cfg,
		Log:      testkit.DiscardLogger(),
		Security: HTTPSecurityInput{},
		TraceGen: diag.NewTraceIDGenerator(),
		Inner:    http.NewServeMux(),
		HTTPProm: nil,
		testOuterWrap: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				panic("synthetic outer before next")
			})
		},
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	res, err := http.Get(srv.URL + "/any")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500", res.StatusCode)
	}
	for name, want := range map[string]string{
		"Content-Security-Policy":   "default-src 'self'; script-src 'self'",
		"X-Frame-Options":           "DENY",
		"X-Content-Type-Options":    "nosniff",
		"Strict-Transport-Security": "max-age=31536000; includeSubDomains",
		"Referrer-Policy":           "strict-origin-when-cross-origin",
		"Permissions-Policy":        "geolocation=(), microphone=(), camera=()",
	} {
		if got := res.Header.Get(name); got != want {
			t.Errorf("outer-panic 500 header %s = %q, want %q", name, got, want)
		}
	}
}

func TestStackHTTPHandler_testOuterWrap_panicAfterInnerCommit_noSecondBodyOrStatusChange(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("inner"))
	})
	cfg := &coreconfig.Config{
		Logging:       coreconfig.LoggingConfig{AccessLog: false},
		Observability: coreconfig.ObservabilityConfig{Tracing: coreconfig.TracingConfig{Enabled: false}},
	}
	h := stackHTTPHandler(stackHTTPInput{
		Cfg:      cfg,
		Log:      testkit.DiscardLogger(),
		Security: HTTPSecurityInput{},
		TraceGen: diag.NewTraceIDGenerator(),
		Inner:    mux,
		HTTPProm: nil,
		testOuterWrap: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				next.ServeHTTP(w, r)
				panic("after inner committed")
			})
		},
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	res, err := http.Get(srv.URL + "/ok")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := res.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200 (outer must not rewrite after commit)", res.StatusCode)
	}
	if string(body) != "inner" {
		t.Fatalf("body=%q want inner only", body)
	}
	if strings.Contains(string(body), "internal error") {
		t.Fatalf("must not append internal error body, got %q", body)
	}
}
