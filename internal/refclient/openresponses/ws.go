package openresponses

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// readPollInterval bounds how long a WS read may block before cancellation is
// re-checked when the server stalls.
const readPollInterval = 300 * time.Millisecond

// WSDialOptions configures a WebSocket session.
type WSDialOptions struct {
	// BaseURL is the http(s) origin; /responses is appended and the scheme is
	// switched to ws(s).
	BaseURL string
	APIKey  string
	// Dialer, when set, supplies the gorilla dialer. The dialer is copied per
	// call so its configuration is never mutated by Dial.
	Dialer *websocket.Dialer
	// ParseOptions bounds frame parsing.
	ParseOptions ParseOptions
}

// TurnResult is the parsed outcome of one WebSocket turn.
type TurnResult struct {
	Events    []Event
	Response  *ResponseResource
	ErrorCode string
	Error     *ErrorObject
	RawText   []string
}

// WSSession is a persistent sequential OpenResponses WebSocket connection.
type WSSession struct {
	conn    *websocket.Conn
	opts    ParseOptions
	turnSeq int
	mu      sync.Mutex
	closed  bool
}

// Dial opens an authenticated OpenResponses WebSocket session.
func Dial(ctx context.Context, opts WSDialOptions) (*WSSession, error) {
	if err := validBaseURL(opts.BaseURL); err != nil {
		return nil, fmt.Errorf("refclient/openresponses: ws dial: %w", err)
	}
	u, err := url.Parse(strings.TrimRight(opts.BaseURL, "/") + "/responses")
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	default:
		return nil, fmt.Errorf("unsupported ws scheme %q", u.Scheme)
	}

	// Copy the dialer so the handshake timeout is never written on a shared
	// dialer (websocket.DefaultDialer or a caller-supplied one), which would
	// race when multiple sessions dial concurrently.
	dialer := websocket.DefaultDialer
	if opts.Dialer != nil {
		dialer = opts.Dialer
	}
	copied := *dialer
	copied.HandshakeTimeout = 15 * time.Second
	dialer = &copied

	header := http.Header{}
	if opts.APIKey != "" {
		header.Set("Authorization", "Bearer "+opts.APIKey)
	}
	header.Set("Content-Type", "application/json")

	conn, _, err := dialer.DialContext(ctx, u.String(), header)
	if err != nil {
		return nil, fmt.Errorf("refclient/openresponses: ws dial: %w", err)
	}
	po := opts.ParseOptions.normalize()
	conn.SetReadLimit(int64(po.MaxEventBytes))
	return &WSSession{conn: conn, opts: po}, nil
}

// marshalCreateEnvelope wraps create params in the response.create turn envelope.
func marshalCreateEnvelope(params CreateParams) ([]byte, error) {
	inner, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(inner, &m); err != nil {
		return nil, err
	}
	m["type"] = json.RawMessage(`"response.create"`)
	return json.Marshal(m)
}

// Turn sends one response.create message and reads events until the terminal
// response event, a structured error, or a literal [DONE] frame. Turns are serialized.
func (s *WSSession) Turn(ctx context.Context, params CreateParams) (*TurnResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, fmt.Errorf("refclient/openresponses: ws session closed")
	}

	env, err := marshalCreateEnvelope(params)
	if err != nil {
		return nil, err
	}
	if err := s.conn.WriteMessage(websocket.TextMessage, env); err != nil {
		return nil, fmt.Errorf("refclient/openresponses: ws write: %w", err)
	}
	s.turnSeq++

	turn := &TurnResult{}
	for {
		if err := ctx.Err(); err != nil {
			return turn, err
		}
		// Poll reads so a stalled server cannot block cancellation.
		_ = s.conn.SetReadDeadline(time.Now().Add(readPollInterval))
		mt, msg, err := s.conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return turn, ctx.Err()
			}
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			return turn, fmt.Errorf("refclient/openresponses: ws read: %w", err)
		}
		text := string(msg)
		turn.RawText = append(turn.RawText, text)

		if mt == websocket.TextMessage || mt == websocket.BinaryMessage {
			trimmed := strings.TrimSpace(text)
			if trimmed == "[DONE]" {
				break
			}
			if len(msg) > s.opts.MaxEventBytes {
				return turn, limitf("ws frame exceeds %d bytes", s.opts.MaxEventBytes)
			}
			evt, err := ParseEvent(msg, s.opts)
			if err != nil {
				return turn, err
			}
			turn.Events = append(turn.Events, *evt)
			if evt.IsError() {
				turn.Error = evt.Error
				if evt.Error != nil {
					turn.ErrorCode = evt.Error.Code
				}
				break
			}
			if evt.IsTerminal() {
				turn.Response = evt.Response
				break
			}
		}
	}
	return turn, nil
}

// Close terminates the WebSocket session.
func (s *WSSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	_ = s.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(5*time.Second))
	return s.conn.Close()
}
