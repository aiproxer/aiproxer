package sessionclassification

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	featurestate "github.com/aiproxer/aiproxer/internal/plugins/features/sessionclassification"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

type countingStore struct {
	inner          featurestate.Store
	loads          atomic.Int64
	promotes       atomic.Int64
	claims         atomic.Int64
	completes      atomic.Int64
	loadHook       func(context.Context, featurestate.Key) error
	loadResultHook func(context.Context, featurestate.Key, featurestate.Record, bool, error) error
	promoteHook    func(context.Context, featurestate.Key, session.Classification, time.Time) error
}

func (s *countingStore) Load(ctx context.Context, key featurestate.Key) (featurestate.Record, bool, error) {
	s.loads.Add(1)
	if s.loadHook != nil {
		if err := s.loadHook(ctx, key); err != nil {
			return featurestate.Record{}, false, err
		}
	}
	record, found, err := s.inner.Load(ctx, key)
	if s.loadResultHook != nil {
		if hookErr := s.loadResultHook(ctx, key, record, found, err); hookErr != nil {
			return featurestate.Record{}, false, hookErr
		}
	}
	return record, found, err
}

func (s *countingStore) Promote(ctx context.Context, key featurestate.Key, proposal session.Classification, now time.Time) (featurestate.Record, bool, error) {
	s.promotes.Add(1)
	if s.promoteHook != nil {
		if err := s.promoteHook(ctx, key, proposal, now); err != nil {
			return featurestate.Record{}, false, err
		}
	}
	return s.inner.Promote(ctx, key, proposal, now)
}

func (s *countingStore) ClaimRemote(ctx context.Context, key featurestate.Key, now time.Time, maxAttempts uint32, leaseTTL, retryBackoff time.Duration) (featurestate.RemoteClaim, featurestate.Record, bool, error) {
	s.claims.Add(1)
	return s.inner.ClaimRemote(ctx, key, now, maxAttempts, leaseTTL, retryBackoff)
}

func (s *countingStore) CompleteRemote(ctx context.Context, claim featurestate.RemoteClaim, result featurestate.RemoteCompletion, now time.Time) (featurestate.Record, error) {
	s.completes.Add(1)
	return s.inner.CompleteRemote(ctx, claim, result, now)
}

func newCountingStore(t *testing.T) *countingStore {
	t.Helper()
	store, err := NewMemoryStore(MemoryStoreConfig{MaxEntries: 16, IdleTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	return &countingStore{inner: store}
}

func newCountingStoreWithClock(t *testing.T, now func() time.Time) *countingStore {
	t.Helper()
	store, err := NewMemoryStore(MemoryStoreConfig{MaxEntries: 16, IdleTTL: time.Hour}, WithClock(now))
	if err != nil {
		t.Fatal(err)
	}
	return &countingStore{inner: store}
}

func memoryStoreForTest(t *testing.T, store *countingStore) *MemoryStore {
	t.Helper()
	authority, ok := store.inner.(*MemoryStore)
	if !ok {
		t.Fatal("counting store does not wrap MemoryStore")
	}
	return authority
}

func newTestCoordinator(t *testing.T, store featurestate.Store, now func() time.Time) *Coordinator {
	t.Helper()
	return newTestCoordinatorWithConfig(t, store, CoordinatorConfig{
		CacheCapacity: 4,
		MaxInflight:   4,
		IdleTTL:       time.Minute,
		Now:           now,
	})
}

func newTestCoordinatorWithConfig(t *testing.T, store featurestate.Store, config CoordinatorConfig) *Coordinator {
	t.Helper()
	coordinator, err := NewCoordinator(store, config)
	if err != nil {
		t.Fatal(err)
	}
	return coordinator
}

type coordinatorFakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *coordinatorFakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *coordinatorFakeClock) Advance(by time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(by)
	c.mu.Unlock()
}

type operationGate struct {
	key     featurestate.Key
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newOperationGate(key featurestate.Key) *operationGate {
	return &operationGate{key: key, entered: make(chan struct{}), release: make(chan struct{})}
}

func (g *operationGate) wait(ctx context.Context, key featurestate.Key) error {
	if key != g.key {
		return nil
	}
	g.once.Do(func() { close(g.entered) })
	select {
	case <-g.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *operationGate) open() {
	select {
	case <-g.release:
	default:
		close(g.release)
	}
}

type signalDoneContext struct {
	context.Context
	signaled chan struct{}
	once     sync.Once
}

func newSignalDoneContext(ctx context.Context) *signalDoneContext {
	return &signalDoneContext{Context: ctx, signaled: make(chan struct{})}
}

func (c *signalDoneContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.signaled) })
	return c.Context.Done()
}

