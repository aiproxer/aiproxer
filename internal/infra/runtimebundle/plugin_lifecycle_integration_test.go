package runtimebundle_test

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
	"gopkg.in/yaml.v3"
)

func TestCompileCandidate_LifecycleCleanupOnLaterAssemblyFailure(t *testing.T) {
	t.Parallel()
	factoryID := "lifecycle-cleanup-" + strings.ReplaceAll(t.Name(), "/", "-")
	reg := pluginreg.NewRegistry()
	var cleaned atomic.Int64
	err := reg.RegisterLifecycleBackend(factoryID, func(_ string, _ yaml.Node, _ *http.Client, _ pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
		return pluginreg.BackendBuildResult{
			Backend: execbackend.Backend{
				Caps:            aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
				BackendPrefixes: []string{factoryID},
				ModelInventory: modelinventory.StaticProvider{
					Source: modelinventory.SourceStaticBuiltin,
					Models: []modelinventory.Model{{
						CanonicalID: "test/model", NativeID: "model", DisplayName: "Test Model",
					}},
				},
				Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
					return nil, nil
				},
			},
			Cleanup: func() error {
				cleaned.Add(1)
				return nil
			},
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Accounting: config.AccountingConfig{StrictAuthoritative: true},
		Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{
			Kind: factoryID, ID: "be", Enabled: true,
		}}},
	}
	_, _, err = processAndCandidateErr(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if err == nil || !strings.Contains(err.Error(), "strict_authoritative requires billing finalizer") {
		t.Fatalf("compile err = %v, want strict_authoritative failure", err)
	}
	if cleaned.Load() != 1 {
		t.Fatalf("plugin BuildResult cleanup ran %d times, want 1", cleaned.Load())
	}
}
