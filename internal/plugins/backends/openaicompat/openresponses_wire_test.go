package openaicompat_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openaicompat"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

const (
	testOpenResponsesProfile = "openresponses_v1"
)

func makeValidOpenResponsesWireRequestFacts(clientModel, candModel string, rewrite largebody.RewriteSemantics) largebody.WireRequestFacts {
	return largebody.WireRequestFacts{
		ProfileID:            testOpenResponsesProfile,
		Operation:            aipapi.OperationOpenResponsesCreate,
		Delivery:             aipapi.DeliveryModeStreaming,
		BodyMode:             largebody.BodyModeIdentityJSON,
		Rewrite:              rewrite,
		ClientModel:          clientModel,
		CandidateModel:       candModel,
		MaxOutputTokens:      2048,
		RequiredCapabilities: []aipapi.Capability{aipapi.CapabilityStreaming, aipapi.CapabilityOrderedItems},
	}
}

func makeValidOpenResponsesWireDomainFacts(universal bool, candidateModels []string) largebody.WireDomainFacts {
	return largebody.WireDomainFacts{
		ProfileID:            testOpenResponsesProfile,
		Operation:            aipapi.OperationOpenResponsesCreate,
		Delivery:             aipapi.DeliveryModeStreaming,
		BodyMode:             largebody.BodyModeIdentityJSON,
		Rewrite:              testRewriteModelToken(),
		UniversalModel:       universal,
		CandidateModels:      candidateModels,
		RequiredCapabilities: []aipapi.Capability{aipapi.CapabilityStreaming, aipapi.CapabilityOrderedItems},
	}
}

func staticTestInventory(models ...string) modelinventory.Provider {
	var ms []modelinventory.Model
	for _, m := range models {
		ms = append(ms, modelinventory.Model{CanonicalID: m, NativeID: m})
	}
	return modelinventory.StaticProvider{Models: ms}
}

func newTestOpenResponsesWireBackend(baseURL string, inventory modelinventory.Provider, anyAcceptedModel *bool) execbackend.Backend {
	return openaicompat.NewBackend(openaicompat.BackendSpec{
		ID:                         "test-compat-openresponses",
		BaseURL:                    baseURL,
		APIKey:                     "sk-test-openresponses-key",
		Flavor:                     openaicompat.FlavorResponses,
		Inventory:                  inventory,
		WireDomainAnyAcceptedModel: anyAcceptedModel,
		RateLimitFallback:          time.Minute,
	})
}

// Requirement 8, 17.2: Exact compatibility for OpenResponses create.
func TestOpenResponsesWireRequest_ExactCompatibility(t *testing.T) {
	t.Parallel()

	be := newTestOpenResponsesWireBackend("http://127.0.0.1:8080/v1", nil, nil)
	if be.ResolveWireRequest == nil {
		t.Fatal("expected be.ResolveWireRequest to be non-nil on Responses backend")
	}

	cand := routing.AttemptCandidate{
		Primary: routing.Primary{
			Backend: "test-compat-openresponses",
			Model:   "gpt-4o",
		},
	}

	facts := makeValidOpenResponsesWireRequestFacts("gpt-4o", "gpt-4o", largebody.RewriteSemantics{})
	res := be.ResolveWireRequest(context.Background(), facts, cand)
	if !res.Compatible {
		t.Fatalf("expected Compatible=true, got reason: %v", res.Reason)
	}
	if res.NeedsModelRewrite {
		t.Fatal("expected NeedsModelRewrite=false when models match")
	}
}

// Requirement 8, 9, 17.2: Model rewrite validation.
func TestOpenResponsesWireRequest_ModelRewriteValidation(t *testing.T) {
	t.Parallel()

	be := newTestOpenResponsesWireBackend("http://127.0.0.1:8080/v1", nil, nil)

	cand := routing.AttemptCandidate{
		Primary: routing.Primary{
			Backend: "test-compat-openresponses",
			Model:   "gpt-4o-mini",
		},
	}

	t.Run("rewrite supported when profile provides model token rewrite", func(t *testing.T) {
		t.Parallel()
		facts := makeValidOpenResponsesWireRequestFacts("gpt-4o", "gpt-4o-mini", testRewriteModelToken())
		res := be.ResolveWireRequest(context.Background(), facts, cand)
		if !res.Compatible {
			t.Fatalf("expected Compatible=true with rewrite, got reason: %v", res.Reason)
		}
		if !res.NeedsModelRewrite {
			t.Fatal("expected NeedsModelRewrite=true when models differ")
		}
	})

	t.Run("rewrite rejected when profile does not provide rewrite", func(t *testing.T) {
		t.Parallel()
		facts := makeValidOpenResponsesWireRequestFacts("gpt-4o", "gpt-4o-mini", largebody.RewriteSemantics{})
		res := be.ResolveWireRequest(context.Background(), facts, cand)
		if res.Compatible {
			t.Fatal("expected Compatible=false when models differ but profile does not support rewrite")
		}
		if res.Reason != largebody.WireSupportReasonRewriteUnsupported {
			t.Fatalf("expected WireSupportReasonRewriteUnsupported, got %v", res.Reason)
		}
	})
}

