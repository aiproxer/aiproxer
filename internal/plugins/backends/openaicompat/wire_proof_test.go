package openaicompat_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/credpool"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openaicompat"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

const (
	testResponsesProfile = "openai_responses_v1"
	testSSEPayload       = "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"r_wire_1\",\"status\":\"in_progress\"}}\n\nevent: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"wire tokens\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r_wire_1\",\"status\":\"completed\"}}\n\n"
)

func testRewriteModelToken() largebody.RewriteSemantics {
	rw, err := largebody.NewModelTokenRewrite(largebody.Span{Offset: 10, Length: 8})
	if err != nil {
		panic(err)
	}
	return rw
}

func makeValidResponsesWireRequestFacts(clientModel, candModel string, rewrite largebody.RewriteSemantics) largebody.WireRequestFacts {
	return largebody.WireRequestFacts{
		ProfileID:       testResponsesProfile,
		Operation:       aipapi.OperationOpenAIResponses,
		Delivery:        aipapi.DeliveryModeStreaming,
		BodyMode:        largebody.BodyModeIdentityJSON,
		Rewrite:         rewrite,
		ClientModel:     clientModel,
		CandidateModel:  candModel,
		MaxOutputTokens: 2048,
	}
}

func makeValidResponsesWireDomainFacts(universal bool, candidateModels []string) largebody.WireDomainFacts {
	return largebody.WireDomainFacts{
		ProfileID:       testResponsesProfile,
		Operation:       aipapi.OperationOpenAIResponses,
		Delivery:        aipapi.DeliveryModeStreaming,
		BodyMode:        largebody.BodyModeIdentityJSON,
		Rewrite:         testRewriteModelToken(),
		UniversalModel:  universal,
		CandidateModels: candidateModels,
	}
}

func newTestResponsesBackend(baseURL string, inventory modelinventory.Provider, anyAcceptedModel *bool) execbackend.Backend {
	return openaicompat.NewBackend(openaicompat.BackendSpec{
		ID:                         "test-compat-responses",
		BaseURL:                    baseURL,
		APIKey:                     "sk-test-key",
		Flavor:                     openaicompat.FlavorResponses,
		Inventory:                  inventory,
		WireDomainAnyAcceptedModel: anyAcceptedModel,
		RateLimitFallback:          time.Minute,
	})
}

