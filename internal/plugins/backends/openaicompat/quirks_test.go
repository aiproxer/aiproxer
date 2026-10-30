package openaicompat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openaicred"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
)

// Claim: quirk off leaves the Chat wire byte-identical: the assistant
// tool-call message carries exactly the pre-quirk serializer fields and no
// reasoning_content is injected anywhere.
// Source: CompatibleQuirks godoc, "Quirk off means byte-identical requests".
// Mutant: delete the quirk-off guard so OpenChat always injects -> FAIL.
func TestQuirks_ChatOffByteIdentical(t *testing.T) {
	t.Parallel()
	call := quirkChatToolCall()
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "m"}}

	body := quirkCaptureChatBody(t, call, cand, CompatibleQuirks{})
	if strings.Contains(string(body), "reasoning_content") {
		t.Fatalf("quirk off must not inject reasoning_content: %s", body)
	}
	var payload struct {
		Messages []map[string]json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, body)
	}
	if len(payload.Messages) != 3 {
		t.Fatalf("messages = %d, want 3: %s", len(payload.Messages), body)
	}
	if got := quirkSortedKeys(payload.Messages[1]); got != "content role tool_calls" {
		t.Fatalf("assistant tool-call fields = %q, want the pre-quirk set: %s", got, body)
	}
}

// Claim: with quirk, assistant tool-call without reasoning gets "".
// Source: DeepSeek thinking_mode docs (reasoning_content must be passed back).
// Mutant: remove applyChatReasoningContentQuirk call -> strict 400.
func TestQuirks_ChatInjectsEmptyReasoning(t *testing.T) {
	t.Parallel()
	call := quirkChatToolCall()
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "m"}}
	body := quirkCaptureChatBody(t, call, cand, CompatibleQuirks{ReasoningContentOnToolCalls: true})
	var payload struct {
		Messages []map[string]json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, body)
	}
	found := false
	for _, m := range payload.Messages {
		if _, hasTC := m["tool_calls"]; !hasTC {
			continue
		}
		raw, ok := m["reasoning_content"]
		if !ok {
			t.Fatalf("tool-call message lacks reasoning_content: %s", body)
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil || s != "" {
			t.Fatalf("reasoning_content must be empty string, got %s body=%s", raw, body)
		}
		found = true
	}
	if !found {
		t.Fatalf("no tool-call message found: %s", body)
	}
}

// Claim: quirk never overwrites non-empty reasoning; tool-less requests stay
// untouched; plain assistant turns in tools-carrying requests gain the field.
// Source: DeepSeek thinking_mode docs (passback on all turns with tools).
// Mutant: always overwrite / touch tool-less requests -> FAIL.
func TestQuirks_ChatPreservesNonEmpty(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}},
			{
				Role: aipapi.RoleAssistant,
				Parts: []aipapi.Part{
					{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{Dialect: aipapi.ReasoningDialectOpenAIChatTextV1, Text: "real-thought"}},
					{Kind: aipapi.PartJSON, ToolCallID: "call_1", ToolName: "w", Content: []byte(`{"id":"call_1","type":"function","function":{"name":"w","arguments":"{}"}}`)},
				},
			},
		},
		Tools: []aipapi.ToolDef{{Name: "w", Parameters: []byte(`{}`)}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "m"}}
	body := quirkCaptureChatBody(t, call, cand, CompatibleQuirks{ReasoningContentOnToolCalls: true})
	if !strings.Contains(string(body), "real-thought") {
		t.Fatalf("non-empty reasoning must be preserved: %s", body)
	}
	// A present-but-empty field is left as-is (no rewrite churn).
	// Plain assistant text turns in tools-carrying requests gain the field;
	// tool-less requests are never touched even with the quirk on.
	plain := aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("just text")}},
		},
		Tools: []aipapi.ToolDef{{Name: "w", Parameters: []byte(`{}`)}},
	}
	plainBody := quirkCaptureChatBody(t, plain, cand, CompatibleQuirks{ReasoningContentOnToolCalls: true})
	var plainPayload struct {
		Messages []map[string]json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(plainBody, &plainPayload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(plainPayload.Messages) != 2 {
		t.Fatalf("messages=%d, want 2: %s", len(plainPayload.Messages), plainBody)
	}
	if _, ok := plainPayload.Messages[1]["reasoning_content"]; !ok {
		t.Fatalf("plain assistant turn with tools must gain reasoning_content: %s", plainBody)
	}
	toolLess := aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("just text")}},
		},
	}
	toolLessBody := quirkCaptureChatBody(t, toolLess, cand, CompatibleQuirks{ReasoningContentOnToolCalls: true})
	if strings.Contains(string(toolLessBody), "reasoning_content") {
		t.Fatalf("tool-less request must stay untouched: %s", toolLessBody)
	}
}

