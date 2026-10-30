package runtime

import (
	"context"
	"fmt"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/steering"
)

// InterleavedTurnInput carries per-turn facts required by interleaved thinking.
type InterleavedTurnInput struct {
	ALegID              string
	Selector            string
	Backend             string
	Model               string
	RequestID           string
	StreamToClient      string
	SuppressVisibleMemo bool
}

// InterleavedMemo carries minimal evidence of captured thinker output.
type InterleavedMemo struct {
	Text       string
	Reference  string
	Version    int64
	HadContent bool
}

// IsEmpty reports whether the memo evidence is empty.
func (m InterleavedMemo) IsEmpty() bool {
	return m.Reference == "" && m.Text == ""
}

// InterleavedProcessor is the runtime-owned consumer interface for interleaved thinking.
// Memo steering policy (rendering, overlay identity, placement/fallback
// selection, memo filtering) is feature-owned: the processor answers every
// memo-policy question so core never hardcodes feature semantics.
type InterleavedProcessor interface {
	BeginTurn(ctx context.Context, in InterleavedTurnInput) (InterleavedTurn, error)
	IsMemoVisibleToClient(ctx context.Context, aLegID string) bool
	// MemoSteeringPutRequest builds the feature-owned steering mutation that
	// persists a captured memo.
	MemoSteeringPutRequest(memo string) steering.PutRequest
	// MemoSteeringOverlayID returns the feature-owned stable overlay identity
	// for the thinker memo.
	MemoSteeringOverlayID() steering.OverlayID
	// IsMemoSteeringOverlay reports whether overlayID carries the thinker memo.
	IsMemoSteeringOverlay(overlayID string) bool
}

// InterleavedTurn is the runtime-owned per-turn lifecycle contract.
type InterleavedTurn interface {
	ShapeThinker(call aipapi.Call) (aipapi.Call, error)
	ObserveThinkerEvent(ev aipapi.Event) ([]aipapi.Event, error)
	FinalizeThinker(ctx context.Context) (InterleavedMemo, error)
	FinalizeThinkerStatus(ctx context.Context, interrupted bool, visibleCommitted bool) (InterleavedMemo, error)
	ShapeExecutor(ctx context.Context, call aipapi.Call, memo InterleavedMemo) (aipapi.Call, error)
	Visible() bool
	CanContinue() bool
	ShapeDiagnostics() (outcome string, turnsRemaining int)
	CommitExecutor(ctx context.Context) (remaining int, err error)
	FlushVisible() []aipapi.Event
}

// interleavedEnabled reports whether interleaved thinking is configured on the executor.
func (e *Executor) interleavedEnabled() bool {
	if e == nil {
		return false
	}
	return e.InterleavedProcessor != nil
}

// loadInterleavedState fetches the persisted thinker cycle state for the A-leg.
func (e *Executor) loadInterleavedState(ctx context.Context, aLegID string) (interleavedstate.State, error) {
	if !e.interleavedEnabled() {
		return interleavedstate.State{}, nil
	}
	is, ok := e.Store.(b2bua.InterleavedStateStore)
	if !ok || is == nil {
		return interleavedstate.State{}, nil
	}
	return is.FetchInterleavedState(ctx, aLegID)
}

// commitInterleavedExecutorTurn commits a winning executor turn's memo and
// retires the memo steering overlay once no executor turns remain. It is a
// no-op for any other role. Sequential and parallel opens share it.
func (e *Executor) commitInterleavedExecutorTurn(ctx context.Context, aLegID string, role interleavedstate.Role, turn InterleavedTurn) error {
	if role != interleavedstate.RoleExecutor || turn == nil {
		return nil
	}
	remaining, err := turn.CommitExecutor(ctx)
	if err != nil {
		return fmt.Errorf("executor: commit interleaved memo: %w", err)
	}
	if remaining == 0 {
		if err := e.deactivateMemoSteeringOverlay(ctx, aLegID); err != nil {
			return fmt.Errorf("executor: deactivate memo steering: %w", err)
		}
	}
	return nil
}

// advanceInterleavedCycle moves state to next and persists it; a nil next
// leaves state unchanged. Sequential and parallel opens share it.
func (e *Executor) advanceInterleavedCycle(ctx context.Context, aLegID string, state interleavedstate.State, next *interleavedstate.CycleState) (interleavedstate.State, error) {
	if next == nil {
		return state, nil
	}
	state.Cycle = *next
	if err := e.persistInterleavedState(ctx, aLegID, state); err != nil {
		return state, fmt.Errorf("executor: persist interleaved cycle: %w", err)
	}
	return state, nil
}

