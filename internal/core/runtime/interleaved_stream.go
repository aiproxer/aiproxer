package runtime

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"

	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

type interleavedPhase int

const (
	interleavedPhaseUnknown interleavedPhase = iota
	interleavedPhaseThinker
	interleavedPhaseExecutor
)

// interleavedContinuationStream sequences thinker capture and executor continuation
// within one logical A-leg. Hidden mode drains thinker output; visible mode surfaces
// sanitized reasoning deltas before executor output.
//
// Recv is single-consumer: callers must not invoke Recv concurrently on the same stream.
type interleavedContinuationStream struct {
	thinker  *retryRecvStream
	executor *retryRecvStream
	phase    interleavedPhase

	turn  InterleavedTurn
	state interleavedstate.State

	surfaceVisible  bool
	responseStarted bool

	mu               sync.Mutex
	pending          []aipapi.Event
	visibleCommitted bool
	finished         bool
	memoPersisted    bool
	// processorDone is non-nil exactly while one admitted Observe, FlushVisible,
	// or memo Finalize call is in flight. It is the exclusive processor slot: a
	// channel, not a goroutine, so joining never starts background work. It is
	// nil while idle.
	processorDone chan struct{}
	// interruptedMemoPending records that a withdrawal still owes one
	// interrupted memo capture. The admission slot owns the claim, so a
	// duplicate Cancel/Close joins or defers instead of finalizing again.
	interruptedMemoPending bool
	// memoFinalizeAttempted records that one memo finalization attempt was
	// admitted under the exclusive slot. It is attempt consumption, kept
	// separate from memoPersisted success publication: once an attempt was
	// admitted it is never admitted again, even when it produced an empty memo
	// or a real store error. An Observe/Flush still in flight has not finalized
	// yet, so its deferred interrupted request stays claimable exactly once.
	memoFinalizeAttempted bool
	transitionInFlight    bool
	cancelPending         bool
	closePending          bool
}

var (
	_                                aipapi.EventStream        = (*interleavedContinuationStream)(nil)
	_                                aipapi.ManagedEventStream = (*interleavedContinuationStream)(nil)
	errUnknownInterleavedPhase                                 = errors.New("runtime: unknown interleaved phase")
	errInterleavedClientClosed                                 = errors.New("client closed")
	errInterleavedProcessorWithdrawn                           = errors.New("runtime: interleaved thinker processor withdrawn")
)

type hiddenInterleavedStream = interleavedContinuationStream

func newInterleavedContinuationStream(thinker *retryRecvStream, turn InterleavedTurn, state interleavedstate.State) *interleavedContinuationStream {
	if thinker != nil && thinker.terminal != nil {
		thinker.terminal.deferALegEndToOuter()
	}
	s := &interleavedContinuationStream{
		thinker: thinker,
		phase:   interleavedPhaseThinker,
		turn:    turn,
		state:   state,
	}
	if turn != nil && turn.Visible() {
		s.surfaceVisible = true
	}
	return s
}

func newHiddenInterleavedStream(thinker *retryRecvStream, turn InterleavedTurn, state interleavedstate.State) *hiddenInterleavedStream {
	return newInterleavedContinuationStream(thinker, turn, state)
}

func newVisibleInterleavedStream(thinker *retryRecvStream, turn InterleavedTurn, state interleavedstate.State) *interleavedContinuationStream {
	s := newHiddenInterleavedStream(thinker, turn, state)
	s.surfaceVisible = true
	return s
}

func (s *interleavedContinuationStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if s == nil {
		return aipapi.Event{}, errNilRetryRecvStream
	}
	if ctx == nil {
		return aipapi.Event{}, aipapi.ErrNilContext
	}
	s.mu.Lock()
	if s.finished {
		s.mu.Unlock()
		return aipapi.Event{}, io.EOF
	}
	phase := s.phase
	s.mu.Unlock()

	switch phase {
	case interleavedPhaseThinker:
		return s.recvThinker(ctx)
	case interleavedPhaseExecutor:
		return s.recvExecutor(ctx)
	default:
		return aipapi.Event{}, errUnknownInterleavedPhase
	}
}

// popPending releases at most one queued client frame. ctx is the original Recv
// caller context, passed in rather than stored, so the live predicate can also
// see caller cancellation and the shared A-leg cause. Withdrawal discards every
// queued kind - lifecycle frames included - not only reasoning.
func (s *interleavedContinuationStream) popPending(ctx context.Context) (aipapi.Event, bool) {
	if s.liveWithdrawalError(ctx) != nil {
		s.mu.Lock()
		s.pending = nil
		s.mu.Unlock()
		return aipapi.Event{}, false
	}
	s.mu.Lock()
	if s.withdrawalRequestedLocked() {
		s.pending = nil
		s.mu.Unlock()
		return aipapi.Event{}, false
	}
	if len(s.pending) == 0 {
		s.mu.Unlock()
		return aipapi.Event{}, false
	}
	ev := s.pending[0]
	s.pending[0] = aipapi.Event{} // zero out for GC before advancing
	s.pending = s.pending[1:]
	if len(s.pending) == 0 {
		s.pending = nil
	}
	reasoning := ev.Kind == aipapi.EventReasoningDelta
	s.mu.Unlock()
	if reasoning {
		// Accounting and recovery effects stay outside mu; visibleCommitted is
		// published under mu as its own fact. The post-effect fence then keeps a
		// withdrawal that won while they ran from releasing the frame.
		if s.liveWithdrawalError(ctx) != nil {
			return aipapi.Event{}, false
		}
		s.recordVisibleOutput(ev)
		if s.liveWithdrawalError(ctx) != nil {
			return aipapi.Event{}, false
		}
	}
	return ev, true
}

