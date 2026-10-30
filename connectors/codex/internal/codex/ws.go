package codex

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/connectors/codex/internal/routingstub"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/gorilla/websocket"
)

const wsHandshakeTimeout = 30 * time.Second

// wsFirstEventTimeout bounds the wait for the first canonical event after the
// WebSocket handshake. Without it, a server that upgrades but never sends would
// leave openWS blocked forever on conn.ReadMessage (which ignores ctx). It is a
// package var instead of a const so internal tests can shorten it; production
// callers always see the default.
var wsFirstEventTimeout = 30 * time.Second

var errWSPreviousResponseNotFound = errors.New("websocket previous response not found")

// wsEndpoint converts an HTTPS Codex base URL into the WebSocket scheme used by
// the Codex Responses WebSocket transport. Path handling mirrors
// responsesEndpoint so the same base_url value configures both transports.
func wsEndpoint(baseURL string) string {
	base := normalizedResponsesBase(baseURL)
	switch {
	case strings.HasPrefix(base, "https://"):
		return "wss://" + strings.TrimPrefix(base, "https://")
	case strings.HasPrefix(base, "http://"):
		return "ws://" + strings.TrimPrefix(base, "http://")
	default:
		return base
	}
}

func newWSDialer(client *http.Client) *websocket.Dialer {
	d := &websocket.Dialer{HandshakeTimeout: wsHandshakeTimeout}
	if client != nil {
		if t, ok := client.Transport.(*http.Transport); ok && t != nil {
			d.Proxy = t.Proxy
			d.NetDialContext = t.DialContext
			if t.TLSClientConfig != nil {
				d.TLSClientConfig = t.TLSClientConfig.Clone()
			} else {
				d.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
			}
		}
		// When client.Transport is a custom RoundTripper (e.g. instrumentation)
		// rather than *http.Transport, proxy/TLS settings cannot be introspected
		// generically, so the WS dialer falls back to default networking. This
		// differs from the HTTPS path that uses the same client.
	}
	return d
}

// openWS dials the Codex Responses WebSocket, sends a response.create frame, and
// returns a managed event stream after the first canonical event is received.
// A failure before the first canonical event is returned as an error so the
// auto transport can fall back to HTTPS.
func openWS(ctx context.Context, cfg *Config, native *NativeContextCoordinator, policy downgradePolicy, usageEst *usageEstimator, sessions *wsSessionStore, continuation *wsContinuationStore, call aipapi.Call, cand routingstub.AttemptCandidate, turns *sessionTurnCounter) (aipapi.ManagedEventStream, error) {
	env, err := prepareCodexOpenEnv(ctx, cfg, call, cand, policy, turns)
	if err != nil {
		return nil, err
	}
	model := policy.modelForPlan(env.originalModel, cfg.PlanTypeHint)
	if err := prepareNativeContextForOpen(ctx, env, cfg, call, model, native, continuation); err != nil {
		env.releaseVerbosityTurn()
		return nil, err
	}
	es, _, err := openWSPrepared(ctx, env, cfg, model, call, usageEst, sessions, continuation)
	es = env.wrapNativeUsage(es, err)
	if err != nil {
		env.releaseVerbosityTurn()
	} else {
		env.commitVerbosityTurn()
	}
	return es, err
}

func openWSPrepared(ctx context.Context, env *codexOpenEnv, cfg *Config, model string, call aipapi.Call, usageEst *usageEstimator, sessions *wsSessionStore, continuation *wsContinuationStore) (aipapi.ManagedEventStream, *http.Response, error) {
	es, resp, rawFirst, err := openWSPreparedAttempt(ctx, env, cfg, model, call, usageEst, sessions, continuation)
	if err == nil {
		return es, resp, nil
	}
	if !isWSFreePlanRejection(rawFirst, env.downgrade, env.originalModel) {
		return nil, resp, err
	}
	es, resp, _, err = openWSPreparedAttempt(ctx, env, cfg, env.downgrade.target, call, usageEst, sessions, continuation)
	return es, resp, err
}

type wsOpenRetryDecision int

const (
	wsOpenNoRetry wsOpenRetryDecision = iota
	wsOpenRetryFreshSession
	wsOpenRetryWithoutContinuation
)

