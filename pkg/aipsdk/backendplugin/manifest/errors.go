package manifest

import "errors"

var (
	ErrInvalidManifest      = errors.New("backendplugin/manifest: invalid manifest")
	ErrUnknownSchema        = errors.New("backendplugin/manifest: unknown schema")
	ErrUnknownField         = errors.New("backendplugin/manifest: unknown field")
	ErrForbiddenField       = errors.New("backendplugin/manifest: forbidden field")
	ErrUnsupportedExtension = errors.New("backendplugin/manifest: unsupported extension")
	ErrBoundsExceeded       = errors.New("backendplugin/manifest: bounds exceeded")
	ErrDuplicateExport      = errors.New("backendplugin/manifest: duplicate export kind")
	ErrInvalidExecutable    = errors.New("backendplugin/manifest: invalid executable path")
	ErrInvalidDigest        = errors.New("backendplugin/manifest: invalid sha256")
	ErrInvalidPlatform      = errors.New("backendplugin/manifest: invalid platform")
	// ErrInconsistentExportSecurityPosture rejects an export whose declared
	// credential mode contradicts its declared access scope. Under the host
	// principal model a user-scoped OAuth credential is never valid in a shared
	// deployment, so credential_mode: oauth_user must declare
	// access_scope: local_only instead of advertising broad eligibility.
	ErrInconsistentExportSecurityPosture = errors.New("backendplugin/manifest: inconsistent export security posture")
	// ErrMissingCredentialContract rejects a static provider-secret export
	// with no versioned credential contract. Unclassified static-key exports
	// fail closed: every new API-key connector is rejected without its
	// registration contract.
	ErrMissingCredentialContract = errors.New("backendplugin/manifest: missing credential contract for static export")
	// ErrUnexpectedCredentialContract rejects a non-static export carrying a
	// credential contract. Only static provider-secret consumers declare one,
	// so v1 non-static manifests keep validating unchanged.
	ErrUnexpectedCredentialContract = errors.New("backendplugin/manifest: unexpected credential contract for non-static export")
)
