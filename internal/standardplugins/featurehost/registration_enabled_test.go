package featurehost_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/state"
	"github.com/aiproxer/aiproxer/internal/plugins/features/interleavedthinking"
	"github.com/aiproxer/aiproxer/internal/plugins/features/keepwarm"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/prometheus/client_golang/prometheus"
)

func mustFeatureTestRuntime(t *testing.T) *featurehost.Runtime {
	t.Helper()
	ctx := context.Background()
	sched := newTestScheduler(t)
	t.Cleanup(func() { _ = sched.Close() })
	rt, err := featurehost.NewProcess(ctx, featurehost.ProcessInput{
		Logger:          slog.Default(),
		ExtensionState:  state.NewMem(nil),
		BackgroundAux:   sched,
		MetricsRegistry: prometheus.NewRegistry(),
	})
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	return rt
}

func featureRegistration(t *testing.T, id string, enabled bool, configYAML string) aipsdk.Registration {
	t.Helper()
	reg := aipsdk.Registration{
		ID:          id,
		FactoryKind: id,
		Kind:        aipsdk.PluginKindFeature,
		Enabled:     enabled,
	}
	if configYAML != "" {
		reg.Config = aipsdk.ConfigPayload{Node: mustYAMLNode(t, configYAML)}
	}
	return reg
}

// TestCompileGeneration_InterleavedRegistrationEnabled pins outer
// Registration.Enabled as authoritative: absent entries fall back to legacy
// defaults, outer-enabled entries construct a processor, and outer-disabled
// entries (even with inner enabled:true) construct nothing.
func TestCompileGeneration_InterleavedRegistrationEnabled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	cases := []struct {
		name          string
		registrations []aipsdk.Registration
		wantProcessor bool
	}{
		{
			name:          "absent entry disables processor without legacy config",
			registrations: nil,
			wantProcessor: false,
		},
		{
			name: "outer enabled with inner enabled constructs processor",
			registrations: []aipsdk.Registration{
				featureRegistration(t, interleavedthinking.ID, true, "enabled: true\n"),
			},
			wantProcessor: true,
		},
		{
			name: "outer disabled with empty config constructs nothing",
			registrations: []aipsdk.Registration{
				featureRegistration(t, interleavedthinking.ID, false, ""),
			},
			wantProcessor: false,
		},
		{
			name: "outer disabled with inner enabled constructs nothing",
			registrations: []aipsdk.Registration{
				featureRegistration(t, interleavedthinking.ID, false, "enabled: true\n"),
			},
			wantProcessor: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rt := mustFeatureTestRuntime(t)
			out, err := rt.CompileGeneration(ctx, featurehost.GenerationInput{
				Registrations: tc.registrations,
			})
			if err != nil {
				t.Fatalf("CompileGeneration: %v", err)
			}
			if got := out.CorePorts.InterleavedProcessor != nil; got != tc.wantProcessor {
				t.Fatalf("InterleavedProcessor present=%v want %v", got, tc.wantProcessor)
			}
		})
	}
}

// TestCompileGeneration_KeepwarmRegistrationEnabled pins outer
// Registration.Enabled as authoritative: absent entries apply decoder
// defaults (enabled), outer-enabled entries construct a manager, and
// outer-disabled entries (even with inner enabled:true or decoder defaults)
// construct nothing.
func TestCompileGeneration_KeepwarmRegistrationEnabled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	cases := []struct {
		name          string
		registrations []aipsdk.Registration
		wantManager   bool
	}{
		{
			name:          "absent entry applies defaults",
			registrations: nil,
			wantManager:   true,
		},
		{
			name: "outer enabled with inner enabled constructs manager",
			registrations: []aipsdk.Registration{
				featureRegistration(t, keepwarm.ID, true, "enabled: true\n"),
			},
			wantManager: true,
		},
		{
			name: "outer disabled with empty config constructs nothing",
			registrations: []aipsdk.Registration{
				featureRegistration(t, keepwarm.ID, false, ""),
			},
			wantManager: false,
		},
		{
			name: "outer disabled with inner enabled constructs nothing",
			registrations: []aipsdk.Registration{
				featureRegistration(t, keepwarm.ID, false, "enabled: true\n"),
			},
			wantManager: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rt := mustFeatureTestRuntime(t)
			out, err := rt.CompileGeneration(ctx, featurehost.GenerationInput{
				Registrations: tc.registrations,
			})
			if err != nil {
				t.Fatalf("CompileGeneration: %v", err)
			}
			if got := out.HostProjections.MetricsSwap != nil; got != tc.wantManager {
				t.Fatalf("MetricsSwap present=%v want %v", got, tc.wantManager)
			}
			if got := out.CorePorts.PromptCacheMaintenance != nil; got != tc.wantManager {
				t.Fatalf("PromptCacheMaintenance present=%v want %v", got, tc.wantManager)
			}
		})
	}
}

// TestCompileGeneration_InterleavedDisabledRegistrationSuppressesLegacyFallback
// pins that an outer-disabled canonical entry suppresses the legacy
// config.interleaved fallback as well: even with legacy enabled, no processor
// is built and no legacy/canonical conflict is reported for the disabled entry.
func TestCompileGeneration_InterleavedDisabledRegistrationSuppressesLegacyFallback(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	cases := []struct {
		name          string
		registrations []aipsdk.Registration
	}{
		{
			name: "outer disabled with empty config suppresses enabled legacy config",
			registrations: []aipsdk.Registration{
				featureRegistration(t, interleavedthinking.ID, false, ""),
			},
		},
		{
			name: "outer disabled with inner enabled suppresses enabled legacy config",
			registrations: []aipsdk.Registration{
				featureRegistration(t, interleavedthinking.ID, false, "enabled: true\n"),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rt := mustFeatureTestRuntime(t)
			out, err := rt.CompileGeneration(ctx, featurehost.GenerationInput{
				Registrations:     tc.registrations,
				ConfigInterleaved: config.InterleavedConfig{Enabled: true},
			})
			if err != nil {
				t.Fatalf("CompileGeneration: %v", err)
			}
			if out.CorePorts.InterleavedProcessor != nil {
				t.Fatal("InterleavedProcessor present with outer-disabled registration and enabled legacy config, want nil")
			}
		})
	}
}
