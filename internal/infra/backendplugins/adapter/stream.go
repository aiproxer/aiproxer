package adapter

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/promptcache"
)

const defaultMaxStderrBytes = 64 << 10

const maxBufferedUsageEvidence = 1024

// siwcPreviewOmissionMarker is the private exact marker the SIWC connector
// emits once on a served stream when its preview policy omitted an unsupported
// request field. The connector duplicates the literal across the module
// boundary; the staged acceptance test pins both sides together. The adapter
// consumes this exact warning after recording a fixed operator warning, so it
// never reaches the client stream or the OpenResponses lifecycle.
const siwcPreviewOmissionMarker = "siwc_preview_omission_v1"

// siwcPreviewOmissionLogMessage is the fixed, content-free operator warning
// recorded for the marker. It never includes connector warning text, request
// fields, client ids or conversation content.
const siwcPreviewOmissionLogMessage = "openai-chatgpt-plan: request degraded under the SIWC preview policy"

// errCanonicalEventAfterFinish rejects a canonical event that follows
// response_finished on a prompt-cache-negotiated connector. response_finished
// is the response's terminal event; while the prompt-cache finish gate withholds
// it, accepting later canonical events would let them fill the bounded event
// channel and block the Execute pump before the terminal frame that settles the
// observation sideband.
var errCanonicalEventAfterFinish = errors.New("adapter: canonical event after response_finished")

// openStream starts one bidirectional execute attempt. It never collects the
// provider response and never restarts after output commitment.
func openStream(
	ctx context.Context,
	session ExecuteSession,
	opt Options,
	call aipapi.Call,
	cand routing.AttemptCandidate,
) (aipapi.ManagedEventStream, error) {
	inv, err := InvocationFromCall(call, cand, opt.Negotiation)
	if err != nil {
		return nil, err
	}
	// The host allocates a fresh B-leg for every actual attempt. The request id
	// alone repeats across retries and parallel attempts, so it is not a
	// per-charge identity; the host attempt identity replaces the deterministic
	// fallback when the executor supplies it.
	if meta, ok := execbackend.AttemptMetaFromContext(ctx); ok {
		inv.AttemptID = meta.ID
	}
	pending := opt.MaxPendingEvents
	if pending <= 0 {
		pending = 64
	}
	hostFrames := make(chan backendplugin.ClientFrame, 4)
	start := backendplugin.ClientFrame{
		Kind:       backendplugin.ClientFrameStart,
		InstanceID: opt.InstanceID,
		Invocation: &inv,
	}
	if err := start.ValidateShape(); err != nil {
		return nil, &ClassifiedError{Code: "invalid_invocation", Message: err.Error(), Retryable: false}
	}
	if err := backendplugin.ValidateClientFrameBounds(start); err != nil {
		return nil, &ClassifiedError{Code: "oversized_frame", Message: err.Error(), Retryable: false}
	}
	select {
	case hostFrames <- start:
	default:
		return nil, &ClassifiedError{Code: "internal", Message: "start frame enqueue failed", Retryable: true}
	}

	execCtx, execCancel := context.WithCancel(ctx)
	s := &managedStream{
		ctx:           execCtx,
		cancel:        execCancel,
		opt:           opt,
		events:        make(chan aipapi.Event, pending),
		errCh:         make(chan error, 1),
		hostFrames:    hostFrames,
		done:          make(chan struct{}),
		lineageALegID: inv.ALegID,
		lineageBLegID: inv.BLegID,
		claims: newConnectorClaimState(
			opt.ConnectorEconomicEvidencePolicy,
			backendplugin.HostBoundEconomicEvidenceV2Negotiated(opt.Negotiation),
		),
		promptCacheOutcomeClosed: make(chan struct{}),
	}
	if opt.MaxStreamFrame > 0 {
		s.maxFrame = opt.MaxStreamFrame
	} else {
		s.maxFrame = int(backendplugin.DefaultMaxStreamFrameBytes)
	}
	s.stats.ProviderAttempts.Add(1)

	execStream := &bridgeExecuteStream{
		ctx:     execCtx,
		closeCh: make(chan struct{}),
		recv:    hostFrames,
		send:    s.onPluginFrame,
		neg:     opt.Negotiation,
	}

	s.wg.Go(func() {
		defer close(s.done)
		defer close(s.events)
		// The Execute session is the last point at which the prompt-cache
		// sideband can be committed or discarded. Closing the outcome here
		// bounds the finish gate even when a connector ends without a terminal:
		// a withheld response_finished must still reach the consumer.
		defer s.closePromptCacheOutcome()
		err := session.Execute(execStream)
		// The Execute session returned: no further connector frame can arrive,
		// so the connector evidence window is closed.
		s.executeFinished.Store(true)
		if err != nil || !s.terminalSeen.Load() {
			s.promptCacheMu.Lock()
			s.promptCacheBuffer.Discard()
			s.promptCacheMu.Unlock()
		}
		if err != nil {
			wasCanceled := s.cancelState.interrupted() || s.closed.Load() || (s.ctx != nil && s.ctx.Err() != nil)

			var fe *ExecuteFailureError
			if wasCanceled && !isProtocolSentinel(err) {
				fe = &ExecuteFailureError{
					Kind:            ExecuteFailureCanceled,
					Err:             err,
					OutputCommitted: s.outputCommitted(),
				}
			} else {
				fe = ClassifyExecuteError(err, s.outputCommitted())
			}
			if fe.InvalidatesGeneration() && opt.InvalidateGeneration != nil {
				s.invalidateOnce.Do(opt.InvalidateGeneration)
			}
			select {
			case s.errCh <- fe.ToClassifiedError():
			default:
			}
		}
	})

	if opt.Stderr != nil {
		s.wg.Go(func() {
			s.drainStderr(opt.Stderr)
		})
	}

	return s, nil
}