// Claim: Responses quirk off means byte-identical; a healthy call/output
// history (no trapped item) is byte-identical on/off.
// Source: applyResponsesAdjacencyQuirk godoc, "Healthy input is byte-identical".
// Mutant: reorder unconditionally -> healthy bytes change -> FAIL.
func TestQuirks_ResponsesHealthyUnchanged(t *testing.T) {
	t.Parallel()
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "m"}}
	// Healthy call (no trapped item) must be identical on/off.
	healthy := aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("go")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{Kind: aipapi.PartJSON, ToolCallID: "c1", ToolName: "w", Content: []byte(`{"type":"function_call","call_id":"c1","name":"w","arguments":"{}"}`)}}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "c1", Content: []byte(`"ok"`)}}},
		},
		Tools: []aipapi.ToolDef{{Name: "w", Parameters: []byte(`{}`)}},
	}
	off := quirkCaptureResponsesBody(t, healthy, cand, CompatibleQuirks{})
	on := quirkCaptureResponsesBody(t, healthy, cand, CompatibleQuirks{ToolOutputAdjacency: true})
	if string(off) != string(on) {
		t.Fatalf("healthy input must be byte-identical on/off:\noff=%s\non=%s", off, on)
	}
}

// Claim: Responses quirk moves trapped item before earliest call, keeps order.
// Mutant: drop reordering -> trapped stays -> FAIL.
func TestQuirks_ResponsesMovesTrapped(t *testing.T) {
	t.Parallel()
	call := quirkResponsesTrapped()
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "m"}}
	off := quirkCaptureResponsesBody(t, call, cand, CompatibleQuirks{})
	on := quirkCaptureResponsesBody(t, call, cand, CompatibleQuirks{ToolOutputAdjacency: true})
	if string(off) == string(on) {
		t.Fatalf("trapped input must change with quirk on")
	}
	// Verify order in the quirked body: the output moved up to its call and
	// the trapped user text follows it.
	var payload struct {
		Input []map[string]json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(on, &payload); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, on)
	}
	callIdx, outIdx, trappedIdx := -1, -1, -1
	for i, it := range payload.Input {
		var typ string
		_ = json.Unmarshal(it["type"], &typ)
		var cid string
		_ = json.Unmarshal(it["call_id"], &cid)
		if typ == "function_call" && cid == "c1" && callIdx < 0 {
			callIdx = i
		}
		if typ == "function_call_output" && cid == "c1" && outIdx < 0 {
			outIdx = i
		}
		var content string
		_ = json.Unmarshal(it["content"], &content)
		if content == "trapped" {
			trappedIdx = i
		}
		// Content may be an array for message items; fall back to raw check.
		if trappedIdx < 0 {
			if raw, ok := it["content"]; ok && strings.Contains(string(raw), "trapped") {
				trappedIdx = i
			}
		}
	}
	if callIdx < 0 || outIdx < 0 || trappedIdx < 0 {
		t.Fatalf("missing items call=%d out=%d trapped=%d body=%s", callIdx, outIdx, trappedIdx, on)
	}
	// The output is pulled up to its call; the message keeps its place after.
	if callIdx >= outIdx || outIdx >= trappedIdx {
		t.Fatalf("want call < output < trapped, got call=%d out=%d trapped=%d body=%s", callIdx, outIdx, trappedIdx, on)
	}
}

// Claim: duplicate/missing call_ids get no reordering.
// Mutant: reorder duplicates -> outputs mispaired -> FAIL.
func TestQuirks_ResponsesDuplicateNoReorder(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
				{Kind: aipapi.PartJSON, ToolCallID: "dup", ToolName: "w", Content: []byte(`{"type":"function_call","call_id":"dup","name":"w","arguments":"{}"}`)},
				{Kind: aipapi.PartJSON, ToolCallID: "dup", ToolName: "w", Content: []byte(`{"type":"function_call","call_id":"dup","name":"w","arguments":"{}"}`)},
			}},
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("trapped")}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "dup", Content: []byte(`"ok"`)}}},
		},
		Tools: []aipapi.ToolDef{{Name: "w", Parameters: []byte(`{}`)}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "m"}}
	off := quirkCaptureResponsesBody(t, call, cand, CompatibleQuirks{})
	on := quirkCaptureResponsesBody(t, call, cand, CompatibleQuirks{ToolOutputAdjacency: true})
	if string(off) != string(on) {
		t.Fatalf("duplicate call_ids must not reorder:\noff=%s\non=%s", off, on)
	}
}

