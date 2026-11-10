//go:build integration

package featurehost

// PostgreSQL composed acceptance for Proxy Memory V1, issue #793.
// This reuses the default SQLite sentinel's actual generation, executor,
// capture writer, worker and HTTP projections with two independent PostgreSQL
// pools in one disposable schema. It is stub-backed, not a live-provider or
// native-CLI session-continuity certificate.

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/stretchr/testify/require"
)

func TestProxyMemorySentinel_PostgresDirect(t *testing.T) {
	runtimeDSN := testkit.SkipUnlessPostgres(t)
	adminDSN, ok := testkit.PostgresAdminDSN()
	if !ok {
		adminDSN = runtimeDSN
	}

	// Separate schema isolates the complete composed topology, including
	// continuity, secure-session and memory migrations, from shared test DBs.
	schema := "pm_accept_" + strings.ReplaceAll(testkit.UniquePostgresStoreID("pm793"), "-", "_")
	admin := testkit.OpenPostgresBunForTest(t, adminDSN, 2)
	t.Cleanup(func() {
		if err := admin.Close(); err != nil {
			t.Errorf("close PostgreSQL acceptance admin pool: %v", err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := admin.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`)
	require.NoError(t, err, "create isolated PostgreSQL acceptance schema")
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if _, err := admin.ExecContext(cleanupCtx, `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`); err != nil {
			t.Errorf("drop isolated PostgreSQL acceptance schema: %v", err)
		}
	})

	// search_path travels with every connection in each dedicated pool.
	// A one-time SET on a pooled connection would not isolate the test.
	scopedDSN := postgresMemoryAcceptanceScopedDSN(t, runtimeDSN, schema)
	continuityDB := testkit.OpenPostgresBunForTest(t, scopedDSN, 4)
	t.Cleanup(func() { _ = continuityDB.Close() })
	memoryDB := testkit.OpenPostgresBunForTest(t, scopedDSN, 4)
	t.Cleanup(func() { _ = memoryDB.Close() })

	// Fail closed rather than accidentally testing the public schema.
	for _, bunDB := range []struct {
		name string
		run  func() (string, error)
	}{
		{"continuity", func() (string, error) {
			var got string
			err := continuityDB.NewRaw("SELECT current_schema()").Scan(ctx, &got)
			return got, err
		}},
		{"memory", func() (string, error) {
			var got string
			err := memoryDB.NewRaw("SELECT current_schema()").Scan(ctx, &got)
			return got, err
		}},
	} {
		got, err := bunDB.run()
		require.NoError(t, err, "inspect %s pool schema", bunDB.name)
		require.Equal(t, schema, got, "%s pool must use isolated schema", bunDB.name)
	}

	runProxyMemorySentinelCrossPrincipalIsolationAndForget(t, continuityDB, memoryDB)
}

func postgresMemoryAcceptanceScopedDSN(t *testing.T, runtimeDSN, schema string) string {
	t.Helper()
	trimmed := strings.TrimSpace(runtimeDSN)
	u, err := url.Parse(trimmed)
	if err == nil && (u.Scheme == "postgres" || u.Scheme == "postgresql") && u.Host != "" {
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		return u.String()
	}
	// Also support the libpq-style key=value DSNs accepted by the repository.
	// Reject an existing explicit search_path instead of ambiguously duplicating it.
	if strings.Contains(strings.ToLower(trimmed), "search_path=") {
		t.Fatal("PostgreSQL acceptance requires a DSN without a preexisting search_path in key=value form")
	}
	if trimmed == "" {
		t.Fatal("PostgreSQL acceptance requires a non-empty test DSN")
	}
	if strings.Contains(trimmed, "://") {
		t.Fatal(fmt.Sprintf("PostgreSQL acceptance requires postgres:// or postgresql:// DSN; got a different URL scheme"))
	}
	return trimmed + " search_path=" + schema
}
