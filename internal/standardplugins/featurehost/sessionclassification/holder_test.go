package sessionclassification

import (
	"context"
	"database/sql"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/db"
	featurestate "github.com/aiproxer/aiproxer/internal/plugins/features/sessionclassification"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/uptrace/bun"
	_ "modernc.org/sqlite"
)

type schemaTestStore struct {
	featurestate.Store
	ensure func(context.Context) error
}

type observedContext struct {
	context.Context
	called chan struct{}
	once   sync.Once
}

type observedErrContext struct {
	context.Context
	calls    atomic.Int32
	signalAt int32
	checked  chan struct{}
	once     sync.Once
}

func (c *observedContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.called) })
	return c.Context.Done()
}

func (c *observedErrContext) Err() error {
	if c.calls.Add(1) == c.signalAt {
		c.once.Do(func() { close(c.checked) })
	}
	return c.Context.Err()
}

func (s *schemaTestStore) EnsureSchema(ctx context.Context) error {
	return s.ensure(ctx)
}

func TestStateHolder_LazilySelectsMemoryStoreAndSharesItAcrossOwners(t *testing.T) {
	t.Parallel()

	collector := NewPrometheusCollector()
	holder := NewStateHolder(nil, collector)
	if holder.store != nil || holder.Coordinator() != nil {
		t.Fatal("new holder performed store construction before an enabled generation")
	}

	ctx := context.Background()
	if err := holder.Acquire(ctx); err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	memory, ok := holder.store.(*MemoryStore)
	if !ok || memory == nil {
		t.Fatalf("selected store = %T, want *MemoryStore", holder.store)
	}
	coordinator := holder.Coordinator()
	if coordinator == nil {
		t.Fatal("successful Acquire did not publish the process coordinator")
	}
	if holder.owners != 1 {
		t.Fatalf("generation owner count = %d, want 1", holder.owners)
	}
	if err := holder.Acquire(ctx); err != nil {
		t.Fatalf("overlapping Acquire: %v", err)
	}
	if holder.Coordinator() != coordinator || holder.store != memory {
		t.Fatal("overlapping generation did not share the store and coordinator")
	}
	if holder.owners != 2 {
		t.Fatalf("generation owner count = %d, want 2", holder.owners)
	}

	holder.Release()
	if holder.Coordinator() != coordinator || holder.owners != 1 {
		t.Fatal("releasing one generation destroyed or replaced shared process state")
	}
	holder.Release()
	if holder.Coordinator() != coordinator || holder.owners != 0 {
		t.Fatal("releasing the final generation destroyed shared state before process Close")
	}

	if err := holder.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := holder.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if !holder.closed || holder.store != nil || holder.coordinator != nil || holder.Coordinator() != nil {
		t.Fatal("Close did not dispose the holder's store and coordinator references")
	}
	if err := holder.Acquire(ctx); !errors.Is(err, ErrStateHolderClosed) {
		t.Fatalf("Acquire after Close = %v, want ErrStateHolderClosed", err)
	}
}

func TestStateHolder_BunSelectionEnsuresSchemaOnlyOnAcquireAndKeepsDBBorrowed(t *testing.T) {
	t.Parallel()

	_, bunDB := openHolderSQLiteBunDB(t)
	holder := NewStateHolder(bunDB, nil)
	if holder.store != nil || holder.Coordinator() != nil {
		t.Fatal("new Bun holder initialized classification state eagerly")
	}
	if got := classificationTableCount(t, bunDB); got != 0 {
		t.Fatalf("classification table count before Acquire = %d, want 0", got)
	}

	if err := holder.Acquire(context.Background()); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if _, ok := holder.store.(*BunStore); !ok {
		t.Fatalf("selected store = %T, want *BunStore", holder.store)
	}
	if got := classificationTableCount(t, bunDB); got != 1 {
		t.Fatalf("classification table count after Acquire = %d, want 1", got)
	}
	if err := holder.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	var one int
	if err := bunDB.NewRaw("SELECT 1").Scan(context.Background(), &one); err != nil || one != 1 {
		t.Fatalf("post-holder-close query = (%d, %v), want (1, nil); the Bun DB is borrowed", one, err)
	}
}

