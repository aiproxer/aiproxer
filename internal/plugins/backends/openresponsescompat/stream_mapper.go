package openresponsescompat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	proto "github.com/aiproxer/aiproxer/internal/plugins/protocols/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// remoteStreamEvent is the decoded body of one remote SSE event. The outer
// SequenceNumber shadows the embedded int so a nil pointer distinguishes an
// absent sequence number from an explicit 0 while all other fields stay
// promoted from the production wire type. The error event carries its details
// as top-level code/param/message fields in addition to the optional nested
// error object, so both shapes are captured here.
type remoteStreamEvent struct {
	proto.StreamEvent
	SequenceNumber *int   `json:"sequence_number"`
	Code           string `json:"code"`
	Param          string `json:"param"`
	Message        string `json:"message"`
}

// decodeRemoteStreamEvent decodes one non-[DONE] SSE record and enforces
// event/body type agreement: the wire event discriminator must exactly match
// the JSON body's type discriminator, and both must be present. The bounded
// data payload has already been capped by the record parser.
func decodeRemoteStreamEvent(id string, rec sseRecord) (*remoteStreamEvent, error) {
	data := bytes.TrimSpace(rec.data)
	if len(data) == 0 {
		return nil, fmt.Errorf("%s: %w: SSE event %q carries no data payload", id, ErrMalformedResponse, rec.eventType)
	}
	var ev remoteStreamEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		return nil, fmt.Errorf("%s: %w: malformed SSE event body: %v", id, ErrMalformedResponse, err)
	}
	if strings.TrimSpace(ev.Type) == "" {
		return nil, fmt.Errorf("%s: %w: SSE event body is missing the type discriminator", id, ErrMalformedResponse)
	}
	if strings.TrimSpace(rec.eventType) == "" {
		return nil, fmt.Errorf("%s: %w: SSE event %q is missing the event field", id, ErrMalformedResponse, ev.Type)
	}
	if rec.eventType != ev.Type {
		return nil, fmt.Errorf("%s: %w: SSE event field %q does not match body type %q", id, ErrMalformedResponse, rec.eventType, ev.Type)
	}
	return &ev, nil
}

// streamMapper maps remote OpenResponses SSE events incrementally onto the
// canonical event stream. It enforces the pinned profile's item/content/part
// lifecycle and sequence rules, and ensures every produced canonical event
// satisfies the canonical envelope bounds before it reaches the stream. Unknown
// unprefixed output is rejected; valid vendor-prefixed output is preserved as
// bounded private evidence and never disturbs the canonical stream.
type streamMapper struct {
	id     string
	limits ResponseLimits

	started  bool
	terminal bool
	sawDone  bool
	sawMsg   bool
	lastSeq  int
	sawSeq   bool

	itemCount      int
	textBytes      int
	argBytes       int
	reasoningBytes int

	activeItemType  string
	activeItemID    string
	activeCallID    string
	contentPartOpen bool
	textPartOpen    bool

	pendingError *proto.WireErrorDetails

	native NativeEvidence
}

func newStreamMapper(id string, limits ResponseLimits) *streamMapper {
	return &streamMapper{
		id:     id,
		limits: limits,
	}
}

// Native returns the bounded private native evidence captured while mapping.
// It is never forwarded onto the canonical stream.
func (m *streamMapper) Native() NativeEvidence {
	return m.native
}

// mapRecord processes one parsed SSE record. [DONE] and post-terminal records
// are validated here; everything else is decoded and mapped. An empty event
// slice means the record produced no canonical events (extension/error
// carriers, item metadata, or [DONE]).
func (m *streamMapper) mapRecord(rec sseRecord) ([]aipapi.Event, error) {
	if isDONESeRecord(rec.data) {
		if m.sawDone {
			return nil, m.malformed("duplicate [DONE]")
		}
		m.sawDone = true
		if !m.terminal {
			return nil, m.malformed("[DONE] received before a terminal response event")
		}
		return nil, nil
	}
	if m.terminal {
		return nil, m.malformed("SSE event %q received after the terminal response event", rec.eventType)
	}
	ev, err := decodeRemoteStreamEvent(m.id, rec)
	if err != nil {
		return nil, err
	}
	if err := m.checkSequence(ev); err != nil {
		return nil, err
	}
	return m.mapEvent(ev)
}

