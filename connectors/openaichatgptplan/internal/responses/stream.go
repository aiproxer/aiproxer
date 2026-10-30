package responses

// The raw Responses SSE stream.
//
// The provider's answer is a server-sent-events body. This file owns the
// framing: it turns the bytes into the events the provider sent, in the
// order the provider sent them, and it bounds the bytes one event and the
// whole answer may cost the connector.
//
// # Raw, not canonical
//
// An Event is the provider's event name and its data payload, byte for byte.
// The canonical mapping (text/tool/reasoning/usage/terminal events) belongs
// to the normalizer (spec task 5.2); this file does not invent an event, drop
// one, or rewrite a payload.
//
// # The bounds are acquisition bounds
//
// The per-event and whole-stream budgets are enforced WHILE reading, not
// after: the stream stops at the bound and the unread remainder is closed
// with the body. A refused answer is never read to the end first.
//
// # Cancellation
//
// The stream is bound to the context of the request that opened it. The
// context cancels the underlying read, a cancelled stream returns the
// context's error, and the body is closed to release the connection.

import (
	"bufio"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"strings"
	"sync"
)

const (
	// maxStreamEventBytes bounds one framed SSE event.
	maxStreamEventBytes = 1 << 20

	// maxStreamBytes bounds the whole SSE answer.
	maxStreamBytes = 8 << 20

	// sseReadBufferBytes is the read-ahead of the line reader. It is small
	// so that the overshoot past a per-event bound is a small constant.
	sseReadBufferBytes = 4096

	// defaultSSEEventName is the event type of an event that carries no
	// event: field. It is the SSE default, not an invented event.
	defaultSSEEventName = "message"
)

// The failures of the raw SSE stream.
var (
	// ErrStreamTooLarge reports an answer that exceeds its whole-stream
	// bound.
	ErrStreamTooLarge = errors.New("responses: the provider stream exceeds its bound")

	// ErrStreamEventTooLarge reports one event that exceeds its bound.
	ErrStreamEventTooLarge = errors.New("responses: one provider stream event exceeds its bound")

	// ErrStreamUnreadable reports a stream body that could not be read.
	// Cancellation is reported as the context's own error instead. The
	// failure is CONSTANT and value-free: the underlying read error is
	// provider-controlled text (a malformed chunk trailer can even repeat the
	// request's Authorization header), so the cause is deliberately not
	// wrapped or retained. A diagnostic belongs in separately sanitized,
	// token-redacted and bounded data, never in this error.
	ErrStreamUnreadable = errors.New("responses: the provider stream could not be read")

	// ErrStreamClosed reports a stream the caller has already closed.
	ErrStreamClosed = errors.New("responses: the provider stream is closed")
)

// Event is one raw server-sent event.
//
// Name is the event: field, or the SSE default "message" when the provider
// sent none. Data is the joined data: payload, byte for byte as the provider
// sent it. The slice is owned by the caller after Next returns.
type Event struct {
	// Name is the provider's event name.
	Name string
	// Data is the provider's data payload.
	Data []byte
}

// Stream is the raw SSE stream of one provider answer.
//
// Next must not be called concurrently with itself. Close may be called at
// any time; it releases the body and makes a blocked read fail.
type Stream struct {
	ctx        context.Context
	body       io.ReadCloser
	reader     *bufio.Reader
	profileID  string
	generation [sha256.Size]byte
	requestID  string
	// requestIdentity is the full correlation identity, before the diagnostic
	// display bound.
	requestIdentity string
	sanitize        func(string, int) string

	mu       sync.Mutex
	closed   bool
	terminal error
}

// answerMetadata is the transport-owned, non-secret provenance of one answer.
//
// RequestID is the provider's correlation id after redaction and bounding.
// RequestIdentity is the SAME correlation id before the display bound, so an
// accounting consumer can identify the charge without inheriting a truncation
// that merges two answers. Sanitize is the redact-before-bound path for provider
// text that arrives after the response headers; it closes over the credential
// that was SENT so a mid-stream diagnostic can be redacted exactly like a
// transport diagnostic. It is never rendered, and the Stream exposes no way to
// read it.
type answerMetadata struct {
	requestID       string
	requestIdentity string
	sanitize        func(text string, limit int) string
}

// newStream returns the raw stream of one provider answer.
func newStream(ctx context.Context, body io.ReadCloser, profileID string, generation [sha256.Size]byte) *Stream {
	return newStreamWithMetadata(ctx, body, profileID, generation, answerMetadata{})
}

