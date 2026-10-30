//go:build integration

package leasestore_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/domain"
	"github.com/aiproxer/aiproxer/internal/infra/concurrencyauthority/leasestore"
	"github.com/aiproxer/aiproxer/internal/testkit"
)

var (
	directLeaseSchemaOnce sync.Once
	directLeaseSchemaErr  error
)

func TestPostgresStore_FiveSlotAcrossTwoInstances(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	storeID := testkit.UniquePostgresStoreID("pg-lease")
	t.Cleanup(func() {
		testkit.CleanupPostgresStoreByID(t, adminDSNForCleanup(dsn), storeID, testkit.PostgresComponentLease)
	})
	a := newPostgresStore(t, dsn, storeID)
	b := newPostgresStore(t, dsn, storeID)
	runFiveSlotContract(t, a, b)
}

func TestPostgresStore_AcquireSetFiveSlotMultiRule(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	storeID := testkit.UniquePostgresStoreID("pg-lease-set")
	t.Cleanup(func() {
		testkit.CleanupPostgresStoreByID(t, adminDSNForCleanup(dsn), storeID, testkit.PostgresComponentLease)
	})
	a := newPostgresStore(t, dsn, storeID)
	b := newPostgresStore(t, dsn, storeID)
	runFiveSlotAcquireSetContract(t, a, b)
}

func TestPostgresStore_AcquireSetFailureMatrix(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	storeID := testkit.UniquePostgresStoreID("pg-lease-set-fail")
	t.Cleanup(func() {
		testkit.CleanupPostgresStoreByID(t, adminDSNForCleanup(dsn), storeID, testkit.PostgresComponentLease)
	})
	a := newPostgresStore(t, dsn, storeID)
	b := newPostgresStore(t, dsn, storeID)
	runAcquireSetFailureMatrixContract(t, a, b)
}

func TestPostgresStore_ReadinessDistributedStrict(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	storeID := testkit.UniquePostgresStoreID("pg-ready")
	t.Cleanup(func() {
		testkit.CleanupPostgresStoreByID(t, adminDSNForCleanup(dsn), storeID, testkit.PostgresComponentLease)
	})
	store := newPostgresStore(t, dsn, storeID)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	ready, err := store.CheckReadiness(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ready.State != domain.ReadinessStateReady {
		t.Fatalf("state=%s want ready", ready.State)
	}
	if !strings.Contains(strings.ToLower(ready.Reason), "postgres") &&
		!strings.Contains(strings.ToLower(ready.Reason), "distributed") {
		t.Fatalf("expected distributed/postgres readiness reason, got %q", ready.Reason)
	}
}

func TestPostgresStore_ConcurrentReleaseRenew_NoResurrection(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	storeID := testkit.UniquePostgresStoreID("pg-cas-race")
	t.Cleanup(func() {
		testkit.CleanupPostgresStoreByID(t, adminDSNForCleanup(dsn), storeID, testkit.PostgresComponentLease)
	})
	store := newPostgresStore(t, dsn, storeID)
	runConcurrentReleaseRenewNoResurrection(t, store)
}

func adminDSNForCleanup(runtimeDSN string) string {
	if admin, ok := testkit.PostgresAdminDSN(); ok {
		return admin
	}
	return runtimeDSN
}

func ensureDirectLeaseSchema(t *testing.T, dsn string) {
	t.Helper()
	directLeaseSchemaOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		bunDB, err := testkit.OpenPostgresBun(dsn, 2)
		if err != nil {
			directLeaseSchemaErr = err
			return
		}
		store, err := leasestore.NewDurable(ctx, bunDB, leasestore.DurableConfig{
			StoreID: testkit.UniquePostgresStoreID("pg-lease-direct-schema"),
		})
		if err != nil {
			_ = bunDB.Close()
			directLeaseSchemaErr = err
			return
		}
		directLeaseSchemaErr = store.Close()
	})
	if directLeaseSchemaErr != nil {
		t.Fatalf("direct schema bootstrap: %v", directLeaseSchemaErr)
	}
}

func newPostgresStore(t *testing.T, dsn, storeID string) *leasestore.DurableStore {
	t.Helper()
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
	return store
}
