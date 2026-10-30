package stdhttp

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit"
)

func TestRecoveryMiddleware_nilNext_returns500SafeBody(t *testing.T) {
	t.Parallel()
	h := RecoveryMiddleware(testkit.DiscardLogger(), nil)
	if h == nil {
		t.Fatal("RecoveryMiddleware(nil next) must not return nil")
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

func TestRecoveryMiddleware_panicBeforeCommit_returns500SafeBody(t *testing.T) {
	t.Parallel()
	h := RecoveryMiddleware(testkit.DiscardLogger(), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("do not leak this string")
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
}

// headerCountWriter records how many times WriteHeader was invoked on the outer writer.
type headerCountWriter struct {
	http.ResponseWriter
	n int
}

func (h *headerCountWriter) WriteHeader(code int) {
	h.n++
	h.ResponseWriter.WriteHeader(code)
}

func TestRecoveryMiddleware_panicAfterWriteHeader_doesNotWriteSecondError(t *testing.T) {
	t.Parallel()
	hc := &headerCountWriter{ResponseWriter: httptest.NewRecorder()}
	h := RecoveryMiddleware(testkit.DiscardLogger(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		panic("after commit")
	}))
	h.ServeHTTP(hc, httptest.NewRequest(http.MethodGet, "/", nil))
	// One WriteHeader(200) from the handler; recovery must not call http.Error (which would 500 again).
	if hc.n != 1 {
		t.Fatalf("WriteHeader calls=%d want 1 (no second error response after commit)", hc.n)
	}
}

func TestRecoveryMiddleware_panicAfterWrite_doesNotWriteSecondError(t *testing.T) {
	t.Parallel()
	// [httptest.ResponseRecorder].Write sets status 200 on the recorder directly; a wrapper
	// that only counts [http.ResponseWriter.WriteHeader] will not see that implicit commit.
	// Assert instead that recovery does not append a second error body after the first write.
	rec := httptest.NewRecorder()
	h := RecoveryMiddleware(testkit.DiscardLogger(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok")) // implicit 200; commits response
		panic("after first body bytes")
	}))
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d want 200 (implicit commit before panic; recovery must not ovewrite to 500)", rec.Code)
	}
	body := rec.Body.String()
	if !strings.HasPrefix(body, "ok") {
		t.Fatalf("body=%q want leading bytes from first write", body)
	}
	if strings.Contains(body, "internal error") {
		t.Fatalf("recovery must not append a second error body, got: %q", body)
	}
}

func TestRecoveryMiddleware_panicLogsIsolated_httpError500DoesNot(t *testing.T) {
	t.Parallel()
	t.Run("http error without isolated panic log", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError}))
		h := RecoveryMiddleware(log, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "plain", http.StatusInternalServerError)
		}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("code=%d", rec.Code)
		}
		if strings.Contains(buf.String(), "isolated_panic") || strings.Contains(buf.String(), "stdhttp: isolated") {
			t.Fatalf("expected no isolated-panic log for normal handler, got %q", buf.String())
		}
	})
	t.Run("handler panic logs isolated", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError}))
		h := RecoveryMiddleware(log, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic("boom")
		}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		s := buf.String()
		if !strings.Contains(s, "stdhttp: isolated panic in request handler") {
			t.Fatalf("expected isolated panic log, got %q", s)
		}
		if !strings.Contains(s, "panic_boundary") {
			t.Fatalf("expected panic_boundary in log, got %q", s)
		}
	})
}
