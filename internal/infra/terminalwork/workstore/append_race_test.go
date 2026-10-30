package workstore_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/terminalwork/workstore"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

func TestSQLiteStore_ConcurrentSameKeyAppendIntentIsIdempotent(t *testing.T) {
	t.Parallel()
	store := newSQLiteWorkMultiConn(t)
	ctx := context.Background()
	rec := sampleRecord("w-sqlite-race", "sk-sqlite-race", "prov-a", sdk.WorkKindSettleRequestProvider)

	const workers = 8
	var wg sync.WaitGroup
	errs := make([]error, workers)
	start := make(chan struct{})
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = retryAppendIntentBusy(func() error {
				return store.AppendIntent(ctx, rec)
			})
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: %v", i, err)
		}
	}
	got, err := store.GetBySourceKey(ctx, rec.SourceKey)
	if err != nil {
		t.Fatal(err)
	}
	if got.WorkID != rec.WorkID {
		t.Fatalf("work_id=%q want %q", got.WorkID, rec.WorkID)
	}
}

func newSQLiteWorkMultiConn(t *testing.T) *workstore.DurableStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "terminal-work-race.db")
	sqlDB, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if _, err := sqlDB.ExecContext(context.Background(), `PRAGMA busy_timeout=5000; PRAGMA journal_mode=WAL`); err != nil {
		t.Fatal(err)
	}
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	store, err := workstore.NewDurableStore(context.Background(), bunDB, workstore.DurableConfig{StoreID: "sqlite-race"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
