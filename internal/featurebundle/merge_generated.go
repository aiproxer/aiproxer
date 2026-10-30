package featurebundle

import (
	"errors"
	"fmt"
	"slices"

	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	aipplugin "github.com/aiproxer/aiproxer/pkg/aipsdk/plugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
)

// HostContributions holds host-provided contributions (process options) that can be merged
// alongside feature plugin contributions. Host contributions are merged under SourceHost
// and are strictly limited to planes that permit host contributions (TrafficObservers, UsageObservers).
type HostContributions struct {
	TrafficObservers []traffic.Observer
	UsageObservers   []usage.Observer
}

// ContributeHost contributes host-level observer contributions into the given ContributionSet
// with SourceHost semantics.
func ContributeHost(cs *aipfeature.ContributionSet, host HostContributions) error {
	if cs == nil {
		return errors.New("featurebundle: nil ContributionSet")
	}
	if len(host.TrafficObservers) > 0 {
		if err := aipfeature.ContributeSource(cs, aipfeature.PlaneTrafficObservers, aipfeature.SourceHost, "host", slices.Clone(host.TrafficObservers)); err != nil {
			return err
		}
	}
	if len(host.UsageObservers) > 0 {
		if err := aipfeature.ContributeSource(cs, aipfeature.PlaneUsageObservers, aipfeature.SourceHost, "host", slices.Clone(host.UsageObservers)); err != nil {
			return err
		}
	}
	return nil
}

// GeneratedMergeSurface represents the composed result of feature contributions merged through
// generated typed plane adapters. It holds an immutable FrozenPlaneSet containing all declared
// plane values, and a separate Lifecycles slice side-channel (lifecycles are not an extension plane).
type GeneratedMergeSurface struct {
	Frozen             aipfeature.FrozenPlaneSet
	Lifecycles         []aipplugin.Lifecycle
	set                *aipfeature.ContributionSet
	featureDiagnostics []FeatureDiagnosticProjection
}

// FeatureDiagnosticProjection contains one feature instance's immutable diagnostic projections.
type FeatureDiagnosticProjection struct {
	InstanceID  string
	Projections []aipfeature.DiagnosticPlaneProjection
}

// FeatureDiagnosticProjections returns a defensive copy of the projections captured during merge.
func (g GeneratedMergeSurface) FeatureDiagnosticProjections() []FeatureDiagnosticProjection {
	if g.featureDiagnostics == nil {
		return nil
	}
	out := make([]FeatureDiagnosticProjection, len(g.featureDiagnostics))
	for i, feature := range g.featureDiagnostics {
		out[i] = FeatureDiagnosticProjection{
			InstanceID:  feature.InstanceID,
			Projections: cloneDiagnosticPlaneProjections(feature.Projections),
		}
	}
	return out
}

func cloneDiagnosticPlaneProjections(in []aipfeature.DiagnosticPlaneProjection) []aipfeature.DiagnosticPlaneProjection {
	if in == nil {
		return nil
	}
	out := slices.Clone(in)
	for i := range out {
		out[i].Privileges.Flags = slices.Clone(in[i].Privileges.Flags)
		out[i].Occupants = slices.Clone(in[i].Occupants)
		for j := range out[i].Occupants {
			out[i].Occupants[j].Privileges = slices.Clone(in[i].Occupants[j].Privileges)
		}
	}
	return out
}

func (g GeneratedMergeSurface) workingSet() *aipfeature.ContributionSet {
	if g.set != nil {
		return g.set.Clone()
	}
	return g.Frozen.ToContributions()
}

// BindStreamObserverFactories replaces stream observer factories contributed by contributorID
// in this GeneratedMergeSurface under SourceGenerationBinder semantics (CombReplaceByIdentity).
// If validation or combination fails, the candidate is left unmodified and an error is returned.
func (g GeneratedMergeSurface) BindStreamObserverFactories(contributorID string, factories []response.StreamObserverFactory) (GeneratedMergeSurface, error) {
	working := g.workingSet()
	if err := working.BindStreamObserverFactories(contributorID, factories); err != nil {
		return GeneratedMergeSurface{}, err
	}
	return GeneratedMergeSurface{
		Frozen:             working.Freeze(),
		Lifecycles:         slices.Clone(g.Lifecycles),
		set:                working,
		featureDiagnostics: g.featureDiagnostics,
	}, nil
}

// BindAttemptTransforms replaces attempt transforms contributed by contributorID
// in this GeneratedMergeSurface under SourceGenerationBinder semantics (CombReplaceByIdentity).
// If validation or combination fails, the candidate is left unmodified and an error is returned.
func (g GeneratedMergeSurface) BindAttemptTransforms(contributorID string, transforms []request.AttemptTransform) (GeneratedMergeSurface, error) {
	working := g.workingSet()
	if err := working.BindAttemptTransforms(contributorID, transforms); err != nil {
		return GeneratedMergeSurface{}, err
	}
	return GeneratedMergeSurface{
		Frozen:             working.Freeze(),
		Lifecycles:         slices.Clone(g.Lifecycles),
		set:                working,
		featureDiagnostics: g.featureDiagnostics,
	}, nil
}

