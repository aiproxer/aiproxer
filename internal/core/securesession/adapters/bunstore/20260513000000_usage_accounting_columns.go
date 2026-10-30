package bunstore

import (
	"context"

	"github.com/uptrace/bun"
)

const usageAccountingColumnsMigrationName = "20260513000000"

func registerSecureSessionUsageAccountingColumnsMigration() {
	secureSessionMigrations.MustRegister(secureSessionUsageAccountingColumnsUp, func(ctx context.Context, db *bun.DB) error {
		_ = ctx
		_ = db
		return nil
	})
}

func secureSessionUsageAccountingColumnsUp(ctx context.Context, db *bun.DB) error {
	return upgradeUsageAccountingColumns(ctx, db)
}
