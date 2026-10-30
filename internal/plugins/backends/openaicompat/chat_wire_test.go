package openaicompat_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/credpool"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openaicompat"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

const (
	testChatProfile   = "openai_chat_v1"
	testChatSSEChunk1 = "data: {\"id\":\"chatcmpl-test\",\"object\":\"chat.completion.chunk\",\"created\":1726000000,\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n\n"
	testChatSSEChunk2 = "data: {\"id\":\"chatcmpl-test\",\"object\":\"chat.completion.chunk\",\"created\":1726000000,\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello \"},\"finish_reason\":null}]}\n\n"
	testChatSSEChunk3 = "data: {\"id\":\"chatcmpl-test\",\"object\":\"chat.completion.chunk\",\"created\":1726000000,\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"world!\"},\"finish_reason\":null}]}\n\n"
	testChatSSEChunk4 = "data: {\"id\":\"chatcmpl-test\",\"object\":\"chat.completion.chunk\",\"created\":1726000000,\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"
	testChatSSEDone   = "data: [DONE]\n\n"
	testChatSSEStream = testChatSSEChunk1 + testChatSSEChunk2 + testChatSSEChunk3 + testChatSSEChunk4 + testChatSSEDone
)

func makeValidChatWireRequestFacts(clientModel, candModel string, rewrite largebody.RewriteSemantics) largebody.WireRequestFacts {
	return largebody.WireRequestFacts{
		ProfileID:       testChatProfile,
		Operation:       aipapi.OperationOpenAIChatCompletions,
		Delivery:        aipapi.DeliveryModeStreaming,
		BodyMode:        largebody.BodyModeIdentityJSON,
		Rewrite:         rewrite,
		ClientModel:     clientModel,
		CandidateModel:  candModel,
		MaxOutputTokens: 2048,
	}
}

func makeValidChatWireDomainFacts(universal bool, candidateModels []string) largebody.WireDomainFacts {
	return largebody.WireDomainFacts{
		ProfileID:       testChatProfile,
		Operation:       aipapi.OperationOpenAIChatCompletions,
		Delivery:        aipapi.DeliveryModeStreaming,
		BodyMode:        largebody.BodyModeIdentityJSON,
		Rewrite:         testRewriteModelToken(),
		UniversalModel:  universal,
		CandidateModels: candidateModels,
	}
}

func newTestChatBackend(baseURL string, inventory modelinventory.Provider, anyAcceptedModel *bool) execbackend.Backend {
	return openaicompat.NewBackend(openaicompat.BackendSpec{
		ID:                         "test-compat-chat",
		BaseURL:                    baseURL,
		APIKey:                     "sk-test-chat-key",
		Flavor:                     openaicompat.FlavorChat,
		Inventory:                  inventory,
		WireDomainAnyAcceptedModel: anyAcceptedModel,
		RateLimitFallback:          time.Minute,
	})
}

