package runtimebundle_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"gopkg.in/yaml.v3"
)

func TestBuild_twoInstancesSameFactoryKind(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
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
				{Kind: "openai-responses", ID: "openai-primary", Enabled: true, Config: empty},
				{Kind: "openai-responses", ID: "openai-fallback", Enabled: true, Config: empty},
			},
		},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if len(b.Executor().Backends) != 2 {
		t.Fatalf("backends: got %d want 2", len(b.Executor().Backends))
	}
	if _, ok := b.Executor().Backends["openai-primary"]; !ok {
		t.Fatal("missing instance openai-primary")
	}
	if _, ok := b.Executor().Backends["openai-fallback"]; !ok {
		t.Fatal("missing instance openai-fallback")
	}
}

func TestBuild_customBackendsRejectDuplicatePrefixBeforeModelRegistry(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var node yaml.Node
	if err := yaml.Unmarshal([]byte("backend_prefix: provider123\nbase_url: http://127.0.0.1:9/v1\n"), &node); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Routing: config.RoutingConfig{MaxAttempts: 3},
		Plugins: config.PluginsConfig{Backends: []config.PluginConfig{
			{Kind: standardplugins.CustomOpenAILegacyCompatibleID, ID: "provider-chat", Enabled: true, Config: node},
			{Kind: standardplugins.CustomOpenAIResponsesCompatibleID, ID: "provider-responses", Enabled: true, Config: node},
		}},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	_, _, err := processAndCandidateErr(t, cfg, &runtimebundle.BuildOptions{PluginRegistry: reg})
	if err == nil {
		t.Fatal("expected duplicate custom backend prefix error")
	}
	assertOwnershipCollisionErr(t, err, "provider123", "provider-chat", "provider-responses")
}

func TestBuild_customBackendsRejectReservedStandardPrefix(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	var node yaml.Node
	if err := yaml.Unmarshal([]byte("backend_prefix: openai-legacy\nbase_url: http://127.0.0.1:9/v1\n"), &node); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Routing: config.RoutingConfig{MaxAttempts: 3},
		Plugins: config.PluginsConfig{Backends: []config.PluginConfig{
			{Kind: standardplugins.CustomOpenAILegacyCompatibleID, ID: "openai-legacy-copy", Enabled: true, Config: node},
		}},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	_, _, err := processAndCandidateErr(t, cfg, &runtimebundle.BuildOptions{PluginRegistry: reg})
	if err == nil {
		t.Fatal("expected reserved custom backend prefix error")
	}
	assertOwnershipCollisionErr(t, err, "openai-legacy", "openai-legacy", "openai-legacy-copy")
}

func assertOwnershipCollisionErr(t *testing.T, err error, key, ownerA, ownerB string) {
	t.Helper()
	var coll *pluginreg.OwnershipCollisionError
	if !errors.As(err, &coll) {
		t.Fatalf("error type %T (%v) is not OwnershipCollisionError", err, err)
	}
	if coll.Key != key {
		t.Fatalf("collision key = %q, want %q", coll.Key, key)
	}
	msg := coll.Error()
	if !strings.Contains(msg, ownerA) || !strings.Contains(msg, ownerB) {
		t.Fatalf("collision error %q must identify owners %q and %q", msg, ownerA, ownerB)
	}
}
