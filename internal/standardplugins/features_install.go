package standardplugins

import (
	"fmt"

	"github.com/aiproxer/aiproxer/internal/plugins/features/codexclientcompat"
	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity"
	"github.com/aiproxer/aiproxer/internal/plugins/features/downstreamcacheaffinity"
	"github.com/aiproxer/aiproxer/internal/plugins/features/interleavedthinking"
	"github.com/aiproxer/aiproxer/internal/plugins/features/keepwarm"
	"github.com/aiproxer/aiproxer/internal/plugins/features/modelsystemprompt"
	"github.com/aiproxer/aiproxer/internal/plugins/features/partsnoop"
	pathvirtualizationbundle "github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/bundle"
	pathvirtualizationconfig "github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/config"
	"github.com/aiproxer/aiproxer/internal/plugins/features/prerequestpolicy"
	"github.com/aiproxer/aiproxer/internal/plugins/features/proxymemory"
	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/internal/plugins/features/refautoappend"
	"github.com/aiproxer/aiproxer/internal/plugins/features/refparts"
	"github.com/aiproxer/aiproxer/internal/plugins/features/refsubmit"
	"github.com/aiproxer/aiproxer/internal/plugins/features/reftool"
	"github.com/aiproxer/aiproxer/internal/plugins/features/reftoolpolicy"
	"github.com/aiproxer/aiproxer/internal/plugins/features/reftraffictranscript"
	"github.com/aiproxer/aiproxer/internal/plugins/features/refverifier"
	"github.com/aiproxer/aiproxer/internal/plugins/features/refworkspaceguard"
	"github.com/aiproxer/aiproxer/internal/plugins/features/secretguard"
	"github.com/aiproxer/aiproxer/internal/plugins/features/sessionclassification"
	"github.com/aiproxer/aiproxer/internal/plugins/features/submitnoop"
	"github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair"
	"github.com/aiproxer/aiproxer/internal/plugins/features/toolnamecorrection"
	"github.com/aiproxer/aiproxer/internal/plugins/features/toolreactornoop"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	aipplugin "github.com/aiproxer/aiproxer/pkg/aipsdk/plugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcatalog"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"
	sdktraffic "github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
	"gopkg.in/yaml.v3"
)

func featureSubmitNoop(n yaml.Node) (aipfeature.FeatureBundle, error) {
	cfg, err := submitnoop.DecodeHookConfig(n)
	if err != nil {
		return aipfeature.FeatureBundle{}, err
	}
	cs := aipfeature.NewContributionSet()
	if err := aipfeature.Contribute(cs, aipfeature.PlaneSubmitHooks, submitnoop.ID, []sdk.SubmitHook{submitnoop.NewSubmitHookWithConfig(cfg)}); err != nil {
		return aipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", submitnoop.ID, err)
	}
	var lifecycles []aipplugin.Lifecycle
	if cfg.LifecycleProbe {
		lifecycles = append(lifecycles, submitnoop.NewLifecycleProbeForConfig())
	}
	return aipfeature.BundleFromPlanes(cs.Freeze(), lifecycles), nil
}

func featurePartsNoop(n yaml.Node) (aipfeature.FeatureBundle, error) {
	if err := requireEmptyFeatureYAML(partsnoop.ID, n); err != nil {
		return aipfeature.FeatureBundle{}, err
	}
	cs := aipfeature.NewContributionSet()
	if err := aipfeature.Contribute(cs, aipfeature.PlaneRequestPartHooks, partsnoop.ID, []sdk.RequestPartHook{partsnoop.NewRequestPartHook()}); err != nil {
		return aipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", partsnoop.ID, err)
	}
	if err := aipfeature.Contribute(cs, aipfeature.PlaneResponsePartHooks, partsnoop.ID, []sdk.ResponsePartHook{partsnoop.NewResponsePartHook()}); err != nil {
		return aipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", partsnoop.ID, err)
	}
	return aipfeature.BundleFromPlanes(cs.Freeze(), nil), nil
}