func TestChatWireRequest_ExactCompatibility(t *testing.T) {
	t.Parallel()

	be := newTestChatBackend("http://127.0.0.1:8080/v1", nil, nil)
	if be.ResolveWireRequest == nil {
		t.Fatal("expected be.ResolveWireRequest to be non-nil on Chat backend")
	}

	cand := routing.AttemptCandidate{
		Primary: routing.Primary{
			Backend: "test-compat-chat",
			Model:   "gpt-4o",
		},
	}

	// 1. Same model: Compatible: true, NeedsModelRewrite: false
	facts := makeValidChatWireRequestFacts("gpt-4o", "gpt-4o", largebody.NewNoRewrite())
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
	factsDiff := makeValidChatWireRequestFacts("gpt-4o", "gpt-4o-mini", testRewriteModelToken())
	supportDiff := execbackend.EffectiveWireRequestSupport(context.Background(), be, factsDiff, cand)
	if !supportDiff.Compatible {
		t.Fatalf("expected Compatible: true for rewritten model, got false (reason: %s)", supportDiff.Reason)
	}
	if !supportDiff.NeedsModelRewrite {
		t.Fatal("expected NeedsModelRewrite: true for rewritten model")
	}
	if supportDiff.Reason != largebody.WireSupportReasonNone {
		t.Fatalf("expected Reason: none, got %s", supportDiff.Reason)
	}

	// 3. Different model without RewriteModelToken: Compatible: false, Reason: rewrite_unsupported
	factsNoRewrite := makeValidChatWireRequestFacts("gpt-4o", "gpt-4o-mini", largebody.NewNoRewrite())
	supportNoRewrite := execbackend.EffectiveWireRequestSupport(context.Background(), be, factsNoRewrite, cand)
	if supportNoRewrite.Compatible {
		t.Fatal("expected Compatible: false when different model lacks rewrite semantics")
	}
	if supportNoRewrite.Reason != largebody.WireSupportReasonRewriteUnsupported {
		t.Fatalf("expected Reason: rewrite_unsupported, got %s", supportNoRewrite.Reason)
	}

	// 4. Responses backend rejects Chat wire request
	responsesBE := newTestResponsesBackend("http://127.0.0.1:8080/v1", nil, nil)
	supportResponsesBE := execbackend.EffectiveWireRequestSupport(context.Background(), responsesBE, facts, cand)
	if supportResponsesBE.Compatible {
		t.Fatal("expected Responses backend to reject Chat wire request")
	}
	if supportResponsesBE.Reason != largebody.WireSupportReasonOperationUnsupported {
		t.Fatalf("expected Reason: operation_unsupported, got %s", supportResponsesBE.Reason)
	}

	// 5. Wrong operation (OperationOpenAIResponses): Compatible: false, Reason: operation_unsupported
	factsWrongOp := facts
	factsWrongOp.Operation = aipapi.OperationOpenAIResponses
	supportWrongOp := execbackend.EffectiveWireRequestSupport(context.Background(), be, factsWrongOp, cand)
	if supportWrongOp.Compatible {
		t.Fatal("expected Compatible: false for wrong operation")
	}
	if supportWrongOp.Reason != largebody.WireSupportReasonOperationUnsupported {
		t.Fatalf("expected Reason: operation_unsupported, got %s", supportWrongOp.Reason)
	}

	// 6. Wrong profile: Compatible: false, Reason: profile_unsupported
	factsWrongProf := facts
	factsWrongProf.ProfileID = "unsupported_chat_profile"
	supportWrongProf := execbackend.EffectiveWireRequestSupport(context.Background(), be, factsWrongProf, cand)
	if supportWrongProf.Compatible {
		t.Fatal("expected Compatible: false for wrong profile")
	}
	if supportWrongProf.Reason != largebody.WireSupportReasonProfileUnsupported {
		t.Fatalf("expected Reason: profile_unsupported, got %s", supportWrongProf.Reason)
	}

	// 7. Non-streaming delivery: Compatible: false, Reason: delivery_unsupported
	factsNonStream := facts
	factsNonStream.Delivery = aipapi.DeliveryModeNonStreaming
	supportNonStream := execbackend.EffectiveWireRequestSupport(context.Background(), be, factsNonStream, cand)
	if supportNonStream.Compatible {
		t.Fatal("expected Compatible: false for non-streaming delivery")
	}
	if supportNonStream.Reason != largebody.WireSupportReasonDeliveryUnsupported {
		t.Fatalf("expected Reason: delivery_unsupported, got %s", supportNonStream.Reason)
	}

	// 8. BodyMode unsupported: Compatible: false
	factsWrongBody := facts
	factsWrongBody.BodyMode = largebody.BodyModeUnknown
	supportWrongBody := execbackend.EffectiveWireRequestSupport(context.Background(), be, factsWrongBody, cand)
	if supportWrongBody.Compatible {
		t.Fatal("expected Compatible: false for unknown body mode")
	}

	// 9. Empty model: Compatible: false, Reason: model_unsupported
	factsEmptyModel := facts
	factsEmptyModel.ClientModel = ""
	factsEmptyModel.CandidateModel = ""
	candEmpty := routing.AttemptCandidate{}
	supportEmptyModel := execbackend.EffectiveWireRequestSupport(context.Background(), be, factsEmptyModel, candEmpty)
	if supportEmptyModel.Compatible {
		t.Fatal("expected Compatible: false for empty model")
	}
	if supportEmptyModel.Reason != largebody.WireSupportReasonModelUnsupported {
		t.Fatalf("expected Reason: model_unsupported, got %s", supportEmptyModel.Reason)
	}

	// 10. Canceled context: Compatible: false
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	supportCanceled := execbackend.EffectiveWireRequestSupport(canceledCtx, be, facts, cand)
	if supportCanceled.Compatible {
		t.Fatal("expected Compatible: false on canceled context")
	}
}

