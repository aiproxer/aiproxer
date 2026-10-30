package featurehost

import (
	"fmt"

	"github.com/aiproxer/aiproxer/internal/core/auxreq"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/featurebundle"
	featurecontinuity "github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/compaction"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
)

// CompactionSchedulerBounds translates compaction continuity feature config into
// generic auxiliary scheduler bounds.
func CompactionSchedulerBounds(cfg *config.Config) auxreq.SchedulerConfig {
	return compaction.SchedulerBoundsFromConfig(cfg)
}

// validateCompactionPrerequisites gates only enabled official composition.
func (r *Runtime) validateCompactionPrerequisites(regs []aipsdk.Registration) error {
	hasDetector := r != nil && r.compactionDetector != nil
	hasCoordinator := r != nil && r.branchCoordinator != nil
	hasBackground := r != nil && r.bgAux != nil

	for _, reg := range regs {
		if reg.Kind != aipsdk.PluginKindFeature || !reg.Enabled || reg.RegistryFactoryKey() != featurecontinuity.ID {
			continue
		}
		cfg, err := featurecontinuity.DecodeConfig(reg.Config.Node)
		if err != nil {
			return fmt.Errorf("featurehost: compaction-continuity config: %w", err)
		}
		if err := featurecontinuity.ValidatePrerequisites(cfg, featurecontinuity.Prerequisites{
			DetectorPreview:   hasDetector,
			DetectorCommit:    hasDetector,
			BranchCoordinator: hasCoordinator,
			BackgroundAux:     hasBackground,
		}); err != nil {
			return fmt.Errorf("featurehost: generation prerequisite: %w", err)
		}
	}
	return nil
}

// bindCompactionContinuity replaces the configuration-only official preserver with
// the explicitly composed process parent port using generated typed replacement operations.
// The binder stages all registration replacements and commits only when the entire loop succeeds,
// ensuring fail-before-mutate transactional atomicity across all feature registrations.
func (r *Runtime) bindCompactionContinuity(genMerged featurebundle.GeneratedMergeSurface, regs []aipsdk.Registration) (featurebundle.GeneratedMergeSurface, error) {
	if r == nil || r.compactionParentPort == nil {
		return genMerged, nil
	}
	staged := genMerged
	for _, reg := range regs {
		if reg.Kind != aipsdk.PluginKindFeature || !reg.Enabled || reg.RegistryFactoryKey() != featurecontinuity.ID {
			continue
		}
		cfg, err := featurecontinuity.DecodeConfig(reg.Config.Node)
		if err != nil {
			return featurebundle.GeneratedMergeSurface{}, fmt.Errorf("featurehost: compaction-continuity config: %w", err)
		}
		bundle, err := featurecontinuity.FeatureBundleWithPort(cfg, r.compactionParentPort)
		if err != nil {
			return featurebundle.GeneratedMergeSurface{}, fmt.Errorf("featurehost: compaction-continuity composition: %w", err)
		}
		var bindErr error
		staged, bindErr = staged.BindCompactionPreservers(featurecontinuity.ID, aipfeature.Get(bundle.PlaneSet, aipfeature.PlaneCompactionPreservers))
		if bindErr != nil {
			return featurebundle.GeneratedMergeSurface{}, fmt.Errorf("featurehost: compaction-continuity binding: %w", bindErr)
		}
	}
	return staged, nil
}
