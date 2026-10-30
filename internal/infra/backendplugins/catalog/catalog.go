package catalog

import (
	"slices"
	"sort"

	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/discovery"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	sdkmanifest "github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/manifest"
)

// Entry is one inspect-ready catalog record.
type Entry struct {
	SafeID     string
	PluginID   string
	ExportKind string
	State      State
	Reason     Reason
	Owners     []string // SafeIDs involved in a conflict
}

// Snapshot is an immutable catalog view.
type Snapshot struct {
	Entries []Entry
}

// Input feeds catalog resolution.
type Input struct {
	Discovered   []discovery.Descriptor
	TrustBySafe  map[string]trust.VerifyResult
	BuiltinKinds []string
	EnabledKinds []string
	Strict       bool
	HostMajor    uint32
	HostMinor    uint32
}

// Resolve builds a deterministic conflict-aware catalog snapshot.
// It never silently picks one of two colliding artifacts.
func Resolve(in Input) (Snapshot, error) {
	builtin := map[string]struct{}{}
	for _, k := range in.BuiltinKinds {
		builtin[k] = struct{}{}
	}
	type owner struct {
		safeID string
		plugin string
		kind   string
		m      sdkmanifest.Manifest
		status discovery.Status
	}
	var owners []owner
	for _, d := range in.Discovered {
		if d.Status != discovery.StatusDiscovered {
			owners = append(owners, owner{safeID: d.SafeID, plugin: d.Manifest.PluginID, status: d.Status})
			continue
		}
		for _, exp := range d.Manifest.Exports {
			owners = append(owners, owner{
				safeID: d.SafeID, plugin: d.Manifest.PluginID, kind: exp.Kind, m: d.Manifest, status: d.Status,
			})
		}
	}

	pluginOwners := map[string][]string{}
	kindOwners := map[string][]string{}
	for _, o := range owners {
		if o.status != discovery.StatusDiscovered || o.plugin == "" {
			continue
		}
		pluginOwners[o.plugin] = appendUnique(pluginOwners[o.plugin], o.safeID)
		if o.kind != "" {
			kindOwners[o.kind] = appendUnique(kindOwners[o.kind], o.safeID)
		}
	}

	var entries []Entry
	seenEntry := map[string]struct{}{}
	for _, d := range in.Discovered {
		base := Entry{SafeID: d.SafeID, PluginID: d.Manifest.PluginID}
		if d.Status == discovery.StatusInvalid {
			base.State = StateInvalid
			base.Reason = ReasonInvalidUnused
			entries = append(entries, base)
			continue
		}
		if d.Status != discovery.StatusDiscovered {
			base.State = StateFailed
			base.Reason = ReasonUntrusted
			entries = append(entries, base)
			continue
		}
		tr, ok := in.TrustBySafe[d.SafeID]
		if !ok || tr.Reason != trust.ReasonOK || tr.Artifact == nil {
			base.State = StateUntrusted
			base.Reason = ReasonUntrusted
			if ok && tr.Reason == trust.ReasonDigestMismatch {
				base.State = StateDigestMismatch
				base.Reason = ReasonDigestMismatch
			}
			entries = append(entries, base)
			continue
		}
		if d.Manifest.ProtocolMajor != in.HostMajor ||
			in.HostMinor < d.Manifest.ProtocolMinMinor ||
			in.HostMinor > d.Manifest.ProtocolMaxMinor {
			base.State = StateIncompatible
			base.Reason = ReasonProtocolIncompatible
			entries = append(entries, base)
			continue
		}
		if outs := pluginOwners[d.Manifest.PluginID]; len(outs) > 1 {
			base.State = StateConflict
			base.Reason = ReasonDuplicatePluginID
			base.Owners = cloneStrings(outs)
			entries = append(entries, base)
			continue
		}
		for _, exp := range d.Manifest.Exports {
			key := d.SafeID + ":" + exp.Kind
			if _, ok := seenEntry[key]; ok {
				continue
			}
			seenEntry[key] = struct{}{}
			e := Entry{SafeID: d.SafeID, PluginID: d.Manifest.PluginID, ExportKind: exp.Kind}
			if _, ok := builtin[exp.Kind]; ok {
				e.State = StateConflict
				e.Reason = ReasonBuiltinCollision
				e.Owners = cloneStrings([]string{d.SafeID, "builtin:" + exp.Kind})
				entries = append(entries, e)
				continue
			}
			if outs := kindOwners[exp.Kind]; len(outs) > 1 {
				e.State = StateConflict
				e.Reason = ReasonDuplicateExportKind
				e.Owners = cloneStrings(outs)
				entries = append(entries, e)
				continue
			}
			e.State = StateDiscovered
			e.Reason = ReasonOK
			entries = append(entries, e)
		}
	}

	enabledErr := false
	for _, kind := range in.EnabledKinds {
		foundOK := false
		foundInvalid := false
		for _, e := range entries {
			if e.ExportKind != kind {
				continue
			}
			if e.State == StateDiscovered || e.State == StateConfigured || e.State == StateActive {
				foundOK = true
			}
			if e.State == StateInvalid || e.State == StateConflict || e.State == StateUntrusted ||
				e.State == StateDigestMismatch || e.State == StateIncompatible {
				foundInvalid = true
			}
		}
		if !foundOK {
			enabledErr = true
			reason := ReasonEnabledMissing
			if foundInvalid {
				reason = ReasonEnabledInvalid
			}
			entries = append(entries, Entry{
				ExportKind: kind, State: StateFailed, Reason: reason,
			})
		}
	}

	enabledSet := map[string]struct{}{}
	for _, kind := range in.EnabledKinds {
		enabledSet[kind] = struct{}{}
	}
	for i := range entries {
		if entries[i].State == StateDiscovered {
			if _, ok := enabledSet[entries[i].ExportKind]; ok {
				entries[i].State = StateConfigured
			}
		}
	}

	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].SafeID != entries[j].SafeID {
			return entries[i].SafeID < entries[j].SafeID
		}
		return entries[i].ExportKind < entries[j].ExportKind
	})

	if in.Strict {
		for _, e := range entries {
			if e.State == StateInvalid || e.Reason == ReasonInvalidUnused {
				enabledErr = true
			}
		}
	}
	if enabledErr && len(in.EnabledKinds) > 0 {
		// Fatal resolution signal for enabled missing/invalid.
		return Snapshot{Entries: entries}, ErrEnabledUnresolved
	}
	if in.Strict {
		for _, e := range entries {
			if e.State == StateInvalid {
				return Snapshot{Entries: entries}, ErrStrictInvalid
			}
		}
	}
	return Snapshot{Entries: entries}, nil
}

func appendUnique(in []string, v string) []string {
	if slices.Contains(in, v) {
		return in
	}
	return append(in, v)
}

func cloneStrings(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}