type wsOpenAttemptState struct {
	allowContinuation bool
	allowStaleRetry   bool
}

type wsOpenAttemptPayload struct {
	env                 *codexOpenEnv
	cfg                 *Config
	call                aipapi.Call
	continuation        *wsContinuationStore
	fullPayload         Payload
	fullInputFP         []string
	continuationApplied bool
}

func newWSOpenAttemptPayload(ctx context.Context, env *codexOpenEnv, cfg *Config, model string, call aipapi.Call, continuation *wsContinuationStore, state wsOpenAttemptState) wsOpenAttemptPayload {
	env.payload.Model = model
	fullPayload := env.payload
	fullInputFP := append([]string(nil), env.inputFingerprints...)
	return wsOpenAttemptPayload{
		env:                 env,
		cfg:                 cfg,
		call:                call,
		continuation:        continuation,
		fullPayload:         fullPayload,
		fullInputFP:         fullInputFP,
		continuationApplied: state.allowContinuation && continuation.prepareWithFingerprints(ctx, cfg, call, &env.payload, fullInputFP),
	}
}

func (p wsOpenAttemptPayload) rollback() {
	if p.continuationApplied {
		p.continuation.invalidateWithFingerprints(p.cfg, p.call, &p.fullPayload, p.fullInputFP)
	}
	p.env.payload = p.fullPayload
}

func openWSPreparedAttempt(ctx context.Context, env *codexOpenEnv, cfg *Config, model string, call aipapi.Call, usageEst *usageEstimator, sessions *wsSessionStore, continuation *wsContinuationStore) (aipapi.ManagedEventStream, *http.Response, []byte, error) {
	state := wsOpenAttemptState{
		allowContinuation: true,
		allowStaleRetry:   true,
	}
	for {
		es, resp, rawFirst, retry, err := openWSPreparedAttemptOnce(ctx, env, cfg, model, call, usageEst, sessions, continuation, state)
		switch retry {
		case wsOpenNoRetry:
			return es, resp, rawFirst, err
		case wsOpenRetryFreshSession:
			state.allowStaleRetry = false
		case wsOpenRetryWithoutContinuation:
			state.allowContinuation = false
			state.allowStaleRetry = false
		}
	}
}

