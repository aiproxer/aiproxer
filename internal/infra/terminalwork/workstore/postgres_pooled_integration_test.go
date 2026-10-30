//go:build integration

package workstore_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/terminalwork"
	"github.com/aiproxer/aiproxer/internal/infra/terminalwork/workstore"
	"github.com/aiproxer/aiproxer/internal/testkit"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
	"github.com/uptrace/bun"
	"go.uber.org/goleak"
)

const pooledWorkOpenTimeout = 2 * time.Minute

var (
	pooledWorkSchemaOnce sync.Once
	pooledWorkSchemaErr  error

	pooledWorkRuntimeOnce sync.Once
	pooledWorkRuntimeDB   *bun.DB
	pooledWorkRuntimeErr  error
	pooledWorkSharedGuard = testkit.NewRuntimeSQLGuard()
	pooledWorkTestMu      sync.Mutex
)

func TestMain(m *testing.M) {
	code := m.Run()
	if pooledWorkRuntimeDB != nil {
		_ = pooledWorkRuntimeDB.Close()
	}
	if code == 0 {
		if err := goleak.Find(); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "goleak: %v\n", err)
			code = 1
		}
	}
	os.Exit(code)
}

func TestPostgresPooled_Phase43Contracts(t *testing.T) {
	adminDSN, runtimeDSN := testkit.SkipUnlessPostgresPooled(t)
	holdPooledWorkTestLock(t)
	open := func(t *testing.T, storeID string) phase43Store {
		t.Helper()
		store, guard := openSharedPooledWorkStore(t, adminDSN, runtimeDSN, storeID)
		t.Cleanup(func() { guard.AssertNoViolations(t) })
		return store
	}
	runPhase43WorkStoreContracts(t, phase43Adapter{
		name:     "postgres-pooled",
		open:     open,
		openPeer: open,
		reopen:   open,
		uniqueID: testkit.UniquePostgresStoreID,
	})
}

func TestPostgresPooled_IntentReplayAfterRuntimeOpen(t *testing.T) {
	adminDSN, runtimeDSN := testkit.SkipUnlessPostgresPooled(t)
	holdPooledWorkTestLock(t)
	storeID := testkit.UniquePostgresStoreID("pg-pooled-work")
	store, guard := openSharedPooledWorkStore(t, adminDSN, runtimeDSN, storeID)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	rec := sampleRecord("w-pooled", "sk-pooled", "prov-a", sdk.WorkKindSettleRequestProvider)
	if err := store.AppendIntent(ctx, rec); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := store.AppendIntent(ctx, rec); err != nil {
		t.Fatalf("replay: %v", err)
	}
	got, err := store.GetBySourceKey(ctx, rec.SourceKey)
	if err != nil {
		t.Fatal(err)
	}
	if !terminalwork.SameIntentReplay(rec, got) {
		t.Fatalf("got=%+v", got)
	}
	guard.AssertNoViolations(t)
}

func TestPostgresPooled_ClaimDueUsesRuntimePool(t *testing.T) {
	adminDSN, runtimeDSN := testkit.SkipUnlessPostgresPooled(t)
	holdPooledWorkTestLock(t)
	storeID := testkit.UniquePostgresStoreID("pg-pooled-work-claim")
	store, guard := openSharedPooledWorkStore(t, adminDSN, runtimeDSN, storeID)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	rec := sampleRecord("w-claim-pooled", "sk-claim-pooled", "prov-a", sdk.WorkKindReleaseAttemptProvider)
	if err := store.AppendIntent(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if err := store.PromotePending(ctx, workstore.PromotePendingCommand{WorkID: rec.WorkID, Now: now}); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimDue(ctx, workstore.ClaimDueCommand{
		OwnerID: "worker-pooled",
		TTL:     time.Minute,
		Limit:   1,
		Now:     now,
	})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim err=%v len=%d", err, len(claimed))
	}
	guard.AssertNoViolations(t)
}

func ensurePooledWorkSchema(t *testing.T, adminDSN string) {
	t.Helper()
	pooledWorkSchemaOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), pooledWorkOpenTimeout)
		defer cancel()
		admin, err := testkit.OpenPostgresBun(adminDSN, 2)
		if err != nil {
			pooledWorkSchemaErr = err
			return
		}
		bootstrap, err := workstore.NewDurableStore(ctx, admin, workstore.DurableConfig{
			StoreID: testkit.UniquePostgresStoreID("pg-work-schema"),
		})
		if err != nil {
			_ = admin.Close()
			pooledWorkSchemaErr = err
			return
		}
		pooledWorkSchemaErr = bootstrap.Close()
	})
	if pooledWorkSchemaErr != nil {
		t.Fatalf("package admin schema bootstrap: %v", pooledWorkSchemaErr)
	}
}

func sharedPooledWorkRuntime(t *testing.T, runtimeDSN string) *bun.DB {
	t.Helper()
	pooledWorkRuntimeOnce.Do(func() {
		db, err := testkit.OpenPostgresBun(runtimeDSN, 4)
		if err != nil {
			pooledWorkRuntimeErr = err
			return
		}
		db.AddQueryHook(pooledWorkSharedGuard)
		pooledWorkRuntimeDB = db
	})
	if pooledWorkRuntimeErr != nil {
		t.Fatalf("package shared runtime pool: %v", pooledWorkRuntimeErr)
	}
	return pooledWorkRuntimeDB
}

func holdPooledWorkTestLock(t *testing.T) {
	t.Helper()
	pooledWorkTestMu.Lock()
	t.Cleanup(pooledWorkTestMu.Unlock)
}

func openSharedPooledWorkStore(t *testing.T, adminDSN, runtimeDSN, storeID string) (*workstore.DurableStore, *testkit.RuntimeSQLGuard) {
	t.Helper()
	ensurePooledWorkSchema(t, adminDSN)
	t.Cleanup(func() {
		testkit.CleanupPostgresStoreByID(t, adminDSN, storeID, testkit.PostgresComponentTerminalWork)
	})

	pooledWorkSharedGuard.Reset()
	runtime := sharedPooledWorkRuntime(t, runtimeDSN)
	ctx, cancel := context.WithTimeout(t.Context(), pooledWorkOpenTimeout)
	defer cancel()
	if err := workstore.VerifySchema(ctx, runtime); err != nil {
		t.Fatalf("verify runtime schema: %v", err)
	}
	store, err := workstore.OpenStore(ctx, runtime, workstore.DurableConfig{StoreID: storeID})
	if err != nil {
		t.Fatalf("open pooled runtime store: %v", err)
	}
	pooledWorkSharedGuard.AssertNoViolations(t)
	return store, pooledWorkSharedGuard
}
