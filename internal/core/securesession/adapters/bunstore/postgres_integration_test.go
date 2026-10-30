//go:build integration

package bunstore

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/testkit"
)

func TestPostgres_migrateTwice_idempotent(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), db.DefaultPostgresOpenMigrateTimeout)
	defer cancel()
	poolCfg, err := config.ParseDatabasePoolSettings(config.DatabaseConfig{MaxOpenConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	pool := db.PoolSettings{
		MaxOpenConns:    poolCfg.MaxOpenConns,
		MaxIdleConns:    poolCfg.MaxIdleConns,
		ConnMaxLifetime: poolCfg.ConnMaxLifetime,
		ConnMaxIdleTime: poolCfg.ConnMaxIdleTime,
	}
	bunDB, err := db.OpenPostgresBun(ctx, dsn, pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bunDB.Close() })
	s, err := NewWithContext(ctx, bunDB)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := runSecureSessionSchemaMigrate(ctx, bunDB); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	var applied int
	if err := bunDB.NewRaw(
		`SELECT count(*) FROM bun_securesession_migrations WHERE name = ?`, BaselineMigrationName,
	).Scan(ctx, &applied); err != nil {
		t.Fatal(err)
	}
	if applied != 1 {
		t.Fatalf("expected one applied baseline migration row, got %d", applied)
	}
}
