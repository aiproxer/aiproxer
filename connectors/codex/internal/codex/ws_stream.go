package codex

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/gorilla/websocket"
)

var _ aipapi.ManagedEventStream = (*wsStream)(nil)

type wsStream struct {
	mapper         *codexEventMapper
	mu             sync.Mutex
	conn           *websocket.Conn
	closed         bool
	release        func(closeConn bool)
	releaseOnceF   sync.Once
	accountWindows []AccountWindowSnapshot
}

func newWSStream(conn *websocket.Conn, maxPending int) *wsStream {
	return newWSStreamWithMapper(conn, newCodexEventMapper(maxPending))
}

// newWSStreamWithMapper builds a wsStream over a pre-existing event mapper. The caller
// may have already populated the mapper's pending queue (e.g. from a pre-read first
// frame); the stream's Recv drains pending before reading the next wire frame.
func newWSStreamWithMapper(conn *websocket.Conn, mapper *codexEventMapper) *wsStream {
	return &wsStream{
		mapper: mapper,
		conn:   conn,
	}
}

func (s *wsStream) captureAccountWindowSnapshots(headers http.Header, accountID string, observedAt time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accountWindows = appendBoundedAccountWindowSnapshots(s.accountWindows, accountWindowSnapshots(headers, accountID, observedAt))
}

func (s *wsStream) DrainAccountWindowSnapshots() []AccountWindowSnapshot {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.accountWindows) == 0 {
		return nil
	}
	out := append([]AccountWindowSnapshot(nil), s.accountWindows...)
	s.accountWindows = nil
	return out
}

func (s *wsStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if ctx == nil {
		return aipapi.Event{}, aipapi.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return aipapi.Event{}, err
	}
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return aipapi.Event{}, io.EOF
		}
		if ev, ok := s.mapper.pending.PopFront(); ok {
			s.mu.Unlock()
			return ev, nil
		}
		if s.mapper.terminal {
			s.mu.Unlock()
			s.releaseOnce(false)
			return aipapi.Event{}, io.EOF
		}
		s.mu.Unlock()

		text, ok, err := s.readMessage(ctx)
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed {
				return aipapi.Event{}, io.EOF
			}
			s.releaseOnce(true)
			return aipapi.Event{}, err
		}
		if !ok {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed {
				return aipapi.Event{}, io.EOF
			}
			s.releaseOnce(false)
			return aipapi.Event{}, io.EOF
		}
		if text == "" {
			continue
		}

		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			continue
		}
		if err := s.mapper.handleData(text); err != nil {
			s.mu.Unlock()
			// A malformed frame desyncs the stream; release with a close so the
			// session is evicted even if the caller never calls Close, mirroring the
			// read-error path. releaseOnce is idempotent, so a later Close is safe.
			s.releaseOnce(true)
			return aipapi.Event{}, err
		}
		s.mu.Unlock()
	}
}

func (s *wsStream) readMessage(ctx context.Context) (string, bool, error) {
	stopCancel := context.AfterFunc(ctx, func() {
		_ = s.conn.SetReadDeadline(time.Now())
	})
	defer stopCancel()
	_, data, err := s.conn.ReadMessage()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			_ = s.conn.SetReadDeadline(time.Time{})
			return "", false, ctxErr
		}
		if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
			return "", false, io.EOF
		}
		return "", false, newWSStreamReadError(err)
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return "", true, nil
	}
	return text, true, nil
}

func (s *wsStream) Close() error {
	closeConn := true
	s.mu.Lock()
	if s.mapper.terminal {
		closeConn = false
	}
	s.mu.Unlock()
	if closeConn && s.conn != nil {
		// Close first, without taking s.mu: Recv holds that lock while blocked in
		// ReadMessage, so taking it before closing would deadlock cancellation.
		_ = s.conn.Close()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.releaseOnce(closeConn)
	return nil
}

func (s *wsStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	// Codex WebSocket does not have a request-cancel frame in this adapter. Close
	// the socket instead of pretending cancellation is protocol-level; this also
	// prevents reuse of an in-flight session whose upstream generation may still be
	// producing frames.
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly, Err: s.Close()}
}

func (s *wsStream) releaseOnce(closeConn bool) {
	s.releaseOnceF.Do(func() {
		if s.release != nil {
			s.release(closeConn)
		}
	})
}
