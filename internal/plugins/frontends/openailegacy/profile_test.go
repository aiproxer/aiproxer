package openailegacy_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	frontendlimits "github.com/aiproxer/aiproxer/internal/plugins/frontends/limits"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/routeselect"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// memSource wraps a byte slice as largebody.Source for testing.
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

func defaultChatProofInput(body []byte, routeSelector string, headers http.Header) frontendpipe.ProofInput {
	if headers == nil {
		headers = make(http.Header)
	}
	return frontendpipe.ProofInput{
		Ctx:                  context.Background(),
		Headers:              headers,
		URLPath:              "/v1/chat/completions",
		RouteSelector:        routeSelector,
		RoutePrefixes:        routeselect.NewPrefixSet([]string{"openai", "stub", "custom"}),
		DefaultRouteSelector: "stub:default",
		RouteFromBodyModel:   true,
		Source:               newMemSource(body),
		BodyBytes:            int64(len(body)),
	}
}

// Requirement 13, 17.6: Confirm at implementation time: no legacy full-body resolver; RouteFromBodyModel=true.
func TestOpenAIChatProfile_PrerequisitesAndIdentity(t *testing.T) {
	t.Parallel()

	prof := openailegacy.NewProfile()
	if prof.ProfileID() != openailegacy.ProfileID {
		t.Fatalf("ProfileID: got %q, want %q", prof.ProfileID(), openailegacy.ProfileID)
	}
	if prof.ProfileID() != "openai_chat_v1" {
		t.Fatalf("ProfileID constant: got %q, want openai_chat_v1", prof.ProfileID())
	}

	// Verify Handler builds pipe with no custom route hook and RouteFromBodyModel=true
	h := &openailegacy.Handler{}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req) // triggers buildPipe via pipeOnce

	spec := h.Spec()
	if spec == nil {
		t.Fatal("Handler.Spec() returned nil")
	}
	if spec.ResolvePreAdmissionRoute != nil || spec.ResolveBodyRoute != nil {
		t.Fatal("Handler must NOT configure custom route hooks (Requirement 13.2, 17.6)")
	}
	if !spec.RouteFromBodyModel {
		t.Fatal("Handler must configure RouteFromBodyModel=true (Requirement 4.8, 17.6)")
	}
	if spec.Profile == nil {
		t.Fatal("Handler must wire certified Profile into pipe (Requirement 17.1)")
	}
	if spec.Profile.ProfileID() != "openai_chat_v1" {
		t.Fatalf("Handler pipe profile ID: got %q, want openai_chat_v1", spec.Profile.ProfileID())
	}

	// Custom profile override is honored
	customProf := openailegacy.NewProfile()
	h2 := &openailegacy.Handler{Profile: customProf}
	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	rec2 := httptest.NewRecorder()
	h2.ServeHTTP(rec2, req2)
	if h2.Spec().Profile != customProf {
		t.Fatal("Handler must honor explicit Profile override")
	}
}

// Requirement 4, 14, 16, 17: Compile proof for simple user message.
func TestOpenAIChatProfile_CompileProof_SimpleUserMessage(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hello world"}]}`)
	prof := openailegacy.NewProfile()
	in := defaultChatProofInput(body, "", nil)

	out, err := prof.CompileProof(context.Background(), in)
	if err != nil {
		t.Fatalf("CompileProof failed: %v", err)
	}

	proof := out.State.Proof
	if proof.ProfileID != "openai_chat_v1" {
		t.Errorf("Proof.ProfileID: got %q, want openai_chat_v1", proof.ProfileID)
	}
	if proof.Operation != aipapi.OperationOpenAIChatCompletions {
		t.Errorf("Proof.Operation: got %q, want %q", proof.Operation, aipapi.OperationOpenAIChatCompletions)
	}
	if proof.Delivery != aipapi.DeliveryModeNonStreaming {
		t.Errorf("Proof.Delivery: got %v, want NonStreaming", proof.Delivery)
	}
	if proof.ClientModel != "gpt-4o" {
		t.Errorf("Proof.ClientModel: got %q, want gpt-4o", proof.ClientModel)
	}
	if proof.RouteSelector != "stub:default" {
		t.Errorf("Proof.RouteSelector: got %q, want stub:default", proof.RouteSelector)
	}
	if proof.Mode != largebody.BodyModeIdentityJSON {
		t.Errorf("Proof.Mode: got %v, want BodyModeIdentityJSON", proof.Mode)
	}
	if !proof.Rewrite.NeedsModelRewrite() {
		t.Error("Proof.Rewrite: expected NeedsModelRewrite=true")
	}

	// Verify model token span
	if proof.ModelSpan.Length == 0 {
		t.Fatal("Proof.ModelSpan: expected non-zero length")
	}
	spanVal := string(body[proof.ModelSpan.Offset : proof.ModelSpan.Offset+proof.ModelSpan.Length])
	if spanVal != `"gpt-4o"` {
		t.Errorf("Proof.ModelSpan slice: got %q, want %q", spanVal, `"gpt-4o"`)
	}

	// Verify seeds
	seeds := out.State.Seeds
	if seeds.DeterministicToken == "" {
		t.Error("Seeds.DeterministicToken is empty")
	}
	if seeds.CancellationID != "chatcmpl_"+seeds.DeterministicToken {
		t.Errorf("Seeds.CancellationID: got %q, want chatcmpl_%s", seeds.CancellationID, seeds.DeterministicToken)
	}

	// Verify canonical identity parity
	decoded, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{
		RouteSelector: "stub:default",
	})
	if err != nil {
		t.Fatalf("DecodeChatRequest: %v", err)
	}
	canonicalDigest := largebody.CanonicalCallIdentity(decoded.Call)
	if proof.Identity != canonicalDigest {
		t.Fatalf("Proof.Identity mismatch:\n  proof:     %s\n  canonical: %s", proof.Identity, canonicalDigest)
	}
}