func TestStateHolder_FailedInitializationCanRetryAndCountsSuccessfulWorkOnce(t *testing.T) {
	t.Parallel()

	failure := errors.New("temporary schema failure")
	baseStore, err := NewMemoryStore(MemoryStoreConfig{})
	if err != nil {
		t.Fatal(err)
	}
	var bunConstructs atomic.Int32
	var schemaCalls atomic.Int32
	var coordinatorConstructs atomic.Int32
	factories := defaultHolderFactories()
	factories.newBunStore = func(*bun.DB) (schemaStore, error) {
		bunConstructs.Add(1)
		return &schemaTestStore{Store: baseStore, ensure: func(context.Context) error {
			if schemaCalls.Add(1) == 1 {
				return failure
			}
			return nil
		}}, nil
	}
	factories.newCoordinator = func(store featurestate.Store, _ featurestate.Observer) (*Coordinator, error) {
		coordinatorConstructs.Add(1)
		return NewCoordinator(store, CoordinatorConfig{})
	}
	holder := newStateHolder(&bun.DB{}, nil, factories)
	ctx := context.Background()

	if err := holder.Acquire(ctx); !errors.Is(err, failure) {
		t.Fatalf("first Acquire = %v, want schema failure", err)
	}
	if holder.store != nil || holder.Coordinator() != nil || holder.owners != 0 {
		t.Fatal("failed initialization published partial holder state")
	}
	if err := holder.Acquire(ctx); err != nil {
		t.Fatalf("retry Acquire: %v", err)
	}
	coordinator := holder.Coordinator()
	if coordinator == nil || holder.owners != 1 {
		t.Fatal("successful retry did not publish exactly one generation owner")
	}
	if err := holder.Acquire(ctx); err != nil {
		t.Fatalf("overlapping Acquire after success: %v", err)
	}
	if holder.Coordinator() != coordinator || holder.owners != 2 {
		t.Fatal("later generation did not share successful process state")
	}
	if got := bunConstructs.Load(); got != 2 {
		t.Fatalf("Bun store constructions = %d, want 2 (one failed attempt, one successful attempt)", got)
	}
	if got := schemaCalls.Load(); got != 2 {
		t.Fatalf("schema calls = %d, want 2 (retry after one failure)", got)
	}
	if got := coordinatorConstructs.Load(); got != 1 {
		t.Fatalf("coordinator constructions after successful initialization = %d, want 1", got)
	}
	holder.Release()
	holder.Release()
	if err := holder.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStateHolder_ConcurrentAcquireCoalescesInitialization(t *testing.T) {
	t.Parallel()

	baseStore, err := NewMemoryStore(MemoryStoreConfig{})
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var ensureCalls atomic.Int32
	factories := defaultHolderFactories()
	factories.newBunStore = func(*bun.DB) (schemaStore, error) {
		return &schemaTestStore{Store: baseStore, ensure: func(ctx context.Context) error {
			if ensureCalls.Add(1) == 1 {
				close(entered)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-release:
				return nil
			}
		}}, nil
	}
	var coordinatorCalls atomic.Int32
	factories.newCoordinator = func(store featurestate.Store, _ featurestate.Observer) (*Coordinator, error) {
		coordinatorCalls.Add(1)
		return NewCoordinator(store, CoordinatorConfig{})
	}
	holder := newStateHolder(&bun.DB{}, nil, factories)

	waiterCtx := &observedContext{Context: context.Background(), called: make(chan struct{})}

	first := make(chan error, 1)
	second := make(chan error, 1)
	go func() { first <- holder.Acquire(context.Background()) }()
	<-entered
	go func() { second <- holder.Acquire(waiterCtx) }()
	<-waiterCtx.called
	close(release)
	if err := <-first; err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	if err := <-second; err != nil {
		t.Fatalf("second Acquire: %v", err)
	}
	if got := ensureCalls.Load(); got != 1 {
		t.Fatalf("schema initializer calls = %d, want 1", got)
	}
	if got := coordinatorCalls.Load(); got != 1 {
		t.Fatalf("coordinator constructions = %d, want 1", got)
	}
	if holder.owners != 2 || holder.Coordinator() == nil {
		t.Fatalf("coalesced holder state = owners %d, coordinator %p; want two owners and one coordinator", holder.owners, holder.Coordinator())
	}
	holder.Release()
	holder.Release()
	if err := holder.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStateHolder_WaiterCancellationDoesNotCancelSharedInitialization(t *testing.T) {
	t.Parallel()

	baseStore, err := NewMemoryStore(MemoryStoreConfig{})
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var ensureCalls atomic.Int32
	factories := defaultHolderFactories()
	factories.newBunStore = func(*bun.DB) (schemaStore, error) {
		return &schemaTestStore{Store: baseStore, ensure: func(ctx context.Context) error {
			if ensureCalls.Add(1) == 1 {
				close(entered)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-release:
				return nil
			}
		}}, nil
	}
	holder := newStateHolder(&bun.DB{}, nil, factories)

	ownerResult := make(chan error, 1)
	go func() { ownerResult <- holder.Acquire(context.Background()) }()
	<-entered

	waiterBase, cancelWaiter := context.WithCancel(context.Background())
	defer cancelWaiter()
	waiterCtx := &observedContext{Context: waiterBase, called: make(chan struct{})}
	waiterResult := make(chan error, 1)
	go func() { waiterResult <- holder.Acquire(waiterCtx) }()
	<-waiterCtx.called
	cancelWaiter()
	if err := <-waiterResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("waiter Acquire = %v, want context cancellation", err)
	}
	if got := ensureCalls.Load(); got != 1 {
		t.Fatalf("shared initialization calls after waiter cancellation = %d, want 1", got)
	}

	close(release)
	if err := <-ownerResult; err != nil {
		t.Fatalf("owner Acquire: %v", err)
	}
	if holder.Coordinator() == nil || holder.owners != 1 {
		t.Fatal("waiter cancellation canceled or corrupted the owner initialization")
	}
	holder.Release()
	if err := holder.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStateHolder_CanceledInitializerCannotPublish(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	collector := NewPrometheusCollector()
	registry := prometheus.NewRegistry()
	if err := registry.Register(collector); err != nil {
		t.Fatalf("Register collector: %v", err)
	}
	factories := defaultHolderFactories()
	var coordinatorCalls atomic.Int32
	factories.newCoordinator = func(store featurestate.Store, _ featurestate.Observer) (*Coordinator, error) {
		coordinator, err := NewCoordinator(store, CoordinatorConfig{})
		if coordinatorCalls.Add(1) == 1 {
			cancel()
		}
		return coordinator, err
	}
	holder := newStateHolder(nil, collector, factories)
	t.Cleanup(func() { _ = holder.Close() })

	if err := holder.Acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled initializer Acquire = %v, want context.Canceled", err)
	}
	if holder.store != nil || holder.Coordinator() != nil || holder.owners != 0 {
		t.Fatalf("canceled initializer published state: store=%T coordinator=%p owners=%d; want nil, nil, 0", holder.store, holder.Coordinator(), holder.owners)
	}
	if got := storeReadyMetric(t, registry); got != 0 {
		t.Fatalf("readiness after canceled initialization = %v, want 0", got)
	}

	if err := holder.Acquire(context.Background()); err != nil {
		t.Fatalf("Acquire after canceled initialization: %v", err)
	}
	if holder.Coordinator() == nil || holder.owners != 1 || coordinatorCalls.Load() != 2 {
		t.Fatalf("retry state = coordinator %p, owners %d, coordinator constructions %d; want published coordinator, one owner, two constructions", holder.Coordinator(), holder.owners, coordinatorCalls.Load())
	}
	if got := storeReadyMetric(t, registry); got != 1 {
		t.Fatalf("readiness after retry = %v, want 1", got)
	}
}

func TestStateHolder_LiveWaiterRetriesCanceledOwner(t *testing.T) {
	t.Parallel()

	baseStore, err := NewMemoryStore(MemoryStoreConfig{})
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	var ensureCalls atomic.Int32
	factories := defaultHolderFactories()
	factories.newBunStore = func(*bun.DB) (schemaStore, error) {
		return &schemaTestStore{Store: baseStore, ensure: func(ctx context.Context) error {
			if ensureCalls.Add(1) == 1 {
				close(entered)
				<-ctx.Done()
				return ctx.Err()
			}
			return nil
		}}, nil
	}
	holder := newStateHolder(&bun.DB{}, nil, factories)
	t.Cleanup(func() { _ = holder.Close() })

	ownerCtx, cancelOwner := context.WithCancel(context.Background())
	defer cancelOwner()
	ownerResult := make(chan error, 1)
	waiterResult := make(chan error, 1)
	go func() { ownerResult <- holder.Acquire(ownerCtx) }()
	<-entered

	waiterCtx := &observedContext{Context: context.Background(), called: make(chan struct{})}
	go func() { waiterResult <- holder.Acquire(waiterCtx) }()
	<-waiterCtx.called
	cancelOwner()
	if err := <-ownerResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled owner Acquire = %v, want context.Canceled", err)
	}
	if err := <-waiterResult; err != nil {
		t.Fatalf("live waiter inherited canceled initialization: %v; want successful retry", err)
	}
	if got := ensureCalls.Load(); got != 2 {
		t.Fatalf("schema initialization calls = %d, want 2 (canceled owner attempt and waiter retry)", got)
	}
	if holder.Coordinator() == nil || holder.owners != 1 {
		t.Fatalf("waiter retry state = coordinator %p, owners %d; want published coordinator and one owner", holder.Coordinator(), holder.owners)
	}
	holder.Release()
}

