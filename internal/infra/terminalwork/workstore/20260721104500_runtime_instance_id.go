package workstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

const RuntimeInstanceIDMigrationName = "20260721104500"

func registerRuntimeInstanceIDMigration() {
	migrations.MustRegister(runtimeInstanceIDUp, func(context.Context, *bun.DB) error { return nil })
}

func runtimeInstanceIDUp(ctx context.Context, db *bun.DB) error {
	var stmts []string
	switch db.Dialect().Name() {
	case dialect.SQLite:
		stmts = []string{
			`ALTER TABLE economic_terminal_work ADD COLUMN runtime_instance_id TEXT NOT NULL DEFAULT ''`,
		}
	case dialect.PG:
		stmts = []string{
			`ALTER TABLE economic_terminal_work ADD COLUMN IF NOT EXISTS runtime_instance_id TEXT NOT NULL DEFAULT ''`,
		}
	default:
		return fmt.Errorf("unsupported bun dialect %s", db.Dialect().Name().String())
	}
	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			if db.Dialect().Name() == dialect.SQLite && isDuplicateColumnErr(err) {
				continue
			}
			return fmt.Errorf("terminal work runtime_instance_id: %w", err)
		}
	}
	return nil
}

func isDuplicateColumnErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate column") || strings.Contains(msg, "already exists")
}
