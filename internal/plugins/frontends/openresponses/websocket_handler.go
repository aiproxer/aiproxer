package openresponses

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	proto "github.com/aiproxer/aiproxer/internal/plugins/protocols/openresponses"
	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
	httpauth "github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
	"github.com/gorilla/websocket"
)

const (
	// wsHandshakeTimeout bounds the HTTP-to-WebSocket upgrade handshake.
	wsHandshakeTimeout = 10 * time.Second
	// wsDefaultMaxMessageBytes bounds a single inbound WebSocket message. It mirrors
	// the HTTP request body limit so turn envelopes stay equally bounded.
	wsDefaultMaxMessageBytes = proto.MaxRequestBytes
)

// WSSessionRunner processes the message stream of an established session. The
// sequential-turn runner (SessionRunner, Task 6.2) executes one accepted turn at
// a time on the session pump goroutine; a nil runner keeps the bounded keepalive
// shell alive without turn execution. HandleMessage runs on the session pump
// goroutine (the request handler goroutine). The runner must not start
// goroutines it does not own and join; returning an error terminates the
// session.
type WSSessionRunner interface {
	HandleMessage(ctx context.Context, s *WSSession, data []byte) error
}

// WSCounterSnapshot is a point-in-time view of the WebSocket transport counters.
type WSCounterSnapshot struct {
	SessionsOpened    int64
	SessionsClosed    int64
	AuthRejected      int64
	OriginRejected    int64
	HandshakeRejected int64
	MethodRejected    int64
	AgeExpired        int64
	IdleClosed        int64
}

// WSCounters tracks authenticated-upgrade and session-lifecycle outcomes. A
// counter is incremented only after a decision is made; a rejected attempt never
// allocates session state, so SessionsOpened stays at zero for every failure.
type WSCounters struct {
	sessionsOpened    atomic.Int64
	sessionsClosed    atomic.Int64
	authRejected      atomic.Int64
	originRejected    atomic.Int64
	handshakeRejected atomic.Int64
	methodRejected    atomic.Int64
	ageExpired        atomic.Int64
	idleClosed        atomic.Int64
}

// Snapshot returns a consistent point-in-time view of all counters.
func (c *WSCounters) Snapshot() WSCounterSnapshot {
	return WSCounterSnapshot{
		SessionsOpened:    c.sessionsOpened.Load(),
		SessionsClosed:    c.sessionsClosed.Load(),
		AuthRejected:      c.authRejected.Load(),
		OriginRejected:    c.originRejected.Load(),
		HandshakeRejected: c.handshakeRejected.Load(),
		MethodRejected:    c.methodRejected.Load(),
		AgeExpired:        c.ageExpired.Load(),
		IdleClosed:        c.idleClosed.Load(),
	}
}

// WebSocketHandlerConfig configures the OpenResponses WebSocket upgrade/transport handler.
type WebSocketHandlerConfig struct {
	// Authorizer is evaluated before the upgrade and before any session state is
	// allocated. When nil, the handler requires an authenticated transport context
	// unless AllowUnauthenticated is explicitly set.
	Authorizer            Authorizer
	RequireAuthentication bool
	AllowUnauthenticated  bool
	// Config is the validated frontend configuration supplying the WebSocket policy.
	Config Config
	// Runner processes established sessions (Task 6.2). A nil runner keeps the
	// bounded keepalive shell alive without turn execution.
	Runner WSSessionRunner
	// ShutdownCtx, when non-nil, is a runtime-owned context that cancels when the
	// frontend begins shutdown. Every session observes it and closes exactly once.
	ShutdownCtx context.Context
	// MaxMessageBytes overrides the default per-message bound; zero uses the default.
	MaxMessageBytes int64
	// LocalContinuation configures connection-local store:false continuation.
	// A nil value enables the profile defaults derived from Config.
	LocalContinuation *WSLocalContinuationConfig
	// WriteTextWrapper, when non-nil, wraps the socket data-frame writer for every
	// session this handler establishes. next writes one text frame to the socket.
	// A wrapper may count frames and return an error on the Nth write to force a
	// deterministic writer failure; production leaves it nil.
	WriteTextWrapper func(next func(data []byte) error) func(data []byte) error
}

