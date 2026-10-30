package app_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/terminalwork"
	"github.com/aiproxer/aiproxer/internal/core/terminalwork/app"
	"github.com/aiproxer/aiproxer/internal/infra/terminalwork/workstore"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// Phase 4.4 RED/GREEN processor contracts (requirements 8.4–8.8, design D4, D5, D9).

type manualClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *manualClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

type fakeProvider struct {
	id    string
	kinds []sdk.WorkKind
	ver   string

	mu        sync.Mutex
	calls     []string
	inflight  atomic.Int32
	maxIn     atomic.Int32
	fn        func(ctx context.Context, rec terminalwork.WorkRecord, key string) error
	blockCh   chan struct{}
	enteredCh chan struct{}
}

func (p *fakeProvider) ProviderID() string             { return p.id }
func (p *fakeProvider) SupportedKinds() []sdk.WorkKind { return p.kinds }
func (p *fakeProvider) Version() string                { return p.ver }

func (p *fakeProvider) Invoke(ctx context.Context, rec terminalwork.WorkRecord, key string) error {
	n := p.inflight.Add(1)
	defer p.inflight.Add(-1)
	for {
		cur := p.maxIn.Load()
		if n <= cur || p.maxIn.CompareAndSwap(cur, n) {
			break
		}
	}
	p.mu.Lock()
	p.calls = append(p.calls, key)
	p.mu.Unlock()
	if p.enteredCh != nil {
		select {
		case p.enteredCh <- struct{}{}:
		default:
		}
	}
	if p.blockCh != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-p.blockCh:
		}
	}
	if p.fn != nil {
		return p.fn(ctx, rec, key)
	}
	return nil
}

func (p *fakeProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

func (p *fakeProvider) callKeys() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := append([]string(nil), p.calls...)
	return out
}

func sampleRec(workID, sourceKey, provider string, kind sdk.WorkKind) terminalwork.WorkRecord {
	return terminalwork.WorkRecord{
		WorkID:         workID,
		SourceKey:      terminalwork.SourceKey{IdentityVersion: 1, Key: sourceKey},
		PayloadVersion: 1,
		Kind:           kind,
		State:          sdk.WorkStateIntent,
		ProviderID:     provider,
		Lifecycle:      terminalwork.LifecycleCorrelation{RequestID: "req-1", AttemptID: "att-1", TraceID: "tr-1"},
		Versions:       terminalwork.BoundVersions{GenerationID: "g1", ProviderID: provider, RatingID: "r1"},
		Payload:        []byte(`{"ok":true}`),
	}
}