// featureDownstreamCacheAffinity publishes the opt-in downstream cache-affinity
// hint (requirements.md 1.3, 1.5, design section 1.3).
//
// The feature owns exactly one attempt transform and nothing else: no
// lifecycle, no observer, no exclusive plane. It has no knobs, so the subtree is
// required to be empty and anything written into it fails generation
// compilation instead of being silently ignored.
//
// Enablement lives on the outer plugin row (`plugins.features[].enabled`), not
// in this subtree. An absent or `enabled: false` row never reaches this factory,
// so the plane keeps no occupant and large-payload wire fast-path eligibility is
// exactly what it is today (requirements.md 1.2).
func featureDownstreamCacheAffinity(n yaml.Node) (aipfeature.FeatureBundle, error) {
	if err := requireEmptyFeatureYAML(downstreamcacheaffinity.ID, n); err != nil {
		return aipfeature.FeatureBundle{}, err
	}
	cs := aipfeature.NewContributionSet()
	if err := aipfeature.Contribute(cs, aipfeature.PlaneAttemptTransforms, downstreamcacheaffinity.ID,
		[]request.AttemptTransform{downstreamcacheaffinity.Transform{}}); err != nil {
		return aipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", downstreamcacheaffinity.ID, err)
	}
	return aipfeature.BundleFromPlanes(cs.Freeze(), nil), nil
}

func featureToolReactorNoop(n yaml.Node) (aipfeature.FeatureBundle, error) {
	if err := requireEmptyFeatureYAML(toolreactornoop.ID, n); err != nil {
		return aipfeature.FeatureBundle{}, err
	}
	cs := aipfeature.NewContributionSet()
	if err := aipfeature.Contribute(cs, aipfeature.PlaneToolReactors, toolreactornoop.ID, []sdk.ToolReactor{toolreactornoop.NewToolReactor()}); err != nil {
		return aipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", toolreactornoop.ID, err)
	}
	return aipfeature.BundleFromPlanes(cs.Freeze(), nil), nil
}

func featureRefSubmit(n yaml.Node) (aipfeature.FeatureBundle, error) {
	cfg, err := refsubmit.DecodeConfig(n)
	if err != nil {
		return aipfeature.FeatureBundle{}, err
	}
	cs := aipfeature.NewContributionSet()
	if err := aipfeature.Contribute(cs, aipfeature.PlaneSubmitHooks, refsubmit.ID, []sdk.SubmitHook{refsubmit.NewSubmitHook(cfg)}); err != nil {
		return aipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", refsubmit.ID, err)
	}
	return aipfeature.BundleFromPlanes(cs.Freeze(), nil), nil
}

func featureRefParts(n yaml.Node) (aipfeature.FeatureBundle, error) {
	cfg, err := refparts.DecodeConfig(n)
	if err != nil {
		return aipfeature.FeatureBundle{}, err
	}
	cs := aipfeature.NewContributionSet()
	if err := aipfeature.Contribute(cs, aipfeature.PlaneRequestPartHooks, refparts.ID, []sdk.RequestPartHook{refparts.NewRequestPartHook(cfg)}); err != nil {
		return aipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", refparts.ID, err)
	}
	if err := aipfeature.Contribute(cs, aipfeature.PlaneResponsePartHooks, refparts.ID, []sdk.ResponsePartHook{refparts.NewResponsePartHook(cfg)}); err != nil {
		return aipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", refparts.ID, err)
	}
	return aipfeature.BundleFromPlanes(cs.Freeze(), nil), nil
}

func featureRefTool(n yaml.Node) (aipfeature.FeatureBundle, error) {
	cfg, err := reftool.DecodeConfig(n)
	if err != nil {
		return aipfeature.FeatureBundle{}, err
	}
	cs := aipfeature.NewContributionSet()
	if err := aipfeature.Contribute(cs, aipfeature.PlaneToolReactors, reftool.ID, []sdk.ToolReactor{reftool.NewToolReactor(cfg)}); err != nil {
		return aipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", reftool.ID, err)
	}
	return aipfeature.BundleFromPlanes(cs.Freeze(), nil), nil
}

func featureRefAutoappend(n yaml.Node) (aipfeature.FeatureBundle, error) {
	cfg, err := refautoappend.DecodeConfig(n)
	if err != nil {
		return aipfeature.FeatureBundle{}, err
	}
	cs := aipfeature.NewContributionSet()
	if err := aipfeature.Contribute(cs, aipfeature.PlaneSessionOpeners, refautoappend.ID, []session.Opener{refautoappend.NewSessionOpener()}); err != nil {
		return aipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", refautoappend.ID, err)
	}
	if err := aipfeature.Contribute(cs, aipfeature.PlaneRequestTransforms, refautoappend.ID, []request.Transform{refautoappend.NewRequestTransform(cfg)}); err != nil {
		return aipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", refautoappend.ID, err)
	}
	return aipfeature.BundleFromPlanes(cs.Freeze(), nil), nil
}

