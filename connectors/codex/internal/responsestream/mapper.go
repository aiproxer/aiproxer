package responsestream

import (
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/streampump"
)

// Mapper is not concurrency-safe; callers must serialize Handle calls on a single
// instance. It holds mutable per-stream state including tool-call tracking maps.
type Mapper struct {
	pending *streampump.PendingEventQueue

	sawResp      bool
	sawMsg       bool
	sawTextDelta bool

	toolCallStarted   map[string]bool
	toolCallArgDeltas map[string]bool
	toolCallFinished  map[string]bool
	pendingToolArgs   map[string][]string
}

func New(pending *streampump.PendingEventQueue) *Mapper {
	return &Mapper{
		pending:           pending,
		toolCallStarted:   make(map[string]bool),
		toolCallArgDeltas: make(map[string]bool),
		toolCallFinished:  make(map[string]bool),
		pendingToolArgs:   make(map[string][]string),
	}
}

func ToolCallID(primary, fallback string) string {
	if primary != "" {
		return primary
	}
	return fallback
}

func (m *Mapper) SawResponseStarted() bool {
	return m.sawResp
}

func (m *Mapper) SawTextDelta() bool {
	return m.sawTextDelta
}

func (m *Mapper) EnsureResponseStarted() error {
	return m.ensureResponseStarted()
}

func (m *Mapper) EnsureMessageStarted() error {
	return m.ensureMessageStarted()
}

func (m *Mapper) ResponseCreated() error {
	return m.ensureResponseStarted()
}

func (m *Mapper) OutputTextDelta(delta string) error {
	if err := m.ensureResponseStarted(); err != nil {
		return err
	}
	if err := m.ensureMessageStarted(); err != nil {
		return err
	}
	if delta == "" {
		return nil
	}
	m.sawTextDelta = true
	return m.pending.Push(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: delta})
}

// ReasoningDelta enqueues a reasoning text delta. ensureMessageStarted is not
// message-content coupling: the canonical event sequence contract
// (aipapi.ValidateEventSequence) treats EventReasoningDelta as a content-class
// event that requires a preceding EventMessageStarted, so the mapper must open
// the message frame before reasoning exactly as it does for text and tool
// calls. Dropping it would produce streams that fail sequence validation
// ("reasoning_delta before message_started").
func (m *Mapper) ReasoningDelta(delta string) error {
	if err := m.ensureResponseStarted(); err != nil {
		return err
	}
	if err := m.ensureMessageStarted(); err != nil {
		return err
	}
	if delta == "" {
		return nil
	}
	return m.pending.Push(aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: delta})
}

// ReasoningPart enqueues one complete dialect-tagged reasoning artifact.
func (m *Mapper) ReasoningPart(part *aipapi.ReasoningPart) error {
	if part == nil {
		return nil
	}
	if err := m.ensureResponseStarted(); err != nil {
		return err
	}
	if err := m.ensureMessageStarted(); err != nil {
		return err
	}
	return m.pending.Push(aipapi.Event{Kind: aipapi.EventReasoningPart, Reasoning: part})
}

func (m *Mapper) BeginCompleted() error {
	if err := m.ensureResponseStarted(); err != nil {
		return err
	}
	return m.ensureMessageStarted()
}

func (m *Mapper) CompletedTextFallback(text string) error {
	if m.sawTextDelta || text == "" {
		return nil
	}
	return m.pending.Push(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: text})
}

func (m *Mapper) PushUsage(usage *aipapi.Event) error {
	if usage == nil {
		return nil
	}
	return m.pending.Push(*usage)
}

func (m *Mapper) ResponseFinished() error {
	return m.pending.Push(aipapi.Event{Kind: aipapi.EventResponseFinished})
}

func (m *Mapper) StreamError(code, message, defaultMessage string) error {
	if err := m.ensureResponseStarted(); err != nil {
		return err
	}
	if message == "" {
		message = defaultMessage
	}
	return m.pending.Push(aipapi.Event{
		Kind:         aipapi.EventError,
		ErrorCode:    code,
		ErrorMessage: message,
	})
}

func (m *Mapper) ToolCallAdded(id, name string) error {
	if id == "" {
		return nil
	}
	if err := m.ensureResponseStarted(); err != nil {
		return err
	}
	if m.toolCallStarted[id] {
		return nil
	}
	if err := m.emitToolCallStarted(id, name); err != nil {
		return err
	}
	return m.flushPendingToolArgs(id)
}

func (m *Mapper) ToolCallArgsDelta(id, delta string) error {
	if id == "" || delta == "" {
		return nil
	}
	if err := m.ensureResponseStarted(); err != nil {
		return err
	}
	if !m.toolCallStarted[id] {
		m.pendingToolArgs[id] = append(m.pendingToolArgs[id], delta)
		return nil
	}
	m.toolCallArgDeltas[id] = true
	return m.pending.Push(aipapi.Event{
		Kind:       aipapi.EventToolCallArgsDelta,
		ToolCallID: id,
		Delta:      delta,
	})
}

