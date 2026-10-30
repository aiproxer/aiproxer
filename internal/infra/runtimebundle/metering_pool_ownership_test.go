package runtimebundle

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/uptrace/bun"
	_ "modernc.org/sqlite"
)

func TestOpenDurableMeteringJournalSchemaFailureKeepsRegistryPoolOpen(t *testing.T) {
	var opened *bun.DB
	registry := db.NewPoolRegistry(func(context.Context, string, db.PoolSettings) (*bun.DB, error) {
		sqlDB, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			return nil, err
		}
		opened, err = db.NewBunDB(sqlDB, db.DialectSQLite)
		return opened, err
	})
	t.Cleanup(func() { _ = registry.Close(context.Background()) })
	cfg := &config.Config{
		Database: config.DatabaseConfig{
			ConnectionMode: config.DatabaseConnectionModeTransactionPool,
			SchemaMode:     config.DatabaseSchemaModeVerifyOnly,
		},
		Metering: config.MeteringConfig{
			Enabled: true,
			Journal: config.MeteringJournalConfig{Store: "postgres", PostgresDSN: "postgres://runtime/db"},
		},
	}

	owner, _ := newTestProcessOwner()
	_, _, _, err := openDurableMeteringJournal(owner, t.Context(), cfg, time.Now, registry, nil)
	if err == nil {
		t.Fatal("expected missing-schema error")
	}
	if opened == nil {
		t.Fatal("registry opener was not called")
	}
	if err := opened.PingContext(t.Context()); err != nil {
		t.Fatalf("registry-owned pool was closed on schema failure: %v", err)
	}
}

func TestOpenPostgresStoreSchemaFailureLeavesPoolUnclaimedForPrune(t *testing.T) {
	var opened *bun.DB
	registry := db.NewPoolRegistry(func(context.Context, string, db.PoolSettings) (*bun.DB, error) {
		sqlDB, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			return nil, err
		}
		opened, err = db.NewBunDB(sqlDB, db.DialectSQLite)
		return opened, err
	})
	t.Cleanup(func() { _ = registry.Close(context.Background()) })

	_, _, err := openPostgresStore(t.Context(), "postgres://runtime/db", db.PoolSettings{}, config.DatabaseConfig{
		ConnectionMode: config.DatabaseConnectionModeTransactionPool,
		SchemaMode:     config.DatabaseSchemaModeVerifyOnly,
	}, registry, nil, postgresStoreLifecycle[*struct{}]{
		Verify: func(context.Context, *bun.DB) error {
			return context.Canceled
		},
		Open: func(context.Context, *bun.DB) (*struct{}, error) {
			t.Fatal("Open must not run after verify failure")
			return nil, nil
		},
	})
	if err == nil {
		t.Fatal("expected verify failure")
	}
	if registry.Len() != 1 {
		t.Fatalf("Len=%d want 1 unclaimed pool before prune", registry.Len())
	}
	if err := registry.PruneUnclaimed(); err != nil {
		t.Fatal(err)
	}
	if registry.Len() != 0 {
		t.Fatalf("Len=%d want 0 after prune", registry.Len())
	}
	if opened == nil {
		t.Fatal("opener was not called")
	}
	if err := opened.PingContext(t.Context()); err == nil {
		t.Fatal("pruned unclaimed pool should be closed")
	}
}

func TestOpenPostgresStoreClaimFailureClosesStore(t *testing.T) {
	registry := db.NewPoolRegistry(func(context.Context, string, db.PoolSettings) (*bun.DB, error) {
		sqlDB, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			return nil, err
		}
		return db.NewBunDB(sqlDB, db.DialectSQLite)
	})
	t.Cleanup(func() { _ = registry.Close(context.Background()) })

	closeErr := errors.New("store close failed")
	var closed bool
	_, _, err := openPostgresStore(t.Context(), "postgres://runtime/db", db.PoolSettings{}, config.DatabaseConfig{
		ConnectionMode: config.DatabaseConnectionModeTransactionPool,
		SchemaMode:     config.DatabaseSchemaModeVerifyOnly,
	}, registry, nil, postgresStoreLifecycle[*struct{}]{
		Verify: func(context.Context, *bun.DB) error { return nil },
		Open: func(context.Context, *bun.DB) (*struct{}, error) {
			// Remove the just-opened pool so Claim fails with "unknown postgres pool".
			if err := registry.PruneUnclaimed(); err != nil {
				t.Fatalf("prune before claim: %v", err)
			}
			return &struct{}{}, nil
		},
		Close: func(*struct{}) error {
			closed = true
			return closeErr
		},
	})
	if err == nil {
		t.Fatal("expected claim failure")
	}
	if !closed {
		t.Fatal("lifecycle.Close must run when Claim fails")
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("error=%v want joined store close failure", err)
	}
	if !strings.Contains(err.Error(), "claim postgres pool") {
		t.Fatalf("error=%v want claim failure", err)
	}
}

func TestJoinCloseErrSurfacesCloseError(t *testing.T) {
	primary := errors.New("verify failed")
	closeErr := errors.New("drain failed")
	joined := joinCloseErr(primary, closeErr)
	if !errors.Is(joined, primary) || !errors.Is(joined, closeErr) {
		t.Fatalf("error=%v want both primary and closeErr", joined)
	}
	if !strings.Contains(joined.Error(), "close postgres") {
		t.Fatalf("error=%v want close postgres wrap", joined)
	}
	if got := joinCloseErr(primary, nil); !errors.Is(got, primary) || got != primary {
		t.Fatalf("nil closeErr must return primary unchanged, got %v", got)
	}
}

func TestOpenPostgresStoreRegistrySuccessUsesNoopCloser(t *testing.T) {
	var storeClosed bool
	registry := db.NewPoolRegistry(func(context.Context, string, db.PoolSettings) (*bun.DB, error) {
		sqlDB, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			return nil, err
		}
		return db.NewBunDB(sqlDB, db.DialectSQLite)
	})
	t.Cleanup(func() { _ = registry.Close(context.Background()) })

	store, closer, err := openPostgresStore(t.Context(), "postgres://runtime/db", db.PoolSettings{}, config.DatabaseConfig{
		ConnectionMode: config.DatabaseConnectionModeTransactionPool,
		SchemaMode:     config.DatabaseSchemaModeVerifyOnly,
	}, registry, nil, postgresStoreLifecycle[*struct{}]{
		Verify: func(context.Context, *bun.DB) error { return nil },
		Open: func(context.Context, *bun.DB) (*struct{}, error) {
			return &struct{}{}, nil
		},
		Close: func(*struct{}) error {
			storeClosed = true
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if store == nil || closer == nil {
		t.Fatal("expected store and closer")
	}
	if err := closer(); err != nil {
		t.Fatalf("registry-owned closer is a no-op: %v", err)
	}
	if storeClosed {
		t.Fatal("lifecycle.Close must not run for registry-owned success path")
	}
	if registry.Len() != 1 {
		t.Fatalf("Len=%d want 1 claimed pool", registry.Len())
	}
}
