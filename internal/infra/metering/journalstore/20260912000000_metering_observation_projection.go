package journalstore

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// ObservationProjectionMigrationName identifies the additive V2 observation
// projection migration. It extends metering_facts; it does not introduce a
// second authoritative observation journal.
const ObservationProjectionMigrationName = "20260912000000"

// ObservationProjectionVersion is bumped only when the deterministic
// component projection shape changes.
const ObservationProjectionVersion = 1

const (
	meteringObservationIndex = "metering_facts_store_observation_revision_key"
	meteringFactsStoreID     = "metering_facts_store_id_key"
	meteringComponentSubject = "idx_metering_components_store_subject"
	meteringComponentKey     = "idx_metering_components_store_component"
	meteringComponentObserv  = "idx_metering_components_observation"
)

func registerObservationProjectionMigration() {
	migrations.MustRegister(observationProjectionSchemaUp, func(context.Context, *bun.DB) error { return nil })
}

func observationProjectionSchemaUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("metering observation projection schema: nil database")
	}
	switch db.Dialect().Name() {
	case dialect.SQLite:
		return observationProjectionSQLite(ctx, db)
	case dialect.PG:
		return observationProjectionPostgres(ctx, db)
	default:
		return fmt.Errorf("metering observation projection schema: unsupported bun dialect %s", db.Dialect().Name().String())
	}
}

