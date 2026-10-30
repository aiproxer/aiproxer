package runtimebundle

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/continuity"
	"github.com/aiproxer/aiproxer/internal/core/continuity/bunstore"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/uptrace/bun"
)

// OpenContinuityStore opens a store with a dedicated database handle.
func OpenContinuityStore(ctx context.Context, cfg *config.Config) (b2bua.Store, error) {
	store, _, err := openContinuityStore(ctx, cfg, nil, nil)
	return store, err
}

// openContinuityStore optionally shares its Postgres handle through pools.
func openContinuityStore(ctx context.Context, cfg *config.Config, pools *db.PoolRegistry, migrator *dualPlaneMigrator) (b2bua.Store, func() error, error) {
	if cfg == nil {
		return nil, nil, fmt.Errorf("continuity: nil config")
	}
	if ctx == nil {
		return nil, nil, fmt.Errorf("continuity: nil context")
	}
	cc := cfg.Continuity
	switch config.EffectiveContinuityStore(cc) {
	case "sqlite":
		path := strings.TrimSpace(cc.SQLitePath)
		if path == "" {
			return nil, nil, fmt.Errorf("continuity: sqlite_path is required when store is \"sqlite\"")
		}
		bunDB, err := db.OpenSQLiteBun(ctx, path)
		if err != nil {
			return nil, nil, fmt.Errorf("continuity: open sqlite store: %w", err)
		}
		s, err := bunstore.NewWithContext(ctx, bunDB)
		if err != nil {
			schemaErr := fmt.Errorf("continuity: prepare sqlite schema: %w", err)
			if cerr := bunDB.Close(); cerr != nil {
				return nil, nil, errors.Join(schemaErr, fmt.Errorf("continuity: close db after schema error: %w", cerr))
			}
			return nil, nil, schemaErr
		}
		return s, s.Close, nil
	case "memory":
		s, err := continuity.NewMemoryStoreFromConfig(cc)
		if err != nil {
			return nil, nil, err
		}
		return s, nil, nil
	case "postgres":
		poolCfg, err := config.ParseDatabasePoolSettings(cfg.Database)
		if err != nil {
			return nil, nil, fmt.Errorf("continuity: %w", err)
		}
		pool := db.PoolSettings{
			MaxOpenConns:    poolCfg.MaxOpenConns,
			MaxIdleConns:    poolCfg.MaxIdleConns,
			ConnMaxLifetime: poolCfg.ConnMaxLifetime,
			ConnMaxIdleTime: poolCfg.ConnMaxIdleTime,
		}
		dsn := strings.TrimSpace(cc.PostgresDSN)
		child, cancel := context.WithTimeout(ctx, db.DefaultPostgresOpenMigrateTimeout)
		defer cancel()
		store, closeFn, err := openPostgresStore(child, dsn, pool, cfg.Database, pools, migrator, postgresStoreLifecycle[b2bua.Store]{
			// Migrate/Verify nil: bunstore.NewWithContext owns schema preparation.
			Open: func(ctx context.Context, handle *bun.DB) (b2bua.Store, error) {
				s, err := bunstore.NewWithContext(ctx, handle)
				if err != nil {
					return nil, fmt.Errorf("continuity: prepare postgres schema: %w", err)
				}
				return s, nil
			},
		})
		if err != nil {
			return nil, nil, fmt.Errorf("continuity: open postgres store: %w", err)
		}
		return store, closeFn, nil
	default:
		s := strings.TrimSpace(cc.Store)
		if s == "" {
			s = "(empty)"
		}
		return nil, nil, fmt.Errorf("continuity: store %q is not supported (supported: memory, sqlite, postgres)", s)
	}
}
