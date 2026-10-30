package runtimebundle

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	concurrencyapp "github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/app"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/infra/concurrencyauthority/configsource"
	"github.com/aiproxer/aiproxer/internal/infra/concurrencyauthority/leasestore"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/uptrace/bun"
	_ "modernc.org/sqlite" // register sqlite driver for configured lease stores
)

type concurrencyAuthorityRuntime struct {
	Provider             authority.ConcurrencyProvider
	Service              *concurrencyapp.Service
	LeaseTTL             time.Duration
	RenewBefore          time.Duration
	AuxiliaryLeasePolicy string
}

//nolint:revive // owner is the resource owner parameter
func buildConcurrencyAuthorityRuntime(owner *processResourceOwner, parent context.Context, cfg *config.Config, testing TestingOptions, registry *db.PoolRegistry, migrator *dualPlaneMigrator) (*concurrencyAuthorityRuntime, error) {
	if cfg == nil || !cfg.Accounting.Concurrency.Enabled {
		return nil, nil
	}
	if owner == nil {
		return nil, fmt.Errorf("runtimebundle: nil process owner")
	}
	if parent == nil {
		parent = context.Background()
	}
	src, err := configsource.New(cfg.Accounting.Concurrency)
	if err != nil {
		return nil, err
	}
	leaseTTL, err := cfg.Accounting.Concurrency.LeaseTTLDuration()
	if err != nil {
		return nil, err
	}
	renewBefore, err := cfg.Accounting.Concurrency.RenewBeforeDuration()
	if err != nil {
		return nil, err
	}
	store, err := buildConcurrencyLeaseStore(owner, parent, cfg, testing, registry, migrator)
	if err != nil {
		return nil, err
	}
	clock := concurrencyClockFromTesting(testing)
	svc := concurrencyapp.NewService(src, store, clock)
	return &concurrencyAuthorityRuntime{
		Provider:             concurrencyapp.NewProvider(svc),
		Service:              svc,
		LeaseTTL:             leaseTTL,
		RenewBefore:          renewBefore,
		AuxiliaryLeasePolicy: strings.TrimSpace(cfg.Accounting.Concurrency.AuxiliaryLeasePolicy),
	}, nil
}

//nolint:revive // owner is the resource owner parameter
func buildConcurrencyLeaseStore(owner *processResourceOwner, parent context.Context, cfg *config.Config, testing TestingOptions, registry *db.PoolRegistry, migrator *dualPlaneMigrator) (concurrencyapp.LeaseStore, error) {
	if override := testing.ConcurrencyLeaseStoreOverride; override != nil {
		return override, nil
	}
	if owner == nil {
		return nil, fmt.Errorf("runtimebundle: nil process owner")
	}
	cCfg := cfg.Accounting.Concurrency
	storeID := strings.TrimSpace(cCfg.StoreID)
	if storeID == "" {
		storeID = "default"
	}
	switch strings.ToLower(strings.TrimSpace(cCfg.Store)) {
	case "", "memory":
		return leasestore.NewMemory(leasestore.MemoryConfig{StoreID: storeID}), nil
	case "sqlite":
		path := strings.TrimSpace(cCfg.SQLitePath)
		dsn := "file:" + filepath.ToSlash(path) + "?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_txlock=immediate"
		sqlDB, err := sql.Open("sqlite", dsn)
		if err != nil {
			return nil, fmt.Errorf("runtimebundle: concurrency lease sqlite open: %w", err)
		}
		sqlDB.SetMaxOpenConns(1)
		if err := sqlDB.PingContext(parent); err != nil {
			_ = sqlDB.Close()
			return nil, fmt.Errorf("runtimebundle: concurrency lease sqlite ping: %w", err)
		}
		bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
		if err != nil {
			_ = sqlDB.Close()
			return nil, fmt.Errorf("runtimebundle: concurrency lease sqlite bun: %w", err)
		}
		store, err := leasestore.NewDurable(parent, bunDB, leasestore.DurableConfig{StoreID: storeID})
		if err != nil {
			_ = bunDB.Close()
			return nil, fmt.Errorf("runtimebundle: concurrency lease durable sqlite: %w", err)
		}
		owner.Own(store.Close)
		return store, nil
	case "postgres":
		poolCfg, err := config.ParseDatabasePoolSettings(cfg.Database)
		if err != nil {
			return nil, fmt.Errorf("runtimebundle: concurrency lease postgres pool: %w", err)
		}
		child, cancel := context.WithTimeout(parent, db.DefaultPostgresOpenMigrateTimeout)
		defer cancel()
		pool := db.PoolSettings{
			MaxOpenConns:    poolCfg.MaxOpenConns,
			MaxIdleConns:    poolCfg.MaxIdleConns,
			ConnMaxLifetime: poolCfg.ConnMaxLifetime,
			ConnMaxIdleTime: poolCfg.ConnMaxIdleTime,
		}
		store, err := acquireOwnedProcess(child, owner, func(ctx context.Context) (*leasestore.DurableStore, func() error, error) {
			return openPostgresStore(ctx, cCfg.PostgresDSN, pool, cfg.Database, registry, migrator, postgresStoreLifecycle[*leasestore.DurableStore]{
				Migrate: leasestore.Migrate, Verify: leasestore.VerifySchema,
				Open: func(ctx context.Context, handle *bun.DB) (*leasestore.DurableStore, error) {
					return leasestore.OpenStore(ctx, handle, leasestore.DurableConfig{StoreID: storeID})
				},
				Close: (*leasestore.DurableStore).Close,
			})
		})
		if err != nil {
			return nil, fmt.Errorf("runtimebundle: concurrency lease durable postgres: %w", err)
		}
		return store, nil
	default:
		return nil, fmt.Errorf("runtimebundle: concurrency lease store %q is invalid", cCfg.Store)
	}
}

func concurrencyClockFromTesting(testing TestingOptions) concurrencyapp.Clock {
	if testing.Clock == nil {
		return nil
	}
	return concurrencyClockFunc(testing.Clock)
}

type concurrencyClockFunc func() time.Time

func (f concurrencyClockFunc) Now() time.Time { return f() }

func attachConcurrencyToAccounting(rt *runtime.AccountingRuntime, conc *concurrencyAuthorityRuntime) {
	if rt == nil || conc == nil {
		return
	}
	rt.ConcurrencyProvider = conc.Provider
	rt.ConcurrencyLeaseTTL = conc.LeaseTTL
	rt.ConcurrencyRenewBefore = conc.RenewBefore
	rt.ConcurrencyAuxiliaryLeasePolicy = conc.AuxiliaryLeasePolicy
}
