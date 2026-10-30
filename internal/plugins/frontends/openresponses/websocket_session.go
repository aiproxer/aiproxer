package openresponses

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
	"github.com/gorilla/websocket"
)

const (
	// wsWriteWait bounds writes to the socket (close frames, pings, error envelopes).
	wsWriteWait = 10 * time.Second
	// wsReadTerminationWait bounds the handoff window between peer-close
	// signaling and publication of the read-pump result.
	wsReadTerminationWait = 10 * time.Millisecond
	// wsTransportReadLimitPadding leaves room for Gorilla to receive an
	// oversized frame and let application decoding produce a graceful 413.
	wsTransportReadLimitPadding int64 = 4096
)

var (
	errWSAgeLimit  = errors.New("openresponses: websocket connection age limit reached")
	errWSIdleClose = errors.New("openresponses: websocket peer idle")
)

const (
	wsTerminationActive uint32 = iota
	wsTerminationAge
	wsTerminationPeer
	wsTerminationShutdown
)

// wsWireErrorEnvelope is the pinned WebSocket error envelope shape.
type wsWireErrorEnvelope struct {
	Type   string          `json:"type"`
	Status int             `json:"status"`
	Error  wsWireErrorBody `json:"error"`
}

type wsWireErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Param   string `json:"param,omitempty"`
}

// WSSession is the bounded connection shell established by an authenticated
// handshake. It owns the socket, its limits, its deadlines, and closes it exactly
// once. Run drives one read pump and one pinger goroutine; both are owned and
// joined before Run returns, so no goroutine escapes the session.
type WSSession struct {
	conn      *websocket.Conn
	bounds    wsBounds
	counters  *WSCounters
	auth      sdkauth.Decision
	origin    string
	startedAt time.Time

	// byteBudget gates the total turn payload buffered in the session queue
	// (finding 4). It is created with the session, owned by Run, and closed
	// before Run joins the pumps.
	byteBudget *wsByteBudget

	// localStore and localScope are the connection-local store:false continuation
	// state allocated only after authorization. They are cleared when the session
	// closes, so reconnects never see a previous connection's records.
	localStore aipcont.Store
	localScope aipcont.Scope

	// peerClosedCh is closed exactly once when a pump observes a fatal transport
	// error (dead peer or failed control write). In-flight turn execution
	// observes it through PeerClosed so a disconnected client cancels a blocked
	// downstream stream promptly instead of hanging until the age limit.
	peerClosedCh   chan struct{}
	termination    atomic.Uint32
	peerClosedOnce sync.Once

	// writeText is the effective data-frame writer: the socket writer, optionally
	// wrapped by the handler's WriteTextWrapper seam (tests). A nil wrapper keeps
	// the plain socket writer.
	writeText func([]byte) error

	closeOnce sync.Once
	closeErr  error
}

func newWSSession(conn *websocket.Conn, b wsBounds, counters *WSCounters, auth sdkauth.Decision, origin string, wrap func(func([]byte) error) func([]byte) error) *WSSession {
	next := func(data []byte) error {
		_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
		err := conn.WriteMessage(websocket.TextMessage, data)
		_ = conn.SetWriteDeadline(time.Time{})
		return err
	}
	if wrap != nil {
		next = wrap(next)
	}
	return &WSSession{
		conn:         conn,
		bounds:       b,
		counters:     counters,
		auth:         auth,
		origin:       origin,
		startedAt:    time.Now(),
		peerClosedCh: make(chan struct{}),
		byteBudget:   newWSByteBudget(b.maxQueuedBytes),
		writeText:    next,
	}
}

// PeerClosed returns a channel that is closed when the peer or transport fails.
// Turn runners select on it (directly or through a derived context) to propagate
// disconnects into in-flight executor reads.
func (s *WSSession) PeerClosed() <-chan struct{} {
	return s.peerClosedCh
}

func (s *WSSession) markPeerClosed() {
	s.peerClosedOnce.Do(func() {
		// The terminal arbiter gives peer closure a single linearization point.
		// If age already claimed termination, the already-selected age result
		// remains authoritative; otherwise peer closure wins classification.
		s.termination.CompareAndSwap(wsTerminationActive, wsTerminationPeer)
		close(s.peerClosedCh)
	})
}