// newStreamWithMetadata returns the raw stream with its answer envelope.
func newStreamWithMetadata(ctx context.Context, body io.ReadCloser, profileID string, generation [sha256.Size]byte, metadata answerMetadata) *Stream {
	bounded := &boundedBody{reader: body, remaining: maxStreamBytes}
	return &Stream{
		ctx:             ctx,
		body:            body,
		reader:          bufio.NewReaderSize(bounded, sseReadBufferBytes),
		profileID:       profileID,
		generation:      generation,
		requestID:       metadata.requestID,
		requestIdentity: metadata.requestIdentity,
		sanitize:        metadata.sanitize,
	}
}

// boundedBody caps the bytes one answer may cost the connector.
//
// The cap is maxStreamBytes plus the ONE byte that detects an excess, so an
// oversize answer is DETECTED instead of silently truncated. The reader never
// requests more than that, so the bound is an acquisition bound.
type boundedBody struct {
	reader    io.Reader
	remaining int64
}

// Read reads at most the remaining budget plus the detecting byte.
func (b *boundedBody) Read(p []byte) (int, error) {
	if b.remaining < 0 {
		return 0, ErrStreamTooLarge
	}
	limit := b.remaining + 1
	if int64(len(p)) > limit {
		p = p[:limit]
	}
	n, err := b.reader.Read(p)
	b.remaining -= int64(n)
	if b.remaining < 0 {
		return n, ErrStreamTooLarge
	}
	return n, err
}

// ProfileID is the local, non-secret id of the registration that authorized
// the request.
func (s *Stream) ProfileID() string {
	if s == nil {
		return ""
	}
	return s.profileID
}

// CredentialGeneration is the one-way fingerprint of the credential pair the
// request presented. It is provenance, not a copy of the credential.
func (s *Stream) CredentialGeneration() [sha256.Size]byte {
	if s == nil {
		return [sha256.Size]byte{}
	}
	return s.generation
}

// RequestID is the provider request id of this answer, or empty when the
// provider sent none.
//
// It is captured on the SUCCESSFUL event-stream path and sanitized before it is
// stored: it is correlation metadata, never a credential and never raw provider
// text. Spec task 5.1 left this capture open (follow-up S2); carrying it on the
// transport-owned answer envelope lets a mid-stream failure be correlated with
// the provider.
func (s *Stream) RequestID() string {
	if s == nil {
		return ""
	}
	return s.requestID
}

// RequestIdentity is the provider correlation identity of this answer, or empty
// when the provider sent none.
//
// It is the SAME correlation id as RequestID, taken before the diagnostic length
// bound: RequestID is shortened for display, and a shortened id would make two
// distinct answers indistinguishable to an accounting consumer. It is opaque to
// that consumer - never rendered, never logged - and it is not a credential: the
// redaction that protects the diagnostic form runs first.
func (s *Stream) RequestIdentity() string {
	if s == nil {
		return ""
	}
	return s.requestIdentity
}

// sanitizeText is the redact-before-bound path for provider text this answer
// carries after its headers.
//
// The transport binds the credential at open time; a stream that was built
// without an envelope (a framing unit test, for example) still strips control
// characters and bounds the text, it just has no credential to redact.
func (s *Stream) sanitizeText(text string, limit int) string {
	if s == nil || s.sanitize == nil {
		return safeProviderText(text, limit)
	}
	return s.sanitize(text, limit)
}

// Next returns the next raw event, or io.EOF at the end of the answer.
//
// A read failure, an oversize event and a cancelled context are terminal:
// the body is released and every later call reports the same failure. A
// cancelled context is reported as the context's own error and the two size
// bounds as their sentinels; every other read failure is reported as the
// constant ErrStreamUnreadable, because the underlying error is untrusted
// provider text and may carry the credential.
func (s *Stream) Next() (Event, error) {
	if s == nil {
		return Event{}, ErrStreamClosed
	}
	s.mu.Lock()
	terminal := s.terminal
	closed := s.closed
	s.mu.Unlock()
	if terminal != nil {
		return Event{}, terminal
	}
	if closed {
		return Event{}, ErrStreamClosed
	}
	ctx := s.ctx
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			s.finish(err)
			return Event{}, err
		}
	}

	event, err := s.readEvent()
	if err == nil {
		return event, nil
	}
	if errors.Is(err, io.EOF) {
		s.finish(io.EOF)
		return Event{}, io.EOF
	}
	if ctx != nil && ctx.Err() != nil {
		s.finish(ctx.Err())
		return Event{}, ctx.Err()
	}
	s.mu.Lock()
	closed = s.closed
	s.mu.Unlock()
	if closed {
		return Event{}, ErrStreamClosed
	}
	// The two size sentinels are this package's own value-free failures, so
	// they stay classified. Every other read failure is provider-controlled
	// text: net/http reports a malformed chunk trailer verbatim, and a trailer
	// can echo the request headers. Replace it with the constant sentinel and
	// do not retain the cause, so a caller that logs the terminal error cannot
	// disclose the credential or an unbounded diagnostic.
	if !errors.Is(err, ErrStreamEventTooLarge) && !errors.Is(err, ErrStreamTooLarge) {
		s.finish(ErrStreamUnreadable)
		return Event{}, ErrStreamUnreadable
	}
	s.finish(err)
	return Event{}, err
}

