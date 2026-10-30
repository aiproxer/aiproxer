package localstubreg

import (
	"net/http"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/localstub"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"gopkg.in/yaml.v3"
)

// RegisterInProcess registers the in-process local-stub factory for tests that
// cannot launch the connectors/localstub executable. Production composition
// must not call this; serve uses discovered plugins instead.
func RegisterInProcess(reg *pluginreg.Registry) error {
	if reg == nil {
		return nil
	}
	if reg.HasBackend(localstub.ID) {
		return nil
	}
	return reg.RegisterBackendWithProfiles(localstub.ID, func(n yaml.Node, _ *http.Client, _ pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return localstub.NewFromYAML(n)
	}, pluginreg.BackendSecurityProfile{
		CredentialMode: pluginreg.CredentialNone,
		AccessScope:    pluginreg.BackendAccessAny,
	}, pluginreg.BackendExecutionProfile{
		Class: aipsdk.BackendExecutionInference,
	})
}
