package openairesponses_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/internal/plugins/openrouterwire"
	"github.com/aiproxer/aiproxer/internal/refclient/refclienttest"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func readGolden(t *testing.T, name string) []byte {
	t.Helper()
	p := filepath.Join(refclienttest.ModuleRoot(t), "testdata", "openairesponses_frontend", name)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDecodeCreate_textNonStream(t *testing.T) {
	t.Parallel()
	body := readGolden(t, "create_text_nonstream.json")
	d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Stream {
		t.Fatal("expected stream false")
	}
	if d.Model != "gpt-4o-mini" {
		t.Fatalf("model %q", d.Model)
	}
	if openairesponses.ModelFromCall(d.Call) != "gpt-4o-mini" {
		t.Fatal("model extension")
	}
	if got := d.Call.Route.Selector; got != "stub:gpt-4o-mini" {
		t.Fatalf("route %q", got)
	}
	if len(d.Call.Messages) != 1 || d.Call.Messages[0].Role != aipapi.RoleUser {
		t.Fatalf("messages: %+v", d.Call.Messages)
	}
	if len(d.Call.Messages[0].Parts) != 1 || d.Call.Messages[0].Parts[0].Kind != aipapi.PartText {
		t.Fatalf("parts: %+v", d.Call.Messages[0].Parts)
	}
	if d.Call.Messages[0].Parts[0].Text != "ping" {
		t.Fatalf("text %q", d.Call.Messages[0].Parts[0].Text)
	}
	if d.Call.Tools == nil {
		t.Fatal("expected empty tools slice, got nil")
	}
	if len(d.Call.Tools) != 0 {
		t.Fatalf("tools: %+v", d.Call.Tools)
	}
	if d.Call.Instructions == nil {
		t.Fatal("expected empty instructions slice, got nil")
	}
	if len(d.Call.Instructions) != 0 {
		t.Fatalf("instructions: %+v", d.Call.Instructions)
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeCreate_PromptCacheKey(t *testing.T) {
	for _, tc := range []struct {
		raw, want string
		invalid   bool
	}{
		{`" client-owned-key "`, "client-owned-key", false},
		{`null`, "", false},
		{`42`, "", true},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			d, err := openairesponses.DecodeCreateRequest([]byte(`{"model":"m","input":"hi","prompt_cache_key":`+tc.raw+`}`), openairesponses.DecodeOptions{RouteSelector: "stub:m"})
			if tc.invalid {
				if err == nil {
					t.Fatal("non-string key accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if d.Call.PromptCacheKey != tc.want {
				t.Fatalf("key = %q, want %q", d.Call.PromptCacheKey, tc.want)
			}
			if _, ok := d.Call.Extensions[openrouterwire.ExtraBodyExtPrefix+"prompt_cache_key"]; ok {
				t.Fatal("canonical key duplicated as provider extra body")
			}
		})
	}
}

func TestDecodeCreate_nestedTextVerbosityBecomesCanonicalAndPreservesSiblings(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"gpt-4o-mini","input":"hi","text":{"verbosity":" HIGH ","format":{"type":"text"}}}`)
	d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.Options.Verbosity != aipapi.VerbosityHigh {
		t.Fatalf("verbosity = %q, want high", d.Call.Options.Verbosity)
	}
	textRaw := d.Call.Extensions[openrouterwire.ExtraBodyExtPrefix+"text"]
	if string(textRaw) != `{"format":{"type":"text"}}` {
		t.Fatalf("preserved text fields = %s", textRaw)
	}

	bad := []byte(`{"model":"gpt-4o-mini","input":"hi","text":{"verbosity":"extreme"}}`)
	if _, err := openairesponses.DecodeCreateRequest(bad, openairesponses.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"}); err == nil {
		t.Fatal("expected invalid verbosity error")
	}
}

func TestDecodeCreate_textStream(t *testing.T) {
	t.Parallel()
	body := readGolden(t, "create_text_stream.json")
	d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !d.Stream {
		t.Fatal("expected stream true")
	}
}

func TestDecodeCreate_invocationMetadata_nonStream(t *testing.T) {
	t.Parallel()
	body := readGolden(t, "create_text_nonstream.json")
	d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.Invocation.Operation != aipapi.OperationOpenAIResponses {
		t.Fatalf("operation = %q, want %q", d.Call.Invocation.Operation, aipapi.OperationOpenAIResponses)
	}
	if d.Call.Invocation.DeliveryMode != aipapi.DeliveryModeNonStreaming {
		t.Fatalf("delivery mode = %q, want %q", d.Call.Invocation.DeliveryMode, aipapi.DeliveryModeNonStreaming)
	}
}

func TestDecodeCreate_invocationMetadata_stream(t *testing.T) {
	t.Parallel()
	body := readGolden(t, "create_text_stream.json")
	d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.Invocation.Operation != aipapi.OperationOpenAIResponses {
		t.Fatalf("operation = %q, want %q", d.Call.Invocation.Operation, aipapi.OperationOpenAIResponses)
	}
	if d.Call.Invocation.DeliveryMode != aipapi.DeliveryModeStreaming {
		t.Fatalf("delivery mode = %q, want %q", d.Call.Invocation.DeliveryMode, aipapi.DeliveryModeStreaming)
	}
}

func TestDecodeCreate_multimodal(t *testing.T) {
	t.Parallel()
	body := readGolden(t, "create_multimodal_nonstream.json")
	d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err != nil {
		t.Fatal(err)
	}
	parts := d.Call.Messages[0].Parts
	if len(parts) != 3 {
		t.Fatalf("want 3 parts, got %d", len(parts))
	}
	if parts[0].Kind != aipapi.PartText || parts[0].Text != "describe attachments" {
		t.Fatalf("part0: %+v", parts[0])
	}
	if parts[1].Kind != aipapi.PartImageRef || !strings.Contains(parts[1].ImageRef, "base64,") {
		t.Fatalf("part1: %+v", parts[1])
	}
	if parts[1].ImageMIME != "image/png" {
		t.Fatalf("image mime %q", parts[1].ImageMIME)
	}
	if parts[2].Kind != aipapi.PartFileRef || parts[2].FileMIME != "application/pdf" {
		t.Fatalf("part2: %+v", parts[2])
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeCreate_requiresRoute(t *testing.T) {
	t.Parallel()
	body := readGolden(t, "create_text_nonstream.json")
	_, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{RouteSelector: ""})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDecodeCreate_invalidJSON(t *testing.T) {
	t.Parallel()
	_, err := openairesponses.DecodeCreateRequest([]byte("{"), openairesponses.DecodeOptions{
		RouteSelector: "stub:m",
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDecodeCreate_functionCallInputItem(t *testing.T) {
	t.Parallel()
	const body = `{"model":"gpt-4o-mini","input":[{"type":"function_call","id":"x","call_id":"c","name":"n","arguments":"{\"a\":1}"}]}`
	d, err := openairesponses.DecodeCreateRequest([]byte(body), openairesponses.DecodeOptions{
		RouteSelector: "stub:m",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Call.Messages) != 1 {
		t.Fatalf("messages: %d", len(d.Call.Messages))
	}
	m := d.Call.Messages[0]
	if m.Role != aipapi.RoleAssistant || len(m.Parts) != 1 || m.Parts[0].Kind != aipapi.PartJSON {
		t.Fatalf("unexpected message: %+v", m)
	}
	part := m.Parts[0]
	if part.ToolCallID != "c" || part.ToolName != "n" {
		t.Fatalf("tool identity: call_id=%q name=%q", part.ToolCallID, part.ToolName)
	}
	if got := string(part.Content); got != `{"a":1}` {
		t.Fatalf("arguments = %s, want the exact argument document", got)
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

// TestDecodeCreate_functionCallHistoryPreservesOrderAndExactArguments proves a
// two-call Responses history reaches the canonical message view with its exact
// tool identity, argument bytes, and item order, and that the ordered-item
// projector used by OpenResponses-compatible backends consumes it without an
// envelope or a reordering.
func TestDecodeCreate_functionCallHistoryPreservesOrderAndExactArguments(t *testing.T) {
	t.Parallel()
	const body = `{
  "model":"gpt-4o-mini",
  "stream":true,
  "input":[
    {"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},
    {"type":"function_call","id":"fc_1","call_id":"call_1","name":"close_agent","arguments":"{\"target\":\"nope\",\"nested\":{\"b\":2,\"a\":1}}"},
    {"type":"function_call_output","call_id":"call_1","output":"err"},
    {"type":"function_call","id":"fc_2","call_id":"call_2","name":"exec_command","arguments":{"cmd":"echo hi"}},
    {"type":"function_call_output","call_id":"call_2","output":"hi"}
  ]
}`
	d, err := openairesponses.DecodeCreateRequest([]byte(body), openairesponses.DecodeOptions{
		RouteSelector: "stub:m",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
	wantRoles := []aipapi.Role{aipapi.RoleUser, aipapi.RoleAssistant, aipapi.RoleTool, aipapi.RoleAssistant, aipapi.RoleTool}
	if len(d.Call.Messages) != len(wantRoles) {
		t.Fatalf("messages = %d, want %d: %+v", len(d.Call.Messages), len(wantRoles), d.Call.Messages)
	}
	for i, want := range wantRoles {
		if d.Call.Messages[i].Role != want {
			t.Fatalf("Messages[%d].Role = %q, want %q", i, d.Call.Messages[i].Role, want)
		}
	}
	wantCalls := []struct {
		callID, name, arguments string
	}{
		{"call_1", "close_agent", `{"target":"nope","nested":{"b":2,"a":1}}`},
		{"call_2", "exec_command", `{"cmd":"echo hi"}`},
	}
	for i, want := range wantCalls {
		part := d.Call.Messages[1+i*2].Parts[0]
		if part.Kind != aipapi.PartJSON || part.ToolCallID != want.callID || part.ToolName != want.name {
			t.Fatalf("call %d part identity: %+v", i, part)
		}
		if got := string(part.Content); got != want.arguments {
			t.Fatalf("call %d arguments = %s, want %s", i, got, want.arguments)
		}
		result := d.Call.Messages[2+i*2].Parts[0]
		if result.Kind != aipapi.PartToolResult || result.ToolCallID != want.callID {
			t.Fatalf("call %d result: %+v", i, result)
		}
	}

	items, _, err := aipapi.ProjectLegacyToOrderedItems(*d.Call, aipapi.DefaultOrderedItemProjectionTarget())
	if err != nil {
		t.Fatalf("ordered projection: %v", err)
	}
	wantKinds := []aipapi.ItemKind{
		aipapi.ItemKindMessage,
		aipapi.ItemKindToolCall,
		aipapi.ItemKindToolResult,
		aipapi.ItemKindToolCall,
		aipapi.ItemKindToolResult,
	}
	if len(items) != len(wantKinds) {
		t.Fatalf("items = %d, want %d: %+v", len(items), len(wantKinds), items)
	}
	for i, want := range wantKinds {
		if items[i].Kind != want {
			t.Fatalf("Items[%d].Kind = %q, want %q", i, items[i].Kind, want)
		}
	}
	for i, want := range wantCalls {
		toolCall := items[1+i*2].ToolCall
		if toolCall == nil || toolCall.CallID != want.callID || toolCall.Name != want.name {
			t.Fatalf("item call %d identity: %+v", i, toolCall)
		}
		if got := string(toolCall.Arguments); got != want.arguments {
			t.Fatalf("item call %d arguments = %s, want %s", i, got, want.arguments)
		}
	}
}

// TestDecodeCreate_functionCallArgumentForms pins how the Responses wire
// argument carrier (a JSON string or a JSON document) becomes one canonical
// argument document without dropping or inventing content.
func TestDecodeCreate_functionCallArgumentForms(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		argumentsJSON string
		want          string
	}{
		{name: "string object document", argumentsJSON: `"{\"a\": 1}"`, want: `{"a": 1}`},
		{name: "object document", argumentsJSON: `{"a": 1}`, want: `{"a": 1}`},
		{name: "array string", argumentsJSON: `"[1,2]"`, want: `[1,2]`},
		{name: "string document keeps its quoted argument text", argumentsJSON: `"\"hello\""`, want: `"hello"`},
		{name: "absent", argumentsJSON: ``, want: `{}`},
		{name: "null", argumentsJSON: `null`, want: `{}`},
		{name: "empty string normalizes to the empty object document", argumentsJSON: `""`, want: `{}`},
		{name: "non-json string", argumentsJSON: `"not json"`, want: `"not json"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			arguments := ""
			if tc.argumentsJSON != "" {
				arguments = `,"arguments":` + tc.argumentsJSON
			}
			body := `{"model":"m","input":[{"type":"function_call","call_id":"c","name":"n"` + arguments + `}]}`
			d, err := openairesponses.DecodeCreateRequest([]byte(body), openairesponses.DecodeOptions{
				RouteSelector: "stub:m",
			})
			if err != nil {
				t.Fatal(err)
			}
			part := d.Call.Messages[0].Parts[0]
			if got := string(part.Content); got != tc.want {
				t.Fatalf("arguments = %s, want %s", got, tc.want)
			}
			if err := d.Call.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDecodeCreate_skipsEmptyNamedFunctionCallFragments(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model":"gpt-4o-mini",
  "input":[
    {"type":"message","role":"user","content":"hi"},
    {"type":"function_call","call_id":"call_good","name":"bash","arguments":"{\"command\":\"ls\"}"},
    {"type":"function_call","call_id":"call_bad","name":"","arguments":"\"}\""},
    {"type":"function_call_output","call_id":"call_good","output":"{\"output\":\"ok\"}"},
    {"type":"function_call_output","call_id":"","output":"{\"output\":\"Tool  not found\"}"}
  ]
}`)
	d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
		RouteSelector: "stub:m",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Call.Messages) != 3 {
		t.Fatalf("messages len=%d want 3; got %+v", len(d.Call.Messages), d.Call.Messages)
	}
	if d.Call.Messages[0].Role != aipapi.RoleUser {
		t.Fatalf("msg0 role=%q", d.Call.Messages[0].Role)
	}
	if d.Call.Messages[1].Role != aipapi.RoleAssistant {
		t.Fatalf("msg1 role=%q", d.Call.Messages[1].Role)
	}
	if d.Call.Messages[2].Role != aipapi.RoleTool {
		t.Fatalf("msg2 role=%q", d.Call.Messages[2].Role)
	}
	if d.Call.Messages[2].Parts[0].ToolCallID != "call_good" {
		t.Fatalf("tool result call_id=%q", d.Call.Messages[2].Parts[0].ToolCallID)
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeCreate_reasoningInputItem_RED(t *testing.T) {
	t.Parallel()
	const body = `{"model":"gpt-4o-mini","input":[{"type":"reasoning","id":"r1","summary":[{"type":"summary_text","text":"s"}],"content":[{"type":"reasoning_text","text":"c"}],"encrypted_content":"enc"}]}`
	d, err := openairesponses.DecodeCreateRequest([]byte(body), openairesponses.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err != nil {
		t.Fatalf("RED: reasoning input item must decode: %v", err)
	}
	var reasoning *aipapi.Part
	for _, m := range d.Call.Messages {
		if m.Role != aipapi.RoleAssistant {
			continue
		}
		for i := range m.Parts {
			if m.Parts[i].Kind == aipapi.PartReasoning {
				reasoning = &m.Parts[i]
				break
			}
		}
	}
	if reasoning == nil || reasoning.Reasoning == nil {
		t.Fatalf("RED: expected PartReasoning for reasoning input item, got messages: %+v", d.Call.Messages)
	}
	if reasoning.Reasoning.Dialect != aipapi.ReasoningDialectOpenAIResponsesItemV1 {
		t.Fatalf("RED: dialect = %q, want %q", reasoning.Reasoning.Dialect, aipapi.ReasoningDialectOpenAIResponsesItemV1)
	}
	if len(reasoning.Reasoning.Opaque) == 0 {
		t.Fatal("RED: responses reasoning item must preserve wire fields in Opaque")
	}
	var wire struct {
		ID               string          `json:"id"`
		EncryptedContent string          `json:"encrypted_content"`
		Summary          json.RawMessage `json:"summary"`
		Content          json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(reasoning.Reasoning.Opaque, &wire); err != nil {
		t.Fatalf("RED: opaque must be valid reasoning item JSON: %v", err)
	}
	if wire.ID != "r1" {
		t.Fatalf("RED: opaque id = %q, want r1", wire.ID)
	}
	if wire.EncryptedContent != "enc" {
		t.Fatalf("RED: opaque encrypted_content = %q, want enc", wire.EncryptedContent)
	}
}

func TestDecodeCreate_unsupportedInputItemType(t *testing.T) {
	t.Parallel()
	const body = `{"model":"gpt-4o-mini","input":[{"type":"computer_call","id":"c1"}]}`
	_, err := openairesponses.DecodeCreateRequest([]byte(body), openairesponses.DecodeOptions{
		RouteSelector: "stub:m",
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDecodeCreate_inputString(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"gpt-4o-mini","stream":false,"input":"plain user string"}`)
	d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Call.Messages) != 1 || d.Call.Messages[0].Role != aipapi.RoleUser {
		t.Fatalf("messages: %+v", d.Call.Messages)
	}
	if len(d.Call.Messages[0].Parts) != 1 || d.Call.Messages[0].Parts[0].Text != "plain user string" {
		t.Fatalf("parts: %+v", d.Call.Messages[0].Parts)
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeCreate_instructionsNonStringRejected(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"gpt-4o-mini","stream":false,"input":"hi","instructions":[{"type":"text","text":"sys"}]}`)
	_, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "instructions must be a JSON string") {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestDecodeCreate_emptyInstructionsYieldsEmptySlice(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"gpt-4o-mini","stream":false,"input":"hi","instructions":"   "}`)
	d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.Instructions == nil {
		t.Fatal("expected empty instructions slice, got nil")
	}
	if len(d.Call.Instructions) != 0 {
		t.Fatalf("instructions: %+v", d.Call.Instructions)
	}
}

func TestDecodeCreate_toolsSamplingAndParallelToolCalls(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "stream": false,
  "temperature": 0.25,
  "top_p": 0.9,
  "max_output_tokens": 512,
  "parallel_tool_calls": true,
  "tools": [
    {
      "type": "function",
      "function": {
        "name": "get_weather",
        "description": "Weather lookup",
        "parameters": {"type": "object", "properties": {"city": {"type": "string"}}}
      }
    }
  ],
  "input": [{"type": "message", "role": "user", "content": "ping"}]
}`)
	d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.ToolChoice.Mode != aipapi.ToolChoiceAuto {
		t.Fatalf("tool choice mode %q", d.Call.ToolChoice.Mode)
	}
	if len(d.Call.Tools) != 1 || d.Call.Tools[0].Name != "get_weather" {
		t.Fatalf("tools: %+v", d.Call.Tools)
	}
	if d.Call.Tools[0].Description != "Weather lookup" {
		t.Fatal(d.Call.Tools[0].Description)
	}
	if string(d.Call.Tools[0].Parameters) == "" {
		t.Fatal("expected parameters JSON")
	}
	if d.Call.Options.Temperature == nil || *d.Call.Options.Temperature != 0.25 {
		t.Fatalf("temperature %+v", d.Call.Options.Temperature)
	}
	if d.Call.Options.TopP == nil || *d.Call.Options.TopP != 0.9 {
		t.Fatalf("top_p %+v", d.Call.Options.TopP)
	}
	if d.Call.Options.MaxOutputTokens == nil || *d.Call.Options.MaxOutputTokens != 512 {
		t.Fatalf("max_output_tokens %+v", d.Call.Options.MaxOutputTokens)
	}
	if d.Call.Options.ParallelToolCalls == nil || !*d.Call.Options.ParallelToolCalls {
		t.Fatalf("parallel_tool_calls %+v", d.Call.Options.ParallelToolCalls)
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

// Flat tool objects (type + name at top level) appear in some OpenAI SDK unions; decode must accept them.
func TestDecodeCreate_toolsFlatSDKShape(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "stream": false,
  "tools": [
    {
      "type": "function",
      "name": "flat_fn",
      "description": "d",
      "parameters": {"type": "object", "properties": {"x": {"type": "string"}}}
    }
  ],
  "input": [{"type": "message", "role": "user", "content": "ping"}]
}`)
	d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Call.Tools) != 1 || d.Call.Tools[0].Name != "flat_fn" || d.Call.Tools[0].Description != "d" {
		t.Fatalf("tools: %+v", d.Call.Tools)
	}
	if string(d.Call.Tools[0].Parameters) == "" {
		t.Fatal("expected parameters JSON")
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeCreate_toolChoiceFunctionObject(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "stream": false,
  "tool_choice": {"type": "function", "function": {"name": "get_weather"}},
  "tools": [{"type": "function", "function": {"name": "get_weather", "parameters": {}}}],
  "input": [{"type": "message", "role": "user", "content": "ping"}]
}`)
	d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.ToolChoice.Mode != aipapi.ToolChoiceRequired || d.Call.ToolChoice.Name != "get_weather" {
		t.Fatalf("tool_choice got %+v", d.Call.ToolChoice)
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeCreate_toolChoiceRequiredString(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "stream": false,
  "tool_choice": "required",
  "tools": [{"type": "function", "function": {"name": "get_weather", "parameters": {}}}],
  "input": [{"type": "message", "role": "user", "content": "ping"}]
}`)
	d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.ToolChoice.Mode != aipapi.ToolChoiceAny || d.Call.ToolChoice.Name != "" {
		t.Fatalf("tool_choice got %+v (OpenAI required -> canonical any-tool)", d.Call.ToolChoice)
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeCreate_developerRoleMapsToSystem(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "stream": false,
  "input": [{"type": "message", "role": "developer", "content": "You are helpful."}]
}`)
	d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Call.Messages) != 1 || d.Call.Messages[0].Role != aipapi.RoleSystem {
		t.Fatalf("messages: %+v", d.Call.Messages)
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeCreate_functionCallOutputItem(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "stream": false,
  "input": [{"type":"function_call_output","call_id":"call_1","output":"{\"ok\":true}"}]
}`)
	d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Call.Messages) != 1 || d.Call.Messages[0].Role != aipapi.RoleTool {
		t.Fatalf("messages: %+v", d.Call.Messages)
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeCreate_unsupportedRoleInMessage(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "stream": false,
  "input": [{"type":"message","role":"narrator","content":"x"}]
}`)
	_, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err == nil {
		t.Fatal("expected error for unsupported role")
	}
	if !strings.Contains(err.Error(), "unsupported role") {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestDecodeCreate_unsupportedContentBlockInMessage(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "stream": false,
  "input": [{"type":"message","role":"user","content":[{"type":"output_text","text":"x"}]}]
}`)
	_, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err == nil {
		t.Fatal("expected error for unsupported content block type")
	}
	if !strings.Contains(err.Error(), "unsupported content block type") {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestDecodeCreate_unsupportedToolType(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "stream": false,
  "tools": [{"type":"file_search"}],
  "input": [{"type":"message","role":"user","content":"q"}]
}`)
	_, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "unsupported type") {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestDecodeCreate_instructionsPlusMessagesTurn(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "stream": false,
  "instructions": "You are concise.",
  "input": [
    {"type":"message","role":"user","content":"first"},
    {"type":"message","role":"assistant","content":"ack"},
    {"type":"message","role":"user","content":"second"}
  ]
}`)
	d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Call.Instructions) != 1 || d.Call.Instructions[0].Parts[0].Text != "You are concise." {
		t.Fatalf("instructions: %+v", d.Call.Instructions)
	}
	if len(d.Call.Messages) != 3 {
		t.Fatalf("messages count %d", len(d.Call.Messages))
	}
	if d.Call.Messages[0].Role != aipapi.RoleUser || d.Call.Messages[1].Role != aipapi.RoleAssistant {
		t.Fatalf("roles: %+v / %+v", d.Call.Messages[0].Role, d.Call.Messages[1].Role)
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeCreate_toolChoice_auto(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "input": "hi",
  "tools": [{"type":"function","name":"fn","parameters":{}}],
  "tool_choice": "auto"
}`)
	d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.ToolChoice.Mode != aipapi.ToolChoiceAuto {
		t.Fatalf("mode: %q", d.Call.ToolChoice.Mode)
	}
}

func TestDecodeCreate_toolChoice_none(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "input": "hi",
  "tool_choice": "none"
}`)
	d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.ToolChoice.Mode != aipapi.ToolChoiceNone {
		t.Fatalf("mode: %q", d.Call.ToolChoice.Mode)
	}
}