// withdrawalRequestedLocked reports whether client withdrawal already closed the
// wrapper's admission, output, and continuation boundaries.
func (s *interleavedContinuationStream) withdrawalRequestedLocked() bool {
	return s.cancelPending || s.closePending || s.finished
}

// withdrawalError snapshots the withdrawal reason under mu. It is a live fence
// check: an already-admitted external call may still finish late, but no new
// output, memo handoff, or executor admission may start after it reports one.
func (s *interleavedContinuationStream) withdrawalError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancelPending {
		return context.Canceled
	}
	if s.closePending {
		return errInterleavedClientClosed
	}
	if s.finished {
		return io.EOF
	}
	return nil
}

// liveWithdrawalError is the single full wrapper-output withdrawal predicate.
// It combines the sticky wrapper state snapshotted under mu with the original
// Recv caller context and the shared A-leg cause read outside mu. Sticky flags
// alone are insufficient: the client can withdraw by cancelling its own Recv
// context or by cancelling the shared A-leg without ever calling Cancel/Close,
// and an admitted context-free callback may return late into that window.
// ctx is always the caller's lexical context, never stored.
func (s *interleavedContinuationStream) liveWithdrawalError(ctx context.Context) error {
	if err := s.withdrawalError(); err != nil {
		return err
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return s.sharedCancellationErr()
}

// sharedCancellationErr reports the shared A-leg cause. A canceled A-leg closes
// processor admission even before the client's own Cancel or Close arrives.
func (s *interleavedContinuationStream) sharedCancellationErr() error {
	if s.thinker == nil || s.thinker.terminal == nil || !s.thinker.terminal.hasALeg() {
		return nil
	}
	return s.thinker.terminal.aLegErr()
}

// admitThinkerProcessor reserves the exclusive processor slot for one admitted
// Observe, FlushVisible, or memo Finalize call. Recv is the single consumer, so
// the slot admits at most one such call at a time; Finalize shares it with
// Observe/Flush instead of racing them.
func (s *interleavedContinuationStream) admitThinkerProcessor(ctx context.Context) (chan struct{}, error) {
	if s == nil {
		return nil, errUnknownInterleavedPhase
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if err := s.sharedCancellationErr(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.phase != interleavedPhaseThinker || s.turn == nil {
		return nil, errInterleavedProcessorWithdrawn
	}
	if s.withdrawalRequestedLocked() || s.memoPersisted || s.processorDone != nil {
		return nil, errInterleavedProcessorWithdrawn
	}
	done := make(chan struct{})
	s.processorDone = done
	return done, nil
}

// completeThinkerProcessor releases only the matching admission slot and closes
// its channel under mu. No caller ever closes another caller's channel.
func (s *interleavedContinuationStream) completeThinkerProcessor(done chan struct{}) {
	if done == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.processorDone != done {
		return
	}
	s.processorDone = nil
	close(done)
}

// joinThinkerProcessor waits for admitted processor work within an established
// bounded cleanup context. It never starts a background waiter: on budget
// exhaustion the caller returns conservatively and the already-running Recv
// keeps owning the callback lifetime.
func (s *interleavedContinuationStream) joinThinkerProcessor(cleanupCtx context.Context) error {
	if cleanupCtx == nil {
		return nil
	}
	for {
		s.mu.Lock()
		done := s.processorDone
		s.mu.Unlock()
		if done == nil {
			return nil
		}
		select {
		case <-done:
		case <-cleanupCtx.Done():
			return cleanupCtx.Err()
		}
		// Loop: finalization may own the slot again after this admission ends.
	}
}

func (s *interleavedContinuationStream) recvThinker(ctx context.Context) (aipapi.Event, error) {
	for {
		if ev, ok := s.popPending(ctx); ok {
			return ev, nil
		}
		ev, err := s.thinker.Recv(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				if s.surfaceVisible && s.turn != nil {
					visibles, flushErr := s.flushThinkerVisible(ctx)
					if flushErr != nil {
						s.finishWithCleanup(ctx)
						return aipapi.Event{}, flushErr
					}
					for _, visible := range visibles {
						if visible.Kind == aipapi.EventReasoningDelta {
							s.enqueueVisibleReasoning(ctx, visible)
						}
					}
					if out, ok := s.popPending(ctx); ok {
						return out, nil
					}
				}
				// Only response_finished completion sets the request terminal's
				// accounting-finalized claim.
				// Truncated EOF / cancel / error terminals must not open an executor
				// continuation (that would race a second request/call closure).
				if s.thinker.terminal == nil || !s.thinker.terminal.accountingFinalized() {
					s.finishWithCleanup(ctx)
					return aipapi.Event{}, io.EOF
				}
				return s.beginExecutorContinuation(ctx)
			}
			if _, persistErr := s.captureInterruptedThinkerMemo(ctx); persistErr != nil {
				s.finishWithCleanup(ctx)
				return aipapi.Event{}, persistErr
			}
			s.finishWithCleanup(ctx)
			return aipapi.Event{}, err
		}
		if ev.Kind == aipapi.EventError {
			if _, persistErr := s.captureInterruptedThinkerMemo(ctx); persistErr != nil {
				s.finishWithCleanup(ctx)
				return aipapi.Event{}, persistErr
			}
			s.finishWithCleanup(ctx)
			return ev, nil
		}
		if s.turn != nil {
			visibles, observeErr := s.observeThinkerEvent(ctx, ev)
			if observeErr != nil {
				s.finishWithCleanup(ctx)
				return aipapi.Event{}, observeErr
			}
			for _, visible := range visibles {
				if visible.Kind == aipapi.EventReasoningDelta {
					s.enqueueVisibleReasoning(ctx, visible)
					if out, ok := s.popPending(ctx); ok {
						return out, nil
					}
				}
			}
		}
	}
}

// captureInterruptedThinkerMemo captures the interrupted memo a thinker Recv owes
// when its inner receive ends on a transport error or an error event. Once
// withdrawal closed admission, only a genuinely pending interrupted request may
// be claimed: a request an already-admitted Finalize consumed stays consumed
// even when that Finalize produced an empty memo or a real store error. Without
// withdrawal this is the ordinary error-path capture.
func (s *interleavedContinuationStream) captureInterruptedThinkerMemo(ctx context.Context) (interleavedstate.State, error) {
	if s.liveWithdrawalError(ctx) != nil {
		// Caller/A-leg withdrawal owns the same reconciled request an explicit
		// Cancel/Close would own, and the same permanently consumed attempt.
		s.reconcileLiveWithdrawal(ctx)
		return s.snapshotState(), nil
	}
	return s.captureAndPersistThinkerMemo(ctx, true)
}

// observeThinkerEvent forwards one thinker event to the real processor under the
// exclusive admission slot. The callback is context-free and may mutate the
// feature-owned recorder before it returns, so withdrawal closes admission
// before the call and is rechecked after it: a late completion releases its
// slot, discards its visible output, and arranges the one interrupted memo
// capture it still owes.
func (s *interleavedContinuationStream) observeThinkerEvent(ctx context.Context, ev aipapi.Event) ([]aipapi.Event, error) {
	done, err := s.admitThinkerProcessor(ctx)
	if err != nil {
		// Admission can already be closed by a live withdrawal that never called
		// Cancel/Close. That wrapper terminal is still owed the one interrupted
		// memo, so reconcile it here rather than leaving it uncaptured.
		if s.liveWithdrawalError(ctx) != nil {
			s.reconcileLiveWithdrawal(ctx)
		}
		return nil, err
	}
	visibles, observeErr := s.turn.ObserveThinkerEvent(ev)
	s.completeThinkerProcessor(done)
	if observeErr != nil {
		// Close may have timed out with an interrupted request still owed.
		// Consume only that existing request after releasing Observe admission.
		s.finalizePendingInterruptedMemo(ctx)
		return nil, observeErr
	}
	// The callback is context-free, so a late return can land after the caller
	// or the shared A-leg withdrew. The live predicate owns that window: the
	// returned visibles are discarded and the one owed interrupted capture is
	// reconciled through the same exclusive slot.
	if withdrawErr := s.liveWithdrawalError(ctx); withdrawErr != nil {
		s.reconcileLiveWithdrawal(ctx)
		return nil, withdrawErr
	}
	return visibles, nil
}

// flushThinkerVisible drains the sanitizer under the same exclusive slot as
// Observe and memo Finalize.
func (s *interleavedContinuationStream) flushThinkerVisible(ctx context.Context) ([]aipapi.Event, error) {
	done, err := s.admitThinkerProcessor(ctx)
	if err != nil {
		if s.liveWithdrawalError(ctx) != nil {
			s.reconcileLiveWithdrawal(ctx)
		}
		return nil, err
	}
	visibles := s.turn.FlushVisible()
	s.completeThinkerProcessor(done)
	if withdrawErr := s.liveWithdrawalError(ctx); withdrawErr != nil {
		s.reconcileLiveWithdrawal(ctx)
		return nil, withdrawErr
	}
	return visibles, nil
}

// reconcileLiveWithdrawal arranges the single interrupted memo a live caller or
// shared-A-leg withdrawal owes when no explicit wrapper Cancel/Close armed the
// request. It reuses the existing Mu-owned request bit and the exclusive slot, so
// a consumed or already-attempted finalization is never recreated. External work
// happens outside mu.
func (s *interleavedContinuationStream) reconcileLiveWithdrawal(ctx context.Context) {
	s.mu.Lock()
	s.requestInterruptedMemoLocked()
	s.mu.Unlock()
	s.finalizePendingInterruptedMemo(ctx)
}

// enqueueVisibleReasoning admits one sanitized reasoning frame plus the lifecycle
// frames that precede it. ctx is the original Recv caller context so a withdrawal
// that is only visible there or in the shared A-leg still closes admission.
func (s *interleavedContinuationStream) enqueueVisibleReasoning(ctx context.Context, ev aipapi.Event) {
	if s.liveWithdrawalError(ctx) != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.withdrawalRequestedLocked() {
		return
	}
	if !s.responseStarted {
		s.pending = append(
			s.pending,
			aipapi.Event{Kind: aipapi.EventResponseStarted},
			aipapi.Event{Kind: aipapi.EventMessageStarted},
		)
		s.responseStarted = true
	}
	s.pending = append(s.pending, ev)
}

// recordVisibleOutput publishes the external accounting and recovery effects of
// one surfaced reasoning delta. Those effects are deliberately outside mu; only
// the visibleCommitted fact is published under it.
func (s *interleavedContinuationStream) recordVisibleOutput(ev aipapi.Event) {
	if s.thinker == nil {
		return
	}
	s.thinker.terminal.markOutputCommittedForAttempt(ev, s.thinker.attempt.snapshot(), s.thinker.recovery)
	s.thinker.attempt.require().observeAccountingClientEvent(s.thinker.responsePipeline.nowTime(), ev)
	if s.thinker.recovery != nil && s.thinker.recovery.recoverPolicy != nil {
		s.thinker.recovery.recoverPolicy.ObserveClientEvent(ev, s.thinker.responsePipeline.nowTime())
	}
	s.mu.Lock()
	s.visibleCommitted = true
	s.mu.Unlock()
}

// captureAndPersistThinkerMemo claims the exclusive processor slot for one memo
// finalization. Duplicate cleanup joins or defers through the withdrawal path
// instead of reading memoPersisted as completed while a processor is active, so
// memoPersisted is published only after a successful Finalize.
func (s *interleavedContinuationStream) captureAndPersistThinkerMemo(ctx context.Context, interrupted bool) (interleavedstate.State, error) {
	done, state, visibleCommitted, claimed := s.claimMemoFinalize(interrupted, false)
	if !claimed {
		return s.snapshotState(), nil
	}
	defer s.completeMemoProcessor(done)
	return state, s.runMemoFinalize(ctx, interrupted, visibleCommitted)
}

// finalizePendingInterruptedMemo claims and runs the single interrupted memo
// capture a withdrawal still owes. It runs only when that request is pending and
// the slot is idle, so a late Observe/Flush completion and duplicate cleanup
// cannot both finalize the same prefix.
func (s *interleavedContinuationStream) finalizePendingInterruptedMemo(ctx context.Context) {
	done, _, visibleCommitted, claimed := s.claimMemoFinalize(true, true)
	if !claimed {
		return
	}
	defer s.completeMemoProcessor(done)
	if err := s.runMemoFinalize(ctx, true, visibleCommitted); err != nil {
		if s.thinker != nil && s.thinker.recovery != nil {
			s.thinker.recovery.logMemoPersistFailed(ctx, s.thinker.facts.traceID, err)
		}
	}
}

// claimMemoFinalize atomically claims the exclusive slot for one memo
// Finalize and snapshots the facts it publishes. State, turn, and
// visibleCommitted are read under mu; every external call happens after unlock.
// A normal capture is new work on the normal handoff, so its claim rejects a
// withdrawal that already closed that boundary. An interrupted claim consumes
// the deferred withdrawal request exactly once, and recording the attempt means
// a Finalize already admitted before withdrawal consumes it on completion, so
// no second relabeling Finalize can follow it.
func (s *interleavedContinuationStream) claimMemoFinalize(interrupted, requireRequest bool) (chan struct{}, interleavedstate.State, bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.phase != interleavedPhaseThinker || s.turn == nil || s.thinker == nil || s.thinker.recovery == nil {
		return nil, s.state, false, false
	}
	if s.memoPersisted || s.memoFinalizeAttempted || s.processorDone != nil {
		return nil, s.state, false, false
	}
	if interrupted {
		if requireRequest && !s.interruptedMemoPending {
			return nil, s.state, false, false
		}
		s.interruptedMemoPending = false
	} else if s.clientWithdrawalLocked() != nil {
		return nil, s.state, false, false
	}
	done := make(chan struct{})
	s.processorDone = done
	s.memoFinalizeAttempted = true
	return done, s.state, s.visibleCommitted, true
}

// completeMemoProcessor releases the claimed slot and clears any interrupted
// memo request the completed Finalize consumed, including on error.
func (s *interleavedContinuationStream) completeMemoProcessor(done chan struct{}) {
	if done == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.interruptedMemoPending = false
	if s.processorDone != done {
		return
	}
	s.processorDone = nil
	close(done)
}

func (s *interleavedContinuationStream) snapshotState() interleavedstate.State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// runMemoFinalize performs the already-admitted feature-owned Finalize plus its
// bounded logging and steering publication. mu is never held across any of it,
// so an admitted private effect can finish late; only later fences prevent it
// from becoming a normal handoff or executor admission.
func (s *interleavedContinuationStream) runMemoFinalize(ctx context.Context, interrupted, visibleCommitted bool) error {
	persistCtx := ctx
	if interrupted {
		var cleanupCancel context.CancelFunc
		persistCtx, cleanupCancel = detachedCleanupContext(ctx, cancelLosersTimeout)
		defer cleanupCancel()
	}

	memo, err := s.turn.FinalizeThinkerStatus(persistCtx, interrupted, visibleCommitted)
	if err != nil {
		return err
	}
	if strings.TrimSpace(memo.Text) == "" {
		reason := "empty_memo"
		if interrupted {
			reason = "stream_interrupted"
		} else if !memo.HadContent {
			reason = "no_extractable_memo"
		}
		if s.thinker != nil && s.thinker.recovery != nil {
			s.thinker.recovery.logMemoStoreSkipped(persistCtx, s.thinker.facts.traceID, reason, interrupted)
		}
		return nil
	}
	s.thinker.recovery.logMemoCaptured(persistCtx, s.thinker.facts.traceID, memo)
	// Success publication is separate from finalization-attempt consumption:
	// the durable memo write already committed inside the admitted call, so it
	// is recorded whether or not a later overlay is still admitted.
	s.markMemoPersisted()
	if !interrupted {
		s.thinker.recovery.logPhaseTransition(persistCtx, s.thinker.facts.traceID)
		// Fence the normal steering overlay as a NEW external effect: a
		// withdrawal that won while this already-admitted Finalize was running
		// must not start another one. The private store write above is not
		// rolled back or relabeled; the interrupted overlay stays allowed.
		if s.memoOverlayFence(persistCtx) != nil {
			return nil
		}
	}
	if err := s.thinker.recovery.publishMemoSteeringOverlay(persistCtx, s.thinker.facts.aLegID, s.thinker.facts.ingressCall, s.thinker.facts.conversationSnapshot, memo.Text); err != nil {
		s.thinker.recovery.logMemoSteeringSkipped(persistCtx, s.thinker.facts.traceID)
	}
	return nil
}

// markMemoPersisted publishes the durable memo success fact under mu.
func (s *interleavedContinuationStream) markMemoPersisted() {
	s.mu.Lock()
	s.memoPersisted = true
	s.mu.Unlock()
}

// memoOverlayFence reports whether withdrawal already closed the normal memo
// handoff. It deliberately uses the explicit client-withdrawal flags rather than
// the generic finished bit: a normally completed turn is not a client
// withdrawal and must still be able to reach its own terminal path. It also
// honors the original caller context and the shared A-leg, so an explicit
// wrapper Cancel is caught even though this call's context is often detached.
// It holds mu only for the flag snapshot and never spans an external call.
func (s *interleavedContinuationStream) memoOverlayFence(ctx context.Context) error {
	if err := s.clientWithdrawalError(); err != nil {
		return err
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return s.sharedCancellationErr()
}

func (s *interleavedContinuationStream) beginExecutorContinuation(ctx context.Context) (aipapi.Event, error) {
	s.mu.Lock()
	// Withdrawal is rejected before the transition is announced, so no normal
	// continuation ever starts behind a canceled or closed client.
	if abortErr := s.clientWithdrawalLocked(); abortErr != nil {
		s.mu.Unlock()
		s.finishWithCleanup(ctx)
		return aipapi.Event{}, abortErr
	}
	s.transitionInFlight = true
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.transitionInFlight = false
		s.mu.Unlock()
	}()

	if abortErr := s.clientWithdrawalError(); abortErr != nil {
		s.finishWithCleanup(ctx)
		return aipapi.Event{}, abortErr
	}
	state, err := s.captureAndPersistThinkerMemo(ctx, false)
	if err != nil {
		s.finishWithCleanup(ctx)
		return aipapi.Event{}, err
	}
	// Fence after the admitted Finalize and its overlay and before the executor
	// open: an admitted private store effect cannot be rolled back, so this
	// check is what stops it from becoming a normal handoff.
	if abortErr := s.memoOverlayFence(ctx); abortErr != nil {
		s.finishWithCleanup(ctx)
		return aipapi.Event{}, abortErr
	}
	execStream, err := s.thinker.recovery.openInterleavedContinuation(ctx, s.thinker, state)
	if err != nil {
		s.finishWithCleanup(ctx)
		return aipapi.Event{}, err
	}
	if abortErr := s.handoffAborted(ctx); abortErr != nil {
		s.abortExecutorHandoff(ctx, execStream, abortErr)
		return aipapi.Event{}, abortErr
	}
	visibleCommitted := s.snapshotVisibleCommitted()
	if visibleCommitted {
		execStream.terminal.markCommitted(execStream.attempt.snapshot())
		if execStream.recovery != nil && execStream.recovery.ttft != nil {
			execStream.recovery.ttft.markCommitted()
		}
	}
	s.mu.Lock()
	if s.cancelPending || s.closePending || s.finished {
		var abortErr error
		switch {
		case s.cancelPending:
			abortErr = context.Canceled
		case s.closePending:
			abortErr = errInterleavedClientClosed
		default:
			abortErr = io.EOF
		}
		// Clear transitionInFlight before unlock so concurrent Cancel/Close take the
		// post-assignment path if abort races with a late observer; abort owns finished.
		s.transitionInFlight = false
		s.mu.Unlock()
		s.abortExecutorHandoff(ctx, execStream, abortErr)
		return aipapi.Event{}, abortErr
	}
	s.executor = execStream
	s.phase = interleavedPhaseExecutor
	s.state = state
	// Clear atomically with executor/phase assignment before first executor Recv so
	// Cancel/Close during that Recv cancel the opened continuation (not pending-only).
	s.transitionInFlight = false
	s.mu.Unlock()
	return s.recvExecutor(ctx)
}

// clientWithdrawalLocked reports only an explicit client withdrawal, which is
// what fences continuation admission. A generic finished bit still means the
// stream is closed for output.
func (s *interleavedContinuationStream) clientWithdrawalLocked() error {
	if s.cancelPending {
		return context.Canceled
	}
	if s.closePending {
		return errInterleavedClientClosed
	}
	return nil
}

func (s *interleavedContinuationStream) clientWithdrawalError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clientWithdrawalLocked()
}

func (s *interleavedContinuationStream) snapshotVisibleCommitted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.visibleCommitted
}

