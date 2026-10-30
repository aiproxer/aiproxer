package bunstore

import (
	"context"

	"github.com/uptrace/bun"
)

const attemptOpenFailedMigrationName = "20261022000000"

func registerSecureSessionAttemptOpenFailedMigration() {
	secureSessionMigrations.MustRegister(secureSessionAttemptOpenFailedUp, func(ctx context.Context, db *bun.DB) error {
		_ = ctx
		_ = db
		return nil
	})
}

func secureSessionAttemptOpenFailedUp(ctx context.Context, db *bun.DB) error {
	return upgradeAttemptOpenFailedColumn(ctx, db)
}
