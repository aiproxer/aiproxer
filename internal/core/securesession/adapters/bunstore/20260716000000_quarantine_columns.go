package bunstore

import (
	"context"

	"github.com/uptrace/bun"
)

const quarantineColumnsMigrationName = "20260716000000"

func registerSecureSessionQuarantineColumnsMigration() {
	secureSessionMigrations.MustRegister(secureSessionQuarantineColumnsUp, func(ctx context.Context, db *bun.DB) error {
		_ = ctx
		_ = db
		return nil
	})
}

func secureSessionQuarantineColumnsUp(ctx context.Context, db *bun.DB) error {
	return upgradeQuarantineColumns(ctx, db)
}
