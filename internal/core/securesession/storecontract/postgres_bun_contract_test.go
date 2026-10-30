//go:build integration

package storecontract_test

import (
	"context"
	"fmt"
	"net/url"
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
)

// pgBuildMu serializes schema bootstrap so concurrent packages opening the same
// DSN do not race on CREATE SCHEMA while this suite isolates each subtest.
var pgBuildMu sync.Mutex

var pgSchemaSeq atomic.Uint64

// TestStoreContract_BunPostgreSQL runs the secure-session contract suite against Bun on PostgreSQL
// when AIP_TEST_POSTGRES_DSN (or legacy AIP_MANAGED_POSTGRES_DSN) is set.
// Each subtest gets an isolated schema so hardcoded session IDs and resume
// fingerprints from the shared contract suite cannot collide.
func TestStoreContract_BunPostgreSQL(t *testing.T) {
	t.Parallel()
	dsn := testkit.SkipUnlessPostgres(t)
	poolCfg, err := config.ParseDatabasePoolSettings(config.DatabaseConfig{MaxOpenConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	pool := db.PoolSettings{
		MaxOpenConns:    poolCfg.MaxOpenConns,
		MaxIdleConns:    poolCfg.MaxIdleConns,
		ConnMaxLifetime: poolCfg.ConnMaxLifetime,
		ConnMaxIdleTime: poolCfg.ConnMaxIdleTime,
	}
	storecontract.RunAll(t, func(t *testing.T) app.Store {
		t.Helper()
		return newIsolatedPostgresSecureSessionStore(t, dsn, pool)
	})
}

func newIsolatedPostgresSecureSessionStore(t *testing.T, dsn string, pool db.PoolSettings) app.Store {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), db.DefaultPostgresOpenMigrateTimeout)
	defer cancel()

	pgBuildMu.Lock()
	seq := pgSchemaSeq.Add(1)
	schemaName := fmt.Sprintf("ss_contract_%d_%d", time.Now().UnixNano(), seq)
	bootstrap, err := db.OpenPostgresBun(ctx, dsn, pool)
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

	schemaDSN, err := setPostgresSearchPath(dsn, schemaName)
	if err != nil {
		pgBuildMu.Unlock()
		t.Fatalf("set search_path: %v", err)
	}
	bunDB, err := db.OpenPostgresBun(ctx, schemaDSN, pool)
	if err != nil {
		pgBuildMu.Unlock()
		t.Fatal(err)
	}
	if _, err := bunDB.ExecContext(ctx, fmt.Sprintf("SET search_path TO %s", schemaName)); err != nil {
		_ = bunDB.Close()
		pgBuildMu.Unlock()
		t.Fatalf("set search_path on test store: %v", err)
	}
	s, err := bunstore.NewWithContext(ctx, bunDB)
	if err != nil {
		_ = bunDB.Close()
		pgBuildMu.Unlock()
		t.Fatal(err)
	}
	pgBuildMu.Unlock()

	t.Cleanup(func() {
		_ = s.Close()
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dropCancel()
		dropper, derr := db.OpenPostgresBun(dropCtx, dsn, pool)
		if derr != nil {
			return
		}
		defer func() { _ = dropper.Close() }()
		_, _ = dropper.ExecContext(dropCtx, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schemaName))
	})
	return s
}

// setPostgresSearchPath returns dsn with a search_path query parameter set to
// schema, replacing any existing search_path. pgdriver forwards unknown query
// params to the server as SET commands, and search_path is a server GUC, so
// each pooled connection binds to the isolated schema.
func setPostgresSearchPath(dsn, schema string) (string, error) {
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
