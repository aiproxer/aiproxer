package terminalwork_test

import (
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/terminalwork"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// Phase 4.1 RED/GREEN contracts: durable terminal-work domain
// (requirements 8.1–8.9, 13.1, 13.8; design D9, D17).

type manualClock struct{ t time.Time }

func (c *manualClock) Now() time.Time { return c.t }

func (c *manualClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

func validSource(key string) terminalwork.SourceKey {
	return terminalwork.SourceKey{IdentityVersion: 1, Key: key}
}

func newIntent(t *testing.T, key, workID, provider string, kind sdk.WorkKind) *terminalwork.WorkItem {
	t.Helper()
	w, err := terminalwork.NewIntent(
		validSource(key),
		workID,
		kind,
		provider,
		terminalwork.LifecycleCorrelation{RequestID: "req-1", AttemptID: "att-1", TraceID: "tr-1"},
		terminalwork.BoundVersions{GenerationID: "g1", ProviderID: provider, RatingID: "r1"},
	)
	if err != nil {
		t.Fatalf("NewIntent: %v", err)
	}
	if w.State != sdk.WorkStateIntent {
		t.Fatalf("state=%q want intent", w.State)
	}
	return w
}

func TestWorkItem_LegalTransitionMatrix(t *testing.T) {
	t.Parallel()
	clock := &manualClock{t: time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)}
	sched := terminalwork.RetrySchedule{Initial: time.Second, Multiplier: 2, Max: 8 * time.Second}

	type action string
	const (
		actPending    action = "pending"
		actClaim      action = "claim"
		actComplete   action = "complete"
		actRetry      action = "retry"
		actQuarantine action = "quarantine"
	)

	apply := func(w *terminalwork.WorkItem, a action) error {
		switch a {
		case actPending:
			return w.MarkPending()
		case actClaim:
			return w.Claim("worker-1", time.Minute, clock)
		case actComplete:
			return w.Complete()
		case actRetry:
			return w.Retry(sched, clock, terminalwork.BoundedError{Code: "timeout", Permanent: false})
		case actQuarantine:
			return w.Quarantine(terminalwork.BoundedError{Code: "malformed", Permanent: true, Message: "bad"})
		default:
			return errors.New("unknown action")
		}
	}

	// Happy path: intent -> pending -> claimed -> completed
	w := newIntent(t, "sk-happy", "w-happy", "prov-a", sdk.WorkKindSettleRequestProvider)
	for _, a := range []action{actPending, actClaim, actComplete} {
		if err := apply(w, a); err != nil {
			t.Fatalf("%s: %v", a, err)
		}
	}
	if w.State != sdk.WorkStateCompleted {
		t.Fatalf("state=%q", w.State)
	}

	// Retry path: claimed -> retry -> claim -> complete
	w2 := newIntent(t, "sk-retry", "w-retry", "prov-a", sdk.WorkKindReleaseAttemptProvider)
	_ = w2.MarkPending()
	_ = w2.Claim("worker-1", time.Minute, clock)
	if err := w2.Retry(sched, clock, terminalwork.BoundedError{Code: "ambiguous", Permanent: false}); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if w2.State != sdk.WorkStateRetry {
		t.Fatalf("state=%q", w2.State)
	}
	wantNext := clock.Now().Add(time.Second)
	if !w2.NextRetryAt.After(clock.Now()) {
		t.Fatalf("NextRetryAt=%v must be strictly after now=%v", w2.NextRetryAt, clock.Now())
	}
	if !w2.NextRetryAt.Equal(wantNext) {
		t.Fatalf("NextRetryAt=%v want %v", w2.NextRetryAt, wantNext)
	}
	if err := w2.Claim("worker-2", time.Minute, clock); !errors.Is(err, sdk.ErrNotDue) {
		t.Fatalf("claim before due: %v", err)
	}
	clock.Advance(time.Second)
	if err := w2.Claim("worker-2", time.Minute, clock); err != nil {
		t.Fatalf("claim when due: %v", err)
	}
	if err := w2.Complete(); err != nil {
		t.Fatalf("complete: %v", err)
	}

	illegal := []struct {
		name string
		prep func(*testing.T) *terminalwork.WorkItem
		act  action
	}{
		{
			name: "complete from intent",
			prep: func(t *testing.T) *terminalwork.WorkItem {
				t.Helper()
				return newIntent(t, "sk-ill-1", "w1", "", sdk.WorkKindAppendFact)
			},
			act: actComplete,
		},
		{
			name: "claim from intent",
			prep: func(t *testing.T) *terminalwork.WorkItem {
				t.Helper()
				return newIntent(t, "sk-ill-2", "w2", "", sdk.WorkKindAppendFact)
			},
			act: actClaim,
		},
		{
			name: "pending from claimed",
			prep: func(t *testing.T) *terminalwork.WorkItem {
				t.Helper()
				w := newIntent(t, "sk-ill-3", "w3", "p", sdk.WorkKindCompensateProvider)
				_ = w.MarkPending()
				_ = w.Claim("w", time.Minute, clock)
				return w
			},
			act: actPending,
		},
		{
			name: "retry from completed",
			prep: func(t *testing.T) *terminalwork.WorkItem {
				t.Helper()
				w := newIntent(t, "sk-ill-4", "w4", "p", sdk.WorkKindSettleAttemptProvider)
				_ = w.MarkPending()
				_ = w.Claim("w", time.Minute, clock)
				_ = w.Complete()
				return w
			},
			act: actRetry,
		},
		{
			name: "claim from quarantined",
			prep: func(t *testing.T) *terminalwork.WorkItem {
				t.Helper()
				w := newIntent(t, "sk-ill-5", "w5", "p", sdk.WorkKindSettleRequestProvider)
				_ = w.MarkPending()
				_ = w.Quarantine(terminalwork.BoundedError{Code: "bad", Permanent: true})
				return w
			},
			act: actClaim,
		},
	}
	for _, tc := range illegal {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			item := tc.prep(t)
			err := apply(item, tc.act)
			if !errors.Is(err, sdk.ErrInvalidTransition) {
				t.Fatalf("err=%v want ErrInvalidTransition", err)
			}
		})
	}
}

