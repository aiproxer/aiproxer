package openresponses

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestRequestCodec_OfficialGolden(t *testing.T) {
	t.Parallel()
	paramPath := filepath.Join("testdata", "official_examples", "ResponseParam.json")
	data, err := os.ReadFile(paramPath)
	if err != nil {
		t.Fatalf("failed to read official ResponseParam.json: %v", err)
	}

	param, call, err := DecodeRequest(data)
	if err != nil {
		t.Fatalf("DecodeRequest failed on official golden: %v", err)
	}

	if !call.HasItemAuthority() {
		t.Fatalf("expected call to have item authority")
	}

	if len(call.Items) != 1 {
		t.Fatalf("expected 1 item in call, got %d", len(call.Items))
	}

	item := call.Items[0]
	if item.Kind != aipapi.ItemKindMessage {
		t.Fatalf("expected item kind message, got %q", item.Kind)
	}
	if item.Role != aipapi.RoleUser {
		t.Fatalf("expected role user, got %q", item.Role)
	}
	if len(item.Content) != 1 || item.Content[0].Kind != aipapi.ContentPartText {
		t.Fatalf("expected 1 text content part, got %+v", item.Content)
	}
	if item.Content[0].Text != "What's the weather like in Paris today?" {
		t.Fatalf("expected text content, got %q", item.Content[0].Text)
	}

	reEncoded, err := EncodeRequest(call)
	if err != nil {
		t.Fatalf("EncodeRequest failed: %v", err)
	}

	_, reDecodedCall, err := DecodeRequest(reEncoded)
	if err != nil {
		t.Fatalf("DecodeRequest failed on re-encoded request: %v", err)
	}

	if len(reDecodedCall.Items) != len(call.Items) {
		t.Fatalf("re-decoded items count mismatch: want %d, got %d", len(call.Items), len(reDecodedCall.Items))
	}

	_ = param
}

func TestRequestCodec_StringInput(t *testing.T) {
	t.Parallel()
	data := []byte(`{"model": "gpt-4o", "input": "Hello world"}`)
	_, call, err := DecodeRequest(data)
	if err != nil {
		t.Fatalf("DecodeRequest failed for string input: %v", err)
	}

	if !call.HasItemAuthority() {
		t.Fatalf("expected item authority for string input")
	}
	if len(call.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(call.Items))
	}
	if call.Items[0].Role != aipapi.RoleUser || call.Items[0].Content[0].Text != "Hello world" {
		t.Fatalf("unexpected item for string input: %+v", call.Items[0])
	}
}

func TestRequestCodec_ItemUnionForms(t *testing.T) {
	t.Parallel()
	data := []byte(`{
		"model": "gpt-4o",
		"input": [
			{"type": "message", "id": "msg_1", "role": "system", "content": [{"type": "input_text", "text": "System prompt"}]},
			{"type": "item_reference", "id": "msg_prev"},
			{"type": "function_call", "id": "call_1", "call_id": "call_123", "name": "get_weather", "arguments": "{\"location\":\"Paris\"}"},
			{"type": "function_call_output", "call_id": "call_123", "output": "Sunny 22C"},
			{"type": "reasoning", "id": "reas_1", "reasoning": "Thinking step..."},
			{"type": "compaction", "id": "comp_1", "encapsulated_id": "enc_1", "dialect": "standard", "implementor": "proxy"},
			{"type": "acme:custom_type", "id": "ext_1", "namespace": "custom", "direction": "in", "data": {"key": "val"}}
		]
	}`)

	_, call, err := DecodeRequest(data)
	if err != nil {
		t.Fatalf("DecodeRequest failed for item union forms: %v", err)
	}

	if len(call.Items) != 7 {
		t.Fatalf("expected 7 items, got %d", len(call.Items))
	}

	if call.Items[0].Kind != aipapi.ItemKindMessage || call.Items[0].Role != aipapi.RoleSystem {
		t.Errorf("item 0 mismatch: %+v", call.Items[0])
	}
	if call.Items[1].Kind != aipapi.ItemKindItemReference || call.Items[1].Reference.ID != "msg_prev" {
		t.Errorf("item 1 mismatch: %+v", call.Items[1])
	}
	if call.Items[2].Kind != aipapi.ItemKindToolCall || call.Items[2].ToolCall.Name != "get_weather" {
		t.Errorf("item 2 mismatch: %+v", call.Items[2])
	}
	if call.Items[3].Kind != aipapi.ItemKindToolResult || call.Items[3].ToolResult.Output != "Sunny 22C" {
		t.Errorf("item 3 mismatch: %+v", call.Items[3])
	}
	if call.Items[4].Kind != aipapi.ItemKindReasoning || call.Items[4].Reasoning == nil {
		t.Errorf("item 4 mismatch: %+v", call.Items[4])
	}
	if call.Items[5].Kind != aipapi.ItemKindCompaction || call.Items[5].Compaction.EncapsulatedID != "enc_1" {
		t.Errorf("item 5 mismatch: %+v", call.Items[5])
	}
	if call.Items[6].Kind != aipapi.ItemKindExtension || call.Items[6].Extension.Namespace != "custom" {
		t.Errorf("item 6 mismatch: %+v", call.Items[6])
	}
}

