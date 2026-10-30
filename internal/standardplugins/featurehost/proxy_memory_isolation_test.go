package featurehost

// Corrective regressions of the independent review (design §9, §12, §14.2,
// §14.3):
//
//  1. a real memory transaction must not block mandatory session preparation:
//     the memory database is isolated from the core continuity database, so a
//     stalled advisory write cannot hold the core connection;
//  2. a failed first-turn decision must not authorize a durable capture;
//  3. the retired A-leg's replay cache is evicted and the published
//     generation's cleanup still runs;
//  4. the isolated memory database is owned by the process: one handle per
//     identity, no handle escapes the process cleanup, and a closed process
//     serves no generation.

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	continuitybunstore "github.com/aiproxer/aiproxer/internal/core/continuity/bunstore"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/featuredb"
	"github.com/aiproxer/aiproxer/internal/plugins/features/proxymemory"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"gopkg.in/yaml.v3"
)

// proxyMemoryHoldEpochWrite holds a real memory transaction after its epoch
// write. The transaction keeps its connection while it is held, exactly like
// a stalled advisory write.
type proxyMemoryHoldEpochWrite struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (*proxyMemoryHoldEpochWrite) BeforeQuery(ctx context.Context, _ *bun.QueryEvent) context.Context {
	return ctx
}

func (h *proxyMemoryHoldEpochWrite) AfterQuery(ctx context.Context, event *bun.QueryEvent) {
	if event.Err != nil || !strings.Contains(event.Query, "INSERT INTO proxy_memory_erasure_epochs") {
		return
	}
	h.once.Do(func() {
		close(h.started)
		select {
		case <-h.release:
		case <-ctx.Done():
		}
	})
}

// proxyMemoryIsolatedDatabases hands the composition the isolated memory
// database of the test. The production opener opens a separate file for the
// memory store, so the memory transaction never holds the core connection.
type proxyMemoryIsolatedDatabases struct {
	database *bun.DB
	opens    int
}

func (o *proxyMemoryIsolatedDatabases) OpenFeatureDatabase(context.Context, featuredb.Spec) (featuredb.Database, error) {
	o.opens++
	return featuredb.Database{DB: o.database, Identity: "isolated-memory"}, nil
}

// TestProxyMemoryRealMemoryTransactionDoesNotBlockCoreSessionPreparation is
// the composed regression of the shared-connection defect: a real memory
// transaction holds the memory database's only connection, and the mandatory
// core session preparation of the next request must still complete.
func TestProxyMemoryRealMemoryTransactionDoesNotBlockCoreSessionPreparation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	core := certificationOpenSQLite(t, certificationSQLiteDSN(filepath.Join(t.TempDir(), "core.db")))
	// SQLite has one writer; keep this composed contract on that supported topology.
	core.SetMaxOpenConns(1)
	memory := certificationOpenSQLite(t, certificationSQLiteDSN(filepath.Join(t.TempDir(), "proxy-memory.db")))
	memory.SetMaxOpenConns(1)
	hold := &proxyMemoryHoldEpochWrite{started: make(chan struct{}), release: make(chan struct{})}
	memory.AddQueryHook(hold)

	continuity, err := continuitybunstore.New(core)
	require.NoError(t, err)
	process, err := NewProcess(ctx, ProcessInput{
		Logger:           slog.Default(),
		BunDB:            core,
		ContinuityStore:  continuity,
		FeatureDatabases: &proxyMemoryIsolatedDatabases{database: memory},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = process.Close() })
	out, err := process.CompileGeneration(ctx, GenerationInput{
		Registrations: sentinelRegistrations(t), BackgroundClient: &sentinelAuxClient{},
	})
	require.NoError(t, err)
	var writer captureLifecycle
	for _, lifecycle := range out.Lifecycles {
		if candidate, ok := lifecycle.(captureLifecycle); ok {
			writer = candidate
			break
		}
	}
	require.NotNil(t, writer.capture)
	require.NoError(t, writer.Start(ctx))
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(hold.release) }) }
	t.Cleanup(func() {
		release()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = writer.Stop(stopCtx)
	})

	backend := &sentinelBackend{}
	ex := sentinelExecutor(t, core, out, backend)
	stream, err := ex.Execute(scope.WithScope(ctx, sentinelScope(sentinelPrincipalA)), sentinelClaudeTurn("isolated-first"))
	require.NoError(t, err)
	_, err = aipapi.Collect(ctx, stream)
	require.NoError(t, err)
	require.Equal(t, 1, backend.callCount())

	select {
	case <-hold.started:
	case <-ctx.Done():
		t.Fatal("the memory writer never entered a real memory transaction")
	}

	// The memory transaction still owns the memory database's only
	// connection. The next request must still prepare its core session and
	// reach the backend.
	nextCtx, nextCancel := context.WithTimeout(ctx, 5*time.Second)
	defer nextCancel()
	stream, err = ex.Execute(scope.WithScope(nextCtx, sentinelScope(sentinelPrincipalA)), sentinelClaudeTurn("isolated-second"))
	require.NoError(t, err, "mandatory session preparation must not wait for a memory write")
	_, err = aipapi.Collect(nextCtx, stream)
	require.NoError(t, err)
	require.Equal(t, 2, backend.callCount(), "the second request must reach the backend while the memory transaction is held")

	release()
}