func (s *interleavedContinuationStream) handoffAborted(ctx context.Context) error {
	s.mu.Lock()
	finished := s.finished
	cancelPending := s.cancelPending
	closePending := s.closePending
	s.mu.Unlock()
	// Pending cancel/close from transition Cancel/Close must win over a generic
	// finished bit so abortExecutorHandoff picks CommandCancel vs CommandClose.
	if cancelPending {
		return context.Canceled
	}
	if closePending {
		return errInterleavedClientClosed
	}
	if finished {
		return io.EOF
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.thinker != nil && s.thinker.terminal != nil && s.thinker.terminal.hasALeg() {
		if err := s.thinker.terminal.aLegErr(); err != nil {
			return err
		}
	}
	return nil
}

func (s *interleavedContinuationStream) finishWithCleanup(ctx context.Context) {
	s.mu.Lock()
	if s.finished {
		s.mu.Unlock()
		return
	}
	phase := s.phase
	thinker := s.thinker
	executor := s.executor
	closePending := s.closePending
	cancelPending := s.cancelPending
	s.mu.Unlock()

	cleanupCtx, cleanupCancel := detachedCleanupContext(ctx, cancelLosersTimeout)
	defer cleanupCancel()

	if phase == interleavedPhaseThinker && thinker != nil {
		attempt := thinker.attempt.snapshot()
		if closePending {
			thinker.terminal.closeClose(cleanupCtx, thinker.facts.terminalFacts(), attempt, thinker.responsePipeline)
		} else if cancelPending {
			thinker.terminal.terminalizeCancellation(cleanupCtx, thinker.facts.terminalFacts(), attempt, thinker.responsePipeline, "canceled", false)
		} else if ctx != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			thinker.terminal.terminalizeTimeout(cleanupCtx, thinker.facts.terminalFacts(), attempt, thinker.responsePipeline)
		} else if ctx != nil && errors.Is(ctx.Err(), context.Canceled) {
			thinker.terminal.terminalizeCancellation(cleanupCtx, thinker.facts.terminalFacts(), attempt, thinker.responsePipeline, "canceled", false)
		} else if thinker.terminal != nil && thinker.terminal.hasALeg() && errors.Is(thinker.terminal.aLegErr(), context.DeadlineExceeded) {
			thinker.terminal.terminalizeTimeout(cleanupCtx, thinker.facts.terminalFacts(), attempt, thinker.responsePipeline)
		} else if thinker.terminal != nil && thinker.terminal.hasALeg() && errors.Is(thinker.terminal.aLegErr(), leglifecycle.ErrALegCanceled) {
			thinker.terminal.terminalizeCancellation(cleanupCtx, thinker.facts.terminalFacts(), attempt, thinker.responsePipeline, "canceled", false)
		} else if thinker.terminal != nil && !thinker.terminal.finished() {
			if !thinker.terminal.accountingFinalized() {
				thinker.terminal.terminalizeEOF(cleanupCtx, thinker.facts.terminalFacts(), attempt, thinker.responsePipeline)
			} else {
				thinker.terminal.terminalizePartialFailure(cleanupCtx, thinker.responsePipeline, thinker.facts.terminalFacts(), attempt, sdkterminal.CommandPartialError, "interleaved continuation failure", errors.New("interleaved continuation failure"))
			}
		}
	} else if phase == interleavedPhaseExecutor {
		if executor != nil && executor.terminal != nil && !executor.terminal.finished() {
			execAttempt := executor.attempt.snapshot()
			if closePending {
				executor.terminal.closeClose(cleanupCtx, executor.facts.terminalFacts(), execAttempt, executor.responsePipeline)
			} else if cancelPending {
				executor.terminal.terminalizeCancellation(cleanupCtx, executor.facts.terminalFacts(), execAttempt, executor.responsePipeline, "canceled", false)
			} else if ctx != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
				executor.terminal.terminalizeTimeout(cleanupCtx, executor.facts.terminalFacts(), execAttempt, executor.responsePipeline)
			} else if ctx != nil && errors.Is(ctx.Err(), context.Canceled) {
				executor.terminal.terminalizeCancellation(cleanupCtx, executor.facts.terminalFacts(), execAttempt, executor.responsePipeline, "canceled", false)
			} else {
				executor.terminal.terminalizeEOF(cleanupCtx, executor.facts.terminalFacts(), execAttempt, executor.responsePipeline)
			}
		}
		if thinker != nil && thinker.terminal != nil && !thinker.terminal.finished() {
			attempt := thinker.attempt.snapshot()
			thinker.terminal.terminalizeEOF(cleanupCtx, thinker.facts.terminalFacts(), attempt, thinker.responsePipeline)
		}
	}
	s.markFinished()
}

