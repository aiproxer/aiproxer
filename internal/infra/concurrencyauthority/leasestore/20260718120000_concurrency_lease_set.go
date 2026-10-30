package leasestore

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

const LeaseSetMigrationName = "20260718120000"

func registerLeaseSetMigration() {
	migrations.MustRegister(leaseSetUp, func(context.Context, *bun.DB) error { return nil })
}

func leaseSetUp(ctx context.Context, db *bun.DB) error {
	var stmts []string
	switch db.Dialect().Name() {
	case dialect.SQLite:
		stmts = []string{
			`ALTER TABLE concurrency_leases ADD COLUMN identity_version INTEGER NOT NULL DEFAULT 1`,
			`ALTER TABLE concurrency_leases ADD COLUMN set_id TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE concurrency_leases ADD COLUMN set_generation INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE concurrency_leases ADD COLUMN set_state TEXT NOT NULL DEFAULT ''`,
			`CREATE INDEX IF NOT EXISTS idx_concurrency_leases_set
				ON concurrency_leases(store_id, set_id)`,
		}
	case dialect.PG:
		stmts = []string{
			`ALTER TABLE concurrency_leases ADD COLUMN IF NOT EXISTS identity_version BIGINT NOT NULL DEFAULT 1`,
			`ALTER TABLE concurrency_leases ADD COLUMN IF NOT EXISTS set_id TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE concurrency_leases ADD COLUMN IF NOT EXISTS set_generation BIGINT NOT NULL DEFAULT 0`,
			`ALTER TABLE concurrency_leases ADD COLUMN IF NOT EXISTS set_state TEXT NOT NULL DEFAULT ''`,
			`CREATE INDEX IF NOT EXISTS idx_concurrency_leases_set
				ON concurrency_leases(store_id, set_id)`,
		}
	default:
		return fmt.Errorf("unsupported bun dialect %s", db.Dialect().Name().String())
	}
	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("concurrency lease set migration: %w", err)
		}
	}
	return nil
}