// proxyMemoryFailingDecisionStore fails the first-turn decision write, the
// way a refused or failed advisory write does.
type proxyMemoryFailingDecisionStore struct {
	proxymemory.Store
	fail bool
}

func (s *proxyMemoryFailingDecisionStore) CommitFirstTurn(context.Context, proxymemory.FirstTurnJob) (bool, error) {
	if s.fail {
		return false, errors.New("synthetic decision persistence failure")
	}
	return false, nil
}

// TestProxyMemoryFailedDecisionMustNotPersistUnboundCapture proves the
// durable-binding invariant: a capture whose authoritative first-turn decision
// never committed is dropped, never repaired.
func TestProxyMemoryFailedDecisionMustNotPersistUnboundCapture(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail bool
	}{
		{name: "write error", fail: true},
		{name: "refused without error", fail: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newProxyMemoryTestHarness(t)
			proxyMemoryInstallCompositionStore(t, func(store proxymemory.Store) proxymemory.Store {
				return &proxyMemoryFailingDecisionStore{Store: store, fail: tc.fail}
			})
			out := h.compileGeneration(t, proxyMemoryRegistration(t, true))
			capture := proxyMemoryCaptureTransform(out)
			require.NotNil(t, capture)
			aLeg := h.createALeg(t)
			turn := proxyMemoryTestSession(aLeg)
			turn.AuthoritativeFirstTurn = true
			ctx := context.Background()
			require.NoError(t, capture.Handle(ctx, proxyMemoryTestAcceptedCall(), request.RequestMeta{
				TraceID: "failed-decision", Scope: proxyMemoryTestScope(), Session: turn,
			}, request.Services{}))
			observer, err := capture.Open(ctx, response.StreamMeta{TraceID: "failed-decision", ALegID: aLeg}, response.Services{})
			require.NoError(t, err)
			require.NoError(t, observer.Finish(ctx, response.OutcomeSuccessReleased))
			require.NoError(t, capture.Start(ctx))
			require.NoError(t, capture.Stop(ctx))

			var bindings, captures int
			require.NoError(t, h.db.NewRaw(`SELECT COUNT(*) FROM proxy_memory_session_bindings WHERE a_leg_id = ?`, aLeg).Scan(ctx, &bindings))
			require.NoError(t, h.db.NewRaw(`SELECT COUNT(*) FROM proxy_memory_sessions WHERE a_leg_id = ?`, aLeg).Scan(ctx, &captures))
			require.Zero(t, bindings, "the failed decision must not commit a binding")
			require.Zero(t, captures, "a failed first-turn decision must not authorize a durable capture")
		})
	}
}