func observationProjectionSQLite(ctx context.Context, db *bun.DB) error {
	columns := []struct {
		name string
		ddl  string
	}{
		{"payload_kind", `ALTER TABLE metering_facts ADD COLUMN payload_kind TEXT NOT NULL DEFAULT 'fact'`},
		{"observation_id", `ALTER TABLE metering_facts ADD COLUMN observation_id TEXT NOT NULL DEFAULT ''`},
		{"observation_revision", `ALTER TABLE metering_facts ADD COLUMN observation_revision INTEGER NOT NULL DEFAULT 0`},
		{"observation_fingerprint", `ALTER TABLE metering_facts ADD COLUMN observation_fingerprint TEXT NOT NULL DEFAULT ''`},
		{"observation_subject_kind", `ALTER TABLE metering_facts ADD COLUMN observation_subject_kind TEXT NOT NULL DEFAULT ''`},
		{"observation_subject_id", `ALTER TABLE metering_facts ADD COLUMN observation_subject_id TEXT NOT NULL DEFAULT ''`},
		{"observation_tenant_id", `ALTER TABLE metering_facts ADD COLUMN observation_tenant_id TEXT NOT NULL DEFAULT ''`},
		{"observation_origin", `ALTER TABLE metering_facts ADD COLUMN observation_origin TEXT NOT NULL DEFAULT ''`},
		{"observation_acquisition", `ALTER TABLE metering_facts ADD COLUMN observation_acquisition TEXT NOT NULL DEFAULT ''`},
		{"observation_provider_account_key", `ALTER TABLE metering_facts ADD COLUMN observation_provider_account_key TEXT NOT NULL DEFAULT ''`},
	}
	for _, column := range columns {
		if err := sqliteAddColumnIfMissing(ctx, db, "metering_facts", column.name, column.ddl); err != nil {
			return fmt.Errorf("metering observation projection sqlite: %w", err)
		}
	}
	statements := []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS metering_facts_store_id_key
			ON metering_facts(store_id, id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS metering_facts_store_observation_revision_key
			ON metering_facts(store_id, observation_id, observation_revision)
			WHERE payload_kind = 'observation' AND observation_id != ''`,
		`CREATE TABLE IF NOT EXISTS metering_components (
			id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			store_id TEXT NOT NULL,
			observation_row_id INTEGER NOT NULL,
			observation_id TEXT NOT NULL,
			observation_revision INTEGER NOT NULL,
			observation_fingerprint TEXT NOT NULL,
			item_kind TEXT NOT NULL CHECK (item_kind IN ('measure','reported_charge')),
			item_id TEXT NOT NULL,
			component_key TEXT NOT NULL DEFAULT '',
			component_key_hash TEXT NOT NULL DEFAULT '',
			coefficient TEXT NOT NULL DEFAULT '',
			scale INTEGER NOT NULL DEFAULT 0,
			value_present INTEGER NOT NULL DEFAULT 0,
			money_present INTEGER NOT NULL DEFAULT 0,
			currency TEXT NOT NULL DEFAULT '',
			charge_coverage_json TEXT NOT NULL DEFAULT '[]',
			subject_kind TEXT NOT NULL,
			subject_id TEXT NOT NULL,
			tenant_id TEXT NOT NULL DEFAULT '',
			provider_account_key TEXT NOT NULL DEFAULT '',
			stream_id TEXT NOT NULL,
			sequence INTEGER NOT NULL,
			origin TEXT NOT NULL,
			acquisition TEXT NOT NULL,
			authority TEXT NOT NULL,
			projection_version INTEGER NOT NULL DEFAULT 1,
			UNIQUE(observation_row_id, item_kind, item_id),
			FOREIGN KEY(store_id, observation_row_id) REFERENCES metering_facts(store_id, id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_metering_components_store_subject
			ON metering_components(store_id, subject_kind, subject_id, stream_id, sequence, observation_id, observation_revision, item_kind, item_id)`,
		`CREATE INDEX IF NOT EXISTS idx_metering_components_store_component
			ON metering_components(store_id, component_key_hash, component_key, subject_kind, subject_id, observation_id, observation_revision, item_id)`,
		`CREATE INDEX IF NOT EXISTS idx_metering_components_store_provider_account
			ON metering_components(store_id, provider_account_key, stream_id, sequence, observation_id, observation_revision, item_kind, item_id)`,
		`CREATE INDEX IF NOT EXISTS idx_metering_components_observation
			ON metering_components(observation_row_id, item_kind, item_id)`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("metering observation projection sqlite DDL: %w", err)
		}
	}
	return nil
}

func observationProjectionPostgres(ctx context.Context, db *bun.DB) error {
	statements := []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS metering_facts_store_id_key
			ON metering_facts(store_id, id)`,
		`ALTER TABLE metering_facts ADD COLUMN IF NOT EXISTS payload_kind TEXT NOT NULL DEFAULT 'fact'`,
		`ALTER TABLE metering_facts ADD COLUMN IF NOT EXISTS observation_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE metering_facts ADD COLUMN IF NOT EXISTS observation_revision BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE metering_facts ADD COLUMN IF NOT EXISTS observation_fingerprint TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE metering_facts ADD COLUMN IF NOT EXISTS observation_subject_kind TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE metering_facts ADD COLUMN IF NOT EXISTS observation_subject_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE metering_facts ADD COLUMN IF NOT EXISTS observation_tenant_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE metering_facts ADD COLUMN IF NOT EXISTS observation_origin TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE metering_facts ADD COLUMN IF NOT EXISTS observation_acquisition TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE metering_facts ADD COLUMN IF NOT EXISTS observation_provider_account_key TEXT NOT NULL DEFAULT ''`,
		`CREATE UNIQUE INDEX IF NOT EXISTS metering_facts_store_observation_revision_key
			ON metering_facts(store_id, observation_id, observation_revision)
			WHERE payload_kind = 'observation' AND observation_id <> ''`,
		`CREATE TABLE IF NOT EXISTS metering_components (
			id BIGSERIAL PRIMARY KEY,
			store_id TEXT NOT NULL,
			observation_row_id BIGINT NOT NULL,
			observation_id TEXT NOT NULL,
			observation_revision BIGINT NOT NULL,
			observation_fingerprint TEXT NOT NULL,
			item_kind TEXT NOT NULL CHECK (item_kind IN ('measure','reported_charge')),
			item_id TEXT NOT NULL,
			component_key TEXT NOT NULL DEFAULT '',
			component_key_hash TEXT NOT NULL DEFAULT '',
			coefficient TEXT NOT NULL DEFAULT '',
			scale INTEGER NOT NULL DEFAULT 0,
			value_present BOOLEAN NOT NULL DEFAULT FALSE,
			money_present BOOLEAN NOT NULL DEFAULT FALSE,
			currency TEXT NOT NULL DEFAULT '',
			charge_coverage_json TEXT NOT NULL DEFAULT '[]',
			subject_kind TEXT NOT NULL,
			subject_id TEXT NOT NULL,
			tenant_id TEXT NOT NULL DEFAULT '',
			provider_account_key TEXT NOT NULL DEFAULT '',
			stream_id TEXT NOT NULL,
			sequence BIGINT NOT NULL,
			origin TEXT NOT NULL,
			acquisition TEXT NOT NULL,
			authority TEXT NOT NULL,
			projection_version INTEGER NOT NULL DEFAULT 1,
			UNIQUE(observation_row_id, item_kind, item_id),
			FOREIGN KEY(store_id, observation_row_id) REFERENCES metering_facts(store_id, id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_metering_components_store_subject
			ON metering_components(store_id, subject_kind, subject_id, stream_id, sequence, observation_id, observation_revision, item_kind, item_id)`,
		`CREATE INDEX IF NOT EXISTS idx_metering_components_store_component
			ON metering_components(store_id, component_key_hash, component_key, subject_kind, subject_id, observation_id, observation_revision, item_id)`,
		`CREATE INDEX IF NOT EXISTS idx_metering_components_store_provider_account
			ON metering_components(store_id, provider_account_key, stream_id, sequence, observation_id, observation_revision, item_kind, item_id)`,
		`CREATE INDEX IF NOT EXISTS idx_metering_components_observation
			ON metering_components(observation_row_id, item_kind, item_id)`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("metering observation projection postgres DDL: %w", err)
		}
	}
	// Older development builds briefly used INTEGER for presence flags. Keep
	// the additive migration repairable when such a table already exists while
	// converging fresh PostgreSQL schemas on the logical BOOLEAN shape.
	for _, statement := range []string{
		`ALTER TABLE IF EXISTS metering_components ALTER COLUMN value_present TYPE BOOLEAN USING (value_present::text IN ('1', 'true', 't'))`,
		`ALTER TABLE IF EXISTS metering_components ALTER COLUMN money_present TYPE BOOLEAN USING (money_present::text IN ('1', 'true', 't'))`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("metering observation projection postgres presence repair: %w", err)
		}
	}
	return nil
}
