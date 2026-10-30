package runtimebundle

import (
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
)

// secretGuardFromPlanes reconstructs the generation-composed secret-guard
// engine posture and diagnostics inventory purely from the ordinary frozen
// plane surface (PlaneSecretGuardExecution). Guards are intentionally not
// read here: the request snapshot overlays guards from its feature planes on
// every access (SecretGuardPlane/SecretGuardExecutionPlane), so carrying them
// in the plane would be dead weight. It returns a zero plane and nil
// inventory when the generation carries no composed execution config
// (secret guard disabled).
func secretGuardFromPlanes(frozen aipfeature.FrozenPlaneSet) (extensions.SecretGuardPlane, *diag.InventorySecretGuard) {
	execCfg := aipfeature.Get(frozen, aipfeature.PlaneSecretGuardExecution)
	if execCfg == nil || execCfg.IsZero() {
		return extensions.SecretGuardPlane{}, nil
	}
	plane := extensions.SecretGuardPlane{
		MatcherResolver:    execCfg.MatcherResolver,
		Capability:         execCfg.Capability,
		DecisionObserver:   execCfg.DecisionObserver,
		AuditFailurePolicy: execCfg.AuditFailurePolicy,
		AccessMode:         execCfg.AccessMode,
		ConfigVersion:      execCfg.ConfigVersion,
	}
	var ruleCount, decodeDepth *int
	if execCfg.BetterLeaksEnabled {
		rules, depth := execCfg.BetterLeaksRuleCount, execCfg.BetterLeaksDecodeDepth
		ruleCount, decodeDepth = &rules, &depth
	}
	return plane, &diag.InventorySecretGuard{
		Action:                    strings.TrimSpace(execCfg.CatalogAction),
		CatalogEntryCount:         execCfg.CatalogEntryCount,
		SourceCategories:          append([]string(nil), execCfg.SourceCategories...),
		AccessMode:                execCfg.AccessMode,
		LocalAutoDiscoveryEnabled: execCfg.LocalAutoDiscoveryEnabled,
		BetterLeaksEnabled:        execCfg.BetterLeaksEnabled,
		BetterLeaksVersion:        execCfg.BetterLeaksVersion,
		BetterLeaksConfigHash:     execCfg.BetterLeaksConfigHash,
		BetterLeaksRuleCount:      ruleCount,
		BetterLeaksConfidence:     execCfg.BetterLeaksConfidence,
		BetterLeaksDecodeDepth:    decodeDepth,
		BetterLeaksWorkers:        execCfg.BetterLeaksWorkers,
		DiscoveryDetectorCount:    execCfg.DiscoveryDetectorCount,
	}
}
