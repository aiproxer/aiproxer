package featurehost_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/state"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	sdkfeaturehost "github.com/aiproxer/aiproxer/pkg/aipsdk/featurehost"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguardhost"
)

type stubHostEnv struct {
	vals map[string]string
}

func (e stubHostEnv) Lookup(name string) (string, bool) {
	v, ok := e.vals[name]
	return v, ok
}

func (e stubHostEnv) Snapshot() []string {
	out := make([]string, 0, len(e.vals))
	for k, v := range e.vals {
		out = append(out, k+"="+v)
	}
	return out
}

// TestNewProcess_DefaultEnvBinding synthesizes the default env-derived
// secret-guard host binding exactly when no explicit binding is registered:
// explicit registrations win wholesale and are never duplicated, nil env
// synthesizes nothing.
func TestNewProcess_DefaultEnvBinding(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	newProc := func(t *testing.T, regs []sdkfeaturehost.Registration, env featurehost.HostEnvironment) *featurehost.Runtime {
		t.Helper()
		sched := newTestScheduler(t)
		t.Cleanup(func() { _ = sched.Close() })
		rt, err := featurehost.NewProcess(ctx, featurehost.ProcessInput{
			Logger:            slog.Default(),
			ExtensionState:    state.NewMem(nil),
			BackgroundAux:     sched,
			HostRegistrations: regs,
			HostEnv:           env,
		})
		if err != nil {
			t.Fatalf("NewProcess: %v", err)
		}
		t.Cleanup(func() { _ = rt.Close() })
		return rt
	}

	t.Run("nil env synthesizes nothing", func(t *testing.T) {
		t.Parallel()
		rt := newProc(t, nil, nil)
		if got := rt.BoundSecretGuard(); got.Present {
			t.Fatalf("BoundSecretGuard.Present = true, want false without env")
		}
	})

	t.Run("env without explicit binding synthesizes default", func(t *testing.T) {
		t.Parallel()
		env := stubHostEnv{vals: map[string]string{"AIP_TEST_DEFAULT": "v"}}
		rt := newProc(t, nil, env)
		bound := rt.BoundSecretGuard()
		if !bound.Present {
			t.Fatal("BoundSecretGuard.Present = false, want default binding")
		}
		if bound.Environment == nil {
			t.Fatal("BoundSecretGuard.Environment = nil, want default env")
		}
	})

	t.Run("explicit binding wins without duplication", func(t *testing.T) {
		t.Parallel()
		hostEnv := stubHostEnv{vals: map[string]string{"AIP_TEST_HOST": "v"}}
		explicit := (&secretguardhost.Binding{Environment: hostEnv}).Registration()
		defaultEnv := stubHostEnv{vals: map[string]string{"AIP_TEST_DEFAULT": "v"}}
		rt := newProc(t, []sdkfeaturehost.Registration{explicit}, defaultEnv)
		bound := rt.BoundSecretGuard()
		if !bound.Present {
			t.Fatal("BoundSecretGuard.Present = false, want explicit binding")
		}
		if bound.Environment == nil {
			t.Fatal("BoundSecretGuard.Environment = nil")
		}
		if _, ok := bound.Environment.(stubHostEnv); !ok {
			t.Fatalf("BoundSecretGuard.Environment = %T, want host-supplied env", bound.Environment)
		}
	})
}
