package openaicompat_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/httpclient"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/credpool"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openaicompat"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/routeselect"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/sessionwire"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Task 16.3: Provider-effective JSON differential tests for Lane 2:
// OpenAI Chat Completions frontend -> OpenAI-compatible Chat backend.
// Requirements: 17, 18, 21.

type capturedChatProviderRequest struct {
	Method        string
	Path          string
	RawQuery      string
	Header        http.Header
	ContentLength int64
	Trailer       http.Header
	Body          []byte
	ParsedJSON    map[string]any
}

func startChatProviderCaptureServer(t *testing.T) (*httptest.Server, *capturedChatProviderRequest) {
	t.Helper()
	var captured capturedChatProviderRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.Method = r.Method
		captured.Path = r.URL.Path
		captured.RawQuery = r.URL.RawQuery
		captured.Header = r.Header.Clone()
		captured.ContentLength = r.ContentLength
		captured.Trailer = r.Trailer.Clone()

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		captured.Body = body

		if len(body) > 0 {
			var m map[string]any
			if err := json.Unmarshal(body, &m); err == nil {
				captured.ParsedJSON = m
			}
		}

		isStream := strings.Contains(r.Header.Get("Accept"), "text/event-stream")
		if !isStream && len(body) > 0 {
			var probe struct {
				Stream bool `json:"stream"`
			}
			if err := json.Unmarshal(body, &probe); err == nil && probe.Stream {
				isStream = true
			}
		}

		if isStream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, testChatSSEStream)
		} else {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"id":"chatcmpl-1","object":"chat.completion","created":1726000000,"model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &captured
}

func executeChatCanonicalFlow(
	t *testing.T,
	clientBody []byte,
	extraHeaders http.Header,
	candidateModel string,
	streaming bool,
) capturedChatProviderRequest {
	t.Helper()
	srv, captured := startChatProviderCaptureServer(t)

	decoded, err := openailegacy.DecodeChatRequest(clientBody, openailegacy.DecodeOptions{
		RouteSelector: "test-openai-chat:" + candidateModel,
		Headers:       extraHeaders,
	})
	if err != nil {
		t.Fatalf("canonical DecodeChatRequest failed: %v", err)
	}

	spec := openaicompat.BackendSpec{
		ID:      "test-openai-chat",
		BaseURL: srv.URL + "/v1",
		Flavor:  openaicompat.FlavorChat,
		ResolveFlavor: func(call aipapi.Call) openaicompat.Flavor {
			return openaicompat.FlavorChat
		},
		APIKey:     "sk-backend-secret",
		HTTPClient: httpclient.Standard(),
	}
	be := openaicompat.NewBackend(spec)

	cand := routing.AttemptCandidate{
		Primary: routing.Primary{
			Backend: "test-openai-chat",
			Model:   candidateModel,
		},
	}

	call := *decoded.Call
	if streaming {
		call.Invocation.TransportMode = aipapi.TransportModeStreaming
	} else {
		call.Invocation.TransportMode = aipapi.TransportModeNonStreaming
	}

	stream, err := be.Open(context.Background(), call, cand)
	if err != nil {
		t.Fatalf("canonical be.Open failed: %v", err)
	}
	defer func() { _ = stream.Close() }()

	for {
		_, err := stream.Recv(context.Background())
		if err != nil {
			break
		}
	}

	return *captured
}

