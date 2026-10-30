package db

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/driver/pgdriver"
)

func newTestBunDB() *bun.DB {
	return bun.NewDB(sql.OpenDB(pgdriver.NewConnector(pgdriver.WithDSN("postgres://unused"))), pgdialect.New())
}

func TestPostgresPoolRegistryZeroValueOpenAndClose(t *testing.T) {
	var opens atomic.Int32
	var registry PoolRegistry
	registry.opener = func(context.Context, string, PoolSettings) (*bun.DB, error) {
		opens.Add(1)
		return newTestBunDB(), nil
	}

	db, err := registry.Open(t.Context(), "postgres://host/db", PoolSettings{MaxOpenConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	if db == nil || opens.Load() != 1 {
		t.Fatalf("open db=%v opens=%d", db, opens.Load())
	}
	if err := registry.Claim(db); err != nil {
		t.Fatal(err)
	}
	if registry.Len() != 1 {
		t.Fatalf("Len=%d want 1", registry.Len())
	}
	if err := registry.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if registry.Len() != 0 {
		t.Fatalf("Len after Close=%d want 0", registry.Len())
	}
}

func TestPostgresPoolRegistryReusesSanitizedKey(t *testing.T) {
	var opens atomic.Int32
	registry := NewPoolRegistry(func(context.Context, string, PoolSettings) (*bun.DB, error) {
		opens.Add(1)
		return newTestBunDB(), nil
	})
	t.Cleanup(func() { _ = registry.Close(context.Background()) })

	pool := PoolSettings{MaxOpenConns: 4}
	a, err := registry.Open(t.Context(), "postgres://host/db?sslmode=require&channel_binding=require", pool)
	if err != nil {
		t.Fatal(err)
	}
	b, err := registry.Open(t.Context(), "postgres://host/db?sslmode=require", pool)
	if err != nil {
		t.Fatal(err)
	}
	if a != b || opens.Load() != 1 {
		t.Fatalf("same sanitized key opened %d pools", opens.Load())
	}
}

func TestPostgresPoolRegistrySeparatesPoolSettings(t *testing.T) {
	var opens atomic.Int32
	registry := NewPoolRegistry(func(context.Context, string, PoolSettings) (*bun.DB, error) {
		opens.Add(1)
		return newTestBunDB(), nil
	})
	t.Cleanup(func() { _ = registry.Close(context.Background()) })

	if _, err := registry.Open(t.Context(), "postgres://host/db", PoolSettings{MaxOpenConns: 4}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Open(t.Context(), "postgres://host/db", PoolSettings{MaxOpenConns: 8}); err != nil {
		t.Fatal(err)
	}
	if opens.Load() != 2 {
		t.Fatalf("opens=%d want 2", opens.Load())
	}
}

func TestPostgresPoolRegistryDoesNotCacheFailedOpen(t *testing.T) {
	var opens atomic.Int32
	want := errors.New("open failed")
	registry := NewPoolRegistry(func(context.Context, string, PoolSettings) (*bun.DB, error) {
		opens.Add(1)
		return nil, want
	})
	for range 2 {
		_, err := registry.Open(t.Context(), "postgres://host/db", PoolSettings{})
		if !errors.Is(err, want) {
			t.Fatalf("error=%v", err)
		}
	}
	if opens.Load() != 2 {
		t.Fatalf("opens=%d want retryable 2", opens.Load())
	}
}

func TestPostgresPoolRegistryRejectsNilDBWithoutError(t *testing.T) {
	registry := NewPoolRegistry(func(context.Context, string, PoolSettings) (*bun.DB, error) {
		return nil, nil
	})
	db, err := registry.Open(t.Context(), "postgres://host/db", PoolSettings{})
	if err == nil || !strings.Contains(err.Error(), "nil db") {
		t.Fatalf("error=%v want nil db", err)
	}
	if db != nil {
		t.Fatal("expected nil db on rejected open")
	}
	if registry.Len() != 0 {
		t.Fatalf("Len=%d want 0", registry.Len())
	}
	if err := registry.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestPostgresPoolRegistryPruneUnclaimed(t *testing.T) {
	registry := NewPoolRegistry(func(context.Context, string, PoolSettings) (*bun.DB, error) {
		return newTestBunDB(), nil
	})
	t.Cleanup(func() { _ = registry.Close(context.Background()) })

	unclaimed, err := registry.Open(t.Context(), "postgres://host/unclaimed", PoolSettings{})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := registry.Open(t.Context(), "postgres://host/claimed", PoolSettings{})
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Claim(claimed); err != nil {
		t.Fatal(err)
	}
	if err := registry.PruneUnclaimed(); err != nil {
		t.Fatal(err)
	}
	if registry.Len() != 1 {
		t.Fatalf("Len=%d want 1 claimed pool", registry.Len())
	}
	again, err := registry.Open(t.Context(), "postgres://host/claimed", PoolSettings{})
	if err != nil {
		t.Fatal(err)
	}
	if again != claimed {
		t.Fatal("claimed pool must remain reusable after prune")
	}
	if err := unclaimed.PingContext(t.Context()); err == nil {
		t.Fatal("unclaimed pool should be closed after prune")
	}
}

func TestPostgresPoolRegistryClaimUnknownPool(t *testing.T) {
	registry := NewPoolRegistry(func(context.Context, string, PoolSettings) (*bun.DB, error) {
		return newTestBunDB(), nil
	})
	t.Cleanup(func() { _ = registry.Close(context.Background()) })
	foreign := newTestBunDB()
	t.Cleanup(func() { _ = foreign.Close() })
	if err := registry.Claim(foreign); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("error=%v want unknown", err)
	}
	if err := registry.Claim(nil); err == nil || !strings.Contains(err.Error(), "nil") {
		t.Fatalf("error=%v want nil", err)
	}
}

func TestPostgresPoolRegistryConcurrentOpenSharesOnePool(t *testing.T) {
	var opens atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	registry := NewPoolRegistry(func(context.Context, string, PoolSettings) (*bun.DB, error) {
		opens.Add(1)
		close(started)
		<-release
		return newTestBunDB(), nil
	})
	t.Cleanup(func() { _ = registry.Close(context.Background()) })

	start := make(chan struct{})
	results := make(chan *bun.DB, 3)
	errs := make(chan error, 3)
	var wg sync.WaitGroup
	for range 3 {
		wg.Go(func() {
			<-start
			pool, err := registry.Open(t.Context(), "postgres://host/db", PoolSettings{})
			results <- pool
			errs <- err
		})
	}
	close(start)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("pool opener did not start")
	}
	close(release)
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first *bun.DB
	for pool := range results {
		if first == nil {
			first = pool
		} else if pool != first {
			t.Fatal("concurrent open returned different pools")
		}
	}
	if opens.Load() != 1 {
		t.Fatalf("opens=%d want 1", opens.Load())
	}
}

func TestPostgresPoolRegistryWaiterHonorsContextCancellation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	registry := NewPoolRegistry(func(context.Context, string, PoolSettings) (*bun.DB, error) {
		close(started)
		<-release
		return newTestBunDB(), nil
	})
	t.Cleanup(func() { _ = registry.Close(context.Background()) })

	firstDone := make(chan error, 1)
	go func() {
		_, err := registry.Open(t.Context(), "postgres://host/db", PoolSettings{})
		firstDone <- err
	}()
	<-started
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := registry.Open(cancelled, "postgres://host/db", PoolSettings{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v want context canceled", err)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
}

func TestPostgresPoolRegistryLeaderCancelPropagatesAndWaiterRetries(t *testing.T) {
	var opens atomic.Int32
	started := make(chan struct{})
	registry := NewPoolRegistry(func(ctx context.Context, _ string, _ PoolSettings) (*bun.DB, error) {
		if opens.Add(1) == 1 {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return newTestBunDB(), nil
	})
	t.Cleanup(func() { _ = registry.Close(context.Background()) })

	leaderCtx, leaderCancel := context.WithCancel(t.Context())
	leaderDone := make(chan struct {
		db  *bun.DB
		err error
	}, 1)
	go func() {
		db, err := registry.Open(leaderCtx, "postgres://host/db", PoolSettings{})
		leaderDone <- struct {
			db  *bun.DB
			err error
		}{db, err}
	}()
	<-started

	waiterDone := make(chan struct {
		db  *bun.DB
		err error
	}, 1)
	go func() {
		db, err := registry.Open(t.Context(), "postgres://host/db", PoolSettings{})
		waiterDone <- struct {
			db  *bun.DB
			err error
		}{db, err}
	}()

	leaderCancel()

	leader := <-leaderDone
	waiter := <-waiterDone
	if !errors.Is(leader.err, context.Canceled) {
		t.Fatalf("leader error=%v want context canceled", leader.err)
	}
	if waiter.err != nil {
		t.Fatalf("waiter error=%v want success", waiter.err)
	}
	if leader.db != nil || waiter.db == nil {
		t.Fatalf("leader db=%v waiter db=%v want only waiter pool", leader.db, waiter.db)
	}
	if opens.Load() != 2 {
		t.Fatalf("opens=%d want leader retry", opens.Load())
	}
}

func TestPostgresPoolRegistryCloseNilContext(t *testing.T) {
	registry := NewPoolRegistry(func(context.Context, string, PoolSettings) (*bun.DB, error) {
		return newTestBunDB(), nil
	})
	owned, err := registry.Open(t.Context(), "postgres://host/db", PoolSettings{})
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Claim(owned); err != nil {
		t.Fatal(err)
	}
	if err := registry.Close(nil); !errors.Is(err, ErrNilContext) { //nolint:staticcheck // contract: explicit nil ctx
		t.Fatalf("error=%v want ErrNilContext", err)
	}
	if registry.Len() != 1 {
		t.Fatalf("Len=%d want 1 (nil Close must not mark closed)", registry.Len())
	}
	if err := registry.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresPoolRegistryCloseHonorsContextCancellation(t *testing.T) {
	ownedStarted := make(chan struct{})
	ownedRelease := make(chan struct{})
	inflightStarted := make(chan struct{})
	inflightRelease := make(chan struct{})
	var opens atomic.Int32

	registry := NewPoolRegistry(func(context.Context, string, PoolSettings) (*bun.DB, error) {
		n := opens.Add(1)
		if n == 1 {
			close(ownedStarted)
			<-ownedRelease
			return newTestBunDB(), nil
		}
		close(inflightStarted)
		<-inflightRelease
		return newTestBunDB(), nil
	})

	ownedDone := make(chan error, 1)
	go func() {
		db, err := registry.Open(t.Context(), "postgres://host/owned", PoolSettings{})
		if err != nil {
			ownedDone <- err
			return
		}
		ownedDone <- registry.Claim(db)
	}()
	<-ownedStarted
	close(ownedRelease)
	if err := <-ownedDone; err != nil {
		t.Fatal(err)
	}
	if registry.Len() != 1 {
		t.Fatalf("Len=%d want 1 claimed pool before cancel Close", registry.Len())
	}

	openDone := make(chan error, 1)
	go func() {
		_, err := registry.Open(t.Context(), "postgres://host/inflight", PoolSettings{})
		openDone <- err
	}()
	<-inflightStarted

	entered := make(chan struct{})
	registry.beforeGateWait = func() { close(entered) }

	closeCtx, cancelClose := context.WithCancel(t.Context())
	closeDone := make(chan error, 1)
	go func() {
		closeDone <- registry.Close(closeCtx)
	}()
	<-entered
	cancelClose()

	closeErr := <-closeDone
	if !errors.Is(closeErr, context.Canceled) {
		t.Fatalf("Close error=%v want context.Canceled", closeErr)
	}
	if registry.Len() != 0 {
		t.Fatalf("Len=%d want 0 after cancelled Close closed snapshot", registry.Len())
	}

	close(inflightRelease)
	openErr := <-openDone
	if openErr == nil {
		t.Fatal("inflight open must fail after Close marked registry closed")
	}
}

func TestPostgresPoolRegistryCloseWaitsForInflightOpen(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	openerFinished := make(chan struct{})

	registry := NewPoolRegistry(func(context.Context, string, PoolSettings) (*bun.DB, error) {
		close(started)
		<-release
		close(openerFinished)
		return newTestBunDB(), nil
	})

	openDone := make(chan error, 1)
	go func() {
		_, err := registry.Open(t.Context(), "postgres://host/db", PoolSettings{})
		openDone <- err
	}()
	<-started

	entered := make(chan struct{})
	registry.beforeGateWait = func() { close(entered) }

	closeDone := make(chan error, 1)
	go func() {
		closeDone <- registry.Close(t.Context())
	}()
	<-entered
	select {
	case <-closeDone:
		t.Fatal("Close returned before in-flight open finished")
	default:
	}

	close(release)
	<-openerFinished

	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	openErr := <-openDone
	if openErr == nil || !strings.Contains(openErr.Error(), "closed during open") {
		t.Fatalf("open error=%v want closed during open", openErr)
	}
	if registry.Len() != 0 {
		t.Fatalf("Len=%d want 0", registry.Len())
	}
	if err := registry.Close(t.Context()); err != nil {
		t.Fatalf("idempotent Close: %v", err)
	}
}

func TestPostgresPoolRegistryClosedDuringOpenJoinsCloseError(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	drainErr := errors.New("drain failed")

	registry := NewPoolRegistry(func(context.Context, string, PoolSettings) (*bun.DB, error) {
		close(started)
		<-release
		return newTestBunDB(), nil
	})
	registry.closePool = func(db *bun.DB) error {
		if db != nil {
			_ = db.Close()
		}
		return drainErr
	}

	openDone := make(chan error, 1)
	go func() {
		_, err := registry.Open(t.Context(), "postgres://host/db", PoolSettings{})
		openDone <- err
	}()
	<-started

	entered := make(chan struct{})
	registry.beforeGateWait = func() { close(entered) }

	closeDone := make(chan error, 1)
	go func() {
		closeDone <- registry.Close(t.Context())
	}()
	<-entered
	close(release)

	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	openErr := <-openDone
	if openErr == nil {
		t.Fatal("want error when registry closes during open")
	}
	if !strings.Contains(openErr.Error(), "closed during open") {
		t.Fatalf("error=%v want closed during open", openErr)
	}
	if !errors.Is(openErr, drainErr) {
		t.Fatalf("error=%v want joined close failure", openErr)
	}
}

func TestPostgresPoolRegistryWaiterErrorsWhenClosedDuringOpen(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})

	registry := NewPoolRegistry(func(context.Context, string, PoolSettings) (*bun.DB, error) {
		close(started)
		<-release
		return newTestBunDB(), nil
	})

	leaderDone := make(chan error, 1)
	go func() {
		_, err := registry.Open(t.Context(), "postgres://host/db", PoolSettings{})
		leaderDone <- err
	}()
	<-started

	waiterDone := make(chan error, 1)
	go func() {
		_, err := registry.Open(t.Context(), "postgres://host/db", PoolSettings{})
		waiterDone <- err
	}()

	entered := make(chan struct{})
	registry.beforeGateWait = func() { close(entered) }

	closeDone := make(chan error, 1)
	go func() {
		closeDone <- registry.Close(t.Context())
	}()
	<-entered
	close(release)

	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	leaderErr := <-leaderDone
	if leaderErr == nil {
		t.Fatal("leader want error when registry closes during open")
	}
	waiterErr := <-waiterDone
	if waiterErr == nil {
		t.Fatal("waiter must not receive a usable pool after Close")
	}
}

func TestPostgresPoolRegistryStatsSnapshotsOwnedPools(t *testing.T) {
	registry := NewPoolRegistry(func(context.Context, string, PoolSettings) (*bun.DB, error) {
		return newTestBunDB(), nil
	})
	t.Cleanup(func() { _ = registry.Close(context.Background()) })

	if got := len(registry.Stats()); got != 0 {
		t.Fatalf("Stats()=%d want 0 before any open", got)
	}
	if _, err := registry.Open(t.Context(), "postgres://host/a", PoolSettings{MaxOpenConns: 4}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Open(t.Context(), "postgres://host/b", PoolSettings{MaxOpenConns: 8}); err != nil {
		t.Fatal(err)
	}
	stats := registry.Stats()
	if len(stats) != 2 {
		t.Fatalf("Stats()=%d want 2", len(stats))
	}
	// Closing the registry must yield an empty snapshot without panicking.
	if err := registry.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := len(registry.Stats()); got != 0 {
		t.Fatalf("Stats() after Close=%d want 0", got)
	}
}

func TestPostgresPoolRegistryStatsConcurrentWithOpenAndClose(t *testing.T) {
	registry := NewPoolRegistry(func(context.Context, string, PoolSettings) (*bun.DB, error) {
		return newTestBunDB(), nil
	})

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for range 20 {
			_ = registry.Stats()
		}
	}()
	go func() {
		defer wg.Done()
		for i := range 10 {
			dsn := "postgres://host/stats-" + strconv.Itoa(i%3)
			db, err := registry.Open(t.Context(), dsn, PoolSettings{MaxOpenConns: 2})
			if err != nil {
				continue
			}
			_ = registry.Claim(db)
		}
	}()
	go func() {
		defer wg.Done()
		for range 20 {
			_ = registry.Stats()
		}
	}()
	wg.Wait()
	if err := registry.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	_ = registry.Stats()
}
