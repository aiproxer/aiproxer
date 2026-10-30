package bunstore

import (
	"context"
	"fmt"
	"sync"

	"github.com/aiproxer/aiproxer/internal/bunmigrate"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
	"github.com/uptrace/bun/migrate"
)

// BaselineMigrationName is the name bun/migrate records for this adapter's baseline (file prefix).
const BaselineMigrationName = "20250426000000"

// continuityMigrations holds versioned DDL for this adapter.
var continuityMigrations = migrate.NewMigrations()

var registerContinuityMigrationsOnce sync.Once

func registerContinuityBaselineMigration() {
	registerContinuityMigrationsOnce.Do(func() {
		continuityMigrations.MustRegister(continuityBaselineUp, func(ctx context.Context, db *bun.DB) error {
			_ = ctx
			_ = db
			return nil
		})
		registerInterleavedStateMigration()
		registerRouteOverrideMigration()
		registerConversationViewMigration()
		registerConversationBootstrapMigration()
	})
}

func continuityBaselineUp(ctx context.Context, db *bun.DB) error {
	var stmts []string
	switch db.Dialect().Name() {
	case dialect.SQLite:
		stmts = sqliteContinuityDDL()
	case dialect.PG:
		stmts = postgresContinuityDDL()
	default:
		return fmt.Errorf("bunstore: unsupported bun dialect %s", db.Dialect().Name().String())
	}
	for _, q := range stmts {
		if _, err := db.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("bunstore: continuity baseline: %w", err)
		}
	}
	return nil
}

func runContinuitySchemaMigrate(ctx context.Context, db *bun.DB) error {
	registerContinuityBaselineMigration()
	if err := bunmigrate.Migrate(ctx, db, "bun_continuity_migrations", continuityMigrations); err != nil {
		return fmt.Errorf("bunstore: %w", err)
	}
	return nil
}

// RunSchemaMigrate runs the schema migrations for continuity against the provided DB.
func RunSchemaMigrate(ctx context.Context, db *bun.DB) error {
	return runContinuitySchemaMigrate(ctx, db)
}

func sqliteContinuityDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS a_legs (
			a_leg_id TEXT NOT NULL PRIMARY KEY,
			continuity_key TEXT NOT NULL DEFAULT '',
			created_at_unix INTEGER NOT NULL,
			last_seen_at_unix INTEGER NOT NULL,
			weighted_first_consumed INTEGER NOT NULL DEFAULT 0,
			next_b_seq INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_a_legs_continuity
			ON a_legs(continuity_key) WHERE continuity_key != ''`,
		`CREATE TABLE IF NOT EXISTS b_legs (
			a_leg_id TEXT NOT NULL,
			seq INTEGER NOT NULL,
			b_leg_id TEXT NOT NULL,
			PRIMARY KEY(a_leg_id, seq),
			FOREIGN KEY(a_leg_id) REFERENCES a_legs(a_leg_id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS attempts (
			a_leg_id TEXT NOT NULL,
			seq INTEGER NOT NULL,
			b_leg_id TEXT NOT NULL,
			backend_id TEXT NOT NULL,
			effective_model TEXT NOT NULL,
			started_at_unix INTEGER NOT NULL,
			finished_at_unix INTEGER NOT NULL,
			outcome TEXT NOT NULL,
			reason TEXT NOT NULL DEFAULT '',
			PRIMARY KEY(a_leg_id, seq),
			FOREIGN KEY(a_leg_id) REFERENCES a_legs(a_leg_id) ON DELETE CASCADE
		)`,
	}
}

func postgresContinuityDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS a_legs (
			a_leg_id TEXT NOT NULL PRIMARY KEY,
			continuity_key TEXT NOT NULL DEFAULT '',
			created_at_unix BIGINT NOT NULL,
			last_seen_at_unix BIGINT NOT NULL,
			weighted_first_consumed INTEGER NOT NULL DEFAULT 0,
			next_b_seq INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_a_legs_continuity
			ON a_legs(continuity_key) WHERE continuity_key <> ''`,
		`CREATE TABLE IF NOT EXISTS b_legs (
			a_leg_id TEXT NOT NULL,
			seq INTEGER NOT NULL,
			b_leg_id TEXT NOT NULL,
			PRIMARY KEY(a_leg_id, seq),
			FOREIGN KEY(a_leg_id) REFERENCES a_legs(a_leg_id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS attempts (
			a_leg_id TEXT NOT NULL,
			seq INTEGER NOT NULL,
			b_leg_id TEXT NOT NULL,
			backend_id TEXT NOT NULL,
			effective_model TEXT NOT NULL,
			started_at_unix BIGINT NOT NULL,
			finished_at_unix BIGINT NOT NULL,
			outcome TEXT NOT NULL,
			reason TEXT NOT NULL DEFAULT '',
			PRIMARY KEY(a_leg_id, seq),
			FOREIGN KEY(a_leg_id) REFERENCES a_legs(a_leg_id) ON DELETE CASCADE
		)`,
	}
}
