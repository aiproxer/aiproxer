package runtimebundle

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/accessmode"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	"gopkg.in/yaml.v3"
)

// auditTestRuntime composes secret-guard audit through the featurehost facade
// with an explicit per-generation observer, reading the plane back purely via
// plane access (the same channel generic code uses).
func auditTestRuntime(t *testing.T, regs []aipsdk.Registration, observer secretguard.Observer) *secretGuardTestRuntime {
	t.Helper()
	fh, err := featurehost.NewProcess(context.Background(), featurehost.ProcessInput{Logger: slog.Default()})
	if err != nil {
		t.Fatal(err)
	}
	out, err := fh.CompileGeneration(context.Background(), featurehost.GenerationInput{
		Registrations:    regs,
		AccessMode:       accessmode.ModeSingleUser,
		DecisionObserver: observer,
	})
	if err != nil {
		t.Fatal(err)
	}
	plane, inv := secretGuardFromPlanes(out.Planes)
	return &secretGuardTestRuntime{Plane: plane, Inventory: inv}
}

func TestBindSecretGuardAudit_failClosedChainsObserverErrors(t *testing.T) {
	t.Parallel()
	boom := errors.New("sink down")
	var n int
	failing := secretguard.ObserverFunc(func(context.Context, secretguard.DecisionEvent) error {
		n++
		return boom
	})
	second := secretguard.ObserverFunc(func(context.Context, secretguard.DecisionEvent) error {
		n++
		return nil
	})
	var node yaml.Node
	if err := yaml.Unmarshal([]byte("action: block\n"), &node); err != nil {
		t.Fatal(err)
	}
	regs := []aipsdk.Registration{{
		Kind:        aipsdk.PluginKindFeature,
		ID:          "secrets-guard",
		FactoryKind: "secrets-guard",
		Enabled:     true,
		Config:      aipsdk.ConfigPayload{Node: node},
	}}
	rt := auditTestRuntime(t, regs, secretguard.ChainObservers(secretguard.AuditFailClosed, failing, second))
	err := rt.Plane.DecisionObserver.OnSecretDecision(t.Context(), secretguard.DecisionEvent{})
	if !errors.Is(err, boom) {
		t.Fatalf("err=%v want sink down", err)
	}
	if n != 1 {
		t.Fatalf("observers invoked=%d want 1", n)
	}
}

func TestBindSecretGuardAudit_bestEffortFromDecodedConfig(t *testing.T) {
	t.Parallel()
	var n int
	failing := secretguard.ObserverFunc(func(context.Context, secretguard.DecisionEvent) error {
		n++
		return errors.New("ignored")
	})
	second := secretguard.ObserverFunc(func(context.Context, secretguard.DecisionEvent) error {
		n++
		return nil
	})
	var node yaml.Node
	if err := yaml.Unmarshal([]byte("action: block\naudit_failure_policy: best_effort\n"), &node); err != nil {
		t.Fatal(err)
	}
	regs := []aipsdk.Registration{{
		Kind:        aipsdk.PluginKindFeature,
		ID:          "secrets-guard",
		FactoryKind: "secrets-guard",
		Enabled:     true,
		Config:      aipsdk.ConfigPayload{Node: node},
	}}
	rt := auditTestRuntime(t, regs, secretguard.ChainObservers(secretguard.AuditBestEffort, failing, second))
	if rt.Plane.AuditFailurePolicy != secretguard.AuditBestEffort {
		t.Fatalf("policy=%q want best_effort", rt.Plane.AuditFailurePolicy)
	}
	if err := rt.Plane.DecisionObserver.OnSecretDecision(t.Context(), secretguard.DecisionEvent{}); err != nil {
		t.Fatalf("best_effort must swallow: %v", err)
	}
	if n != 2 {
		t.Fatalf("observers invoked=%d want 2", n)
	}
}

func TestBindSecretGuardAudit_disabledSkipsObserver(t *testing.T) {
	t.Parallel()
	opts := &BuildOptions{}
	rt, err := bindSecretGuardAudit(&config.Config{}, opts, nil, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if rt == nil {
		t.Fatal("disabled secrets-guard must still return runtime snapshot")
	}
	if rt.Plane.DecisionObserver != nil {
		t.Fatal("disabled secrets-guard must not wire audit observer")
	}
}
