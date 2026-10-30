package standardplugins

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/featurebundle"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"gopkg.in/yaml.v3"
)

func TestModelSystemPrompt_RegistrationAndOuterEnablement(t *testing.T) {
	reg := testRegistryWithStdBundle(t)
	for _, tc := range []struct {
		raw            string
		enabled, valid bool
		count          int
	}{
		{"rules: [{id: x, model_pattern: '.*', append: text}]", true, true, 1},
		{"rules: [{id: x, model_pattern: '[', append: text}]", true, false, 0},
		{"enabled: false", true, false, 0},
		{"enabled: true", false, true, 0},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			var node yaml.Node
			if err := yaml.Unmarshal([]byte(tc.raw), &node); err != nil {
				t.Fatal(err)
			}
			bundles, err := featurebundle.BuildEnabledFeatureBundles(reg, []aipsdk.Registration{{ID: "model-system-prompt", Kind: aipsdk.PluginKindFeature, Enabled: tc.enabled, Config: aipsdk.ConfigPayload{Node: node}}})
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, err=%v", tc.valid, err)
			}
			if err == nil && len(bundles) != tc.count {
				t.Fatalf("bundles=%d, want %d", len(bundles), tc.count)
			}
			for _, b := range bundles {
				if err := b.Validate(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
