package proxymemorystore

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
)

// SettingsMigrationName is the version prefix of the proxy memory settings
// migration. It matches the file name so the archtest database-parity
// discovery and the bun_proxy_memory_migrations history agree.
//
// The settings table arrives in its own migration rather than in the
// baseline: a database that already applied the baseline must still receive
// the table, and an applied migration is never replayed.
const SettingsMigrationName = "20261022000000"

// registerSettingsMigration registers the settings migration with the
// family's migration registry. The down migration is a no-op: the settings
// rows carry no history that a rollback must preserve.
func registerSettingsMigration() {
	migrations.MustRegister(settingsUp, func(context.Context, *bun.DB) error { return nil })
}

// settingsUp creates the owner-scoped settings table of design §4.
//
// The row holds the owner's own unprojected per-field overrides. The key is
// the tenant and the principal plus the phase: there is no project column,
// so the settings are global for that owner and apply to every project of
// that owner.
func settingsUp(ctx context.Context, db *bun.DB) error {
	for _, stmt := range settingsDDL() {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("proxy memory settings migration: %w", err)
		}
	}
	return nil
}
