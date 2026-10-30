package standardplugins

import (
	"fmt"
	"os"
	"strings"

	"github.com/aiproxer/aiproxer/internal/plugins/backends/modeldiscover"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
	"gopkg.in/yaml.v3"
)

type modelInventoryYAML struct {
	Source string                   `yaml:"source"`
	Path   string                   `yaml:"path"`
	Items  []modelInventoryItemYAML `yaml:"items"`
}

type modelInventoryFileYAML struct {
	Items  []modelInventoryItemYAML `yaml:"items"`
	Models []modelInventoryItemYAML `yaml:"models"`
}

type modelInventoryItemYAML struct {
	CanonicalID string `yaml:"canonical_id"`
	NativeID    string `yaml:"native_id"`
	DisplayName string `yaml:"display_name"`
}

type prefixedModelYAML struct {
	RawID       string
	DisplayName string
}

func modelInventoryRows(y modelInventoryYAML, emptyOK bool) ([]modelInventoryItemYAML, modelinventory.Source, bool, error) {
	source := strings.ToLower(strings.TrimSpace(y.Source))
	path := strings.TrimSpace(y.Path)
	if path != "" && len(y.Items) > 0 {
		return nil, "", false, fmt.Errorf("backend models: specify either path or items, not both")
	}
	if source == "" {
		if len(y.Items) == 0 && path == "" {
			return nil, "", false, nil
		}
		if path != "" && len(y.Items) == 0 {
			source = "file"
		} else {
			source = "inline"
		}
	}
	var rows []modelInventoryItemYAML
	var inventorySource modelinventory.Source
	switch source {
	case "inline", "static_inline":
		rows = y.Items
		inventorySource = modelinventory.SourceStaticInline
	case "file", "static_file":
		path := strings.TrimSpace(y.Path)
		if path == "" {
			return nil, "", false, fmt.Errorf("backend models: path is required for source %q", source)
		}
		items, err := loadModelInventoryFile(path)
		if err != nil {
			return nil, "", false, err
		}
		rows = items
		inventorySource = modelinventory.SourceStaticFile
	default:
		return nil, "", false, fmt.Errorf("backend models: unsupported source %q", y.Source)
	}
	if len(rows) == 0 {
		if emptyOK {
			return nil, "", false, nil
		}
		return nil, "", false, fmt.Errorf("backend models: at least one item is required")
	}
	return rows, inventorySource, true, nil
}

func prefixedModelIDsFromYAML(prefix string, y modelInventoryYAML) ([]prefixedModelYAML, error) {
	rows, _, ok, err := modelInventoryRows(y, true)
	if err != nil || !ok {
		return nil, err
	}
	prefix = strings.TrimSpace(prefix)
	if prefix != "" {
		prefix += "/"
	}
	models := make([]prefixedModelYAML, 0, len(rows))
	for i, row := range rows {
		raw := strings.TrimSpace(row.NativeID)
		switch {
		case prefix != "" && strings.HasPrefix(raw, prefix):
			raw = strings.TrimPrefix(raw, prefix)
		case raw == "":
			canonical := strings.TrimSpace(row.CanonicalID)
			if canonical == "" {
				return nil, fmt.Errorf("backend models: item[%d] requires native_id or canonical_id", i)
			}
			if idx := strings.LastIndex(canonical, "/"); idx >= 0 {
				raw = canonical[idx+1:]
			} else {
				raw = canonical
			}
		}
		if raw == "" {
			return nil, fmt.Errorf("backend models: item[%d] requires a model id", i)
		}
		models = append(models, prefixedModelYAML{RawID: raw, DisplayName: strings.TrimSpace(row.DisplayName)})
	}
	return models, nil
}

func loadModelInventoryFile(path string) ([]modelInventoryItemYAML, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("backend models: read %q: %w", path, err)
	}
	var y modelInventoryFileYAML
	if err := yaml.Unmarshal(b, &y); err != nil {
		return nil, fmt.Errorf("backend models: decode %q: %w", path, err)
	}
	if len(y.Items) > 0 {
		return y.Items, nil
	}
	return y.Models, nil
}

func staticModelInventory(source modelinventory.Source, rows []modelInventoryItemYAML) (modelinventory.Provider, bool, error) {
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
		Source:   source,
		Models:   bounded,
		Warnings: warnings,
	}, true, nil
}
