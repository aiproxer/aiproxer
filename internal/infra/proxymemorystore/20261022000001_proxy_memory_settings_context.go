package proxymemorystore

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
)

// SettingsContextMigrationName is the version prefix of the additive migration
// that adds the trusted write-time dependency context to the owner settings
// table. It matches the file name so the archtest database-parity discovery
// and the bun_proxy_memory_migrations history agree.
//
// The context arrives in its own migration rather than in the settings table's
// creation: a database that already applied the settings migration must still
// receive the columns, and an applied migration is never replayed. The added
// columns default to "no context", so a legacy row keeps its raw overrides and
// never gets a history the deployment did not observe.
const SettingsContextMigrationName = "20261022000001"

// registerSettingsContextMigration registers the additive settings-context
// migration with the family's migration registry. The down migration is a
// no-op: the columns carry no history a rollback must preserve, and the
// settings migration's own down migration is a no-op as well.
func registerSettingsContextMigration() {
	migrations.MustRegister(settingsContextUp, func(context.Context, *bun.DB) error { return nil })
}

// settingsContextUp adds the write-time dependency context to an existing
// settings table.
//
// Every statement is an additive ALTER with a default, which both engines
// apply identically and without rewriting the owner's raw values. The
// migration runner executes the statements in one transaction, so a failed
// upgrade leaves the table and its migration history unchanged and retryable.
func settingsContextUp(ctx context.Context, db *bun.DB) error {
	// The bun migration runner executes a Go migration function outside a
	// transaction, so the statements are wrapped here: a failure part-way
	// leaves the table and the migration history unchanged and retryable
	// instead of a half-added column set.
	return db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		for _, stmt := range settingsContextDDL() {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("proxy memory settings context migration: %w", err)
			}
		}
		return nil
	})
}

// settingsContextDDL returns the additive context statements of design §4.
//
// dependency_present distinguishes a trusted context from a legacy row: the
// booleans alone cannot, because a row whose owner named the whole target has
// no inherited dimension. The backend and model name the effective target the
// accepted effort was checked against; the inherited booleans say which
// dimensions the owner left to the operator.
func settingsContextDDL() []string {
	return []string{
		`ALTER TABLE proxy_memory_settings ADD COLUMN dependency_present INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE proxy_memory_settings ADD COLUMN dependency_backend TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE proxy_memory_settings ADD COLUMN dependency_model TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE proxy_memory_settings ADD COLUMN inherited_backend INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE proxy_memory_settings ADD COLUMN inherited_model INTEGER NOT NULL DEFAULT 0`,
	}
}