func TestDecodeCreate_toolChoice_required(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "input": "hi",
  "tools": [{"type":"function","name":"fn","parameters":{}}],
  "tool_choice": "required"
}`)
	d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.ToolChoice.Mode != aipapi.ToolChoiceAny {
		t.Fatalf("mode: %q", d.Call.ToolChoice.Mode)
	}
}

func TestDecodeCreate_toolChoice_functionObject(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "input": "hi",
  "tools": [{"type":"function","name":"pick_me","parameters":{}}],
  "tool_choice": {"type":"function","function":{"name":"pick_me"}}
}`)
	d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.ToolChoice.Mode != aipapi.ToolChoiceRequired || d.Call.ToolChoice.Name != "pick_me" {
		t.Fatalf("choice: %+v", d.Call.ToolChoice)
	}
}

func TestDecodeCreate_toolChoice_functionObject_missingName(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "input": "hi",
  "tool_choice": {"type":"function","function":{}}
}`)
	_, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
	if err == nil {
		t.Fatal("expected error for missing function name")
	}
}

func TestDecodeCreate_modelExtensionMatchesJSONMarshal(t *testing.T) {
	t.Parallel()
	model := "a<b"
	body, err := json.Marshal(map[string]any{
		"model": model,
		"input": "hi",
	})
	if err != nil {
		t.Fatal(err)
	}
	d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{RouteSelector: "stub:x"})
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	raw := d.Call.Extensions["openairesponses.model"]
	if string(raw) != string(want) {
		t.Fatalf("extension %q want %q", raw, want)
	}
	if openairesponses.ModelFromCall(d.Call) != model {
		t.Fatalf("ModelFromCall got %q", openairesponses.ModelFromCall(d.Call))
	}
}

func TestDecodeCreate_openRouterBodyFieldsPassthrough(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "openai/gpt-4o-mini",
  "input": "hi",
  "provider": {"order":["OpenAI"]},
  "models": ["openai/gpt-4o"],
  "route": "fallback",
  "plugins": [{"id":"web"}],
  "debug": true,
  "service_tier": "default",
  "session_id": "sess-abc",
  "stop_server_tools_when": "tool_call",
  "reasoning": {"effort":"high"}
}`)
	d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{RouteSelector: "openrouter:openai/gpt-4o-mini"})
	if err != nil {
		t.Fatal(err)
	}
	ext := d.Call.Extensions
	if openrouterwire.GetRaw(ext, openrouterwire.ExtProvider) == nil {
		t.Error("missing provider extension")
	}
	if openrouterwire.GetRaw(ext, openrouterwire.ExtModels) == nil {
		t.Error("missing models extension")
	}
	if openrouterwire.GetString(ext, openrouterwire.ExtRoute) != "fallback" {
		t.Errorf("route: %s", ext[openrouterwire.ExtRoute])
	}
	if openrouterwire.GetRaw(ext, openrouterwire.ExtPlugins) == nil {
		t.Error("missing plugins extension")
	}
	if openrouterwire.GetRaw(ext, openrouterwire.ExtDebug) == nil {
		t.Error("missing debug extension")
	}
	if openrouterwire.GetString(ext, openrouterwire.ExtServiceTier) != "default" {
		t.Errorf("service_tier: %s", ext[openrouterwire.ExtServiceTier])
	}
	if openrouterwire.GetString(ext, openrouterwire.ExtSessionID) != "sess-abc" {
		t.Errorf("session_id: %s", ext[openrouterwire.ExtSessionID])
	}
	if openrouterwire.GetRaw(ext, openrouterwire.ExtReasoning) == nil {
		t.Error("missing reasoning extension")
	}
	if got := openrouterwire.GetString(ext, openrouterwire.ExtUpstreamFlavor); got != openrouterwire.FlavorResponses {
		t.Errorf("upstream flavor: got %q want %q", got, openrouterwire.FlavorResponses)
	}
}

