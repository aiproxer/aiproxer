package keepwarm

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/promptcache"
)

// TestManagerRefreshCapKeepsInFlightSiblingsUntilCompletion pins the
// completion-side half of the refresh-cap rule: once the cap's jobs have been
// admitted, the first completion must not retire the epoch or cancel a sibling
// that is still in flight. Only the last completion retires the capped epoch,
// and no further refresh is admitted afterwards.
func TestManagerRefreshCapKeepsInFlightSiblingsUntilCompletion(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	cfg := DefaultConfig()
	cfg.MaxRefreshesPerIdleEpoch = 2
	cfg.MaxConcurrentRenewals = 2
	ctl := &testController{}
	m, err := NewManager(cfg, clock, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Quiesce(context.Background()) }()
	armed := m.ArmFromCommittedTurn(ArmInput{
		ALegID: "a", BLegID: "b", CommittedSuccessful: true, ToolEvents: osTool(),
		Observations: []promptcache.Observation{
			regressionObservation(now, "one", 10*time.Minute),
			regressionObservation(now, "two", 10*time.Minute),
		},
		BackendInstanceID: "backend", Controller: ctl,
	})
	if !armed.Armed || armed.TargetCount != 2 {
		t.Fatalf("arm = %+v", armed)
	}
	clock.Advance(9*time.Minute + 50*time.Second)
	first := m.claimDue(context.Background(), false)
	second := m.claimDue(context.Background(), false)
	if first == nil || second == nil {
		t.Fatal("two allowed jobs were not admitted")
	}
	if extra := m.claimDue(context.Background(), false); extra != nil {
		t.Fatal("refresh admitted beyond the cap")
	}
	renewed := regressionObservation(clock.Now(), "one", 10*time.Minute)
	firstRecord := &RenewalRecord{}
	m.apply(first, promptcache.RenewResponse{Result: promptcache.RenewResult{Status: promptcache.Renewed, Observation: &renewed}}, nil, firstRecord)
	if firstRecord.Stale {
		t.Fatal("first allowed completion was classified stale")
	}
	if second.ctx.Err() != nil {
		t.Fatalf("first completion canceled the in-flight sibling: %v", second.ctx.Err())
	}
	if got := m.ActiveEpochCount(); got != 1 {
		t.Fatalf("epoch retired while an allowed job was in flight: epochs=%d", got)
	}
	secondRecord := &RenewalRecord{}
	m.apply(second, promptcache.RenewResponse{Result: promptcache.RenewResult{Status: promptcache.StillResident, Observation: &renewed}}, nil, secondRecord)
	if secondRecord.Stale {
		t.Fatal("last allowed completion was classified stale")
	}
	if got := m.ActiveEpochCount(); got != 0 {
		t.Fatalf("capped epoch not retired after the last completion: epochs=%d", got)
	}
	if extra := m.claimDue(context.Background(), false); extra != nil {
		t.Fatal("refresh admitted after the cap")
	}
}

// blockingRenewController blocks every renewal until release (or forever when
// it must ignore cancellation) and reports when each call actually started and
// whether its context was canceled.
type blockingRenewController struct {
	started  chan struct{}
	released chan struct{}
	canceled atomic.Int64
	ignore   bool
}

func (c *blockingRenewController) Renew(ctx context.Context, req promptcache.RenewRequest) (promptcache.RenewResponse, error) {
	select {
	case c.started <- struct{}{}:
	default:
	}
	if c.ignore {
		<-c.released
		_ = ctx.Err()
		return promptcache.RenewResponse{}, context.Canceled
	}
	select {
	case <-c.released:
		return promptcache.RenewResponse{}, nil
	case <-ctx.Done():
		c.canceled.Add(1)
		return promptcache.RenewResponse{}, ctx.Err()
	}
}

func (c *blockingRenewController) Release(context.Context, promptcache.ReleaseRequest) error {
	return nil
}

// TestManagerQuiesceJoinsAdmittedRenewalsBeforeAccountingStops proves the
// shutdown join: with both admitted renewals still blocked, Quiesce must not
// return, and once it does no further accounting callback may fire. This is
// the deterministic channel schedule behind the public-runtime close proof.
func TestManagerQuiesceJoinsAdmittedRenewalsBeforeAccountingStops(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	cfg := DefaultConfig()
	cfg.MaxRefreshesPerIdleEpoch = 2
	cfg.MaxConcurrentRenewals = 2
	cfg.RenewTimeout = 5 * time.Second
	ctl := &blockingRenewController{
		started:  make(chan struct{}, 2),
		released: make(chan struct{}),
		ignore:   true,
	}
	var accounting atomic.Int64
	m, err := NewManager(cfg, clock, Hooks{Accounting: func(context.Context, RenewalRecord) error {
		accounting.Add(1)
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	armed := m.ArmFromCommittedTurn(ArmInput{
		ALegID: "a", BLegID: "b", CommittedSuccessful: true, ToolEvents: osTool(),
		Observations: []promptcache.Observation{
			regressionObservation(now, "one", 10*time.Minute),
			regressionObservation(now, "two", 10*time.Minute),
		},
		BackendInstanceID: "backend", Controller: ctl,
	})
	if !armed.Armed || armed.TargetCount != 2 {
		t.Fatalf("arm = %+v", armed)
	}
	clock.Advance(9*time.Minute + 50*time.Second)
	m.RunDue(context.Background())
	for i := range 2 {
		select {
		case <-ctl.started:
		case <-time.After(5 * time.Second):
			t.Fatalf("renewal %d never started", i)
		}
	}
	quiesceDone := make(chan error, 1)
	go func() { quiesceDone <- m.Quiesce(context.Background()) }()
	select {
	case err := <-quiesceDone:
		t.Fatalf("Quiesce returned while admitted renewals were still in flight: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(ctl.released)
	select {
	case err := <-quiesceDone:
		if err != nil {
			t.Fatalf("Quiesce: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Quiesce did not join the admitted renewals")
	}
	if got := m.ActiveEpochCount(); got != 0 {
		t.Fatalf("epochs after quiesce = %d, want 0", got)
	}
	settled := accounting.Load()
	time.Sleep(100 * time.Millisecond)
	if got := accounting.Load(); got != settled {
		t.Fatalf("accounting callback fired after Quiesce returned: %d -> %d", settled, got)
	}
}
