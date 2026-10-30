package openailegacy_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	"github.com/aiproxer/aiproxer/internal/plugins/openrouterwire"
	"github.com/aiproxer/aiproxer/internal/refclient/refclienttest"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func readGolden(t *testing.T, name string) []byte {
	t.Helper()
	p := filepath.Join(refclienttest.ModuleRoot(t), "testdata", "openailegacy_frontend", name)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDecodeChat_textNonStream(t *testing.T) {
	t.Parallel()
	body := readGolden(t, "create_text_nonstream.json")
	d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{
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
	if openailegacy.ModelFromCall(d.Call) != "gpt-4o-mini" {
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
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeChat_PromptCacheKey(t *testing.T) {
	for _, tc := range []struct {
		raw, want string
		invalid   bool
	}{
		{`" client-owned-key "`, "client-owned-key", false},
		{`null`, "", false},
		{`42`, "", true},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			d, err := openailegacy.DecodeChatRequest([]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"prompt_cache_key":`+tc.raw+`}`), openailegacy.DecodeOptions{RouteSelector: "stub:m"})
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

func TestDecodeChat_verbosityIsCanonicalAndValidated(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}],"verbosity":" HIGH "}`)
	d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.Options.Verbosity != aipapi.VerbosityHigh {
		t.Fatalf("verbosity = %q, want high", d.Call.Options.Verbosity)
	}

	bad := []byte(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}],"verbosity":"extreme"}`)
	if _, err := openailegacy.DecodeChatRequest(bad, openailegacy.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"}); err == nil {
		t.Fatal("expected invalid verbosity error")
	}
}

func TestDecodeChat_textStream(t *testing.T) {
	t.Parallel()
	body := readGolden(t, "create_text_stream.json")
	d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !d.Stream {
		t.Fatal("expected stream true")
	}
}

func TestDecodeChat_invocationMetadata_nonStream(t *testing.T) {
	t.Parallel()
	body := readGolden(t, "create_text_nonstream.json")
	d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.Invocation.Operation != aipapi.OperationOpenAIChatCompletions {
		t.Fatalf("operation = %q, want %q", d.Call.Invocation.Operation, aipapi.OperationOpenAIChatCompletions)
	}
	if d.Call.Invocation.DeliveryMode != aipapi.DeliveryModeNonStreaming {
		t.Fatalf("delivery mode = %q, want %q", d.Call.Invocation.DeliveryMode, aipapi.DeliveryModeNonStreaming)
	}
}

func TestDecodeChat_invocationMetadata_stream(t *testing.T) {
	t.Parallel()
	body := readGolden(t, "create_text_stream.json")
	d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.Invocation.Operation != aipapi.OperationOpenAIChatCompletions {
		t.Fatalf("operation = %q, want %q", d.Call.Invocation.Operation, aipapi.OperationOpenAIChatCompletions)
	}
	if d.Call.Invocation.DeliveryMode != aipapi.DeliveryModeStreaming {
		t.Fatalf("delivery mode = %q, want %q", d.Call.Invocation.DeliveryMode, aipapi.DeliveryModeStreaming)
	}
}

func TestDecodeChat_multimodal(t *testing.T) {
	t.Parallel()
	body := readGolden(t, "create_multimodal_nonstream.json")
	d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{
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

func TestDecodeChat_requiresRoute(t *testing.T) {
	t.Parallel()
	body := readGolden(t, "create_text_nonstream.json")
	_, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{RouteSelector: ""})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDecodeChat_invalidJSON(t *testing.T) {
	t.Parallel()
	_, err := openailegacy.DecodeChatRequest([]byte("{"), openailegacy.DecodeOptions{
		RouteSelector: "stub:m",
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDecodeChat_toolsAndToolChoice(t *testing.T) {
	t.Parallel()
	t.Run("auto", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{
  "model": "gpt-4o-mini",
  "messages": [{"role":"user","content":"x"}],
  "tool_choice": "auto",
  "tools": [{"type":"function","function":{"name":"fn_a","description":"d","parameters":{"type":"object"}}}]
}`)
		d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
		if err != nil {
			t.Fatal(err)
		}
		if d.Call.ToolChoice.Mode != aipapi.ToolChoiceAuto || len(d.Call.Tools) != 1 || d.Call.Tools[0].Name != "fn_a" {
			t.Fatalf("got tools=%+v choice=%+v", d.Call.Tools, d.Call.ToolChoice)
		}
		if err := d.Call.Validate(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("none_without_tools", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"x"}],"tool_choice":"none"}`)
		d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
		if err != nil {
			t.Fatal(err)
		}
		if d.Call.ToolChoice.Mode != aipapi.ToolChoiceNone {
			t.Fatal(d.Call.ToolChoice.Mode)
		}
		if err := d.Call.Validate(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("function_by_name", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{
  "model": "gpt-4o-mini",
  "messages": [{"role":"user","content":"x"}],
  "tool_choice": {"type":"function","function":{"name":"pick"}},
  "tools": [{"type":"function","function":{"name":"pick","parameters":{}}}]
}`)
		d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
		if err != nil {
			t.Fatal(err)
		}
		if d.Call.ToolChoice.Mode != aipapi.ToolChoiceRequired || d.Call.ToolChoice.Name != "pick" {
			t.Fatalf("choice %+v", d.Call.ToolChoice)
		}
		if err := d.Call.Validate(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("parallel_tool_calls", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{
  "model": "gpt-4o-mini",
  "messages": [{"role":"user","content":"x"}],
  "parallel_tool_calls": false,
  "tools": [{"type":"function","function":{"name":"t","parameters":{}}}]
}`)
		d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
		if err != nil {
			t.Fatal(err)
		}
		if d.Call.Options.ParallelToolCalls == nil || *d.Call.Options.ParallelToolCalls {
			t.Fatalf("parallel %+v", d.Call.Options.ParallelToolCalls)
		}
		if err := d.Call.Validate(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestDecodeChat_assistantToolCalls(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "messages": [{
    "role": "assistant",
    "content": null,
    "tool_calls": [{"id":"call_1","type":"function","function":{"name":"x","arguments":"{}"}}]
  }]
}`)
	d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Call.Messages) != 1 || len(d.Call.Messages[0].Parts) != 1 {
		t.Fatalf("parts: %#v", d.Call.Messages)
	}
	if d.Call.Messages[0].Parts[0].Kind != aipapi.PartJSON {
		t.Fatalf("want PartJSON, got %#v", d.Call.Messages[0].Parts[0])
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

// TestDecodeChat_assistantToolCallsEmptyContentString proves an assistant turn carrying only a
// tool call may use an empty content string (not just null), as some clients (e.g. OpenCode) emit.
func TestDecodeChat_assistantToolCallsEmptyContentString(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "messages": [{
    "role": "assistant",
    "content": "",
    "tool_calls": [{"id":"call_1","type":"function","function":{"name":"x","arguments":"{}"}}]
  }]
}`)
	d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(d.Call.Messages) != 1 || len(d.Call.Messages[0].Parts) != 1 {
		t.Fatalf("parts: %#v", d.Call.Messages)
	}
	if d.Call.Messages[0].Parts[0].Kind != aipapi.PartJSON {
		t.Fatalf("want PartJSON, got %#v", d.Call.Messages[0].Parts[0])
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

// TestDecodeChat_emptyAssistantMessageIsSkipped proves OpenAI-compatible
// clients may include empty assistant history entries after compaction.
func TestDecodeChat_emptyAssistantMessageIsSkipped(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "messages": [
    {"role":"user","content":"before"},
    {"role":"assistant","content":""},
    {"role":"user","content":"after"}
  ]
}`)
	d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := len(d.Call.Messages); got != 2 {
		t.Fatalf("messages = %d, want 2", got)
	}
	for _, m := range d.Call.Messages {
		if m.Role == aipapi.RoleAssistant {
			t.Fatalf("empty assistant message was not skipped: %#v", d.Call.Messages)
		}
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

// TestDecodeChat_reasoningEffortFromBody proves the Chat Completions body's reasoning_effort
// field is decoded into call.Options.ReasoningEffort before executor routing merges URI
// params. Route URI params override matching body options later in core execution.
func TestDecodeChat_reasoningEffortFromBody(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "openai-codex:openai/gpt-5.4-mini?reasoning_effort=xhigh",
  "reasoning_effort": "medium",
  "messages": [{"role":"user","content":"hi"}]
}`)
	d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{RouteSelector: "openai-codex:openai/gpt-5.4-mini?reasoning_effort=xhigh"})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if d.Call.Options.ReasoningEffort != "medium" {
		t.Fatalf("ReasoningEffort %q, want %q (body field must be decoded)", d.Call.Options.ReasoningEffort, "medium")
	}
}

func TestDecodeChat_assistantFunctionCallLegacy(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "messages": [{
    "role": "assistant",
    "content": "ok",
    "function_call": {"name": "legacy_fn", "arguments": "{}"}
  }]
}`)
	d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Call.Messages[0].Parts) != 2 {
		t.Fatalf("parts: %d", len(d.Call.Messages[0].Parts))
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeChat_toolRoleMessage(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "messages": [
    {"role": "user", "content": "call the tool"},
    {"role": "tool", "tool_call_id": "call_abc", "content": "{\"temp\":21}"}
  ]
}`)
	d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Call.Messages) != 2 {
		t.Fatalf("messages: %+v", d.Call.Messages)
	}
	tm := d.Call.Messages[1]
	if tm.Role != aipapi.RoleTool || len(tm.Parts) != 1 || tm.Parts[0].Kind != aipapi.PartToolResult {
		t.Fatalf("tool message: %+v", tm)
	}
	if tm.Parts[0].ToolCallID != "call_abc" {
		t.Fatalf("tool_call_id: %+v", tm.Parts[0])
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeChat_toolRoleMessageArrayContent(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "messages": [
    {"role": "user", "content": "call the tool"},
    {"role": "tool", "tool_call_id": "call_abc", "content": [{"type":"text","text":"ok"}]}
  ]
}`)
	d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
	if err != nil {
		t.Fatal(err)
	}
	tm := d.Call.Messages[1]
	if len(tm.Parts) != 1 {
		t.Fatalf("tool message parts: %+v", tm.Parts)
	}
	if tm.Parts[0].Kind != aipapi.PartToolResult {
		t.Fatalf("want tool_result part, got %v", tm.Parts[0].Kind)
	}
	testkit.AssertJSONEqual(t, []byte(`[{"type":"text","text":"ok"}]`), []byte(tm.Parts[0].Content))
}

