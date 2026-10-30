package terminalwork_test

import (
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/terminalwork"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// Exhaustive work-state × operation matrix (requirement 8.2, design D9).

type workOp string

const (
	opPending    workOp = "pending"
	opClaim      workOp = "claim"
	opRenewClaim workOp = "renew_claim"
	opComplete   workOp = "complete"
	opRetry      workOp = "retry"
	opQuarantine workOp = "quarantine"
)

func allWorkOps() []workOp {
	return []workOp{opPending, opClaim, opRenewClaim, opComplete, opRetry, opQuarantine}
}

func TestWorkItem_ExhaustiveStateOperationMatrix(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	sched := terminalwork.RetrySchedule{Initial: time.Second, Multiplier: 2, Max: 8 * time.Second}

	type fixture struct {
		name  string
		state sdk.WorkState
		prep  func(t *testing.T, clock *manualClock) *terminalwork.WorkItem
		// legal maps op -> nil means success; non-nil means exact sentinel expected
		legal map[workOp]error
	}

	fixtures := []fixture{
		{
			name:  "intent",
			state: sdk.WorkStateIntent,
			prep: func(t *testing.T, _ *manualClock) *terminalwork.WorkItem {
				t.Helper()
				return newIntent(t, "mx-intent", "wi", "p", sdk.WorkKindSettleRequestProvider)
			},
			legal: map[workOp]error{
				opPending:    nil,
				opClaim:      sdk.ErrInvalidTransition,
				opRenewClaim: sdk.ErrInvalidTransition,
				opComplete:   sdk.ErrInvalidTransition,
				opRetry:      sdk.ErrInvalidTransition,
				opQuarantine: sdk.ErrInvalidTransition,
			},
		},
		{
			name:  "pending",
			state: sdk.WorkStatePending,
			prep: func(t *testing.T, _ *manualClock) *terminalwork.WorkItem {
				t.Helper()
				w := newIntent(t, "mx-pending", "wp", "p", sdk.WorkKindSettleRequestProvider)
				if err := w.MarkPending(); err != nil {
					t.Fatal(err)
				}
				return w
			},
			legal: map[workOp]error{
				opPending:    sdk.ErrInvalidTransition,
				opClaim:      nil,
				opRenewClaim: sdk.ErrInvalidTransition,
				opComplete:   sdk.ErrInvalidTransition,
				opRetry:      sdk.ErrInvalidTransition,
				opQuarantine: nil,
			},
		},
		{
			name:  "claimed",
			state: sdk.WorkStateClaimed,
			prep: func(t *testing.T, clock *manualClock) *terminalwork.WorkItem {
				t.Helper()
				w := newIntent(t, "mx-claimed", "wc", "p", sdk.WorkKindSettleAttemptProvider)
				_ = w.MarkPending()
				if err := w.Claim("owner-a", time.Minute, clock); err != nil {
					t.Fatal(err)
				}
				return w
			},
			legal: map[workOp]error{
				opPending:    sdk.ErrInvalidTransition,
				opClaim:      sdk.ErrClaimLeaseHeld, // lease still held
				opRenewClaim: nil,
				opComplete:   nil,
				opRetry:      nil,
				opQuarantine: nil,
			},
		},
		{
			name:  "retry_not_due",
			state: sdk.WorkStateRetry,
			prep: func(t *testing.T, clock *manualClock) *terminalwork.WorkItem {
				t.Helper()
				w := newIntent(t, "mx-retry-nd", "wrnd", "p", sdk.WorkKindReleaseAttemptProvider)
				_ = w.MarkPending()
				_ = w.Claim("o", time.Minute, clock)
				if err := w.Retry(sched, clock, terminalwork.BoundedError{Code: "tmp", Permanent: false}); err != nil {
					t.Fatal(err)
				}
				return w
			},
			legal: map[workOp]error{
				opPending:    sdk.ErrInvalidTransition,
				opClaim:      sdk.ErrNotDue,
				opRenewClaim: sdk.ErrInvalidTransition,
				opComplete:   sdk.ErrInvalidTransition,
				opRetry:      sdk.ErrInvalidTransition,
				opQuarantine: nil,
			},
		},
		{
			name:  "retry_due",
			state: sdk.WorkStateRetry,
			prep: func(t *testing.T, clock *manualClock) *terminalwork.WorkItem {
				t.Helper()
				w := newIntent(t, "mx-retry-due", "wrd", "p", sdk.WorkKindCompensateProvider)
				_ = w.MarkPending()
				_ = w.Claim("o", time.Minute, clock)
				_ = w.Retry(sched, clock, terminalwork.BoundedError{Code: "tmp", Permanent: false})
				clock.Advance(time.Second)
				return w
			},
			legal: map[workOp]error{
				opPending:    sdk.ErrInvalidTransition,
				opClaim:      nil,
				opRenewClaim: sdk.ErrInvalidTransition,
				opComplete:   sdk.ErrInvalidTransition,
				opRetry:      sdk.ErrInvalidTransition,
				opQuarantine: nil,
			},
		},
		{
			name:  "completed",
			state: sdk.WorkStateCompleted,
			prep: func(t *testing.T, clock *manualClock) *terminalwork.WorkItem {
				t.Helper()
				w := newIntent(t, "mx-done", "wd", "p", sdk.WorkKindAppendFact)
				_ = w.MarkPending()
				_ = w.Claim("o", time.Minute, clock)
				_ = w.Complete()
				return w
			},
			legal: map[workOp]error{
				opPending:    sdk.ErrInvalidTransition,
				opClaim:      sdk.ErrInvalidTransition,
				opRenewClaim: sdk.ErrInvalidTransition,
				opComplete:   sdk.ErrInvalidTransition,
				opRetry:      sdk.ErrInvalidTransition,
				opQuarantine: sdk.ErrInvalidTransition,
			},
		},
		{
			name:  "quarantined",
			state: sdk.WorkStateQuarantined,
			prep: func(t *testing.T, clock *manualClock) *terminalwork.WorkItem {
				t.Helper()
				w := newIntent(t, "mx-q", "wq", "p", sdk.WorkKindAuthoritativeCorrection)
				_ = w.MarkPending()
				_ = w.Quarantine(terminalwork.BoundedError{Code: "bad", Permanent: true})
				return w
			},
			legal: map[workOp]error{
				opPending:    sdk.ErrInvalidTransition,
				opClaim:      sdk.ErrInvalidTransition,
				opRenewClaim: sdk.ErrInvalidTransition,
				opComplete:   sdk.ErrInvalidTransition,
				opRetry:      sdk.ErrInvalidTransition,
				opQuarantine: sdk.ErrInvalidTransition,
			},
		},
	}

	for _, fx := range fixtures {
		for _, op := range allWorkOps() {
			wantErr, ok := fx.legal[op]
			if !ok {
				t.Fatalf("fixture %s missing op %s", fx.name, op)
			}
			t.Run(fx.name+"/"+string(op), func(t *testing.T) {
				t.Parallel()
				clock := &manualClock{t: base}
				w := fx.prep(t, clock)
				if w.State != fx.state {
					t.Fatalf("prep state=%q want %q", w.State, fx.state)
				}
				err := applyWorkOp(w, op, clock, sched)
				if wantErr == nil {
					if err != nil {
						t.Fatalf("expected success, got %v", err)
					}
					return
				}
				if !errors.Is(err, wantErr) {
					t.Fatalf("err=%v want %v", err, wantErr)
				}
				if w.State != fx.state {
					t.Fatalf("failed op mutated state %q -> %q", fx.state, w.State)
				}
			})
		}
	}
}

func applyWorkOp(w *terminalwork.WorkItem, op workOp, clock *manualClock, sched terminalwork.RetrySchedule) error {
	switch op {
	case opPending:
		return w.MarkPending()
	case opClaim:
		return w.Claim("worker-mx", time.Minute, clock)
	case opRenewClaim:
		owner := w.Lease.OwnerID
		if owner == "" {
			owner = "worker-mx"
		}
		return w.RenewClaim(owner, time.Minute, clock)
	case opComplete:
		return w.Complete()
	case opRetry:
		return w.Retry(sched, clock, terminalwork.BoundedError{Code: "tmp", Permanent: false})
	case opQuarantine:
		return w.Quarantine(terminalwork.BoundedError{Code: "q", Permanent: true})
	default:
		return errors.New("unknown op")
	}
}
