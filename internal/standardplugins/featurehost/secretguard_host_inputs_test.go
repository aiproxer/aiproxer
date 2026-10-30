package featurehost_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/accessmode"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkfeaturehost "github.com/aiproxer/aiproxer/pkg/aipsdk/featurehost"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguardhost"
)

const (
	sgHostMinBytesVar   = "AIP_TEST_SG_HOST_MIN"
	sgHostMinBytesValue = "0123456789ab" // 12 bytes: kept by min 8, dropped by min 16.
)

type sgHostMapEnv struct {
	vals map[string]string
}

func (e *sgHostMapEnv) Lookup(name string) (string, bool) {
	v, ok := e.vals[name]
	return v, ok
}

func (e *sgHostMapEnv) Snapshot() []string {
	out := make([]string, 0, len(e.vals))
	for k, v := range e.vals {
		out = append(out, k+"="+v)
	}
	return out
}

func sgHostMinBytesRegs(t *testing.T) []aipsdk.Registration {
	t.Helper()
	return []aipsdk.Registration{{
		Kind:        aipsdk.PluginKindFeature,
		ID:          "secrets-guard",
		FactoryKind: "secrets-guard",
		Enabled:     true,
		Config: aipsdk.ConfigPayload{Node: mustYAMLNode(t, `
action: redact
min_secret_bytes: 8
single_user:
  include_popular_env: false
  include_env: [AIP_TEST_SG_HOST_MIN]
`)},
	}}
}

// minSecretBytesOnlyBinding returns the exact dropped shape from the finding:
// a secret-guard binding carrying only SingleUserOptions{MinSecretBytes:16}.
func minSecretBytesOnlyBinding(env *sgHostMapEnv) *secretguardhost.Binding {
	return &secretguardhost.Binding{
		Environment: env,
		SingleUser:  secretguardhost.SingleUserOptions{MinSecretBytes: 16},
	}
}

// secretGuardExecFromOutput extracts the composed secret-guard execution plane
// from the ordinary frozen surface. The dedicated GenerationOutput fields are
// gone by design; planes are the only channel.
func secretGuardExecFromOutput(t *testing.T, out featurehost.GenerationOutput) *secretguard.ExecutionConfig {
	t.Helper()
	execCfg := aipfeature.Get(out.Planes, aipfeature.PlaneSecretGuardExecution)
	if execCfg == nil || execCfg.IsZero() {
		t.Fatalf("expected composed secret-guard execution plane, got %#v", execCfg)
	}
	return execCfg
}

