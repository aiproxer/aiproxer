package bunmigrate

import (
	"context"
	"errors"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/migrate"
)

// errFailingMigrationUp is returned by the failing migration registered through
// registerFailingMigration while the configured predicate wants a failure.
var errFailingMigrationUp = errors.New("failing migration up")

// registerFailingMigration registers a versioned migration whose up function
// fails with errFailingMigrationUp unless succeed is true. The file name gives
// it the Bun migration name "20260101000001".
func registerFailingMigration(migrations *migrate.Migrations, succeed func() bool) {
	migrations.MustRegister(func(context.Context, *bun.DB) error {
		if succeed() {
			return nil
		}
		return errFailingMigrationUp
	}, func(context.Context, *bun.DB) error { return nil })
}
