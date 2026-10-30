//go:build integration

package conformance

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/internal/refclient/refclienttest"
)

func TestParityMatrixCompleteness(t *testing.T) {
	t.Parallel()
	root := refclienttest.ModuleRoot(t)
	dir := filepath.Join(root, "internal", "testkit", "conformance")
	for _, id := range AllBundledProtocolIDs() {
		files, ok := ParityProtocolEvidence[id]
		if !ok || len(files) == 0 {
			t.Fatalf("protocol %q missing ParityProtocolEvidence entry (extend parity_evidence.go)", id)
		}
		for _, name := range files {
			path := filepath.Join(dir, name)
			st, err := os.Stat(path)
			if err != nil {
				t.Fatalf("protocol %q evidence file %s: %v", id, path, err)
			}
			if st.IsDir() {
				t.Fatalf("expected file not directory: %s", path)
			}
		}
	}
}

func TestParitySuiteSourceFilesPresent(t *testing.T) {
	t.Parallel()
	root := refclienttest.ModuleRoot(t)
	dir := filepath.Join(root, "internal", "testkit", "conformance")
	for _, name := range ParitySuiteGoFiles {
		path := filepath.Join(dir, name)
		st, err := os.Stat(path)
		if err != nil {
			t.Fatalf("missing parity suite file %s: %v", path, err)
		}
		if st.IsDir() {
			t.Fatalf("expected file not directory: %s", path)
		}
	}
}

func TestMigrationGoldenFixtureInventory(t *testing.T) {
	t.Parallel()
	root := refclienttest.ModuleRoot(t)
	dir := filepath.Join(root, "testdata", "migration")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var jsonFiles []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if filepath.Ext(e.Name()) == ".json" {
			jsonFiles = append(jsonFiles, e.Name())
		}
	}
	if len(jsonFiles) != len(ExpectedMigrationGoldenJSON) {
		t.Fatalf("migration JSON count: got %d want %d; files=%v", len(jsonFiles), len(ExpectedMigrationGoldenJSON), jsonFiles)
	}
	for _, want := range ExpectedMigrationGoldenJSON {
		if !slices.Contains(jsonFiles, want) {
			t.Fatalf("missing expected migration golden %q (have %v)", want, jsonFiles)
		}
	}
}
