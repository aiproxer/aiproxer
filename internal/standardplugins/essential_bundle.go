package standardplugins

import (
	"slices"
)

// EssentialBackendKinds derives the built-in allowlist from contributions.
// Optional connector identities never enter this view.
func EssentialBackendKinds() []string { return essentialBackendKinds() }

func essentialBackendKinds() []string {
	views, err := DerivedViews()
	if err != nil {
		return nil
	}
	return append([]string(nil), views.EssentialIDs...)
}

// IsEssentialBackendKind reports whether id is in the final essential allowlist.
func IsEssentialBackendKind(id string) bool {
	return slices.Contains(EssentialBackendKinds(), id)
}

// EssentialBackendBundle returns only the five essential families plus approved
// dependency-free compatible modes.
func EssentialBackendBundle(keys UpstreamAPIKeys) Bundle {
	return Bundle{Backends: backendRegistrationsFrom(standardBackendContributions(keys))}
}
