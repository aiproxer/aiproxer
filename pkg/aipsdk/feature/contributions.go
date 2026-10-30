package feature

import (
	"fmt"
	"maps"
)

// ContributionSet accumulates typed, validated plane contributions during feature plugin construction.
// It is mutable during assembly and converted to an immutable [FrozenPlaneSet] via [ContributionSet.Freeze].
type ContributionSet struct {
	pluginIDs map[string]string
	generated *generatedContributions
}

// NewContributionSet creates a new empty, mutable [ContributionSet].
func NewContributionSet() *ContributionSet {
	return &ContributionSet{
		pluginIDs: make(map[string]string),
		generated: newGeneratedContributions(),
	}
}

// Has reports whether a contribution for planeID exists in the set.
func (s *ContributionSet) Has(planeID string) bool {
	if s == nil || s.pluginIDs == nil {
		return false
	}
	_, ok := s.pluginIDs[planeID]
	return ok
}

// Clone returns a deep copy of the ContributionSet.
func (s *ContributionSet) Clone() *ContributionSet {
	if s == nil {
		return nil
	}
	pluginIDsCopy := make(map[string]string, len(s.pluginIDs))
	maps.Copy(pluginIDsCopy, s.pluginIDs)
	var genCopy *generatedContributions
	if s.generated != nil {
		genCopy = s.generated.clone()
		if onCloneGenerated != nil {
			onCloneGenerated(s.generated, genCopy)
		}
	}
	return &ContributionSet{
		pluginIDs: pluginIDsCopy,
		generated: genCopy,
	}
}

// Freeze produces an immutable [FrozenPlaneSet] from the accumulated contributions.
// Slice backing arrays and metadata maps are isolated via shallow copy so that subsequent
// mutations to the [ContributionSet] or source slices do not affect the frozen snapshot;
// element values (e.g. interface handlers) are shallow-copied, not deep-cloned.
func (s *ContributionSet) Freeze() FrozenPlaneSet {
	if s == nil {
		return FrozenPlaneSet{}
	}
	var pluginIDsCopy map[string]string
	if s.pluginIDs != nil {
		pluginIDsCopy = make(map[string]string, len(s.pluginIDs))
		maps.Copy(pluginIDsCopy, s.pluginIDs)
	}
	var genFrozen *generatedFrozen
	if s.generated != nil {
		genFrozen = s.generated.freeze()
		if onFreezeGenerated != nil {
			onFreezeGenerated(s.generated, genFrozen)
		}
	}
	return FrozenPlaneSet{
		pluginIDs: pluginIDsCopy,
		frozen:    genFrozen,
	}
}

var (
	onFreezeGenerated func(*generatedContributions, *generatedFrozen)
	onCloneGenerated  func(src, dst *generatedContributions)
)

