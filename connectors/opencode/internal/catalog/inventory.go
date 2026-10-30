package catalog

import (
	"context"
	"fmt"
	"net/http"

	"github.com/aiproxer/aiproxer/connectors/opencode/internal/catalog/vendor"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

type InventoryProviderConfig struct {
	BaseURL        string
	APIKey         string
	APIKeys        []string
	Credentials    []string
	HTTPClient     *http.Client
	Kind           BackendKind
	Fallback       []ModelEntry
	VendorResolver vendor.VendorResolver
}

type InventoryProvider struct {
	source *ModelSource
}

func NewInventoryProvider(cfg InventoryProviderConfig) *InventoryProvider {
	source := NewModelSource(cfg.Kind, ModelLoaderConfig{
		BaseURL:     cfg.BaseURL,
		Kind:        cfg.Kind,
		APIKey:      cfg.APIKey,
		APIKeys:     cfg.APIKeys,
		Credentials: cfg.Credentials,
		HTTPClient:  cfg.HTTPClient,
	}, cfg.Fallback, cfg.VendorResolver)
	return &InventoryProvider{source: source}
}

func NewInventoryProviderFromSource(source *ModelSource) *InventoryProvider {
	return &InventoryProvider{source: source}
}

func (p *InventoryProvider) LoadModels(ctx context.Context) (modelinventory.Snapshot, error) {
	if p == nil || p.source == nil {
		return modelinventory.Snapshot{}, fmt.Errorf("opencode: inventory provider is not configured")
	}
	return p.source.LoadModels(ctx)
}
