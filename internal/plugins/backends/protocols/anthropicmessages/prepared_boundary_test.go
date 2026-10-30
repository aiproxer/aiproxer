package anthropicmessages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestNewBackendObservesFinalAnthropicRepresentation(t *testing.T) {
	t.Parallel()
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"api_error","message":"test"}}`))
	}))
	t.Cleanup(server.Close)

	call := aipapi.Call{Messages: []aipapi.Message{{
		Role: aipapi.RoleUser,
		Parts: []aipapi.Part{
			aipapi.TextPart("describe"),
			{Kind: aipapi.PartImageRef, ImageRef: "data:image/png;base64,QUJD", ImageMIME: "image/png"},
		},
	}}}
	candidate := routing.AttemptCandidate{Primary: routing.Primary{Backend: "anthropic-test", Model: "claude-test"}}
	canonical := coremetering.PreparedInputSummaryFromCall(call)
	var observed coremetering.PreparedInputSummary
	ctx := coremetering.WithPreparedInputObserver(context.Background(), func(summary coremetering.PreparedInputSummary) {
		observed = summary
	})
	backend := NewBackend(Config{
		BackendID:     "anthropic-test",
		BaseURL:       server.URL,
		APIKey:        "sk-ant-test",
		HTTPClient:    server.Client(),
		SDKMaxRetries: new(int),
	})
	if _, err := backend.Open(ctx, call, candidate); err == nil {
		t.Fatal("Open unexpectedly succeeded against forced upstream failure")
	}
	if requests != 1 {
		t.Fatalf("upstream requests=%d, want one real adapter request", requests)
	}
	if observed.MethodRef != anthropicPreparedInputMethod {
		t.Fatalf("method=%q, want %q", observed.MethodRef, anthropicPreparedInputMethod)
	}
	if len(observed.Media) != 1 || observed.Media[0].Kind != coremetering.MediaImage || !observed.Media[0].BytesPresent || observed.Media[0].Bytes != 4 {
		t.Fatalf("final provider media=%#v, want decoded provider-bound image bytes", observed.Media)
	}
	if len(canonical.Media) != 1 || canonical.Media[0].BytesPresent {
		t.Fatalf("canonical ingress media changed or unexpectedly has bytes: %#v", canonical.Media)
	}
	if observed.TextBytes != canonical.TextBytes {
		t.Fatalf("text bytes changed unexpectedly: final=%d canonical=%d", observed.TextBytes, canonical.TextBytes)
	}
}