func (s *WSSession) claimAgeTermination() bool {
	return s.termination.CompareAndSwap(wsTerminationActive, wsTerminationAge)
}

func (s *WSSession) claimShutdown() bool {
	return s.termination.CompareAndSwap(wsTerminationActive, wsTerminationShutdown)
}

// Auth returns the authenticated decision scoped to this session.
func (s *WSSession) Auth() sdkauth.Decision { return s.auth }

// Origin returns the normalized request Origin, or "" when the client sent none.
func (s *WSSession) Origin() string { return s.origin }

// StartedAt returns the session start time.
func (s *WSSession) StartedAt() time.Time { return s.startedAt }

// LocalStore returns the connection-local continuation store, or nil when local
// continuation is disabled for this session.
func (s *WSSession) LocalStore() aipcont.Store { return s.localStore }

// ContinuationScope returns the authoritative connection scope isolating this
// session's local continuation records.
func (s *WSSession) ContinuationScope() aipcont.Scope { return s.localScope }

// SetLocalContinuation attaches the connection-scoped continuation store. It is
// called once, after a successful upgrade and authorization.
func (s *WSSession) SetLocalContinuation(store aipcont.Store, scope aipcont.Scope) {
	s.localStore = store
	s.localScope = scope
}

// WriteText writes one bounded text frame to the client. Data writes are owned
// by the session pump goroutine (the request handler goroutine); control frames
// (pings, pongs, close) are safe from any goroutine.
func (s *WSSession) WriteText(data []byte) error {
	if s.writeText == nil {
		return errors.New("openresponses: session data writer is unavailable")
	}
	return s.writeText(data)
}

// WriteJSON writes one bounded JSON text frame to the client. See WriteText for
// the single-data-writer ownership invariant.
func (s *WSSession) WriteJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.WriteText(data)
}

type sessionPumpResult struct {
	err      error
	fromRead bool
}

