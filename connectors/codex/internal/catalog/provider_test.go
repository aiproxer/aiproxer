package catalog_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/connectors/codex/internal/catalog"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

func slugModels(prefix string) catalog.MapModels {
	return func(cat *catalog.Catalog, _ catalog.Source) []modelinventory.Model {
		var out []modelinventory.Model
		for _, slug := range cat.RoutableSlugs() {
			out = append(out, modelinventory.Model{
				CanonicalID: prefix + "/" + slug,
				NativeID:    slug,
				DisplayName: slug,
			})
		}
		return out
	}
}

// servingCodexBinary returns a CodexBinaryPath whose `debug models` output is
// the current content of servedPath, so tests can change discovery results
// between LoadModels calls.
func servingCodexBinary(t *testing.T, servedPath string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "codex")
	script := "#!/bin/sh\ncat \"" + servedPath + "\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestInventoryProvider_RediscoversEveryCall(t *testing.T) {
	t.Parallel()

	served := filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(served, sampleRawCatalog(), 0o644); err != nil {
		t.Fatal(err)
	}
	p := catalog.InventoryProvider{
		Opts: catalog.LoadOptions{Enabled: true, CodexBinaryPath: servingCodexBinary(t, served), Timeout: 10 * time.Second},
		Map:  slugModels("openai"),
	}

	first, err := p.LoadModels(context.Background())
	if err != nil {
		t.Fatalf("first LoadModels: %v", err)
	}
	if first.Source != modelinventory.SourceRemote {
		t.Fatalf("first Source = %q, want remote", first.Source)
	}
	if len(first.Models) != 3 {
		t.Fatalf("first Models = %+v, want 3 discovered slugs", first.Models)
	}

	// Change what the CLI serves: the next load must observe the new catalog
	// instead of the Configure-time (first-load) result.
	rotated := `{"models":[{"slug":"gpt-9","supported_reasoning_levels":[{"effort":"medium"}]}]}`
	if err := os.WriteFile(served, []byte(rotated), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := p.LoadModels(context.Background())
	if err != nil {
		t.Fatalf("second LoadModels: %v", err)
	}
	if len(second.Models) != 1 || second.Models[0].NativeID != "gpt-9" {
		t.Fatalf("second Models = %+v, want exactly [gpt-9]", second.Models)
	}
}

func TestInventoryProvider_DisabledUsesShippedFallback(t *testing.T) {
	t.Parallel()

	p := catalog.InventoryProvider{
		Opts: catalog.LoadOptions{Enabled: false},
		Map:  slugModels("openai"),
	}
	snap, err := p.LoadModels(context.Background())
	if err != nil {
		t.Fatalf("LoadModels: %v", err)
	}
	if snap.Source != modelinventory.SourceStaticBuiltin {
		t.Fatalf("Source = %q, want static_builtin", snap.Source)
	}
	if len(snap.Models) == 0 {
		t.Fatal("expected shipped fallback slugs")
	}
}

func TestInventoryProvider_DiscoveryFailureUsesFallback(t *testing.T) {
	t.Parallel()

	p := catalog.InventoryProvider{
		Opts: catalog.LoadOptions{Enabled: true, CodexBinaryPath: brokenCodexBinary(t), Timeout: time.Second},
		Map:  slugModels("openai"),
	}
	snap, err := p.LoadModels(context.Background())
	if err != nil {
		t.Fatalf("LoadModels: %v", err)
	}
	if snap.Source != modelinventory.SourceStaticBuiltin {
		t.Fatalf("Source = %q, want static_builtin fallback", snap.Source)
	}
	if len(snap.Models) == 0 {
		t.Fatal("expected shipped fallback slugs")
	}
}

func TestInventoryProvider_CanceledPropagatesWithoutFallback(t *testing.T) {
	t.Parallel()

	p := catalog.InventoryProvider{
		Opts: catalog.LoadOptions{Enabled: true, Timeout: time.Second},
		Map:  slugModels("openai"),
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.LoadModels(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want errors.Is(context.Canceled)", err)
	}
}

func TestInventoryProvider_NilContextAndNilMap(t *testing.T) {
	t.Parallel()

	p := catalog.InventoryProvider{
		Opts: catalog.LoadOptions{Enabled: false},
		Map:  slugModels("openai"),
	}
	//nolint:staticcheck // defensive nil-context handling is part of the provider contract
	if _, err := p.LoadModels(nil); !errors.Is(err, modelinventory.ErrNilContext) {
		t.Fatalf("nil ctx error = %v, want ErrNilContext", err)
	}
	if _, err := (catalog.InventoryProvider{Opts: catalog.LoadOptions{}}).LoadModels(context.Background()); err == nil {
		t.Fatal("nil Map = nil error, want error")
	}
}

func TestInventorySource_Mapping(t *testing.T) {
	t.Parallel()

	cases := map[catalog.Source]modelinventory.Source{
		catalog.SourceDiscovered:       modelinventory.SourceRemote,
		catalog.SourceOverrideFallback: modelinventory.SourceStaticFile,
		catalog.SourceShippedFallback:  modelinventory.SourceStaticBuiltin,
		catalog.SourceUnknown:          modelinventory.SourceUnknown,
	}
	for src, want := range cases {
		if got := catalog.InventorySource(src); got != want {
			t.Fatalf("InventorySource(%q) = %q, want %q", src, got, want)
		}
	}
}
