package standardplugins

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/featurebundle"
	"github.com/aiproxer/aiproxer/internal/plugins/features/downstreamcacheaffinity"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type countingFeatureRegistry struct {
	inner featurebundle.FeatureBundleRegistry
	calls int
}

func (c *countingFeatureRegistry) BuildFeatureBundle(key string, n yaml.Node) (aipfeature.FeatureBundle, error) {
	c.calls++
	return c.inner.BuildFeatureBundle(key, n)
}

func TestStandardBundle_DownstreamCacheAffinityOptIn(t *testing.T) {
	for _, tc := range []struct {
		name             string
		present, enabled bool
		want             int
		config           string
		invalid          bool
	}{
		{name: "absent"},
		{name: "disabled", present: true},
		{name: "enabled", present: true, enabled: true, want: 1},
		{name: "null", present: true, enabled: true, want: 1, config: "null\n"},
		{name: "empty_map", present: true, enabled: true, want: 1, config: "{}\n"},
		{name: "mapping", present: true, enabled: true, want: 1, config: "mode: session\n", invalid: true},
		{name: "nested", present: true, enabled: true, want: 1, config: "derive:\n  keyed: true\n", invalid: true},
		{name: "scalar", present: true, enabled: true, want: 1, config: "always\n", invalid: true},
		{name: "sequence", present: true, enabled: true, want: 1, config: "- downstream\n", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var rows []aipsdk.Registration
			if tc.present {
				rows = []aipsdk.Registration{{
					ID: downstreamcacheaffinity.ID, Kind: aipsdk.PluginKindFeature, Enabled: tc.enabled,
					Config: aipsdk.ConfigPayload{Node: featureConfigNode(t, tc.config)},
				}}
			}
			counter := &countingFeatureRegistry{inner: testRegistryWithStdBundle(t)}
			merged, err := featurebundle.MergeFeatureSurfacesWithHost(counter, rows, featurebundle.HostContributions{})
			require.Equal(t, tc.want, counter.calls, "absent/disabled must not build")
			if tc.invalid {
				require.ErrorContains(t, err, "downstream-cache-affinity")
				require.ErrorContains(t, err, "config")
				require.True(t, merged.Frozen.IsZero())
				return
			}
			require.NoError(t, err)
			transforms := aipfeature.Get(merged.Frozen, aipfeature.PlaneAttemptTransforms)
			require.Len(t, transforms, tc.want)
			for _, tr := range transforms {
				require.Equal(t, "downstream-cache-affinity", tr.ID())
				require.IsType(t, downstreamcacheaffinity.Transform{}, tr)
				require.Equal(t, []string{aipfeature.PlaneAttemptTransforms.ID}, occupiedStandardPlanes(t, merged.Frozen))
			}
			require.Empty(t, merged.Lifecycles)
		})
	}
}