func waitForSignal(t *testing.T, signal <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

type loadObservation struct {
	record featurestate.Record
	found  bool
	err    error
}

type promoteObservation struct {
	record   featurestate.Record
	promoted bool
	err      error
}

func TestCoordinatorCoalescesLoadsAndKeepsKeysIndependent(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.September, 30, 12, 0, 0, 0, time.UTC)
	store := newCountingStore(t)
	keyA := featurestate.Key{Kind: featurestate.ScopeSecureSession, ID: "session-load-a"}
	keyB := featurestate.Key{Kind: featurestate.ScopeSecureSession, ID: "session-load-b"}
	gate := newOperationGate(keyA)
	store.loadHook = gate.wait
	coordinator := newTestCoordinator(t, store, func() time.Time { return now })
	defer gate.open()

	leader := make(chan loadObservation, 1)
	go func() {
		record, found, err := coordinator.Load(ctx, keyA)
		leader <- loadObservation{record: record, found: found, err: err}
	}()
	waitForSignal(t, gate.entered, "first authoritative Load")

	const waiterCount = 4
	waiters := make([]chan loadObservation, waiterCount)
	for i := range waiters {
		waiters[i] = make(chan loadObservation, 1)
		waitCtx := newSignalDoneContext(ctx)
		go func(result chan<- loadObservation, requestCtx context.Context) {
			record, found, err := coordinator.Load(requestCtx, keyA)
			result <- loadObservation{record: record, found: found, err: err}
		}(waiters[i], waitCtx)
		waitForSignal(t, waitCtx.signaled, "same-key Load waiter")
	}

	otherKey := make(chan loadObservation, 1)
	go func() {
		record, found, err := coordinator.Load(ctx, keyB)
		otherKey <- loadObservation{record: record, found: found, err: err}
	}()
	select {
	case got := <-otherKey:
		if got.err != nil || got.found {
			t.Fatalf("unrelated key Load() = (%+v, %v, %v); want unknown without error", got.record, got.found, got.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("unrelated key waited for the blocked Store.Load")
	}

	gate.open()
	if got := <-leader; got.err != nil || got.found {
		t.Fatalf("leader Load() = (%+v, %v, %v); want unknown", got.record, got.found, got.err)
	}
	for i, result := range waiters {
		if got := <-result; got.err != nil || got.found {
			t.Fatalf("same-key waiter %d Load() = (%+v, %v, %v); want shared unknown", i, got.record, got.found, got.err)
		}
	}
	if got := store.loads.Load(); got != 2 {
		t.Fatalf("Store.Load calls = %d; want one coalesced key-A load and one independent key-B load", got)
	}
	if _, found, err := coordinator.Load(ctx, keyA); err != nil || found {
		t.Fatalf("later unknown Load() = found %v, err %v; want fresh unknown", found, err)
	}
	if got := store.loads.Load(); got != 3 {
		t.Fatalf("later unknown turn reused a negative result: Store.Load calls = %d, want 3", got)
	}
}

func TestCoordinatorCoalescesFirstPromotionWithoutReplacingProposal(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.September, 30, 12, 0, 0, 0, time.UTC)
	store := newCountingStore(t)
	key := featurestate.Key{Kind: featurestate.ScopeSecureSession, ID: "session-promote"}
	gate := newOperationGate(key)
	store.promoteHook = func(ctx context.Context, key featurestate.Key, _ session.Classification, _ time.Time) error {
		return gate.wait(ctx, key)
	}
	coordinator := newTestCoordinator(t, store, func() time.Time { return now })
	defer gate.open()

	firstProposal := positiveProposal(session.SourceLocalIdentity, "client.identity")
	secondProposal := positiveProposal(session.SourceLocalTooling, "tooling.cluster")
	want := session.Classification{
		Kind:       session.KindCodingAgent,
		Source:     session.SourceLocalIdentity,
		Confidence: session.ConfidenceHigh,
		Evidence:   "client.identity",
		Revision:   1,
	}
	leader := make(chan promoteObservation, 1)
	go func() {
		record, promoted, err := coordinator.Promote(ctx, key, firstProposal, now)
		leader <- promoteObservation{record: record, promoted: promoted, err: err}
	}()
	waitForSignal(t, gate.entered, "first authoritative Promote")

	const waiterCount = 3
	waiters := make([]chan promoteObservation, waiterCount)
	for i := range waiters {
		waiters[i] = make(chan promoteObservation, 1)
		waitCtx := newSignalDoneContext(ctx)
		go func(result chan<- promoteObservation, requestCtx context.Context) {
			record, promoted, err := coordinator.Promote(requestCtx, key, secondProposal, now)
			result <- promoteObservation{record: record, promoted: promoted, err: err}
		}(waiters[i], waitCtx)
		waitForSignal(t, waitCtx.signaled, "same-key promotion waiter")
	}

	// An unknown load concurrent with a pending promotion must not become a
	// retained negative; the next turn sees the accepted positive.
	if _, found, err := coordinator.Load(ctx, key); err != nil || found {
		t.Fatalf("Load during pending Promote() = found %v, err %v; want current unknown", found, err)
	}
	gate.open()

	if got := <-leader; got.err != nil || !got.promoted || got.record.Classification != want {
		t.Fatalf("first Promote() = (%+v, %v, %v); want first proposal promoted", got.record, got.promoted, got.err)
	}
	for i, result := range waiters {
		if got := <-result; got.err != nil || got.promoted || got.record.Classification != want {
			t.Fatalf("promotion waiter %d = (%+v, %v, %v); want first record and promoted=false", i, got.record, got.promoted, got.err)
		}
	}
	if got := store.promotes.Load(); got != 1 {
		t.Fatalf("concurrent first promotions called Store.Promote %d times; want one", got)
	}
	if got := store.loads.Load(); got != 1 {
		t.Fatalf("pending unknown Load called Store.Load %d times; want one", got)
	}
	if record, found, err := coordinator.Load(ctx, key); err != nil || !found || record.Classification != want {
		t.Fatalf("later Load() = (%+v, %v, %v); want cached first positive", record, found, err)
	}
	if _, promoted, err := coordinator.Promote(ctx, key, secondProposal, now); err != nil || promoted {
		t.Fatalf("replayed Promote() = promoted %v, err %v; want idempotent false", promoted, err)
	}
	if got := store.promotes.Load(); got != 1 {
		t.Fatalf("warm positive issued %d Store.Promote calls; want one initial call", got)
	}
}

func TestCoordinatorPendingUnknownLoadCannotReplaceRemotePositive(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.September, 30, 12, 0, 0, 0, time.UTC)
	key := featurestate.Key{Kind: featurestate.ScopeSecureSession, ID: "session-pending-remote"}
	store := newCountingStore(t)
	authority := memoryStoreForTest(t, store)
	claim, _, ok, err := authority.ClaimRemote(ctx, key, now, 2, time.Minute, 0)
	if err != nil || !ok {
		t.Fatalf("setup ClaimRemote() = ok %v, err %v", ok, err)
	}
	loadRead := make(chan struct{})
	releaseLoad := make(chan struct{})
	var loadOnce sync.Once
	store.loadResultHook = func(ctx context.Context, key featurestate.Key, _ featurestate.Record, _ bool, err error) error {
		if key != claim.Key || err != nil {
			return err
		}
		loadOnce.Do(func() {
			close(loadRead)
			select {
			case <-releaseLoad:
			case <-ctx.Done():
			}
		})
		return ctx.Err()
	}
	coordinator := newTestCoordinator(t, store, func() time.Time { return now })
	defer func() {
		select {
		case <-releaseLoad:
		default:
			close(releaseLoad)
		}
	}()

	loadResult := make(chan loadObservation, 1)
	go func() {
		record, found, err := coordinator.Load(ctx, key)
		loadResult <- loadObservation{record: record, found: found, err: err}
	}()
	waitForSignal(t, loadRead, "authoritative unknown read before remote completion")

	want := session.Classification{
		Kind:       session.KindCodingAgent,
		Source:     session.SourceRemote,
		Confidence: session.ConfidenceHigh,
		Evidence:   "remote.threshold",
		Revision:   1,
	}
	completed, err := coordinator.CompleteRemote(ctx, claim, featurestate.RemoteCompletion{
		Proposal: positiveProposal(session.SourceRemote, "remote.threshold"),
	}, now.Add(time.Second))
	if err != nil || completed.Classification != want {
		t.Fatalf("CompleteRemote() = (%+v, %v); want remote positive", completed, err)
	}
	close(releaseLoad)

	if got := <-loadResult; got.err != nil || !got.found || got.record.Classification != want {
		t.Fatalf("pending Load() = (%+v, %v, %v); want concurrently accepted positive", got.record, got.found, got.err)
	}
	if got := store.loads.Load(); got != 1 {
		t.Fatalf("pending read count = %d; want one despite concurrent completion", got)
	}
	if cached, found, err := coordinator.Load(ctx, key); err != nil || !found || cached.Classification != want {
		t.Fatalf("later Load() = (%+v, %v, %v); want retained remote positive", cached, found, err)
	}
	if got := store.loads.Load(); got != 1 {
		t.Fatalf("stale unknown replaced the positive cache: Store.Load calls = %d, want one", got)
	}
}