func TestDecodeChat_toolRoleMessageEmptyContent(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "messages": [
    {"role": "user", "content": "call the tool"},
    {"role": "tool", "tool_call_id": "call_abc", "content": ""}
  ]
}`)
	_, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
	if err == nil {
		t.Fatal("expected error for empty tool content")
	}
}

func TestDecodeChat_streamOptionsExtension(t *testing.T) {
	t.Parallel()
	want := json.RawMessage(`{"include_usage":true}`)
	body := []byte(`{
  "model": "gpt-4o-mini",
  "messages": [{"role":"user","content":"x"}],
  "stream_options": ` + string(want) + `
}`)
	d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
	if err != nil {
		t.Fatal(err)
	}
	raw, ok := d.Call.Extensions["openailegacy.stream_options"]
	if !ok {
		t.Fatal("missing stream_options extension")
	}
	if string(raw) != string(want) {
		t.Fatalf("extension got %s want %s", raw, want)
	}
}

// Chat Completions API: tool_choice may be the string "required" (forces model to call a tool).
func TestDecodeChat_toolChoiceRequiredString(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "messages": [{"role":"user","content":"x"}],
  "tool_choice": "required",
  "tools": [{"type":"function","function":{"name":"t","parameters":{}}}]
}`)
	d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.ToolChoice.Mode != aipapi.ToolChoiceAny || d.Call.ToolChoice.Name != "" {
		t.Fatalf("tool_choice %+v (OpenAI required -> canonical any-tool)", d.Call.ToolChoice)
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeChat_developerRoleMapsToSystem(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "messages": [{"role":"developer","content":"You are helpful."}]
}`)
	d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
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

func TestDecodeChat_unsupportedToolType(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "messages": [{"role":"user","content":"x"}],
  "tools": [{"type":"web_search_preview"}]
}`)
	_, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "unsupported type") {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestDecodeChat_unsupportedContentBlockRejected(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "gpt-4o-mini",
  "messages": [{
    "role": "user",
    "content": [{"type":"audio","audio":"AAAA"}]
  }]
}`)
	_, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{RouteSelector: "stub:gpt-4o-mini"})
	if err == nil {
		t.Fatal("expected error for unsupported content block")
	}
	if !strings.Contains(err.Error(), "unsupported content block type") {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestDecodeChat_openRouterBodyFieldsPassthrough(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "openai/gpt-4o-mini",
  "messages": [{"role":"user","content":"hi"}],
  "provider": {"order":["OpenAI"]},
  "models": ["openai/gpt-4o","anthropic/claude-3.5-sonnet"],
  "route": "fallback",
  "plugins": [{"id":"web"}],
  "debug": true,
  "service_tier": "default",
  "user": "user-123",
  "response_format": {"type":"json_object"}
}`)
	d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{RouteSelector: "openrouter:openai/gpt-4o-mini"})
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
	if openrouterwire.GetString(ext, openrouterwire.ExtUser) != "user-123" {
		t.Errorf("user: %s", ext[openrouterwire.ExtUser])
	}
	if openrouterwire.GetRaw(ext, openrouterwire.ExtResponseFormat) == nil {
		t.Error("missing response_format extension")
	}
	if got := openrouterwire.GetString(ext, openrouterwire.ExtUpstreamFlavor); got != openrouterwire.FlavorChat {
		t.Errorf("upstream flavor: got %q want %q", got, openrouterwire.FlavorChat)
	}
}

