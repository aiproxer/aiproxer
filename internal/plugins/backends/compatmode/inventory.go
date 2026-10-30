package compatmode

import (
	"fmt"
	"os"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/modeldiscover"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
	"gopkg.in/yaml.v3"
)

type inventoryFileYAML struct {
	Items  []inventoryItemYAML `yaml:"items"`
	Models []inventoryItemYAML `yaml:"models"`
}

type inventoryItemYAML struct {
	CanonicalID string `yaml:"canonical_id"`
	NativeID    string `yaml:"native_id"`
	DisplayName string `yaml:"display_name"`
}

// ApplyStaticModelInventory attaches a static inventory provider when configured.
func ApplyStaticModelInventory(be execbackend.Backend, models config.CompatibleModeModelsConfig) (execbackend.Backend, error) {
	provider, ok, err := staticInventoryFromConfig(models)
	if err != nil {
		return execbackend.Backend{}, err
	}
	if ok {
		be.ModelInventory = provider
	}
	return be, nil
}

func staticInventoryFromConfig(m config.CompatibleModeModelsConfig) (modelinventory.Provider, bool, error) {
	source := strings.ToLower(strings.TrimSpace(m.Source))
	path := strings.TrimSpace(m.Path)
	if path != "" && len(m.Items) > 0 {
		return nil, false, fmt.Errorf("backend models: specify either path or items, not both")
	}
	if source == "" {
		if len(m.Items) == 0 && path == "" {
			return nil, false, nil
		}
		if path != "" && len(m.Items) == 0 {
			source = "file"
		} else {
			source = "inline"
		}
	}
	var rows []inventoryItemYAML
	var inventorySource modelinventory.Source
	switch source {
	case "inline", "static_inline":
		for _, item := range m.Items {
			rows = append(rows, inventoryItemYAML{
				CanonicalID: item.CanonicalID,
				NativeID:    item.NativeID,
				DisplayName: item.DisplayName,
			})
		}
		inventorySource = modelinventory.SourceStaticInline
	case "file", "static_file":
		if path == "" {
			return nil, false, fmt.Errorf("backend models: path is required for source %q", source)
		}
		items, err := loadInventoryFile(path)
		if err != nil {
			return nil, false, err
		}
		rows = items
		inventorySource = modelinventory.SourceStaticFile
	default:
		return nil, false, fmt.Errorf("backend models: unsupported source %q", m.Source)
	}
	if len(rows) == 0 {
		return nil, false, fmt.Errorf("backend models: at least one item is required")
	}
	models := make([]modelinventory.Model, 0, len(rows))
	for i, row := range rows {
		canonical := strings.TrimSpace(row.CanonicalID)
		native := strings.TrimSpace(row.NativeID)
		if canonical == "" || native == "" {
			return nil, false, fmt.Errorf("backend models: item[%d] requires canonical_id and native_id", i)
		}
		models = append(models, modelinventory.Model{
			CanonicalID: canonical,
			NativeID:    native,
			DisplayName: strings.TrimSpace(row.DisplayName),
		})
	}
	bounded, warnings := modeldiscover.BoundInventoryModels(models)
	return modelinventory.StaticProvider{
		Source:   inventorySource,
		Models:   bounded,
		Warnings: warnings,
	}, true, nil
}

func loadInventoryFile(path string) ([]inventoryItemYAML, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("backend models: read %q: %w", path, err)
	}
	var y inventoryFileYAML
	if err := yaml.Unmarshal(b, &y); err != nil {
		return nil, fmt.Errorf("backend models: decode %q: %w", path, err)
	}
	if len(y.Items) > 0 {
		return y.Items, nil
	}
	return y.Models, nil
}
