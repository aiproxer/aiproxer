package runtimebundle_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
)

func controlPlaneBuildConfig() *config.Config {
	return &config.Config{
		Routing: config.RoutingConfig{MaxAttempts: 3},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}},
		},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
}

func buildControlPlaneBundle(t *testing.T, cfg *config.Config) *runtimebundle.CandidateHTTPCompile {
	t.Helper()
	_, built := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
	}) // Dispose every Build-owned resource in reverse registration order so a
	// t.Fatalf in any caller still releases handles (e.g. sqlite file locks
	// that would block t.TempDir cleanup on Windows). Matches disposeClosers
	// in build.go.
	return built
}

func TestBuild_ControlPlaneDisabled_DefaultNoHandles(t *testing.T) {
	t.Parallel()
	cfg := controlPlaneBuildConfig()
	cfg.ControlPlane.Enabled = false
	built := buildControlPlaneBundle(t, cfg)
	if runtimebundle.CandidateControlPlaneQueries(built) != nil {
		t.Fatalf("disabled: expected nil ControlPlaneQueries")
	}
	if runtimebundle.CandidateControlPlaneStatus(built) != nil {
		t.Fatalf("disabled: expected nil ControlPlaneStatus")
	}
	if runtimebundle.CandidateControlPlaneRetention(built) != nil {
		t.Fatalf("disabled: expected nil ControlPlaneRetention")
	}
}

func TestBuild_ControlPlaneMemory_WiresStatusNotQueries(t *testing.T) {
	t.Parallel()
	cfg := controlPlaneBuildConfig()
	cfg.ControlPlane.Enabled = true
	cfg.ControlPlane.Store = "memory"
	cfg.ControlPlane.RecordingPolicy = "best_effort"
	built := buildControlPlaneBundle(t, cfg)
	if runtimebundle.CandidateControlPlaneStatus(built) == nil {
		t.Fatalf("memory: expected ControlPlaneStatus")
	}
	if runtimebundle.CandidateControlPlaneQueries(built) != nil {
		t.Fatalf("memory: query disabled, expected nil ControlPlaneQueries")
	}
}

func TestBuild_ControlPlaneQueryEnabled_WiresQueries(t *testing.T) {
	t.Parallel()
	cfg := controlPlaneBuildConfig()
	cfg.ControlPlane.Enabled = true
	cfg.ControlPlane.Store = "memory"
	cfg.ControlPlane.Query.Enabled = true
	cfg.ControlPlane.Query.PathPrefix = "/cp"
	cfg.Diagnostics.SharedSecret = "test-secret-1234"
	built := buildControlPlaneBundle(t, cfg)
	if runtimebundle.CandidateControlPlaneQueries(built) == nil {
		t.Fatalf("query: expected ControlPlaneQueries")
	}
	if runtimebundle.CandidateControlPlaneStatus(built) == nil {
		t.Fatalf("query: expected ControlPlaneStatus")
	}
}

func TestBuild_ControlPlaneSqlite_WiresStatusAndCloser(t *testing.T) {
	t.Parallel()
	cfg := controlPlaneBuildConfig()
	cfg.ControlPlane.Enabled = true
	cfg.ControlPlane.Store = "sqlite"
	cfg.ControlPlane.SQLitePath = t.TempDir() + "/cp.sqlite"
	built := buildControlPlaneBundle(t, cfg)
	if runtimebundle.CandidateControlPlaneStatus(built) == nil {
		t.Fatalf("sqlite: expected ControlPlaneStatus")
	}
	// closers disposed via buildControlPlaneBundle t.Cleanup
}