// persistInterleavedState stores the thinker cycle state for the A-leg.
func (e *Executor) persistInterleavedState(ctx context.Context, aLegID string, state interleavedstate.State) error {
	if !e.interleavedEnabled() {
		return nil
	}
	is, ok := e.Store.(b2bua.InterleavedStateStore)
	if !ok || is == nil {
		if state.IsEmpty() {
			return nil
		}
		return b2bua.ErrInterleavedStateUnsupported
	}
	return is.SetInterleavedState(ctx, aLegID, state)
}

// shapeAttemptCall applies candidate-specific interleaved shaping to a canonical call before
// capability negotiation and backend open.
func (e *Executor) shapeAttemptCall(
	ctx context.Context,
	call aipapi.Call,
	c routing.AttemptCandidate,
	turn InterleavedTurn,
) (aipapi.Call, error) {
	if !e.interleavedEnabled() || turn == nil {
		return aipapi.CloneCall(call), nil
	}
	switch c.InterleavedRole {
	case interleavedstate.RoleThinker:
		return turn.ShapeThinker(call)
	case interleavedstate.RoleExecutor:
		return turn.ShapeExecutor(ctx, call, InterleavedMemo{})
	default:
		return aipapi.CloneCall(call), nil
	}
}

func (e *Executor) shouldWrapInterleavedThinker(c routing.AttemptCandidate, turn InterleavedTurn) bool {
	if !e.interleavedEnabled() || c.InterleavedRole != interleavedstate.RoleThinker {
		return false
	}
	if turn != nil {
		return turn.CanContinue()
	}
	return true
}

func (e *Executor) shouldWrapHiddenInterleavedThinker(c routing.AttemptCandidate, turn InterleavedTurn) bool {
	return e.shouldWrapInterleavedThinker(c, turn)
}

func (e *Executor) shouldWrapVisibleInterleavedThinker(c routing.AttemptCandidate, turn InterleavedTurn) bool {
	return e.shouldWrapInterleavedThinker(c, turn)
}

func (e *Executor) openInterleavedExecutorContinuation(ctx context.Context, from *retryRecvStream, state interleavedstate.State) (*retryRecvStream, error) {
	if e == nil || from == nil {
		return nil, fmt.Errorf("executor: invalid interleaved continuation arguments")
	}
	facts := from.facts
	facts, _ = e.refreshMemoSteeringFacts(ctx, facts, state, true)

	boundCtx := projectRefreshedMemoContext(ctx, facts, from.responsePipeline.log)
	boundCtx = from.responsePipeline.withDecisionEvidence(boundCtx, from.terminal)
	e.logInterleavedThinkerSuppressed(boundCtx, facts.traceID)
	if from.recovery == nil {
		return nil, fmt.Errorf("executor: interleaved continuation recovery unavailable")
	}
	from.recovery.bindOpener(e, from.responsePipeline.bus, from.terminal.aLegScope())
	out, err := from.recovery.openInterleavedAttempt(boundCtx, facts, state)
	if err != nil {
		return nil, fmt.Errorf("executor: interleaved continuation plan/open: %w", err)
	}
	if out.ready == nil {
		return nil, fmt.Errorf("executor: interleaved continuation: %w", routing.ErrNoEligibleCandidate)
	}
	responsePipeline := newResponsePipelineForExecutor(e)
	if err := out.ready.Prepare(boundCtx, facts, responsePipeline, false); err != nil {
		return nil, err
	}
	terminal := newTurnTerminalWithSharedALeg(from.terminal)
	rs := &retryRecvStream{
		facts:            cloneRefreshedMemoFacts(facts),
		recovery:         from.recovery,
		responsePipeline: responsePipeline,
		attempt:          attemptSlot{},
		terminal:         terminal,
	}
	bindTurnTerminalRuntime(rs.terminal, e)
	responsePipeline.bindTerminalSnapshot(func() (bool, bool) { return terminal.committed(), terminal.accountingFinalized() })
	responsePipeline.bindCustomerUsage(func(ctx context.Context, text string, events []aipapi.Event) aipapi.Event {
		return reconstructCustomerUsageForResponse(ctx, responsePipeline.streamUsage, responsePipeline.log, rs.facts, rs.attempt.snapshot(), text, events)
	})
	if _, published := rs.attempt.publishReady(out.ready); !published {
		pubErr := fmt.Errorf("interleaved open: %w", errPublicationClosed)
		out.ready.Dispose(boundCtx, pubErr)
		return nil, pubErr
	}
	return rs, nil
}
