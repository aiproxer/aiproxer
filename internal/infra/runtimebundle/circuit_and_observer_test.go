package runtimebundle_test

import (
	"log/slog"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/policy"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
)

func TestBuild_circuitBreakerDisabledUsesEmptyHealth(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing: config.RoutingConfig{MaxAttempts: 3},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{PluginRegistry: pluginreg.NewRegistry()})
	if _, ok := b.Executor().CandidateHealth.(*policy.CircuitBreaker); ok {
		t.Fatal("expected no circuit breaker when disabled")
	}
}

func TestBuild_circuitBreakerEnabledWiresPolicy(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing: config.RoutingConfig{
			MaxAttempts: 3,
			Health: config.RoutingHealthConfig{
				CircuitBreaker: config.CircuitBreakerConfig{
					Enabled:          true,
					FailureThreshold: 3,
					OpenFor:          "5s",
				},
			},
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{PluginRegistry: pluginreg.NewRegistry()})
	if b.Executor().CandidateHealth == nil {
		t.Fatal("expected CandidateHealth when circuit breaker enabled")
	}
	if _, ok := b.Executor().CandidateHealth.(policy.RoutingAttemptOutcomeSink); !ok {
		t.Fatalf("want RoutingAttemptOutcomeSink (namespaced process health view), got %T", b.Executor().CandidateHealth)
	}
}

func TestBuild_routeObserverUsesSlogWhenLoggerSet(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{ID: "x", Enabled: false}}},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
	_ = slog.New(slog.DiscardHandler)
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{PluginRegistry: pluginreg.NewRegistry()})
	if b.Executor().RouteObserver == nil {
		t.Fatal("expected RouteObserver")
	}
}