func executeChatWireFlow(
	t *testing.T,
	clientBody []byte,
	inboundHeaders http.Header,
	candidateModel string,
	streaming bool,
) (capturedChatProviderRequest, int64) {
	t.Helper()
	srv, captured := startChatProviderCaptureServer(t)

	prof := openailegacy.NewProfile()
	proofIn := frontendpipe.ProofInput{
		Ctx:                  context.Background(),
		Headers:              inboundHeaders,
		URLPath:              "/v1/chat/completions",
		RouteSelector:        "test-openai-chat:" + candidateModel,
		RoutePrefixes:        routeselect.NewPrefixSet([]string{"test-openai-chat"}),
		DefaultRouteSelector: "test-openai-chat:default",
		RouteFromBodyModel:   true,
		Source:               newMemSource(clientBody),
		BodyBytes:            int64(len(clientBody)),
	}
	proofOut, err := prof.CompileProof(context.Background(), proofIn)
	if err != nil {
		t.Fatalf("CompileProof failed: %v", err)
	}
	proof := proofOut.State.Proof

	spliceReader, err := largebody.SpliceModelToken(clientBody, proof.ModelSpan, candidateModel)
	if err != nil {
		t.Fatalf("SpliceModelToken failed: %v", err)
	}
	defer func() { _ = spliceReader.Close() }()
	rewrittenLen := spliceReader.RewrittenLength()

	pool, err := credpool.New([]credpool.Credential{{ID: "k1", Secret: "sk-backend-secret"}})
	if err != nil {
		t.Fatalf("credpool.New: %v", err)
	}
	prims := openaicompat.WireOpenPrimitives{
		ProviderID:        "test-openai-chat",
		BaseURL:           srv.URL + "/v1",
		Flavor:            openaicompat.FlavorChat,
		Pool:              pool,
		HTTPClient:        httpclient.Standard(),
		RateLimitFallback: 5 * time.Second,
		MaxPending:        50,
	}

	delivery := aipapi.DeliveryModeStreaming
	if !streaming {
		delivery = aipapi.DeliveryModeNonStreaming
	}

	wireReq := largebody.WireOpenRequest{
		WireRequest: largebody.WireRequestFacts{
			ProfileID:       openailegacy.ProfileID,
			Operation:       aipapi.OperationOpenAIChatCompletions,
			Delivery:        delivery,
			BodyMode:        largebody.BodyModeIdentityJSON,
			Rewrite:         proof.Rewrite,
			ClientModel:     proof.ClientModel,
			CandidateModel:  candidateModel,
			MaxOutputTokens: proof.MaxOutputTokens,
		},
		Candidate: routing.AttemptCandidate{
			Primary: routing.Primary{
				Backend: "test-openai-chat",
				Model:   candidateModel,
			},
		},
		Body:          spliceReader,
		ContentLength: rewrittenLen,
		TraceID:       "test-trace-id",
		ALegID:        "test-aleg-id",
		BLegID:        "test-bleg-id",
		Header:        inboundHeaders,
	}

	stream, err := prims.OpenWire(context.Background(), wireReq)
	if err != nil {
		t.Fatalf("OpenWire failed: %v", err)
	}
	defer func() { _ = stream.Close() }()

	for {
		_, err := stream.Recv(context.Background())
		if err != nil {
			break
		}
	}

	return *captured, rewrittenLen
}