func (m *Mapper) FinishToolCallArguments(id, name, arguments string) error {
	if id == "" {
		return nil
	}
	if err := m.ensureResponseStarted(); err != nil {
		return err
	}
	if !m.toolCallStarted[id] {
		if err := m.emitToolCallStarted(id, name); err != nil {
			return err
		}
	}
	if err := m.flushPendingToolArgs(id); err != nil {
		return err
	}
	if !m.toolCallArgDeltas[id] && arguments != "" {
		if err := m.pending.Push(aipapi.Event{
			Kind:       aipapi.EventToolCallArgsDelta,
			ToolCallID: id,
			Delta:      arguments,
		}); err != nil {
			return err
		}
		// Record the fallback emission so a second final notification for the
		// same call cannot re-emit arguments after the finish.
		m.toolCallArgDeltas[id] = true
	}
	return m.EmitToolCallFinished(id)
}

func (m *Mapper) EmitCompletedToolCall(id, name, arguments string) error {
	if id == "" || m.toolCallFinished[id] {
		return nil
	}
	if err := m.ensureResponseStarted(); err != nil {
		return err
	}
	if !m.toolCallStarted[id] {
		if err := m.emitToolCallStarted(id, name); err != nil {
			return err
		}
	}
	if err := m.flushPendingToolArgs(id); err != nil {
		return err
	}
	if !m.toolCallArgDeltas[id] && arguments != "" {
		if err := m.pending.Push(aipapi.Event{
			Kind:       aipapi.EventToolCallArgsDelta,
			ToolCallID: id,
			Delta:      arguments,
		}); err != nil {
			return err
		}
		// Same idempotency as FinishToolCallArguments: a repeated completed
		// snapshot must not re-emit arguments the first snapshot already sent.
		m.toolCallArgDeltas[id] = true
	}
	return m.EmitToolCallFinished(id)
}

func (m *Mapper) EmitToolCallFinished(id string) error {
	if id == "" {
		return nil
	}
	if m.toolCallFinished[id] {
		return nil
	}
	m.toolCallFinished[id] = true
	return m.pending.Push(aipapi.Event{
		Kind:       aipapi.EventToolCallFinished,
		ToolCallID: id,
	})
}

func (m *Mapper) ensureResponseStarted() error {
	if m.sawResp {
		return nil
	}
	m.sawResp = true
	return m.pending.Push(aipapi.Event{Kind: aipapi.EventResponseStarted})
}

func (m *Mapper) ensureMessageStarted() error {
	if m.sawMsg {
		return nil
	}
	m.sawMsg = true
	return m.pending.Push(aipapi.Event{Kind: aipapi.EventMessageStarted})
}

func (m *Mapper) emitToolCallStarted(id, name string) error {
	m.toolCallStarted[id] = true
	if err := m.ensureMessageStarted(); err != nil {
		return err
	}
	return m.pending.Push(aipapi.Event{
		Kind:       aipapi.EventToolCallStarted,
		ToolCallID: id,
		ToolName:   name,
	})
}

func (m *Mapper) flushPendingToolArgs(id string) error {
	deltas := m.pendingToolArgs[id]
	if len(deltas) == 0 {
		return nil
	}
	delete(m.pendingToolArgs, id)
	for _, delta := range deltas {
		m.toolCallArgDeltas[id] = true
		if err := m.pending.Push(aipapi.Event{
			Kind:       aipapi.EventToolCallArgsDelta,
			ToolCallID: id,
			Delta:      delta,
		}); err != nil {
			return err
		}
	}
	return nil
}

// RemapToolCallID consolidates tool-call state buffered under oldID onto newID.
// It is used when a tool call's real call_id is learned after argument deltas
// were already buffered under the provisional item-only ID, so pending args and
// started/arg-delta/finished flags all move onto the canonical ID instead of
// fragmenting into two tool calls. Remapping is a no-op when no state exists
// under oldID.
func (m *Mapper) RemapToolCallID(oldID, newID string) {
	if oldID == "" || newID == "" || oldID == newID {
		return
	}
	if deltas, ok := m.pendingToolArgs[oldID]; ok {
		m.pendingToolArgs[newID] = append(m.pendingToolArgs[newID], deltas...)
		delete(m.pendingToolArgs, oldID)
	}
	if m.toolCallStarted[oldID] {
		m.toolCallStarted[newID] = true
		delete(m.toolCallStarted, oldID)
	}
	if m.toolCallArgDeltas[oldID] {
		m.toolCallArgDeltas[newID] = true
		delete(m.toolCallArgDeltas, oldID)
	}
	if m.toolCallFinished[oldID] {
		m.toolCallFinished[newID] = true
		delete(m.toolCallFinished, oldID)
	}
}
