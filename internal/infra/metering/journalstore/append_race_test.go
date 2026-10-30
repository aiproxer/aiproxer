package journalstore_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// TestSQLiteStore_ConcurrentSameKeyAppendIsIdempotent races identical Appends.
// The UNIQUE(source_event_key) loser must resolve via replay, not a raw insert error.
func TestSQLiteStore_ConcurrentSameKeyAppendIsIdempotent(t *testing.T) {
	t.Parallel()
	store := newSQLiteJournalMultiConn(t)
	ctx := context.Background()

	const workers = 10
	const rounds = 20
	for round := range rounds {
		fact := validFact("fact-race-"+strconv.Itoa(round), "stream-race-shared", int64(round+1))

		var wg sync.WaitGroup
		errs := make([]error, workers)
		start := make(chan struct{})
		for i := range workers {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				errs[i] = appendIdempotentRetry(ctx, store, fact)
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

	page, err := store.List(ctx, metering.Query{StreamID: "stream-race-shared", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Facts) != rounds {
		t.Fatalf("facts=%d want %d", len(page.Facts), rounds)
	}
}

// appendIdempotentRetry retries only transient SQLite busy/cancellation outcomes so
// the replay invariant is asserted without depending on wall-clock contention under
// machine-wide parallel test load. Any other error fails immediately.
func appendIdempotentRetry(ctx context.Context, store *journalstore.DurableStore, fact metering.Fact) error {
	const budget = 10 * time.Second
	deadline := time.Now().Add(budget)
	backoff := 10 * time.Millisecond
	for {
		err := store.Append(ctx, fact)
		if err == nil {
			return nil
		}
		if !errors.Is(err, journalstore.ErrSQLiteBusyRetryExhausted) && !errors.Is(err, journalstore.ErrSQLiteRetryCanceled) {
			return err
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(backoff)
		if backoff < 250*time.Millisecond {
			backoff *= 2
		}
	}
}

func newSQLiteJournalMultiConn(t *testing.T) *journalstore.DurableStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "metering-race.db")
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
	store, err := journalstore.NewDurableStore(context.Background(), bunDB, journalstore.DurableConfig{StoreID: "sqlite-race"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
