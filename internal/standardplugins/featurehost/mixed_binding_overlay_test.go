package featurehost_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/accessmode"
	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	sdkfeaturehost "github.com/aiproxer/aiproxer/pkg/aipsdk/featurehost"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/reasoninghost"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguardhost"
)

// stubBackgroundAux satisfies both auxiliary.BackgroundClient and
// auxiliary.BackgroundPoller without performing work: reasoning composition
// only nil-checks these capabilities during CompileGeneration.
type stubBackgroundAux struct{}

func (stubBackgroundAux) SubmitCollect(context.Context, auxiliary.Request, auxiliary.SubmitOptions) (auxiliary.JobID, error) {
	return "", nil
}

func (stubBackgroundAux) Await(context.Context, auxiliary.JobID) (aipapi.Collected, error) {
	return aipapi.Collected{}, nil
}

func (stubBackgroundAux) Forget(auxiliary.JobID) {}

func (stubBackgroundAux) Poll(context.Context, auxiliary.JobID) (auxiliary.PollResult, error) {
	return auxiliary.PollResult{}, nil
}

func compressionEnabledReg(t *testing.T, egressRef string) aipsdk.Registration {
	t.Helper()
	return aipsdk.Registration{
		ID:          reasoningpreservation.ID,
		FactoryKind: reasoningpreservation.ID,
		Kind:        aipsdk.PluginKindFeature,
		Enabled:     true,
		Config: aipsdk.ConfigPayload{Node: mustYAMLNode(t, `
action: restore
on_ambiguous: log_skip
on_unrepresentable: reject
on_state_error: reject
state:
  ttl: 24h
  max_turns_per_session: 10
  max_reasoning_bytes_per_turn: 100000
  max_session_bytes: 1000000
compression:
  enabled: true
  mode: shadow
  route: test-route
  timeout: 5s
  max_input_tokens: 10000
  max_input_bytes: 100000
  max_output_tokens: 1000
  max_output_bytes: 100000
  max_surrogate_bytes: 50000
  min_source_bytes: 100
  min_saved_bytes: 50
  min_savings_ratio: 0.5
  max_pending_per_session: 10
  max_surrogate_bytes_per_session: 100000
  max_pending_total: 100
  max_surrogate_bytes_total: 1000000
  egress_policy_ref: `+egressRef+`
`)},
	}
}

func processReasoningBinding() sdkfeaturehost.Registration {
	policy := &recordingHostPolicy{
		decision: reasoninghost.EgressDecision{Action: reasoninghost.EgressAllow},
	}
	return (&reasoninghost.Binding{
		EgressPolicies:  map[string]reasoninghost.EgressPolicy{"policy-test": policy},
		MatcherResolver: stubSecretResolver{},
	}).Registration()
}

// TestCompileGeneration_ReasoningOnlySlicePreservesProcessSecretGuard is the
// F1 regression: a generation host slice carrying only a reasoning binding
// must not suppress the process-bound (HostEnv-defaulted) secret-guard
// binding. The env-derived secret must remain in the single-user catalog.
func TestCompileGeneration_ReasoningOnlySlicePreservesProcessSecretGuard(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := &sgHostMapEnv{vals: map[string]string{sgHostMinBytesVar: sgHostMinBytesValue}}

	rt, err := featurehost.NewProcess(ctx, featurehost.ProcessInput{
		Logger:  slog.Default(),
		HostEnv: env,
	})
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	if !rt.BoundSecretGuard().Present {
		t.Fatal("precondition: expected default env secret-guard binding to be present")
	}

	out, err := rt.CompileGeneration(ctx, featurehost.GenerationInput{
		Registrations: sgHostMinBytesRegs(t),
		HostRegistrations: []sdkfeaturehost.Registration{
			(&reasoninghost.Binding{}).Registration(),
		},
		AccessMode: accessmode.ModeSingleUser,
	})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}
	assertTwelveByteSecretFound(t, out)
}