func assertChatProviderEffectiveMatch(
	t *testing.T,
	canonical, wire capturedChatProviderRequest,
	expectedModel string,
	expectedRewrittenLength int64,
	streaming bool,
) {
	t.Helper()

	// 1. HTTP Method parity (Requirement 12.7)
	if wire.Method != "POST" {
		t.Fatalf("wire method = %q, want POST", wire.Method)
	}
	if canonical.Method != "POST" {
		t.Fatalf("canonical method = %q, want POST", canonical.Method)
	}

	// 2. Target URL Path and Query parity (Requirement 12.7)
	if wire.Path != "/v1/chat/completions" {
		t.Fatalf("wire path = %q, want /v1/chat/completions", wire.Path)
	}
	if canonical.Path != "/v1/chat/completions" {
		t.Fatalf("canonical path = %q, want /v1/chat/completions", canonical.Path)
	}
	if wire.RawQuery != canonical.RawQuery {
		t.Fatalf("query mismatch: wire = %q, canonical = %q", wire.RawQuery, canonical.RawQuery)
	}

	// 3. Relevant Headers: Content-Type, Accept, Authorization (Requirement 12.7)
	if got := wire.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("wire Content-Type = %q, want application/json", got)
	}
	if got := canonical.Header.Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("canonical Content-Type = %q, want application/json", got)
	}

	wantWireAccept := "application/json"
	if streaming {
		wantWireAccept = "text/event-stream"
	}
	if got := wire.Header.Get("Accept"); got != wantWireAccept {
		t.Fatalf("wire Accept = %q, want %q", got, wantWireAccept)
	}

	if got := wire.Header.Get("Authorization"); got != "Bearer sk-backend-secret" {
		t.Fatalf("wire Authorization = %q, want Bearer sk-backend-secret", got)
	}
	if got := canonical.Header.Get("Authorization"); got != "Bearer sk-backend-secret" {
		t.Fatalf("canonical Authorization = %q, want Bearer sk-backend-secret", got)
	}

	// 4. Stale framing headers & length assertions (Requirement 12.2, 12.4)
	if expectedRewrittenLength >= 0 && wire.ContentLength != expectedRewrittenLength {
		t.Fatalf("wire ContentLength = %d, want exact rewritten length %d", wire.ContentLength, expectedRewrittenLength)
	}
	if got := wire.Header.Get("Transfer-Encoding"); got != "" {
		t.Fatalf("wire leaked Transfer-Encoding: %q", got)
	}
	if got := wire.Header.Get("Content-Encoding"); got != "" {
		t.Fatalf("wire leaked Content-Encoding: %q", got)
	}
	if got := wire.Header.Get("Expect"); got != "" {
		t.Fatalf("wire leaked Expect: %q", got)
	}
	if got := wire.Header.Get("Trailer"); got != "" {
		t.Fatalf("wire leaked Trailer header: %q", got)
	}
	if len(wire.Trailer) != 0 {
		t.Fatalf("wire request has %d trailers, want 0", len(wire.Trailer))
	}

	// 5. Parsed JSON semantics after candidate model rewrite (Requirement 9, 17.9)
	if wire.ParsedJSON == nil {
		t.Fatal("wire parsed JSON is nil")
	}
	if canonical.ParsedJSON == nil {
		t.Fatal("canonical parsed JSON is nil")
	}

	if got := wire.ParsedJSON["model"]; got != expectedModel {
		t.Fatalf("wire JSON model = %v, want %q", got, expectedModel)
	}
	if got := canonical.ParsedJSON["model"]; got != expectedModel {
		t.Fatalf("canonical JSON model = %v, want %q", got, expectedModel)
	}

	compareChatSemantics(t, canonical.ParsedJSON, wire.ParsedJSON)
}