// Requirement 8, 17.2: Incompatible facts rejected.
func TestOpenResponsesWireRequest_IncompatibleFacts(t *testing.T) {
	t.Parallel()

	be := newTestOpenResponsesWireBackend("http://127.0.0.1:8080/v1", nil, nil)
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{
			Backend: "test-compat-openresponses",
			Model:   "gpt-4o",
		},
	}

	t.Run("wrong operation rejected", func(t *testing.T) {
		t.Parallel()
		facts := makeValidOpenResponsesWireRequestFacts("gpt-4o", "gpt-4o", largebody.RewriteSemantics{})
		facts.Operation = aipapi.OperationOpenAIChatCompletions
		res := be.ResolveWireRequest(context.Background(), facts, cand)
		if res.Compatible {
			t.Fatal("expected Compatible=false for wrong operation")
		}
	})

	t.Run("wrong profile rejected", func(t *testing.T) {
		t.Parallel()
		facts := makeValidOpenResponsesWireRequestFacts("gpt-4o", "gpt-4o", largebody.RewriteSemantics{})
		facts.ProfileID = "unknown_profile"
		res := be.ResolveWireRequest(context.Background(), facts, cand)
		if res.Compatible {
			t.Fatal("expected Compatible=false for wrong profile")
		}
		if res.Reason != largebody.WireSupportReasonProfileUnsupported {
			t.Fatalf("expected WireSupportReasonProfileUnsupported, got %v", res.Reason)
		}
	})

	t.Run("non-streaming delivery rejected", func(t *testing.T) {
		t.Parallel()
		facts := makeValidOpenResponsesWireRequestFacts("gpt-4o", "gpt-4o", largebody.RewriteSemantics{})
		facts.Delivery = aipapi.DeliveryModeNonStreaming
		res := be.ResolveWireRequest(context.Background(), facts, cand)
		if res.Compatible {
			t.Fatal("expected Compatible=false for non-streaming delivery")
		}
		if res.Reason != largebody.WireSupportReasonDeliveryUnsupported {
			t.Fatalf("expected WireSupportReasonDeliveryUnsupported, got %v", res.Reason)
		}
	})

	t.Run("non-identity body mode rejected", func(t *testing.T) {
		t.Parallel()
		facts := makeValidOpenResponsesWireRequestFacts("gpt-4o", "gpt-4o", largebody.RewriteSemantics{})
		facts.BodyMode = largebody.BodyMode("gzip_json")
		res := be.ResolveWireRequest(context.Background(), facts, cand)
		if res.Compatible {
			t.Fatal("expected Compatible=false for non-identity body mode")
		}
		if res.Reason != largebody.WireSupportReasonBodyModeUnsupported {
			t.Fatalf("expected WireSupportReasonBodyModeUnsupported, got %v", res.Reason)
		}
	})

	t.Run("canceled context returns unsupported", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		facts := makeValidOpenResponsesWireRequestFacts("gpt-4o", "gpt-4o", largebody.RewriteSemantics{})
		res := be.ResolveWireRequest(ctx, facts, cand)
		if res.Compatible {
			t.Fatal("expected Compatible=false on canceled context")
		}
	})
}

