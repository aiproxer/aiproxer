package manifest

import (
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// SchemaV1 is the only closed manifest schema identifier accepted in Phase 2.
const SchemaV1 = "aiproxer.backendplugin.manifest/v1"

// Manifest is the public installation metadata document for one plugin artifact.
type Manifest struct {
	Schema           string
	PluginID         string
	Version          string
	BuildID          string
	Executable       string
	SHA256           string
	ProtocolMajor    uint32
	ProtocolMinMinor uint32
	ProtocolMaxMinor uint32
	Platforms        []Platform
	Exports          []Export
	Extensions       []Extension
}

// Platform is one supported OS/architecture pair.
type Platform struct {
	OS   string
	Arch string
}

// Export describes one factory kind exported by the plugin.
type Export struct {
	Kind           string
	DisplayName    string
	Description    string
	CredentialMode backendplugin.CredentialMode
	AccessScope    backendplugin.AccessScope
	ProcessSharing backendplugin.ProcessSharing
	ExecutionClass aipsdk.BackendExecutionClass
	Experimental   bool
	Deprecated     bool
	// CredentialContract is the optional versioned nonsecret static-key
	// registration for this export. It is required for static
	// provider-secret consumers and forbidden for every other credential
	// mode, so v1 non-static manifests validate unchanged while unclassified
	// static-key exports fail closed.
	CredentialContract *backendplugin.CredentialContract
}

// Extension is a versioned, allowlisted metadata block. v1 ships with an empty
// allowlist; non-empty unknown extensions are rejected by the strict parser.
type Extension struct {
	Name    string
	Version uint32
}
