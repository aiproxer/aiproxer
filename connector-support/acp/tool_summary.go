package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// toolAccum tracks per-tool-call streaming statistics for completion summaries.
// It retains timing, byte counts, and completion state for one tool call.
type toolAccum struct {
	name        string
	startedAt   time.Time
	endedAt     time.Time
	input       any
	inputBytes  int
	outputBytes int
	inputSet    bool // prevents double-counting if rawInput appears in updates
	outputSet   bool // prevents double-counting if rawOutput appears in updates
	summarySent bool
	startSent   bool
}

// toolSummarySink coalesces ACP tool_call and tool_call_update payloads and emits
// compact completion summaries as aipapi events.
// It implements ToolUpdateSink so it can be plugged into SessionUpdateMapperOptions.
//
// Completion summary format:
//
//	---
//	```text
//	Tool: <name>
//	Input size: <N> bytes
//	Started: <iso8601>
//	Ended: <iso8601> (<elapsed>s)
//	Output size: <N> bytes
//	```
type toolSummarySink struct {
	mu       sync.Mutex
	tools    map[string]*toolAccum // keyed by tool call id or anonymous key
	now      func() time.Time
	nextAnon int64 // counter for anonymous keys
}

// NewToolSummarySink creates a ToolUpdateSink that coalesces tool updates and
// emits compact completion summaries. If now is nil, time.Now is used.
func NewToolSummarySink(now func() time.Time) ToolUpdateSink {
	if now == nil {
		now = time.Now
	}
	return &toolSummarySink{
		tools: make(map[string]*toolAccum),
		now:   now,
	}
}

// HandleToolUpdate processes a tool_call or tool_call_update session/update
// payload, coalescing state and emitting a completion summary when the tool
// call is complete.
func (s *toolSummarySink) HandleToolUpdate(_ context.Context, kind string, update map[string]any) ([]aipapi.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := resolveToolStreamKey(update, &s.nextAnon)
	if key == "" {
		return nil, nil
	}

	accum := s.tools[key]
	if accum == nil {
		accum = &toolAccum{startedAt: s.now()}
		s.tools[key] = accum
	}

	// Merge fields from the update.
	merged := coalesceToolSessionDict(update, kind)
	// Only update the name when a real (non-default) name is found, so a
	// tool_call_update without a name field doesn't overwrite the name set
	// by the initial tool_call. Fall back to "tool" if no name was ever set.
	if name := extractToolName(merged); name != "tool" && name != "" {
		accum.name = name
	}
	if accum.name == "" {
		accum.name = "tool"
	}
	// Set input/output bytes once (first writer wins), avoiding double-counting
	// when a tool_call_update repeats the rawInput field from the initial tool_call.
	if !accum.inputSet {
		if input, ok := extractToolInput(merged); ok {
			ib := estimateJSONSize(input)
			accum.input = input
			accum.inputBytes = ib
			accum.inputSet = true
		}
	}
	if !accum.outputSet {
		if ob := extractToolOutputBytes(merged); ob > 0 {
			accum.outputBytes = ob
			accum.outputSet = true
		}
	}

	// Check for completion status.
	status := extractToolStatus(merged)
	if isToolComplete(status) {
		if accum.summarySent {
			return nil, nil
		}
		accum.summarySent = true
		accum.startSent = true
		accum.endedAt = s.now()
		summary := formatToolCompletionSummaryWithInput(accum.name, accum.input, accum.inputSet, accum.inputBytes, accum.outputBytes, accum.startedAt, accum.endedAt)
		// Keep the entry in the map (with summarySent=true) so duplicate
		// completion updates are silently ignored rather than producing a
		// second summary.
		return []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: summary}}, nil
	}

	if accum.startSent || accum.summarySent {
		return nil, nil
	}
	accum.startSent = true
	return []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: formatToolStartedSummary(accum.name, accum.input, accum.inputSet, accum.inputBytes, accum.startedAt)}}, nil
}