type CancellationProgress struct {
	Requested           bool
	Cause               aipapi.CancelCause
	EffectiveDeadline   time.Time
	OutcomeSeen         bool
	OutcomeAcknowledged bool
	OutcomeMode         backendplugin.CancelMode
	OutcomeReason       backendplugin.CancelReason
	OutcomeDetail       string
	ForcedAbort         bool
	TerminalSeen        bool
}

type managedStream struct {
	ctx               context.Context
	cancel            context.CancelFunc
	opt               Options
	events            chan aipapi.Event
	errCh             chan error
	hostFrames        chan backendplugin.ClientFrame
	done              chan struct{}
	cancelState       cancelState
	wg                sync.WaitGroup
	closeOnce         sync.Once
	closed            atomic.Bool
	terminalSeen      atomic.Bool
	invalidateOnce    sync.Once
	validator         backendplugin.StreamValidator
	stats             streamStats
	maxFrame          int
	mu                sync.Mutex
	recvErr           error
	stderrBytes       int
	usageMu           sync.Mutex
	usageEvidence     []aipapi.Event
	economicMu        sync.Mutex
	claims            *connectorClaimState
	promptCacheMu     sync.Mutex
	promptCacheBuffer promptcache.ObservationBuffer
	// promptCacheOutcomeClosed is closed exactly once when the Execute attempt's
	// terminal frame has committed or discarded the prompt-cache sideband, or
	// when the Execute session returned without one. The runtime drains that
	// sideband exactly once at the response_finished boundary, so a finish
	// exposed before this point would observe an uncommitted buffer and silently
	// lose the maintenance target. Nil in hand-built test streams, where the
	// gate is a no-op.
	promptCacheOutcomeClosed chan struct{}
	promptCacheOutcomeOnce   sync.Once
	// pendingFinish retains a response_finished event already taken from the
	// event channel while the prompt-cache outcome was still open. Recv is the
	// stream's single consumer, so no lock is needed; a cancelled receive keeps
	// the event here for the next receive instead of dropping it.
	pendingFinish aipapi.Event
	// promptCacheFinishQueued records that a prompt-cache-negotiated stream
	// queued its canonical terminal event. Canonical events after that point
	// are rejected instead of sent, so a malformed connector cannot fill the
	// event channel and block before the terminal frame settles the sideband.
	promptCacheFinishQueued atomic.Bool
	// omissionWarningLogged records that the exact private SIWC omission marker
	// already produced its one fixed operator warning for this attempt. It is
	// touched only by the Execute pump through onPluginFrame.
	omissionWarningLogged bool
	// lineageALegID/lineageBLegID are the exact attempt A-leg and transport leg
	// pseudonym the host supplied in the start invocation. A plugin-side
	// prompt-cache observation may only echo these values (or leave a field
	// empty); anything else is a transport protocol violation.
	lineageALegID string
	lineageBLegID string
	// executeFinished reports that the Execute session returned, so no further
	// connector frame can arrive and the connector evidence window is closed.
	executeFinished atomic.Bool
	// decision is the single linearizable state of the output commitment and
	// the pending strict refusal. The commitment transition and the refusal
	// delivery are decided by one compare-and-swap.
	decision atomic.Pointer[streamDecision]
	// beforeRefusalDelivery is the test seam of the refusal delivery. When it
	// is set, it runs between the refusal check and the refusal delivery, so a
	// test can order the output commitment deterministically inside that
	// window. It is nil in production.
	beforeRefusalDelivery func()
}

// streamDecision is the single linearizable state of the managed stream's
// output commitment and its pending strict refusal.
//
// The commitment transition and the refusal delivery are decided by one
// compare-and-swap, so exactly one of them wins: a refusal that is published
// before the commitment prevents the commitment, and a commitment that
// happened first drops the refusal and keeps the committed stream.
type streamDecision struct {
	// refusal is the typed strict refusal of the connector evidence the host
	// could not admit. It is set only when the refusal won the decision.
	refusal *ExecuteFailureError
	// committed reports that the client's output committed. A committed stream
	// never delivers a strict refusal.
	committed bool
	// delivered reports that the refusal was handed to the caller. The
	// refusal is delivered exactly once.
	delivered bool
}

