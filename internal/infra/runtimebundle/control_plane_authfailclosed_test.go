package runtimebundle

import (
	"io"
	"log/slog"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

// TestControlPlaneRuntime_AuthFailClosed verifies that buildControlPlaneRuntime
// derives controlPlaneRuntime.authFailClosed from auth.event_failure_policy so
// the auth sink adapter can fail closed on required pre-work recording failures.
// Regression for the Bugbot finding where wrapAuthSink built AuthSinkAdapter
// without setting FailClosed from config.
func TestControlPlaneRuntime_AuthFailClosed(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		policy         string
		enabled        bool
		wantFailClosed bool
		wantNilRT      bool
	}{
		{
			name:           "fail_closed policy sets authFailClosed true",
			policy:         "fail_closed",
			enabled:        true,
			wantFailClosed: true,
		},
		{
			name:           "best_effort policy leaves authFailClosed false",
			policy:         "best_effort",
			enabled:        true,
			wantFailClosed: false,
		},
		{
			name:           "empty policy defaults to best_effort (false)",
			policy:         "",
			enabled:        true,
			wantFailClosed: false,
		},
		{
			name:      "control plane disabled returns nil runtime",
			policy:    "fail_closed",
			enabled:   false,
			wantNilRT: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{
				Routing:    config.RoutingConfig{MaxAttempts: 3},
				Plugins:    config.PluginsConfig{Backends: []config.PluginConfig{{ID: "openai-responses", Enabled: false}}},
				Continuity: config.ContinuityConfig{InMemory: true},
				ControlPlane: config.ControlPlaneConfig{
					Enabled:         tc.enabled,
					Store:           "memory",
					RecordingPolicy: "best_effort",
				},
				Auth: config.AuthConfig{EventFailurePolicy: tc.policy},
			}
			log := slog.New(slog.NewTextHandler(io.Discard, nil))
			rt, err := buildControlPlaneRuntime(controlPlaneBuildInput{Cfg: cfg, Log: log})
			if err != nil {
				t.Fatalf("buildControlPlaneRuntime: unexpected error: %v", err)
			}
			if tc.wantNilRT {
				if rt != nil {
					t.Fatalf("expected nil runtime when control plane disabled, got %+v", rt)
				}
				return
			}
			if rt == nil {
				t.Fatalf("expected non-nil runtime")
			}
			if rt.authFailClosed != tc.wantFailClosed {
				t.Fatalf("authFailClosed: got %v, want %v (policy=%q)", rt.authFailClosed, tc.wantFailClosed, tc.policy)
			}
		})
	}
}