// Requirement 4, 14, 16, 17: Compile proof with streaming and generation options.
func TestOpenAIChatProfile_CompileProof_StreamingAndOptions(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"model": "gpt-4o",
		"stream": true,
		"messages": [
			{"role": "system", "content": "you are a helpful assistant"},
			{"role": "user", "content": "hello"}
		],
		"temperature": 0.7,
		"top_p": 0.9,
		"max_tokens": 150,
		"parallel_tool_calls": true,
		"reasoning_effort": "high",
		"verbosity": "high",
		"stream_options": {"include_usage": true}
	}`)
	prof := openailegacy.NewProfile()
	in := defaultChatProofInput(body, "openai:gpt-4o", nil)

	out, err := prof.CompileProof(context.Background(), in)
	if err != nil {
		t.Fatalf("CompileProof failed: %v", err)
	}

	proof := out.State.Proof
	if proof.Delivery != aipapi.DeliveryModeStreaming {
		t.Errorf("Proof.Delivery: got %v, want Streaming", proof.Delivery)
	}
	if proof.RouteSelector != "openai:gpt-4o" {
		t.Errorf("Proof.RouteSelector: got %q, want openai:gpt-4o", proof.RouteSelector)
	}
	if proof.MaxOutputTokens != 150 {
		t.Errorf("Proof.MaxOutputTokens: got %d, want 150", proof.MaxOutputTokens)
	}

	// Verify canonical identity parity
	decoded, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{
		RouteSelector: "openai:gpt-4o",
	})
	if err != nil {
		t.Fatalf("DecodeChatRequest: %v", err)
	}
	canonicalDigest := largebody.CanonicalCallIdentity(decoded.Call)
	if proof.Identity != canonicalDigest {
		t.Fatalf("Proof.Identity mismatch:\n  proof:     %s\n  canonical: %s", proof.Identity, canonicalDigest)
	}
}

// Requirement 4.8, 17.6: Route selector precedence: header > inline prefix > default.
func TestOpenAIChatProfile_RoutePrecedence(t *testing.T) {
	t.Parallel()

	bodyPrefix := []byte(`{"model":"openai:gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	bodyNoPrefix := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	prof := openailegacy.NewProfile()

	// 1. Explicit RouteSelector takes precedence
	in1 := defaultChatProofInput(bodyPrefix, "custom:explicit", nil)
	out1, err := prof.CompileProof(context.Background(), in1)
	if err != nil {
		t.Fatalf("case 1 failed: %v", err)
	}
	if out1.State.Proof.RouteSelector != "custom:explicit" {
		t.Errorf("case 1: got %q, want custom:explicit", out1.State.Proof.RouteSelector)
	}

	// 2. Inline prefix from model
	in2 := defaultChatProofInput(bodyPrefix, "", nil)
	out2, err := prof.CompileProof(context.Background(), in2)
	if err != nil {
		t.Fatalf("case 2 failed: %v", err)
	}
	if out2.State.Proof.RouteSelector != "openai:gpt-4o" {
		t.Errorf("case 2: got %q, want openai:gpt-4o", out2.State.Proof.RouteSelector)
	}

	// 3. Default route selector when model has no recognized prefix
	in3 := defaultChatProofInput(bodyNoPrefix, "", nil)
	out3, err := prof.CompileProof(context.Background(), in3)
	if err != nil {
		t.Fatalf("case 3 failed: %v", err)
	}
	if out3.State.Proof.RouteSelector != "stub:default" {
		t.Errorf("case 3: got %q, want stub:default", out3.State.Proof.RouteSelector)
	}
}

