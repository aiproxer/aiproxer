// Package ssestream turns a provider's server-sent-events body into a canonical
// event stream for backend connectors.
//
// The stream owns SSE framing: line scanning with a bounded line buffer, an
// optional total byte cap, comment and non-data lines, the "[DONE]" sentinel,
// the pending-event queue, context checks, and idempotent close. A provider
// supplies only a [Decoder] that maps one data payload to canonical events.
package ssestream

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const maxLineBytes = 1024 * 1024

// Decoder maps a provider's SSE payloads to canonical events.
type Decoder struct {
	// Start, when set, runs before the first line is read. Its events are
	// delivered first, even when the body is empty.
	Start func() []aipapi.Event
	// Data maps one non-empty "data:" payload to events. Returning a nil slice
	// and nil error skips the payload; a non-nil error fails Recv.
	Data func(payload []byte) ([]aipapi.Event, error)
	// End, when set, runs exactly once when the stream ends at "[DONE]" or at
	// end of body. Its events are delivered before io.EOF. It does not run when
	// Recv fails with a read, byte-cap, or Data error.
	End func() []aipapi.Event
}

// Options bounds and labels a [Stream].
type Options struct {
	// Name prefixes errors, for example "vertex".
	Name string
	// MaxBytes caps the total body bytes read; zero means no cap.
	MaxBytes int64
	// Project, when set, maps each event as Recv returns it (for example to
	// attach usage evidence).
	Project func(aipapi.Event) aipapi.Event
}

// Stream is a canonical event stream over one SSE body. It is safe for
// concurrent Recv and Close.
type Stream struct {
	mu      sync.Mutex
	body    io.ReadCloser
	sc      *bufio.Scanner
	dec     Decoder
	opts    Options
	read    int64
	pending []aipapi.Event
	begun   bool
	ended   bool
	closed  bool
}

// New returns a stream that reads body and decodes payloads with dec. The
// stream owns body and closes it on [Stream.Close].
func New(body io.ReadCloser, dec Decoder, opts Options) *Stream {
	var r io.Reader = body
	if opts.MaxBytes > 0 {
		// One extra byte lets Recv tell "exactly at the cap" from "over it".
		r = io.LimitReader(body, opts.MaxBytes+1)
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	return &Stream{body: body, sc: sc, dec: dec, opts: opts}
}

// Recv returns the next canonical event, or io.EOF after the stream ended.
func (s *Stream) Recv(ctx context.Context) (aipapi.Event, error) {
	if ctx == nil {
		return aipapi.Event{}, aipapi.ErrNilContext
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return aipapi.Event{}, fmt.Errorf("%s: stream closed", s.opts.Name)
	}
	if !s.begun {
		s.begun = true
		if s.dec.Start != nil {
			s.pending = append(s.pending, s.dec.Start()...)
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			return aipapi.Event{}, err
		}
		if len(s.pending) > 0 {
			ev := s.pending[0]
			s.pending = s.pending[1:]
			if s.opts.Project != nil {
				ev = s.opts.Project(ev)
			}
			return ev, nil
		}
		if s.ended {
			return aipapi.Event{}, io.EOF
		}
		if !s.sc.Scan() {
			if err := s.sc.Err(); err != nil {
				return aipapi.Event{}, fmt.Errorf("%s: sse: %w", s.opts.Name, err)
			}
			s.end()
			continue
		}
		line := s.sc.Text()
		s.read += int64(len(line) + 1)
		if s.opts.MaxBytes > 0 && s.read > s.opts.MaxBytes {
			return aipapi.Event{}, fmt.Errorf("%s: sse exceeded %d bytes", s.opts.Name, s.opts.MaxBytes)
		}
		payload, ok := dataPayload(line)
		if !ok {
			continue
		}
		if payload == "[DONE]" {
			s.end()
			continue
		}
		events, err := s.dec.Data([]byte(payload))
		if err != nil {
			return aipapi.Event{}, err
		}
		s.pending = append(s.pending, events...)
	}
}

// Close releases the body. Later Recv calls fail; Close is idempotent.
func (s *Stream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.body == nil {
		return nil
	}
	return s.body.Close()
}

func (s *Stream) end() {
	if s.ended {
		return
	}
	s.ended = true
	if s.dec.End != nil {
		s.pending = append(s.pending, s.dec.End()...)
	}
}

// dataPayload returns the trimmed payload of a "data:" line. Blank lines,
// comments, and other SSE fields are not data.
func dataPayload(line string) (string, bool) {
	line = strings.TrimSpace(line)
	after, ok := strings.CutPrefix(line, "data:")
	if !ok {
		return "", false
	}
	payload := strings.TrimSpace(after)
	return payload, payload != ""
}