func TestDecodeChat_openRouterHeadersPassthrough(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`)
	h := http.Header{}
	h.Set("HTTP-Referer", "https://myapp.com")
	h.Set("X-OpenRouter-Title", "MyApp")
	h.Set("X-OpenRouter-Categories", "ai")
	d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{
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
	if openrouterwire.GetString(ext, openrouterwire.ExtTitle) != "MyApp" {
		t.Errorf("title: %s", ext[openrouterwire.ExtTitle])
	}
	if openrouterwire.GetString(ext, openrouterwire.ExtCategories) != "ai" {
		t.Errorf("categories: %s", ext[openrouterwire.ExtCategories])
	}
}

func TestDecodeChat_extraBodyFieldsCaptured(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"nvidia/test","messages":[{"role":"user","content":"hi"}],"chat_template_kwargs":{"enable_thinking":true},"custom_number":42}`)
	d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{
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

func TestDecodeChat_capturesClientUserAgent(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`)
	h := http.Header{}
	h.Set("user-agent", "LegacyClient/2.0")
	h.Set("Authorization", "Bearer sk-secret")
	d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
		Headers:       h,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.Invocation.ClientUserAgent != "LegacyClient/2.0" {
		t.Fatalf("ClientUserAgent: %q", d.Call.Invocation.ClientUserAgent)
	}
	if h.Get("Authorization") != "Bearer sk-secret" {
		t.Fatal("headers mutated")
	}
}

func TestDecodeChat_invalidUserAgentDropped(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`)
	h := http.Header{}
	h.Set("User-Agent", "bad\nagent")
	d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{
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

func TestDecodeChat_skipsEmptyNamedToolCallFragments(t *testing.T) {
	t.Parallel()
	// Agent harnesses (e.g. pi) sometimes replay a garbage second tool_calls
	// entry with empty id/name and fragment arguments like `"}`. Upstream
	// backends reject those with HTTP 400; drop them at decode instead.
	body := []byte(`{
  "model": "gpt-4o-mini",
  "messages": [
    {"role":"user","content":"study the repo"},
    {
      "role":"assistant",
      "content": null,
      "tool_calls": [
        {
          "id": "call_good",
          "type": "function",
          "function": {"name":"web_search","arguments":"{\"q\":\"x\"}"}
        },
        {
          "id": "",
          "type": "function",
          "function": {"name":"","arguments":"\"}\""}
        }
      ]
    },
    {
      "role":"tool",
      "tool_call_id":"call_good",
      "content":"search results"
    },
    {
      "role":"tool",
      "tool_call_id":"",
      "content":"Tool  not found"
    }
  ]
}`)
	d, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{
		RouteSelector: "stub:gpt-4o-mini",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Call.Messages) != 3 {
		t.Fatalf("messages len=%d want 3 (user+assistant+tool); got %+v", len(d.Call.Messages), d.Call.Messages)
	}
	asst := d.Call.Messages[1]
	if asst.Role != aipapi.RoleAssistant {
		t.Fatalf("message[1] role=%q", asst.Role)
	}
	var toolParts []aipapi.Part
	for _, p := range asst.Parts {
		if p.Kind == aipapi.PartJSON && p.ToolName != "" {
			toolParts = append(toolParts, p)
		}
	}
	if len(toolParts) != 1 {
		t.Fatalf("assistant tool parts=%d want 1; parts=%+v", len(toolParts), asst.Parts)
	}
	if toolParts[0].ToolCallID != "call_good" || toolParts[0].ToolName != "web_search" {
		t.Fatalf("kept tool part: %+v", toolParts[0])
	}
	tool := d.Call.Messages[2]
	if tool.Role != aipapi.RoleTool || len(tool.Parts) != 1 || tool.Parts[0].ToolCallID != "call_good" {
		t.Fatalf("tool message: %+v", tool)
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}
