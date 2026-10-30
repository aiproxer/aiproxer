package runtimebundle

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/dbmigrate"
	"github.com/uptrace/bun"
)

func TestDualPlaneComponentsForDSNShared(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Accounting: config.AccountingConfig{
			Authority:   config.AccountingAuthorityConfig{Enabled: true, Store: "postgres", PostgresDSN: "postgres://u:p@host/db?sslmode=disable"},
			Concurrency: config.ConcurrencyAuthorityConfig{Enabled: true, Store: "postgres", PostgresDSN: "postgres://u:p@host/db?sslmode=disable"},
		},
		Metering: config.MeteringConfig{
			Enabled: true,
			Journal: config.MeteringJournalConfig{Store: "postgres", PostgresDSN: "postgres://u:p@host/db?sslmode=disable"},
		},
	}
	key, err := db.SanitizePostgresDSN("postgres://u:p@host/db?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	components, err := dualPlaneComponentsForDSN(cfg, key)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(components, ","); got != strings.Join([]string{
		dbmigrate.ComponentConcurrency,
		dbmigrate.ComponentMetering,
	}, ",") {
		t.Fatalf("components=%q", got)
	}
}

func TestDualPlaneMigratorEnsureOneAdminOpenSharedDSN(t *testing.T) {
	var adminOpens atomic.Int32
	var migrateCalls atomic.Int32
	origOpen := openPostgresBun
	t.Cleanup(func() { openPostgresBun = origOpen })
	openPostgresBun = func(_ context.Context, _ string, pool db.PoolSettings) (*bun.DB, error) {
		adminOpens.Add(1)
		if pool.MaxOpenConns != 1 || pool.MaxIdleConns != 1 {
			t.Fatalf("admin pool=%+v want MaxOpen=1 MaxIdle=1", pool)
		}
		return openMemoryBun(t), nil
	}

	cfg := &config.Config{
		Database: config.DatabaseConfig{SchemaMode: config.DatabaseSchemaModeAutoMigrate, MaxOpenConns: 8},
		Accounting: config.AccountingConfig{
			Authority:   config.AccountingAuthorityConfig{Enabled: true, Store: "postgres", PostgresDSN: "postgres://u:p@host/db"},
			Concurrency: config.ConcurrencyAuthorityConfig{Enabled: true, Store: "postgres", PostgresDSN: "postgres://u:p@host/db"},
		},
		Metering: config.MeteringConfig{
			Enabled: true,
			Journal: config.MeteringJournalConfig{Store: "postgres", PostgresDSN: "postgres://u:p@host/db"},
		},
	}
	migrator := newDualPlaneMigrator(cfg)
	migrator.lookupComponent = func(string) (func(context.Context, *bun.DB) error, func(context.Context, *bun.DB) error, error) {
		return func(context.Context, *bun.DB) error {
			migrateCalls.Add(1)
			return nil
		}, func(context.Context, *bun.DB) error { return nil }, nil
	}
	if err := migrator.Ensure(t.Context(), "postgres://u:p@host/db"); err != nil {
		t.Fatal(err)
	}
	if err := migrator.Ensure(t.Context(), "postgres://u:p@host/db"); err != nil {
		t.Fatal(err)
	}
	if adminOpens.Load() != 1 {
		t.Fatalf("admin opens=%d want 1", adminOpens.Load())
	}
	if migrateCalls.Load() != 2 {
		t.Fatalf("migrate calls=%d want 2", migrateCalls.Load())
	}
}

func TestDualPlaneMigratorEnsureVerifyOnlyNoOp(t *testing.T) {
	origOpen := openPostgresBun
	t.Cleanup(func() { openPostgresBun = origOpen })
	var adminOpens atomic.Int32
	openPostgresBun = func(context.Context, string, db.PoolSettings) (*bun.DB, error) {
		adminOpens.Add(1)
		return openMemoryBun(t), nil
	}
	cfg := &config.Config{
		Database: config.DatabaseConfig{
			ConnectionMode: config.DatabaseConnectionModeTransactionPool,
			SchemaMode:     config.DatabaseSchemaModeVerifyOnly,
			MaxOpenConns:   8,
		},
		Metering: config.MeteringConfig{
			Enabled: true,
			Journal: config.MeteringJournalConfig{Store: "postgres", PostgresDSN: "postgres://u:p@host/db"},
		},
	}
	if err := newDualPlaneMigrator(cfg).Ensure(t.Context(), "postgres://u:p@host/db"); err != nil {
		t.Fatal(err)
	}
	if adminOpens.Load() != 0 {
		t.Fatalf("admin opens=%d want 0", adminOpens.Load())
	}
}

func TestOpenPostgresStoreRegistryAutoMigrateUsesMigratorOnce(t *testing.T) {
	var adminOpens atomic.Int32
	var migrateCalls atomic.Int32
	origOpen := openPostgresBun
	t.Cleanup(func() { openPostgresBun = origOpen })
	openPostgresBun = func(context.Context, string, db.PoolSettings) (*bun.DB, error) {
		adminOpens.Add(1)
		return openMemoryBun(t), nil
	}

	cfg := &config.Config{
		Database: config.DatabaseConfig{SchemaMode: config.DatabaseSchemaModeAutoMigrate, MaxOpenConns: 8},
		Metering: config.MeteringConfig{
			Enabled: true,
			Journal: config.MeteringJournalConfig{Store: "postgres", PostgresDSN: "postgres://runtime/db"},
		},
	}
	migrator := newDualPlaneMigrator(cfg)
	migrator.lookupComponent = func(string) (func(context.Context, *bun.DB) error, func(context.Context, *bun.DB) error, error) {
		return func(context.Context, *bun.DB) error {
			migrateCalls.Add(1)
			return nil
		}, func(context.Context, *bun.DB) error { return nil }, nil
	}
	registry := db.NewPoolRegistry(func(context.Context, string, db.PoolSettings) (*bun.DB, error) {
		return openMemoryBun(t), nil
	})
	t.Cleanup(func() { _ = registry.Close(context.Background()) })

	for range 2 {
		_, _, err := openPostgresStore(t.Context(), "postgres://runtime/db", db.PoolSettings{MaxOpenConns: 8}, cfg.Database, registry, migrator, postgresStoreLifecycle[*struct{}]{
			Verify: func(context.Context, *bun.DB) error { return nil },
			Open: func(context.Context, *bun.DB) (*struct{}, error) {
				return &struct{}{}, nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if adminOpens.Load() != 1 {
		t.Fatalf("admin opens=%d want 1", adminOpens.Load())
	}
	if migrateCalls.Load() != 1 {
		t.Fatalf("migrate calls=%d want 1", migrateCalls.Load())
	}
}