// Run starts the bounded session pumps and blocks until the session terminates:
// on context cancellation, connection-age expiry, peer close/error, or a runner
// error. It owns both pump goroutines and joins them before returning. The read
// pump forwards text messages to the bounded queue; the pinger emits periodic
// pings so a silent-but-live peer is kept alive and a dead peer is detected
// within the idle window.
func (s *WSSession) Run(ctx context.Context, runner WSSessionRunner) error {
	queueCap := max(s.bounds.maxQueuedTurns, 1)
	messageCh := make(chan []byte, queueCap)
	doneCh := make(chan sessionPumpResult, 2)
	stop := make(chan struct{})
	budget := s.byteBudget

	wg := &sync.WaitGroup{}
	wg.Add(2)
	go s.readPump(ctx, messageCh, doneCh, stop, budget, wg)
	go s.pinger(ctx, doneCh, stop, wg)

	// The connection-age deadline must also cancel an in-flight turn: Run is
	// blocked inside HandleMessage while the turn executes, so the age case
	// alone cannot terminate a session whose downstream turn is blocked and
	// whose peer stays connected. The AfterFunc callback is the sole owner of
	// the age result; its completion channel lets cleanup join it without
	// concurrently draining a timer channel.
	ageDur := time.Until(s.startedAt.Add(s.bounds.maxAge))
	turnCtx, cancelTurn := context.WithCancel(ctx)
	ageExpired := make(chan struct{})
	ageTimerDone := make(chan struct{})
	ageTimer := time.AfterFunc(ageDur, func() {
		close(ageExpired)
		cancelTurn()
		close(ageTimerDone)
	})

	peerWatcherDone := make(chan struct{})
	go func() {
		defer close(peerWatcherDone)
		select {
		case <-s.PeerClosed():
			cancelTurn()
		case <-turnCtx.Done():
		}
	}()
	shutdownWatcherStop := make(chan struct{})
	shutdownWatcherDone := make(chan struct{})
	go func() {
		defer close(shutdownWatcherDone)
		select {
		case <-ctx.Done():
			s.claimShutdown()
		case <-shutdownWatcherStop:
		}
	}()
	defer func() {
		cancelTurn()
		if !ageTimer.Stop() {
			<-ageTimerDone
		}
		close(shutdownWatcherStop)
		<-peerWatcherDone
		<-shutdownWatcherDone
	}()
	// Run owns pump teardown, including panic paths from the turn runner. The
	// HTTP handler's Close defer is a second idempotent safety net, not the owner
	// of joining these goroutines.
	defer func() {
		budget.close()
		_ = s.close()
		close(stop)
		wg.Wait()
	}()

	var result error
loop:
	for {
		select {
		case <-ctx.Done():
			s.claimShutdown()
			result = ctx.Err()
			break loop
		case <-ageExpired:
			// Shutdown wins over age. Otherwise age must claim the terminal
			// classification atomically; a peer that claimed it first wins,
			// without a timing-based check-then-poll window.
			if ctx.Err() != nil {
				s.claimShutdown()
				result = ctx.Err()
				break loop
			}
			if !s.claimAgeTermination() {
				if s.termination.Load() == wsTerminationShutdown {
					result = ctx.Err()
				} else if term, ok := s.peerTermination(doneCh); ok {
					if term.fromRead && isReadTimeout(term.err) && s.counters != nil {
						s.counters.idleClosed.Add(1)
					}
					result = term.err
				} else {
					result = errWSIdleClose
				}
				break loop
			}
			if s.counters != nil {
				s.counters.ageExpired.Add(1)
			}
			_ = s.sendLimitReachedError()
			result = errWSAgeLimit
			break loop
		case r := <-doneCh:
			for {
				select {
				case msg := <-messageCh:
					if runner != nil {
						if err := runner.HandleMessage(turnCtx, s, msg); err != nil {
							result = err
							break loop
						}
					}
					budget.release(int64(len(msg)))
				default:
					goto donePump
				}
			}
		donePump:
			if r.fromRead && isReadTimeout(r.err) {
				if s.counters != nil {
					s.counters.idleClosed.Add(1)
				}
			}
			result = r.err
			break loop
		case msg := <-messageCh:
			if runner != nil {
				if err := runner.HandleMessage(turnCtx, s, msg); err != nil {
					// A peer-idle read timeout may have fired while the turn was
					// in flight; it takes precedence so the session classifies
					// the closure as an idle close (finding 5).
					if term, ok := pollReadTermination(doneCh, s.PeerClosed()); ok {
						if term.fromRead && isReadTimeout(term.err) {
							if s.counters != nil {
								s.counters.idleClosed.Add(1)
							}
						}
						result = term.err
						break loop
					}
					ageExpiredWhileTurn := false
					select {
					case <-ageExpired:
						ageExpiredWhileTurn = true
					default:
					}
					if ageExpiredWhileTurn && ctx.Err() == nil {
						if !s.claimAgeTermination() {
							if s.termination.Load() == wsTerminationShutdown {
								result = ctx.Err()
							} else if term, ok := s.peerTermination(doneCh); ok {
								if term.fromRead && isReadTimeout(term.err) && s.counters != nil {
									s.counters.idleClosed.Add(1)
								}
								result = term.err
							} else {
								result = errWSIdleClose
							}
						} else {
							// The connection age limit expired while a turn was in
							// flight; classify it as the bounded age termination and
							// emit the limit-reached envelope before the close.
							if s.counters != nil {
								s.counters.ageExpired.Add(1)
							}
							_ = s.sendLimitReachedError()
							result = errWSAgeLimit
						}
					} else {
						result = err
					}
					break loop
				}
			}
			budget.release(int64(len(msg)))
		}
	}

	return result
}

// peerTermination returns a peer-associated pump result without using a timing
// guess to arbitrate against age. markPeerClosed claims the peer terminal state
// before closing the notification channel, so the atomic termination state is
// the causal source of truth. The bounded poll remains only for collecting the
// read-pump error after that state has already been published.
func (s *WSSession) peerTermination(doneCh <-chan sessionPumpResult) (sessionPumpResult, bool) {
	if s.termination.Load() != wsTerminationPeer {
		return sessionPumpResult{}, false
	}
	if term, ok := pollReadTermination(doneCh, s.PeerClosed()); ok {
		return term, true
	}
	return sessionPumpResult{err: errWSIdleClose, fromRead: true}, true
}

