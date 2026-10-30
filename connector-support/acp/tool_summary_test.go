package acp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestToolSummarySink_CompletionSummary(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	sink := NewToolSummarySink(func() time.Time { return now })

	// Simulate a tool_call start.
	startUpdate := map[string]any{
		"toolCallId": "tc-1",
		"toolCall": map[string]any{
			"title":    "Read File",
			"rawInput": map[string]any{"path": "/tmp/test.txt"},
		},
	}
	evs, err := sink.HandleToolUpdate(context.Background(), acpToolCall, startUpdate)
	if err != nil {
		t.Fatalf("HandleToolUpdate start: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("expected start event, got %d", len(evs))
	}
	if !strings.Contains(evs[0].Delta, "Status: started") {
		t.Fatalf("start summary missing Status: started: %s", evs[0].Delta)
	}
	if strings.Contains(evs[0].Delta, "Ended:") {
		t.Fatalf("start summary should not include Ended: %s", evs[0].Delta)
	}

	// Simulate a tool_call_update with output and completed status.
	endUpdate := map[string]any{
		"toolCallId": "tc-1",
		"toolCallUpdate": map[string]any{
			"rawOutput": "file contents here",
			"status":    "completed",
		},
	}
	evs, err = sink.HandleToolUpdate(context.Background(), acpToolCallUpdate, endUpdate)
	if err != nil {
		t.Fatalf("HandleToolUpdate end: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("expected 1 event on completion, got %d", len(evs))
	}
	if evs[0].Kind != aipapi.EventTextDelta {
		t.Fatalf("expected EventTextDelta, got %v", evs[0].Kind)
	}
	summary := evs[0].Delta
	if !strings.Contains(summary, "Tool: Read File") {
		t.Fatalf("summary missing tool name: %s", summary)
	}
	if !strings.Contains(summary, `Arguments: {"path":"/tmp/test.txt"}`) {
		t.Fatalf("summary missing tool arguments: %s", summary)
	}
	if !strings.Contains(summary, "Input size:") {
		t.Fatalf("summary missing input size: %s", summary)
	}
	if !strings.Contains(summary, "Output size:") {
		t.Fatalf("summary missing output size: %s", summary)
	}
	if !strings.Contains(summary, "```text") {
		t.Fatalf("summary missing fenced code block: %s", summary)
	}
}

func TestToolSummarySink_MultipleTools(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	tick := 0
	sink := NewToolSummarySink(func() time.Time {
		now := base.Add(time.Duration(tick) * time.Second)
		tick++
		return now
	})

	// Start two tools.
	for _, id := range []string{"tc-a", "tc-b"} {
		evs, err := sink.HandleToolUpdate(context.Background(), acpToolCall, map[string]any{
			"toolCallId": id,
			"toolCall":   map[string]any{"title": "Tool " + id},
		})
		if err != nil {
			t.Fatalf("start %s: %v", id, err)
		}
		if len(evs) != 1 {
			t.Fatalf("expected start event for %s, got %d", id, len(evs))
		}
	}

	// Complete both tools.
	for _, id := range []string{"tc-a", "tc-b"} {
		evs, err := sink.HandleToolUpdate(context.Background(), acpToolCallUpdate, map[string]any{
			"toolCallId": id,
			"toolCallUpdate": map[string]any{
				"status": "completed",
			},
		})
		if err != nil {
			t.Fatalf("end %s: %v", id, err)
		}
		if len(evs) != 1 {
			t.Fatalf("expected 1 event for %s, got %d", id, len(evs))
		}
	}
}

func TestToolSummarySink_FlushIncomplete(t *testing.T) {
	t.Parallel()
	sink := NewToolSummarySink(nil)

	// Start a tool that never completes.
	_, err := sink.HandleToolUpdate(context.Background(), acpToolCall, map[string]any{
		"toolCallId": "tc-incomplete",
		"toolCall":   map[string]any{"title": "Incomplete Tool", "rawInput": nil},
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	concreteSink, ok := sink.(*toolSummarySink)
	if !ok {
		t.Fatal("NewToolSummarySink did not return *toolSummarySink")
	}
	// Flush should emit a summary.
	evs := concreteSink.FlushIncomplete()
	if len(evs) != 1 {
		t.Fatalf("expected 1 flushed event, got %d", len(evs))
	}
	if !strings.Contains(evs[0].Delta, "Incomplete Tool") {
		t.Fatalf("flushed summary missing tool name: %s", evs[0].Delta)
	}
	if !strings.Contains(evs[0].Delta, "Arguments: null") {
		t.Fatalf("flushed summary missing explicit null input: %s", evs[0].Delta)
	}

	// Second flush should emit nothing (already flushed).
	evs = concreteSink.FlushIncomplete()
	if len(evs) != 0 {
		t.Fatalf("expected 0 events on second flush, got %d", len(evs))
	}
}

func TestToolSummarySink_DefaultToolName(t *testing.T) {
	t.Parallel()
	sink := NewToolSummarySink(nil)

	evs, err := sink.HandleToolUpdate(context.Background(), acpToolCall, map[string]any{
		"toolCallId": "tc-no-name",
		"status":     "completed",
	})
	if err != nil {
		t.Fatalf("HandleToolUpdate: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("expected 1 event, got %d", len(evs))
	}
	if !strings.Contains(evs[0].Delta, "Tool: tool") {
		t.Fatalf("expected default name 'tool', got: %s", evs[0].Delta)
	}
}

func TestToolSummarySink_CompletionPreservesExplicitNullInput(t *testing.T) {
	t.Parallel()
	sink := NewToolSummarySink(nil)

	evs, err := sink.HandleToolUpdate(context.Background(), acpToolCall, map[string]any{
		"toolCallId": "tc-null-input",
		"rawInput":   nil,
		"status":     "completed",
	})
	if err != nil {
		t.Fatalf("HandleToolUpdate: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("expected 1 event, got %d", len(evs))
	}
	if !strings.Contains(evs[0].Delta, "Arguments: null") {
		t.Fatalf("summary missing explicit null input: %s", evs[0].Delta)
	}
}

func TestToolSummarySink_OnlyOneSummaryPerTool(t *testing.T) {
	t.Parallel()
	sink := NewToolSummarySink(nil)

	// Complete a tool.
	update := map[string]any{
		"toolCallId": "tc-double",
		"status":     "completed",
	}
	evs, _ := sink.HandleToolUpdate(context.Background(), acpToolCall, update)
	if len(evs) != 1 {
		t.Fatalf("expected 1 event on first completion, got %d", len(evs))
	}

	// Second completion update should NOT produce another summary.
	evs, _ = sink.HandleToolUpdate(context.Background(), acpToolCallUpdate, update)
	if len(evs) != 0 {
		t.Fatalf("expected 0 events on second completion, got %d", len(evs))
	}
}

func TestToolSummarySink_KeyResolution(t *testing.T) {
	t.Parallel()
	sink := NewToolSummarySink(nil)

	// Test different key field names.
	cases := []string{"toolCallId", "id", "callId"}
	for _, key := range cases {
		update := map[string]any{
			key:      "tc-" + key,
			"status": "completed",
		}
		evs, err := sink.HandleToolUpdate(context.Background(), acpToolCall, update)
		if err != nil {
			t.Fatalf("key %s: %v", key, err)
		}
		if len(evs) != 1 {
			t.Fatalf("key %s: expected 1 event, got %d", key, len(evs))
		}
	}
}

func TestToolSummarySink_NilUpdate(t *testing.T) {
	t.Parallel()
	sink := NewToolSummarySink(nil)
	evs, err := sink.HandleToolUpdate(context.Background(), acpToolCall, nil)
	if err != nil {
		t.Fatalf("HandleToolUpdate nil: %v", err)
	}
	if len(evs) != 0 {
		t.Fatalf("expected 0 events for nil update, got %d", len(evs))
	}
}

func TestToolSummarySink_IsToolComplete(t *testing.T) {
	t.Parallel()
	complete := []string{"completed", "done", "finished", "success", "failed", "error", "cancelled", "Completed", "COMPLETED"}
	for _, s := range complete {
		if !isToolComplete(s) {
			t.Errorf("isToolComplete(%q) = false, want true", s)
		}
	}
	incomplete := []string{"", "running", "pending", "started", "in_progress"}
	for _, s := range incomplete {
		if isToolComplete(s) {
			t.Errorf("isToolComplete(%q) = true, want false", s)
		}
	}
}

func TestToolSummarySink_ExtractToolName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		input map[string]any
		want  string
	}{
		{map[string]any{"title": "My Tool"}, "My Tool"},
		{map[string]any{"name": "read_file"}, "read_file"},
		{map[string]any{"tool": "bash"}, "bash"},
		{map[string]any{"function": map[string]any{"name": "fn_name"}}, "fn_name"},
		{map[string]any{}, "tool"},
	}
	for _, c := range cases {
		got := extractToolName(c.input)
		if got != c.want {
			t.Errorf("extractToolName(%v) = %q, want %q", c.input, got, c.want)
		}
	}
}

func TestToolSummarySink_EstimateJSONSize(t *testing.T) {
	t.Parallel()
	// estimateJSONSize JSON-encodes all types for consistent size reporting.
	// json.Marshal("hello") = `"hello"` = 7 bytes (including quotes).
	if got := estimateJSONSize("hello"); got != 7 {
		t.Errorf("estimateJSONSize(string) = %d, want 7", got)
	}
	if got := estimateJSONSize(nil); got != 0 {
		t.Errorf("estimateJSONSize(nil) = %d, want 0", got)
	}
	if got := estimateJSONSize(map[string]any{"a": "b"}); got == 0 {
		t.Error("estimateJSONSize(map) should be > 0")
	}
}

func TestToolSummarySink_FormatSummary(t *testing.T) {
	t.Parallel()
	started := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	ended := started.Add(2 * time.Second)
	summary := formatToolCompletionSummary("test_tool", 100, 200, started, ended)
	if !strings.Contains(summary, "test_tool") {
		t.Fatalf("missing tool name: %s", summary)
	}
	if !strings.Contains(summary, "100 bytes") {
		t.Fatalf("missing input size: %s", summary)
	}
	if !strings.Contains(summary, "200 bytes") {
		t.Fatalf("missing output size: %s", summary)
	}
	if !strings.Contains(summary, "2.000 s") {
		t.Fatalf("missing elapsed time: %s", summary)
	}
}

func TestToolSummarySink_TruncatesLargeArguments(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	sink := NewToolSummarySink(func() time.Time { return now })
	largeQuery := strings.Repeat("x", 5000)

	evs, err := sink.HandleToolUpdate(context.Background(), acpToolCall, map[string]any{
		"toolCallId": "tc-large",
		"title":      "grep_search",
		"rawInput":   map[string]any{"Query": largeQuery},
		"status":     "completed",
	})
	if err != nil {
		t.Fatalf("HandleToolUpdate: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("expected 1 event, got %d", len(evs))
	}

	var argumentsLine string
	for line := range strings.SplitSeq(evs[0].Delta, "\n") {
		if strings.HasPrefix(line, "Arguments: ") {
			argumentsLine = line
			break
		}
	}
	if argumentsLine == "" {
		t.Fatalf("summary missing arguments: %s", evs[0].Delta)
	}
	renderedArguments := strings.TrimPrefix(argumentsLine, "Arguments: ")
	if len([]rune(renderedArguments)) > maxToolArgumentChars {
		t.Fatalf("arguments were not bounded: %d chars", len([]rune(renderedArguments)))
	}
	if !strings.HasSuffix(argumentsLine, "… [truncated]") {
		t.Fatalf("arguments line missing truncation marker: %s", argumentsLine)
	}
}
