//go:build integration

package featurehost

// Shared PostgreSQL fixture for the proxy-memory composed-acceptance tests
// (issue #793). Each acceptance run needs the complete composed topology —
// continuity, secure session, conversation view and proxy-memory migrations —
// in its own schema, so the run never touches shared tables and never proves
// isolation against the public schema by accident.
//
// The fixture is deliberately small: it validates the runtime DSN, creates one
// bounded disposable schema, and opens pools that prove they resolve it.
// Assertions about memory behaviour stay in the tests that own them.

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
)

const (
	// proxyMemoryPostgresFixtureTimeout bounds every fixture topology step —
	// DSN validation, schema creation, the pool schema probe and the schema
	// drop — so a wedged server fails the acceptance run instead of hanging it.
	proxyMemoryPostgresFixtureTimeout = 30 * time.Second
	// proxyMemoryPostgresSchemaNameMaxBytes is PostgreSQL's identifier limit (NAMEDATALEN
	// - 1). A longer name is silently truncated by the server, so the pools
	// would report a current_schema different from the name the test compares.
	proxyMemoryPostgresSchemaNameMaxBytes = 63
)

// proxyMemoryPostgresSchema creates the isolated schema of one composition
// acceptance run and returns the DSN every pool of that run must open with,
// plus the schema name the pools must prove they resolve. namePrefix names the
// test that owns the schema, so a schema leaked by a crashed run is
// attributable.
//
// The runtime DSN must be a postgres:// or postgresql:// URL without a
// preexisting search_path (an explicitly empty one counts as preexisting): the
// repository opens PostgreSQL through its bounded Bun/pgdriver path, which
// rejects libpq key=value DSNs, and an existing search_path would leave the
// isolation ambiguous instead of additive. Both refusals happen before anything
// is created, so a refused run leaves no schema behind.
//
// Register this fixture before opening the pools it scopes: cleanups run LIFO,
// so the caller's pools close before the schema drop.
func proxyMemoryPostgresSchema(t *testing.T, namePrefix string) (scopedDSN, schema string) {
	t.Helper()
	runtimeDSN := testkit.SkipUnlessPostgres(t)
	adminDSN, ok := testkit.PostgresAdminDSN()
	if !ok {
		adminDSN = runtimeDSN
	}

	// Validation runs first: a refused DSN must not cost a schema create/drop.
	u, err := url.Parse(strings.TrimSpace(runtimeDSN))
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" {
		t.Fatal("proxy-memory PostgreSQL acceptance requires a postgres:// or postgresql:// URL test DSN")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		t.Fatal("proxy-memory PostgreSQL acceptance requires a parseable test DSN query")
	}
	// PostgreSQL GUC names are case-insensitive, so SEARCH_PATH is the same
	// setting; refuse it too rather than emitting two competing SET commands.
	for key := range query {
		if strings.EqualFold(key, "search_path") {
			t.Fatal("proxy-memory PostgreSQL acceptance requires a test DSN without a preexisting search_path (including an empty one)")
		}
	}

	schema = proxyMemoryPostgresSchemaName(t, namePrefix)

	admin := testkit.OpenPostgresBunForTest(t, adminDSN, 1)
	createCtx, createCancel := context.WithTimeout(context.Background(), proxyMemoryPostgresFixtureTimeout)
	defer createCancel()
	// proxyMemoryPostgresSchemaName emits only identifier-safe ASCII, and the
	// name is quoted here, so it cannot need further escaping.
	_, err = admin.ExecContext(createCtx, `CREATE SCHEMA "`+schema+`"`)
	// Register cleanup before asserting the create result, so a run that fails
	// later never leaks the admin pool and IF EXISTS removes any schema the
	// server did create. Cleanup keeps its own bounded context: the create
	// context above is cancelled when this call returns.
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), proxyMemoryPostgresFixtureTimeout)
		defer cleanupCancel()
		if _, dropErr := admin.ExecContext(cleanupCtx, `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`); dropErr != nil {
			t.Errorf("drop isolated proxy-memory acceptance schema: %v", dropErr)
		}
		if closeErr := admin.Close(); closeErr != nil {
			t.Errorf("close PostgreSQL acceptance admin pool: %v", closeErr)
		}
	})
	require.NoError(t, err, "create the isolated proxy-memory acceptance schema")

	// search_path travels with every connection of each pool: a one-time SET on
	// a pooled connection would leave the other pooled connections public.
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	return u.String(), schema
}

// proxyMemoryPostgresPool opens one owned pool over scopedDSN and fails closed
// unless that pool actually resolves the isolated schema. A pool that reports
// the public schema would silently test shared tables, so the probe is part of
// the fixture rather than a caller obligation.
func proxyMemoryPostgresPool(t *testing.T, scopedDSN, schema, poolName string, maxOpen int) *bun.DB {
	t.Helper()
	pool := testkit.OpenPostgresBunForTest(t, scopedDSN, maxOpen)
	t.Cleanup(func() { _ = pool.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), proxyMemoryPostgresFixtureTimeout)
	defer cancel()
	var current string
	require.NoError(t, pool.NewRaw("SELECT current_schema()").Scan(ctx, &current),
		"inspect %s pool schema", poolName)
	require.Equal(t, schema, current, "%s pool must use the isolated schema", poolName)
	return pool
}

// proxyMemoryPostgresSchemaName returns a process-unique disposable schema name
// that starts with the caller's test-specific prefix, sanitized to
// identifier-safe ASCII and within PostgreSQL's identifier limit.
func proxyMemoryPostgresSchemaName(t *testing.T, namePrefix string) string {
	t.Helper()
	sanitized := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			return r
		default:
			return '_'
		}
	}, strings.TrimSpace(namePrefix))
	if strings.Trim(sanitized, "_") == "" {
		t.Fatal("proxy-memory PostgreSQL acceptance requires a non-empty schema name prefix")
	}
	name := sanitized + "_" + strings.ReplaceAll(testkit.UniquePostgresStoreID("pm793"), "-", "_")
	if len(name) > proxyMemoryPostgresSchemaNameMaxBytes {
		t.Fatalf("proxy-memory acceptance schema name is longer than PostgreSQL's %d-byte identifier limit; shorten the prefix", proxyMemoryPostgresSchemaNameMaxBytes)
	}
	return name
}