func featureRefToolPolicy(n yaml.Node) (aipfeature.FeatureBundle, error) {
	cfg, err := reftoolpolicy.DecodeConfig(n)
	if err != nil {
		return aipfeature.FeatureBundle{}, err
	}
	cs := aipfeature.NewContributionSet()
	if err := aipfeature.Contribute(cs, aipfeature.PlaneToolCatalogFilters, reftoolpolicy.ID, []toolcatalog.Filter{reftoolpolicy.NewToolCatalogFilter(cfg)}); err != nil {
		return aipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", reftoolpolicy.ID, err)
	}
	if err := aipfeature.Contribute(cs, aipfeature.PlaneToolCallPolicies, reftoolpolicy.ID, []toolpolicy.Policy{reftoolpolicy.NewToolCallPolicy(cfg)}); err != nil {
		return aipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", reftoolpolicy.ID, err)
	}
	if err := aipfeature.Contribute(cs, aipfeature.PlaneToolReactors, reftoolpolicy.ID, []sdk.ToolReactor{reftoolpolicy.NewToolReactor(cfg)}); err != nil {
		return aipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", reftoolpolicy.ID, err)
	}
	return aipfeature.BundleFromPlanes(cs.Freeze(), nil), nil
}

func featureRefWorkspaceGuard(n yaml.Node) (aipfeature.FeatureBundle, error) {
	cfg, err := refworkspaceguard.DecodeConfig(n)
	if err != nil {
		return aipfeature.FeatureBundle{}, err
	}
	cs := aipfeature.NewContributionSet()
	if err := aipfeature.Contribute(cs, aipfeature.PlaneWorkspaceResolvers, refworkspaceguard.ID, []workspace.Resolver{refworkspaceguard.NewStaticResolver(cfg)}); err != nil {
		return aipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", refworkspaceguard.ID, err)
	}
	if err := aipfeature.Contribute(cs, aipfeature.PlaneRequestTransforms, refworkspaceguard.ID, []request.Transform{refworkspaceguard.NewSessionUnlockTransform(cfg)}); err != nil {
		return aipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", refworkspaceguard.ID, err)
	}
	if err := aipfeature.Contribute(cs, aipfeature.PlaneToolCatalogFilters, refworkspaceguard.ID, []toolcatalog.Filter{refworkspaceguard.NewCatalogFilter(cfg)}); err != nil {
		return aipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", refworkspaceguard.ID, err)
	}
	if err := aipfeature.Contribute(cs, aipfeature.PlaneToolReactors, refworkspaceguard.ID, []sdk.ToolReactor{refworkspaceguard.NewHeatReactor(cfg)}); err != nil {
		return aipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", refworkspaceguard.ID, err)
	}
	return aipfeature.BundleFromPlanes(cs.Freeze(), nil), nil
}

func featureRefTrafficTranscript(n yaml.Node) (aipfeature.FeatureBundle, error) {
	cfg, err := reftraffictranscript.DecodeConfig(n)
	if err != nil {
		return aipfeature.FeatureBundle{}, err
	}
	cs := aipfeature.NewContributionSet()
	if err := aipfeature.Contribute(cs, aipfeature.PlaneTrafficObservers, reftraffictranscript.ID, []sdktraffic.Observer{reftraffictranscript.NewTranscript()}); err != nil {
		return aipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", reftraffictranscript.ID, err)
	}
	if err := aipfeature.Contribute(cs, aipfeature.PlaneUsageObservers, reftraffictranscript.ID, []usage.Observer{reftraffictranscript.NewUsageLedger()}); err != nil {
		return aipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", reftraffictranscript.ID, err)
	}
	if err := aipfeature.Contribute(cs, aipfeature.PlaneRawCaptureSinks, reftraffictranscript.ID, []sdktraffic.RawCaptureSink{reftraffictranscript.NewRawLog()}); err != nil {
		return aipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", reftraffictranscript.ID, err)
	}
	if err := aipfeature.Contribute(cs, aipfeature.PlaneTrafficRedactors, reftraffictranscript.ID, []sdktraffic.Redactor{reftraffictranscript.NewPatternRedactor(cfg)}); err != nil {
		return aipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", reftraffictranscript.ID, err)
	}
	return aipfeature.BundleFromPlanes(cs.Freeze(), nil), nil
}

