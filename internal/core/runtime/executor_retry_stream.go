package runtime

// Stream lifecycle helpers (loadInner, storeInner, Close, etc.) and the
// recv-phase support surface (stream-evidence
// seam, traffic emission) for retryRecvStream. Response/event evidence and
// completion-gate, logical-tool, and response-observation state live in
// responsePipeline. The inner-loop control (Recv and tryReplacementIteration) has been extracted
// to executor_recv_loop.go; the retryRecvStream type itself, its error
// sentinel, and the aipapi.EventStream interface assertion remain here.

import (
	"context"
	"errors"
	"slices"

	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

// retryRecvStream is the small recv-phase EventStream facade: it wraps a
// backend stream and coordinates the five lifetime owners for failover and
// terminal completion without owning their state.
//
// Concurrency: one goroutine calls Recv until completion (aipapi.EventStream). Close may run
// concurrently with Recv blocked on the active inner stream; Close forwards to that attempt stream
// and does not clear the attempt pointer. Recv clears the attempt stream on cancellation and
// recoverable-recv teardown paths.
// Recv must not be called concurrently from multiple goroutines; the stream is not multi-Recv-safe.
type retryRecvStream struct {
	// facts is the sole request-lifetime receive authority. It contains only
	// immutable request facts; retry, event, terminal, attempt, and lock state
	// remains owned by cohesive collaborators.
	facts recvTurnFacts

	responsePipeline *responsePipeline

	attempt  attemptSlot
	terminal *turnTerminal
	recovery *recoveryController
}

var _ aipapi.EventStream = (*retryRecvStream)(nil)

var errNilRetryRecvStream = errors.New("runtime: nil retryRecvStream")

type idleContextDeadline struct {
	active bool
	parent context.Context
}

func (d idleContextDeadline) expired(_ context.Context, err error) bool {
	return d.active && d.parent != nil && d.parent.Err() == nil && errors.Is(err, context.DeadlineExceeded)
}

func lifecycleAttempt(stream aipapi.EventStream) leglifecycle.BLegAttempt {
	if stream == nil {
		return nil
	}
	if managed, ok := stream.(leglifecycle.BLegAttempt); ok {
		return managed
	}
	return aipapi.CloseOnlyManagedStream{Stream: stream}
}

// recvFinishAuthorityInput builds the narrow private typed optional input of the
// response_finished authority chokepoint for this receive stream. It owns no
// terminal authority: it only names the continuation seam that already existed and
// the two publication seams of a prepared pending completion.
//
// callerCtx is the original live receive context, captured on purpose and stored
// ONLY in this lexical callback. The terminal owner runs request effects under its
// own detached bounded cleanup context, so the publication fence needs the
// caller's context to notice a client cancellation that already won instead of
// reading the detached context. No context is retained in persistent drain state.
//
// expectedFence is the ONE lexical publication predicate of the receive call. It is
// handed over rather than rebuilt, so staging, activation, and physical delivery
// all consult the identical checks over the identical expected candidate and
// origin instead of three partial ones. A nil predicate preserves the provisional
// behavior of callers that own no receive transaction.
func recvFinishAuthorityInput(
	callerCtx context.Context,
	s *retryRecvStream,
	origin *attemptSession,
	pending *pendingCompletion,
	endALeg bool,
	expectedFence func(*pendingCompletion, *attemptSession) bool,
) finishAuthorityInput {
	hooks := finishAuthorityInput{
		continuation: func(cctx context.Context, intent terminaldecision.ContinuationIntent) (bool, error) {
			return runContinuationTransaction(cctx, s.terminal, s, intent)
		},
		publishable: func() bool {
			return expectedFence == nil || expectedFence(pending, origin)
		},
		// Activation must consult the ORIGINAL caller, not the detached bounded
		// cleanup context the terminal owner runs its effects on, so a client
		// cancellation that already won is never masked there.
		callerFence: func() bool { return callerCtx != nil && callerCtx.Err() == nil },
		// The expected identity is captured HERE, before any terminal work runs, and
		// is carried as private identity facts only. Every later claim, reservation,
		// activation, and delivery decision is checked against this exact pair.
		expectedPrepared: pending,
		expectedOrigin:   origin,
	}
	if pending.holds() {
		hooks.acceptedNormal = func(ownerCtx context.Context, usage aipapi.Event, usageOK bool) error {
			return s.terminal.stagePendingCompletion(ownerCtx, origin, s.responsePipeline,
				s.facts.terminalFacts(),
				pendingPublication{
					prepared:    pending,
					usage:       usage,
					usageOK:     usageOK,
					facts:       s.facts,
					recovery:    s.recovery,
					callerCtx:   callerCtx,
					publishable: hooks.publishable,
					endALeg:     endALeg,
				},
			)
		}
	}
	return hooks
}

// recvPendingPublishable is the receive-side publication fence: the attempt
// publication window must still be open. Close closes that window before terminal
// competition, so a Close winner always suppresses a late pending result. It
// performs no I/O and holds no lock across an external call.
func recvPendingPublishable(s *retryRecvStream) bool {
	if s == nil {
		return false
	}
	return !s.attempt.publicationIsClosed()
}

func (s *retryRecvStream) Close() error {
	if s == nil {
		return nil
	}
	current := s.attempt.closePublicationAndSnapshot()
	// Close closes the existing publication window BEFORE clearing private state,
	// so activation can never resurrect a drain afterwards and the undelivered
	// remainder is discarded here rather than stranding for a later Recv.
	s.responsePipeline.withdrawPendingCompletion(current)
	s.responsePipeline.clearAttemptState(s.attempt.snapshot())
	// aipapi.EventStream.Close has no caller context. Project a detached
	// request context from immutable facts; no mutable context cache belongs on
	// the EventStream facade.
	ctx := s.responsePipeline.withDecisionEvidence(s.facts.projectContext(context.Background(), s.responsePipeline.log), s.terminal)
	s.terminal.closeClose(ctx, s.facts.terminalFacts(), current, s.responsePipeline)
	s.terminal.endALeg(aLegEndBase)
	return nil
}

func gateBufHasCommittedOutput(buf []aipapi.Event) bool {
	return slices.ContainsFunc(buf, aipapi.OutputCommitted)
}
