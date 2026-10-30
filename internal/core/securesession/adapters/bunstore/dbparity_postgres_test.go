//go:build integration

package bunstore_test

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
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

var (
	bunstorePGBuildMu   sync.Mutex
	bunstorePGSchemaSeq atomic.Uint64
)

type bunstorePGFixture struct {
	runtimeDSN string
	adminDSN   string
	pool       db.PoolSettings
}

func (f *bunstorePGFixture) NewStore(t *testing.T) app.Store {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), db.DefaultPostgresOpenMigrateTimeout)
	defer cancel()

	bunstorePGBuildMu.Lock()
	seq := bunstorePGSchemaSeq.Add(1)
	schemaName := fmt.Sprintf("ss_bun_%d_%d", time.Now().UnixNano(), seq)
	bootstrap, err := db.OpenPostgresBun(ctx, f.adminDSN, f.pool)
	if err != nil {
		bunstorePGBuildMu.Unlock()
		t.Fatal(err)
	}
	if _, err := bootstrap.ExecContext(ctx, fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %q", schemaName)); err != nil {
		_ = bootstrap.Close()
		bunstorePGBuildMu.Unlock()
		t.Fatalf("create schema %s: %v", schemaName, err)
	}
	if err := bootstrap.Close(); err != nil {
		bunstorePGBuildMu.Unlock()
		t.Fatalf("close bootstrap: %v", err)
	}

	schemaDSN, err := setPostgresSearchPathForBun(f.runtimeDSN, schemaName)
	if err != nil {
		bunstorePGBuildMu.Unlock()
		t.Fatalf("set search_path: %v", err)
	}
	bunDB, err := db.OpenPostgresBun(ctx, schemaDSN, f.pool)
	if err != nil {
		bunstorePGBuildMu.Unlock()
		t.Fatal(err)
	}
	if _, err := bunDB.ExecContext(ctx, fmt.Sprintf("SET search_path TO %s", schemaName)); err != nil {
		_ = bunDB.Close()
		bunstorePGBuildMu.Unlock()
		t.Fatalf("set search_path: %v", err)
	}
	s, err := bunstore.NewWithContext(ctx, bunDB)
	if err != nil {
		_ = bunDB.Close()
		bunstorePGBuildMu.Unlock()
		t.Fatal(err)
	}
	bunstorePGBuildMu.Unlock()

	t.Cleanup(func() {
		_ = s.Close()
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dropCancel()
		dropper, derr := db.OpenPostgresBun(dropCtx, f.adminDSN, f.pool)
		if derr != nil {
			return
		}
		defer func() { _ = dropper.Close() }()
		_, _ = dropper.ExecContext(dropCtx, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schemaName))
	})
	return s
}

func (f *bunstorePGFixture) ReopenStore(t *testing.T) (app.Store, func() app.Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), db.DefaultPostgresOpenMigrateTimeout)
	defer cancel()

	bunstorePGBuildMu.Lock()
	seq := bunstorePGSchemaSeq.Add(1)
	schemaName := fmt.Sprintf("ss_bun_reopen_%d_%d", time.Now().UnixNano(), seq)
	bootstrap, err := db.OpenPostgresBun(ctx, f.adminDSN, f.pool)
	if err != nil {
		bunstorePGBuildMu.Unlock()
		t.Fatal(err)
	}
	if _, err := bootstrap.ExecContext(ctx, fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %q", schemaName)); err != nil {
		_ = bootstrap.Close()
		bunstorePGBuildMu.Unlock()
		t.Fatalf("create schema %s: %v", schemaName, err)
	}
	if err := bootstrap.Close(); err != nil {
		bunstorePGBuildMu.Unlock()
		t.Fatalf("close bootstrap: %v", err)
	}

	schemaDSN, err := setPostgresSearchPathForBun(f.runtimeDSN, schemaName)
	if err != nil {
		bunstorePGBuildMu.Unlock()
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
	bunstorePGBuildMu.Unlock()

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

func setPostgresSearchPathForBun(dsn, schema string) (string, error) {
	before, after, ok := strings.Cut(dsn, "?")
	var base, rawQuery string
	if !ok {
		base, rawQuery = dsn, ""
	} else {
		base, rawQuery = before, after
	}
	vals, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", fmt.Errorf("parse dsn query: %w", err)
	}
	vals.Del("search_path")
	vals.Set("search_path", schema)
	encoded := vals.Encode()
	if encoded == "" {
		return base, nil
	}
	return base + "?" + encoded, nil
}

// TestDBParity_PostgresDirect is the canonical parity entry point for securesession/bunstore on PostgreSQL Direct.
func TestDBParity_PostgresDirect(t *testing.T) {
	runtimeDSN := testkit.SkipUnlessPostgres(t)
	adminDSN, ok := testkit.PostgresAdminDSN()
	if !ok {
		adminDSN = runtimeDSN
	}
	poolCfg, err := config.ParseDatabasePoolSettings(config.DatabaseConfig{MaxOpenConns: 4})
	require.NoError(t, err)
	pool := db.PoolSettings{
		MaxOpenConns:    poolCfg.MaxOpenConns,
		MaxIdleConns:    poolCfg.MaxIdleConns,
		ConnMaxLifetime: poolCfg.ConnMaxLifetime,
		ConnMaxIdleTime: poolCfg.ConnMaxIdleTime,
	}
	storecontract.RunParitySuite(t, &bunstorePGFixture{
		runtimeDSN: runtimeDSN,
		adminDSN:   adminDSN,
		pool:       pool,
	})

	t.Run("MigrationAndSchemaParity", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), db.DefaultPostgresOpenMigrateTimeout)
		defer cancel()

		bunstorePGBuildMu.Lock()
		seq := bunstorePGSchemaSeq.Add(1)
		schemaName := fmt.Sprintf("ss_bun_mig_%d_%d", time.Now().UnixNano(), seq)
		bootstrap, err := db.OpenPostgresBun(ctx, adminDSN, pool)
		require.NoError(t, err)
		_, err = bootstrap.ExecContext(ctx, fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %q", schemaName))
		_ = bootstrap.Close()
		require.NoError(t, err)

		schemaDSN, err := setPostgresSearchPathForBun(runtimeDSN, schemaName)
		if err != nil {
			bunstorePGBuildMu.Unlock()
			t.Fatal(err)
		}
		bunDB, err := db.OpenPostgresBun(ctx, schemaDSN, pool)
		if err != nil {
			bunstorePGBuildMu.Unlock()
			t.Fatal(err)
		}
		if _, err := bunDB.ExecContext(ctx, fmt.Sprintf("SET search_path TO %s", schemaName)); err != nil {
			_ = bunDB.Close()
			bunstorePGBuildMu.Unlock()
			t.Fatal(err)
		}
		_, err = bunstore.NewWithContext(ctx, bunDB)
		bunstorePGBuildMu.Unlock()
		require.NoError(t, err)

		t.Cleanup(func() {
			_ = bunDB.Close()
			dropCtx, dropCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer dropCancel()
			dropper, derr := db.OpenPostgresBun(dropCtx, adminDSN, pool)
			if derr != nil {
				return
			}
			defer func() { _ = dropper.Close() }()
			_, _ = dropper.ExecContext(dropCtx, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schemaName))
		})

		_, thisFile, _, ok := runtime.Caller(0)
		require.True(t, ok)
		runSecureSessionMigrationAndSchemaParity(t, bunDB, filepath.Dir(thisFile))
	})
}