func TestResponsesWireRequest_ExactCompatibility(t *testing.T) {
	t.Parallel()

	be := newTestResponsesBackend("http://127.0.0.1:8080/v1", nil, nil)
	if be.ResolveWireRequest == nil {
		t.Fatal("expected be.ResolveWireRequest to be non-nil on Responses backend")
	}

	cand := routing.AttemptCandidate{
		Primary: routing.Primary{
			Backend: "test-compat-responses",
			Model:   "gpt-4o",
		},
	}

	// 1. Same model: Compatible: true, NeedsModelRewrite: false
	facts := makeValidResponsesWireRequestFacts("gpt-4o", "gpt-4o", largebody.NewNoRewrite())
	support := execbackend.EffectiveWireRequestSupport(context.Background(), be, facts, cand)
	if !support.Compatible {
		t.Fatalf("expected Compatible: true for same model, got false (reason: %s)", support.Reason)
	}
	if support.NeedsModelRewrite {
		t.Fatal("expected NeedsModelRewrite: false for same model")
	}
	if support.Reason != largebody.WireSupportReasonNone {
		t.Fatalf("expected Reason: none, got %s", support.Reason)
	}

	// 2. Different model with RewriteModelToken: Compatible: true, NeedsModelRewrite: true
	factsRewrite := makeValidResponsesWireRequestFacts("gpt-4o", "openai/gpt-4o", testRewriteModelToken())
	candRewrite := routing.AttemptCandidate{
		Primary: routing.Primary{
			Backend: "test-compat-responses",
			Model:   "openai/gpt-4o",
		},
	}
	supportRewrite := execbackend.EffectiveWireRequestSupport(context.Background(), be, factsRewrite, candRewrite)
	if !supportRewrite.Compatible {
		t.Fatalf("expected Compatible: true for model rewrite, got false (reason: %s)", supportRewrite.Reason)
	}
	if !supportRewrite.NeedsModelRewrite {
		t.Fatal("expected NeedsModelRewrite: true when target model differs from client model")
	}

	// 3. Different model when rewrite unsupported (RewriteNone): Compatible: false, Reason: rewrite_unsupported
	factsRewriteBlocked := makeValidResponsesWireRequestFacts("gpt-4o", "openai/gpt-4o", largebody.NewNoRewrite())
	supportRewriteBlocked := execbackend.EffectiveWireRequestSupport(context.Background(), be, factsRewriteBlocked, candRewrite)
	if supportRewriteBlocked.Compatible {
		t.Fatal("expected Compatible: false when rewrite is needed but rewrite semantics is RewriteNone")
	}
	if supportRewriteBlocked.Reason != largebody.WireSupportReasonRewriteUnsupported {
		t.Fatalf("expected Reason: rewrite_unsupported, got %s", supportRewriteBlocked.Reason)
	}

	// 4. Incompatible Profile
	factsBadProfile := facts
	factsBadProfile.ProfileID = "unknown_profile_v1"
	supportBadProfile := execbackend.EffectiveWireRequestSupport(context.Background(), be, factsBadProfile, cand)
	if supportBadProfile.Compatible {
		t.Fatal("expected Compatible: false for unknown profile")
	}
	if supportBadProfile.Reason != largebody.WireSupportReasonProfileUnsupported {
		t.Fatalf("expected Reason: profile_unsupported, got %s", supportBadProfile.Reason)
	}

	// 5. Incompatible Operation
	factsBadOp := facts
	factsBadOp.Operation = aipapi.OperationOpenAIChatCompletions
	supportBadOp := execbackend.EffectiveWireRequestSupport(context.Background(), be, factsBadOp, cand)
	if supportBadOp.Compatible {
		t.Fatal("expected Compatible: false for non-responses operation")
	}
	if supportBadOp.Reason != largebody.WireSupportReasonOperationUnsupported {
		t.Fatalf("expected Reason: operation_unsupported, got %s", supportBadOp.Reason)
	}

	// 6. Non-streaming Delivery rejected
	factsBadDelivery := facts
	factsBadDelivery.Delivery = aipapi.DeliveryModeNonStreaming
	supportBadDelivery := execbackend.EffectiveWireRequestSupport(context.Background(), be, factsBadDelivery, cand)
	if supportBadDelivery.Compatible {
		t.Fatal("expected Compatible: false for non-streaming delivery")
	}
	if supportBadDelivery.Reason != largebody.WireSupportReasonDeliveryUnsupported {
		t.Fatalf("expected Reason: delivery_unsupported, got %s", supportBadDelivery.Reason)
	}

	// 7. Non-Identity BodyMode rejected
	factsBadBodyMode := facts
	factsBadBodyMode.BodyMode = "compressed_json"
	supportBadBodyMode := execbackend.EffectiveWireRequestSupport(context.Background(), be, factsBadBodyMode, cand)
	if supportBadBodyMode.Compatible {
		t.Fatal("expected Compatible: false for non-identity body mode")
	}

	// 8. Canceled Context returns incompatible
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	supportCanceled := execbackend.EffectiveWireRequestSupport(canceledCtx, be, facts, cand)
	if supportCanceled.Compatible {
		t.Fatal("expected Compatible: false on canceled context")
	}
}

