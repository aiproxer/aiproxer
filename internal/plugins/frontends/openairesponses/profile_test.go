package openairesponses_test

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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/routeselect"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/sessionwire"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
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

func defaultProofInput(body []byte, routeSelector string, headers http.Header) frontendpipe.ProofInput {
	if headers == nil {
		headers = make(http.Header)
	}
	return frontendpipe.ProofInput{
		Ctx:                  context.Background(),
		Headers:              headers,
		URLPath:              "/v1/responses",
		RouteSelector:        routeSelector,
		RoutePrefixes:        routeselect.NewPrefixSet([]string{"openai", "stub", "custom"}),
		DefaultRouteSelector: "stub:default",
		RouteFromBodyModel:   true,
		Source:               newMemSource(body),
		BodyBytes:            int64(len(body)),
	}
}

// Requirement 13, 17.6: Confirm at implementation time: no legacy full-body resolver; RouteFromBodyModel=true.
func TestOpenAIResponsesProfile_PrerequisitesAndIdentity(t *testing.T) {
	t.Parallel()

	prof := openairesponses.NewProfile()
	if prof.ProfileID() != openairesponses.ProfileID {
		t.Fatalf("ProfileID: got %q, want %q", prof.ProfileID(), openairesponses.ProfileID)
	}
	if prof.ProfileID() != "openai_responses_v1" {
		t.Fatalf("ProfileID constant: got %q, want openai_responses_v1", prof.ProfileID())
	}

	// Verify Handler builds pipe with no custom route hook and RouteFromBodyModel=true
	h := &openairesponses.Handler{}
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`))
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
	if spec.Profile.ProfileID() != "openai_responses_v1" {
		t.Fatalf("Handler pipe profile ID: got %q, want openai_responses_v1", spec.Profile.ProfileID())
	}
}

// Requirement 4, 14, 16, 17: Compile proof for simple string input.
func TestOpenAIResponsesProfile_CompileProof_SimpleStringInput(t *testing.T) {
	t.Parallel()

	prof := openairesponses.NewProfile()
	body := []byte(`{"model":"gpt-4o","input":"Hello, world!"}`)
	in := defaultProofInput(body, "custom:gpt-4o", nil)

	out, err := prof.CompileProof(context.Background(), in)
	if err != nil {
		t.Fatalf("CompileProof failed: %v", err)
	}

	proof := out.Proof()
	if proof.ProfileID != "openai_responses_v1" {
		t.Errorf("Proof.ProfileID: got %q, want openai_responses_v1", proof.ProfileID)
	}
	if proof.Operation != aipapi.OperationOpenAIResponses {
		t.Errorf("Proof.Operation: got %v, want %v", proof.Operation, aipapi.OperationOpenAIResponses)
	}
	if proof.Delivery != aipapi.DeliveryModeNonStreaming {
		t.Errorf("Proof.Delivery: got %v, want NonStreaming", proof.Delivery)
	}
	if proof.ClientModel != "gpt-4o" {
		t.Errorf("Proof.ClientModel: got %q, want gpt-4o", proof.ClientModel)
	}
	if proof.RouteSelector != "custom:gpt-4o" {
		t.Errorf("Proof.RouteSelector: got %q, want custom:gpt-4o", proof.RouteSelector)
	}

	// ModelSpan must point to `"gpt-4o"` in body
	modelSpan := proof.ModelSpan
	if modelSpan.Length != int64(len(`"gpt-4o"`)) {
		t.Errorf("Proof.ModelSpan length: got %d, want %d", modelSpan.Length, len(`"gpt-4o"`))
	}
	extractedModel := string(body[modelSpan.Offset : modelSpan.Offset+modelSpan.Length])
	if extractedModel != `"gpt-4o"` {
		t.Errorf("extracted model from span: got %q, want %q", extractedModel, `"gpt-4o"`)
	}

	// Rewrite must be ModelToken rewrite with matching span
	if proof.Rewrite.Kind() != largebody.RewriteKindModelToken {
		t.Errorf("Proof.Rewrite.Kind: got %v, want ModelToken", proof.Rewrite.Kind())
	}
	if proof.Rewrite.Span() != modelSpan {
		t.Errorf("Proof.Rewrite.Span: got %v, want %v", proof.Rewrite.Span(), modelSpan)
	}

	// BodyMode
	if proof.Mode != largebody.BodyModeIdentityJSON {
		t.Errorf("Proof.Mode: got %q, want %q", proof.Mode, largebody.BodyModeIdentityJSON)
	}

	// ProtocolFacts
	if proof.Facts.RequirementsID != "openai_responses_v1" {
		t.Errorf("Proof.Facts.RequirementsID: got %q, want openai_responses_v1", proof.Facts.RequirementsID)
	}

	// ClientTurnShape (Requirement 14.3)
	if len(proof.Turn.Items) != 1 {
		t.Fatalf("Proof.Turn.Items count: got %d, want 1", len(proof.Turn.Items))
	}
	it := proof.Turn.Items[0]
	if it.Kind != aipapi.ItemKindMessage || it.Role != aipapi.RoleUser || it.Ordinal != 0 {
		t.Errorf("Proof.Turn.Items[0]: got kind=%v role=%v ordinal=%d", it.Kind, it.Role, it.Ordinal)
	}
	if len(it.Parts) != 1 || it.Parts[0].Kind != aipapi.ContentPartText || it.Parts[0].ContentBytes != int64(len("Hello, world!")) {
		t.Errorf("Proof.Turn.Items[0].Parts: got %v", it.Parts)
	}
	if proof.Turn.TotalContentBytes != int64(len("Hello, world!")) {
		t.Errorf("Proof.Turn.TotalContentBytes: got %d, want %d", proof.Turn.TotalContentBytes, len("Hello, world!"))
	}

	// Exact Canonical Identity Parity (Requirement 16)
	canonDecoded, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{RouteSelector: "custom:gpt-4o"})
	if err != nil {
		t.Fatalf("canonical DecodeCreateRequest failed: %v", err)
	}
	wantDigest := largebody.CanonicalCallIdentity(canonDecoded.Call)
	if proof.Identity.Sum() != wantDigest.Sum() {
		t.Fatalf("Proof.Identity sum mismatch:\ngot:  %x\nwant: %x", proof.Identity.Sum(), wantDigest.Sum())
	}

	// ResponseStateSeeds (Requirement 18)
	seeds := out.Seeds()
	if seeds.Stream != false {
		t.Errorf("Seeds.Stream: got %t, want false", seeds.Stream)
	}
	if seeds.RouteSelector != "custom:gpt-4o" {
		t.Errorf("Seeds.RouteSelector: got %q, want custom:gpt-4o", seeds.RouteSelector)
	}
	if seeds.ClientModel != "gpt-4o" {
		t.Errorf("Seeds.ClientModel: got %q, want gpt-4o", seeds.ClientModel)
	}
	if seeds.DeterministicCallID != wantDigest.CallID("") {
		t.Errorf("Seeds.DeterministicCallID: got %q, want %q", seeds.DeterministicCallID, wantDigest.CallID(""))
	}
	if seeds.DeterministicTimestamp != wantDigest.Unix() {
		t.Errorf("Seeds.DeterministicTimestamp: got %d, want %d", seeds.DeterministicTimestamp, wantDigest.Unix())
	}
	if seeds.DeterministicToken != wantDigest.Token() {
		t.Errorf("Seeds.DeterministicToken: got %q, want %q", seeds.DeterministicToken, wantDigest.Token())
	}

	// Validate ProofOutput
	if err := out.Validate(256 * 1024); err != nil {
		t.Fatalf("out.Validate failed: %v", err)
	}
}

// Requirement 4.8, 17.6: Header selector precedence over body-model derivation and default.
func TestOpenAIResponsesProfile_CompileProof_RouteSelectorPrecedence(t *testing.T) {
	t.Parallel()

	prof := openairesponses.NewProfile()

	// 1. Header route selector wins when non-empty
	body1 := []byte(`{"model":"openai:gpt-4o","input":"Hello"}`)
	in1 := defaultProofInput(body1, "custom:override-route", nil)
	out1, err := prof.CompileProof(context.Background(), in1)
	if err != nil {
		t.Fatalf("case 1 failed: %v", err)
	}
	if out1.Proof().RouteSelector != "custom:override-route" {
		t.Errorf("case 1 (header wins): got %q, want custom:override-route", out1.Proof().RouteSelector)
	}

	// 2. Header selector empty, model has recognized route prefix -> inline selector used
	body2 := []byte(`{"model":"openai:gpt-4o","input":"Hello"}`)
	in2 := defaultProofInput(body2, "", nil)
	out2, err := prof.CompileProof(context.Background(), in2)
	if err != nil {
		t.Fatalf("case 2 failed: %v", err)
	}
	if out2.Proof().RouteSelector != "openai:gpt-4o" {
		t.Errorf("case 2 (inline prefix): got %q, want openai:gpt-4o", out2.Proof().RouteSelector)
	}

	// 3. Header selector empty, model has no recognized prefix -> default route selector used
	body3 := []byte(`{"model":"unknown-model-without-prefix","input":"Hello"}`)
	in3 := defaultProofInput(body3, "", nil)
	in3.DefaultRouteSelector = "stub:fallback-default"
	out3, err := prof.CompileProof(context.Background(), in3)
	if err != nil {
		t.Fatalf("case 3 failed: %v", err)
	}
	if out3.Proof().RouteSelector != "stub:fallback-default" {
		t.Errorf("case 3 (default selector): got %q, want stub:fallback-default", out3.Proof().RouteSelector)
	}

	// 4. Header selector empty, no inline prefix, no default selector -> decline
	body4 := []byte(`{"model":"bare-model","input":"Hello"}`)
	in4 := defaultProofInput(body4, "", nil)
	in4.DefaultRouteSelector = ""
	_, err = prof.CompileProof(context.Background(), in4)
	if err == nil {
		t.Fatal("case 4 (empty route selector): expected error/decline, got nil")
	}
}

// Requirement 4.2, 17.1: Streaming delivery and max_output_tokens.
func TestOpenAIResponsesProfile_CompileProof_StreamingAndMaxOutput(t *testing.T) {
	t.Parallel()

	prof := openairesponses.NewProfile()
	body := []byte(`{"model":"gpt-4o","input":"Generate poem","stream":true,"max_output_tokens":4096}`)
	in := defaultProofInput(body, "stub:gpt-4o", nil)

	out, err := prof.CompileProof(context.Background(), in)
	if err != nil {
		t.Fatalf("CompileProof failed: %v", err)
	}

	proof := out.Proof()
	if proof.Delivery != aipapi.DeliveryModeStreaming {
		t.Errorf("Proof.Delivery: got %v, want Streaming", proof.Delivery)
	}
	if proof.MaxOutputTokens != 4096 {
		t.Errorf("Proof.MaxOutputTokens: got %d, want 4096", proof.MaxOutputTokens)
	}
	if !out.Seeds().Stream {
		t.Errorf("Seeds.Stream: got false, want true")
	}

	// Exact Canonical Identity Parity
	canonDecoded, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{RouteSelector: "stub:gpt-4o"})
	if err != nil {
		t.Fatalf("canonical DecodeCreateRequest failed: %v", err)
	}
	wantDigest := largebody.CanonicalCallIdentity(canonDecoded.Call)
	if proof.Identity.Sum() != wantDigest.Sum() {
		t.Fatalf("Identity mismatch:\ngot:  %x\nwant: %x", proof.Identity.Sum(), wantDigest.Sum())
	}
}

// Requirement 14.1, 14.6: Authoritative session headers and sensitive resume token.
func TestOpenAIResponsesProfile_CompileProof_SessionHeaders(t *testing.T) {
	t.Parallel()

	prof := openairesponses.NewProfile()
	body := []byte(`{"model":"gpt-4o","input":"Continue session"}`)
	h := make(http.Header)
	h.Set(sessionwire.HeaderAuthoritativeSessionID, "sess_auth_999")
	h.Set(sessionwire.HeaderResumeToken, "secret_resume_token_abc")
	h.Set(sessionwire.HeaderALegID, "aleg_wire_777")
	in := defaultProofInput(body, "stub:gpt-4o", h)

	out, err := prof.CompileProof(context.Background(), in)
	if err != nil {
		t.Fatalf("CompileProof failed: %v", err)
	}

	proof := out.Proof()
	sess := proof.Session
	if sess.AuthoritativeSessionID != "sess_auth_999" {
		t.Errorf("Session.AuthoritativeSessionID: got %q, want sess_auth_999", sess.AuthoritativeSessionID)
	}
	if sess.ALegID != "aleg_wire_777" {
		t.Errorf("Session.ALegID: got %q, want aleg_wire_777", sess.ALegID)
	}
	if sess.ResumeToken.Reveal() != "secret_resume_token_abc" {
		t.Errorf("Session.ResumeToken.Reveal(): got %q, want secret_resume_token_abc", sess.ResumeToken.Reveal())
	}
	// Verify resume token renders as redacted in String() and json.Marshal()
	if sess.ResumeToken.String() != "[redacted]" {
		t.Errorf("ResumeToken.String(): got %q, want [redacted]", sess.ResumeToken.String())
	}
	sessJSON, _ := json.Marshal(sess)
	if strings.Contains(string(sessJSON), "secret_resume_token_abc") {
		t.Errorf("ResumeToken leaked into SessionInput JSON: %s", string(sessJSON))
	}

	// Seeds must carry CancellationID formatted carrier
	seeds := out.Seeds()
	expectedCarrier := frontendpipe.FormatOpenAICancellationCarrier("aleg_wire_777", "sess_auth_999")
	if seeds.CancellationID != expectedCarrier {
		t.Errorf("Seeds.CancellationID: got %q, want %q", seeds.CancellationID, expectedCarrier)
	}
	if seeds.SessionID != "sess_auth_999" {
		t.Errorf("Seeds.SessionID: got %q, want sess_auth_999", seeds.SessionID)
	}
	if seeds.ALegID != "aleg_wire_777" {
		t.Errorf("Seeds.ALegID: got %q, want aleg_wire_777", seeds.ALegID)
	}

	// Canonical Identity Parity
	canonDecoded, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
		RouteSelector: "stub:gpt-4o",
		Headers:       h,
	})
	if err != nil {
		t.Fatalf("canonical DecodeCreateRequest failed: %v", err)
	}
	wantDigest := largebody.CanonicalCallIdentity(canonDecoded.Call)
	if proof.Identity.Sum() != wantDigest.Sum() {
		t.Fatalf("Identity mismatch:\ngot:  %x\nwant: %x", proof.Identity.Sum(), wantDigest.Sum())
	}
}

// Requirement 4, 14.3, 16: Instructions and message array input items.
func TestOpenAIResponsesProfile_CompileProof_MessageArrayAndInstructions(t *testing.T) {
	t.Parallel()

	prof := openairesponses.NewProfile()
	body := []byte(`{
		"model": "gpt-4o",
		"instructions": "You are a helpful assistant.",
		"input": [
			{"type": "message", "role": "system", "content": "Context setup"},
			{"type": "message", "role": "user", "content": "Hello!"},
			{"type": "message", "role": "assistant", "content": "Hi there!"}
		]
	}`)
	in := defaultProofInput(body, "stub:gpt-4o", nil)

	out, err := prof.CompileProof(context.Background(), in)
	if err != nil {
		t.Fatalf("CompileProof failed: %v", err)
	}

	proof := out.Proof()
	// Turn shape must include instructions system message + 3 input messages
	if len(proof.Turn.Items) != 4 {
		t.Fatalf("Proof.Turn.Items count: got %d, want 4", len(proof.Turn.Items))
	}
	expectedRoles := []aipapi.Role{aipapi.RoleSystem, aipapi.RoleSystem, aipapi.RoleUser, aipapi.RoleAssistant}
	for i, expectedRole := range expectedRoles {
		if proof.Turn.Items[i].Role != expectedRole {
			t.Errorf("Turn.Items[%d].Role: got %v, want %v", i, proof.Turn.Items[i].Role, expectedRole)
		}
		if proof.Turn.Items[i].Ordinal != int64(i) {
			t.Errorf("Turn.Items[%d].Ordinal: got %d, want %d", i, proof.Turn.Items[i].Ordinal, i)
		}
	}

	// Canonical Identity Parity
	canonDecoded, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{RouteSelector: "stub:gpt-4o"})
	if err != nil {
		t.Fatalf("canonical DecodeCreateRequest failed: %v", err)
	}
	wantDigest := largebody.CanonicalCallIdentity(canonDecoded.Call)
	if proof.Identity.Sum() != wantDigest.Sum() {
		t.Fatalf("Identity mismatch:\ngot:  %x\nwant: %x", proof.Identity.Sum(), wantDigest.Sum())
	}
}

// Requirement 4, 16, 17: Tools, tool_choice, temperature, top_p, parallel_tool_calls, text.
func TestOpenAIResponsesProfile_CompileProof_ToolsAndOptions(t *testing.T) {
	t.Parallel()

	prof := openairesponses.NewProfile()
	body := []byte(`{
		"model": "gpt-4o",
		"input": "Call tool",
		"temperature": 0.5,
		"top_p": 0.9,
		"parallel_tool_calls": false,
		"text": {"verbosity": "low"},
		"tools": [
			{
				"type": "function",
				"name": "lookup",
				"description": "Look up data",
				"parameters": {"type": "object", "properties": {"q": {"type": "string"}}}
			}
		],
		"tool_choice": "auto"
	}`)
	in := defaultProofInput(body, "stub:gpt-4o", nil)

	out, err := prof.CompileProof(context.Background(), in)
	if err != nil {
		t.Fatalf("CompileProof failed: %v", err)
	}

	proof := out.Proof()

	// Canonical Identity Parity
	canonDecoded, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{RouteSelector: "stub:gpt-4o"})
	if err != nil {
		t.Fatalf("canonical DecodeCreateRequest failed: %v", err)
	}
	wantDigest := largebody.CanonicalCallIdentity(canonDecoded.Call)
	if proof.Identity.Sum() != wantDigest.Sum() {
		t.Fatalf("Identity mismatch:\ngot:  %x\nwant: %x", proof.Identity.Sum(), wantDigest.Sum())
	}
}

// Requirement 16.3, 17.1: Exact canonical identity differential corpus across all 9 canonical test cases.
func TestOpenAIResponsesProfile_CompileProof_ExactIdentityDifferentialCorpus(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		body    string
		headers http.Header
	}{
		{
			name: "simple string input",
			body: `{"model":"gpt-4o","input":"Hello, how are you today?"}`,
		},
		{
			name: "message array with system instruction",
			body: `{
				"model": "gpt-4o",
				"instructions": "You are a specialized code translation assistant.",
				"input": [
					{"type": "message", "role": "user", "content": "Translate this to Go"}
				]
			}`,
		},
		{
			name: "instructions with multi-paragraph system prompt",
			body: `{
				"model": "gpt-4o",
				"instructions": "System prompt paragraph 1.\n\nSystem prompt paragraph 2.",
				"input": "User query"
			}`,
		},
		{
			name: "function call and output items",
			body: `{
				"model": "gpt-4o",
				"input": [
					{"type": "function_call", "name": "lookup_weather", "call_id": "call_abc123", "arguments": "{\"city\":\"Zurich\"}"},
					{"type": "function_call_output", "call_id": "call_abc123", "output": "Sunny, 22C"}
				]
			}`,
		},
		{
			name: "tools with complex schema and auto tool choice",
			body: `{
				"model": "gpt-4o",
				"input": "Check stock price",
				"tools": [
					{
						"type": "function",
						"name": "get_stock_quote",
						"description": "Fetch real-time stock quote",
						"parameters": {
							"type": "object",
							"properties": {
								"ticker": {"type": "string", "description": "Stock symbol"},
								"extended_hours": {"type": "boolean"}
							},
							"required": ["ticker"]
						},
						"strict": true
					}
				],
				"tool_choice": "auto"
			}`,
		},
		{
			name: "tools with specific function choice and parallel false",
			body: `{
				"model": "gpt-4o",
				"input": "Run analysis",
				"tools": [
					{
						"type": "function",
						"name": "analyze",
						"parameters": {"type": "object"}
					}
				],
				"tool_choice": {"type": "function", "function": {"name": "analyze"}},
				"parallel_tool_calls": false
			}`,
		},
		{
			name: "options and text verbosity",
			body: `{
				"model": "gpt-4o",
				"input": "Provide concise summary",
				"temperature": 0.3,
				"top_p": 0.85,
				"max_output_tokens": 1500,
				"text": {"verbosity": "low"}
			}`,
		},
		{
			name: "metadata and authoritative session headers",
			body: `{
				"model": "gpt-4o",
				"input": "Session continuation",
				"metadata": {
					"client_session_id": "client-sess-42",
					"user_id": "usr-1234"
				}
			}`,
			headers: func() http.Header {
				h := make(http.Header)
				h.Set(sessionwire.HeaderAuthoritativeSessionID, "auth-sess-99")
				h.Set(sessionwire.HeaderALegID, "aleg-777")
				return h
			}(),
		},
		{
			name: "multilingual unicode and html escapes in prompt",
			body: `{
				"model": "gpt-4o",
				"input": "日本語: こんにちは世界! 中文: 你好，世界! العربية: مرحبا بالعالم! <div>HTML & safe \"quotes\" \\ backslash \u2028\u2029 🚀🌍🧪"
			}`,
		},
		{
			name: "padded string input with leading and trailing whitespace",
			body: `{
				"model": "gpt-4o",
				"input": "  \n\t  Hello, padded and trimmed differential!  \r\n  "
			}`,
		},
	}

	prof := openairesponses.NewProfile()

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			in := defaultProofInput([]byte(tc.body), "stub:gpt-4o", tc.headers)
			out, err := prof.CompileProof(context.Background(), in)
			if err != nil {
				t.Fatalf("CompileProof failed: %v", err)
			}

			canonDecoded, err := openairesponses.DecodeCreateRequest([]byte(tc.body), openairesponses.DecodeOptions{
				RouteSelector: "stub:gpt-4o",
				Headers:       tc.headers,
			})
			if err != nil {
				t.Fatalf("canonical DecodeCreateRequest failed: %v", err)
			}

			wantDigest := largebody.CanonicalCallIdentity(canonDecoded.Call)
			if out.Proof().Identity.Sum() != wantDigest.Sum() {
				t.Fatalf("[%s] Identity sum mismatch:\ngot:  %x\nwant: %x", tc.name, out.Proof().Identity.Sum(), wantDigest.Sum())
			}
		})
	}
}

// Requirement 4.4, 4.5, 4.6, 14.2, 17.2, 17.3, 17.5: Conservative decline cases.
func TestOpenAIResponsesProfile_DeclineToCanonical(t *testing.T) {
	t.Parallel()

	prof := openairesponses.NewProfile()

	testCases := []struct {
		name    string
		body    string
		path    string
		headers http.Header
	}{
		{
			name: "duplicate top-level model field",
			body: `{"model":"gpt-4o","model":"gpt-4o-mini","input":"hello"}`,
		},
		{
			name: "duplicate top-level input field",
			body: `{"model":"gpt-4o","input":"hello","input":"world"}`,
		},
		{
			name: "unknown top-level body field",
			body: `{"model":"gpt-4o","input":"hello","custom_unknown_field":"value"}`,
		},
		{
			name: "body AIProxer authoritative session ID metadata",
			body: `{"model":"gpt-4o","input":"hello","metadata":{"aip_session_id":"sess_client_1"}}`,
		},
		{
			name: "body AIProxer resume token metadata",
			body: `{"model":"gpt-4o","input":"hello","metadata":{"aip_resume_token":"tok_client_1"}}`,
		},
		{
			name: "unsupported continuation previous_response_id",
			body: `{"model":"gpt-4o","input":"hello","previous_response_id":"resp_prev_123"}`,
		},
		{
			name: "unsupported store control",
			body: `{"model":"gpt-4o","input":"hello","store":true}`,
		},
		{
			name: "prompt cache key requires canonical decode",
			body: `{"model":"gpt-4o","input":"hello","prompt_cache_key":"client-owned-key"}`,
		},
		{
			name: "unsupported truncation control",
			body: `{"model":"gpt-4o","input":"hello","truncation":"auto"}`,
		},
		{
			name: "repair-sensitive malformed history function_call with empty name",
			body: `{"model":"gpt-4o","input":[{"type":"function_call","name":"","call_id":"c1"}]}`,
		},
		{
			name: "repair-sensitive malformed history function_call_output with empty call_id",
			body: `{"model":"gpt-4o","input":[{"type":"function_call_output","call_id":"","output":"done"}]}`,
		},
		{
			name: "object-shaped function_call_output output requires canonical string encoding",
			body: `{"model":"gpt-4o","input":[{"type":"function_call_output","call_id":"c1","output":{"temp":72}}]}`,
		},
		{
			name: "empty model",
			body: `{"model":"","input":"hello"}`,
		},
		{
			name: "missing model",
			body: `{"input":"hello"}`,
		},
		{
			name: "empty input",
			body: `{"model":"gpt-4o","input":""}`,
		},
		{
			name: "missing input",
			body: `{"model":"gpt-4o"}`,
		},
		{
			name: "unsupported path",
			body: `{"model":"gpt-4o","input":"hello"}`,
			path: "/v1/chat/completions",
		},
		{
			name: "malformed JSON",
			body: `{"model":"gpt-4o","input":"hello"`,
		},
		{
			name:    "session hint requires canonical decode",
			body:    `{"model":"gpt-4o","input":"hello"}`,
			headers: http.Header{"X-Aip-Session-Hint": []string{"client-hint-123"}},
		},
		{
			name: "non-string instructions number declines",
			body: `{"model":"gpt-4o","input":"hello","instructions":12345}`,
		},
		{
			name: "non-string instructions boolean declines",
			body: `{"model":"gpt-4o","input":"hello","instructions":true}`,
		},
		{
			name: "non-string instructions array declines",
			body: `{"model":"gpt-4o","input":"hello","instructions":["invalid"]}`,
		},
		{
			name: "scalar tools string declines",
			body: `{"model":"gpt-4o","input":"hello","tools":"tool1"}`,
		},
		{
			name: "scalar tools number declines",
			body: `{"model":"gpt-4o","input":"hello","tools":42}`,
		},
		{
			name: "scalar text string declines",
			body: `{"model":"gpt-4o","input":"hello","text":"plain"}`,
		},
		{
			name: "scalar text number declines",
			body: `{"model":"gpt-4o","input":"hello","text":42}`,
		},
		{
			name: "scalar tool_choice number declines",
			body: `{"model":"gpt-4o","input":"hello","tool_choice":123}`,
		},
		{
			name: "scalar tool_choice boolean declines",
			body: `{"model":"gpt-4o","input":"hello","tool_choice":false}`,
		},
		{
			name: "wrong-typed stream string declines",
			body: `{"model":"gpt-4o","input":"hello","stream":"true"}`,
		},
		{
			name: "wrong-typed stream number declines",
			body: `{"model":"gpt-4o","input":"hello","stream":1}`,
		},
		{
			name: "wrong-typed parallel_tool_calls string declines",
			body: `{"model":"gpt-4o","input":"hello","parallel_tool_calls":"false"}`,
		},
		{
			name: "wrong-typed temperature string declines",
			body: `{"model":"gpt-4o","input":"hello","temperature":"warm"}`,
		},
		{
			name: "wrong-typed top_p string declines",
			body: `{"model":"gpt-4o","input":"hello","top_p":"high"}`,
		},
		{
			name: "wrong-typed max_output_tokens string declines",
			body: `{"model":"gpt-4o","input":"hello","max_output_tokens":"1000"}`,
		},
		{
			name: "wrong-typed max_output_tokens float declines",
			body: `{"model":"gpt-4o","input":"hello","max_output_tokens":100.5}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			path := tc.path
			if path == "" {
				path = "/v1/responses"
			}
			in := defaultProofInput([]byte(tc.body), "stub:gpt-4o", tc.headers)
			in.URLPath = path

			_, err := prof.CompileProof(context.Background(), in)
			if err == nil {
				t.Fatalf("[%s] expected CompileProof to decline (return error), but got nil", tc.name)
			}
		})
	}
}

