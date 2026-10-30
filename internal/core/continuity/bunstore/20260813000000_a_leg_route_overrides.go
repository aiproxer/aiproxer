package bunstore

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// RouteOverrideMigrationName is the bun/migrate name for the A-leg route-override
// table (file prefix).
const RouteOverrideMigrationName = "20260813000000"

func registerRouteOverrideMigration() {
	continuityMigrations.MustRegister(routeOverrideMigrationUp, func(ctx context.Context, db *bun.DB) error {
		_ = ctx
		_ = db
		return nil
	})
}

func routeOverrideMigrationUp(ctx context.Context, db *bun.DB) error {
	var stmt string
	switch db.Dialect().Name() {
	case dialect.SQLite:
		stmt = `CREATE TABLE IF NOT EXISTS a_leg_route_overrides (
			a_leg_id TEXT NOT NULL PRIMARY KEY,
			active INTEGER NOT NULL,
			selector TEXT NOT NULL,
			revision INTEGER NOT NULL,
			updated_at_unix INTEGER NOT NULL,
			FOREIGN KEY(a_leg_id) REFERENCES a_legs(a_leg_id) ON DELETE CASCADE
		)`
	case dialect.PG:
		stmt = `CREATE TABLE IF NOT EXISTS a_leg_route_overrides (
			a_leg_id TEXT NOT NULL PRIMARY KEY,
			active INTEGER NOT NULL,
			selector TEXT NOT NULL,
			revision BIGINT NOT NULL,
			updated_at_unix BIGINT NOT NULL,
			FOREIGN KEY(a_leg_id) REFERENCES a_legs(a_leg_id) ON DELETE CASCADE
		)`
	default:
		return fmt.Errorf("bunstore: unsupported bun dialect %s", db.Dialect().Name().String())
	}
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("bunstore: route override migrate: %w", err)
	}
	return nil
}