// pollReadTermination drains any pump terminations that have already arrived
// while the runner was executing a turn, returning the first one and preferring
// a peer-idle read timeout so the session classifies such a close as idle.
func pollReadTermination(doneCh <-chan sessionPumpResult, peerClosed <-chan struct{}) (sessionPumpResult, bool) {
	var first sessionPumpResult
	found := false
	deadline := time.NewTimer(wsReadTerminationWait)
	defer deadline.Stop()

	for {
		select {
		case r := <-doneCh:
			if r.fromRead && isReadTimeout(r.err) {
				return r, true
			}
			if found {
				return first, true
			}
			first = r
			found = true
		case <-peerClosed:
			// readPump signals peerClosed before publishing its result. Disable
			// this case after observing it and continue waiting for the result.
			peerClosed = nil
		case <-deadline.C:
			if found {
				return first, true
			}
			return sessionPumpResult{}, false
		}
	}
}

// readPump is the bounded reader. It enforces the per-message read limit and the
// idle window via read deadlines refreshed by pong/ping activity, and forwards
// text messages to the bounded queue. Each forwarded message first reserves its
// byte size in the session's byte budget, so a burst of large turns cannot make
// the session buffer more than maxQueuedBytes of queued payload (finding 4).
func (s *WSSession) readPump(ctx context.Context, messageCh chan<- []byte, doneCh chan<- sessionPumpResult, stop <-chan struct{}, budget *wsByteBudget, wg *sync.WaitGroup) {
	defer wg.Done()
	conn := s.conn
	readLimit := s.bounds.maxMessageBytes
	if readLimit <= (1<<63-1)-wsTransportReadLimitPadding {
		readLimit += wsTransportReadLimitPadding
	}
	conn.SetReadLimit(readLimit)
	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(s.bounds.idleTimeout))
		return nil
	})
	conn.SetPingHandler(func(appData string) error {
		_ = conn.SetReadDeadline(time.Now().Add(s.bounds.idleTimeout))
		return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(wsWriteWait))
	})

	for {
		if err := conn.SetReadDeadline(time.Now().Add(s.bounds.idleTimeout)); err != nil {
			s.markPeerClosed()
			doneCh <- sessionPumpResult{err: err, fromRead: true}
			return
		}
		mt, data, err := conn.ReadMessage()
		if err != nil {
			s.markPeerClosed()
			doneCh <- sessionPumpResult{err: err, fromRead: true}
			return
		}
		if mt != websocket.TextMessage {
			continue
		}
		size := int64(len(data))
		if !budget.reserve(size) {
			return
		}
		select {
		case messageCh <- data:
		case <-ctx.Done():
			budget.release(size)
			doneCh <- sessionPumpResult{err: ctx.Err(), fromRead: true}
			return
		case <-stop:
			budget.release(size)
			return
		}
	}
}

// pinger emits periodic pings so liveness is observable while the reader is idle.
func (s *WSSession) pinger(ctx context.Context, doneCh chan<- sessionPumpResult, stop <-chan struct{}, wg *sync.WaitGroup) {
	defer wg.Done()
	period := max(s.bounds.idleTimeout*9/10, time.Millisecond)
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := s.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(wsWriteWait)); err != nil {
				s.markPeerClosed()
				doneCh <- sessionPumpResult{err: err}
				return
			}
		case <-ctx.Done():
			doneCh <- sessionPumpResult{err: ctx.Err()}
			return
		case <-stop:
			return
		}
	}
}

// Close closes the socket exactly once, sending a normal close frame first.
func (s *WSSession) Close() error {
	return s.close()
}

func (s *WSSession) close() error {
	s.closeOnce.Do(func() {
		_ = s.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(wsWriteWait))
		s.closeErr = s.conn.Close()
		if s.localStore != nil {
			if closer, ok := s.localStore.(interface{ Close() error }); ok {
				_ = closer.Close()
			}
		}
	})
	return s.closeErr
}

func (s *WSSession) sendLimitReachedError() error {
	env := wsWireErrorEnvelope{
		Type:   "error",
		Status: http.StatusBadRequest,
		Error: wsWireErrorBody{
			Code:    "websocket_connection_limit_reached",
			Message: "WebSocket connection age limit reached; open a new connection",
			Param:   "connection_age",
		},
	}
	return s.WriteJSON(env)
}

func isReadTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
