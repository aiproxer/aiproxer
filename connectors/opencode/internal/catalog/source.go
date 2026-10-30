package catalog

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/aiproxer/aiproxer/connectors/opencode/internal/catalog/vendor"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

type ModelSource struct {
	kind           BackendKind
	loader         ModelLoaderConfig
	vendorResolver vendor.VendorResolver
	static         *ModelCatalog

	mu              sync.Mutex
	remoteAttempted bool
	remoteEntries   []ModelEntry
	remoteCatalog   *ModelCatalog
}

func NewModelSource(kind BackendKind, loader ModelLoaderConfig, staticEntries []ModelEntry, vendors vendor.VendorResolver) *ModelSource {
	return &ModelSource{
		kind:           kind,
		loader:         loader,
		vendorResolver: vendors,
		static:         NewModelCatalog(kind, staticEntries, vendors),
	}
}

func (s *ModelSource) Resolve(ctx context.Context, model string) (ResolvedModel, error) {
	if ctx == nil {
		return ResolvedModel{}, fmt.Errorf("opencode: %w", aipapi.ErrNilContext)
	}
	if res, err := s.static.Resolve(model); err == nil {
		return res, nil
	}
	if err := s.ensureRemote(ctx); err != nil {
		return ResolvedModel{}, fmt.Errorf("%w: %q", ErrUnknownModel, model)
	}
	s.mu.Lock()
	catalog := s.remoteCatalog
	s.mu.Unlock()
	if catalog == nil {
		return ResolvedModel{}, fmt.Errorf("%w: %q", ErrUnknownModel, model)
	}
	res, err := catalog.Resolve(model)
	if err != nil {
		return ResolvedModel{}, err
	}
	return res, nil
}

func (s *ModelSource) LoadModels(ctx context.Context) (modelinventory.Snapshot, error) {
	if ctx == nil {
		return modelinventory.Snapshot{}, modelinventory.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return modelinventory.Snapshot{}, err
	}

	s.mu.Lock()
	if s.remoteAttempted && len(s.remoteEntries) > 0 {
		entries := slices.Clone(s.remoteEntries)
		s.mu.Unlock()
		return modelInventorySnapshot(s.kind, entries, modelinventory.SourceRemote, []string{}, s.vendorResolver), nil
	}
	s.mu.Unlock()

	staticEntries := s.staticEntries()
	entries, source, warnings, err := LoadModelEntries(ctx, s.loader, staticEntries)
	if err != nil {
		return modelinventory.Snapshot{}, err
	}

	s.mu.Lock()
	if source == modelinventory.SourceRemote {
		s.remoteAttempted = true
		s.remoteEntries = slices.Clone(entries)
		s.remoteCatalog = NewModelCatalog(s.kind, s.remoteEntries, s.vendorResolver)
	} else {
		s.remoteAttempted = false
	}
	s.mu.Unlock()

	return modelInventorySnapshot(s.kind, entries, source, warnings, s.vendorResolver), nil
}

func (s *ModelSource) staticEntries() []ModelEntry {
	return s.static.entries()
}

func (s *ModelSource) ensureRemote(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.remoteAttempted {
		if len(s.remoteEntries) == 0 {
			return fmt.Errorf("opencode: remote model catalog unavailable")
		}
		return nil
	}
	entries, err := FetchRemoteModelEntries(ctx, s.loader)
	if err != nil || len(entries) == 0 {
		if err != nil {
			return err
		}
		return fmt.Errorf("opencode: remote model discovery returned no models")
	}
	s.remoteAttempted = true
	s.remoteEntries = slices.Clone(entries)
	s.remoteCatalog = NewModelCatalog(s.kind, s.remoteEntries, s.vendorResolver)
	return nil
}