// TestCompileGeneration_SecretGuardOnlySlicePreservesProcessReasoning is the
// F1 mirror: a generation host slice carrying only a secret-guard binding
// must not suppress process-bound reasoning options. The generation enables
// reasoning compression requiring the process-bound egress policy, so a
// suppression surfaces as a trusted-policy validation failure.
func TestCompileGeneration_SecretGuardOnlySlicePreservesProcessReasoning(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	rt, err := featurehost.NewProcess(ctx, featurehost.ProcessInput{
		Logger:            slog.Default(),
		HostRegistrations: []sdkfeaturehost.Registration{processReasoningBinding()},
	})
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })

	bg := stubBackgroundAux{}
	_, err = rt.CompileGeneration(ctx, featurehost.GenerationInput{
		Registrations:     []aipsdk.Registration{compressionEnabledReg(t, "policy-test")},
		BackgroundClient:  bg,
		BackgroundPoller:  bg,
		HostRegistrations: []sdkfeaturehost.Registration{(&secretguardhost.Binding{}).Registration()},
	})
	if err != nil {
		t.Fatalf("CompileGeneration with SG-only slice dropped process reasoning: %v", err)
	}

	// Control: without the process-bound reasoning binding the same
	// generation must fail, proving this test discriminates the F1 fix.
	bare, err := featurehost.NewProcess(ctx, featurehost.ProcessInput{Logger: slog.Default()})
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}
	t.Cleanup(func() { _ = bare.Close() })
	if _, err := bare.CompileGeneration(ctx, featurehost.GenerationInput{
		Registrations:    []aipsdk.Registration{compressionEnabledReg(t, "policy-test")},
		BackgroundClient: bg,
		BackgroundPoller: bg,
	}); err == nil {
		t.Fatal("control: expected failure without a process-bound egress policy; this test cannot detect F1 regressions")
	}
}

// TestCompileGeneration_ExplicitSecretGuardBindingWinsOverDefault locks the
// process default semantics: an explicit SG binding wins wholesale over the
// HostEnv-synthesized default (min 16 from the explicit binding drops the
// 12-byte secret that the default would keep under YAML min 8).
func TestCompileGeneration_ExplicitSecretGuardBindingWinsOverDefault(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := &sgHostMapEnv{vals: map[string]string{sgHostMinBytesVar: sgHostMinBytesValue}}

	explicit := &secretguardhost.Binding{
		Environment: env,
		SingleUser:  secretguardhost.SingleUserOptions{MinSecretBytes: 16},
	}
	rt, err := featurehost.NewProcess(ctx, featurehost.ProcessInput{
		Logger:            slog.Default(),
		HostRegistrations: []sdkfeaturehost.Registration{explicit.Registration()},
		HostEnv:           env,
	})
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })

	out, err := rt.CompileGeneration(ctx, featurehost.GenerationInput{
		Registrations: sgHostMinBytesRegs(t),
		AccessMode:    accessmode.ModeSingleUser,
	})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}
	assertMinBytesSixteenEffective(t, out)
}

// TestCompileGeneration_DuplicateSecretGuardAcrossMergedSlicesFails locks the
// fail-fast validation: a generation slice carrying two secret-guard bindings
// (as produced by merging Production+Testing registrations) still fails with
// a duplicate-binding error.
func TestCompileGeneration_DuplicateSecretGuardAcrossMergedSlicesFails(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	rt, err := featurehost.NewProcess(ctx, featurehost.ProcessInput{Logger: slog.Default()})
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })

	_, err = rt.CompileGeneration(ctx, featurehost.GenerationInput{
		Registrations: sgHostMinBytesRegs(t),
		HostRegistrations: []sdkfeaturehost.Registration{
			(&secretguardhost.Binding{}).Registration(),
			(&secretguardhost.Binding{}).Registration(),
		},
		AccessMode: accessmode.ModeSingleUser,
	})
	if err == nil {
		t.Fatal("expected duplicate secret-guard binding error, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate binding error, got: %v", err)
	}
}
