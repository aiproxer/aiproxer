//go:build integration

package leasestore_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/concurrencyauthority/leasestore"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/uptrace/bun"
	"go.uber.org/goleak"
)

const pooledLeaseOpenTimeout = 2 * time.Minute

var (
	pooledLeaseSchemaOnce sync.Once
	pooledLeaseSchemaErr  error

	pooledLeaseRuntimeOnce sync.Once
	pooledLeaseRuntimeDB   *bun.DB
	pooledLeaseRuntimeErr  error
	pooledLeaseSharedGuard = testkit.NewRuntimeSQLGuard()
	// pooledLeaseTestMu serializes shared-pool tests so Reset/Assert cannot cross-talk
	// if a test accidentally calls t.Parallel().
	pooledLeaseTestMu sync.Mutex
)

func TestMain(m *testing.M) {
	code := m.Run()
	if pooledLeaseRuntimeDB != nil {
		_ = pooledLeaseRuntimeDB.Close()
	}
	if code == 0 {
		if err := goleak.Find(); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "goleak: %v\n", err)
			code = 1
		}
	}
	os.Exit(code)
}

func ensurePooledLeaseSchema(t *testing.T, adminDSN string) {
	t.Helper()
	pooledLeaseSchemaOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), pooledLeaseOpenTimeout)
		defer cancel()
		admin, err := testkit.OpenPostgresBun(adminDSN, 2)
		if err != nil {
			pooledLeaseSchemaErr = err
			return
		}
		bootstrap, err := leasestore.NewDurable(ctx, admin, leasestore.DurableConfig{
			StoreID: testkit.UniquePostgresStoreID("pg-lease-schema"),
		})
		if err != nil {
			_ = admin.Close()
			pooledLeaseSchemaErr = err
			return
		}
		pooledLeaseSchemaErr = bootstrap.Close()
	})
	if pooledLeaseSchemaErr != nil {
		t.Fatalf("package admin schema bootstrap: %v", pooledLeaseSchemaErr)
	}
}

func sharedPooledLeaseRuntime(t *testing.T, runtimeDSN string) *bun.DB {
	t.Helper()
	pooledLeaseRuntimeOnce.Do(func() {
		db, err := testkit.OpenPostgresBun(runtimeDSN, 4)
		if err != nil {
			pooledLeaseRuntimeErr = err
			return
		}
		db.AddQueryHook(pooledLeaseSharedGuard)
		pooledLeaseRuntimeDB = db
	})
	if pooledLeaseRuntimeErr != nil {
		t.Fatalf("package shared runtime pool: %v", pooledLeaseRuntimeErr)
	}
	return pooledLeaseRuntimeDB
}

func openSharedPooledLeaseStore(t *testing.T, adminDSN, runtimeDSN, storeID string) (*leasestore.DurableStore, *testkit.RuntimeSQLGuard) {
	t.Helper()
	pooledLeaseTestMu.Lock()
	t.Cleanup(pooledLeaseTestMu.Unlock)
	ensurePooledLeaseSchema(t, adminDSN)
	t.Cleanup(func() {
		testkit.CleanupPostgresStoreByID(t, adminDSN, storeID, testkit.PostgresComponentLease)
	})

	pooledLeaseSharedGuard.Reset()
	runtime := sharedPooledLeaseRuntime(t, runtimeDSN)
	ctx, cancel := context.WithTimeout(t.Context(), pooledLeaseOpenTimeout)
	defer cancel()
	if err := leasestore.VerifySchema(ctx, runtime); err != nil {
		t.Fatalf("verify runtime schema: %v", err)
	}
	store, err := leasestore.OpenStore(ctx, runtime, leasestore.DurableConfig{StoreID: storeID})
	if err != nil {
		t.Fatalf("open pooled runtime store: %v", err)
	}
	pooledLeaseSharedGuard.AssertNoViolations(t)
	return store, pooledLeaseSharedGuard
}

func openOwnedPooledLeaseStore(t *testing.T, adminDSN, runtimeDSN, storeID string) (*leasestore.DurableStore, *testkit.RuntimeSQLGuard) {
	t.Helper()
	ensurePooledLeaseSchema(t, adminDSN)
	t.Cleanup(func() {
		testkit.CleanupPostgresStoreByID(t, adminDSN, storeID, testkit.PostgresComponentLease)
	})

	guard := testkit.NewRuntimeSQLGuard()
	runtime := testkit.OpenPostgresBunForTest(t, runtimeDSN, 4)
	runtime.AddQueryHook(guard)
	ctx, cancel := context.WithTimeout(t.Context(), pooledLeaseOpenTimeout)
	defer cancel()
	if err := leasestore.VerifySchema(ctx, runtime); err != nil {
		_ = runtime.Close()
		t.Fatalf("verify runtime schema: %v", err)
	}
	store, err := leasestore.OpenStore(ctx, runtime, leasestore.DurableConfig{StoreID: storeID})
	if err != nil {
		_ = runtime.Close()
		t.Fatalf("open pooled runtime store: %v", err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(); err != nil {
			t.Fatalf("close pooled runtime: %v", err)
		}
		if err := runtime.PingContext(context.Background()); err == nil {
			t.Fatal("pooled runtime should be closed")
		}
	})
	guard.AssertNoViolations(t)
	return store, guard
}
