// Package bunmigrate runs Bun schema migrations with store-correct history
// semantics.
//
// It exists because migration option construction is easy to get wrong: Bun's
// migrate.Migrator records a migration as applied BEFORE running its up
// function unless migrate.WithMarkAppliedOnSuccess(true) is passed. A failed
// or interrupted migration is then recorded as applied and silently skipped on
// every later startup, leaving a half-built schema. Centralizing the options
// here makes that behavior the only path: every store records an upgrade only
// after its up function succeeds, so a failed upgrade stays retryable on the
// next open.
//
// The package is a leaf (Bun only, no internal dependencies) shared by store
// adapters under internal/core and internal/infra.
package bunmigrate

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/migrate"
)

// ErrNilMigrations reports a nil migration registry.
var ErrNilMigrations = errors.New("bunmigrate: nil migrations")

// Migrate initializes the migration history table named table and applies the
// unapplied migrations in migrations, recording each one only after its up
// function succeeds (migrate.WithMarkAppliedOnSuccess(true)).
//
// The history table name stays per-store; pass each store's existing migration
// table name. Errors name the table and wrap init and apply failures so callers
// keep their own package-prefixed wrapping.
func Migrate(ctx context.Context, db *bun.DB, table string, migrations *migrate.Migrations) error {
	if ctx == nil {
		return errors.New("bunmigrate: nil context")
	}
	if db == nil {
		return errors.New("bunmigrate: nil bun db")
	}
	if strings.TrimSpace(table) == "" {
		return errors.New("bunmigrate: empty migration table name")
	}
	if migrations == nil {
		return ErrNilMigrations
	}
	migrator := migrate.NewMigrator(db, migrations,
		migrate.WithTableName(table),
		migrate.WithMarkAppliedOnSuccess(true),
	)
	if err := migrator.Init(ctx); err != nil {
		return fmt.Errorf("migrate %s: init: %w", table, err)
	}
	if _, err := migrator.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate %s: migrate: %w", table, err)
	}
	return nil
}
