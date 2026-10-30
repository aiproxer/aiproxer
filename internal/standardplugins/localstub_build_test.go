package standardplugins

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/localstub"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func TestStandardBackends_excludeLocalStubFactory(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	if reg.HasBackend(localstub.ID) {
		t.Fatal("local-stub must not be statically registered; use external localstub discovery")
	}
}

func TestStandardBundle_validateMandatoryWithoutLocalStub(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := InstallStandardBundleOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	if err := reg.ValidateBundledFactories(aipsdk.StandardDistributionRequirements()); err != nil {
		t.Fatal(err)
	}
}
