package diagnostics

import (
	"sort"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/catalog"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/discovery"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	sdkmanifest "github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/manifest"
)

// DefaultHostProtocolMajor is the host ABI major version used for inspect compatibility.
const DefaultHostProtocolMajor uint32 = 1

// ConfiguredBackend is one enabled/disabled backend row from operator config.
type ConfiguredBackend struct {
	InstanceID string
	Kind       string
	Enabled    bool
}

// Entry is one bounded inspect row (no secrets or full filesystem paths in Reason).
type Entry struct {
	Source             string         `json:"source"` // builtin | discovered | configured
	InstanceID         string         `json:"instance_id,omitempty"`
	SafeID             string         `json:"safe_id,omitempty"`
	PluginID           string         `json:"plugin_id,omitempty"`
	Kind               string         `json:"kind,omitempty"`
	State              catalog.State  `json:"state"`
	Reason             catalog.Reason `json:"reason,omitempty"`
	Owners             []string       `json:"owners,omitempty"`
	ActivationRequired bool           `json:"activation_required"`
}

// Report is the inspect snapshot.
type Report struct {
	Entries            []Entry                     `json:"entries"`
	CompatibleBackends []diag.CompatibleBackendRow `json:"compatible_backends,omitempty"`
}

// TrustFunc verifies a discovered manifest executable without launching it.
type TrustFunc func(root string, m sdkmanifest.Manifest, opt trust.VerifyOptions) trust.VerifyResult

// InspectInput configures a non-executing inspect pass.
type InspectInput struct {
	DiscoveryEnabled bool
	Discovery        discovery.Config
	BuiltinKinds     []string
	Configured       []ConfiguredBackend
	Strict           bool
	HostMajor        uint32
	HostMinor        uint32
	StagingDir       string
	// Trust is optional; defaults to trust.Verify.
	Trust TrustFunc
	// Discover is optional; defaults to discovery.Discover (tests may stub).
	Discover func(discovery.Config) (discovery.Result, error)
}

// Inspect scans/parses/trusts/catalogs without process launch.
func Inspect(in InspectInput) (Report, error) {
	res, err := ResolveCatalog(in)
	if err != nil {
		return Report{}, err
	}
	return FormatInspectReport(res, in.BuiltinKinds, in.Configured), res.CatalogErr
}

// FormatInspectReport projects a CatalogResolution into operator-facing entries.
func FormatInspectReport(res CatalogResolution, builtinKinds []string, configured []ConfiguredBackend) Report {
	return FormatInspectReportWithCompatible(res, builtinKinds, nil, configured)
}

// FormatInspectReportWithCompatible splits native built-ins from built-in-compatible factory kinds.
func FormatInspectReportWithCompatible(res CatalogResolution, builtinKinds, builtinCompatibleKinds []string, configured []ConfiguredBackend) Report {
	builtinSet := map[string]struct{}{}
	for _, k := range builtinKinds {
		builtinSet[k] = struct{}{}
	}
	compatibleSet := map[string]struct{}{}
	for _, k := range builtinCompatibleKinds {
		compatibleSet[k] = struct{}{}
		delete(builtinSet, k)
	}

	var entries []Entry
	for _, k := range builtinKinds {
		entries = append(entries, Entry{
			Source: "builtin",
			Kind:   k,
			State:  catalog.StateBuiltin,
			Reason: catalog.ReasonOK,
		})
	}
	for _, k := range builtinCompatibleKinds {
		entries = append(entries, Entry{
			Source: "built_in_compatible",
			Kind:   k,
			State:  catalog.StateBuiltin,
			Reason: catalog.ReasonOK,
		})
	}

	seenConfiguredKind := map[string]bool{}
	for _, e := range res.Snapshot.Entries {
		src := "discovered"
		act := e.State == catalog.StateDiscovered || e.State == catalog.StateConfigured
		if e.State == catalog.StateConfigured {
			src = "configured"
			seenConfiguredKind[e.ExportKind] = true
		}
		if e.State == catalog.StateFailed && (e.Reason == catalog.ReasonEnabledMissing || e.Reason == catalog.ReasonEnabledInvalid) {
			src = "configured"
		}
		if _, ok := compatibleSet[e.ExportKind]; ok && e.Reason == catalog.ReasonBuiltinCollision {
			src = "discovered"
		}
		if _, ok := builtinSet[e.ExportKind]; ok && e.Reason == catalog.ReasonBuiltinCollision {
			src = "discovered"
		}
		entries = append(entries, Entry{
			Source:             src,
			SafeID:             e.SafeID,
			PluginID:           e.PluginID,
			Kind:               e.ExportKind,
			State:              e.State,
			Reason:             e.Reason,
			Owners:             append([]string(nil), e.Owners...),
			ActivationRequired: act && e.State == catalog.StateConfigured,
		})
	}

	for _, c := range configured {
		if !c.Enabled {
			continue
		}
		if _, isCompatible := compatibleSet[c.Kind]; isCompatible {
			entries = append(entries, Entry{
				Source:             "built_in_compatible",
				InstanceID:         c.InstanceID,
				Kind:               c.Kind,
				State:              catalog.StateConfigured,
				Reason:             catalog.ReasonOK,
				ActivationRequired: false,
			})
			continue
		}
		if _, isBuiltin := builtinSet[c.Kind]; isBuiltin {
			entries = append(entries, Entry{
				Source:             "configured",
				InstanceID:         c.InstanceID,
				Kind:               c.Kind,
				State:              catalog.StateConfigured,
				Reason:             catalog.ReasonOK,
				ActivationRequired: false,
			})
			continue
		}
		if seenConfiguredKind[c.Kind] {
			entries = append(entries, Entry{
				Source:             "configured",
				InstanceID:         c.InstanceID,
				Kind:               c.Kind,
				State:              catalog.StateConfigured,
				Reason:             catalog.ReasonOK,
				ActivationRequired: true,
			})
			continue
		}
		entries = append(entries, Entry{
			Source:             "configured",
			InstanceID:         c.InstanceID,
			Kind:               c.Kind,
			State:              catalog.StateFailed,
			Reason:             catalog.ReasonEnabledMissing,
			ActivationRequired: true,
		})
	}

	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Source != entries[j].Source {
			return entries[i].Source < entries[j].Source
		}
		if entries[i].Kind != entries[j].Kind {
			return entries[i].Kind < entries[j].Kind
		}
		return entries[i].InstanceID < entries[j].InstanceID
	})

	return Report{Entries: entries}
}

// WithCompatibleBackends attaches config-derived compatible rows to a report.
func (r Report) WithCompatibleBackends(rows []diag.CompatibleBackendRow) Report {
	r.CompatibleBackends = rows
	return r
}