func TestResponsesWireRequest_ChatBackendRejectsResponses(t *testing.T) {
	t.Parallel()

	chatBackend := openaicompat.NewBackend(openaicompat.BackendSpec{
		ID:      "test-chat",
		BaseURL: "http://127.0.0.1:8080/v1",
		APIKey:  "sk-test",
		Flavor:  openaicompat.FlavorChat,
	})

	facts := makeValidResponsesWireRequestFacts("gpt-4o", "gpt-4o", largebody.NewNoRewrite())
	cand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "test-chat", Model: "gpt-4o"},
	}

	support := execbackend.EffectiveWireRequestSupport(context.Background(), chatBackend, facts, cand)
	if support.Compatible {
		t.Fatal("expected Chat backend to decline Responses wire request")
	}
	if support.Reason != largebody.WireSupportReasonOperationUnsupported && support.Reason != largebody.WireSupportReasonUnsupported {
		t.Fatalf("expected Reason: operation_unsupported or unsupported, got %s", support.Reason)
	}
}

func TestResponsesWireRequest_StaticModelInventoryRestriction(t *testing.T) {
	t.Parallel()

	inv := modelinventory.StaticProvider{
		Models: []modelinventory.Model{
			{CanonicalID: "gpt-4o", NativeID: "gpt-4o-native"},
			{CanonicalID: "o3-mini", NativeID: "o3-mini"},
		},
	}
	be := newTestResponsesBackend("http://127.0.0.1:8080/v1", inv, nil)

	// In inventory -> Compatible
	factsIn := makeValidResponsesWireRequestFacts("gpt-4o", "gpt-4o", largebody.NewNoRewrite())
	candIn := routing.AttemptCandidate{Primary: routing.Primary{Backend: "test-compat-responses", Model: "gpt-4o"}}
	supportIn := execbackend.EffectiveWireRequestSupport(context.Background(), be, factsIn, candIn)
	if !supportIn.Compatible {
		t.Fatalf("expected Compatible: true for model in static inventory, got false (reason: %s)", supportIn.Reason)
	}

	// Native ID in inventory -> Compatible
	factsNative := makeValidResponsesWireRequestFacts("gpt-4o", "gpt-4o-native", testRewriteModelToken())
	candNative := routing.AttemptCandidate{Primary: routing.Primary{Backend: "test-compat-responses", Model: "gpt-4o-native"}}
	supportNative := execbackend.EffectiveWireRequestSupport(context.Background(), be, factsNative, candNative)
	if !supportNative.Compatible {
		t.Fatalf("expected Compatible: true for native model in static inventory, got false (reason: %s)", supportNative.Reason)
	}

	// Not in inventory -> Incompatible with model_unsupported
	factsOut := makeValidResponsesWireRequestFacts("gpt-4o", "claude-3-opus", testRewriteModelToken())
	candOut := routing.AttemptCandidate{Primary: routing.Primary{Backend: "test-compat-responses", Model: "claude-3-opus"}}
	supportOut := execbackend.EffectiveWireRequestSupport(context.Background(), be, factsOut, candOut)
	if supportOut.Compatible {
		t.Fatal("expected Compatible: false for model not in static inventory")
	}
	if supportOut.Reason != largebody.WireSupportReasonModelUnsupported {
		t.Fatalf("expected Reason: model_unsupported, got %s", supportOut.Reason)
	}
}

