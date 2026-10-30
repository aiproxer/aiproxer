package leasestore

import (
	"context"
	"fmt"
	"sync"

	"github.com/aiproxer/aiproxer/internal/bunmigrate"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
	"github.com/uptrace/bun/migrate"
)

const BaselineMigrationName = "20260714160000"

var (
	migrations             = migrate.NewMigrations()
	registerMigrationsOnce sync.Once
)

func registerMigrations() {
	registerMigrationsOnce.Do(func() {
		migrations.MustRegister(baselineUp, func(context.Context, *bun.DB) error { return nil })
		registerLeaseSetMigration()
	})
}

func runSchemaMigrate(ctx context.Context, db *bun.DB) error {
	registerMigrations()
	if err := bunmigrate.Migrate(ctx, db, "bun_concurrency_lease_migrations", migrations); err != nil {
		return fmt.Errorf("leasestore: %w", err)
	}
	return nil
}

func baselineUp(ctx context.Context, db *bun.DB) error {
	var stmts []string
	switch db.Dialect().Name() {
	case dialect.SQLite:
		stmts = sqliteDDL()
	case dialect.PG:
		stmts = postgresDDL()
	default:
		return fmt.Errorf("unsupported bun dialect %s", db.Dialect().Name().String())
	}
	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("concurrency lease baseline: %w", err)
		}
	}
	return nil
}

func sqliteDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS concurrency_leases (
			store_id TEXT NOT NULL,
			lease_id TEXT NOT NULL,
			rule_id TEXT NOT NULL,
			rule_version TEXT NOT NULL DEFAULT '',
			namespace TEXT NOT NULL DEFAULT '',
			dimension_key TEXT NOT NULL,
			logical_id TEXT NOT NULL DEFAULT '',
			holder_id TEXT NOT NULL DEFAULT '',
			acquired_at_unix INTEGER NOT NULL,
			renewed_at_unix INTEGER NOT NULL,
			expires_at_unix INTEGER NOT NULL,
			released_at_unix INTEGER NOT NULL DEFAULT 0,
			generation INTEGER NOT NULL,
			state TEXT NOT NULL,
			dimensions_json TEXT NOT NULL,
			PRIMARY KEY (store_id, lease_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_concurrency_leases_capacity
			ON concurrency_leases(store_id, rule_id, dimension_key, state, expires_at_unix)`,
		`CREATE TABLE IF NOT EXISTS concurrency_lease_capacity (
			store_id TEXT NOT NULL,
			rule_id TEXT NOT NULL,
			dimension_key TEXT NOT NULL,
			PRIMARY KEY (store_id, rule_id, dimension_key)
		)`,
	}
}

func postgresDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS concurrency_leases (
			store_id TEXT NOT NULL,
			lease_id TEXT NOT NULL,
			rule_id TEXT NOT NULL,
			rule_version TEXT NOT NULL DEFAULT '',
			namespace TEXT NOT NULL DEFAULT '',
			dimension_key TEXT NOT NULL,
			logical_id TEXT NOT NULL DEFAULT '',
			holder_id TEXT NOT NULL DEFAULT '',
			acquired_at_unix BIGINT NOT NULL,
			renewed_at_unix BIGINT NOT NULL,
			expires_at_unix BIGINT NOT NULL,
			released_at_unix BIGINT NOT NULL DEFAULT 0,
			generation BIGINT NOT NULL,
			state TEXT NOT NULL,
			dimensions_json TEXT NOT NULL,
			PRIMARY KEY (store_id, lease_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_concurrency_leases_capacity
			ON concurrency_leases(store_id, rule_id, dimension_key, state, expires_at_unix)`,
		`CREATE TABLE IF NOT EXISTS concurrency_lease_capacity (
			store_id TEXT NOT NULL,
			rule_id TEXT NOT NULL,
			dimension_key TEXT NOT NULL,
			PRIMARY KEY (store_id, rule_id, dimension_key)
		)`,
	}
}
