package workstore

import (
	"context"
	"fmt"
	"sync"

	"github.com/aiproxer/aiproxer/internal/bunmigrate"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
	"github.com/uptrace/bun/migrate"
)

const BaselineMigrationName = "20260718153000"

var (
	migrations             = migrate.NewMigrations()
	registerMigrationsOnce sync.Once
)

func registerMigrations() {
	registerMigrationsOnce.Do(func() {
		migrations.MustRegister(baselineUp, func(context.Context, *bun.DB) error { return nil })
		registerRuntimeGenerationIDMigration()
		registerRuntimeInstanceIDMigration()
	})
}

func runSchemaMigrate(ctx context.Context, db *bun.DB) error {
	registerMigrations()
	if err := bunmigrate.Migrate(ctx, db, "bun_terminal_work_migrations", migrations); err != nil {
		return fmt.Errorf("workstore: %w", err)
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
			return fmt.Errorf("terminal work baseline: %w", err)
		}
	}
	return nil
}

func sqliteDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS economic_terminal_work (
			store_id TEXT NOT NULL,
			work_id TEXT NOT NULL,
			source_key TEXT NOT NULL,
			identity_version INTEGER NOT NULL,
			payload_version INTEGER NOT NULL,
			kind TEXT NOT NULL,
			state TEXT NOT NULL,
			provider_id TEXT NOT NULL DEFAULT '',
			request_id TEXT NOT NULL DEFAULT '',
			attempt_id TEXT NOT NULL DEFAULT '',
			trace_id TEXT NOT NULL DEFAULT '',
			generation_id TEXT NOT NULL DEFAULT '',
			runtime_instance_id TEXT NOT NULL DEFAULT '',
			runtime_generation_id TEXT NOT NULL DEFAULT '',
			bound_provider_id TEXT NOT NULL DEFAULT '',
			rating_id TEXT NOT NULL DEFAULT '',
			fact_id TEXT NOT NULL DEFAULT '',
			lease_set_id TEXT NOT NULL DEFAULT '',
			payload_json TEXT NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0,
			next_retry_at_unix INTEGER NOT NULL DEFAULT 0,
			claim_owner_id TEXT NOT NULL DEFAULT '',
			claim_expires_at_unix INTEGER NOT NULL DEFAULT 0,
			error_code TEXT NOT NULL DEFAULT '',
			error_permanent INTEGER NOT NULL DEFAULT 0,
			error_message TEXT NOT NULL DEFAULT '',
			created_at_unix INTEGER NOT NULL,
			updated_at_unix INTEGER NOT NULL,
			PRIMARY KEY (store_id, work_id),
			UNIQUE (store_id, source_key)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_terminal_work_due
			ON economic_terminal_work(store_id, state, next_retry_at_unix, claim_expires_at_unix)`,
		`CREATE INDEX IF NOT EXISTS idx_terminal_work_provider
			ON economic_terminal_work(store_id, provider_id, state)`,
		`CREATE INDEX IF NOT EXISTS idx_terminal_work_request
			ON economic_terminal_work(store_id, request_id) WHERE request_id != ''`,
	}
}

func postgresDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS economic_terminal_work (
			store_id TEXT NOT NULL,
			work_id TEXT NOT NULL,
			source_key TEXT NOT NULL,
			identity_version BIGINT NOT NULL,
			payload_version BIGINT NOT NULL,
			kind TEXT NOT NULL,
			state TEXT NOT NULL,
			provider_id TEXT NOT NULL DEFAULT '',
			request_id TEXT NOT NULL DEFAULT '',
			attempt_id TEXT NOT NULL DEFAULT '',
			trace_id TEXT NOT NULL DEFAULT '',
			generation_id TEXT NOT NULL DEFAULT '',
			runtime_instance_id TEXT NOT NULL DEFAULT '',
			runtime_generation_id TEXT NOT NULL DEFAULT '',
			bound_provider_id TEXT NOT NULL DEFAULT '',
			rating_id TEXT NOT NULL DEFAULT '',
			fact_id TEXT NOT NULL DEFAULT '',
			lease_set_id TEXT NOT NULL DEFAULT '',
			payload_json TEXT NOT NULL,
			attempts BIGINT NOT NULL DEFAULT 0,
			next_retry_at_unix BIGINT NOT NULL DEFAULT 0,
			claim_owner_id TEXT NOT NULL DEFAULT '',
			claim_expires_at_unix BIGINT NOT NULL DEFAULT 0,
			error_code TEXT NOT NULL DEFAULT '',
			error_permanent BOOLEAN NOT NULL DEFAULT FALSE,
			error_message TEXT NOT NULL DEFAULT '',
			created_at_unix BIGINT NOT NULL,
			updated_at_unix BIGINT NOT NULL,
			PRIMARY KEY (store_id, work_id),
			UNIQUE (store_id, source_key)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_terminal_work_due
			ON economic_terminal_work(store_id, state, next_retry_at_unix, claim_expires_at_unix)`,
		`CREATE INDEX IF NOT EXISTS idx_terminal_work_provider
			ON economic_terminal_work(store_id, provider_id, state)`,
		`CREATE INDEX IF NOT EXISTS idx_terminal_work_request
			ON economic_terminal_work(store_id, request_id) WHERE request_id != ''`,
	}
}