func openStore(t *testing.T, clock *manualClock) *workstore.MemoryStore {
	t.Helper()
	s, err := workstore.NewMemoryStore(workstore.MemoryConfig{
		StoreID: "proc-test",
		Now:     clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func seedPending(t *testing.T, store *workstore.MemoryStore, clock *manualClock, rec terminalwork.WorkRecord) {
	t.Helper()
	ctx := context.Background()
	if err := store.AppendIntent(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if err := store.PromotePending(ctx, terminalwork.PromotePendingCommand{WorkID: rec.WorkID, Now: clock.Now()}); err != nil {
		t.Fatal(err)
	}
}

func newProc(t *testing.T, store app.WorkStore, reg *app.Registry, clock *manualClock, cfg app.Config) *app.Processor {
	t.Helper()
	cfg.OwnerID = "worker-1"
	cfg.ClaimTTL = time.Minute
	cfg.Clock = clock
	if cfg.ClaimLimit == 0 {
		cfg.ClaimLimit = 8
	}
	if cfg.GlobalMax == 0 {
		cfg.GlobalMax = 8
	}
	if cfg.PerProviderMax == 0 {
		cfg.PerProviderMax = 8
	}
	cfg.RetrySchedule = terminalwork.RetrySchedule{Initial: time.Second, Multiplier: 2, Max: 8 * time.Second}
	p, err := app.NewProcessor(store, reg, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRegistry_RejectsDuplicateNilAndCapability(t *testing.T) {
	t.Parallel()
	reg := app.NewRegistry()
	p := &fakeProvider{id: "prov-a", kinds: []sdk.WorkKind{sdk.WorkKindSettleRequestProvider}, ver: "1"}
	if err := reg.Register(p); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(p); !errors.Is(err, app.ErrDuplicateProvider) {
		t.Fatalf("duplicate got %v", err)
	}
	if err := reg.Register(nil); !errors.Is(err, app.ErrNilProvider) {
		t.Fatalf("nil got %v", err)
	}
	var typedNil *fakeProvider
	if err := reg.Register(typedNil); !errors.Is(err, app.ErrNilProvider) {
		t.Fatalf("typed nil got %v", err)
	}
	bad := &fakeProvider{id: "prov-b", kinds: []sdk.WorkKind{sdk.WorkKind("nope")}, ver: "1"}
	if err := reg.Register(bad); !errors.Is(err, app.ErrUnsupportedKind) {
		t.Fatalf("bad kind got %v", err)
	}
	if _, err := reg.Resolve("missing", sdk.WorkKindSettleRequestProvider); !errors.Is(err, app.ErrMissingProvider) {
		t.Fatalf("missing got %v", err)
	}
	if _, err := reg.Resolve("prov-a", sdk.WorkKindCompensateProvider); !errors.Is(err, app.ErrUnsupportedKind) {
		t.Fatalf("capability got %v", err)
	}
	got, err := reg.Resolve("prov-a", sdk.WorkKindSettleRequestProvider)
	if err != nil || got.ProviderID() != "prov-a" {
		t.Fatalf("resolve got=%v err=%v", got, err)
	}
	noVer := &fakeProvider{id: "prov-c", kinds: []sdk.WorkKind{sdk.WorkKindSettleRequestProvider}, ver: ""}
	if err := reg.Register(noVer); !errors.Is(err, app.ErrMalformedProvider) {
		t.Fatalf("empty version got %v", err)
	}
}

func TestProcessor_TimeoutRetries(t *testing.T) {
	t.Parallel()
	clock := &manualClock{t: time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)}
	store := openStore(t, clock)
	prov := &fakeProvider{
		id: "prov-a", kinds: []sdk.WorkKind{sdk.WorkKindSettleRequestProvider}, ver: "1",
		fn: func(context.Context, terminalwork.WorkRecord, string) error { return app.ErrProviderTimeout },
	}
	reg := app.NewRegistry()
	_ = reg.Register(prov)
	seedPending(t, store, clock, sampleRec("w-to", "sk-to", "prov-a", sdk.WorkKindSettleRequestProvider))
	p := newProc(t, store, reg, clock, app.Config{})
	if err := p.ProcessDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetByWorkID(context.Background(), "w-to")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != sdk.WorkStateRetry || got.Error.Code != "timeout" || got.Error.Permanent {
		t.Fatalf("got %+v", got)
	}
	if prov.callCount() != 1 {
		t.Fatalf("calls=%d", prov.callCount())
	}
}

func TestProcessor_PanicRecoversToRetry(t *testing.T) {
	t.Parallel()
	clock := &manualClock{t: time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)}
	store := openStore(t, clock)
	prov := &fakeProvider{
		id: "prov-a", kinds: []sdk.WorkKind{sdk.WorkKindSettleRequestProvider}, ver: "1",
		fn: func(context.Context, terminalwork.WorkRecord, string) error { panic("boom") },
	}
	reg := app.NewRegistry()
	_ = reg.Register(prov)
	seedPending(t, store, clock, sampleRec("w-panic", "sk-panic", "prov-a", sdk.WorkKindSettleRequestProvider))
	p := newProc(t, store, reg, clock, app.Config{})
	if err := p.ProcessDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetByWorkID(context.Background(), "w-panic")
	if got.State != sdk.WorkStateRetry || got.Error.Code != "outage" {
		t.Fatalf("got %+v", got)
	}
}

func TestProcessor_OutageRetries(t *testing.T) {
	t.Parallel()
	clock := &manualClock{t: time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)}
	store := openStore(t, clock)
	prov := &fakeProvider{
		id: "prov-a", kinds: []sdk.WorkKind{sdk.WorkKindSettleRequestProvider}, ver: "1",
		fn: func(context.Context, terminalwork.WorkRecord, string) error { return app.ErrProviderOutage },
	}
	reg := app.NewRegistry()
	_ = reg.Register(prov)
	seedPending(t, store, clock, sampleRec("w-out", "sk-out", "prov-a", sdk.WorkKindSettleRequestProvider))
	p := newProc(t, store, reg, clock, app.Config{})
	_ = p.ProcessDue(context.Background())
	got, _ := store.GetByWorkID(context.Background(), "w-out")
	if got.State != sdk.WorkStateRetry || got.Error.Code != "outage" {
		t.Fatalf("got %+v", got)
	}
}

func TestProcessor_AmbiguousCommitIdempotent(t *testing.T) {
	t.Parallel()
	clock := &manualClock{t: time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)}
	store := openStore(t, clock)
	var attempts atomic.Int32
	seen := map[string]int{}
	var mu sync.Mutex
	prov := &fakeProvider{
		id: "prov-a", kinds: []sdk.WorkKind{sdk.WorkKindSettleRequestProvider}, ver: "1",
		fn: func(_ context.Context, _ terminalwork.WorkRecord, key string) error {
			mu.Lock()
			seen[key]++
			n := seen[key]
			mu.Unlock()
			attempts.Add(1)
			if n == 1 {
				return app.ErrAmbiguousCommit
			}
			return nil // idempotent replay
		},
	}
	reg := app.NewRegistry()
	_ = reg.Register(prov)
	seedPending(t, store, clock, sampleRec("w-amb", "sk-amb", "prov-a", sdk.WorkKindSettleRequestProvider))
	p := newProc(t, store, reg, clock, app.Config{})
	_ = p.ProcessDue(context.Background())
	got, _ := store.GetByWorkID(context.Background(), "w-amb")
	if got.State != sdk.WorkStateRetry {
		t.Fatalf("first pass state=%s", got.State)
	}
	clock.set(got.NextRetryAt)
	_ = p.ProcessDue(context.Background())
	got, _ = store.GetByWorkID(context.Background(), "w-amb")
	if got.State != sdk.WorkStateCompleted {
		t.Fatalf("second pass state=%s", got.State)
	}
	keys := prov.callKeys()
	if len(keys) != 2 || keys[0] != keys[1] || keys[0] != "v1:sk-amb" {
		t.Fatalf("keys=%v", keys)
	}
}

func TestProcessor_PartialProvidersNotRepeated(t *testing.T) {
	t.Parallel()
	clock := &manualClock{t: time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)}
	store := openStore(t, clock)
	ok := &fakeProvider{id: "prov-ok", kinds: []sdk.WorkKind{sdk.WorkKindSettleRequestProvider}, ver: "1"}
	bad := &fakeProvider{
		id: "prov-bad", kinds: []sdk.WorkKind{sdk.WorkKindSettleRequestProvider}, ver: "1",
		fn: func(context.Context, terminalwork.WorkRecord, string) error { return app.ErrProviderOutage },
	}
	reg := app.NewRegistry()
	_ = reg.Register(ok)
	_ = reg.Register(bad)
	seedPending(t, store, clock, sampleRec("w-ok", "sk-ok", "prov-ok", sdk.WorkKindSettleRequestProvider))
	seedPending(t, store, clock, sampleRec("w-bad", "sk-bad", "prov-bad", sdk.WorkKindSettleRequestProvider))
	p := newProc(t, store, reg, clock, app.Config{ClaimLimit: 2})
	_ = p.ProcessDue(context.Background())
	if ok.callCount() != 1 {
		t.Fatalf("ok calls=%d", ok.callCount())
	}
	badRec, _ := store.GetByWorkID(context.Background(), "w-bad")
	clock.set(badRec.NextRetryAt)
	_ = p.ProcessDue(context.Background())
	if ok.callCount() != 1 {
		t.Fatalf("completed provider repeated: calls=%d", ok.callCount())
	}
	if bad.callCount() != 2 {
		t.Fatalf("bad calls=%d", bad.callCount())
	}
}

func TestProcessor_RestartDoesNotRepeatCompleted(t *testing.T) {
	t.Parallel()
	clock := &manualClock{t: time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)}
	state := workstore.NewMemoryState()
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "restart", State: state, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	prov := &fakeProvider{id: "prov-a", kinds: []sdk.WorkKind{sdk.WorkKindSettleRequestProvider}, ver: "1"}
	reg := app.NewRegistry()
	_ = reg.Register(prov)
	seedPending(t, store, clock, sampleRec("w-done", "sk-done", "prov-a", sdk.WorkKindSettleRequestProvider))
	p1 := newProc(t, store, reg, clock, app.Config{})
	_ = p1.ProcessDue(context.Background())
	if prov.callCount() != 1 {
		t.Fatalf("calls=%d", prov.callCount())
	}
	// Simulate process restart with shared durable state.
	store2, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "restart", State: state, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	p2 := newProc(t, store2, reg, clock, app.Config{})
	_ = p2.ProcessDue(context.Background())
	if prov.callCount() != 1 {
		t.Fatalf("restart repeated completed work: calls=%d", prov.callCount())
	}
	got, _ := store2.GetByWorkID(context.Background(), "w-done")
	if got.State != sdk.WorkStateCompleted {
		t.Fatalf("state=%s", got.State)
	}
}