func (s *interleavedContinuationStream) abortExecutorHandoff(ctx context.Context, exec *retryRecvStream, abortErr error) {
	cleanupCtx, cleanupCancel := detachedCleanupContext(ctx, cancelLosersTimeout)
	defer cleanupCancel()

	s.mu.Lock()
	closePending := s.closePending
	cancelPending := s.cancelPending
	s.mu.Unlock()

	if exec != nil {
		execAttempt := exec.attempt.closePublicationAndSnapshot()
		if execAttempt == nil {
			execAttempt = exec.attempt.snapshot()
		}
		if execAttempt != nil {
			reason := "interleaved executor handoff aborted"
			if abortErr != nil {
				reason = abortErr.Error()
			}
			execAttempt.terminalizeSwallowed(cleanupCtx, exec.facts, exec.responsePipeline, false, reason, abortErr)
			exec.terminal.finishResponse(exec.responsePipeline, execAttempt)
		}
	}

	if s.thinker != nil {
		attempt := s.thinker.attempt.snapshot()
		if closePending {
			s.thinker.terminal.closeClose(cleanupCtx, s.thinker.facts.terminalFacts(), attempt, s.thinker.responsePipeline)
		} else {
			timeout := errors.Is(abortErr, context.DeadlineExceeded) || (s.thinker.terminal != nil && s.thinker.terminal.hasALeg() && errors.Is(s.thinker.terminal.aLegErr(), context.DeadlineExceeded))
			reason := "canceled"
			aLegCanceled := s.thinker.terminal != nil && s.thinker.terminal.hasALeg() && errors.Is(s.thinker.terminal.aLegErr(), leglifecycle.ErrALegCanceled)
			if !cancelPending && !errors.Is(abortErr, context.Canceled) && !aLegCanceled {
				if abortErr != nil {
					reason = abortErr.Error()
				} else {
					reason = "interleaved executor handoff aborted"
				}
			}
			s.thinker.terminal.terminalizeCancellation(cleanupCtx, s.thinker.facts.terminalFacts(), attempt, s.thinker.responsePipeline, reason, timeout)
		}
	}

	s.markFinished()
}

