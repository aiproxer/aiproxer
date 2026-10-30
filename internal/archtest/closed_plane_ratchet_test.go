package archtest

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClosedPlaneArchitectureRatchets_ProductionClean(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	findings, err := ScanClosedPlaneViolations(root)
	require.NoError(t, err)
	assert.Empty(t, findings, "production codebase must have zero closed-plane violations")
}

func TestClosedPlaneArchitectureRatchets_SyntheticViolations(t *testing.T) {
	t.Parallel()

	t.Run("rejects values map on ContributionSet", func(t *testing.T) {
		t.Parallel()
		src := `package feature

type ContributionSet struct {
	pluginIDs map[string]string
	values    map[string]any
	generated *generatedContributions
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/contributions.go", src)
		require.NotEmpty(t, findings)
		assert.Equal(t, RuleClosedPlaneNoArbitraryValueMaps, findings[0].Rule)
		assert.Contains(t, findings[0].Detail, "values")
	})

	t.Run("rejects values map on FrozenPlaneSet", func(t *testing.T) {
		t.Parallel()
		src := `package feature

type FrozenPlaneSet struct {
	pluginIDs map[string]string
	values    map[string]any
	frozen    *generatedFrozen
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/frozen.go", src)
		require.NotEmpty(t, findings)
		assert.Equal(t, RuleClosedPlaneNoArbitraryValueMaps, findings[0].Rule)
		assert.Contains(t, findings[0].Detail, "values")
	})

	t.Run("rejects identities map on FrozenPlaneSet", func(t *testing.T) {
		t.Parallel()
		src := `package feature

type FrozenPlaneSet struct {
	pluginIDs  map[string]string
	identities map[string]string
	frozen     *generatedFrozen
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/frozen.go", src)
		require.NotEmpty(t, findings)
		assert.Equal(t, RuleClosedPlaneNoArbitraryValueMaps, findings[0].Rule)
		assert.Contains(t, findings[0].Detail, "identities")
	})

	t.Run("rejects arbitrary map[string]interface{} on ContributionSet", func(t *testing.T) {
		t.Parallel()
		src := `package feature

type ContributionSet struct {
	pluginIDs map[string]string
	fallback  map[string]interface{}
	generated *generatedContributions
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/contributions.go", src)
		require.NotEmpty(t, findings)
		assert.Equal(t, RuleClosedPlaneNoArbitraryValueMaps, findings[0].Rule)
	})

	t.Run("rejects nested inline struct with map on ContributionSet", func(t *testing.T) {
		t.Parallel()
		src := `package feature

type ContributionSet struct {
	pluginIDs map[string]string
	storage   struct {
		values map[string]any
	}
	generated *generatedContributions
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/contributions.go", src)
		require.NotEmpty(t, findings)
		assert.Equal(t, RuleClosedPlaneNoArbitraryValueMaps, findings[0].Rule)
		assert.Contains(t, findings[0].Detail, "storage.values")
	})

	t.Run("rejects anonymous embedded named type with map on FrozenPlaneSet", func(t *testing.T) {
		t.Parallel()
		src := `package feature

type EmbeddedStore struct {
	mapping map[string]string
}

type FrozenPlaneSet struct {
	pluginIDs map[string]string
	EmbeddedStore
	frozen    *generatedFrozen
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/frozen.go", src)
		require.NotEmpty(t, findings)
		assert.Equal(t, RuleClosedPlaneNoArbitraryValueMaps, findings[0].Rule)
		assert.Contains(t, findings[0].Detail, "EmbeddedStore.mapping")
	})

	t.Run("rejects alias map on ContributionSet", func(t *testing.T) {
		t.Parallel()
		src := `package feature

type DynamicPlaneMap map[string]any

type ContributionSet struct {
	pluginIDs map[string]string
	fallback  DynamicPlaneMap
	generated *generatedContributions
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/contributions.go", src)
		require.NotEmpty(t, findings)
		assert.Equal(t, RuleClosedPlaneNoArbitraryValueMaps, findings[0].Rule)
		assert.Contains(t, findings[0].Detail, "fallback")
	})

	t.Run("rejects anonymous embedded alias map on ContributionSet", func(t *testing.T) {
		t.Parallel()
		src := `package feature

type DynamicPlaneMap map[string]any

type ContributionSet struct {
	pluginIDs map[string]string
	DynamicPlaneMap
	generated *generatedContributions
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/contributions.go", src)
		require.NotEmpty(t, findings)
		assert.Equal(t, RuleClosedPlaneNoArbitraryValueMaps, findings[0].Rule)
	})

	t.Run("rejects renamed map replay helper on generatedFrozen", func(t *testing.T) {
		t.Parallel()
		src := `package feature

func (gf *generatedFrozen) customReplay(gc *generatedContributions, source SourceKind, contributorID string) error {
	m := make(map[string]any)
	for k, v := range m {
		_ = k
		_ = v
	}
	return nil
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/plane_generated.go", src)
		require.NotEmpty(t, findings)
		var found bool
		for _, f := range findings {
			if f.Rule == RuleClosedPlaneNoMapReplayHelpers {
				found = true
				break
			}
		}
		assert.True(t, found, "must flag RuleClosedPlaneNoMapReplayHelpers for renamed helper")
	})

	t.Run("rejects renamed map replay helper with map parameter", func(t *testing.T) {
		t.Parallel()
		src := `package feature

func (gf *generatedFrozen) customReplay(m map[string]any) error {
	return nil
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/plane_generated.go", src)
		require.NotEmpty(t, findings)
		assert.Equal(t, RuleClosedPlaneNoMapReplayHelpers, findings[0].Rule)
	})

	t.Run("rejects dynamic type assertion in operational function", func(t *testing.T) {
		t.Parallel()
		src := `package feature

func (s FrozenPlaneSet) DynamicUnpack(v any) string {
	return v.(string)
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/frozen.go", src)
		require.NotEmpty(t, findings)
		assert.Equal(t, RuleClosedPlaneNoMapReplayHelpers, findings[0].Rule)
		assert.Contains(t, findings[0].Detail, "dynamic type assertion")
	})

	t.Run("rejects reflection combine in ContributeSource", func(t *testing.T) {
		t.Parallel()
		src := `package feature

import "reflect"

func ContributeSource[P any](s *ContributionSet, p Plane[P], source SourceKind, contributorID string, v P) error {
	rv := reflect.ValueOf(v)
	_ = reflect.Append(rv, rv)
	return nil
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/contributions.go", src)
		require.NotEmpty(t, findings)
		var found bool
		for _, f := range findings {
			if f.Rule == RuleClosedPlaneNoReflectionFallback {
				found = true
				break
			}
		}
		assert.True(t, found, "must flag RuleClosedPlaneNoReflectionFallback")
	})

	t.Run("rejects map replay helper invocation in ReplaySourceTo", func(t *testing.T) {
		t.Parallel()
		src := `package feature

func (s FrozenPlaneSet) ReplaySourceTo(dst *ContributionSet, source SourceKind, contributorID string) error {
	if s.frozen != nil {
		return s.frozen.replayAllPlanesMapTo(dst, source, contributorID)
	}
	return nil
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/frozen.go", src)
		require.NotEmpty(t, findings)
		var found bool
		for _, f := range findings {
			if f.Rule == RuleClosedPlaneNoMapReplayHelpers {
				found = true
				break
			}
		}
		assert.True(t, found, "must flag RuleClosedPlaneNoMapReplayHelpers")
	})

	t.Run("rejects map replay helper declaration in generated file", func(t *testing.T) {
		t.Parallel()
		src := `package feature

func (gf *generatedFrozen) replayAllPlanesMapTo(gc *generatedContributions, source SourceKind, contributorID string) error {
	return nil
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/plane_generated.go", src)
		require.NotEmpty(t, findings)
		assert.Equal(t, RuleClosedPlaneNoMapReplayHelpers, findings[0].Rule)
	})

	t.Run("rejects map validation helper declaration in generated file", func(t *testing.T) {
		t.Parallel()
		src := `package feature

func (gf *generatedFrozen) validateAllPlanesMap() error {
	return nil
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/plane_generated.go", src)
		require.NotEmpty(t, findings)
		assert.Equal(t, RuleClosedPlaneNoMapReplayHelpers, findings[0].Rule)
	})

	t.Run("rejects map candidate helper declaration in generated file", func(t *testing.T) {
		t.Parallel()
		src := `package feature

func (gf *generatedFrozen) contributeCandidateMapTo(gc *generatedContributions, source SourceKind, contributorID string) error {
	return nil
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/plane_generated.go", src)
		require.NotEmpty(t, findings)
		assert.Equal(t, RuleClosedPlaneNoMapReplayHelpers, findings[0].Rule)
	})
}

func TestClosedPlaneArchitectureRatchets_AllowsLegitimateMapsAndReflection(t *testing.T) {
	t.Parallel()

	t.Run("allows pluginIDs map on ContributionSet and FrozenPlaneSet", func(t *testing.T) {
		t.Parallel()
		src := `package feature

type ContributionSet struct {
	pluginIDs map[string]string
	generated *generatedContributions
}

type FrozenPlaneSet struct {
	pluginIDs map[string]string
	frozen    *generatedFrozen
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/contributions.go", src)
		assert.Empty(t, findings, "pluginIDs map[string]string is allowed attribution metadata")
	})

	t.Run("allows isReflectNil in frozen.go", func(t *testing.T) {
		t.Parallel()
		src := `package feature

import "reflect"

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
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/frozen.go", src)
		assert.Empty(t, findings, "isReflectNil/isNilValue for typed nil checking is explicitly allowed")
	})

	t.Run("allows ValidateManifest in plane.go", func(t *testing.T) {
		t.Parallel()
		src := `package feature

func ValidateManifest(declarations ...PlaneDeclaration) error {
	seen := make(map[string]struct{}, len(declarations))
	for _, decl := range declarations {
		seen[decl.PlaneID()] = struct{}{}
	}
	return nil
}
`
		findings := scanClosedPlaneSyntheticSource(t, "pkg/aipsdk/feature/plane.go", src)
		assert.Empty(t, findings, "ValidateManifest is allowed legitimate package map for declaration deduplication")
	})

	t.Run("allows unrelated package with map[string]any and reflection", func(t *testing.T) {
		t.Parallel()
		src := `package unrelated

import (
	"reflect"
)

type ConfigBag struct {
	Data map[string]any
}

func CloneBag(c ConfigBag) ConfigBag {
	rv := reflect.ValueOf(c.Data)
	_ = rv.Kind()
	return c
}
`
		findings := scanClosedPlaneSyntheticSource(t, "internal/core/config/bag.go", src)
		assert.Empty(t, findings, "unrelated packages outside feature plane lifecycle are not restricted")
	})
}
