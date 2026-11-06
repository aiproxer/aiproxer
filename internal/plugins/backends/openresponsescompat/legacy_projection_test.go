package openresponsescompat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func openResponsesCreateInvocation() aipapi.Invocation {
	return aipapi.Invocation{
		Operation:     aipapi.OperationOpenResponsesCreate,
		DeliveryMode:  aipapi.DeliveryModeNonStreaming,
		TransportMode: aipapi.TransportModeNonStreaming,
	}
}

// legacyFamilyFixture is one canonical legacy message-authority call as produced
// by a bundled frontend family. Fixtures identify the source family only for
// evidence; the backend consumes every family through the single explicit
// legacy→ordered-items projector (no pairwise translator code).
type legacyFamilyFixture struct {
	source     string
	call       aipapi.Call
	wantTypes  []string
	wantRoles  []string
	wantTools  int
	wantImage  bool
	wantCallID string
	wantOutput string
	wantTemp   *float64
	wantMax    *int
}

func legacyFamilyFixtures() []legacyFamilyFixture {
	// openai-chat: system in Messages, PartJSON tool calls, RoleTool results.
	chatCall := aipapi.Call{
		Invocation: openResponsesCreateInvocation(),
		Messages: []aipapi.Message{
			{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("You are a helpful assistant.")}},
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("What is the weather in Paris?")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{
				Kind:       aipapi.PartJSON,
				ToolCallID: "call_abc",
				ToolName:   "get_weather",
				Content:    json.RawMessage(`{"location":"Paris"}`),
			}}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{{
				Kind:       aipapi.PartToolResult,
				ToolCallID: "call_abc",
				ToolName:   "get_weather",
				Text:       "Sunny 22C",
			}}},
		},
		Tools: []aipapi.ToolDef{{
			Name:        "get_weather",
			Description: "Get the current weather",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"location":{"type":"string"}}}`),
		}},
		Options: aipapi.GenerationOptions{Temperature: new(0.7)},
		Extensions: map[string]json.RawMessage{
			"openailegacy.model": json.RawMessage(`"gpt-4o-mini"`),
		},
	}

	// openai-responses: system in Instructions, PartJSON tool calls, RoleTool results.
	responsesCall := aipapi.Call{
		Invocation:   openResponsesCreateInvocation(),
		Instructions: []aipapi.Message{{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("You are concise.")}}},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{
				Kind:       aipapi.PartJSON,
				ToolCallID: "call_r1",
				ToolName:   "lookup",
				Content:    json.RawMessage(`{"q":"codex"}`),
			}}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{{
				Kind:       aipapi.PartToolResult,
				ToolCallID: "call_r1",
				ToolName:   "lookup",
				Text:       `{"hits":2}`,
			}}},
		},
		Extensions: map[string]json.RawMessage{
			"openairesponses.model": json.RawMessage(`"gpt-5"`),
		},
	}

	// anthropic: system in Instructions, user/assistant text plus an image part.
	anthropicCall := aipapi.Call{
		Invocation:   openResponsesCreateInvocation(),
		Instructions: []aipapi.Message{{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("Be brief.")}}},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("ping")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("pong")}},
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{{
				Kind:      aipapi.PartImageRef,
				ImageRef:  "data:image/png;base64,AAAA",
				ImageMIME: "image/png",
			}}},
		},
		Options: aipapi.GenerationOptions{MaxOutputTokens: new(64)},
		Extensions: map[string]json.RawMessage{
			"anthropic.model": json.RawMessage(`"claude-3-5-haiku-20241022"`),
		},
	}

	// gemini: system in Instructions, user text+inline image, model text.
	geminiCall := aipapi.Call{
		Invocation:   openResponsesCreateInvocation(),
		Instructions: []aipapi.Message{{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("answer tersely")}}},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{
				aipapi.TextPart("describe this"),
				{Kind: aipapi.PartImageRef, ImageRef: "data:image/jpeg;base64,BBBB", ImageMIME: "image/jpeg"},
			}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("a green field")}},
		},
		Extensions: map[string]json.RawMessage{
			"gemini.model": json.RawMessage(`"gemini-2.0-flash"`),
		},
	}

	return []legacyFamilyFixture{
		{
			source:     "openai-chat",
			call:       chatCall,
			wantTypes:  []string{"message", "message", "function_call", "function_call_output"},
			wantRoles:  []string{"system", "user", "", ""},
			wantTools:  1,
			wantCallID: "call_abc",
			wantOutput: "Sunny 22C",
			wantTemp:   new(0.7),
		},
		{
			source:     "openai-responses",
			call:       responsesCall,
			wantTypes:  []string{"message", "message", "function_call", "function_call_output"},
			wantRoles:  []string{"system", "user", "", ""},
			wantCallID: "call_r1",
			wantOutput: `{"hits":2}`,
		},
		{
			source:    "anthropic",
			call:      anthropicCall,
			wantTypes: []string{"message", "message", "message", "message"},
			wantRoles: []string{"system", "user", "assistant", "user"},
			wantImage: true,
			wantMax:   new(64),
		},
		{
			source:    "gemini",
			call:      geminiCall,
			wantTypes: []string{"message", "message", "message"},
			wantRoles: []string{"system", "user", "assistant"},
			wantImage: true,
		},
	}
}

func TestLegacyAuthority_Open_ProjectsFourFamiliesIntoOrderedRequests(t *testing.T) {
	for _, tc := range legacyFamilyFixtures() {
		t.Run(tc.source, func(t *testing.T) {
			t.Parallel()
			be, obs := newObserverBackend(t, "", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, completeResourceJSON)
			})

			before := snapshotCall(tc.call)
			es, err := be.Open(context.Background(), tc.call, routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
			if err != nil {
				t.Fatal(err)
			}
			_ = drainManagedEvents(t, es)
			assertSnapshotUnchanged(t, before, tc.call)

			if obs.count() != 1 {
				t.Fatalf("observer request count = %d, want exactly 1", obs.count())
			}
			req := obs.last(t)
			if req.Method != http.MethodPost || req.Path != "/responses" {
				t.Fatalf("method/path = %s %s, want POST /responses", req.Method, req.Path)
			}
			if string(req.Body) == "" {
				t.Fatal("empty request body")
			}

			var payload map[string]json.RawMessage
			if err := json.Unmarshal(req.Body, &payload); err != nil {
				t.Fatalf("body is not valid JSON: %v body=%s", err, string(req.Body))
			}
			if got := string(payload["model"]); got != `"model-x"` {
				t.Fatalf("model = %s, want model-x", got)
			}
			assertOrderedInput(t, payload, tc.wantTypes, tc.wantRoles)

			var tools []json.RawMessage
			if len(payload["tools"]) > 0 {
				if err := json.Unmarshal(payload["tools"], &tools); err != nil {
					t.Fatalf("tools unmarshal: %v", err)
				}
			}
			if len(tools) != tc.wantTools {
				t.Fatalf("tools count = %d, want %d", len(tools), tc.wantTools)
			}

			body := string(req.Body)
			if tc.wantImage && !strings.Contains(body, `"input_image"`) {
				t.Fatalf("multimodal image part missing from ordered request: %s", body)
			}
			if tc.wantCallID != "" && !strings.Contains(body, `"call_id":"`+tc.wantCallID+`"`) {
				t.Fatalf("tool call id %q missing from ordered request: %s", tc.wantCallID, body)
			}
			if tc.wantOutput != "" {
				assertToolResultOutput(t, payload, tc.wantOutput)
			}
			if tc.wantTemp != nil {
				if got := string(payload["temperature"]); got != "0.7" {
					t.Fatalf("temperature = %s, want 0.7", got)
				}
			}
			if tc.wantMax != nil {
				if got := string(payload["max_output_tokens"]); got != "64" {
					t.Fatalf("max_output_tokens = %s, want 64", got)
				}
			}

			for _, forbidden := range []string{
				"previous_response_id", `"store"`, `"stream"`, `"background"`,
				"openailegacy.model", "openairesponses.model", "anthropic.model", "gemini.model",
				"proxy_call", "client-session", "auth-session", "resume_secret",
			} {
				if bodyHasForbiddenField(req.Body, forbidden) {
					t.Fatalf("request forwarded forbidden field %q: %s", forbidden, string(req.Body))
				}
			}
		})
	}
}

func TestLegacyAuthority_ProjectedItemsCarryExactOrderedFields(t *testing.T) {
	t.Parallel()
	be, obs := newObserverBackend(t, "", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, completeResourceJSON)
	})
	call := legacyFamilyFixtures()[0].call // openai-chat family
	es, err := be.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
	if err != nil {
		t.Fatal(err)
	}
	_ = drainManagedEvents(t, es)

	req := obs.last(t)
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(req.Body, &payload); err != nil {
		t.Fatal(err)
	}
	var input []map[string]json.RawMessage
	if err := json.Unmarshal(payload["input"], &input); err != nil {
		t.Fatal(err)
	}
	if len(input) != 4 {
		t.Fatalf("input items = %d, want 4", len(input))
	}
	if got := string(input[0]["content"]); got != `[{"type":"input_text","text":"You are a helpful assistant."}]` {
		t.Fatalf("system message content = %s", got)
	}
	if got := string(input[2]["arguments"]); got != `"{\"location\":\"Paris\"}"` {
		t.Fatalf("arguments must be a JSON string on the wire, got %s", got)
	}
	if got := string(input[2]["name"]); got != `"get_weather"` {
		t.Fatalf("tool call name = %s", got)
	}
	if got := string(input[3]["output"]); got != `"Sunny 22C"` {
		t.Fatalf("tool result output = %s", got)
	}
	if string(payload["tools"]) == "" {
		t.Fatal("tools must be forwarded when the legacy call declares them")
	}
}

// TestLegacyAuthority_ResponsesFrontendArgumentsReachWireExactly decodes a real
// Responses function_call item through the shipped frontend and proves the
// compatible backend forwards its argument text byte-for-byte, including JSON
// string documents and surrounding document whitespace.
func TestLegacyAuthority_ResponsesFrontendArgumentsReachWireExactly(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		wireArguments string
		want          string
	}{
		{name: "object document", wireArguments: `"{\"target\":\"nope\"}"`, want: `"{\"target\":\"nope\"}"`},
		{name: "whitespace object document", wireArguments: `"{ \"a\" : 1 }"`, want: `"{ \"a\" : 1 }"`},
		{name: "string document", wireArguments: `"\"hello\""`, want: `"\"hello\""`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := []byte(`{"model":"m","input":[{"type":"function_call","call_id":"c1","name":"t","arguments":` + tc.wireArguments + `}]}`)
			decoded, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
				RouteSelector: "stub:m",
			})
			if err != nil {
				t.Fatal(err)
			}
			be, obs := newObserverBackend(t, "", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, completeResourceJSON)
			})
			es, err := be.Open(context.Background(), *decoded.Call, routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
			if err != nil {
				t.Fatal(err)
			}
			_ = drainManagedEvents(t, es)

			req := obs.last(t)
			var payload struct {
				Input []map[string]json.RawMessage `json:"input"`
			}
			if err := json.Unmarshal(req.Body, &payload); err != nil {
				t.Fatal(err)
			}
			if len(payload.Input) != 1 {
				t.Fatalf("input items = %d, want 1: %s", len(payload.Input), req.Body)
			}
			if got := string(payload.Input[0]["arguments"]); got != tc.want {
				t.Fatalf("wire arguments = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestLegacyAuthority_ConflictRejectedZeroRoundTrips(t *testing.T) {
	t.Parallel()
	be, obs := newObserverBackend(t, "", unexpectedRequest)
	call := aipapi.Call{
		Invocation: openResponsesCreateInvocation(),
		Items: []aipapi.Item{{
			Kind: aipapi.ItemKindMessage, ID: "msg_1", Status: aipapi.ItemStatusCompleted,
			Role:    aipapi.RoleUser,
			Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "x"}},
		}},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("y")}}},
	}
	_, err := be.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
	if err == nil {
		t.Fatal("expected conflicting authority rejection")
	}
	if !strings.Contains(err.Error(), "conflicting") {
		t.Fatalf("error must name conflicting authority: %v", err)
	}
	if obs.count() != 0 {
		t.Fatalf("conflicting authority caused %d round trips, want 0", obs.count())
	}
}

func TestLegacyAuthority_ReplayDialectRejectedZeroRoundTrips(t *testing.T) {
	// Reasoning is preserved only when lossless. The established legacy→ordered
	// projector carries PartReasoning as a message-content reasoning part, which
	// the pinned profile wire cannot encode without downgrading to text; every
	// family replay dialect therefore rejects before network work, even when a
	// matching reasoning dialect is declared.
	reasons := []struct {
		name    string
		dialect aipapi.ReasoningDialect
		extra   string
	}{
		{name: "openai_chat_reasoning", dialect: aipapi.ReasoningDialectOpenAIChatTextV1},
		{name: "openai_responses_reasoning", dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1},
		{name: "anthropic_thinking", dialect: aipapi.ReasoningDialectAnthropicThinkingV1},
		{name: "anthropic_redacted_thinking", dialect: aipapi.ReasoningDialectAnthropicRedactedThinkingV1},
		{
			name:    "declared_reasoning_never_downgraded",
			dialect: aipapi.ReasoningDialectOpenAIChatTextV1,
			extra: `capabilities: [streaming, tools, vision, documents, reasoning, parallel_tool_calls,
  ordered_items, assistant_phase, item_references, compaction, opaque_extensions, reasoning_replay]