func (m *streamMapper) checkSequence(ev *remoteStreamEvent) error {
	if ev.SequenceNumber == nil {
		return nil
	}
	seq := *ev.SequenceNumber
	if m.sawSeq && seq <= m.lastSeq {
		return m.malformed("sequence_number %d is not strictly increasing (previous %d)", seq, m.lastSeq)
	}
	m.lastSeq = seq
	m.sawSeq = true
	return nil
}

func (m *streamMapper) mapEvent(ev *remoteStreamEvent) ([]aipapi.Event, error) {
	switch ev.Type {
	case "response.created", "response.in_progress":
		return m.mapCreated(ev)
	case "response.output_item.added":
		return m.mapOutputItemAdded(ev)
	case "response.content_part.added":
		return m.mapContentPartAdded(ev)
	case "response.output_text.delta":
		return m.mapTextDelta(ev)
	case "response.output_text.done":
		return m.mapTextDone(ev)
	case "response.content_part.done":
		return m.mapContentPartDone(ev)
	case "response.function_call_arguments.delta":
		return m.mapToolArgsDelta(ev)
	case "response.function_call_arguments.done":
		return m.mapToolArgsDone(ev)
	case "response.reasoning_text.delta":
		return m.mapReasoningDelta(ev)
	case "response.reasoning_text.done":
		return m.mapReasoningDone(ev)
	case "response.output_item.done":
		return m.mapOutputItemDone(ev)
	case "response.error", "error":
		return m.mapErrorEvent(ev)
	case "response.failed":
		return m.mapFailed(ev)
	case "response.completed":
		return m.mapTerminal(ev, "completed")
	case "response.incomplete":
		return m.mapTerminal(ev, "incomplete")
	default:
		if isPrefixedWireType(ev.Type) {
			// Valid vendor-prefixed extension event: accepted, preserved as
			// bounded private evidence, and never surfaced on the canonical
			// stream (extended events MUST NOT alter core stream semantics).
			m.native.ExtensionTypes = append(m.native.ExtensionTypes, ev.Type)
			return nil, nil
		}
		return nil, m.malformed("unknown unprefixed SSE event type %q", ev.Type)
	}
}

// emit validates one produced canonical event against the canonical envelope
// bounds before it reaches the stream.
func (m *streamMapper) emit(ev *aipapi.Event) error {
	if err := aipapi.ValidateEventEnvelope(ev); err != nil {
		return m.malformed("canonical event envelope: %v", err)
	}
	return nil
}

func (m *streamMapper) mapCreated(ev *remoteStreamEvent) ([]aipapi.Event, error) {
	if m.started {
		return nil, m.malformed("duplicate %s event", ev.Type)
	}
	if ev.Response != nil {
		switch ev.Response.Status {
		case "in_progress", "queued", "":
		default:
			return nil, m.malformed("%s event carries terminal status %q", ev.Type, ev.Response.Status)
		}
		m.native.ResponseID = strings.TrimSpace(ev.Response.ID)
	}
	m.started = true
	evt := aipapi.Event{Kind: aipapi.EventResponseStarted}
	if err := m.emit(&evt); err != nil {
		return nil, err
	}
	return []aipapi.Event{evt}, nil
}