func openWSPreparedAttemptOnce(ctx context.Context, env *codexOpenEnv, cfg *Config, model string, call aipapi.Call, usageEst *usageEstimator, sessions *wsSessionStore, continuation *wsContinuationStore, state wsOpenAttemptState) (aipapi.ManagedEventStream, *http.Response, []byte, wsOpenRetryDecision, error) {
	if sessions == nil {
		sessions = newWSSessionStore()
	}
	if continuation == nil {
		continuation = newWSContinuationStore(codexContinuationTTL, codexContinuationMaxEntries)
	}
	attemptPayload := newWSOpenAttemptPayload(ctx, env, cfg, model, call, continuation, state)
	frame, err := payloadToWSResponseCreate(env.payload)
	if err != nil {
		attemptPayload.rollback()
		return nil, nil, nil, wsOpenNoRetry, err
	}
	session, resp, reusedSession, err := sessions.acquire(ctx, env.client, wsEndpoint(cfg.BaseURL), cfg, env.convID)
	if err != nil {
		// Restore the full payload snapshot before returning so a rotation retry on
		// another account does not inherit this attempt's continuation-trimmed Input
		// and PreviousResponseID. The other retry paths restore below for the same
		// reason; the handshake-error path must too because it hands resp back to the
		// managed loop, which rotates accounts on 401/403/429 reusing this env.
		attemptPayload.rollback()
		// Return the (body-closed) handshake response so the managed WS path can
		// classify 401/403/429 handshakes and rotate to the next account.
		return nil, resp, nil, wsOpenNoRetry, err
	}
	conn := session.conn
	if err := writeWSResponseCreate(ctx, conn, frame); err != nil {
		session.release(true)
		if reusedSession && state.allowStaleRetry {
			attemptPayload.rollback()
			return nil, nil, nil, wsOpenRetryFreshSession, err
		}
		attemptPayload.rollback()
		return nil, nil, nil, wsOpenNoRetry, err
	}
	effectiveModel := strings.TrimSpace(env.payload.Model)
	if effectiveModel == "" {
		effectiveModel = env.originalModel
	}
	// Read the first raw frame directly so a pre-content model rejection can be
	// detected before canonical mapping: the mapper synthesizes a ResponseStarted
	// event ahead of an EventError, which would hide the rejection from a
	// first-canonical-event check.
	rawFirst, rerr := readFirstNonEmptyWSMessage(ctx, conn, wsFirstEventTimeout)
	if rerr != nil {
		session.release(true)
		if reusedSession && state.allowStaleRetry && isWSFallbackError(ctx, rerr) {
			attemptPayload.rollback()
			return nil, nil, nil, wsOpenRetryFreshSession, rerr
		}
		attemptPayload.rollback()
		return nil, nil, nil, wsOpenNoRetry, rerr
	}
	if isWSFreePlanRejection(rawFirst, env.downgrade, env.originalModel) {
		session.release(true)
		attemptPayload.rollback()
		return nil, resp, rawFirst, wsOpenNoRetry, fmt.Errorf("%s: websocket model rejected before first event", ID)
	}
	mapper := newCodexEventMapper(call.MaxPendingWireEvents)
	mapper.providerAccountKey = strings.TrimSpace(cfg.AccountID)
	if err := mapper.handleData(string(rawFirst)); err != nil {
		session.release(true)
		attemptPayload.rollback()
		return nil, nil, rawFirst, wsOpenNoRetry, err
	}
	wsStream := newWSStreamWithMapper(conn, mapper)
	if resp != nil {
		wsStream.captureAccountWindowSnapshots(resp.Header, cfg.AccountID, time.Now().UTC())
	}
	wsStream.release = session.release
	var managed aipapi.ManagedEventStream
	if cfg.Transport == TransportWebSocket {
		// Strict WS mode returns as soon as the first canonical event is available.
		// This mirrors the HTTPS open contract and lets the frontend stream
		// response.started immediately. Waiting here for committed output is only
		// needed in auto mode, where the transport must still be able to downgrade
		// to HTTPS before any downstream-visible content commits.
		managed, rerr = openManagedFirstEvent(ctx, wsStream, usageEst, call, effectiveModel)
	} else {
		managed, rerr = openManagedUntilCommitted(ctx, wsStream, usageEst, call, effectiveModel, wsFirstEventTimeout)
	}
	if rerr != nil {
		if attemptPayload.continuationApplied && errors.Is(rerr, errWSPreviousResponseNotFound) {
			attemptPayload.rollback()
			wsStream.releaseOnce(true)
			return nil, nil, rawFirst, wsOpenRetryWithoutContinuation, rerr
		}
		attemptPayload.rollback()
		return nil, nil, rawFirst, wsOpenNoRetry, wsPreFirstEventFailure(rerr)
	}
	managed = newCodexContinuationRecordingStream(managed, cfg, call, attemptPayload.fullPayload, attemptPayload.fullInputFP, mapper, continuation)
	// The opening boundary has been reached: strict websocket mode returns after
	// the first canonical event, while auto mode waits until output is committed
	// or terminal. Clear the deadline so subsequent streaming reads are governed
	// by caller contexts rather than the open-time fallback window.
	_ = conn.SetReadDeadline(time.Time{})
	return managed, resp, rawFirst, wsOpenNoRetry, nil
}

