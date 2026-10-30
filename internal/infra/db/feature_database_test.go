package db

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/featuredb"
)

// openContinuitySQLite creates the continuity database file of a test and
// returns its path. The file must exist for the physical file comparison.
func openContinuitySQLite(t *testing.T, path string) string {
	t.Helper()
	handle, err := OpenSQLiteBun(context.Background(), path)
	if err != nil {
		t.Fatalf("open continuity sqlite: %v", err)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("close continuity sqlite: %v", err)
	}
	return path
}

// TestOpenFeatureDatabaseRefusesTheContinuityFile proves the isolated
// database stays isolated: a request that names the continuity file itself is
// refused before the file is opened, whatever the spelling of the path.
func TestOpenFeatureDatabaseRefusesTheContinuityFile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	continuity := openContinuitySQLite(t, filepath.Join(dir, "continuity.db"))

	relative, err := filepath.Rel(mustWorkingDir(t), continuity)
	if err != nil {
		t.Skipf("the continuity file is not reachable relatively: %v", err)
	}
	symlink := filepath.Join(dir, "continuity-link.db")
	if err := os.Symlink(continuity, symlink); err != nil {
		t.Logf("symlink alias unavailable: %v", err)
		symlink = ""
	}
	hardlink := filepath.Join(dir, "continuity-hard.db")
	if err := os.Link(continuity, hardlink); err != nil {
		t.Logf("hardlink alias unavailable: %v", err)
		hardlink = ""
	}

	for _, tc := range []struct {
		name string
		path string
	}{
		{name: "exact path", path: continuity},
		{name: "relative path", path: relative},
		{name: "symlink alias", path: symlink},
		{name: "hardlink alias", path: hardlink},
	} {
		if tc.path == "" {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			opener := NewFeatureDatabaseOpener(FeatureDatabaseConfig{Store: "sqlite", SQLitePath: continuity})
			opened, err := opener.OpenFeatureDatabase(ctx, featuredb.Spec{
				FeatureID: "proxy-memory", SQLitePath: tc.path,
			})
			if opened.DB != nil {
				t.Cleanup(func() { _ = opened.DB.Close() })
			}
			if err == nil {
				t.Fatal("the isolated feature database must not open the continuity file")
			}
			if !strings.Contains(err.Error(), "must not be the continuity database") {
				t.Fatalf("unexpected rejection: %v", err)
			}
			if strings.Contains(err.Error(), continuity) {
				t.Fatalf("the rejection must not echo the operator path: %v", err)
			}
		})
	}
}

// TestOpenFeatureDatabaseRefusesTheDefaultContinuityFile proves the derived
// default file is refused too: a continuity database named after the feature
// would make the default path the continuity file itself.
func TestOpenFeatureDatabaseRefusesTheDefaultContinuityFile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	continuity := openContinuitySQLite(t, filepath.Join(dir, "proxy-memory.db"))

	opener := NewFeatureDatabaseOpener(FeatureDatabaseConfig{Store: "sqlite", SQLitePath: continuity})
	opened, err := opener.OpenFeatureDatabase(ctx, featuredb.Spec{FeatureID: "proxy-memory"})
	if opened.DB != nil {
		t.Cleanup(func() { _ = opened.DB.Close() })
	}
	if err == nil {
		t.Fatal("the derived default file must not be the continuity file")
	}
}

// TestOpenFeatureDatabaseOpensAnIsolatedFile proves the isolated database
// still opens next to the continuity file and names it canonically.
func TestOpenFeatureDatabaseOpensAnIsolatedFile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	continuity := openContinuitySQLite(t, filepath.Join(dir, "continuity.db"))

	opener := NewFeatureDatabaseOpener(FeatureDatabaseConfig{Store: "sqlite", SQLitePath: continuity})
	opened, err := opener.OpenFeatureDatabase(ctx, featuredb.Spec{FeatureID: "proxy-memory"})
	if err != nil {
		t.Fatalf("open feature database: %v", err)
	}
	t.Cleanup(func() { _ = opened.DB.Close() })
	if opened.DB == nil {
		t.Fatal("the isolated feature database must be opened")
	}
	want := "sqlite|" + filepath.Join(dir, "proxy-memory.db")
	if opened.Identity != want {
		t.Fatalf("identity = %q, want %q", opened.Identity, want)
	}
}

// TestOpenFeatureDatabaseIdentityIsCanonical proves two spellings of one
// file name one database, so the process opens it once.
func TestOpenFeatureDatabaseIdentityIsCanonical(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	isolated := filepath.Join(dir, "memory.db")

	opener := NewFeatureDatabaseOpener(FeatureDatabaseConfig{Store: "sqlite", SQLitePath: filepath.Join(dir, "continuity.db")})
	first, err := opener.OpenFeatureDatabase(ctx, featuredb.Spec{FeatureID: "proxy-memory", SQLitePath: isolated})
	if err != nil {
		t.Fatalf("open feature database: %v", err)
	}
	t.Cleanup(func() { _ = first.DB.Close() })

	spelling := dir + "/./memory.db"
	second, err := opener.OpenFeatureDatabase(ctx, featuredb.Spec{FeatureID: "proxy-memory", SQLitePath: spelling})
	if err != nil {
		t.Fatalf("open feature database: %v", err)
	}
	t.Cleanup(func() { _ = second.DB.Close() })
	if first.Identity != second.Identity {
		t.Fatalf("one file must have one identity: %q != %q", first.Identity, second.Identity)
	}
}

// TestOpenFeatureDatabaseRejectsAnInvalidPath proves the existing path
// validation still applies to the isolated database.
func TestOpenFeatureDatabaseRejectsAnInvalidPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()

	opener := NewFeatureDatabaseOpener(FeatureDatabaseConfig{Store: "sqlite", SQLitePath: filepath.Join(dir, "continuity.db")})
	opened, err := opener.OpenFeatureDatabase(ctx, featuredb.Spec{FeatureID: "proxy-memory", SQLitePath: dir + "/memory?.db"})
	if opened.DB != nil {
		t.Cleanup(func() { _ = opened.DB.Close() })
	}
	if err == nil {
		t.Fatal("an invalid sqlite path must be refused")
	}
	if !strings.Contains(err.Error(), "invalid character") {
		t.Fatalf("unexpected rejection: %v", err)
	}
}

// TestOpenFeatureDatabaseRefusesAnEphemeralTarget proves the isolated
// feature database is durable: an in-memory target is refused before the
// database is opened, and the rejection never echoes the operator path.
func TestOpenFeatureDatabaseRefusesAnEphemeralTarget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()

	for _, target := range []string{":memory:", "file::memory:"} {
		opener := NewFeatureDatabaseOpener(FeatureDatabaseConfig{Store: "sqlite", SQLitePath: filepath.Join(dir, "continuity.db")})
		opened, err := opener.OpenFeatureDatabase(ctx, featuredb.Spec{FeatureID: "proxy-memory", SQLitePath: target})
		if opened.DB != nil {
			t.Cleanup(func() { _ = opened.DB.Close() })
		}
		if err == nil {
			t.Fatalf("the ephemeral target %q must be refused", target)
		}
		if strings.Contains(err.Error(), target) {
			t.Fatalf("the rejection must not echo the operator path: %v", err)
		}
	}
}

// mustWorkingDir returns the working directory of the test process.
func mustWorkingDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	return wd
}