func TestResponsesWireDomain_UniversalAndRestricted(t *testing.T) {
	t.Parallel()

	// Case 1: Universal backend (no static inventory restriction)
	beUniversal := newTestResponsesBackend("http://127.0.0.1:8080/v1", nil, nil)
	if beUniversal.ResolveWireDomain == nil {
		t.Fatal("expected be.ResolveWireDomain to be non-nil on Responses backend")
	}

	domainUniversal := makeValidResponsesWireDomainFacts(true, nil)
	supportUniv := execbackend.EffectiveWireDomainSupport(context.Background(), beUniversal, domainUniversal)
	if !supportUniv.Compatible {
		t.Fatalf("expected Compatible: true for universal domain, got false (reason: %s)", supportUniv.Reason)
	}
	if !supportUniv.AnyAcceptedModel {
		t.Fatal("expected AnyAcceptedModel: true on universal backend")
	}
	if supportUniv.Reason != largebody.WireSupportReasonNone {
		t.Fatalf("expected Reason: none, got %s", supportUniv.Reason)
	}

	// Case 2: Backend with static inventory models (restricted declared domain)
	inv := modelinventory.StaticProvider{
		Models: []modelinventory.Model{
			{CanonicalID: "gpt-4o", NativeID: "gpt-4o"},
			{CanonicalID: "gpt-4o-mini", NativeID: "gpt-4o-mini"},
		},
	}
	beRestricted := newTestResponsesBackend("http://127.0.0.1:8080/v1", inv, nil)

	// Unbounded universal domain MUST decline on restricted inventory backend (Requirement 7.3)
	supportRestrictedUniv := execbackend.EffectiveWireDomainSupport(context.Background(), beRestricted, domainUniversal)
	if supportRestrictedUniv.Compatible {
		t.Fatal("expected Compatible: false for unbounded domain on restricted backend")
	}
	if supportRestrictedUniv.Reason != largebody.WireSupportReasonModelUnsupported {
		t.Fatalf("expected Reason: model_unsupported, got %s", supportRestrictedUniv.Reason)
	}

	// Finite domain with all matching candidate models MUST succeed
	domainFiniteMatch := makeValidResponsesWireDomainFacts(false, []string{"gpt-4o", "gpt-4o-mini"})
	supportFiniteMatch := execbackend.EffectiveWireDomainSupport(context.Background(), beRestricted, domainFiniteMatch)
	if !supportFiniteMatch.Compatible {
		t.Fatalf("expected Compatible: true for matching finite domain, got false (reason: %s)", supportFiniteMatch.Reason)
	}
	if supportFiniteMatch.AnyAcceptedModel {
		t.Fatal("expected AnyAcceptedModel: false for restricted backend")
	}

	// Finite domain with non-matching candidate model MUST decline
	domainFiniteMismatch := makeValidResponsesWireDomainFacts(false, []string{"gpt-4o", "unregistered-model"})
	supportFiniteMismatch := execbackend.EffectiveWireDomainSupport(context.Background(), beRestricted, domainFiniteMismatch)
	if supportFiniteMismatch.Compatible {
		t.Fatal("expected Compatible: false for non-matching model in finite domain")
	}
	if supportFiniteMismatch.Reason != largebody.WireSupportReasonModelUnsupported {
		t.Fatalf("expected Reason: model_unsupported, got %s", supportFiniteMismatch.Reason)
	}

	// Case 3: Explicit override with WireDomainAnyAcceptedModel: &false
	f := false
	beExplicitFalse := newTestResponsesBackend("http://127.0.0.1:8080/v1", nil, &f)
	supportExplicitFalse := execbackend.EffectiveWireDomainSupport(context.Background(), beExplicitFalse, domainUniversal)
	if supportExplicitFalse.Compatible {
		t.Fatal("expected Compatible: false when WireDomainAnyAcceptedModel is explicitly false")
	}

	// Case 4: Incompatible operation in domain facts
	domainBadOp := makeValidResponsesWireDomainFacts(false, []string{"gpt-4o"})
	domainBadOp.Operation = aipapi.OperationOpenAIChatCompletions
	supportBadOp := execbackend.EffectiveWireDomainSupport(context.Background(), beUniversal, domainBadOp)
	if supportBadOp.Compatible {
		t.Fatal("expected Compatible: false for bad operation in domain facts")
	}
	if supportBadOp.Reason != largebody.WireSupportReasonOperationUnsupported {
		t.Fatalf("expected Reason: operation_unsupported, got %s", supportBadOp.Reason)
	}
}

