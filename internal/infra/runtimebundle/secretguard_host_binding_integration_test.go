package runtimebundle_test

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/featurehost"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguardhost"
)

type stubSecretGuardHostEnv struct {
	vals map[string]string
}

func (e *stubSecretGuardHostEnv) Lookup(name string) (string, bool) {
	v, ok := e.vals[name]
	return v, ok
}

func (e *stubSecretGuardHostEnv) Snapshot() []string {
	out := make([]string, 0, len(e.vals))
	for k, v := range e.vals {
		out = append(out, k+"="+v)
	}
	return out
}

// TestBuildHost_WithSecretGuardHostBinding_NoDuplicateAndHostOptionsEffective is the
// end-to-end regression for the duplicate secret_guard host binding rejection:
// BuildHost supplies a non-nil process environment, so buildProcessServicesOp must not
// append a second default env-derived secret-guard binding when Production already
// carries a host-supplied one. The surviving binding must be the host's own.
func TestBuildHost_WithSecretGuardHostBinding_NoDuplicateAndHostOptionsEffective(t *testing.T) {
	t.Parallel()

	cfgPath := runtimebundle.MaterializeExampleConfigForTest(t, filepath.Join("..", "..", "..", "config", "examples", "dogfood-local-stub.yaml"))

	hostEnv := &stubSecretGuardHostEnv{vals: map[string]string{
		"AIP_TEST_SG_HOST": "host-supplied-secret-value",
	}}
	binding := &secretguardhost.Binding{
		Environment: hostEnv,
		SingleUser: secretguardhost.SingleUserOptions{
			IncludeEnv:     []string{"AIP_TEST_SG_HOST"},
			MinSecretBytes: 16,
			Matcher: secretguardhost.MatcherOptions{
				PreserveKnownPrefixes: false,
				MaskByte:              '#',
			},
			MatcherConfigured: true,
		},
	}

	host, err := runtimebundle.BuildHost(context.Background(), runtimebundle.BuildHostInput{
		ConfigPath: cfgPath,
		Mandatory:  aipsdk.StandardDistributionRequirements(),
		LogWriter:  io.Discard,
		Production: runtimebundle.ProductionOptions{
			FeatureHostRegistrations: []featurehost.Registration{
				binding.Registration(),
			},
		},
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		if strings.Contains(err.Error(), "duplicate host binding ID") {
			t.Fatalf("BuildHost rejected host-supplied secret-guard binding as duplicate: %v", err)
		}
		t.Fatalf("BuildHost with secret-guard host binding failed: %v", err)
	}
	t.Cleanup(func() { _ = host.Close(context.Background()) })

	bound := runtimebundle.HostProcess(host).StandardFeatures.BoundSecretGuard()
	if bound.Environment != hostEnv {
		t.Fatalf("bound secret-guard environment must be the host-supplied env %p, got %p", hostEnv, bound.Environment)
	}
	if bound.Inputs.SingleUser.MinSecretBytes != 16 {
		t.Fatalf("bound MinSecretBytes: got %d, want 16 (host values must survive)", bound.Inputs.SingleUser.MinSecretBytes)
	}
	if len(bound.Inputs.SingleUser.IncludeEnv) != 1 || bound.Inputs.SingleUser.IncludeEnv[0] != "AIP_TEST_SG_HOST" {
		t.Fatalf("bound IncludeEnv: got %v, want [AIP_TEST_SG_HOST]", bound.Inputs.SingleUser.IncludeEnv)
	}
	if !bound.Inputs.SingleUser.MatcherConfigured || bound.Inputs.SingleUser.Matcher.MaskByte != '#' {
		t.Fatalf("bound matcher override lost: got %+v", bound.Inputs.SingleUser.Matcher)
	}
}

// TestBuildHost_DefaultSecretGuardBinding proves the host entry point still
// works end-to-end with no secretguardhost knowledge in generic runtimebundle:
// without an explicit registration, featurehost synthesizes exactly one
// default env-derived binding from the generic process environment.
func TestBuildHost_DefaultSecretGuardBinding(t *testing.T) {
	t.Parallel()

	cfgPath := runtimebundle.MaterializeExampleConfigForTest(t, filepath.Join("..", "..", "..", "config", "examples", "dogfood-local-stub.yaml"))

	host, err := runtimebundle.BuildHost(context.Background(), runtimebundle.BuildHostInput{
		ConfigPath:      cfgPath,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("BuildHost with default env binding failed: %v", err)
	}
	t.Cleanup(func() { _ = host.Close(context.Background()) })

	bound := runtimebundle.HostProcess(host).StandardFeatures.BoundSecretGuard()
	if !bound.Present {
		t.Fatal("bound secret-guard binding must be present via default env synthesis")
	}
	if bound.Environment == nil {
		t.Fatal("bound secret-guard environment must be non-nil via default env synthesis")
	}
}
