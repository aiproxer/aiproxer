//go:build integration

package storecontract_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/bunstore"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/securesession/storecontract"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/stretchr/testify/require"
)

type postgresSecureSessionFixture struct {
	runtimeDSN string
	adminDSN   string
	pool       db.PoolSettings
}

func newPostgresSecureSessionFixture(t *testing.T, runtimeDSN, adminDSN string) storecontract.ParityFixture {
	t.Helper()
	poolCfg, err := config.ParseDatabasePoolSettings(config.DatabaseConfig{MaxOpenConns: 4})
	require.NoError(t, err)
	pool := db.PoolSettings{
		MaxOpenConns:    poolCfg.MaxOpenConns,
		MaxIdleConns:    poolCfg.MaxIdleConns,
		ConnMaxLifetime: poolCfg.ConnMaxLifetime,
		ConnMaxIdleTime: poolCfg.ConnMaxIdleTime,
	}

	return &postgresSecureSessionFixture{
		runtimeDSN: runtimeDSN,
		adminDSN:   adminDSN,
		pool:       pool,
	}
}

func (f *postgresSecureSessionFixture) NewStore(t *testing.T) app.Store {
	t.Helper()
	return newIsolatedPostgresSecureSessionStore(t, f.runtimeDSN, f.pool)
}

func (f *postgresSecureSessionFixture) ReopenStore(t *testing.T) (app.Store, func() app.Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), db.DefaultPostgresOpenMigrateTimeout)
	defer cancel()

	pgBuildMu.Lock()
	seq := pgSchemaSeq.Add(1)
	schemaName := fmt.Sprintf("ss_reopen_%d_%d", time.Now().UnixNano(), seq)
	bootstrap, err := db.OpenPostgresBun(ctx, f.adminDSN, f.pool)
	if err != nil {
		pgBuildMu.Unlock()
		t.Fatal(err)
	}
	if _, err := bootstrap.ExecContext(ctx, fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %q", schemaName)); err != nil {
		_ = bootstrap.Close()
		pgBuildMu.Unlock()
		t.Fatalf("create schema %s: %v", schemaName, err)
	}
	if err := bootstrap.Close(); err != nil {
		pgBuildMu.Unlock()
		t.Fatalf("close bootstrap: %v", err)
	}

	schemaDSN, err := setPostgresSearchPath(f.runtimeDSN, schemaName)
	if err != nil {
		pgBuildMu.Unlock()
		t.Fatalf("set search_path: %v", err)
	}

	var current *bunstore.Store
	openStore := func() app.Store {
		openCtx, openCancel := context.WithTimeout(context.Background(), db.DefaultPostgresOpenMigrateTimeout)
		defer openCancel()
		bunDB, err := db.OpenPostgresBun(openCtx, schemaDSN, f.pool)
		require.NoError(t, err)
		if _, err := bunDB.ExecContext(openCtx, fmt.Sprintf("SET search_path TO %s", schemaName)); err != nil {
			_ = bunDB.Close()
			t.Fatalf("set search_path: %v", err)
		}
		s, err := bunstore.NewWithContext(openCtx, bunDB)
		require.NoError(t, err)
		current = s
		return s
	}

	s1 := openStore()
	pgBuildMu.Unlock()

	t.Cleanup(func() {
		if current != nil {
			_ = current.Close()
		}
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dropCancel()
		dropper, derr := db.OpenPostgresBun(dropCtx, f.adminDSN, f.pool)
		if derr != nil {
			return
		}
		defer func() { _ = dropper.Close() }()
		_, _ = dropper.ExecContext(dropCtx, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schemaName))
	})

	reopen := func() app.Store {
		if current != nil {
			_ = current.Close()
			current = nil
		}
		return openStore()
	}

	return s1, reopen
}

// TestDBParity_PostgresDirect is the canonical parity entry point for secure sessions on PostgreSQL Direct.
func TestDBParity_PostgresDirect(t *testing.T) {
	runtimeDSN := testkit.SkipUnlessPostgres(t)
	adminDSN, ok := testkit.PostgresAdminDSN()
	if !ok {
		adminDSN = runtimeDSN
	}
	storecontract.RunParitySuite(t, newPostgresSecureSessionFixture(t, runtimeDSN, adminDSN))
}