func (s *interleavedContinuationStream) recvExecutor(ctx context.Context) (aipapi.Event, error) {
	if ev, ok := s.popPending(ctx); ok {
		return ev, nil
	}
	if s.executor == nil {
		s.finishWithCleanup(ctx)
		return aipapi.Event{}, io.EOF
	}
	for {
		ev, err := s.executor.Recv(ctx)
		if err != nil {
			s.finishWithCleanup(ctx)
			return ev, err
		}
		s.mu.Lock()
		responseStarted := s.responseStarted
		withdrawn := s.withdrawalRequestedLocked()
		s.mu.Unlock()
		if responseStarted && (ev.Kind == aipapi.EventResponseStarted || ev.Kind == aipapi.EventMessageStarted) {
			continue
		}
		// The executor leg can also hand out a frame the client withdrew against
		// while that receive was blocked, so the live predicate closes the return
		// seam too, not only the sticky flags.
		if withdrawErr := s.liveWithdrawalError(ctx); withdrawErr != nil || withdrawn {
			if withdrawErr == nil {
				withdrawErr = s.withdrawalError()
			}
			s.finishWithCleanup(ctx)
			if withdrawErr == nil {
				withdrawErr = errInterleavedClientClosed
			}
			return aipapi.Event{}, withdrawErr
		}
		return ev, nil
	}
}