func TestDecodeCreate_openRouterHeadersPassthrough(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"gpt-4o-mini","input":"hi"}`)
	h := http.Header{}
	h.Set("HTTP-Referer", "https://myapp.com")
	h.Set("X-Title", "FallbackTitle")
	d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
		RouteSelector: "openrouter:gpt-4o-mini",
		Headers:       h,
	})
	if err != nil {
		t.Fatal(err)
	}
	ext := d.Call.Extensions
	if openrouterwire.GetString(ext, openrouterwire.ExtHTTPReferer) != "https://myapp.com" {
		t.Errorf("referer: %s", ext[openrouterwire.ExtHTTPReferer])
	}
	if openrouterwire.GetString(ext, openrouterwire.ExtTitle) != "FallbackTitle" {
		t.Errorf("title: %s", ext[openrouterwire.ExtTitle])
	}
}

func TestDecodeCreate_extraBodyFieldsCaptured(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"nvidia/test","input":"hi","chat_template_kwargs":{"enable_thinking":true},"custom_number":42}`)
	d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
		RouteSelector: "nvidia:nvidia/test",
	})
	if err != nil {
		t.Fatal(err)
	}
	ext := d.Call.Extensions
	ctk := ext[openrouterwire.ExtraBodyExtPrefix+"chat_template_kwargs"]
	if string(ctk) != `{"enable_thinking":true}` {
		t.Errorf("chat_template_kwargs: got %s", ctk)
	}
	cn := ext[openrouterwire.ExtraBodyExtPrefix+"custom_number"]
	if string(cn) != `42` {
		t.Errorf("custom_number: got %s", cn)
	}
	if _, ok := ext[openrouterwire.ExtraBodyExtPrefix+"model"]; ok {
		t.Error("known field 'model' should not be captured as extra_body")
	}
}