func TestWorkItem_IndependentProviderCompletion(t *testing.T) {
	t.Parallel()
	clock := &manualClock{t: time.Date(2026, 7, 18, 1, 0, 0, 0, time.UTC)}
	a := newIntent(t, "settle:req:prov-a", "wa", "prov-a", sdk.WorkKindSettleRequestProvider)
	b := newIntent(t, "settle:req:prov-b", "wb", "prov-b", sdk.WorkKindSettleRequestProvider)
	_ = a.MarkPending()
	_ = b.MarkPending()
	_ = a.Claim("w", time.Minute, clock)
	_ = b.Claim("w", time.Minute, clock)
	if err := a.Complete(); err != nil {
		t.Fatal(err)
	}
	// b remains independently claimable/completable; failure on b must not affect a
	if err := b.Retry(terminalwork.RetrySchedule{Initial: time.Second, Multiplier: 2, Max: time.Minute}, clock,
		terminalwork.BoundedError{Code: "timeout", Permanent: false}); err != nil {
		t.Fatal(err)
	}
	if a.State != sdk.WorkStateCompleted {
		t.Fatalf("provider a must stay completed, got %q", a.State)
	}
	if b.State != sdk.WorkStateRetry {
		t.Fatalf("provider b state=%q", b.State)
	}
	if a.SourceKey.String() == b.SourceKey.String() {
		t.Fatal("providers must have distinct source keys")
	}
}