// publishRefusal publishes the strict pre-commit refusal of the connector
// evidence the host could not admit. It reports whether the refusal won the
// decision: a refusal published after the output committed is dropped, so the
// committed stream is preserved and no refusal is raised.
func (s *managedStream) publishRefusal(refusal *ExecuteFailureError) bool {
	for {
		current := s.decision.Load()
		if current != nil {
			// The output committed, or a refusal is already published.
			return false
		}
		if s.decision.CompareAndSwap(nil, &streamDecision{refusal: refusal}) {
			return true
		}
	}
}

// commitOutput commits the client's output. It reports whether the commitment
// was applied. A pending refusal owns the attempt: the output must not commit
// and the content must not reach the client.
func (s *managedStream) commitOutput() bool {
	for {
		current := s.decision.Load()
		switch {
		case current == nil:
			if s.decision.CompareAndSwap(nil, &streamDecision{committed: true}) {
				return true
			}
		case current.committed:
			return true
		default:
			return false
		}
	}
}

// takeRefusal claims the pending refusal for delivery. It reports nil when no
// refusal is pending, when the output committed first, or when the refusal
// was already delivered.
func (s *managedStream) takeRefusal() *ExecuteFailureError {
	for {
		current := s.decision.Load()
		if current == nil || current.refusal == nil || current.delivered {
			return nil
		}
		next := &streamDecision{refusal: current.refusal, delivered: true}
		if s.decision.CompareAndSwap(current, next) {
			return current.refusal
		}
	}
}

// refusalPending reports whether a strict refusal is waiting for delivery.
func (s *managedStream) refusalPending() bool {
	current := s.decision.Load()
	return current != nil && current.refusal != nil && !current.delivered
}

// outputCommitted reports whether the client's output committed.
func (s *managedStream) outputCommitted() bool {
	current := s.decision.Load()
	return current != nil && current.committed
}

// AccountingEvidenceEnabled reports whether this adapter negotiated a typed
// host-only accounting sideband that owns the canonical provider usage path.
// Legacy V1 is deliberately not reported here: its six counters have no
// source-role marker, so each connector must project its own canonical key
// when V1 is authoritative. Keeping V1 false also preserves auxiliary/native
// V1 evidence (for example Codex compaction) alongside primary usage.
func (s *managedStream) AccountingEvidenceEnabled() bool {
	if s == nil {
		return false
	}
	return backendplugin.AccountingEvidenceV2Negotiated(s.opt.Negotiation)
}

func (s *managedStream) CancellationProgress() CancellationProgress {
	prog := s.cancelState.snapshot()
	prog.TerminalSeen = s.terminalSeen.Load()
	return prog
}

func (s *managedStream) CancellationOutcomeSeen() bool {
	return s.cancelState.isOutcomeSeen()
}

func (s *managedStream) CancellationForcedAbort() bool {
	return s.cancelState.forcedAbort()
}

func (s *managedStream) CancellationHandshakeNegotiated() bool {
	return backendplugin.CancellationHandshakeNegotiated(s.opt.Negotiation)
}

type streamStats struct {
	ProviderAttempts atomic.Int64
}

func (s *managedStream) Recv(ctx context.Context) (aipapi.Event, error) {
	// A strict pre-commit refusal is delivered before any queued event: the
	// attempt is refused before the client can observe its content. The
	// commitment and the refusal delivery are one linearizable decision, so
	// exactly one of them wins: a refusal that was published first prevents
	// the commitment, and a commitment that happened first drops the refusal
	// and keeps the committed stream.
	if s.refusalPending() {
		select {
		case <-ctx.Done():
			return aipapi.Event{}, ctx.Err()
		default:
		}
		if s.beforeRefusalDelivery != nil {
			s.beforeRefusalDelivery()
		}
		if refusal := s.takeRefusal(); refusal != nil {
			return aipapi.Event{}, refusal.ToClassifiedError()
		}
	}
	// A response_finished withheld by the prompt-cache finish gate is always
	// delivered before any later queued event, preserving connector order even
	// when the consumer was cancelled while it was withheld.
	if s.pendingFinish.Kind != "" {
		return s.releasePendingFinish(ctx)
	}
	for {
		// Prefer queued plugin events over a concurrently ready Execute error so
		// commit-then-fail and similar modes deliver output before the failure.
		select {
		case <-ctx.Done():
			return aipapi.Event{}, ctx.Err()
		case ev, ok := <-s.events:
			if ok {
				return s.releaseEvent(ctx, ev)
			}
			return s.recvAfterEventsClosed()
		default:
		}
		select {
		case <-ctx.Done():
			return aipapi.Event{}, ctx.Err()
		case ev, ok := <-s.events:
			if ok {
				return s.releaseEvent(ctx, ev)
			}
			return s.recvAfterEventsClosed()
		case err := <-s.errCh:
			select {
			case ev, ok := <-s.events:
				if ok {
					s.stashRecvErr(err)
					return s.releaseEvent(ctx, ev)
				}
			default:
			}
			if err == nil {
				return aipapi.Event{}, io.EOF
			}
			return aipapi.Event{}, err
		case <-s.ctx.Done():
			return s.recvAfterStreamCanceled(ctx)
		}
	}
}

