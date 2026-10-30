package acp

import (
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

// ConnectorConfig holds the normalized local-agent fields that are common to
// every ACP CLI subprocess connector (cursorcliacp, geminicliacp, agycliacp,
// codexappserver). Concrete connector Config structs embed this value so the
// YAML→connector bridges in product modules populate the shared fields
// once and append only vendor-specific extras.
//
// Vendor-specific fields (AutoAccept, TrustWorkspace, MCPServers,
// WrapperExecutable, ConfigOverrides, ...) stay on the concrete Config and are
// never duplicated here, so a change to one connector cannot silently shift
// decoding semantics for another.
type ConnectorConfig struct {
	// Executable is the path to the agent binary. If empty, the connector
	// resolves it from vendor-specific env vars or PATH.
	Executable string
	// Model is the default model (vendor prefix stripped). Connectors apply
	// their own default when empty inside New.
	Model string
	// ExtraArgs are additional CLI arguments appended after the standard flags.
	ExtraArgs []string
	// DefaultWorkspace is the fallback workspace directory.
	DefaultWorkspace string
	// IdleTimeout is the subprocess idle reaping timeout.
	IdleTimeout time.Duration
	// StaleKillDelay is the stale kill timer delay after a prompt turn.
	StaleKillDelay time.Duration
}

// ResolveVendorModel strips the vendor prefix from effective and applies the
// connector's default when effective is empty, "auto", or matches the bare/slash
// vendor prefix forms. configuredModel is the operator-configured default
// (typically from YAML); fallbackDefault is the connector's hardcoded default
// used when configuredModel is empty. This is the shared implementation that
// cursorcliacp, geminicliacp, and agycliacp previously triplicated; agy's
// internal vendor namespaces (e.g. "google/gemini-...") pass through unchanged
// because only the route-level "agy:" / "agy/" prefix is stripped.
func ResolveVendorModel(prefix, configuredModel, fallbackDefault, effective string) string {
	m := strings.TrimSpace(effective)
	if m == "" || m == prefix || m == prefix+":auto" || m == "auto" {
		def := strings.TrimSpace(configuredModel)
		if def == "" {
			def = fallbackDefault
		}
		return def
	}
	if strings.HasPrefix(m, prefix+":") {
		return strings.TrimSpace(m[len(prefix)+1:])
	}
	if strings.HasPrefix(m, prefix+"/") {
		return strings.TrimSpace(m[len(prefix)+1:])
	}
	return m
}

// DefaultInventoryModels builds a static model inventory from ids, prefixing
// each CanonicalID with "prefix/" and using the id verbatim as NativeID and
// DisplayName. This is the shared implementation that cursorcliacp,
// geminicliacp, agycliacp, and codexappserver previously duplicated. Callers
// pass a deduplicated id list. Returns nil when ids is empty (matching the
// var-slice + append idiom of the originals).
func DefaultInventoryModels(prefix string, ids []string) []modelinventory.Model {
	if len(ids) == 0 {
		return nil
	}
	models := make([]modelinventory.Model, 0, len(ids))
	for _, m := range ids {
		models = append(models, modelinventory.Model{
			CanonicalID: prefix + "/" + m,
			NativeID:    m,
			DisplayName: m,
		})
	}
	return models
}
