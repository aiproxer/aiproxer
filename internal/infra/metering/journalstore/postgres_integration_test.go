//go:build integration

package journalstore_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

var (
	directJournalSchemaOnce sync.Once
	directJournalSchemaErr  error
)

func TestPostgresStore_AppendIdempotent(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	storeID := testkit.UniquePostgresStoreID("pg-journal")
	t.Cleanup(func() {
		testkit.CleanupPostgresStoreByID(t, adminDSNForCleanup(dsn), storeID, testkit.PostgresComponentJournal)
	})
	store := newPostgresJournal(t, dsn, storeID)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	f := validFact(storeID+"-fact-1", storeID+"-stream", 1)
	f.Money = &metering.MoneyObservation{NanoUnits: 9, Currency: "USD", Present: true, Source: metering.SourceProviderReported}
	if err := store.Append(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(ctx, f); err != nil {
		t.Fatal(err)
	}
	collide := f
	collide.Sequence = 2
	if err := store.Append(ctx, collide); !errors.Is(err, journalstore.ErrIdentityCollision) {
		t.Fatalf("got %v", err)
	}
	page, err := store.List(ctx, metering.Query{StreamID: storeID + "-stream"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Facts) != 1 || page.Facts[0].Money == nil || page.Facts[0].Money.NanoUnits != 9 {
		t.Fatalf("page=%+v", page)
	}
}

func TestPostgresStore_AppendRejectsSameIdentityDifferentContent(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	storeID := testkit.UniquePostgresStoreID("pg-journal-content")
	t.Cleanup(func() {
		testkit.CleanupPostgresStoreByID(t, adminDSNForCleanup(dsn), storeID, testkit.PostgresComponentJournal)
	})
	store := newPostgresJournal(t, dsn, storeID)
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
}

func adminDSNForCleanup(runtimeDSN string) string {
	if admin, ok := testkit.PostgresAdminDSN(); ok {
		return admin
	}
	return runtimeDSN
}

func ensureDirectJournalSchema(t *testing.T, dsn string) {
	t.Helper()
	directJournalSchemaOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		bunDB, err := testkit.OpenPostgresBun(dsn, 2)
		if err != nil {
			directJournalSchemaErr = err
			return
		}
		store, err := journalstore.NewDurableStore(ctx, bunDB, journalstore.DurableConfig{
			StoreID: testkit.UniquePostgresStoreID("pg-journal-direct-schema"),
		})
		if err != nil {
			_ = bunDB.Close()
			directJournalSchemaErr = err
			return
		}
		directJournalSchemaErr = store.Close()
	})
	if directJournalSchemaErr != nil {
		t.Fatalf("direct schema bootstrap: %v", directJournalSchemaErr)
	}
}

func newPostgresJournal(t *testing.T, dsn, storeID string) *journalstore.DurableStore {
	t.Helper()
	ensureDirectJournalSchema(t, dsn)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	bunDB := testkit.OpenPostgresBunForTest(t, dsn, 2)
	store, err := journalstore.NewDurableStore(ctx, bunDB, journalstore.DurableConfig{StoreID: storeID})
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
