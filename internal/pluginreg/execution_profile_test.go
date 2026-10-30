package pluginreg_test

import (
	"net/http"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"gopkg.in/yaml.v3"
)

func TestRegistry_ExecutionProfile_DefaultsAndRegistration(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()

	dummyFactory := func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{}, nil
	}
	dummyLifecycle := func(string, yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
		return pluginreg.BackendBuildResult{}, nil
	}

	// 1. Default RegisterBackend sets empty (effective unknown)
	if err := reg.RegisterBackend("be-default", dummyFactory); err != nil {
		t.Fatalf("RegisterBackend: %v", err)
	}
	prof, ok := reg.BackendExecutionProfile("be-default")
	if !ok {
		t.Fatal("expected be-default to have profile")
	}
	if prof.EffectiveClass() != aipsdk.BackendExecutionUnknown {
		t.Fatalf("expected effective unknown, got %v", prof.EffectiveClass())
	}

	// 2. RegisterBackendWithProfiles sets explicit inference
	if err := reg.RegisterBackendWithProfiles(
		"be-inf",
		dummyFactory,
		pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialStatic},
		aipsdk.BackendExecutionProfile{Class: aipsdk.BackendExecutionInference},
	); err != nil {
		t.Fatalf("RegisterBackendWithProfiles: %v", err)
	}
	prof, ok = reg.BackendExecutionProfile("be-inf")
	if !ok || prof.Class != aipsdk.BackendExecutionInference {
		t.Fatalf("expected inference, got %+v (ok=%v)", prof, ok)
	}

	// 3. RegisterLifecycleBackendWithProfiles sets explicit agent_runtime
	if err := reg.RegisterLifecycleBackendWithProfiles(
		"be-agent",
		dummyLifecycle,
		pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialNone},
		aipsdk.BackendExecutionProfile{Class: aipsdk.BackendExecutionAgentRuntime},
	); err != nil {
		t.Fatalf("RegisterLifecycleBackendWithProfiles: %v", err)
	}
	prof, ok = reg.BackendExecutionProfile("be-agent")
	if !ok || prof.Class != aipsdk.BackendExecutionAgentRuntime {
		t.Fatalf("expected agent_runtime, got %+v (ok=%v)", prof, ok)
	}

	// 4. Invalid execution profile fails registration
	if err := reg.RegisterBackendWithProfiles(
		"be-invalid",
		dummyFactory,
		pluginreg.BackendSecurityProfile{},
		aipsdk.BackendExecutionProfile{Class: "bad_class"},
	); err == nil {
		t.Fatal("expected error registering invalid execution class")
	}

	// 5. BackendExecutionProfiles map returns all registered entries defensively
	all := reg.BackendExecutionProfiles()
	if len(all) != 3 {
		t.Fatalf("expected 3 entries in all profiles, got %d", len(all))
	}
	if all["be-inf"].Class != aipsdk.BackendExecutionInference {
		t.Fatalf("mismatch in map copy for be-inf: %+v", all["be-inf"])
	}
}
