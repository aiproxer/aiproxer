package keepwarm

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/promptcache"
)

// clockRenewController renews deterministically against the injected clock so
// rescheduling math stays on the fake clock instead of wall time.
type clockRenewController struct {
	clock Clock
	calls atomic.Int64
	one   int64
}

func (c *clockRenewController) Renew(ctx context.Context, req promptcache.RenewRequest) (promptcache.RenewResponse, error) {
	c.calls.Add(1)
	now := c.clock.Now()
	input := c.one
	o := promptcache.Observation{
		ALegID: "a", BLegID: "b", BackendInstanceID: "backend", TargetID: "target", GenerationID: "gen",
		Lifecycle: promptcache.LifecycleSlidingExpiry, Renewable: true,
		Timing: promptcache.Timing{ObservedAt: now, ExpiresAt: new(now.Add(30 * time.Minute))},
		Handle: promptcache.Handle("new"), Evidence: promptcache.CacheEvidence{TotalTokens: &input},
	}
	return promptcache.RenewResponse{
		Result: promptcache.RenewResult{Status: promptcache.Renewed, Observation: &o},
		Accounting: &promptcache.AccountingEvidence{
			TotalTokens: &input, Presence: aipapi.UsagePresence{TotalTokens: true},
			Source: promptcache.AccountingSourceProviderReported, Authority: promptcache.AccountingAuthorityAuthoritative,
			Plane: promptcache.AccountingPlaneProviderBillable, DedupeKey: req.OperationID,
		},
	}, nil
}

func (c *clockRenewController) Release(context.Context, promptcache.ReleaseRequest) error { return nil }

func regressionObservation(now time.Time, targetID string, expires time.Duration) promptcache.Observation {
	o := testObservation(now, promptcache.LifecycleSlidingExpiry, expires)
	o.TargetID = promptcache.TargetID(targetID)
	o.GenerationID = promptcache.GenerationID(targetID + "-gen")
	o.Evidence = promptcache.CacheEvidence{TotalTokens: &[]int64{1}[0]}
	return o
}

// #1: an administrative Clear must restore inheritance for the live generation
// manager, not merely remove the process-store entry.
func TestPolicyServiceClearRestoresLiveManagerInheritance(t *testing.T) {
	t.Parallel()
	store, err := NewPolicyStore(16)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewManagerRegistry()
	svc := NewPolicyService(store, registry, ClockFunc(time.Now))

	m, err := NewManager(DefaultConfig(), ClockFunc(time.Now), Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Register(m); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Quiesce(context.Background()) }()

	input := ArmInput{
		ALegID: "a", BLegID: "b", CommittedSuccessful: true,
		ToolEvents:        osTool(),
		Observations:      []promptcache.Observation{regressionObservation(time.Now().UTC(), "t1", time.Minute)},
		BackendInstanceID: "backend", CanonicalModelID: "model",
		Controller: &clockRenewController{clock: ClockFunc(time.Now)},
	}

	if _, err := svc.Disable("a"); err != nil {
		t.Fatal(err)
	}
	if r := m.ArmFromCommittedTurn(input); r.Armed || r.Reason != "disabled_session" {
		t.Fatalf("disable did not gate live manager: %+v", r)
	}

	if err := svc.Clear("a"); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Get("a"); ok {
		t.Fatalf("policy entry survived Clear")
	}
	// Clear must restore inheritance on the live generation so a subsequent
	// eligible committed turn can arm again.
	if r := m.ArmFromCommittedTurn(input); !r.Armed {
		t.Fatalf("Clear did not restore live manager inheritance: %+v", r)
	}
}