// TestProxyMemoryRetirementEvictsReplayCacheAndKeepsGenerationCleanup proves
// the composed retirement: the process-owned cleanup (the replay cache) and
// the published generation's cleanup both run when an A-leg retires.
func TestProxyMemoryRetirementEvictsReplayCacheAndKeepsGenerationCleanup(t *testing.T) {
	ctx := context.Background()
	core := certificationOpenSQLite(t, certificationSQLiteDSN(filepath.Join(t.TempDir(), "retirement.db")))
	continuity, err := continuitybunstore.New(core)
	require.NoError(t, err)
	process, err := NewProcess(ctx, ProcessInput{
		Logger: slog.Default(), BunDB: core, ContinuityStore: continuity,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = process.Close() })

	// The published generation registers its own cleanup, as the generic
	// runtime bundle does at publication.
	var generationCleanups int
	continuity.SetKeyedALegRetirementObserver("generation", func(string) { generationCleanups++ })

	aLeg, err := continuity.CreateALeg(ctx, "retirement-key")
	require.NoError(t, err)
	process.proxyMemoryDecisions.Put(proxymemory.FirstTurnJob{
		Principal: proxymemory.Principal{TenantID: "tenant", PrincipalID: "alice"},
		ALegID:    aLeg.ALegID,
		Project:   proxymemory.Project{Key: "project", Label: "/work"},
	})
	_, status := process.proxyMemoryDecisions.GetStatus(
		proxymemory.Principal{TenantID: "tenant", PrincipalID: "alice"}, aLeg.ALegID)
	require.Equal(t, proxymemory.DecisionPending, status)

	// Replacing the continuity key retires the A-leg and notifies every
	// registered observer.
	_, err = continuity.CreateALeg(ctx, "retirement-key")
	require.NoError(t, err)
	require.Equal(t, 1, generationCleanups, "the published generation's cleanup must still run")
	_, status = process.proxyMemoryDecisions.GetStatus(
		proxymemory.Principal{TenantID: "tenant", PrincipalID: "alice"}, aLeg.ALegID)
	require.Equal(t, proxymemory.DecisionAbsent, status,
		"the retired A-leg's replay cache entry must be evicted")
}

// proxyMemoryRecordingOpener records every handle the composition opened and
// how often the opener ran.
type proxyMemoryRecordingOpener struct {
	featuredb.Opener

	mu      sync.Mutex
	opens   int
	handles []*bun.DB
}

func (o *proxyMemoryRecordingOpener) OpenFeatureDatabase(ctx context.Context, spec featuredb.Spec) (featuredb.Database, error) {
	opened, err := o.Opener.OpenFeatureDatabase(ctx, spec)
	o.mu.Lock()
	defer o.mu.Unlock()
	o.opens++
	if opened.DB != nil {
		o.handles = append(o.handles, opened.DB)
	}
	return opened, err
}

func (o *proxyMemoryRecordingOpener) snapshot() (int, []*bun.DB) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.opens, append([]*bun.DB(nil), o.handles...)
}

// proxyMemoryAliasingOpener opens the requested file and reports one
// identity for every request: the aliasing two spellings of one file show.
type proxyMemoryAliasingOpener struct {
	mu      sync.Mutex
	opens   int
	handles []*bun.DB
}

func (o *proxyMemoryAliasingOpener) OpenFeatureDatabase(ctx context.Context, spec featuredb.Spec) (featuredb.Database, error) {
	handle, err := db.OpenSQLiteBun(ctx, spec.SQLitePath)
	if err != nil {
		return featuredb.Database{}, err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.opens++
	o.handles = append(o.handles, handle)
	return featuredb.Database{DB: handle, Identity: "aliasing-memory"}, nil
}

func (o *proxyMemoryAliasingOpener) snapshot() (int, []*bun.DB) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.opens, append([]*bun.DB(nil), o.handles...)
}

// proxyMemorySameHandleOpener hands out one handle for every request. The
// process does not own that handle, so it must survive a composition that
// finds the database already open.
type proxyMemorySameHandleOpener struct {
	handle *bun.DB
	opens  int
}

func (o *proxyMemorySameHandleOpener) OpenFeatureDatabase(context.Context, featuredb.Spec) (featuredb.Database, error) {
	o.opens++
	return featuredb.Database{DB: o.handle, Identity: "same-handle-memory"}, nil
}

// proxyMemoryBlockingOpener opens the memory database only after the test
// releases it, so a process close can race the opening.
type proxyMemoryBlockingOpener struct {
	dir     string
	entered chan struct{}
	release chan struct{}
	once    sync.Once

	mu      sync.Mutex
	opens   int
	handles []*bun.DB
}

func (o *proxyMemoryBlockingOpener) OpenFeatureDatabase(ctx context.Context, _ featuredb.Spec) (featuredb.Database, error) {
	o.mu.Lock()
	o.opens++
	o.mu.Unlock()
	o.once.Do(func() { close(o.entered) })
	select {
	case <-o.release:
	case <-ctx.Done():
		return featuredb.Database{}, ctx.Err()
	}
	handle, err := db.OpenSQLiteBun(ctx, filepath.Join(o.dir, "memory.db"))
	if err != nil {
		return featuredb.Database{}, err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.handles = append(o.handles, handle)
	return featuredb.Database{DB: handle, Identity: "blocking-memory"}, nil
}

func (o *proxyMemoryBlockingOpener) snapshot() (int, []*bun.DB) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.opens, append([]*bun.DB(nil), o.handles...)
}