func TestProcessor_MissingProviderRetriesThenCompletes(t *testing.T) {
	t.Parallel()
	clock := &manualClock{t: time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)}
	store := openStore(t, clock)
	reg := app.NewRegistry()
	seedPending(t, store, clock, sampleRec("w-miss", "sk-miss", "ghost", sdk.WorkKindSettleRequestProvider))
	p := newProc(t, store, reg, clock, app.Config{})
	_ = p.ProcessDue(context.Background())
	got, _ := store.GetByWorkID(context.Background(), "w-miss")
	if got.State != sdk.WorkStateRetry || got.Error.Code != "missing_provider" || got.Error.Permanent {
		t.Fatalf("missing provider must retry, got %+v", got)
	}
	ids := p.UnresolvedProviderIDs()
	if len(ids) != 1 || ids[0] != "ghost" {
		t.Fatalf("unresolved=%v", ids)
	}
	prov := &fakeProvider{id: "ghost", kinds: []sdk.WorkKind{sdk.WorkKindSettleRequestProvider}, ver: "1"}
	if err := reg.Register(prov); err != nil {
		t.Fatal(err)
	}
	clock.set(got.NextRetryAt)
	_ = p.ProcessDue(context.Background())
	got, _ = store.GetByWorkID(context.Background(), "w-miss")
	if got.State != sdk.WorkStateCompleted {
		t.Fatalf("after register state=%s", got.State)
	}
	if len(p.UnresolvedProviderIDs()) != 0 {
		t.Fatalf("unresolved after resolve=%v", p.UnresolvedProviderIDs())
	}
}

