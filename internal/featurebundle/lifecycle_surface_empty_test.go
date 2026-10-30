package featurebundle

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	aipplugin "github.com/aiproxer/aiproxer/pkg/aipsdk/plugin"
)

// TestLifecycleSurface_ExplicitEmptyPreserved tests that an explicit non-nil empty lifecycle bundle
// (Lifecycles: []aipplugin.Lifecycle{}) preserves a non-nil length-zero slice in GeneratedMergeSurface.
func TestLifecycleSurface_ExplicitEmptyPreserved(t *testing.T) {
	t.Parallel()

	bEmpty := aipfeature.FeatureBundle{
		SchemaVersion: aipfeature.SchemaVersionV1,
		Lifecycles:    []aipplugin.Lifecycle{},
	}

	t.Run("MergeBundlesGenerated_ExplicitEmptyPreserved", func(t *testing.T) {
		t.Parallel()
		gen, err := MergeBundlesGenerated(bEmpty)
		require.NoError(t, err)
		require.NotNil(t, gen.Lifecycles, "explicit non-nil empty lifecycles must be preserved as non-nil in GeneratedMergeSurface")
		require.Len(t, gen.Lifecycles, 0)
	})

	t.Run("MergeFeatureSurfaceGenerated_ExplicitEmptyPreserved", func(t *testing.T) {
		t.Parallel()
		reg := fakeFeatureBundleRegistry{
			buildFn: func(string, yaml.Node) (aipfeature.FeatureBundle, error) {
				return bEmpty, nil
			},
		}
		regs := []aipsdk.Registration{
			{Kind: aipsdk.PluginKindFeature, ID: "empty-feat", FactoryKind: "empty-feat", Enabled: true},
		}
		gen, err := MergeFeatureSurfaceGenerated(reg, regs)
		require.NoError(t, err)
		require.NotNil(t, gen.Lifecycles, "explicit non-nil empty lifecycles must be preserved as non-nil in GeneratedMergeSurface")
		require.Len(t, gen.Lifecycles, 0)
	})

	t.Run("MergeFeatureSurfacesWithHost_ExplicitEmptyPreserved", func(t *testing.T) {
		t.Parallel()
		reg := fakeFeatureBundleRegistry{
			buildFn: func(string, yaml.Node) (aipfeature.FeatureBundle, error) {
				return bEmpty, nil
			},
		}
		regs := []aipsdk.Registration{
			{Kind: aipsdk.PluginKindFeature, ID: "empty-feat", FactoryKind: "empty-feat", Enabled: true},
		}
		gen, err := MergeFeatureSurfacesWithHost(reg, regs, HostContributions{}, bEmpty)
		require.NoError(t, err)
		require.NotNil(t, gen.Lifecycles, "explicit non-nil empty lifecycles must be preserved as non-nil in GeneratedMergeSurface")
		require.Len(t, gen.Lifecycles, 0)
	})
}