// recvAfterStreamCanceled is the cancellation tail of Recv: the stream context
// is already done, so it delivers one already-queued event before the
// cancellation error, preserving the pre-gate prompt-unblock behavior. A
// queued response_finished still goes through the same finish gate; a canceled
// stream cannot commit a new sideband, so the gate releases it promptly instead
// of waiting for a terminal that may never arrive.
func (s *managedStream) recvAfterStreamCanceled(ctx context.Context) (aipapi.Event, error) {
	select {
	case ev, ok := <-s.events:
		if ok {
			return s.releaseEvent(ctx, ev)
		}
	default:
	}
	if _, err := s.recvAfterEventsClosed(); err != nil && !errors.Is(err, io.EOF) {
		return aipapi.Event{}, err
	}
	return aipapi.Event{}, s.ctx.Err()
}

// releaseEvent applies the executable connector's prompt-cache finish gate. A
// connector commits its prompt-cache observation sideband only on the terminal
// frame, which follows response_finished, while the runtime drains that
// sideband exactly once at the finish boundary. Releasing the finish before the
// terminal would let the runtime observe an uncommitted buffer and silently
// lose the keep-warm maintenance target, so the finish is retained until the
// terminal fixes the sideband's disposition. Streams that did not negotiate
// prompt-cache residency are unaffected.
func (s *managedStream) releaseEvent(ctx context.Context, ev aipapi.Event) (aipapi.Event, error) {
	if ev.Kind != aipapi.EventResponseFinished || !backendplugin.PromptCacheNegotiated(s.opt.Negotiation) {
		return ev, nil
	}
	s.pendingFinish = ev
	return s.releasePendingFinish(ctx)
}

// releasePendingFinish waits for the terminal outcome and returns the withheld
// finish. A cancelled wait keeps the event pending so the next receive can
// still deliver it after the terminal, exactly once.
func (s *managedStream) releasePendingFinish(ctx context.Context) (aipapi.Event, error) {
	if err := s.awaitPromptCacheOutcome(ctx); err != nil {
		return aipapi.Event{}, err
	}
	ev := s.pendingFinish
	s.pendingFinish = aipapi.Event{}
	return ev, nil
}

// awaitPromptCacheOutcome blocks until the prompt-cache observation sideband's
// disposition is final (terminal frame processed or Execute returned), or the
// caller cancels. A canceled or closed stream is not a source of new committed
// observations, so it releases rather than waiting for a terminal that may
// never arrive; only caller cancellation withholds the finish for a later
// receive. A nil outcome channel (hand-built test streams) never waits.
func (s *managedStream) awaitPromptCacheOutcome(ctx context.Context) error {
	if s.promptCacheOutcomeClosed == nil {
		return nil
	}
	var execDone <-chan struct{}
	if s.ctx != nil {
		execDone = s.ctx.Done()
	}
	select {
	case <-s.promptCacheOutcomeClosed:
		return nil
	case <-execDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// closePromptCacheOutcome makes the prompt-cache sideband's disposition final
// exactly once, waking any consumer withholding a response_finished.
func (s *managedStream) closePromptCacheOutcome() {
	if s == nil || s.promptCacheOutcomeClosed == nil {
		return
	}
	s.promptCacheOutcomeOnce.Do(func() { close(s.promptCacheOutcomeClosed) })
}

func (s *managedStream) stashRecvErr(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recvErr == nil {
		s.recvErr = err
	}
}

func (s *managedStream) recvAfterEventsClosed() (aipapi.Event, error) {
	s.mu.Lock()
	err := s.recvErr
	s.mu.Unlock()
	if err != nil {
		return aipapi.Event{}, err
	}
	select {
	case err := <-s.errCh:
		if err != nil {
			return aipapi.Event{}, err
		}
	default:
	}
	return aipapi.Event{}, io.EOF
}

func (s *managedStream) Close() error {
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		select {
		case s.hostFrames <- backendplugin.ClientFrame{Kind: backendplugin.ClientFrameCloseInput, InstanceID: s.opt.InstanceID}:
		case <-s.done:
		case <-s.ctx.Done():
		}
		s.cancel()
		s.wg.Wait()
	})
	return nil
}

func (s *managedStream) Cancel(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	if s.closed.Load() {
		return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
	}
	if s.terminalSeen.Load() {
		return s.cancelAfterTerminal()
	}
	if !backendplugin.CancellationHandshakeNegotiated(s.opt.Negotiation) {
		return s.cancelLegacy(ctx, cause)
	}
	return s.cancelNegotiated(ctx, cause)
}

func (s *managedStream) cancelAfterTerminal() aipapi.CancelResult {
	prog := s.CancellationProgress()
	if prog.OutcomeSeen {
		return outcomeCancelResult(prog)
	}
	return aipapi.CancelResult{Mode: aipapi.CancelModeNone}
}