func quirkChatToolCall() aipapi.Call {
	return aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}},
			{
				Role: aipapi.RoleAssistant,
				Parts: []aipapi.Part{
					{Kind: aipapi.PartJSON, ToolCallID: "call_1", ToolName: "w", Content: []byte(`{"id":"call_1","type":"function","function":{"name":"w","arguments":"{}"}}`)},
				},
			},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "call_1", Content: []byte(`"ok"`)}}},
		},
		Tools: []aipapi.ToolDef{{Name: "w", Parameters: []byte(`{}`)}},
	}
}

func quirkResponsesTrapped() aipapi.Call {
	return aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("start")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{Kind: aipapi.PartJSON, ToolCallID: "c1", ToolName: "w", Content: []byte(`{"type":"function_call","call_id":"c1","name":"w","arguments":"{}"}`)}}},
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("trapped")}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "c1", Content: []byte(`"ok"`)}}},
		},
		Tools: []aipapi.ToolDef{{Name: "w", Parameters: []byte(`{}`)}},
	}
}

func quirkCaptureChatBody(t *testing.T, call aipapi.Call, cand routing.AttemptCandidate, q CompatibleQuirks) []byte {
	t.Helper()
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		captured = append([]byte(nil), b...)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"q","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	t.Cleanup(srv.Close)
	cli := openaicred.NewClient(srv.URL, "sk-test", srv.Client(), new(int))
	call.Invocation.TransportMode = aipapi.TransportModeNonStreaming
	es, err := OpenChat(context.Background(), cli, InvokeRequest{ProviderID: "q", Call: call, Candidate: cand, Quirks: q})
	if err != nil {
		t.Fatalf("OpenChat: %v", err)
	}
	drainQuirkEvents(t, es)
	if len(captured) == 0 {
		t.Fatal("no body captured")
	}
	return captured
}

func quirkCaptureResponsesBody(t *testing.T, call aipapi.Call, cand routing.AttemptCandidate, q CompatibleQuirks) []byte {
	t.Helper()
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		captured = append([]byte(nil), b...)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"r","object":"response","created_at":1,"status":"completed","model":"m","output":[{"type":"message","id":"m1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
	}))
	t.Cleanup(srv.Close)
	cli := openaicred.NewClient(srv.URL, "sk-test", srv.Client(), new(int))
	call.Invocation.TransportMode = aipapi.TransportModeNonStreaming
	es, err := OpenResponses(context.Background(), cli, InvokeRequest{ProviderID: "q", Call: call, Candidate: cand, Quirks: q})
	if err != nil {
		t.Fatalf("OpenResponses: %v", err)
	}
	drainQuirkEvents(t, es)
	if len(captured) == 0 {
		t.Fatal("no body captured")
	}
	return captured
}

func drainQuirkEvents(t *testing.T, es aipapi.ManagedEventStream) {
	t.Helper()
	defer func() { _ = es.Close() }()
	for {
		_, err := es.Recv(context.Background())
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
	}
}

// Claim: an exact reasoning item between a call and its output is refused,
// never relocated. Requirement 4.5 of the archived reasoning-preservation
// spec fixes reasoning-item ordering against other input items.
// Source: third review P1 on PR #1102.
// Mutant: relocate the output across reasoning -> FAIL.
func TestQuirks_ResponsesReasoningInterleaveRefused(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("start")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{Kind: aipapi.PartJSON, ToolCallID: "cA", ToolName: "w", Content: []byte(`{"type":"function_call","call_id":"cA","name":"w","arguments":"{}"}`)}}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
				{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1, Opaque: json.RawMessage(`{"id":"rs_1","summary":[]}`)}},
				{Kind: aipapi.PartJSON, ToolCallID: "cB", ToolName: "w", Content: []byte(`{"type":"function_call","call_id":"cB","name":"w","arguments":"{}"}`)},
			}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "cA", Content: []byte(`"ok-a"`)}}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "cB", Content: []byte(`"ok-b"`)}}},
		},
		Tools: []aipapi.ToolDef{{Name: "w", Parameters: []byte(`{}`)}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "m"}}
	rec := newInvokeRecorder()
	srv := newInvokeServer(t, rec)
	cli := openaicred.NewClient(srv.URL, "sk-test", srv.Client(), new(int))
	call.Invocation.TransportMode = aipapi.TransportModeNonStreaming
	_, err := OpenResponses(context.Background(), cli, InvokeRequest{ProviderID: "q", Call: call, Candidate: cand, Quirks: CompatibleQuirks{ToolOutputAdjacency: true}})
	if !errors.Is(err, ErrResponsesAdjacencyRefused) {
		t.Fatalf("reasoning interleave must be refused with a classified error, got %v", err)
	}
	if len(rec.body) != 0 {
		t.Fatalf("refused request must not be sent, body=%s", rec.body)
	}
}

