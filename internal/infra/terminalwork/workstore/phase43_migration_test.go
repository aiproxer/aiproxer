package workstore_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/terminalwork/workstore"
)

func TestPhase43_MigrateAndVerifySchema_SQLite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "phase43-migrate.db")
	sqlDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bunDB.Close() })
	ctx := context.Background()
	if err := workstore.Migrate(ctx, bunDB); err != nil {
		t.Fatal(err)
	}
	if err := workstore.VerifySchema(ctx, bunDB); err != nil {
		t.Fatal(err)
	}
	for _, name := range workstore.RequiredMigrationNames {
		var n int
		if err := bunDB.NewRaw(
			`SELECT COUNT(1) FROM bun_terminal_work_migrations WHERE name = ?`, name,
		).Scan(ctx, &n); err != nil {
			t.Fatal(err)
		}
		if n < 1 {
			t.Fatalf("missing migration %s", name)
		}
	}
}
