package runtimebundle

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/dbmigrate"
	"github.com/uptrace/bun"
)

// adminPostgresPoolSettings caps the short-lived DDL/admin handle so startup
// never mirrors a large (or unlimited) runtime max_open_conns.
var adminPostgresPoolSettings = db.PoolSettings{MaxOpenConns: 1, MaxIdleConns: 1}

// openPostgresBun opens a Postgres bun handle. Tests may override.
var openPostgresBun = db.OpenPostgresBun

// closePostgresBun closes an admin bun handle. Tests may override to inject close failures.
var closePostgresBun = func(handle *bun.DB) error {
	if handle == nil {
		return nil
	}
	return handle.Close()
}

func migratePostgresAdmin(
	ctx context.Context,
	dsn string,
	migrate func(context.Context, *bun.DB) error,
) error {
	admin, err := openPostgresBun(ctx, dsn, adminPostgresPoolSettings)
	if err != nil {
		return fmt.Errorf("open admin postgres: %w", err)
	}
	migrateErr := migrate(ctx, admin)
	closeErr := closePostgresBun(admin)
	if migrateErr != nil {
		migrateErr = fmt.Errorf("migrate postgres schema: %w", migrateErr)
	}
	if closeErr != nil {
		closeErr = fmt.Errorf("close admin postgres: %w", closeErr)
	}
	return errors.Join(migrateErr, closeErr)
}

// dualPlaneMigrator runs one capped admin migrate pass per distinct dual-plane
// DSN the first time a registry-owned store for that DSN opens under auto_migrate.
type dualPlaneMigrator struct {
	cfg  *config.Config
	mu   sync.Mutex
	done map[string]struct{}
	// lookupComponent resolves each dual-plane component's migration entry
	// point. Instance-scoped so callers substitute on their own migrator instead
	// of a process-global.
	lookupComponent dbmigrate.ComponentLookup
}

func newDualPlaneMigrator(cfg *config.Config) *dualPlaneMigrator {
	return &dualPlaneMigrator{
		cfg:             cfg,
		done:            make(map[string]struct{}),
		lookupComponent: dbmigrate.LookupPostgresComponent,
	}
}

// Ensure migrates every enabled dual-plane component that shares dsn's sanitized
// identity. Subsequent Ensure calls for the same key are no-ops.
func (m *dualPlaneMigrator) Ensure(ctx context.Context, dsn string) error {
	if m == nil || m.cfg == nil {
		return nil
	}
	if m.cfg.Database.EffectiveSchemaMode() != config.DatabaseSchemaModeAutoMigrate {
		return nil
	}
	key, err := db.SanitizePostgresDSN(dsn)
	if err != nil {
		return fmt.Errorf("sanitize postgres dsn: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.done[key]; ok {
		return nil
	}
	components, err := dualPlaneComponentsForDSN(m.cfg, key)
	if err != nil {
		return err
	}
	if len(components) == 0 {
		m.done[key] = struct{}{}
		return nil
	}
	if err := migratePostgresAdmin(ctx, dsn, func(ctx context.Context, database *bun.DB) error {
		return dbmigrate.MigrateComponentsWithLookup(ctx, database, components, m.lookupComponent)
	}); err != nil {
		return err
	}
	m.done[key] = struct{}{}
	return nil
}

func dualPlaneComponentsForDSN(cfg *config.Config, sanitizedKey string) ([]string, error) {
	type pending struct {
		dsn       string
		component string
	}
	var items []pending
	if cfg.Accounting.Concurrency.Enabled && storeIsPostgres(cfg.Accounting.Concurrency.Store) {
		items = append(items, pending{dsn: strings.TrimSpace(cfg.Accounting.Concurrency.PostgresDSN), component: dbmigrate.ComponentConcurrency})
	}
	if cfg.Metering.Enabled && storeIsPostgres(cfg.Metering.Journal.Store) {
		items = append(items, pending{dsn: strings.TrimSpace(cfg.Metering.Journal.PostgresDSN), component: dbmigrate.ComponentMetering})
	}
	out := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if item.dsn == "" {
			continue
		}
		key, err := db.SanitizePostgresDSN(item.dsn)
		if err != nil {
			return nil, fmt.Errorf("dual-plane postgres migrate: %w", err)
		}
		if key != sanitizedKey {
			continue
		}
		if _, ok := seen[item.component]; ok {
			continue
		}
		seen[item.component] = struct{}{}
		out = append(out, item.component)
	}
	return out, nil
}

func storeIsPostgres(store string) bool {
	return strings.EqualFold(strings.TrimSpace(store), "postgres")
}
