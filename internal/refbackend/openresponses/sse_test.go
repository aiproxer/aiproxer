package openresponses

import (
	"bytes"
	"strings"
	"testing"
)

func TestBuildStreamEvents_TextLifecycle(t *testing.T) {
	t.Parallel()
	res := NewResource("resp_stream_1", "gpt-openresponses-1", 1719900600, []Item{
		NewMessagePartsItem("assistant", "", NewTextPart("Hello world")),
	})
	events := buildStreamEvents(res)
	if len(events) != 8 {
		t.Fatalf("expected 8 events, got %d", len(events))
	}
	wantTypes := []string{
		"response.created",
		"response.output_item.added",
		"response.content_part.added",
		"response.output_text.delta",
		"response.output_text.done",
		"response.content_part.done",
		"response.output_item.done",
		"response.completed",
	}
	for i, e := range events {
		if e.Type != wantTypes[i] {
			t.Fatalf("event %d: got %q want %q", i, e.Type, wantTypes[i])
		}
	}
	for i := 1; i < len(events); i++ {
		if events[i].Seq <= events[i-1].Seq {
			t.Fatalf("sequence not increasing at %d", i)
		}
	}
	// Exactly one terminal event, and it must be the last.
	terminals := 0
	for i, e := range events {
		if e.Type == "response.completed" {
			terminals++
			if i != len(events)-1 {
				t.Fatalf("terminal at %d not last", i)
			}
		}
	}
	if terminals != 1 {
		t.Fatalf("terminals: %d", terminals)
	}
}

func TestBuildStreamEvents_TextDeltaAccumulates(t *testing.T) {
	t.Parallel()
	res := NewResource("r", "m", 1, []Item{NewMessagePartsItem("assistant", "", NewTextPart("Hello world"))})
	events := buildStreamEvents(res)
	var delta, done string
	for _, e := range events {
		switch e.Type {
		case "response.output_text.delta":
			value, ok := e.Fields["delta"].(string)
			if !ok {
				t.Fatalf("delta field has type %T", e.Fields["delta"])
			}
			delta += value
		case "response.output_text.done":
			value, ok := e.Fields["text"].(string)
			if !ok {
				t.Fatalf("text field has type %T", e.Fields["text"])
			}
			done = value
		}
	}
	if delta != "Hello world" || done != "Hello world" {
		t.Fatalf("delta=%q done=%q", delta, done)
	}
}

func TestBuildStreamEvents_ToolAndReasoningAndExtension(t *testing.T) {
	t.Parallel()
	res := NewResource("r", "m", 1, []Item{
		NewFunctionCallItem("fc_1", "call_1", "get_weather", `{"city":"paris"}`),
		NewReasoningItem("rs_1", []ContentPart{{Type: "summary_text", Text: "sum"}}, nil),
		NewExtensionItem("acme:telemetry", `{"id":"t1"}`),
	})
	events := buildStreamEvents(res)
	seen := map[string]bool{}
	for _, e := range events {
		seen[e.Type] = true
	}
	for _, want := range []string{
		"response.function_call_arguments.delta",
		"response.function_call_arguments.done",
		"response.reasoning_summary_text.delta",
		"response.reasoning_summary_text.done",
		"response.output_item.added",
		"response.output_item.done",
		"response.completed",
	} {
		if !seen[want] {
			t.Errorf("missing event %s (have %v)", want, seen)
		}
	}
	// The extension item is preserved opaquely in output_item.added.
	for _, e := range events {
		if e.Type == "response.output_item.added" {
			if it, ok := e.Fields["item"].(Item); ok && it.IsExtension() && len(it.Opaque) > 0 {
				if !strings.Contains(string(it.Opaque), "acme:telemetry") {
					t.Fatalf("extension opaque lost: %s", it.Opaque)
				}
				return
			}
		}
	}
	t.Fatal("extension item not preserved in stream")
}

func TestBuildStreamEvents_ReasoningContentPinnedNames(t *testing.T) {
	t.Parallel()
	res := NewResource("r", "m", 1, []Item{
		NewReasoningItem("rs_1", nil, []ContentPart{{Type: "output_text", Text: "trace"}}),
	})
	events := buildStreamEvents(res)
	var delta, done bool
	for _, e := range events {
		switch e.Type {
		case "response.reasoning.delta":
			delta = true
		case "response.reasoning.done":
			done = true
		case "response.reasoning_text.delta", "response.reasoning_text.done":
			t.Fatalf("legacy reasoning_text event emitted: %s", e.Type)
		}
	}
	if !delta || !done {
		t.Fatalf("missing pinned reasoning delta/done events (have %v)", eventTypes(events))
	}
}

func eventTypes(events []StreamEvent) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.Type)
	}
	return out
}

func TestBuildStreamEvents_PhasePreserved(t *testing.T) {
	t.Parallel()
	res := NewResource("r", "m", 1, []Item{
		NewMessagePartsItem("assistant", "commentary", NewTextPart("a")),
		NewMessagePartsItem("assistant", "final_answer", NewTextPart("b")),
	})
	events := buildStreamEvents(res)
	phases := map[string]bool{}
	for _, e := range events {
		if it, ok := e.Fields["item"].(Item); ok {
			if it.Phase != "" {
				phases[it.Phase] = true
			}
		}
	}
	if !phases["commentary"] || !phases["final_answer"] {
		t.Fatalf("phases lost: %v", phases)
	}
}

func TestSSEWriter_Framing(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	sw := &sseWriter{w: &buf}
	ev := StreamEvent{Type: "response.created", Seq: 0, Fields: map[string]any{
		"response": map[string]any{"id": "r"},
	}}
	if err := sw.writeEvent(ev, ev.Type); err != nil {
		t.Fatal(err)
	}
	if err := sw.writeDone(); err != nil {
		t.Fatal(err)
	}
	s := buf.String()
	if !strings.HasPrefix(s, "event: response.created\n") {
		t.Fatalf("framing: %q", s)
	}
	if !strings.Contains(s, "\ndata: {") || !strings.Contains(s, `"type":"response.created"`) ||
		!strings.Contains(s, `"sequence_number":0`) || !strings.Contains(s, `"response":{"id":"r"}`) {
		t.Fatalf("payload: %q", s)
	}
	if !strings.HasSuffix(s, "data: [DONE]\n\n") {
		t.Fatalf("done framing: %q", s)
	}
}

func TestSSEWriter_UnsafeTypeRejected(t *testing.T) {
	t.Parallel()
	sw := &sseWriter{w: &bytes.Buffer{}}
	if err := sw.writeEvent(StreamEvent{Type: "bad\ntype", Seq: 0}, "bad\ntype"); err == nil {
		t.Fatal("expected unsafe type rejection")
	}
}

func TestStreamEvent_RenderPayloadSkipsReserved(t *testing.T) {
	t.Parallel()
	ev := StreamEvent{Type: "x", Seq: 7, Fields: map[string]any{"type": "evil", "sequence_number": 99, "ok": 1}}
	raw, err := ev.renderPayload()
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if strings.Contains(s, `"type":"evil"`) || strings.Contains(s, `"sequence_number":99`) {
		t.Fatalf("reserved keys leaked: %s", s)
	}
	if !strings.Contains(s, `"type":"x"`) || !strings.Contains(s, `"sequence_number":7`) || !strings.Contains(s, `"ok":1`) {
		t.Fatalf("payload wrong: %s", s)
	}
}