func TestNewCoordinatorUsesBoundedDefaultsAndRejectsInvalidConfig(t *testing.T) {
	store := newCountingStore(t)
	coordinator, err := NewCoordinator(store, CoordinatorConfig{})
	if err != nil {
		t.Fatalf("NewCoordinator(defaults) error = %v", err)
	}
	if coordinator.config.CacheCapacity != 4096 || coordinator.config.MaxInflight != 1024 || coordinator.config.IdleTTL != 30*time.Minute || coordinator.now == nil {
		t.Fatalf("default config = %+v; want finite documented defaults", coordinator.config)
	}
	if store.loads.Load()+store.promotes.Load()+store.claims.Load()+store.completes.Load() != 0 {
		t.Fatal("NewCoordinator performed Store I/O")
	}
	if _, err := NewCoordinator(nil, CoordinatorConfig{}); !errors.Is(err, ErrInvalidCoordinatorConfig) {
		t.Fatalf("NewCoordinator(nil) error = %v; want invalid coordinator config", err)
	}
	invalid := []CoordinatorConfig{
		{CacheCapacity: -1},
		{CacheCapacity: 65537},
		{MaxInflight: -1},
		{MaxInflight: 65537},
		{IdleTTL: -time.Second},
		{IdleTTL: 30*24*time.Hour + time.Nanosecond},
	}
	for i, config := range invalid {
		if _, err := NewCoordinator(store, config); !errors.Is(err, ErrInvalidCoordinatorConfig) {
			t.Errorf("NewCoordinator(invalid[%d]) error = %v; want invalid coordinator config", i, err)
		}
	}
}