func TestProcessor_InvalidPayloadQuarantines(t *testing.T) {
	t.Parallel()
	clock := &manualClock{t: time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)}
	store := openStore(t, clock)
	prov := &fakeProvider{
		id: "prov-a", kinds: []sdk.WorkKind{sdk.WorkKindSettleRequestProvider}, ver: "1",
		fn: func(context.Context, terminalwork.WorkRecord, string) error { return app.ErrInvalidPayload },
	}
	reg := app.NewRegistry()
	_ = reg.Register(prov)
	seedPending(t, store, clock, sampleRec("w-badp", "sk-badp", "prov-a", sdk.WorkKindSettleRequestProvider))
	p := newProc(t, store, reg, clock, app.Config{})
	_ = p.ProcessDue(context.Background())
	got, _ := store.GetByWorkID(context.Background(), "w-badp")
	if got.State != sdk.WorkStateQuarantined || got.Error.Code != "invalid_payload" {
		t.Fatalf("got %+v", got)
	}
}

func TestProcessor_ShutdownCancelsInFlight(t *testing.T) {
	t.Parallel()
	clock := &manualClock{t: time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)}
	store := openStore(t, clock)
	entered := make(chan struct{}, 1)
	block := make(chan struct{})
	prov := &fakeProvider{
		id: "prov-a", kinds: []sdk.WorkKind{sdk.WorkKindSettleRequestProvider}, ver: "1",
		enteredCh: entered,
		blockCh:   block,
	}
	reg := app.NewRegistry()
	_ = reg.Register(prov)
	seedPending(t, store, clock, sampleRec("w-shut", "sk-shut", "prov-a", sdk.WorkKindSettleRequestProvider))
	tick := make(chan struct{})
	p := newProc(t, store, reg, clock, app.Config{TickC: tick, ClaimLimit: 1})
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	tick <- struct{}{}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("invoke did not start")
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := p.Shutdown(shutCtx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	close(block) // unblock if still waiting
	got, _ := store.GetByWorkID(context.Background(), "w-shut")
	if got.State != sdk.WorkStateRetry {
		t.Fatalf("state after shutdown=%s want retry", got.State)
	}
}

