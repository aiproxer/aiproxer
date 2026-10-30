package gemini_test

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/gemini"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestDecodeGenerateContent_minimalUserText(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "contents": [{"role":"user","parts":[{"text":"ping"}]}],
  "generationConfig": {"maxOutputTokens": 128, "temperature": 0.5, "topP": 0.9}
}`)
	d, err := gemini.DecodeGenerateContentRequest(body, gemini.DecodeOptions{
		RouteSelector: "stub:gemini-2.0-flash",
		Model:         "gemini-2.0-flash",
		Stream:        false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Stream {
		t.Fatal("expected stream false")
	}
	if d.Model != "gemini-2.0-flash" {
		t.Fatalf("model %q", d.Model)
	}
	if gemini.ModelFromCall(d.Call) != "gemini-2.0-flash" {
		t.Fatal("model extension")
	}
	if got := d.Call.Route.Selector; got != "stub:gemini-2.0-flash" {
		t.Fatalf("route %q", got)
	}
	if d.Call.Options.MaxOutputTokens == nil || *d.Call.Options.MaxOutputTokens != 128 {
		t.Fatalf("maxOutputTokens: %+v", d.Call.Options.MaxOutputTokens)
	}
	if d.Call.Options.Temperature == nil || *d.Call.Options.Temperature != 0.5 {
		t.Fatalf("temperature: %+v", d.Call.Options.Temperature)
	}
	if d.Call.Options.TopP == nil || *d.Call.Options.TopP != 0.9 {
		t.Fatalf("topP: %+v", d.Call.Options.TopP)
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

func TestDecodeGenerateContent_systemInstruction(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "systemInstruction": {"role":"user","parts":[{"text":"Be brief."}]},
  "contents": [{"role":"user","parts":[{"text":"hi"}]}]
}`)
	d, err := gemini.DecodeGenerateContentRequest(body, gemini.DecodeOptions{
		RouteSelector: "stub:gemini-2.0-flash",
		Model:         "gemini-2.0-flash",
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

func TestDecodeGenerateContent_streamFlag(t *testing.T) {
	t.Parallel()
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	d, err := gemini.DecodeGenerateContentRequest(body, gemini.DecodeOptions{
		RouteSelector: "stub:x",
		Model:         "gemini-2.0-flash",
		Stream:        true,
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

func TestDecodeGenerateContent_nonStreamFlag(t *testing.T) {
	t.Parallel()
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	d, err := gemini.DecodeGenerateContentRequest(body, gemini.DecodeOptions{
		RouteSelector: "stub:x",
		Model:         "gemini-2.0-flash",
		Stream:        false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Stream {
		t.Fatal("expected stream false")
	}
	if d.Call.Invocation.DeliveryMode != aipapi.DeliveryModeNonStreaming {
		t.Fatalf("delivery mode = %q, want %q", d.Call.Invocation.DeliveryMode, aipapi.DeliveryModeNonStreaming)
	}
}

func TestDecodeGenerateContent_operationGeminiGenerateContent(t *testing.T) {
	t.Parallel()
	for _, stream := range []bool{false, true} {
		body := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
		d, err := gemini.DecodeGenerateContentRequest(body, gemini.DecodeOptions{RouteSelector: "stub:x", Model: "gemini-2.0-flash", Stream: stream})
		if err != nil {
			t.Fatalf("stream=%v: %v", stream, err)
		}
		if d.Call.Invocation.Operation != aipapi.OperationGeminiGenerateContent {
			t.Fatalf("stream=%v operation %q want %q", stream, d.Call.Invocation.Operation, aipapi.OperationGeminiGenerateContent)
		}
	}
}

func TestDecodeGenerateContent_invalidJSON(t *testing.T) {
	t.Parallel()
	_, err := gemini.DecodeGenerateContentRequest([]byte(`{`), gemini.DecodeOptions{
		RouteSelector: "stub:x",
		Model:         "m",
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDecodeGenerateContent_missingRouteSelector(t *testing.T) {
	t.Parallel()
	_, err := gemini.DecodeGenerateContentRequest([]byte(`{"contents":[{"parts":[{"text":"x"}]}]}`), gemini.DecodeOptions{
		Model: "m",
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDecodeGenerateContent_missingModel(t *testing.T) {
	t.Parallel()
	_, err := gemini.DecodeGenerateContentRequest([]byte(`{"contents":[{"parts":[{"text":"x"}]}]}`), gemini.DecodeOptions{
		RouteSelector: "stub:x",
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDecodeGenerateContent_multimodalInline(t *testing.T) {
	t.Parallel()
	pngB64 := base64.StdEncoding.EncodeToString([]byte{0x89, 0x50})
	pdfB64 := base64.StdEncoding.EncodeToString([]byte("%PDF-1."))
	body := []byte(`{
  "contents": [{
    "role": "user",
    "parts": [
      {"text": "describe"},
      {"inlineData": {"mimeType": "image/png", "data": "` + pngB64 + `"}},
      {"inline_data": {"mime_type": "application/pdf", "data": "` + pdfB64 + `"}}
    ]
  }]
}`)
	d, err := gemini.DecodeGenerateContentRequest(body, gemini.DecodeOptions{
		RouteSelector: "stub:gemini-2.0-flash",
		Model:         "gemini-2.0-flash",
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

func TestDecodeGenerateContent_modelRole(t *testing.T) {
	t.Parallel()
	body := []byte(`{"contents":[{"role":"model","parts":[{"text":"hello"}]}]}`)
	d, err := gemini.DecodeGenerateContentRequest(body, gemini.DecodeOptions{
		RouteSelector: "stub:x",
		Model:         "gemini-2.0-flash",
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.Messages[0].Role != aipapi.RoleAssistant {
		t.Fatalf("role %q", d.Call.Messages[0].Role)
	}
}

func TestDecodeGenerateContent_generationConfig(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "contents": [{"role":"user","parts":[{"text":"x"}]}],
  "generationConfig": {"temperature": 0.7, "topP": 0.95, "maxOutputTokens": 123}
}`)
	d, err := gemini.DecodeGenerateContentRequest(body, gemini.DecodeOptions{
		RouteSelector: "stub:gemini-2.0-flash",
		Model:         "gemini-2.0-flash",
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.Options.Temperature == nil || *d.Call.Options.Temperature != 0.7 {
		t.Fatalf("temperature %+v", d.Call.Options.Temperature)
	}
	if d.Call.Options.TopP == nil || *d.Call.Options.TopP != 0.95 {
		t.Fatalf("topP %+v", d.Call.Options.TopP)
	}
	if d.Call.Options.MaxOutputTokens == nil || *d.Call.Options.MaxOutputTokens != 123 {
		t.Fatalf("maxOutputTokens %+v", d.Call.Options.MaxOutputTokens)
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeGenerateContent_toolsAndToolConfig(t *testing.T) {
	t.Parallel()
	t.Run("tools_and_auto_tool_config", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{
  "contents": [{"role":"user","parts":[{"text":"x"}]}],
  "tools": [{
    "functionDeclarations": [
      {"name": "todo_add", "description": "Add item", "parameters": {"type": "object"}}
    ]
  }],
  "toolConfig": {"functionCallingConfig": {"mode": "AUTO"}}
}`)
		d, err := gemini.DecodeGenerateContentRequest(body, gemini.DecodeOptions{
			RouteSelector: "stub:gemini-2.0-flash",
			Model:         "gemini-2.0-flash",
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(d.Call.Tools) != 1 || d.Call.Tools[0].Name != "todo_add" {
			t.Fatalf("tools %+v", d.Call.Tools)
		}
		if d.Call.ToolChoice.Mode != aipapi.ToolChoiceAuto {
			t.Fatal(d.Call.ToolChoice.Mode)
		}
		if err := d.Call.Validate(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("tool_config_any", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{
  "contents": [{"role":"user","parts":[{"text":"x"}]}],
  "tools": [{"functionDeclarations": [{"name": "a", "parameters": {}}]}],
  "toolConfig": {"functionCallingConfig": {"mode": "ANY"}}
}`)
		d, err := gemini.DecodeGenerateContentRequest(body, gemini.DecodeOptions{
			RouteSelector: "stub:gemini-2.0-flash",
			Model:         "gemini-2.0-flash",
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
	t.Run("tool_config_none_without_tools", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{
  "contents": [{"role":"user","parts":[{"text":"x"}]}],
  "toolConfig": {"functionCallingConfig": {"mode": "NONE"}}
}`)
		d, err := gemini.DecodeGenerateContentRequest(body, gemini.DecodeOptions{
			RouteSelector: "stub:gemini-2.0-flash",
			Model:         "gemini-2.0-flash",
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
	t.Run("tool_config_allowed_function_names_mode_auto", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{
  "contents": [{"role":"user","parts":[{"text":"x"}]}],
  "tools": [{"functionDeclarations": [{"name": "only_one", "parameters": {}}]}],
  "toolConfig": {"functionCallingConfig": {"mode": "AUTO", "allowedFunctionNames": ["only_one"]}}
}`)
		d, err := gemini.DecodeGenerateContentRequest(body, gemini.DecodeOptions{
			RouteSelector: "stub:gemini-2.0-flash",
			Model:         "gemini-2.0-flash",
		})
		if err != nil {
			t.Fatal(err)
		}
		if d.Call.ToolChoice.Mode != aipapi.ToolChoiceAuto {
			t.Fatalf("mode %+v (allowedFunctionNames are not mapped to canonical ToolChoice.Name in v1)", d.Call.ToolChoice)
		}
		if err := d.Call.Validate(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestDecodeGenerateContent_unsupportedContentRole(t *testing.T) {
	t.Parallel()
	body := []byte(`{"contents":[{"role":"system","parts":[{"text":"bad"}]}]}`)
	_, err := gemini.DecodeGenerateContentRequest(body, gemini.DecodeOptions{
		RouteSelector: "stub:gemini-2.0-flash",
		Model:         "gemini-2.0-flash",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "unsupported role") {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestDecodeGenerateContent_toolConfigUnsupportedMode(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "contents": [{"role":"user","parts":[{"text":"x"}]}],
  "toolConfig": {"functionCallingConfig": {"mode": "UNKNOWN_MODE_X"}}
}`)
	_, err := gemini.DecodeGenerateContentRequest(body, gemini.DecodeOptions{
		RouteSelector: "stub:gemini-2.0-flash",
		Model:         "gemini-2.0-flash",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "unsupported toolConfig.functionCallingConfig.mode") {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestDecodeGenerateContent_emptyPartsRejected(t *testing.T) {
	t.Parallel()
	_, err := gemini.DecodeGenerateContentRequest([]byte(`{"contents":[{"role":"user","parts":[]}]}`), gemini.DecodeOptions{
		RouteSelector: "stub:gemini-2.0-flash",
		Model:         "gemini-2.0-flash",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "parts is required") {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestDecodeGenerateContent_functionCallPart(t *testing.T) {
	t.Parallel()
	t.Run("valid", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"contents":[{"role":"model","parts":[{"functionCall":{"name":"my_tool","args":{"a":1}}}]}]}`)
		d, err := gemini.DecodeGenerateContentRequest(body, gemini.DecodeOptions{
			RouteSelector: "stub:gemini-2.0-flash",
			Model:         "gemini-2.0-flash",
		})
		if err != nil {
			t.Fatal(err)
		}
		p := d.Call.Messages[0].Parts[0]
		if p.Kind != aipapi.PartJSON || p.ToolName != "my_tool" || string(p.Content) != `{"a":1}` {
			t.Fatalf("unexpected part: %+v", p)
		}
	})
	t.Run("missing_name", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"contents":[{"role":"model","parts":[{"functionCall":{"args":{"a":1}}}]}]}`)
		_, err := gemini.DecodeGenerateContentRequest(body, gemini.DecodeOptions{
			RouteSelector: "stub:gemini-2.0-flash",
			Model:         "gemini-2.0-flash",
		})
		if err == nil {
			t.Fatal("expected error for missing name")
		}
	})
	t.Run("empty_args", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"contents":[{"role":"model","parts":[{"functionCall":{"name":"my_tool"}}]}]}`)
		d, err := gemini.DecodeGenerateContentRequest(body, gemini.DecodeOptions{
			RouteSelector: "stub:gemini-2.0-flash",
			Model:         "gemini-2.0-flash",
		})
		if err != nil {
			t.Fatal(err)
		}
		p := d.Call.Messages[0].Parts[0]
		if string(p.Content) != `{}` {
			t.Fatalf("unexpected args fallback: %s", p.Content)
		}
	})
}

func TestDecodeGenerateContent_unsupportedPartRejected(t *testing.T) {
	t.Parallel()
	body := []byte(`{"contents":[{"role":"user","parts":[{"executableCode":{"language":"PYTHON","code":"1"}}]}]}`)
	_, err := gemini.DecodeGenerateContentRequest(body, gemini.DecodeOptions{
		RouteSelector: "stub:gemini-2.0-flash",
		Model:         "gemini-2.0-flash",
	})
	if err == nil {
		t.Fatal("expected error for unsupported part shape")
	}
	if !strings.Contains(err.Error(), "unsupported part") {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestDecodeGenerateContent_functionResponse(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "contents": [
    {"role":"user","parts":[{"text":"call the tool"}]},
    {"role":"model","parts":[{"functionCall":{"name":"get_weather","args":{"city":"NY"}}}]},
    {"role":"user","parts":[{
      "functionResponse":{
        "name":"get_weather",
        "response":{"temperature":22,"unit":"C"}
      }
    }]}
  ]
}`)
	d, err := gemini.DecodeGenerateContentRequest(body, gemini.DecodeOptions{
		RouteSelector: "stub:gemini-2.0-flash",
		Model:         "gemini-2.0-flash",
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
	if p.ToolCallID != "get_weather" {
		t.Fatalf("tool_call_id: %q", p.ToolCallID)
	}
	if !strings.Contains(p.Text, "temperature") {
		t.Fatalf("response text: %q", p.Text)
	}
}

func TestDecodeGenerateContent_functionResponse_snakeCase(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "contents": [
    {"role":"user","parts":[{"text":"x"}]},
    {"role":"user","parts":[{
      "function_response":{
        "name":"lookup",
        "response":{"result":"ok"}
      }
    }]}
  ]
}`)
	d, err := gemini.DecodeGenerateContentRequest(body, gemini.DecodeOptions{
		RouteSelector: "stub:gemini-2.0-flash",
		Model:         "gemini-2.0-flash",
	})
	if err != nil {
		t.Fatal(err)
	}
	p := d.Call.Messages[1].Parts[0]
	if p.Kind != aipapi.PartToolResult {
		t.Fatalf("kind: %q", p.Kind)
	}
	if p.ToolCallID != "lookup" {
		t.Fatalf("tool_call_id: %q", p.ToolCallID)
	}
}

func TestDecodeGenerateContent_functionCall_snakeCase(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "contents": [
    {"role":"model","parts":[{"function_call":{"name":"search","args":{"q":"go"}}}]}
  ]
}`)
	d, err := gemini.DecodeGenerateContentRequest(body, gemini.DecodeOptions{
		RouteSelector: "stub:gemini-2.0-flash",
		Model:         "gemini-2.0-flash",
	})
	if err != nil {
		t.Fatal(err)
	}
	parts := d.Call.Messages[0].Parts
	if len(parts) != 1 {
		t.Fatalf("parts: %+v", parts)
	}
	p := parts[0]
	if p.Kind != aipapi.PartJSON {
		t.Fatalf("kind: %q", p.Kind)
	}
	if p.ToolName != "search" {
		t.Fatalf("tool name: %q", p.ToolName)
	}
	if !strings.Contains(string(p.Content), `"q"`) {
		t.Fatalf("content: %s", string(p.Content))
	}
	if err := d.Call.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeGenerateContent_functionResponse_missingName(t *testing.T) {
	t.Parallel()
	body := []byte(`{
  "contents": [
    {"role":"user","parts":[{
      "functionResponse":{"response":{"x":1}}
    }]}
  ]
}`)
	_, err := gemini.DecodeGenerateContentRequest(body, gemini.DecodeOptions{
		RouteSelector: "stub:gemini-2.0-flash",
		Model:         "gemini-2.0-flash",
	})
	if err == nil {
		t.Fatal("expected error for missing name")
	}
}

func TestDecodeGenerateContent_capturesClientUserAgent(t *testing.T) {
	t.Parallel()
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"ping"}]}]}`)
	h := http.Header{}
	h.Set("User-Agent", "GeminiClient/4.0")
	h.Set("Authorization", "Bearer sk-secret")
	d, err := gemini.DecodeGenerateContentRequest(body, gemini.DecodeOptions{
		RouteSelector: "stub:gemini-2.0-flash",
		Model:         "gemini-2.0-flash",
		Headers:       h,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.Invocation.ClientUserAgent != "GeminiClient/4.0" {
		t.Fatalf("ClientUserAgent: %q", d.Call.Invocation.ClientUserAgent)
	}
	if h.Get("Authorization") != "Bearer sk-secret" {
		t.Fatal("headers mutated")
	}
}

func TestDecodeGenerateContent_invalidUserAgentDropped(t *testing.T) {
	t.Parallel()
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"ping"}]}]}`)
	h := http.Header{}
	h.Set("User-Agent", "bad\nagent")
	d, err := gemini.DecodeGenerateContentRequest(body, gemini.DecodeOptions{
		RouteSelector: "stub:gemini-2.0-flash",
		Model:         "gemini-2.0-flash",
		Headers:       h,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Call.Invocation.ClientUserAgent != "" {
		t.Fatalf("invalid UA should be dropped, got %q", d.Call.Invocation.ClientUserAgent)
	}
}
