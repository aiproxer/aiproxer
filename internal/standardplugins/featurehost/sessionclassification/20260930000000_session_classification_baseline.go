package sessionclassification

import (
	"context"
	"fmt"

	"github.com/aiproxer/aiproxer/internal/bunmigrate"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
	"github.com/uptrace/bun/migrate"
)

const sessionClassificationMigrationHistoryTable = "bun_session_classification_migrations"

// sessionClassificationSchemaMigrations constructs the feature-owned versioned
// history explicitly during EnsureSchema; package initialization performs no
// migration registration or database work.
func sessionClassificationSchemaMigrations() *migrate.Migrations {
	migrations := migrate.NewMigrations()
	migrations.MustRegister(sessionClassificationBaselineUp, func(context.Context, *bun.DB) error {
		return nil
	})
	return migrations
}

func runSessionClassificationSchemaMigrations(ctx context.Context, db *bun.DB) error {
	return bunmigrate.Migrate(ctx, db, sessionClassificationMigrationHistoryTable, sessionClassificationSchemaMigrations())
}

func sessionClassificationBaselineUp(ctx context.Context, db *bun.DB) error {
	if ctx == nil {
		return fmt.Errorf("session classification migration: nil context")
	}
	if db == nil || db.Dialect() == nil {
		return fmt.Errorf("session classification migration: nil database")
	}
	var statement string
	switch db.Dialect().Name() {
	case dialect.SQLite:
		statement = `CREATE TABLE IF NOT EXISTS session_classification (
			scope_kind TEXT NOT NULL,
			scope_id TEXT NOT NULL,
			kind TEXT NOT NULL,
			source TEXT NOT NULL,
			confidence TEXT NOT NULL,
			evidence_code TEXT NOT NULL,
			classification_revision BIGINT NOT NULL,
			remote_attempts INTEGER NOT NULL,
			remote_lease_id TEXT NOT NULL,
			remote_lease_until TIMESTAMP,
			remote_next_eligible_at TIMESTAMP,
			updated_at TIMESTAMP NOT NULL,
			PRIMARY KEY (scope_kind, scope_id)
		)`
	case dialect.PG:
		statement = `CREATE TABLE IF NOT EXISTS session_classification (
			scope_kind TEXT NOT NULL,
			scope_id TEXT NOT NULL,
			kind TEXT NOT NULL,
			source TEXT NOT NULL,
			confidence TEXT NOT NULL,
			evidence_code TEXT NOT NULL,
			classification_revision BIGINT NOT NULL,
			remote_attempts INTEGER NOT NULL,
			remote_lease_id TEXT NOT NULL,
			remote_lease_until TIMESTAMPTZ,
			remote_next_eligible_at TIMESTAMPTZ,
			updated_at TIMESTAMPTZ NOT NULL,
			PRIMARY KEY (scope_kind, scope_id)
		)`
	default:
		return fmt.Errorf("session classification migration: unsupported dialect %q", db.Dialect().Name())
	}
	if _, err := db.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("session classification baseline migration failed")
	}
	return nil
}
