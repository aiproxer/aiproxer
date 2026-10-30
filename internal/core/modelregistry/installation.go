package modelregistry

import (
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

// InstallationState is the deployment fact about one (backend, model) pair.
//
// It is a *fact about the deployment*, never about the operator's policy: an
// allowed inventory constrains what an owner may choose, and it is never
// presented as installation (design §3; Requirement 1.4, U7).
type InstallationState uint8

const (
	// InstallationUnknown means the publication does not prove whether the
	// backend carries the model. It is the zero value and the safe default:
	// an unknown answer is never a refusal.
	InstallationUnknown InstallationState = iota
	// InstallationPresent means the publication names the model for the
	// backend, as its canonical id or as its native id.
	InstallationPresent
	// InstallationAbsent means the publication proves the backend does not
	// carry the model: the backend enumerated its models and this one is not
	// among them.
	InstallationAbsent
)

// InstallationState reports the deployment fact about (backendID, model) in
// this publication.
//
// The rule is one table with one outcome per state:
//
//   - Present: a frozen row names the model for the backend, as its canonical
//     id or as its native id. A cached row is a valid positive: a cached
//     publication can prove installation.
//   - Absent: the backend's discovery status is "ok" — the backend enumerated
//     its models in this publication — and no frozen row names the model.
//   - Unknown: everything else. A "cached" status is a cached snapshot, so an
//     absence proves nothing. An "empty" status does not differentiate a fresh
//     enumeration that returned no models from a cached snapshot that had no
//     rows for the backend (the cached path produces the same status), so its
//     freshness is not proven and it never proves an absence. An
//     "unavailable" status, a missing discovery record and a canonical that is
//     registered under another backend (which says nothing about this
//     backend's native names) are unknown as well.
//
// The publication is a snapshot: a model installed after the last refresh is
// refused until the next refresh. That window is the operator's registry
// refresh cadence; it is documented and counted, never hidden, and no age
// threshold or timestamp heuristic is invented here.
func (v BoundView) InstallationState(backendID, model string) InstallationState {
	backendID = strings.TrimSpace(backendID)
	model = strings.TrimSpace(model)
	if backendID == "" || model == "" || v.pub == nil || v.pub.reg == nil {
		return InstallationUnknown
	}
	for i := range v.pub.reg.all {
		row := &v.pub.reg.all[i]
		if strings.TrimSpace(row.BackendID) != backendID {
			continue
		}
		if strings.TrimSpace(row.CanonicalID) == model || strings.TrimSpace(row.NativeID) == model {
			return InstallationPresent
		}
	}
	if v.knownUnderAnotherBackend(backendID, model) {
		// The canonical is registered under another backend. That proves
		// nothing about this backend's native names, so the absence is not
		// proven either.
		return InstallationUnknown
	}
	if v.discoveryStatus(backendID) == modelinventory.DiscoveryStatusOK {
		return InstallationAbsent
	}
	return InstallationUnknown
}

// knownUnderAnotherBackend reports whether the model is registered as the
// canonical id of a backend other than the named one.
func (v BoundView) knownUnderAnotherBackend(backendID, model string) bool {
	for i := range v.pub.reg.all {
		row := &v.pub.reg.all[i]
		if strings.TrimSpace(row.BackendID) == backendID {
			continue
		}
		if strings.TrimSpace(row.CanonicalID) == model {
			return true
		}
	}
	return false
}

// discoveryStatus returns the frozen discovery status of one backend, or the
// empty status when the publication carries no record for it.
func (v BoundView) discoveryStatus(backendID string) modelinventory.DiscoveryStatus {
	if v.pub == nil {
		return ""
	}
	for _, discovery := range v.pub.discoveries {
		if strings.TrimSpace(discovery.BackendID) == backendID {
			return discovery.Status
		}
	}
	return ""
}
