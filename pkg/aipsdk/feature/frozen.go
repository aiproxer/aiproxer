package feature

import (
	"fmt"
	"maps"
	"reflect"
)

// FrozenPlaneSet holds an immutable collection of composed feature plane values.
type FrozenPlaneSet struct {
	pluginIDs map[string]string
	frozen    *generatedFrozen
}

var (
	onThawGenerated func(*generatedFrozen, *generatedContributions)
	onCloneFrozen   func(src, dst *generatedFrozen)
)

// cloneSlice returns a copy of slice s with its own backing array,
// preserving nil vs non-nil empty slice semantics. Slice backing arrays
// are isolated; element values (e.g. interface handlers) are shallow-copied,
// not deep-cloned.
func cloneSlice[T any](s []T) []T {
	if s == nil {
		return nil
	}
	return append(make([]T, 0, len(s)), s...)
}

// materializeRequestSlice evaluates a slice request materializer, cloning the resulting slice
// to guarantee isolation of the frozen request view.
func materializeRequestSlice[T any](
	source []T,
	materialize func([]T) []T,
) []T {
	if materialize == nil {
		return cloneSlice(source)
	}
	return cloneSlice(materialize(source))
}

// Get retrieves the typed value for plane p from the frozen set.
// If the plane was not contributed, is ungenerated, or is absent, the zero value of P is returned.
// For slice-valued planes, ordinary Get calls return defensive copies to ensure that
// caller modifications cannot corrupt the frozen snapshot.
// For planes bound to generated storage, Get dispatches directly with zero map lookups,
// zero reflection, and zero type assertions.
func Get[P any](s FrozenPlaneSet, p Plane[P]) P {
	if p.generated.get != nil && s.frozen != nil && p.generated.policy != nil && p.generated.policy.planeID == p.ID {
		return p.generated.get(s.frozen)
	}
	var zero P
	return zero
}

// FreezeRequestPlanes materializes request-scoped feature planes into an immutable FrozenPlaneSet.
// Planes with a declared RequestMaterializer (e.g. sorted execution planes) are materialized
// once at snapshot construction; all other planes are preserved in their frozen order.
func FreezeRequestPlanes(in FrozenPlaneSet) FrozenPlaneSet {
	if in.IsZero() {
		return FrozenPlaneSet{}
	}
	var pluginIDsCopy map[string]string
	if in.pluginIDs != nil {
		pluginIDsCopy = make(map[string]string, len(in.pluginIDs))
		maps.Copy(pluginIDsCopy, in.pluginIDs)
	}
	if in.frozen != nil {
		return FrozenPlaneSet{
			pluginIDs: pluginIDsCopy,
			frozen:    in.frozen.freezeRequest(),
		}
	}
	return FrozenPlaneSet{
		pluginIDs: pluginIDsCopy,
	}
}

// FrozenIdentity retrieves the validated identity string associated with an exclusive
// or replace-by-identity plane in the frozen set, if present.
// FrozenIdentity reads validated cached identity metadata and does not invoke live identity methods.
// For planes bound to generated storage, FrozenIdentity dispatches directly with zero map lookups.
func FrozenIdentity[P any](s FrozenPlaneSet, p Plane[P]) (string, bool) {
	if p.generated.identity != nil && s.frozen != nil && p.generated.policy != nil && p.generated.policy.planeID == p.ID {
		return p.generated.identity(s.frozen)
	}
	return "", false
}

// IsZero reports whether s is an uninitialized, zero-value FrozenPlaneSet.
func (s FrozenPlaneSet) IsZero() bool {
	return s.frozen == nil && len(s.pluginIDs) == 0
}

// ToContributions reconstructs a mutable ContributionSet from the frozen snapshot.
func (s FrozenPlaneSet) ToContributions() *ContributionSet {
	if s.IsZero() {
		return NewContributionSet()
	}
	var pluginIDsCopy map[string]string
	if s.pluginIDs != nil {
		pluginIDsCopy = make(map[string]string, len(s.pluginIDs))
		maps.Copy(pluginIDsCopy, s.pluginIDs)
	}
	var gen *generatedContributions
	if s.frozen != nil {
		gen = s.frozen.toContributions()
		if onThawGenerated != nil {
			onThawGenerated(s.frozen, gen)
		}
	} else {
		gen = newGeneratedContributions()
	}
	return &ContributionSet{
		pluginIDs: pluginIDsCopy,
		generated: gen,
	}
}