func (s *managedStream) cancelLegacy(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	s.cancelState.request(cause, time.Time{})
	s.cancelState.markForced()

	s.cancel()
	timeout := s.opt.CancelTimeout
	if timeout <= 0 {
		timeout = legacyCancelWaitFallback
	}
	select {
	case <-s.done:
		return aipapi.CancelResult{Mode: aipapi.CancelModeTransport}
	case <-ctx.Done():
		return aipapi.CancelResult{Mode: aipapi.CancelModeTransport, Err: ctx.Err()}
	case <-time.After(timeout):
		return aipapi.CancelResult{Mode: aipapi.CancelModeTransport, Err: context.DeadlineExceeded}
	}
}

func (s *managedStream) cancelNegotiated(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	effectiveDeadline, deadlineMS := computeCancelDeadline(ctx, s.opt.CancelTimeout)
	firstRequest := s.cancelState.request(cause, effectiveDeadline)

	if firstRequest {
		reason := backendplugin.CancelReasonHost
		switch cause.Kind {
		case aipapi.CancelContextDone:
			reason = backendplugin.CancelReasonDeadline
		case aipapi.CancelClientGone:
			reason = backendplugin.CancelReasonClient
		case aipapi.CancelExplicit, aipapi.CancelRaceLoser:
			reason = backendplugin.CancelReasonHost
		default:
			if cause.Detail != "" {
				reason = backendplugin.CancelReason(cause.Detail)
			}
		}

		cancelFrame := backendplugin.ClientFrame{
			Kind:                 backendplugin.ClientFrameCancel,
			InstanceID:           s.opt.InstanceID,
			CancelReason:         reason,
			CancelDeadlineUnixMS: deadlineMS,
		}

		select {
		case s.hostFrames <- cancelFrame:
		case <-s.done:
		case <-s.ctx.Done():
		case <-ctx.Done():
			return s.forceAbortAndWait(ctx.Err())
		}
	}

	var timerChan <-chan time.Time
	if !effectiveDeadline.IsZero() {
		graceDuration := time.Until(effectiveDeadline)
		if graceDuration <= 0 {
			return s.forceAbortAndWait(context.DeadlineExceeded)
		}
		timer := time.NewTimer(graceDuration)
		defer timer.Stop()
		timerChan = timer.C
	}

	select {
	case <-s.done:
	case <-ctx.Done():
		return s.forceAbortAndWait(ctx.Err())
	case <-timerChan:
		return s.forceAbortAndWait(context.DeadlineExceeded)
	}

	prog := s.CancellationProgress()
	if prog.ForcedAbort {
		return aipapi.CancelResult{Mode: aipapi.CancelModeTransport}
	}
	if prog.OutcomeSeen {
		return outcomeCancelResult(prog)
	}
	return aipapi.CancelResult{Mode: aipapi.CancelModeNone}
}

func (s *managedStream) forceAbortAndWait(err error) aipapi.CancelResult {
	s.cancelState.markForced()
	s.cancel()
	<-s.done
	return aipapi.CancelResult{Mode: aipapi.CancelModeTransport, Err: err}
}

