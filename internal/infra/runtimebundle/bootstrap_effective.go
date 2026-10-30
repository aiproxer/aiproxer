package runtimebundle

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/configsource"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/standardplugins/legacyfeatureconfig"
)

// bootstrapEffectiveLoader is the singular startup effective-load operation;
// callers pass it per invocation and there is no package-global loader hook.
// The capability set is validated at load time (fail closed when empty).
type bootstrapEffectiveLoader func(ctx context.Context, path string, cliOverrides config.StreamRecoveryOverrides, capabilities config.CapabilitySet) (*config.EffectiveConfig, *configsource.ActiveSourceVersion, *configsource.SourceOwnerSlot, config.StreamRecoveryOverrides, error)

// LoadBootstrapEffectiveWithSource is the sole canonical startup effective-load
// owner (req: single config-load owner). It loads the fixed startup path
// through the shared strict effective-configuration pipeline used by
// BuildHost, ValidateDistribution, InspectRoutes/InspectInventory, and
// pkg/aipruntime.Build, plus the active source identity and the resolved
// CLI+environment stream-recovery override snapshot. The snapshot is captured
// exactly once for process composition and must be reused for every future
// effective reload (startup-fixed; no env reread).
//
// Order: stable source read → strict decode → defaults → fixed CLI/env
// stream-recovery overrides → standard feature injection → core validation →
// alias/prefix validation → private/public identity.
func LoadBootstrapEffectiveWithSource(ctx context.Context, path string, cliOverrides config.StreamRecoveryOverrides, capabilities config.CapabilitySet) (effective *config.EffectiveConfig, active *configsource.ActiveSourceVersion, ownerSlot *configsource.SourceOwnerSlot, fixed config.StreamRecoveryOverrides, err error) {
	if ctx == nil {
		return nil, nil, nil, config.StreamRecoveryOverrides{}, fmt.Errorf("runtimebundle: nil context")
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil, nil, config.StreamRecoveryOverrides{}, fmt.Errorf("runtimebundle: empty config path")
	}
	envOverrides, err := config.StreamRecoveryOverridesFromEnv()
	if err != nil {
		return nil, nil, nil, config.StreamRecoveryOverrides{}, err
	}
	merged := mergeStreamRecoveryOverrides(envOverrides, cliOverrides)
	src, err := configsource.NewFixedSource(path, 0)
	if err != nil {
		return nil, nil, nil, config.StreamRecoveryOverrides{}, err
	}
	snap, _, err := src.ReadStable(ctx, nil)
	if err != nil {
		return nil, nil, nil, config.StreamRecoveryOverrides{}, err
	}
	snapshotOwned := true
	transferred := false
	defer func() {
		if !transferred && ownerSlot != nil {
			err = errors.Join(err, ownerSlot.Close(ctx))
		}
		if snapshotOwned {
			err = errors.Join(err, snap.Close(ctx))
		}
		if err != nil {
			effective, active, ownerSlot = nil, nil, nil
		}
	}()
	eff, loadErr := config.LoadEffective(ctx, snap.Bytes, config.LoadEffectiveOptions{
		ConfigDir:           filepath.Dir(src.AbsolutePath()),
		Capabilities:        capabilities,
		FixedStreamRecovery: &merged,
		NormalizeYAML:       legacyfeatureconfig.NormalizeYAML,
		InjectFeatures:      injectStandardBootstrapFeatures,
		ExtraValidate:       extraBootstrapValidate,
	})
	if loadErr != nil {
		return nil, nil, nil, config.StreamRecoveryOverrides{}, loadErr
	}
	active, ownerSlot = snap.TakeBaseline()
	snapshotOwned = false
	effective, fixed = eff, merged
	transferred = true
	return effective, active, ownerSlot, fixed, nil
}

func injectStandardBootstrapFeatures(cfg *config.Config) error {
	if err := standardplugins.EnsureToolCallRepairInConfig(cfg, standardplugins.ToolCallRepairInjectOpts{
		StandardDistribution: true,
	}); err != nil {
		return fmt.Errorf("tool-call-repair defaults: %w", err)
	}
	if err := standardplugins.EnsureReasoningOutputPreservationInConfig(cfg, standardplugins.ReasoningOutputPreservationInjectOpts{
		StandardDistribution: true,
	}); err != nil {
		return fmt.Errorf("reasoning-output-preservation defaults: %w", err)
	}
	return nil
}

func extraBootstrapValidate(cfg *config.Config) error {
	if err := routing.ValidateModelAliasesConfig(cfg); err != nil {
		return err
	}
	if _, err := standardplugins.PrepareProviderProfiles(cfg); err != nil {
		return fmt.Errorf("runtimebundle: provider profiles: %w", err)
	}
	if err := standardplugins.ValidateCompatibleManifestOwnership(cfg.Plugins.Backends, nil); err != nil {
		return fmt.Errorf("runtimebundle: %w", err)
	}
	return nil
}
