package featurebundle

import (
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	aipplugin "github.com/aiproxer/aiproxer/pkg/aipsdk/plugin"
)

// FeatureBundleRegistry represents any registry or catalog capable of building FeatureBundles.
type FeatureBundleRegistry interface {
	BuildFeatureBundle(factoryKey string, n yaml.Node) (aipfeature.FeatureBundle, error)
}

func appendLifecycles(dst []aipplugin.Lifecycle, incoming []aipplugin.Lifecycle) []aipplugin.Lifecycle {
	if incoming == nil {
		return dst
	}
	if dst == nil {
		dst = make([]aipplugin.Lifecycle, 0, len(incoming))
	}
	return append(dst, incoming...)
}

// BuildEnabledFeatureBundles builds FeatureBundles from enabled feature registrations in order.
func BuildEnabledFeatureBundles(reg FeatureBundleRegistry, registrations []aipsdk.Registration) ([]aipfeature.FeatureBundle, error) {
	nFeat := 0
	for _, regEntry := range registrations {
		if regEntry.Kind == aipsdk.PluginKindFeature && regEntry.Enabled {
			nFeat++
		}
	}
	bundles := make([]aipfeature.FeatureBundle, 0, nFeat)
	for _, regEntry := range registrations {
		if regEntry.Kind != aipsdk.PluginKindFeature || !regEntry.Enabled {
			continue
		}
		factoryKey := regEntry.RegistryFactoryKey()
		b, err := reg.BuildFeatureBundle(factoryKey, regEntry.Config.Node)
		if err != nil {
			return nil, err
		}
		bundles = append(bundles, b)
	}
	return bundles, nil
}

func buildEnabledFeatureBundles(reg FeatureBundleRegistry, registrations []aipsdk.Registration) ([]aipfeature.FeatureBundle, error) {
	return BuildEnabledFeatureBundles(reg, registrations)
}

func mergeBuiltFeatureBundlesWithHost(
	bundles []aipfeature.FeatureBundle,
	registrations []aipsdk.Registration,
	host HostContributions,
	extraFeatureBundles ...aipfeature.FeatureBundle,
) (GeneratedMergeSurface, error) {
	cs := aipfeature.NewContributionSet()
	var lifecycles []aipplugin.Lifecycle
	diagnostics := make([]FeatureDiagnosticProjection, 0, len(bundles))
	bIdx := 0
	for _, regEntry := range registrations {
		if regEntry.Kind != aipsdk.PluginKindFeature || !regEntry.Enabled {
			continue
		}
		b := bundles[bIdx]
		bIdx++
		pluginID := regEntry.ID
		if pluginID == "" {
			pluginID = regEntry.RegistryFactoryKey()
		}
		if pluginID == "" {
			pluginID = fmt.Sprintf("feature-%d", bIdx)
		}
		if err := ContributeBundle(cs, pluginID, b); err != nil {
			return GeneratedMergeSurface{}, err
		}
		lifecycles = appendLifecycles(lifecycles, b.Lifecycles)
		diagnostics = append(diagnostics, FeatureDiagnosticProjection{
			InstanceID:  pluginID,
			Projections: cloneDiagnosticPlaneProjections(aipfeature.ProjectDiagnostics(b.PlaneSet)),
		})
	}
	if err := ContributeHost(cs, host); err != nil {
		return GeneratedMergeSurface{}, err
	}
	for i, eb := range extraFeatureBundles {
		extraID := fmt.Sprintf("candidate-feature-%d", i)
		if err := ContributeBundle(cs, extraID, eb); err != nil {
			return GeneratedMergeSurface{}, err
		}
		lifecycles = appendLifecycles(lifecycles, eb.Lifecycles)
	}
	return GeneratedMergeSurface{
		Frozen:             cs.Freeze(),
		Lifecycles:         lifecycles,
		set:                cs,
		featureDiagnostics: diagnostics,
	}, nil
}

// MergeFeatureSurfacesWithHost merges enabled feature plugins, host contributions, and optional candidate feature bundles into
// a GeneratedMergeSurface. Feature bundles are contributed under SourceFeature (plugins first, candidate extras last),
// and host observer contributions are contributed under SourceHost between initial feature plugins and candidate extras.
func MergeFeatureSurfacesWithHost(reg FeatureBundleRegistry, registrations []aipsdk.Registration, host HostContributions, extraFeatureBundles ...aipfeature.FeatureBundle) (GeneratedMergeSurface, error) {
	bundles, err := BuildEnabledFeatureBundles(reg, registrations)
	if err != nil {
		return GeneratedMergeSurface{}, err
	}
	return mergeBuiltFeatureBundlesWithHost(bundles, registrations, host, extraFeatureBundles...)
}