func TestProcessor_RenewPulseExtendsClaim(t *testing.T) {
	t.Parallel()
	clock := &manualClock{t: time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)}
	store := openStore(t, clock)
	entered := make(chan struct{}, 1)
	block := make(chan struct{})
	prov := &fakeProvider{
		id: "prov-a", kinds: []sdk.WorkKind{sdk.WorkKindSettleRequestProvider}, ver: "1",
		enteredCh: entered,
		blockCh:   block,
	}
	reg := app.NewRegistry()
	_ = reg.Register(prov)
	seedPending(t, store, clock, sampleRec("w-renew", "sk-renew", "prov-a", sdk.WorkKindSettleRequestProvider))
	pulse := make(chan struct{})
	p := newProc(t, store, reg, clock, app.Config{RenewPulse: pulse, ClaimTTL: time.Minute, ClaimLimit: 1})

	done := make(chan error, 1)
	go func() { done <- p.ProcessDue(context.Background()) }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("invoke did not start")
	}
	before, _ := store.GetByWorkID(context.Background(), "w-renew")
	clock.set(clock.Now().Add(30 * time.Second))
	pulse <- struct{}{}
	// Allow renew goroutine to apply.
	deadline := time.Now().Add(2 * time.Second)
	for {
		after, _ := store.GetByWorkID(context.Background(), "w-renew")
		if after.Lease.ExpiresAt.After(before.Lease.ExpiresAt) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("lease not renewed: before=%v after=%v", before.Lease.ExpiresAt, after.Lease.ExpiresAt)
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(block)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetByWorkID(context.Background(), "w-renew")
	if got.State != sdk.WorkStateCompleted {
		t.Fatalf("state=%s", got.State)
	}
}

func TestProcessor_GlobalAndPerProviderMax(t *testing.T) {
	t.Parallel()
	clock := &manualClock{t: time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)}
	store := openStore(t, clock)

	var globalInflight atomic.Int32
	var globalMax atomic.Int32
	gate := make(chan struct{})
	makeProv := func(id string) *fakeProvider {
		return &fakeProvider{
			id: id, kinds: []sdk.WorkKind{sdk.WorkKindSettleRequestProvider}, ver: "1",
			fn: func(ctx context.Context, _ terminalwork.WorkRecord, _ string) error {
				n := globalInflight.Add(1)
				defer globalInflight.Add(-1)
				for {
					cur := globalMax.Load()
					if n <= cur || globalMax.CompareAndSwap(cur, n) {
						break
					}
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-gate:
					return nil
				}
			},
		}
	}
	provA := makeProv("prov-a")
	provB := makeProv("prov-b")
	reg := app.NewRegistry()
	_ = reg.Register(provA)
	_ = reg.Register(provB)
	seedPending(t, store, clock, sampleRec("w1", "sk1", "prov-a", sdk.WorkKindSettleRequestProvider))
	seedPending(t, store, clock, sampleRec("w2", "sk2", "prov-a", sdk.WorkKindSettleRequestProvider))
	seedPending(t, store, clock, sampleRec("w3", "sk3", "prov-b", sdk.WorkKindSettleRequestProvider))

	p := newProc(t, store, reg, clock, app.Config{ClaimLimit: 3, GlobalMax: 1, PerProviderMax: 1})
	done := make(chan error, 1)
	go func() { done <- p.ProcessDue(context.Background()) }()

	deadline := time.Now().Add(2 * time.Second)
	for globalMax.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(30 * time.Millisecond)
	if globalMax.Load() != 1 {
		t.Fatalf("global max observed=%d want 1", globalMax.Load())
	}
	if provA.maxIn.Load() > 1 {
		t.Fatalf("per-provider max breached: %d", provA.maxIn.Load())
	}
	close(gate)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ProcessDue hung")
	}
}

type claimGateStore struct {
	app.WorkStore
	enter   chan struct{}
	release chan struct{}
	active  atomic.Int32
	max     atomic.Int32
}

