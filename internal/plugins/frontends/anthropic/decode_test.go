package anthropic_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/anthropic"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestDecodeMessage_minimalUserText(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "claude-3-5-haiku-20241022",
  "max_tokens": 64,
  "messages": [{"role":"user","content":"ping"}]
}`)
	d, err := anthropic.DecodeMessageRequest(body, anthropic.DecodeOptions{
		RouteSelector: "stub:claude-3-5-haiku-20241022",
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Stream {
		t.Fatal("expected stream false")
	}
	if d.Model != "claude-3-5-haiku-20241022" {
		t.Fatalf("model %q", d.Model)
	}
	if anthropic.ModelFromCall(d.Call) != "claude-3-5-haiku-20241022" {
		t.Fatal("model extension")
	}
	if got := d.Call.Route.Selector; got != "stub:claude-3-5-haiku-20241022" {
		t.Fatalf("route %q", got)
	}
	if d.Call.Options.MaxOutputTokens == nil || *d.Call.Options.MaxOutputTokens != 64 {
		t.Fatalf("max tokens: %+v", d.Call.Options.MaxOutputTokens)
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
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeMessage_systemString(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "claude-3-5-haiku-20241022",
  "max_tokens": 64,
  "system": "Be brief.",
  "messages": [{"role":"user","content":"hi"}]
}`)
	d, err := anthropic.DecodeMessageRequest(body, anthropic.DecodeOptions{
		RouteSelector: "stub:claude-3-5-haiku-20241022",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Call.Instructions) != 1 || d.Call.Instructions[0].Role != aipapi.RoleSystem {
		t.Fatalf("instructions: %+v", d.Call.Instructions)
	}
	if d.Call.Instructions[0].Parts[0].Text != "Be brief." {
		t.Fatal(d.Call.Instructions[0].Parts[0].Text)
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeMessage_stream(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "claude-3-5-haiku-20241022",
  "max_tokens": 64,
  "stream": true,
  "messages": [{"role":"user","content":"hi"}]
}`)
	d, err := anthropic.DecodeMessageRequest(body, anthropic.DecodeOptions{
		RouteSelector: "stub:x",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !d.Stream {
		t.Fatal("expected stream true")
	}
	if d.Call.Invocation.DeliveryMode != aipapi.DeliveryModeStreaming {
		t.Fatalf("delivery mode = %q, want %q", d.Call.Invocation.DeliveryMode, aipapi.DeliveryModeStreaming)
	}
}

func TestDecodeMessage_operationAnthropicMessages(t *testing.T) {
	t.Parallel()
	for _, stream := range []bool{false, true} {
		body := []byte(`{"model":"claude-3-5-haiku-20241022","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)
		if stream {
			body = []byte(`{"model":"claude-3-5-haiku-20241022","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
		}
		d, err := anthropic.DecodeMessageRequest(body, anthropic.DecodeOptions{RouteSelector: "stub:x"})
		if err != nil {
			t.Fatalf("stream=%v: %v", stream, err)
		}
		if d.Call.Invocation.Operation != aipapi.OperationAnthropicMessages {
			t.Fatalf("stream=%v operation %q want %q", stream, d.Call.Invocation.Operation, aipapi.OperationAnthropicMessages)
		}
	}
}

func TestDecodeMessage_invalidJSON(t *testing.T) {
	t.Parallel()
	_, err := anthropic.DecodeMessageRequest([]byte(`{`), anthropic.DecodeOptions{
		RouteSelector: "stub:x",
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDecodeMessage_missingRouteSelector(t *testing.T) {
	t.Parallel()
	_, err := anthropic.DecodeMessageRequest([]byte(`{}`), anthropic.DecodeOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDecodeMessage_multimodal(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "claude-3-5-haiku-20241022",
  "max_tokens": 128,
  "messages": [{
    "role": "user",
    "content": [
      {"type":"text","text":"describe"},
      {"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAA"}},
      {"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"BBB"}}
    ]
  }]
}`)
	d, err := anthropic.DecodeMessageRequest(body, anthropic.DecodeOptions{
		RouteSelector: "stub:claude-3-5-haiku-20241022",
	})
	if err != nil {
		t.Fatal(err)
	}
	parts := d.Call.Messages[0].Parts
	if len(parts) != 3 {
		t.Fatalf("parts: %+v", parts)
	}
	if parts[1].Kind != aipapi.PartImageRef || !strings.Contains(parts[1].ImageRef, "base64,") {
		t.Fatalf("image part: %+v", parts[1])
	}
	if parts[2].Kind != aipapi.PartFileRef || parts[2].FileMIME != "application/pdf" {
		t.Fatalf("file part: %+v", parts[2])
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeMessage_toolsAndToolChoice(t *testing.T) {
	t.Parallel()
	t.Run("tools_only_auto_choice", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{
  "model": "claude-3-5-haiku-20241022",
  "max_tokens": 64,
  "messages": [{"role":"user","content":"x"}],
  "tools": [
    {"name": "get_time", "description": "Clock", "input_schema": {"type": "object", "properties": {}}}
  ]
}`)
		d, err := anthropic.DecodeMessageRequest(body, anthropic.DecodeOptions{
			RouteSelector: "stub:claude-3-5-haiku-20241022",
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(d.Call.Tools) != 1 || d.Call.Tools[0].Name != "get_time" || d.Call.Tools[0].Description != "Clock" {
			t.Fatalf("tools %+v", d.Call.Tools)
		}
		if d.Call.ToolChoice.Mode != aipapi.ToolChoiceAuto {
			t.Fatal(d.Call.ToolChoice.Mode)
		}
		if err := d.Call.Validate(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("tool_choice_none_without_tools", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{
  "model": "claude-3-5-haiku-20241022",
  "max_tokens": 64,
  "messages": [{"role":"user","content":"x"}],
  "tool_choice": {"type": "none"}
}`)
		d, err := anthropic.DecodeMessageRequest(body, anthropic.DecodeOptions{
			RouteSelector: "stub:claude-3-5-haiku-20241022",
		})
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
	t.Run("tool_choice_any_with_tools", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{
  "model": "claude-3-5-haiku-20241022",
  "max_tokens": 64,
  "messages": [{"role":"user","content":"x"}],
  "tool_choice": "any",
  "tools": [{"name": "alpha", "input_schema": {"type": "object"}}]
}`)
		d, err := anthropic.DecodeMessageRequest(body, anthropic.DecodeOptions{
			RouteSelector: "stub:claude-3-5-haiku-20241022",
		})
		if err != nil {
			t.Fatal(err)
		}
		if d.Call.ToolChoice.Mode != aipapi.ToolChoiceAny {
			t.Fatal(d.Call.ToolChoice.Mode)
		}
		if err := d.Call.Validate(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("tool_choice_tool_by_name", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{
  "model": "claude-3-5-haiku-20241022",
  "max_tokens": 64,
  "messages": [{"role":"user","content":"x"}],
  "tool_choice": {"type": "tool", "name": "pick_one"},
  "tools": [{"name": "pick_one", "input_schema": {"type": "object"}}]
}`)
		d, err := anthropic.DecodeMessageRequest(body, anthropic.DecodeOptions{
			RouteSelector: "stub:claude-3-5-haiku-20241022",
		})
		if err != nil {
			t.Fatal(err)
		}
		if d.Call.ToolChoice.Mode != aipapi.ToolChoiceRequired || d.Call.ToolChoice.Name != "pick_one" {
			t.Fatalf("choice %+v", d.Call.ToolChoice)
		}
		if err := d.Call.Validate(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestDecodeMessage_systemBlockArray(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "claude-3-5-haiku-20241022",
  "max_tokens": 64,
  "system": [
    {"type":"text","text":"First block."},
    {"type":"text","text":"Second block."}
  ],
  "messages": [{"role":"user","content":"hi"}]
}`)
	d, err := anthropic.DecodeMessageRequest(body, anthropic.DecodeOptions{
		RouteSelector: "stub:claude-3-5-haiku-20241022",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Call.Instructions) != 1 || d.Call.Instructions[0].Role != aipapi.RoleSystem {
		t.Fatalf("instructions: %+v", d.Call.Instructions)
	}
	parts := d.Call.Instructions[0].Parts
	if len(parts) != 2 || parts[0].Text != "First block." || parts[1].Text != "Second block." {
		t.Fatalf("system parts: %+v", parts)
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeMessage_metadataAndTopKIgnored(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "claude-3-5-haiku-20241022",
  "max_tokens": 64,
  "top_k": 40,
  "metadata": {"session":"abc"},
  "messages": [{"role":"user","content":"hi"}]
}`)
	d, err := anthropic.DecodeMessageRequest(body, anthropic.DecodeOptions{
		RouteSelector: "stub:claude-3-5-haiku-20241022",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Call.Messages) != 1 || d.Call.Messages[0].Parts[0].Text != "hi" {
		t.Fatalf("messages: %+v", d.Call.Messages)
	}
	if d.Call.Options.TopP != nil || d.Call.Options.Temperature != nil {
		t.Fatalf("unexpected sampling from top_k passthrough: %+v", d.Call.Options)
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeMessage_toolResultBlock_stringContent(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "claude-3-5-haiku-20241022",
  "max_tokens": 64,
  "messages": [
    {"role":"user","content":"hi"},
    {"role":"assistant","content":[{"type":"tool_use","id":"tu_1","name":"fn","input":{}}]},
    {"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_1","content":"42"}]}
  ]
}`)
	d, err := anthropic.DecodeMessageRequest(body, anthropic.DecodeOptions{
		RouteSelector: "stub:claude-3-5-haiku-20241022",
	})
	if err != nil {
		t.Fatal(err)
	}
	msgs := d.Call.Messages
	if len(msgs) != 3 {
		t.Fatalf("messages: %d", len(msgs))
	}
	lastMsg := msgs[2]
	if lastMsg.Role != aipapi.RoleUser {
		t.Fatalf("role: %q", lastMsg.Role)
	}
	if len(lastMsg.Parts) != 1 {
		t.Fatalf("parts: %+v", lastMsg.Parts)
	}
	p := lastMsg.Parts[0]
	if p.Kind != aipapi.PartToolResult {
		t.Fatalf("kind: %q", p.Kind)
	}
	if p.ToolCallID != "tu_1" {
		t.Fatalf("tool_use_id: %q", p.ToolCallID)
	}
	if p.Text != "42" {
		t.Fatalf("text: %q", p.Text)
	}
}

func TestDecodeMessage_toolResultBlock_arrayContent(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "claude-3-5-haiku-20241022",
  "max_tokens": 64,
  "messages": [
    {"role":"user","content":"hi"},
    {"role":"assistant","content":[{"type":"tool_use","id":"tu_2","name":"fn","input":{}}]},
    {"role":"user","content":[{
      "type":"tool_result",
      "tool_use_id":"tu_2",
      "content":[
        {"type":"text","text":"first"},
        {"type":"text","text":" second"}
      ]
    }]}
  ]
}`)
	d, err := anthropic.DecodeMessageRequest(body, anthropic.DecodeOptions{
		RouteSelector: "stub:claude-3-5-haiku-20241022",
	})
	if err != nil {
		t.Fatal(err)
	}
	p := d.Call.Messages[2].Parts[0]
	if p.Kind != aipapi.PartToolResult {
		t.Fatalf("kind: %q", p.Kind)
	}
	if p.ToolCallID != "tu_2" {
		t.Fatalf("tool_use_id: %q", p.ToolCallID)
	}
	if p.Text != "first second" {
		t.Fatalf("text: %q", p.Text)
	}
}

func TestDecodeMessage_toolResultBlock_missingToolUseID(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "claude-3-5-haiku-20241022",
  "max_tokens": 64,
  "messages": [
    {"role":"user","content":[{"type":"tool_result","content":"x"}]}
  ]
}`)
	_, err := anthropic.DecodeMessageRequest(body, anthropic.DecodeOptions{
		RouteSelector: "stub:claude-3-5-haiku-20241022",
	})
	if err == nil {
		t.Fatal("expected error for missing tool_use_id")
	}
}

func TestDecodeMessage_unsupportedRole(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "claude-3-5-haiku-20241022",
  "max_tokens": 64,
  "messages": [{"role":"tool","content":"result"}]
}`)
	_, err := anthropic.DecodeMessageRequest(body, anthropic.DecodeOptions{
		RouteSelector: "stub:claude-3-5-haiku-20241022",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "unsupported message role") {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestDecodeMessage_temperatureAndTopP(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "claude-3-5-haiku-20241022",
  "max_tokens": 64,
  "temperature": 0.35,
  "top_p": 0.88,
  "messages": [{"role":"user","content":"hi"}]
}`)
	d, err := anthropic.DecodeMessageRequest(body, anthropic.DecodeOptions{
		RouteSelector: "stub:claude-3-5-haiku-20241022",
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.Options.Temperature == nil || *d.Call.Options.Temperature != 0.35 {
		t.Fatalf("temperature %+v", d.Call.Options.Temperature)
	}
	if d.Call.Options.TopP == nil || *d.Call.Options.TopP != 0.88 {
		t.Fatalf("top_p %+v", d.Call.Options.TopP)
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeMessage_maxTokensZeroRejected(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "claude-3-5-haiku-20241022",
  "max_tokens": 0,
  "messages": [{"role":"user","content":"hi"}]
}`)
	_, err := anthropic.DecodeMessageRequest(body, anthropic.DecodeOptions{
		RouteSelector: "stub:claude-3-5-haiku-20241022",
	})
	if err == nil {
		t.Fatal("expected error for max_tokens <= 0")
	}
	if !strings.Contains(err.Error(), "max_tokens") {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestDecodeMessage_invalidToolChoiceString(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "claude-3-5-haiku-20241022",
  "max_tokens": 64,
  "messages": [{"role":"user","content":"hi"}],
  "tool_choice": "invalid_choice_xyz"
}`)
	_, err := anthropic.DecodeMessageRequest(body, anthropic.DecodeOptions{
		RouteSelector: "stub:claude-3-5-haiku-20241022",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "unsupported tool_choice string") {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestDecodeMessage_toolChoiceObject(t *testing.T) {
	t.Parallel()
	tests := []struct {
		desc       string
		toolChoice string
		wantMode   aipapi.ToolChoiceMode
		wantName   string
		wantErr    bool
	}{
		{"auto", `{"type":"auto"}`, aipapi.ToolChoiceAuto, "", false},
		{"any", `{"type":"any"}`, aipapi.ToolChoiceAny, "", false},
		{"none", `{"type":"none"}`, aipapi.ToolChoiceNone, "", false},
		{"tool_ok", `{"type":"tool","name":"my_tool"}`, aipapi.ToolChoiceRequired, "my_tool", false},
		{"tool_missing_name", `{"type":"tool"}`, "", "", true},
		{"invalid_type", `{"type":"unknown"}`, "", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()
			body := fmt.Appendf(nil, `{
			  "model": "claude-3",
			  "max_tokens": 64,
			  "messages": [{"role":"user","content":"hi"}],
			  "tool_choice": %s
			}`, tc.toolChoice)
			d, err := anthropic.DecodeMessageRequest(body, anthropic.DecodeOptions{RouteSelector: "stub:claude-3"})
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if d.Call.ToolChoice.Mode != tc.wantMode {
				t.Errorf("got mode %q, want %q", d.Call.ToolChoice.Mode, tc.wantMode)
			}
			if d.Call.ToolChoice.Name != tc.wantName {
				t.Errorf("got name %q, want %q", d.Call.ToolChoice.Name, tc.wantName)
			}
		})
	}
}

func TestDecodeMessage_imageNonBase64SourceRejected(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "claude-3-5-haiku-20241022",
  "max_tokens": 64,
  "messages": [{
    "role": "user",
    "content": [{"type":"image","source":{"type":"url","url":"https://example.com/x.png"}}]
  }]
}`)
	_, err := anthropic.DecodeMessageRequest(body, anthropic.DecodeOptions{
		RouteSelector: "stub:claude-3-5-haiku-20241022",
	})
	if err == nil {
		t.Fatal("expected error for non-base64 image source")
	}
	if !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestDecodeMessage_documentNonBase64SourceRejected(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "claude-3-5-haiku-20241022",
  "max_tokens": 64,
  "messages": [{
    "role": "user",
    "content": [{"type":"document","source":{"type":"url","url":"https://example.com/doc.pdf"}}]
  }]
}`)
	_, err := anthropic.DecodeMessageRequest(body, anthropic.DecodeOptions{
		RouteSelector: "stub:claude-3-5-haiku-20241022",
	})
	if err == nil {
		t.Fatal("expected error for non-base64 document source")
	}
	if !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestDecodeMessage_capturesClientUserAgent(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "claude-3-5-haiku-20241022",
  "max_tokens": 64,
  "messages": [{"role":"user","content":"ping"}]
}`)
	h := http.Header{}
	h.Set("User-Agent", "AnthropicClient/3.0")
	h.Set("x-api-key", "sk-secret")
	d, err := anthropic.DecodeMessageRequest(body, anthropic.DecodeOptions{
		RouteSelector: "stub:claude-3-5-haiku-20241022",
		Headers:       h,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.Invocation.ClientUserAgent != "AnthropicClient/3.0" {
		t.Fatalf("ClientUserAgent: %q", d.Call.Invocation.ClientUserAgent)
	}
	if h.Get("x-api-key") != "sk-secret" {
		t.Fatal("headers mutated")
	}
}

func TestDecodeMessage_credentialsOnlyLeavesUserAgentEmpty(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "claude-3-5-haiku-20241022",
  "max_tokens": 64,
  "messages": [{"role":"user","content":"ping"}]
}`)
	h := http.Header{}
	h.Set("x-api-key", "sk-secret")
	h.Set("Authorization", "Bearer sk-secret")
	h.Set("Cookie", "session=abc")
	d, err := anthropic.DecodeMessageRequest(body, anthropic.DecodeOptions{
		RouteSelector: "stub:claude-3-5-haiku-20241022",
		Headers:       h,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.Invocation.ClientUserAgent != "" {
		t.Fatalf("ClientUserAgent: %q", d.Call.Invocation.ClientUserAgent)
	}
}

func TestDecodeMessage_invalidUserAgentDropped(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "model": "claude-3-5-haiku-20241022",
  "max_tokens": 64,
  "messages": [{"role":"user","content":"ping"}]
}`)
	h := http.Header{}
	h.Set("User-Agent", "bad\nagent")
	d, err := anthropic.DecodeMessageRequest(body, anthropic.DecodeOptions{
		RouteSelector: "stub:claude-3-5-haiku-20241022",
		Headers:       h,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.Invocation.ClientUserAgent != "" {
		t.Fatalf("invalid UA should be dropped, got %q", d.Call.Invocation.ClientUserAgent)
	}
}