func TestRequestCodec_ContentUnionForms(t *testing.T) {
	t.Parallel()
	data := []byte(`{
		"model": "gpt-4o",
		"input": [
			{
				"type": "message",
				"role": "user",
				"content": [
					{"type": "input_text", "text": "Text part"},
					{"type": "input_image", "image_url": "https://example.com/a.png"},
					{"type": "input_image", "image_url": {"url": "https://example.com/b.png"}},
					{"type": "refusal", "refusal": "Cannot fulfill request"}
				]
			}
		]
	}`)

	_, call, err := DecodeRequest(data)
	if err != nil {
		t.Fatalf("DecodeRequest failed for content union forms: %v", err)
	}

	if len(call.Items[0].Content) != 4 {
		t.Fatalf("expected 4 content parts, got %d", len(call.Items[0].Content))
	}
	if call.Items[0].Content[1].ImageRef != "https://example.com/a.png" {
		t.Errorf("image_url string mismatch: got %q", call.Items[0].Content[1].ImageRef)
	}
	if call.Items[0].Content[2].ImageRef != "https://example.com/b.png" {
		t.Errorf("image_url object mismatch: got %q", call.Items[0].Content[2].ImageRef)
	}
}

func TestRequestCodec_ToolAndControlParameters(t *testing.T) {
	t.Parallel()
	data := []byte(`{
		"model": "gpt-4o",
		"input": "test",
		"tools": [
			{"type": "function", "name": "fn1", "description": "desc", "parameters": {"type": "object"}}
		],
		"tool_choice": "required",
		"temperature": 0.5,
		"top_p": 0.9,
		"max_output_tokens": 100,
		"store": true
	}`)

	_, call, err := DecodeRequest(data)
	if err != nil {
		t.Fatalf("DecodeRequest failed for tool and control parameters: %v", err)
	}

	if len(call.Tools) != 1 || call.Tools[0].Name != "fn1" {
		t.Fatalf("tool decoding mismatch: %+v", call.Tools)
	}
	if call.ToolChoice.Mode != aipapi.ToolChoiceAny {
		t.Fatalf("tool_choice mode mismatch: got %v", call.ToolChoice.Mode)
	}
	if call.Options.Temperature == nil || *call.Options.Temperature != 0.5 {
		t.Fatalf("temperature mismatch: %v", call.Options.Temperature)
	}
}

const allowedToolsDeclaredTools = `"tools": [
		{"type": "function", "name": "fn1", "description": "desc", "parameters": {"type": "object"}},
		{"type": "function", "name": "fn2", "parameters": {"type": "object"}}
	]`