// Requirement 1, 6, 17: Handler integration with candidate fast path and clean fallback.
func TestOpenAIResponsesProfile_HandlerIntegration(t *testing.T) {
	t.Parallel()

	var candidateProofCalled bool
	var proofOutcome frontendpipe.CandidateProofResult
	var executedCall *aipapi.Call

	exec := &testExecView{
		onExecute: func(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
			executedCall = call
			return aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventMessageStarted},
				{Kind: aipapi.EventResponseFinished},
			}), nil
		},
	}

	h := &openairesponses.Handler{
		Exec:                 exec,
		DefaultRouteSelector: "stub:gpt-4o",
	}

	// We verify that an eligible request runs candidate proof and a declining request
	// executes canonically with no error.
	spec := h.Spec()
	spec.LargePayload = frontendpipe.LargePayloadConfig{
		Enabled:        true,
		ThresholdBytes: 20, // low threshold for testing
	}
	spec.OnCandidateProof = func(r *http.Request, res frontendpipe.CandidateProofResult) {
		candidateProofCalled = true
		proofOutcome = res
	}

	// 1. Send request with duplicate keys: should decline proof and fallback to canonical
	bodyDecline := `{"model":"gpt-4o","model":"gpt-4o-mini","input":"Hello fallback"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(bodyDecline))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	frontendpipe.ServeHTTP(spec, rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200 on fallback, got %d: %s", rec.Code, rec.Body.String())
	}
	if !candidateProofCalled {
		t.Fatal("expected candidate proof to be called")
	}
	if proofOutcome.Err == nil {
		t.Fatal("expected proof outcome to have error for duplicate keys")
	}
	if executedCall == nil {
		t.Fatal("expected canonical Spec.Decode to execute call on fallback")
	}
}

type testExecView struct {
	onExecute         func(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error)
	assessCalls       atomic.Int64
	executeLargeCalls atomic.Int64
}

func (e *testExecView) Execute(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
	if e.onExecute != nil {
		return e.onExecute(ctx, call)
	}
	return aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventResponseFinished},
	}), nil
}

func (e *testExecView) CancelALeg(ctx context.Context, req aipapi.ALegCancelRequest) error {
	return nil
}

func (e *testExecView) WallClock() func() time.Time {
	return nil
}

func (e *testExecView) AssessLargeBody(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
	e.assessCalls.Add(1)
	return largebody.Assessment{}, nil
}

func (e *testExecView) ExecuteLargeBody(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
	e.executeLargeCalls.Add(1)
	return largebody.ExecutionResult{}, nil
}

var (
	_ aipsdk.ExecutorView         = (*testExecView)(nil)
	_ largebody.LargeBodyExecutor = (*testExecView)(nil)
)

func test1MiBResponsesBody(tb testing.TB, target int) []byte {
	tb.Helper()
	const prefix = `{"model":"gpt-4o","input":"`
	const suffix = `"}`
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

// Task 19.3 / Remediation Phase 2 (Lane 1):
// Proof-time transient allocation (B/op) must be bounded by
// memory_spool_bytes (64 KiB) + max_semantic_fact_bytes (256 KiB) + fixed buffers (128 KiB) = 448 KiB.
func TestOpenAIResponsesProfile_CompileProof_TransientAllocBounded(t *testing.T) {
	const target = 1 << 20 // 1 MiB
	body := test1MiBResponsesBody(t, target)

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

	prof := openairesponses.NewProfile()
	proofIn := frontendpipe.ProofInput{
		Ctx:                  context.Background(),
		Headers:              make(http.Header),
		URLPath:              "/v1/responses",
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

	t.Logf("[openai_responses/1MiB] proof-time transient: %d B/op (ceiling: %d B/op)",
		allocBytesPerOp, maxAllowedProofBytesPerOp)

	if allocBytesPerOp > maxAllowedProofBytesPerOp {
		t.Fatalf("proof-time B/op (%d B) must be bounded by %d B, but exceeded target invariant by %d B",
			allocBytesPerOp, maxAllowedProofBytesPerOp, allocBytesPerOp-maxAllowedProofBytesPerOp)
	}
}

// Item 2: Large array differential with mixed roles (system, user, assistant).
// Payload exceeds 2*DefaultMaxSemanticFactBytes (512 KiB) to trigger the streaming large array path.
func TestOpenAIResponsesProfile_CompileProof_MixedRoleLargeArrayDifferential(t *testing.T) {
	t.Parallel()

	prof := openairesponses.NewProfile()
	// Build an array with mixed roles exceeding 512 KiB
	chunkU := strings.Repeat("u", 280*1024)
	chunkV := strings.Repeat("v", 280*1024)

	var sb strings.Builder
	sb.WriteString(`{"model":"gpt-4o","input":[`)
	sb.WriteString(`{"type":"message","role":"system","content":"System prompt initialization."}`)
	sb.WriteString(`,{"type":"message","role":"user","content":"` + chunkU + `"}`)
	sb.WriteString(`,{"type":"message","role":"assistant","content":"Understood, processing your data."}`)
	sb.WriteString(`,{"type":"message","role":"user","content":"` + chunkV + `"}`)
	sb.WriteString(`]}`)

	body := []byte(sb.String())
	in := defaultProofInput(body, "stub:gpt-4o", nil)

	out, err := prof.CompileProof(context.Background(), in)
	if err != nil {
		t.Fatalf("CompileProof failed for mixed-role large array: %v", err)
	}

	proof := out.Proof()

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
	canonDecoded, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
		RouteSelector: "stub:gpt-4o",
	})
	if err != nil {
		t.Fatalf("canonical DecodeCreateRequest failed: %v", err)
	}
	wantDigest := largebody.CanonicalCallIdentity(canonDecoded.Call)
	if proof.Identity.Sum() != wantDigest.Sum() {
		t.Fatalf("Identity mismatch for mixed-role large array:\ngot:  %x\nwant: %x", proof.Identity.Sum(), wantDigest.Sum())
	}
}