// FlushIncomplete emits summaries for any tools that haven't received a completion
// status. Called when a prompt stream ends to ensure all tool calls are summarized.
func (s *toolSummarySink) FlushIncomplete() []aipapi.Event {
	s.mu.Lock()
	defer s.mu.Unlock()

	var events []aipapi.Event
	for key, accum := range s.tools {
		if accum.summarySent {
			continue
		}
		accum.summarySent = true
		accum.endedAt = s.now()
		summary := formatToolCompletionSummaryWithInput(accum.name, accum.input, accum.inputSet, accum.inputBytes, accum.outputBytes, accum.startedAt, accum.endedAt)
		events = append(events, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: summary})
		delete(s.tools, key)
	}
	// Also clean up entries that were already summarized (kept for dedup).
	for key, accum := range s.tools {
		if accum.summarySent {
			delete(s.tools, key)
		}
	}
	return events
}

// resolveToolStreamKey extracts a stable correlation key from a tool update.
// It falls back to an anonymous sequential key if no stable id is present
// in the form __anon__:{seq}. The counter belongs to the
// calling sink instance.
func resolveToolStreamKey(update map[string]any, nextAnon *int64) string {
	if update == nil {
		return ""
	}
	// Check for toolCallId, id, or callId fields.
	for _, key := range []string{"toolCallId", "id", "callId", "toolCallIdHash"} {
		if v, ok := update[key].(string); ok && v != "" {
			return v
		}
	}
	// Check nested content for toolCallId.
	if content, ok := update["content"].(map[string]any); ok {
		for _, key := range []string{"toolCallId", "id", "callId"} {
			if v, ok := content[key].(string); ok && v != "" {
				return v
			}
		}
	}
	// Anonymous key for tools without stable IDs.
	*nextAnon++
	return fmt.Sprintf("__anon__:%d", *nextAnon)
}

// coalesceToolSessionDict merges tool_call and tool_call_update fields into a
// single map for extraction. Nested toolCall and
// toolCallUpdate maps are flattened into the top level (existing top-level keys
// take priority — only missing keys are filled from nested maps).
func coalesceToolSessionDict(update map[string]any, kind string) map[string]any {
	result := make(map[string]any)
	// Copy all fields from the update.
	maps.Copy(result, update)
	// For tool_call_update, merge the nested toolCallUpdate fields.
	if kind == acpToolCallUpdate {
		if tcu, ok := update["toolCallUpdate"].(map[string]any); ok {
			for k, v := range tcu {
				if _, exists := result[k]; !exists {
					result[k] = v
				}
			}
		}
	}
	// Also merge nested toolCall fields (higher priority — overwrite if the
	// top level doesn't already have a non-nil value for the key).
	if tc, ok := update["toolCall"].(map[string]any); ok {
		// toolCall fields overwrite top-level
		maps.Copy(result, tc)
	}
	// Merge content fields.
	if content, ok := update["content"].(map[string]any); ok {
		for k, v := range content {
			if _, exists := result[k]; !exists {
				result[k] = v
			}
		}
	}
	return result
}

// extractToolName tries multiple possible field names for the tool name.
func extractToolName(d map[string]any) string {
	for _, key := range []string{"title", "name", "tool", "command", "kind"} {
		if v, ok := d[key].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	// Check function.name.
	if fn, ok := d["function"].(map[string]any); ok {
		if name, ok := fn["name"].(string); ok && name != "" {
			return name
		}
	}
	return "tool"
}

// extractToolInput returns the first input payload exposed by ACP.
func extractToolInput(d map[string]any) (any, bool) {
	for _, key := range []string{"rawInput", "arguments", "params", "args"} {
		if v, ok := d[key]; ok {
			return v, true
		}
	}
	return nil, false
}

// extractToolOutputBytes estimates the output size from the tool payload.
func extractToolOutputBytes(d map[string]any) int {
	for _, key := range []string{"rawOutput", "result", "output", "response"} {
		if v, ok := d[key]; ok {
			return estimateJSONSize(v)
		}
	}
	// Check for content blocks with text.
	if content, ok := d["content"].([]any); ok {
		total := 0
		for _, block := range content {
			if m, ok := block.(map[string]any); ok {
				if text, ok := m["text"].(string); ok {
					total += len(text)
				}
			}
		}
		return total
	}
	return 0
}

// extractToolStatus returns the tool status string if present.
func extractToolStatus(d map[string]any) string {
	for _, key := range []string{"status", "state"} {
		if v, ok := d[key].(string); ok {
			return v
		}
	}
	return ""
}

// isToolComplete returns true if the status indicates the tool call is done.
func isToolComplete(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "done", "finished", "success", "failed", "error", "cancelled":
		return true
	default:
		return false
	}
}