// TestLifecycleSurface_MergeBundlesGenerated_Sequences tests table-driven combinations of
// nil, empty, and populated lifecycle sequences for exact nil/non-nil preservation and ordering.
func TestLifecycleSurface_MergeBundlesGenerated_Sequences(t *testing.T) {
	t.Parallel()

	l1 := testLifecycle{tag: "l1"}
	l2 := testLifecycle{tag: "l2"}
	l3 := testLifecycle{tag: "l3"}

	bNil := aipfeature.FeatureBundle{SchemaVersion: aipfeature.SchemaVersionV1, Lifecycles: nil}
	bEmpty := aipfeature.FeatureBundle{SchemaVersion: aipfeature.SchemaVersionV1, Lifecycles: []aipplugin.Lifecycle{}}
	bPop1 := aipfeature.FeatureBundle{SchemaVersion: aipfeature.SchemaVersionV1, Lifecycles: []aipplugin.Lifecycle{l1, l2}}
	bPop2 := aipfeature.FeatureBundle{SchemaVersion: aipfeature.SchemaVersionV1, Lifecycles: []aipplugin.Lifecycle{l3}}

	cases := []struct {
		name    string
		bundles []aipfeature.FeatureBundle
		wantNil bool
		want    []aipplugin.Lifecycle
	}{
		{
			name:    "nil_to_nil",
			bundles: []aipfeature.FeatureBundle{bNil, bNil},
			wantNil: true,
			want:    nil,
		},
		{
			name:    "nil_to_empty_to_nil",
			bundles: []aipfeature.FeatureBundle{bNil, bEmpty, bNil},
			wantNil: false,
			want:    []aipplugin.Lifecycle{},
		},
		{
			name:    "empty_to_nil_to_nil",
			bundles: []aipfeature.FeatureBundle{bEmpty, bNil, bNil},
			wantNil: false,
			want:    []aipplugin.Lifecycle{},
		},
		{
			name:    "nil_to_nil_to_empty",
			bundles: []aipfeature.FeatureBundle{bNil, bNil, bEmpty},
			wantNil: false,
			want:    []aipplugin.Lifecycle{},
		},
		{
			name:    "empty_to_empty",
			bundles: []aipfeature.FeatureBundle{bEmpty, bEmpty},
			wantNil: false,
			want:    []aipplugin.Lifecycle{},
		},
		{
			name:    "empty_to_populated",
			bundles: []aipfeature.FeatureBundle{bEmpty, bPop1},
			wantNil: false,
			want:    []aipplugin.Lifecycle{l1, l2},
		},
		{
			name:    "populated_to_empty",
			bundles: []aipfeature.FeatureBundle{bPop1, bEmpty},
			wantNil: false,
			want:    []aipplugin.Lifecycle{l1, l2},
		},
		{
			name:    "populated_to_nil",
			bundles: []aipfeature.FeatureBundle{bPop1, bNil},
			wantNil: false,
			want:    []aipplugin.Lifecycle{l1, l2},
		},
		{
			name:    "nil_to_populated",
			bundles: []aipfeature.FeatureBundle{bNil, bPop1},
			wantNil: false,
			want:    []aipplugin.Lifecycle{l1, l2},
		},
		{
			name:    "populated1_to_nil_to_populated2",
			bundles: []aipfeature.FeatureBundle{bPop1, bNil, bPop2},
			wantNil: false,
			want:    []aipplugin.Lifecycle{l1, l2, l3},
		},
		{
			name:    "populated1_to_empty_to_populated2",
			bundles: []aipfeature.FeatureBundle{bPop1, bEmpty, bPop2},
			wantNil: false,
			want:    []aipplugin.Lifecycle{l1, l2, l3},
		},
		{
			name:    "empty_to_populated1_to_empty_to_populated2",
			bundles: []aipfeature.FeatureBundle{bEmpty, bPop1, bEmpty, bPop2},
			wantNil: false,
			want:    []aipplugin.Lifecycle{l1, l2, l3},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gen, err := MergeBundlesGenerated(tc.bundles...)
			require.NoError(t, err)
			if tc.wantNil {
				require.Nil(t, gen.Lifecycles)
			} else {
				require.NotNil(t, gen.Lifecycles)
				require.Equal(t, tc.want, gen.Lifecycles)
			}
		})
	}
}