// Claim: the third-review counterexample (output crossing a reasoning item and
// a later call, reversing output completion order) is refused and reports no
// application, so a refusal never inflates the applied-quirk metric.
// Source: third review P1 example
// [callA, reasoningR, callB, outB, outA].
// Mutant: relocate -> FAIL.
func TestQuirks_ResponsesReversedOutputOrderRefused(t *testing.T) {
	t.Parallel()
	mkCall := func(id string) responses.ResponseInputItemUnionParam {
		return responses.ResponseInputItemUnionParam{OfFunctionCall: &responses.ResponseFunctionToolCallParam{CallID: id, Name: "w", Arguments: "{}"}}
	}
	mkOut := func(id string) responses.ResponseInputItemUnionParam {
		return responses.ResponseInputItemUnionParam{OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{CallID: param.NewOpt(id), Output: responses.ResponseInputItemFunctionCallOutputOutputUnionParam{OfString: param.NewOpt("ok")}}}
	}
	reasoning := responses.ResponseInputItemUnionParam{OfReasoning: &responses.ResponseReasoningItemParam{ID: "rs_1"}}
	in := []responses.ResponseInputItemUnionParam{mkCall("cA"), reasoning, mkCall("cB"), mkOut("cB"), mkOut("cA")}
	_, applied, err := reorderResponsesAdjacency(in)
	if !errors.Is(err, ErrResponsesAdjacencyRefused) {
		t.Fatalf("output crossing reasoning and a later call must be refused, got %v", err)
	}
	if applied {
		t.Fatal("a refusal is not an application and must not be counted")
	}
}

// Claim: only non-assistant conversation messages are delayed; model-authored
// items (reasoning, assistant messages, other tool shapes) are refused. An
// applied delay is reported once, a refusal is never reported.
// Source: third review P1 on PR #1102.
// Mutant: relocate across any trapped item -> FAIL.
func TestQuirks_ResponsesOnlyNonAssistantMessagesDelayable(t *testing.T) {
	t.Parallel()
	mkCall := func(id string) responses.ResponseInputItemUnionParam {
		return responses.ResponseInputItemUnionParam{OfFunctionCall: &responses.ResponseFunctionToolCallParam{CallID: id, Name: "w", Arguments: "{}"}}
	}
	mkOut := func(id string) responses.ResponseInputItemUnionParam {
		return responses.ResponseInputItemUnionParam{OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{CallID: param.NewOpt(id), Output: responses.ResponseInputItemFunctionCallOutputOutputUnionParam{OfString: param.NewOpt("ok")}}}
	}
	userMsg := responses.ResponseInputItemParamOfMessage("trapped", responses.EasyInputMessageRoleUser)
	assistantMsg := responses.ResponseInputItemParamOfMessage("assistant-said", responses.EasyInputMessageRoleAssistant)
	exotic := responses.ResponseInputItemUnionParam{OfFileSearchCall: &responses.ResponseFileSearchToolCallParam{ID: "fs_1", Queries: []string{"q"}, Status: responses.ResponseFileSearchToolCallStatusInProgress}}

	// Non-assistant message: delayed past the output, content untouched.
	in := []responses.ResponseInputItemUnionParam{mkCall("c1"), userMsg, mkOut("c1")}
	got, applied, err := reorderResponsesAdjacency(in)
	if err != nil {
		t.Fatalf("user message must be delayable: %v", err)
	}
	if !applied {
		t.Fatal("delaying a message rewrites the wire and must be reported as an application")
	}
	if len(got) != len(in) || got[0].OfFunctionCall == nil || got[1].OfFunctionCallOutput == nil || got[2].OfMessage == nil {
		t.Fatalf("unexpected result %+v", got)
	}
	if quirkMarshalAll(t, got[2:3]) != quirkMarshalAll(t, in[1:2]) {
		t.Fatalf("delayed message content must survive unchanged")
	}

	// Model-authored items are refused, not relocated.
	for name, item := range map[string]responses.ResponseInputItemUnionParam{"reasoning": {OfReasoning: &responses.ResponseReasoningItemParam{ID: "rs_1"}}, "assistant-message": assistantMsg, "exotic-tool-item": exotic} {
		bad := []responses.ResponseInputItemUnionParam{mkCall("c1"), item, mkOut("c1")}
		out, refused, err := reorderResponsesAdjacency(bad)
		if !errors.Is(err, ErrResponsesAdjacencyRefused) {
			t.Fatalf("%s must be refused, got err=%v", name, err)
		}
		if refused {
			t.Fatalf("%s: a refusal is not an application and must not be counted", name)
		}
		if quirkMarshalAll(t, out) != quirkMarshalAll(t, bad) {
			t.Fatalf("%s: refused request must be returned unchanged", name)
		}
	}
}

