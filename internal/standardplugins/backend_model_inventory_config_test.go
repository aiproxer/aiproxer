package standardplugins

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openairesponses"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
	"gopkg.in/yaml.v3"
)

func TestBuildBackend_openAIResponses_usesInlineModelInventory(t *testing.T) {
	t.Parallel()

	reg := pluginreg.NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}

	raw := `models:
  source: inline
  items:
    - canonical_id: openai/gpt-4o-mini
      native_id: gpt-4o-mini
      display_name: GPT-4o mini
`
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}

	b, err := reg.BuildBackend(openairesponses.ID, node, nil, pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := b.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Source != modelinventory.SourceStaticInline {
		t.Fatalf("Source = %q, want %q", snap.Source, modelinventory.SourceStaticInline)
	}
	if len(snap.Models) != 1 || snap.Models[0].CanonicalID != "openai/gpt-4o-mini" {
		t.Fatalf("Models = %+v", snap.Models)
	}
}

func TestBuildBackend_openAIResponses_usesFileModelInventory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "models.yaml")
	if err := os.WriteFile(path, []byte(`items:
  - canonical_id: openai/gpt-4.1
    native_id: gpt-4.1
`), 0o600); err != nil {
		t.Fatal(err)
	}

	reg := pluginreg.NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}

	raw := "models:\n  source: file\n  path: " + path + "\n"
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}

	b, err := reg.BuildBackend(openairesponses.ID, node, nil, pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := b.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Source != modelinventory.SourceStaticFile {
		t.Fatalf("Source = %q, want %q", snap.Source, modelinventory.SourceStaticFile)
	}
	if len(snap.Models) != 1 || snap.Models[0].NativeID != "gpt-4.1" {
		t.Fatalf("Models = %+v", snap.Models)
	}
}

func TestBuildBackend_openAIResponses_pathOnlyDefaultsToFileModelInventory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "models.yaml")
	if err := os.WriteFile(path, []byte(`items:
  - canonical_id: openai/gpt-4.1-mini
    native_id: gpt-4.1-mini
`), 0o600); err != nil {
		t.Fatal(err)
	}

	reg := pluginreg.NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}

	raw := "models:\n  path: " + path + "\n"
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}

	b, err := reg.BuildBackend(openairesponses.ID, node, nil, pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := b.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Source != modelinventory.SourceStaticFile {
		t.Fatalf("Source = %q, want %q", snap.Source, modelinventory.SourceStaticFile)
	}
	if len(snap.Models) != 1 || snap.Models[0].NativeID != "gpt-4.1-mini" {
		t.Fatalf("Models = %+v", snap.Models)
	}
}

func TestBuildBackend_openAIResponses_fileModelInventoryAcceptsModelsAlias(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "models.yaml")
	if err := os.WriteFile(path, []byte(`models:
  - canonical_id: openai/gpt-4.1-nano
    native_id: gpt-4.1-nano
`), 0o600); err != nil {
		t.Fatal(err)
	}

	reg := pluginreg.NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}

	raw := "models:\n  source: file\n  path: " + path + "\n"
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}

	b, err := reg.BuildBackend(openairesponses.ID, node, nil, pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := b.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Models) != 1 || snap.Models[0].NativeID != "gpt-4.1-nano" {
		t.Fatalf("Models = %+v", snap.Models)
	}
}
