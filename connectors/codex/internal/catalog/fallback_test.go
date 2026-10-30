package catalog_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/codex/internal/catalog"
)

func TestLoadFallback_ShippedSnapshot(t *testing.T) {
	t.Parallel()
	c, err := catalog.LoadFallback("")
	if err != nil {
		t.Fatalf("LoadFallback: %v", err)
	}
	slugs := c.RoutableSlugs()
	if len(slugs) == 0 {
		t.Fatal("shipped snapshot has no routable slugs")
	}
	if !slices.Contains(slugs, "gpt-5.6-sol") {
		t.Fatalf("shipped snapshot routable slugs %v do not include gpt-5.6-sol", slugs)
	}
	if c.DefaultReasoningEffort() != "medium" {
		t.Fatalf("DefaultReasoningEffort = %q, want medium", c.DefaultReasoningEffort())
	}
}

func TestLoadFallback_OverridePath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.json")
	if err := os.WriteFile(path, sampleRawCatalog(), 0o644); err != nil {
		t.Fatalf("write override: %v", err)
	}
	c, err := catalog.LoadFallback(path)
	if err != nil {
		t.Fatalf("LoadFallback: %v", err)
	}
	want := []string{"gpt-5.6-sol", "gpt-5.6-luna", "gpt-5.5"}
	if !slices.Equal(c.RoutableSlugs(), want) {
		t.Fatalf("RoutableSlugs = %v, want %v", c.RoutableSlugs(), want)
	}
}

func TestLoadFallback_OverrideMissingReturnsError(t *testing.T) {
	t.Parallel()
	if _, err := catalog.LoadFallback(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Fatal("LoadFallback(missing) = nil error, want error")
	}
}
