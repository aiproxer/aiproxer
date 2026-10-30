package bundle_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/bundle"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/config"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"gopkg.in/yaml.v3"
)

func BenchmarkBundle_OptionalTelemetry(b *testing.B) {
	var node yaml.Node
	if err := yaml.Unmarshal([]byte("enabled: true\nmode: rewrite\n"), &node); err != nil {
		b.Fatal(err)
	}
	resolved, err := config.Decode(node)
	if err != nil {
		b.Fatal(err)
	}
	for _, record := range []bool{false, true} {
		name := "stock"
		if record {
			name = "reader_enabled"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				var result aipfeature.FeatureBundle
				var err error
				if record {
					_, result, err = bundle.FeatureBundleWithTelemetry(resolved)
				} else {
					result, err = bundle.FeatureBundle(resolved)
				}
				if err != nil {
					b.Fatal(err)
				}
				if len(aipfeature.Get(result.PlaneSet, aipfeature.PlaneAttemptTransforms)) != 1 {
					b.Fatal("enabled bundle lost its attempt transform")
				}
			}
		})
	}
}