func TestOpenWire_SuccessStreaming(t *testing.T) {
	t.Parallel()

	var receivedPath string
	var receivedAuth string
	var receivedContentType string
	var receivedAccept string
	var receivedTrailers int
	var receivedBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		receivedAuth = r.Header.Get("Authorization")
		receivedContentType = r.Header.Get("Content-Type")
		receivedAccept = r.Header.Get("Accept")
		receivedTrailers = len(r.Trailer)
		receivedBody, _ = io.ReadAll(r.Body)

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, testSSEPayload)
	}))
	defer srv.Close()

	pool, err := credpool.New([]credpool.Credential{{ID: "c1", Secret: "sk-wire-success"}})
	if err != nil {
		t.Fatal(err)
	}

	prims := openaicompat.WireOpenPrimitives{
		ProviderID:        "responses-provider",
		BaseURL:           srv.URL + "/v1",
		Flavor:            openaicompat.FlavorResponses,
		Pool:              pool,
		RateLimitFallback: time.Minute,
		MaxPending:        50,
	}

	reqPayload := `{"model":"gpt-4o","input":"test wire open"}`
	wireReq := largebody.WireOpenRequest{
		Candidate: routing.AttemptCandidate{
			Primary: routing.Primary{
				Backend: "responses-provider",
				Model:   "gpt-4o",
			},
		},
		Body:          io.NopCloser(strings.NewReader(reqPayload)),
		ContentLength: int64(len(reqPayload)),
		WireRequest:   makeValidResponsesWireRequestFacts("gpt-4o", "gpt-4o", largebody.NewNoRewrite()),
		TraceID:       "trace-wire-1",
		ALegID:        "aleg-wire-1",
		BLegID:        "bleg-wire-1",
		Header: http.Header{
			"X-Session-Id":  []string{"leak-check-session"},
			"Authorization": []string{"Bearer client-token-must-not-leak"},
			"X-Custom-Safe": []string{"keep-this-header"},
		},
	}

	stream, err := prims.OpenWire(context.Background(), wireReq)
	if err != nil {
		t.Fatalf("OpenWire failed: %v", err)
	}
	defer func() { _ = stream.Close() }()

	// Verify HTTP outbound semantics (Requirement 12.1, 12.2, 12.4, 12.7)
	if receivedPath != "/v1/responses" {
		t.Fatalf("receivedPath = %q, want /v1/responses", receivedPath)
	}
	if receivedAuth != "Bearer sk-wire-success" {
		t.Fatalf("receivedAuth = %q, want Bearer sk-wire-success", receivedAuth)
	}
	if receivedContentType != "application/json" {
		t.Fatalf("receivedContentType = %q, want application/json", receivedContentType)
	}
	if receivedAccept != "text/event-stream" {
		t.Fatalf("receivedAccept = %q, want text/event-stream", receivedAccept)
	}
	if receivedTrailers != 0 {
		t.Fatalf("receivedTrailers = %d, want 0", receivedTrailers)
	}
	if string(receivedBody) != reqPayload {
		t.Fatalf("receivedBody = %q, want %q", string(receivedBody), reqPayload)
	}

	// Verify canonical event stream (Requirement 12.5)
	var events []aipapi.Event
	for {
		ev, err := stream.Recv(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("stream.Recv failed: %v", err)
		}
		events = append(events, ev)
	}

	if len(events) == 0 {
		t.Fatal("expected non-empty canonical events")
	}
	var hasTextDelta bool
	for _, ev := range events {
		if ev.Kind == aipapi.EventTextDelta && ev.Delta == "wire tokens" {
			hasTextDelta = true
		}
	}
	if !hasTextDelta {
		t.Fatalf("expected text delta event 'wire tokens', got: %v", events)
	}
}