func TestCoordinatorCacheCapacityAndIdleExpiryReloadFromStore(t *testing.T) {
	ctx := context.Background()
	clock := &coordinatorFakeClock{now: time.Date(2026, time.September, 30, 12, 0, 0, 0, time.UTC)}
	store := newCountingStoreWithClock(t, clock.Now)
	authority := memoryStoreForTest(t, store)
	keyA := featurestate.Key{Kind: featurestate.ScopeSecureSession, ID: "session-cache-a"}
	keyB := featurestate.Key{Kind: featurestate.ScopeSecureSession, ID: "session-cache-b"}
	wantA := session.Classification{
		Kind:       session.KindCodingAgent,
		Source:     session.SourceLocalIdentity,
		Confidence: session.ConfidenceHigh,
		Evidence:   "identity.a",
		Revision:   1,
	}
	wantB := session.Classification{
		Kind:       session.KindCodingAgent,
		Source:     session.SourceLocalTooling,
		Confidence: session.ConfidenceHigh,
		Evidence:   "tools.b",
		Revision:   1,
	}
	wantByKey := map[featurestate.Key]session.Classification{keyA: wantA, keyB: wantB}
	if _, _, err := authority.Promote(ctx, keyA, positiveProposal(session.SourceLocalIdentity, "identity.a"), clock.Now()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := authority.Promote(ctx, keyB, positiveProposal(session.SourceLocalTooling, "tools.b"), clock.Now()); err != nil {
		t.Fatal(err)
	}
	coordinator := newTestCoordinatorWithConfig(t, store, CoordinatorConfig{
		CacheCapacity: 1,
		MaxInflight:   1,
		IdleTTL:       time.Minute,
		Now:           clock.Now,
	})

	for _, key := range []featurestate.Key{keyA, keyB, keyA} {
		record, found, err := coordinator.Load(ctx, key)
		if err != nil || !found || record.Classification != wantByKey[key] {
			t.Fatalf("Load(%q) = (%+v, %v, %v); want positive", key.ID, record, found, err)
		}
	}
	if got := store.loads.Load(); got != 3 {
		t.Fatalf("capacity eviction Store.Load calls = %d; want three misses for A, B, evicted A", got)
	}
	clock.Advance(time.Minute)
	record, found, err := coordinator.Load(ctx, keyA)
	if err != nil || !found || record.Classification != wantA {
		t.Fatalf("idle-expired Load() = (%+v, %v, %v); want durable positive A", record, found, err)
	}
	if got := store.loads.Load(); got != 4 {
		t.Fatalf("idle-expired positive reused local cache: Store.Load calls = %d, want four", got)
	}
	if len(coordinator.cache) > 1 {
		t.Fatalf("cache retained %d entries above configured capacity 1", len(coordinator.cache))
	}
}

func TestCoordinatorInflightCapacityBypassesCoordination(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.September, 30, 12, 0, 0, 0, time.UTC)
	store := newCountingStore(t)
	authority := memoryStoreForTest(t, store)
	keyA := featurestate.Key{Kind: featurestate.ScopeSecureSession, ID: "session-flight-a"}
	keyB := featurestate.Key{Kind: featurestate.ScopeSecureSession, ID: "session-flight-b"}
	wantB := session.Classification{
		Kind:       session.KindCodingAgent,
		Source:     session.SourceLocalTooling,
		Confidence: session.ConfidenceHigh,
		Evidence:   "tools.b",
		Revision:   1,
	}
	if _, _, err := authority.Promote(ctx, keyB, positiveProposal(session.SourceLocalTooling, "tools.b"), now); err != nil {
		t.Fatal(err)
	}
	gate := newOperationGate(keyA)
	store.loadHook = gate.wait
	coordinator := newTestCoordinatorWithConfig(t, store, CoordinatorConfig{
		CacheCapacity: 1,
		MaxInflight:   1,
		IdleTTL:       time.Minute,
		Now:           func() time.Time { return now },
	})
	defer gate.open()

	leader := make(chan loadObservation, 1)
	go func() {
		record, found, err := coordinator.Load(ctx, keyA)
		leader <- loadObservation{record: record, found: found, err: err}
	}()
	waitForSignal(t, gate.entered, "capacity-filling Load")

	bypass := make(chan loadObservation, 1)
	go func() {
		record, found, err := coordinator.Load(ctx, keyB)
		bypass <- loadObservation{record: record, found: found, err: err}
	}()
	select {
	case got := <-bypass:
		if got.err != nil || !got.found || got.record.Classification != wantB {
			t.Fatalf("capacity bypass Load() = (%+v, %v, %v); want stored positive B", got.record, got.found, got.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("new key was blocked instead of bypassing a full in-flight map")
	}
	coordinator.mu.Lock()
	flightCount := len(coordinator.flights)
	coordinator.mu.Unlock()
	if flightCount != 1 {
		t.Fatalf("in-flight map retained %d entries at configured capacity 1", flightCount)
	}
	loadsBeforeWarm := store.loads.Load()
	if record, found, err := coordinator.Load(ctx, keyB); err != nil || !found || record.Classification != wantB {
		t.Fatalf("bypassed positive was not cached: (%+v, %v, %v)", record, found, err)
	}
	if got := store.loads.Load(); got != loadsBeforeWarm {
		t.Fatalf("cached bypass result made another Store.Load: before %d, after %d", loadsBeforeWarm, got)
	}

	gate.open()
	if got := <-leader; got.err != nil || got.found {
		t.Fatalf("capacity-filling Load() = (%+v, %v, %v); want unknown", got.record, got.found, got.err)
	}
}

// TestCoordinatorPromoteBypassesCoordinationWhenInflightIsFull pins the
// MaxInflight contract for the promotion half of the keyed operations: when the
// in-flight map holds its capacity, a Promote for a key that owns no flight must
// still reach the durable store and cache its result rather than waiting for
// capacity or being refused.
//
// TestCoordinatorInflightCapacityBypassesCoordination pins the Load half of the
// same contract, so a regression that removed only the promotion bypass would
// leave that test green.
func TestCoordinatorPromoteBypassesCoordinationWhenInflightIsFull(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.September, 30, 12, 0, 0, 0, time.UTC)
	store := newCountingStore(t)
	heldKey := featurestate.Key{Kind: featurestate.ScopeSecureSession, ID: "session-bypass-held"}
	promotedKey := featurestate.Key{Kind: featurestate.ScopeSecureSession, ID: "session-bypass-promote"}
	gate := newOperationGate(heldKey)
	store.loadHook = gate.wait
	coordinator := newTestCoordinatorWithConfig(t, store, CoordinatorConfig{
		CacheCapacity: 4,
		MaxInflight:   1,
		IdleTTL:       time.Minute,
		Now:           func() time.Time { return now },
	})
	defer gate.open()

	held := make(chan loadObservation, 1)
	go func() {
		record, found, err := coordinator.Load(ctx, heldKey)
		held <- loadObservation{record: record, found: found, err: err}
	}()
	waitForSignal(t, gate.entered, "capacity-filling Load")

	// The in-flight map now holds its single permitted flight. This promotion
	// must bypass coordination instead of blocking behind it.
	record, promoted, err := coordinator.Promote(ctx, promotedKey,
		positiveProposal(session.SourceLocalIdentity, "bypass.promotion"), now)
	if err != nil || !promoted {
		t.Fatalf("bypass Promote() = promoted %v, err %v; want a durable first positive", promoted, err)
	}
	if record.Classification.Evidence != "bypass.promotion" {
		t.Fatalf("bypass Promote() evidence = %q; want the proposed evidence", record.Classification.Evidence)
	}
	if got := store.promotes.Load(); got != 1 {
		t.Fatalf("bypass Promote() issued %d Store.Promote calls; want one", got)
	}
	// The bypass result must still be cached: a repeated promotion is answered
	// from the process cache without touching the store again.
	if _, promotedAgain, err := coordinator.Promote(ctx, promotedKey,
		positiveProposal(session.SourceLocalIdentity, "bypass.promotion"), now); err != nil || promotedAgain {
		t.Fatalf("repeated Promote() = promoted %v, err %v; want the cached first positive", promotedAgain, err)
	}
	if got := store.promotes.Load(); got != 1 {
		t.Fatalf("cached bypass promotion issued %d Store.Promote calls; want one", got)
	}

	gate.open()
	if got := <-held; got.err != nil || got.found {
		t.Fatalf("capacity-filling Load() = (found %v, err %v); want unknown", got.found, got.err)
	}
}

func TestCoordinatorWaiterCancellationAndOwnerFailureRetry(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.September, 30, 12, 0, 0, 0, time.UTC)
	key := featurestate.Key{Kind: featurestate.ScopeSecureSession, ID: "session-cancel-waiter"}
	store := newCountingStore(t)
	gate := newOperationGate(key)
	store.loadHook = gate.wait
	coordinator := newTestCoordinator(t, store, func() time.Time { return now })
	defer gate.open()

	leader := make(chan loadObservation, 1)
	go func() {
		record, found, err := coordinator.Load(ctx, key)
		leader <- loadObservation{record: record, found: found, err: err}
	}()
	waitForSignal(t, gate.entered, "blocked Load owner")

	waiterCtx, cancelWaiter := context.WithCancel(ctx)
	signalCtx := newSignalDoneContext(waiterCtx)
	waiter := make(chan loadObservation, 1)
	go func() {
		record, found, err := coordinator.Load(signalCtx, key)
		waiter <- loadObservation{record: record, found: found, err: err}
	}()
	waitForSignal(t, signalCtx.signaled, "cancellable Load waiter")
	cancelWaiter()
	select {
	case got := <-waiter:
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("canceled waiter error = %v; want context.Canceled", got.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("canceled waiter did not return promptly")
	}
	gate.open()
	if got := <-leader; got.err != nil || got.found {
		t.Fatalf("Load owner = (%+v, %v, %v); want unknown", got.record, got.found, got.err)
	}
	if got := store.loads.Load(); got != 1 {
		t.Fatalf("canceled waiter started another Store.Load; total calls %d, want 1", got)
	}

	// Bun wraps a canceled database operation in a store error. A live waiter
	// must observe owner-context cancellation separately and retry on its context.
	ownerFailure := errors.New("bun load failed after owner cancellation")
	store = newCountingStore(t)
	authority := memoryStoreForTest(t, store)
	key = featurestate.Key{Kind: featurestate.ScopeSecureSession, ID: "session-cancel-owner"}
	want := session.Classification{
		Kind:       session.KindCodingAgent,
		Source:     session.SourceLocalIdentity,
		Confidence: session.ConfidenceHigh,
		Evidence:   "client.identity",
		Revision:   1,
	}
	if _, _, err := authority.Promote(ctx, key, positiveProposal(session.SourceLocalIdentity, "client.identity"), now); err != nil {
		t.Fatal(err)
	}
	ownerEntered := make(chan struct{})
	var firstCall atomic.Bool
	store.loadHook = func(ctx context.Context, _ featurestate.Key) error {
		if firstCall.CompareAndSwap(false, true) {
			close(ownerEntered)
			<-ctx.Done()
			return ownerFailure
		}
		return nil
	}
	coordinator = newTestCoordinator(t, store, func() time.Time { return now })
	ownerCtx, cancelOwner := context.WithCancel(ctx)
	ownerResult := make(chan loadObservation, 1)
	go func() {
		record, found, err := coordinator.Load(ownerCtx, key)
		ownerResult <- loadObservation{record: record, found: found, err: err}
	}()
	waitForSignal(t, ownerEntered, "owner Store.Load before wrapped error")

	followerCtx := newSignalDoneContext(ctx)
	followerResult := make(chan loadObservation, 1)
	go func() {
		record, found, err := coordinator.Load(followerCtx, key)
		followerResult <- loadObservation{record: record, found: found, err: err}
	}()
	waitForSignal(t, followerCtx.signaled, "live follower of canceled owner")
	cancelOwner()
	if got := <-ownerResult; !errors.Is(got.err, ownerFailure) {
		t.Fatalf("owner error = %v; want wrapped Store error", got.err)
	}
	if got := <-followerResult; got.err != nil || !got.found || got.record.Classification != want {
		t.Fatalf("live follower Load() = (%+v, %v, %v); want retry with positive record", got.record, got.found, got.err)
	}
	if got := store.loads.Load(); got != 2 {
		t.Fatalf("owner cancellation/follower retry Store.Load calls = %d; want 2", got)
	}

	// A later turn also starts a fresh operation after an ordinary store error.
	store = newCountingStore(t)
	coordinator = newTestCoordinator(t, store, func() time.Time { return now })
	loadFailure := errors.New("temporary Store.Load failure")
	firstCall = atomic.Bool{}
	store.loadHook = func(context.Context, featurestate.Key) error {
		if firstCall.CompareAndSwap(false, true) {
			return loadFailure
		}
		return nil
	}
	if _, _, err := coordinator.Load(ctx, key); !errors.Is(err, loadFailure) {
		t.Fatalf("first failed Load() error = %v; want temporary Store error", err)
	}
	if _, found, err := coordinator.Load(ctx, key); err != nil || found {
		t.Fatalf("Load after failed operation = found %v, err %v; want retryable unknown", found, err)
	}
	if got := store.loads.Load(); got != 2 {
		t.Fatalf("failed operation was retained: Store.Load calls = %d, want 2", got)
	}
}

type claimObservation struct {
	index  int
	claim  featurestate.RemoteClaim
	record featurestate.Record
	ok     bool
	err    error
}

func TestCoordinatorPreservesAtomicRemoteClaimsAndPositiveCompletion(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.September, 30, 12, 0, 0, 0, time.UTC)
	key := featurestate.Key{Kind: featurestate.ScopeSecureSession, ID: "session-remote-claim"}
	store := newCountingStore(t)
	first := newTestCoordinator(t, store, func() time.Time { return now })
	second := newTestCoordinator(t, store, func() time.Time { return now })
	coordinators := []*Coordinator{first, second}
	start := make(chan struct{})
	results := make(chan claimObservation, 2)
	for i, coordinator := range coordinators {
		go func(index int, coordinator *Coordinator) {
			<-start
			claim, record, ok, err := coordinator.ClaimRemote(ctx, key, now, 2, time.Minute, 5*time.Second)
			results <- claimObservation{index: index, claim: claim, record: record, ok: ok, err: err}
		}(i, coordinator)
	}
	close(start)

	winnerIndex := -1
	var winningClaim featurestate.RemoteClaim
	for range 2 {
		got := <-results
		if got.err != nil {
			t.Fatalf("ClaimRemote() error = %v", got.err)
		}
		if got.ok {
			if winnerIndex != -1 {
				t.Fatal("more than one concurrent caller acquired the remote lease")
			}
			winnerIndex = got.index
			winningClaim = got.claim
		}
	}
	if winnerIndex == -1 || store.claims.Load() != 2 {
		t.Fatalf("remote claim results had winner %d and %d Store calls; want exactly one of two", winnerIndex, store.claims.Load())
	}

	remoteProposal := positiveProposal(session.SourceRemote, "remote.threshold")
	want := session.Classification{
		Kind:       session.KindCodingAgent,
		Source:     session.SourceRemote,
		Confidence: session.ConfidenceHigh,
		Evidence:   "remote.threshold",
		Revision:   1,
	}
	completedAt := now.Add(time.Second)
	record, err := coordinators[winnerIndex].CompleteRemote(ctx, winningClaim, featurestate.RemoteCompletion{Proposal: remoteProposal}, completedAt)
	if err != nil || record.Classification != want || record.RemoteAttempts != 1 {
		t.Fatalf("positive CompleteRemote() = (%+v, %v); want remote positive and one attempt", record, err)
	}

	staleCoordinator := newTestCoordinator(t, store, func() time.Time { return now })
	staleRecord, staleErr := staleCoordinator.CompleteRemote(ctx, winningClaim, featurestate.RemoteCompletion{}, completedAt.Add(time.Second))
	if !errors.Is(staleErr, featurestate.ErrStaleRemoteClaim) || staleRecord.Classification != want {
		t.Fatalf("stale CompleteRemote() = (%+v, %v); want authoritative positive plus stale-claim error", staleRecord, staleErr)
	}
	loadsBeforeWarm := store.loads.Load()
	if cached, found, err := staleCoordinator.Load(ctx, key); err != nil || !found || cached.Classification != want {
		t.Fatalf("Load after stale completion = (%+v, %v, %v); want cached current positive", cached, found, err)
	}
	if got := store.loads.Load(); got != loadsBeforeWarm {
		t.Fatalf("stale completion did not retain its returned positive: loads before %d, after %d", loadsBeforeWarm, got)
	}

	claimsBefore := store.claims.Load()
	if _, cached, ok, err := staleCoordinator.ClaimRemote(ctx, key, completedAt.Add(2*time.Second), 2, time.Minute, 5*time.Second); err != nil || ok || cached.Classification != want {
		t.Fatalf("warm ClaimRemote() = (ok %v, %+v, %v); want no claim and current positive", ok, cached, err)
	}
	if _, _, _, err := staleCoordinator.ClaimRemote(ctx, key, completedAt.Add(2*time.Second), 0, time.Minute, 5*time.Second); !errors.Is(err, featurestate.ErrInvalidRemoteOptions) {
		t.Fatalf("invalid ClaimRemote() on cached positive error = %v; want validation error", err)
	}
	if got := store.claims.Load(); got != claimsBefore {
		t.Fatalf("cached ClaimRemote shortcuts called Store %d times; want zero", got-claimsBefore)
	}

	if _, promoted, err := staleCoordinator.Promote(ctx, key, positiveProposal(session.SourceLocalTooling, "later.local"), completedAt.Add(2*time.Second)); err != nil || promoted {
		t.Fatalf("later local Promote() = promoted %v, err %v; want existing remote positive", promoted, err)
	}
	if got := store.promotes.Load(); got != 0 {
		t.Fatalf("warm remote positive called Store.Promote %d times; want zero", got)
	}
	var nilContext context.Context
	if _, _, err := staleCoordinator.Load(nilContext, key); !errors.Is(err, featurestate.ErrInvalidStoreContext) {
		t.Fatalf("Load(nil context) on cached positive error = %v; want nil-context validation", err)
	}
	canceledContext, cancelContext := context.WithCancel(ctx)
	cancelContext()
	if _, _, err := staleCoordinator.Load(canceledContext, key); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Load() on cached positive error = %v; want context.Canceled", err)
	}
	if _, _, err := staleCoordinator.Load(ctx, featurestate.Key{Kind: featurestate.ScopeSecureSession}); !errors.Is(err, featurestate.ErrInvalidKey) {
		t.Fatalf("Load(invalid key) error = %v; want key validation", err)
	}
	if _, _, err := staleCoordinator.Promote(ctx, key, session.Classification{}, completedAt.Add(2*time.Second)); !errors.Is(err, featurestate.ErrInvalidProposal) {
		t.Fatalf("invalid Promote() on cached positive error = %v; want proposal validation", err)
	}
	if _, _, err := staleCoordinator.Promote(ctx, key, positiveProposal(session.SourceLocalTooling, "later.local"), time.Time{}); !errors.Is(err, featurestate.ErrInvalidStoreTime) {
		t.Fatalf("zero-time Promote() on cached positive error = %v; want timestamp validation", err)
	}
}