func featureRefVerifier(n yaml.Node) (aipfeature.FeatureBundle, error) {
	cfg, err := refverifier.DecodeConfig(n)
	if err != nil {
		return aipfeature.FeatureBundle{}, err
	}
	cs := aipfeature.NewContributionSet()
	if err := aipfeature.Contribute(cs, aipfeature.PlaneCompletionGates, refverifier.ID, []completion.Gate{refverifier.NewCompletionGate(cfg)}); err != nil {
		return aipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", refverifier.ID, err)
	}
	return aipfeature.BundleFromPlanes(cs.Freeze(), nil), nil
}

func featurePreRequestPolicy(n yaml.Node) (aipfeature.FeatureBundle, error) {
	cfg, err := prerequestpolicy.DecodeConfig(n)
	if err != nil {
		return aipfeature.FeatureBundle{}, err
	}
	handlers, err := prerequestpolicy.NewHandlers(cfg)
	if err != nil {
		return aipfeature.FeatureBundle{}, err
	}
	cs := aipfeature.NewContributionSet()
	if err := aipfeature.Contribute(cs, aipfeature.PlanePreRequestHandlers, prerequestpolicy.ID, handlers); err != nil {
		return aipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", prerequestpolicy.ID, err)
	}
	return aipfeature.BundleFromPlanes(cs.Freeze(), nil), nil
}

func featureCodexClientCompat(n yaml.Node) (aipfeature.FeatureBundle, error) {
	cfg, err := codexclientcompat.DecodeConfig(n)
	if err != nil {
		return aipfeature.FeatureBundle{}, err
	}
	cs := aipfeature.NewContributionSet()
	if err := aipfeature.Contribute(cs, aipfeature.PlaneRequestPartHooks, codexclientcompat.ID, []sdk.RequestPartHook{codexclientcompat.NewRequestPartHook(cfg)}); err != nil {
		return aipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", codexclientcompat.ID, err)
	}
	return aipfeature.BundleFromPlanes(cs.Freeze(), nil), nil
}

func featureSecretGuard(n yaml.Node) (aipfeature.FeatureBundle, error) {
	cfg, err := secretguard.DecodeConfig(n)
	if err != nil {
		return aipfeature.FeatureBundle{}, err
	}
	return secretguard.FeatureBundle(cfg), nil
}

func featureReasoningOutputPreservation(n yaml.Node) (aipfeature.FeatureBundle, error) {
	cfg, err := reasoningpreservation.DecodeConfig(n)
	if err != nil {
		return aipfeature.FeatureBundle{}, err
	}
	if cfg.Compression.Enabled {
		// Configuration factory must not fail closed on generation-bound capabilities.
		// Enabled compression is validated for config correctness here, but the
		// actual AttemptTransform/StreamObserver is injected at CompileGeneration
		// via bindReasoningPreservationCompression using process BackgroundAux
		// and trusted egress/matcher resolvers.
		return aipfeature.FeatureBundle{SchemaVersion: aipfeature.SchemaVersionV1}, nil
	}
	return reasoningpreservation.FeatureBundleWithCompanionPolicy(cfg, codexCompanionPolicy())
}

func featureCompactionContinuity(n yaml.Node) (aipfeature.FeatureBundle, error) {
	cfg, err := compactioncontinuity.DecodeConfig(n)
	if err != nil {
		return aipfeature.FeatureBundle{}, err
	}
	return compactioncontinuity.FeatureBundle(cfg), nil
}

func featureToolCallRepair(n yaml.Node) (aipfeature.FeatureBundle, error) {
	cfg, err := toolcallrepair.DecodeConfig(n)
	if err != nil {
		return aipfeature.FeatureBundle{}, err
	}
	return toolcallrepair.FeatureBundle(cfg)
}

func featureToolNameCorrection(n yaml.Node) (aipfeature.FeatureBundle, error) {
	cfg, err := toolnamecorrection.DecodeConfig(n)
	if err != nil {
		return aipfeature.FeatureBundle{}, err
	}
	return toolnamecorrection.FeatureBundle(cfg)
}