func compareChatSemantics(t *testing.T, canonical, wire map[string]any) {
	t.Helper()

	// Messages semantics
	canonMsgs, ok1 := canonical["messages"].([]any)
	wireMsgs, ok2 := wire["messages"].([]any)
	if !ok1 || !ok2 {
		t.Fatalf("messages must be array: canon=%T, wire=%T", canonical["messages"], wire["messages"])
	}
	if len(canonMsgs) != len(wireMsgs) {
		t.Fatalf("messages count mismatch: canon=%d, wire=%d", len(canonMsgs), len(wireMsgs))
	}
	for i := range canonMsgs {
		cm, okC := canonMsgs[i].(map[string]any)
		wm, okW := wireMsgs[i].(map[string]any)
		if !okC || !okW {
			t.Fatalf("message[%d] must be map: canon=%T, wire=%T", i, canonMsgs[i], wireMsgs[i])
		}
		if cm["role"] != wm["role"] {
			t.Fatalf("message[%d] role mismatch: canon=%v, wire=%v", i, cm["role"], wm["role"])
		}
		if cm["content"] != nil || wm["content"] != nil {
			cStr := ""
			if cm["content"] != nil {
				cStr = fmt.Sprintf("%v", cm["content"])
			}
			wStr := ""
			if wm["content"] != nil {
				wStr = fmt.Sprintf("%v", wm["content"])
			}
			if cStr != wStr {
				t.Fatalf("message[%d] content mismatch: canon=%v, wire=%v", i, cm["content"], wm["content"])
			}
		}
		if cm["tool_call_id"] != nil || wm["tool_call_id"] != nil {
			if cm["tool_call_id"] != wm["tool_call_id"] {
				t.Fatalf("message[%d] tool_call_id mismatch: canon=%v, wire=%v", i, cm["tool_call_id"], wm["tool_call_id"])
			}
		}
		if cm["tool_calls"] != nil || wm["tool_calls"] != nil {
			cTC, _ := cm["tool_calls"].([]any)
			wTC, _ := wm["tool_calls"].([]any)
			if len(cTC) != len(wTC) {
				t.Fatalf("message[%d] tool_calls count mismatch: canon=%d, wire=%d", i, len(cTC), len(wTC))
			}
			for j := range cTC {
				cCall, _ := cTC[j].(map[string]any)
				wCall, _ := wTC[j].(map[string]any)
				if cCall["id"] != wCall["id"] || cCall["type"] != wCall["type"] {
					t.Fatalf("message[%d].tool_calls[%d] mismatch: canon=%v, wire=%v", i, j, cCall, wCall)
				}
				cFn, _ := cCall["function"].(map[string]any)
				wFn, _ := wCall["function"].(map[string]any)
				if cFn["name"] != wFn["name"] || cFn["arguments"] != wFn["arguments"] {
					t.Fatalf("message[%d].tool_calls[%d].function mismatch: canon=%v, wire=%v", i, j, cFn, wFn)
				}
			}
		}
	}

	// Temperature
	if canonTemp, ok := canonical["temperature"]; ok {
		if wireTemp, ok := wire["temperature"]; !ok || fmt.Sprintf("%v", canonTemp) != fmt.Sprintf("%v", wireTemp) {
			t.Fatalf("temperature mismatch: canonical=%v, wire=%v", canonTemp, wireTemp)
		}
	}

	// TopP
	if canonTopP, ok := canonical["top_p"]; ok {
		if wireTopP, ok := wire["top_p"]; !ok || fmt.Sprintf("%v", canonTopP) != fmt.Sprintf("%v", wireTopP) {
			t.Fatalf("top_p mismatch: canonical=%v, wire=%v", canonTopP, wireTopP)
		}
	}

	// MaxTokens
	if canonMax, ok := canonical["max_tokens"]; ok {
		if wireMax, ok := wire["max_tokens"]; !ok || fmt.Sprintf("%v", canonMax) != fmt.Sprintf("%v", wireMax) {
			t.Fatalf("max_tokens mismatch: canonical=%v, wire=%v", canonMax, wireMax)
		}
	}

	// ParallelToolCalls
	if canonPTC, ok := canonical["parallel_tool_calls"]; ok {
		if wirePTC, ok := wire["parallel_tool_calls"]; !ok || canonPTC != wirePTC {
			t.Fatalf("parallel_tool_calls mismatch: canonical=%v, wire=%v", canonPTC, wirePTC)
		}
	}

	// Tools
	if canonTools, ok := canonical["tools"]; ok {
		wireTools, ok := wire["tools"]
		if !ok {
			t.Fatalf("canonical has tools but wire does not")
		}
		compareChatTools(t, canonTools, wireTools)
	} else if _, ok := wire["tools"]; ok {
		t.Fatalf("wire has tools but canonical does not")
	}

	// ToolChoice
	if canonTC, ok := canonical["tool_choice"]; ok {
		wireTC, ok := wire["tool_choice"]
		if !ok {
			if canonTC != "auto" {
				t.Fatalf("canonical has tool_choice=%v but wire omitted it", canonTC)
			}
		} else if !reflect.DeepEqual(canonTC, wireTC) {
			t.Fatalf("tool_choice mismatch: canonical=%v, wire=%v", canonTC, wireTC)
		}
	} else if wireTC, ok := wire["tool_choice"]; ok {
		if wireTC != "auto" {
			t.Fatalf("wire has tool_choice=%v but canonical omitted it", wireTC)
		}
	}
}

