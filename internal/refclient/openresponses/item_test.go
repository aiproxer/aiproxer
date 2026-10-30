package openresponses

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestItem_ConstructorsAndRoundTrip(t *testing.T) {
	t.Parallel()

	msg := NewMessageItem("user", "input_text", "ping")
	if msg.Type != ItemMessage || msg.Role != "user" || len(msg.Content) != 1 || msg.Content[0].Text != "ping" {
		t.Fatalf("message item: %+v", msg)
	}

	call := NewFunctionCallItem("fc_1", "call_1", "get_weather", `{"location":"SF"}`)
	if call.Type != ItemFunctionCall || call.CallID != "call_1" || call.Name != "get_weather" {
		t.Fatalf("function call: %+v", call)
	}

	output := NewFunctionCallOutputItem("call_1", "{\"temp\":58}")
	if output.Type != ItemFunctionCallOutput || output.CallID != "call_1" {
		t.Fatalf("function output: %+v", output)
	}

	ref := NewItemReference("resp_prev", "msg_prev")
	if ref.Type != ItemItemReference || ref.EncapsulatedID != "resp_prev" || ref.ID != "msg_prev" {
		t.Fatalf("item reference: %+v", ref)
	}

	custom := NewCustomItem("acme:telemetry_chunk", `{"latency_ms":72}`)
	if custom.Type != "acme:telemetry_chunk" || !custom.IsExtension() {
		t.Fatalf("custom item: %+v", custom)
	}

	// JSON round trip preserves discriminators.
	for _, it := range []Item{msg, call, output, ref, custom} {
		b, err := json.Marshal(it)
		if err != nil {
			t.Fatalf("marshal %s: %v", it.Type, err)
		}
		var back Item
		if err := json.Unmarshal(b, &back); err != nil {
			t.Fatalf("unmarshal %s: %v", it.Type, err)
		}
		if back.Type != it.Type {
			t.Fatalf("type mismatch: got %q want %q", back.Type, it.Type)
		}
	}
}

func TestItem_ParsesMessageVariants(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		json   string
		role   string
		phase  string
		status string
	}{
		{"user_string_content", `{"type":"message","id":"m1","role":"user","status":"received","content":"hi"}`, "user", "", "received"},
		{"assistant_content_array", `{"type":"message","id":"m2","role":"assistant","status":"completed","phase":"final_answer","content":[{"type":"output_text","text":"bye"}]}`, "assistant", "final_answer", "completed"},
		{"system_role", `{"type":"message","id":"m3","role":"system","content":"sys"}`, "system", "", ""},
		{"developer_role", `{"type":"message","id":"m4","role":"developer","content":"dev"}`, "developer", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var it Item
			if err := json.Unmarshal([]byte(tc.json), &it); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if it.Role != tc.role || it.Phase != tc.phase || it.Status != tc.status {
				t.Fatalf("item: %+v", it)
			}
			if len(it.Content) == 1 {
				if tc.role == "assistant" && it.Content[0].Type != "output_text" {
					t.Fatalf("content type: %q", it.Content[0].Type)
				}
			}
		})
	}
}

func TestItem_RejectsUnknownUnprefixedType(t *testing.T) {
	t.Parallel()
	// An unknown unprefixed item type is not a legal extension and must fail.
	var it Item
	if err := json.Unmarshal([]byte(`{"type":"random_thing","id":"x"}`), &it); err == nil {
		t.Fatal("expected error for unknown unprefixed item type")
	}
}

func TestContentPart_ParsesMultimodal(t *testing.T) {
	t.Parallel()
	var it Item
	if err := json.Unmarshal([]byte(`{"type":"message","role":"user","content":[{"type":"input_text","text":"see this"},{"type":"input_image","image_url":{"data":"aGVsbG8=","media_type":"image/png"}},{"type":"input_file","file_url":{"file_id":"f1"}}]}`), &it); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(it.Content) != 3 {
		t.Fatalf("content len: %d", len(it.Content))
	}
	img := it.Content[1]
	if img.Type != "input_image" || len(img.ImageURL) == 0 {
		t.Fatalf("image part: %+v", img)
	}
	var m map[string]any
	if err := json.Unmarshal(img.ImageURL, &m); err != nil {
		t.Fatal(err)
	}
	if m["data"] != "aGVsbG8=" || m["media_type"] != "image/png" {
		t.Fatalf("image url data: %v", m)
	}
}

func TestContentPart_RefusalAndAnnotations(t *testing.T) {
	t.Parallel()
	var it Item
	if err := json.Unmarshal([]byte(`{"type":"message","role":"assistant","content":[{"type":"refusal","refusal":"I can't help."},{"type":"output_text","text":"ok","annotations":[{"type":"url_citation","url":"https://example.com"}]}]}`), &it); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if it.Content[0].Type != "refusal" || it.Content[0].Refusal != "I can't help." {
		t.Fatalf("refusal: %+v", it.Content[0])
	}
	if len(it.Content[1].Annotations) == 0 {
		t.Fatal("annotations missing")
	}
}

func TestItem_OpaqueItem_RoundTrips(t *testing.T) {
	t.Parallel()
	ext := NewCustomItem("openai:web_search_call", `{"action":{"type":"search","query":"weather: San Francisco, CA"}}`)
	b, err := json.Marshal(ext)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"type":"openai:web_search_call"`) {
		t.Fatalf("marshal extension: %s", b)
	}
	raw := ext.OpaqueItem()
	if raw == nil || !strings.Contains(string(raw), `"query"`) {
		t.Fatalf("opaque item: %s", raw)
	}
}

func TestTool_ParsesFunctionAndCustom(t *testing.T) {
	t.Parallel()
	var tool Tool
	if err := json.Unmarshal([]byte(`{"type":"function","name":"f","description":"d","parameters":{"type":"object"},"strict":true}`), &tool); err != nil {
		t.Fatal(err)
	}
	if tool.Type != "function" || tool.Name != "f" || tool.Strict == nil || *tool.Strict != true {
		t.Fatalf("tool: %+v", tool)
	}
	var custom Tool
	if err := json.Unmarshal([]byte(`{"type":"acme:custom_document_search","documents":[{"type":"external_file","url":"https://example.com/a.pdf"}]}`), &custom); err != nil {
		t.Fatal(err)
	}
	if !custom.IsExtension() || len(custom.Opaque) == 0 {
		t.Fatalf("custom tool: %+v", custom)
	}
}