// ContributionSetFromFrozen reconstructs a mutable ContributionSet from a FrozenPlaneSet.
func ContributionSetFromFrozen(s FrozenPlaneSet) *ContributionSet {
	return s.ToContributions()
}

// ContributeCandidateTo contributes candidate plane values in s into dst under the given source and contributor ID.
func (s FrozenPlaneSet) ContributeCandidateTo(dst *ContributionSet, source SourceKind, contributorID string) error {
	if s.IsZero() || dst == nil || s.frozen == nil {
		return nil
	}
	if contributorID == "" {
		contributorID = "candidate"
	}
	if err := s.frozen.checkCandidateSourceAdmission(source, contributorID); err != nil {
		return err
	}
	staged := dst.Clone()
	if staged.generated != nil {
		if err := s.frozen.contributeCandidateTo(staged.generated, source, contributorID); err != nil {
			return err
		}
		if s.pluginIDs != nil && staged.pluginIDs != nil {
			maps.Copy(staged.pluginIDs, s.pluginIDs)
		}
	}
	*dst = *staged
	return nil
}

// Clone returns a deep copy of the FrozenPlaneSet with independent slice backing arrays.
func (s FrozenPlaneSet) Clone() FrozenPlaneSet {
	if s.IsZero() {
		return FrozenPlaneSet{}
	}
	var pluginIDsCopy map[string]string
	if s.pluginIDs != nil {
		pluginIDsCopy = make(map[string]string, len(s.pluginIDs))
		maps.Copy(pluginIDsCopy, s.pluginIDs)
	}
	var genFrozen *generatedFrozen
	if s.frozen != nil {
		genFrozen = s.frozen.clone()
		if onCloneFrozen != nil {
			onCloneFrozen(s.frozen, genFrozen)
		}
	}
	return FrozenPlaneSet{
		pluginIDs: pluginIDsCopy,
		frozen:    genFrozen,
	}
}

// validateStored checks that all planes in s conform to their declared plane validation rules
// without replaying into a ContributionSet.
func (s FrozenPlaneSet) validateStored() error {
	if s.IsZero() || s.frozen == nil {
		return nil
	}
	return s.frozen.validate()
}

// Validate checks that all planes in s conform to their declared plane validation rules.
func (s FrozenPlaneSet) Validate() error {
	return s.validateStored()
}

// ReplayTo replays all planes in s into dst under SourceFeature with contributorID transactionally.
// If any plane fails validation or combination, dst is left unmodified (fail-before-mutate).
func (s FrozenPlaneSet) ReplayTo(dst *ContributionSet, contributorID string) error {
	return s.ReplaySourceTo(dst, SourceFeature, contributorID)
}

// ReplaySourceTo replays all planes in s into dst under source with contributorID transactionally.
// If any plane fails validation or combination, dst is left unmodified (fail-before-mutate).
func (s FrozenPlaneSet) ReplaySourceTo(dst *ContributionSet, source SourceKind, contributorID string) error {
	if s.IsZero() || dst == nil {
		return nil
	}
	if contributorID == "" {
		contributorID = "feature"
	}
	if s.frozen != nil {
		if planeID, ok := s.frozen.hasIdentityReplayRule(source, CombReplaceByIdentity); ok {
			return &AttributedError{
				PluginID: contributorID,
				PlaneID:  planeID,
				Err:      fmt.Errorf("%w: source %s requires identity-aware binder operation", ErrUnsupportedReplaySource, source),
			}
		}
		if err := s.frozen.checkSourceAdmission(source, contributorID); err != nil {
			return err
		}
	}
	if err := s.validateStored(); err != nil {
		return attributeReplayValidationError(err, contributorID)
	}
	if s.frozen == nil {
		return nil
	}
	staged := dst.Clone()
	if staged.generated != nil {
		if err := s.frozen.replayAllPlanesTo(staged.generated, source, contributorID); err != nil {
			return err
		}
		if s.pluginIDs != nil && staged.pluginIDs != nil {
			for k := range s.pluginIDs {
				staged.pluginIDs[k] = contributorID
			}
		}
	}
	*dst = *staged
	return nil
}

func isReflectNil(v reflect.Value) bool {
	if !v.IsValid() {
		return true
	}
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Map, reflect.Pointer, reflect.Interface, reflect.Slice, reflect.UnsafePointer:
		return v.IsNil()
	default:
		return false
	}
}

func isNilValue(v any) bool {
	if v == nil {
		return true
	}
	return isReflectNil(reflect.ValueOf(v))
}
