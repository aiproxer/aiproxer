package journalstore

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

const StoreScopedSourceKeyMigrationName = "20260718000000"

func registerStoreScopedSourceKeyMigration() {
	migrations.MustRegister(storeScopedSourceKeyUp, func(context.Context, *bun.DB) error { return nil })
}

func storeScopedSourceKeyUp(ctx context.Context, db *bun.DB) error {
	switch db.Dialect().Name() {
	case dialect.PG:
		for _, stmt := range postgresStoreScopedSourceKeyStmts() {
			if _, err := db.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("store-scoped source key migration: %w", err)
			}
		}
		return nil
	case dialect.SQLite:
		return rebuildSQLiteStoreScopedUnique(ctx, db)
	default:
		return fmt.Errorf("unsupported bun dialect %s", db.Dialect().Name().String())
	}
}

func postgresStoreScopedSourceKeyStmts() []string {
	return []string{
		`ALTER TABLE metering_facts DROP CONSTRAINT IF EXISTS metering_facts_source_event_key_key`,
		`DO $$ BEGIN
				ALTER TABLE metering_facts
					ADD CONSTRAINT metering_facts_store_source_event_key_key UNIQUE (store_id, source_event_key);
			EXCEPTION
				WHEN duplicate_object THEN NULL;
				WHEN duplicate_table THEN NULL;
				WHEN unique_violation THEN NULL;
			END $$`,
	}
}

func rebuildSQLiteStoreScopedUnique(ctx context.Context, db *bun.DB) error {
	var legacy int
	err := db.NewRaw(`
SELECT COUNT(1) FROM sqlite_master
WHERE type = 'index'
  AND tbl_name = 'metering_facts'
  AND sql LIKE '%UNIQUE%source_event_key%'
  AND sql NOT LIKE '%store_id%source_event_key%'`).Scan(ctx, &legacy)
	if err != nil {
		// Also detect table-level UNIQUE(source_event_key) via index auto-name.
		legacy = 0
	}
	var tableSQL string
	_ = db.NewRaw(`SELECT sql FROM sqlite_master WHERE type='table' AND name='metering_facts'`).Scan(ctx, &tableSQL)
	if tableSQL == "" {
		return nil
	}
	if containsStoreScopedUnique(tableSQL) && legacy == 0 {
		return nil
	}
	stmts := []string{
		`ALTER TABLE metering_facts RENAME TO metering_facts_legacy_v1`,
		`CREATE TABLE metering_facts (
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
		`INSERT INTO metering_facts(
			id, store_id, fact_id, stream_id, sequence, source_event_key, fact_kind,
			perspective, boundary, lifecycle_scope, request_id, a_leg_id, b_leg_id, attempt_id,
			frontend_id, backend_id, model, presence, source, authority, recorded_at_unix, payload_json
		)
		SELECT
			id, store_id, fact_id, stream_id, sequence, source_event_key, fact_kind,
			perspective, boundary, lifecycle_scope, request_id, a_leg_id, b_leg_id, attempt_id,
			frontend_id, backend_id, model, presence, source, authority, recorded_at_unix, payload_json
		FROM metering_facts_legacy_v1`,
		`DROP TABLE metering_facts_legacy_v1`,
		`CREATE INDEX IF NOT EXISTS idx_metering_facts_stream_seq ON metering_facts(stream_id, sequence)`,
		`CREATE INDEX IF NOT EXISTS idx_metering_facts_request ON metering_facts(request_id) WHERE request_id != ''`,
	}
	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("sqlite store-scoped rebuild: %w", err)
		}
	}
	return nil
}

func containsStoreScopedUnique(tableSQL string) bool {
	return len(tableSQL) > 0 &&
		(containsAll(tableSQL, "UNIQUE(store_id, source_event_key)") ||
			containsAll(tableSQL, "UNIQUE (store_id, source_event_key)"))
}

func containsAll(s string, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		(len(s) > 0 && indexFold(s, sub) >= 0))
}

func indexFold(s, sub string) int {
	// case-sensitive is enough for our DDL literals
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