func TestChatWireRequest_StaticModelInventoryRestriction(t *testing.T) {
	t.Parallel()

	inventory := modelinventory.StaticProvider{
		Models: []modelinventory.Model{
			{CanonicalID: "gpt-4o", NativeID: "gpt-4o-native"},
			{CanonicalID: "gpt-4o-mini", NativeID: "gpt-4o-mini-native"},
		},
	}
	be := newTestChatBackend("http://127.0.0.1:8080/v1", inventory, nil)

	cand4o := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "test-compat-chat", Model: "gpt-4o"},
	}

	// 1. Model in static inventory: Compatible: true
	factsIn := makeValidChatWireRequestFacts("gpt-4o", "gpt-4o", largebody.NewNoRewrite())
	supportIn := execbackend.EffectiveWireRequestSupport(context.Background(), be, factsIn, cand4o)
	if !supportIn.Compatible {
		t.Fatalf("expected Compatible: true for model in inventory, got false (%s)", supportIn.Reason)
	}

	// 2. Model not in static inventory: Compatible: false, Reason: model_unsupported
	factsOut := makeValidChatWireRequestFacts("claude-3-5-sonnet", "claude-3-5-sonnet", largebody.NewNoRewrite())
	candClaude := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "test-compat-chat", Model: "claude-3-5-sonnet"},
	}
	supportOut := execbackend.EffectiveWireRequestSupport(context.Background(), be, factsOut, candClaude)
	if supportOut.Compatible {
		t.Fatal("expected Compatible: false for model not in static inventory")
	}
	if supportOut.Reason != largebody.WireSupportReasonModelUnsupported {
		t.Fatalf("expected Reason: model_unsupported, got %s", supportOut.Reason)
	}

	// 3. Candidate model rewritten to listed model: Compatible: true
	factsRewriteIn := makeValidChatWireRequestFacts("custom-client-model", "gpt-4o-mini", testRewriteModelToken())
	candMini := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "test-compat-chat", Model: "gpt-4o-mini"},
	}
	supportRewriteIn := execbackend.EffectiveWireRequestSupport(context.Background(), be, factsRewriteIn, candMini)
	if !supportRewriteIn.Compatible {
		t.Fatalf("expected Compatible: true for rewritten model in inventory, got false (%s)", supportRewriteIn.Reason)
	}
	if !supportRewriteIn.NeedsModelRewrite {
		t.Fatal("expected NeedsModelRewrite: true")
	}

	// 4. Candidate model rewritten to unlisted model: Compatible: false, Reason: model_unsupported
	factsRewriteOut := makeValidChatWireRequestFacts("custom-client-model", "unlisted-model", testRewriteModelToken())
	candUnlisted := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "test-compat-chat", Model: "unlisted-model"},
	}
	supportRewriteOut := execbackend.EffectiveWireRequestSupport(context.Background(), be, factsRewriteOut, candUnlisted)
	if supportRewriteOut.Compatible {
		t.Fatal("expected Compatible: false for rewritten model not in inventory")
	}
	if supportRewriteOut.Reason != largebody.WireSupportReasonModelUnsupported {
		t.Fatalf("expected Reason: model_unsupported, got %s", supportRewriteOut.Reason)
	}
}

