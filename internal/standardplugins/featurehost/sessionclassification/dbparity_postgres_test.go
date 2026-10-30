//go:build integration

package sessionclassification_test

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	store "github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/sessionclassification"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
)

var postgresClassificationSchemaSequence atomic.Uint64

// TestDBParity_PostgresDirect is the fail-closed canonical PostgreSQL entry
// point for the session-classification durable Load/Promote and schema contract.
func TestDBParity_PostgresDirect(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	openScopedDB := openIsolatedSessionClassificationPostgresBun(t, dsn, 8)
	currentDB := openScopedDB()
	openAgain := func() *bun.DB {
		require.NoError(t, currentDB.Close())
		currentDB = openScopedDB()
		return currentDB
	}

	classificationStore, err := store.NewBunStore(currentDB)
	require.NoError(t, err)
	runSessionClassificationParityContract(t, currentDB, classificationStore, openAgain)
}

func openIsolatedSessionClassificationPostgresBun(t *testing.T, runtimeDSN string, maxOpen int) func() *bun.DB {
	t.Helper()
	adminDSN, ok := testkit.PostgresAdminDSN()
	if !ok {
		adminDSN = runtimeDSN
	}
	admin := testkit.OpenPostgresBunForTest(t, adminDSN, 1)
	schema := fmt.Sprintf("session_classification_test_%d_%d", time.Now().UnixNano(), postgresClassificationSchemaSequence.Add(1))
	quotedSchema := quoteSessionClassificationSchema(schema)
	if _, err := admin.ExecContext(context.Background(), "CREATE SCHEMA "+quotedSchema); err != nil {
		_ = admin.Close()
		t.Fatalf("create isolated session-classification PostgreSQL schema: %v", err)
	}

	scopedDSN, hasURLSearchPath := sessionClassificationDSNWithSearchPath(runtimeDSN, schema)
	openMax := maxOpen
	if !hasURLSearchPath {
		openMax = 1
	}
	t.Cleanup(func() {
		if _, err := admin.ExecContext(context.Background(), "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop isolated session-classification PostgreSQL schema: %v", err)
		}
		_ = admin.Close()
	})

	return func() *bun.DB {
		bunDB, err := testkit.OpenPostgresBun(scopedDSN, openMax)
		require.NoError(t, err, "open isolated session-classification PostgreSQL runtime")
		t.Cleanup(func() { _ = bunDB.Close() })
		if !hasURLSearchPath {
			if _, err := bunDB.ExecContext(context.Background(), "SET search_path TO "+quotedSchema); err != nil {
				_ = bunDB.Close()
				t.Fatalf("set isolated session-classification PostgreSQL search_path: %v", err)
			}
		}
		return bunDB
	}
}

func sessionClassificationDSNWithSearchPath(dsn, schema string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(dsn))
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Host == "" {
		return dsn, false
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String(), true
}

func quoteSessionClassificationSchema(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}
