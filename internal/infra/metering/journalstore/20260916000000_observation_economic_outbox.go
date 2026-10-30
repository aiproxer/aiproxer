package journalstore

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// ObservationEconomicOutboxMigrationName identifies the durable relay
// outbox. Observation and outbox rows are written by the same local
// transaction, so a crash cannot acknowledge an observation without leaving a
// restartable economic trigger.
const ObservationEconomicOutboxMigrationName = "20260916000000"

const ObservationEconomicOutboxPendingIndexName = "idx_metering_observation_outbox_pending"

func registerObservationEconomicOutboxMigration() {
	migrations.MustRegister(observationEconomicOutboxUp, func(context.Context, *bun.DB) error { return nil })
}

func observationEconomicOutboxUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("metering observation economic outbox schema: nil database")
	}
	switch db.Dialect().Name() {
	case dialect.SQLite:
		return observationEconomicOutboxSQLite(ctx, db)
	case dialect.PG:
		return observationEconomicOutboxPostgres(ctx, db)
	default:
		return fmt.Errorf("metering observation economic outbox schema: unsupported bun dialect %s", db.Dialect().Name().String())
	}
}

func observationEconomicOutboxSQLite(ctx context.Context, db *bun.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS metering_observation_economic_outbox (
			id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			store_id TEXT NOT NULL,
			observation_id TEXT NOT NULL,
			observation_revision INTEGER NOT NULL,
			observation_fingerprint TEXT NOT NULL,
			payload_json TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','processing','delivered')),
			attempt_count INTEGER NOT NULL DEFAULT 0,
			next_attempt_at_unix INTEGER NOT NULL DEFAULT 0,
			lease_owner TEXT NOT NULL DEFAULT '',
			lease_until_unix INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT '',
			created_at_unix INTEGER NOT NULL,
			updated_at_unix INTEGER NOT NULL,
			UNIQUE(store_id, observation_id, observation_revision)
		)`,
		`CREATE INDEX IF NOT EXISTS ` + ObservationEconomicOutboxPendingIndexName + `
			ON metering_observation_economic_outbox(store_id, status, next_attempt_at_unix, lease_until_unix, created_at_unix, id)`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("metering observation economic outbox sqlite: %w", err)
		}
	}
	return nil
}

func observationEconomicOutboxPostgres(ctx context.Context, db *bun.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS metering_observation_economic_outbox (
			id BIGSERIAL PRIMARY KEY,
			store_id TEXT NOT NULL,
			observation_id TEXT NOT NULL,
			observation_revision BIGINT NOT NULL,
			observation_fingerprint TEXT NOT NULL,
			payload_json JSONB NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','processing','delivered')),
			attempt_count BIGINT NOT NULL DEFAULT 0,
			next_attempt_at_unix BIGINT NOT NULL DEFAULT 0,
			lease_owner TEXT NOT NULL DEFAULT '',
			lease_until_unix BIGINT NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT '',
			created_at_unix BIGINT NOT NULL,
			updated_at_unix BIGINT NOT NULL,
			CONSTRAINT metering_observation_economic_outbox_identity_key UNIQUE(store_id, observation_id, observation_revision)
		)`,
		`CREATE INDEX IF NOT EXISTS ` + ObservationEconomicOutboxPendingIndexName + `
			ON metering_observation_economic_outbox(store_id, status, next_attempt_at_unix, lease_until_unix, created_at_unix, id)`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("metering observation economic outbox postgres: %w", err)
		}
	}
	return nil
}