func TestChatWireDomain_UniversalAndRestricted(t *testing.T) {
	t.Parallel()

	// Universal backend (no static inventory)
	beUniversal := newTestChatBackend("http://127.0.0.1:8080/v1", nil, nil)
	if beUniversal.ResolveWireDomain == nil {
		t.Fatal("expected beUniversal.ResolveWireDomain to be non-nil")
	}

	// 1. Universal model on universal backend: Compatible: true, AnyAcceptedModel: true
	factsUniv := makeValidChatWireDomainFacts(true, nil)
	domainUniv := execbackend.EffectiveWireDomainSupport(context.Background(), beUniversal, factsUniv)
	if !domainUniv.Compatible {
		t.Fatalf("expected domain Compatible: true on universal backend, got false (%s)", domainUniv.Reason)
	}
	if !domainUniv.AnyAcceptedModel {
		t.Fatal("expected AnyAcceptedModel: true on universal backend")
	}

	// 2. Finite model domain on universal backend: Compatible: true, AnyAcceptedModel: true
	factsFinite := makeValidChatWireDomainFacts(false, []string{"gpt-4o", "custom-model"})
	domainFinite := execbackend.EffectiveWireDomainSupport(context.Background(), beUniversal, factsFinite)
	if !domainFinite.Compatible {
		t.Fatalf("expected domain Compatible: true on universal backend, got false (%s)", domainFinite.Reason)
	}

	// Restricted backend with static inventory
	inventory := modelinventory.StaticProvider{
		Models: []modelinventory.Model{
			{CanonicalID: "gpt-4o"},
			{CanonicalID: "gpt-4o-mini"},
		},
	}
	beRestricted := newTestChatBackend("http://127.0.0.1:8080/v1", inventory, nil)

	// 3. Universal model on restricted backend: Compatible: false, Reason: model_unsupported
	domainRestrictedUniv := execbackend.EffectiveWireDomainSupport(context.Background(), beRestricted, factsUniv)
	if domainRestrictedUniv.Compatible {
		t.Fatal("expected domain Compatible: false for universal model on restricted backend")
	}
	if domainRestrictedUniv.Reason != largebody.WireSupportReasonModelUnsupported {
		t.Fatalf("expected Reason: model_unsupported, got %s", domainRestrictedUniv.Reason)
	}

	// 4. Finite candidate models all in inventory: Compatible: true, AnyAcceptedModel: false
	factsFiniteAllIn := makeValidChatWireDomainFacts(false, []string{"gpt-4o", "gpt-4o-mini"})
	domainFiniteAllIn := execbackend.EffectiveWireDomainSupport(context.Background(), beRestricted, factsFiniteAllIn)
	if !domainFiniteAllIn.Compatible {
		t.Fatalf("expected domain Compatible: true when all candidates in inventory, got false (%s)", domainFiniteAllIn.Reason)
	}
	if domainFiniteAllIn.AnyAcceptedModel {
		t.Fatal("expected AnyAcceptedModel: false on restricted backend")
	}

	// 5. Finite candidate models with one missing: Compatible: false, Reason: model_unsupported
	factsFiniteMissing := makeValidChatWireDomainFacts(false, []string{"gpt-4o", "unlisted-model"})
	domainFiniteMissing := execbackend.EffectiveWireDomainSupport(context.Background(), beRestricted, factsFiniteMissing)
	if domainFiniteMissing.Compatible {
		t.Fatal("expected domain Compatible: false when candidate model missing from inventory")
	}
	if domainFiniteMissing.Reason != largebody.WireSupportReasonModelUnsupported {
		t.Fatalf("expected Reason: model_unsupported, got %s", domainFiniteMissing.Reason)
	}

	// 6. Explicit override WireDomainAnyAcceptedModel: true bypasses static inventory
	trueVal := true
	beOverrideUniversal := newTestChatBackend("http://127.0.0.1:8080/v1", inventory, &trueVal)
	domainOverride := execbackend.EffectiveWireDomainSupport(context.Background(), beOverrideUniversal, factsUniv)
	if !domainOverride.Compatible || !domainOverride.AnyAcceptedModel {
		t.Fatalf("expected domain Compatible: true, AnyAcceptedModel: true with WireDomainAnyAcceptedModel override, got %v, %v",
			domainOverride.Compatible, domainOverride.AnyAcceptedModel)
	}

	// 7. Responses backend rejects Chat domain facts
	responsesBE := newTestResponsesBackend("http://127.0.0.1:8080/v1", nil, nil)
	domainResponsesBE := execbackend.EffectiveWireDomainSupport(context.Background(), responsesBE, factsFinite)
	if domainResponsesBE.Compatible {
		t.Fatal("expected Responses backend to reject Chat wire domain facts")
	}
	if domainResponsesBE.Reason != largebody.WireSupportReasonOperationUnsupported {
		t.Fatalf("expected Reason: operation_unsupported, got %s", domainResponsesBE.Reason)
	}

	// 8. Canceled context
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	domainCanceled := execbackend.EffectiveWireDomainSupport(canceledCtx, beUniversal, factsUniv)
	if domainCanceled.Compatible {
		t.Fatal("expected domain Compatible: false on canceled context")
	}
}

