package openresponses

// Phase 6 review repair tests that need internals: a real gorilla connection
// pair over an in-memory pipe plus direct access to the session's byte budget
// and counters.
//   - finding 4: the per-session queued-byte bound is enforced by the byte
//     budget; buffered turn payload never exceeds maxQueuedBytes, a full budget
//     exerts read-side backpressure, and close() releases blocked producers;
//   - finding 5: an idle-timeout read deadline that fires during an active turn
//     increments IdleClosed and classifies the session close as idle.

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	"github.com/gorilla/websocket"
)

// tcpWSPair establishes a real gorilla WebSocket pair over a loopback TCP
// socket. Unlike net.Pipe, the OS buffers control frames, so a silent client
// that never pongs lets the server read deadline fire naturally without blocking
// the server pinger.
func tcpWSPair(t *testing.T, upgrader *websocket.Upgrader) (server *websocket.Conn, client *websocket.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("tcp listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	type upResult struct {
		c   *websocket.Conn
		err error
	}
	upCh := make(chan upResult, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			upCh <- upResult{err: err}
			return
		}
		br := bufio.NewReader(conn)
		req, err := http.ReadRequest(br)
		if err != nil {
			upCh <- upResult{err: err}
			return
		}
		c, err := upgrader.Upgrade(&pipeHijacker{conn: conn}, req, nil)
		upCh <- upResult{c: c, err: err}
	}()
	cliConn, _, err := websocket.DefaultDialer.Dial("ws://"+ln.Addr().String()+"/", nil)
	if err != nil {
		t.Fatalf("client dial: %v", err)
	}
	res := <-upCh
	if res.err != nil {
		t.Fatalf("server upgrade: %v", res.err)
	}
	return res.c, cliConn
}

// blockingRunner blocks each dispatched turn until release or the session
// cancels. It mirrors production turn execution, which observes PeerClosed so a
// peer-idle read timeout can cancel an in-flight downstream stream.
type blockingRunner struct {
	release <-chan struct{}
	mu      sync.Mutex
	started bool
	calls   int
}

