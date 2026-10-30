//go:build integration

package journalstore_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/uptrace/bun"
	"go.uber.org/goleak"
)

const pooledJournalOpenTimeout = 2 * time.Minute

var (
	pooledJournalSchemaOnce sync.Once
	pooledJournalSchemaErr  error

	pooledJournalRuntimeOnce sync.Once
	pooledJournalRuntimeDB   *bun.DB
	pooledJournalRuntimeErr  error
	pooledJournalSharedGuard = testkit.NewRuntimeSQLGuard()
	// pooledJournalTestMu serializes shared-pool tests so Reset/Assert cannot cross-talk
	// if a test accidentally calls t.Parallel(). Re-entrant for the same *testing.T so
	// open + openPeer (store_id_isolation) does not deadlock.
	pooledJournalTestMu reentrantTBMutex
)

// reentrantTBMutex serializes tests that share package-level pooled runtime
// state, while allowing the same *testing.T to nest acquire (e.g. open + openPeer).
type reentrantTBMutex struct {
	mu    sync.Mutex
	meta  sync.Mutex
	owner *testing.T
	depth int
}

func (r *reentrantTBMutex) Lock(t *testing.T) {
	t.Helper()
	r.meta.Lock()
	if r.owner == t {
		r.depth++
		r.meta.Unlock()
		t.Cleanup(r.release)
		return
	}
	r.meta.Unlock()

	r.mu.Lock()
	r.meta.Lock()
	r.owner = t
	r.depth = 1
	r.meta.Unlock()
	t.Cleanup(r.release)
}

func (r *reentrantTBMutex) release() {
	r.meta.Lock()
	r.depth--
	done := r.depth == 0
	if done {
		r.owner = nil
	}
	r.meta.Unlock()
	if done {
		r.mu.Unlock()
	}
}

func TestMain(m *testing.M) {
	code := m.Run()
	if pooledJournalRuntimeDB != nil {
		_ = pooledJournalRuntimeDB.Close()
	}
	if code == 0 {
		if err := goleak.Find(); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "goleak: %v\n", err)
			code = 1
		}
	}
	os.Exit(code)
}

func ensurePooledJournalSchema(t *testing.T, adminDSN string) {
	t.Helper()
	pooledJournalSchemaOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), pooledJournalOpenTimeout)
		defer cancel()
		admin, err := testkit.OpenPostgresBun(adminDSN, 2)
		if err != nil {
			pooledJournalSchemaErr = err
			return
		}
		bootstrap, err := journalstore.NewDurableStore(ctx, admin, journalstore.DurableConfig{
			StoreID: testkit.UniquePostgresStoreID("pg-journal-schema"),
		})
		if err != nil {
			_ = admin.Close()
			pooledJournalSchemaErr = err
			return
		}
		pooledJournalSchemaErr = bootstrap.Close()
	})
	if pooledJournalSchemaErr != nil {
		t.Fatalf("package admin schema bootstrap: %v", pooledJournalSchemaErr)
	}
}

func sharedPooledJournalRuntime(t *testing.T, runtimeDSN string) *bun.DB {
	t.Helper()
	pooledJournalRuntimeOnce.Do(func() {
		db, err := testkit.OpenPostgresBun(runtimeDSN, 2)
		if err != nil {
			pooledJournalRuntimeErr = err
			return
		}
		db.AddQueryHook(pooledJournalSharedGuard)
		pooledJournalRuntimeDB = db
	})
	if pooledJournalRuntimeErr != nil {
		t.Fatalf("package shared runtime pool: %v", pooledJournalRuntimeErr)
	}
	return pooledJournalRuntimeDB
}

func openSharedPooledJournalStore(t *testing.T, adminDSN, runtimeDSN, storeID string) (*journalstore.DurableStore, *testkit.RuntimeSQLGuard) {
	t.Helper()
	pooledJournalTestMu.Lock(t)
	ensurePooledJournalSchema(t, adminDSN)
	t.Cleanup(func() {
		testkit.CleanupPostgresStoreByID(t, adminDSN, storeID, testkit.PostgresComponentJournal)
	})

	pooledJournalSharedGuard.Reset()
	runtime := sharedPooledJournalRuntime(t, runtimeDSN)
	ctx, cancel := context.WithTimeout(t.Context(), pooledJournalOpenTimeout)
	defer cancel()
	if err := journalstore.VerifySchema(ctx, runtime); err != nil {
		t.Fatalf("verify runtime schema: %v", err)
	}
	store, err := journalstore.OpenStore(ctx, runtime, journalstore.DurableConfig{StoreID: storeID})
	if err != nil {
		t.Fatalf("open pooled runtime store: %v", err)
	}
	pooledJournalSharedGuard.AssertNoViolations(t)
	return store, pooledJournalSharedGuard
}