func TestStateHolder_LiveWaiterRetriesCanceledOwnerWithBoundedSchemaError(t *testing.T) {
	t.Parallel()

	baseStore, err := NewMemoryStore(MemoryStoreConfig{})
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	var ensureCalls atomic.Int32
	factories := defaultHolderFactories()
	factories.newBunStore = func(*bun.DB) (schemaStore, error) {
		return &schemaTestStore{Store: baseStore, ensure: func(ctx context.Context) error {
			if ensureCalls.Add(1) == 1 {
				close(entered)
				<-ctx.Done()
				return ErrBunSchema
			}
			return nil
		}}, nil
	}
	holder := newStateHolder(&bun.DB{}, nil, factories)
	t.Cleanup(func() { _ = holder.Close() })

	ownerCtx, cancelOwner := context.WithCancel(context.Background())
	defer cancelOwner()
	ownerResult := make(chan error, 1)
	waiterResult := make(chan error, 1)
	go func() { ownerResult <- holder.Acquire(ownerCtx) }()
	<-entered

	waiterCtx := &observedContext{Context: context.Background(), called: make(chan struct{})}
	go func() { waiterResult <- holder.Acquire(waiterCtx) }()
	<-waiterCtx.called
	cancelOwner()
	if err := <-ownerResult; !errors.Is(err, ErrBunSchema) {
		t.Fatalf("canceled owner Acquire = %v, want bounded schema failure", err)
	}
	if err := <-waiterResult; err != nil {
		t.Fatalf("live waiter inherited bounded owner failure: %v; want successful retry", err)
	}
	if got := ensureCalls.Load(); got != 2 {
		t.Fatalf("schema initialization calls = %d, want 2 (canceled owner attempt and waiter retry)", got)
	}
	if holder.Coordinator() == nil || holder.owners != 1 {
		t.Fatalf("waiter retry state = coordinator %p, owners %d; want published coordinator and one owner", holder.Coordinator(), holder.owners)
	}
	holder.Release()
}