func TestCoordinatorWarmPositiveSkipsStore(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.September, 30, 12, 0, 0, 0, time.UTC)
	store := newCountingStore(t)
	coordinator := newTestCoordinator(t, store, func() time.Time { return now })
	key := featurestate.Key{Kind: featurestate.ScopeSecureSession, ID: "session-warm"}
	proposal := positiveProposal(session.SourceLocalIdentity, "client.identity")
	want := session.Classification{
		Kind:       session.KindCodingAgent,
		Source:     session.SourceLocalIdentity,
		Confidence: session.ConfidenceHigh,
		Evidence:   "client.identity",
		Revision:   1,
	}

	if _, promoted, err := coordinator.Promote(ctx, key, proposal, now); err != nil || !promoted {
		t.Fatalf("initial Promote() = promoted %v, err %v; want true, nil", promoted, err)
	}
	store.loads.Store(0)
	store.promotes.Store(0)
	store.claims.Store(0)
	store.completes.Store(0)

	for turn := range 3 {
		record, found, err := coordinator.Load(ctx, key)
		if err != nil || !found || record.Classification != want {
			t.Fatalf("warm Load() turn %d = (%+v, %v, %v); want original positive record", turn, record, found, err)
		}
	}
	if got := store.loads.Load(); got != 0 {
		t.Fatalf("warm positive required %d Store.Load calls; want zero", got)
	}
	if got := store.promotes.Load(); got != 0 {
		t.Fatalf("warm positive required %d Store.Promote calls; want zero", got)
	}
}

