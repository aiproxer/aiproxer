package runtimebundle_test

import (
	"context"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
)

func TestBuild_nilPluginRegistry(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
	_, _, err := processAndCandidateErr(t, cfg, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "nil PluginRegistry") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuild_nilPluginRegistryInOptions(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
	_, _, err := processAndCandidateErr(t, cfg, &runtimebundle.BuildOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "nil PluginRegistry") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuild_nilLogger(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
	_, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  nil,
		Opts: &runtimebundle.BuildOptions{PluginRegistry: pluginreg.NewRegistry()},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "nil logger") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuild_usesProvidedRegistryOnly(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if b.PluginRegistry() != reg {
		t.Fatalf("PluginRegistry pointer: got %p want %p", b.PluginRegistry(), reg)
	}
}