// featurePathVirtualization builds the B-leg path virtualization bundle.
//
// It decodes AND compiles the operator subtree in one call, because
// requirements.md 7.5 requires an invalid selector, a conflicting profile, an
// unsupported bound, or an ambiguous configuration to fail generation compilation
// before publication, and the only way that survives a later edit is a single call a
// factory cannot forget to make. An absent subtree, an empty mapping, and an
// explicit `enabled: false` all decode to an inert resolution, which contributes
// nothing at all, so a stock deployment builds no attempt transform, no
// request-part hook, and no expansion finalizer (requirements.md 7.1, 8.1).
//
// No workspace authority is bound here, and none is needed: the runtime pins one per
// logical turn and projects it onto both stages the two outbound passes read, so the
// late pass is armed by the same pin the early pass uses rather than by anything this
// composition root could hand it. A factory signature that took a workspace view would
// be a service locator, and binding the SDK's unbound default would leave the late pass
// inert in the only shipping composition.
func featurePathVirtualization(n yaml.Node) (aipfeature.FeatureBundle, error) {
	resolved, err := pathvirtualizationconfig.Decode(n)
	if err != nil {
		return aipfeature.FeatureBundle{}, err
	}
	if resolved.Disabled() {
		return aipfeature.FeatureBundle{SchemaVersion: aipfeature.SchemaVersionV1}, nil
	}
	return pathvirtualizationbundle.FeatureBundle(resolved)
}

func featureInterleavedThinking(n yaml.Node) (aipfeature.FeatureBundle, error) {
	cfg, err := interleavedthinking.DecodeConfig(n)
	if err != nil {
		return aipfeature.FeatureBundle{}, err
	}
	if !cfg.Enabled {
		return aipfeature.FeatureBundle{SchemaVersion: aipfeature.SchemaVersionV1}, nil
	}
	return aipfeature.FeatureBundle{SchemaVersion: aipfeature.SchemaVersionV1}, nil
}

// The registry validates configuration only; generation-bound bootstrap
// composition belongs to featurehost rather than a public feature plane.
func featureModelSystemPrompt(n yaml.Node) (aipfeature.FeatureBundle, error) {
	cfg, err := modelsystemprompt.DecodeConfig(n)
	if err != nil {
		return aipfeature.FeatureBundle{}, err
	}
	if _, err := modelsystemprompt.New(cfg); err != nil {
		return aipfeature.FeatureBundle{}, err
	}
	return aipfeature.FeatureBundle{SchemaVersion: aipfeature.SchemaVersionV1}, nil
}

func featureKeepwarm(n yaml.Node) (aipfeature.FeatureBundle, error) {
	_, err := keepwarm.DecodeConfig(n)
	if err != nil {
		return aipfeature.FeatureBundle{}, err
	}
	return aipfeature.FeatureBundle{SchemaVersion: aipfeature.SchemaVersionV1}, nil
}

// featureSessionClassification validates and decodes the opaque feature-owned
// config subtree. The concrete classifier needs the process-owned coordinator,
// so the exclusive classifier plane is published later as a generation-bound
// value during standard featurehost composition.
func featureSessionClassification(n yaml.Node) (aipfeature.FeatureBundle, error) {
	cfg, err := sessionclassification.DecodeConfig(n)
	if err != nil {
		return aipfeature.FeatureBundle{}, err
	}
	return sessionclassification.FeatureBundle(cfg), nil
}

// featureProxyMemory decodes and validates the proxy-memory config subtree and
// publishes no plane: capture, extraction and the memory service need
// process-owned resources (the durable store, the background auxiliary client,
// the conversation bootstrap authority), so the generation-bound composition
// happens later in standard featurehost (design §14.3, §14.5). A missing or
// blank route therefore fails generation compile here (Requirement 1.8) rather
// than surfacing later as an inert or half-configured feature.
func featureProxyMemory(n yaml.Node) (aipfeature.FeatureBundle, error) {
	cfg, err := proxymemory.DecodeConfig(n)
	if err != nil {
		return aipfeature.FeatureBundle{}, err
	}
	if err := cfg.Validate(); err != nil {
		return aipfeature.FeatureBundle{}, err
	}
	return aipfeature.FeatureBundle{SchemaVersion: aipfeature.SchemaVersionV1}, nil
}
