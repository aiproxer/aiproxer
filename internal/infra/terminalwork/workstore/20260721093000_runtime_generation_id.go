package workstore

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

const RuntimeGenerationIDMigrationName = "20260721093000"

func registerRuntimeGenerationIDMigration() {
	migrations.MustRegister(runtimeGenerationIDUp, func(context.Context, *bun.DB) error { return nil })
}

func runtimeGenerationIDUp(ctx context.Context, db *bun.DB) error {
	var stmts []string
	switch db.Dialect().Name() {
	case dialect.SQLite:
		stmts = []string{
			`ALTER TABLE economic_terminal_work ADD COLUMN runtime_generation_id TEXT NOT NULL DEFAULT ''`,
		}
	case dialect.PG:
		stmts = []string{
			`ALTER TABLE economic_terminal_work ADD COLUMN IF NOT EXISTS runtime_generation_id TEXT NOT NULL DEFAULT ''`,
		}
	default:
		return fmt.Errorf("unsupported bun dialect %s", db.Dialect().Name().String())
	}
	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			// SQLite lacks IF NOT EXISTS for ADD COLUMN; treat duplicate as success.
			if db.Dialect().Name() == dialect.SQLite && isDuplicateColumnErr(err) {
				continue
			}
			return fmt.Errorf("terminal work runtime_generation_id: %w", err)
		}
	}
	return nil
}
