package manifest

import "errors"

var (
	// ErrUnregisteredCredentialKind rejects a static provider-secret export
	// whose factory kind has no canonical credential-namespace registration.
	// Each new API-key connector is rejected without its registration
	// contract, even when it carries a well-formed manifest block.
	ErrUnregisteredCredentialKind = errors.New("backendplugins/manifest: unregistered static credential kind")
	// ErrCredentialNamespaceMismatch rejects a manifest contract that
	// diverges from the host's canonical registration for its kind. The
	// manifest-authored binding is host authority at admission, but it must
	// agree with the registered provider namespace: silent drift would split
	// quota identity and break explicit sharing/isolation.
	ErrCredentialNamespaceMismatch = errors.New("backendplugins/manifest: credential contract diverges from registration")
	// ErrDescriptorManifestMismatch rejects a runtime Describe descriptor
	// that disagrees with its packaged manifest on export identity or
	// credential posture. Errors name only kinds and posture values, never
	// secret material.
	ErrDescriptorManifestMismatch = errors.New("backendplugins/manifest: descriptor disagrees with manifest")
)
