package journalstore

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// LinkedStatementOrderedIndexMigrationName extends the linked-statement B-leg
// partial index with the ListObservations keyset columns. One index then
// serves both the store-scoped B-leg bound and the deterministic page
// ordering, so SQLite keeps the plan out of a temp B-tree. It replaces the
// interim store_id/b_leg_id-only index recorded by
// LinkedStatementIndexMigrationName. The canonical observation envelope
// remains the sole immutable source; this index is only a query accelerator.
//
// Lock window, for the operator running the cutover. On PostgreSQL this builds
// with a plain CREATE INDEX, which takes a ShareLock on metering_facts and
// blocks INSERT for the duration of the build. That is accepted deliberately:
// the build is fast relative to the append rate this journal sees, the DROP
// below cannot be made concurrent either (see below), and the alternative -
// CREATE INDEX CONCURRENTLY - is unavailable inside this migrator because bun
// runs a registered Go migration on a pooled connection with no surrounding
// transaction to escape from, and CONCURRENTLY is illegal in a transaction
// block. Choosing it would mean either a bespoke non-transactional migration
// path for this one index or accepting a failed CONCURRENTLY build leaving an
// INVALID index behind for VerifySchema to reject.
//
// The DROP/CREATE pair is the reason the interim index is rebuilt twice on a
// database that has not yet run 20260919000000: 20260918000000 builds the
// store_id/b_leg_id shape, this migration drops and rebuilds it with the
// keyset columns, and 20260920000000 adds the separate statement candidate
// index. Only the first two touch the same index name, so a database already
// past 20260919000000 performs one build here, not three. Between the DROP and
// the CREATE the B-leg lookup has no index and falls back to a scan; the
// window is one index build.
const LinkedStatementOrderedIndexMigrationName = "20260919000000"

func registerLinkedStatementOrderedIndexMigration() {
	migrations.MustRegister(linkedStatementOrderedIndexUp, func(context.Context, *bun.DB) error { return nil })
}

func linkedStatementOrderedIndexUp(ctx context.Context, db *bun.DB) error {
	if db == nil {
		return fmt.Errorf("metering linked-statement ordered index schema: nil database")
	}
	switch db.Dialect().Name() {
	case dialect.SQLite, dialect.PG:
		// DROP then CREATE converges both a fresh database and one that already
		// recorded the interim index shape. The migration stays unmarked on
		// failure, so a partial run retries the idempotent pair.
		stmts := []string{
			`DROP INDEX IF EXISTS idx_metering_facts_store_bleg`,
			`CREATE INDEX IF NOT EXISTS idx_metering_facts_store_bleg
				ON metering_facts(store_id, b_leg_id, stream_id, sequence, observation_id, observation_revision, id)
				WHERE payload_kind = 'observation'`,
		}
		for _, stmt := range stmts {
			if _, err := db.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("metering linked-statement ordered index schema: %w", err)
			}
		}
		return nil
	default:
		return fmt.Errorf("metering linked-statement ordered index schema: unsupported bun dialect %s", db.Dialect().Name().String())
	}
}