// proxyMemoryRegistrationWithPath builds one enabled proxy-memory
// registration whose memory database file is the given operator path.
func proxyMemoryRegistrationWithPath(t *testing.T, sqlitePath string) []aipsdk.Registration {
	t.Helper()
	var node yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(
		"principals:\n  - "+sentinelPrincipalA+
			"\nextract_route: aux-extract\nconsolidate_route: aux-consolidate\n"+
			"sqlite_path: '"+sqlitePath+"'\n"), &node))
	return []aipsdk.Registration{{
		Kind:    aipsdk.PluginKindFeature,
		ID:      proxymemory.ID,
		Enabled: true,
		Config:  aipsdk.ConfigPayload{Node: node},
	}}
}

// proxyMemoryProcessWithOpener builds a process over a real continuity
// database and the given memory database opener.
func proxyMemoryProcessWithOpener(t *testing.T, opener featuredb.Opener) *Runtime {
	t.Helper()
	ctx := context.Background()
	corePath := filepath.Join(t.TempDir(), "continuity.db")
	core := certificationOpenSQLite(t, certificationSQLiteDSN(corePath))
	continuity, err := continuitybunstore.New(core)
	require.NoError(t, err)
	process, err := NewProcess(ctx, ProcessInput{
		Logger: slog.Default(), BunDB: core, ContinuityStore: continuity, FeatureDatabases: opener,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = process.Close() })
	return process
}

// TestProxyMemoryDatabaseIsOpenedOnceAndClosesWithTheProcess proves the
// memory database is owned by the process: a generation reload reuses the
// handle of the running process, and the handle never escapes the process
// cleanup.
func TestProxyMemoryDatabaseIsOpenedOnceAndClosesWithTheProcess(t *testing.T) {
	ctx := context.Background()
	corePath := filepath.Join(t.TempDir(), "continuity.db")
	core := certificationOpenSQLite(t, certificationSQLiteDSN(corePath))
	continuity, err := continuitybunstore.New(core)
	require.NoError(t, err)
	opener := &proxyMemoryRecordingOpener{Opener: db.NewFeatureDatabaseOpener(db.FeatureDatabaseConfig{
		Store: "sqlite", SQLitePath: corePath,
	})}
	process, err := NewProcess(ctx, ProcessInput{
		Logger: slog.Default(), BunDB: core, ContinuityStore: continuity, FeatureDatabases: opener,
	})
	require.NoError(t, err)
	for range 2 {
		_, err := process.CompileGeneration(ctx, GenerationInput{
			Registrations: sentinelRegistrations(t), BackgroundClient: &sentinelAuxClient{},
		})
		require.NoError(t, err)
	}
	opens, handles := opener.snapshot()
	require.Equal(t, 1, opens, "the process must own one memory database handle, not one per generation")
	require.Len(t, handles, 1)
	require.NoError(t, handles[0].PingContext(ctx), "the process-owned handle must stay usable while the process runs")

	require.NoError(t, process.Close())
	for i, handle := range handles {
		require.Error(t, handle.PingContext(ctx), "memory database handle %d escaped the process cleanup", i)
	}
}

