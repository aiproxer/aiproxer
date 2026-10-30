package runtimebundle

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/uptrace/bun"
	_ "modernc.org/sqlite"
)

func openMemoryBun(t *testing.T) *bun.DB {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatal(err)
	}
	return bunDB
}

func TestOpenPostgresStoreRejectsTransactionPoolSearchPathBeforeOpen(t *testing.T) {
	var opens atomic.Int32
	registry := db.NewPoolRegistry(func(context.Context, string, db.PoolSettings) (*bun.DB, error) {
		opens.Add(1)
		return openMemoryBun(t), nil
	})
	t.Cleanup(func() { _ = registry.Close(context.Background()) })

	_, _, err := openPostgresStore(t.Context(), "postgres://runtime/db?search_path=tenant", db.PoolSettings{}, config.DatabaseConfig{
		ConnectionMode: config.DatabaseConnectionModeTransactionPool,
		SchemaMode:     config.DatabaseSchemaModeVerifyOnly,
	}, registry, nil, postgresStoreLifecycle[*struct{}]{
		Verify: func(context.Context, *bun.DB) error {
			t.Fatal("Verify must not run after DSN rejection")
			return nil
		},
		Open: func(context.Context, *bun.DB) (*struct{}, error) {
			t.Fatal("Open must not run after DSN rejection")
			return nil, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "search_path") {
		t.Fatalf("error=%v want search_path rejection", err)
	}
	if opens.Load() != 0 {
		t.Fatalf("opens=%d want 0", opens.Load())
	}
}

func TestOpenPostgresStoreRegistryAutoMigrateSkipsPerStoreAdminMigrate(t *testing.T) {
	orig := openPostgresBun
	t.Cleanup(func() { openPostgresBun = orig })

	var adminOpens atomic.Int32
	var migrateCalls atomic.Int32
	openPostgresBun = func(context.Context, string, db.PoolSettings) (*bun.DB, error) {
		adminOpens.Add(1)
		return openMemoryBun(t), nil
	}

	registry := db.NewPoolRegistry(func(context.Context, string, db.PoolSettings) (*bun.DB, error) {
		return openMemoryBun(t), nil
	})
	t.Cleanup(func() { _ = registry.Close(context.Background()) })

	store, closer, err := openPostgresStore(t.Context(), "postgres://runtime/db", db.PoolSettings{}, config.DatabaseConfig{
		ConnectionMode: config.DatabaseConnectionModeDirect,
		SchemaMode:     config.DatabaseSchemaModeAutoMigrate,
	}, registry, nil, postgresStoreLifecycle[*struct{}]{
		Migrate: func(context.Context, *bun.DB) error {
			migrateCalls.Add(1)
			return nil
		},
		Verify: func(context.Context, *bun.DB) error { return nil },
		Open: func(context.Context, *bun.DB) (*struct{}, error) {
			return &struct{}{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if store == nil || closer == nil {
		t.Fatal("expected store and closer")
	}
	if adminOpens.Load() != 0 {
		t.Fatalf("admin opens=%d want 0 (nil migrator skips admin migrate)", adminOpens.Load())
	}
	if migrateCalls.Load() != 0 {
		t.Fatalf("migrate calls=%d want 0 on registry open path", migrateCalls.Load())
	}
	if registry.Len() != 1 {
		t.Fatalf("Len=%d want 1 claimed pool", registry.Len())
	}
}

func TestOpenPostgresStoreNonRegistryVerifyFailureClosesDB(t *testing.T) {
	orig := openPostgresBun
	t.Cleanup(func() { openPostgresBun = orig })

	var opened *bun.DB
	openPostgresBun = func(context.Context, string, db.PoolSettings) (*bun.DB, error) {
		opened = openMemoryBun(t)
		return opened, nil
	}

	var migrateCalls atomic.Int32
	_, _, err := openPostgresStore(t.Context(), "postgres://runtime/db", db.PoolSettings{}, config.DatabaseConfig{
		ConnectionMode: config.DatabaseConnectionModeDirect,
		SchemaMode:     config.DatabaseSchemaModeAutoMigrate,
	}, nil, nil, postgresStoreLifecycle[*struct{}]{
		Migrate: func(context.Context, *bun.DB) error {
			migrateCalls.Add(1)
			return nil
		},
		Verify: func(context.Context, *bun.DB) error {
			return context.Canceled
		},
		Open: func(context.Context, *bun.DB) (*struct{}, error) {
			t.Fatal("Open must not run after verify failure")
			return nil, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "verify schema") {
		t.Fatalf("error=%v want verify schema failure", err)
	}
	if migrateCalls.Load() != 1 {
		t.Fatalf("migrate calls=%d want 1", migrateCalls.Load())
	}
	if opened == nil {
		t.Fatal("opener was not called")
	}
	if pingErr := opened.PingContext(t.Context()); pingErr == nil {
		t.Fatal("non-registry verify failure must close the DB")
	}
}

func TestMigratePostgresAdminJoinsMigrateAndCloseErrors(t *testing.T) {
	origOpen := openPostgresBun
	origClose := closePostgresBun
	t.Cleanup(func() {
		openPostgresBun = origOpen
		closePostgresBun = origClose
	})

	migrateErr := errors.New("migrate failed")
	closeErr := errors.New("close failed")

	tests := []struct {
		name          string
		openErr       error
		migrateErr    error
		closeErr      error
		wantMigrate   bool
		wantContains  []string
		wantIsMigrate bool
		wantIsClose   bool
	}{
		{
			name:         "open fails before migrate",
			openErr:      errors.New("dial refused"),
			wantMigrate:  false,
			wantContains: []string{"open admin postgres", "dial refused"},
		},
		{
			name:          "migrate fails close ok",
			migrateErr:    migrateErr,
			wantMigrate:   true,
			wantIsMigrate: true,
			wantContains:  []string{"migrate postgres schema"},
		},
		{
			name:         "migrate ok close fails",
			closeErr:     closeErr,
			wantMigrate:  true,
			wantIsClose:  true,
			wantContains: []string{"close admin postgres"},
		},
		{
			name:          "both fail joined",
			migrateErr:    migrateErr,
			closeErr:      closeErr,
			wantMigrate:   true,
			wantIsMigrate: true,
			wantIsClose:   true,
			wantContains:  []string{"migrate postgres schema", "close admin postgres"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var migrateCalls atomic.Int32
			openPostgresBun = func(context.Context, string, db.PoolSettings) (*bun.DB, error) {
				if tt.openErr != nil {
					return nil, tt.openErr
				}
				return openMemoryBun(t), nil
			}
			closePostgresBun = func(handle *bun.DB) error {
				if handle != nil {
					_ = handle.Close()
				}
				return tt.closeErr
			}
			err := migratePostgresAdmin(t.Context(), "postgres://admin/db", func(context.Context, *bun.DB) error {
				migrateCalls.Add(1)
				return tt.migrateErr
			})
			if tt.openErr == nil && tt.migrateErr == nil && tt.closeErr == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected error")
			}
			gotMigrate := migrateCalls.Load() > 0
			if gotMigrate != tt.wantMigrate {
				t.Fatalf("migrate called=%v want %v", gotMigrate, tt.wantMigrate)
			}
			if tt.wantIsMigrate && !errors.Is(err, migrateErr) {
				t.Fatalf("error=%v want migrateErr", err)
			}
			if tt.wantIsClose && !errors.Is(err, closeErr) {
				t.Fatalf("error=%v want closeErr", err)
			}
			for _, frag := range tt.wantContains {
				if !strings.Contains(err.Error(), frag) {
					t.Fatalf("error=%v want containing %q", err, frag)
				}
			}
		})
	}
}