func (s *claimGateStore) ClaimDue(ctx context.Context, cmd terminalwork.ClaimDueCommand) ([]terminalwork.WorkRecord, error) {
	n := s.active.Add(1)
	defer s.active.Add(-1)
	for {
		cur := s.max.Load()
		if n <= cur || s.max.CompareAndSwap(cur, n) {
			break
		}
	}
	if s.enter != nil {
		select {
		case s.enter <- struct{}{}:
		default:
		}
	}
	if s.release != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-s.release:
		}
	}
	return s.WorkStore.ClaimDue(ctx, cmd)
}

func TestProcessor_ProcessDueSerializesOverlap(t *testing.T) {
	t.Parallel()
	clock := &manualClock{t: time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)}
	base := openStore(t, clock)
	enter := make(chan struct{}, 2)
	release := make(chan struct{})
	store := &claimGateStore{WorkStore: base, enter: enter, release: release}
	prov := &fakeProvider{id: "prov-a", kinds: []sdk.WorkKind{sdk.WorkKindSettleRequestProvider}, ver: "1"}
	reg := app.NewRegistry()
	_ = reg.Register(prov)
	seedPending(t, base, clock, sampleRec("w-a", "sk-a", "prov-a", sdk.WorkKindSettleRequestProvider))
	seedPending(t, base, clock, sampleRec("w-b", "sk-b", "prov-a", sdk.WorkKindSettleRequestProvider))
	p := newProc(t, store, reg, clock, app.Config{ClaimLimit: 1})

	errCh := make(chan error, 2)
	go func() { errCh <- p.ProcessDue(context.Background()) }()
	go func() { errCh <- p.ProcessDue(context.Background()) }()
	select {
	case <-enter:
	case <-time.After(2 * time.Second):
		t.Fatal("ClaimDue did not start")
	}
	if store.max.Load() != 1 {
		t.Fatalf("overlapping ClaimDue max=%d", store.max.Load())
	}
	close(release)
	for range 2 {
		select {
		case err := <-errCh:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("ProcessDue hung")
		}
	}
}

type renewFailStore struct {
	app.WorkStore
	failRenew atomic.Bool
	renewed   atomic.Int32
}

func (s *renewFailStore) RenewClaim(ctx context.Context, cmd terminalwork.RenewClaimCommand) error {
	s.renewed.Add(1)
	if s.failRenew.Load() {
		return errors.New("lease lost")
	}
	return s.WorkStore.RenewClaim(ctx, cmd)
}

func TestProcessor_RenewFailureCancelsAndRetries(t *testing.T) {
	t.Parallel()
	clock := &manualClock{t: time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)}
	base := openStore(t, clock)
	store := &renewFailStore{WorkStore: base}
	store.failRenew.Store(true)
	entered := make(chan struct{}, 1)
	var calls atomic.Int32
	prov := &fakeProvider{
		id: "prov-a", kinds: []sdk.WorkKind{sdk.WorkKindSettleRequestProvider}, ver: "1",
		enteredCh: entered,
		fn: func(ctx context.Context, _ terminalwork.WorkRecord, _ string) error {
			calls.Add(1)
			<-ctx.Done()
			return ctx.Err()
		},
	}
	reg := app.NewRegistry()
	_ = reg.Register(prov)
	seedPending(t, base, clock, sampleRec("w-rf", "sk-rf", "prov-a", sdk.WorkKindSettleRequestProvider))
	pulse := make(chan struct{})
	p := newProc(t, store, reg, clock, app.Config{RenewPulse: pulse, ClaimLimit: 1})
	done := make(chan error, 1)
	go func() { done <- p.ProcessDue(context.Background()) }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("invoke did not start")
	}
	pulse <- struct{}{}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ProcessDue hung after renew fail")
	}
	got, _ := base.GetByWorkID(context.Background(), "w-rf")
	if got.State != sdk.WorkStateRetry || got.Error.Code != "claim_renew_failed" {
		t.Fatalf("got %+v", got)
	}
	if calls.Load() != 1 || store.renewed.Load() < 1 {
		t.Fatalf("calls=%d renewed=%d", calls.Load(), store.renewed.Load())
	}
}

