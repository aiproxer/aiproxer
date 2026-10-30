package acp

import (
	"context"
	"maps"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func toolCallUpdate(fields map[string]any) map[string]any {
	upd := map[string]any{"sessionUpdate": "tool_call", "toolCallId": "tc-1", "title": "Read"}
	maps.Copy(upd, fields)
	return upd
}

func kinds(evs []aipapi.Event) []aipapi.EventKind {
	out := make([]aipapi.EventKind, 0, len(evs))
	for _, ev := range evs {
		out = append(out, ev.Kind)
	}
	return out
}

func TestToolProvenanceSink_StartEmitsIdentity(t *testing.T) {
	t.Parallel()
	sink := NewToolProvenanceSink()
	evs, err := sink.HandleToolUpdate(context.Background(), acpToolCall, toolCallUpdate(nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Kind != aipapi.EventToolCallStarted {
		t.Fatalf("got %#v", kinds(evs))
	}
	if evs[0].ToolCallID != "tc-1" || evs[0].ToolName != "Read" {
		t.Fatalf("identity = %q/%q", evs[0].ToolCallID, evs[0].ToolName)
	}
	for _, ev := range evs {
		if ev.Kind == aipapi.EventTextDelta {
			t.Fatal("provenance sink must not emit generated text")
		}
	}
}

func TestToolProvenanceSink_ArgsDeltaOnce(t *testing.T) {
	t.Parallel()
	sink := NewToolProvenanceSink()
	evs, err := sink.HandleToolUpdate(context.Background(), acpToolCall, toolCallUpdate(map[string]any{
		"rawInput": map[string]any{"path": "main.go"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || evs[0].Kind != aipapi.EventToolCallStarted || evs[1].Kind != aipapi.EventToolCallArgsDelta {
		t.Fatalf("got %#v", kinds(evs))
	}
	if evs[1].ToolCallID != "tc-1" || !strings.Contains(evs[1].Delta, "main.go") {
		t.Fatalf("args = %#v", evs[1])
	}
	repeat, err := sink.HandleToolUpdate(context.Background(), acpToolCallUpdate, toolCallUpdate(map[string]any{
		"toolCallUpdate": map[string]any{"rawInput": map[string]any{"path": "main.go"}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range repeat {
		if ev.Kind == aipapi.EventToolCallArgsDelta || ev.Kind == aipapi.EventToolCallStarted {
			t.Fatalf("repeated input must not re-emit, got %#v", kinds(repeat))
		}
	}
}

func TestToolProvenanceSink_CompletionFinishesOnce(t *testing.T) {
	t.Parallel()
	sink := NewToolProvenanceSink()
	if _, err := sink.HandleToolUpdate(context.Background(), acpToolCall, toolCallUpdate(nil)); err != nil {
		t.Fatal(err)
	}
	evs, err := sink.HandleToolUpdate(context.Background(), acpToolCallUpdate, toolCallUpdate(map[string]any{
		"toolCallUpdate": map[string]any{"status": "completed"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Kind != aipapi.EventToolCallFinished || evs[0].ToolCallID != "tc-1" {
		t.Fatalf("got %#v", evs)
	}
	again, err := sink.HandleToolUpdate(context.Background(), acpToolCallUpdate, toolCallUpdate(map[string]any{
		"toolCallUpdate": map[string]any{"status": "completed"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("duplicate completion must be silent, got %#v", kinds(again))
	}
}

func TestToolProvenanceSink_FlushClosesOpenCalls(t *testing.T) {
	t.Parallel()
	sink := NewToolProvenanceSink()
	if _, err := sink.HandleToolUpdate(context.Background(), acpToolCall, toolCallUpdate(nil)); err != nil {
		t.Fatal(err)
	}
	flushed := sink.FlushIncomplete()
	if len(flushed) != 1 || flushed[0].Kind != aipapi.EventToolCallFinished {
		t.Fatalf("got %#v", kinds(flushed))
	}
	if again := sink.FlushIncomplete(); len(again) != 0 {
		t.Fatalf("second flush must be empty, got %#v", kinds(again))
	}
}

func TestToolProvenanceSink_DropsGarbage(t *testing.T) {
	t.Parallel()
	sink := NewToolProvenanceSink()
	if evs, err := sink.HandleToolUpdate(context.Background(), acpToolCall, nil); err != nil || len(evs) != 0 {
		t.Fatalf("got %#v, %v", evs, err)
	}
}

// Names past the bound are truncated to exactly maxToolProvenanceNameChars
// runes, and truncation must not split a multi-byte rune into invalid UTF-8.
// Mutant: slice the name by bytes instead of runes.
func TestToolProvenanceSink_TruncatesNameAtRuneBound(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		caseName string
		title    string
	}{
		{caseName: "ascii", title: strings.Repeat("n", 4096)},
		{caseName: "multi-byte runes", title: strings.Repeat("é", 4096)},
	} {
		t.Run(tc.caseName, func(t *testing.T) {
			t.Parallel()
			sink := NewToolProvenanceSink()
			evs, err := sink.HandleToolUpdate(context.Background(), acpToolCall, toolCallUpdate(map[string]any{
				"title": tc.title,
			}))
			if err != nil {
				t.Fatal(err)
			}
			if len(evs) != 1 || evs[0].Kind != aipapi.EventToolCallStarted {
				t.Fatalf("got %#v", kinds(evs))
			}
			if want := 512; len([]rune(evs[0].ToolName)) != want {
				t.Fatalf("name runes = %d, want %d", len([]rune(evs[0].ToolName)), want)
			}
			if !utf8.ValidString(evs[0].ToolName) {
				t.Fatalf("truncation split a rune: %q", evs[0].ToolName)
			}
		})
	}
}

func TestProtocol_StructuredProvenanceOptIn(t *testing.T) {
	t.Parallel()
	legacy := NewACPProtocol(legacyOptionsSpec{}, nil)
	evs, err := legacyMapperEvents(legacy, toolCallUpdate(map[string]any{"rawInput": "x"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) == 0 || evs[0].Kind != aipapi.EventTextDelta {
		t.Fatalf("legacy spec keeps text summaries, got %#v", kinds(evs))
	}
	structured := NewACPProtocol(structuredProvenanceSpec{}, nil)
	evs, err = legacyMapperEvents(structured, toolCallUpdate(map[string]any{"rawInput": "x"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) == 0 || evs[0].Kind != aipapi.EventToolCallStarted {
		t.Fatalf("opt-in spec emits provenance, got %#v", kinds(evs))
	}
	for _, ev := range evs {
		if ev.Kind == aipapi.EventTextDelta {
			t.Fatal("opt-in path must not emit generated text")
		}
	}
}

type structuredProvenanceSpec struct{ legacyOptionsSpec }

func (structuredProvenanceSpec) StructuredToolProvenance() bool { return true }

func legacyMapperEvents(proto SubprocessProtocol, upd map[string]any) ([]aipapi.Event, error) {
	p, ok := proto.(*acpProtocol)
	if !ok {
		panic("test protocol is not *acpProtocol")
	}
	// The tool sink is per turn: the mapper under test owns its own.
	return mapSessionUpdateToEvents(context.Background(), SessionUpdateMapperOptions{ToolSink: selectToolSink(p.spec)}, upd)
}