func openManagedUntilCommitted(ctx context.Context, es aipapi.ManagedEventStream, usageEst *usageEstimator, call aipapi.Call, model string, timeout time.Duration) (aipapi.ManagedEventStream, error) {
	managed := newUsageEstimatingStream(es, usageEst, call, model)
	recvCtx := ctx
	cancel := func() {}
	if timeout > 0 {
		recvCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	var first []aipapi.Event
	for {
		ev, err := managed.Recv(recvCtx)
		if err != nil {
			_ = managed.Close()
			if ctx != nil && ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, newWSTransportError(err)
		}
		first = append(first, ev)
		if ev.Kind == aipapi.EventError {
			_ = managed.Close()
			if ev.ErrorCode == "previous_response_not_found" {
				return nil, errWSPreviousResponseNotFound
			}
			return nil, fmt.Errorf("%s: upstream websocket error before output: %s", ID, ev.ErrorMessage)
		}
		if wsOpenCommitted(ev) {
			return prependManagedEvents(first, managed), nil
		}
	}
}

func wsOpenCommitted(ev aipapi.Event) bool {
	return aipapi.OutputCommitted(ev) || ev.Kind == aipapi.EventError || ev.Kind == aipapi.EventResponseFinished
}

func prependManagedEvents(events []aipapi.Event, rest aipapi.ManagedEventStream) aipapi.ManagedEventStream {
	out := rest
	for _, event := range slices.Backward(events) {
		out = &codexPrependManaged{first: event, rest: out}
	}
	return out
}

// codexPrependManaged keeps the provider sidebands visible after the WebSocket
// open path has pre-read one or more canonical events. The generic streampeek
// wrapper cannot name this connector's account-window snapshot type, so using
// a connector-local wrapper is required to avoid dropping native gauge data.
type codexPrependManaged struct {
	first aipapi.Event
	rest  aipapi.ManagedEventStream
	sent  bool
}

func (s *codexPrependManaged) Recv(ctx context.Context) (aipapi.Event, error) {
	if !s.sent {
		s.sent = true
		return s.first, nil
	}
	if s.rest == nil {
		return aipapi.Event{}, io.EOF
	}
	return s.rest.Recv(ctx)
}

func (s *codexPrependManaged) Close() error {
	if s == nil || s.rest == nil {
		return nil
	}
	return s.rest.Close()
}

func (s *codexPrependManaged) Cancel(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	if s == nil || s.rest == nil {
		return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
	}
	return s.rest.Cancel(ctx, cause)
}

func (s *codexPrependManaged) DrainAccountWindowSnapshots() []AccountWindowSnapshot {
	if s == nil || s.rest == nil {
		return nil
	}
	if source, ok := s.rest.(AccountWindowSnapshotSource); ok {
		return source.DrainAccountWindowSnapshots()
	}
	return nil
}

var _ aipapi.ManagedEventStream = (*codexContinuationRecordingStream)(nil)

type codexContinuationRecordingStream struct {
	inner    aipapi.ManagedEventStream
	cfg      *Config
	call     aipapi.Call
	payload  Payload
	inputFP  []string
	mapper   *codexEventMapper
	store    *wsContinuationStore
	once     sync.Once
	mu       sync.Mutex
	recorded bool
}

func newCodexContinuationRecordingStream(inner aipapi.ManagedEventStream, cfg *Config, call aipapi.Call, payload Payload, inputFingerprints []string, mapper *codexEventMapper, store *wsContinuationStore) aipapi.ManagedEventStream {
	return &codexContinuationRecordingStream{
		inner:   inner,
		cfg:     cfg,
		call:    call,
		payload: payload,
		inputFP: append([]string(nil), inputFingerprints...),
		mapper:  mapper,
		store:   store,
	}
}

func (s *codexContinuationRecordingStream) Recv(ctx context.Context) (aipapi.Event, error) {
	ev, err := s.inner.Recv(ctx)
	if err == nil && ev.Kind == aipapi.EventResponseFinished {
		s.record()
	}
	return ev, err
}

func (s *codexContinuationRecordingStream) Close() error {
	err := s.inner.Close()
	if !s.wasRecorded() {
		s.store.invalidateWithFingerprints(s.cfg, s.call, &s.payload, s.inputFP)
	}
	return err
}

func (s *codexContinuationRecordingStream) Cancel(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	res := s.inner.Cancel(ctx, cause)
	if !s.wasRecorded() {
		s.store.invalidateWithFingerprints(s.cfg, s.call, &s.payload, s.inputFP)
	}
	return res
}

func (s *codexContinuationRecordingStream) DrainAccountWindowSnapshots() []AccountWindowSnapshot {
	if source, ok := s.inner.(AccountWindowSnapshotSource); ok {
		return source.DrainAccountWindowSnapshots()
	}
	return nil
}

func (s *codexContinuationRecordingStream) record() {
	s.once.Do(func() {
		if s.mapper == nil {
			return
		}
		if strings.TrimSpace(s.mapper.responseID) == "" {
			return
		}
		s.mu.Lock()
		s.recorded = true
		s.mu.Unlock()
		s.store.recordWithFingerprints(s.cfg, s.call, s.payload, s.inputFP, s.mapper.responseID, s.mapper.outputItems...)
	})
}

func (s *codexContinuationRecordingStream) wasRecorded() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recorded
}

