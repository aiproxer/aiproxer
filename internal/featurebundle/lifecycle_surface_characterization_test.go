package featurebundle

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
)

// TestGeneratedMergeSurface_LifecycleNilSemantics characterizes Requirement 4.1, 4.2 (Task 1.4):
// Zero bundles and bundles with nil lifecycle slices must produce exactly nil
// GeneratedMergeSurface.Lifecycles across all generated merge entry points.
func TestGeneratedMergeSurface_LifecycleNilSemantics(t *testing.T) {
	t.Parallel()

	bNil1 := aipfeature.FeatureBundle{
		SchemaVersion: aipfeature.SchemaVersionV1,
		Lifecycles:    nil,
	}
	bNil2 := aipfeature.FeatureBundle{
		SchemaVersion: aipfeature.SchemaVersionV1,
		Lifecycles:    nil,
	}

	t.Run("MergeBundlesGenerated_ZeroBundlesProducesNil", func(t *testing.T) {
		t.Parallel()
		gen, err := MergeBundlesGenerated()
		require.NoError(t, err)
		require.Nil(t, gen.Lifecycles)
	})

	t.Run("MergeBundlesGenerated_NilLifecyclesProducesNil", func(t *testing.T) {
		t.Parallel()
		gen, err := MergeBundlesGenerated(bNil1, bNil2)
		require.NoError(t, err)
		require.Nil(t, gen.Lifecycles)
	})

	t.Run("MergeFeatureSurfaceGenerated_NilLifecyclesProducesNil", func(t *testing.T) {
		t.Parallel()
		reg := fakeFeatureBundleRegistry{
			buildFn: func(string, yaml.Node) (aipfeature.FeatureBundle, error) {
				return bNil1, nil
			},
		}
		regs := []aipsdk.Registration{
			{Kind: aipsdk.PluginKindFeature, ID: "nil-feat", FactoryKind: "nil-feat", Enabled: true},
		}
		gen, err := MergeFeatureSurfaceGenerated(reg, regs)
		require.NoError(t, err)
		require.Nil(t, gen.Lifecycles)
	})

	t.Run("MergeFeatureSurfacesWithHost_NilLifecyclesProducesNil", func(t *testing.T) {
		t.Parallel()
		reg := fakeFeatureBundleRegistry{
			buildFn: func(string, yaml.Node) (aipfeature.FeatureBundle, error) {
				return bNil1, nil
			},
		}
		regs := []aipsdk.Registration{
			{Kind: aipsdk.PluginKindFeature, ID: "nil-feat", FactoryKind: "nil-feat", Enabled: true},
		}
		gen, err := MergeFeatureSurfacesWithHost(reg, regs, HostContributions{}, bNil2)
		require.NoError(t, err)
		require.Nil(t, gen.Lifecycles)
	})
}
