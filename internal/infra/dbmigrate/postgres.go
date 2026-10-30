package dbmigrate

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/internal/infra/concurrencyauthority/leasestore"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/uptrace/bun"
)

func PostgresComponents(ctx context.Context, dsn string, components []string) error {
	database, err := db.OpenPostgresBun(ctx, dsn, db.PoolSettings{MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		return fmt.Errorf("open admin postgres: %w", err)
	}
	migrateErr := migrateAndVerifyComponents(ctx, database, components, LookupPostgresComponent)
	closeErr := database.Close()
	if migrateErr != nil {
		migrateErr = fmt.Errorf("postgres components: %w", migrateErr)
	}
	if closeErr != nil {
		closeErr = fmt.Errorf("close admin postgres: %w", closeErr)
	}
	return errors.Join(migrateErr, closeErr)
}

// The open-source migration catalog holds the components whose schema the
// open-source distribution owns. The usage-authority component moved to the
// Enterprise tree with task 6.1 Slice A: the Enterprise composition migrates
// and verifies that schema itself. The open-source CLI has no Enterprise
// branch, so `aipstd migrate --components usage-authority` now fails closed
// with `unknown component "usage-authority"`. The billing component left the
// same way with task 6.3: `aipstd-enterprise migrate` applies the billing
// store schema, and `aipstd migrate --components billing` fails closed.
const (
	ComponentConcurrency = "concurrency"
	ComponentMetering    = "metering"
)

type postgresComponentDefinition struct {
	name    string
	migrate func(context.Context, *bun.DB) error
	verify  func(context.Context, *bun.DB) error
}

var postgresComponentCatalog = []postgresComponentDefinition{
	{name: ComponentConcurrency, migrate: leasestore.Migrate, verify: leasestore.VerifySchema},
	{name: ComponentMetering, migrate: journalstore.Migrate, verify: journalstore.VerifySchema},
}

// ComponentLookup resolves a component name to its migrate and verify
// functions. Callers inject an explicit resolver; the built-in catalog is
// LookupPostgresComponent.
type ComponentLookup func(component string) (migrate func(context.Context, *bun.DB) error, verify func(context.Context, *bun.DB) error, err error)

// LookupPostgresComponent resolves the migrate/verify functions for a
// dual-plane component from the built-in catalog. It is a pure function, so
// callers may hold it without introducing shared mutable state.
func LookupPostgresComponent(component string) (migrate func(context.Context, *bun.DB) error, verify func(context.Context, *bun.DB) error, err error) {
	return postgresComponent(component)
}

func migrateAndVerifyComponents(ctx context.Context, database *bun.DB, components []string, lookup ComponentLookup) error {
	for _, component := range components {
		migrate, verify, err := lookup(component)
		if err != nil {
			return err
		}
		if err := migrate(ctx, database); err != nil {
			return fmt.Errorf("%s migration failed: %w", component, err)
		}
		if err := verify(ctx, database); err != nil {
			return fmt.Errorf("%s verification failed: %w", component, err)
		}
	}
	return nil
}

// MigrateComponents runs DDL migrations for the named dual-plane components on
// an already-open admin handle, resolving them from the built-in catalog.
// Verification stays with the caller (CLI verify pass or runtime OpenStore
// readiness).
func MigrateComponents(ctx context.Context, database *bun.DB, components []string) error {
	return MigrateComponentsWithLookup(ctx, database, components, LookupPostgresComponent)
}

// MigrateComponentsWithLookup runs DDL migrations for the named dual-plane
// components on an already-open admin handle, resolving each component through
// lookup instead of the built-in catalog. Verification stays with the caller.
func MigrateComponentsWithLookup(ctx context.Context, database *bun.DB, components []string, lookup ComponentLookup) error {
	for _, component := range components {
		migrate, _, err := lookup(component)
		if err != nil {
			return err
		}
		if err := migrate(ctx, database); err != nil {
			return fmt.Errorf("%s migration failed: %w", component, err)
		}
	}
	return nil
}

func ParseComponents(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		names := make([]string, 0, len(postgresComponentCatalog))
		for _, component := range postgresComponentCatalog {
			names = append(names, component.name)
		}
		raw = strings.Join(names, ",")
	}
	seen := make(map[string]struct{})
	components := make([]string, 0, 3)
	for part := range strings.SplitSeq(raw, ",") {
		component := strings.TrimSpace(part)
		if _, _, err := postgresComponent(component); err != nil {
			return nil, err
		}
		if _, ok := seen[component]; ok {
			continue
		}
		seen[component] = struct{}{}
		components = append(components, component)
	}
	return components, nil
}

func postgresComponent(component string) (func(context.Context, *bun.DB) error, func(context.Context, *bun.DB) error, error) {
	for _, definition := range postgresComponentCatalog {
		if definition.name == component {
			return definition.migrate, definition.verify, nil
		}
	}
	return nil, nil, fmt.Errorf("unknown component %q", component)
}