func compareChatTools(t *testing.T, canonTools, wireTools any) {
	t.Helper()
	cSlice, ok1 := canonTools.([]any)
	wSlice, ok2 := wireTools.([]any)
	if !ok1 || !ok2 {
		t.Fatalf("tools must be array: canon=%T, wire=%T", canonTools, wireTools)
	}
	if len(cSlice) != len(wSlice) {
		t.Fatalf("tools length mismatch: canon=%d, wire=%d", len(cSlice), len(wSlice))
	}
	for i := range cSlice {
		cTool, _ := cSlice[i].(map[string]any)
		wTool, _ := wSlice[i].(map[string]any)
		if cTool["type"] != wTool["type"] {
			t.Fatalf("tool[%d] type mismatch: canon=%v, wire=%v", i, cTool["type"], wTool["type"])
		}
		cFn, _ := cTool["function"].(map[string]any)
		wFn, _ := wTool["function"].(map[string]any)
		if cFn["name"] != wFn["name"] {
			t.Fatalf("tool[%d] name mismatch: canon=%v, wire=%v", i, cFn["name"], wFn["name"])
		}
		if cFn["description"] != nil && cFn["description"] != wFn["description"] {
			t.Fatalf("tool[%d] description mismatch: canon=%v, wire=%v", i, cFn["description"], wFn["description"])
		}
	}
}

// Requirement 17: Differential corpus testing exact JSON semantics across diverse request shapes.
func TestChatProviderDifferential_ExactJSONSemantics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		body           string
		candidateModel string
		streaming      bool
		headers        http.Header
	}{
		{
			name: "SimpleUserMessageStreaming",
			body: `{
				"model": "gpt-4o",
				"messages": [{"role": "user", "content": "Hello, world!"}],
				"stream": true
			}`,
			candidateModel: "gpt-4o-mini",
			streaming:      true,
		},
		{
			name: "SystemAndUserMessagesStreaming",
			body: `{
				"model": "gpt-4o",
				"messages": [
					{"role": "system", "content": "You are a helpful assistant."},
					{"role": "user", "content": "Explain differential testing."}
				],
				"stream": true
			}`,
			candidateModel: "gpt-4o",
			streaming:      true,
		},
		{
			name: "SamplingParametersWithModelRewrite",
			body: `{
				"model": "gpt-4o-2024-05-13",
				"messages": [{"role": "user", "content": "Tell me a joke."}],
				"temperature": 0.7,
				"top_p": 0.9,
				"max_tokens": 150,
				"stream": true
			}`,
			candidateModel: "gpt-4o-mini",
			streaming:      true,
		},
		{
			name: "ToolsAndToolChoiceAuto",
			body: `{
				"model": "gpt-4o",
				"messages": [{"role": "user", "content": "What is the weather?"}],
				"tools": [
					{
						"type": "function",
						"function": {
							"name": "get_weather",
							"description": "Get current weather for location",
							"parameters": {
								"type": "object",
								"properties": {
									"location": {"type": "string"}
								},
								"required": ["location"]
							}
						}
					}
				],
				"tool_choice": "auto",
				"parallel_tool_calls": true,
				"stream": true
			}`,
			candidateModel: "gpt-4o",
			streaming:      true,
		},
		{
			name: "ToolCallHistoryAndResponse",
			body: `{
				"model": "gpt-4o",
				"messages": [
					{"role": "user", "content": "What is 2+2?"},
					{
						"role": "assistant",
						"content": null,
						"tool_calls": [
							{
								"id": "call_calc123",
								"type": "function",
								"function": {
									"name": "calc",
									"arguments": "{\"expr\":\"2+2\"}"
								}
							}
						]
					},
					{
						"role": "tool",
						"tool_call_id": "call_calc123",
						"content": "4"
					}
				],
				"stream": true
			}`,
			candidateModel: "gpt-4o-mini",
			streaming:      true,
		},
		{
			name: "StreamOptionsWithModelRewrite",
			body: `{
				"model": "gpt-4o",
				"messages": [{"role": "user", "content": "Hi"}],
				"stream": true,
				"stream_options": {"include_usage": true}
			}`,
			candidateModel: "gpt-4o-mini",
			streaming:      true,
		},
		{
			name: "OpenRouterHeadersPreserved",
			body: `{
				"model": "gpt-4o",
				"messages": [{"role": "user", "content": "Header test"}],
				"stream": true
			}`,
			candidateModel: "gpt-4o",
			streaming:      true,
			headers: http.Header{
				"HTTP-Referer": []string{"https://mychatapp.com"},
				"X-Title":      []string{"MyChatApp"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rawBody := []byte(tc.body)
			canonical := executeChatCanonicalFlow(t, rawBody, tc.headers, tc.candidateModel, tc.streaming)
			wire, rewrittenLen := executeChatWireFlow(t, rawBody, tc.headers, tc.candidateModel, tc.streaming)

			assertChatProviderEffectiveMatch(t, canonical, wire, tc.candidateModel, rewrittenLen, tc.streaming)

			// If OpenRouter headers were passed, verify they appear on the captured requests
			if tc.headers != nil {
				if ref := tc.headers.Get("HTTP-Referer"); ref != "" {
					if got := wire.Header.Get("HTTP-Referer"); got != ref {
						t.Errorf("wire HTTP-Referer = %q, want %q", got, ref)
					}
				}
				if title := tc.headers.Get("X-Title"); title != "" {
					if got := wire.Header.Get("X-Title"); got != title {
						t.Errorf("wire X-Title = %q, want %q", got, title)
					}
				}
			}
		})
	}
}

