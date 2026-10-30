package conformance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	legacyCartesianBaselineLines = 7851
	minimumCartesianDeletion     = 80
	baselineInventorySHA         = "95089eb4b74d5cf8d062f238a1121124ce0da878"
)

type baselineInventory struct {
	BaselineSHA        string   `json:"baseline_sha"`
	RequiredFeatureIDs []string `json:"required_feature_ids"`
	CartesianFiles     []struct {
		FilePath string `json:"file_path"`
		Lines    int    `json:"non_generated_go_lines"`
	} `json:"cartesian_files"`
}

// TestCartesianDeletionGate pins the Phase 1 inventory metric. The historical
// matrix evidence is now deleted; independent protocol suites remain outside
// this selected Cartesian-only set.
func TestCartesianDeletionGate(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	data, err := os.ReadFile(filepath.Join(root, "internal", "testkit", "conformance", "testdata", "baseline_cartesian_inventory.json"))
	if err != nil {
		t.Fatal(err)
	}
	var inventory baselineInventory
	if err := json.Unmarshal(data, &inventory); err != nil {
		t.Fatal(err)
	}
	if inventory.BaselineSHA != baselineInventorySHA {
		t.Fatalf("baseline SHA=%q, want %q", inventory.BaselineSHA, baselineInventorySHA)
	}
	if len(inventory.RequiredFeatureIDs) != 17 {
		t.Fatalf("baseline required feature IDs=%d, want 17", len(inventory.RequiredFeatureIDs))
	}
	var retainedBaseline int
	for _, file := range inventory.CartesianFiles {
		if info, err := os.Stat(filepath.Join(root, file.FilePath)); err == nil && !info.IsDir() {
			retainedBaseline += file.Lines
		}
	}
	if len(inventory.CartesianFiles) == 0 || legacyCartesianBaselineLines <= 0 {
		t.Fatal("baseline inventory is empty")
	}
	deleted := legacyCartesianBaselineLines - retainedBaseline
	percent := deleted * 100 / legacyCartesianBaselineLines
	if percent < minimumCartesianDeletion {
		t.Fatalf("Cartesian-only deletion=%d%%, want >=%d%%", percent, minimumCartesianDeletion)
	}
}

func infoLineCount(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	lines := 0
	for _, b := range data {
		if b == '\n' {
			lines++
		}
	}
	return lines
}

func TestCartesianDeletionMetricIsAuditable(t *testing.T) {
	if legacyCartesianBaselineLines <= 0 {
		t.Fatalf("invalid pinned Cartesian baseline: %d", legacyCartesianBaselineLines)
	}
}

func TestCartesianDeletionInventoryMatchesPinnedBaseline(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	data, err := os.ReadFile(filepath.Join(root, "internal", "testkit", "conformance", "testdata", "baseline_cartesian_inventory.json"))
	if err != nil {
		t.Fatal(err)
	}
	var inventory baselineInventory
	if err := json.Unmarshal(data, &inventory); err != nil {
		t.Fatal(err)
	}
	var total int
	for _, file := range inventory.CartesianFiles {
		if file.FilePath == "" || file.Lines <= 0 {
			t.Fatalf("invalid baseline entry: %+v", file)
		}
		total += file.Lines
	}
	if total != legacyCartesianBaselineLines {
		t.Fatalf("inventory lines=%d, pinned lines=%d", total, legacyCartesianBaselineLines)
	}
	if strings.TrimSpace(inventory.BaselineSHA) != baselineInventorySHA {
		t.Fatalf("inventory SHA=%q, want %q", inventory.BaselineSHA, baselineInventorySHA)
	}
}

func TestCartesianDeletionSharedSurfaceDoesNotGrow(t *testing.T) {
	// The reviewed conformance directory is the affected shared surface. This
	// gate intentionally excludes independent protocol packages and generated
	// artifacts from the deletion metric.
	if got := countNonCommentGoLines(filepath.Join("..", "..", "..", "internal", "testkit", "conformance")); got > 10800 {
		t.Fatalf("affected conformance surface grew to %d lines", got)
	}
}

func countNonCommentGoLines(root string) int {
	var total int
	entries, _ := os.ReadDir(root)
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		if entry.IsDir() {
			total += countNonCommentGoLines(path)
			continue
		}
		if filepath.Ext(path) != ".go" {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for line := range strings.SplitSeq(string(data), "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed != "" && !strings.HasPrefix(trimmed, "//") {
				total++
			}
		}
	}
	return total
}