func (m *streamMapper) mapOutputItemAdded(ev *remoteStreamEvent) ([]aipapi.Event, error) {
	if !m.started {
		return nil, m.malformed("%s before response start", ev.Type)
	}
	if ev.Item == nil {
		return nil, m.malformed("%s event is missing the item payload", ev.Type)
	}
	item, err := proto.DecodeItem(*ev.Item, proto.DefaultLimits())
	if err != nil {
		return nil, m.malformed("output item decode: %v", err)
	}
	m.native.addItemID(item.ID)
	if m.activeItemType != "" {
		return nil, m.malformed("%s while output item %q is still open", ev.Type, m.activeItemType)
	}
	switch item.Kind {
	case aipapi.ItemKindMessage:
		if err := m.bumpItemCount(); err != nil {
			return nil, err
		}
		m.activeItemType = "message"
		m.activeItemID = item.ID
		m.sawMsg = true
		evt := aipapi.Event{Kind: aipapi.EventMessageStarted}
		if err := m.emit(&evt); err != nil {
			return nil, err
		}
		return []aipapi.Event{evt}, nil
	case aipapi.ItemKindToolCall:
		if item.ToolCall == nil {
			return nil, m.malformed("function_call output item is missing its payload")
		}
		if strings.TrimSpace(item.ToolCall.CallID) == "" {
			return nil, m.malformed("function_call output item is missing call_id")
		}
		if strings.TrimSpace(item.ToolCall.Name) == "" {
			return nil, m.malformed("function_call output item is missing its name")
		}
		if err := m.bumpItemCount(); err != nil {
			return nil, err
		}
		// The canonical content-class contract (aipapi.ValidateEventSequence)
		// requires an open message frame before tool-call events, exactly as the
		// OpenAI Responses stream mapper opens one for its tool calls. A real
		// OpenResponses provider emits function_call output items without a
		// surrounding message item, so the mapper synthesizes the boundary.
		var out []aipapi.Event
		if !m.sawMsg {
			opened, err := m.ensureMessageOpen()
			if err != nil {
				return nil, err
			}
			out = append(out, opened...)
		}
		m.activeItemType = "function_call"
		m.activeItemID = item.ID
		m.activeCallID = item.ToolCall.CallID
		evt := aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: item.ToolCall.CallID, ToolName: item.ToolCall.Name}
		if err := m.emit(&evt); err != nil {
			return nil, err
		}
		return append(out, evt), nil
	case aipapi.ItemKindReasoning:
		if err := m.bumpItemCount(); err != nil {
			return nil, err
		}
		m.activeItemType = "reasoning"
		m.activeItemID = item.ID
		if item.Reasoning != nil && item.Reasoning.Reasoning != nil && item.Reasoning.Reasoning.Text != "" {
			// Preserve reasoning text carried inline on the added item so a
			// non-streaming-shaped reasoning item is never silently dropped.
			m.reasoningBytes += len(item.Reasoning.Reasoning.Text)
			if m.reasoningBytes > m.limits.MaxReasoningBytes {
				return nil, m.limitError("response_reasoning", m.reasoningBytes)
			}
			var out []aipapi.Event
			if !m.sawMsg {
				opened, err := m.ensureMessageOpen()
				if err != nil {
					return nil, err
				}
				out = append(out, opened...)
			}
			evt := aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: item.Reasoning.Reasoning.Text}
			if err := m.emit(&evt); err != nil {
				return nil, err
			}
			return append(out, evt), nil
		}
		return nil, nil
	case aipapi.ItemKindItemReference:
		// Provider-native item references are private attempt evidence and are
		// never forwarded onto the canonical stream.
		return nil, nil
	case aipapi.ItemKindExtension:
		if item.Extension != nil {
			m.native.ExtensionTypes = append(m.native.ExtensionTypes, item.Extension.Type)
		}
		return nil, nil
	default:
		return nil, m.malformed("output item type %q is not representable in the canonical stream", item.Kind)
	}
}

func (m *streamMapper) bumpItemCount() error {
	m.itemCount++
	if m.itemCount > m.limits.MaxItems {
		return m.limitError("response_items", m.itemCount)
	}
	return nil
}

// ensureMessageOpen opens the canonical message frame exactly once per stream so
// content-class events (tool calls, reasoning) satisfy the canonical sequence
// contract even when the provider emits them without a surrounding message item.
func (m *streamMapper) ensureMessageOpen() ([]aipapi.Event, error) {
	if m.sawMsg {
		return nil, nil
	}
	m.sawMsg = true
	evt := aipapi.Event{Kind: aipapi.EventMessageStarted}
	if err := m.emit(&evt); err != nil {
		return nil, err
	}
	return []aipapi.Event{evt}, nil
}

func (m *streamMapper) mapContentPartAdded(ev *remoteStreamEvent) ([]aipapi.Event, error) {
	if !m.started {
		return nil, m.malformed("%s before response start", ev.Type)
	}
	if m.activeItemType != "message" {
		return nil, m.malformed("%s requires an open message output item", ev.Type)
	}
	if m.contentPartOpen {
		return nil, m.malformed("%s while a content part is already open", ev.Type)
	}
	partType := "output_text"
	if ev.Part != nil && ev.Part.Type != "" {
		partType = ev.Part.Type
	}
	if partType != "output_text" {
		return nil, m.malformed("content part type %q is not representable in the canonical stream", partType)
	}
	m.contentPartOpen = true
	m.textPartOpen = true
	return nil, nil
}