// Requirement 4, 14, 16, 17: Complex conversation with tools, tool results, reasoning_content.
func TestOpenAIChatProfile_ComplexMessagesAndIdentityParity(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"model": "gpt-4o",
		"messages": [
			{"role": "system", "content": "system instructions"},
			{"role": "user", "content": "calculate 2+2"},
			{
				"role": "assistant",
				"content": "thinking about it",
				"reasoning_content": "math reasoning here",
				"tool_calls": [
					{
						"id": "call_abc123",
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
				"tool_call_id": "call_abc123",
				"content": "4"
			}
		],
		"tools": [
			{
				"type": "function",
				"function": {
					"name": "calc",
					"description": "calculator",
					"parameters": {"type": "object", "properties": {"expr": {"type": "string"}}}
				}
			}
		],
		"tool_choice": "auto"
	}`)

	prof := openailegacy.NewProfile()
	in := defaultChatProofInput(body, "stub:default", nil)

	out, err := prof.CompileProof(context.Background(), in)
	if err != nil {
		t.Fatalf("CompileProof failed: %v", err)
	}

	// Verify canonical identity matches 100%
	decoded, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{
		RouteSelector: "stub:default",
	})
	if err != nil {
		t.Fatalf("DecodeChatRequest: %v", err)
	}
	canonicalDigest := largebody.CanonicalCallIdentity(decoded.Call)
	if out.State.Proof.Identity != canonicalDigest {
		t.Fatalf("Proof.Identity mismatch:\n  proof:     %s\n  canonical: %s", out.State.Proof.Identity, canonicalDigest)
	}
}

// Requirement 14.1, 14.6: Session headers integration and cancellation ID.
func TestOpenAIChatProfile_SessionHeadersAndCarrier(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	headers := make(http.Header)
	headers.Set("X-AIP-Session-ID", "sess_xyz789")
	headers.Set("X-AIP-A-Leg-ID", "aleg_456")

	prof := openailegacy.NewProfile()
	in := defaultChatProofInput(body, "stub:default", headers)

	out, err := prof.CompileProof(context.Background(), in)
	if err != nil {
		t.Fatalf("CompileProof failed: %v", err)
	}

	seeds := out.State.Seeds
	if seeds.SessionID != "sess_xyz789" {
		t.Errorf("Seeds.SessionID: got %q, want sess_xyz789", seeds.SessionID)
	}
	if seeds.ALegID != "aleg_456" {
		t.Errorf("Seeds.ALegID: got %q, want aleg_456", seeds.ALegID)
	}
	expectedCancel := frontendpipe.FormatOpenAICancellationCarrier("aleg_456", "sess_xyz789")
	if seeds.CancellationID != expectedCancel {
		t.Errorf("Seeds.CancellationID: got %q, want %q", seeds.CancellationID, expectedCancel)
	}
}

// Requirement 4, 13, 14, 16, 17: Decline to canonical tests.
func TestOpenAIChatProfile_DeclinesToCanonical(t *testing.T) {
	t.Parallel()

	prof := openailegacy.NewProfile()

	cases := []struct {
		name        string
		body        string
		urlPath     string
		source      largebody.Source
		headers     http.Header
		wantErrSub  string
		errIsTarget error
	}{
		{
			name:       "duplicate key in root",
			body:       `{"model":"gpt-4o","model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "json scanner",
		},
		{
			name:       "unknown top-level key",
			body:       `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"unknown_key":"val"}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "unsupported or unknown body key",
		},
		{
			name:       "unsupported control store",
			body:       `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"store":false}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "unsupported or unknown body key",
		},
		{
			name:       "unsupported control max_completion_tokens",
			body:       `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"max_completion_tokens":100}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "unsupported or unknown body key",
		},
		{
			name:       "unsupported control stop",
			body:       `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"stop":["END"]}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "unsupported or unknown body key",
		},
		{
			name:        "body-carried AIProxer session metadata",
			body:        `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"metadata":{"aip_session_id":"s123"}}`,
			urlPath:     "/v1/chat/completions",
			errIsTarget: largebody.ErrBodySessionMetadataRejected,
		},
		{
			name:       "developer role requires canonical normalization",
			body:       `{"model":"gpt-4o","messages":[{"role":"developer","content":"system prompt"},{"role":"user","content":"hi"}]}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "developer role requires canonical normalization",
		},
		{
			name:       "malformed tool message with empty tool_call_id",
			body:       `{"model":"gpt-4o","messages":[{"role":"tool","tool_call_id":"","content":"result"}]}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "malformed tool message requires canonical repair",
		},
		{
			name:       "non-string tool content requires canonical encoding",
			body:       `{"model":"gpt-4o","messages":[{"role":"tool","tool_call_id":"c1","content":{"status":"ok"}}]}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "non-string tool content requires canonical encoding",
		},
		{
			name:       "empty assistant message requires canonical drop",
			body:       `{"model":"gpt-4o","messages":[{"role":"assistant","content":""}]}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "empty assistant message requires canonical drop",
		},
		{
			name:       "reasoning alias requires canonical normalization",
			body:       `{"model":"gpt-4o","messages":[{"role":"assistant","content":"ans","reasoning":"my thoughts"}]}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "reasoning alias requires canonical normalization",
		},
		{
			name:       "legacy function_call requires canonical normalization",
			body:       `{"model":"gpt-4o","messages":[{"role":"assistant","content":"ans","function_call":{"name":"calc","arguments":"{}"}}]}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "legacy function_call requires canonical normalization",
		},
		{
			name:       "malformed tool_calls entry with empty name",
			body:       `{"model":"gpt-4o","messages":[{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"","arguments":"{}"}}]}]}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "malformed tool call requires canonical repair",
		},
		{
			name:       "non-string tool_calls arguments requires canonical encoding",
			body:       `{"model":"gpt-4o","messages":[{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"calc","arguments":{"a":1}}}]}]}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "non-string tool_calls arguments requires canonical encoding",
		},
		{
			name:       "missing model",
			body:       `{"messages":[{"role":"user","content":"hi"}]}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "model is required",
		},
		{
			name:       "empty messages",
			body:       `{"model":"gpt-4o","messages":[]}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "messages is required",
		},
		{
			name:       "unsupported url path",
			body:       `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`,
			urlPath:    "/v1/other",
			wantErrSub: "unsupported url path",
		},
		{
			name:       "nil source",
			body:       `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`,
			urlPath:    "/v1/chat/completions",
			source:     nil,
			wantErrSub: "nil replay source",
		},
		{
			name:       "invalid json",
			body:       `{not json}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "json scanner",
		},
		{
			name:       "session hint requires canonical decode",
			body:       `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`,
			urlPath:    "/v1/chat/completions",
			headers:    http.Header{"X-Aip-Session-Hint": []string{"client-hint-123"}},
			wantErrSub: "session hint requires canonical decode",
		},
		{
			name:       "non-string model number declines",
			body:       `{"model":123,"messages":[{"role":"user","content":"hi"}]}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "model must be a string",
		},
		{
			name:       "non-array messages string declines",
			body:       `{"model":"gpt-4o","messages":"not an array"}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "messages must be an array",
		},
		{
			name:       "scalar tools string declines",
			body:       `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"tools":"tool1"}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "tools must be an array",
		},
		{
			name:       "scalar tools number declines",
			body:       `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"tools":42}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "tools must be an array",
		},
		{
			name:       "scalar tool_choice number declines",
			body:       `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"tool_choice":123}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "tool_choice must be a string or object",
		},
		{
			name:       "scalar tool_choice boolean declines",
			body:       `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"tool_choice":false}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "tool_choice must be a string or object",
		},
		{
			name:       "wrong-typed stream string declines",
			body:       `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"stream":"true"}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "stream must be a boolean",
		},
		{
			name:       "wrong-typed stream number declines",
			body:       `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"stream":1}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "stream must be a boolean",
		},
		{
			name:       "wrong-typed parallel_tool_calls string declines",
			body:       `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"parallel_tool_calls":"false"}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "parallel_tool_calls must be a boolean",
		},
		{
			name:       "wrong-typed temperature string declines",
			body:       `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"temperature":"warm"}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "temperature must be a number",
		},
		{
			name:       "wrong-typed top_p string declines",
			body:       `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"top_p":"high"}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "top_p must be a number",
		},
		{
			name:       "wrong-typed max_tokens string declines",
			body:       `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"max_tokens":"1000"}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "max_tokens must be an integer",
		},
		{
			name:       "wrong-typed max_tokens float declines",
			body:       `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"max_tokens":100.5}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "max_tokens must be an integer",
		},
		{
			name:       "wrong-typed reasoning_effort number declines",
			body:       `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"reasoning_effort":1}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "reasoning_effort must be a string",
		},
		{
			name:       "wrong-typed verbosity number declines",
			body:       `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"verbosity":1}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "verbosity must be a string",
		},
		{
			name:       "scalar stream_options string declines",
			body:       `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"stream_options":"opt"}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "stream_options must be an object",
		},
		{
			name:       "scalar metadata string declines",
			body:       `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"metadata":"data"}`,
			urlPath:    "/v1/chat/completions",
			wantErrSub: "metadata must be an object",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			bodyBytes := []byte(tc.body)
			var src largebody.Source = newMemSource(bodyBytes)
			if tc.name == "nil source" {
				src = nil
			}

			in := frontendpipe.ProofInput{
				Ctx:                  context.Background(),
				Headers:              tc.headers,
				URLPath:              tc.urlPath,
				RouteSelector:        "stub:default",
				RoutePrefixes:        routeselect.NewPrefixSet([]string{"stub"}),
				DefaultRouteSelector: "stub:default",
				RouteFromBodyModel:   true,
				Source:               src,
				BodyBytes:            int64(len(bodyBytes)),
			}

			_, err := prof.CompileProof(context.Background(), in)
			if err == nil {
				t.Fatal("expected CompileProof to fail and decline to canonical, got nil error")
			}
			if tc.errIsTarget != nil && !errors.Is(err, tc.errIsTarget) {
				t.Fatalf("expected error to match %v, got %v", tc.errIsTarget, err)
			}
			if tc.wantErrSub != "" && !strings.Contains(err.Error(), tc.wantErrSub) {
				t.Fatalf("expected error containing %q, got %q", tc.wantErrSub, err.Error())
			}
		})
	}
}

// Test limits on messages and metadata.
func TestOpenAIChatProfile_Limits(t *testing.T) {
	t.Parallel()

	prof := openailegacy.NewProfile()

	// MaxMessages exceeded
	msgs := make([]map[string]string, frontendlimits.MaxMessages+1)
	for i := range msgs {
		msgs[i] = map[string]string{"role": "user", "content": "hi"}
	}
	body, _ := json.Marshal(map[string]any{
		"model":    "gpt-4o",
		"messages": msgs,
	})
	in := defaultChatProofInput(body, "stub:default", nil)
	_, err := prof.CompileProof(context.Background(), in)
	if err == nil || !strings.Contains(err.Error(), "maximum is") {
		t.Fatalf("expected messages limit error, got %v", err)
	}
}

func test1MiBChatBody(tb testing.TB, target int) []byte {
	tb.Helper()
	const prefix = `{"model":"gpt-4o","messages":[{"role":"user","content":"`
	const suffix = `"}]}`
	pad := target - len(prefix) - len(suffix)
	if pad < 0 {
		tb.Fatalf("target %d smaller than envelope", target)
	}
	var b strings.Builder
	b.Grow(target)
	b.WriteString(prefix)
	b.WriteString(strings.Repeat("a", pad))
	b.WriteString(suffix)
	return []byte(b.String())
}

// Task 19.3 / Remediation Phase 2 (Lane 2):
// Proof-time transient allocation (B/op) must be bounded by
// memory_spool_bytes (64 KiB) + max_semantic_fact_bytes (256 KiB) + fixed buffers (128 KiB) = 448 KiB.
func TestOpenAIChatProfile_CompileProof_TransientAllocBounded(t *testing.T) {
	const target = 1 << 20 // 1 MiB
	body := test1MiBChatBody(t, target)

	spoolDir := t.TempDir()
	spill, err := largebody.NewSpillBuffer(largebody.SpillConfig{
		SpoolDir:         spoolDir,
		MemorySpoolBytes: 64 << 10,
		CopyBufferSize:   32 << 10,
	})
	if err != nil {
		t.Fatalf("spill buffer init: %v", err)
	}
	if _, err := spill.Write(body); err != nil {
		t.Fatalf("spill write: %v", err)
	}
	src, err := spill.Complete()
	if err != nil {
		t.Fatalf("spill complete: %v", err)
	}
	t.Cleanup(func() { _ = src.Close() })

	prof := openailegacy.NewProfile()
	proofIn := frontendpipe.ProofInput{
		Ctx:                  context.Background(),
		Headers:              make(http.Header),
		URLPath:              "/v1/chat/completions",
		RouteSelector:        "stub:gpt-4o",
		RoutePrefixes:        routeselect.NewPrefixSet([]string{"stub", "openai"}),
		DefaultRouteSelector: "stub:gpt-4o",
		RouteFromBodyModel:   true,
		Source:               src,
		BodyBytes:            src.Size(),
	}

	var proofSink frontendpipe.ProofOutput
	benchRes := testing.Benchmark(func(b *testing.B) {
		b.Helper()
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			out, perr := prof.CompileProof(b.Context(), proofIn)
			if perr != nil {
				b.Fatalf("CompileProof failed: %v", perr)
			}
			proofSink = out
		}
	})
	_ = proofSink

	allocBytesPerOp := benchRes.AllocedBytesPerOp()
	const maxAllowedProofBytesPerOp = int64(64<<10) + int64(256<<10) + int64(128<<10) // 448 KiB

	t.Logf("[openai_chat/1MiB] proof-time transient: %d B/op (ceiling: %d B/op)",
		allocBytesPerOp, maxAllowedProofBytesPerOp)

	if allocBytesPerOp > maxAllowedProofBytesPerOp {
		t.Fatalf("proof-time B/op (%d B) must be bounded by %d B, but exceeded target invariant by %d B",
			allocBytesPerOp, maxAllowedProofBytesPerOp, allocBytesPerOp-maxAllowedProofBytesPerOp)
	}
}

// Item 4: Envelope field (model) exceeding DefaultMaxSemanticFactBytes (256 KiB)
// must be bounded and return ErrSemanticFactBudgetExceeded.
func TestOpenAIChatProfile_CompileProof_EnvelopeFactBudget(t *testing.T) {
	t.Parallel()

	prof := openailegacy.NewProfile()
	// Create model larger than 256 KiB budget (e.g. 270 KiB)
	largeModel := strings.Repeat("m", 270*1024)
	body := fmt.Appendf(nil, `{"model":"%s","messages":[{"role":"user","content":"Hello"}]}`, largeModel)
	in := defaultChatProofInput(body, "stub:gpt-4o", nil)

	_, err := prof.CompileProof(context.Background(), in)
	if err == nil {
		t.Fatal("expected error for model exceeding fact budget, got nil")
	}
	if !errors.Is(err, largebody.ErrSemanticFactBudgetExceeded) {
		t.Fatalf("expected ErrSemanticFactBudgetExceeded, got: %v", err)
	}
}

// Item 1: Padded string differential (small payload).
// Tests that a message with leading and trailing whitespace in string content
// matches canonical identity byte-for-byte and turn shape has trimmed bytes.
func TestOpenAIChatProfile_CompileProof_PaddedStringDifferential(t *testing.T) {
	t.Parallel()

	prof := openailegacy.NewProfile()
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"  \n\t  Hello, padded and trimmed chat differential!  \r\n  "}]}`)
	in := defaultChatProofInput(body, "stub:gpt-4o", nil)

	out, err := prof.CompileProof(context.Background(), in)
	if err != nil {
		t.Fatalf("CompileProof failed: %v", err)
	}

	canonDecoded, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{
		RouteSelector: "stub:gpt-4o",
	})
	if err != nil {
		t.Fatalf("canonical DecodeChatRequest failed: %v", err)
	}
	wantDigest := largebody.CanonicalCallIdentity(canonDecoded.Call)
	if out.State.Proof.Identity.Sum() != wantDigest.Sum() {
		t.Fatalf("Identity mismatch for padded string:\ngot:  %x\nwant: %x", out.State.Proof.Identity.Sum(), wantDigest.Sum())
	}

	wantTurnShape, err := largebody.ClientTurnShapeFromCall(canonDecoded.Call, frontendpipe.DefaultMaxSemanticFactBytes)
	if err != nil {
		t.Fatalf("canonical ClientTurnShapeFromCall failed: %v", err)
	}
	if out.State.Proof.Turn.TotalContentBytes != wantTurnShape.TotalContentBytes {
		t.Fatalf("Turn.TotalContentBytes mismatch: got %d, want %d", out.State.Proof.Turn.TotalContentBytes, wantTurnShape.TotalContentBytes)
	}
}

// Item 1 & 2: Large array differential with mixed roles (system, user, assistant).
// Payload exceeds 2*DefaultMaxSemanticFactBytes (512 KiB) to trigger the streaming large array path.
func TestOpenAIChatProfile_CompileProof_MixedRoleLargeArrayDifferential(t *testing.T) {
	t.Parallel()

	prof := openailegacy.NewProfile()
	chunkU := strings.Repeat("u", 280*1024)
	chunkV := strings.Repeat("v", 280*1024)

	var sb strings.Builder
	sb.WriteString(`{"model":"gpt-4o","messages":[`)
	sb.WriteString(`{"role":"system","content":"System prompt initialization."}`)
	sb.WriteString(`,{"role":"user","content":"` + chunkU + `"}`)
	sb.WriteString(`,{"role":"assistant","content":"Understood, processing your data."}`)
	sb.WriteString(`,{"role":"user","content":"` + chunkV + `"}`)
	sb.WriteString(`]}`)

	body := []byte(sb.String())
	in := defaultChatProofInput(body, "stub:gpt-4o", nil)

	out, err := prof.CompileProof(context.Background(), in)
	if err != nil {
		t.Fatalf("CompileProof failed for mixed-role large array: %v", err)
	}

	proof := out.State.Proof

	// Verify roles in ClientTurnShape
	if len(proof.Turn.Items) != 4 {
		t.Fatalf("Proof.Turn.Items count: got %d, want 4", len(proof.Turn.Items))
	}
	expectedRoles := []aipapi.Role{
		aipapi.RoleSystem,
		aipapi.RoleUser,
		aipapi.RoleAssistant,
		aipapi.RoleUser,
	}
	for i, expectedRole := range expectedRoles {
		if proof.Turn.Items[i].Role != expectedRole {
			t.Errorf("Turn.Items[%d].Role: got %v, want %v", i, proof.Turn.Items[i].Role, expectedRole)
		}
	}

	// Compare with canonical decode
	canonDecoded, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{
		RouteSelector: "stub:gpt-4o",
	})
	if err != nil {
		t.Fatalf("canonical DecodeChatRequest failed: %v", err)
	}
	wantDigest := largebody.CanonicalCallIdentity(canonDecoded.Call)
	if proof.Identity.Sum() != wantDigest.Sum() {
		t.Fatalf("Identity mismatch for mixed-role large array:\ngot:  %x\nwant: %x", proof.Identity.Sum(), wantDigest.Sum())
	}
}

// Item 1: Padded large array differential.
// Large array payload (> 512 KiB) where content strings have leading and trailing whitespace.
// Canonical decode trims whitespace via strings.TrimSpace; streaming proof must match byte-for-byte.
func TestOpenAIChatProfile_CompileProof_PaddedLargeArrayDifferential(t *testing.T) {
	t.Parallel()

	prof := openailegacy.NewProfile()
	// Large array exceeding 512 KiB with padded message contents
	chunkU := "  \\n\\t  " + strings.Repeat("u", 280*1024) + "  \\r\\n  "
	chunkV := " \\t " + strings.Repeat("v", 280*1024) + " \\n "

	var sb strings.Builder
	sb.WriteString(`{"model":"gpt-4o","messages":[`)
	sb.WriteString(`{"role":"system","content":"  \\nSystem prompt initialization.  \\t"}`)
	sb.WriteString(`,{"role":"user","content":"` + chunkU + `"}`)
	sb.WriteString(`,{"role":"assistant","content":"  Understood, processing your data.  \\r\\n"}`)
	sb.WriteString(`,{"role":"user","content":"` + chunkV + `"}`)
	sb.WriteString(`]}`)

	body := []byte(sb.String())
	in := defaultChatProofInput(body, "stub:gpt-4o", nil)

	out, err := prof.CompileProof(context.Background(), in)
	if err != nil {
		t.Fatalf("CompileProof failed for padded large array: %v", err)
	}

	proof := out.State.Proof

	canonDecoded, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{
		RouteSelector: "stub:gpt-4o",
	})
	if err != nil {
		t.Fatalf("canonical DecodeChatRequest failed: %v", err)
	}
	wantDigest := largebody.CanonicalCallIdentity(canonDecoded.Call)
	if proof.Identity.Sum() != wantDigest.Sum() {
		t.Fatalf("Identity mismatch for padded large array:\ngot:  %x\nwant: %x", proof.Identity.Sum(), wantDigest.Sum())
	}

	// Also verify that turn content bytes count trimmed bytes
	wantTurnShape, err := largebody.ClientTurnShapeFromCall(canonDecoded.Call, frontendpipe.DefaultMaxSemanticFactBytes)
	if err != nil {
		t.Fatalf("canonical ClientTurnShapeFromCall failed: %v", err)
	}
	if proof.Turn.TotalContentBytes != wantTurnShape.TotalContentBytes {
		t.Fatalf("Turn.TotalContentBytes mismatch: got %d, want %d", proof.Turn.TotalContentBytes, wantTurnShape.TotalContentBytes)
	}
	for i := range proof.Turn.Items {
		if proof.Turn.Items[i].Parts[0].ContentBytes != wantTurnShape.Items[i].Parts[0].ContentBytes {
			t.Fatalf("Turn.Items[%d].Parts[0].ContentBytes mismatch: got %d, want %d",
				i, proof.Turn.Items[i].Parts[0].ContentBytes, wantTurnShape.Items[i].Parts[0].ContentBytes)
		}
	}
}

// Item 5: Number tokens spanning the 32 KiB chunk boundary.
// Tests that options (max_tokens, temperature, top_p) whose number tokens straddle
// offset 32768 are correctly extracted and match canonical identity.
func TestOpenAIChatProfile_CompileProof_ChunkBoundaryNumbersDifferential(t *testing.T) {
	t.Parallel()

	prof := openailegacy.NewProfile()

	cases := []struct {
		name       string
		optionKey  string
		numLiteral string
		verify     func(t *testing.T, proof largebody.Proof, canon *aipapi.Call)
	}{
		{
			name:       "max_tokens straddling 32768",
			optionKey:  "max_tokens",
			numLiteral: "1500",
			verify: func(t *testing.T, proof largebody.Proof, canon *aipapi.Call) {
				t.Helper()
				if proof.MaxOutputTokens != 1500 {
					t.Fatalf("MaxOutputTokens dropped: got %d, want 1500", proof.MaxOutputTokens)
				}
			},
		},
		{
			name:       "temperature straddling 32768",
			optionKey:  "temperature",
			numLiteral: "0.7",
			verify: func(t *testing.T, proof largebody.Proof, canon *aipapi.Call) {
				t.Helper()
				// Verified via identity match
			},
		},
		{
			name:       "top_p straddling 32768",
			optionKey:  "top_p",
			numLiteral: "0.85",
			verify: func(t *testing.T, proof largebody.Proof, canon *aipapi.Call) {
				t.Helper()
				// Verified via identity match
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Target offset for number start: 32766 (straddling 32768 chunk boundary)
			targetOffset := 32766
			prefix1 := `{"model":"gpt-4o","metadata":{"pad":"`
			suffix1 := `"},"` + tc.optionKey + `":`
			padLen := targetOffset - len(prefix1) - len(suffix1)
			if padLen < 0 {
				t.Fatalf("padLen %d < 0", padLen)
			}
			pad := strings.Repeat("x", padLen)
			tail := `,"messages":[{"role":"user","content":"hello"}]}`

			body := []byte(prefix1 + pad + suffix1 + tc.numLiteral + tail)

			// Verify that the number token actually straddles offset 32768:
			numStart := len(prefix1) + padLen + len(suffix1)
			numEnd := numStart + len(tc.numLiteral)
			if numStart >= 32768 || numEnd <= 32768 {
				t.Fatalf("test setup error: number span [%d, %d] does not straddle 32768", numStart, numEnd)
			}

			in := defaultChatProofInput(body, "stub:gpt-4o", nil)
			out, err := prof.CompileProof(context.Background(), in)
			if err != nil {
				t.Fatalf("CompileProof failed: %v", err)
			}

			proof := out.State.Proof

			canonDecoded, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{
				RouteSelector: "stub:gpt-4o",
			})
			if err != nil {
				t.Fatalf("canonical DecodeChatRequest failed: %v", err)
			}

			wantDigest := largebody.CanonicalCallIdentity(canonDecoded.Call)
			if proof.Identity.Sum() != wantDigest.Sum() {
				t.Fatalf("Identity mismatch for %s:\ngot:  %x\nwant: %x", tc.name, proof.Identity.Sum(), wantDigest.Sum())
			}
			if tc.verify != nil {
				tc.verify(t, proof, canonDecoded.Call)
			}
		})
	}
}

