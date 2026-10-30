package bunstore

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/testkit/dbparity"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/stretchr/testify/require"
)

func copyLegacyFixture(t *testing.T, testSourceFile string) string {
	t.Helper()
	src := filepath.Join(filepath.Dir(testSourceFile), "testdata", "legacy_sqlite.db")
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close() }()
	dst := filepath.Join(t.TempDir(), "legacy_sqlite.db")
	out, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	return dst
}

func TestLegacySQLiteFixture_ReadCompat(t *testing.T) {
	t.Parallel()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := copyLegacyFixture(t, thisFile)
	ctx, cancel := context.WithTimeout(context.Background(), db.DefaultPostgresOpenMigrateTimeout)
	defer cancel()
	bunDB, err := db.OpenSQLiteBun(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewWithContext(ctx, bunDB)
	if err != nil {
		_ = bunDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// Verify logical schema invariants after upgrade
	require.NoError(t, dbparity.VerifySQLiteSchema(ctx, bunDB, ContinuityLogicalSchemaSpec()))

	// Verify all migrations applied after upgrade
	discovered, err := dbparity.DiscoverMigrations(filepath.Dir(thisFile))
	require.NoError(t, err)
	migRows, err := bunDB.QueryContext(ctx, "SELECT name FROM bun_continuity_migrations")
	require.NoError(t, err)
	defer func() { _ = migRows.Close() }()
	recorded := make(map[string]bool)
	for migRows.Next() {
		var name string
		require.NoError(t, migRows.Scan(&name))
		id := name
		if len(name) >= 14 {
			id = name[:14]
		}
		recorded[id] = true
	}
	require.NoError(t, dbparity.AssertMigrationHistoryIDs(dbparity.MigrationIDs(discovered), recorded))

	got, err := s.ResolveALeg(ctx, "legacy-ck")
	if err != nil {
		t.Fatalf("ResolveALeg: %v", err)
	}
	if got.ContinuityKey != "legacy-ck" {
		t.Fatalf("continuity_key: got %q", got.ContinuityKey)
	}
	if got.ALegID == "" {
		t.Fatal("expected non-empty a_leg_id")
	}
	rows, err := s.LoadAttempts(ctx, got.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("attempts: got %d want 1", len(rows))
	}
	if rows[0].BackendID != "stub" || rows[0].Outcome != aipapi.AttemptSuccess {
		t.Fatalf("attempt row: %+v", rows[0])
	}
}
