package openresponsescompat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/capabilities"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func extensionContentPartCall() aipapi.Call {
	call := itemAuthorityCreateCall()
	call.Items[0].Content = []aipapi.ContentPart{{
		Kind: aipapi.ContentPartExtension,
		Extension: &aipapi.ExtensionContentPart{
			Type: "acme:part",
			Data: json.RawMessage(`{"type":"acme:part","payload":{"k":1}}`),
		},
	}}
	return call
}

// TestBackend_ExtensionContentPartMismatchFailsBeforeNetwork proves that the
// generic OpenResponses backend rejects a content-part extension whose exact
// namespace/type is not declared by the operator before any HTTP round trip.
func TestBackend_ExtensionContentPartMismatchFailsBeforeNetwork(t *testing.T) {
	t.Parallel()
	be, obs := newObserverBackend(t, "", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, completeResourceJSON)
	})
	call := extensionContentPartCall()
	if _, err := be.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}}); err == nil {
		t.Fatal("expected rejection for undeclared extension content part")
	}
	if obs.count() != 0 {
		t.Fatalf("rejection caused %d upstream requests, want 0", obs.count())
	}
}

// TestBackend_ExtensionContentPartMismatchedImplementorFailsBeforeNetwork
// proves that declaring the namespace/type without the exact implementor still
// rejects before network when the call requires the implementor.
func TestBackend_ExtensionContentPartMismatchedImplementorFailsBeforeNetwork(t *testing.T) {
	t.Parallel()
	cfg := "dialects:\n  extensions:\n    - namespace: acme\n      type: acme:part\n      implementor: acme-vendor\n"
	be, obs := newObserverBackend(t, cfg, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, completeResourceJSON)
	})
	call := extensionContentPartCall() // derived requirement carries no implementor
	if _, err := be.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}}); err == nil {
		t.Fatal("expected rejection when declared implementor mismatches the call requirement")
	}
	if obs.count() != 0 {
		t.Fatalf("rejection caused %d upstream requests, want 0", obs.count())
	}
}

// TestBackend_ExtensionContentPartDeclaredRoundTrips proves that declaring the
// exact extension type admits the call and forwards the structured payload.
func TestBackend_ExtensionContentPartDeclaredRoundTrips(t *testing.T) {
	t.Parallel()
	cfg := "dialects:\n  extensions:\n    - namespace: acme\n      type: acme:part\n"
	be, obs := newObserverBackend(t, cfg, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, completeResourceJSON)
	})
	call := extensionContentPartCall()
	es, err := be.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	events := drainManagedEvents(t, es)
	if !hasTextDelta(events, "The weather in Paris is sunny.") {
		t.Fatalf("missing text delta in %+v", events)
	}
	if obs.count() != 1 {
		t.Fatalf("request count = %d, want 1", obs.count())
	}
	body := string(obs.last(t).Body)
	for _, marker := range []string{`"type":"acme:part"`, `"payload":{"k":1}`} {
		if !strings.Contains(body, marker) {
			t.Fatalf("upstream wire missing %q:\n%s", marker, body)
		}
	}
}

// TestBackend_StructuredToolResultExtensionPartDeclaredRoundTrips proves that a
// structured tool-result extension part is admitted only when declared and is
// preserved on the upstream wire.
func TestBackend_StructuredToolResultExtensionPartDeclaredRoundTrips(t *testing.T) {
	t.Parallel()
	cfg := "dialects:\n  extensions:\n    - namespace: acme\n      type: acme:result\n"
	be, obs := newObserverBackend(t, cfg, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, completeResourceJSON)
	})
	call := itemAuthorityCreateCall()
	call.Items = append(call.Items, aipapi.Item{
		Kind: aipapi.ItemKindToolResult,
		ID:   "tr-2",
		ToolResult: &aipapi.ToolResultItem{
			CallID: "call_1",
			Name:   "get_weather",
			Parts: []aipapi.ContentPart{{
				Kind: aipapi.ContentPartExtension,
				Extension: &aipapi.ExtensionContentPart{
					Type: "acme:result",
					Data: json.RawMessage(`{"type":"acme:result","rows":2}`),
				},
			}},
		},
	})
	es, err := be.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	events := drainManagedEvents(t, es)
	if !hasTextDelta(events, "The weather in Paris is sunny.") {
		t.Fatalf("missing text delta in %+v", events)
	}
	if obs.count() != 1 {
		t.Fatalf("request count = %d, want 1", obs.count())
	}
	body := string(obs.last(t).Body)
	if !strings.Contains(body, `"acme:result"`) || !strings.Contains(body, `"rows":2`) {
		t.Fatalf("upstream wire lost the structured tool-result extension:\n%s", body)
	}
}

// TestFailover_ContentPartExtensionRequiresExactDeclaredType proves that the
// failover requirement set retains the exact extension requirement and that a
// candidate satisfies it only when it declares the exact namespace/type.
func TestFailover_ContentPartExtensionRequiresExactDeclaredType(t *testing.T) {
	t.Parallel()
	set := capabilities.NewFailoverRequirementSet(extensionContentPartCall())
	base := aipapi.ProtocolRequirements{
		Capabilities: append([]aipapi.Capability(nil), defaultCapabilities...),
		ItemDialects: []aipapi.DialectRequirement{{Kind: "item", Dialect: DefaultItemDialect}},
	}
	if set.CandidateMatchesFailoverRequirements(base, aipapi.ReasoningReplaySupport{}) {
		t.Fatal("expected failover mismatch without the declared extension type")
	}
	declared := base
	declared.ExtensionTypes = []aipapi.ExtensionRequirement{{Namespace: "acme", Type: "acme:part"}}
	if !set.CandidateMatchesFailoverRequirements(declared, aipapi.ReasoningReplaySupport{}) {
		t.Fatal("expected failover match with the exact declared extension type")
	}
	mismatched := base
	mismatched.ExtensionTypes = []aipapi.ExtensionRequirement{{Namespace: "other", Type: "other:part"}}
	if set.CandidateMatchesFailoverRequirements(mismatched, aipapi.ReasoningReplaySupport{}) {
		t.Fatal("expected failover mismatch with a mismatched extension type")
	}
}
