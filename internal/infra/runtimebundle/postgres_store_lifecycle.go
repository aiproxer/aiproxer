package runtimebundle

import (
	"context"
	"errors"
	"fmt"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/uptrace/bun"
)

// postgresStoreLifecycle splits a postgres-backed store open into phases.
// Migrate and Verify may be nil when the Open constructor owns schema
// preparation itself (self-migrating store constructors).
type postgresStoreLifecycle[T any] struct {
	Migrate func(context.Context, *bun.DB) error
	Verify  func(context.Context, *bun.DB) error
	Open    func(context.Context, *bun.DB) (T, error)
	Close   func(T) error
}

func joinCloseErr(primary, closeErr error) error {
	if closeErr == nil {
		return primary
	}
	return errors.Join(primary, fmt.Errorf("close postgres: %w", closeErr))
}

func closeJoin(bunDB *bun.DB, primary error) error {
	if bunDB == nil {
		return primary
	}
	return joinCloseErr(primary, bunDB.Close())
}

func openPostgresStore[T any](
	ctx context.Context,
	dsn string,
	pool db.PoolSettings,
	database config.DatabaseConfig,
	registry *db.PoolRegistry,
	migrator *dualPlaneMigrator,
	lifecycle postgresStoreLifecycle[T],
) (T, func() error, error) {
	var zero T
	registryOwned := registry != nil
	if database.EffectiveConnectionMode() == config.DatabaseConnectionModeTransactionPool {
		if err := db.ValidateTransactionPoolDSN(dsn); err != nil {
			return zero, nil, err
		}
	}
	// Registry-owned auto_migrate: one capped admin pass per DSN via migrator.
	// Non-registry (owning) paths still migrate on the handle below.
	if registryOwned && database.EffectiveSchemaMode() == config.DatabaseSchemaModeAutoMigrate {
		if err := migrator.Ensure(ctx, dsn); err != nil {
			return zero, nil, err
		}
	}
	var bunDB *bun.DB
	var err error
	if registryOwned {
		bunDB, err = registry.Open(ctx, dsn, pool)
	} else {
		bunDB, err = openPostgresBun(ctx, dsn, pool)
	}
	if err != nil {
		return zero, nil, err
	}
	if !registryOwned && database.EffectiveSchemaMode() == config.DatabaseSchemaModeAutoMigrate && lifecycle.Migrate != nil {
		if err := lifecycle.Migrate(ctx, bunDB); err != nil {
			return zero, nil, closeJoin(bunDB, fmt.Errorf("migrate postgres schema: %w", err))
		}
	}
	if lifecycle.Verify != nil {
		if err := lifecycle.Verify(ctx, bunDB); err != nil {
			if !registryOwned {
				return zero, nil, closeJoin(bunDB, fmt.Errorf("verify schema: %w", err))
			}
			return zero, nil, fmt.Errorf("verify schema: %w", err)
		}
	}
	store, err := lifecycle.Open(ctx, bunDB)
	if err != nil {
		if !registryOwned {
			return zero, nil, closeJoin(bunDB, err)
		}
		return zero, nil, err
	}
	if registryOwned {
		if err := registry.Claim(bunDB); err != nil {
			claimErr := fmt.Errorf("claim postgres pool: %w", err)
			if lifecycle.Close != nil {
				if cerr := lifecycle.Close(store); cerr != nil {
					claimErr = errors.Join(claimErr, fmt.Errorf("close store after claim failure: %w", cerr))
				}
			}
			return zero, nil, claimErr
		}
		return store, func() error { return nil }, nil
	}
	return store, bunDB.Close, nil
}
