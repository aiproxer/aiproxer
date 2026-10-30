package runtimebundle_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"gopkg.in/yaml.v3"
)

func TestBuild_backendConstructionUsesInjectedRegistryNotDefault(t *testing.T) {
	t.Parallel()

	factoryID := "custom-registry-backend-" + strings.ReplaceAll(t.Name(), "/", "-")
	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackend(factoryID, func(n yaml.Node, upstream *http.Client, _ pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		_ = n
		_ = upstream
		return execbackend.Backend{
			Caps:            aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			BackendPrefixes: []string{factoryID},
			ModelInventory:  testModelInventory(),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return nil, errors.New("exclusive-registry-probe")
			},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}

	empty := pluginreg.NewRegistry()
	if _, err := empty.BuildBackend(factoryID, yaml.Node{}, nil, pluginreg.BackendFactoryDeps{}); err == nil {
		t.Fatal("expected empty registry to omit custom factory id")
	}

	var cfgNode yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &cfgNode); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Routing: config.RoutingConfig{MaxAttempts: 3},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{
				{Kind: factoryID, ID: "only-instance", Enabled: true, Config: cfgNode},
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
	be, ok := b.Executor().Backends["only-instance"]
	if !ok {
		t.Fatal("expected backend instance")
	}
	call := aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("x")}}},
		Route:    aipapi.RouteIntent{Selector: "only-instance:model"},
	}
	_, err := be.Open(context.Background(), call, routing.AttemptCandidate{})
	if err == nil || !strings.Contains(err.Error(), "exclusive-registry-probe") {
		t.Fatalf("Open: %v", err)
	}
}
