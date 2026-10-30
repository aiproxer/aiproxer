package openresponses

// Task 6.5 in-package robustness tests that need internals the black-box
// stress suite cannot reach: a real gorilla connection pair over an in-memory
// pipe (so tests drive a session without a TCP listener) and a virtual one-hour
// connection age (so the maximum-allowed age path is exercised without waiting).

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"

	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	"github.com/gorilla/websocket"
)

// pipeHijacker implements http.Hijacker over an in-memory pipe so the real
// gorilla Upgrader handshake completes without a TCP listener.
type pipeHijacker struct {
	conn net.Conn
}

func (h *pipeHijacker) Header() http.Header       { return make(http.Header) }
func (h *pipeHijacker) Write([]byte) (int, error) { return 0, nil }
func (h *pipeHijacker) WriteHeader(int)           {}

func (h *pipeHijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return h.conn, bufio.NewReadWriter(bufio.NewReader(h.conn), bufio.NewWriter(h.conn)), nil
}

// pipeWSPair establishes a real gorilla WebSocket connection pair over an
// in-memory net.Pipe. The server side reads the client's actual handshake
// request from the pipe (so the Sec-WebSocket-Accept matches) and completes it
// through the production Upgrader path.
func pipeWSPair(t *testing.T, upgrader *websocket.Upgrader) (server *websocket.Conn, client *websocket.Conn) {
	t.Helper()
	srvConn, cliConn := net.Pipe()
	type upResult struct {
		c   *websocket.Conn
		err error
	}
	upCh := make(chan upResult, 1)
	go func() {
		br := bufio.NewReader(srvConn)
		req, err := http.ReadRequest(br)
		if err != nil {
			upCh <- upResult{err: err}
			return
		}
		c, err := upgrader.Upgrade(&pipeHijacker{conn: srvConn}, req, nil)
		upCh <- upResult{c: c, err: err}
	}()

	//nolint:staticcheck // NewClient accepts the preconnected net.Conn used by this pipe test.
	client, _, err := websocket.NewClient(cliConn, &url.URL{Scheme: "ws", Host: "pipe.test"}, nil, 1024, 1024)
	if err != nil {
		t.Fatalf("client handshake: %v", err)
	}
	res := <-upCh
	if res.err != nil {
		t.Fatalf("server upgrade: %v", res.err)
	}
	return res.c, client
}

func TestWebSocketStress_VirtualOneHourAgeTerminatesBackdatedSession(t *testing.T) {
	upgrader := &websocket.Upgrader{
		HandshakeTimeout: 5 * time.Second,
		CheckOrigin:      func(*http.Request) bool { return true },
	}
	serverConn, clientConn := pipeWSPair(t, upgrader)
	defer func() { _ = clientConn.Close() }()

	counters := &WSCounters{}
	session := newWSSession(serverConn, wsBounds{
		maxAge:          MaxAllowedWSConnectionAgeDur, // the maximum allowed 1h
		idleTimeout:     5 * time.Minute,
		maxMessageBytes: wsDefaultMaxMessageBytes,
		maxQueuedTurns:  1,
		maxQueuedBytes:  DefaultMaxQueuedBytes,
	}, counters, sdkauth.Decision{}, "", nil)
	// Virtual elapsed time: the session believes it started one hour ago, so a
	// maximum-allowed one-hour age has already expired at Run start.
	session.startedAt = time.Now().Add(-MaxAllowedWSConnectionAgeDur)

	done := make(chan error, 1)
	go func() { done <- session.Run(context.Background(), nil) }()

	_ = clientConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, data, err := clientConn.ReadMessage()
	if err != nil {
		t.Fatalf("client read: %v", err)
	}
	var env wsWireErrorEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("server wrote a non-envelope frame: %q", data)
	}
	if env.Error.Code != "websocket_connection_limit_reached" {
		t.Fatalf("envelope code=%q, want websocket_connection_limit_reached", env.Error.Code)
	}

	// Drain the server's close frame so the session's socket close over the
	// unbuffered pipe completes before Run returns.
	_ = clientConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		if _, _, err := clientConn.ReadMessage(); err != nil {
			break
		}
	}

	select {
	case err := <-done:
		if err != errWSAgeLimit {
			t.Fatalf("Run returned %v, want errWSAgeLimit", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session did not terminate after a virtual one-hour age")
	}
	snap := counters.Snapshot()
	if snap.AgeExpired != 1 {
		t.Fatalf("counters after virtual age: %+v (want AgeExpired=1)", snap)
	}
}
