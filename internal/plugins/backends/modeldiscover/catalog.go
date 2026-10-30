package modeldiscover

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/modelcatalog/modelsdev"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

type CatalogConfig struct {
	Enabled *bool
	URL     string
	Timeout time.Duration
}

type CatalogAwareOpenAICompatibleModelsProvider struct {
	BaseURL           string
	APIKey            string
	APIKeys           []string
	Credentials       []string
	HTTPClient        *http.Client
	CanonicalPrefix   string
	PreserveVendorIDs bool
	Catalog           CatalogConfig
}

func (p CatalogAwareOpenAICompatibleModelsProvider) LoadModels(ctx context.Context) (modelinventory.Snapshot, error) {
	base := OpenAICompatibleModelsProvider{
		BaseURL:           p.BaseURL,
		APIKey:            p.APIKey,
		APIKeys:           p.APIKeys,
		Credentials:       p.Credentials,
		HTTPClient:        p.HTTPClient,
		CanonicalPrefix:   p.CanonicalPrefix,
		PreserveVendorIDs: p.PreserveVendorIDs,
	}
	snap, err := base.LoadModels(ctx)
	if err != nil {
		return modelinventory.Snapshot{}, err
	}
	if !catalogEnabled(p.Catalog) {
		return snap, nil
	}
	catalogURL := strings.TrimSpace(p.Catalog.URL)
	if catalogURL == "" {
		return snap, nil
	}
	ids, err := p.loadCatalogModelIDs(ctx, catalogURL)
	if err != nil {
		snap.Warnings = append(snap.Warnings, fmt.Sprintf("models.dev catalog lookup failed: %v", err))
		return snap, nil
	}
	prefix := strings.Trim(strings.TrimSpace(p.CanonicalPrefix), "/")
	for i := range snap.Models {
		snap.Models[i].CanonicalID = mapNativeToCatalogCanonical(snap.Models[i].NativeID, ids, prefix, p.PreserveVendorIDs)
	}
	return snap, nil
}

func catalogEnabled(cfg CatalogConfig) bool {
	if cfg.Enabled == nil {
		return true
	}
	return *cfg.Enabled
}

func mapNativeToCatalogCanonical(native string, catalogIDs []string, fallbackPrefix string, preserveVendorIDs bool) string {
	native = strings.TrimSpace(native)
	if native == "" {
		return fallbackCanonicalWithPrefix(fallbackPrefix, "unknown", false)
	}
	if len(catalogIDs) == 0 {
		return fallbackCanonicalWithPrefix(fallbackPrefix, native, preserveVendorIDs)
	}
	want := catalogComparableModelID(native)
	var matched string
	for _, id := range catalogIDs {
		if catalogComparableModelID(stripProviderPrefix(id)) != want {
			continue
		}
		if matched != "" {
			return fallbackCanonicalWithPrefix(fallbackPrefix, native, preserveVendorIDs)
		}
		matched = id
	}
	if matched == "" {
		return fallbackCanonicalWithPrefix(fallbackPrefix, native, preserveVendorIDs)
	}
	return matched
}

func fallbackCanonicalWithPrefix(prefix, native string, preserveVendorIDs bool) string {
	native = strings.Trim(strings.TrimSpace(native), "/")
	if preserveVendorIDs && isSingleProviderModelID(native) {
		return native
	}
	return strings.Trim(strings.TrimSpace(prefix), "/") + "/" + native
}

func isSingleProviderModelID(id string) bool {
	before, after, ok := strings.Cut(strings.TrimSpace(id), "/")
	return ok && before != "" && after != "" && !strings.Contains(after, "/")
}

func catalogComparableModelID(id string) string {
	id = strings.TrimSpace(id)
	id = strings.TrimSuffix(id, ":latest")
	return id
}

func stripProviderPrefix(id string) string {
	_, after, ok := strings.Cut(strings.TrimSpace(id), "/")
	if !ok {
		return strings.TrimSpace(id)
	}
	return after
}

func (p CatalogAwareOpenAICompatibleModelsProvider) loadCatalogModelIDs(ctx context.Context, endpoint string) ([]string, error) {
	if ctx == nil {
		return nil, modelinventory.ErrNilContext
	}
	if p.Catalog.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.Catalog.Timeout)
		defer cancel()
	}
	client := p.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("models.dev catalog request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("models.dev catalog http: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("models.dev catalog HTTP status %d", resp.StatusCode)
	}
	limited := io.LimitedReader{R: resp.Body, N: 64<<20 + 1}
	body, err := io.ReadAll(&limited)
	if err != nil {
		return nil, fmt.Errorf("models.dev catalog read: %w", err)
	}
	if int64(len(body)) > 64<<20 {
		return nil, fmt.Errorf("models.dev catalog response exceeds %d bytes", 64<<20)
	}
	ids, err := modelsdev.ParseModelIDs(body)
	if err != nil {
		return nil, err
	}
	return slices.Clone(ids), nil
}
