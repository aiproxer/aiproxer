package configreload_test

import (
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/configreload"
)

func collapseBase() *config.Config {
	return &config.Config{
		Server: config.ServerConfig{Address: "127.0.0.1:8080"},
		Access: config.AccessConfig{Mode: "single_user"},
		Auth:   config.AuthConfig{Handler: "none"},
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		Routing: config.RoutingConfig{
			DefaultRoute: "stub:default",
			MaxAttempts:  1,
		},
		ModelCatalog: config.ModelCatalogConfig{
			CachePath: "/tmp/catalog-cache",
		},
	}
}

func TestReloadabilityClassify_CollapseThresholdChange_RestartRequired(t *testing.T) {
	t.Parallel()
	active := collapseBase()
	candidate := collapseBase()
	candidate.Observability.Metrics.PromptCacheCollapse.MinPromptTokens = new(100)

	_, err := configreload.Classify(active, candidate)
	var rr *configreload.RestartRequiredError
	if !errors.As(err, &rr) {
		t.Fatalf("collapse threshold change must be restart_required, got %v", err)
	}
	if !containsPath(rr.RestartRequiredFields, "observability.metrics.prompt_cache_collapse") {
		t.Fatalf("want observability.metrics.prompt_cache_collapse blocked, got %v", rr.RestartRequiredFields)
	}
	assertSortedBoundedPaths(t, rr)
}

func TestReloadabilityClassify_CollapseExplicitDefaults_Noop(t *testing.T) {
	t.Parallel()
	active := collapseBase()
	candidate := collapseBase()
	candidate.Observability.Metrics.PromptCacheCollapse.MinPromptTokens = new(8000)
	candidate.Observability.Metrics.PromptCacheCollapse.HealthyRatio = new(0.5)
	candidate.Observability.Metrics.PromptCacheCollapse.ColdRatio = new(0.1)
	candidate.Observability.Metrics.PromptCacheCollapse.ConsecutiveCold = new(5)
	candidate.Observability.Metrics.PromptCacheCollapse.MaxTrackedKeys = new(1024)
	candidate.Observability.Metrics.PromptCacheCollapse.IdleExpiry = "2h"

	changes, err := configreload.Classify(active, candidate)
	if err != nil {
		t.Fatalf("spelling documented defaults must not block reload: %v", err)
	}
	if changes != nil {
		t.Fatalf("effective-noop must return nil changes, got %v", changes)
	}
}

func TestReloadabilityClassify_CollapseEquivalentDuration_Noop(t *testing.T) {
	t.Parallel()
	active := collapseBase()
	active.Observability.Metrics.PromptCacheCollapse.IdleExpiry = "2h"
	candidate := collapseBase()
	candidate.Observability.Metrics.PromptCacheCollapse.IdleExpiry = "120m"

	changes, err := configreload.Classify(active, candidate)
	if err != nil {
		t.Fatalf("equivalent durations must not block reload: %v", err)
	}
	if changes != nil {
		t.Fatalf("effective-noop must return nil changes, got %v", changes)
	}
}

func TestReloadabilityClassify_CollapseSeamFlagChange_RestartRequired(t *testing.T) {
	t.Parallel()
	active := collapseBase()
	candidate := collapseBase()
	candidate.Observability.Metrics.PromptCacheCollapse.SeamDigestsEnabled = true

	_, err := configreload.Classify(active, candidate)
	var rr *configreload.RestartRequiredError
	if !errors.As(err, &rr) {
		t.Fatalf("seam digest gate change must be restart_required, got %v", err)
	}
	if !containsPath(rr.RestartRequiredFields, "observability.metrics.prompt_cache_collapse") {
		t.Fatalf("want observability.metrics.prompt_cache_collapse blocked, got %v", rr.RestartRequiredFields)
	}
	assertSortedBoundedPaths(t, rr)
}

func TestReloadabilityClassify_CollapseExplicitDefaults_NoopWithSeamFlag(t *testing.T) {
	t.Parallel()
	active := collapseBase()
	candidate := collapseBase()
	candidate.Observability.Metrics.PromptCacheCollapse.MinPromptTokens = new(8000)
	candidate.Observability.Metrics.PromptCacheCollapse.HealthyRatio = new(0.5)
	candidate.Observability.Metrics.PromptCacheCollapse.ColdRatio = new(0.1)
	candidate.Observability.Metrics.PromptCacheCollapse.ConsecutiveCold = new(5)
	candidate.Observability.Metrics.PromptCacheCollapse.MaxTrackedKeys = new(1024)
	candidate.Observability.Metrics.PromptCacheCollapse.IdleExpiry = "2h"
	candidate.Observability.Metrics.PromptCacheCollapse.SeamDigestsEnabled = false

	changes, err := configreload.Classify(active, candidate)
	if err != nil {
		t.Fatalf("spelling documented defaults must not block reload: %v", err)
	}
	if changes != nil {
		t.Fatalf("effective-noop must return nil changes, got %v", changes)
	}
}