func (r *blockingRunner) HandleMessage(ctx context.Context, s *WSSession, data []byte) error {
	r.mu.Lock()
	r.started = true
	r.calls++
	r.mu.Unlock()
	select {
	case <-r.release:
	case <-s.PeerClosed():
		return errors.New("peer closed during turn")
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

func (r *blockingRunner) hasStarted() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.started
}

func (r *blockingRunner) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func eventuallyInternal(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

// TestWebSocketByteBudget_RespectsBoundAndReleasesProducer covers the byte
// budget in isolation: it blocks a reserve when the bound is reached, releases
// on drain, and broadcasts on close so no producer is retained.
func TestWebSocketByteBudget_RespectsBoundAndReleasesProducer(t *testing.T) {
	b := newWSByteBudget(1000)
	if !b.reserve(600) {
		t.Fatal("first reserve should succeed")
	}
	if !b.reserve(400) {
		t.Fatal("second reserve should succeed up to the bound")
	}
	if got := b.buffered(); got != 1000 {
		t.Fatalf("buffered=%d, want 1000", got)
	}

	// A third reserve exceeds the bound and must block until a release.
	reserved := make(chan bool, 1)
	go func() {
		reserved <- b.reserve(200)
	}()
	select {
	case ok := <-reserved:
		t.Fatalf("oversized reserve returned %v without a release", ok)
	case <-time.After(50 * time.Millisecond):
	}

	b.release(600)
	select {
	case ok := <-reserved:
		if !ok {
			t.Fatal("reserve failed after a release freed budget")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reserve did not wake after a release")
	}
	if got := b.buffered(); got != 600 {
		t.Fatalf("buffered=%d, want 600", got)
	}

	// Close must unblock a blocked producer.
	blocked := make(chan bool, 1)
	go func() {
		blocked <- b.reserve(1000000)
	}()
	time.Sleep(20 * time.Millisecond)
	b.close()
	select {
	case ok := <-blocked:
		if ok {
			t.Fatal("reserve succeeded on a closed budget")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("close did not unblock a reserved producer")
	}
}

// TestWebSocketSession_QueuedByteBoundHolds exercises the real session over a
// pipe: with maxQueuedBytes smaller than maxQueuedTurns full-size messages, the
// byte budget caps buffered turn payload and the read pump exerts backpressure
// until the active turn drains.
type contextOnlyRunner struct {
	started  chan struct{}
	canceled chan struct{}
	once     sync.Once
}

func (r *contextOnlyRunner) HandleMessage(ctx context.Context, _ *WSSession, _ []byte) error {
	close(r.started)
	<-ctx.Done()
	r.once.Do(func() { close(r.canceled) })
	return ctx.Err()
}

func TestWebSocketSession_PeerClosedCancelsContextOnlyRunner(t *testing.T) {
	upgrader := &websocket.Upgrader{
		HandshakeTimeout: 5 * time.Second,
		CheckOrigin:      func(*http.Request) bool { return true },
	}
	serverConn, clientConn := pipeWSPair(t, upgrader)
	defer func() { _ = clientConn.Close() }()

	runner := &contextOnlyRunner{started: make(chan struct{}), canceled: make(chan struct{})}
	session := newWSSession(serverConn, wsBounds{
		maxAge:          time.Hour,
		idleTimeout:     time.Minute,
		maxMessageBytes: 4096,
		maxQueuedTurns:  1,
		maxQueuedBytes:  4096,
	}, &WSCounters{}, sdkauth.Decision{}, "", nil)
	done := make(chan error, 1)
	go func() { done <- session.Run(context.Background(), runner) }()

	if err := clientConn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create"}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not start")
	}
	if err := clientConn.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("peer closure did not cancel runner context")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("session did not terminate after context cancellation")
	}
}

func TestWebSocketSession_QueuedByteBoundHolds(t *testing.T) {
	upgrader := &websocket.Upgrader{
		HandshakeTimeout: 5 * time.Second,
		CheckOrigin:      func(*http.Request) bool { return true },
	}
	serverConn, clientConn := pipeWSPair(t, upgrader)
	defer func() { _ = clientConn.Close() }()

	counters := &WSCounters{}
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	runner := &blockingRunner{release: release}

	session := newWSSession(serverConn, wsBounds{
		maxAge:          time.Hour,
		idleTimeout:     time.Minute,
		maxMessageBytes: 4096,
		maxQueuedTurns:  4,
		maxQueuedBytes:  8192,
	}, counters, sdkauth.Decision{}, "", nil)

	done := make(chan error, 1)
	go func() { done <- session.Run(context.Background(), runner) }()
	go func() {
		_ = clientConn.SetReadDeadline(time.Now().Add(8 * time.Second))
		for {
			if _, _, err := clientConn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	payload := make([]byte, 4000)
	for i := range payload {
		payload[i] = 'x'
	}

	// Turn 1 blocks; message 1 is consumed by the runner, message 2 fits the
	// byte budget and queues. Both client writes complete.
	if err := clientConn.WriteMessage(websocket.TextMessage, payload); err != nil {
		t.Fatalf("write 1: %v", err)
	}
	eventuallyInternal(t, 3*time.Second, func() bool { return runner.hasStarted() })
	if err := clientConn.WriteMessage(websocket.TextMessage, payload); err != nil {
		t.Fatalf("write 2: %v", err)
	}

	// The third message is fully read but its reserve exceeds the 8192 bound, so
	// the read pump blocks before it can read a fourth message: the fourth
	// client write cannot complete until the active turn drains.
	if err := clientConn.WriteMessage(websocket.TextMessage, payload); err != nil {
		t.Fatalf("write 3: %v", err)
	}
	fourthDone := make(chan error, 1)
	go func() {
		fourthDone <- clientConn.WriteMessage(websocket.TextMessage, payload)
	}()
	select {
	case err := <-fourthDone:
		t.Fatalf("fourth message was not held back by the byte budget (write completed: %v)", err)
	case <-time.After(150 * time.Millisecond):
	}

	if got := session.byteBudget.buffered(); got != 8000 {
		t.Fatalf("buffered=%d, want 8000 (2 x 4000 under an 8192 bound)", got)
	}

	// Releasing the blocked turn drains one message, frees 4000 bytes, and lets
	// the read pump admit the waiting messages in order.
	close(release)
	released = true
	select {
	case err := <-fourthDone:
		if err != nil {
			t.Fatalf("fourth write after drain: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fourth message never admitted after the active turn drained")
	}
	eventuallyInternal(t, 3*time.Second, func() bool { return runner.callCount() == 4 })
	if got := runner.callCount(); got != 4 {
		t.Fatalf("runner calls=%d, want 4", got)
	}
	if got := session.byteBudget.buffered(); got != 0 {
		t.Fatalf("buffered=%d after drain, want 0", got)
	}

	// The queue is drained but the session stays alive (long idle timeout);
	// closing the client terminates it.
	_ = clientConn.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("session Run returned nil after client close, want a transport error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session did not terminate after the client closed")
	}
}

// TestWebSocketSession_IdleDuringActiveTurnClassifiesIdle verifies finding 5: a
// peer that goes idle while a turn is in flight is classified as an idle close
// (IdleClosed incremented, session terminated with the read timeout) rather than
// a generic turn error.
func TestWebSocketSession_PeerTerminationPrecedesAgeClassification(t *testing.T) {
	session := &WSSession{peerClosedCh: make(chan struct{})}
	session.markPeerClosed()

	term, ok := session.peerTermination(make(chan sessionPumpResult))
	if !ok {
		t.Fatal("published peer closure was not observed")
	}
	if term.err != errWSIdleClose {
		t.Fatalf("peer termination error = %v, want %v", term.err, errWSIdleClose)
	}
	if !term.fromRead {
		t.Fatal("peer termination should be treated as a read-side termination")
	}
}

// TestWebSocketSession_IdleDuringActiveTurnClassifiesIdle verifies finding 5: a
// peer that goes idle while a turn is in flight is classified as an idle close
// (IdleClosed incremented, session terminated with the read timeout) rather than
// a generic turn error.
func TestWebSocketSession_RunPeerTerminationWinsOverImmediateAge(t *testing.T) {
	upgrader := &websocket.Upgrader{
		HandshakeTimeout: 5 * time.Second,
		CheckOrigin:      func(*http.Request) bool { return true },
	}
	serverConn, clientConn := pipeWSPair(t, upgrader)
	if err := clientConn.Close(); err != nil {
		t.Fatal(err)
	}

	var frames [][]byte
	session := newWSSession(serverConn, wsBounds{
		maxAge:          -time.Second,
		idleTimeout:     time.Minute,
		maxMessageBytes: 4096,
		maxQueuedTurns:  1,
		maxQueuedBytes:  4096,
	}, &WSCounters{}, sdkauth.Decision{}, "", func(_ func([]byte) error) func([]byte) error {
		return func(data []byte) error {
			frames = append(frames, append([]byte(nil), data...))
			return nil
		}
	})
	// Publish the peer event before Run starts so the terminal arbiter has a
	// deterministic peer winner even though the age timer is immediately due.
	session.markPeerClosed()

	err := session.Run(context.Background(), nil)
	if err == nil {
		t.Fatal("Run returned nil after peer termination")
	}
	if got := session.counters.Snapshot().AgeExpired; got != 0 {
		t.Fatalf("age_expired=%d, want 0 for peer termination", got)
	}
	if len(frames) != 0 {
		t.Fatalf("peer termination emitted %d data frames, want no age envelope", len(frames))
	}
}

func TestWebSocketSession_RunShutdownWinsOverImmediateAge(t *testing.T) {
	upgrader := &websocket.Upgrader{
		HandshakeTimeout: 5 * time.Second,
		CheckOrigin:      func(*http.Request) bool { return true },
	}
	serverConn, clientConn := pipeWSPair(t, upgrader)
	peerResult := make(chan error, 1)
	peerDone := make(chan struct{})
	defer func() {
		_ = clientConn.Close()
		<-peerDone
	}()
	// A connected peer must read the close control frame; an unread net.Pipe
	// otherwise makes shutdown wait for the production write deadline.
	clientConn.SetCloseHandler(func(int, string) error { return nil })
	go func() {
		defer close(peerDone)
		_, _, err := clientConn.ReadMessage()
		peerResult <- err
	}()

	var frames [][]byte
	session := newWSSession(serverConn, wsBounds{
		maxAge:          -time.Second,
		idleTimeout:     time.Minute,
		maxMessageBytes: 4096,
		maxQueuedTurns:  1,
		maxQueuedBytes:  4096,
	}, &WSCounters{}, sdkauth.Decision{}, "", func(_ func([]byte) error) func([]byte) error {
		return func(data []byte) error {
			frames = append(frames, append([]byte(nil), data...))
			return nil
		}
	})
	runCtx, cancel := context.WithCancel(context.Background())
	cancel()

	err := session.Run(runCtx, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	if got := session.counters.Snapshot().AgeExpired; got != 0 {
		t.Fatalf("age_expired=%d, want 0 for shutdown", got)
	}
	if len(frames) != 0 {
		t.Fatalf("shutdown emitted %d data frames, want no age envelope", len(frames))
	}
	select {
	case err := <-peerResult:
		if !websocket.IsCloseError(err, websocket.CloseNormalClosure) {
			t.Fatalf("peer shutdown = %v, want normal close frame", err)
		}
	case <-time.After(time.Second):
		t.Fatal("peer did not observe the normal shutdown close frame")
	}
}

func TestWebSocketSession_IdleDuringActiveTurnClassifiesIdle(t *testing.T) {
	upgrader := &websocket.Upgrader{
		HandshakeTimeout: 5 * time.Second,
		CheckOrigin:      func(*http.Request) bool { return true },
	}
	serverConn, clientConn := tcpWSPair(t, upgrader)
	defer func() { _ = clientConn.Close() }()

	counters := &WSCounters{}
	release := make(chan struct{})
	defer close(release)
	runner := &blockingRunner{release: release}

	session := newWSSession(serverConn, wsBounds{
		maxAge:          time.Hour,
		idleTimeout:     120 * time.Millisecond,
		maxMessageBytes: wsDefaultMaxMessageBytes,
		maxQueuedTurns:  1,
		maxQueuedBytes:  DefaultMaxQueuedBytes,
	}, counters, sdkauth.Decision{}, "", nil)

	done := make(chan error, 1)
	go func() { done <- session.Run(context.Background(), runner) }()

	if err := clientConn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"gpt-4o"}`)); err != nil {
		t.Fatalf("client write: %v", err)
	}
	eventuallyInternal(t, 3*time.Second, func() bool { return runner.hasStarted() })

	// The client stays silent and does not read, so it never pongs: the server's
	// read deadline fires while the turn is in flight. The turn observes the
	// closed peer and the session classifies the close as idle.
	select {
	case err := <-done:
		if !isReadTimeout(err) {
			t.Fatalf("session terminated with %v, want the idle read timeout", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session did not terminate on idle during an active turn")
	}

	// Drain the client side so the server's close write completes cleanly.
	_ = clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		if _, _, err := clientConn.ReadMessage(); err != nil {
			break
		}
	}

	snap := counters.Snapshot()
	if snap.IdleClosed != 1 {
		t.Fatalf("idle_closed=%d, want 1", snap.IdleClosed)
	}
	if snap.AgeExpired != 0 {
		t.Fatalf("age_expired=%d, want 0 (idle, not age, closed the session)", snap.AgeExpired)
	}
}