// TestLifecycleSurface_RegistryAndHostExtras_MixedSequences tests registry and host+extras
// mixed sequences for lifecycle handling across all entry points.
func TestLifecycleSurface_RegistryAndHostExtras_MixedSequences(t *testing.T) {
	t.Parallel()

	l1 := testLifecycle{tag: "reg-l1"}
	l2 := testLifecycle{tag: "reg-l2"}
	lExtra1 := testLifecycle{tag: "extra-l1"}
	lExtra2 := testLifecycle{tag: "extra-l2"}

	bNil := aipfeature.FeatureBundle{SchemaVersion: aipfeature.SchemaVersionV1, Lifecycles: nil}
	bEmpty := aipfeature.FeatureBundle{SchemaVersion: aipfeature.SchemaVersionV1, Lifecycles: []aipplugin.Lifecycle{}}
	bPop := aipfeature.FeatureBundle{SchemaVersion: aipfeature.SchemaVersionV1, Lifecycles: []aipplugin.Lifecycle{l1, l2}}
	bExtraPop := aipfeature.FeatureBundle{SchemaVersion: aipfeature.SchemaVersionV1, Lifecycles: []aipplugin.Lifecycle{lExtra1, lExtra2}}

	t.Run("Registry_MixedSequence_With_Disabled", func(t *testing.T) {
		t.Parallel()
		reg := fakeFeatureBundleRegistry{
			buildFn: func(kind string, _ yaml.Node) (aipfeature.FeatureBundle, error) {
				switch kind {
				case "feat-nil":
					return bNil, nil
				case "feat-empty":
					return bEmpty, nil
				case "feat-pop":
					return bPop, nil
				case "feat-disabled":
					return bPop, nil
				default:
					return aipfeature.FeatureBundle{}, nil
				}
			},
		}
		regs := []aipsdk.Registration{
			{Kind: aipsdk.PluginKindFeature, ID: "f1", FactoryKind: "feat-nil", Enabled: true},
			{Kind: aipsdk.PluginKindFeature, ID: "f-dis", FactoryKind: "feat-disabled", Enabled: false},
			{Kind: aipsdk.PluginKindFeature, ID: "f2", FactoryKind: "feat-empty", Enabled: true},
			{Kind: aipsdk.PluginKindFeature, ID: "f3", FactoryKind: "feat-pop", Enabled: true},
		}

		gen, err := MergeFeatureSurfaceGenerated(reg, regs)
		require.NoError(t, err)
		require.NotNil(t, gen.Lifecycles)
		require.Equal(t, []aipplugin.Lifecycle{l1, l2}, gen.Lifecycles)
	})

	t.Run("MergeFeatureSurfacesWithHost_RegistryAndExtra_Ordering", func(t *testing.T) {
		t.Parallel()
		reg := fakeFeatureBundleRegistry{
			buildFn: func(kind string, _ yaml.Node) (aipfeature.FeatureBundle, error) {
				if kind == "reg-feat" {
					return bPop, nil
				}
				return bNil, nil
			},
		}
		regs := []aipsdk.Registration{
			{Kind: aipsdk.PluginKindFeature, ID: "r1", FactoryKind: "reg-feat", Enabled: true},
		}

		// registry (bPop) + extra (bEmpty, bExtraPop, bNil)
		gen, err := MergeFeatureSurfacesWithHost(reg, regs, HostContributions{}, bEmpty, bExtraPop, bNil)
		require.NoError(t, err)
		require.NotNil(t, gen.Lifecycles)
		require.Equal(t, []aipplugin.Lifecycle{l1, l2, lExtra1, lExtra2}, gen.Lifecycles)
	})

	t.Run("MergeFeatureSurfacesWithHost_OnlyEmptyAndNilProducesEmpty", func(t *testing.T) {
		t.Parallel()
		reg := fakeFeatureBundleRegistry{
			buildFn: func(string, yaml.Node) (aipfeature.FeatureBundle, error) {
				return bNil, nil
			},
		}
		regs := []aipsdk.Registration{
			{Kind: aipsdk.PluginKindFeature, ID: "r1", FactoryKind: "nil-feat", Enabled: true},
		}

		gen, err := MergeFeatureSurfacesWithHost(reg, regs, HostContributions{}, bEmpty, bNil)
		require.NoError(t, err)
		require.NotNil(t, gen.Lifecycles)
		require.Len(t, gen.Lifecycles, 0)
	})
}