func TestRequestCodec_AllowedTools(t *testing.T) {
	t.Parallel()
	data := []byte(`{
		"model": "gpt-4o",
		"input": "test",
		` + allowedToolsDeclaredTools + `,
		"tool_choice": {
			"type": "allowed_tools",
			"tools": [{"type": "function", "name": "fn1"}]
		}
	}`)

	_, call, err := DecodeRequest(data)
	if err != nil {
		t.Fatalf("DecodeRequest failed for allowed_tools tool_choice: %v", err)
	}

	if call.ToolChoice.Mode != aipapi.ToolChoiceAuto {
		t.Fatalf("expected ToolChoiceAuto mode, got %v", call.ToolChoice.Mode)
	}
	if !slices.Equal(call.ToolChoice.AllowedTools, []string{"fn1"}) {
		t.Fatalf("expected allowed subset [fn1], got %v", call.ToolChoice.AllowedTools)
	}
	if call.ToolChoice.Name != "" {
		t.Fatalf("expected empty ToolChoice.Name, got %q", call.ToolChoice.Name)
	}
}

func TestRequestCodec_AllowedToolsEncodeRoundTrip(t *testing.T) {
	t.Parallel()
	data := []byte(`{
		"model": "gpt-4o",
		"input": "test",
		` + allowedToolsDeclaredTools + `,
		"tool_choice": {
			"type": "allowed_tools",
			"tools": [{"type": "function", "name": "fn2"}, {"type": "function", "name": "fn1"}],
			"mode": "required"
		}
	}`)

	_, call, err := DecodeRequest(data)
	if err != nil {
		t.Fatalf("DecodeRequest failed: %v", err)
	}
	if call.ToolChoice.Mode != aipapi.ToolChoiceAny {
		t.Fatalf("expected ToolChoiceAny for mode required, got %v", call.ToolChoice.Mode)
	}
	if !slices.Equal(call.ToolChoice.AllowedTools, []string{"fn2", "fn1"}) {
		t.Fatalf("expected allowed subset [fn2 fn1], got %v", call.ToolChoice.AllowedTools)
	}

	reEncoded, err := EncodeRequest(call)
	if err != nil {
		t.Fatalf("EncodeRequest failed: %v", err)
	}
	_, reDecodedCall, err := DecodeRequest(reEncoded)
	if err != nil {
		t.Fatalf("DecodeRequest failed on re-encoded request: %v", err)
	}
	if reDecodedCall.ToolChoice.Mode != aipapi.ToolChoiceAny {
		t.Fatalf("re-decoded mode mismatch: got %v", reDecodedCall.ToolChoice.Mode)
	}
	if !slices.Equal(reDecodedCall.ToolChoice.AllowedTools, []string{"fn2", "fn1"}) {
		t.Fatalf("re-decoded allowed subset mismatch: got %v", reDecodedCall.ToolChoice.AllowedTools)
	}
}

func TestRequestCodec_AllowedToolsModeVariants(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		json string
		want aipapi.ToolChoiceMode
	}{
		{name: "mode absent defaults to auto", json: `{"type":"allowed_tools","tools":[{"type":"function","name":"fn1"}]}`, want: aipapi.ToolChoiceAuto},
		{name: "mode auto", json: `{"type":"allowed_tools","tools":[{"type":"function","name":"fn1"}],"mode":"auto"}`, want: aipapi.ToolChoiceAuto},
		{name: "mode required", json: `{"type":"allowed_tools","tools":[{"type":"function","name":"fn1"}],"mode":"required"}`, want: aipapi.ToolChoiceAny},
		{name: "mode none", json: `{"type":"allowed_tools","tools":[{"type":"function","name":"fn1"}],"mode":"none"}`, want: aipapi.ToolChoiceNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := []byte(`{"model": "gpt-4o", "input": "test", ` + allowedToolsDeclaredTools + `, "tool_choice": ` + tc.json + `}`)
			_, call, err := DecodeRequest(body)
			if err != nil {
				t.Fatalf("DecodeRequest failed: %v", err)
			}
			if call.ToolChoice.Mode != tc.want {
				t.Fatalf("mode mismatch: got %v, want %v", call.ToolChoice.Mode, tc.want)
			}
			if !slices.Equal(call.ToolChoice.AllowedTools, []string{"fn1"}) {
				t.Fatalf("subset mismatch: got %v", call.ToolChoice.AllowedTools)
			}
		})
	}
}

