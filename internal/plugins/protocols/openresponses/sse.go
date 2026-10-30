package openresponses

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// StreamEvent represents an OpenResponses event on the wire (SSE/WebSocket).
type StreamEvent struct {
	Type           string                `json:"type"`
	SequenceNumber int                   `json:"sequence_number"`
	Response       *WireResponseResource `json:"response,omitempty"`
	OutputIndex    *int                  `json:"output_index,omitempty"`
	ContentIndex   *int                  `json:"content_index,omitempty"`
	Item           *WireItem             `json:"item,omitempty"`
	Part           *WireContentPart      `json:"part,omitempty"`
	ItemID         string                `json:"item_id,omitempty"`
	CallID         string                `json:"call_id,omitempty"`
	Delta          string                `json:"delta,omitempty"`
	Text           string                `json:"text,omitempty"`
	Refusal        string                `json:"refusal,omitempty"`
	Summary        string                `json:"summary,omitempty"`
	Arguments      string                `json:"arguments,omitempty"`
	Signature      string                `json:"signature,omitempty"`
	Opaque         json.RawMessage       `json:"opaque,omitempty"`
	Error          json.RawMessage       `json:"error,omitempty"`
}

// FormatSSEEvent formats a StreamEvent into SSE wire bytes:
// event: <Type>\n
// data: <JSON>\n\n
func FormatSSEEvent(evt StreamEvent) ([]byte, error) {
	if evt.Type == "" || strings.ContainsAny(evt.Type, "\r\n") {
		return nil, fmt.Errorf("%w: invalid or unsafe event type", ErrEncodeFailed)
	}

	dataBytes, err := json.Marshal(evt)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to marshal stream event: %v", ErrEncodeFailed, err)
	}
	return frameSSEEvent(evt.Type, dataBytes), nil
}

// frameSSEEvent builds one SSE frame ("event: <type>\\ndata: <payload>\\n\\n")
// from already-marshaled payload bytes. SSE data is line-oriented: every
// physical payload line must carry its own data: prefix.
func frameSSEEvent(eventType string, dataBytes []byte) []byte {
	// The common case needs no rewrite: build the frame directly instead of
	// copying the payload through a string and fmt.Sprintf.
	const headerPrefix = "event: "
	const dataPrefix = "\ndata: "
	const terminator = "\n\n"
	if bytes.IndexByte(dataBytes, '\n') < 0 && bytes.IndexByte(dataBytes, '\r') < 0 {
		out := make([]byte, 0, len(headerPrefix)+len(eventType)+len(dataPrefix)+len(dataBytes)+len(terminator))
		out = append(out, headerPrefix...)
		out = append(out, eventType...)
		out = append(out, dataPrefix...)
		out = append(out, dataBytes...)
		out = append(out, terminator...)
		return out
	}
	normalized := bytes.ReplaceAll(dataBytes, []byte("\r\n"), []byte("\n"))
	normalized = bytes.ReplaceAll(normalized, []byte("\r"), []byte("\n"))
	lines := bytes.Split(normalized, []byte("\n"))
	out := make([]byte, 0, len(headerPrefix)+len(eventType)+len(dataPrefix)+len(dataBytes)+len(terminator)+(len(lines)-1)*len(dataPrefix))
	out = append(out, headerPrefix...)
	out = append(out, eventType...)
	for i, line := range lines {
		if i == 0 {
			out = append(out, dataPrefix...)
		} else {
			out = append(out, "\ndata: "...)
		}
		out = append(out, line...)
	}
	out = append(out, terminator...)
	return out
}

// FormattedDONE returns the literal terminal SSE payload:
// data: [DONE]\n\n
func FormattedDONE() []byte {
	return []byte("data: [DONE]\n\n")
}

// SSEWriter formats and streams OpenResponses events over an io.Writer.
type SSEWriter struct {
	writer          io.Writer
	terminalEmitted bool
	doneEmitted     bool
}

// NewSSEWriter constructs an SSEWriter wrapping w.
func NewSSEWriter(w io.Writer) *SSEWriter {
	return &SSEWriter{
		writer: w,
	}
}

// WriteEvent formats and writes a single StreamEvent.
func (s *SSEWriter) WriteEvent(evt StreamEvent) error {
	if s.doneEmitted || s.terminalEmitted {
		return &SequenceError{
			Code:     "output_after_terminal",
			Event:    evt.Type,
			Sequence: evt.SequenceNumber,
			Message:  "cannot write event after terminal event or DONE",
			Err:      ErrOutputAfterTerminal,
		}
	}

	b, err := FormatSSEEvent(evt)
	if err != nil {
		return err
	}

	if _, err := s.writer.Write(b); err != nil {
		return err
	}
	if f, ok := s.writer.(http.Flusher); ok {
		f.Flush()
	}

	if isTerminalEventType(evt.Type) {
		s.terminalEmitted = true
	}
	return nil
}

// WriteDONE writes the terminal data: [DONE]\n\n chunk.
func (s *SSEWriter) WriteDONE() error {
	if s.doneEmitted {
		return &SequenceError{
			Code:    "duplicate_terminal",
			Message: "DONE already emitted",
			Err:     ErrDuplicateTerminal,
		}
	}

	if !s.terminalEmitted {
		return &SequenceError{
			Code:    "done_before_terminal",
			Message: "cannot write DONE before terminal response event",
			Err:     ErrSequenceViolation,
		}
	}

	if _, err := s.writer.Write(FormattedDONE()); err != nil {
		return err
	}
	if f, ok := s.writer.(http.Flusher); ok {
		f.Flush()
	}
	s.doneEmitted = true
	return nil
}

func isTerminalEventType(t string) bool {
	switch t {
	case "response.completed", "response.failed", "response.incomplete":
		return true
	default:
		return false
	}
}

// StreamErrorToRawMessage converts a aipapi.StreamError into raw JSON bytes for event.error payloads.
func StreamErrorToRawMessage(err *aipapi.StreamError) json.RawMessage {
	if err == nil {
		return json.RawMessage("null")
	}
	m := map[string]string{
		"code":    err.Code,
		"message": sanitizeErrorMessage(err.Message),
	}
	b, _ := json.Marshal(m)
	return b
}
