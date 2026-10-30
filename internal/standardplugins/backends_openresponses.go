package standardplugins

import (
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openresponsescompat"
)

// CustomOpenResponsesCompatibleID is the stable built-in-compatible factory kind
// for the generic remote OpenResponses backend (Requirement 9.1).
const CustomOpenResponsesCompatibleID = "custom-openresponses-compatible"

// IsOpenResponsesCompatibleBackendKind reports whether id is the generic
// OpenResponses built-in-compatible factory kind.
func IsOpenResponsesCompatibleBackendKind(id string) bool {
	return strings.TrimSpace(id) == CustomOpenResponsesCompatibleID
}

// DecodeCompatibleBackendPrefix returns the enabled-row backend_prefix for any
// built-in compatible factory kind, dispatching to the kind-specific strict
// decoder so prefix/instance ownership validation applies to the OpenResponses
// config surface as well.
func DecodeCompatibleBackendPrefix(kind string, row config.PluginConfig) (string, error) {
	if IsOpenResponsesCompatibleBackendKind(kind) {
		cfg, err := openresponsescompat.DecodeConfig(row.InstanceID(), kind, row.Config)
		if err != nil {
			return "", err
		}
		return cfg.BackendPrefix, nil
	}
	cfg, err := config.DecodeCompatibleModeConfig(row.InstanceID(), kind, row.Config)
	if err != nil {
		return "", err
	}
	return cfg.BackendPrefix, nil
}
