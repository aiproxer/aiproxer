package runtimebundle

import (
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/featurebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	aipplugin "github.com/aiproxer/aiproxer/pkg/aipsdk/plugin"
)

// BuildFeatureHooks merges enabled feature plugins into hook bus configuration.
func BuildFeatureHooks(reg *pluginreg.Registry, registrations []aipsdk.Registration) (hooks.Config, []aipplugin.Lifecycle, error) {
	gen, err := featurebundle.MergeFeatureSurfaceGenerated(reg, registrations)
	if err != nil {
		return hooks.Config{}, nil, err
	}
	return aipfeature.ProjectHookConfig(gen.Frozen, sdkhooks.ToolReactorErrorPolicyUnspecified), gen.Lifecycles, nil
}