// withdrawThinkerProcessor closes processor admission and visible output for an
// ordinary thinker withdrawal: it joins any admitted Observe/Flush within the
// established detached cleanup budget, then claims the single interrupted memo
// capture the withdrawal requested. It never finalizes concurrently with
// admitted processor work and never reports a memo as persisted when the join
// exhausted its budget.
func (s *interleavedContinuationStream) withdrawThinkerProcessor(ctx context.Context) error {
	cleanupCtx, cleanupCancel := detachedCleanupContext(ctx, cancelLosersTimeout)
	defer cleanupCancel()
	if err := s.joinThinkerProcessor(cleanupCtx); err != nil {
		// The admitted callback is still running and context-free. Leave
		// interruptedMemoPending for it: when it returns it releases admission
		// and claims one interrupted Finalize itself. No background waiter.
		return err
	}
	s.finalizePendingInterruptedMemo(ctx)
	return nil
}

// requestInterruptedMemoLocked records that this withdrawal still owes one
// interrupted memo capture. It is set at Cancel/Close entry for every phase
// before any external work, so duplicate cleanup joins or defers instead of
// interpreting memoPersisted as completed while a processor is active.
func (s *interleavedContinuationStream) requestInterruptedMemoLocked() {
	if s.phase != interleavedPhaseThinker || s.turn == nil || s.thinker == nil || s.thinker.recovery == nil {
		return
	}
	if s.memoPersisted {
		return
	}
	s.interruptedMemoPending = true
}

