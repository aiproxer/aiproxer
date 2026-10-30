package runtimebundle_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
)

type capturePolicyObserver struct {
	mu      sync.Mutex
	records []policydecision.Record
}

func (c *capturePolicyObserver) OnPolicyDecision(_ context.Context, record policydecision.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, record)
	return nil
}

func (c *capturePolicyObserver) snapshot() []policydecision.Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]policydecision.Record, len(c.records))
	copy(out, c.records)
	return out
}

func minimalPolicyBuildConfig() *config.Config {
	return &config.Config{
		Routing: config.RoutingConfig{MaxAttempts: 3},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
}

func TestBuildRuntimeBundle_PolicyObserverDefaultsToNoop(t *testing.T) {
	t.Parallel()
	cfg := minimalPolicyBuildConfig()
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
	})
	obs := b.RuntimeSnapshot().PolicyObserver()
	if obs == nil {
		t.Fatal("expected non-nil policy observer")
	}
	if _, ok := obs.(policydecision.NoopObserver); !ok {
		t.Fatalf("expected policydecision.NoopObserver, got %T", obs)
	}
	if err := obs.OnPolicyDecision(context.Background(), policydecision.Record{}); err != nil {
		t.Fatalf("noop observer returned error: %v", err)
	}
	budgetSrc := b.RuntimeSnapshot().TimeoutBudgetSource()
	if budgetSrc == nil {
		t.Fatal("expected non-nil timeout budget source")
	}
	if got := budgetSrc.TimeoutFor(feature.StageIDPreRequest, "p"); got != 0 {
		t.Fatalf("default budget TimeoutFor = %v, want 0", got)
	}
}

func TestBuildRuntimeBundle_PolicyObserverWiresConfigured(t *testing.T) {
	t.Parallel()
	cfg := minimalPolicyBuildConfig()
	cap1 := &capturePolicyObserver{}
	cap2 := &capturePolicyObserver{}
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
		Policy:         runtimebundle.PolicyOptions{PolicyObservers: []policydecision.Observer{cap1, cap2}},
	})
	obs := b.RuntimeSnapshot().PolicyObserver()
	if _, ok := obs.(policydecision.ChainObserver); !ok {
		t.Fatalf("expected policydecision.ChainObserver, got %T", obs)
	}
	want := policydecision.Record{
		Stage:    feature.StageIDPreRequest,
		Provider: policydecision.ProviderRef{ID: "p1", Stage: feature.StageIDPreRequest},
		Outcome:  policydecision.OutcomeAllow,
	}
	if err := obs.OnPolicyDecision(context.Background(), want.Clone()); err != nil {
		t.Fatalf("OnPolicyDecision error: %v", err)
	}
	cap1Records := cap1.snapshot()
	cap2Records := cap2.snapshot()
	if len(cap1Records) != 1 || len(cap2Records) != 1 {
		t.Fatalf("both chain observers should receive the record: cap1=%d cap2=%d", len(cap1Records), len(cap2Records))
	}
	if cap1Records[0].Stage != want.Stage || cap1Records[0].Provider.ID != want.Provider.ID {
		t.Fatalf("cap1 record mismatch: got %+v want %+v", cap1Records[0], want)
	}
	if cap2Records[0].Stage != want.Stage || cap2Records[0].Provider.ID != want.Provider.ID {
		t.Fatalf("cap2 record mismatch: got %+v want %+v", cap2Records[0], want)
	}
}

func TestBuildRuntimeBundle_PolicyTimeoutBudgetWiresConfigured(t *testing.T) {
	t.Parallel()
	cfg := minimalPolicyBuildConfig()
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
		Policy:         runtimebundle.PolicyOptions{PolicyTimeoutBudgetSource: extensions.StaticTimeoutBudgetSource{Budget: 250 * time.Millisecond}},
	})
	src := b.RuntimeSnapshot().TimeoutBudgetSource()
	if _, ok := src.(extensions.StaticTimeoutBudgetSource); !ok {
		t.Fatalf("expected extensions.StaticTimeoutBudgetSource, got %T", src)
	}
	if got := src.TimeoutFor(feature.StageIDPreRequest, "p"); got != 250*time.Millisecond {
		t.Fatalf("TimeoutFor = %v, want 250ms", got)
	}
}

func TestBuildRuntimeBundle_PolicyDiagnosticsWiresConfigured(t *testing.T) {
	t.Parallel()
	cfg := minimalPolicyBuildConfig()
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
		Policy: runtimebundle.PolicyOptions{
			PolicyDiagnosticsEnabled:  true,
			PolicyTimeoutBudgetSource: extensions.StaticTimeoutBudgetSource{Budget: 250 * time.Millisecond},
		},
	})
	if b.Executor() == nil {
		t.Fatal("expected non-nil executor")
	}
	if !b.Executor().PolicyDiagnosticsEnabled {
		t.Fatal("PolicyDiagnosticsEnabled should be wired to runtime.Executor")
	}
}

func TestBuildRuntimeBundle_NoPolicyNoninterference(t *testing.T) {
	t.Parallel()
	cfg := minimalPolicyBuildConfig()
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
	})
	if b.Executor() == nil {
		t.Fatal("expected non-nil Executor")
	}
	if b.Executor().RuntimeSnapshot == nil {
		t.Fatal("expected non-nil Executor.RuntimeSnapshot")
	}
	if b.Executor().RuntimeSnapshot != b.RuntimeSnapshot() {
		t.Fatal("Executor.RuntimeSnapshot should match CandidateRuntime.RuntimeSnapshot")
	}
	obs := b.RuntimeSnapshot().PolicyObserver()
	if _, ok := obs.(policydecision.NoopObserver); !ok {
		t.Fatalf("expected noop observer without policy config, got %T", obs)
	}
	src := b.RuntimeSnapshot().TimeoutBudgetSource()
	if _, ok := src.(extensions.DefaultTimeoutBudgetSource); !ok {
		t.Fatalf("expected default zero-budget source, got %T", src)
	}
	if got := src.TimeoutFor(feature.StageIDPreRequest, "p"); got != 0 {
		t.Fatalf("default budget TimeoutFor = %v, want 0", got)
	}
}