func TestRequestCodec_AllowedToolsRejectsMalformed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		json string
	}{
		{name: "missing tools array", json: `{"type":"allowed_tools"}`},
		{name: "empty tools array", json: `{"type":"allowed_tools","tools":[]}`},
		{name: "tools not array", json: `{"type":"allowed_tools","tools":"fn1"}`},
		{name: "tool reference wrong type", json: `{"type":"allowed_tools","tools":[{"type":"hosted","name":"fn1"}]}`},
		{name: "tool reference missing type", json: `{"type":"allowed_tools","tools":[{"name":"fn1"}]}`},
		{name: "tool reference missing name", json: `{"type":"allowed_tools","tools":[{"type":"function"}]}`},
		{name: "tool reference empty name", json: `{"type":"allowed_tools","tools":[{"type":"function","name":""}]}`},
		{name: "tool reference unknown mode", json: `{"type":"allowed_tools","tools":[{"type":"function","name":"fn1"}],"mode":"bogus"}`},
		{name: "allowed tool not declared", json: `{"type":"allowed_tools","tools":[{"type":"function","name":"fn_missing"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := []byte(`{"model": "gpt-4o", "input": "test", ` + allowedToolsDeclaredTools + `, "tool_choice": ` + tc.json + `}`)
			if _, _, err := DecodeRequest(body); err == nil {
				t.Fatalf("expected error for allowed_tools %s", tc.json)
			}
		})
	}
}

func TestRequestCodec_AllowedToolsRefCountBound(t *testing.T) {
	t.Parallel()

	allowedTools := func(n int) []byte {
		var b strings.Builder
		b.WriteString(`{"type":"allowed_tools","tools":[`)
		for i := range n {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(`{"type":"function","name":"fn`)
			b.WriteString(strconv.Itoa(i))
			b.WriteString(`"}`)
		}
		b.WriteString(`]}`)
		return []byte(b.String())
	}

	if _, err := decodeToolChoice(allowedTools(aipapi.MaxAllowedToolRefs)); err != nil {
		t.Fatalf("exactly %d allowed tool refs must be accepted: %v", aipapi.MaxAllowedToolRefs, err)
	}
	if _, err := decodeToolChoice(allowedTools(aipapi.MaxAllowedToolRefs + 1)); err == nil {
		t.Fatalf("expected allowed_tools with more than %d refs to be rejected", aipapi.MaxAllowedToolRefs)
	}
}

func TestRequestCodec_PreviousResponseID_Continuation(t *testing.T) {
	t.Parallel()
	data := []byte(`{
		"model": "gpt-4o",
		"previous_response_id": "resp_123"
	}`)

	param, call, err := DecodeRequest(data)
	if err != nil {
		t.Fatalf("DecodeRequest failed for previous_response_id continuation: %v", err)
	}

	if param.PreviousResponseID == nil || *param.PreviousResponseID != "resp_123" {
		t.Fatalf("previous_response_id mismatch: %v", param.PreviousResponseID)
	}
	if len(call.Items) != 0 {
		t.Fatalf("expected 0 items for absent input continuation, got %d", len(call.Items))
	}
}

func TestRequestCodec_NegativeTable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		json    string
		wantErr string
	}{
		{
			name:    "MalformedJSON",
			json:    `{"model": "gpt-4o", "input": `,
			wantErr: "decode failed",
		},
		{
			name:    "DuplicateKey",
			json:    `{"model": "gpt-4o", "model": "gpt-3.5", "input": "test"}`,
			wantErr: "duplicate key",
		},
		{
			name:    "InvalidUTF8",
			json:    string([]byte{'{', '"', 'm', 'o', 'd', 'e', 'l', '"', ':', ' ', 0xff, '}'}),
			wantErr: "invalid UTF-8",
		},
		{
			name:    "UnknownUnprefixedDiscriminator",
			json:    `{"model": "gpt-4o", "input": [{"type": "unknown_magic_type"}]}`,
			wantErr: "unknown unprefixed discriminator",
		},
		{
			name:    "InventedContentDiscriminator",
			json:    `{"model": "gpt-4o", "input": [{"type": "message", "role": "user", "content": [{"type": "fancy_new_part", "value": 1}]}]}`,
			wantErr: "unknown unprefixed discriminator",
		},
		{
			name:    "UnsupportedBackgroundMode",
			json:    `{"model": "gpt-4o", "input": "hi", "background": true}`,
			wantErr: "background execution mode is unsupported",
		},
		{
			name:    "TrailingData",
			json:    `{"model": "gpt-4o", "input": "hi"} {"extra": 1}`,
			wantErr: "trailing data",
		},
		{
			name:    "InvalidInputType",
			json:    `{"model": "gpt-4o", "input": 12345}`,
			wantErr: "decode failed",
		},
		{
			name:    "MissingInputWithoutPreviousID",
			json:    `{"model": "gpt-4o"}`,
			wantErr: "input is required",
		},
		{
			name:    "ConflictingAuthority",
			json:    `{"model": "gpt-4o", "input": [{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "hi"}]}], "messages": [{"role": "user", "content": "hi"}]}`,
			wantErr: "conflicting raw item and legacy message authorities",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := DecodeRequest([]byte(tt.json))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
		})
	}
}