func (s *interleavedContinuationStream) markFinished() {
	s.mu.Lock()
	s.finished = true
	s.mu.Unlock()
	if s.thinker != nil && s.thinker.terminal != nil {
		s.thinker.terminal.endALeg(aLegEndOuter)
	}
}

func (s *interleavedContinuationStream) activeRecvLocked() *retryRecvStream {
	if s == nil {
		return nil
	}
	if s.phase == interleavedPhaseExecutor && s.executor != nil {
		return s.executor
	}
	return s.thinker
}

func (s *interleavedContinuationStream) activeRecv() *retryRecvStream {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activeRecvLocked()
}

func (s *interleavedContinuationStream) Cancel(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	if s == nil {
		return aipapi.CancelResult{}
	}
	s.mu.Lock()
	if s.finished {
		s.mu.Unlock()
		return aipapi.CancelResult{}
	}
	// Withdrawal flags are set at entry for every phase, before any external
	// work, so admission, visible output, and memo capture all see it.
	s.cancelPending = true
	s.pending = nil
	s.requestInterruptedMemoLocked()
	if s.transitionInFlight {
		// Pending flags are owned by abort/cleanup (do not set finished here).
		thinker := s.thinker
		executor := s.executor
		s.mu.Unlock()
		// Cancel an already-opened continuation when assigned; otherwise cancel the
		// shared A-leg scope so handoffAborted/open sees cancellation.
		if executor != nil && executor.terminal != nil && executor.terminal.hasALeg() {
			_ = executor.terminal.cancelALeg(ctx, cause)
		} else if thinker != nil && thinker.terminal != nil && thinker.terminal.hasALeg() {
			_ = thinker.terminal.cancelALeg(ctx, cause)
		}
		return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
	}
	active := s.activeRecvLocked()
	phase := s.phase
	s.mu.Unlock()
	if active == nil {
		return aipapi.CancelResult{}
	}

	var res aipapi.CancelResult
	if !active.terminal.finished() {
		if active.terminal != nil && active.terminal.hasALeg() {
			_ = active.terminal.cancelALeg(ctx, cause)
			res = aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
		}
	}

	switch phase {
	case interleavedPhaseThinker:
		if err := s.withdrawThinkerProcessor(ctx); err != nil {
			res.Err = err
		}
		if s.thinker != nil && s.thinker.terminal != nil && !s.thinker.terminal.finished() {
			s.thinker.terminal.terminalizeCancellation(ctx, s.thinker.facts.terminalFacts(), s.thinker.attempt.snapshot(), s.thinker.responsePipeline, cause.Detail, false)
		}
	case interleavedPhaseExecutor:
		if s.executor != nil && s.executor.terminal != nil && !s.executor.terminal.finished() {
			s.executor.terminal.terminalizeCancellation(ctx, s.executor.facts.terminalFacts(), s.executor.attempt.snapshot(), s.executor.responsePipeline, cause.Detail, false)
		}
	}
	s.markFinished()
	return res
}