// TestProxyMemoryDatabaseDuplicateHandleIsClosedAndBorrowedHandleSurvives
// proves the duplicate discipline: a second handle for a database the process
// already owns is closed again, while a handle the process does not own (the
// very same pointer) stays open.
func TestProxyMemoryDatabaseDuplicateHandleIsClosedAndBorrowedHandleSurvives(t *testing.T) {
	t.Run("a duplicate handle is closed", func(t *testing.T) {
		ctx := context.Background()
		dir := t.TempDir()
		opener := &proxyMemoryAliasingOpener{}
		process := proxyMemoryProcessWithOpener(t, opener)
		for _, name := range []string{"first.db", "second.db"} {
			_, err := process.CompileGeneration(ctx, GenerationInput{
				Registrations:    proxyMemoryRegistrationWithPath(t, filepath.Join(dir, name)),
				BackgroundClient: &sentinelAuxClient{},
			})
			require.NoError(t, err)
		}
		opens, handles := opener.snapshot()
		require.Equal(t, 2, opens)
		require.Len(t, handles, 2)
		require.NoError(t, handles[0].PingContext(ctx), "the owned handle must stay usable")
		require.Error(t, handles[1].PingContext(ctx), "the duplicate handle must be closed again")
	})

	t.Run("a borrowed handle stays open", func(t *testing.T) {
		ctx := context.Background()
		dir := t.TempDir()
		borrowed := certificationOpenSQLite(t, certificationSQLiteDSN(filepath.Join(dir, "borrowed.db")))
		opener := &proxyMemorySameHandleOpener{handle: borrowed}
		process := proxyMemoryProcessWithOpener(t, opener)
		for _, name := range []string{"first.db", "second.db"} {
			_, err := process.CompileGeneration(ctx, GenerationInput{
				Registrations:    proxyMemoryRegistrationWithPath(t, filepath.Join(dir, name)),
				BackgroundClient: &sentinelAuxClient{},
			})
			require.NoError(t, err)
		}
		require.Equal(t, 2, opener.opens)
		require.NoError(t, borrowed.PingContext(ctx), "a handle the process does not own must not be closed")
	})
}

// TestProxyMemoryDatabaseAcquisitionIsRefusedAfterTheProcessClosed proves a
// closed process serves no generation and opens no memory database.
func TestProxyMemoryDatabaseAcquisitionIsRefusedAfterTheProcessClosed(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	opener := &proxyMemoryAliasingOpener{}
	process := proxyMemoryProcessWithOpener(t, opener)
	require.NoError(t, process.Close())

	_, err := process.CompileGeneration(ctx, GenerationInput{
		Registrations:    proxyMemoryRegistrationWithPath(t, filepath.Join(dir, "memory.db")),
		BackgroundClient: &sentinelAuxClient{},
	})
	require.Error(t, err, "a closed process must serve no generation")
	opens, _ := opener.snapshot()
	require.Zero(t, opens, "a closed process must open no memory database")
}

// TestProxyMemoryDatabaseSchemaFailureClosesTheOpenedHandle proves a
// composition that cannot apply the memory schema releases the handle it
// opened instead of leaking it.
func TestProxyMemoryDatabaseSchemaFailureClosesTheOpenedHandle(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "query-only.db")
	seed, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=foreign_keys(ON)")
	require.NoError(t, err)
	_, err = seed.ExecContext(ctx, "CREATE TABLE seed (id INTEGER)")
	require.NoError(t, err)
	require.NoError(t, seed.Close())

	// A query-only connection refuses the schema DDL but stays healthy, so
	// the handle is observable before and after the cleanup.
	sqlDB, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_query_only=1")
	require.NoError(t, err)
	handle, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	require.NoError(t, err)
	require.NoError(t, handle.PingContext(ctx))

	opener := &proxyMemorySameHandleOpener{handle: handle}
	process := proxyMemoryProcessWithOpener(t, opener)
	_, err = process.CompileGeneration(ctx, GenerationInput{
		Registrations: sentinelRegistrations(t), BackgroundClient: &sentinelAuxClient{},
	})
	require.Error(t, err, "the composition must fail when the memory schema cannot be applied")
	require.Error(t, handle.PingContext(ctx), "the opened handle must be closed when the composition fails")
}

// TestProxyMemoryDatabaseConcurrentCloseKeepsEveryHandleOwned proves a
// composition that loses the race with the process close is refused and that
// no handle escapes the process cleanup.
func TestProxyMemoryDatabaseConcurrentCloseKeepsEveryHandleOwned(t *testing.T) {
	ctx := context.Background()
	opener := &proxyMemoryBlockingOpener{
		dir: t.TempDir(), entered: make(chan struct{}), release: make(chan struct{}),
	}
	process := proxyMemoryProcessWithOpener(t, opener)
	registrations := sentinelRegistrations(t)

	compileDone := make(chan error, 1)
	go func() {
		_, err := process.CompileGeneration(ctx, GenerationInput{
			Registrations: registrations, BackgroundClient: &sentinelAuxClient{},
		})
		compileDone <- err
	}()
	<-opener.entered

	closing := make(chan struct{})
	process.registerCloser(func() error { close(closing); return nil })
	closeDone := make(chan error, 1)
	go func() { closeDone <- process.Close() }()
	<-closing
	close(opener.release)

	compileErr := <-compileDone
	require.Error(t, compileErr, "a composition that loses the race with the process close must fail")
	require.NoError(t, <-closeDone)
	_, handles := opener.snapshot()
	require.Len(t, handles, 1)
	require.Error(t, handles[0].PingContext(ctx), "the handle must not escape the process cleanup")
}

