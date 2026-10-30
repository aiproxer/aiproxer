package terminal

import (
	"fmt"
	"sync"

	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// Outcome is the published terminal result for one owner.
type Outcome struct {
	Code               sdk.OutcomeCode
	Command            sdk.Command
	Snapshot           AccumulatorSnapshot
	SettleCustomer     bool // request plane: customer authority
	ReleaseConcurrency bool // request plane: logical concurrency / request provider
	SettleOperator     bool // attempt plane: operator authority
	ReleaseAttempt     bool // attempt plane: attempt provider / attempt lease
	Scope              sdk.Scope
}

// Result is the Claim response observed by a caller.
type Result struct {
	Won     bool
	Outcome Outcome
	State   sdk.State
	Err     error
}

// Owner is a CAS-owned terminal state machine for one request or attempt.
type Owner struct {
	mu      sync.Mutex
	scope   sdk.Scope
	state   sdk.State
	outcome *Outcome
	claimed bool
}

// NewOwner constructs an owner in StateOpen.
func NewOwner(scope sdk.Scope) *Owner {
	return &Owner{scope: scope, state: sdk.StateOpen}
}

// Scope returns the owner scope.
func (o *Owner) Scope() sdk.Scope {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.scope
}

// State returns the current owner state.
func (o *Owner) State() sdk.State {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.state
}

// Outcome returns the published outcome when claimed.
func (o *Owner) Outcome() (Outcome, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.outcome == nil {
		return Outcome{}, false
	}
	return cloneOutcome(*o.outcome), true
}

// Claim competes for the single terminalization outcome.
//
// The winner snapshots accumulators once and publishes the outcome. Concurrent
// or subsequent callers observe the same outcome without re-running effects.
func (o *Owner) Claim(cmd sdk.Command, snap AccumulatorSnapshot) Result {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.claimed {
		return o.observeLocked(cmd)
	}

	if err := cmd.Validate(); err != nil {
		return Result{Won: false, State: o.state, Err: fmt.Errorf("%w: %v", sdk.ErrInvalid, err)}
	}
	if !cmd.AllowsScope(o.scope) {
		return Result{Won: false, State: o.state, Err: sdk.ErrScopeMismatch}
	}

	if o.state != sdk.StateOpen {
		return Result{Won: false, State: o.state, Err: sdk.ErrInvalidTransition}
	}

	if cmd.IsRetryOrReplacement() && snap.OutputCommitted() {
		return Result{Won: false, State: o.state, Err: sdk.ErrOutputCommitted}
	}

	out := Outcome{
		Code:               sdk.OutcomeCodeFor(cmd),
		Command:            cmd,
		Snapshot:           snap.Clone(),
		SettleCustomer:     o.scope == sdk.ScopeRequest,
		ReleaseConcurrency: o.scope == sdk.ScopeRequest,
		SettleOperator:     o.scope == sdk.ScopeAttempt,
		ReleaseAttempt:     o.scope == sdk.ScopeAttempt,
		Scope:              o.scope,
	}
	o.outcome = &out
	o.claimed = true
	o.state = sdk.StateTerminalizing
	return Result{Won: true, Outcome: cloneOutcome(out), State: o.state}
}

func (o *Owner) observeLocked(cmd sdk.Command) Result {
	out := cloneOutcome(*o.outcome)
	res := Result{Won: false, Outcome: out, State: o.state}

	if cmd.IsRetryOrReplacement() && out.Snapshot.OutputCommitted() {
		res.Err = sdk.ErrOutputCommitted
		return res
	}
	if cmd == out.Command {
		// Idempotent re-claim of the winning command: observe, no re-effects.
		return res
	}
	res.Err = sdk.ErrConflict
	return res
}

// Advance moves the owner along the post-claim lifecycle.
func (o *Owner) Advance(to sdk.State) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	if err := to.Validate(); err != nil {
		return fmt.Errorf("%w: %v", sdk.ErrInvalid, err)
	}
	if !legalAdvance(o.state, to) {
		return fmt.Errorf("%w: %s -> %s", sdk.ErrInvalidTransition, o.state, to)
	}
	o.state = to
	return nil
}

func legalAdvance(from, to sdk.State) bool {
	if to == sdk.StateFailed {
		switch from {
		case sdk.StateTerminalizing, sdk.StateWorkPending, sdk.StateSettled, sdk.StateReleasePending:
			return true
		}
		return false
	}
	switch from {
	case sdk.StateTerminalizing:
		return to == sdk.StateWorkPending
	case sdk.StateWorkPending:
		return to == sdk.StateSettled
	case sdk.StateSettled:
		return to == sdk.StateReleasePending
	case sdk.StateReleasePending:
		return to == sdk.StateReleased
	}
	return false
}

func cloneOutcome(o Outcome) Outcome {
	o.Snapshot = o.Snapshot.Clone()
	return o
}