// TestLifecycleSurface_TwoWayDefensiveCopying verifies that mutating source bundle slices
// after merge does not modify the merged output, and mutating the merged output does not
// modify source slices or other bundle inputs.
func TestLifecycleSurface_TwoWayDefensiveCopying(t *testing.T) {
	t.Parallel()

	l1 := testLifecycle{tag: "orig-1"}
	l2 := testLifecycle{tag: "orig-2"}
	l3 := testLifecycle{tag: "orig-3"}
	mutated := testLifecycle{tag: "MUTATED"}

	t.Run("Source_Mutation_Does_Not_Affect_Output", func(t *testing.T) {
		t.Parallel()

		src1 := []aipplugin.Lifecycle{l1, l2}
		src2 := []aipplugin.Lifecycle{l3}

		b1 := aipfeature.FeatureBundle{SchemaVersion: aipfeature.SchemaVersionV1, Lifecycles: src1}
		b2 := aipfeature.FeatureBundle{SchemaVersion: aipfeature.SchemaVersionV1, Lifecycles: src2}

		gen, err := MergeBundlesGenerated(b1, b2)
		require.NoError(t, err)
		require.Equal(t, []aipplugin.Lifecycle{l1, l2, l3}, gen.Lifecycles)

		// Mutate source slices
		src1[0] = mutated
		src1[1] = mutated
		src2[0] = mutated
		b1.Lifecycles[0] = mutated
		b2.Lifecycles[0] = mutated

		// Verify merge output remains pristine
		assert.Equal(t, []aipplugin.Lifecycle{l1, l2, l3}, gen.Lifecycles, "mutating source lifecycle slices must not affect merged output")
	})

	t.Run("Output_Mutation_Does_Not_Affect_Source", func(t *testing.T) {
		t.Parallel()

		src1 := []aipplugin.Lifecycle{l1, l2}
		src2 := []aipplugin.Lifecycle{l3}

		b1 := aipfeature.FeatureBundle{SchemaVersion: aipfeature.SchemaVersionV1, Lifecycles: src1}
		b2 := aipfeature.FeatureBundle{SchemaVersion: aipfeature.SchemaVersionV1, Lifecycles: src2}

		gen, err := MergeBundlesGenerated(b1, b2)
		require.NoError(t, err)
		require.Equal(t, []aipplugin.Lifecycle{l1, l2, l3}, gen.Lifecycles)

		// Mutate output slice
		gen.Lifecycles[0] = mutated
		gen.Lifecycles[2] = mutated

		// Verify source slices remain pristine
		assert.Equal(t, l1, src1[0], "mutating output lifecycles must not affect source slice 1")
		assert.Equal(t, l2, src1[1], "mutating output lifecycles must not affect source slice 1")
		assert.Equal(t, l3, src2[0], "mutating output lifecycles must not affect source slice 2")
	})

	t.Run("MergeFeatureSurfacesWithHost_TwoWayAliasIsolation", func(t *testing.T) {
		t.Parallel()

		regSrc := []aipplugin.Lifecycle{l1}
		extraSrc := []aipplugin.Lifecycle{l2}

		bReg := aipfeature.FeatureBundle{SchemaVersion: aipfeature.SchemaVersionV1, Lifecycles: regSrc}
		bExtra := aipfeature.FeatureBundle{SchemaVersion: aipfeature.SchemaVersionV1, Lifecycles: extraSrc}

		reg := fakeFeatureBundleRegistry{
			buildFn: func(string, yaml.Node) (aipfeature.FeatureBundle, error) {
				return bReg, nil
			},
		}
		regs := []aipsdk.Registration{
			{Kind: aipsdk.PluginKindFeature, ID: "r1", FactoryKind: "reg-feat", Enabled: true},
		}

		gen, err := MergeFeatureSurfacesWithHost(reg, regs, HostContributions{}, bExtra)
		require.NoError(t, err)
		require.Equal(t, []aipplugin.Lifecycle{l1, l2}, gen.Lifecycles)

		// Mutate source slices
		regSrc[0] = mutated
		extraSrc[0] = mutated

		// Verify output is unaffected
		assert.Equal(t, []aipplugin.Lifecycle{l1, l2}, gen.Lifecycles)

		// Mutate output
		gen.Lifecycles[0] = testLifecycle{tag: "OUT-MUT"}
		assert.Equal(t, mutated, regSrc[0], "output mutation must not affect registry source slice")
		assert.Equal(t, mutated, extraSrc[0], "output mutation must not affect extra source slice")
	})
}