func (m *streamMapper) mapTextDelta(ev *remoteStreamEvent) ([]aipapi.Event, error) {
	if !m.started {
		return nil, m.malformed("%s before response start", ev.Type)
	}
	if m.activeItemType != "message" || !m.contentPartOpen || !m.textPartOpen {
		return nil, m.malformed("output_text.delta requires an open text content part on a message item")
	}
	m.textBytes += len(ev.Delta)
	if m.textBytes > m.limits.MaxTextBytes {
		return nil, m.limitError("response_text", m.textBytes)
	}
	evt := aipapi.Event{Kind: aipapi.EventTextDelta, Delta: ev.Delta}
	if err := m.emit(&evt); err != nil {
		return nil, err
	}
	return []aipapi.Event{evt}, nil
}

func (m *streamMapper) mapTextDone(ev *remoteStreamEvent) ([]aipapi.Event, error) {
	if !m.started {
		return nil, m.malformed("%s before response start", ev.Type)
	}
	if m.activeItemType != "message" || !m.contentPartOpen || !m.textPartOpen {
		return nil, m.malformed("output_text.done without an open text content part on a message item")
	}
	m.textPartOpen = false
	return nil, nil
}

func (m *streamMapper) mapContentPartDone(ev *remoteStreamEvent) ([]aipapi.Event, error) {
	if !m.started {
		return nil, m.malformed("%s before response start", ev.Type)
	}
	if m.activeItemType != "message" || !m.contentPartOpen {
		return nil, m.malformed("content_part.done without an open content part on a message item")
	}
	m.contentPartOpen = false
	return nil, nil
}

func (m *streamMapper) mapToolArgsDelta(ev *remoteStreamEvent) ([]aipapi.Event, error) {
	if !m.started {
		return nil, m.malformed("%s before response start", ev.Type)
	}
	if m.activeItemType != "function_call" {
		return nil, m.malformed("function_call_arguments.delta requires an open function_call item")
	}
	if ev.CallID != "" && ev.CallID != m.activeCallID {
		return nil, m.malformed("function_call_arguments.delta call_id %q does not match active call %q", ev.CallID, m.activeCallID)
	}
	m.argBytes += len(ev.Delta)
	if m.textBytes+m.argBytes > m.limits.MaxTextBytes {
		return nil, m.limitError("response_text", m.textBytes+m.argBytes)
	}
	evt := aipapi.Event{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: m.activeCallID, Delta: ev.Delta}
	if err := m.emit(&evt); err != nil {
		return nil, err
	}
	return []aipapi.Event{evt}, nil
}

func (m *streamMapper) mapToolArgsDone(ev *remoteStreamEvent) ([]aipapi.Event, error) {
	if !m.started {
		return nil, m.malformed("%s before response start", ev.Type)
	}
	if m.activeItemType != "function_call" {
		return nil, m.malformed("function_call_arguments.done without an open function_call item")
	}
	return nil, nil
}

func (m *streamMapper) mapReasoningDelta(ev *remoteStreamEvent) ([]aipapi.Event, error) {
	if !m.started {
		return nil, m.malformed("%s before response start", ev.Type)
	}
	if m.activeItemType != "reasoning" {
		return nil, m.malformed("reasoning_text.delta requires an open reasoning item")
	}
	m.reasoningBytes += len(ev.Delta)
	if m.reasoningBytes > m.limits.MaxReasoningBytes {
		return nil, m.limitError("response_reasoning", m.reasoningBytes)
	}
	var out []aipapi.Event
	if !m.sawMsg {
		opened, err := m.ensureMessageOpen()
		if err != nil {
			return nil, err
		}
		out = append(out, opened...)
	}
	evt := aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: ev.Delta}
	if err := m.emit(&evt); err != nil {
		return nil, err
	}
	return append(out, evt), nil
}

func (m *streamMapper) mapReasoningDone(ev *remoteStreamEvent) ([]aipapi.Event, error) {
	if !m.started {
		return nil, m.malformed("%s before response start", ev.Type)
	}
	if m.activeItemType != "reasoning" {
		return nil, m.malformed("reasoning_text.done without an open reasoning item")
	}
	return nil, nil
}

func (m *streamMapper) mapOutputItemDone(ev *remoteStreamEvent) ([]aipapi.Event, error) {
	if !m.started {
		return nil, m.malformed("%s before response start", ev.Type)
	}
	if m.activeItemType == "" {
		return nil, m.malformed("output_item.done without an open output item")
	}
	defer m.closeItem()
	switch m.activeItemType {
	case "function_call":
		evt := aipapi.Event{Kind: aipapi.EventToolCallFinished, ToolCallID: m.activeCallID}
		if err := m.emit(&evt); err != nil {
			return nil, err
		}
		return []aipapi.Event{evt}, nil
	default:
		return nil, nil
	}
}

