package runtimebundle_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/internal/testkit"
)

// keepwarmLedgerTestConfig enables keep-warm through absent-entry defaults:
// no keepwarm feature registration means the feature decoder defaults apply
// (enabled), so every candidate acquires a generation manager.
func keepwarmLedgerTestConfig() *config.Config {
	return &config.Config{
		Server:     config.ServerConfig{Address: "127.0.0.1:0"},
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
	}
}

func mustKeepwarmLedgerProcess(t *testing.T, cfg *config.Config) *runtimebundle.ProcessServices {
	t.Helper()
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: pluginreg.NewRegistry()},
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	return ps
}

func keepwarmRegistryLen(t *testing.T, ps *runtimebundle.ProcessServices) int {
	t.Helper()
	if ps.StandardFeatures == nil || ps.StandardFeatures.KeepwarmRegistry() == nil {
		t.Fatal("expected process-owned keepwarm registry")
	}
	return ps.StandardFeatures.KeepwarmRegistry().Len()
}

// TestCompileGeneration_RejectedCandidateQuiescesKeepwarmManager is the H1
// regression: a candidate that fails after keep-warm acquisition (fault
// injected at handler composition) must not retain its generation manager in
// the process registry.
func TestCompileGeneration_RejectedCandidateQuiescesKeepwarmManager(t *testing.T) {
	t.Parallel()
	cfg := keepwarmLedgerTestConfig()
	ps := mustKeepwarmLedgerProcess(t, cfg)
	before := keepwarmRegistryLen(t, ps)

	_, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:     ps,
		Candidate:   cfg,
		Compose:     stdhttp.ComposeStandardHTTP,
		FaultInject: runtimebundle.CandidateFaultInject{After: "handler"},
	})
	if err == nil {
		t.Fatal("expected injected handler fault, got nil")
	}

	if got := keepwarmRegistryLen(t, ps); got != before {
		t.Fatalf("rejected candidate retained keepwarm manager: registry len=%d want %d", got, before)
	}
}

// TestCompileGeneration_OverlappingGenerationsKeepwarmRegistryCounts pins the
// steady-state ownership: each published generation owns exactly one registry
// entry, rejected candidates add none, and close releases each entry once.
func TestCompileGeneration_OverlappingGenerationsKeepwarmRegistryCounts(t *testing.T) {
	t.Parallel()
	cfg := keepwarmLedgerTestConfig()
	ps := mustKeepwarmLedgerProcess(t, cfg)
	if got := keepwarmRegistryLen(t, ps); got != 0 {
		t.Fatalf("initial registry len=%d want 0", got)
	}

	gen1, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: cfg,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("CompileGeneration #1: %v", err)
	}
	if got := keepwarmRegistryLen(t, ps); got != 1 {
		t.Fatalf("after gen1 registry len=%d want 1", got)
	}

	if _, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:     ps,
		Candidate:   cfg,
		Compose:     stdhttp.ComposeStandardHTTP,
		FaultInject: runtimebundle.CandidateFaultInject{After: "composer-clone"},
	}); err == nil {
		t.Fatal("expected injected composer-clone fault, got nil")
	}
	if got := keepwarmRegistryLen(t, ps); got != 1 {
		t.Fatalf("after rejected candidate registry len=%d want 1", got)
	}

	gen2, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: cfg,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("CompileGeneration #2: %v", err)
	}
	if got := keepwarmRegistryLen(t, ps); got != 2 {
		t.Fatalf("after gen2 registry len=%d want 2", got)
	}

	if err := gen2.Close(); err != nil {
		t.Fatalf("gen2 Close: %v", err)
	}
	if got := keepwarmRegistryLen(t, ps); got != 1 {
		t.Fatalf("after gen2 Close registry len=%d want 1", got)
	}
	if err := gen1.Close(); err != nil {
		t.Fatalf("gen1 Close: %v", err)
	}
	if got := keepwarmRegistryLen(t, ps); got != 0 {
		t.Fatalf("after gen1 Close registry len=%d want 0", got)
	}
}

// TestCompileGeneration_CandidateCompileFailureLeavesNoKeepwarmTrace covers
// failure INSIDE candidate compilation: a fault injected at the "model"
// boundary (before the candidate ledger adopts the generation handle) must
// leave no keep-warm trace in the process registry. The handle starts and
// registers at ledger prepare, so pre-prepare failures release nothing.
func TestCompileGeneration_CandidateCompileFailureLeavesNoKeepwarmTrace(t *testing.T) {
	t.Parallel()
	cfg := keepwarmLedgerTestConfig()
	ps := mustKeepwarmLedgerProcess(t, cfg)
	before := keepwarmRegistryLen(t, ps)

	_, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:     ps,
		Candidate:   cfg,
		Compose:     stdhttp.ComposeStandardHTTP,
		FaultInject: runtimebundle.CandidateFaultInject{After: "model"},
	})
	if err == nil {
		t.Fatal("expected injected model fault, got nil")
	}

	if got := keepwarmRegistryLen(t, ps); got != before {
		t.Fatalf("candidate-compile failure leaked keepwarm manager: registry len=%d want %d", got, before)
	}
}

// TestCompileGeneration_RetirementQuiescesKeepwarmManager pins Requirement 6.5
// quiesce ordering: retiring a published generation (Quiesce, without Close)
// must release its keep-warm manager from the process registry so maintenance
// work stops at retirement, not only at final close.
func TestCompileGeneration_RetirementQuiescesKeepwarmManager(t *testing.T) {
	t.Parallel()
	cfg := keepwarmLedgerTestConfig()
	ps := mustKeepwarmLedgerProcess(t, cfg)

	gen, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: cfg,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}
	if got := keepwarmRegistryLen(t, ps); got != 1 {
		t.Fatalf("after publish registry len=%d want 1", got)
	}
	qc, ok := gen.(interface{ Quiesce(context.Context) error })
	if !ok {
		t.Fatal("published generation does not expose Quiesce")
	}
	if err := qc.Quiesce(context.Background()); err != nil {
		t.Fatalf("Quiesce: %v", err)
	}
	if got := keepwarmRegistryLen(t, ps); got != 0 {
		t.Fatalf("retired generation retained keepwarm manager: registry len=%d want 0", got)
	}
	if err := gen.Close(); err != nil {
		t.Fatalf("Close after Quiesce: %v", err)
	}
	if got := keepwarmRegistryLen(t, ps); got != 0 {
		t.Fatalf("after Close registry len=%d want 0", got)
	}
}
