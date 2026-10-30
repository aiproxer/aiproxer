package pluginreg_test

import (
	"net/http"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"gopkg.in/yaml.v3"
)

func TestValidateBundledFactories_succeedsForStandardRequirements(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	if err := reg.ValidateBundledFactories(aipsdk.StandardDistributionRequirements()); err != nil {
		t.Fatal(err)
	}
}

func TestValidateBundledFactories_explicitPartialRegistryFails(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	if err := reg.ValidateBundledFactories(aipsdk.StandardDistributionRequirements()); err == nil {
		t.Fatal("expected error when registry only has backends")
	}
}

func TestValidateBundledFactories_customRegistryIndependentOfDefaultCompleteness(t *testing.T) {
	t.Parallel()

	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackend(
		"validate-custom-only",
		func(n yaml.Node, upstream *http.Client, deps pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
			_ = n
			_ = upstream
			_ = deps
			return execbackend.Backend{Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming)}, nil
		},
	); err != nil {
		t.Fatal(err)
	}

	req := []aipsdk.Requirement{{
		Kind: aipsdk.PluginKindBackend,
		ID:   "validate-custom-only",
	}}
	if err := reg.ValidateBundledFactories(req); err != nil {
		t.Fatal(err)
	}

	empty := pluginreg.NewRegistry()
	if err := empty.ValidateBundledFactories(req); err == nil {
		t.Fatal("expected empty registry to fail mandatory validation")
	}
}