// Requirement 9, 17: Escaped and complex model values rewrite parity.
func TestChatProviderDifferential_EscapedModelValues(t *testing.T) {
	t.Parallel()

	escapedCases := []struct {
		name           string
		body           string
		candidateModel string
	}{
		{
			name: "EscapedQuotesInModel",
			body: `{
				"model": "gpt-4o-\"special\"",
				"messages": [{"role": "user", "content": "Escaped test"}],
				"stream": true
			}`,
			candidateModel: "gpt-4o-rewritten",
		},
		{
			name: "UnicodeEscapesInModel",
			body: `{
				"model": "gpt-4o-\u0031\u0032\u0033",
				"messages": [{"role": "user", "content": "Unicode test"}],
				"stream": true
			}`,
			candidateModel: "gpt-4o-mini",
		},
		{
			name: "EscapedBackslashInModel",
			body: `{
				"model": "gpt-4o\\special",
				"messages": [{"role": "user", "content": "Backslash test"}],
				"stream": true
			}`,
			candidateModel: "gpt-4o-final",
		},
	}

	for _, tc := range escapedCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rawBody := []byte(tc.body)
			canonical := executeChatCanonicalFlow(t, rawBody, nil, tc.candidateModel, true)
			wire, rewrittenLen := executeChatWireFlow(t, rawBody, nil, tc.candidateModel, true)

			assertChatProviderEffectiveMatch(t, canonical, wire, tc.candidateModel, rewrittenLen, true)
		})
	}
}

// Requirement 4, 9, 17: Late-positioned model key in JSON stream.
func TestChatProviderDifferential_LatePositionedModel(t *testing.T) {
	t.Parallel()

	lateModelBody := `{
		"messages": [
			{"role": "system", "content": "You are a helpful assistant."},
			{"role": "user", "content": "Testing late model position in JSON."}
		],
		"temperature": 0.5,
		"stream": true,
		"model": "gpt-4o"
	}`

	rawBody := []byte(lateModelBody)
	candidateModel := "gpt-4o-mini"

	canonical := executeChatCanonicalFlow(t, rawBody, nil, candidateModel, true)
	wire, rewrittenLen := executeChatWireFlow(t, rawBody, nil, candidateModel, true)

	assertChatProviderEffectiveMatch(t, canonical, wire, candidateModel, rewrittenLen, true)
}

