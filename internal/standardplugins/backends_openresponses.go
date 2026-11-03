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

// compatibleRowProjection is the kind-aware, secret-free view of one enabled
// built-in compatible row shared by the prefix, credential and admission
// startup projections. Each factory kind keeps its own strict decoder and
// schema; only the projection is common.
type compatibleRowProjection struct {
	Prefix                string
	APIKeyEnvVarRoot      string
	MaxConcurrentRequests int
}

// decodeCompatibleRowProjection decodes one compatible row through the strict
// decoder of its own factory kind. The generic OpenResponses schema accepts
// keys (for example capabilities) that the OpenAI-family compatible schema
// rejects, so every consumer must dispatch by kind instead of decoding every
// row with one schema.
func decodeCompatibleRowProjection(kind string, row config.PluginConfig) (compatibleRowProjection, error) {
	if IsOpenResponsesCompatibleBackendKind(kind) {
		cfg, err := openresponsescompat.DecodeConfig(row.InstanceID(), kind, row.Config)
		if err != nil {
			return compatibleRowProjection{}, err
		}
		// The generic OpenResponses schema has no max_concurrent_requests key,
		// so this kind contributes no per-instance admission limit.
		return compatibleRowProjection{
			Prefix:           cfg.BackendPrefix,
			APIKeyEnvVarRoot: cfg.APIKeyEnvVarRoot,
		}, nil
	}
	cfg, err := config.DecodeCompatibleModeConfig(row.InstanceID(), kind, row.Config)
	if err != nil {
		return compatibleRowProjection{}, err
	}
	return compatibleRowProjection{
		Prefix:                cfg.BackendPrefix,
		APIKeyEnvVarRoot:      cfg.APIKeyEnvVarRoot,
		MaxConcurrentRequests: cfg.MaxConcurrentRequests,
	}, nil
}

// DecodeCompatibleBackendPrefix returns the enabled-row backend_prefix for any
// built-in compatible factory kind, dispatching to the kind-specific strict
// decoder so prefix/instance ownership validation applies to the OpenResponses
// config surface as well.
func DecodeCompatibleBackendPrefix(kind string, row config.PluginConfig) (string, error) {
	projection, err := decodeCompatibleRowProjection(kind, row)
	if err != nil {
		return "", err
	}
	return projection.Prefix, nil
}
