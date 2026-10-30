package openaicompat_test

import (
	"bytes"
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
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openresponsescompat"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/routeselect"
	proto "github.com/aiproxer/aiproxer/internal/plugins/protocols/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Task 17.3: Provider-effective JSON differential tests for Lane 3:
// OpenResponses HTTP create frontend -> OpenResponses-compatible backend.
// Requirements: 17, 18.

type capturedOpenResponsesProviderRequest struct {
	Method        string
	Path          string
	RawQuery      string
	Header        http.Header
	ContentLength int64
	Trailer       http.Header
	Body          []byte
	ParsedJSON    map[string]any
}

func startOpenResponsesCaptureServer(t *testing.T) (*httptest.Server, *capturedOpenResponsesProviderRequest) {
	t.Helper()
	var captured capturedOpenResponsesProviderRequest
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
			_, _ = io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_diff_001\",\"model\":\"gpt-4o\",\"status\":\"in_progress\"}}\n\n")
			_, _ = io.WriteString(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n")
			_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_diff_001\",\"model\":\"gpt-4o\",\"status\":\"completed\"}}\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		} else {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"id":"resp_diff_001","object":"response","status":"completed","output":[]}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &captured
}

type authInjectTransport struct {
	token string
	base  http.RoundTripper
}

func (t *authInjectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get("Authorization") == "" && t.token != "" {
		req.Header.Set("Authorization", "Bearer "+t.token)
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

func executeOpenResponsesCanonicalFlow(
	t *testing.T,
	clientBody []byte,
	extraHeaders http.Header,
	candidateModel string,
	streaming bool,
) capturedOpenResponsesProviderRequest {
	t.Helper()
	srv, captured := startOpenResponsesCaptureServer(t)

	opts := openresponses.DecodeCreateOptions{
		RouteSelector: "test-compat-openresponses:" + candidateModel,
		Headers:       extraHeaders,
		Limits:        proto.DefaultLimits(),
	}
	decoded, err := openresponses.AuthenticateAndDecodeCreate(context.Background(), clientBody, opts)
	if err != nil {
		t.Fatalf("canonical AuthenticateAndDecodeCreate failed: %v", err)
	}

	cli := &http.Client{
		Transport: &authInjectTransport{
			token: "sk-backend-secret",
			base:  http.DefaultTransport,
		},
	}

	spec := openresponsescompat.BackendSpec{
		ID:         "test-compat-openresponses",
		BaseURL:    srv.URL + "/openresponses/v1",
		HTTPClient: cli,
		Caps: aipapi.NewBackendCaps(
			aipapi.CapabilityStreaming,
			aipapi.CapabilityTools,
			aipapi.CapabilityVision,
			aipapi.CapabilityDocuments,
			aipapi.CapabilityReasoning,
			aipapi.CapabilityParallelToolCalls,
			aipapi.CapabilityOrderedItems,
			aipapi.CapabilityAssistantPhase,
			aipapi.CapabilityItemReferences,
			aipapi.CapabilityCompaction,
			aipapi.CapabilityOpaqueExtensions,
		),
		DialectSupport: aipapi.NormalizeDialectSupport(aipapi.DialectSupport{
			ItemDialects: []aipapi.DialectRequirement{
				{Kind: "item", Dialect: openresponsescompat.DefaultItemDialect},
				{Kind: "item", Dialect: "item_reference"},
			},
			CompactionDialects: []aipapi.DialectRequirement{
				{Kind: "compaction", Dialect: openresponsescompat.DefaultCompactionDialect},
			},
		}),
	}
	be := openresponsescompat.NewBackend(spec)

	cand := routing.AttemptCandidate{
		Primary: routing.Primary{
			Backend: "test-compat-openresponses",
			Model:   candidateModel,
		},
	}

	call := *decoded.Call
	if streaming {
		call.Invocation.TransportMode = aipapi.TransportModeStreaming
		call.Invocation.DeliveryMode = aipapi.DeliveryModeStreaming
	} else {
		call.Invocation.TransportMode = aipapi.TransportModeNonStreaming
		call.Invocation.DeliveryMode = aipapi.DeliveryModeNonStreaming
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

func executeOpenResponsesWireFlow(
	t *testing.T,
	clientBody []byte,
	inboundHeaders http.Header,
	candidateModel string,
	streaming bool,
) (capturedOpenResponsesProviderRequest, int64) {
	t.Helper()
	srv, captured := startOpenResponsesCaptureServer(t)

	prof := openresponses.NewProfile()
	proofIn := frontendpipe.ProofInput{
		Ctx:                  context.Background(),
		Headers:              inboundHeaders,
		URLPath:              "/openresponses/v1/responses",
		RouteSelector:        "test-compat-openresponses:" + candidateModel,
		RoutePrefixes:        routeselect.NewPrefixSet([]string{"test-compat-openresponses"}),
		DefaultRouteSelector: "test-compat-openresponses:default",
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
		ProviderID:        "test-compat-openresponses",
		BaseURL:           srv.URL + "/openresponses/v1",
		Flavor:            openaicompat.FlavorResponses,
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
			ProfileID:       openresponses.ProfileID,
			Operation:       aipapi.OperationOpenResponsesCreate,
			Delivery:        delivery,
			BodyMode:        largebody.BodyModeIdentityJSON,
			Rewrite:         proof.Rewrite,
			ClientModel:     proof.ClientModel,
			CandidateModel:  candidateModel,
			MaxOutputTokens: proof.MaxOutputTokens,
		},
		Candidate: routing.AttemptCandidate{
			Primary: routing.Primary{
				Backend: "test-compat-openresponses",
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

func assertOpenResponsesProviderEffectiveMatch(
	t *testing.T,
	canonical, wire capturedOpenResponsesProviderRequest,
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
	if wire.Path != canonical.Path {
		t.Fatalf("path mismatch: wire = %q, canonical = %q", wire.Path, canonical.Path)
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
	if got := canonical.Header.Get("Accept"); got != "application/json" && got != "text/event-stream" {
		t.Fatalf("canonical Accept = %q, want application/json or text/event-stream", got)
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

	// 5. Parsed JSON semantics after candidate model rewrite (Requirement 9, 17.4, 17.9)
	if wire.ParsedJSON == nil {
		t.Fatal("wire parsed JSON is nil")
	}
	if canonical.ParsedJSON == nil {
		t.Fatal("canonical parsed JSON is nil")
	}

	// Both paths must reflect the exact rewritten model name
	if got := wire.ParsedJSON["model"]; got != expectedModel {
		t.Fatalf("wire JSON model = %v, want %q", got, expectedModel)
	}
	if got := canonical.ParsedJSON["model"]; got != expectedModel {
		t.Fatalf("canonical JSON model = %v, want %q", got, expectedModel)
	}

	compareOpenResponsesSemantics(t, canonical, wire, expectedModel)
}

func jsonEqual(a, b []byte) bool {
	ta := bytes.TrimSpace(a)
	tb := bytes.TrimSpace(b)
	if len(ta) == 0 && len(tb) == 0 {
		return true
	}
	var va, vb any
	if err := json.Unmarshal(ta, &va); err != nil {
		return bytes.Equal(ta, tb)
	}
	if err := json.Unmarshal(tb, &vb); err != nil {
		return bytes.Equal(ta, tb)
	}
	return reflect.DeepEqual(va, vb)
}

func compareOpenResponsesSemantics(t *testing.T, canonical, wire capturedOpenResponsesProviderRequest, expectedModel string) {
	t.Helper()

	// Requirement 17.4: Wire JSON must explicitly have store: false
	if storeVal, ok := wire.ParsedJSON["store"]; ok {
		if storeVal != false {
			t.Fatalf("wire store must be false, got %v", storeVal)
		}
	} else {
		t.Fatal("wire JSON must include store: false")
	}

	// Temperature
	if canonTemp, ok := canonical.ParsedJSON["temperature"]; ok {
		if wireTemp, ok := wire.ParsedJSON["temperature"]; !ok || fmt.Sprintf("%v", canonTemp) != fmt.Sprintf("%v", wireTemp) {
			t.Fatalf("temperature mismatch: canonical=%v, wire=%v", canonTemp, wireTemp)
		}
	}

	// TopP
	if canonTopP, ok := canonical.ParsedJSON["top_p"]; ok {
		if wireTopP, ok := wire.ParsedJSON["top_p"]; !ok || fmt.Sprintf("%v", canonTopP) != fmt.Sprintf("%v", wireTopP) {
			t.Fatalf("top_p mismatch: canonical=%v, wire=%v", canonTopP, wireTopP)
		}
	}

	// MaxOutputTokens
	if canonMax, ok := canonical.ParsedJSON["max_output_tokens"]; ok {
		if wireMax, ok := wire.ParsedJSON["max_output_tokens"]; !ok || fmt.Sprintf("%v", canonMax) != fmt.Sprintf("%v", wireMax) {
			t.Fatalf("max_output_tokens mismatch: canonical=%v, wire=%v", canonMax, wireMax)
		}
	}

	// ParallelToolCalls
	if canonPTC, ok := canonical.ParsedJSON["parallel_tool_calls"]; ok {
		if wirePTC, ok := wire.ParsedJSON["parallel_tool_calls"]; !ok || canonPTC != wirePTC {
			t.Fatalf("parallel_tool_calls mismatch: canonical=%v, wire=%v", canonPTC, wirePTC)
		}
	}

	// Requirement 17.9: Decode both provider-effective request bodies via official protocol decoder
	opts := openresponses.DecodeCreateOptions{
		Limits: proto.DefaultLimits(),
	}
	wireDec, err := openresponses.AuthenticateAndDecodeCreate(context.Background(), wire.Body, opts)
	if err != nil {
		t.Fatalf("wire AuthenticateAndDecodeCreate failed: %v", err)
	}
	canonDec, err := openresponses.AuthenticateAndDecodeCreate(context.Background(), canonical.Body, opts)
	if err != nil {
		t.Fatalf("canonical AuthenticateAndDecodeCreate failed: %v", err)
	}

	if wireDec.Model != expectedModel {
		t.Fatalf("wire decoded model = %v, want %q", wireDec.Model, expectedModel)
	}
	if canonDec.Model != expectedModel {
		t.Fatalf("canonical decoded model = %v, want %q", canonDec.Model, expectedModel)
	}

	if wireDec.ExplicitStore == nil || *wireDec.ExplicitStore != false {
		t.Fatalf("wire decoded store = %v, want false", wireDec.ExplicitStore)
	}

	wireCall := *wireDec.Call
	canonCall := *canonDec.Call

	// Compare Tools
	if len(wireCall.Tools) != len(canonCall.Tools) {
		t.Fatalf("tools count mismatch: wire=%d, canon=%d", len(wireCall.Tools), len(canonCall.Tools))
	}
	for i := range wireCall.Tools {
		wt := wireCall.Tools[i]
		ct := canonCall.Tools[i]
		if wt.Name != ct.Name || wt.Description != ct.Description || !jsonEqual(wt.Parameters, ct.Parameters) {
			t.Fatalf("tool[%d] mismatch: wire=%+v, canon=%+v", i, wt, ct)
		}
	}

	// Compare ToolChoice
	if wireCall.ToolChoice.Mode != canonCall.ToolChoice.Mode || wireCall.ToolChoice.Name != canonCall.ToolChoice.Name {
		t.Fatalf("tool choice mismatch: wire=%+v, canon=%+v", wireCall.ToolChoice, canonCall.ToolChoice)
	}

	// Compare Items
	if len(wireCall.Items) != len(canonCall.Items) {
		t.Fatalf("items count mismatch: wire=%d, canon=%d", len(wireCall.Items), len(canonCall.Items))
	}
	for i := range wireCall.Items {
		wi := wireCall.Items[i]
		ci := canonCall.Items[i]
		if wi.Kind != ci.Kind {
			t.Fatalf("item[%d] kind mismatch: wire=%v, canon=%v", i, wi.Kind, ci.Kind)
		}
		if wi.Role != ci.Role {
			t.Fatalf("item[%d] role mismatch: wire=%v, canon=%v", i, wi.Role, ci.Role)
		}
		if len(wi.Content) != len(ci.Content) {
			t.Fatalf("item[%d] content parts count mismatch: wire=%d, canon=%d", i, len(wi.Content), len(ci.Content))
		}
		for j := range wi.Content {
			wPart := wi.Content[j]
			cPart := ci.Content[j]
			if wPart.Kind != cPart.Kind || wPart.Text != cPart.Text {
				t.Fatalf("item[%d] content part[%d] mismatch: wire=%+v, canon=%+v", i, j, wPart, cPart)
			}
		}
		if (wi.ToolCall == nil) != (ci.ToolCall == nil) {
			t.Fatalf("item[%d] tool call presence mismatch", i)
		}
		if wi.ToolCall != nil {
			if wi.ToolCall.CallID != ci.ToolCall.CallID || wi.ToolCall.Name != ci.ToolCall.Name ||
				!bytes.Equal(bytes.TrimSpace(wi.ToolCall.Arguments), bytes.TrimSpace(ci.ToolCall.Arguments)) {
				t.Fatalf("item[%d] tool call mismatch: wire=%+v, canon=%+v", i, wi.ToolCall, ci.ToolCall)
			}
		}
		if (wi.ToolResult == nil) != (ci.ToolResult == nil) {
			t.Fatalf("item[%d] tool result presence mismatch", i)
		}
		if wi.ToolResult != nil {
			if wi.ToolResult.CallID != ci.ToolResult.CallID {
				t.Fatalf("item[%d] tool result call id mismatch: wire=%s, canon=%s", i, wi.ToolResult.CallID, ci.ToolResult.CallID)
			}
		}
	}
}

// Requirement 17: Provider-effective JSON differential tests for Lane 3 (OpenResponses).
func TestOpenResponsesProviderDifferential_ExactJSONSemantics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		body           string
		candidateModel string
		streaming      bool
	}{
		{
			name: "single_user_message_array",
			body: `{
				"model": "gpt-4o",
				"stream": true,
				"store": false,
				"input": [{"type": "message", "role": "user", "content": "Hello, world!"}]
			}`,
			candidateModel: "gpt-4o-candidate-target",
			streaming:      true,
		},
		{
			name: "multi_turn_conversation",
			body: `{
				"model": "gpt-4o-mini",
				"stream": true,
				"store": false,
				"input": [
					{"type": "message", "role": "user", "content": "Hello"},
					{"type": "message", "role": "assistant", "content": "Hi there!"},
					{"type": "message", "role": "user", "content": "How are you?"}
				]
			}`,
			candidateModel: "custom-candidate-model",
			streaming:      true,
		},
		{
			name: "instructions_and_system_prompt",
			body: `{
				"model": "gpt-4o",
				"stream": true,
				"store": false,
				"instructions": "You are a professional Go software engineer.",
				"input": [
					{"type": "message", "role": "user", "content": "Write a unit test."}
				]
			}`,
			candidateModel: "gpt-4o-target",
			streaming:      true,
		},
		{
			name: "generation_options_temperature_topp_maxoutput",
			body: `{
				"model": "gpt-4o",
				"stream": true,
				"store": false,
				"input": [{"type": "message", "role": "user", "content": "Tell me a joke"}],
				"temperature": 0.7,
				"top_p": 0.95,
				"max_output_tokens": 256,
				"parallel_tool_calls": true
			}`,
			candidateModel: "fast-gpt-4o",
			streaming:      true,
		},
		{
			name: "tools_and_tool_choice_auto",
			body: `{
				"model": "gpt-4o",
				"stream": true,
				"store": false,
				"input": [{"type": "message", "role": "user", "content": "What is the weather in Tokyo?"}],
				"tools": [
					{
						"type": "function",
						"name": "get_weather",
						"description": "Get current weather for a city",
						"parameters": {
							"type": "object",
							"properties": {
								"city": {"type": "string"}
							},
							"required": ["city"]
						}
					}
				],
				"tool_choice": "auto"
			}`,
			candidateModel: "tool-capable-model",
			streaming:      true,
		},
		{
			name: "tools_and_tool_choice_required",
			body: `{
				"model": "gpt-4o",
				"stream": true,
				"store": false,
				"input": [{"type": "message", "role": "user", "content": "Look up weather in London"}],
				"tools": [
					{
						"type": "function",
						"name": "get_weather",
						"description": "Get current weather for a city",
						"parameters": {
							"type": "object",
							"properties": {
								"city": {"type": "string"}
							},
							"required": ["city"]
						}
					}
				],
				"tool_choice": "required"
			}`,
			candidateModel: "tool-capable-model-v2",
			streaming:      true,
		},
		{
			name: "function_call_history_and_tool_output",
			body: `{
				"model": "gpt-4o",
				"stream": true,
				"store": false,
				"input": [
					{"type": "message", "role": "user", "content": "Weather in NY?"},
					{
						"type": "function_call",
						"call_id": "call_weather_1",
						"name": "get_weather",
						"arguments": "{\"city\":\"NY\"}"
					},
					{
						"type": "function_call_output",
						"call_id": "call_weather_1",
						"output": "{\"temp\": 72, \"condition\": \"sunny\"}"
					}
				]
			}`,
			candidateModel: "function-model-target",
			streaming:      true,
		},
		{
			name: "shorthand_string_input",
			body: `{
				"model": "gpt-4o",
				"stream": true,
				"store": false,
				"input": "This is a simple direct string prompt."
			}`,
			candidateModel: "string-input-target-model",
			streaming:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			canonical := executeOpenResponsesCanonicalFlow(t, []byte(tt.body), nil, tt.candidateModel, tt.streaming)
			wire, rewrittenLen := executeOpenResponsesWireFlow(t, []byte(tt.body), nil, tt.candidateModel, tt.streaming)
			assertOpenResponsesProviderEffectiveMatch(t, canonical, wire, tt.candidateModel, rewrittenLen, tt.streaming)
		})
	}
}

// Requirement 9, 17: Escaped model values in model token rewrite.
func TestOpenResponsesProviderDifferential_EscapedModelValues(t *testing.T) {
	t.Parallel()

	body := `{
		"model": "client-model",
		"stream": true,
		"store": false,
		"input": "test escaped model replacement"
	}`

	escapedCandidates := []string{
		`model"with"quotes`,
		`model\with\backslashes`,
		`model/with/slashes`,
		"model\nwith\nnewlines",
		"model\twith\ttabs",
		`complex\"model\\name\/test`,
	}

	for i, candModel := range escapedCandidates {
		t.Run(fmt.Sprintf("escape_case_%d", i), func(t *testing.T) {
			t.Parallel()
			canonical := executeOpenResponsesCanonicalFlow(t, []byte(body), nil, candModel, true)
			wire, rewrittenLen := executeOpenResponsesWireFlow(t, []byte(body), nil, candModel, true)
			assertOpenResponsesProviderEffectiveMatch(t, canonical, wire, candModel, rewrittenLen, true)
		})
	}
}

// Requirement 4, 17: Model field positioned late in large request payload.
func TestOpenResponsesProviderDifferential_LatePositionedModel(t *testing.T) {
	t.Parallel()

	padding := strings.Repeat("This is filler content to make the payload large. ", 1000)
	body := fmt.Sprintf(`{
		"instructions": "You are a helpful assistant.",
		"stream": true,
		"store": false,
		"input": %q,
		"temperature": 0.5,
		"model": "gpt-4o"
	}`, padding)

	candidateModel := "target-after-large-input-model"
	canonical := executeOpenResponsesCanonicalFlow(t, []byte(body), nil, candidateModel, true)
	wire, rewrittenLen := executeOpenResponsesWireFlow(t, []byte(body), nil, candidateModel, true)
	assertOpenResponsesProviderEffectiveMatch(t, canonical, wire, candidateModel, rewrittenLen, true)
}

// Requirement 12.2, 12.4: Outbound request headers must strip stale framing.
func TestOpenResponsesProviderDifferential_NoStaleFramingHeadersAndExactLength(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"model": "gpt-4o",
		"stream": true,
		"store": false,
		"input": "Checking framing headers"
	}`)

	headers := make(http.Header)
	headers.Set("Transfer-Encoding", "chunked")
	headers.Set("Content-Encoding", "gzip")
	headers.Set("Expect", "100-continue")
	headers.Set("Trailer", "X-Custom-Trailer")
	headers.Set("X-Custom-Trailer", "trailer-value")
	headers.Set("Authorization", "Bearer client-supplied-token")

	candModel := "rewritten-gpt-4o"
	wire, rewrittenLen := executeOpenResponsesWireFlow(t, body, headers, candModel, true)

	if wire.ContentLength != rewrittenLen {
		t.Fatalf("wire ContentLength = %d, want exact rewritten length %d", wire.ContentLength, rewrittenLen)
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
	if got := wire.Header.Get("Authorization"); got != "Bearer sk-backend-secret" {
		t.Fatalf("wire Authorization = %q, want Bearer sk-backend-secret", got)
	}
}

// Requirement 9, 12.4: Exact rewritten length verification for shorter and longer candidates.
func TestOpenResponsesProviderDifferential_ModelRewriteCheckedLength(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"medium-length-model","stream":true,"store":false,"input":"test"}`)

	t.Run("rewrite_to_shorter_model", func(t *testing.T) {
		t.Parallel()
		candModel := "m"
		wire, rewrittenLen := executeOpenResponsesWireFlow(t, body, nil, candModel, true)
		if wire.ContentLength != rewrittenLen {
			t.Fatalf("wire ContentLength = %d, want %d", wire.ContentLength, rewrittenLen)
		}
		if int64(len(wire.Body)) != rewrittenLen {
			t.Fatalf("wire body bytes len = %d, want %d", len(wire.Body), rewrittenLen)
		}
		if wire.ParsedJSON["model"] != candModel {
			t.Fatalf("wire parsed model = %v, want %q", wire.ParsedJSON["model"], candModel)
		}
	})

	t.Run("rewrite_to_longer_model", func(t *testing.T) {
		t.Parallel()
		candModel := "very-long-expanded-model-identifier-for-rewrite-verification"
		wire, rewrittenLen := executeOpenResponsesWireFlow(t, body, nil, candModel, true)
		if wire.ContentLength != rewrittenLen {
			t.Fatalf("wire ContentLength = %d, want %d", wire.ContentLength, rewrittenLen)
		}
		if int64(len(wire.Body)) != rewrittenLen {
			t.Fatalf("wire body bytes len = %d, want %d", len(wire.Body), rewrittenLen)
		}
		if wire.ParsedJSON["model"] != candModel {
			t.Fatalf("wire parsed model = %v, want %q", wire.ParsedJSON["model"], candModel)
		}
	})
}

// HARD GATE: Requirement 17.4: Missing store, store:true, previous_response_id,
// and compaction shapes MUST decline wire proof and NEVER reach the wire backend.
func TestOpenResponsesProviderDifferential_HardGate_StoreNeverReachesWireBackend(t *testing.T) {
	t.Parallel()

	prof := openresponses.NewProfile()

	t.Run("missing_store_declines_wire_proof", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"model":"gpt-4o","stream":true,"input":"test"}`)
		proofIn := frontendpipe.ProofInput{
			Ctx:           context.Background(),
			URLPath:       "/openresponses/v1/responses",
			RouteSelector: "test:gpt-4o",
			Source:        newMemSource(body),
			BodyBytes:     int64(len(body)),
		}
		_, err := prof.CompileProof(context.Background(), proofIn)
		if err == nil {
			t.Fatal("HARD GATE FAILURE: missing store MUST decline wire proof")
		}
		if !strings.Contains(err.Error(), "store is required and must be explicitly false") {
			t.Fatalf("expected store requirement error, got %v", err)
		}
	})

	t.Run("store_true_declines_wire_proof", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"model":"gpt-4o","stream":true,"store":true,"input":"test"}`)
		proofIn := frontendpipe.ProofInput{
			Ctx:           context.Background(),
			URLPath:       "/openresponses/v1/responses",
			RouteSelector: "test:gpt-4o",
			Source:        newMemSource(body),
			BodyBytes:     int64(len(body)),
		}
		_, err := prof.CompileProof(context.Background(), proofIn)
		if err == nil {
			t.Fatal("HARD GATE FAILURE: store:true MUST decline wire proof")
		}
		if !strings.Contains(err.Error(), "store must be false, got true") {
			t.Fatalf("expected store must be false error, got %v", err)
		}
	})

	t.Run("previous_response_id_declines_wire_proof", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"model":"gpt-4o","stream":true,"store":false,"input":"test","previous_response_id":"resp_123"}`)
		proofIn := frontendpipe.ProofInput{
			Ctx:           context.Background(),
			URLPath:       "/openresponses/v1/responses",
			RouteSelector: "test:gpt-4o",
			Source:        newMemSource(body),
			BodyBytes:     int64(len(body)),
		}
		_, err := prof.CompileProof(context.Background(), proofIn)
		if err == nil {
			t.Fatal("HARD GATE FAILURE: previous_response_id MUST decline wire proof")
		}
		if !strings.Contains(err.Error(), "previous_response_id requires continuation state") {
			t.Fatalf("expected continuation state error, got %v", err)
		}
	})

	t.Run("compaction_path_declines_wire_proof", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"model":"gpt-4o","input":"test"}`)
		proofIn := frontendpipe.ProofInput{
			Ctx:           context.Background(),
			URLPath:       "/openresponses/v1/responses/compact",
			RouteSelector: "test:gpt-4o",
			Source:        newMemSource(body),
			BodyBytes:     int64(len(body)),
		}
		_, err := prof.CompileProof(context.Background(), proofIn)
		if err == nil {
			t.Fatal("HARD GATE FAILURE: compaction path MUST decline wire proof")
		}
	})

	t.Run("unsupported_controls_decline_wire_proof", func(t *testing.T) {
		t.Parallel()
		unsupportedCases := []struct {
			name string
			json string
		}{
			{"truncation", `{"model":"gpt-4o","stream":true,"store":false,"input":"t","truncation":"auto"}`},
			{"background", `{"model":"gpt-4o","stream":true,"store":false,"input":"t","background":true}`},
			{"include", `{"model":"gpt-4o","stream":true,"store":false,"input":"t","include":["input"]}`},
			{"presence_penalty", `{"model":"gpt-4o","stream":true,"store":false,"input":"t","presence_penalty":0.5}`},
			{"frequency_penalty", `{"model":"gpt-4o","stream":true,"store":false,"input":"t","frequency_penalty":0.5}`},
			{"stream_options", `{"model":"gpt-4o","stream":true,"store":false,"input":"t","stream_options":{"include_usage":true}}`},
			{"top_logprobs", `{"model":"gpt-4o","stream":true,"store":false,"input":"t","top_logprobs":2}`},
			{"service_tier", `{"model":"gpt-4o","stream":true,"store":false,"input":"t","service_tier":"default"}`},
			{"safety_identifier", `{"model":"gpt-4o","stream":true,"store":false,"input":"t","safety_identifier":"safe"}`},
			{"prompt_cache_key", `{"model":"gpt-4o","stream":true,"store":false,"input":"t","prompt_cache_key":"key"}`},
			{"prompt_cache_retention", `{"model":"gpt-4o","stream":true,"store":false,"input":"t","prompt_cache_retention":"in_memory"}`},
			{"max_tool_calls", `{"model":"gpt-4o","stream":true,"store":false,"input":"t","max_tool_calls":5}`},
			{"unknown_field", `{"model":"gpt-4o","stream":true,"store":false,"input":"t","unknown_custom_control":123}`},
		}

		for _, tc := range unsupportedCases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				body := []byte(tc.json)
				proofIn := frontendpipe.ProofInput{
					Ctx:           context.Background(),
					URLPath:       "/openresponses/v1/responses",
					RouteSelector: "test:gpt-4o",
					Source:        newMemSource(body),
					BodyBytes:     int64(len(body)),
				}
				_, err := prof.CompileProof(context.Background(), proofIn)
				if err == nil {
					t.Fatalf("HARD GATE FAILURE: %s MUST decline wire proof", tc.name)
				}
			})
		}
	})
}