// Requirement 7, 8, 17.2: Universal-only domain proof for OpenResponses.
// Backend with static model inventory without AnyAcceptedModel must decline domain proof.
func TestOpenResponsesWireDomain_UniversalOnly(t *testing.T) {
	t.Parallel()

	t.Run("universal backend accepts domain proof", func(t *testing.T) {
		t.Parallel()
		be := newTestOpenResponsesWireBackend("http://127.0.0.1:8080/v1", nil, nil)
		if be.ResolveWireDomain == nil {
			t.Fatal("expected be.ResolveWireDomain to be non-nil")
		}
		facts := makeValidOpenResponsesWireDomainFacts(true, nil)
		res := be.ResolveWireDomain(context.Background(), facts)
		if !res.Compatible {
			t.Fatalf("expected Compatible=true for universal backend, got reason: %v", res.Reason)
		}
		if !res.AnyAcceptedModel {
			t.Fatal("expected AnyAcceptedModel=true")
		}
	})

	t.Run("non-universal backend declines domain proof (universal-only proof)", func(t *testing.T) {
		t.Parallel()
		inv := staticTestInventory("gpt-4o", "gpt-4o-mini")
		// Non-universal backend: has static inventory, AnyAcceptedModel is nil (false)
		be := newTestOpenResponsesWireBackend("http://127.0.0.1:8080/v1", inv, nil)
		facts := makeValidOpenResponsesWireDomainFacts(true, nil)
		res := be.ResolveWireDomain(context.Background(), facts)
		if res.Compatible {
			t.Fatal("expected Compatible=false for non-universal backend on OpenResponses universal-only domain proof")
		}
		if res.Reason != largebody.WireSupportReasonModelUnsupported {
			t.Fatalf("expected WireSupportReasonModelUnsupported, got %v", res.Reason)
		}
	})

	t.Run("explicit WireDomainAnyAcceptedModel=true overrides static inventory", func(t *testing.T) {
		t.Parallel()
		inv := staticTestInventory("gpt-4o")
		tr := true
		be := newTestOpenResponsesWireBackend("http://127.0.0.1:8080/v1", inv, &tr)
		facts := makeValidOpenResponsesWireDomainFacts(true, nil)
		res := be.ResolveWireDomain(context.Background(), facts)
		if !res.Compatible {
			t.Fatalf("expected Compatible=true when WireDomainAnyAcceptedModel=true, got reason: %v", res.Reason)
		}
		if !res.AnyAcceptedModel {
			t.Fatal("expected AnyAcceptedModel=true")
		}
	})
}

// Requirement 12, 17.2: OpenWire end-to-end transport for OpenResponses create.
func TestOpenResponsesWireOpen_ReusingTask8OpenWire(t *testing.T) {
	t.Parallel()

	var receivedPath string
	var receivedMethod string
	var receivedAuth string
	var receivedContentType string
	var receivedAccept string
	var receivedBody string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		receivedMethod = r.Method
		receivedAuth = r.Header.Get("Authorization")
		receivedContentType = r.Header.Get("Content-Type")
		receivedAccept = r.Header.Get("Accept")
		b, _ := io.ReadAll(r.Body)
		receivedBody = string(b)

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_wire_test\",\"model\":\"gpt-4o\",\"status\":\"in_progress\"}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	be := newTestOpenResponsesWireBackend(srv.URL+"/openresponses/v1", nil, nil)
	if be.OpenWire == nil {
		t.Fatal("expected be.OpenWire to be non-nil")
	}

	bodyContent := `{"model":"gpt-4o","input":"test message","stream":true,"store":false}`
	req := largebody.WireOpenRequest{
		Candidate: routing.AttemptCandidate{
			Primary: routing.Primary{
				Backend: "test-compat-openresponses",
				Model:   "gpt-4o",
			},
		},
		WireRequest:   makeValidOpenResponsesWireRequestFacts("gpt-4o", "gpt-4o", largebody.RewriteSemantics{}),
		TraceID:       "trace-openresponses-wire-1",
		ALegID:        "aleg-1",
		BLegID:        "bleg-1",
		Body:          io.NopCloser(strings.NewReader(bodyContent)),
		ContentLength: int64(len(bodyContent)),
	}

	stream, err := be.OpenWire(context.Background(), req)
	if err != nil {
		t.Fatalf("OpenWire failed: %v", err)
	}
	defer func() { _ = stream.Close() }()

	// Verify server received correct path and headers
	if receivedMethod != http.MethodPost {
		t.Fatalf("receivedMethod: got %q, want POST", receivedMethod)
	}
	if !strings.HasSuffix(receivedPath, "/responses") {
		t.Fatalf("receivedPath: got %q, want suffix /responses", receivedPath)
	}
	if receivedAuth != "Bearer sk-test-openresponses-key" {
		t.Fatalf("receivedAuth: got %q, want Bearer sk-test-openresponses-key", receivedAuth)
	}
	if receivedContentType != "application/json" {
		t.Fatalf("receivedContentType: got %q, want application/json", receivedContentType)
	}
	if receivedAccept != "text/event-stream" {
		t.Fatalf("receivedAccept: got %q, want text/event-stream", receivedAccept)
	}
	if receivedBody != bodyContent {
		t.Fatalf("receivedBody: got %q, want %q", receivedBody, bodyContent)
	}

	// Verify stream delivers event
	ev, err := stream.Recv(context.Background())
	if err != nil {
		t.Fatalf("stream.Recv failed: %v", err)
	}
	if ev.Kind != aipapi.EventResponseStarted {
		t.Fatalf("event kind: got %v, want EventResponseStarted", ev.Kind)
	}
}
