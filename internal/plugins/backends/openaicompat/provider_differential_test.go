package openaicompat_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	frontresponses "github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/routeselect"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type memSource struct {
	data []byte
}

func newMemSource(data []byte) *memSource {
	return &memSource{data: data}
}

func (s *memSource) Size() int64 {
	return int64(len(s.data))
}

func (s *memSource) Open() (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(s.data)), nil
}

func (s *memSource) Close() error {
	return nil
}

var _ largebody.Source = (*memSource)(nil)

type capturedProviderRequest struct {
	Method        string
	Path          string
	RawQuery      string
	Header        http.Header
	ContentLength int64
	Trailer       http.Header
	Body          []byte
	ParsedJSON    map[string]any
}

const validResponsesSSE = `event: response.created
data: {"type":"response.created","response":{"id":"resp_123","status":"in_progress"}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"ok"}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_123","status":"completed"}}

`

func startProviderCaptureServer(t *testing.T) (*httptest.Server, *capturedProviderRequest) {
	t.Helper()
	var captured capturedProviderRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.Method = r.Method
		captured.Path = r.URL.Path
		captured.RawQuery = r.URL.RawQuery
		captured.Header = r.Header.Clone()
		captured.ContentLength = r.ContentLength
		captured.Trailer = r.Trailer.Clone()
		t.Logf("srv received: Method=%s, Path=%s, Accept=%s", r.Method, r.URL.Path, r.Header.Get("Accept"))

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		captured.Body = body
		t.Logf("srv body: %s", string(body))

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
			_, _ = io.WriteString(w, validResponsesSSE)
		} else {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"id":"resp-1","object":"response","status":"completed","output":[]}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &captured
}