// estimateJSONSize estimates the byte size of a value by JSON-encoding it.
// All types (including strings) are JSON-encoded; the reported size is the
// encoded byte length, or zero for nil values and encoding failures.
func estimateJSONSize(v any) int {
	if v == nil {
		return 0
	}
	b, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return len(b)
}

// FormatToolCompletionSummary generates the compact fenced summary block shared
// by ACP tool_call completions and Codex app-server item/completed summaries.
// started and ended are normalized to UTC for stable cross-path output. The
// elapsed seconds are derived from ended-started.
func FormatToolCompletionSummary(name string, inputBytes, outputBytes int, started, ended time.Time) string {
	return formatToolCompletionSummaryWithInput(name, nil, false, inputBytes, outputBytes, started, ended)
}

const (
	maxToolArgumentChars         = 1024
	toolArgumentTruncationMarker = "… [truncated]"
)

func formatToolStartedSummary(name string, input any, inputSet bool, inputBytes int, started time.Time) string {
	lines := []string{"---", "```text", fmt.Sprintf("Tool: %s", name), "Status: started"}
	if inputSet {
		lines = append(lines, "Arguments: "+formatToolArguments(input))
	}
	lines = append(
		lines,
		fmt.Sprintf("Input size: %d bytes", inputBytes),
		fmt.Sprintf("Started: %s", started.UTC().Format(time.RFC3339Nano)),
		"```",
	)
	return strings.Join(lines, "\n") + "\n"
}

func formatToolCompletionSummaryWithInput(name string, input any, inputSet bool, inputBytes, outputBytes int, started, ended time.Time) string {
	elapsed := ended.Sub(started).Seconds()
	lines := []string{"---", "```text", fmt.Sprintf("Tool: %s", name)}
	if inputSet {
		lines = append(lines, "Arguments: "+formatToolArguments(input))
	}
	lines = append(
		lines,
		fmt.Sprintf("Input size: %d bytes", inputBytes),
		fmt.Sprintf("Started: %s", started.UTC().Format(time.RFC3339Nano)),
		fmt.Sprintf("Ended: %s (%.3f s)", ended.UTC().Format(time.RFC3339Nano), elapsed),
		fmt.Sprintf("Output size: %d bytes", outputBytes),
		"```",
	)
	return strings.Join(lines, "\n") + "\n"
}

func formatToolArguments(input any) string {
	var rendered string
	if raw, ok := input.(string); ok {
		var compact bytes.Buffer
		if err := json.Compact(&compact, []byte(raw)); err == nil {
			rendered = compact.String()
		} else {
			rendered = raw
		}
	} else if encoded, err := json.Marshal(input); err == nil {
		rendered = string(encoded)
	} else {
		rendered = fmt.Sprint(input)
	}

	runes := []rune(rendered)
	if len(runes) > maxToolArgumentChars {
		limit := maxToolArgumentChars - len([]rune(toolArgumentTruncationMarker))
		rendered = strings.TrimSpace(string(runes[:limit])) + toolArgumentTruncationMarker
	}
	return rendered
}

// formatToolCompletionSummary is retained as an unexported alias for in-package
// call sites; new external callers should use FormatToolCompletionSummary.
func formatToolCompletionSummary(name string, inputBytes, outputBytes int, started, ended time.Time) string {
	return FormatToolCompletionSummary(name, inputBytes, outputBytes, started, ended)
}
