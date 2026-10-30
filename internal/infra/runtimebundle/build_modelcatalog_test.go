package runtimebundle_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/modelcatalog/modelsdev"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"gopkg.in/yaml.v3"
)

func TestBuild_modelCatalog_disabledDoesNotStartRuntime(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var empty yaml.Node
	if err := yaml.Unmarshal([]byte(testOpenAIBackendYAML()), &empty); err != nil {
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
			Enabled:                false,
			ExternalUpdatesEnabled: false,
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if runtimebundle.CandidateCatalogRuntime(b) != nil {
		t.Fatalf("expected nil CatalogRuntime")
	}
	if b.Executor().CatalogResolver != nil {
		t.Fatalf("expected nil CatalogResolver")
	}
	if b.Executor().RequestTokenEstimator != nil {
		t.Fatalf("expected nil RequestTokenEstimator")
	}
	if b.Ledger().Len() != 3 {
		t.Fatalf("expected generation-owned upstream idle closer plus ledger-owned keep-warm lifecycle prepare and quiesce entries (disabled catalog, in-memory continuity), got %d", b.Ledger().Len())
	}
	closeRuntimeBuilt(t, b)
}

func TestBuild_modelCatalog_enabled_wiresResolversEstimatorAndCloser(t *testing.T) {
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
	raw := []byte(`{"p":{"id":"p","models":[{"id":"m","tool_call":true}]}}`)
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
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if runtimebundle.CandidateCatalogRuntime(b) == nil {
		t.Fatal("expected CatalogRuntime")
	}
	if b.Executor().CatalogResolver == nil || b.Executor().EligibilityResolver == nil || b.Executor().RequestTokenEstimator == nil {
		t.Fatalf("expected catalog wiring on executor: cr=%v el=%v rte=%v",
			b.Executor().CatalogResolver != nil,
			b.Executor().EligibilityResolver != nil,
			b.Executor().RequestTokenEstimator != nil)
	}
	if b.Ledger().Len() == 0 {
		t.Fatal("expected closers")
	}
	closeRuntimeBuilt(t, b)
}

func closeRuntimeBuilt(t *testing.T, b *runtimebundle.CandidateHTTPCompile) {
	t.Helper()
	if b == nil {
		return
	}
	if err := b.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestBuild_modelCatalog_enabled_missingCachePath_validationFails(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var empty yaml.Node
	if err := yaml.Unmarshal([]byte(testOpenAIBackendYAML()), &empty); err != nil {
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
			Enabled:   true,
			CachePath: "",
		},
	}
	err := config.Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "model_catalog.cache_path") {
		t.Fatalf("want cache_path validation error, got %v", err)
	}
}