// WebSocketHandler serves the OpenResponses `GET <base_path>/responses` upgrade.
type WebSocketHandler struct {
	auth                  Authorizer
	requireAuthentication bool
	config                Config
	runner                WSSessionRunner
	shutdown              context.Context
	bounds                wsBounds
	counters              *WSCounters
	localCont             WSLocalContinuationConfig
	writeText             func(func([]byte) error) func([]byte) error
	upgrader              websocket.Upgrader
}

// NewWebSocketHandler creates a bounded, strict WebSocket upgrade handler.
func NewWebSocketHandler(cfg WebSocketHandlerConfig) *WebSocketHandler {
	bounds := wsBoundsFromConfig(cfg.Config.WebSocket)
	if cfg.MaxMessageBytes > 0 {
		bounds.maxMessageBytes = cfg.MaxMessageBytes
	}
	// Keep the application limit representable by the bounded queue. The
	// transport accepts an additional padding window so the decoder can emit a
	// graceful 413, but no accepted frame may exceed the queue ceiling.
	maxMessageBytes := wsMaxMessageBytes()
	if bounds.maxMessageBytes > maxMessageBytes {
		bounds.maxMessageBytes = maxMessageBytes
	}
	if bounds.maxQueuedBytes < wsMinimumQueuedBytes(bounds.maxMessageBytes) {
		bounds.maxQueuedBytes = wsMinimumQueuedBytes(bounds.maxMessageBytes)
	}
	if bounds.maxQueuedBytes > MaxAllowedQueuedBytes {
		bounds.maxQueuedBytes = MaxAllowedQueuedBytes
	}
	localCont := cfg.LocalContinuation
	if localCont == nil {
		derived := DefaultWSLocalContinuation(cfg.Config)
		localCont = &derived
	}
	if !cfg.AllowUnauthenticated {
		cfg.RequireAuthentication = true
	}
	return &WebSocketHandler{
		auth:                  cfg.Authorizer,
		requireAuthentication: cfg.RequireAuthentication,
		config:                cfg.Config,
		runner:                cfg.Runner,
		shutdown:              cfg.ShutdownCtx,
		bounds:                bounds,
		counters:              &WSCounters{},
		localCont:             *localCont,
		writeText:             cfg.WriteTextWrapper,
		upgrader: websocket.Upgrader{
			HandshakeTimeout: wsHandshakeTimeout,
			Error: func(w http.ResponseWriter, _ *http.Request, status int, _ error) {
				// Gorilla's default hook writes plain text. Keep handshake failures
				// on the canonical OpenResponses error wire shape instead.
				if status < 400 {
					status = http.StatusBadRequest
				}
				writeWireError(w, status, "invalid_request_error", "invalid_upgrade", "Invalid WebSocket upgrade request")
			},
			// Origin is enforced as an explicit allowlist policy in ServeHTTP before
			// the upgrade; this mirrors the same policy as a second line of defense.
			CheckOrigin: func(r *http.Request) bool {
				allowed, _ := cfg.Config.WebSocket.originAllowed(strings.TrimSpace(r.Header.Get("Origin")))
				return allowed
			},
		},
	}
}

// Counters returns the transport counters for this handler.
func (h *WebSocketHandler) Counters() *WSCounters {
	return h.counters
}

// buildLocalStore constructs the connection-local continuation store for a scope,
// honoring an injected factory (tests) or the default bounded memory store.
func (h *WebSocketHandler) buildLocalStore(scope aipcont.Scope) aipcont.Store {
	if h.localCont.StoreFactory != nil {
		return h.localCont.StoreFactory(scope)
	}
	return newWSLocalStore(scope, h.localCont.Limits)
}