func (m *streamMapper) closeItem() {
	m.activeItemType = ""
	m.activeItemID = ""
	m.activeCallID = ""
	m.contentPartOpen = false
	m.textPartOpen = false
}

func (m *streamMapper) mapErrorEvent(ev *remoteStreamEvent) ([]aipapi.Event, error) {
	if !m.started {
		return nil, m.malformed("%s before response start", ev.Type)
	}
	details, err := m.parseErrorPayload(ev.Error)
	if err != nil {
		return nil, err
	}
	if details == nil {
		// The pinned profile error event carries code/param/message as top-level
		// fields on the event body.
		details = &proto.WireErrorDetails{
			Type:    ev.Code,
			Code:    ev.Code,
			Param:   ev.Param,
			Message: ev.Message,
		}
	}
	m.pendingError = details
	return nil, nil
}

func (m *streamMapper) mapFailed(ev *remoteStreamEvent) ([]aipapi.Event, error) {
	details := m.pendingError
	m.pendingError = nil
	if details == nil && ev.Response != nil {
		d, err := m.parseErrorPayload(ev.Response.Error)
		if err != nil {
			return nil, err
		}
		details = d
	}
	code, msg := "upstream_error", ""
	if details != nil {
		if details.Code != "" {
			code = details.Code
		} else if details.Type != "" {
			code = details.Type
		}
		msg = details.Message
	}
	evt := aipapi.Event{Kind: aipapi.EventError, ErrorCode: sanitizeErrorCode(code), ErrorMessage: sanitizeErrorMessage(msg)}
	if !m.started {
		// A provider may send response.failed as its first SSE record. There
		// is no response.started event to normalize yet, so expose the
		// bounded canonical error only to the stream opener, which converts
		// it into a recoverable pre-output failure before committing.
		return []aipapi.Event{evt}, nil
	}
	if err := m.emit(&evt); err != nil {
		return nil, err
	}
	m.terminal = true
	m.closeItem()
	return []aipapi.Event{evt}, nil
}

func (m *streamMapper) mapTerminal(ev *remoteStreamEvent, status string) ([]aipapi.Event, error) {
	if !m.started {
		return nil, m.malformed("response.%s before response start", status)
	}
	events := make([]aipapi.Event, 0, 2)
	if ev.Response != nil && (ev.Response.UsagePresent || usagePresent(ev.Response.Usage)) {
		if id := strings.TrimSpace(ev.Response.ID); id != "" {
			m.native.ResponseID = id
		}
		u := usageEventWithContext(ev.Response.Usage, ev.Response.ID, ev.Response.ServiceTier)
		if err := m.emit(&u); err != nil {
			return nil, err
		}
		events = append(events, u)
	}
	finishReason := ""
	if status == "incomplete" {
		if ev.Response != nil {
			finishReason = incompleteFinishReason(ev.Response.IncompleteDetails)
		} else {
			finishReason = "length"
		}
	}
	fin := aipapi.Event{Kind: aipapi.EventResponseFinished, ResponseStatus: status, FinishReason: finishReason}
	if err := m.emit(&fin); err != nil {
		return nil, err
	}
	events = append(events, fin)
	m.terminal = true
	m.closeItem()
	return events, nil
}

func (m *streamMapper) parseErrorPayload(raw json.RawMessage) (*proto.WireErrorDetails, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil, nil
	}
	var details proto.WireErrorDetails
	if err := json.Unmarshal(trimmed, &details); err != nil {
		return nil, m.malformed("malformed error payload: %v", err)
	}
	return &details, nil
}

func (m *streamMapper) malformed(format string, args ...any) error {
	return fmt.Errorf("%s: %w: %s", m.id, ErrMalformedResponse, fmt.Sprintf(format, args...))
}

func (m *streamMapper) limitError(param string, actual int) error {
	limit := 0
	switch param {
	case "response_items":
		limit = m.limits.MaxItems
	case "response_text":
		limit = m.limits.MaxTextBytes
	case "response_reasoning":
		limit = m.limits.MaxReasoningBytes
	}
	return fmt.Errorf("%s: %w", m.id, limitError(param, actual, limit))
}