func TestCoordinatorLoadsPersistedPositiveIntoNewProcessCache(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.September, 30, 12, 0, 0, 0, time.UTC)
	store := newCountingStore(t)
	key := featurestate.Key{Kind: featurestate.ScopeSecureSession, ID: "session-restart"}
	want := session.Classification{
		Kind:       session.KindCodingAgent,
		Source:     session.SourceLocalIdentity,
		Confidence: session.ConfidenceHigh,
		Evidence:   "client.identity",
		Revision:   1,
	}
	if _, _, err := store.inner.Promote(ctx, key, positiveProposal(session.SourceLocalIdentity, "client.identity"), now); err != nil {
		t.Fatal(err)
	}

	coordinator := newTestCoordinator(t, store, func() time.Time { return now })
	record, found, err := coordinator.Load(ctx, key)
	if err != nil || !found || record.Classification != want {
		t.Fatalf("new coordinator Load() = (%+v, %v, %v); want previously stored positive", record, found, err)
	}
	if got := store.loads.Load(); got != 1 {
		t.Fatalf("first post-restart turn Store.Load calls = %d; want one cache-miss load", got)
	}
	if _, found, err := coordinator.Load(ctx, key); err != nil || !found {
		t.Fatalf("second post-restart turn Load() found %v, err %v; want cached positive", found, err)
	}
	if got := store.loads.Load(); got != 1 {
		t.Fatalf("post-restart warm turn did another Store.Load: calls = %d, want one", got)
	}
}

