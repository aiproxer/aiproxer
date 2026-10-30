package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

func testRegistryWithStdBundle(t *testing.T) *pluginreg.Registry {
	t.Helper()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestFeatureHooksFromReferenceConfig_chainsAndPassThrough(t *testing.T) {
	t.Parallel()
	reg := testRegistryWithStdBundle(t)

	cfgPath := filepath.Join("..", "..", "config", "config.yaml")
	cfg, err := config.LoadFile(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := routing.ValidateModelAliasesConfig(cfg); err != nil {
		t.Fatalf("model_aliases: %v", err)
	}

	regs := config.RegistrationsFromConfig(cfg)
	hookCfg, _, err := runtimebundle.BuildFeatureHooks(reg, regs)
	if err != nil {
		t.Fatalf("feature hooks: %v", err)
	}

	bus := hooks.New(hookCfg)
	ns, nrq, nrs, nt := bus.HookChainLengths()
	if ns != 1 || nrq != 1 || nrs != 1 || nt != 1 {
		t.Fatalf("hook counts: submit=%d request=%d response=%d tools=%d", ns, nrq, nrs, nt)
	}

	call := &aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
		Route: aipapi.RouteIntent{Selector: "stub:x"},
	}
	if err := call.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := bus.RunSubmit(context.Background(), call, &sdkhooks.SubmitMeta{Annotations: map[string]string{}}); err != nil {
		t.Fatalf("RunSubmit: %v", err)
	}
	if err := bus.RunRequestPartHooks(context.Background(), call, sdkhooks.PartMeta{TraceID: "t"}); err != nil {
		t.Fatalf("RunRequestPartHooks: %v", err)
	}
	ev := &aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"}
	if err := bus.RunResponsePartHooks(context.Background(), ev, sdkhooks.PartMeta{TraceID: "t"}); err != nil {
		t.Fatalf("RunResponsePartHooks: %v", err)
	}
	te := aipapi.ToolEvent{Kind: aipapi.ToolEventStarted, ToolCallID: "c1", ToolName: "n"}
	res := bus.ApplyToolReactors(context.Background(), te, sdkhooks.ToolMeta{TraceID: "t"})
	if !res.Emit || res.Event != te {
		t.Fatalf("ApplyToolReactors: %+v", res)
	}
}

func TestFeatureHooksFromRegistrations_unknownEnabledFeature(t *testing.T) {
	t.Parallel()
	reg := testRegistryWithStdBundle(t)

	_, _, err := runtimebundle.BuildFeatureHooks(reg, []aipsdk.Registration{
		{Kind: aipsdk.PluginKindFeature, ID: "unknown-feature", Enabled: true},
	})
	if err == nil {
		t.Fatal("expected error for unknown feature id")
	}
}

func TestNewBootstrapApp_withComposedHooks(t *testing.T) {
	t.Parallel()
	reg := testRegistryWithStdBundle(t)

	cfgPath := filepath.Join("..", "..", "config", "config.yaml")
	cfg, err := config.LoadFile(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := routing.ValidateModelAliasesConfig(cfg); err != nil {
		t.Fatalf("model_aliases: %v", err)
	}
	regs := config.RegistrationsFromConfig(cfg)
	hookCfg, _, err := runtimebundle.BuildFeatureHooks(reg, regs)
	if err != nil {
		t.Fatalf("feature hooks: %v", err)
	}

	app, err := runtimebundle.NewBootstrapApp(runtimebundle.BootstrapOptions{
		Config:        cfg,
		Logger:        testkit.DiscardLogger(),
		Registrations: regs,
		Mandatory:     mandatoryStandardPlugins(),
		Hooks:         hookCfg,
	})
	if err != nil {
		t.Fatalf("NewBootstrapApp: %v", err)
	}
	ns, nrq, nrs, nt := app.HookBus().HookChainLengths()
	if ns != 1 || nrq != 1 || nrs != 1 || nt != 1 {
		t.Fatalf("hook counts: submit=%d request=%d response=%d tools=%d", ns, nrq, nrs, nt)
	}
	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
}