// Claim: ordering invariants on a fixable history — user messages keep their
// relative order and their content, calls keep their relative order, and tool
// outputs keep completion order.
// Source: third review P2 on PR #1102 (output-order invariant).
// Mutant: any intra-class reorder -> FAIL.
func TestQuirks_ResponsesOrderingInvariants(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("start")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{Kind: aipapi.PartJSON, ToolCallID: "cA", ToolName: "w", Content: []byte(`{"type":"function_call","call_id":"cA","name":"w","arguments":"{}"}`)}}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{Kind: aipapi.PartJSON, ToolCallID: "cB", ToolName: "w", Content: []byte(`{"type":"function_call","call_id":"cB","name":"w","arguments":"{}"}`)}}},
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("t1")}},
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("t2")}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "cA", Content: []byte(`"ok-a"`)}}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "cB", Content: []byte(`"ok-b"`)}}},
		},
		Tools: []aipapi.ToolDef{{Name: "w", Parameters: []byte(`{}`)}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "m"}}
	on := quirkCaptureResponsesBody(t, call, cand, CompatibleQuirks{ToolOutputAdjacency: true})
	if got := strings.Join(quirkInputDescriptors(t, on), "|"); got != "msg:start|call:cA|call:cB|out:cA|out:cB|msg:t1|msg:t2" {
		t.Fatalf("invariant order broken: %v body=%s", quirkInputDescriptors(t, on), on)
	}
}

// Claim: a history whose fix would move an output across a later tool call is
// refused rather than reordered, and the refusal reports no application.
// Source: third review P1 (outputA before callB implies the model observed A's
// result before generating B).
// Mutant: relocate -> FAIL.
func TestQuirks_ResponsesAcrossLaterCallRefused(t *testing.T) {
	t.Parallel()
	mkCall := func(id string) responses.ResponseInputItemUnionParam {
		return responses.ResponseInputItemUnionParam{OfFunctionCall: &responses.ResponseFunctionToolCallParam{CallID: id, Name: "w", Arguments: "{}"}}
	}
	mkOut := func(id string) responses.ResponseInputItemUnionParam {
		return responses.ResponseInputItemUnionParam{OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{CallID: param.NewOpt(id), Output: responses.ResponseInputItemFunctionCallOutputOutputUnionParam{OfString: param.NewOpt("ok")}}}
	}
	msg := responses.ResponseInputItemParamOfMessage("t1", responses.EasyInputMessageRoleUser)
	in := []responses.ResponseInputItemUnionParam{mkCall("cA"), msg, mkCall("cB"), mkOut("cA"), mkOut("cB")}
	got, applied, err := reorderResponsesAdjacency(in)
	if !errors.Is(err, ErrResponsesAdjacencyRefused) {
		t.Fatalf("move across a later call must be refused, got %v", err)
	}
	if applied {
		t.Fatal("a refusal is not an application and must not be counted")
	}
	if quirkMarshalAll(t, got) != quirkMarshalAll(t, in) {
		t.Fatalf("refused request must be returned unchanged")
	}
}

// Claim: two outputs completing before one delayable message keep their
// completion order and the rewrite is reported as one application.
// Mutant: reverse output order -> FAIL.
func TestQuirks_ResponsesCompletionOrderPreserved(t *testing.T) {
	t.Parallel()
	mkCall := func(id string) responses.ResponseInputItemUnionParam {
		return responses.ResponseInputItemUnionParam{OfFunctionCall: &responses.ResponseFunctionToolCallParam{CallID: id, Name: "w", Arguments: "{}"}}
	}
	mkOut := func(id string) responses.ResponseInputItemUnionParam {
		return responses.ResponseInputItemUnionParam{OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{CallID: param.NewOpt(id), Output: responses.ResponseInputItemFunctionCallOutputOutputUnionParam{OfString: param.NewOpt("ok")}}}
	}
	msg := responses.ResponseInputItemParamOfMessage("t1", responses.EasyInputMessageRoleUser)
	in := []responses.ResponseInputItemUnionParam{mkCall("cA"), mkCall("cB"), msg, mkOut("cA"), mkOut("cB")}
	got, applied, err := reorderResponsesAdjacency(in)
	if err != nil {
		t.Fatalf("unexpected refusal: %v", err)
	}
	if !applied {
		t.Fatal("a reordered history must be reported as an application")
	}
	want := []responses.ResponseInputItemUnionParam{mkCall("cA"), mkCall("cB"), mkOut("cA"), mkOut("cB"), msg}
	if quirkMarshalAll(t, got) != quirkMarshalAll(t, want) {
		t.Fatalf("completion order reversed:\ngot  %s\nwant %s", quirkMarshalAll(t, got), quirkMarshalAll(t, want))
	}
}

