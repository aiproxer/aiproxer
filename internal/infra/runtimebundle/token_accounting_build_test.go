package runtimebundle_test

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	accountingapp "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	accountingpreflight "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/preflight"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"gopkg.in/yaml.v3"
)

type requestAdmissionCapture struct {
	mu   sync.Mutex
	last authority.RequestAdmission
}

func (p *requestAdmissionCapture) AdmitRequest(_ context.Context, in authority.RequestAdmission) (authority.Decision, error) {
	p.mu.Lock()
	p.last = in
	p.mu.Unlock()
	return authority.Decision{Kind: authority.DecisionAllow, ProviderID: "token-accounting-request"}, nil
}

func (*requestAdmissionCapture) SettleRequest(_ context.Context, in authority.RequestSettlement) (authority.Settlement, error) {
	return authority.OwnedFinalSettlement(in.Handles), nil
}

func (*requestAdmissionCapture) ReleaseRequest(context.Context, authority.RequestRelease) error {
	return nil
}

func (p *requestAdmissionCapture) admission() authority.RequestAdmission {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.last
}

func TestBuildWiresTokenAccountingContracts(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackend("stub", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{
			Caps:            aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			BackendPrefixes: []string{"stub"},
			ModelInventory:  testModelInventory(),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
			},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}

	var empty yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &empty); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Server:     config.ServerConfig{Address: "127.0.0.1:0"},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{Backends: []config.PluginConfig{
			{Kind: "stub", ID: "stub", Enabled: true, Config: empty},
		}},
		Accounting: config.AccountingConfig{
			Enabled:      true,
			Mode:         "provider_first",
			CountTimeout: "1s",
			Tokenizer:    config.AccountingTokenizerConfig{DefaultEncoding: "cl100k_base"},
			Preflight:    config.AccountingPreflightConfig{Mode: "advisory", MaxContextTokens: 8192},
			Ledger:       config.AccountingLedgerConfig{Store: "memory", WritePolicy: "required"},
			Admin:        config.AccountingAdminConfig{Enabled: true, Path: "/admin/token-count", MaxBodyBytes: 2048},
			Observability: config.AccountingObservabilityConfig{
				Enabled: true,
			},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}

	_, built := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if runtimebundle.CandidateTokenAccountingAdmin(built) == nil {
		t.Fatal("Built.TokenAccountingAdmin is nil")
	}
	if built.Executor().Preflight == nil {
		t.Fatal("Executor.Preflight is nil")
	}
	if built.Executor().StreamUsage == nil {
		t.Fatal("Executor.StreamUsage is nil")
	}
	if built.Executor().TokenAccountingObservability == nil {
		t.Fatal("Executor.TokenAccountingObservability is nil")
	}
	if built.Executor().AdminCountService == nil {
		t.Fatal("Executor.AdminCountService is nil")
	}
	result, err := built.Executor().AdminCountService.CountCall(context.Background(), accountingapp.CountCallInput{
		Backend: "stub",
		Model:   "gpt-4o-mini",
		CallID:  "call-1",
		Call: aipapi.Call{Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hello")},
		}}},
	})
	if err != nil {
		t.Fatalf("admin CountCall: %v", err)
	}
	if result.InputTokens <= 0 {
		t.Fatalf("InputTokens = %d, want > 0", result.InputTokens)
	}
	if result.Accounting.Source != aipapi.UsageSourceLocalTokenizer {
		t.Fatalf("Source = %q, want %q", result.Accounting.Source, aipapi.UsageSourceLocalTokenizer)
	}
}

func TestBuildWiresIngressCountingWhenAccountingAdminDisabled(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackend("stub", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{
			Caps:            aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			BackendPrefixes: []string{"stub"},
			ModelInventory:  testModelInventory(),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}
	var empty yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &empty); err != nil {
		t.Fatal(err)
	}
	provider := &requestAdmissionCapture{}
	cfg := &config.Config{
		Server:     config.ServerConfig{Address: "127.0.0.1:0"},
		Routing:    config.RoutingConfig{DefaultRoute: "stub:gpt-4o-mini", MaxAttempts: 1},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{Kind: "stub", ID: "stub", Enabled: true, Config: empty}}},
		Accounting: config.AccountingConfig{
			Enabled:   true,
			Mode:      "local_only",
			Tokenizer: config.AccountingTokenizerConfig{DefaultEncoding: "cl100k_base"},
			Preflight: config.AccountingPreflightConfig{Mode: "advisory"},
			Ledger:    config.AccountingLedgerConfig{Store: "memory", WritePolicy: "required"},
			Admin:     config.AccountingAdminConfig{Enabled: false},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	_, built := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
		Production: runtimebundle.ProductionOptions{
			RequestRegistrations: []authority.RequestRegistration{{
				Descriptor: authority.ProviderDescriptor{
					ID:   "token-accounting-request",
					Kind: authority.ProviderKindAuthority,
					Postures: []authority.StagePosture{{
						Stage:           authority.StageRequestAdmit,
						Strength:        authority.StrengthRequired,
						FailureBehavior: authority.FailureFailClosed,
					}},
				},
				Priority: authority.RequestPriorityQuotaBudgetRate,
				Provider: provider,
			}},
		},
	})
	if runtimebundle.CandidateTokenAccountingAdmin(built) != nil {
		t.Fatal("public token accounting admin must remain disabled")
	}
	stream, err := built.Executor().Execute(context.Background(), &aipapi.Call{
		ID:    "request-admin-disabled",
		Route: aipapi.RouteIntent{Selector: "stub:gpt-4o-mini"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("count this request")},
		}},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	got := provider.admission()
	var inputTokens int64
	for _, q := range got.Exposure.Quantities {
		if q.Component == metering.ComponentInputToken && q.Present {
			inputTokens = q.Value
		}
	}
	if inputTokens <= 0 {
		t.Fatalf("request admission input_token = %d, want > 0; exposure=%+v", inputTokens, got.Exposure)
	}
}

