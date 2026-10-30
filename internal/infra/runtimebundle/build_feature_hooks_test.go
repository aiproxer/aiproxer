package runtimebundle

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/features/submitnoop"
	"github.com/aiproxer/aiproxer/internal/plugins/features/toolreactornoop"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"gopkg.in/yaml.v3"
)

func TestBuildFeatureHooks_partialBundlesLeaveOtherChainsAbsent(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	submitFacID := "test-fac-submit-" + strings.ReplaceAll(t.Name(), "/", "-")
	toolFacID := "test-fac-tool-" + strings.ReplaceAll(t.Name(), "/", "-")

	if err := reg.RegisterFeature(submitFacID, func(n yaml.Node) (aipfeature.FeatureBundle, error) {
		cfg, err := submitnoop.DecodeHookConfig(n)
		if err != nil {
			return aipfeature.FeatureBundle{}, err
		}
		return testkit.FeatureBundle(t, submitFacID, func(cs *aipfeature.ContributionSet) error {
			return aipfeature.Contribute(cs, aipfeature.PlaneSubmitHooks, submitFacID, []sdk.SubmitHook{submitnoop.NewSubmitHookWithConfig(cfg)})
		}, nil), nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterFeature(toolFacID, func(n yaml.Node) (aipfeature.FeatureBundle, error) {
		return testkit.FeatureBundle(t, toolFacID, func(cs *aipfeature.ContributionSet) error {
			return aipfeature.Contribute(cs, aipfeature.PlaneToolReactors, toolFacID, []sdk.ToolReactor{toolreactornoop.NewToolReactor()})
		}, nil), nil
	}); err != nil {
		t.Fatal(err)
	}

	var cfgNode yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &cfgNode); err != nil {
		t.Fatal(err)
	}
	hookCfg, _, err := BuildFeatureHooks(reg, []aipsdk.Registration{
		{Kind: aipsdk.PluginKindFeature, ID: "inst-submit", FactoryKind: submitFacID, Enabled: true, Config: aipsdk.ConfigPayload{Node: cfgNode}},
		{Kind: aipsdk.PluginKindFeature, ID: "inst-tool", FactoryKind: toolFacID, Enabled: true, Config: aipsdk.ConfigPayload{Node: cfgNode}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hookCfg.SubmitHooks) != 1 {
		t.Fatalf("submit hooks: %d", len(hookCfg.SubmitHooks))
	}
	if len(hookCfg.ToolReactors) != 1 {
		t.Fatalf("tool reactors: %d", len(hookCfg.ToolReactors))
	}
	if len(hookCfg.RequestPartHooks) != 0 {
		t.Fatalf("expected no request part hooks, got %d", len(hookCfg.RequestPartHooks))
	}
	if len(hookCfg.ResponsePartHooks) != 0 {
		t.Fatalf("expected no response part hooks, got %d", len(hookCfg.ResponsePartHooks))
	}

	bus := hooks.New(hookCfg)
	ns, nrq, nrs, nt := bus.HookChainLengths()
	if ns != 1 || nrq != 0 || nrs != 0 || nt != 1 {
		t.Fatalf("hook chain lengths: submit=%d request=%d response=%d tools=%d", ns, nrq, nrs, nt)
	}
}
