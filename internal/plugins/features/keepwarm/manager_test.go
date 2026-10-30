package keepwarm

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/promptcache"
)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *testClock) Advance(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }

type testController struct {
	calls        atomic.Int64
	releases     atomic.Int64
	started      chan struct{}
	operationIDs chan string
	unblock      chan struct{}
	ignoreCancel bool
}

func (c *testController) Renew(ctx context.Context, req promptcache.RenewRequest) (promptcache.RenewResponse, error) {
	c.calls.Add(1)
	if c.started != nil {
		select {
		case c.started <- struct{}{}:
		default:
		}
	}
	if c.operationIDs != nil {
		c.operationIDs <- req.OperationID
	}
	if c.unblock != nil {
		select {
		case <-c.unblock:
		case <-ctx.Done():
			if !c.ignoreCancel {
				return promptcache.RenewResponse{}, ctx.Err()
			}
			if c.unblock != nil {
				<-c.unblock
			}
		}
	}
	now := time.Now().UTC()
	input := int64(1)
	o := promptcache.Observation{ALegID: "a", BLegID: "b", BackendInstanceID: "backend", TargetID: "target", GenerationID: "gen", Lifecycle: promptcache.LifecycleSlidingExpiry, Timing: promptcache.Timing{ObservedAt: now, ExpiresAt: new(now.Add(time.Hour))}, Renewable: true, Handle: promptcache.Handle("new"), Evidence: promptcache.CacheEvidence{TotalTokens: &input}}
	return promptcache.RenewResponse{Result: promptcache.RenewResult{Status: promptcache.Renewed, Observation: &o}, Accounting: &promptcache.AccountingEvidence{TotalTokens: &input, Presence: aipapi.UsagePresence{TotalTokens: true}, Source: promptcache.AccountingSourceProviderReported, Authority: promptcache.AccountingAuthorityAuthoritative, Plane: promptcache.AccountingPlaneProviderBillable, DedupeKey: req.OperationID}}, nil
}

func (c *testController) Release(context.Context, promptcache.ReleaseRequest) error {
	c.releases.Add(1)
	return nil
}

func testObservation(now time.Time, life promptcache.LifecycleKind, expires time.Duration) promptcache.Observation {
	var exp *time.Time
	if expires > 0 {
		v := now.Add(expires)
		exp = &v
	}
	return promptcache.Observation{ALegID: "a", BLegID: "b", BackendInstanceID: "backend", TargetID: "target", GenerationID: "gen", Lifecycle: life, Timing: promptcache.Timing{ObservedAt: now, ExpiresAt: exp}, Renewable: true, Handle: promptcache.Handle("opaque")}
}

func osTool() []aipapi.ToolEvent {
	return []aipapi.ToolEvent{{Kind: aipapi.ToolEventFinished, ToolCallID: "t", ToolName: "bash", Category: aipapi.ToolCategoryOSCommand, MayMutateLocalFS: true}}
}

