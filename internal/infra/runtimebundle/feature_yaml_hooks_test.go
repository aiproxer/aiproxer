package runtimebundle

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/features/submitnoop"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"gopkg.in/yaml.v3"
)

func testRegistryWithStdBundle(t *testing.T) *pluginreg.Registry {
	t.Helper()
	r := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(r, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestBuildFeatureHooks_rejectsUnknownNoopConfig(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte("foo: bar"), &n); err != nil {
		t.Fatal(err)
	}
	reg := testRegistryWithStdBundle(t)
	_, _, err := BuildFeatureHooks(reg, []aipsdk.Registration{
		{Kind: aipsdk.PluginKindFeature, ID: "submit-noop", Enabled: true, Config: aipsdk.ConfigPayload{Node: n}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestBuildFeatureHooks_acceptsEmptyConfig(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &n); err != nil {
		t.Fatal(err)
	}
	reg := testRegistryWithStdBundle(t)
	_, _, err := BuildFeatureHooks(reg, []aipsdk.Registration{
		{Kind: aipsdk.PluginKindFeature, ID: "submit-noop", Enabled: true, Config: aipsdk.ConfigPayload{Node: n}},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestBuildFeatureHooks_submitNoopLifecycleProbe(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte("lifecycle_probe: true"), &n); err != nil {
		t.Fatal(err)
	}
	reg := testRegistryWithStdBundle(t)
	hookCfg, lifes, err := BuildFeatureHooks(reg, []aipsdk.Registration{
		{Kind: aipsdk.PluginKindFeature, ID: "submit-noop", Enabled: true, Config: aipsdk.ConfigPayload{Node: n}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hookCfg.SubmitHooks) != 1 {
		t.Fatalf("submit hooks: %d", len(hookCfg.SubmitHooks))
	}
	if len(lifes) != 1 {
		t.Fatalf("lifecycles: %d", len(lifes))
	}
	probe, ok := lifes[0].(*submitnoop.LifecycleProbe)
	if !ok {
		t.Fatalf("wrong lifecycle type %T", lifes[0])
	}
	ctx := context.Background()
	if err := probe.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if !probe.WasStarted() {
		t.Fatal("expected Start to run")
	}
	if err := probe.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if !probe.WasStopped() {
		t.Fatal("expected Stop to run")
	}
}

func TestBuildFeatureHooks_submitNoopOrderFromConfig(t *testing.T) {
	t.Parallel()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte("order: 3"), &n); err != nil {
		t.Fatal(err)
	}
	reg := testRegistryWithStdBundle(t)
	hookCfg, lifes, err := BuildFeatureHooks(reg, []aipsdk.Registration{
		{Kind: aipsdk.PluginKindFeature, ID: "submit-noop", Enabled: true, Config: aipsdk.ConfigPayload{Node: n}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(lifes) != 0 {
		t.Fatalf("unexpected lifecycles: %d", len(lifes))
	}
	if hookCfg.SubmitHooks[0].Order() != 3 {
		t.Fatalf("order: %d", hookCfg.SubmitHooks[0].Order())
	}
}
