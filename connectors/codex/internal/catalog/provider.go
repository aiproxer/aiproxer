package catalog

import (
	"context"
	"errors"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

// MapModels builds inventory models from a loaded catalog and the source it
// came from. Each engine supplies its own mapping (prefixing, gating) so the
// provider stays provenance-neutral.
type MapModels func(cat *Catalog, src Source) []modelinventory.Model

// InventorySource maps catalog provenance to inventory snapshot provenance so
// host diagnostics distinguish live discovery from snapshot fallbacks.
func InventorySource(src Source) modelinventory.Source {
	switch src {
	case SourceDiscovered:
		return modelinventory.SourceRemote
	case SourceOverrideFallback:
		return modelinventory.SourceStaticFile
	case SourceShippedFallback:
		return modelinventory.SourceStaticBuiltin
	default:
		return modelinventory.SourceUnknown
	}
}

// InventoryProvider re-resolves the catalog on every LoadModels call instead
// of freezing the Configure-time result, so removals and additions propagate
// on the next inventory refresh. Successful discovery is authoritative for
// that call; Load's fallback policy applies otherwise (snapshot fallback for
// discovery failures or disabled discovery, explicit errors for cancellation
// and unusable fallback configuration).
type InventoryProvider struct {
	Opts LoadOptions
	Map  MapModels
}

// LoadModels implements modelinventory.Provider.
func (p InventoryProvider) LoadModels(ctx context.Context) (modelinventory.Snapshot, error) {
	if ctx == nil {
		return modelinventory.Snapshot{}, modelinventory.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return modelinventory.Snapshot{}, err
	}
	if p.Map == nil {
		return modelinventory.Snapshot{}, errors.New("codexcatalog: nil model mapping")
	}
	cat, src, err := Load(ctx, p.Opts)
	if err != nil {
		return modelinventory.Snapshot{}, err
	}
	models := p.Map(cat, src)
	if models == nil {
		models = []modelinventory.Model{}
	}
	return modelinventory.Snapshot{
		Source:   InventorySource(src),
		LoadedAt: time.Now().UTC(),
		Models:   models,
		Warnings: []string{},
	}, nil
}