// Close releases the body. It is idempotent and may be called while Next is
// blocked on a read.
func (s *Stream) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	body := s.body
	s.mu.Unlock()
	if body == nil {
		return nil
	}
	return body.Close()
}

// finish records a terminal outcome and releases the body.
func (s *Stream) finish(err error) {
	s.mu.Lock()
	if s.terminal == nil {
		s.terminal = err
	}
	alreadyClosed := s.closed
	s.closed = true
	body := s.body
	s.mu.Unlock()
	if !alreadyClosed && body != nil {
		_ = body.Close()
	}
}

// readEvent reads one framed event.
//
// A blank line dispatches the frame when it carried a data: field, including
// an empty payload. A frame with no data field (comments, keep-alives,
// id/retry and unknown fields) is not an event: its name and byte accounting
// are discarded so a suppressed frame cannot name, extend or exhaust the next
// one. A line without a colon names a field with an empty value, so a bare
// "data" is an empty data field rather than an ignored line.
//
// A final event whose terminating blank line was lost is still delivered when
// its last line was complete and it carried a NON-EMPTY payload: its bytes
// arrived, and discarding them would turn a complete terminal event into an
// absent one. A line cut off mid-way is discarded, because its payload is
// incomplete. An empty final frame is deliberately not salvaged: extending the
// leniency to empty data is a terminal-acceptance decision (spec task 5.2),
// not part of frame dispatch.
func (s *Stream) readEvent() (Event, error) {
	var name string
	var data []byte
	sawData := false
	raw := 0
	for {
		line, consumed, err := s.readLine(maxStreamEventBytes - raw)
		if err != nil {
			if errors.Is(err, io.EOF) {
				if consumed == 0 && len(data) > 0 {
					return finishEvent(name, data), nil
				}
				return Event{}, io.EOF
			}
			return Event{}, err
		}
		raw += consumed
		if len(line) == 0 {
			if sawData {
				return finishEvent(name, data), nil
			}
			// Reset every frame-local fact on the blank separator. The byte
			// budget is per FRAME: a data-less keep-alive must not exhaust the
			// next event's; the whole-stream bound is the separate cap.
			name = ""
			data = nil
			sawData = false
			raw = 0
			continue
		}
		if line[0] == ':' {
			continue
		}
		field, value, _ := strings.Cut(string(line), ":")
		// Cut returns the whole line as the field and an empty value when
		// there is no colon: a line names a field with an empty value.
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			name = value
		case "data":
			if sawData {
				data = append(data, '\n')
			}
			data = append(data, value...)
			sawData = true
		}
	}
}

// finishEvent returns one dispatched event with the SSE default name.
func finishEvent(name string, data []byte) Event {
	if name == "" {
		name = defaultSSEEventName
	}
	return Event{Name: name, Data: data}
}

// readLine reads one SSE line within the remaining event budget.
//
// The consumed count includes the terminator. LF, CRLF and CR are accepted.
// A line that cannot be completed within the budget is refused at
// acquisition instead of being read to its end.
func (s *Stream) readLine(budget int) ([]byte, int, error) {
	if budget <= 0 {
		return nil, 0, ErrStreamEventTooLarge
	}
	line := make([]byte, 0, 64)
	consumed := 0
	for consumed < budget {
		b, err := s.reader.ReadByte()
		if err != nil {
			return line, consumed, err
		}
		consumed++
		switch b {
		case '\n':
			return line, consumed, nil
		case '\r':
			next, nextErr := s.reader.ReadByte()
			if nextErr == nil {
				if next == '\n' {
					consumed++
				} else {
					_ = s.reader.UnreadByte()
				}
			} else if !errors.Is(nextErr, io.EOF) {
				return line, consumed, nextErr
			}
			if consumed > budget {
				return nil, consumed, ErrStreamEventTooLarge
			}
			return line, consumed, nil
		default:
			line = append(line, b)
		}
	}
	return nil, consumed, ErrStreamEventTooLarge
}