dialects:
  reasoning:
    - dialect: openai.chat.reasoning_text.v1
`,
		},
	}
	for _, tc := range reasons {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			be, obs := newObserverBackend(t, tc.extra, unexpectedRequest)
			call := aipapi.Call{
				Invocation: openResponsesCreateInvocation(),
				Messages: []aipapi.Message{{
					Role: aipapi.RoleAssistant,
					Parts: []aipapi.Part{{
						Kind:      aipapi.PartReasoning,
						Reasoning: &aipapi.ReasoningPart{Dialect: tc.dialect, Text: "think"},
					}},
				}},
			}
			_, err := be.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
			if err == nil {
				t.Fatal("expected replay rejection before network work")
			}
			if !errors.Is(err, ErrUnrepresentable) {
				t.Fatalf("error = %v, want ErrUnrepresentable", err)
			}
			if obs.count() != 0 {
				t.Fatalf("replay dialect caused %d round trips, want 0", obs.count())
			}
		})
	}
}

func TestLegacyAuthority_SourceExtensionRejectedZeroRoundTrips(t *testing.T) {
	sourceExtensions := []struct {
		name string
		ext  map[string]json.RawMessage
	}{
		{name: "anthropic_cache_control", ext: map[string]json.RawMessage{"anthropic.cache_control": json.RawMessage(`{"ephemeral":true}`)}},
		{name: "gemini_cached_content", ext: map[string]json.RawMessage{"gemini.cached_content": json.RawMessage(`{"ttl":"3600s"}`)}},
		{name: "proprietary_vendor", ext: map[string]json.RawMessage{"acme:widget": json.RawMessage(`{"a":1}`)}},
	}
	for _, tc := range sourceExtensions {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			be, obs := newObserverBackend(t, "", unexpectedRequest)
			call := aipapi.Call{
				Invocation: openResponsesCreateInvocation(),
				Messages:   []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
				Extensions: tc.ext,
			}
			_, err := be.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
			if err == nil {
				t.Fatal("expected source-specific extension rejection")
			}
			var pe *aipapi.ProjectionError
			if !errors.As(err, &pe) || pe.Reason != aipapi.ProjectionReasonOpaqueExtension {
				t.Fatalf("error = %v, want ProjectionError(OpaqueExtension)", err)
			}
			if obs.count() != 0 {
				t.Fatalf("source extension caused %d round trips, want 0", obs.count())
			}
		})
	}
}

func TestLegacyAuthority_UnsupportedContentRejectedZeroRoundTrips(t *testing.T) {
	cases := []struct {
		name       string
		call       aipapi.Call
		wantReason aipapi.ProjectionReason
	}{
		{
			name: "anthropic_user_tool_result",
			call: aipapi.Call{
				Invocation: openResponsesCreateInvocation(),
				Messages: []aipapi.Message{{
					Role:  aipapi.RoleUser,
					Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "toolu_1", Text: "Sunny"}},
				}},
			},
		},
		{
			name: "gemini_function_call_without_call_id",
			call: aipapi.Call{
				Invocation: openResponsesCreateInvocation(),
				Messages: []aipapi.Message{{
					Role:  aipapi.RoleAssistant,
					Parts: []aipapi.Part{{Kind: aipapi.PartJSON, ToolName: "my_tool", Content: json.RawMessage(`{"a":1}`)}},
				}},
			},
			wantReason: aipapi.ProjectionReasonUnsupportedContent,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			be, obs := newObserverBackend(t, "", unexpectedRequest)
			_, err := be.Open(context.Background(), tc.call, routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
			if err == nil {
				t.Fatal("expected unsupported-content rejection before network work")
			}
			if tc.wantReason != "" {
				var pe *aipapi.ProjectionError
				if !errors.As(err, &pe) || pe.Reason != tc.wantReason {
					t.Fatalf("error = %v, want ProjectionError(%s)", err, tc.wantReason)
				}
			} else if !errors.Is(err, ErrUnrepresentable) {
				t.Fatalf("error = %v, want ErrUnrepresentable", err)
			}
			if obs.count() != 0 {
				t.Fatalf("unsupported content caused %d round trips, want 0", obs.count())
			}
		})
	}
}

func TestLegacyAuthority_SourceNonMutationOnRejection(t *testing.T) {
	t.Parallel()
	be, obs := newObserverBackend(t, "", unexpectedRequest)
	call := aipapi.Call{
		Invocation:   openResponsesCreateInvocation(),
		Instructions: []aipapi.Message{{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("system prompt")}}},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("user prompt")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{
				Kind:      aipapi.PartReasoning,
				Reasoning: &aipapi.ReasoningPart{Dialect: aipapi.ReasoningDialectOpenAIChatTextV1, Text: "think"},
			}}},
		},
		Extensions: map[string]json.RawMessage{"openailegacy.model": json.RawMessage(`"gpt-4o-mini"`)},
	}
	before := snapshotCall(call)
	_, err := be.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
	if err == nil {
		t.Fatal("expected rejection")
	}
	if obs.count() != 0 {
		t.Fatalf("rejection caused %d round trips, want 0", obs.count())
	}
	assertSnapshotUnchanged(t, before, call)
}

type callSnapshot struct {
	instructions int
	messages     int
	extensions   int
	firstText    string
}

func snapshotCall(c aipapi.Call) callSnapshot {
	s := callSnapshot{instructions: len(c.Instructions), messages: len(c.Messages), extensions: len(c.Extensions)}
	if len(c.Messages) > 0 && len(c.Messages[0].Parts) > 0 {
		s.firstText = c.Messages[0].Parts[0].Text
	}
	return s
}

func assertSnapshotUnchanged(t *testing.T, before callSnapshot, c aipapi.Call) {
	t.Helper()
	if len(c.Instructions) != before.instructions || len(c.Messages) != before.messages || len(c.Extensions) != before.extensions {
		t.Fatalf("source call mutated: instructions=%d messages=%d extensions=%d (want %d/%d/%d)",
			len(c.Instructions), len(c.Messages), len(c.Extensions), before.instructions, before.messages, before.extensions)
	}
	if len(c.Messages) > 0 && c.Messages[0].Parts[0].Text != before.firstText {
		t.Fatalf("source call message text mutated: %q", c.Messages[0].Parts[0].Text)
	}
}

func assertToolResultOutput(t *testing.T, payload map[string]json.RawMessage, want string) {
	t.Helper()
	var input []map[string]json.RawMessage
	if err := json.Unmarshal(payload["input"], &input); err != nil {
		t.Fatalf("input unmarshal: %v", err)
	}
	for i, item := range input {
		if string(item["type"]) != `"function_call_output"` {
			continue
		}
		var got string
		if err := json.Unmarshal(item["output"], &got); err != nil {
			t.Fatalf("input[%d].output is not a JSON string: %v (raw=%s)", i, err, string(item["output"]))
		}
		if got != want {
			t.Fatalf("input[%d].output = %q, want %q", i, got, want)
		}
		return
	}
	t.Fatalf("no function_call_output item found: %s", string(payload["input"]))
}

func assertOrderedInput(t *testing.T, payload map[string]json.RawMessage, wantTypes, wantRoles []string) {
	t.Helper()
	var input []map[string]json.RawMessage
	if err := json.Unmarshal(payload["input"], &input); err != nil {
		t.Fatalf("input unmarshal: %v", err)
	}
	if len(input) != len(wantTypes) {
		t.Fatalf("input item count = %d, want %d (types=%v)", len(input), len(wantTypes), wantTypes)
	}
	for i, want := range wantTypes {
		if got := string(input[i]["type"]); got != `"`+want+`"` {
			t.Fatalf("input[%d].type = %s, want %q", i, got, want)
		}
		wantRole := ""
		if i < len(wantRoles) {
			wantRole = wantRoles[i]
		}
		if wantRole == "" {
			if _, present := input[i]["role"]; present {
				t.Fatalf("input[%d] must not carry a role, got %q", i, string(input[i]["role"]))
			}
			continue
		}
		if got := string(input[i]["role"]); got != `"`+wantRole+`"` {
			t.Fatalf("input[%d].role = %s, want %q", i, got, wantRole)
		}
	}
}

func unexpectedRequest(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "unexpected upstream request", http.StatusInternalServerError)
}
