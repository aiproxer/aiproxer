package pluginreg_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"gopkg.in/yaml.v3"
)

func TestBuildFeatureHooks_usesExplicitRegistryNotDefault(t *testing.T) {
	t.Parallel()

	factoryID := "custom-registry-feature-" + strings.ReplaceAll(t.Name(), "/", "-")
	reg := pluginreg.NewRegistry()
	if err := reg.RegisterFeature(
		factoryID,
		func(n yaml.Node) (aipfeature.FeatureBundle, error) {
			_ = n
			return aipfeature.FeatureBundle{SchemaVersion: aipfeature.SchemaVersionV1}, nil
		},
	); err != nil {
		t.Fatal(err)
	}

	var cfgNode yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &cfgNode); err != nil {
		t.Fatal(err)
	}
	regs := []aipsdk.Registration{{
		Kind:        aipsdk.PluginKindFeature,
		ID:          "feat-inst",
		FactoryKind: factoryID,
		Enabled:     true,
		Config:      aipsdk.ConfigPayload{Node: cfgNode},
	}}

	if _, _, err := runtimebundle.BuildFeatureHooks(reg, regs); err != nil {
		t.Fatal(err)
	}
	empty := pluginreg.NewRegistry()
	if _, _, err := runtimebundle.BuildFeatureHooks(empty, regs); err == nil {
		t.Fatal("expected empty registry to miss custom-only feature factory")
	}
}