func (s *managedStream) onPluginFrame(frame backendplugin.ServerFrame) error {
	if s.terminalSeen.Load() {
		return backendplugin.ErrEventAfterTerminal
	}
	if s.maxFrame > 0 {
		if err := backendplugin.ValidateServerFrameSize(frame, uint64(s.maxFrame)); err != nil {
			if errors.Is(err, backendplugin.ErrOversizedMessage) {
				return ProtocolViolation(err)
			}
			return err
		}
	}
	if err := s.validator.Push(frame); err != nil {
		return err
	}
	switch frame.Kind {
	case backendplugin.ServerFrameAccepted, backendplugin.ServerFrameDiagnostic:
		return nil
	case backendplugin.ServerFrameCancelOutcome:
		if !backendplugin.CancellationHandshakeNegotiated(s.opt.Negotiation) {
			return ProtocolViolation(backendplugin.ErrInvalidFrame)
		}
		if frame.CancelOutcome == nil {
			return ProtocolViolation(backendplugin.ErrInvalidFrame)
		}
		s.cancelState.observeOutcome(frame.CancelOutcome)
		return nil
	case backendplugin.ServerFramePromptCacheObservation:
		if !backendplugin.PromptCacheNegotiated(s.opt.Negotiation) {
			return ProtocolViolation(backendplugin.ErrPromptCacheUnsupported)
		}
		// Transport boundary: a plugin-side observation may only claim the
		// exact attempt A-leg and transport leg pseudonym this host supplied,
		// with the instance identity it was configured under. A non-empty
		// conflicting claim is rejected before the runtime can bind it; the
		// runtime later maps the validated pseudonym to the real B-leg.
		bound, err := execbackend.ReconcilePromptCacheObservationLineage(*frame.PromptCacheObservation, execbackend.PromptCacheObservationLineage{
			ALegID:            s.lineageALegID,
			BLegID:            s.lineageBLegID,
			BackendInstanceID: s.opt.InstanceID,
		})
		if err != nil {
			return ProtocolViolation(err)
		}
		s.promptCacheMu.Lock()
		err = s.promptCacheBuffer.Add(bound)
		s.promptCacheMu.Unlock()
		if err != nil {
			return ProtocolViolation(err)
		}
		return nil
	case backendplugin.ServerFrameAccountingEvidence:
		if frame.AccountingV2 != nil {
			if !backendplugin.AccountingEvidenceV2Negotiated(s.opt.Negotiation) {
				return ProtocolViolation(backendplugin.ErrAccountingEvidenceV2Unsupported)
			}
			if s.claims == nil {
				// A managed stream without the host-binding state cannot admit
				// a connector claim. Fail closed instead of dropping it
				// silently.
				return ProtocolViolation(backendplugin.ErrInvalidFrame)
			}
			reason := s.claims.accept(*frame.AccountingV2, time.Now().UTC())
			released := s.claims.takeReleasedRejections()
			if reason == "" && len(released) > 0 {
				// A claim the bind released and rejected is a rejection of this
				// attempt's economic evidence: the policy owner sees it like a
				// rejection of the frame's own claim.
				reason = released[0]
			}
			if reason == "" {
				return nil
			}
			return s.connectorRejectionOutcome(reason)
		}
		if !slices.Contains(s.opt.Negotiation.EnabledFeatures, backendplugin.FeatureAccountingEvidence) {
			return ProtocolViolation(backendplugin.ErrInvalidFrame)
		}
		ev, err := accountingEvidenceToEvent(frame.Accounting)
		if err != nil {
			return ProtocolViolation(err)
		}
		s.usageMu.Lock()
		if len(s.usageEvidence) >= maxBufferedUsageEvidence {
			s.usageMu.Unlock()
			return ProtocolViolation(backendplugin.ErrOversizedMessage)
		}
		s.usageEvidence = append(s.usageEvidence, ev)
		s.usageMu.Unlock()
		return nil
	case backendplugin.ServerFrameEvent:
		if err := backendplugin.RequireExactOpenResponsesEventABISupport(s.opt.Negotiation, frame.Event); err != nil {
			return err
		}
		ev, err := eventToAipapi(frame.Event)
		if err != nil {
			return err
		}
		// The SIWC connector emits one private exact marker for a served request
		// whose preview policy omitted an unsupported field. Record one fixed,
		// content-free operator warning and consume this exact event: it has no
		// client-visible meaning, and forwarding it would hit the OpenResponses
		// unknown-event path instead of preserving the continuation lifecycle.
		// Ordinary warnings, including near-miss texts, stay untouched.
		if ev.Kind == aipapi.EventWarning && ev.WarningMessage == siwcPreviewOmissionMarker {
			if !s.omissionWarningLogged {
				s.omissionWarningLogged = true
				slog.Default().Warn(siwcPreviewOmissionLogMessage, "backend_instance_id", s.opt.InstanceID)
			}
			return nil
		}
		if backendplugin.PromptCacheNegotiated(s.opt.Negotiation) {
			// The finish gate withholds response_finished until the terminal
			// frame settles the prompt-cache sideband. A later canonical event
			// would sit behind the withheld finish and could fill the bounded
			// event channel, blocking the Execute pump before that terminal.
			// response_finished ends the canonical response sequence, so reject
			// the invalid frame explicitly instead of risking the stall.
			if s.promptCacheFinishQueued.Load() {
				return ProtocolViolation(errCanonicalEventAfterFinish)
			}
			if ev.Kind == aipapi.EventResponseFinished {
				s.promptCacheFinishQueued.Store(true)
			}
		}
		if aipapi.OutputCommitted(ev) && !s.commitOutput() {
			// A strict refusal owns the attempt: the output must not commit
			// and the content must not reach the client. The refusal is
			// delivered by the receive boundary.
			return nil
		}
		select {
		case s.events <- ev:
			return nil
		case <-s.ctx.Done():
			return s.ctx.Err()
		}
	case backendplugin.ServerFrameTerminal:
		if !s.terminalSeen.CompareAndSwap(false, true) {
			return backendplugin.ErrMultipleTerminals
		}
		s.promptCacheMu.Lock()
		if frame.Terminal != nil && frame.Terminal.Status == backendplugin.TerminalSuccess {
			s.promptCacheBuffer.Commit()
		} else {
			s.promptCacheBuffer.Discard()
		}
		s.promptCacheMu.Unlock()
		if frame.Terminal != nil && frame.Terminal.Error != nil {
			cerr := sanitizePluginError(frame.Terminal.Error)
			if s.outputCommitted() {
				cerr.OutputCommitted = true
				cerr.Retryable = false
			}
			s.mu.Lock()
			s.recvErr = cerr
			s.mu.Unlock()
		}
		// The terminal is the connector's successful (or failed) outcome: only
		// now is the prompt-cache sideband's disposition final, and any terminal
		// error is recorded, so a withheld response_finished may be released to
		// the consumer without overtaking the error state it must preserve.
		s.closePromptCacheOutcome()
		return nil
	default:
		return backendplugin.ErrUnknownFrameKind
	}
}

