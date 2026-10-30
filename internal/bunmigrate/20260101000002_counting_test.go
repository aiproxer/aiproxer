package bunmigrate

import (
	"context"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/migrate"
)

// registerCountingMigration registers a versioned migration whose up function
// invokes onUp every time it runs. The file name gives it the Bun migration
// name "20260101000002".
func registerCountingMigration(migrations *migrate.Migrations, onUp func()) {
	migrations.MustRegister(func(context.Context, *bun.DB) error {
		onUp()
		return nil
	}, func(context.Context, *bun.DB) error { return nil })
}