func executeCanonicalFlow(
	t *testing.T,
	clientBody []byte,
	extraHeaders http.Header,
	candidateModel string,
	streaming bool,
) capturedProviderRequest {
	t.Helper()
	srv, captured := startProviderCaptureServer(t)

	decoded, err := frontresponses.DecodeCreateRequest(clientBody, frontresponses.DecodeOptions{
		RouteSelector: "test-openai-responses:" + candidateModel,
		Headers:       extraHeaders,
	})
	if err != nil {
		t.Fatalf("canonical DecodeCreateRequest failed: %v", err)
	}

	spec := openaicompat.BackendSpec{
		ID:      "test-openai-responses",
		BaseURL: srv.URL + "/v1",
		Flavor:  openaicompat.FlavorResponses,
		ResolveFlavor: func(call aipapi.Call) openaicompat.Flavor {
			return openaicompat.FlavorResponses
		},
		APIKey:     "sk-backend-secret",
		HTTPClient: httpclient.Standard(),
	}
	be := openaicompat.NewBackend(spec)

	cand := routing.AttemptCandidate{
		Primary: routing.Primary{
			Backend: "test-openai-responses",
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

func executeWireFlow(
	t *testing.T,
	clientBody []byte,
	inboundHeaders http.Header,
	candidateModel string,
	streaming bool,
) (capturedProviderRequest, int64) {
	t.Helper()
	srv, captured := startProviderCaptureServer(t)

	prof := frontresponses.NewProfile()
	proofIn := frontendpipe.ProofInput{
		Ctx:                  context.Background(),
		Headers:              inboundHeaders,
		URLPath:              "/v1/responses",
		RouteSelector:        "test-openai-responses:" + candidateModel,
		RoutePrefixes:        routeselect.NewPrefixSet([]string{"test-openai-responses"}),
		DefaultRouteSelector: "test-openai-responses:default",
		RouteFromBodyModel:   true,
		Source:               newMemSource(clientBody),
		BodyBytes:            int64(len(clientBody)),
	}
	proofOut, err := prof.CompileProof(context.Background(), proofIn)
	if err != nil {
		t.Fatalf("CompileProof failed: %v", err)
	}
	proof := proofOut.Proof()

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
		ProviderID:        "test-openai-responses",
		BaseURL:           srv.URL + "/v1",
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
			ProfileID:       frontresponses.ProfileID,
			Operation:       aipapi.OperationOpenAIResponses,
			Delivery:        delivery,
			BodyMode:        largebody.BodyModeIdentityJSON,
			Rewrite:         proof.Rewrite,
			ClientModel:     proof.ClientModel,
			CandidateModel:  candidateModel,
			MaxOutputTokens: proof.MaxOutputTokens,
		},
		Candidate: routing.AttemptCandidate{
			Primary: routing.Primary{
				Backend: "test-openai-responses",
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

// assertProviderEffectiveMatch asserts protocol-effective parity between canonical and wire requests (Requirements 9, 12, 17).
func assertProviderEffectiveMatch(
	t *testing.T,
	canonical, wire capturedProviderRequest,
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
	// Note: OpenAI Go SDK defaults canonical Accept to application/json, while wire uses text/event-stream for streaming (Requirement 12, 17.9).
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

	// 5. Parsed JSON semantics after candidate model rewrite (Requirement 9, 17.9)
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

	// Compare standard protocol fields
	compareSemantics(t, canonical.ParsedJSON, wire.ParsedJSON)
}

func compareSemantics(t *testing.T, canonical, wire map[string]any) {
	t.Helper()

	// Instructions
	if canonInst, ok := canonical["instructions"]; ok {
		if wireInst, ok := wire["instructions"]; !ok || !reflect.DeepEqual(canonInst, wireInst) {
			t.Fatalf("instructions mismatch: canonical=%v, wire=%v", canonInst, wireInst)
		}
	} else if _, ok := wire["instructions"]; ok {
		t.Fatalf("wire has instructions but canonical does not")
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

	// MaxOutputTokens
	if canonMax, ok := canonical["max_output_tokens"]; ok {
		if wireMax, ok := wire["max_output_tokens"]; !ok || fmt.Sprintf("%v", canonMax) != fmt.Sprintf("%v", wireMax) {
			t.Fatalf("max_output_tokens mismatch: canonical=%v, wire=%v", canonMax, wireMax)
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
		compareToolsSemantics(t, canonTools, wireTools)
	} else if _, ok := wire["tools"]; ok {
		t.Fatalf("wire has tools but canonical does not")
	}

	// ToolChoice
	if canonTC, ok := canonical["tool_choice"]; ok {
		wireTC, ok := wire["tool_choice"]
		if !ok {
			// In OpenAI Responses API, tool_choice defaults to "auto" when tools are present and tool_choice is omitted.
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

	// Input semantics: compare normalized input items
	compareInputSemantics(t, canonical["input"], wire["input"])
}

func compareToolsSemantics(t *testing.T, canonTools, wireTools any) {
	t.Helper()
	canonSlice, ok1 := canonTools.([]any)
	wireSlice, ok2 := wireTools.([]any)
	if !ok1 || !ok2 {
		t.Fatalf("tools must be array: canon=%T, wire=%T", canonTools, wireTools)
	}
	if len(canonSlice) != len(wireSlice) {
		t.Fatalf("tools length mismatch: canon=%d, wire=%d", len(canonSlice), len(wireSlice))
	}
	for i := range canonSlice {
		cm, ok1 := canonSlice[i].(map[string]any)
		wm, ok2 := wireSlice[i].(map[string]any)
		if !ok1 || !ok2 {
			t.Fatalf("tools[%d] must be object", i)
		}
		if cm["type"] != wm["type"] {
			t.Fatalf("tools[%d].type mismatch: canon=%v, wire=%v", i, cm["type"], wm["type"])
		}
		if cm["name"] != wm["name"] {
			t.Fatalf("tools[%d].name mismatch: canon=%v, wire=%v", i, cm["name"], wm["name"])
		}
		if cm["description"] != wm["description"] {
			t.Fatalf("tools[%d].description mismatch: canon=%v, wire=%v", i, cm["description"], wm["description"])
		}
	}
}

func compareInputSemantics(t *testing.T, canonInput, wireInput any) {
	t.Helper()
	canonItems, ok := canonInput.([]any)
	if !ok {
		t.Fatalf("canonical input must be []any, got %T: %v", canonInput, canonInput)
	}

	switch w := wireInput.(type) {
	case string:
		if len(canonItems) != 1 {
			t.Fatalf("string wire input maps to 1 canon item, got %d", len(canonItems))
		}
		first, ok := canonItems[0].(map[string]any)
		if !ok {
			t.Fatalf("canonical item must be object: %v", canonItems[0])
		}
		if first["role"] != "user" {
			t.Fatalf("canonical item role = %v, want user", first["role"])
		}
		if first["content"] != w {
			t.Fatalf("canonical item content = %v, want %q", first["content"], w)
		}
	case []any:
		if len(canonItems) != len(w) {
			t.Fatalf("input length mismatch: canon=%d, wire=%d", len(canonItems), len(w))
		}
		for i := range canonItems {
			cm, ok1 := canonItems[i].(map[string]any)
			wm, ok2 := w[i].(map[string]any)
			if !ok1 || !ok2 {
				t.Fatalf("input[%d] must be object", i)
			}
			if r1, r2 := cm["role"], wm["role"]; r1 != nil && r2 != nil && r1 != r2 {
				t.Fatalf("input[%d].role mismatch: canon=%v, wire=%v", i, r1, r2)
			}
			t1 := fmt.Sprintf("%v", cm["type"])
			t2 := fmt.Sprintf("%v", wm["type"])
			isMsg1 := t1 == "<nil>" || t1 == "message" || t1 == ""
			isMsg2 := t2 == "<nil>" || t2 == "message" || t2 == ""
			if isMsg1 && isMsg2 {
				// Both are message items
			} else if t1 != t2 {
				t.Fatalf("input[%d].type mismatch: canon=%v, wire=%v", i, cm["type"], wm["type"])
			}
			if c1, c2 := cm["content"], wm["content"]; c1 != nil && c2 != nil {
				if !reflect.DeepEqual(c1, c2) {
					t.Fatalf("input[%d].content mismatch: canon=%v, wire=%v", i, c1, c2)
				}
			}
			if cid1, cid2 := cm["call_id"], wm["call_id"]; cid1 != nil && cid2 != nil && cid1 != cid2 {
				t.Fatalf("input[%d].call_id mismatch: canon=%v, wire=%v", i, cid1, cid2)
			}
			if out1, out2 := cm["output"], wm["output"]; out1 != nil && out2 != nil {
				if !reflect.DeepEqual(out1, out2) {
					t.Fatalf("input[%d].output mismatch: canon=%v, wire=%v", i, out1, out2)
				}
			}
		}
	default:
		t.Fatalf("unsupported wire input type: %T", wireInput)
	}
}

// TestProviderDifferential_ExactJSONSemantics exercises canonical vs wire differential comparison (Requirements 9, 12, 17).
func TestProviderDifferential_ExactJSONSemantics(t *testing.T) {
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
				"input": "This is a simple direct string prompt."
			}`,
			candidateModel: "string-input-target-model",
			streaming:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			clientBody := []byte(tt.body)
			inboundHeaders := make(http.Header)

			canonReq := executeCanonicalFlow(t, clientBody, inboundHeaders, tt.candidateModel, tt.streaming)
			wireReq, rewrittenLen := executeWireFlow(t, clientBody, inboundHeaders, tt.candidateModel, tt.streaming)

			assertProviderEffectiveMatch(t, canonReq, wireReq, tt.candidateModel, rewrittenLen, tt.streaming)
		})
	}
}

// TestProviderDifferential_EscapedModelValues verifies JSON escaping and semantic equality for model names (Requirement 9.2, 9.6).
func TestProviderDifferential_EscapedModelValues(t *testing.T) {
	t.Parallel()

	models := []struct {
		name           string
		candidateModel string
	}{
		{
			name:           "slashes",
			candidateModel: "openai/gpt-4o-2024-11-20/custom",
		},
		{
			name:           "escaped_quotes",
			candidateModel: `custom"quoted"model`,
		},
		{
			name:           "escaped_backslashes",
			candidateModel: `vendor\model\variant`,
		},
		{
			name:           "unicode_emojis",
			candidateModel: "gpt-4o-🚀-edition",
		},
		{
			name:           "unicode_japanese",
			candidateModel: "gpt-4o-日本語-v1",
		},
		{
			name:           "whitespace_escapes",
			candidateModel: "model\twith\nnewlines",
		},
	}

	baseBody := `{
		"model": "original-model",
		"stream": true,
		"input": [{"type": "message", "role": "user", "content": "Testing escaped model names"}]
	}`

	for _, m := range models {
		t.Run(m.name, func(t *testing.T) {
			t.Parallel()
			clientBody := []byte(baseBody)
			inboundHeaders := make(http.Header)

			canonReq := executeCanonicalFlow(t, clientBody, inboundHeaders, m.candidateModel, true)
			wireReq, rewrittenLen := executeWireFlow(t, clientBody, inboundHeaders, m.candidateModel, true)

			assertProviderEffectiveMatch(t, canonReq, wireReq, m.candidateModel, rewrittenLen, true)

			// Additional assertion: wire body must be strictly valid JSON
			if !json.Valid(wireReq.Body) {
				t.Fatalf("wire request body with escaped model %q is not valid JSON: %s", m.candidateModel, string(wireReq.Body))
			}
		})
	}
}

// TestProviderDifferential_LatePositionedModel verifies that model keys appearing late in the JSON object are handled properly (Requirement 9.1, 9.3).
func TestProviderDifferential_LatePositionedModel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{
			name: "model_at_very_end",
			body: `{
				"instructions": "Be extremely thorough and precise.",
				"temperature": 0.3,
				"input": [
					{"type": "message", "role": "user", "content": "Explain quantum computing."}
				],
				"top_p": 0.85,
				"max_output_tokens": 1024,
				"stream": true,
				"model": "gpt-4o"
			}`,
		},
		{
			name: "model_after_large_input_payload",
			body: fmt.Sprintf(`{
				"instructions": "You are a compiler.",
				"stream": true,
				"input": [
					{"type": "message", "role": "user", "content": "%s"}
				],
				"model": "gpt-4o"
			}`, strings.Repeat("a", 16*1024)),
		},
		{
			name: "model_in_middle_between_tools_and_input",
			body: `{
				"tools": [
					{
						"type": "function",
						"name": "lookup",
						"description": "lookup tool",
						"parameters": {"type": "object"}
					}
				],
				"model": "gpt-4o",
				"stream": true,
				"input": [{"type": "message", "role": "user", "content": "lookup x"}]
			}`,
		},
	}

	targetModel := "rewritten-late-target-model"

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			clientBody := []byte(tt.body)
			inboundHeaders := make(http.Header)

			canonReq := executeCanonicalFlow(t, clientBody, inboundHeaders, targetModel, true)
			wireReq, rewrittenLen := executeWireFlow(t, clientBody, inboundHeaders, targetModel, true)

			assertProviderEffectiveMatch(t, canonReq, wireReq, targetModel, rewrittenLen, true)
		})
	}
}

// TestProviderDifferential_NoStaleFramingHeadersAndExactLength verifies outbound framing and headers hygiene (Requirement 12.2, 12.4, 8.5).
func TestProviderDifferential_NoStaleFramingHeadersAndExactLength(t *testing.T) {
	t.Parallel()

	dirtyInboundHeaders := http.Header{
		"Transfer-Encoding":   []string{"chunked"},
		"Content-Length":      []string{"999999"},
		"Content-Encoding":    []string{"gzip"},
		"Expect":              []string{"100-continue"},
		"Trailer":             []string{"X-Test-Trailer"},
		"Connection":          []string{"close", "X-Custom-Hop"},
		"X-Custom-Hop":        []string{"hop-val"},
		"Authorization":       []string{"Bearer client-secret-must-not-leak"},
		"Proxy-Authorization": []string{"Basic proxy-pass"},
		"X-Api-Key":           []string{"client-api-key"},
		"Api-Key":             []string{"azure-key"},
		"X-Goog-Api-Key":      []string{"google-key"},
		"X-Session-Id":        []string{"client-sess-1"},
		"X-Resume-Token":      []string{"client-token-1"},
		"X-Aip-Session-Id":    []string{"sess-999"},
		"X-Aip-Route":         []string{"route-selector"},
		"X-Aip-A-Leg-Id":      []string{"aleg-1"},
		"X-Trace-Id":          []string{"trace-1"},
	}

	clientBody := []byte(`{
		"model": "gpt-4o",
		"stream": true,
		"input": [{"type": "message", "role": "user", "content": "Header hygiene check"}]
	}`)

	targetModel := "hygiene-checked-model"

	canonReq := executeCanonicalFlow(t, clientBody, dirtyInboundHeaders, targetModel, true)
	wireReq, rewrittenLen := executeWireFlow(t, clientBody, dirtyInboundHeaders, targetModel, true)

	assertProviderEffectiveMatch(t, canonReq, wireReq, targetModel, rewrittenLen, true)

	// Specifically verify that dirty inbound headers did NOT leak to wire request
	leakHeaders := []string{
		"Transfer-Encoding",
		"Content-Encoding",
		"Expect",
		"Trailer",
		"X-Custom-Hop",
		"Proxy-Authorization",
		"X-Api-Key",
		"Api-Key",
		"X-Goog-Api-Key",
		"X-Session-Id",
		"X-Resume-Token",
		"X-Aip-Session-Id",
		"X-Aip-Route",
		"X-Aip-A-Leg-Id",
		"X-Trace-Id",
	}

	for _, k := range leakHeaders {
		if got := wireReq.Header.Get(k); got != "" {
			t.Errorf("wire request leaked restricted header %s = %q", k, got)
		}
	}

	// Verify Authorization was overwritten by backend secret
	if got := wireReq.Header.Get("Authorization"); got != "Bearer sk-backend-secret" {
		t.Errorf("wire Authorization = %q, want Bearer sk-backend-secret", got)
	}

	// Verify exact rewritten Content-Length
	if wireReq.ContentLength != rewrittenLen {
		t.Errorf("wire ContentLength = %d, want %d", wireReq.ContentLength, rewrittenLen)
	}
}

// TestProviderDifferential_HostedWebSearchDeclinesBeforeWire pins the fast-path
// guard on a wire-capable backend: a raw hosted web-search declaration must
// decline to canonical processing before it can be forwarded, while an ordinary
// function-tool request still takes the wire path unchanged.
func TestProviderDifferential_HostedWebSearchDeclinesBeforeWire(t *testing.T) {
	t.Parallel()

	prof := frontresponses.NewProfile()
	hosted := []byte(`{"model":"gpt-4o","stream":true,"input":"find the answer","tools":[{"type":"web_search_preview","user_location":{"type":"approximate","city":"San Francisco"},"search_context_size":"high"}]}`)
	_, err := prof.CompileProof(context.Background(), frontendpipe.ProofInput{
		Ctx:                  context.Background(),
		URLPath:              "/v1/responses",
		RouteSelector:        "test-openai-responses:gpt-4o",
		RoutePrefixes:        routeselect.NewPrefixSet([]string{"test-openai-responses"}),
		DefaultRouteSelector: "test-openai-responses:default",
		RouteFromBodyModel:   true,
		Source:               newMemSource(hosted),
		BodyBytes:            int64(len(hosted)),
	})
	if !errors.Is(err, frontresponses.ErrHostedWebSearchRequiresCanonicalDecode) {
		t.Fatalf("a raw hosted web-search declaration must decline before wire execution, got %v", err)
	}

	// Positive control: an ordinary function-tool request still uses the wire
	// path and reaches the provider with its declarations intact.
	ordinary := []byte(`{"model":"gpt-4o","stream":true,"input":"look it up","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]}`)
	captured, _ := executeWireFlow(t, ordinary, nil, "gpt-4o", true)
	tools, ok := captured.ParsedJSON["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("ordinary wire request tools = %#v, want one function declaration", captured.ParsedJSON["tools"])
	}
	tool, ok := tools[0].(map[string]any)
	if !ok || tool["name"] != "lookup" {
		t.Fatalf("ordinary wire request tool = %#v, want function lookup", tools[0])
	}
	if bytes.Contains(captured.Body, []byte("web_search")) || bytes.Contains(captured.Body, []byte("user_location")) {
		t.Fatalf("the ordinary wire request must not carry a hosted web-search shape: %s", captured.Body)
	}
}

// TestProviderDifferential_ModelRewriteCheckedLength verifies checked int64 length math on rewrite (Requirement 9.4).
func TestProviderDifferential_ModelRewriteCheckedLength(t *testing.T) {
	t.Parallel()

	shortBase := `{"model":"m","stream":true,"input":[{"type":"message","role":"user","content":"test"}]}`
	longTarget := "very-long-candidate-model-name-for-expansion"

	wireReq1, len1 := executeWireFlow(t, []byte(shortBase), nil, longTarget, true)
	expectedLen1 := int64(len(shortBase) - len(`"m"`) + len(`"`+longTarget+`"`))
	if len1 != expectedLen1 {
		t.Fatalf("expansion rewrittenLen = %d, want %d", len1, expectedLen1)
	}
	if wireReq1.ContentLength != expectedLen1 {
		t.Fatalf("expansion wire ContentLength = %d, want %d", wireReq1.ContentLength, expectedLen1)
	}
	if int64(len(wireReq1.Body)) != expectedLen1 {
		t.Fatalf("expansion actual body bytes = %d, want %d", len(wireReq1.Body), expectedLen1)
	}

	longBase := fmt.Sprintf(`{"model":"%s","stream":true,"input":[{"type":"message","role":"user","content":"test"}]}`, longTarget)
	shortTarget := "m"

	wireReq2, len2 := executeWireFlow(t, []byte(longBase), nil, shortTarget, true)
	expectedLen2 := int64(len(longBase) - len(`"`+longTarget+`"`) + len(`"`+shortTarget+`"`))
	if len2 != expectedLen2 {
		t.Fatalf("shrink rewrittenLen = %d, want %d", len2, expectedLen2)
	}
	if wireReq2.ContentLength != expectedLen2 {
		t.Fatalf("shrink wire ContentLength = %d, want %d", wireReq2.ContentLength, expectedLen2)
	}
	if int64(len(wireReq2.Body)) != expectedLen2 {
		t.Fatalf("shrink actual body bytes = %d, want %d", len(wireReq2.Body), expectedLen2)
	}
}
