package standardplugins

import (
	"fmt"

	"github.com/aiproxer/aiproxer/internal/core/config"
	frontopenresponses "github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
	"gopkg.in/yaml.v3"
)

// StandardContinuationWiringFactory creates lifecycle-owned continuation
// resources for the standard OpenResponses frontend. The factory is kept in
// the composition root so the frontend plugin only consumes SDK ports.
func StandardContinuationWiringFactory(_ *config.Config) aipsdk.ContinuationMountWiringFactory {
	return func(frontendID, _ string, n yaml.Node) (aipsdk.ContinuationMountWiring, error) {
		if frontendID != frontopenresponses.ID {
			return aipsdk.ContinuationMountWiring{}, nil
		}
		cfg, err := frontopenresponses.DecodeConfig(n)
		if err != nil {
			return aipsdk.ContinuationMountWiring{}, fmt.Errorf("standard continuation wiring: %w", err)
		}
		depth := cfg.Continuation.MaxChainDepth
		if depth <= 0 {
			depth = frontopenresponses.DefaultMaxChainDepth
		}
		maxBytes := cfg.Continuation.MaxMaterializedBytes
		if maxBytes <= 0 {
			maxBytes = frontopenresponses.DefaultMaxMaterializedBytes
		}
		store := aipcont.NewMemoryStoreWithLimits(aipcont.StorageLimits{
			MaxRecords:     10_000,
			MaxBytes:       maxBytes,
			MaxRecordBytes: 16 << 20,
			MaxChainDepth:  depth,
		})
		return aipsdk.ContinuationMountWiring{
			Store:    store,
			Resolver: aipcont.NewResolver(store, aipcont.Bounds{MaxChainDepth: depth, MaxMaterializedBytes: maxBytes, MaxMaterializedItems: 100_000}),
			Close:    store.Close,
		}, nil
	}
}
