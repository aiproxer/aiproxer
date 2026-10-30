package openresponses

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNewResource_RequiredPresenceOnWire(t *testing.T) {
	t.Parallel()
	res := NewResource("resp_presence", "gpt-openresponses-1", 1719900000, []Item{
		NewMessageItem("assistant", "output_text", "hello"),
	})
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, f := range requiredResponseFields {
		if _, ok := m[f]; !ok {
			t.Errorf("required field %q missing from wire", f)
		}
	}
	if string(m["object"]) != `"response"` || string(m["status"]) != `"completed"` {
		t.Fatalf("object/status: %s/%s", m["object"], m["status"])
	}
	// Nullable pointer fields must be present as null.
	for _, f := range []string{"error", "instructions", "previous_response_id", "incomplete_details", "max_output_tokens", "max_tool_calls"} {
		if string(m[f]) != "null" {
			t.Errorf("field %q must be explicit null, got %s", f, m[f])
		}
	}
	// Output items and tools must be arrays.
	if string(m["output"]) == "null" || string(m["tools"]) == "null" {
		t.Fatal("output/tools must be arrays, not null")
	}
}

func TestNewResource_OptionalFieldsEmitted(t *testing.T) {
	t.Parallel()
	res := NewResource("resp_opt", "m", 1, nil)
	completed := int64(5)
	safety := "safe"
	cacheKey := "k"
	retention := "10m"
	res.CompletedAt = &completed
	res.SafetyIdentifier = &safety
	res.PromptCacheKey = &cacheKey
	res.PromptCacheRetention = &retention
	res.Extensions["acme:meta"] = json.RawMessage(`{"k":1}`)

	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{`"completed_at":5`, `"safety_identifier":"safe"`, `"prompt_cache_key":"k"`, `"prompt_cache_retention":"10m"`, `"acme:meta":{"k":1}`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
}

func TestResource_OmitRequiredField(t *testing.T) {
	t.Parallel()
	res := NewResource("r", "m", 1, nil)
	raw, err := res.OmitRequiredField("output")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["output"]; ok {
		t.Fatal("output must be omitted")
	}
}

func TestResource_CorruptField(t *testing.T) {
	t.Parallel()
	res := NewResource("r", "m", 1, nil)
	raw, err := res.CorruptField("output")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if string(m["output"]) != `"not-the-right-type"` {
		t.Fatalf("corrupt field: %s", m["output"])
	}
}

func TestUsage_MarshalRequiredTokens(t *testing.T) {
	t.Parallel()
	u := Usage{InputTokens: 1, OutputTokens: 2, TotalTokens: 3, CachedTokens: 4, ReasoningTokens: 5}
	raw, err := json.Marshal(u)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{`"input_tokens":1`, `"output_tokens":2`, `"total_tokens":3`, `"cached_tokens":4`, `"reasoning_tokens":5`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
}

func TestNewCompactResource_Marshal(t *testing.T) {
	t.Parallel()
	res := NewCompactResource("resp_c", "m", 1, []Item{NewCompactionItem("cmp_1", "")})
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{`"object":"response.compaction"`, `"id":"resp_c"`, `"status":"completed"`, `"usage"`, `"output"`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
}

func TestItemBuilders_RoundTrip(t *testing.T) {
	t.Parallel()
	items := []Item{
		NewMessagePartsItem("assistant", "commentary", NewTextPart("thinking"), ContentPart{Type: "output_text", Text: "final", Annotations: json.RawMessage(`[]`)}),
		NewFunctionCallItem("fc_1", "call_1", "get_weather", `{"city":"paris"}`),
		NewFunctionCallOutputItem("call_1", `ok`),
		NewReasoningItem("rs_1", []ContentPart{{Type: "summary_text", Text: "sum"}}, []ContentPart{{Type: "output_text", Text: "trace"}}),
		NewItemReference("resp_prev", "ref_1"),
		NewCompactionItem("cmp_2", ""),
		NewExtensionItem("acme:telemetry", `{"id":"t1","latency_ms":72}`),
	}
	for _, it := range items {
		raw, err := json.Marshal(it)
		if err != nil {
			t.Fatalf("marshal %s: %v", it.Type, err)
		}
		var back Item
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatalf("unmarshal %s (%s): %v", it.Type, raw, err)
		}
		if back.Type != it.Type {
			t.Fatalf("type: got %q want %q", back.Type, it.Type)
		}
	}
	ext := items[6]
	if !ext.IsExtension() || len(ext.Opaque) == 0 {
		t.Fatalf("extension item opaque lost: %+v", ext)
	}
	if !strings.Contains(string(ext.Opaque), "acme:telemetry") {
		t.Fatalf("extension opaque: %s", ext.Opaque)
	}
}

func TestContentPart_UnknownUnprefixedRejected(t *testing.T) {
	t.Parallel()
	var p ContentPart
	if err := json.Unmarshal([]byte(`{"type":"bogus_part"}`), &p); err == nil {
		t.Fatal("expected unknown unprefixed part rejection")
	}
	var p2 ContentPart
	if err := json.Unmarshal([]byte(`{"type":"acme:part","k":1}`), &p2); err != nil {
		t.Fatalf("extension part rejected: %v", err)
	}
	if p2.IsExtension() != true || !isExtensionType(p2.Type) {
		t.Fatalf("extension part must be flagged: %+v", p2)
	}
}

func TestErrorObject_Marshal(t *testing.T) {
	t.Parallel()
	raw, err := json.Marshal(ErrorObject{Type: "invalid_request", Code: "model_not_found", Message: "bad model", Param: "model"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{`"type":"invalid_request"`, `"code":"model_not_found"`, `"message":"bad model"`, `"param":"model"`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
}

func TestInputValue_MarshalForms(t *testing.T) {
	t.Parallel()
	text := InputValue{Text: "hi", TextSet: true}
	if raw, err := json.Marshal(text); err != nil || string(raw) != `"hi"` {
		t.Fatalf("text input: %s %v", raw, err)
	}
	items := InputValue{Items: []Item{NewMessageItem("user", "input_text", "x")}}
	raw, err := json.Marshal(items)
	if err != nil || !strings.Contains(string(raw), `"type":"message"`) {
		t.Fatalf("items input: %s %v", raw, err)
	}
	empty := InputValue{}
	if raw, err := json.Marshal(empty); err != nil || string(raw) != "null" {
		t.Fatalf("empty input: %s %v", raw, err)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}()
}

// TestNewCompactionItem_WireIsSchemaValid pins that a compaction item produced
// by the independent refbackend carries the pinned-profile required
// encrypted_content blob and round-trips through the independent wire model.
func TestNewCompactionItem_WireIsSchemaValid(t *testing.T) {
	t.Parallel()
	item := NewCompactionItem("cmp_schema", "")
	raw, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if string(m["type"]) != `"compaction"` {
		t.Fatalf("type = %s", m["type"])
	}
	if string(m["id"]) != `"cmp_schema"` {
		t.Fatalf("id = %s", m["id"])
	}
	var ec string
	if err := json.Unmarshal(m["encrypted_content"], &ec); err != nil || ec == "" {
		t.Fatalf("compaction item must carry non-empty encrypted_content: %s (err=%v)", raw, err)
	}
	var back Item
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.EncryptedContent != ec {
		t.Fatalf("round-trip encrypted_content = %q, want %q", back.EncryptedContent, ec)
	}
}
