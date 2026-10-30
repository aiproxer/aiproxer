package streamdebug

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Enabled reports whether verbose local turn diagnostics are enabled.
func Enabled() bool {
	return diag.DebugTurnsEnabled()
}

// LogCall records a compact canonical request shape without logging prompt text.
func LogCall(ctx context.Context, log *slog.Logger, frontend string, call *aipapi.Call, stream bool, bodyBytes int, selector string) {
	if !Enabled() || call == nil {
		return
	}
	s := summarizeCall(call)
	diag.LoggerOrDefault(log).DebugContext(
		ctx, "aip.debug.frontend_call",
		"frontend", frontend,
		"call_id", call.ID,
		"trace_id", diag.StableCallID(call),
		"a_leg_id", strings.TrimSpace(call.Session.ALegID),
		"operation", string(call.Invocation.Operation),
		"route_selector", selector,
		"stream", stream,
		"body_bytes", bodyBytes,
		"messages", len(call.Messages),
		"instructions", len(call.Instructions),
		"tools", len(call.Tools),
		"role_counts", strings.Join(s.roleCounts, ","),
		"part_counts", strings.Join(s.partCounts, ","),
		"tool_result_ids", strings.Join(s.toolResultIDs, ","),
		"assistant_tool_call_ids", strings.Join(s.assistantToolCallIDs, ","),
		"reasoning_effort", call.Options.ReasoningEffort,
		"verbosity", call.Options.Verbosity,
		"has_max_output_tokens", call.Options.MaxOutputTokens != nil,
		"has_temperature", call.Options.Temperature != nil,
		"has_top_p", call.Options.TopP != nil,
	)
}

// LogDecodeFailure records why a frontend rejected a request before canonical call creation.
func LogDecodeFailure(ctx context.Context, log *slog.Logger, frontend string, body []byte, err error) {
	if !Enabled() {
		return
	}
	summary := summarizeBody(body)
	diag.LoggerOrDefault(log).DebugContext(
		ctx, "aip.debug.frontend_decode_failed",
		"frontend", frontend,
		"body_bytes", len(body),
		"json_valid", summary.valid,
		"top_keys", strings.Join(summary.keys, ","),
		"model", summary.model,
		"messages", summary.messages,
		"input_items", summary.inputItems,
		"tools_present", summary.toolsPresent,
		"error", errString(err),
	)
}

// LogExecuteOpened records time spent before the executor returns an event stream.
func LogExecuteOpened(ctx context.Context, log *slog.Logger, frontend string, call *aipapi.Call, start time.Time) {
	if !Enabled() || call == nil {
		return
	}
	diag.LoggerOrDefault(log).DebugContext(
		ctx, "aip.debug.frontend_execute_opened",
		"frontend", frontend,
		"call_id", call.ID,
		"trace_id", diag.StableCallID(call),
		"a_leg_id", strings.TrimSpace(call.Session.ALegID),
		"duration_ms", time.Since(start).Milliseconds(),
	)
}

// Wrap logs stream progress and terminal state while preserving EventStream semantics.
// Diag correlation fields (trace ID, A-leg ID) are captured from ctx now — not stored
// as a context — so Close can log with the same correlation after the request ctx is
// gone (AGENTS.md: do not store contexts in structs).
func Wrap(ctx context.Context, log *slog.Logger, frontend string, call *aipapi.Call, es aipapi.EventStream, start time.Time) aipapi.EventStream {
	if !Enabled() || es == nil || call == nil {
		return es
	}
	traceID := diag.TraceID(ctx)
	if traceID == "" {
		traceID = diag.StableCallID(call)
	}
	aLegID := diag.ALegID(ctx)
	if aLegID == "" {
		aLegID = strings.TrimSpace(call.Session.ALegID)
	}
	return &stream{
		log:      diag.LoggerOrDefault(log),
		frontend: frontend,
		call:     call,
		inner:    es,
		start:    start,
		traceID:  traceID,
		aLegID:   aLegID,
	}
}

var _ aipapi.EventStream = (*stream)(nil)

type stream struct {
	mu             sync.Mutex
	log            *slog.Logger
	frontend       string
	call           *aipapi.Call
	inner          aipapi.EventStream
	start          time.Time
	traceID        string
	aLegID         string
	count          int
	kindCounts     map[string]int
	firstTextMs    int64
	firstReasonMs  int64
	firstLogged    bool
	terminalLogged bool
}

