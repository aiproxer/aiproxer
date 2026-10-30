package keepwarm

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/promptcache"
)

type fixedResultController struct {
	response promptcache.RenewResponse
	err      error
	calls    atomic.Int64
	releases atomic.Int64
}

type releaseTrackingController struct {
	fixedResultController
	releaseStarted chan struct{}
	unblockFirst   chan struct{}
}

func (c *releaseTrackingController) Release(context.Context, promptcache.ReleaseRequest) error {
	if c.releases.Add(1) == 1 {
		close(c.releaseStarted)
		<-c.unblockFirst
	}
	return nil
}

func (c *fixedResultController) Renew(context.Context, promptcache.RenewRequest) (promptcache.RenewResponse, error) {
	c.calls.Add(1)
	return c.response, c.err
}

func (c *fixedResultController) Release(context.Context, promptcache.ReleaseRequest) error {
	c.releases.Add(1)
	return nil
}

func armTestTarget(t *testing.T, m *Manager, ctl promptcache.Controller, observation promptcache.Observation) ArmResult {
	t.Helper()
	return m.ArmFromCommittedTurn(ArmInput{
		ALegID: "a", BLegID: "b", CommittedSuccessful: true, ToolEvents: osTool(),
		Observations: []promptcache.Observation{observation}, BackendInstanceID: "backend",
		CanonicalModelID: "model", Controller: ctl,
	})
}

