package storecontract_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/bunstore"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/securesession/storecontract"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	_ "modernc.org/sqlite"
)

func TestStoreContract_Quarantine_Memory(t *testing.T) {
	t.Parallel()
	storecontract.RunQuarantineContracts(t, func(*testing.T) app.Store {
		return memory.New(memory.Options{SimulateDurable: true})
	})
}

func TestStoreContract_Quarantine_SQLite(t *testing.T) {
	t.Parallel()
	storecontract.RunQuarantineContracts(t, func(t *testing.T) app.Store {
		t.Helper()
		dir, err := os.MkdirTemp("", "securesession-quarantine-")
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

var bunQuarantineMemSeq atomic.Int64

func TestStoreContract_Quarantine_BunSQLite(t *testing.T) {
	t.Parallel()
	storecontract.RunQuarantineContracts(t, func(t *testing.T) app.Store {
		t.Helper()
		id := bunQuarantineMemSeq.Add(1)
		dsn := fmt.Sprintf("file:memquarantine%d?mode=memory&cache=shared&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)", id)
		sqlDB, err := sql.Open("sqlite", dsn)
		if err != nil {
			t.Fatal(err)
		}
		sqlDB.SetMaxOpenConns(1)
		bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
		if err != nil {
			_ = sqlDB.Close()
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), db.DefaultPostgresOpenMigrateTimeout)
		defer cancel()
		s, err := bunstore.NewWithContext(ctx, bunDB)
		if err != nil {
			_ = sqlDB.Close()
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	})
}