// quirkSortedKeys returns a JSON object's field names in stable order, so the
// exact wire field set can be asserted without depending on key order.
func quirkSortedKeys(m map[string]json.RawMessage) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return strings.Join(keys, " ")
}

func quirkInputDescriptors(t *testing.T, body []byte) []string {
	t.Helper()
	var payload struct {
		Input []map[string]json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	out := make([]string, 0, len(payload.Input))
	for _, it := range payload.Input {
		var typ string
		_ = json.Unmarshal(it["type"], &typ)
		var cid string
		_ = json.Unmarshal(it["call_id"], &cid)
		switch typ {
		case "function_call":
			out = append(out, "call:"+cid)
		case "function_call_output":
			out = append(out, "out:"+cid)
		case "reasoning":
			out = append(out, "reasoning")
		default:
			var content string
			_ = json.Unmarshal(it["content"], &content)
			if content != "" {
				out = append(out, "msg:"+content)
			} else if raw, ok := it["content"]; ok && strings.Contains(string(raw), "t1") {
				out = append(out, "msg:t1")
			} else {
				out = append(out, "msg")
			}
		}
	}
	return out
}

func quirkMarshalAll(t *testing.T, items []responses.ResponseInputItemUnionParam) string {
	t.Helper()
	raw, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

// Claim: duplicate outputs for one call_id block normalization entirely
// (ambiguous pairing is not a violation we can establish) and are not counted
// as an application.
// Source: re-review P2 on PR #1102 ([callA, messageX, outA, outA]).
// Mutant: treat outputCount[id] != 0 as pairable -> reorder -> FAIL.
func TestQuirks_ResponsesDuplicateOutputsNoReorder(t *testing.T) {
	t.Parallel()
	mkCall := func(id string) responses.ResponseInputItemUnionParam {
		return responses.ResponseInputItemUnionParam{OfFunctionCall: &responses.ResponseFunctionToolCallParam{CallID: id, Name: "w", Arguments: "{}"}}
	}
	mkOut := func(id string) responses.ResponseInputItemUnionParam {
		return responses.ResponseInputItemUnionParam{OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{CallID: param.NewOpt(id), Output: responses.ResponseInputItemFunctionCallOutputOutputUnionParam{OfString: param.NewOpt("ok")}}}
	}
	msg := responses.ResponseInputItemParamOfMessage("trapped", responses.EasyInputMessageRoleUser)
	in := []responses.ResponseInputItemUnionParam{mkCall("c1"), msg, mkOut("c1"), mkOut("c1")}
	got, applied, err := reorderResponsesAdjacency(in)
	if err != nil {
		t.Fatalf("ambiguous pairing is not an error: %v", err)
	}
	if applied {
		t.Fatal("an unchanged request must not be reported as an application")
	}
	if quirkMarshalAll(t, got) != quirkMarshalAll(t, in) {
		t.Fatalf("duplicate outputs must leave the request unchanged")
	}
}

// Claim: overlapping pending groups resolve in one global pass, so no rewrite
// is left half-applied, and the single global pass reports one application.
// Source: re-review P1 on PR #1102
// ([callA, callB, outA, callC, messageX, outC, outB] left messageX trapped).
// Mutant: per-span resumption -> messageX still between callB and outB -> FAIL.
func TestQuirks_ResponsesOverlappingPendingGroups(t *testing.T) {
	t.Parallel()
	mkCall := func(id string) responses.ResponseInputItemUnionParam {
		return responses.ResponseInputItemUnionParam{OfFunctionCall: &responses.ResponseFunctionToolCallParam{CallID: id, Name: "w", Arguments: "{}"}}
	}
	mkOut := func(id string) responses.ResponseInputItemUnionParam {
		return responses.ResponseInputItemUnionParam{OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{CallID: param.NewOpt(id), Output: responses.ResponseInputItemFunctionCallOutputOutputUnionParam{OfString: param.NewOpt("ok")}}}
	}
	msg := responses.ResponseInputItemParamOfMessage("X", responses.EasyInputMessageRoleDeveloper)
	in := []responses.ResponseInputItemUnionParam{mkCall("cA"), mkCall("cB"), mkOut("cA"), mkCall("cC"), msg, mkOut("cC"), mkOut("cB")}
	got, applied, err := reorderResponsesAdjacency(in)
	if err != nil {
		t.Fatalf("developer message must be delayable: %v", err)
	}
	if !applied {
		t.Fatal("a reordered history must be reported as an application")
	}
	want := []responses.ResponseInputItemUnionParam{mkCall("cA"), mkCall("cB"), mkOut("cA"), mkCall("cC"), mkOut("cC"), mkOut("cB"), msg}
	if quirkMarshalAll(t, got) != quirkMarshalAll(t, want) {
		t.Fatalf("overlapping groups:\ngot  %s\nwant %s", quirkMarshalAll(t, got), quirkMarshalAll(t, want))
	}
	// Every pair must now be separated only by sibling calls/outputs.
	assertStrictAdjacency(t, got)
}

// Claim: adjacency normalization reports an application only when it actually
// rewrote the wire. Histories with nothing to restore — an empty input, a call
// without output, and an output that already precedes its call — are no-ops,
// and an empty Responses input is a no-op at the entry point as well.
// Source: applyResponsesAdjacencyQuirk godoc, "reports whether the input was
// actually reordered"; the applied-quirk metric counts only real rewrites.
// Mutant: report applied=true on a no-op path -> the metric counts a rewrite
// that never happened -> FAIL.
func TestQuirks_AdjacencyNoopPathsReportNoApplication(t *testing.T) {
	t.Parallel()
	mkCall := func(id string) responses.ResponseInputItemUnionParam {
		return responses.ResponseInputItemUnionParam{OfFunctionCall: &responses.ResponseFunctionToolCallParam{CallID: id, Name: "w", Arguments: "{}"}}
	}
	mkOut := func(id string) responses.ResponseInputItemUnionParam {
		return responses.ResponseInputItemUnionParam{OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{CallID: param.NewOpt(id), Output: responses.ResponseInputItemFunctionCallOutputOutputUnionParam{OfString: param.NewOpt("ok")}}}
	}
	cases := map[string][]responses.ResponseInputItemUnionParam{
		"empty history":          {},
		"call without output":    {mkCall("c1")},
		"output before its call": {mkOut("c1"), mkCall("c1")},
	}
	for name, in := range cases {
		got, applied, err := reorderResponsesAdjacency(in)
		if err != nil {
			t.Fatalf("%s: a no-op history must not error: %v", name, err)
		}
		if applied {
			t.Fatalf("%s: a no-op history must not be reported as an application", name)
		}
		if quirkMarshalAll(t, got) != quirkMarshalAll(t, in) {
			t.Fatalf("%s: a no-op history must be returned unchanged:\ngot  %s\nwant %s", name, quirkMarshalAll(t, got), quirkMarshalAll(t, in))
		}
	}
	applied, err := applyResponsesAdjacencyQuirk(&responses.ResponseNewParams{})
	if err != nil || applied {
		t.Fatalf("empty Responses input: applied=%v err=%v, want no application", applied, err)
	}
}

// assertStrictAdjacency applies the strict upstream rule to a param item list:
// between a call and its output there may be only other calls and outputs.
func assertStrictAdjacency(t *testing.T, items []responses.ResponseInputItemUnionParam) {
	t.Helper()
	outAt := map[string]int{}
	for i, it := range items {
		if id, ok := isResponsesOutput(it); ok {
			outAt[id] = i
		}
	}
	for i, it := range items {
		id, ok := isResponsesCall(it)
		if !ok {
			continue
		}
		o, ok := outAt[id]
		if !ok {
			continue
		}
		for k := i + 1; k < o; k++ {
			if _, isCall := isResponsesCall(items[k]); isCall {
				continue
			}
			if _, isOut := isResponsesOutput(items[k]); isOut {
				continue
			}
			t.Fatalf("item at %d still separates %s from its output", k, id)
		}
	}
}

// Claim: quirk-level reasoning handling is non-destructive — the quirk never
// changes whatever the serializer produced.
// Source: re-review P2 on PR #1102 (Contains-based assertion was weak).
// Mutant: rewrite reasoning_content -> off/on bodies differ -> FAIL.
func TestQuirks_ChatReasoningNonOverwriteEquality(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}},
			{
				Role: aipapi.RoleAssistant,
				Parts: []aipapi.Part{
					{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{Dialect: aipapi.ReasoningDialectOpenAIChatTextV1, Text: "  padded-thought  "}},
					{Kind: aipapi.PartJSON, ToolCallID: "call_1", ToolName: "w", Content: []byte(`{"id":"call_1","type":"function","function":{"name":"w","arguments":"{}"}}`)},
				},
			},
		},
		Tools: []aipapi.ToolDef{{Name: "w", Parameters: []byte(`{}`)}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "m"}}
	off := quirkChatReasoningField(t, call, cand, CompatibleQuirks{})
	on := quirkChatReasoningField(t, call, cand, CompatibleQuirks{ReasoningContentOnToolCalls: true})
	if on != off {
		t.Fatalf("quirk must not alter reasoning_content: off=%q on=%q", off, on)
	}
	// Documented end-to-end bound: the shared serializer trims each fragment
	// and concatenates without a separator, so passback fidelity is bounded by
	// that pre-existing behavior, not by the quirk.
	if off != "padded-thought" {
		t.Fatalf("expected serializer-trimmed concatenation %q, got %q", "padded-thought", off)
	}
}

