//go:build integration

package leasestore_test

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/concurrencyauthority/leasestore"
	"github.com/aiproxer/aiproxer/internal/testkit"
)

// TestDBParity_PostgresDirect is the canonical parity entry point for concurrency-authority on PostgreSQL Direct.
func TestDBParity_PostgresDirect(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	runLeaseParitySuite(t, func(t *testing.T) *leaseStoreFactory {
		t.Helper()
		storeID := "parity-pg-" + testkit.UniquePostgresStoreID("lease")
		ensureDirectLeaseSchema(t, dsn)
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
		defer cancel()
		bunDB := testkit.OpenPostgresBunForTest(t, dsn, 4)
		store, err := leasestore.NewDurable(ctx, bunDB, leasestore.DurableConfig{StoreID: storeID})
		if err != nil {
			_ = bunDB.Close()
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		t.Cleanup(func() { testkit.CleanupPostgresStoreByID(t, dsn, storeID, testkit.PostgresComponentAuthority) })
		return &leaseStoreFactory{
			store: store,
			db:    bunDB,
		}
	})
}