// connectorRejectionOutcome applies the host's connector evidence policy to one
// rejected claim.
//
// A rejection detected after output commitment never raises an error: the claim
// is rejected, the capture loss is recorded, and the already committed stream
// continues unchanged to its actual terminal. Before commitment the strict
// policy raises the typed refusal, which the existing reclassification keeps
// as a recognized non-retryable terminal. The callback performs no close, no
// cancel and no session close, so the refusal can never be reclassified as a
// cancellation.
func (s *managedStream) connectorRejectionOutcome(reason execbackend.ConnectorEvidenceRejection) error {
	if s == nil || reason == "" {
		return nil
	}
	if s.outputCommitted() {
		return nil
	}
	if s.opt.ConnectorEconomicEvidencePolicy != execbackend.ConnectorEconomicEvidenceStrict {
		return nil
	}
	return hostEvidenceRefused(string(reason))
}

// BindEconomicEvidence supplies the trusted host facts of the attempt and
// drains the bounded pre-bind queue exactly once. Claims that arrived before
// the bind keep their original host receipt time.
func (s *managedStream) BindEconomicEvidence(identity coremetering.ObservationIdentity) {
	if s == nil {
		return
	}
	s.economicMu.Lock()
	defer s.economicMu.Unlock()
	reasons := s.claims.bindTrusted(identity, s.lineageBLegID, identity.ReceivedAt)
	s.publishPendingRefusal(reasons)
}

// publishPendingRefusal publishes the strict pre-commit refusal of the
// connector evidence the host could not admit.
//
// The refusal is published, never raised: the adapter performs no close, no
// cancel and no session close while publishing it. The existing owner closes
// the stream after the refusal has been published and classified.
func (s *managedStream) publishPendingRefusal(reasons []execbackend.ConnectorEvidenceRejection) {
	if len(reasons) == 0 {
		return
	}
	if s.opt.ConnectorEconomicEvidencePolicy != execbackend.ConnectorEconomicEvidenceStrict {
		return
	}
	refusal := hostEvidenceRefused(string(reasons[0]))
	if refusal == nil {
		return
	}
	// The publication and the output commitment are one decision: a refusal
	// published after the output committed is dropped and the committed stream
	// is preserved.
	s.publishRefusal(refusal)
}

func (s *managedStream) DrainPromptCacheObservations() []promptcache.Observation {
	if s == nil {
		return nil
	}
	s.promptCacheMu.Lock()
	defer s.promptCacheMu.Unlock()
	return s.promptCacheBuffer.DrainPromptCacheObservations()
}

func (s *managedStream) DrainUsageEvidence() []aipapi.Event {
	if s == nil {
		return nil
	}
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	out := append([]aipapi.Event(nil), s.usageEvidence...)
	s.usageEvidence = nil
	return out
}

// DrainAccountingEvidenceV2 returns the host-bound canonical observations and
// never publishes them on the client-visible event stream. A claim from a peer
// that did not opt into the host-bound contract is never retained here: it is
// dropped with an explicit capture loss instead.
func (s *managedStream) DrainAccountingEvidenceV2() []backendplugin.AccountingEvidenceV2 {
	if s == nil {
		return nil
	}
	admitted, _ := s.drainBoundClaims()
	if len(admitted) == 0 {
		return nil
	}
	out := make([]backendplugin.AccountingEvidenceV2, 0, len(admitted))
	for _, observation := range admitted {
		out = append(out, backendplugin.AccountingEvidenceV2{
			Observation: observation.Clone(),
			Coverage:    backendplugin.EvidenceCoverageComplete,
		})
	}
	return out
}

// DrainEconomicEvidence is the neutral naming alias used by shared connector
// support. Both methods drain the same buffer and therefore cannot duplicate a
// provider charge.
func (s *managedStream) DrainEconomicEvidence() []backendplugin.AccountingEvidenceV2 {
	return s.DrainAccountingEvidenceV2()
}

// DrainEconomicEvidenceRecords is the internal core projection. It retains
// the transport coverage disposition while handing the canonical observation
// to runtime without importing the backend-plugin wire package into core.
func (s *managedStream) DrainEconomicEvidenceRecords() []execbackend.EconomicEvidence {
	admitted, rejected := s.drainBoundClaims()
	if len(admitted) == 0 && len(rejected) == 0 {
		return nil
	}
	out := make([]execbackend.EconomicEvidence, 0, len(admitted)+len(rejected))
	for _, observation := range admitted {
		out = append(out, execbackend.EconomicEvidence{
			Observation: observation.Clone(),
			Coverage:    string(backendplugin.EvidenceCoverageComplete),
		})
	}
	out = append(out, rejected...)
	return out
}

// drainBoundClaims releases the claims that are still waiting for their
// predecessor and returns the admitted observations plus the closed rejection
// records of the attempt.
func (s *managedStream) drainBoundClaims() ([]metering.Observation, []execbackend.EconomicEvidence) {
	if s == nil {
		return nil, nil
	}
	s.economicMu.Lock()
	defer s.economicMu.Unlock()
	// The connector evidence window closes when no further connector frame can
	// arrive: the plugin's terminal frame was seen or the Execute session
	// returned. An ordinary runtime drain is not that boundary, and releasing
	// the held claims there would reject a predecessor that is still on its
	// way.
	if s.terminalSeen.Load() || s.executeFinished.Load() {
		s.claims.releaseHeld()
	}
	return s.claims.drain()
}

