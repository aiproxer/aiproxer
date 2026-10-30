package journalstore_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

var memSQLiteDSN atomic.Int64

// memorySQLiteDSN returns a uniquely named shared-cache in-memory SQLite DSN
// for tests that never close and reopen the database file; restart-persistence
// tests must stay file-backed. The "jsx-" prefix keeps names disjoint from the
// internal test package's helper within the shared test binary.
func memorySQLiteDSN() string {
	return fmt.Sprintf("file:jsx-mem-%d?mode=memory&cache=shared&_pragma=busy_timeout(5000)", memSQLiteDSN.Add(1))
}

func TestSQLiteStore_AppendIdempotentMoneyAndList(t *testing.T) {
	t.Parallel()
	store := newSQLiteJournal(t)
	ctx := context.Background()
	f := validFact("fact-sql-1", "stream-sql", 1)
	f.Money = &metering.MoneyObservation{NanoUnits: 7, Currency: "USD", Present: true, Source: metering.SourceProviderReported}
	if err := store.Append(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(ctx, f); err != nil {
		t.Fatalf("idempotent: %v", err)
	}
	collide := f
	collide.Sequence = 9
	if err := store.Append(ctx, collide); !errors.Is(err, journalstore.ErrIdentityCollision) {
		t.Fatalf("got %v", err)
	}
	page, err := store.List(ctx, metering.Query{StreamID: "stream-sql", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Facts) != 1 {
		t.Fatalf("len=%d", len(page.Facts))
	}
	if page.Facts[0].Money == nil || page.Facts[0].Money.NanoUnits != 7 {
		t.Fatalf("money=%+v", page.Facts[0].Money)
	}
	if err := store.CheckReadiness(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteStore_AppendRejectsSameIdentityDifferentContent(t *testing.T) {
	t.Parallel()
	store := newSQLiteJournal(t)
	ctx := context.Background()
	f := validFact("fact-sql-content", "stream-sql-content", 1)
	f.IdentityVersion = 1
	f.SourceEventKind = "usage-event"
	f.SourceID = f.FactID
	if err := store.Append(ctx, f); err != nil {
		t.Fatal(err)
	}

	diffKind := f
	diffKind.Kind = metering.FactKindDelta
	if err := store.Append(ctx, diffKind); !errors.Is(err, journalstore.ErrIdentityCollision) {
		t.Fatalf("same SourceEventKey different Kind collision got %v", err)
	}

	diffPayload := f
	diffPayload.Money = &metering.MoneyObservation{NanoUnits: 1, Currency: "USD", Present: true, Source: metering.SourceProviderReported}
	if err := store.Append(ctx, diffPayload); !errors.Is(err, journalstore.ErrIdentityCollision) {
		t.Fatalf("different Money collision got %v", err)
	}

	if err := store.Append(ctx, f); err != nil {
		t.Fatalf("identical content must stay idempotent: %v", err)
	}
}

func newSQLiteJournal(t *testing.T) *journalstore.DurableStore {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", memorySQLiteDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	store, err := journalstore.NewDurableStore(ctx, bunDB, journalstore.DurableConfig{StoreID: "sqlite-test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
