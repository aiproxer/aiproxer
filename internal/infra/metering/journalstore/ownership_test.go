package journalstore

import (
	"context"
	"database/sql"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/db"
	_ "modernc.org/sqlite"
)

func TestOpenStoreCloseDoesNotCloseInjectedDB(t *testing.T) {
	sqlDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bunDB.Close() })
	if err := Migrate(t.Context(), bunDB); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(t.Context(), bunDB, DurableConfig{StoreID: "non-owning"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := bunDB.PingContext(context.Background()); err != nil {
		t.Fatalf("OpenStore.Close closed injected DB: %v", err)
	}
}
