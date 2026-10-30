package pluginreg

import "github.com/aiproxer/aiproxer/internal/core/identity"

// GenericBackendFactoryDeps is the host dependency surface for essential
// in-process backend factories. It must not name Codex, OpenCode, ACP-product,
// or other provider-specific collaborators.
//
// External executable plugins do not receive this type: they receive opaque
// YAML, secrets, and pkg/aipsdk/backendplugin.RuntimePolicy only.
type GenericBackendFactoryDeps struct {
	Identity identity.Config
	// Credentials is the authorized per-candidate numbered-credential
	// snapshot. Factories prefer it over Install-time or request-time
	// environment reads; the zero value preserves the legacy source.
	Credentials HostCredentialSnapshot
}