func TestChatOpenWire_SuccessStreaming(t *testing.T) {
	t.Parallel()

	var receivedPath string
	var receivedMethod string
	var receivedContentType string
	var receivedAccept string
	var receivedAuth string
	var receivedBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		receivedPath = r.URL.Path
		receivedContentType = r.Header.Get("Content-Type")
		receivedAccept = r.Header.Get("Accept")
		receivedAuth = r.Header.Get("Authorization")
		var err error
		receivedBody, err = io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, testChatSSEStream)
	}))
	defer server.Close()

	be := newTestChatBackend(server.URL, nil, nil)
	if be.OpenWire == nil {
		t.Fatal("expected be.OpenWire to be non-nil on Chat backend")
	}

	reqBody := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"stream":true}`
	wireReq := makeValidChatWireRequestFacts("gpt-4o", "gpt-4o", largebody.NewNoRewrite())
	openReq := largebody.WireOpenRequest{
		Candidate: routing.AttemptCandidate{
			Primary: routing.Primary{
				Backend: "test-compat-chat",
				Model:   "gpt-4o",
			},
		},
		WireRequest:   wireReq,
		TraceID:       "trace-wire-1",
		ALegID:        "aleg-wire-1",
		BLegID:        "bleg-wire-1",
		Body:          io.NopCloser(strings.NewReader(reqBody)),
		ContentLength: int64(len(reqBody)),
	}

	stream, err := be.OpenWire(context.Background(), openReq)
	if err != nil {
		t.Fatalf("OpenWire failed: %v", err)
	}
	defer func() { _ = stream.Close() }()

	// Verify HTTP framing
	if receivedMethod != http.MethodPost {
		t.Errorf("Method: got %s, want POST", receivedMethod)
	}
	if !strings.HasSuffix(receivedPath, "/chat/completions") {
		t.Errorf("Path: got %s, want suffix /chat/completions", receivedPath)
	}
	if receivedContentType != "application/json" {
		t.Errorf("Content-Type: got %s, want application/json", receivedContentType)
	}
	if receivedAccept != "text/event-stream" {
		t.Errorf("Accept: got %s, want text/event-stream", receivedAccept)
	}
	if receivedAuth != "Bearer sk-test-chat-key" {
		t.Errorf("Authorization: got %s, want Bearer sk-test-chat-key", receivedAuth)
	}
	if string(receivedBody) != reqBody {
		t.Errorf("Body: got %s, want %s", string(receivedBody), reqBody)
	}

	// Consume and verify canonical events
	events := make([]aipapi.Event, 0)
	for {
		ev, err := stream.Recv(context.Background())
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("stream.Recv failed: %v", err)
		}
		events = append(events, ev)
	}

	if len(events) == 0 {
		t.Fatal("expected events from chat stream, got 0")
	}

	// Verify text delta content
	var textBuf strings.Builder
	var sawFinished bool
	for _, ev := range events {
		switch ev.Kind {
		case aipapi.EventTextDelta:
			textBuf.WriteString(ev.Delta)
		case aipapi.EventResponseFinished:
			sawFinished = true
		}
	}

	if textBuf.String() != "Hello world!" {
		t.Errorf("accumulated text: got %q, want %q", textBuf.String(), "Hello world!")
	}
	if !sawFinished {
		t.Error("expected EventResponseFinished in stream")
	}
}

func TestChatOpenWire_CredentialCooldownAndRecoverableError(t *testing.T) {
	t.Parallel()

	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Retry-After", "5")
		http.Error(w, `{"error":{"message":"Rate limit reached","type":"requests","code":"rate_limit"}}`, http.StatusTooManyRequests)
	}))
	defer server.Close()

	cred := credpool.Credential{ID: "cred-chat-1", Secret: "sk-rate-limited"}
	be := openaicompat.NewBackend(openaicompat.BackendSpec{
		ID:                "test-chat-cooldown",
		BaseURL:           server.URL,
		Credentials:       []credpool.Credential{cred},
		Flavor:            openaicompat.FlavorChat,
		RateLimitFallback: 5 * time.Second,
	})

	reqBody := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"stream":true}`
	wireReq := makeValidChatWireRequestFacts("gpt-4o", "gpt-4o", largebody.NewNoRewrite())
	openReq := largebody.WireOpenRequest{
		Candidate: routing.AttemptCandidate{
			Primary: routing.Primary{
				Backend: "test-chat-cooldown",
				Model:   "gpt-4o",
			},
		},
		WireRequest:   wireReq,
		TraceID:       "trace-chat-cooldown",
		ALegID:        "aleg-chat-cooldown",
		BLegID:        "bleg-chat-cooldown",
		Body:          io.NopCloser(strings.NewReader(reqBody)),
		ContentLength: int64(len(reqBody)),
	}

	_, err := be.OpenWire(context.Background(), openReq)
	if err == nil {
		t.Fatal("expected error on 429 response, got nil")
	}
	if !aipapi.IsRecoverablePreOutput(err) {
		t.Errorf("expected RecoverablePreOutputError on 429, got: %v", err)
	}
}

