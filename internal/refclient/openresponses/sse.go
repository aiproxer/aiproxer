package openresponses

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
)

// SSEParser incrementally parses an OpenResponses `text/event-stream` body.
// It validates event/type matching, exactly one terminal event followed by the
// literal [DONE], bounded line lengths, and bounded event counts.
type SSEParser struct {
	sc           *bufio.Scanner
	opts         ParseOptions
	eventName    string
	data         strings.Builder
	terminalSeen bool
	doneSeen     bool
	count        int
	started      bool
}

// NewSSEParser constructs an SSE parser over r.
func NewSSEParser(r io.Reader, opts ParseOptions) *SSEParser {
	opts = opts.normalize()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), opts.MaxLineBytes)
	return &SSEParser{sc: sc, opts: opts}
}

// Next returns the next semantic event. It returns ErrSSEDone after the [DONE]
// terminal and io.EOF only when a [DONE] already preceded the end of input.
func (p *SSEParser) Next() (*Event, error) {
	for {
		if !p.sc.Scan() {
			if err := p.sc.Err(); err != nil {
				return nil, malformedf("sse read error: %v", err)
			}
			// Flush a pending event on EOF (lenient trailing newline).
			if p.data.Len() > 0 {
				return p.dispatch()
			}
			if !p.doneSeen {
				return nil, &ParseError{Category: "sequence", Message: "missing [DONE] terminal", Err: ErrSequence}
			}
			return nil, io.EOF
		}
		line := p.sc.Bytes()
		if len(line) > p.opts.MaxLineBytes {
			return nil, limitf("sse line exceeds %d bytes", p.opts.MaxLineBytes)
		}
		switch {
		case len(line) == 0:
			// Blank line dispatches the accumulated event.
			if p.data.Len() == 0 {
				continue
			}
			return p.dispatch()
		case bytes.HasPrefix(line, []byte("event:")):
			name := strings.TrimSpace(string(bytes.TrimPrefix(line, []byte("event:"))))
			p.eventName = name
		case bytes.HasPrefix(line, []byte("data:")):
			payload := bytes.TrimPrefix(line, []byte("data:"))
			if len(payload) > 0 && payload[0] == ' ' {
				payload = payload[1:]
			}
			if p.data.Len() > 0 {
				p.data.WriteByte('\n')
			}
			p.data.Write(payload)
		case bytes.HasPrefix(line, []byte(":")):
			// Comment line; ignored.
		case bytes.HasPrefix(line, []byte("id:")):
			// Servers SHOULD NOT use id; ignored.
		default:
			return nil, malformedf("unexpected sse field %q", truncate(line))
		}
	}
}

func (p *SSEParser) dispatch() (*Event, error) {
	p.count++
	if p.count > p.opts.MaxEvents {
		return nil, limitf("event count %d exceeds %d", p.count, p.opts.MaxEvents)
	}
	raw := strings.TrimSpace(p.data.String())
	p.data.Reset()
	name := p.eventName
	p.eventName = ""

	if raw == "[DONE]" {
		p.doneSeen = true
		if !p.terminalSeen {
			return nil, &ParseError{Category: "sequence", Message: "[DONE] before terminal response event", Err: ErrSequence}
		}
		return nil, ErrSSEDone
	}

	if p.doneSeen {
		return nil, &ParseError{Category: "sequence", Message: "output after [DONE]", Err: ErrSequence}
	}
	if p.terminalSeen {
		return nil, &ParseError{Category: "sequence", Message: "output after terminal response event", Err: ErrSequence}
	}
	if name == "" {
		return nil, malformedf("event header missing for data payload")
	}

	evt, err := ParseEvent([]byte(raw), p.opts)
	if err != nil {
		return nil, err
	}
	if name != evt.Type {
		return nil, &ParseError{
			Category: "event_mismatch",
			Message:  "event header " + name + " does not match type " + evt.Type,
			Err:      ErrEventMismatch,
		}
	}
	if evt.IsTerminal() {
		if p.terminalSeen {
			return nil, &ParseError{Category: "sequence", Message: "duplicate terminal event", Err: ErrSequence}
		}
		p.terminalSeen = true
	}
	return evt, nil
}

// Done reports whether [DONE] was consumed.
func (p *SSEParser) Done() bool { return p.doneSeen }

// ParseSSE parses a complete SSE body and returns the events plus whether [DONE] was seen.
func ParseSSE(data []byte, opts ParseOptions) ([]Event, bool, error) {
	p := NewSSEParser(bytes.NewReader(data), opts)
	var events []Event
	for {
		evt, err := p.Next()
		if errors.Is(err, ErrSSEDone) {
			return events, true, nil
		}
		if err != nil {
			return events, p.doneSeen, err
		}
		events = append(events, *evt)
	}
}