func TestStateHolder_LiveWaiterReceivesGenuineInitializationFailure(t *testing.T) {
	t.Parallel()

	failure := errors.New("temporary schema failure")
	baseStore, err := NewMemoryStore(MemoryStoreConfig{})
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var ensureCalls atomic.Int32
	factories := defaultHolderFactories()
	factories.newBunStore = func(*bun.DB) (schemaStore, error) {
		return &schemaTestStore{Store: baseStore, ensure: func(context.Context) error {
			if ensureCalls.Add(1) == 1 {
				close(entered)
				<-release
				return failure
			}
			return nil
		}}, nil
	}
	holder := newStateHolder(&bun.DB{}, nil, factories)
	t.Cleanup(func() { _ = holder.Close() })

	ownerResult := make(chan error, 1)
	waiterResult := make(chan error, 1)
	go func() { ownerResult <- holder.Acquire(context.Background()) }()
	<-entered
	waiterCtx := &observedContext{Context: context.Background(), called: make(chan struct{})}
	go func() { waiterResult <- holder.Acquire(waiterCtx) }()
	<-waiterCtx.called
	close(release)
	if err := <-ownerResult; !errors.Is(err, failure) {
		t.Fatalf("owner Acquire = %v, want genuine initialization failure", err)
	}
	if err := <-waiterResult; !errors.Is(err, failure) {
		t.Fatalf("live waiter Acquire = %v, want the genuine initialization failure", err)
	}
	if got := ensureCalls.Load(); got != 1 {
		t.Fatalf("schema initialization calls = %d, want 1; live waiter must not retry a genuine failure", got)
	}
	if holder.store != nil || holder.Coordinator() != nil || holder.owners != 0 {
		t.Fatal("genuine initialization failure published process state")
	}
}

func TestStateHolder_WarmAcquireChecksCancellationAfterLockContention(t *testing.T) {
	t.Parallel()

	holder := NewStateHolder(nil, nil)
	t.Cleanup(func() { _ = holder.Close() })
	if err := holder.Acquire(context.Background()); err != nil {
		t.Fatalf("initial Acquire: %v", err)
	}

	ctxBase, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &observedErrContext{
		Context:  ctxBase,
		signalAt: 2, // ValidateStoreContext and the pre-lock loop check.
		checked:  make(chan struct{}),
	}
	holder.mu.Lock()
	result := make(chan error, 1)
	go func() { result <- holder.Acquire(ctx) }()
	select {
	case <-ctx.checked:
	case <-time.After(5 * time.Second):
		holder.mu.Unlock()
		t.Fatal("Acquire did not reach the holder-lock wait")
	}
	cancel()
	holder.mu.Unlock()

	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("warm Acquire after lock contention = %v, want context.Canceled", err)
	}
	if holder.owners != 1 {
		t.Fatalf("generation owners after canceled warm Acquire = %d, want unchanged at 1", holder.owners)
	}
	holder.Release()
}