func TestOpenWire_BackendIntegration(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, testSSEPayload)
	}))
	defer srv.Close()

	be := newTestResponsesBackend(srv.URL+"/v1", nil, nil)
	if be.OpenWire == nil {
		t.Fatal("expected be.OpenWire to be non-nil")
	}

	reqPayload := `{"model":"gpt-4o","input":"test backend openwire"}`
	wireReq := largebody.WireOpenRequest{
		Candidate: routing.AttemptCandidate{
			Primary: routing.Primary{
				Backend: "test-compat-responses",
				Model:   "gpt-4o",
			},
		},
		Body:          io.NopCloser(strings.NewReader(reqPayload)),
		ContentLength: int64(len(reqPayload)),
		WireRequest:   makeValidResponsesWireRequestFacts("gpt-4o", "gpt-4o", largebody.NewNoRewrite()),
		TraceID:       "trace-be-1",
		ALegID:        "aleg-be-1",
		BLegID:        "bleg-be-1",
	}

	var observed coremetering.PreparedInputSummary
	ctx := coremetering.WithPreparedInputObserver(context.Background(), func(summary coremetering.PreparedInputSummary) {
		observed = summary
	})
	stream, err := execbackend.EffectiveWireOpen(ctx, be, wireReq)
	if err != nil {
		t.Fatalf("EffectiveWireOpen failed: %v", err)
	}
	defer func() { _ = stream.Close() }()

	ev, err := stream.Recv(context.Background())
	if err != nil && err != io.EOF {
		t.Fatalf("stream.Recv failed: %v", err)
	}
	if ev.Kind == "" {
		t.Fatal("expected non-empty event kind from peeked stream")
	}
	if observed.MethodRef != "adapter:openaicompat.wire_payload.v1" || !observed.PayloadBytesPresent || observed.PayloadBytes != int64(len(reqPayload)) {
		t.Fatalf("final wire boundary summary=%#v, want exact bounded payload metadata", observed)
	}
}