// Claim: multiple reasoning fragments concatenate exactly as the serializer
// defines, and the quirk leaves the result untouched.
func TestQuirks_ChatMultipleFragmentsEquality(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}},
			{
				Role: aipapi.RoleAssistant,
				Parts: []aipapi.Part{
					{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{Dialect: aipapi.ReasoningDialectOpenAIChatTextV1, Text: " first "}},
					{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{Dialect: aipapi.ReasoningDialectOpenAIChatTextV1, Text: "second"}},
					{Kind: aipapi.PartJSON, ToolCallID: "call_1", ToolName: "w", Content: []byte(`{"id":"call_1","type":"function","function":{"name":"w","arguments":"{}"}}`)},
				},
			},
		},
		Tools: []aipapi.ToolDef{{Name: "w", Parameters: []byte(`{}`)}},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "m"}}
	off := quirkChatReasoningField(t, call, cand, CompatibleQuirks{})
	on := quirkChatReasoningField(t, call, cand, CompatibleQuirks{ReasoningContentOnToolCalls: true})
	if off != "firstsecond" {
		t.Fatalf("expected fragment concatenation %q, got %q", "firstsecond", off)
	}
	if on != off {
		t.Fatalf("quirk must not alter concatenated reasoning: off=%q on=%q", off, on)
	}
}