// assertMinBytesSixteenEffective asserts the composed generation catalog reflects
// an effective MinSecretBytes of 16: the 12-byte secret must be absent from both
// the inventory entry count and the resolved matcher behavior.
func assertMinBytesSixteenEffective(t *testing.T, out featurehost.GenerationOutput) {
	t.Helper()
	execCfg := secretGuardExecFromOutput(t, out)
	if got := execCfg.CatalogEntryCount; got != 0 {
		t.Fatalf("effective MinSecretBytes: catalog entry count=%d, want 0 (12-byte secret must be dropped by min 16)", got)
	}
	if execCfg.MatcherResolver == nil {
		t.Fatal("expected non-nil MatcherResolver")
	}
	matcher, err := execCfg.MatcherResolver.Resolve(context.Background())
	if err != nil {
		t.Fatalf("matcher resolve: %v", err)
	}
	if matcher == nil {
		t.Fatal("expected non-nil matcher")
	}
	findings, err := matcher.ScanString(context.Background(), "x="+sgHostMinBytesValue)
	if err != nil {
		t.Fatalf("matcher scan: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("effective MinSecretBytes: scan found %d findings, want 0 (12-byte secret must be dropped by min 16)", len(findings))
	}
}

func assertTwelveByteSecretFound(t *testing.T, out featurehost.GenerationOutput) {
	t.Helper()
	execCfg := secretGuardExecFromOutput(t, out)
	if execCfg.CatalogEntryCount < 1 {
		t.Fatalf("control: expected catalog entries for 12-byte secret under min 8, got %d", execCfg.CatalogEntryCount)
	}
	matcher, err := execCfg.MatcherResolver.Resolve(context.Background())
	if err != nil || matcher == nil {
		t.Fatalf("control: matcher resolve: m=%v err=%v", matcher, err)
	}
	findings, err := matcher.ScanString(context.Background(), "x="+sgHostMinBytesValue)
	if err != nil {
		t.Fatalf("control: matcher scan: %v", err)
	}
	if len(findings) == 0 {
		t.Fatal("control: expected scan finding for 12-byte secret under min 8")
	}
}

// TestCompileGeneration_ProcessBoundSecretGuardMinBytesEffective covers the
// process-bound host input path: a NewProcess secret-guard binding carrying only
// MinSecretBytes must reach generation composition instead of being dropped.
func TestCompileGeneration_ProcessBoundSecretGuardMinBytesEffective(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := &sgHostMapEnv{vals: map[string]string{sgHostMinBytesVar: sgHostMinBytesValue}}

	rt, err := featurehost.NewProcess(ctx, featurehost.ProcessInput{
		Logger: slog.Default(),
		HostRegistrations: []sdkfeaturehost.Registration{
			minSecretBytesOnlyBinding(env).Registration(),
		},
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

// TestCompileGeneration_ProcessBoundSecretGuardMinBytesControl proves the test
// setup is sensitive: an env-only process binding leaves YAML min 8 effective,
// so the 12-byte secret must be found.
func TestCompileGeneration_ProcessBoundSecretGuardMinBytesControl(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := &sgHostMapEnv{vals: map[string]string{sgHostMinBytesVar: sgHostMinBytesValue}}

	rt, err := featurehost.NewProcess(ctx, featurehost.ProcessInput{
		Logger: slog.Default(),
		HostRegistrations: []sdkfeaturehost.Registration{
			(&secretguardhost.Binding{Environment: env}).Registration(),
		},
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
	assertTwelveByteSecretFound(t, out)
}

// TestCompileGeneration_GenerationBoundSecretGuardMinBytesEffective covers the
// generation-bound host input path: a per-generation secret-guard binding carrying
// only MinSecretBytes must reach composition instead of being dropped.
func TestCompileGeneration_GenerationBoundSecretGuardMinBytesEffective(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := &sgHostMapEnv{vals: map[string]string{sgHostMinBytesVar: sgHostMinBytesValue}}

	rt, err := featurehost.NewProcess(ctx, featurehost.ProcessInput{
		Logger: slog.Default(),
	})
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })

	out, err := rt.CompileGeneration(ctx, featurehost.GenerationInput{
		Registrations: sgHostMinBytesRegs(t),
		HostRegistrations: []sdkfeaturehost.Registration{
			minSecretBytesOnlyBinding(env).Registration(),
		},
		AccessMode: accessmode.ModeSingleUser,
	})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}
	assertMinBytesSixteenEffective(t, out)
}

// TestCompileGeneration_GenerationBoundSecretGuardMinBytesControl proves the
// test setup is sensitive: legacy SecretEnv input without a host binding leaves
// YAML min 8 effective, so the 12-byte secret must be found.
func TestCompileGeneration_GenerationBoundSecretGuardMinBytesControl(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	env := &sgHostMapEnv{vals: map[string]string{sgHostMinBytesVar: sgHostMinBytesValue}}

	rt, err := featurehost.NewProcess(ctx, featurehost.ProcessInput{
		Logger: slog.Default(),
	})
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })

	out, err := rt.CompileGeneration(ctx, featurehost.GenerationInput{
		Registrations: sgHostMinBytesRegs(t),
		AccessMode:    accessmode.ModeSingleUser,
		SecretEnv:     env,
	})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}
	assertTwelveByteSecretFound(t, out)
}