// Requirement 1.1, 1.2 (large-payload-proof-and-parity): nested role metadata
// shares the single before-write envelope budget. Oversize nested strings
// decline with the semantic-budget failure; whitespace padding counts decoded
// bytes without discount; exact limit still accepts.
func TestOpenAIChatProfile_NestedMetadataBudget(t *testing.T) {
	t.Parallel()

	prof := openailegacy.NewProfile()
	compileWithBudget := func(body []byte, budget int64) error {
		in := defaultChatProofInput(body, "stub:gpt-4o", nil)
		in.Ctx = largebody.WithSemanticFactBudget(context.Background(), budget)
		_, err := prof.CompileProof(context.Background(), in)
		return err
	}

	// Oversized nested role: budget must win over role validation.
	bigRole := strings.Repeat("x", 300)
	bigBody := []byte(`{"model":"gpt-4o","messages":[{"role":"` + bigRole + `","content":"hi"}]}`)
	if err := compileWithBudget(bigBody, 100); !errors.Is(err, largebody.ErrSemanticFactBudgetExceeded) {
		t.Fatalf("oversized role: expected budget error, got %v", err)
	}

	// Whitespace-padded valid role: 94 spaces + "user" decodes to 98 bytes
	// against model(6), so budget 104 accepts and 103 declines.
	paddedRole := strings.Repeat(" ", 94) + "user"
	paddedBody := []byte(`{"model":"gpt-4o","messages":[{"role":"` + paddedRole + `","content":"hi"}]}`)
	if err := compileWithBudget(paddedBody, 103); !errors.Is(err, largebody.ErrSemanticFactBudgetExceeded) {
		t.Fatalf("padded role limit+1: expected budget error, got %v", err)
	}
	if err := compileWithBudget(paddedBody, 104); err != nil {
		t.Fatalf("padded role at limit: unexpected error: %v", err)
	}
}

