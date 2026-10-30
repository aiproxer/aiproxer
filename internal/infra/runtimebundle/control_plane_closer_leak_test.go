package runtimebundle

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
)

// closeObservableStore wraps fakeRetentionStore with an observable Close so leak
// tests can assert the control-plane store handle is released on compile failure.
// It satisfies controlplane.Store via the promoted fakeRetentionStore methods
// and interface{ Close() error } via Close, so buildControlPlaneStore returns
// Close as the closer when this is injected through BuildOptions.
type closeObservableStore struct {
	*fakeRetentionStore
	closed atomic.Bool
}

func (s *closeObservableStore) Close() error {
	s.closed.Store(true)
	return nil
}

// compileCandidateExpectFail runs NewProcessServices + CompileCandidate and
// expects failure. ProcessServices is closed immediately (before return) so
// process-owned closers — including the control-plane store — are disposed
// before the caller asserts. Generation cleanup is not deferred past the assert.
func compileCandidateExpectFail(t *testing.T, cfg *config.Config, opts *BuildOptions) error {
	t.Helper()
	ps, err := NewProcessServices(context.Background(), ProcessServicesInput{
		Cfg:  cfg,
		Log:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Opts: opts,
		Tracing: ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		// Process construction already disposed registered closers.
		return err
	}
	_, err = compileCandidate(context.Background(), GenerationCompileInput{
		Process: ps,
		Bus:     hooks.New(hooks.Config{}),
	})
	// Immediate process close — do not defer past the caller's assertion.
	_ = ps.Close()
	if err == nil {
		t.Fatal("expected CompileCandidate to fail")
	}
	return err
}

// TestBuild_ControlPlaneCloserDisposedOnEarlyFailure proves the control-plane
// store closer is registered immediately after the store opens and is disposed
// when a later startup step fails (auth event delivery). Previously the closer
// was appended only after buildSecureSessionRuntime, so any failure between
// buildControlPlaneRuntime and that point leaked the sqlite/postgres handle.
//
// Not parallel: uses a BuildOptions injection (no package-level state is mutated,
// but the test is self-contained and need not run alongside parallel Build tests).
func TestBuild_ControlPlaneCloserDisposedOnEarlyFailure(t *testing.T) {
	store := &closeObservableStore{fakeRetentionStore: &fakeRetentionStore{}}
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}}},
		Continuity: config.ContinuityConfig{InMemory: true},
		ControlPlane: config.ControlPlaneConfig{
			Enabled:         true,
			Store:           "memory",
			RecordingPolicy: "best_effort",
		},
		Auth: config.AuthConfig{EventDelivery: "bogus"}, // forces buildAuthEventDispatcher to fail after the store opens
	}
	err := compileCandidateExpectFail(t, cfg, &BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
		Testing: TestingOptions{
			ControlPlaneStoreOverride: store,
		},
	})
	if err == nil {
		t.Fatal("expected failure on invalid auth.event_delivery")
	}
	if !store.closed.Load() {
		t.Fatal("control-plane store closer must be disposed when candidate compile fails after opening the store")
	}
}

// TestBuild_ControlPlaneCloserDisposedOnStreamRecoveryFailure covers a later
// error path: by the time streamRecoveryConfigFromConfig runs, closers already
// holds the control-plane store plus model-registry, continuity, and secure-
// session handles. Previously this path returned without disposing closers,
// leaking all of them.
func TestBuild_ControlPlaneCloserDisposedOnStreamRecoveryFailure(t *testing.T) {
	store := &closeObservableStore{fakeRetentionStore: &fakeRetentionStore{}}
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}}},
		Continuity: config.ContinuityConfig{InMemory: true},
		ControlPlane: config.ControlPlaneConfig{
			Enabled:         true,
			Store:           "memory",
			RecordingPolicy: "best_effort",
		},
		StreamRecovery: config.StreamRecoveryConfig{
			AutoResume: config.AutoResumeConfig{IdleTimeout: "not-a-duration"},
		},
	}
	err := compileCandidateExpectFail(t, cfg, &BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
		Testing: TestingOptions{
			ControlPlaneStoreOverride: store,
		},
	})
	if err == nil {
		t.Fatal("expected failure on invalid stream_recovery idle_timeout")
	}
	if !store.closed.Load() {
		t.Fatal("control-plane store closer must be disposed when candidate compile fails at stream recovery config")
	}
}

// TestBuild_ControlPlaneCloserDisposedOnCompileFailure locks disposal when
// candidate compile fails after the control-plane store closer is registered.
func TestBuild_ControlPlaneCloserDisposedOnCompileFailure(t *testing.T) {
	store := &closeObservableStore{fakeRetentionStore: &fakeRetentionStore{}}
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}}},
		Continuity: config.ContinuityConfig{InMemory: true},
		ControlPlane: config.ControlPlaneConfig{
			Enabled:         true,
			Store:           "memory",
			RecordingPolicy: "best_effort",
		},
		Metering: config.MeteringConfig{
			Enabled: true,
			Journal: config.MeteringJournalConfig{Store: "not-a-store"},
		},
	}
	err := compileCandidateExpectFail(t, cfg, &BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
		Testing: TestingOptions{
			ControlPlaneStoreOverride: store,
		},
	})
	if err == nil {
		t.Fatal("expected failure on the invalid metering journal store")
	}
	if !store.closed.Load() {
		t.Fatal("control-plane store closer must be disposed when candidate compile fails after control-plane registration")
	}
}