func quirkChatReasoningField(t *testing.T, call aipapi.Call, cand routing.AttemptCandidate, q CompatibleQuirks) string {
	t.Helper()
	body := quirkCaptureChatBody(t, call, cand, q)
	var payload struct {
		Messages []map[string]json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, body)
	}
	for _, m := range payload.Messages {
		raw, ok := m["reasoning_content"]
		if !ok {
			continue
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatalf("reasoning_content not a string: %s", raw)
		}
		return s
	}
	return ""
}

// Claim: a surfaced adjacency refusal carries all three orchestration roots:
// identity (ErrResponsesAdjacencyRefused), capability rejection (frontends map
// to 502 backend_unavailable), and recoverable pre-output (core fails over).
// It is NOT a *aipapi.RejectError, so it never maps to a 400 client reject.
// Source: track 3 of the adjacency-refusal execution handoff.
// Mutant: return the bare reorder error -> capability/recoverable roots lost.
func TestQuirks_RefusalCarriesCapabilityRejectAndRecoverable(t *testing.T) {
	t.Parallel()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(srv.Close)
	cli := openaicred.NewClient(srv.URL, "sk-test", srv.Client(), new(int))

	call := aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("start")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{Kind: aipapi.PartJSON, ToolCallID: "cA", ToolName: "w", Content: []byte(`{"type":"function_call","call_id":"cA","name":"w","arguments":"{}"}`)}}},
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("trapped")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{Kind: aipapi.PartJSON, ToolCallID: "cB", ToolName: "w", Content: []byte(`{"type":"function_call","call_id":"cB","name":"w","arguments":"{}"}`)}}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "cA", Content: []byte(`"ok-a"`)}}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "cB", Content: []byte(`"ok-b"`)}}},
		},
		Tools: []aipapi.ToolDef{{Name: "w", Parameters: []byte(`{}`)}},
	}
	call.Invocation.TransportMode = aipapi.TransportModeNonStreaming
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "m"}}
	_, err := OpenResponses(context.Background(), cli, InvokeRequest{
		ProviderID: "refuse-probe",
		Call:       call,
		Candidate:  cand,
		Quirks:     CompatibleQuirks{ToolOutputAdjacency: true},
	})
	if err == nil {
		t.Fatal("expected adjacency refusal")
	}
	if !errors.Is(err, ErrResponsesAdjacencyRefused) {
		t.Fatalf("must preserve refusal identity, got %v", err)
	}
	if !errors.Is(err, aipapi.ErrCapabilityReject) {
		t.Fatalf("must match capability reject for the 502 mapping, got %v", err)
	}
	if !aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("must be recoverable pre-output for failover, got %v", err)
	}
	if aipapi.IsReject(err) {
		t.Fatalf("must not be a client reject (400 class), got %v", err)
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Fatalf("refused request must not reach the wire, hits=%d", hits)
	}
}
