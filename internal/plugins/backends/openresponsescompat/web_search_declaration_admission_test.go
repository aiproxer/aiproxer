package openresponsescompat

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// TestBackend_HostedWebSearchDeclarationRejectedBeforeNetwork proves the
// non-supporting side of requirement 6.4/14.12 on the real frontend carrier:
// a backend that advertises opaque extensions but not the exact
// openairesponses.web_search_declarations call extension must reject the
// decoded request before any HTTP round trip. It never silently drops the
// declaration.
func TestBackend_HostedWebSearchDeclarationRejectedBeforeNetwork(t *testing.T) {
	t.Parallel()
	be, obs := newObserverBackend(t, "", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, completeResourceJSON)
	})

	decoded, err := openairesponses.DecodeCreateRequest([]byte(`{
		"model":"gpt-5",
		"stream":false,
		"input":"find the weather",
		"tools":[
			{"type":"function","name":"lookup","parameters":{"type":"object"}},
			{"type":"web_search_preview"}
		]
	}`), openairesponses.DecodeOptions{RouteSelector: "or:gpt-5"})
	if err != nil {
		t.Fatalf("decode the hosted web-search request: %v", err)
	}
	call := *decoded.Call
	if _, ok := call.Extensions["openairesponses.web_search_declarations"]; !ok {
		t.Fatalf("the frontend carrier is missing, got %v", call.Extensions)
	}

	_, err = be.Open(context.Background(), call, routing.AttemptCandidate{
		Primary: routing.Primary{Model: "model-x"},
	})
	if err == nil {
		t.Fatal("a backend without the exact hosted-declaration extension must reject the call")
	}
	if !errors.Is(err, aipapi.ErrProjectionNotRepresentable) {
		t.Fatalf("the refusal must be a typed projection refusal, got %v", err)
	}
	var projection *aipapi.ProjectionError
	if !errors.As(err, &projection) {
		t.Fatalf("the refusal must carry the projection identity, got %v", err)
	}
	if projection.Field != "Extensions.openairesponses.web_search_declarations" {
		t.Fatalf("the refusal field = %q, want the exact call-extension path", projection.Field)
	}
	if obs.count() != 0 {
		t.Fatalf("the refusal caused %d upstream request(s), want 0", obs.count())
	}
}