func (s *stream) Recv(ctx context.Context) (aipapi.Event, error) {
	ev, err := s.inner.Recv(ctx)
	if err != nil {
		s.logTerminal(ctx, err)
		return ev, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.count++
	if s.kindCounts == nil {
		s.kindCounts = map[string]int{}
	}
	s.kindCounts[string(ev.Kind)]++
	if ev.Kind == aipapi.EventTextDelta && s.firstTextMs == 0 {
		s.firstTextMs = time.Since(s.start).Milliseconds()
		s.logContentFirst(ctx, ev.Kind, ev.Delta, "")
	}
	if ev.Kind == aipapi.EventReasoningDelta && s.firstReasonMs == 0 {
		s.firstReasonMs = time.Since(s.start).Milliseconds()
		s.logContentFirst(ctx, ev.Kind, ev.Delta, "")
	}
	if !s.firstLogged {
		s.firstLogged = true
		s.log.DebugContext(
			ctx, "aip.debug.stream_first_event",
			"frontend", s.frontend,
			"call_id", s.call.ID,
			"trace_id", diag.StableCallID(s.call),
			"a_leg_id", strings.TrimSpace(s.call.Session.ALegID),
			"event_kind", string(ev.Kind),
			"duration_ms", time.Since(s.start).Milliseconds(),
		)
	}
	if shouldLogEvent(ev.Kind) {
		s.log.DebugContext(
			ctx, "aip.debug.stream_event",
			"frontend", s.frontend,
			"call_id", s.call.ID,
			"trace_id", diag.StableCallID(s.call),
			"event_index", s.count,
			"event_kind", string(ev.Kind),
			"tool_call_id", ev.ToolCallID,
			"tool_name", ev.ToolName,
			"finish_reason", ev.FinishReason,
		)
	}
	return ev, nil
}

func (s *stream) Close() error {
	err := s.inner.Close()
	// The request ctx is unavailable here by contract; rebuild a correlation-only ctx
	// from the fields captured at Wrap so handlers that read trace/A-leg IDs from ctx
	// keep terminal-log correlation.
	s.logTerminal(diag.EnsureCallDiag(context.Background(), s.traceID, s.aLegID), err)
	return err
}

func (s *stream) logTerminal(ctx context.Context, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminalLogged {
		return
	}
	s.terminalLogged = true
	status := "closed"
	switch {
	case errors.Is(err, io.EOF):
		status = "eof"
	case err != nil:
		status = "error"
	}
	s.log.DebugContext(
		ctx, "aip.debug.stream_terminal",
		"frontend", s.frontend,
		"call_id", s.call.ID,
		"trace_id", diag.StableCallID(s.call),
		"a_leg_id", strings.TrimSpace(s.call.Session.ALegID),
		"status", status,
		"error", errString(err),
		"events", s.count,
		"event_counts", strings.Join(diag.StableCounts(s.kindCounts), ","),
		"first_text_ms", s.firstTextMs,
		"first_reasoning_ms", s.firstReasonMs,
		"duration_ms", time.Since(s.start).Milliseconds(),
	)
}

func (s *stream) logContentFirst(ctx context.Context, kind aipapi.EventKind, delta, detail string) {
	s.log.DebugContext(
		ctx, "aip.debug.stream_first_content_event",
		"frontend", s.frontend,
		"call_id", s.call.ID,
		"trace_id", diag.StableCallID(s.call),
		"a_leg_id", strings.TrimSpace(s.call.Session.ALegID),
		"event_kind", string(kind),
		"delta_bytes", len(delta),
		"detail", detail,
		"duration_ms", time.Since(s.start).Milliseconds(),
	)
}

func shouldLogEvent(kind aipapi.EventKind) bool {
	switch kind {
	case aipapi.EventToolCallStarted, aipapi.EventToolCallFinished, aipapi.EventError, aipapi.EventResponseFinished:
		return true
	default:
		return false
	}
}

func errString(err error) string {
	if err == nil || errors.Is(err, io.EOF) {
		return ""
	}
	return err.Error()
}

type callSummary struct {
	roleCounts           []string
	partCounts           []string
	toolResultIDs        []string
	assistantToolCallIDs []string
}

type bodySummary struct {
	valid        bool
	keys         []string
	model        string
	messages     int
	inputItems   int
	toolsPresent bool
}

func summarizeBody(body []byte) bodySummary {
	out := bodySummary{valid: json.Valid(body)}
	var top map[string]json.RawMessage
	if json.Unmarshal(body, &top) != nil {
		return out
	}
	keys := make([]string, 0, len(top))
	for key := range top {
		keys = append(keys, key)
	}
	out.keys = stableStrings(keys)
	if raw := top["model"]; len(raw) > 0 {
		_ = json.Unmarshal(raw, &out.model)
	}
	if raw := top["messages"]; len(raw) > 0 {
		var msgs []json.RawMessage
		if json.Unmarshal(raw, &msgs) == nil {
			out.messages = len(msgs)
		}
	}
	if raw := top["input"]; len(raw) > 0 {
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) == nil {
			out.inputItems = len(items)
		} else {
			var text string
			if json.Unmarshal(raw, &text) == nil && text != "" {
				out.inputItems = 1
			}
		}
	}
	_, out.toolsPresent = top["tools"]
	return out
}

func summarizeCall(call *aipapi.Call) callSummary {
	roleCounts := map[string]int{}
	partCounts := map[string]int{}
	var toolResultIDs []string
	var assistantToolCallIDs []string
	for _, msg := range call.Messages {
		roleCounts[string(msg.Role)]++
		for _, part := range msg.Parts {
			partCounts[string(part.Kind)]++
			switch {
			case part.Kind == aipapi.PartToolResult:
				toolResultIDs = diag.AppendLimited(toolResultIDs, part.ToolCallID, 12)
			case msg.Role == aipapi.RoleAssistant && part.Kind == aipapi.PartJSON:
				assistantToolCallIDs = diag.AppendLimited(assistantToolCallIDs, assistantJSONCallID(part.Content), 12)
			}
		}
	}
	return callSummary{
		roleCounts:           diag.StableCounts(roleCounts),
		partCounts:           diag.StableCounts(partCounts),
		toolResultIDs:        toolResultIDs,
		assistantToolCallIDs: assistantToolCallIDs,
	}
}

func stableStrings(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}

func assistantJSONCallID(raw []byte) string {
	const maxProbe = 4096
	if len(raw) > maxProbe {
		raw = raw[:maxProbe]
	}
	body := string(raw)
	for _, key := range []string{`"call_id"`, `"id"`} {
		_, rest, ok := strings.Cut(body, key)
		if !ok {
			continue
		}
		colon := strings.IndexByte(rest, ':')
		if colon < 0 {
			continue
		}
		rest = strings.TrimSpace(rest[colon+1:])
		if !strings.HasPrefix(rest, `"`) {
			continue
		}
		rest = rest[1:]
		end := strings.IndexByte(rest, '"')
		if end > 0 {
			return rest[:end]
		}
	}
	return ""
}