// BindCompactionPreservers replaces compaction preservers contributed by contributorID
// in this GeneratedMergeSurface under SourceGenerationBinder semantics (CombReplaceByIdentity).
// If validation or combination fails, the candidate is left unmodified and an error is returned.
func (g GeneratedMergeSurface) BindCompactionPreservers(contributorID string, preservers []compaction.Preserver) (GeneratedMergeSurface, error) {
	working := g.workingSet()
	if err := working.BindCompactionPreservers(contributorID, preservers); err != nil {
		return GeneratedMergeSurface{}, err
	}
	return GeneratedMergeSurface{
		Frozen:             working.Freeze(),
		Lifecycles:         slices.Clone(g.Lifecycles),
		set:                working,
		featureDiagnostics: g.featureDiagnostics,
	}, nil
}

// MergeCandidatePlanes merges candidate feature planes under SourceFeature into the surface.
// If candidate planes are zero/empty, the surface is returned unmodified.
// If validation or combination fails, the surface is left unmodified and an error is returned.
func (g GeneratedMergeSurface) MergeCandidatePlanes(cand aipfeature.FrozenPlaneSet) (GeneratedMergeSurface, error) {
	if cand.IsZero() {
		return g, nil
	}
	working := g.workingSet()
	if err := working.ContributeCandidate(cand); err != nil {
		return GeneratedMergeSurface{}, err
	}
	return GeneratedMergeSurface{
		Frozen:             working.Freeze(),
		Lifecycles:         slices.Clone(g.Lifecycles),
		set:                working,
		featureDiagnostics: g.featureDiagnostics,
	}, nil
}

// ContributeBundle contributes all planes from FeatureBundle b into the given ContributionSet
// using the provided pluginID via b.PlaneSet.ReplayTo. It validates the bundle schema before replay.
// If b.PlaneSet is zero, it is a no-op after schema validation.
func ContributeBundle(cs *aipfeature.ContributionSet, pluginID string, b aipfeature.FeatureBundle) error {
	if cs == nil {
		return errors.New("featurebundle: nil ContributionSet")
	}
	if pluginID == "" {
		pluginID = "feature"
	}
	if err := b.Validate(); err != nil {
		return fmt.Errorf("featurebundle: invalid bundle %q: %w", pluginID, err)
	}
	return b.PlaneSet.ReplayTo(cs, pluginID)
}

// FreezeBundle converts a single FeatureBundle into a FrozenPlaneSet with pluginID.
func FreezeBundle(b aipfeature.FeatureBundle, pluginID string) (aipfeature.FrozenPlaneSet, error) {
	cs := aipfeature.NewContributionSet()
	if pluginID == "" {
		if id, ok := aipfeature.FrozenIdentity(b.PlaneSet, aipfeature.PlaneTerminalDecisionProvider); ok && id != "" {
			pluginID = id
		}
		if pluginID == "" {
			pluginID = "feature"
		}
	}
	if err := ContributeBundle(cs, pluginID, b); err != nil {
		return aipfeature.FrozenPlaneSet{}, err
	}
	return cs.Freeze(), nil
}

// MergeBundlesGenerated merges one or more FeatureBundles through generated plane adapters
// in registration order, returning a GeneratedMergeSurface containing the frozen plane set and
// separate lifecycles. If any contribution fails validation or conflicts, the candidate is
// discarded and an empty GeneratedMergeSurface and error are returned.
func MergeBundlesGenerated(bundles ...aipfeature.FeatureBundle) (GeneratedMergeSurface, error) {
	cs := aipfeature.NewContributionSet()
	var lifecycles []aipplugin.Lifecycle
	diagnostics := make([]FeatureDiagnosticProjection, 0, len(bundles))
	for i, b := range bundles {
		pluginID := fmt.Sprintf("bundle-%d", i)
		if id, ok := aipfeature.FrozenIdentity(b.PlaneSet, aipfeature.PlaneTerminalDecisionProvider); ok && id != "" {
			pluginID = id
		}
		if err := ContributeBundle(cs, pluginID, b); err != nil {
			return GeneratedMergeSurface{}, err
		}
		lifecycles = appendLifecycles(lifecycles, b.Lifecycles)
		diagnostics = append(diagnostics, FeatureDiagnosticProjection{
			InstanceID:  pluginID,
			Projections: cloneDiagnosticPlaneProjections(aipfeature.ProjectDiagnostics(b.PlaneSet)),
		})
	}
	return GeneratedMergeSurface{
		Frozen:             cs.Freeze(),
		Lifecycles:         lifecycles,
		set:                cs,
		featureDiagnostics: diagnostics,
	}, nil
}

// MergeFeatureSurfaceGenerated merges enabled feature plugins into a GeneratedMergeSurface.
// It builds bundles from enabled feature registrations in order and merges them through
// generated plane adapters.
func MergeFeatureSurfaceGenerated(reg FeatureBundleRegistry, registrations []aipsdk.Registration) (GeneratedMergeSurface, error) {
	bundles, err := buildEnabledFeatureBundles(reg, registrations)
	if err != nil {
		return GeneratedMergeSurface{}, err
	}
	return mergeBuiltFeatureBundlesWithHost(bundles, registrations, HostContributions{})
}
