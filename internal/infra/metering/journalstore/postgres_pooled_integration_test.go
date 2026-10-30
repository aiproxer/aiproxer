//go:build integration

package journalstore_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestPostgresPooled_AppendReplayConflictCorrection(t *testing.T) {
	adminDSN, runtimeDSN := testkit.SkipUnlessPostgresPooled(t)
	storeID := testkit.UniquePostgresStoreID("pg-pooled-journal")
	store, guard := openSharedPooledJournalStore(t, adminDSN, runtimeDSN, storeID)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	stream := storeID + "-stream"
	base := validFact(storeID+"-base", stream, 1)
	if err := store.Append(ctx, base); err != nil {
		t.Fatalf("append base: %v", err)
	}
	if err := store.Append(ctx, base); err != nil {
		t.Fatalf("idempotent replay: %v", err)
	}
	collide := base
	collide.Sequence = 2
	if err := store.Append(ctx, collide); !errors.Is(err, journalstore.ErrIdentityCollision) {
		t.Fatalf("conflict got %v", err)
	}

	corr := validFact(storeID+"-corr", stream, 2)
	corr.Kind = metering.FactKindCorrection
	corr.Supersedes = []string{storeID + "-base"}
	corr.Quantities = []metering.Quantity{{
		Component: metering.ComponentOutputToken,
		Unit:      metering.UnitToken,
		Value:     5,
		Present:   true,
	}}
	if err := store.Append(ctx, corr); err != nil {
		t.Fatalf("correction: %v", err)
	}
	page, err := store.List(ctx, metering.Query{StreamID: stream})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Facts) != 2 {
		t.Fatalf("want append-only history len=2 got %d", len(page.Facts))
	}
	guard.AssertNoViolations(t)
}

func TestPostgresPooled_DMLAfterAdminClose(t *testing.T) {
	adminDSN, runtimeDSN := testkit.SkipUnlessPostgresPooled(t)
	storeID := testkit.UniquePostgresStoreID("pg-pooled-journal-dml")
	store, guard := openSharedPooledJournalStore(t, adminDSN, runtimeDSN, storeID)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	f := validFact(storeID+"-dml", storeID+"-dml-stream", 1)
	if err := store.Append(ctx, f); err != nil {
		t.Fatalf("runtime DML after admin close: %v", err)
	}
	guard.AssertNoViolations(t)
}

func TestPostgresPooled_AppendRejectsSameIdentityDifferentContent(t *testing.T) {
	adminDSN, runtimeDSN := testkit.SkipUnlessPostgresPooled(t)
	storeID := testkit.UniquePostgresStoreID("pg-pooled-journal-content")
	store, guard := openSharedPooledJournalStore(t, adminDSN, runtimeDSN, storeID)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	f := validFact(storeID+"-fact-content", storeID+"-stream-content", 1)
	if err := store.Append(ctx, f); err != nil {
		t.Fatal(err)
	}

	diffKind := f
	diffKind.Kind = metering.FactKindDelta
	if err := store.Append(ctx, diffKind); !errors.Is(err, journalstore.ErrIdentityCollision) {
		t.Fatalf("different Kind collision got %v", err)
	}

	diffPayload := f
	diffPayload.Quantities = []metering.Quantity{{
		Component: metering.ComponentInputToken,
		Unit:      metering.UnitToken,
		Value:     99,
		Present:   true,
	}}
	if err := store.Append(ctx, diffPayload); !errors.Is(err, journalstore.ErrIdentityCollision) {
		t.Fatalf("different Quantities collision got %v", err)
	}

	if err := store.Append(ctx, f); err != nil {
		t.Fatalf("identical content must stay idempotent: %v", err)
	}
	guard.AssertNoViolations(t)
}

func TestPostgresPooled_VerifySchemaFailsWhenRequiredIndexMissing(t *testing.T) {
	adminDSN, runtimeDSN := testkit.SkipUnlessPostgresPooled(t)
	pooledJournalTestMu.Lock(t)

	ensurePooledJournalSchema(t, adminDSN)
	runtime := sharedPooledJournalRuntime(t, runtimeDSN)

	admin := testkit.OpenPostgresBunForTest(t, adminDSN, 2)
	t.Cleanup(func() { _ = admin.Close() })
	t.Cleanup(func() {
		restoreCtx, restoreCancel := context.WithTimeout(context.Background(), pooledJournalOpenTimeout)
		defer restoreCancel()
		_, _ = admin.ExecContext(restoreCtx, `CREATE INDEX IF NOT EXISTS idx_metering_facts_stream_seq
			ON metering_facts(stream_id, sequence)`)
	})

	mutateCtx, mutateCancel := context.WithTimeout(context.Background(), pooledJournalOpenTimeout)
	defer mutateCancel()
	if _, err := admin.ExecContext(mutateCtx, `DROP INDEX IF EXISTS idx_metering_facts_stream_seq`); err != nil {
		t.Fatalf("drop pooled journal index: %v", err)
	}

	verifyCtx, verifyCancel := context.WithTimeout(t.Context(), pooledJournalOpenTimeout)
	defer verifyCancel()
	err := journalstore.VerifySchema(verifyCtx, runtime)
	if err == nil {
		t.Fatal("expected verify_only schema validation to fail when the required index is missing")
	}
	if !strings.Contains(err.Error(), "idx_metering_facts_stream_seq") {
		t.Fatalf("verify error %q does not mention the missing index", err)
	}
}

// TestPostgresPooled_ConcurrentSameKeyAppendIsIdempotent races identical Appends.
// UNIQUE losers must resolve via fresh-read replay (Postgres aborts the insert tx).
func TestPostgresPooled_ConcurrentSameKeyAppendIsIdempotent(t *testing.T) {
	adminDSN, runtimeDSN := testkit.SkipUnlessPostgresPooled(t)
	storeID := testkit.UniquePostgresStoreID("pg-pooled-journal-race")
	ensurePooledJournalSchema(t, adminDSN)
	t.Cleanup(func() {
		testkit.CleanupPostgresStoreByID(t, adminDSN, storeID, testkit.PostgresComponentJournal)
	})

	runtime, err := testkit.OpenPostgresBun(runtimeDSN, 8)
	if err != nil {
		t.Fatalf("open race runtime pool: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	guard := testkit.NewRuntimeSQLGuard()
	runtime.AddQueryHook(guard)

	openCtx, openCancel := context.WithTimeout(t.Context(), pooledJournalOpenTimeout)
	defer openCancel()
	if err := journalstore.VerifySchema(openCtx, runtime); err != nil {
		t.Fatalf("verify runtime schema: %v", err)
	}
	store, err := journalstore.OpenStore(openCtx, runtime, journalstore.DurableConfig{StoreID: storeID})
	if err != nil {
		t.Fatalf("open race store: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	stream := storeID + "-stream"
	const workers = 8
	const rounds = 20
	for round := range rounds {
		fact := validFact(storeID+"-fact-"+strconv.Itoa(round), stream, int64(round+1))
		var wg sync.WaitGroup
		errs := make([]error, workers)
		start := make(chan struct{})
		for i := range workers {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				errs[i] = store.Append(ctx, fact)
			}(i)
		}
		close(start)
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("round %d worker %d: %v", round, i, err)
			}
		}
	}

	page, err := store.List(ctx, metering.Query{StreamID: stream, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Facts) != rounds {
		t.Fatalf("facts=%d want %d", len(page.Facts), rounds)
	}
	guard.AssertNoViolations(t)
}