// #4: the per-epoch refresh cap must be enforced per dispatch inside one burst,
// not only at claimDue entry. A blocking controller keeps the asynchronous
// result-apply from invalidating the epoch mid-burst so dispatch counts are
// deterministic.
func TestManagerDispatchRespectsRefreshCapInsideBurst(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	unblock := make(chan struct{})
	ctl := &testController{started: make(chan struct{}, 16), unblock: unblock, ignoreCancel: true}
	cfg := DefaultConfig()
	cfg.MaxRefreshesPerIdleEpoch = 2
	cfg.MaxConcurrentRenewals = 4
	m, err := NewManager(cfg, clock, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	obs := []promptcache.Observation{
		regressionObservation(now, "t1", 10*time.Minute),
		regressionObservation(now, "t2", 10*time.Minute),
		regressionObservation(now, "t3", 10*time.Minute),
	}
	r := m.ArmFromCommittedTurn(ArmInput{ALegID: "a", BLegID: "b", CommittedSuccessful: true, ToolEvents: osTool(), Observations: obs, BackendInstanceID: "backend", Controller: ctl})
	if !r.Armed || r.TargetCount != 3 {
		t.Fatalf("arm=%+v", r)
	}
	// Advance just past every spread-adjusted due time but before ExpiresAt.
	clock.Advance(9*time.Minute + 50*time.Second)
	m.RunDue(context.Background())
	// All claimed jobs block inside Renew; wait until dispatches settle.
	deadline := time.Now().Add(500 * time.Millisecond)
	for ctl.calls.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	close(unblock)
	m.renewWG.Wait()
	if got := ctl.calls.Load(); got != 2 {
		t.Fatalf("refresh cap exceeded across burst: calls=%d want 2", got)
	}
	_ = m.Quiesce(context.Background())
}

// #4: the provider-token budget must be rechecked immediately before each
// dispatch; an epoch that has spent the budget must not fire another call.
func TestManagerDispatchRespectsProviderTokenBudget(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	budget := int64(1)
	cfg := DefaultConfig()
	cfg.MaxRefreshesPerIdleEpoch = 6
	cfg.MaxProviderTokensPerIdleEpoch = &budget
	ctl := &clockRenewController{clock: clock}
	m, err := NewManager(cfg, clock, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	o := regressionObservation(now, "t1", 10*time.Minute)
	r := m.ArmFromCommittedTurn(ArmInput{ALegID: "a", BLegID: "b", CommittedSuccessful: true, ToolEvents: osTool(), Observations: []promptcache.Observation{o}, BackendInstanceID: "backend", Controller: ctl})
	if !r.Armed {
		t.Fatalf("arm=%+v", r)
	}
	clock.Advance(9*time.Minute + 50*time.Second)
	m.RunDue(context.Background())
	m.renewWG.Wait()
	if got := ctl.calls.Load(); got != 1 {
		t.Fatalf("first dispatch calls=%d", got)
	}
	// Renewed reschedules on the fake clock; advance well past the next due.
	clock.Advance(2 * time.Hour)
	m.RunDue(context.Background())
	m.renewWG.Wait()
	if got := ctl.calls.Load(); got != 1 {
		t.Fatalf("token budget not rechecked at dispatch: calls=%d want 1", got)
	}
	_ = m.Quiesce(context.Background())
}

// #7: arm skip reasons must be recorded in the bounded metrics map.
func TestManagerRecordsArmSkipReasons(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	ctl := &testController{}
	m, err := NewManager(DefaultConfig(), clock, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	o := regressionObservation(now, "t1", time.Minute)
	for _, tc := range []struct {
		name      string
		events    []aipapi.ToolEvent
		reason    string
		committed bool
	}{
		{"no_os_command", nil, "no_os_command", true},
		{"uncommitted", osTool(), "uncommitted", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := m.ArmFromCommittedTurn(ArmInput{ALegID: "a", BLegID: "b", CommittedSuccessful: tc.committed, ToolEvents: tc.events, Observations: []promptcache.Observation{o}, BackendInstanceID: "backend", Controller: ctl})
			if r.Armed {
				t.Fatalf("unexpected arm %+v", r)
			}
			events := m.Metrics().Events
			if events[tc.reason] == 0 {
				t.Fatalf("skip reason metric %q not recorded: %+v", tc.reason, events)
			}
		})
	}
}

// #9: a foreground-turn invalidation must be attributed to the foreground
// cause; session-end invalidation must not be reported as a foreground cancel.
func TestManagerCancelMetricScopedToForeground(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	ctl := &testController{}
	m, err := NewManager(DefaultConfig(), clock, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	arm := func() {
		r := m.ArmFromCommittedTurn(ArmInput{ALegID: "a", BLegID: "b", CommittedSuccessful: true, ToolEvents: osTool(), Observations: []promptcache.Observation{regressionObservation(now, "t1", time.Minute)}, BackendInstanceID: "backend", Controller: ctl})
		if !r.Armed {
			t.Fatalf("arm=%+v", r)
		}
	}
	arm()
	m.BeginForegroundTurn("a")
	if m.Metrics().Events["cancel_foreground"] == 0 {
		t.Fatal("foreground cancel not recorded")
	}
	arm()
	m.EndSession("a")
	events := m.Metrics().Events
	if events["cancel_foreground"] != 1 {
		t.Fatalf("session-end invalidation misattributed to foreground: %+v", events)
	}
	if events["cancel_session_end"] == 0 {
		t.Fatalf("session-end cancel not recorded: %+v", events)
	}
	_ = m.Quiesce(context.Background())
}

// #9: observations whose backend instance differs from the committed controller
// must not be released through that controller.
func TestManagerDoesNotReleaseForeignBackendObservationThroughCommittedController(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	ctl := &testController{}
	m, err := NewManager(DefaultConfig(), clock, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	o := regressionObservation(now, "t1", time.Minute)
	o.BackendInstanceID = "other-backend"
	r := m.ArmFromCommittedTurn(ArmInput{ALegID: "a", BLegID: "b", CommittedSuccessful: true, ToolEvents: osTool(), Observations: []promptcache.Observation{o}, BackendInstanceID: "backend", Controller: ctl})
	if r.Armed || r.TargetCount != 0 {
		t.Fatalf("foreign observation armed: %+v", r)
	}
	// Drain the asynchronous release loop so the assertion is deterministic.
	if err := m.Quiesce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := ctl.releases.Load(); got != 0 {
		t.Fatalf("foreign observation released through wrong controller: releases=%d", got)
	}
}

func TestManagerMaxActiveTargetsIsGenerationWide(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	ctl := &releaseTrackingController{releaseStarted: make(chan struct{}), unblockFirst: make(chan struct{})}
	cfg := DefaultConfig()
	cfg.MaxActiveTargets = 1
	m, err := NewManager(cfg, clock, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	first := regressionObservation(now, "first", 10*time.Minute)
	first.ALegID = "a"
	first.BLegID = "b-a"
	if got := m.ArmFromCommittedTurn(ArmInput{ALegID: "a", BLegID: "b-a", CommittedSuccessful: true, ToolEvents: osTool(), Observations: []promptcache.Observation{first}, BackendInstanceID: "backend", Controller: ctl}); !got.Armed {
		t.Fatalf("first arm=%+v", got)
	}
	second := regressionObservation(now, "second", time.Minute)
	second.ALegID = "c"
	second.BLegID = "b-c"
	if got := m.ArmFromCommittedTurn(ArmInput{ALegID: "c", BLegID: "b-c", CommittedSuccessful: true, ToolEvents: osTool(), Observations: []promptcache.Observation{second}, BackendInstanceID: "backend", Controller: ctl}); !got.Armed {
		t.Fatalf("earlier second target should replace the global latest target: %+v", got)
	}
	if got := m.ActiveTargetCount(); got != 1 {
		t.Fatalf("active targets=%d, want generation-wide cap 1", got)
	}
	select {
	case <-ctl.releaseStarted:
	case <-time.After(time.Second):
		t.Fatal("displaced target release did not start")
	}
	if got := ctl.releases.Load(); got != 1 {
		t.Fatalf("displaced target releases=%d, want 1", got)
	}
	close(ctl.unblockFirst)
	if err := m.Quiesce(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestManagerCapacityDoesNotEvictInFlightTarget(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	started := make(chan struct{}, 1)
	unblock := make(chan struct{})
	firstController := &testController{started: started, unblock: unblock, ignoreCancel: true}
	secondController := &releaseTrackingController{releaseStarted: make(chan struct{}), unblockFirst: make(chan struct{})}
	cfg := DefaultConfig()
	cfg.MaxActiveTargets = 1
	m, err := NewManager(cfg, clock, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	first := regressionObservation(now, "first", 30*time.Second)
	first.ALegID = "a"
	first.BLegID = "b-a"
	if result := m.ArmFromCommittedTurn(ArmInput{ALegID: "a", BLegID: "b-a", CommittedSuccessful: true, ToolEvents: osTool(), Observations: []promptcache.Observation{first}, BackendInstanceID: "backend", Controller: firstController}); !result.Armed {
		t.Fatalf("first arm=%+v", result)
	}
	clock.Advance(20 * time.Second)
	m.RunDue(context.Background())
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first renewal did not start")
	}

	second := regressionObservation(clock.Now(), "second", time.Second)
	second.ALegID = "c"
	second.BLegID = "b-c"
	result := m.ArmFromCommittedTurn(ArmInput{ALegID: "c", BLegID: "b-c", CommittedSuccessful: true, ToolEvents: osTool(), Observations: []promptcache.Observation{second}, BackendInstanceID: "backend", Controller: secondController})
	if result.Armed {
		t.Fatalf("in-flight target was evicted for a new target: %+v", result)
	}
	if got := m.ActiveTargetCount(); got != 1 {
		t.Fatalf("active targets=%d, want in-flight target retained", got)
	}
	if got := firstController.releases.Load(); got != 0 {
		t.Fatalf("in-flight target was released during capacity replacement: %d", got)
	}
	select {
	case <-secondController.releaseStarted:
	case <-time.After(time.Second):
		t.Fatal("rejected target release did not start")
	}
	if got := secondController.releases.Load(); got != 1 {
		t.Fatalf("rejected target release=%d, want 1", got)
	}

	close(secondController.unblockFirst)
	close(unblock)
	m.renewWG.Wait()
	if err := m.Quiesce(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestManagerProviderBudgetDoesNotRefundCommittedSpend(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	budget := int64(1)
	cfg := DefaultConfig()
	cfg.MaxProviderTokensPerIdleEpoch = &budget
	ctl := &fixedResultController{response: promptcache.RenewResponse{Result: promptcache.RenewResult{Status: promptcache.Stale}}}
	m, err := NewManager(cfg, clock, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	first := regressionObservation(now, "first", time.Minute)
	second := regressionObservation(now, "second", 2*time.Minute)
	if got := m.ArmFromCommittedTurn(ArmInput{ALegID: "a", BLegID: "b", CommittedSuccessful: true, ToolEvents: osTool(), Observations: []promptcache.Observation{first, second}, BackendInstanceID: "backend", Controller: ctl}); !got.Armed {
		t.Fatalf("arm=%+v", got)
	}
	clock.Advance(55 * time.Second)
	m.RunDue(context.Background())
	m.renewWG.Wait()
	clock.Advance(2 * time.Minute)
	m.RunDue(context.Background())
	m.renewWG.Wait()
	if got := ctl.calls.Load(); got != 1 {
		t.Fatalf("committed provider spend was refunded: calls=%d, want 1", got)
	}
	_ = m.Quiesce(context.Background())
}

func TestManagerQuiesceTimeoutDoesNotHideInProgressShutdown(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	clock := &testClock{now: now}
	started := make(chan struct{}, 1)
	unblock := make(chan struct{})
	ctl := &testController{started: started, unblock: unblock, ignoreCancel: true}
	m, err := NewManager(DefaultConfig(), clock, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	if got := armTestTarget(t, m, ctl, testObservation(now, promptcache.LifecycleSlidingExpiry, time.Minute)); !got.Armed {
		t.Fatal(got)
	}
	clock.Advance(55 * time.Second)
	m.RunDue(context.Background())
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("renewal did not start")
	}
	short, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := m.Quiesce(short); err == nil {
		t.Fatal("timed quiesce unexpectedly completed while provider call was blocked")
	}
	secondDone := make(chan error, 1)
	go func() { secondDone <- m.Quiesce(context.Background()) }()
	select {
	case err := <-secondDone:
		t.Fatalf("second quiesce returned before shutdown completed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(unblock)
	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("second quiesce did not observe shared completion")
	}
}