func TestWorkItem_RenewClaim(t *testing.T) {
	t.Parallel()
	clock := &manualClock{t: time.Date(2026, 7, 18, 5, 0, 0, 0, time.UTC)}
	w := newIntent(t, "renew-key", "wr", "prov", sdk.WorkKindSettleRequestProvider)
	_ = w.MarkPending()
	if err := w.Claim("worker-a", time.Minute, clock); err != nil {
		t.Fatal(err)
	}
	wantExpires := clock.Now().Add(time.Minute)
	if !w.Lease.ExpiresAt.Equal(wantExpires) {
		t.Fatalf("ExpiresAt=%v want %v", w.Lease.ExpiresAt, wantExpires)
	}

	clock.Advance(30 * time.Second)
	if err := w.RenewClaim("worker-a", 2*time.Minute, clock); err != nil {
		t.Fatalf("renew: %v", err)
	}
	wantRenewed := clock.Now().Add(2 * time.Minute)
	if !w.Lease.ExpiresAt.Equal(wantRenewed) {
		t.Fatalf("renewed ExpiresAt=%v want %v", w.Lease.ExpiresAt, wantRenewed)
	}

	if err := w.RenewClaim("worker-b", time.Minute, clock); !errors.Is(err, sdk.ErrConflict) {
		t.Fatalf("wrong owner err=%v want ErrConflict", err)
	}

	clock.Advance(2 * time.Minute)
	if err := w.RenewClaim("worker-a", time.Minute, clock); !errors.Is(err, sdk.ErrClaimLeaseHeld) {
		t.Fatalf("expired lease err=%v want ErrClaimLeaseHeld", err)
	}

	w2 := newIntent(t, "renew-intent", "wri", "prov", sdk.WorkKindAppendFact)
	if err := w2.RenewClaim("worker-a", time.Minute, clock); !errors.Is(err, sdk.ErrInvalidTransition) {
		t.Fatalf("renew from intent err=%v want ErrInvalidTransition", err)
	}
}

func TestWorkItem_ClaimLeaseExpiry(t *testing.T) {
	t.Parallel()
	clock := &manualClock{t: time.Date(2026, 7, 18, 2, 0, 0, 0, time.UTC)}
	w := newIntent(t, "lease-key", "wl", "prov", sdk.WorkKindReleaseLeaseSet)
	_ = w.MarkPending()
	if err := w.Claim("worker-a", 5*time.Second, clock); err != nil {
		t.Fatal(err)
	}
	if err := w.Claim("worker-b", 5*time.Second, clock); !errors.Is(err, sdk.ErrClaimLeaseHeld) {
		t.Fatalf("err=%v want ErrClaimLeaseHeld", err)
	}
	clock.Advance(5 * time.Second)
	if err := w.Claim("worker-b", 5*time.Second, clock); err != nil {
		t.Fatalf("after expiry: %v", err)
	}
	if w.Lease.OwnerID != "worker-b" {
		t.Fatalf("lease owner=%q", w.Lease.OwnerID)
	}
	if w.State != sdk.WorkStateClaimed {
		t.Fatalf("state=%q", w.State)
	}
}

func TestWorkItem_QuarantinePermanentErrors(t *testing.T) {
	t.Parallel()
	clock := &manualClock{t: time.Date(2026, 7, 18, 3, 0, 0, 0, time.UTC)}
	w := newIntent(t, "bad-payload", "wq", "prov", sdk.WorkKindAuthoritativeCorrection)
	_ = w.MarkPending()
	_ = w.Claim("w", time.Minute, clock)
	err := w.Retry(terminalwork.RetrySchedule{Initial: time.Second, Multiplier: 2, Max: time.Minute}, clock,
		terminalwork.BoundedError{Code: "malformed", Permanent: true, Message: "shape"})
	if !errors.Is(err, sdk.ErrPermanent) {
		t.Fatalf("permanent retry err=%v want ErrPermanent", err)
	}
	if err := w.Quarantine(terminalwork.BoundedError{Code: "malformed", Permanent: true}); err != nil {
		t.Fatal(err)
	}
	if w.State != sdk.WorkStateQuarantined {
		t.Fatalf("state=%q", w.State)
	}
	if err := w.Claim("w2", time.Minute, clock); !errors.Is(err, sdk.ErrInvalidTransition) {
		t.Fatalf("claim quarantined: %v", err)
	}
	if err := w.Complete(); !errors.Is(err, sdk.ErrInvalidTransition) {
		t.Fatalf("complete quarantined: %v", err)
	}
}