// ServeHTTP performs strict validation, authentication, and origin checks before
// any upgrade or session-state allocation. Session state is created only after
// auth, origin, and a valid handshake all succeed.
func (h *WebSocketHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if r.Method != http.MethodGet {
		h.counters.methodRejected.Add(1)
		writeWireError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed", "WebSocket upgrades require a GET request")
		return
	}

	var authDecision sdkauth.Decision
	if h.auth != nil {
		meta := sdkauth.InboundCallMeta{
			Frontend:            ID,
			Method:              r.Method,
			Path:                r.URL.Path,
			ClientAddr:          r.RemoteAddr,
			AuthorizationBearer: extractWebSocketBearerToken(r.Header),
		}
		dec, err := h.auth.Authenticate(ctx, meta)
		if err != nil || dec.Outcome != sdkauth.OutcomeAllow {
			h.counters.authRejected.Add(1)
			writeWireError(w, http.StatusUnauthorized, "authentication_error", "unauthorized", "Authentication required")
			return
		}
		authDecision = dec
	} else if h.requireAuthentication {
		if _, ok := httpauth.PrincipalFromContext(ctx); !ok {
			h.counters.authRejected.Add(1)
			writeWireError(w, http.StatusUnauthorized, "authentication_error", "unauthorized", "Authentication required")
			return
		}
	}

	originAllowed, normalizedOrigin := h.config.WebSocket.originAllowed(strings.TrimSpace(r.Header.Get("Origin")))
	if !originAllowed {
		h.counters.originRejected.Add(1)
		writeWireError(w, http.StatusForbidden, "permission_error", "origin_not_allowed", "Request origin is not allowed")
		return
	}

	if err := validateUpgradeRequest(r); err != nil {
		h.counters.handshakeRejected.Add(1)
		writeWireError(w, http.StatusBadRequest, "invalid_request_error", "invalid_upgrade", "Invalid WebSocket upgrade request")
		return
	}

	upgrader := h.upgrader
	if selected := websocketBearerSubprotocol(r.Header); selected != "" {
		// Use a per-request copy: Upgrader carries mutable negotiation fields and
		// the handler is safe for concurrent upgrades.
		upgrader.Subprotocols = []string{selected}
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.counters.handshakeRejected.Add(1)
		return
	}

	// Authentication, origin policy, and handshake validation all passed: only now
	// allocate the bounded session shell and its connection-local continuation state.
	h.counters.sessionsOpened.Add(1)
	session := newWSSession(conn, h.bounds, h.counters, authDecision, normalizedOrigin, h.writeText)
	if h.localCont.Enabled {
		connectionID := newWSConnectionID()
		scope := wsContinuationScope(authDecision, connectionID)
		session.SetLocalContinuation(h.buildLocalStore(scope), scope)
	}

	runCtx := ctx
	if h.shutdown != nil {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithCancel(ctx)
		defer cancel()
		stop := context.AfterFunc(h.shutdown, cancel)
		defer stop()
	}

	// Teardown must run even when a downstream runner/executor panics. Let the
	// enclosing HTTP server recover the panic after the socket and telemetry have
	// been handled exactly once.
	defer func() {
		_ = session.Close()
		h.counters.sessionsClosed.Add(1)
	}()
	_ = session.Run(runCtx, h.runner)
}

// validateUpgradeRequest checks the WebSocket handshake fields before any upgrade.
// Sec-WebSocket-Protocol is intentionally not rejected: browser clients cannot
// set Authorization headers and may carry bearer credentials in a protocol
// token. Unknown protocols are ignored rather than treated as an authorization
// or transport failure.
const wsBearerAuthorizationSubprotocol = "base64url.bearer.authorization.aip"

func websocketSubprotocols(h http.Header) []string {
	var protocols []string
	for _, value := range h.Values("Sec-WebSocket-Protocol") {
		for part := range strings.SplitSeq(value, ",") {
			if token := strings.TrimSpace(part); token != "" {
				protocols = append(protocols, token)
			}
		}
	}
	return protocols
}

// websocketBearerSubprotocol returns the safe marker to negotiate for the
// two-token browser form. The encoded credential is intentionally not selected
// or echoed in the 101 response.
func websocketBearerSubprotocol(h http.Header) string {
	protocols := websocketSubprotocols(h)
	for i, protocol := range protocols {
		if protocol == wsBearerAuthorizationSubprotocol && i+1 < len(protocols) {
			return protocol
		}
	}
	return ""
}

