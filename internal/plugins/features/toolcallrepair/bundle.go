package toolcallrepair

import (
	"fmt"

	"github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair/repair"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

// FeatureBundle constructs a schema-V1 feature bundle from validated configuration,
// contributing the tool-call repair finalizer and finalization max args byte limit.
func FeatureBundle(cfg Config) (aipfeature.FeatureBundle, error) {
	norm, err := normalizeConfig(cfg, cfg.MaxArgsBytes != 0)
	if err != nil {
		return aipfeature.FeatureBundle{}, err
	}
	fin := repair.NewFinalizer(repair.FinalizerPolicy{
		ID:             ID,
		MaxArgsBytes:   norm.MaxArgsBytes,
		OnUnrepairable: norm.OnUnrepairable,
		Order:          norm.FinalizerOrder(),
		Schema: repair.SchemaLimits{
			MaxSchemaBytes:   norm.Schema.MaxSchemaBytes,
			MaxNestingDepth:  norm.Schema.MaxNestingDepth,
			MaxNodes:         norm.Schema.MaxNodes,
			MaxProperties:    norm.Schema.MaxProperties,
			MaxLocalRefDepth: norm.Schema.MaxLocalRefDepth,
			MaxCacheEntries:  norm.Schema.MaxCacheEntries,
			MaxCacheBytes:    norm.Schema.MaxCacheBytes,
		},
	})
	cs := aipfeature.NewContributionSet()
	if err := aipfeature.Contribute(cs, aipfeature.PlaneToolCallFinalizers, ID, []toolcall.Finalizer{fin}); err != nil {
		return aipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", ID, err)
	}
	if err := aipfeature.Contribute(cs, aipfeature.PlaneToolCallFinalizationMaxArgsBytes, ID, norm.MaxArgsBytes); err != nil {
		return aipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", ID, err)
	}
	b := aipfeature.BundleFromPlanes(cs.Freeze(), nil)
	if err := b.Validate(); err != nil {
		return aipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", ID, err)
	}
	return b, nil
}
