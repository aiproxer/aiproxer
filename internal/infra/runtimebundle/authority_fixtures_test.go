package runtimebundle_test

// Shared usage-authority build fixtures. These stay in the open-source test
// suite because fifteen stay files compose the runtime bundle through
// baseAuthorityConfig/baseAuthorityOptions; the billing-subject tests that
// exercise the authority service itself moved to the Enterprise composition
// with their own bounded fixtures (open-core-billing-split task 5.2).

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"gopkg.in/yaml.v3"
)

func baseAuthorityConfig(enabled bool, startupPosture string) *config.Config {
	return &config.Config{
		Server:      config.ServerConfig{Address: "127.0.0.1:0"},
		Routing:     config.RoutingConfig{DefaultRoute: "stub:test-model", MaxAttempts: 3},
		Continuity:  config.ContinuityConfig{InMemory: true, Store: "memory"},
		Database:    config.DatabaseConfig{MaxOpenConns: 8},
		Diagnostics: config.DiagnosticsConfig{SharedSecret: strings.Repeat("s", 12)},
		Plugins:     config.PluginsConfig{},
		Accounting: config.AccountingConfig{
			Authority: config.AccountingAuthorityConfig{
				Enabled:        enabled,
				Mode:           "strict",
				Store:          "memory",
				StartupPosture: startupPosture,
				Query:          config.AccountingAuthorityQueryConfig{Enabled: false},
				Rules: []config.AccountingAuthorityRuleConfig{
					{
						ID:    "tenant.requests",
						Kind:  "quota",
						Mode:  "strict",
						Unit:  "requests",
						Limit: 10,
						Match: config.AccountingAuthorityDimensionsConfig{
							Backend: config.AccountingAuthorityDimensionMatcherConfig{Value: scope.Known("backend-1")},
						},
					},
				},
			},
		},
	}
}

func baseAuthorityOptions(t *testing.T) *runtimebundle.BuildOptions {
	t.Helper()
	reg := pluginreg.NewRegistry()
	registerAuthorityBackend(t, reg, "stub")
	return &runtimebundle.BuildOptions{PluginRegistry: reg}
}

func registerAuthorityBackend(t *testing.T, reg *pluginreg.Registry, factoryID string) {
	t.Helper()
	if err := reg.RegisterBackend(factoryID, func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{
			Caps:            aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			BackendPrefixes: []string{factoryID},
			ModelInventory:  testModelInventory(),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
			},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}
}
