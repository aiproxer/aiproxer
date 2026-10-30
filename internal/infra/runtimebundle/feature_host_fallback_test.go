package runtimebundle

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipplugin "github.com/aiproxer/aiproxer/pkg/aipsdk/plugin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFeatureHost_NoPackageLevelFallback_WhenStandardFeaturesNil(t *testing.T) {
	t.Parallel()

	ps := &ProcessServices{
		StandardFeatures: nil,
	}

	regs := []aipsdk.Registration{
		{
			Kind:        aipsdk.PluginKindFeature,
			ID:          "reasoning-test",
			FactoryKind: "reasoning-preservation-compression",
			Enabled:     true,
			Config: aipsdk.ConfigPayload{
				Node: mustYAMLNode(t, "unsupported_field: true\n"),
			},
		},
	}

	// When ps.StandardFeatures is nil, CompileGeneration returns disabled behavior without fallback.
	out, err := ps.StandardFeatures.CompileGeneration(context.Background(), featurehost.GenerationInput{
		Registrations: regs,
	})
	require.NoError(t, err)
	assert.True(t, out.Planes.IsZero())
	assert.Empty(t, out.Lifecycles)
	assert.Equal(t, featurehost.CorePorts{}, out.CorePorts)
}

func TestCompileCandidate_ComposesSecretGuardOnlyThroughFeatureHost(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	reg := pluginreg.NewRegistry()
	require.NoError(t, standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}))

	ps, err := NewProcessServices(ctx, ProcessServicesInput{
		Cfg:  testProcessServicesOwnershipConfig(),
		Log:  testkit.DiscardLogger(),
		Opts: &BuildOptions{PluginRegistry: reg},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })

	// Candidate with secret-guard enabled
	candCfg := testProcessServicesOwnershipConfig()
	candCfg.Plugins.Features = []config.PluginConfig{
		{
			ID:      "secrets-guard",
			Enabled: true,
			Config:  mustYAMLNode(t, "action: block\npatterns: [\"secret\"]\n"),
		},
	}

	// CompileCandidate without a prebuilt surface runs the production compile,
	// which composes secret guard through the feature host.
	cand, err := CompileCandidate(ctx, GenerationCompileInput{
		Process:   ps,
		Candidate: candCfg,
		CandidateOpts: &BuildOptions{
			PluginRegistry: reg,
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = cand.Close() })

	// Production compile composes secret-guard planes through the feature host,
	// so the accepted candidate reports the compiled posture alongside the
	// generic per-feature metadata.
	inventory := CandidateSecretGuardInventory(cand)
	require.NotNil(t, inventory, "secret guard must be composed through the feature host")
	require.Len(t, inventory.FeatureMetadata, 1)
	feature := inventory.FeatureMetadata[0]
	assert.Equal(t, "secrets-guard", feature.InstanceID)
	assert.NotEmpty(t, feature.StageOccupancy)
	require.NotNil(t, feature.SecretGuard)
	assert.Equal(t, "block", feature.SecretGuard.Action)

	// A caller-supplied surface without secret-guard planes is assembled as
	// given: candidate assembly composes no secret-guard posture on its own,
	// but the generic metadata captured from the compiled bundle is still
	// reported and the secret-guard row stays empty.
	direct, err := CompileCandidate(ctx, GenerationCompileInput{
		Process:   ps,
		Candidate: candCfg,
		CandidateOpts: &BuildOptions{
			PluginRegistry:    reg,
			FeatureLifecycles: []aipplugin.Lifecycle{},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = direct.Close() })
	directInventory := CandidateSecretGuardInventory(direct)
	require.NotNil(t, directInventory, "compiled-bundle metadata must be reported")
	require.Len(t, directInventory.FeatureMetadata, 1)
	directFeature := directInventory.FeatureMetadata[0]
	assert.Equal(t, "secrets-guard", directFeature.InstanceID)
	assert.NotEmpty(t, directFeature.StageOccupancy)
	require.NotNil(t, directFeature.SecretGuard)
	assert.Empty(t, directFeature.SecretGuard.Action)
	assert.Empty(t, directFeature.SecretGuard.AccessMode)
	assert.Zero(t, directFeature.SecretGuard.CatalogEntryCount)
	assert.False(t, directFeature.SecretGuard.BetterLeaksEnabled)
}