func TestManagerMinimumResidencyNeedsExplicitHeuristic(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	ctl := &fixedResultController{}
	o := testObservation(now, promptcache.LifecycleMinimumResidency, 0)
	m, err := NewManager(DefaultConfig(), clock, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	if got := armTestTarget(t, m, ctl, o); got.Armed {
		t.Fatalf("minimum-residency target unexpectedly armed: %+v", got)
	}
	cfg := DefaultConfig()
	cfg.HeuristicOverrides = []HeuristicOverride{{BackendInstance: "backend", CanonicalModel: "model", Interval: time.Minute}}
	m, err = NewManager(cfg, clock, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	if got := armTestTarget(t, m, ctl, o); !got.Armed {
		t.Fatalf("heuristic target did not arm: %+v", got)
	}
	due, ok := m.NextDue()
	if !ok || !due.Equal(now.Add(time.Minute)) {
		t.Fatalf("due=%v ok=%v", due, ok)
	}
}

func TestManagerCapacityKeepsEarliestDueTarget(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	ctl := &fixedResultController{}
	cfg := DefaultConfig()
	cfg.MaxActiveTargets = 1
	m, err := NewManager(cfg, clock, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	late := testObservation(now, promptcache.LifecycleSlidingExpiry, 10*time.Minute)
	late.TargetID = "late"
	early := testObservation(now, promptcache.LifecycleSlidingExpiry, time.Minute)
	early.TargetID = "early"
	result := m.ArmFromCommittedTurn(ArmInput{ALegID: "a", BLegID: "b", CommittedSuccessful: true, ToolEvents: osTool(), Observations: []promptcache.Observation{late, early}, BackendInstanceID: "backend", CanonicalModelID: "model", Controller: ctl})
	if !result.Armed || result.TargetCount != 1 {
		t.Fatalf("result=%+v", result)
	}
	due, ok := m.NextDue()
	if !ok || due.After(now.Add(50*time.Second)) {
		t.Fatalf("earliest target was not retained: due=%v ok=%v", due, ok)
	}
	if err := m.Quiesce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ctl.releases.Load() < 1 {
		t.Fatalf("releases=%d", ctl.releases.Load())
	}
}

func TestManagerBudgetExpiryStopsWithoutProviderCall(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	ctl := &fixedResultController{}
	cfg := DefaultConfig()
	cfg.MaxIdleDuration = 10 * time.Second
	m, err := NewManager(cfg, clock, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	if got := armTestTarget(t, m, ctl, testObservation(now, promptcache.LifecycleSlidingExpiry, time.Minute)); !got.Armed {
		t.Fatal(got)
	}
	clock.Advance(11 * time.Second)
	m.RunDue(context.Background())
	m.renewWG.Wait()
	if ctl.calls.Load() != 0 || m.ActiveEpochCount() != 0 {
		t.Fatalf("calls=%d epochs=%d", ctl.calls.Load(), m.ActiveEpochCount())
	}
}

func TestManagerStopsOnStatusesAndDoesNotRetry(t *testing.T) {
	t.Parallel()
	statuses := []promptcache.RenewStatus{promptcache.Stale, promptcache.Unsupported, promptcache.ColdRecreated, promptcache.StillResident}
	for _, status := range statuses {
		t.Run(string(status), func(t *testing.T) {
			t.Parallel()
			now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
			clock := &testClock{now: now}
			ctl := &fixedResultController{response: promptcache.RenewResponse{Result: promptcache.RenewResult{Status: status}}}
			m, err := NewManager(DefaultConfig(), clock, Hooks{})
			if err != nil {
				t.Fatal(err)
			}
			if got := armTestTarget(t, m, ctl, testObservation(now, promptcache.LifecycleSlidingExpiry, time.Minute)); !got.Armed {
				t.Fatal(got)
			}
			clock.Advance(50 * time.Second)
			m.RunDue(context.Background())
			m.renewWG.Wait()
			if ctl.calls.Load() != 1 {
				t.Fatalf("calls=%d", ctl.calls.Load())
			}
			if m.ActiveEpochCount() != 0 {
				t.Fatalf("status %s retained epoch", status)
			}
		})
	}
}

func TestManagerQuiesceRejectsLateArmAndWaitsForRelease(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	ctl := &fixedResultController{}
	m, err := NewManager(DefaultConfig(), clock, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	if got := armTestTarget(t, m, ctl, testObservation(now, promptcache.LifecycleSlidingExpiry, time.Minute)); !got.Armed {
		t.Fatal(got)
	}
	if err := m.Quiesce(context.Background()); err != nil {
		t.Fatal(err)
	}
	late := armTestTarget(t, m, ctl, testObservation(now, promptcache.LifecycleSlidingExpiry, time.Minute))
	if late.Armed || late.Reason != "generation_quiescing" {
		t.Fatalf("late arm=%+v", late)
	}
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for ctl.releases.Load() < 2 {
		select {
		case <-deadline.C:
			t.Fatalf("releases=%d", ctl.releases.Load())
		case <-time.After(time.Millisecond):
		}
	}
}

func TestManagerQuiesceDoesNotDropReleasesAcrossEpochs(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	ctl := &releaseTrackingController{
		releaseStarted: make(chan struct{}),
		unblockFirst:   make(chan struct{}),
	}
	cfg := DefaultConfig()
	cfg.MaxActiveTargets = 3
	m, err := NewManager(cfg, clock, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	for _, aLegID := range []string{"a", "c", "d"} {
		o := testObservation(now, promptcache.LifecycleSlidingExpiry, time.Minute)
		o.ALegID = aLegID
		result := m.ArmFromCommittedTurn(ArmInput{
			ALegID: aLegID, BLegID: "b", CommittedSuccessful: true, ToolEvents: osTool(),
			Observations: []promptcache.Observation{o}, BackendInstanceID: "backend", Controller: ctl,
		})
		if !result.Armed {
			t.Fatalf("a-leg %q was not armed: %+v", aLegID, result)
		}
	}

	done := make(chan error, 1)
	go func() { done <- m.Quiesce(context.Background()) }()
	select {
	case <-ctl.releaseStarted:
	case <-time.After(time.Second):
		t.Fatal("first release did not start")
	}
	close(ctl.unblockFirst)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("quiesce did not finish")
	}
	if got := ctl.releases.Load(); got != 3 {
		t.Fatalf("releases=%d, expected every epoch target to be released", got)
	}
}

func TestManagerQuiesceRejectsConcurrentArmWhileShutdownIsInProgress(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
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
	clock.Advance(50 * time.Second)
	m.RunDue(context.Background())
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("renewal did not start")
	}

	short, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	if err := m.Quiesce(short); err == nil {
		t.Fatal("quiesce unexpectedly completed while renewal was blocked")
	}
	cancel()

	lateObservation := testObservation(now, promptcache.LifecycleSlidingExpiry, time.Minute)
	late := m.ArmFromCommittedTurn(ArmInput{
		ALegID: "a", BLegID: "b", CommittedSuccessful: true, ToolEvents: []aipapi.ToolEvent{{Kind: aipapi.ToolEventFinished, Category: aipapi.ToolCategoryOSCommand}},
		Observations: []promptcache.Observation{lateObservation}, BackendInstanceID: "backend", Controller: ctl,
	})
	if late.Armed || late.Reason != "generation_quiescing" {
		t.Fatalf("concurrent late arm = %+v", late)
	}

	close(unblock)
	if err := m.Quiesce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := ctl.releases.Load(); got != 2 {
		t.Fatalf("released handles = %d, want original and rejected late observation", got)
	}
}

func TestManagerControlErrorIsRetiredWithoutRetry(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	ctl := &fixedResultController{err: errors.New("provider unavailable")}
	m, err := NewManager(DefaultConfig(), clock, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	if got := armTestTarget(t, m, ctl, testObservation(now, promptcache.LifecycleSlidingExpiry, time.Minute)); !got.Armed {
		t.Fatal(got)
	}
	clock.Advance(50 * time.Second)
	m.RunDue(context.Background())
	m.renewWG.Wait()
	if ctl.calls.Load() != 1 || m.ActiveEpochCount() != 0 {
		t.Fatalf("calls=%d epochs=%d", ctl.calls.Load(), m.ActiveEpochCount())
	}
}
