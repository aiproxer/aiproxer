package bunstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// InterleavedStateMigrationName is the bun/migrate name for the interleaved
// thinking state column addition (file prefix).
const InterleavedStateMigrationName = "20250427000000"

// registerInterleavedStateMigration registers the interleaved_state_json column
// migration. It must be called from within the shared continuity migrations
// once so the migration is registered exactly once; MustRegister derives the
// migration name from this file's timestamp prefix.
func registerInterleavedStateMigration() {
	continuityMigrations.MustRegister(interleavedStateMigrationUp, func(ctx context.Context, db *bun.DB) error {
		_ = ctx
		_ = db
		return nil
	})
}

// interleavedStateMigrationUp adds the bounded JSON column used to persist
// thinker cycle state and memo references on a_legs. It is idempotent: an
// existing column (e.g. on a re-run or a DB where baseline already created it)
// is tolerated. Empty state is stored as the column default ”.
func interleavedStateMigrationUp(ctx context.Context, db *bun.DB) error {
	var stmt string
	switch db.Dialect().Name() {
	case dialect.SQLite, dialect.PG:
		stmt = `ALTER TABLE a_legs ADD COLUMN interleaved_state_json TEXT NOT NULL DEFAULT ''`
	default:
		return fmt.Errorf("bunstore: unsupported bun dialect %s", db.Dialect().Name().String())
	}
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		if !isDuplicateColumnErr(err) {
			return fmt.Errorf("bunstore: interleaved state migrate: %w", err)
		}
	}
	return nil
}

func isDuplicateColumnErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "interleaved_state_json") {
		return false
	}
	if strings.Contains(msg, "duplicate column name") {
		return true
	}
	return strings.Contains(msg, "column ") && strings.Contains(msg, "already exists")
}
