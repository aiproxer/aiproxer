//go:build integration

package bunstore

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/routeoverride"
	"github.com/aiproxer/aiproxer/internal/core/routeoverride/storecontract"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/testkit"
)

func TestPostgresStoreImplementsRouteOverrideStore(t *testing.T) {
	runtimeDSN := testkit.SkipUnlessPostgres(t)
	adminDSN, ok := testkit.PostgresAdminDSN()
	if !ok {
		adminDSN = runtimeDSN
	}
	ctx, cancel := context.WithTimeout(context.Background(), db.DefaultPostgresOpenMigrateTimeout)
	defer cancel()
	poolCfg, err := config.ParseDatabasePoolSettings(config.DatabaseConfig{MaxOpenConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	pool := db.PoolSettings{
		MaxOpenConns:    poolCfg.MaxOpenConns,
		MaxIdleConns:    poolCfg.MaxIdleConns,
		ConnMaxLifetime: poolCfg.ConnMaxLifetime,
		ConnMaxIdleTime: poolCfg.ConnMaxIdleTime,
	}

	migrateDB, err := db.OpenPostgresBun(ctx, adminDSN, pool)
	if err != nil {
		t.Fatal(err)
	}
	migrated, err := NewWithContext(ctx, migrateDB)
	if err != nil {
		_ = migrateDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = migrated.Close() })
	if _, ok := routeoverride.AsStore(migrated); !ok {
		t.Fatal("continuity/bunstore PostgreSQL Store does not implement routeoverride.Store")
	}

	storecontract.RunAll(t, storecontract.ContractEnv{
		New: func(t *testing.T) storecontract.ContractPair {
			t.Helper()
			openCtx, openCancel := context.WithTimeout(context.Background(), db.DefaultPostgresOpenMigrateTimeout)
			defer openCancel()
			bunDB, err := db.OpenPostgresBun(openCtx, runtimeDSN, pool)
			if err != nil {
				t.Fatal(err)
			}
			s := &Store{db: bunDB}
			t.Cleanup(func() { _ = s.Close() })
			return storecontract.ContractPair{Override: s, Legs: s}
		},
		SeedRevision:    seedBunRevision,
		PeekLastSeenAt:  peekBunLastSeenAt,
		AdvanceClock:    advanceBunLastSeen,
		SeedStoredState: seedBunStoredState,
		Spawn:           func(fn func()) { go fn() },
	})
}

func TestRouteOverride_postgresSecondStoreSeesCommittedRevision(t *testing.T) {
	runtimeDSN := testkit.SkipUnlessPostgres(t)
	adminDSN, ok := testkit.PostgresAdminDSN()
	if !ok {
		adminDSN = runtimeDSN
	}
	ctx, cancel := context.WithTimeout(context.Background(), db.DefaultPostgresOpenMigrateTimeout)
	defer cancel()
	poolCfg, err := config.ParseDatabasePoolSettings(config.DatabaseConfig{MaxOpenConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	pool := db.PoolSettings{
		MaxOpenConns:    poolCfg.MaxOpenConns,
		MaxIdleConns:    poolCfg.MaxIdleConns,
		ConnMaxLifetime: poolCfg.ConnMaxLifetime,
		ConnMaxIdleTime: poolCfg.ConnMaxIdleTime,
	}
	migrateDB, err := db.OpenPostgresBun(ctx, adminDSN, pool)
	if err != nil {
		t.Fatal(err)
	}
	migrated, err := NewWithContext(ctx, migrateDB)
	if err != nil {
		_ = migrateDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = migrated.Close() })

	openStore := func(t *testing.T) *Store {
		t.Helper()
		openCtx, openCancel := context.WithTimeout(context.Background(), db.DefaultPostgresOpenMigrateTimeout)
		defer openCancel()
		bunDB, err := db.OpenPostgresBun(openCtx, runtimeDSN, pool)
		if err != nil {
			t.Fatal(err)
		}
		s := &Store{db: bunDB}
		t.Cleanup(func() { _ = s.Close() })
		return s
	}
	s1 := openStore(t)
	s2 := openStore(t)
	leg, err := s1.CreateALeg(ctx, fmt.Sprintf("ov-pg-second-view-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	first, err := s1.Replace(ctx, leg.ALegID, "openai:gpt-4", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	fromS2, err := s2.Snapshot(ctx, leg.ALegID)
	if err != nil {
		t.Fatalf("second store Snapshot: %v", err)
	}
	if !fromS2.Active || fromS2.Selector != first.Selector || fromS2.Revision != first.Revision {
		t.Fatalf("second view stale/missing: got %+v want %+v", fromS2, first)
	}
	second, err := s2.Replace(ctx, leg.ALegID, "anthropic:claude", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	fromS1, err := s1.Snapshot(ctx, leg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if fromS1.Selector != second.Selector || fromS1.Revision != second.Revision {
		t.Fatalf("first view must observe second store commit: got %+v want %+v", fromS1, second)
	}
}
