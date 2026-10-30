package runtimebundle_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"

	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
)

func TestBuild_authEventDispatcher_nonNil(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}}},
		Continuity: config.ContinuityConfig{InMemory: true},
	}
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
	})
	if runtimebundle.CandidateAuthEventDispatcher(b) == nil {
		t.Fatal("expected AuthEventDispatcher")
	}
}

func TestBuild_authEventDelivery_customRequiresAuthEventSink(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}}},
		Continuity: config.ContinuityConfig{InMemory: true},
		Auth:       config.AuthConfig{EventDelivery: "custom"},
	}
	_, _, err := processAndCandidateErr(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
	})
	if err == nil || !errors.Is(err, runtimebundle.ErrAuthEventSinkRequired) {
		t.Fatalf("want %v, got %v", runtimebundle.ErrAuthEventSinkRequired, err)
	}
}

type sliceSink struct {
	got []string
}

func (s *sliceSink) OnAuthDecision(context.Context, sdkauth.AuthDecisionEvent) error {
	s.got = append(s.got, "auth")
	return nil
}

func (s *sliceSink) OnSessionStart(context.Context, sdkauth.SessionStartEvent) error {
	s.got = append(s.got, "session")
	return nil
}

func TestBuild_authEventDelivery_customUsesInjectedSink(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}}},
		Continuity: config.ContinuityConfig{InMemory: true},
		Auth:       config.AuthConfig{EventDelivery: "custom"},
	}
	custom := &sliceSink{}
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
		Auth:           runtimebundle.AuthOptions{AuthEventSink: custom},
	})
	ctx := context.Background()
	if err := runtimebundle.CandidateAuthEventDispatcher(b).DispatchAuthDecision(ctx, sdkauth.AuthDecisionEvent{}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(custom.got) != 1 || custom.got[0] != "auth" {
		t.Fatalf("custom sink calls: %#v", custom.got)
	}
}

func TestBuild_authEventDelivery_defaultRejectsAuthEventSink(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}}},
		Continuity: config.ContinuityConfig{InMemory: true},
		// default / empty event_delivery
	}
	_, _, err := processAndCandidateErr(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
		Auth:           runtimebundle.AuthOptions{AuthEventSink: &sliceSink{}},
	})
	if err == nil || !errors.Is(err, runtimebundle.ErrAuthEventSinkDisallowed) {
		t.Fatalf("want %v, got %v", runtimebundle.ErrAuthEventSinkDisallowed, err)
	}
}

func TestBuild_authEventDelivery_disabledRejectsAuthEventSink(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}}},
		Continuity: config.ContinuityConfig{InMemory: true},
		Auth:       config.AuthConfig{EventDelivery: "disabled"},
	}
	_, _, err := processAndCandidateErr(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
		Auth:           runtimebundle.AuthOptions{AuthEventSink: &sliceSink{}},
	})
	if err == nil || !errors.Is(err, runtimebundle.ErrAuthEventSinkDisallowed) {
		t.Fatalf("want %v, got %v", runtimebundle.ErrAuthEventSinkDisallowed, err)
	}
}

func TestBuild_authEventFailurePolicy_failClosed(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}}},
		Continuity: config.ContinuityConfig{InMemory: true},
		Auth: config.AuthConfig{
			EventFailurePolicy: "fail_closed",
		},
	}
	_, b := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
	}) // default delivery uses internal slog sink — use custom + err sink to test fail_closed policy.
	errSink := &errSink{}
	cfg2 := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}}},
		Continuity: config.ContinuityConfig{InMemory: true},
		Auth: config.AuthConfig{
			EventDelivery:      "custom",
			EventFailurePolicy: "fail_closed",
		},
	}
	_, b2 := mustProcessAndCandidate(t, cfg2, &runtimebundle.BuildOptions{
		PluginRegistry: pluginreg.NewRegistry(),
		Auth:           runtimebundle.AuthOptions{AuthEventSink: errSink},
	})
	ctx := context.Background()
	if err := runtimebundle.CandidateAuthEventDispatcher(b2).DispatchAuthDecision(ctx, sdkauth.AuthDecisionEvent{}); err == nil {
		t.Fatal("expected fail_closed sink error")
	}
	// default path still succeeds with internal slog sink
	if err := runtimebundle.CandidateAuthEventDispatcher(b).DispatchAuthDecision(ctx, sdkauth.AuthDecisionEvent{}); err != nil {
		t.Fatalf("default slog path: %v", err)
	}
}

type errSink struct{}

func (errSink) OnAuthDecision(context.Context, sdkauth.AuthDecisionEvent) error {
	return errors.New("sink error")
}

func (errSink) OnSessionStart(context.Context, sdkauth.SessionStartEvent) error { return nil }
