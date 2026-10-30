package storecontract_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/bunstore"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/securesession/storecontract"
	"github.com/aiproxer/aiproxer/internal/infra/db"
)

func TestStoreContract_SQLite(t *testing.T) {
	t.Parallel()
	storecontract.RunAll(t, func(t *testing.T) app.Store {
		t.Helper()
		dir, err := os.MkdirTemp("", "securesession-storecontract-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		path := filepath.Join(dir, "store.db")
		ctx, cancel := context.WithTimeout(context.Background(), db.DefaultPostgresOpenMigrateTimeout)
		defer cancel()
		bunDB, err := db.OpenSQLiteBun(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		s, err := bunstore.NewWithContext(ctx, bunDB)
		if err != nil {
			_ = bunDB.Close()
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	})
}