func TestOpenWire_CredentialCooldownAndRecoverableError(t *testing.T) {
	t.Parallel()

	// 1. Test 429 Too Many Requests triggers rate limit cooldown and recoverable error
	srv429 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "10")
		http.Error(w, `{"error":{"message":"Rate limit exceeded","type":"rate_limit"}}`, http.StatusTooManyRequests)
	}))
	defer srv429.Close()

	pool429, err := credpool.New([]credpool.Credential{{ID: "k_rate", Secret: "sk-rate-limit"}})
	if err != nil {
		t.Fatal(err)
	}

	prims429 := openaicompat.WireOpenPrimitives{
		ProviderID:        "rate-provider",
		BaseURL:           srv429.URL + "/v1",
		Flavor:            openaicompat.FlavorResponses,
		Pool:              pool429,
		RateLimitFallback: time.Minute,
	}

	req429 := largebody.WireOpenRequest{
		Candidate:     routing.AttemptCandidate{Primary: routing.Primary{Backend: "rate-provider", Model: "gpt-4o"}},
		Body:          io.NopCloser(strings.NewReader(`{}`)),
		ContentLength: 2,
		WireRequest:   makeValidResponsesWireRequestFacts("gpt-4o", "gpt-4o", largebody.NewNoRewrite()),
		TraceID:       "t-429",
		ALegID:        "a-429",
		BLegID:        "b-429",
	}

	_, err429 := prims429.OpenWire(context.Background(), req429)
	if err429 == nil {
		t.Fatal("expected error on 429, got nil")
	}
	if !aipapi.IsRecoverablePreOutput(err429) {
		t.Fatalf("expected 429 error to be RecoverablePreOutputError, got: %v", err429)
	}
	// Pool should have marked k_rate as rate limited
	_, acquireErr := pool429.Acquire(time.Now(), nil)
	if !errors.Is(acquireErr, credpool.ErrNoUsableCredential) {
		t.Fatalf("expected pool to be exhausted due to cooldown, got: %v", acquireErr)
	}
	status429 := pool429.Snapshot(time.Now())
	if len(status429) == 0 || status429[0].State != credpool.StateCooldown {
		t.Fatalf("expected credential state cooldown, got: %v", status429)
	}

	// 2. Test 401 Unauthorized triggers auth invalid and recoverable error
	srv401 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"Invalid API key","type":"invalid_request_error"}}`, http.StatusUnauthorized)
	}))
	defer srv401.Close()

	pool401, err := credpool.New([]credpool.Credential{{ID: "k_auth", Secret: "sk-bad-auth"}})
	if err != nil {
		t.Fatal(err)
	}

	prims401 := openaicompat.WireOpenPrimitives{
		ProviderID:        "auth-provider",
		BaseURL:           srv401.URL + "/v1",
		Flavor:            openaicompat.FlavorResponses,
		Pool:              pool401,
		RateLimitFallback: time.Minute,
	}

	req401 := largebody.WireOpenRequest{
		Candidate:     routing.AttemptCandidate{Primary: routing.Primary{Backend: "auth-provider", Model: "gpt-4o"}},
		Body:          io.NopCloser(strings.NewReader(`{}`)),
		ContentLength: 2,
		WireRequest:   makeValidResponsesWireRequestFacts("gpt-4o", "gpt-4o", largebody.NewNoRewrite()),
		TraceID:       "t-401",
		ALegID:        "a-401",
		BLegID:        "b-401",
	}

	_, err401 := prims401.OpenWire(context.Background(), req401)
	if err401 == nil {
		t.Fatal("expected error on 401, got nil")
	}
	if !aipapi.IsRecoverablePreOutput(err401) {
		t.Fatalf("expected 401 error to be RecoverablePreOutputError, got: %v", err401)
	}
	// Pool should have marked k_auth as invalid
	_, acquireAuthErr := pool401.Acquire(time.Now(), nil)
	if !errors.Is(acquireAuthErr, credpool.ErrNoUsableCredential) {
		t.Fatalf("expected pool to be exhausted due to auth invalid, got: %v", acquireAuthErr)
	}
	status401 := pool401.Snapshot(time.Now())
	if len(status401) == 0 || status401[0].State != credpool.StateAuthInvalid {
		t.Fatalf("expected credential state auth invalid, got: %v", status401)
	}
}

func TestOpenWire_NoAuthCompatibleBackendSucceedsWithoutAuthorizationHeader(t *testing.T) {
	t.Parallel()

	var receivedAuth string
	var authHeaderPresent bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if authVals, ok := r.Header["Authorization"]; ok {
			authHeaderPresent = true
			receivedAuth = strings.Join(authVals, ",")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, testSSEPayload)
	}))
	defer srv.Close()

	be := openaicompat.NewBackend(openaicompat.BackendSpec{
		ID:                 "test-compat-noauth",
		BaseURL:            srv.URL + "/v1",
		Flavor:             openaicompat.FlavorResponses,
		CompatibleModeAuth: true,
	})

	if be.OpenWire == nil {
		t.Fatal("expected be.OpenWire to be non-nil")
	}

	reqPayload := `{"model":"gpt-4o","input":"no auth wire test"}`
	wireReq := largebody.WireOpenRequest{
		Candidate: routing.AttemptCandidate{
			Primary: routing.Primary{
				Backend: "test-compat-noauth",
				Model:   "gpt-4o",
			},
		},
		Body:          io.NopCloser(strings.NewReader(reqPayload)),
		ContentLength: int64(len(reqPayload)),
		WireRequest:   makeValidResponsesWireRequestFacts("gpt-4o", "gpt-4o", largebody.NewNoRewrite()),
		TraceID:       "trace-noauth-1",
		ALegID:        "aleg-noauth-1",
		BLegID:        "bleg-noauth-1",
		Header: http.Header{
			"Authorization": []string{"Bearer client-must-be-stripped"},
		},
	}

	stream, err := execbackend.EffectiveWireOpen(context.Background(), be, wireReq)
	if err != nil {
		t.Fatalf("EffectiveWireOpen failed: %v", err)
	}
	defer func() { _ = stream.Close() }()

	ev, err := stream.Recv(context.Background())
	if err != nil && err != io.EOF {
		t.Fatalf("stream.Recv failed: %v", err)
	}
	if ev.Kind == "" {
		t.Fatal("expected non-empty event kind from stream")
	}

	if authHeaderPresent {
		t.Fatalf("expected no Authorization header sent upstream, got %q", receivedAuth)
	}
}

func TestCallDependentResolverDeclinesWireExecution(t *testing.T) {
	t.Parallel()

	callObserved := false
	be := execbackend.Backend{
		Caps: openaicompat.HostedCaps(),
		ResolveCaps: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) aipapi.BackendCaps {
			if call.Route.Selector != "" {
				callObserved = true
			}
			return aipapi.BackendCaps{aipapi.CapabilityStreaming: {}}
		},
		ResolveWireCaps: nil, // Arbitrary Call-dependent resolver not adapted for wire
	}
	spec := openaicompat.BackendSpec{
		ID:      "test-compat-call-dependent",
		BaseURL: "http://127.0.0.1:8080/v1",
		Flavor:  openaicompat.FlavorResponses,
	}
	be = openaicompat.AttachWireProofForTest(be, spec, nil, nil)

	cand := routing.AttemptCandidate{
		Primary: routing.Primary{
			Backend: "test-compat-call-dependent",
			Model:   "gpt-4o",
		},
	}

	// 1. Request-level wire proof must decline
	reqFacts := makeValidResponsesWireRequestFacts("gpt-4o", "gpt-4o", largebody.NewNoRewrite())
	reqFacts.RequiredCapabilities = []aipapi.Capability{aipapi.CapabilityStreaming}
	reqSupport := execbackend.EffectiveWireRequestSupport(context.Background(), be, reqFacts, cand)
	if reqSupport.Compatible {
		t.Fatal("expected request Compatible: false when ResolveCaps is set but ResolveWireCaps is nil")
	}
	if reqSupport.Reason != largebody.WireSupportReasonCapabilityUnsupported {
		t.Fatalf("expected request Reason: WireSupportReasonCapabilityUnsupported, got %s", reqSupport.Reason)
	}

	// 2. Domain-level wire proof must decline
	domFacts := makeValidResponsesWireDomainFacts(false, []string{"gpt-4o"})
	domFacts.RequiredCapabilities = []aipapi.Capability{aipapi.CapabilityStreaming}
	domSupport := execbackend.EffectiveWireDomainSupport(context.Background(), be, domFacts)
	if domSupport.Compatible {
		t.Fatal("expected domain Compatible: false when ResolveCaps is set but ResolveWireCaps is nil")
	}
	if domSupport.Reason != largebody.WireSupportReasonCapabilityUnsupported {
		t.Fatalf("expected domain Reason: WireSupportReasonCapabilityUnsupported, got %s", domSupport.Reason)
	}

	// 3. Universal domain wire proof must decline
	univFacts := makeValidResponsesWireDomainFacts(true, nil)
	univFacts.RequiredCapabilities = []aipapi.Capability{aipapi.CapabilityStreaming}
	univSupport := execbackend.EffectiveWireDomainSupport(context.Background(), be, univFacts)
	if univSupport.Compatible {
		t.Fatal("expected universal domain Compatible: false when ResolveCaps is set but ResolveWireCaps is nil")
	}
	if univSupport.Reason != largebody.WireSupportReasonCapabilityUnsupported {
		t.Fatalf("expected universal domain Reason: WireSupportReasonCapabilityUnsupported, got %s", univSupport.Reason)
	}

	// 4. Invariant: no synthetic Call was passed to ResolveCaps
	if callObserved {
		t.Fatal("expected no synthetic Call to be passed to ResolveCaps")
	}
}