// Requirement 12.2, 12.4: No stale framing headers and exact content length matching body.
func TestChatProviderDifferential_NoStaleFramingHeadersAndExactLength(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"model": "gpt-4o",
		"messages": [{"role": "user", "content": "Framing test"}],
		"stream": true
	}`)
	candidateModel := "gpt-4o-mini-2024-07-18"

	inboundHeaders := http.Header{
		"Transfer-Encoding": []string{"chunked"},
		"Content-Encoding":  []string{"gzip"},
		"Expect":            []string{"100-continue"},
		"Trailer":           []string{"X-Custom-Trailer"},
	}

	wire, rewrittenLen := executeChatWireFlow(t, body, inboundHeaders, candidateModel, true)

	if wire.ContentLength != rewrittenLen {
		t.Fatalf("wire ContentLength = %d, want exact rewritten length %d", wire.ContentLength, rewrittenLen)
	}
	if int64(len(wire.Body)) != rewrittenLen {
		t.Fatalf("actual body bytes len = %d, want %d", len(wire.Body), rewrittenLen)
	}
	for _, forbidden := range []string{"Transfer-Encoding", "Content-Encoding", "Expect", "Trailer"} {
		if got := wire.Header.Get(forbidden); got != "" {
			t.Errorf("wire leaked forbidden framing header %q: %q", forbidden, got)
		}
	}
}

// Requirement 9: Model rewrite with shorter and longer replacement lengths.
func TestChatProviderDifferential_ModelRewriteCheckedLength(t *testing.T) {
	t.Parallel()

	t.Run("ShortenModelLength", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"model":"gpt-4o-extended-super-long-model-name","messages":[{"role":"user","content":"shorten"}],"stream":true}`)
		candidate := "gpt-4o"
		canonical := executeChatCanonicalFlow(t, body, nil, candidate, true)
		wire, rewrittenLen := executeChatWireFlow(t, body, nil, candidate, true)

		assertChatProviderEffectiveMatch(t, canonical, wire, candidate, rewrittenLen, true)
		if wire.ContentLength >= int64(len(body)) {
			t.Errorf("expected shortened rewritten length < original body (%d), got %d", len(body), wire.ContentLength)
		}
	})

	t.Run("LengthenModelLength", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"lengthen"}],"stream":true}`)
		candidate := "gpt-4o-extended-super-long-model-name"
		canonical := executeChatCanonicalFlow(t, body, nil, candidate, true)
		wire, rewrittenLen := executeChatWireFlow(t, body, nil, candidate, true)

		assertChatProviderEffectiveMatch(t, canonical, wire, candidate, rewrittenLen, true)
		if wire.ContentLength <= int64(len(body)) {
			t.Errorf("expected lengthened rewritten length > original body (%d), got %d", len(body), wire.ContentLength)
		}
	})
}

// Reviewer carry (b): OpenRouter headers (HTTP-Referer / X-Title) in identity differential.
// Prove both canonical DecodeChatRequest and wire CompileProof capture them and produce identical identity digests.
func TestChatProviderDifferential_IdentityDifferential_OpenRouterHeaders(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"model": "gpt-4o",
		"messages": [{"role": "user", "content": "Identity parity with OpenRouter headers"}],
		"temperature": 0.5,
		"top_p": 0.8
	}`)

	headers := make(http.Header)
	headers.Set("HTTP-Referer", "https://chat.example.com")
	headers.Set("X-Title", "OpenRouter Differential Test")

	// Canonical decode
	decoded, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{
		RouteSelector: "test-compat:gpt-4o",
		Headers:       headers,
	})
	if err != nil {
		t.Fatalf("canonical DecodeChatRequest failed: %v", err)
	}
	canonDigest := largebody.CanonicalCallIdentity(decoded.Call)

	// Wire CompileProof
	prof := openailegacy.NewProfile()
	proofIn := frontendpipe.ProofInput{
		Headers:              headers,
		URLPath:              "/v1/chat/completions",
		RouteSelector:        "test-compat:gpt-4o",
		RoutePrefixes:        routeselect.NewPrefixSet([]string{"test-compat"}),
		DefaultRouteSelector: "test-compat:default",
		RouteFromBodyModel:   true,
		Source:               newMemSource(body),
		BodyBytes:            int64(len(body)),
	}
	proofOut, err := prof.CompileProof(context.Background(), proofIn)
	if err != nil {
		t.Fatalf("CompileProof failed: %v", err)
	}
	wireDigest := proofOut.State.Proof.Identity

	if wireDigest != canonDigest {
		t.Fatalf("OpenRouter headers identity mismatch:\n  wire:      %s\n  canonical: %s", wireDigest, canonDigest)
	}
}