func newTestManager(t *testing.T, cfg Config, clock Clock, controller *testController) (*Manager, *testClock) {
	t.Helper()
	m, err := NewManager(cfg, clock, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	tc, ok := clock.(*testClock)
	if !ok {
		t.Fatalf("expected *testClock, got %T", clock)
	}
	return m, tc
}

func TestManagerArmsOnlyCommittedOSCommandWithDeterministicExpiry(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	ctl := &testController{}
	m, _ := newTestManager(t, DefaultConfig(), clock, ctl)
	result := m.ArmFromCommittedTurn(ArmInput{ALegID: "a", BLegID: "b", CommittedSuccessful: true, ToolEvents: osTool(), Observations: []promptcache.Observation{testObservation(now, promptcache.LifecycleSlidingExpiry, 5*time.Minute)}, BackendInstanceID: "backend", Controller: ctl})
	if !result.Armed || result.TargetCount != 1 {
		t.Fatalf("result=%+v", result)
	}
	due, ok := m.NextDue()
	if !ok {
		t.Fatal("missing due")
	}
	if due.Before(now.Add(4*time.Minute+22*time.Second)) || due.After(now.Add(4*time.Minute+30*time.Second)) {
		t.Fatalf("due=%v", due)
	}
	m.BeginForegroundTurn("a")
	if m.ActiveEpochCount() != 0 {
		t.Fatal("foreground did not invalidate")
	}
}

func TestManagerDoesNotReleaseDuplicateHandleStillRetainedByEpoch(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	ctl := &testController{}
	m, _ := newTestManager(t, DefaultConfig(), clock, ctl)
	o := testObservation(now, promptcache.LifecycleSlidingExpiry, time.Minute)
	result := m.ArmFromCommittedTurn(ArmInput{
		ALegID: "a", BLegID: "b", CommittedSuccessful: true, ToolEvents: osTool(),
		Observations: []promptcache.Observation{o, o}, BackendInstanceID: "backend", Controller: ctl,
	})
	if !result.Armed || result.TargetCount != 1 {
		t.Fatalf("result=%+v", result)
	}
	if err := m.Quiesce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := ctl.releases.Load(); got != 1 {
		t.Fatalf("duplicate handle was released with retained target: releases=%d", got)
	}
}

func TestManagerDoesNotArmUnsafeOrUncommittedTurns(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	for _, tc := range []struct {
		name      string
		committed bool
		events    []aipapi.ToolEvent
		life      promptcache.LifecycleKind
	}{{"uncommitted", false, osTool(), promptcache.LifecycleSlidingExpiry}, {"ordinary", true, nil, promptcache.LifecycleSlidingExpiry}, {"unknown lifetime", true, osTool(), promptcache.LifecycleUnknown}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			clock := &testClock{now: now}
			ctl := &testController{}
			m, _ := newTestManager(t, DefaultConfig(), clock, ctl)
			o := testObservation(now, tc.life, 0)
			r := m.ArmFromCommittedTurn(ArmInput{ALegID: "a", BLegID: "b", CommittedSuccessful: tc.committed, ToolEvents: tc.events, Observations: []promptcache.Observation{o}, BackendInstanceID: "backend", Controller: ctl})
			if r.Armed {
				t.Fatalf("unexpected arm=%+v", r)
			}
			if m.ActiveEpochCount() != 0 {
				t.Fatal("unexpected epoch")
			}
		})
	}
}

