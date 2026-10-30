package standardplugins

import (
	"strings"

	compatibleadmission "github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/compatible"
	"github.com/aiproxer/aiproxer/internal/core/config"
)

// CollectCompatibleAdmissionLimits returns positive per-instance limits for enabled
// generic-compatible backends keyed by exact runtime backend instance ID.
func CollectCompatibleAdmissionLimits(rows []config.PluginConfig) (compatibleadmission.Limits, error) {
	out := make(compatibleadmission.Limits)
	for _, row := range rows {
		if !row.Enabled {
			continue
		}
		kind := row.FactoryID()
		if !IsCustomCompatibleBackendKind(kind) {
			continue
		}
		cfg, err := config.DecodeCompatibleModeConfig(row.InstanceID(), kind, row.Config)
		if err != nil {
			return nil, err
		}
		if cfg.MaxConcurrentRequests <= 0 {
			continue
		}
		id := strings.TrimSpace(row.InstanceID())
		if id == "" {
			continue
		}
		out[id] = cfg.MaxConcurrentRequests
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}