func (s *interleavedContinuationStream) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.finished {
		s.mu.Unlock()
		return nil
	}
	// Withdrawal flags are set at entry for every phase, before any external
	// work, so admission, visible output, and memo capture all see it.
	s.closePending = true
	s.pending = nil
	s.requestInterruptedMemoLocked()
	if s.transitionInFlight {
		// Pending flags are owned by abort/cleanup (do not set finished here).
		thinker := s.thinker
		executor := s.executor
		s.mu.Unlock()
		parent := context.Background()
		if thinker != nil {
			parent = thinker.responsePipeline.withDecisionEvidence(thinker.facts.projectContext(parent, thinker.responsePipeline.log), thinker.terminal)
		}
		if executor != nil && executor.terminal != nil && executor.terminal.hasALeg() {
			_ = executor.terminal.cancelALeg(parent, leglifecycle.CancelCause{Kind: leglifecycle.CancelClientGone})
		} else if thinker != nil && thinker.terminal != nil && thinker.terminal.hasALeg() {
			_ = thinker.terminal.cancelALeg(parent, leglifecycle.CancelCause{Kind: leglifecycle.CancelClientGone})
		}
		return nil
	}
	phase := s.phase
	s.mu.Unlock()

	var err error
	if phase == interleavedPhaseThinker {
		parent := context.Background()
		if s.thinker != nil {
			parent = s.thinker.responsePipeline.withDecisionEvidence(s.thinker.facts.projectContext(parent, s.thinker.responsePipeline.log), s.thinker.terminal)
		}
		if joinErr := s.withdrawThinkerProcessor(parent); joinErr != nil {
			err = errors.Join(err, joinErr)
		}
		if s.thinker != nil {
			err = errors.Join(err, s.thinker.Close())
		}
	} else if phase == interleavedPhaseExecutor && s.executor != nil {
		err = s.executor.Close()
	}
	s.markFinished()
	return err
}