// proxyMemorySchemaBarrier pauses the schema setup of the memory database, so
// a process close can begin while the schema is applied.
type proxyMemorySchemaBarrier struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (*proxyMemorySchemaBarrier) BeforeQuery(ctx context.Context, _ *bun.QueryEvent) context.Context {
	return ctx
}

func (b *proxyMemorySchemaBarrier) AfterQuery(ctx context.Context, event *bun.QueryEvent) {
	if event.Err != nil {
		return
	}
	b.once.Do(func() {
		close(b.entered)
		select {
		case <-b.release:
		case <-ctx.Done():
		}
	})
}

// TestProxyMemoryDatabaseRefusesAcquisitionWhenCloseWinsDuringSchemaSetup
// proves the terminal close state is honored after the schema setup too: a
// close that begins while the schema is applied refuses the acquisition
// instead of publishing a handle the process is about to dispose.
func TestProxyMemoryDatabaseRefusesAcquisitionWhenCloseWinsDuringSchemaSetup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	handle, err := db.OpenSQLiteBun(ctx, filepath.Join(t.TempDir(), "memory.db"))
	require.NoError(t, err)
	barrier := &proxyMemorySchemaBarrier{entered: make(chan struct{}), release: make(chan struct{})}
	handle.AddQueryHook(barrier)
	pool := newProxyMemoryDatabasePool()
	t.Cleanup(func() { _ = pool.Close() })

	type acquisition struct {
		database *proxyMemoryDatabase
		err      error
	}
	acquired := make(chan acquisition, 1)
	go func() {
		database, acquireErr := pool.acquire(ctx, featuredb.Spec{
			FeatureID: proxymemory.ID, SQLitePath: "schema-barrier",
		}, &proxyMemorySameHandleOpener{handle: handle})
		acquired <- acquisition{database: database, err: acquireErr}
	}()
	<-barrier.entered

	closed := make(chan error, 1)
	go func() { closed <- pool.Close() }()
	for !pool.closing.Load() {
		select {
		case <-ctx.Done():
			t.Fatal("the pool close did not publish its terminal state")
		default:
			runtime.Gosched()
		}
	}
	close(barrier.release)

	result := <-acquired
	require.Nil(t, result.database, "a refused acquisition must publish no memory database")
	require.ErrorIs(t, result.err, errProxyMemoryDatabaseClosed)
	require.NoError(t, <-closed)
	require.Error(t, handle.PingContext(ctx), "the refused handle must be closed again")
}

// TestProxyMemoryCompileRacingProcessCloseFailsWithoutADisposedMemoryStore
// proves the composition seam of the same race: a compile that loses the race
// with the process close fails instead of serving a memory store the close is
// about to dispose.
func TestProxyMemoryCompileRacingProcessCloseFailsWithoutADisposedMemoryStore(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	handle, err := db.OpenSQLiteBun(ctx, filepath.Join(t.TempDir(), "memory.db"))
	require.NoError(t, err)
	barrier := &proxyMemorySchemaBarrier{entered: make(chan struct{}), release: make(chan struct{})}
	handle.AddQueryHook(barrier)
	process := proxyMemoryProcessWithOpener(t, &proxyMemorySameHandleOpener{handle: handle})
	registrations := sentinelRegistrations(t)

	compileDone := make(chan error, 1)
	go func() {
		_, compileErr := process.CompileGeneration(ctx, GenerationInput{
			Registrations: registrations, BackgroundClient: &sentinelAuxClient{},
		})
		compileDone <- compileErr
	}()
	<-barrier.entered

	closing := make(chan struct{})
	process.registerCloser(func() error { close(closing); return nil })
	closeDone := make(chan error, 1)
	go func() { closeDone <- process.Close() }()
	<-closing
	close(barrier.release)

	compileErr := <-compileDone
	require.Error(t, compileErr, "a composition that loses the race with the process close must fail")
	require.ErrorIs(t, compileErr, errProxyMemoryDatabaseClosed)
	require.NoError(t, <-closeDone)
	require.Error(t, handle.PingContext(ctx), "the refused memory database must be closed again")
}
