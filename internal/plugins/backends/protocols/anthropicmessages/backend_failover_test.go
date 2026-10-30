package anthropicmessages

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
		BackendID:     "anthropic-test",
		BaseURL:       srv.URL,
		APIKey:        "sk-ant-test",
		HTTPClient:    srv.Client(),
		SDKMaxRetries: new(int),
	})
	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "claude-3-5-haiku-20241022"}}
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
		`{"type":"error","error":{"type":"api_error","message":"boom"}}`, &attempts)
	if !aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("expected recoverable pre-output for 500, got: %v", err)
	}
	if !strings.Contains(err.Error(), "anthropic-test: ") {
		t.Fatalf("expected backend ID prefix in error, got: %v", err)
	}
	if n := attempts.Load(); n != 1 {
		t.Fatalf("upstream attempts: %d want 1 (no credential rotation on 500)", n)
	}
}

func TestNewBackend_unclassifiedErrorCarriesBackendID(t *testing.T) {
	t.Parallel()
	var attempts atomic.Int32
	err := openAgainstForcedStatus(t, http.StatusBadRequest,
		`{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`, &attempts)
	if aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("did not expect recoverable pre-output for 400: %v", err)
	}
	if !strings.Contains(err.Error(), "anthropic-test: ") {
		t.Fatalf("expected backend ID prefix in error, got: %v", err)
	}
	if n := attempts.Load(); n != 1 {
		t.Fatalf("upstream attempts: %d want 1 (no credential rotation on 400)", n)
	}
}