func TestBuildTokenAccountingUsesDefaultCountTimeoutWhenOmitted(t *testing.T) {
	t.Parallel()

	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackend("stub", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{
			Caps:            aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			BackendPrefixes: []string{"stub"},
			ModelInventory:  testModelInventory(),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
			},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}
	var empty yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &empty); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Server:     config.ServerConfig{Address: "127.0.0.1:0"},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{Kind: "stub", ID: "stub", Enabled: true, Config: empty}}},
		Accounting: config.AccountingConfig{
			Enabled:   true,
			Mode:      "local_only",
			Tokenizer: config.AccountingTokenizerConfig{DefaultEncoding: "cl100k_base"},
			Preflight: config.AccountingPreflightConfig{Mode: "advisory"},
			Ledger:    config.AccountingLedgerConfig{Store: "memory", WritePolicy: "required"},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}

	_, built := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{PluginRegistry: reg})
	if built.Executor().Preflight == nil {
		t.Fatal("Executor.Preflight is nil")
	}
}

func TestBuildWiresConfiguredAccountingPreflightLimits(t *testing.T) {
	t.Parallel()

	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackend("stub", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{
			Caps:            aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			BackendPrefixes: []string{"stub"},
			ModelInventory:  testModelInventory(),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
			},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}
	var empty yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &empty); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Server:     config.ServerConfig{Address: "127.0.0.1:0"},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{Kind: "stub", ID: "stub", Enabled: true, Config: empty}}},
		Accounting: config.AccountingConfig{
			Enabled:      true,
			Mode:         "local_only",
			CountTimeout: "1s",
			Tokenizer:    config.AccountingTokenizerConfig{DefaultEncoding: "cl100k_base"},
			Preflight:    config.AccountingPreflightConfig{Mode: "required", MaxInputTokens: 1},
			Ledger:       config.AccountingLedgerConfig{Store: "memory", WritePolicy: "required"},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	_, built := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{PluginRegistry: reg})

	decision := built.Executor().Preflight.Check(context.Background(), accountingpreflight.Input{
		Backend: "stub",
		Model:   "gpt-4o-mini",
		CallID:  "call-1",
		Call: aipapi.Call{Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hello world")},
		}}},
	})

	if decision.Allowed {
		t.Fatalf("Allowed = true, want configured max_input_tokens rejection; count=%+v", decision.Count)
	}
	if decision.Reason != accountingpreflight.ReasonInputLimitExceeded {
		t.Fatalf("Reason = %q, want %q", decision.Reason, accountingpreflight.ReasonInputLimitExceeded)
	}
}

func TestBuildProviderRequiredFailsWithoutProviderCounter(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackend("stub", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{
			Caps:            aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			BackendPrefixes: []string{"stub"},
			ModelInventory:  testModelInventory(),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
			},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}
	var empty yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &empty); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Server:     config.ServerConfig{Address: "127.0.0.1:0"},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{Kind: "stub", ID: "stub", Enabled: true, Config: empty}}},
		Accounting: config.AccountingConfig{
			Enabled:      true,
			Mode:         "provider_required",
			CountTimeout: "1s",
			Preflight:    config.AccountingPreflightConfig{Mode: "required"},
			Ledger:       config.AccountingLedgerConfig{Store: "memory", WritePolicy: "required"},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	_, _, err := processAndCandidateErr(t, cfg, &runtimebundle.BuildOptions{PluginRegistry: reg})
	if err == nil {
		t.Fatal("expected provider_required build to fail without provider counter")
	}
}

func TestBuildIgnoresRetiredTokenAccountingLedger(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackend("stub", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{
			Caps:            aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			BackendPrefixes: []string{"stub"},
			ModelInventory:  testModelInventory(),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
			},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}
	var empty yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &empty); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Server:     config.ServerConfig{Address: "127.0.0.1:0"},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{Kind: "stub", ID: "stub", Enabled: true, Config: empty}}},
		Accounting: config.AccountingConfig{
			Enabled:      true,
			Mode:         "local_only",
			CountTimeout: "1s",
			Tokenizer:    config.AccountingTokenizerConfig{DefaultEncoding: "cl100k_base"},
			Preflight:    config.AccountingPreflightConfig{Mode: "advisory"},
			Ledger: config.AccountingLedgerConfig{
				Store:       "sqlite",
				SQLitePath:  t.TempDir() + "/token-accounting.db",
				WritePolicy: "required",
			},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	_, built := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{PluginRegistry: reg})
	if built.Executor() == nil || built.Executor().StreamUsage == nil {
		t.Fatal("expected protocol/quota token accounting to remain wired")
	}
	if err := built.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}