func TestChatOpenWire_NoAuthCompatibleBackend(t *testing.T) {
	t.Parallel()

	var authHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, testChatSSEStream)
	}))
	defer server.Close()

	be := openaicompat.NewBackend(openaicompat.BackendSpec{
		ID:                 "test-chat-noauth",
		BaseURL:            server.URL,
		Flavor:             openaicompat.FlavorChat,
		CompatibleModeAuth: true,
	})

	reqBody := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"stream":true}`
	wireReq := makeValidChatWireRequestFacts("gpt-4o", "gpt-4o", largebody.NewNoRewrite())
	openReqNoAuth := largebody.WireOpenRequest{
		Candidate: routing.AttemptCandidate{
			Primary: routing.Primary{
				Backend: "test-chat-noauth",
				Model:   "gpt-4o",
			},
		},
		WireRequest:   wireReq,
		TraceID:       "trace-chat-noauth",
		ALegID:        "aleg-chat-noauth",
		BLegID:        "bleg-chat-noauth",
		Body:          io.NopCloser(strings.NewReader(reqBody)),
		ContentLength: int64(len(reqBody)),
	}

	stream, err := be.OpenWire(context.Background(), openReqNoAuth)
	if err != nil {
		t.Fatalf("OpenWire failed on no-auth backend: %v", err)
	}
	defer func() { _ = stream.Close() }()

	if authHeader != "" {
		t.Errorf("expected no Authorization header on no-auth compatible backend, got %q", authHeader)
	}
}
