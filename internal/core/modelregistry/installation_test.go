package modelregistry

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

// The installation fact of design §3: one rule, one outcome per state, and a
// cached publication that can prove installation but never non-installation.

// installedPublication is a frozen publication with one backend, one model
// row and the given discovery status.
func installedPublication(status modelinventory.DiscoveryStatus) BoundView {
	row := BackendModel{
		BackendID:   "openai",
		CanonicalID: "openai/test-model",
		NativeID:    "test-model",
	}
	return BoundView{pub: &published{
		reg: &Registry{
			byCanonical: map[string][]BackendModel{"openai/test-model": {row}},
			all:         []BackendModel{row},
		},
		snap: Snapshot{Generation: "gen-1"},
		discoveries: []BackendDiscovery{{
			BackendID: "openai",
			Status:    status,
		}},
	}}
}

// TestInstallationStateProvesInstallation pins the positive rule: a frozen row
// names the model for the backend, as its canonical id or as its native id,
// and a cached publication proves it just as a fresh one does.
func TestInstallationStateProvesInstallation(t *testing.T) {
	t.Parallel()

	for _, status := range []modelinventory.DiscoveryStatus{
		modelinventory.DiscoveryStatusOK,
		modelinventory.DiscoveryStatusCached,
		modelinventory.DiscoveryStatusUnavailable,
	} {
		view := installedPublication(status)
		if got := view.InstallationState("openai", "openai/test-model"); got != InstallationPresent {
			t.Fatalf("canonical installation = %v, want present", got)
		}
		if got := view.InstallationState("openai", "test-model"); got != InstallationPresent {
			t.Fatalf("native installation = %v, want present", got)
		}
	}
}

// TestInstallationStateProvesAbsenceOnlyFromAFreshEnumeration pins the
// negative rule: only a fresh enumeration proves that the backend does not
// carry the model.
func TestInstallationStateProvesAbsenceOnlyFromAFreshEnumeration(t *testing.T) {
	t.Parallel()

	view := installedPublication(modelinventory.DiscoveryStatusOK)
	if got := view.InstallationState("openai", "openai/not-installed"); got != InstallationAbsent {
		t.Fatalf("installation = %v, want absent", got)
	}
}

// TestInstallationStateNeverInventsAnAbsence pins the honesty rule: a cached
// snapshot, an empty discovery, an unavailable discovery and a missing
// discovery record all leave the fact unproven, so the state is unknown and
// never a refusal.
func TestInstallationStateNeverInventsAnAbsence(t *testing.T) {
	t.Parallel()

	for _, status := range []modelinventory.DiscoveryStatus{
		modelinventory.DiscoveryStatusCached,
		modelinventory.DiscoveryStatusEmpty,
		modelinventory.DiscoveryStatusUnavailable,
	} {
		view := installedPublication(status)
		if got := view.InstallationState("openai", "openai/not-installed"); got != InstallationUnknown {
			t.Fatalf("installation = %v, want unknown for status %q", got, status)
		}
	}
}

// TestInstallationStateIgnoresAnotherBackendsModel pins the wrong-backend
// rule: a canonical that is registered under another backend says nothing
// about this backend's native names.
func TestInstallationStateIgnoresAnotherBackendsModel(t *testing.T) {
	t.Parallel()

	row := BackendModel{
		BackendID:   "other",
		CanonicalID: "openai/test-model",
		NativeID:    "test-model",
	}
	view := BoundView{pub: &published{
		reg: &Registry{
			byCanonical: map[string][]BackendModel{"openai/test-model": {row}},
			all:         []BackendModel{row},
		},
		snap: Snapshot{Generation: "gen-1"},
		discoveries: []BackendDiscovery{{
			BackendID: "openai",
			Status:    modelinventory.DiscoveryStatusOK,
		}},
	}}
	if got := view.InstallationState("openai", "openai/test-model"); got != InstallationUnknown {
		t.Fatalf("installation = %v, want unknown", got)
	}
	// The native name belongs to the other backend: the fresh enumeration of
	// this backend proves that this backend does not carry it.
	if got := view.InstallationState("openai", "test-model"); got != InstallationAbsent {
		t.Fatalf("installation = %v, want absent", got)
	}
}

// TestInstallationStateNeedsAPublication pins the absent-publication rule: no
// publication proves nothing.
func TestInstallationStateNeedsAPublication(t *testing.T) {
	t.Parallel()

	if got := EmptyBoundView().InstallationState("openai", "openai/test-model"); got != InstallationUnknown {
		t.Fatalf("installation = %v, want unknown", got)
	}
}