func TestStateHolder_CloseRacingBlockedInitializationPreventsPublication(t *testing.T) {
	t.Parallel()

	baseStore, err := NewMemoryStore(MemoryStoreConfig{})
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	factories := defaultHolderFactories()
	factories.newBunStore = func(*bun.DB) (schemaStore, error) {
		return &schemaTestStore{Store: baseStore, ensure: func(ctx context.Context) error {
			close(entered)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-release:
				return nil
			}
		}}, nil
	}
	holder := newStateHolder(&bun.DB{}, nil, factories)

	ownerResult := make(chan error, 1)
	go func() { ownerResult <- holder.Acquire(context.Background()) }()
	<-entered

	closeStarted := make(chan struct{})
	closeResult := make(chan error, 1)
	go func() {
		close(closeStarted)
		closeResult <- holder.Close()
	}()
	<-closeStarted
	waitForHolderClosing(t, holder)
	select {
	case err := <-closeResult:
		t.Fatalf("Close returned while schema initialization remained blocked: %v", err)
	default:
	}
	if err := holder.Acquire(context.Background()); !errors.Is(err, ErrStateHolderClosed) {
		t.Fatalf("Acquire after Close started = %v, want ErrStateHolderClosed", err)
	}

	close(release)
	if err := <-ownerResult; !errors.Is(err, ErrStateHolderClosed) {
		t.Fatalf("in-flight Acquire after Close started = %v, want ErrStateHolderClosed", err)
	}
	if err := <-closeResult; err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !holder.closed || holder.store != nil || holder.Coordinator() != nil || holder.owners != 0 {
		t.Fatal("Close racing initialization left published state or generation ownership")
	}
}

func TestPrometheusCollector_ReportsOnlyBoundedStoreReadiness(t *testing.T) {
	t.Parallel()

	collector := NewPrometheusCollector()
	registry := prometheus.NewRegistry()
	if err := registry.Register(collector); err != nil {
		t.Fatalf("Register: %v", err)
	}
	holder := NewStateHolder(nil, collector)
	if got := storeReadyMetric(t, registry); got != 0 {
		t.Fatalf("initial store readiness = %v, want 0", got)
	}
	if err := holder.Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := storeReadyMetric(t, registry); got != 1 {
		t.Fatalf("initialized store readiness = %v, want 1", got)
	}
	holder.Release()
	if got := storeReadyMetric(t, registry); got != 1 {
		t.Fatalf("store readiness after generation Stop = %v, want 1 while process state remains", got)
	}
	if err := holder.Close(); err != nil {
		t.Fatal(err)
	}
	if got := storeReadyMetric(t, registry); got != 0 {
		t.Fatalf("store readiness after process Close = %v, want 0", got)
	}
}

func openHolderSQLiteBunDB(t *testing.T) (*sql.DB, *bun.DB) {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bunDB.Close() })
	return sqlDB, bunDB
}

func classificationTableCount(t *testing.T, bunDB *bun.DB) int {
	t.Helper()
	var count int
	if err := bunDB.NewRaw("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'session_classification'").Scan(context.Background(), &count); err != nil {
		t.Fatalf("count classification table: %v", err)
	}
	return count
}

func storeReadyMetric(t *testing.T, registry *prometheus.Registry) float64 {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() == "aip_session_classification_store_ready" {
			if len(family.GetMetric()) != 1 || family.GetMetric()[0].GetGauge() == nil {
				t.Fatalf("store readiness metric family = %+v, want one gauge", family)
			}
			return family.GetMetric()[0].GetGauge().GetValue()
		}
	}
	t.Fatal("store readiness metric was not gathered")
	return 0
}

func waitForHolderClosing(t *testing.T, holder *StateHolder) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		holder.mu.Lock()
		closing := holder.closing
		holder.mu.Unlock()
		if closing {
			return
		}
		select {
		case <-deadline:
			t.Fatal("StateHolder.Close did not begin while initialization was blocked")
		default:
			runtime.Gosched()
		}
	}
}