func TestDecodeCreate_capturesClientUserAgent(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"gpt-4o-mini","input":"hi"}`)
	h := http.Header{}
	h.Set("User-Agent", "ResponsesClient/1.0")
	h.Set("Authorization", "Bearer sk-secret")
	d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
		Headers:       h,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.Invocation.ClientUserAgent != "ResponsesClient/1.0" {
		t.Fatalf("ClientUserAgent: %q", d.Call.Invocation.ClientUserAgent)
	}
	if h.Get("Authorization") != "Bearer sk-secret" {
		t.Fatal("headers mutated")
	}
}

func TestDecodeCreate_credentialsOnlyLeavesUserAgentEmpty(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"gpt-4o-mini","input":"hi"}`)
	h := http.Header{}
	h.Set("Authorization", "Bearer sk-secret")
	h.Set("Cookie", "session=abc")
	h.Set("HTTP-Referer", "https://example.com/")
	h.Set("X-Title", "Example")
	d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
		Headers:       h,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.Invocation.ClientUserAgent != "" {
		t.Fatalf("ClientUserAgent: %q", d.Call.Invocation.ClientUserAgent)
	}
}

func TestDecodeCreate_invalidUserAgentDropped(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"gpt-4o-mini","input":"hi"}`)
	h := http.Header{}
	h.Set("User-Agent", "bad\ragent")
	d, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
		Headers:       h,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.Invocation.ClientUserAgent != "" {
		t.Fatalf("invalid UA should be dropped, got %q", d.Call.Invocation.ClientUserAgent)
	}
}