// DrainEconomicObservations is the core-facing projection of the validated
// host-only V2 sideband. Coverage is a transport disposition and the
// canonical observation remains the durable economic payload.
func (s *managedStream) DrainEconomicObservations() []metering.Observation {
	admitted, _ := s.drainBoundClaims()
	if len(admitted) == 0 {
		return nil
	}
	out := make([]metering.Observation, 0, len(admitted))
	for _, observation := range admitted {
		out = append(out, observation.Clone())
	}
	return out
}

var (
	_ backendplugin.AccountingEvidenceV2Source = (*managedStream)(nil)
	_ backendplugin.EconomicEvidenceSource     = (*managedStream)(nil)
	_ execbackend.EconomicEvidenceSource       = (*managedStream)(nil)
	_ metering.ObservationSource               = (*managedStream)(nil)
)

func accountingEvidenceToEvent(e *backendplugin.AccountingEvidence) (aipapi.Event, error) {
	if e == nil {
		return aipapi.Event{}, backendplugin.ErrInvalidFrame
	}
	if err := backendplugin.ValidateAccountingEvidence(*e); err != nil {
		return aipapi.Event{}, err
	}
	ev := aipapi.Event{Kind: aipapi.EventUsageDelta, UsagePresence: e.Presence, Accounting: aipapi.UsageAccountingMetadata{Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSource(e.Source), Authority: aipapi.UsageAuthority(e.Authority), DedupeKey: e.DedupeKey, InputTokensExcludeCacheRead: e.InputTokensExcludeCacheRead, InputTokensExcludeCacheWrite: e.InputTokensExcludeCacheWrite}}
	if e.InputTokens != nil {
		ev.InputTokens = int(*e.InputTokens)
	}
	if e.OutputTokens != nil {
		ev.OutputTokens = int(*e.OutputTokens)
	}
	if e.CacheReadTokens != nil {
		ev.CacheReadTokens = int(*e.CacheReadTokens)
	}
	if e.CacheWriteTokens != nil {
		ev.CacheWriteTokens = int(*e.CacheWriteTokens)
	}
	if e.ReasoningTokens != nil {
		ev.ReasoningTokens = int(*e.ReasoningTokens)
	}
	if e.TotalTokens != nil {
		ev.TotalTokens = int(*e.TotalTokens)
	}
	return ev, nil
}

func (s *managedStream) drainStderr(r io.Reader) {
	maxBytes := s.opt.MaxStderrBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxStderrBytes
	}
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			s.mu.Lock()
			remain := maxBytes - s.stderrBytes
			if remain > 0 {
				if n > remain {
					n = remain
				}
				s.stderrBytes += n
			}
			s.mu.Unlock()
		}
		if err != nil {
			return
		}
		s.mu.Lock()
		full := s.stderrBytes >= maxBytes
		s.mu.Unlock()
		if full {
			_, _ = io.Copy(io.Discard, r)
			return
		}
	}
}

func (s *managedStream) OutputCommitted() bool { return s.outputCommitted() }
func (s *managedStream) Attempts() int64       { return s.stats.ProviderAttempts.Load() }

type bridgeExecuteStream struct {
	ctx       context.Context
	closeCh   chan struct{}
	closeOnce sync.Once
	recv      <-chan backendplugin.ClientFrame
	send      func(backendplugin.ServerFrame) error
	neg       backendplugin.Negotiation
}

var (
	_ backendplugin.ExecuteStream               = (*bridgeExecuteStream)(nil)
	_ backendplugin.OptionalExecuteStreamCloser = (*bridgeExecuteStream)(nil)
	_ backendplugin.OptionalNegotiatedStream    = (*bridgeExecuteStream)(nil)
)

func (b *bridgeExecuteStream) Negotiation() backendplugin.Negotiation { return b.neg }

func (b *bridgeExecuteStream) Context() context.Context { return b.ctx }

// Close unblocks a host Execute pump when the gRPC server finishes before the
// adapter's client-frame channel has been closed.
func (b *bridgeExecuteStream) Close() error {
	b.closeOnce.Do(func() { close(b.closeCh) })
	return nil
}

func (b *bridgeExecuteStream) Recv() (backendplugin.ClientFrame, error) {
	select {
	case <-b.ctx.Done():
		return backendplugin.ClientFrame{}, b.ctx.Err()
	case <-b.closeCh:
		return backendplugin.ClientFrame{}, io.EOF
	case fr, ok := <-b.recv:
		if !ok {
			return backendplugin.ClientFrame{}, io.EOF
		}
		return fr, nil
	}
}

func (b *bridgeExecuteStream) Send(frame backendplugin.ServerFrame) error {
	return b.send(frame)
}