// extractWebSocketBearerToken recognizes the opt-in browser authorization
// subprotocol without treating arbitrary subprotocol names as credentials. The
// supported form is two tokens: marker followed by a base64url-encoded bearer
// token. Invalid encodings are ignored and fall back to the normal
// Authorization header path.
func extractWebSocketBearerToken(h http.Header) string {
	if token := extractBearerToken(h); token != "" {
		return token
	}
	if h == nil {
		return ""
	}
	protocols := websocketSubprotocols(h)
	for i, protocol := range protocols {
		if protocol != wsBearerAuthorizationSubprotocol || i+1 >= len(protocols) {
			continue
		}
		encoded := protocols[i+1]
		decoded, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil || len(decoded) == 0 || strings.ContainsAny(string(decoded), "\r\n\x00") {
			continue
		}
		return string(decoded)
	}
	return ""
}

func validateUpgradeRequest(r *http.Request) error {
	if !headerContainsToken(r.Header, "Connection", "upgrade") {
		return errors.New("missing Connection: Upgrade")
	}
	if !headerContainsToken(r.Header, "Upgrade", "websocket") {
		return errors.New("missing Upgrade: websocket")
	}
	if !strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-WebSocket-Version")), "13") {
		return errors.New("unsupported Sec-WebSocket-Version")
	}
	if strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key")) == "" {
		return errors.New("missing Sec-WebSocket-Key")
	}
	return nil
}

func headerContainsToken(h http.Header, name, token string) bool {
	for _, value := range h.Values(name) {
		for part := range strings.SplitSeq(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}

// originAllowed implements the strict Origin policy from configuration. Browser
// origins are accepted only when explicitly allowlisted; relaxing browser-origin
// behavior therefore requires explicit configuration. Requests without an Origin
// header (non-browser clients) are always accepted.
//
// Development-only relaxation: when both development_mode and allow_any_origin
// are set, any syntactically valid HTTP(S) origin is accepted (an empty
// allowlist then accepts browser origins). The policy never relaxes unless both
// flags are set, so a config that bypasses validation cannot open the policy.
func (c WebSocketConfig) originAllowed(headerOrigin string) (allowed bool, normalized string) {
	raw := strings.TrimSpace(headerOrigin)
	if raw == "" {
		return true, ""
	}
	normalized, err := normalizeOrigin(raw)
	if err != nil {
		return false, ""
	}
	if c.AllowAnyOrigin && c.DevelopmentMode {
		return true, normalized
	}
	if slices.Contains(c.AllowedOrigins, normalized) {
		return true, normalized
	}
	return false, normalized
}

type wsBounds struct {
	maxAge          time.Duration
	idleTimeout     time.Duration
	maxMessageBytes int64
	maxQueuedTurns  int
	maxQueuedBytes  int64
}

func wsMaxMessageBytes() int64 {
	if MaxAllowedQueuedBytes <= wsTransportReadLimitPadding {
		return MaxAllowedQueuedBytes
	}
	return MaxAllowedQueuedBytes - wsTransportReadLimitPadding
}

func wsMinimumQueuedBytes(maxMessageBytes int64) int64 {
	if maxMessageBytes > (1<<63-1)-wsTransportReadLimitPadding {
		return maxMessageBytes
	}
	return maxMessageBytes + wsTransportReadLimitPadding
}

func wsBoundsFromConfig(c WebSocketConfig) wsBounds {
	maxAge, err := time.ParseDuration(c.MaxConnectionAge)
	if err != nil || maxAge <= 0 || maxAge > MaxAllowedWSConnectionAgeDur {
		maxAge = MaxAllowedWSConnectionAgeDur
	}
	idle, err := time.ParseDuration(c.IdleTimeout)
	if err != nil || idle <= 0 {
		idle = 5 * time.Minute
	}
	queued := max(c.MaxQueuedTurns, 1)
	queuedBytes := c.MaxQueuedBytes
	if queuedBytes < 1 {
		queuedBytes = DefaultMaxQueuedBytes
	}
	if queuedBytes > MaxAllowedQueuedBytes {
		queuedBytes = MaxAllowedQueuedBytes
	}
	if queuedBytes < wsMinimumQueuedBytes(wsDefaultMaxMessageBytes) {
		queuedBytes = wsMinimumQueuedBytes(wsDefaultMaxMessageBytes)
	}
	return wsBounds{
		maxAge:          maxAge,
		idleTimeout:     idle,
		maxMessageBytes: wsDefaultMaxMessageBytes,
		maxQueuedTurns:  queued,
		maxQueuedBytes:  queuedBytes,
	}
}