// Item 4: Envelope field (instructions) exceeding DefaultMaxSemanticFactBytes (256 KiB)
// must be bounded and return ErrSemanticFactBudgetExceeded.
func TestOpenAIResponsesProfile_CompileProof_EnvelopeFactBudget(t *testing.T) {
	t.Parallel()

	prof := openairesponses.NewProfile()
	// Create instructions larger than 256 KiB budget (e.g. 270 KiB)
	largeInstructions := strings.Repeat("i", 270*1024)
	body := fmt.Appendf(nil, `{"model":"gpt-4o","instructions":"%s","input":"Hello"}`, largeInstructions)
	in := defaultProofInput(body, "stub:gpt-4o", nil)

	_, err := prof.CompileProof(context.Background(), in)
	if err == nil {
		t.Fatal("expected error for instructions exceeding fact budget, got nil")
	}
	if !errors.Is(err, largebody.ErrSemanticFactBudgetExceeded) {
		t.Fatalf("expected ErrSemanticFactBudgetExceeded, got: %v", err)
	}
}

// MF-1: Padded array content differential.
// Large array payload (> 512 KiB) where content strings have leading and trailing whitespace.
// Canonical decode trims whitespace via strings.TrimSpace; streaming proof must match byte-for-byte.
func TestOpenAIResponsesProfile_CompileProof_PaddedLargeArrayDifferential(t *testing.T) {
	t.Parallel()

	prof := openairesponses.NewProfile()
	// Large array exceeding 512 KiB with padded message contents
	chunkU := "  \\n\\t  " + strings.Repeat("u", 280*1024) + "  \\r\\n  "
	chunkV := " \\t " + strings.Repeat("v", 280*1024) + " \\n "

	var sb strings.Builder
	sb.WriteString(`{"model":"gpt-4o","input":[`)
	sb.WriteString(`{"type":"message","role":"system","content":"  \\nSystem prompt initialization.  \\t"}`)
	sb.WriteString(`,{"type":"message","role":"user","content":"` + chunkU + `"}`)
	sb.WriteString(`,{"type":"message","role":"assistant","content":"  Understood, processing your data.  \\r\\n"}`)
	sb.WriteString(`,{"type":"message","role":"user","content":"` + chunkV + `"}`)
	sb.WriteString(`]}`)

	body := []byte(sb.String())
	in := defaultProofInput(body, "stub:gpt-4o", nil)

	out, err := prof.CompileProof(context.Background(), in)
	if err != nil {
		t.Fatalf("CompileProof failed for padded large array: %v", err)
	}

	proof := out.Proof()

	canonDecoded, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
		RouteSelector: "stub:gpt-4o",
	})
	if err != nil {
		t.Fatalf("canonical DecodeCreateRequest failed: %v", err)
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

// MF-2: Number tokens spanning the 32 KiB chunk boundary.
// Tests that options (max_output_tokens, temperature, top_p) whose number tokens straddle
// offset 32768 are correctly extracted and match canonical identity.
func TestOpenAIResponsesProfile_CompileProof_ChunkBoundaryNumbersDifferential(t *testing.T) {
	t.Parallel()

	prof := openairesponses.NewProfile()

	cases := []struct {
		name       string
		optionKey  string
		numLiteral string
		verify     func(t *testing.T, proof largebody.Proof, canon *aipapi.Call)
	}{
		{
			name:       "max_output_tokens straddling 32768",
			optionKey:  "max_output_tokens",
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
			prefix1 := `{"model":"gpt-4o","instructions":"`
			suffix1 := `","` + tc.optionKey + `":`
			padLen := targetOffset - len(prefix1) - len(suffix1)
			if padLen < 0 {
				t.Fatalf("padLen %d < 0", padLen)
			}
			pad := strings.Repeat("x", padLen)
			tail := `,"input":"hello"}`

			body := []byte(prefix1 + pad + suffix1 + tc.numLiteral + tail)

			// Verify that the number token actually straddles offset 32768:
			numStart := len(prefix1) + padLen + len(suffix1)
			numEnd := numStart + len(tc.numLiteral)
			if numStart >= 32768 || numEnd <= 32768 {
				t.Fatalf("test setup error: number span [%d, %d] does not straddle 32768", numStart, numEnd)
			}

			in := defaultProofInput(body, "stub:gpt-4o", nil)
			out, err := prof.CompileProof(context.Background(), in)
			if err != nil {
				t.Fatalf("CompileProof failed: %v", err)
			}

			proof := out.Proof()

			canonDecoded, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
				RouteSelector: "stub:gpt-4o",
			})
			if err != nil {
				t.Fatalf("canonical DecodeCreateRequest failed: %v", err)
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

// Requirement 1.1, 1.2 (large-payload-proof-and-parity): nested role/type metadata
// shares the single before-write envelope budget. Oversize nested strings
// decline with the semantic-budget failure instead of bypassing retention;
// whitespace padding and JSON escapes count decoded bytes without discount;
// cumulative items after buffer resets keep charging; exact limit still accepts.
func TestOpenAIResponsesProfile_NestedMetadataBudget(t *testing.T) {
	t.Parallel()

	prof := openairesponses.NewProfile()
	compileWithBudget := func(body []byte, budget int64) (frontendpipe.ProofOutput, error) {
		in := defaultProofInput(body, "stub:gpt-4o", nil)
		in.Ctx = largebody.WithSemanticFactBudget(context.Background(), budget)
		return prof.CompileProof(context.Background(), in)
	}

	// Oversized nested role: budget must win over role validation. The input
	// spans replay chunks but the tiny budget declines inside the first
	// 32 KiB feed; the chunk-split case below carries the cross-boundary
	// accounting claim with remaining budget past the boundary.
	bigRole := strings.Repeat("x", 40*1024)
	bigBody := []byte(`{"model":"gpt-4o","input":[{"type":"message","role":"` + bigRole + `","content":"hi"}]}`)
	if _, err := compileWithBudget(bigBody, 100); !errors.Is(err, largebody.ErrSemanticFactBudgetExceeded) {
		t.Fatalf("oversized role: expected budget error, got %v", err)
	}

	// Whitespace-padded valid role: 200 spaces + "user" decodes to 204 bytes
	// against model(6) + type(7), so budget 217 accepts and 216 declines.
	paddedRole := strings.Repeat(" ", 200) + "user"
	paddedBody := []byte(`{"model":"gpt-4o","input":[{"type":"message","role":"` + paddedRole + `","content":"hi"}]}`)
	if _, err := compileWithBudget(paddedBody, 216); !errors.Is(err, largebody.ErrSemanticFactBudgetExceeded) {
		t.Fatalf("padded role limit+1: expected budget error, got %v", err)
	}
	atLimit, err := compileWithBudget(paddedBody, 217)
	if err != nil {
		t.Fatalf("padded role at limit: unexpected error: %v", err)
	}

	// Escaped padding decodes to the same bytes: same identity, same budget.
	escapedRole := strings.Repeat(`\u0020`, 200) + "user"
	escapedBody := []byte(`{"model":"gpt-4o","input":[{"type":"message","role":"` + escapedRole + `","content":"hi"}]}`)
	if _, err := compileWithBudget(escapedBody, 216); !errors.Is(err, largebody.ErrSemanticFactBudgetExceeded) {
		t.Fatalf("escaped role limit+1: expected budget error, got %v", err)
	}
	escapedOK, err := compileWithBudget(escapedBody, 217)
	if err != nil {
		t.Fatalf("escaped role at limit: unexpected error: %v", err)
	}
	if atLimit.Proof().Identity.Sum() != escapedOK.Proof().Identity.Sum() {
		t.Fatal("escaped vs literal whitespace role identity mismatch")
	}

	// Cumulative small items after buffer resets: 3 items with 44-byte padded
	// roles (132) + types (21) + model (6) = 159 against budget 150.
	var sb strings.Builder
	sb.WriteString(`{"model":"gpt-4o","input":[`)
	for i := range 3 {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`{"type":"message","role":"` + strings.Repeat(" ", 40) + `user","content":"hi"}`)
	}
	sb.WriteString(`]}`)
	if _, err := compileWithBudget([]byte(sb.String()), 150); !errors.Is(err, largebody.ErrSemanticFactBudgetExceeded) {
		t.Fatalf("cumulative roles: expected budget error, got %v", err)
	}

	// Chunk-split decoded accounting: a valid whitespace-padded role whose raw
	// bytes cross the 32 KiB replay chunk boundary with a JSON escape
	// straddling the boundary. The scanner feeds decoded bytes incrementally
	// and keeps escape state across feeds, so the second chunk's writes must
	// still charge against the remaining budget: exact decoded limit accepts,
	// limit+1 declines past the boundary, and escaped vs literal padding prove
	// the same canonical identity.
	const replayChunkBytes = 32 * 1024
	splitPrefix := `{"model":"gpt-4o","input":[{"type":"message","role":"`
	splitSuffix := `","content":"hi"}]}`
	// Start the 6-byte \u0020 escape two raw bytes before the boundary so it
	// straddles offsets 32766..32772.
	escapeAt := replayChunkBytes - 2 - len(splitPrefix)
	splitTail := strings.Repeat(" ", 1200) + "user"
	splitBody := []byte(splitPrefix + strings.Repeat(" ", escapeAt) + `\u0020` + splitTail + splitSuffix)
	literalBody := []byte(splitPrefix + strings.Repeat(" ", escapeAt+1) + splitTail + splitSuffix)
	// Decoded role is escapeAt spaces + 1 (escape) + tail; the charged total
	// adds model(6) + type(7).
	splitTotal := int64(6 + 7 + escapeAt + 1 + len(splitTail))
	if _, err := compileWithBudget(splitBody, splitTotal-1); !errors.Is(err, largebody.ErrSemanticFactBudgetExceeded) {
		t.Fatalf("chunk-split role limit+1: expected budget error, got %v", err)
	}
	splitOK, err := compileWithBudget(splitBody, splitTotal)
	if err != nil {
		t.Fatalf("chunk-split role at limit: unexpected error: %v", err)
	}
	literalOK, err := compileWithBudget(literalBody, splitTotal)
	if err != nil {
		t.Fatalf("chunk-split literal role at limit: unexpected error: %v", err)
	}
	if splitOK.Proof().Identity.Sum() != literalOK.Proof().Identity.Sum() {
		t.Fatal("chunk-split escaped vs literal role identity mismatch")
	}
}

// Requirement 1.1, 1.2: raw envelope capture charges before retaining.
// Tools raw bytes join the same cumulative budget: exact limit accepts,
// limit+1 declines without retaining the excess.
func TestOpenAIResponsesProfile_RawEnvelopeBudgetBoundary(t *testing.T) {
	t.Parallel()

	prof := openairesponses.NewProfile()
	arr := `[{"type":"function","name":"lookup","description":"` + strings.Repeat("d", 64) + `"}]`
	body := []byte(`{"model":"gpt-4o","tools":` + arr + `,"input":"hi"}`)
	// Retained metadata: model "gpt-4o" (6 decoded bytes) + tools raw slice.
	wantTotal := int64(6 + len(arr))
	compileWithBudget := func(budget int64) (frontendpipe.ProofOutput, error) {
		in := defaultProofInput(body, "stub:gpt-4o", nil)
		in.Ctx = largebody.WithSemanticFactBudget(context.Background(), budget)
		return prof.CompileProof(context.Background(), in)
	}
	if _, err := compileWithBudget(wantTotal - 1); !errors.Is(err, largebody.ErrSemanticFactBudgetExceeded) {
		t.Fatalf("raw envelope limit+1: expected budget error, got %v", err)
	}
	if _, err := compileWithBudget(wantTotal); err != nil {
		t.Fatalf("raw envelope at limit: unexpected error: %v", err)
	}

	// Chunk-spanning raw capture: a tools array crossing the 32 KiB replay
	// chunk boundary keeps charging each chunk-hook slice against the same
	// remaining budget through the shared before-write leaf.
	bigArr := `[{"type":"function","name":"lookup","description":"` + strings.Repeat("d", 40*1024) + `"}]`
	bigBody := []byte(`{"model":"gpt-4o","tools":` + bigArr + `,"input":"hi"}`)
	bigWant := int64(6 + len(bigArr))
	compileBigWithBudget := func(budget int64) (frontendpipe.ProofOutput, error) {
		in := defaultProofInput(bigBody, "stub:gpt-4o", nil)
		in.Ctx = largebody.WithSemanticFactBudget(context.Background(), budget)
		return prof.CompileProof(context.Background(), in)
	}
	if _, err := compileBigWithBudget(bigWant - 1); !errors.Is(err, largebody.ErrSemanticFactBudgetExceeded) {
		t.Fatalf("chunk-spanning raw envelope limit+1: expected budget error, got %v", err)
	}
	if _, err := compileBigWithBudget(bigWant); err != nil {
		t.Fatalf("chunk-spanning raw envelope at limit: unexpected error: %v", err)
	}
}

// trackingDecodeAdmission records decode-admission acquisition and release so
// serving tests can pin the same-permit fallback contract: exactly one
// acquisition, still held through canonical Decode, exactly one release, and
// no second admission decision. It mirrors the frontendpipe_test tracking
// limiter, which this external test package cannot import.
type trackingDecodeAdmission struct {
	mu       sync.Mutex
	acquires []int64
	inflight int64
	releases int
}

func (l *trackingDecodeAdmission) TryAcquire(_ context.Context, weight int64) (func(), bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.acquires = append(l.acquires, weight)
	l.inflight += weight
	var released bool
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		if !released {
			released = true
			l.inflight -= weight
			l.releases++
		}
	}, true, nil
}

func (l *trackingDecodeAdmission) callCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.acquires)
}

