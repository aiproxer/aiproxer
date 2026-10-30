package pluginreg_test

import (
	"net/http"
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"gopkg.in/yaml.v3"
)

func TestDynamic_BuiltinBackendFactoryIDs_ExcludesDiscovered(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	builtinID := "prov-builtin-" + t.Name()
	discoveredID := "prov-discovered-" + t.Name()
	if err := reg.RegisterBackendWithProfile(builtinID, func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{}, nil
	}, pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialNone}); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterDiscoveredLifecycleBackendWithProfile(discoveredID, func(string, yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
		return pluginreg.BackendBuildResult{Backend: execbackend.Backend{}}, nil
	}, pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialNone}); err != nil {
		t.Fatal(err)
	}
	got := reg.BuiltinBackendFactoryIDs()
	if !slices.Contains(got, builtinID) {
		t.Fatalf("builtin missing from BuiltinBackendFactoryIDs: %v", got)
	}
	if slices.Contains(got, discoveredID) {
		t.Fatalf("discovered kind leaked into BuiltinBackendFactoryIDs: %v", got)
	}
	if !reg.HasBackend(discoveredID) {
		t.Fatal("discovered kind must still be HasBackend")
	}
}
