package journalstore

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// PresenceBooleanRepairMigrationName identifies the forward repair converting
// legacy INTEGER presence flags to logical BOOLEAN on PostgreSQL and
// ensuring the observation-projection indexes exist on both dialects.
// SQLite keeps its dialect-native INTEGER representation for booleans.
const PresenceBooleanRepairMigrationName = "20260917000000"

func registerPresenceBooleanRepairMigration() {
	migrations.MustRegister(presenceBooleanRepairSchemaUp, func(context.Context, *bun.DB) error { return nil })
}

func presenceBooleanRepairSchemaUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("metering presence boolean repair schema: nil database")
	}
	switch db.Dialect().Name() {
	case dialect.SQLite:
		return presenceBooleanRepairSQLite(ctx, db)
	case dialect.PG:
		return presenceBooleanRepairPostgres(ctx, db)
	default:
		return fmt.Errorf("metering presence boolean repair schema: unsupported bun dialect %s", db.Dialect().Name().String())
	}
}

func presenceBooleanRepairSQLite(ctx context.Context, db *bun.DB) error {
	// SQLite uses INTEGER for logical booleans; only missing indexes need repair.
	for _, stmt := range presenceBooleanRepairSQLiteIndexDDL() {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("metering presence boolean repair sqlite index: %w", err)
		}
	}
	return nil
}

func presenceBooleanRepairSQLiteIndexDDL() []string {
	return []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS metering_facts_store_observation_revision_key
			ON metering_facts(store_id, observation_id, observation_revision)
			WHERE payload_kind = 'observation' AND observation_id != ''`,
		`CREATE INDEX IF NOT EXISTS idx_metering_components_store_subject
			ON metering_components(store_id, subject_kind, subject_id, stream_id, sequence, observation_id, observation_revision, item_kind, item_id)`,
		`CREATE INDEX IF NOT EXISTS idx_metering_components_store_component
			ON metering_components(store_id, component_key_hash, component_key, subject_kind, subject_id, observation_id, observation_revision, item_id)`,
		`CREATE INDEX IF NOT EXISTS idx_metering_components_store_provider_account
			ON metering_components(store_id, provider_account_key, stream_id, sequence, observation_id, observation_revision, item_kind, item_id)`,
		`CREATE INDEX IF NOT EXISTS idx_metering_components_observation
			ON metering_components(observation_row_id, item_kind, item_id)`,
	}
}

func presenceBooleanRepairPostgres(ctx context.Context, db *bun.DB) error {
	// Validate legacy values before conversion: only 0/1 integers or
	// boolean text representations are accepted. Any other value fails
	// the upgrade without partial conversion.
	for _, column := range []string{"value_present", "money_present"} {
		var invalid int
		if err := db.NewRaw(
			`SELECT COUNT(1) FROM metering_components WHERE `+column+`::text NOT IN ('0','1','true','false','t','f','TRUE','FALSE','True','False')`,
		).Scan(ctx, &invalid); err != nil {
			return fmt.Errorf("metering presence boolean repair validate %s: %w", column, err)
		}
		if invalid > 0 {
			return fmt.Errorf("metering presence boolean repair validate %s: %d rows with non-boolean values", column, invalid)
		}
	}
	for _, stmt := range presenceBooleanRepairPostgresDDL() {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("metering presence boolean repair postgres DDL: %w", err)
		}
	}
	return nil
}

func presenceBooleanRepairPostgresDDL() []string {
	var stmts []string
	for _, column := range []string{"value_present", "money_present"} {
		stmts = append(stmts, "ALTER TABLE IF EXISTS metering_components ALTER COLUMN "+column+" DROP DEFAULT")
	}
	for _, column := range []string{"value_present", "money_present"} {
		stmts = append(stmts, "ALTER TABLE IF EXISTS metering_components ALTER COLUMN "+column+" TYPE BOOLEAN USING ("+column+"::text IN ('1','true','t','TRUE','True'))")
	}
	for _, column := range []string{"value_present", "money_present"} {
		stmts = append(stmts, "ALTER TABLE IF EXISTS metering_components ALTER COLUMN "+column+" SET DEFAULT FALSE")
	}
	stmts = append(stmts,
		`CREATE UNIQUE INDEX IF NOT EXISTS metering_facts_store_observation_revision_key
			ON metering_facts(store_id, observation_id, observation_revision)
			WHERE payload_kind = 'observation' AND observation_id <> ''`,
		`CREATE INDEX IF NOT EXISTS idx_metering_components_store_subject
			ON metering_components(store_id, subject_kind, subject_id, stream_id, sequence, observation_id, observation_revision, item_kind, item_id)`,
		`CREATE INDEX IF NOT EXISTS idx_metering_components_store_component
			ON metering_components(store_id, component_key_hash, component_key, subject_kind, subject_id, observation_id, observation_revision, item_id)`,
		`CREATE INDEX IF NOT EXISTS idx_metering_components_store_provider_account
			ON metering_components(store_id, provider_account_key, stream_id, sequence, observation_id, observation_revision, item_kind, item_id)`,
		`CREATE INDEX IF NOT EXISTS idx_metering_components_observation
			ON metering_components(observation_row_id, item_kind, item_id)`,
	)
	return stmts
}
