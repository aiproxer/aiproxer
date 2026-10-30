package runtimebundle_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/modelcatalog/modelsdev"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"gopkg.in/yaml.v3"
)

func TestOverrideSetFromModelCatalog_mapsModelAndPairKeys(t *testing.T) {
	t.Parallel()
	set := runtimebundle.OverrideSetFromModelCatalog(config.ModelCatalogConfig{
		ModelOverrides: []config.ModelCatalogModelOverrideEntry{
			{Model: "  my/model  "},
		},
		BackendModelOverrides: []config.ModelCatalogBackendModelOverrideEntry{
			{Backend: "be1", Model: "m1"},
		},
	})
	if len(set.Pair) != 1 || len(set.Model) != 1 {
		t.Fatalf("pair=%d model=%d", len(set.Pair), len(set.Model))
	}
	pf := set.Pair["be1:m1"]
	if pf.Source != modelcatalog.FactSourcePairOverride {
		t.Fatalf("pair source: %v", pf.Source)
	}
	mf := set.Model["my/model"]
	if mf.Source != modelcatalog.FactSourceModelOverride {
		t.Fatalf("model source: %v", mf.Source)
	}
}

func TestOverrideSetFromModelCatalog_capabilityAndLimits(t *testing.T) {
	t.Parallel()
	toolsFalse := false
	ctxLim := int64(128000)
	set := runtimebundle.OverrideSetFromModelCatalog(config.ModelCatalogConfig{
		ModelOverrides: []config.ModelCatalogModelOverrideEntry{
			{
				Model:              "m1",
				Tools:              &toolsFalse,
				ContextLimitTokens: &ctxLim,
			},
		},
	})
	mf := set.Model["m1"]
	if mf.Tools != modelcatalog.CapabilityUnsupported {
		t.Fatalf("tools: %v", mf.Tools)
	}
	if mf.ContextLimit.State != modelcatalog.LimitPresent || mf.ContextLimit.Tokens != 128000 {
		t.Fatalf("context limit: %+v", mf.ContextLimit)
	}
}

func TestBuild_modelCatalog_operatorOverridesReachResolver(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var empty yaml.Node
	if err := yaml.Unmarshal([]byte(testOpenAIBackendYAML()), &empty); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "catalog.json")
	raw := []byte(`{"p":{"id":"p","models":[{"id":"unrelated","tool_call":true}]}}`)
	s0, err := modelsdev.ParseSnapshot(raw, time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := modelsdev.NewFileSnapshotStore(cachePath).Save(context.Background(), s0); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Routing: config.RoutingConfig{MaxAttempts: 3},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{Kind: "openai-responses", ID: "openai-only", Enabled: true, Config: empty},
			},
		},
		Continuity: config.ContinuityConfig{InMemory: true},
		ModelCatalog: config.ModelCatalogConfig{
			Enabled:                true,
			ExternalUpdatesEnabled: false,
			CachePath:              cachePath,
			ModelOverrides: []config.ModelCatalogModelOverrideEntry{
				{Model: "operator-model-x"},
			},
			BackendModelOverrides: []config.ModelCatalogBackendModelOverrideEntry{
				{Backend: "openai-only", Model: "pair-y"},
			},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if b.Executor().CatalogResolver == nil {
		t.Fatal("expected CatalogResolver")
	}
	base := aipapi.NewBackendCaps(aipapi.CapabilityStreaming)
	ef := b.Executor().CatalogResolver.Resolve(
		context.Background(),
		routing.AttemptCandidate{Primary: routing.Primary{Backend: "openai-only", Model: "operator-model-x"}},
		aipapi.Call{},
		base,
	)
	if ef.Facts.Source != modelcatalog.FactSourceModelOverride {
		t.Fatalf("model override: got source %v", ef.Facts.Source)
	}
	efPair := b.Executor().CatalogResolver.Resolve(
		context.Background(),
		routing.AttemptCandidate{Primary: routing.Primary{Backend: "openai-only", Model: "pair-y"}},
		aipapi.Call{},
		base,
	)
	if efPair.Facts.Source != modelcatalog.FactSourcePairOverride {
		t.Fatalf("pair override: got source %v", efPair.Facts.Source)
	}
}