func TestWorkItem_RetryScheduleBackoff(t *testing.T) {
	t.Parallel()
	sched := terminalwork.RetrySchedule{Initial: time.Second, Multiplier: 2, Max: 8 * time.Second}
	if got := sched.Delay(1); got != time.Second {
		t.Fatalf("attempt1=%v", got)
	}
	if got := sched.Delay(2); got != 2*time.Second {
		t.Fatalf("attempt2=%v", got)
	}
	if got := sched.Delay(3); got != 4*time.Second {
		t.Fatalf("attempt3=%v", got)
	}
	if got := sched.Delay(4); got != 8*time.Second {
		t.Fatalf("attempt4=%v", got)
	}
	if got := sched.Delay(5); got != 8*time.Second {
		t.Fatalf("attempt5 capped=%v", got)
	}

	clock := &manualClock{t: time.Date(2026, 7, 18, 4, 0, 0, 0, time.UTC)}
	w := newIntent(t, "backoff", "wb", "p", sdk.WorkKindAppendFact)
	_ = w.MarkPending()
	_ = w.Claim("w", time.Minute, clock)
	_ = w.Retry(sched, clock, terminalwork.BoundedError{Code: "tmp", Permanent: false})
	if w.Attempts != 1 {
		t.Fatalf("attempts=%d", w.Attempts)
	}
	clock.Advance(time.Second)
	_ = w.Claim("w", time.Minute, clock)
	_ = w.Retry(sched, clock, terminalwork.BoundedError{Code: "tmp", Permanent: false})
	if w.Attempts != 2 {
		t.Fatalf("attempts=%d", w.Attempts)
	}
	want := clock.Now().Add(2 * time.Second)
	if !w.NextRetryAt.Equal(want) {
		t.Fatalf("NextRetryAt=%v want %v", w.NextRetryAt, want)
	}
}

func TestSourceKey_ValidateAndString(t *testing.T) {
	t.Parallel()
	ok := terminalwork.SourceKey{IdentityVersion: 1, Key: "append_fact:req-1:v1"}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	if ok.String() != "v1:append_fact:req-1:v1" {
		t.Fatalf("String=%q", ok.String())
	}
	bad := []terminalwork.SourceKey{
		{IdentityVersion: 0, Key: "x"},
		{IdentityVersion: 1, Key: ""},
		{IdentityVersion: -1, Key: "x"},
		{IdentityVersion: 1, Key: "  "},
	}
	for _, k := range bad {
		if err := k.Validate(); err == nil {
			t.Fatalf("expected invalid: %+v", k)
		}
	}
}

func TestNewIntent_RequiresProviderWhenNeeded(t *testing.T) {
	t.Parallel()
	_, err := terminalwork.NewIntent(
		validSource("x"), "w", sdk.WorkKindSettleRequestProvider, "",
		terminalwork.LifecycleCorrelation{RequestID: "r"},
		terminalwork.BoundVersions{GenerationID: "g"},
	)
	if !errors.Is(err, sdk.ErrInvalid) {
		t.Fatalf("err=%v", err)
	}
}

func FuzzWorkItem_TransitionSequence(f *testing.F) {
	f.Add(uint8(0), uint8(1), uint8(2), uint8(3))
	f.Add(uint8(1), uint8(2), uint8(4), uint8(3))
	f.Fuzz(func(t *testing.T, a, b, c, d uint8) {
		clock := &manualClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
		sched := terminalwork.RetrySchedule{Initial: time.Millisecond, Multiplier: 2, Max: time.Second}
		w, err := terminalwork.NewIntent(
			validSource("fuzz"), "wf", sdk.WorkKindAppendFact, "",
			terminalwork.LifecycleCorrelation{RequestID: "r"},
			terminalwork.BoundVersions{GenerationID: "g"},
		)
		if err != nil {
			t.Fatal(err)
		}
		steps := []uint8{a, b, c, d}
		for _, s := range steps {
			switch s % 5 {
			case 0:
				_ = w.MarkPending()
			case 1:
				_ = w.Claim("w", time.Second, clock)
			case 2:
				_ = w.Complete()
			case 3:
				_ = w.Retry(sched, clock, terminalwork.BoundedError{Code: "t", Permanent: false})
				clock.Advance(sched.Delay(w.Attempts + 1))
			case 4:
				_ = w.Quarantine(terminalwork.BoundedError{Code: "q", Permanent: true})
			}
			if !w.State.IsKnown() {
				t.Fatalf("unknown state %q", w.State)
			}
			// Terminal states never leave.
			if w.State.IsTerminal() {
				prev := w.State
				_ = w.MarkPending()
				_ = w.Claim("w", time.Second, clock)
				_ = w.Complete()
				_ = w.Retry(sched, clock, terminalwork.BoundedError{Code: "t", Permanent: false})
				if w.State != prev {
					t.Fatalf("left terminal state %q -> %q", prev, w.State)
				}
				return
			}
		}
	})
}
