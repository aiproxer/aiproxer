package proxymemorystore

import (
	"context"
	"fmt"
	"sync"

	"github.com/aiproxer/aiproxer/internal/bunmigrate"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/migrate"
)

// BaselineMigrationName is the version prefix of the proxy memory baseline
// migration. It matches the file name so the archtest database-parity
// discovery and the bun_proxy_memory_migrations history agree.
const BaselineMigrationName = "20261016000000"

var (
	migrations             = migrate.NewMigrations()
	registerMigrationsOnce sync.Once
)

func registerMigrations() {
	registerMigrationsOnce.Do(func() {
		// Bun derives each migration name from the caller's file, so each
		// migration registers from its own file.
		migrations.MustRegister(baselineUp, func(context.Context, *bun.DB) error { return nil })
		registerSettingsMigration()
		registerSettingsContextMigration()
		registerPhaseFailuresMigration()
	})
}

// runSchemaMigrate applies the registered proxy memory migrations through the
// store-correct bunmigrate runner (a failed upgrade stays retryable).
func runSchemaMigrate(ctx context.Context, db *bun.DB) error {
	registerMigrations()
	if err := bunmigrate.Migrate(ctx, db, "bun_proxy_memory_migrations", migrations); err != nil {
		return fmt.Errorf("proxymemorystore: %w", err)
	}
	return nil
}

// baselineUp creates the proxy memory tables and indexes of design §12.1.
func baselineUp(ctx context.Context, db *bun.DB) error {
	for _, stmt := range baselineDDL() {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("proxy memory baseline: %w", err)
		}
	}
	return nil
}
