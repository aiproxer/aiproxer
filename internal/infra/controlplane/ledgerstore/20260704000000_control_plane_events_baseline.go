package ledgerstore

import (
	"context"
	"fmt"
	"sync"

	"github.com/aiproxer/aiproxer/internal/bunmigrate"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
	"github.com/uptrace/bun/migrate"
)

// BaselineMigrationName is the name bun/migrate records for this adapter's
// baseline (file prefix).
const BaselineMigrationName = "20260704000000"

// controlPlaneMigrations holds versioned DDL for the control-plane event
// ledger store.
var (
	controlPlaneMigrations = migrate.NewMigrations()
	registerMigrationsOnce sync.Once
)

func registerControlPlaneMigrations() {
	registerMigrationsOnce.Do(func() {
		controlPlaneMigrations.MustRegister(controlPlaneBaselineUp, func(context.Context, *bun.DB) error { return nil })
	})
}

func runControlPlaneSchemaMigrate(ctx context.Context, db *bun.DB) error {
	registerControlPlaneMigrations()
	if err := bunmigrate.Migrate(ctx, db, "bun_controlplane_migrations", controlPlaneMigrations); err != nil {
		return fmt.Errorf("ledgerstore: %w", err)
	}
	return nil
}

func controlPlaneBaselineUp(ctx context.Context, db *bun.DB) error {
	var stmts []string
	switch db.Dialect().Name() {
	case dialect.SQLite:
		stmts = sqliteControlPlaneDDL()
	case dialect.PG:
		stmts = postgresControlPlaneDDL()
	default:
		return fmt.Errorf("ledgerstore: unsupported bun dialect %s", db.Dialect().Name().String())
	}
	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("ledgerstore: control-plane baseline: %w", err)
		}
	}
	return nil
}

// migrationsTableName returns the per-adapter migrations table name for tests
// and diagnostics. It is constant for v1.
