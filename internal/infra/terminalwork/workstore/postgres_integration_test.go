//go:build integration

package workstore_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/terminalwork/workstore"
	"github.com/aiproxer/aiproxer/internal/testkit"
)

var (
	directWorkSchemaOnce sync.Once
	directWorkSchemaErr  error
)

func TestPostgresStore_Phase43Contracts(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	storeID := testkit.UniquePostgresStoreID("pg-work")
	t.Cleanup(func() {
		testkit.CleanupPostgresStoreByID(t, adminDSNForCleanup(dsn), storeID, testkit.PostgresComponentTerminalWork)
	})
	open := func(t *testing.T, id string) phase43Store {
		t.Helper()
		return newPostgresWorkStore(t, dsn, id)
	}
	runPhase43WorkStoreContracts(t, phase43Adapter{
		name:     "postgres",
		open:     open,
		openPeer: open,
		reopen:   open,
		uniqueID: testkit.UniquePostgresStoreID,
	})
}

func TestPostgresStore_VerifySchemaDirect(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ensureDirectWorkSchema(t, dsn)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	admin, err := testkit.OpenPostgresBun(dsn, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close() }()
	if err := workstore.VerifySchema(ctx, admin); err != nil {
		t.Fatal(err)
	}
}

func adminDSNForCleanup(runtimeDSN string) string {
	if admin, ok := testkit.PostgresAdminDSN(); ok {
		return admin
	}
	return runtimeDSN
}

func ensureDirectWorkSchema(t *testing.T, dsn string) {
	t.Helper()
	directWorkSchemaOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		bunDB, err := testkit.OpenPostgresBun(dsn, 2)
		if err != nil {
			directWorkSchemaErr = err
			return
		}
		store, err := workstore.NewDurableStore(ctx, bunDB, workstore.DurableConfig{
			StoreID: testkit.UniquePostgresStoreID("pg-work-direct-schema"),
		})
		if err != nil {
			_ = bunDB.Close()
			directWorkSchemaErr = err
			return
		}
		directWorkSchemaErr = store.Close()
	})
	if directWorkSchemaErr != nil {
		t.Fatalf("direct schema bootstrap: %v", directWorkSchemaErr)
	}
}

func newPostgresWorkStore(t *testing.T, dsn, storeID string) *workstore.DurableStore {
	t.Helper()
	ensureDirectWorkSchema(t, dsn)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	db, err := testkit.OpenPostgresBun(dsn, 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := workstore.OpenStore(ctx, db, workstore.DurableConfig{StoreID: storeID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