func TestProcessor_ShutdownTimeoutThenRestart(t *testing.T) {
	t.Parallel()
	clock := &manualClock{t: time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)}
	store := openStore(t, clock)
	entered := make(chan struct{}, 1)
	block := make(chan struct{})
	prov := &fakeProvider{
		id: "prov-a", kinds: []sdk.WorkKind{sdk.WorkKindSettleRequestProvider}, ver: "1",
		enteredCh: entered,
		blockCh:   block,
	}
	reg := app.NewRegistry()
	_ = reg.Register(prov)
	seedPending(t, store, clock, sampleRec("w-to", "sk-to", "prov-a", sdk.WorkKindSettleRequestProvider))
	tick := make(chan struct{})
	p := newProc(t, store, reg, clock, app.Config{TickC: tick, ClaimLimit: 1})
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	tick <- struct{}{}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("invoke did not start")
	}
	expired, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.Shutdown(expired); !errors.Is(err, context.Canceled) {
		t.Fatalf("timeout shutdown got %v", err)
	}
	if !p.Running() {
		t.Fatal("expected still running until invoke exits")
	}
	close(block)
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer waitCancel()
	if err := p.Shutdown(waitCtx); err != nil {
		t.Fatalf("drain shutdown: %v", err)
	}
	if p.Running() {
		t.Fatal("expected stopped")
	}
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatalf("idempotent shutdown: %v", err)
	}
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if !p.Running() {
		t.Fatal("expected running after restart")
	}
	_ = p.Shutdown(context.Background())
}

func TestProcessor_OwnedTickIntervalContinuous(t *testing.T) {
	t.Parallel()
	clock := &manualClock{t: time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)}
	store := openStore(t, clock)
	processed := make(chan struct{}, 1)
	prov := &fakeProvider{
		id: "prov-a", kinds: []sdk.WorkKind{sdk.WorkKindSettleRequestProvider}, ver: "1",
		fn: func(context.Context, terminalwork.WorkRecord, string) error {
			select {
			case processed <- struct{}{}:
			default:
			}
			return nil
		},
	}
	reg := app.NewRegistry()
	_ = reg.Register(prov)
	seedPending(t, store, clock, sampleRec("w-tick", "sk-tick", "prov-a", sdk.WorkKindSettleRequestProvider))
	pulse := make(chan time.Time, 2)
	stopped := make(chan struct{})
	p := newProc(t, store, reg, clock, app.Config{
		TickInterval: time.Second,
		NewTicker: func(time.Duration) app.Ticker {
			return &manualTicker{ch: pulse, stopped: stopped}
		},
	})
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !p.Readiness().Running {
		t.Fatal("readiness running")
	}
	pulse <- clock.Now()
	select {
	case <-processed:
	case <-time.After(2 * time.Second):
		t.Fatal("tick did not process work")
	}
	deadline := time.Now().Add(2 * time.Second)
	var got terminalwork.WorkRecord
	for {
		var err error
		got, err = store.GetByWorkID(context.Background(), "w-tick")
		if err == nil && got.State == sdk.WorkStateCompleted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("state=%s err=%v", got.State, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("ticker not stopped")
	}
}

type manualTicker struct {
	ch      chan time.Time
	stopped chan struct{}
	once    sync.Once
}

func (m *manualTicker) C() <-chan time.Time { return m.ch }
func (m *manualTicker) Stop() {
	m.once.Do(func() { close(m.stopped) })
}

func TestProcessor_LifecycleNoGoroutineLeak(t *testing.T) {
	t.Parallel()
	clock := &manualClock{t: time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)}
	store := openStore(t, clock)
	prov := &fakeProvider{
		id: "prov-a", kinds: []sdk.WorkKind{sdk.WorkKindSettleRequestProvider}, ver: "1",
	}
	reg := app.NewRegistry()
	_ = reg.Register(prov)
	seedPending(t, store, clock, sampleRec("w-life", "sk-life", "prov-a", sdk.WorkKindSettleRequestProvider))

	tick := make(chan struct{})
	p := newProc(t, store, reg, clock, app.Config{TickC: tick, ClaimLimit: 1})
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !p.Running() {
		t.Fatal("expected running after Start")
	}
	tick <- struct{}{}

	deadline := time.Now().Add(2 * time.Second)
	for {
		got, err := store.GetByWorkID(context.Background(), "w-life")
		if err == nil && got.State == sdk.WorkStateCompleted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("work not completed: err=%v state=%v", err, got.State)
		}
		time.Sleep(10 * time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := p.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if p.Running() {
		t.Fatal("expected stopped after Shutdown")
	}
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatalf("idempotent shutdown: %v", err)
	}
}
