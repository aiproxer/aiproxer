package geminigenerate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func openAgainstForcedStatus(t *testing.T, status int, body string, attempts *atomic.Int32) error {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	be := NewBackend(Config{
		BackendID:  "gemini-test",
		BaseURL:    srv.URL,
		APIKey:     "genai-test",
		HTTPClient: srv.Client(),
	})
	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "gemini-2.0-flash"}}
	_, err := be.Open(context.Background(), call, cand)
	if err == nil {
		t.Fatal("expected Open error")
	}
	return err
}

func TestNewBackend_internalServerErrorIsRecoverablePreOutput(t *testing.T) {
	t.Parallel()
	var attempts atomic.Int32
	err := openAgainstForcedStatus(t, http.StatusInternalServerError,
		`{"error":{"code":500,"message":"boom","status":"INTERNAL"}}`, &attempts)
	if !aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("expected recoverable pre-output for 500, got: %v", err)
	}
	if !strings.Contains(err.Error(), "gemini-test: ") {
		t.Fatalf("expected backend ID prefix in error, got: %v", err)
	}
}

func TestNewBackend_unclassifiedErrorCarriesBackendID(t *testing.T) {
	t.Parallel()
	var attempts atomic.Int32
	err := openAgainstForcedStatus(t, http.StatusBadRequest,
		`{"error":{"code":400,"message":"bad","status":"INVALID_ARGUMENT"}}`, &attempts)
	if aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("did not expect recoverable pre-output for 400: %v", err)
	}
	if !strings.Contains(err.Error(), "gemini-test: ") {
		t.Fatalf("expected backend ID prefix in error, got: %v", err)
	}
}
