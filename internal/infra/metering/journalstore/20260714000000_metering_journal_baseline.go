package journalstore

import (
	"context"
	"fmt"
	"sync"

	"github.com/aiproxer/aiproxer/internal/bunmigrate"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
	"github.com/uptrace/bun/migrate"
)

const BaselineMigrationName = "20260714000000"

var (
	migrations             = migrate.NewMigrations()
	registerMigrationsOnce sync.Once
)

func registerMigrations() {
	registerMigrationsOnce.Do(func() {
		migrations.MustRegister(baselineUp, func(context.Context, *bun.DB) error { return nil })
		registerStoreScopedSourceKeyMigration()
		registerStoreScopedFiltersMigration()
		registerSchemaV2Migration()
		registerObservationProjectionMigration()
		registerAccountWindowProjectionMigration()
		registerObservationEconomicOutboxMigration()
		registerPresenceBooleanRepairMigration()
		registerLinkedStatementIndexMigration()
		registerLinkedStatementOrderedIndexMigration()
		registerLinkedStatementCandidateIndexMigration()
	})
}

func runSchemaMigrate(ctx context.Context, db *bun.DB) error {
	registerMigrations()
	if err := bunmigrate.Migrate(ctx, db, "bun_metering_journal_migrations", migrations); err != nil {
		return fmt.Errorf("journalstore: %w", err)
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
			return fmt.Errorf("metering journal baseline: %w", err)
		}
	}
	return nil
}

func sqliteDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS metering_facts (
			id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			store_id TEXT NOT NULL,
			fact_id TEXT NOT NULL,
			stream_id TEXT NOT NULL,
			sequence INTEGER NOT NULL,
			source_event_key TEXT NOT NULL,
			fact_kind TEXT NOT NULL,
			perspective TEXT NOT NULL,
			boundary TEXT NOT NULL,
			lifecycle_scope TEXT NOT NULL,
			request_id TEXT NOT NULL DEFAULT '',
			a_leg_id TEXT NOT NULL DEFAULT '',
			b_leg_id TEXT NOT NULL DEFAULT '',
			attempt_id TEXT NOT NULL DEFAULT '',
			frontend_id TEXT NOT NULL DEFAULT '',
			backend_id TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			presence TEXT NOT NULL DEFAULT '',
			source TEXT NOT NULL DEFAULT '',
			authority TEXT NOT NULL DEFAULT '',
			recorded_at_unix INTEGER NOT NULL,
			payload_json TEXT NOT NULL,
			UNIQUE(store_id, source_event_key)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_metering_facts_stream_seq ON metering_facts(stream_id, sequence)`,
		`CREATE INDEX IF NOT EXISTS idx_metering_facts_request ON metering_facts(request_id) WHERE request_id != ''`,
		`CREATE TABLE IF NOT EXISTS metering_fact_filters (
			id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			store_id TEXT NOT NULL,
			fact_id TEXT NOT NULL,
			stream_id TEXT NOT NULL,
			field_name TEXT NOT NULL,
			field_value TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_metering_fact_filters_field
			ON metering_fact_filters(store_id, field_name, field_value, stream_id)`,
	}
}

func postgresDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS metering_facts (
			id BIGSERIAL PRIMARY KEY,
			store_id TEXT NOT NULL,
			fact_id TEXT NOT NULL,
			stream_id TEXT NOT NULL,
			sequence BIGINT NOT NULL,
			source_event_key TEXT NOT NULL,
			fact_kind TEXT NOT NULL,
			perspective TEXT NOT NULL,
			boundary TEXT NOT NULL,
			lifecycle_scope TEXT NOT NULL,
			request_id TEXT NOT NULL DEFAULT '',
			a_leg_id TEXT NOT NULL DEFAULT '',
			b_leg_id TEXT NOT NULL DEFAULT '',
			attempt_id TEXT NOT NULL DEFAULT '',
			frontend_id TEXT NOT NULL DEFAULT '',
			backend_id TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			presence TEXT NOT NULL DEFAULT '',
			source TEXT NOT NULL DEFAULT '',
			authority TEXT NOT NULL DEFAULT '',
			recorded_at_unix BIGINT NOT NULL,
			payload_json TEXT NOT NULL,
			CONSTRAINT metering_facts_store_source_event_key_key UNIQUE (store_id, source_event_key)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_metering_facts_stream_seq ON metering_facts(stream_id, sequence)`,
		`CREATE INDEX IF NOT EXISTS idx_metering_facts_request ON metering_facts(request_id) WHERE request_id <> ''`,
		`CREATE TABLE IF NOT EXISTS metering_fact_filters (
			id BIGSERIAL PRIMARY KEY,
			store_id TEXT NOT NULL,
			fact_id TEXT NOT NULL,
			stream_id TEXT NOT NULL,
			field_name TEXT NOT NULL,
			field_value TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_metering_fact_filters_field
			ON metering_fact_filters(store_id, field_name, field_value, stream_id)`,
	}
}
