package main

import (
	"path/filepath"
	"testing"
)

func TestParseRequirementIDs_FromSpec(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	ids, err := parseRequirementIDs(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 116 {
		t.Fatalf("want 116 acceptance ids, got %d first=%v last=%v", len(ids), ids[0], ids[len(ids)-1])
	}
	if ids[0] != "1.1" || ids[len(ids)-1] != "12.11" {
		t.Fatalf("range=%s..%s", ids[0], ids[len(ids)-1])
	}
	seen := map[string]struct{}{}
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			t.Fatalf("duplicate %s", id)
		}
		seen[id] = struct{}{}
	}
}

func TestParseRequirementIDs_RejectsMissingFile(t *testing.T) {
	t.Parallel()
	_, err := parseRequirementIDs(t.TempDir())
	if err == nil {
		t.Fatal("expected error")
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