// ContributeSource adds a typed contribution from an explicit source (e.g. [SourceFeature], [SourceHost],
// [SourceGenerationBinder]) to the [ContributionSet]. If any validation or combination fails, the set is
// left unmodified (fail-before-mutate) and an [AttributedError] attributing the contributor ID and plane ID is returned.
func contributePolicy[P any](
	s *ContributionSet,
	gp *generatedPolicy[P],
	contribute func(*generatedContributions, SourceKind, string, P) error,
	getIdentity func(*generatedFrozen) (string, bool),
	source SourceKind,
	contributorID string,
	v P,
) error {
	if s == nil {
		return fmt.Errorf("feature: nil ContributionSet")
	}
	if gp == nil || contribute == nil {
		return &AttributedError{
			PluginID: contributorID,
			Err:      ErrUngeneratedPlane,
		}
	}

	if contributorID == "" {
		return &AttributedError{
			PlaneID: gp.planeID,
			Err:     fmt.Errorf("%w: plugin ID must not be empty", ErrInvalidContribution),
		}
	}

	rule := gp.rules.RuleFor(source)
	if rule == CombUnsupported {
		return &AttributedError{
			PluginID: contributorID,
			PlaneID:  gp.planeID,
			Err:      fmt.Errorf("%w: source %v is not supported on plane %q", ErrUnsupportedSource, source, gp.planeID),
		}
	}

	// 1. Apply NilPolicy before Validate and Combine.
	isNil := false
	if gp.isNil != nil {
		isNil = gp.isNil(v)
	} else {
		var anyVal any = v
		isNil = (anyVal == nil)
	}

	if isNil {
		switch gp.nilPolicy {
		case NilReject:
			return &AttributedError{
				PluginID: contributorID,
				PlaneID:  gp.planeID,
				Err:      fmt.Errorf("%w: nil contribution rejected by policy on plane %q", ErrNilContribution, gp.planeID),
			}
		case NilSkip:
			// Omit consistently from combination and diagnostics (leave set untouched)
			return nil
		case NilNotApplicable:
			// Continue to Validate / Combine
		}
	}

	// 2. Validate incoming contribution.
	if gp.validate != nil {
		if err := gp.validate(v); err != nil {
			return &AttributedError{
				PluginID: contributorID,
				PlaneID:  gp.planeID,
				Err:      fmt.Errorf("%w: %w", ErrInvalidContribution, err),
			}
		}
	}

	// 3. Identity extraction and validation (if plane requires it or declares an identity extractor).
	var incomingID string
	var hasID bool
	if gp.identity != nil {
		if rule == CombExclusive || rule == CombReplaceByIdentity {
			incomingID, hasID = gp.identity(v)
			if !hasID || incomingID == "" {
				if rule == CombExclusive {
					return &AttributedError{
						PluginID: contributorID,
						PlaneID:  gp.planeID,
						Err:      fmt.Errorf("%w: failed to extract identity from exclusive contribution", ErrInvalidContribution),
					}
				}
				return &AttributedError{
					PluginID: contributorID,
					PlaneID:  gp.planeID,
					Err:      fmt.Errorf("%w: failed to extract identity from replace_by_identity contribution", ErrInvalidContribution),
				}
			}
			if gp.validateIdentity != nil && hasID && incomingID != "" {
				if err := gp.validateIdentity(incomingID); err != nil {
					return &AttributedError{
						PluginID: contributorID,
						PlaneID:  gp.planeID,
						Err:      fmt.Errorf("%w: %w", ErrInvalidContribution, err),
					}
				}
			}
		}
	}

	// 4. Exclusive conflict check.
	if rule == CombExclusive {
		if _, occupied := s.pluginIDs[gp.planeID]; occupied {
			var existingID string
			if getIdentity != nil {
				frozen := s.Freeze()
				if frozen.frozen != nil {
					existingID, _ = getIdentity(frozen.frozen)
				}
			}
			return makeExclusiveConflictError(contributorID, gp.planeID, gp.exclusiveConflictError, existingID, incomingID)
		}
	}

	// 5. Generated storage path: pure storage, closures are NOT responsible for identity validation or conflict checks.
	if s.generated != nil {
		if err := contribute(s.generated, source, contributorID, v); err != nil {
			return &AttributedError{
				PluginID: contributorID,
				PlaneID:  gp.planeID,
				Err:      fmt.Errorf("%w: %w", ErrInvalidContribution, err),
			}
		}
		s.pluginIDs[gp.planeID] = contributorID
		return nil
	}

	return nil
}

// ContributeSource adds a typed contribution from an explicit source (e.g. [SourceFeature], [SourceHost],
// [SourceGenerationBinder]) to the [ContributionSet]. If any validation or combination fails, the set is
// left unmodified (fail-before-mutate) and an [AttributedError] attributing the contributor ID and plane ID is returned.
func ContributeSource[P any](s *ContributionSet, p Plane[P], source SourceKind, contributorID string, v P) error {
	if s == nil {
		return fmt.Errorf("feature: nil ContributionSet")
	}

	gp := p.generated.policy
	if p.generated.contribute == nil || p.generated.get == nil || gp == nil || gp.planeID != p.ID {
		return &AttributedError{
			PluginID: contributorID,
			PlaneID:  p.ID,
			Err:      ErrUngeneratedPlane,
		}
	}

	return contributePolicy(s, gp, p.generated.contribute, p.generated.identity, source, contributorID, v)
}

// Contribute adds a typed contribution from a feature plugin under [SourceFeature] to the [ContributionSet].
// If any validation or combination fails, the set is left unmodified (fail-before-mutate)
// and an [AttributedError] attributing the plugin ID and plane ID is returned.
// Host and generation-binder contributions must use [ContributeSource] or dedicated internal binders instead.
func Contribute[P any](s *ContributionSet, p Plane[P], pluginID string, v P) error {
	return ContributeSource(s, p, SourceFeature, pluginID, v)
}

// ContributeCandidate contributes candidate planes under SourceFeature.
func (s *ContributionSet) ContributeCandidate(cand FrozenPlaneSet) error {
	return cand.ContributeCandidateTo(s, SourceFeature, "candidate")
}
