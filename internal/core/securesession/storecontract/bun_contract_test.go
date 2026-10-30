package storecontract_test

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/bunstore"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/securesession/storecontract"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	_ "modernc.org/sqlite"
)

var bunContractMemSeq atomic.Int64

func TestStoreContract_BunSQLite(t *testing.T) {
	t.Parallel()
	storecontract.RunAll(t, func(t *testing.T) app.Store {
		t.Helper()
		id := bunContractMemSeq.Add(1)
		dsn := fmt.Sprintf("file:memcontract%d?mode=memory&cache=shared&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)", id)
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