func TestCoordinatorUnknownIsReloadedAfterReplicaPromotion(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.September, 30, 12, 0, 0, 0, time.UTC)
	shared := newCountingStore(t)
	first := newTestCoordinator(t, shared, func() time.Time { return now })
	second := newTestCoordinator(t, shared, func() time.Time { return now })
	key := featurestate.Key{Kind: featurestate.ScopeSecureSession, ID: "session-replica"}

	if _, found, err := first.Load(ctx, key); err != nil || found {
		t.Fatalf("first unknown Load() = found %v, err %v; want unknown", found, err)
	}
	proposal := positiveProposal(session.SourceLocalTooling, "tooling.cluster")
	want := session.Classification{
		Kind:       session.KindCodingAgent,
		Source:     session.SourceLocalTooling,
		Confidence: session.ConfidenceHigh,
		Evidence:   "tooling.cluster",
		Revision:   1,
	}
	if _, promoted, err := second.Promote(ctx, key, proposal, now); err != nil || !promoted {
		t.Fatalf("replica Promote() = promoted %v, err %v; want true, nil", promoted, err)
	}

	record, found, err := first.Load(ctx, key)
	if err != nil || !found || record.Classification != want {
		t.Fatalf("later Load() = (%+v, %v, %v); want authoritative replica promotion", record, found, err)
	}
	if got := shared.loads.Load(); got != 2 {
		t.Fatalf("unknown turns issued %d Store.Load calls; want a fresh load per turn", got)
	}
}

func TestCoordinatorWaitersPreserveConcurrentPositiveAfterStoreError(t *testing.T) {
	for _, operation := range []string{"load", "promote"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, time.September, 30, 12, 0, 0, 0, time.UTC)
			key := featurestate.Key{Kind: featurestate.ScopeSecureSession, ID: "review-error-positive-" + operation}
			store := newCountingStore(t)
			authority := memoryStoreForTest(t, store)
			claim, _, claimed, err := authority.ClaimRemote(ctx, key, now, 2, time.Minute, 0)
			if err != nil || !claimed {
				t.Fatalf("setup ClaimRemote() = claimed %v, err %v; want active claim", claimed, err)
			}

			gate := newOperationGate(key)
			defer gate.open()
			storeErr := errors.New("ordinary non-context storage failure")
			if operation == "load" {
				store.loadHook = func(ctx context.Context, key featurestate.Key) error {
					if err := gate.wait(ctx, key); err != nil {
						return err
					}
					return storeErr
				}
			} else {
				store.promoteHook = func(ctx context.Context, key featurestate.Key, _ session.Classification, _ time.Time) error {
					if err := gate.wait(ctx, key); err != nil {
						return err
					}
					return storeErr
				}
			}

			coordinator := newTestCoordinator(t, store, func() time.Time { return now })
			call := func(ctx context.Context) loadObservation {
				if operation == "load" {
					record, found, err := coordinator.Load(ctx, key)
					return loadObservation{record: record, found: found, err: err}
				}
				record, _, err := coordinator.Promote(ctx, key, positiveProposal(session.SourceLocalTooling, "proposal.pending"), now)
				return loadObservation{record: record, found: record.Classification.IsCodingAgent(), err: err}
			}

			ownerResult := make(chan loadObservation, 1)
			go func() { ownerResult <- call(ctx) }()
			waitForSignal(t, gate.entered, "blocked Store operation owner")

			waiterCtx := newSignalDoneContext(ctx)
			waiterResult := make(chan loadObservation, 1)
			go func() { waiterResult <- call(waiterCtx) }()
			waitForSignal(t, waiterCtx.signaled, "joined flight waiter")

			remoteProposal := positiveProposal(session.SourceRemote, "remote.accepted")
			want := session.Classification{
				Kind:       session.KindCodingAgent,
				Source:     session.SourceRemote,
				Confidence: session.ConfidenceHigh,
				Evidence:   "remote.accepted",
				Revision:   1,
			}
			positive, err := coordinator.CompleteRemote(ctx, claim, featurestate.RemoteCompletion{Proposal: remoteProposal}, now.Add(time.Second))
			if err != nil || positive.Classification != want {
				t.Fatalf("concurrent CompleteRemote() = (%+v, %v); want independently specified positive %+v", positive.Classification, err, want)
			}
			gate.open()

			owner := <-ownerResult
			waiter := <-waiterResult
			for name, got := range map[string]loadObservation{"owner": owner, "waiter": waiter} {
				if got.err != nil || !got.found || got.record.Classification != want {
					t.Errorf("%s result = (found %v, classification %+v, err %v); want authoritative positive %+v", name, got.found, got.record.Classification, got.err, want)
				}
			}
			if operation == "load" && (store.loads.Load() != 1 || store.promotes.Load() != 0) {
				t.Errorf("coalesced Load made Store calls: Load=%d Promote=%d; want only one Load", store.loads.Load(), store.promotes.Load())
			}
			if operation == "promote" && (store.promotes.Load() != 1 || store.loads.Load() != 0) {
				t.Errorf("coalesced Promote made Store calls: Load=%d Promote=%d; want only one Promote", store.loads.Load(), store.promotes.Load())
			}
			if store.completes.Load() != 1 {
				t.Errorf("concurrent completion made %d Store.CompleteRemote calls; want one", store.completes.Load())
			}
		})
	}
}

