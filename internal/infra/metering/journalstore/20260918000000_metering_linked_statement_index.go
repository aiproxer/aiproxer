package journalstore

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// LinkedStatementIndexMigrationName identifies the additive partial index that
// makes correlated B-leg observation lookups (verified statement/correction
// evidence) selective. The canonical observation envelope remains the sole
// immutable source; this index is only a query accelerator.
const LinkedStatementIndexMigrationName = "20260918000000"

const meteringFactsStoreBLegIndex = "idx_metering_facts_store_bleg"

func registerLinkedStatementIndexMigration() {
	migrations.MustRegister(linkedStatementIndexSchemaUp, func(context.Context, *bun.DB) error { return nil })
}

func linkedStatementIndexSchemaUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("metering linked-statement index schema: nil database")
	}
	switch db.Dialect().Name() {
	case dialect.SQLite, dialect.PG:
		if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_metering_facts_store_bleg
			ON metering_facts(store_id, b_leg_id)
			WHERE payload_kind = 'observation'`); err != nil {
			return fmt.Errorf("metering linked-statement index schema: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("metering linked-statement index schema: unsupported bun dialect %s", db.Dialect().Name().String())
	}
}
