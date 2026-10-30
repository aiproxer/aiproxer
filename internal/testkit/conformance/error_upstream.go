// Package conformance drives end-to-end matrix tests. Upstream error-shape checks use
// NewUpstream400Server for minimal JSON bodies that match each provider family’s typical
// 400 invalid_request patterns (aligned with internal/refbackend error shapes where those
// emulators return structured errors). Success paths use internal/refbackend via NewSuccessRefBackend.
package conformance

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/backends/anthropic"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/bedrock"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/gemini"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openailegacy"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openairesponses"
)

// NewUpstream400Server returns an httptest server that answers like a provider HTTP 400 for the
// given bundled backend wire family (Task 12.1 protocol-valid upstream error shape).
func NewUpstream400Server(tb testing.TB, backendID string) *httptest.Server {
	tb.Helper()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		switch backendID {
		case openairesponses.ID, openailegacy.ID, BackendOpenResponses, BackendOpenRouter, BackendNVIDIA:
			_, _ = w.Write([]byte(`{"error":{"message":"bad","type":"invalid_request_error","param":"","code":"invalid_request_error"}}`))
		case anthropic.ID:
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"bad request"}}`))
		case gemini.ID:
			_, _ = w.Write([]byte(`{"error":{"code":400,"message":"invalid argument","status":"INVALID_ARGUMENT"}}`))
		case bedrock.ID:
			_, _ = w.Write([]byte(`{"message":"ValidationException","__type":"ValidationException"}`))
		case BackendACP:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"invalid params"}}`))
		default:
			_, _ = w.Write([]byte(`{"error":"unknown backend"}`))
		}
	})
	srv := httptest.NewServer(h)
	tb.Cleanup(srv.Close)
	return srv
}
