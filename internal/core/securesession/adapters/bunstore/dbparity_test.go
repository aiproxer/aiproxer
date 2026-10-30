package bunstore_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/bunstore"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/securesession/storecontract"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

var bunstoreParityMemSeq atomic.Int64

type bunstoreSQLiteFixture struct{}

func (f *bunstoreSQLiteFixture) NewStore(t *testing.T) app.Store {
	t.Helper()
	id := bunstoreParityMemSeq.Add(1)
	dsn := fmt.Sprintf("file:bunstoreparity%d?mode=memory&cache=shared&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)", id)
	sqlDB, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
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
}

func (f *bunstoreSQLiteFixture) ReopenStore(t *testing.T) (app.Store, func() app.Store) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, fmt.Sprintf("reopen_%d.db", bunstoreParityMemSeq.Add(1)))
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"

	var current *bunstore.Store
	open := func() app.Store {
		sqlDB, err := sql.Open("sqlite", dsn)
		require.NoError(t, err)
		sqlDB.SetMaxOpenConns(1)
		bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
		if err != nil {
			_ = sqlDB.Close()
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), db.DefaultPostgresOpenMigrateTimeout)
		defer cancel()
		st, err := bunstore.NewWithContext(ctx, bunDB)
		if err != nil {
			_ = sqlDB.Close()
			t.Fatal(err)
		}
		current = st
		return st
	}

	s1 := open()
	t.Cleanup(func() {
		if current != nil {
			_ = current.Close()
		}
	})

	reopen := func() app.Store {
		if current != nil {
			_ = current.Close()
			current = nil
		}
		return open()
	}
	return s1, reopen
}

// TestDBParity_SQLite is the canonical parity entry point for securesession/bunstore on SQLite.
func TestDBParity_SQLite(t *testing.T) {
	t.Parallel()
	storecontract.RunParitySuite(t, &bunstoreSQLiteFixture{})

	t.Run("MigrationAndSchemaParity", func(t *testing.T) {
		id := bunstoreParityMemSeq.Add(1)
		dsn := fmt.Sprintf("file:bunstoreschemaparity%d?mode=memory&cache=shared&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)", id)
		sqlDB, err := sql.Open("sqlite", dsn)
		require.NoError(t, err)
		sqlDB.SetMaxOpenConns(1)
		bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
		require.NoError(t, err)
		defer func() { _ = bunDB.Close() }()

		ctx, cancel := context.WithTimeout(context.Background(), db.DefaultPostgresOpenMigrateTimeout)
		defer cancel()
		_, err = bunstore.NewWithContext(ctx, bunDB)
		require.NoError(t, err)

		_, thisFile, _, ok := runtime.Caller(0)
		require.True(t, ok)
		runSecureSessionMigrationAndSchemaParity(t, bunDB, filepath.Dir(thisFile))
	})
}