func TestManagerProviderTokenBudgetFailsClosedWithoutEstimate(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	clock := &testClock{now: now}
	ctl := &testController{}
	budget := int64(2)
	cfg := DefaultConfig()
	cfg.MaxProviderTokensPerIdleEpoch = &budget
	m, err := NewManager(cfg, clock, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	o := testObservation(now, promptcache.LifecycleSlidingExpiry, time.Minute)
	r := m.ArmFromCommittedTurn(ArmInput{ALegID: "a", BLegID: "b", CommittedSuccessful: true, ToolEvents: osTool(), Observations: []promptcache.Observation{o}, BackendInstanceID: "backend", Controller: ctl})
	if r.Armed {
		t.Fatalf("unknown evidence armed: %+v", r)
	}
	if ctl.calls.Load() != 0 {
		t.Fatal("budget admission called provider")
	}
}

func TestManagerOperationIDsAreUniqueAcrossGenerations(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cfg := DefaultConfig()
	cfg.MaxRefreshesPerIdleEpoch = 1
	cfg.MaxConcurrentRenewals = 1
	clock1 := &testClock{now: now}
	clock2 := &testClock{now: now}
	ctl1 := &testController{operationIDs: make(chan string, 1)}
	ctl2 := &testController{operationIDs: make(chan string, 1)}
	m1, err := NewManager(cfg, clock1, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	m2, err := NewManager(cfg, clock2, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	if result := armTestTarget(t, m1, ctl1, testObservation(now, promptcache.LifecycleSlidingExpiry, time.Minute)); !result.Armed {
		t.Fatal(result)
	}
	if result := armTestTarget(t, m2, ctl2, testObservation(now, promptcache.LifecycleSlidingExpiry, time.Minute)); !result.Armed {
		t.Fatal(result)
	}
	clock1.Advance(55 * time.Second)
	clock2.Advance(55 * time.Second)
	m1.RunDue(context.Background())
	m2.RunDue(context.Background())
	id1 := <-ctl1.operationIDs
	id2 := <-ctl2.operationIDs
	m1.renewWG.Wait()
	m2.renewWG.Wait()
	if id1 == "" || id1 == id2 {
		t.Fatalf("operation IDs = %q and %q, want distinct non-empty IDs", id1, id2)
	}
	parts := strings.Split(id1, ":")
	if len(parts) != 5 || parts[0] != "keepwarm" || len(parts[1]) != 32 {
		t.Fatalf("operation ID namespace is not a bounded process identity: %q", id1)
	}
	if _, err := hex.DecodeString(parts[1]); err != nil {
		t.Fatalf("operation ID namespace is not hexadecimal: %q: %v", parts[1], err)
	}
	_ = m1.Quiesce(context.Background())
	_ = m2.Quiesce(context.Background())
}

func TestManagerRetriesAccountingDeliveryWithinBoundedContext(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	ctl := &testController{}
	var calls atomic.Int64
	m, err := NewManager(Config{Enabled: true, MaxRefreshesPerIdleEpoch: 1, MaxIdleDuration: time.Hour, MaxActiveTargets: 4, MaxConcurrentRenewals: 1, RenewTimeout: time.Second}, clock, Hooks{
		Accounting: func(context.Context, RenewalRecord) error {
			if calls.Add(1) == 1 {
				return errors.New("transient accounting failure")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result := armTestTarget(t, m, ctl, testObservation(now, promptcache.LifecycleSlidingExpiry, time.Minute)); !result.Armed {
		t.Fatal(result)
	}
	clock.Advance(55 * time.Second)
	m.RunDue(context.Background())
	m.renewWG.Wait()
	if got := calls.Load(); got != 2 {
		t.Fatalf("accounting attempts=%d, want bounded retry after first failure", got)
	}
	if m.Metrics().Events["accounting_error"] != 0 {
		t.Fatal("successful accounting retry was recorded as a terminal accounting error")
	}
}

func TestManagerAccountingDeliveryHonorsCancellation(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	ctl := &testController{}
	accountingDone := make(chan struct{})
	m, err := NewManager(Config{Enabled: true, MaxRefreshesPerIdleEpoch: 1, MaxIdleDuration: time.Hour, MaxActiveTargets: 4, MaxConcurrentRenewals: 1, RenewTimeout: 10 * time.Millisecond}, clock, Hooks{
		Accounting: func(ctx context.Context, _ RenewalRecord) error {
			<-ctx.Done()
			close(accountingDone)
			return ctx.Err()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result := armTestTarget(t, m, ctl, testObservation(now, promptcache.LifecycleSlidingExpiry, time.Minute)); !result.Armed {
		t.Fatal(result)
	}
	clock.Advance(55 * time.Second)
	m.RunDue(context.Background())
	m.renewWG.Wait()
	select {
	case <-accountingDone:
	case <-time.After(time.Second):
		t.Fatal("accounting callback was not canceled by its bound")
	}
	if m.Metrics().Events["accounting_error"] == 0 {
		t.Fatal("bounded accounting failure was not recorded")
	}
}

func TestManagerConsumesRefreshSlotAndDoesNotRetryControlFailure(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	clock := &testClock{now: now}
	ctl := &testController{}
	m, _ := newTestManager(t, Config{Enabled: true, MaxRefreshesPerIdleEpoch: 1, MaxIdleDuration: time.Hour, MaxActiveTargets: 4, MaxConcurrentRenewals: 1, RenewTimeout: time.Second}, clock, ctl)
	m.ArmFromCommittedTurn(ArmInput{ALegID: "a", BLegID: "b", CommittedSuccessful: true, ToolEvents: osTool(), Observations: []promptcache.Observation{testObservation(now, promptcache.LifecycleSlidingExpiry, time.Minute)}, BackendInstanceID: "backend", Controller: ctl})
	clock.Advance(55 * time.Second)
	m.RunDue(context.Background())
	m.renewWG.Wait()
	if ctl.calls.Load() != 1 {
		t.Fatalf("calls=%d", ctl.calls.Load())
	}
	if m.ActiveEpochCount() != 0 {
		t.Fatal("refresh budget did not retire epoch")
	}
}

func TestManagerForegroundCancellationAndStaleAccounting(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	clock := &testClock{now: now}
	started := make(chan struct{}, 1)
	unblock := make(chan struct{})
	ctl := &testController{started: started, unblock: unblock, ignoreCancel: true}
	var got atomic.Int64
	var stale atomic.Bool
	m, err := NewManager(DefaultConfig(), clock, Hooks{Accounting: func(_ context.Context, r RenewalRecord) error { got.Add(1); stale.Store(r.Stale); return nil }})
	if err != nil {
		t.Fatal(err)
	}
	m.ArmFromCommittedTurn(ArmInput{ALegID: "a", BLegID: "b", CommittedSuccessful: true, ToolEvents: osTool(), Observations: []promptcache.Observation{testObservation(now, promptcache.LifecycleSlidingExpiry, time.Minute)}, BackendInstanceID: "backend", Controller: ctl})
	clock.Advance(55 * time.Second)
	m.RunDue(context.Background())
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("renewal did not start")
	}
	m.BeginForegroundTurn("a")
	close(unblock)
	m.renewWG.Wait()
	if got.Load() != 1 || !stale.Load() {
		t.Fatalf("accounting=%d stale=%v", got.Load(), stale.Load())
	}
	if m.ActiveEpochCount() != 0 {
		t.Fatal("stale result recreated epoch")
	}
}

func TestManagerRunDuePropagatesCallerCancellation(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	clock := &testClock{now: now}
	started := make(chan struct{}, 1)
	unblock := make(chan struct{})
	ctl := &testController{started: started, unblock: unblock}
	m, _ := newTestManager(t, DefaultConfig(), clock, ctl)
	if result := armTestTarget(t, m, ctl, testObservation(now, promptcache.LifecycleSlidingExpiry, time.Minute)); !result.Armed {
		t.Fatal(result)
	}
	clock.Advance(55 * time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	m.RunDue(ctx)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("renewal did not start")
	}
	cancel()

	done := make(chan struct{})
	go func() {
		m.renewWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		close(unblock)
		<-done
		t.Fatal("caller cancellation did not stop renewal")
	}
	if err := m.Quiesce(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestManagerRunDueDiscardsStaleHeapAfterForeground(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	ctl := &testController{}
	m, _ := newTestManager(t, DefaultConfig(), clock, ctl)
	if result := armTestTarget(t, m, ctl, testObservation(now, promptcache.LifecycleSlidingExpiry, time.Minute)); !result.Armed {
		t.Fatal(result)
	}
	m.BeginForegroundTurn("a")
	done := make(chan struct{})
	go func() {
		m.RunDue(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RunDue did not discard stale heap entries")
	}
}

func TestManagerRejectsObservationFromAnotherBackendInstance(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	ctl := &testController{}
	m, _ := newTestManager(t, DefaultConfig(), clock, ctl)
	o := testObservation(now, promptcache.LifecycleSlidingExpiry, time.Minute)
	o.BackendInstanceID = "different-backend"
	result := m.ArmFromCommittedTurn(ArmInput{
		ALegID: "a", BLegID: "b", CommittedSuccessful: true, ToolEvents: osTool(),
		Observations: []promptcache.Observation{o}, BackendInstanceID: "backend", Controller: ctl,
	})
	if result.Armed || result.Reason != "no_eligible_target" {
		t.Fatalf("result=%+v", result)
	}
}

func TestManagerRegistryDisablePreventsLaterArm(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	ctl := &testController{}
	m, err := NewManager(DefaultConfig(), clock, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewManagerRegistry()
	id, err := registry.Register(m)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = registry.Unregister(id); _ = m.Quiesce(context.Background()) }()
	registry.Disable("a")
	result := armTestTarget(t, m, ctl, testObservation(now, promptcache.LifecycleSlidingExpiry, time.Minute))
	if result.Armed || result.Reason != "disabled_session" {
		t.Fatalf("result=%+v", result)
	}
}

func TestPolicyStoreRejectsCapacityWithoutEviction(t *testing.T) {
	t.Parallel()
	s, err := NewPolicyStore(1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Disable("a", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Disable("b", time.Now()); !errors.Is(err, ErrPolicyCapacity) {
		t.Fatalf("err=%v", err)
	}
	if _, ok := s.Get("a"); !ok {
		t.Fatal("existing disable evicted")
	}
	if err := s.Clear("b"); !errors.Is(err, ErrPolicyNotFound) {
		t.Fatalf("err=%v", err)
	}
}