// TestCoordinatorWaiterObservesTheFailedOwnerStoreCall pins failure propagation
// through a coalesced flight: when the single durable call fails and no positive
// is cached, every joined waiter must receive the bounded store error instead of
// a plain miss. A miss would make a durable failure indistinguishable from an
// unknown session and would report the operation as a miss to the observation
// surface.
func TestCoordinatorWaiterObservesTheFailedOwnerStoreCall(t *testing.T) {
	for _, operation := range []string{"load", "promote"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, time.September, 30, 12, 0, 0, 0, time.UTC)
			key := featurestate.Key{Kind: featurestate.ScopeSecureSession, ID: "flight-failure-" + operation}
			store := newCountingStore(t)
			gate := newOperationGate(key)
			storeErr := errors.New("durable classification operation failed")
			if operation == "load" {
				store.loadHook = func(ctx context.Context, key featurestate.Key) error {
					if err := gate.wait(ctx, key); err != nil {
						return err
					}
					return storeErr
				}
			} else {
				store.promoteHook = func(ctx context.Context, key featurestate.Key, _ session.Classification, _ time.Time) error {
					if err := gate.wait(ctx, key); err != nil {
						return err
					}
					return storeErr
				}
			}
			coordinator := newTestCoordinator(t, store, func() time.Time { return now })
			defer gate.open()

			call := func(ctx context.Context) loadObservation {
				if operation == "load" {
					record, found, err := coordinator.Load(ctx, key)
					return loadObservation{record: record, found: found, err: err}
				}
				record, _, err := coordinator.Promote(ctx, key, positiveProposal(session.SourceLocalTooling, "waiter.failure"), now)
				return loadObservation{record: record, err: err}
			}

			ownerResult := make(chan loadObservation, 1)
			go func() { ownerResult <- call(ctx) }()
			waitForSignal(t, gate.entered, "blocked Store operation owner")

			waiterCtx := newSignalDoneContext(ctx)
			waiterResult := make(chan loadObservation, 1)
			go func() { waiterResult <- call(waiterCtx) }()
			waitForSignal(t, waiterCtx.signaled, "joined flight waiter")

			gate.open()
			owner := <-ownerResult
			waiter := <-waiterResult
			for name, got := range map[string]loadObservation{"owner": owner, "waiter": waiter} {
				if !errors.Is(got.err, storeErr) {
					t.Errorf("%s %s error = %v; want the durable store failure", name, operation, got.err)
				}
			}
			if got := store.loads.Load() + store.promotes.Load(); got != 1 {
				t.Errorf("coalesced %s failure issued %d Store calls; want one", operation, got)
			}
		})
	}
}

// TestCoordinatorBypassedLoadSurfacesTheStoreFailure pins the failure behaviour of
// the capacity bypass: when the in-flight map is full and the direct durable load
// fails with no cached positive, the caller must receive the bounded store error
// rather than a plain unknown. The coordinated path returns the same error, so a
// bypass copy that swallowed it would hide durable failures only under capacity
// pressure.
func TestCoordinatorBypassedLoadSurfacesTheStoreFailure(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.September, 30, 12, 0, 0, 0, time.UTC)
	store := newCountingStore(t)
	heldKey := featurestate.Key{Kind: featurestate.ScopeSecureSession, ID: "bypass-failure-held"}
	failingKey := featurestate.Key{Kind: featurestate.ScopeSecureSession, ID: "bypass-failure-load"}
	gate := newOperationGate(heldKey)
	storeErr := errors.New("durable classification load failed")
	store.loadHook = func(ctx context.Context, key featurestate.Key) error {
		if key == heldKey {
			return gate.wait(ctx, key)
		}
		if key == failingKey {
			return storeErr
		}
		return nil
	}
	coordinator := newTestCoordinatorWithConfig(t, store, CoordinatorConfig{
		CacheCapacity: 4,
		MaxInflight:   1,
		IdleTTL:       time.Minute,
		Now:           func() time.Time { return now },
	})
	defer gate.open()

	held := make(chan loadObservation, 1)
	go func() {
		record, found, err := coordinator.Load(ctx, heldKey)
		held <- loadObservation{record: record, found: found, err: err}
	}()
	waitForSignal(t, gate.entered, "capacity-filling Load")

	if _, _, err := coordinator.Load(ctx, failingKey); !errors.Is(err, storeErr) {
		t.Fatalf("bypassed Load() error = %v; want the durable store failure", err)
	}

	gate.open()
	if got := <-held; got.err != nil || got.found {
		t.Fatalf("capacity-filling Load() = (found %v, err %v); want unknown", got.found, got.err)
	}
}

// TestCoordinatorCompleteRemoteFailureIsObservedAsAnError pins the bounded
// observation contract for the remote-completion failure path: a durable
// completion that fails for a reason other than a stale lease is reported as the
// closed error outcome, never as an unchanged completion. Without that case an
// observer would count durable failures as ordinary replays.
func TestCoordinatorCompleteRemoteFailureIsObservedAsAnError(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.September, 30, 12, 0, 0, 0, time.UTC)
	observer := &recordingStoreObserver{}
	store := newCountingStore(t)
	coordinator := newTestCoordinatorWithConfig(t, store, CoordinatorConfig{
		CacheCapacity: 4,
		MaxInflight:   4,
		IdleTTL:       time.Minute,
		Now:           func() time.Time { return now },
		Observer:      observer,
	})
	key := featurestate.Key{Kind: featurestate.ScopeSecureSession, ID: "complete-failure"}
	// An empty lease id is an unusable claim, so the durable store refuses it with
	// a bounded error that is not a stale-claim report.
	_, err := coordinator.CompleteRemote(ctx,
		featurestate.RemoteClaim{Key: key, Attempt: 1}, featurestate.RemoteCompletion{}, now)
	if err == nil || errors.Is(err, featurestate.ErrStaleRemoteClaim) {
		t.Fatalf("CompleteRemote() error = %v; want a bounded non-stale durable failure", err)
	}
	got := observer.storeObservations()
	want := []featurestate.StoreObservation{{
		Operation: featurestate.StoreOperationRemoteComplete,
		Outcome:   featurestate.StoreOutcomeError,
	}}
	if !slices.Equal(got, want) {
		t.Fatalf("durable completion observations = %+v; want %+v", got, want)
	}
}

// recordingStoreObserver keeps the bounded store observations one coordinator
// emits so a claim about the closed observation vocabulary is asserted without a
// metrics registry in the loop.
type recordingStoreObserver struct {
	mu   sync.Mutex
	seen []featurestate.StoreObservation
}

func (o *recordingStoreObserver) ObserveEvaluation(featurestate.EvaluationObservation) {}

func (o *recordingStoreObserver) ObserveTransition(featurestate.TransitionObservation) {}

func (o *recordingStoreObserver) ObserveRemote(featurestate.RemoteObservation) {}

func (o *recordingStoreObserver) ObserveStore(observation featurestate.StoreObservation) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.seen = append(o.seen, observation)
}

func (o *recordingStoreObserver) storeObservations() []featurestate.StoreObservation {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]featurestate.StoreObservation(nil), o.seen...)
}

func positiveProposal(source session.ClassificationSource, evidence session.EvidenceCode) session.Classification {
	return session.Classification{
		Kind:       session.KindCodingAgent,
		Source:     source,
		Confidence: session.ConfidenceHigh,
		Evidence:   evidence,
		Revision:   1,
	}
}
