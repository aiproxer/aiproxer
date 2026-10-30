package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	coreterm "github.com/aiproxer/aiproxer/internal/core/terminal"
	terminalworkapp "github.com/aiproxer/aiproxer/internal/core/terminalwork/app"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// streamTerminal binds a domain terminal.Owner to once-only effect execution.
// Concurrent Terminalize callers CAS-claim; the winner snapshots via snapFn,
// runs effects under a detached bounded cleanup context, and advances the
// owner. Losers await the same completion and observe the published outcome
// (requirements 7.1–7.3, 7.6; design D8).
type streamTerminal struct {
	mu     sync.Mutex
	owner  *coreterm.Owner
	done   chan struct{}
	closed bool
	// resultErr is the winner's finalized result error. Publishing it rather
	// than the raw effect error keeps the shared outcome faithful: a waiter must
	// observe a state-advance failure the winner surfaced, not the effect error
	// from before the transition ran.
	resultErr error
	// cleanupTimeout overrides the detached terminal effect budget. Zero keeps
	// the production default (defaultAuthorityCleanupTimeout); only tests that
	// drive real durable stores inside the callback set it.
	cleanupTimeout time.Duration
}

func newStreamTerminal(scope sdk.Scope) *streamTerminal {
	return &streamTerminal{
		owner: coreterm.NewOwner(scope),
		done:  make(chan struct{}),
	}
}

// Owner returns the underlying domain owner for evidence and tests.
func (t *streamTerminal) Owner() *coreterm.Owner {
	if t == nil {
		return nil
	}
	return t.owner
}

// Terminalize competes for the single terminal outcome on this session.
// effects may return an error; on error or panic the owner advances to Failed
// and all waiters observe the same Err. The winner publishes its finalized
// result error before releasing waiters, so a state-advance failure is never
// hidden behind a successful effect.
func (t *streamTerminal) Terminalize(
	ctx context.Context,
	cmd sdk.Command,
	snapFn func() coreterm.AccumulatorSnapshot,
	effects func(context.Context, coreterm.Outcome) error,
) coreterm.Result {
	if t == nil || t.owner == nil {
		return coreterm.Result{Err: sdk.ErrInvalid}
	}
	if snapFn == nil {
		snapFn = func() coreterm.AccumulatorSnapshot {
			return coreterm.NewAccumulatorSnapshot(nil, false)
		}
	}

	t.mu.Lock()
	snap := snapFn()
	r := t.owner.Claim(cmd, snap)
	won := r.Won
	t.mu.Unlock()

	if !won {
		if shouldAwaitTerminalResult(r) {
			<-t.done
			if out, ok := t.owner.Outcome(); ok {
				r.Outcome = out
			}
			r.State = t.owner.State()
			if resultErr := t.publishedResultError(); resultErr != nil {
				r.Err = resultErr
			}
		}
		return r
	}

	defer func() { t.signalDone(r.Err) }()

	cleanupCtx, cancel := cleanupContext(ctx, t.cleanupTimeout)
	defer cancel()

	var effectErr error
	panicked := false
	func() {
		defer func() {
			if recover() != nil {
				panicked = true
			}
		}()
		if effects != nil {
			effectErr = effects(cleanupCtx, r.Outcome)
		}
	}()

	advanceFailed := func(primary error) error {
		if err := t.owner.Advance(sdk.StateFailed); err != nil {
			return errors.Join(primary, err)
		}
		return primary
	}

	durablePending := errors.Is(effectErr, terminalworkapp.ErrDurablePending)
	if panicked || (effectErr != nil && !durablePending) || cmd == sdk.CommandPanic {
		terminalErr := effectErr
		if terminalErr == nil && panicked {
			terminalErr = errors.New("runtime: stream terminal effect panic")
		}
		r.Err = advanceFailed(terminalErr)
		r.State = t.owner.State()
		return r
	}

	if durablePending {
		// Durable intent accepted: keep output commitment and stop at work_pending
		// (requirements 7.7, 8.3; design D8/D9).
		if err := t.owner.Advance(sdk.StateWorkPending); err != nil {
			r.Err = errors.Join(effectErr, err, t.owner.Advance(sdk.StateFailed))
			r.State = t.owner.State()
			return r
		}
		r.State = t.owner.State()
		r.Err = effectErr
		return r
	}

	var advanceErr error
	for _, st := range []sdk.State{
		sdk.StateWorkPending,
		sdk.StateSettled,
		sdk.StateReleasePending,
		sdk.StateReleased,
	} {
		if err := t.owner.Advance(st); err != nil {
			advanceErr = errors.Join(advanceErr, err, t.owner.Advance(sdk.StateFailed))
			break
		}
	}
	r.State = t.owner.State()
	r.Err = advanceErr
	return r
}

// shouldAwaitTerminalResult awaits when a competing claim exists. Initial
// open+committed GateReplacement rejection (StateOpen + ErrOutputCommitted)
// returns immediately without awaiting.
func shouldAwaitTerminalResult(r coreterm.Result) bool {
	if r.Err == nil || errors.Is(r.Err, sdk.ErrConflict) {
		return true
	}
	if errors.Is(r.Err, sdk.ErrOutputCommitted) && r.State != sdk.StateOpen {
		return true
	}
	return false
}

// signalDone publishes the winner's finalized result error and then releases
// every waiter. Publication happens under mu before the channel closes so a
// waiter can never observe a partially finalized outcome.
func (t *streamTerminal) signalDone(resultErr error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	t.resultErr = resultErr
	close(t.done)
	t.closed = true
}

// publishedResultError returns the winner's finalized result error.
func (t *streamTerminal) publishedResultError() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.resultErr
}

// terminalizeAttemptEphemeral runs attempt-scoped terminalization without a stream
// (parallel losers, pre-backend/backend-open cleanup).
func terminalizeAttemptEphemeral(
	ctx context.Context,
	cmd sdk.Command,
	outputCommitted bool,
	effects func(context.Context) error,
) coreterm.Result {
	term := newStreamTerminal(sdk.ScopeAttempt)
	return term.Terminalize(ctx, cmd, func() coreterm.AccumulatorSnapshot {
		return coreterm.NewAccumulatorSnapshot(nil, outputCommitted)
	}, func(cctx context.Context, _ coreterm.Outcome) error {
		if effects == nil {
			return nil
		}
		return effects(cctx)
	})
}

// terminalLossError maps a lost terminal claim to the error Recv/finish paths surface.
func terminalLossError(r coreterm.Result) error {
	if r.Err != nil && !errors.Is(r.Err, sdk.ErrConflict) && !errors.Is(r.Err, sdk.ErrOutputCommitted) {
		return r.Err
	}
	switch r.Outcome.Command {
	case sdk.CommandClose, sdk.CommandCancel:
		// Match Close/a-leg cancel vocabulary already used by Recv cancel paths.
		return leglifecycle.ErrALegCanceled
	case sdk.CommandTimeout:
		return context.DeadlineExceeded
	case sdk.CommandEOF:
		return io.EOF
	case "":
		if r.Err != nil {
			return r.Err
		}
		return errors.New("runtime: stream already terminalized")
	default:
		if r.Err != nil {
			return r.Err
		}
		return fmt.Errorf("runtime: stream terminalized by %s", r.Outcome.Command)
	}
}