func (l *trackingDecodeAdmission) inflightBytes() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.inflight
}

func (l *trackingDecodeAdmission) releaseCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.releases
}

// Requirement 1.3 (large-payload-proof-and-parity): a before-write metadata
// budget decline keeps the existing same-admission canonical fallback: one
// held decode admission, zero wire backend opens, one canonical execution.
func TestOpenAIResponsesProfile_BudgetDeclineFallsBackToCanonical(t *testing.T) {
	t.Parallel()

	admission := &trackingDecodeAdmission{}
	var candidateProofCalled bool
	var proofOutcome frontendpipe.CandidateProofResult
	var canonicalCalls int
	var wireCommits int

	exec := &testExecView{
		onExecute: func(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
			canonicalCalls++
			return aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventMessageStarted},
				{Kind: aipapi.EventResponseFinished},
			}), nil
		},
	}

	h := &openairesponses.Handler{
		Exec:                 exec,
		DefaultRouteSelector: "stub:gpt-4o",
		DecodeAdmission:      admission,
	}
	spec := h.Spec()
	spec.LargePayload = frontendpipe.LargePayloadConfig{
		Enabled:        true,
		ThresholdBytes: 20,
	}
	spec.OnCandidateProof = func(r *http.Request, res frontendpipe.CandidateProofResult) {
		candidateProofCalled = true
		proofOutcome = res
	}
	spec.OnWireCommit = func(r *http.Request, _ frontendpipe.WireCommitResult) {
		wireCommits++
	}
	// Observe the canonical fallback Decode while the test owns the spec: the
	// production Decode still runs, but the wrapper records whether the single
	// admission permit is still held when it does.
	origDecode := spec.Decode
	var heldDuringDecode bool
	var decodeCalls int
	spec.Decode = func(dctx frontendpipe.DecodeContext) (*frontendpipe.Decoded, error) {
		decodeCalls++
		heldDuringDecode = admission.inflightBytes() > 0
		return origDecode(dctx)
	}

	// Envelope instructions above the default 256 KiB semantic-fact budget.
	largeInstructions := strings.Repeat("i", 270*1024)
	bodyDecline := fmt.Sprintf(`{"model":"gpt-4o","instructions":%q,"input":"Hello fallback"}`, largeInstructions)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(bodyDecline))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	frontendpipe.ServeHTTP(spec, rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200 on budget-decline fallback, got %d: %s", rec.Code, rec.Body.String())
	}
	if !candidateProofCalled {
		t.Fatal("expected candidate proof to be called")
	}
	if !errors.Is(proofOutcome.Err, largebody.ErrSemanticFactBudgetExceeded) {
		t.Fatalf("expected proof outcome to wrap budget failure, got %v", proofOutcome.Err)
	}
	if !proofOutcome.PermitHeld {
		t.Fatal("expected decode admission still held at proof decline")
	}
	// Exactly one admission acquisition for the whole request: no second
	// TryAdmit for the canonical fallback.
	if admission.callCount() != 1 {
		t.Fatalf("admission acquisitions = %d, want exactly 1", admission.callCount())
	}
	// The canonical fallback Decode runs once under the still-held permit.
	if decodeCalls != 1 {
		t.Fatalf("canonical Decode calls = %d, want exactly 1", decodeCalls)
	}
	if !heldDuringDecode {
		t.Fatal("expected decode admission held through canonical Decode")
	}
	// The permit is released exactly once at the post-decode boundary.
	if admission.releaseCount() != 1 {
		t.Fatalf("admission releases = %d, want exactly 1", admission.releaseCount())
	}
	if admission.inflightBytes() != 0 {
		t.Fatalf("admission inflight = %d, want 0 after release", admission.inflightBytes())
	}
	// Declined proof never reaches assessment or wire execution:
	// ExecuteLargeBody is the only wire/backend-open seam on this serving
	// path, so zero calls means zero wire backend opens.
	if exec.assessCalls.Load() != 0 {
		t.Fatalf("AssessLargeBody calls = %d, want 0 on proof decline", exec.assessCalls.Load())
	}
	if exec.executeLargeCalls.Load() != 0 {
		t.Fatalf("ExecuteLargeBody calls = %d, want 0 on proof decline", exec.executeLargeCalls.Load())
	}
	if wireCommits != 0 {
		t.Fatalf("wire commits = %d, want 0 on proof decline", wireCommits)
	}
	// Exactly one canonical execution carries the fallback.
	if canonicalCalls != 1 {
		t.Fatalf("canonical executions = %d, want exactly 1", canonicalCalls)
	}
}