// Reviewer carry (a): X-AIP-Session-Hint identity parity vs decline (Req 16.2/16.7).
// Canonical pre-core decode never populates Call.Session.ClientSessionID from X-AIP-Session-Hint.
// Therefore, to prevent identity divergence between canonical pre-core Call and wire proof,
// requests carrying a client session hint must decline to canonical processing (Req 16.7).
// Conversely, requests with authoritative session IDs (X-AIP-Session-ID, X-AIP-A-Leg-ID) maintain exact identity parity.
func TestChatProviderDifferential_IdentityDifferential_SessionHint(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"model": "gpt-4o",
		"messages": [{"role": "user", "content": "Session identity check"}]
	}`)

	t.Run("SessionHintDeclinesToCanonical", func(t *testing.T) {
		t.Parallel()
		headers := make(http.Header)
		headers.Set("X-AIP-Session-Hint", "client-hint-xyz")

		prof := openailegacy.NewProfile()
		proofIn := frontendpipe.ProofInput{
			Headers:              headers,
			URLPath:              "/v1/chat/completions",
			RouteSelector:        "test-compat:gpt-4o",
			RoutePrefixes:        routeselect.NewPrefixSet([]string{"test-compat"}),
			DefaultRouteSelector: "test-compat:default",
			RouteFromBodyModel:   true,
			Source:               newMemSource(body),
			BodyBytes:            int64(len(body)),
		}
		_, err := prof.CompileProof(context.Background(), proofIn)
		if err == nil {
			t.Fatal("expected CompileProof to decline request carrying X-AIP-Session-Hint, got nil error")
		}
		if !strings.Contains(err.Error(), "session hint requires canonical decode") {
			t.Fatalf("expected error mentioning session hint decline, got: %v", err)
		}
	})

	t.Run("AuthoritativeSessionMaintainsExactParity", func(t *testing.T) {
		t.Parallel()
		headers := make(http.Header)
		headers.Set(sessionwire.HeaderAuthoritativeSessionID, "sess_auth_456")
		headers.Set(sessionwire.HeaderALegID, "aleg_auth_789")

		decoded, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{
			RouteSelector: "test-compat:gpt-4o",
			Headers:       headers,
		})
		if err != nil {
			t.Fatalf("DecodeChatRequest failed: %v", err)
		}
		canonDigest := largebody.CanonicalCallIdentity(decoded.Call)

		prof := openailegacy.NewProfile()
		proofIn := frontendpipe.ProofInput{
			Headers:              headers,
			URLPath:              "/v1/chat/completions",
			RouteSelector:        "test-compat:gpt-4o",
			RoutePrefixes:        routeselect.NewPrefixSet([]string{"test-compat"}),
			DefaultRouteSelector: "test-compat:default",
			RouteFromBodyModel:   true,
			Source:               newMemSource(body),
			BodyBytes:            int64(len(body)),
		}
		proofOut, err := prof.CompileProof(context.Background(), proofIn)
		if err != nil {
			t.Fatalf("CompileProof failed: %v", err)
		}
		wireDigest := proofOut.State.Proof.Identity

		if wireDigest != canonDigest {
			t.Fatalf("Authoritative session identity mismatch:\n  wire:      %s\n  canonical: %s", wireDigest, canonDigest)
		}
	})
}