// Requirement 1.1, 1.2: raw envelope capture charges before retaining.
func TestOpenAIChatProfile_RawEnvelopeBudgetBoundary(t *testing.T) {
	t.Parallel()

	prof := openailegacy.NewProfile()
	arr := `[{"type":"function","function":{"name":"lookup","description":"` + strings.Repeat("d", 64) + `"}}]`
	body := []byte(`{"model":"gpt-4o","tools":` + arr + `,"messages":[{"role":"user","content":"hi"}]}`)
	// Retained metadata: model "gpt-4o" (6 decoded bytes) + nested message
	// role "user" (4) + tools raw slice. Message contents stream through
	// identity, not the budget.
	wantTotal := int64(6 + 4 + len(arr))
	compileWithBudget := func(budget int64) error {
		in := defaultChatProofInput(body, "stub:gpt-4o", nil)
		in.Ctx = largebody.WithSemanticFactBudget(context.Background(), budget)
		_, err := prof.CompileProof(context.Background(), in)
		return err
	}
	if err := compileWithBudget(wantTotal - 1); !errors.Is(err, largebody.ErrSemanticFactBudgetExceeded) {
		t.Fatalf("raw envelope limit+1: expected budget error, got %v", err)
	}
	if err := compileWithBudget(wantTotal); err != nil {
		t.Fatalf("raw envelope at limit: unexpected error: %v", err)
	}

	// Chunk-spanning raw capture: a tools array crossing the 32 KiB replay
	// chunk boundary keeps charging each chunk-hook slice against the same
	// remaining budget through the shared before-write leaf.
	bigArr := `[{"type":"function","function":{"name":"lookup","description":"` + strings.Repeat("d", 40*1024) + `"}}]`
	bigBody := []byte(`{"model":"gpt-4o","tools":` + bigArr + `,"messages":[{"role":"user","content":"hi"}]}`)
	bigWant := int64(6 + 4 + len(bigArr))
	compileBigWithBudget := func(budget int64) error {
		in := defaultChatProofInput(bigBody, "stub:gpt-4o", nil)
		in.Ctx = largebody.WithSemanticFactBudget(context.Background(), budget)
		_, err := prof.CompileProof(context.Background(), in)
		return err
	}
	if err := compileBigWithBudget(bigWant - 1); !errors.Is(err, largebody.ErrSemanticFactBudgetExceeded) {
		t.Fatalf("chunk-spanning raw envelope limit+1: expected budget error, got %v", err)
	}
	if err := compileBigWithBudget(bigWant); err != nil {
		t.Fatalf("chunk-spanning raw envelope at limit: unexpected error: %v", err)
	}
}
