package journalstore

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// LinkedStatementCandidateIndexMigrationName identifies the additive partial
// index that makes the economic relay's linked-statement lookup selective on
// verified importer provenance. Without it a single B-leg's unrelated usage
// observations are enumerated as candidates; with it only verified
// statement-line rows for the trusted store + B-leg are visited, and its
// trailing keyset columns still serve the ListObservations ORDER BY. The
// canonical observation envelope remains the sole immutable source; this index
// is only a query accelerator.
const LinkedStatementCandidateIndexMigrationName = "20260920000000"

const meteringFactsStoreBLegStatementIndex = "idx_metering_facts_store_bleg_statement"

func registerLinkedStatementCandidateIndexMigration() {
	migrations.MustRegister(linkedStatementCandidateIndexUp, func(context.Context, *bun.DB) error { return nil })
}

func linkedStatementCandidateIndexUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("metering linked-statement candidate index schema: nil database")
	}
	switch db.Dialect().Name() {
	case dialect.SQLite, dialect.PG:
		if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_metering_facts_store_bleg_statement
			ON metering_facts(store_id, b_leg_id, stream_id, sequence, observation_id, observation_revision, id)
			WHERE payload_kind = 'observation'
				AND observation_subject_kind = 'statement_line'
				AND observation_origin = 'statement'
				AND observation_acquisition = 'statement_importer'
				AND authority = 'verified_statement'`); err != nil {
			return fmt.Errorf("metering linked-statement candidate index schema: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("metering linked-statement candidate index schema: unsupported bun dialect %s", db.Dialect().Name().String())
	}
}
