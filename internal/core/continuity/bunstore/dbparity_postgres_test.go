//go:build integration

package bunstore

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/routeoverride"
	routeoverrideStorecontract "github.com/aiproxer/aiproxer/internal/core/routeoverride/storecontract"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/stretchr/testify/require"
)

type postgresContinuityFixture struct {
	runtimeDSN string
	adminDSN   string
	pool       db.PoolSettings
}

func newPostgresContinuityFixture(t *testing.T, runtimeDSN, adminDSN string) continuityParityFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), db.DefaultPostgresOpenMigrateTimeout)
	defer cancel()

	poolCfg, err := config.ParseDatabasePoolSettings(config.DatabaseConfig{MaxOpenConns: 32, MaxIdleConns: 8})
	require.NoError(t, err)
	pool := db.PoolSettings{
		MaxOpenConns:    poolCfg.MaxOpenConns,
		MaxIdleConns:    poolCfg.MaxIdleConns,
		ConnMaxLifetime: poolCfg.ConnMaxLifetime,
		ConnMaxIdleTime: poolCfg.ConnMaxIdleTime,
	}

	migrateDB, err := db.OpenPostgresBun(ctx, adminDSN, pool)
	require.NoError(t, err)
	migrated, err := NewWithContext(ctx, migrateDB)
	require.NoError(t, err)
	t.Cleanup(func() { _ = migrated.Close() })

	return &postgresContinuityFixture{
		runtimeDSN: runtimeDSN,
		adminDSN:   adminDSN,
		pool:       pool,
	}
}

func (f *postgresContinuityFixture) NewStore(t *testing.T) *Store {
	t.Helper()
	openCtx, openCancel := context.WithTimeout(context.Background(), db.DefaultPostgresOpenMigrateTimeout)
	defer openCancel()
	bunDB, err := db.OpenPostgresBun(openCtx, f.runtimeDSN, f.pool)
	require.NoError(t, err)
	s := &Store{db: bunDB}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func (f *postgresContinuityFixture) RouteOverrideEnv(t *testing.T) routeoverrideStorecontract.ContractEnv {
	t.Helper()
	return routeoverrideStorecontract.ContractEnv{
		New: func(t *testing.T) routeoverrideStorecontract.ContractPair {
			t.Helper()
			s := f.NewStore(t)
			ov, ok := routeoverride.AsStore(s)
			if !ok {
				t.Fatal("continuity/bunstore PostgreSQL Store does not implement routeoverride.Store")
			}
			return routeoverrideStorecontract.ContractPair{Override: ov, Legs: s}
		},
		SeedRevision:    seedBunRevision,
		PeekLastSeenAt:  peekBunLastSeenAt,
		AdvanceClock:    advanceBunLastSeen,
		SeedStoredState: seedBunStoredState,
		Spawn:           func(fn func()) { go fn() },
	}
}

func (f *postgresContinuityFixture) ReopenStore(t *testing.T) (*Store, func() *Store) {
	t.Helper()
	var current *Store
	openStore := func() *Store {
		openCtx, openCancel := context.WithTimeout(context.Background(), db.DefaultPostgresOpenMigrateTimeout)
		defer openCancel()
		bunDB, err := db.OpenPostgresBun(openCtx, f.runtimeDSN, f.pool)
		require.NoError(t, err)
		current = &Store{db: bunDB}
		return current
	}
	s1 := openStore()
	t.Cleanup(func() {
		if current != nil {
			_ = current.Close()
		}
	})
	reopen := func() *Store {
		if current != nil {
			_ = current.Close()
			current = nil
		}
		return openStore()
	}
	return s1, reopen
}

// TestDBParity_PostgresDirect is the canonical parity entry point for continuity persistence on PostgreSQL Direct.
func TestDBParity_PostgresDirect(t *testing.T) {
	runtimeDSN := testkit.SkipUnlessPostgres(t)
	adminDSN, ok := testkit.PostgresAdminDSN()
	if !ok {
		adminDSN = runtimeDSN
	}
	runContinuityParitySuite(t, newPostgresContinuityFixture(t, runtimeDSN, adminDSN))
}
