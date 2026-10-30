//go:build integration

package leasestore_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/domain"
	"github.com/aiproxer/aiproxer/internal/infra/concurrencyauthority/leasestore"
	"github.com/aiproxer/aiproxer/internal/testkit"
)

func TestPostgresPooled_FiveSlotAcrossTwoRuntimeHandles(t *testing.T) {
	adminDSN, runtimeDSN := testkit.SkipUnlessPostgresPooled(t)
	storeID := testkit.UniquePostgresStoreID("pg-pooled-lease")
	a, guardA := openOwnedPooledLeaseStore(t, adminDSN, runtimeDSN, storeID)
	b, guardB := openOwnedPooledLeaseStore(t, adminDSN, runtimeDSN, storeID)
	runFiveSlotContract(t, a, b)
	guardA.AssertNoViolations(t)
	guardB.AssertNoViolations(t)
}

func TestPostgresPooled_AcquireSetFiveSlotMultiRule(t *testing.T) {
	adminDSN, runtimeDSN := testkit.SkipUnlessPostgresPooled(t)
	storeID := testkit.UniquePostgresStoreID("pg-pooled-lease-set")
	a, guardA := openOwnedPooledLeaseStore(t, adminDSN, runtimeDSN, storeID)
	b, guardB := openOwnedPooledLeaseStore(t, adminDSN, runtimeDSN, storeID)
	runFiveSlotAcquireSetContract(t, a, b)
	guardA.AssertNoViolations(t)
	guardB.AssertNoViolations(t)
}

func TestPostgresPooled_AcquireSetFailureMatrix(t *testing.T) {
	adminDSN, runtimeDSN := testkit.SkipUnlessPostgresPooled(t)
	storeID := testkit.UniquePostgresStoreID("pg-pooled-lease-set-fail")
	a, guardA := openOwnedPooledLeaseStore(t, adminDSN, runtimeDSN, storeID)
	b, guardB := openOwnedPooledLeaseStore(t, adminDSN, runtimeDSN, storeID)
	runAcquireSetFailureMatrixContract(t, a, b)
	guardA.AssertNoViolations(t)
	guardB.AssertNoViolations(t)
}

func TestPostgresPooled_ReleaseRenewNoResurrection(t *testing.T) {
	adminDSN, runtimeDSN := testkit.SkipUnlessPostgresPooled(t)
	storeID := testkit.UniquePostgresStoreID("pg-pooled-cas")
	store, guard := openSharedPooledLeaseStore(t, adminDSN, runtimeDSN, storeID)
	runConcurrentReleaseRenewNoResurrection(t, store)
	guard.AssertNoViolations(t)
}

func TestPostgresPooled_DMLAfterAdminCloseNoSearchPath(t *testing.T) {
	adminDSN, runtimeDSN := testkit.SkipUnlessPostgresPooled(t)
	storeID := testkit.UniquePostgresStoreID("pg-pooled-dml")
	store, guard := openSharedPooledLeaseStore(t, adminDSN, runtimeDSN, storeID)

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
	guard.AssertNoViolations(t)
}

func TestPostgresPooled_VerifySchemaFailsWhenRequiredIndexMissing(t *testing.T) {
	adminDSN, runtimeDSN := testkit.SkipUnlessPostgresPooled(t)
	pooledLeaseTestMu.Lock()
	t.Cleanup(pooledLeaseTestMu.Unlock)

	ensurePooledLeaseSchema(t, adminDSN)
	runtime := sharedPooledLeaseRuntime(t, runtimeDSN)

	admin := testkit.OpenPostgresBunForTest(t, adminDSN, 2)
	t.Cleanup(func() { _ = admin.Close() })
	t.Cleanup(func() {
		restoreCtx, restoreCancel := context.WithTimeout(context.Background(), pooledLeaseOpenTimeout)
		defer restoreCancel()
		_, _ = admin.ExecContext(restoreCtx, `CREATE INDEX IF NOT EXISTS idx_concurrency_leases_capacity
			ON concurrency_leases(store_id, rule_id, dimension_key, state, expires_at_unix)`)
	})

	mutateCtx, mutateCancel := context.WithTimeout(context.Background(), pooledLeaseOpenTimeout)
	defer mutateCancel()
	if _, err := admin.ExecContext(mutateCtx, `DROP INDEX IF EXISTS idx_concurrency_leases_capacity`); err != nil {
		t.Fatalf("drop pooled lease index: %v", err)
	}

	verifyCtx, verifyCancel := context.WithTimeout(t.Context(), pooledLeaseOpenTimeout)
	defer verifyCancel()
	err := leasestore.VerifySchema(verifyCtx, runtime)
	if err == nil {
		t.Fatal("expected verify_only schema validation to fail when the required index is missing")
	}
	if !strings.Contains(err.Error(), "idx_concurrency_leases_capacity") {
		t.Fatalf("verify error %q does not mention the missing index", err)
	}
}
