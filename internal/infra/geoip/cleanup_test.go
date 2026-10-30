package geoip

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCleanupVersionsRetainsActiveAndOnePrior(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	for _, name := range []string{
		"dbip-country-lite.active.mmdb",
		"dbip-country-lite.previous.mmdb",
		"dbip-country-lite.obsolete.mmdb",
		".download-stale.tmp",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := cleanupVersions(dir, dbIPEdition, "dbip-country-lite.active.mmdb"); err != nil {
		t.Fatalf("cleanupVersions: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "dbip-country-lite.active.mmdb")); err != nil {
		t.Fatalf("active version was removed: %v", err)
	}
	// With equal timestamps lexical ordering deterministically retains one prior.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".mmdb" {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("retained MMDB count = %d, want 2", count)
	}
}