// readFirstNonEmptyWSMessage reads WebSocket text frames, skipping empty ones, until
// the first non-empty frame arrives. Pre-first-event read/close failures are wrapped
// as wsTransportError so auto mode can fall back to HTTPS. The caller sets the read
// deadline.
func readFirstNonEmptyWSMessage(ctx context.Context, conn *websocket.Conn, timeout time.Duration) ([]byte, error) {
	if timeout > 0 {
		_ = conn.SetReadDeadline(time.Now().Add(timeout))
	}
	stopCancel := func() bool { return true }
	if ctx != nil {
		stopCancel = context.AfterFunc(ctx, func() {
			_ = conn.SetReadDeadline(time.Now())
		})
	}
	defer stopCancel()
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			if ctx != nil && ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, newWSTransportError(fmt.Errorf("read websocket: %w", err))
		}
		if len(strings.TrimSpace(string(data))) > 0 {
			return data, nil
		}
	}
}

func writeWSResponseCreate(ctx context.Context, conn *websocket.Conn, frame json.RawMessage) error {
	if wsFirstEventTimeout > 0 {
		_ = conn.SetWriteDeadline(time.Now().Add(wsFirstEventTimeout))
	}
	stopCancel := func() bool { return true }
	if ctx != nil {
		stopCancel = context.AfterFunc(ctx, func() {
			_ = conn.SetWriteDeadline(time.Now())
		})
	}
	err := conn.WriteJSON(frame)
	stopCancel()
	_ = conn.SetWriteDeadline(time.Time{})
	if err == nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return newWSTransportError(fmt.Errorf("websocket send response.create: %w", err))
}

// isWSFreePlanRejection reports whether a raw WebSocket frame is a pre-content error
// event whose message matches a free-plan gpt-5.5 rejection. Mirrors the HTTP path's
// downgradePolicy.isFreePlanRejection but operates on an error event frame instead of
// an HTTP status+body pair, since the WebSocket transport has no status code.
func isWSFreePlanRejection(rawFrame []byte, policy downgradePolicy, originalModel string) bool {
	var probe struct {
		Type  string `json:"type"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rawFrame, &probe); err != nil {
		return false
	}
	if probe.Type != "error" || probe.Error == nil {
		return false
	}
	return policy.shouldReactiveRetry(originalModel, false, probe.Error.Message)
}

func dialCodexWebSocket(ctx context.Context, client *http.Client, url string, cfg *Config, convID string) (*websocket.Conn, *http.Response, error) {
	d := newWSDialer(client)
	conn, resp, err := d.DialContext(ctx, url, codexWSHeaders(*cfg, convID))
	if err != nil {
		if resp != nil {
			// Body is closed but resp.StatusCode/Header remain readable so callers
			// (e.g. managed WS rotation) can classify 401/403/429 handshakes.
			_ = resp.Body.Close()
			return nil, resp, newWSTransportError(fmt.Errorf("websocket dial: %w (status=%s)", err, resp.Status))
		}
		return nil, nil, newWSTransportError(fmt.Errorf("websocket dial: %w", err))
	}
	return conn, resp, nil
}

const wsFrameTypeResponseCreate = "response.create"

type wsResponseCreateFrame struct {
	Type string `json:"type"`
	Payload
}

// payloadToWSResponseCreate builds a WebSocket response.create frame from a Codex
// HTTPS payload: same fields with stream omitted and type set explicitly.
func payloadToWSResponseCreate(p Payload) (json.RawMessage, error) {
	p.Stream = false
	frame := wsResponseCreateFrame{
		Type:    wsFrameTypeResponseCreate,
		Payload: p,
	}
	out, err := json.Marshal(frame)
	if err != nil {
		return nil, fmt.Errorf("%s: marshal ws frame: %w", ID, err)
	}
	return out, nil
}