func TestRequestCodec_Immutability(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"model": "gpt-4o", "input": "Hello world"}`)

	_, call, err := DecodeRequest(raw)
	if err != nil {
		t.Fatalf("DecodeRequest failed: %v", err)
	}

	// Mutate source bytes
	for i := range raw {
		raw[i] = 0xFF
	}

	// Ensure decoded call content was not mutated
	if call.Items[0].Content[0].Text != "Hello world" {
		t.Fatalf("canonical call text was mutated when source bytes changed")
	}
}

func TestRequestCodec_WireDepthUsesConfiguredLimit(t *testing.T) {
	depth := 17
	parameters := strings.Repeat(`{"nested":`, depth) + `1` + strings.Repeat(`}`, depth)
	body := []byte(`{"model":"gpt-4o","input":"hi","tools":[{"type":"function","name":"deep","parameters":` + parameters + `}]}`)
	limits := DefaultLimits()
	limits.MaxItemDepth = 1
	if _, _, err := DecodeRequest(body, limits); err == nil || !strings.Contains(err.Error(), "exceeds limit 1") {
		t.Fatalf("wire JSON must use MaxItemDepth, got: %v", err)
	}
}

func TestRequestCodec_EmptyToolResultPreservesOutputField(t *testing.T) {
	wire, err := EncodeItem(aipapi.Item{
		Kind: aipapi.ItemKindToolResult,
		ToolResult: &aipapi.ToolResultItem{
			CallID: "call_empty",
		},
	})
	if err != nil {
		t.Fatalf("EncodeItem failed: %v", err)
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		t.Fatalf("marshal encoded item: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("decode encoded item: %v", err)
	}
	if got := string(fields["output"]); got != `""` {
		t.Fatalf("output field = %s, want empty JSON string", got)
	}
}

func TestRequestCodec_MessageContentStringShorthand(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		role     string
		phase    string
		expected string
	}{
		"user":         {role: "user", expected: "Say hello in exactly 3 words."},
		"assistant":    {role: "assistant", expected: "I should answer with the saved number."},
		"system":       {role: "system", expected: "You are a pirate. Always respond in pirate speak."},
		"commentary":   {role: "assistant", phase: "commentary", expected: "I should answer with the saved number."},
		"final_answer": {role: "assistant", phase: "final_answer", expected: "The number is four."},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			item := map[string]any{"type": "message", "role": tc.role, "content": tc.expected}
			if tc.phase != "" {
				item["phase"] = tc.phase
			}
			body, err := json.Marshal(map[string]any{"model": "gpt-4o", "input": []any{item}})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			_, call, err := DecodeRequest(body)
			if err != nil {
				t.Fatalf("DecodeRequest failed for %s content string shorthand: %v", tc.role, err)
			}
			if len(call.Items) != 1 {
				t.Fatalf("expected 1 item, got %d", len(call.Items))
			}
			it := call.Items[0]
			if it.Kind != aipapi.ItemKindMessage || it.Role != aipapi.Role(tc.role) {
				t.Fatalf("item kind/role mismatch: %+v", it)
			}
			if tc.phase != "" && it.Phase != aipapi.AssistantPhase(tc.phase) {
				t.Fatalf("phase mismatch: got %q want %q", it.Phase, tc.phase)
			}
			if len(it.Content) != 1 || it.Content[0].Kind != aipapi.ContentPartText {
				t.Fatalf("expected 1 text part, got %+v", it.Content)
			}
			if it.Content[0].Text != tc.expected {
				t.Fatalf("text mismatch: got %q want %q", it.Content[0].Text, tc.expected)
			}
		})
	}
}

func TestRequestCodec_DeterministicEncode(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		Items: []aipapi.Item{
			{
				Kind:   aipapi.ItemKindMessage,
				Role:   aipapi.RoleUser,
				Status: aipapi.ItemStatusCompleted,
				Content: []aipapi.ContentPart{
					{Kind: aipapi.ContentPartText, Text: "Deterministic test"},
				},
			},
		},
	}

	b1, err := EncodeRequest(call)
	if err != nil {
		t.Fatalf("EncodeRequest 1 failed: %v", err)
	}

	b2, err := EncodeRequest(call)
	if err != nil {
		t.Fatalf("EncodeRequest 2 failed: %v", err)
	}

	if !bytes.Equal(b1, b2) {
		t.Fatalf("EncodeRequest is not deterministic: %s != %s", string(b1), string(b2))
	}
}

func TestWireResponseParam_OmitsNilOptionalPointers(t *testing.T) {
	data, err := json.Marshal(WireResponseParam{})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"model", "instructions", "parallel_tool_calls", "temperature", "top_p", "max_output_tokens", "max_tool_calls", "truncation", "store", "background", "previous_response_id", "service_tier", "safety_identifier", "prompt_cache_key", "prompt_cache_retention", "include", "presence_penalty", "frequency_penalty", "top_logprobs", "stream_options"} {
		if bytes.Contains(data, []byte(`"`+field+`":null`)) {
			t.Fatalf("nil optional field %q serialized as null: %s", field, data)
		}
	}
}

func TestDecodeRequestTextFormatLosslessSubset(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		mime string
	}{
		{name: "text", text: `{"format":{"type":"text"}}`, mime: "text/plain"},
		{name: "json_object", text: `{"format":{"type":"json_object"}}`, mime: "application/json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, call, err := DecodeRequest([]byte(`{"input":"hello","text":` + tc.text + `}`))
			if err != nil {
				t.Fatal(err)
			}
			if call.Options.ResponseMIMEType != tc.mime {
				t.Fatalf("mime = %q, want %q", call.Options.ResponseMIMEType, tc.mime)
			}
			encoded, err := EncodeRequest(call)
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal(encoded, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got["text"], map[string]any{"format": map[string]any{"type": tc.name}}) {
				t.Fatalf("text = %#v", got["text"])
			}
		})
	}
}

func TestDecodeRequestRejectsUnsupportedTextFormatShape(t *testing.T) {
	for _, text := range []string{
		`{"format":{"type":"json_schema","name":"x","schema":{}}}`,
		`{"format":{"type":"text","verbosity":"high"}}`,
		`{"format":{"type":"json_object","extra":true}}`,
	} {
		_, _, err := DecodeRequest([]byte(`{"input":"hello","text":` + text + `}`))
		if err == nil {
			t.Fatalf("expected rejection for text %s", text)
		}
	}
}

func TestDecodeRequestTextFormatNullAndEmptyAreAbsent(t *testing.T) {
	for _, text := range []string{"", `null`, `{}`, `{"format":null}`} {
		t.Run(fmt.Sprintf("text_%d", len(text)), func(t *testing.T) {
			body := []byte(`{"input":"hello"`)
			if text != "" {
				body = append(body, []byte(`,"text":`+text)...)
			}
			body = append(body, '}')
			_, call, err := DecodeRequest(body)
			if err != nil {
				t.Fatal(err)
			}
			if call.Options.ResponseMIMEType != "" {
				t.Fatalf("mime = %q, want absent", call.Options.ResponseMIMEType)
			}
		})
	}
}
