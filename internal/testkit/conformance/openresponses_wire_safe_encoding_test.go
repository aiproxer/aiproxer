package conformance

import (
	"encoding/json"
	"strings"
	"testing"
)

// hostileParityText carries every JSON-significant character class: double
// quote, backslash, and control characters (\n, \r, \t) plus a raw vertical
// tab. Any unsafe string interpolation would either break JSON validity or
// round-trip a mutated value.
const hostileParityText = "a\"b\\c\nd\re\tf\vg"

const testCreated = int64(1715620000)

// sseDataEvents splits an SSE payload into the parsed JSON documents carried by
// each data line, failing the test on any line that is not valid JSON.
func sseDataEvents(t *testing.T, raw string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for frame := range strings.SplitSeq(raw, "\n\n") {
		for line := range strings.SplitSeq(frame, "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			payload := strings.TrimPrefix(line, "data: ")
			if payload == "[DONE]" {
				continue
			}
			var ev map[string]any
			if err := json.Unmarshal([]byte(payload), &ev); err != nil {
				t.Fatalf("data line is not valid JSON: %v\n%q", err, payload)
			}
			out = append(out, ev)
		}
	}
	return out
}

// responseOutputText extracts the assistant text from an OpenResponses response
// resource shaped as {"output":[{"content":[{"type":"output_text","text":...}]}]}.
func responseOutputText(t *testing.T, res map[string]any) string {
	t.Helper()
	output, _ := res["output"].([]any)
	if len(output) == 0 {
		return ""
	}
	item, _ := output[0].(map[string]any)
	return itemOutputText(t, item)
}

// itemOutputText extracts the assistant text from an OpenResponses message item
// shaped as {"content":[{"type":"output_text","text":...}]}.
func itemOutputText(t *testing.T, item map[string]any) string {
	t.Helper()
	content, _ := item["content"].([]any)
	if len(content) == 0 {
		return ""
	}
	part, _ := content[0].(map[string]any)
	txt, _ := part["text"].(string)
	return txt
}

func TestOpenResponsesRichResourcePreservesHostileText(t *testing.T) {
	raw := openResponsesRichResource(hostileParityText, testCreated)
	var res map[string]any
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatalf("resource is not valid JSON: %v", err)
	}
	if got := res["created_at"]; got != float64(testCreated) {
		t.Fatalf("created_at = %v, want %d", got, testCreated)
	}
	if got := responseOutputText(t, res); got != hostileParityText {
		t.Fatalf("text = %q, want exact value %q", got, hostileParityText)
	}
}

func TestOpenResponsesRichSSEPreservesHostileText(t *testing.T) {
	raw := openResponsesRichSSE(hostileParityText, testCreated)
	events := sseDataEvents(t, raw)
	if len(events) != 8 {
		t.Fatalf("want 8 data events, got %d", len(events))
	}
	var assembled strings.Builder
	for _, ev := range events {
		switch ev["type"] {
		case "response.output_text.delta":
			if got, _ := ev["delta"].(string); got != hostileParityText {
				t.Fatalf("delta = %q, want exact value %q", got, hostileParityText)
			}
			assembled.WriteString(hostileParityText)
		case "response.output_text.done":
			if got, _ := ev["text"].(string); got != hostileParityText {
				t.Fatalf("done text = %q, want exact value %q", got, hostileParityText)
			}
		case "response.output_item.done":
			item, _ := ev["item"].(map[string]any)
			if got := itemOutputText(t, item); got != hostileParityText {
				t.Fatalf("output_item.done text = %q, want exact value %q", got, hostileParityText)
			}
		case "response.completed":
			resp, _ := ev["response"].(map[string]any)
			if got := responseOutputText(t, resp); got != hostileParityText {
				t.Fatalf("completed text = %q, want exact value %q", got, hostileParityText)
			}
			if got := resp["created_at"]; got != float64(testCreated) {
				t.Fatalf("completed created_at = %v, want %d", got, testCreated)
			}
		}
	}
	if assembled.String() != hostileParityText {
		t.Fatalf("assembled delta text = %q, want %q", assembled.String(), hostileParityText)
	}
}

func TestConnectorColumnResourceIsValidJSON(t *testing.T) {
	raw := connectorColumnResource(testCreated)
	var res map[string]any
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatalf("resource is not valid JSON: %v", err)
	}
	if got := res["created_at"]; got != float64(testCreated) {
		t.Fatalf("created_at = %v, want %d", got, testCreated)
	}
	if got := responseOutputText(t, res); got != "provider-mode-ok" {
		t.Fatalf("text = %q, want provider-mode-ok", got)
	}
}

func TestConnectorChatNonStreamResourcePreservesHostileText(t *testing.T) {
	raw := chatNonStreamResource(hostileParityText)
	var res map[string]any
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatalf("chat resource is not valid JSON: %v", err)
	}
	choices, _ := res["choices"].([]any)
	if len(choices) == 0 {
		t.Fatal("chat resource carries no choices")
	}
	choice, _ := choices[0].(map[string]any)
	msg, _ := choice["message"].(map[string]any)
	content, _ := msg["content"].(string)
	if content != hostileParityText {
		t.Fatalf("chat content = %q, want exact value %q", content, hostileParityText)
	}
}

func TestConnectorChatStreamSSEPreservesHostileText(t *testing.T) {
	raw := chatStreamSSE(hostileParityText)
	events := sseDataEvents(t, raw)
	if len(events) != 3 {
		t.Fatalf("want 3 data events, got %d", len(events))
	}
	var assembled strings.Builder
	for _, ev := range events {
		choices, _ := ev["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		choice, _ := choices[0].(map[string]any)
		delta, _ := choice["delta"].(map[string]any)
		content, _ := delta["content"].(string)
		if content == "" {
			continue
		}
		if content != hostileParityText {
			t.Fatalf("delta content = %q, want exact value %q", content, hostileParityText)
		}
		assembled.WriteString(content)
	}
	if assembled.String() != hostileParityText {
		t.Fatalf("assembled delta content = %q, want %q", assembled.String(), hostileParityText)
	}
}

func TestConnectorColumnSSEIsValidJSON(t *testing.T) {
	raw := connectorColumnSSE(testCreated)
	events := sseDataEvents(t, raw)
	if len(events) != 8 {
		t.Fatalf("want 8 data events, got %d", len(events))
	}
	var assembled strings.Builder
	for _, ev := range events {
		switch ev["type"] {
		case "response.output_text.delta":
			delta, _ := ev["delta"].(string)
			assembled.WriteString(delta)
		case "response.completed":
			resp, _ := ev["response"].(map[string]any)
			if got := resp["created_at"]; got != float64(testCreated) {
				t.Fatalf("completed created_at = %v, want %d", got, testCreated)
			}
			if got := responseOutputText(t, resp); got != "provider-mode-ok" {
				t.Fatalf("completed text = %q, want provider-mode-ok", got)
			}
		}
	}
	if assembled.String() != "provider-mode-ok" {
		t.Fatalf("assembled delta text = %q, want provider-mode-ok", assembled.String())
	}
}
