package featurehost

import (
	"context"
	"fmt"
	"slices"

	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/featurebundle"
	"github.com/aiproxer/aiproxer/internal/plugins/features/interleavedthinking"
	pathvirtualizationconfig "github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/config"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/reasoning"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

// CompileGeneration compiles standard feature planes, lifecycles, and core ports
// for a candidate generation. It does NOT construct any process-scoped resource
// listed in the Task 1.1 transition table (Requirement 8.3, Task 2.2).
// Candidate failure must not mutate process feature state.
func (r *Runtime) CompileGeneration(ctx context.Context, in GenerationInput) (GenerationOutput, error) {
	if in.FaultInject != nil {
		return GenerationOutput{}, in.FaultInject
	}
	bootstrap, err := r.modelSystemPrompt(in)
	if err != nil {
		return GenerationOutput{}, err
	}
	// Proxy memory composition (design §14.3) is compiled before the disabled
	// facade branch so enabling it without a process facade is still rejected.
	pm, err := r.compileProxyMemory(ctx, in)
	if err != nil {
		return GenerationOutput{}, err
	}

	// When r is nil (StandardFeatures disabled), return disabled feature output.
	if r == nil {
		if err := r.validateCompactionPrerequisites(in.Registrations); err != nil {
			return GenerationOutput{}, err
		}
		bundle := aipfeature.FeatureBundle{
			PlaneSet:   in.Planes,
			Lifecycles: slices.Clone(in.Lifecycles),
		}
		return GenerationOutput{
			HostProjections: HostProjections{FeatureMetadata: InventoryMetadataFromMergeSurface(in.MergeSurface)},
			Bundle:          bundle,
			Planes:          in.Planes,
			Lifecycles:      slices.Clone(in.Lifecycles),
			CorePorts:       CorePorts{},
		}, nil
	}

	// 1. Compaction continuity prerequisites validation.
	if err := r.validateCompactionPrerequisites(in.Registrations); err != nil {
		return GenerationOutput{}, err
	}

	// 2. Cross-feature composition guard. Path virtualization's own subtree decoder
	// cannot see tool-call repair's `order` key, so the rule that its mandatory
	// expansion receives valid completed JSON belongs to the feature that declares
	// the order and is CALLED from here, where the whole enabled registration list
	// is in hand (requirements.md 8.4). It is a no-op for every generation that
	// does not enable path virtualization, and it returns GenerationOutput{} so
	// nothing is published on refusal. This runs before any plane is touched, so a
	// refusal cannot leave a half-composed generation behind.
	if err := pathvirtualizationconfig.ValidateGenerationComposition(in.Registrations); err != nil {
		return GenerationOutput{}, fmt.Errorf("featurehost: path virtualization composition: %w", err)
	}

	surface := in.MergeSurface
	if surface.Frozen.IsZero() && !in.Planes.IsZero() {
		surface = featurebundle.GeneratedMergeSurface{
			Frozen:     in.Planes,
			Lifecycles: slices.Clone(in.Lifecycles),
		}
	}

	// 3. Compaction continuity surface binding.
	surface, err = r.bindCompactionContinuity(surface, in.Registrations)
	if err != nil {
		return GenerationOutput{}, err
	}

	// 4. Reasoning composition. The facade merges production/testing options
	// internally so callers never interpret reasoning policy (Task 2.4).
	reasoningOpts := composeReasoningOptions(in.ReasoningProdOpts, in.ReasoningTestOpts)
	var genBound boundHostFeatures
	genHasReasoning := false
	genHasSecretGuard := false
	if len(in.HostRegistrations) > 0 {
		var err error
		genBound, err = bindHostRegistrations(in.HostRegistrations)
		if err != nil {
			return GenerationOutput{}, fmt.Errorf("featurehost: host registrations: %w", err)
		}
		// Per-binding-type overlay: a generation slice carrying only one
		// binding kind must not suppress the process-bound options of the
		// other kind. Each kind falls back to its process-bound value
		// independently; an override replaces only its own kind.
		genHasReasoning = hasReasoningBinding(in.HostRegistrations)
		genHasSecretGuard = hasSecretGuardBinding(in.HostRegistrations)
	}
	if genHasReasoning {
		reasoningOpts = composeReasoningOptions(reasoningOpts, genBound.reasoning)
	} else if len(r.boundReasoning.EgressPolicies) > 0 || r.boundReasoning.MatcherResolver != nil {
		reasoningOpts = composeReasoningOptions(reasoningOpts, r.boundReasoning)
	}
	if err := reasoning.Validate(reasoning.GenerationInput{
		Registrations: in.Registrations,
		Client:        in.BackgroundClient,
		Poller:        in.BackgroundPoller,
		Options:       reasoningOpts,
	}); err != nil {
		return GenerationOutput{}, fmt.Errorf("featurehost: reasoning validation: %w", err)
	}

	staged, err := reasoning.Bind(surface, reasoning.GenerationInput{
		Registrations: in.Registrations,
		Client:        in.BackgroundClient,
		Poller:        in.BackgroundPoller,
		Options:       reasoningOpts,
	})
	if err != nil {
		return GenerationOutput{}, fmt.Errorf("featurehost: reasoning composition: %w", err)
	}

	outPlanes := staged.Frozen
	if outPlanes.IsZero() {
		outPlanes = in.Planes
	}
	outLifecycles := slices.Clone(staged.Lifecycles)
	if len(outLifecycles) == 0 && len(in.Lifecycles) > 0 {
		outLifecycles = slices.Clone(in.Lifecycles)
	}

	// 5. Secret Guard composition. Bound host inputs are selected by binding
	// presence: a supplied secret-guard binding (generation-bound, else
	// process-bound) is preferred wholesale; legacy inputs apply only when no
	// binding is present. The host pointer lets composition overlay
	// explicitly-set host fields onto YAML-decoded options.
	guards := aipfeature.Get[[]sdk.Guard](outPlanes, aipfeature.PlaneSecretGuards)
	sgEnv := in.SecretEnv
	sgInputs := in.SecretInputs
	var sgHostInputs *SecretGuardInputs
	if genHasSecretGuard {
		if genBound.secretGuard.Environment != nil {
			sgEnv = genBound.secretGuard.Environment
		}
		if genBound.secretGuard.Present {
			sgInputs = genBound.secretGuard.Inputs
			sgHostInputs = &sgInputs
		}
	} else if r != nil {
		if sgEnv == nil && r.boundSecretGuard.Environment != nil {
			sgEnv = r.boundSecretGuard.Environment
		}
		if r.boundSecretGuard.Present {
			sgInputs = r.boundSecretGuard.Inputs
			sgHostInputs = &sgInputs
		}
	}
	sgOut, err := buildSecretGuardRuntime(SecretGuardBuildInput{
		AccessMode:       in.AccessMode,
		Registrations:    in.Registrations,
		Guards:           guards,
		Environment:      sgEnv,
		Inputs:           sgInputs,
		HostInputs:       sgHostInputs,
		DecisionObserver: in.DecisionObserver,
		Logger:           r.logger,
	})
	if err != nil {
		return GenerationOutput{}, fmt.Errorf("featurehost: secret guard composition: %w", err)
	}
	if outPlanes, err = bindSecretGuardExecutionPlane(outPlanes, sgOut); err != nil {
		return GenerationOutput{}, err
	}
	featureMetadata := InventoryMetadataFromMergeSurface(staged)

	// 6. Interleaved Thinking processor. Outer Registration.Enabled is
	// authoritative: disabled entries are skipped, a lone disabled entry
	// disables the feature (no legacy fallback).
	var interleavedProc runtime.InterleavedProcessor
	ic := in.InterleavedConfig
	var featureEntryFound, featureEntryDisabled bool
	for _, r := range in.Registrations {
		if r.Kind != aipsdk.PluginKindFeature || (r.ID != interleavedthinking.ID && r.FactoryKind != interleavedthinking.ID) {
			continue
		}
		if !r.Enabled {
			featureEntryDisabled = true
			continue
		}
		featureEntryFound = true
		decoded, err := interleavedthinking.DecodeConfig(r.Config.Node)
		if err != nil {
			return GenerationOutput{}, fmt.Errorf("featurehost: interleaved config: %w", err)
		}
		if !ic.Enabled && decoded.Enabled {
			ic = decoded
		}
		break
	}
	interleavedDisabled := featureEntryDisabled && !featureEntryFound

	// Conflict detection: if both canonical feature entry and legacy config.interleaved are enabled
	if in.ConfigInterleaved.Enabled && featureEntryFound && ic.Enabled {
		return GenerationOutput{}, fmt.Errorf("featurehost: both legacy config.interleaved and canonical feature %q are configured", interleavedthinking.ID)
	}

	// Fallback to legacy config.interleaved. An explicitly disabled canonical
	// entry is authoritative and suppresses this fallback as well.
	if !interleavedDisabled && !ic.Enabled && in.ConfigInterleaved.Enabled {
		ic = interleavedthinking.Config{
			Enabled:               true,
			StreamToClient:        interleavedthinking.DefaultStreamToClient,
			RegularTurnsRemaining: interleavedthinking.DefaultRegularTurns,
			MaxMemoBytes:          interleavedthinking.DefaultMaxMemoBytes,
		}
	}
	if !interleavedDisabled && ic.Enabled {
		if err := ic.Validate(); err != nil {
			return GenerationOutput{}, fmt.Errorf("featurehost: interleaved processor: %w", err)
		}
		if ic.InstructionsFile != "" && ic.Instructions == "" {
			instructions, err := interleavedthinking.ResolveInstructions(in.ConfigDir, ic.InstructionsFile, "")
			if err != nil {
				return GenerationOutput{}, fmt.Errorf("featurehost: interleaved processor: %w", err)
			}
			ic.Instructions = instructions
			ic.InstructionsFile = ""
		}
		store := interleavedthinking.NewMemoStore(ic.EffectiveMaxMemoBytes())
		proc, err := newInterleavedProcessor(ic, store)
		if err != nil {
			return GenerationOutput{}, fmt.Errorf("featurehost: interleaved processor: %w", err)
		}
		interleavedProc = NewInterleavedProcessorAdapter(proc)
	}

	// 7. Keep-warm attachments: maintenance port, ledger-owned lifecycle,
	// deferred metrics swap, and opaque admin projection for CorePorts.
	kwMaint, kwLife, kwSwap, kwAdmin, err := r.keepwarmGenerationPorts(in)
	if err != nil {
		return GenerationOutput{}, err
	}
	if kwLife != nil {
		outLifecycles = append(outLifecycles, kwLife)
	}
	// 7. Session classification: the generation-bound classifier is published on
	// the exclusive classifier plane only for an outer-enabled registration.
	// Config is validated here, so an invalid candidate fails before publication.
	if outPlanes, err = r.bindSessionClassifier(outPlanes, in); err != nil {
		return GenerationOutput{}, err
	}
	// Proxy memory plane contributions land after the session-classifier
	// binding, so the capture transform and observer factory ride the frozen
	// plane set the candidate publishes (design §14.3).
	if pm.planes != nil {
		if outPlanes, err = pm.planes(outPlanes); err != nil {
			return GenerationOutput{}, err
		}
	}
	if classificationLife := r.sessionClassificationLifecycle(in.Registrations); classificationLife != nil {
		outLifecycles = append(outLifecycles, classificationLife)
	}
	outLifecycles = append(outLifecycles, pm.lifecycles...)
	return GenerationOutput{
		Bundle: aipfeature.FeatureBundle{
			PlaneSet:   outPlanes,
			Lifecycles: slices.Clone(outLifecycles),
		},
		Planes:     outPlanes,
		Lifecycles: outLifecycles,
		CorePorts: CorePorts{
			CompactionDetector:            r.compactionDetector,
			ConversationViewReader:        r.ConversationReader(),
			ConversationReaderStockOrigin: r.conversationStoreStock && r.conversationStore != nil,
			ConversationBootstrap:         bootstrap,
			InterleavedProcessor:          interleavedProc,
			PromptCacheMaintenance:        kwMaint,
			TerminalPolicyReader:          r.TerminalPolicyReader(),
			// The proxy-memory feature's pre-open guard rides the core
			// ports: the core calls it at the provider-open chokepoint
			// (design §2).
			AttemptGuard: pm.attemptGuard,
		},
		HostProjections: HostProjections{
			FeatureMetadata:          featureMetadata,
			MetricsSwap:              kwSwap,
			KeepwarmAdmin:            kwAdmin,
			TerminalPolicyProjection: r.TerminalPolicyProjection(),
			ProxyMemoryHTTP:          pm.http,
		},
	}, nil
}
