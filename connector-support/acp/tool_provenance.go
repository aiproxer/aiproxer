package acp

import (
	"context"
	"sync"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// maxToolProvenanceNameChars bounds tool names retained on structured events.
const maxToolProvenanceNameChars = 512

// StructuredToolProvenanceSpec is the connector opt-in for structured
// tool-call provenance. Opted connectors emit canonical tool lifecycle
// events; all others keep the shared text summaries.
type StructuredToolProvenanceSpec interface {
	StructuredToolProvenance() bool
}

// toolProvenanceCall tracks one tool call's emitted lifecycle for
// exactly-once structured events.
type toolProvenanceCall struct {
	name                string
	started, args, done bool
}

// toolProvenanceSink maps ACP tool_call and tool_call_update payloads to the
// canonical tool lifecycle (started, args delta, finished) instead of
// generated assistant text. It implements ToolUpdateSink so it plugs into
// SessionUpdateMapperOptions wherever the summary sink does.
//
// Result content has no canonical event field, so completion carries
// identity only; output sizes remain visible through the summary sink on
// connectors that keep it.
type toolProvenanceSink struct {
	mu       sync.Mutex
	tools    map[string]*toolProvenanceCall
	nextAnon int64
}

// NewToolProvenanceSink creates a ToolUpdateSink emitting structured tool
// lifecycle events with exactly-once semantics per tool call.
func NewToolProvenanceSink() ToolUpdateSink {
	return &toolProvenanceSink{tools: make(map[string]*toolProvenanceCall)}
}

// HandleToolUpdate processes a tool_call or tool_call_update payload,
// emitting Started once, one args snapshot, and Finished once.
func (s *toolProvenanceSink) HandleToolUpdate(_ context.Context, kind string, update map[string]any) ([]aipapi.Event, error) {
	if update == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	key := s.streamKey(update)
	if key == "" {
		return nil, nil
	}
	call := s.tools[key]
	if call == nil {
		call = &toolProvenanceCall{}
		s.tools[key] = call
	}
	merged := coalesceToolSessionDict(update, kind)
	if name := truncateProvenanceName(extractToolName(merged)); name != "" && name != "tool" {
		call.name = name
	}
	if call.name == "" {
		call.name = "tool"
	}

	var evs []aipapi.Event
	if !call.started {
		call.started = true
		evs = append(evs, aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: key, ToolName: call.name})
	}
	if !call.args {
		if input, ok := extractToolInput(merged); ok {
			call.args = true
			evs = append(evs, aipapi.Event{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: key, Delta: formatToolArguments(input)})
		}
	}
	if !call.done && isToolComplete(extractToolStatus(merged)) {
		call.done = true
		evs = append(evs, aipapi.Event{Kind: aipapi.EventToolCallFinished, ToolCallID: key, ToolName: call.name})
	}
	return evs, nil
}

// FlushIncomplete closes tool calls that never reported completion so
// downstream lifecycle tracking cannot hang on an open call. Entries are
// dropped after flushing; the stream is at EOF, so no later update can
// arrive for them.
func (s *toolProvenanceSink) FlushIncomplete() []aipapi.Event {
	s.mu.Lock()
	defer s.mu.Unlock()

	var events []aipapi.Event
	for key, call := range s.tools {
		if !call.done {
			call.done = true
			events = append(events, aipapi.Event{Kind: aipapi.EventToolCallFinished, ToolCallID: key, ToolName: call.name})
		}
		delete(s.tools, key)
	}
	return events
}

// streamKey extracts a stable correlation key, minting an anonymous one for
// updates without a stable id (same policy as the summary sink).
func (s *toolProvenanceSink) streamKey(update map[string]any) string {
	return resolveToolStreamKey(update, &s.nextAnon)
}

// truncateProvenanceName bounds retained tool names without splitting UTF-8.
func truncateProvenanceName(name string) string {
	runes := []rune(name)
	if len(runes) > maxToolProvenanceNameChars {
		return string(runes[:maxToolProvenanceNameChars])
	}
	return name
}
