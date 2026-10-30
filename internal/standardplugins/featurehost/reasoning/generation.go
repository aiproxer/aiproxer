package reasoning

import (
	"fmt"

	"github.com/aiproxer/aiproxer/internal/featurebundle"
	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

// GenerationInput specifies explicit composition inputs for reasoning semantic compression.
type GenerationInput struct {
	Registrations []aipsdk.Registration
	Client        auxiliary.BackgroundClient
	Poller        auxiliary.BackgroundPoller
	Options       Options
}

type reasoningCompressionBinding struct {
	cfg       reasoningpreservation.Config
	policy    reasoningpreservation.EgressPolicy
	resolver  sdk.MatcherResolver
	sanitizer reasoningpreservation.TrustedTextSanitizer
}

func decodeBindings(in GenerationInput) ([]reasoningCompressionBinding, error) {
	var out []reasoningCompressionBinding
	for _, reg := range in.Registrations {
		if reg.Kind != aipsdk.PluginKindFeature || !reg.Enabled || reg.RegistryFactoryKey() != reasoningpreservation.ID {
			continue
		}
		cfg, err := reasoningpreservation.DecodeConfig(reg.Config.Node)
		if err != nil {
			return nil, fmt.Errorf("reasoningpreservation: config: %w", err)
		}
		if !cfg.Compression.Enabled {
			continue
		}
		if IsNilCapability(in.Client) || IsNilCapability(in.Poller) {
			return nil, fmt.Errorf("reasoningpreservation: compression enabled requires BackgroundAux")
		}
		policy, ok := lookupEgressPolicy(in.Options, cfg.Compression.EgressPolicyRef)
		if !ok || IsNilCapability(policy) {
			return nil, fmt.Errorf("reasoningpreservation: compression enabled requires trusted EgressPolicy for %q", cfg.Compression.EgressPolicyRef)
		}
		resolver := lookupMatcherResolver(in.Options)
		if IsNilCapability(resolver) {
			return nil, fmt.Errorf("reasoningpreservation: compression enabled requires SecretGuard MatcherResolver")
		}
		sanitizer := reasoningpreservation.NewResolverSanitizer(resolver)
		if IsNilCapability(sanitizer) {
			return nil, fmt.Errorf("reasoningpreservation: compression enabled requires BackgroundClient, BackgroundPoller, EgressPolicy, TrustedTextSanitizer")
		}
		out = append(out, reasoningCompressionBinding{cfg: cfg, policy: policy, resolver: resolver, sanitizer: sanitizer})
	}
	return out, nil
}

func lookupEgressPolicy(opts Options, ref string) (reasoningpreservation.EgressPolicy, bool) {
	if opts.EgressPolicies == nil {
		return nil, false
	}
	p, ok := opts.EgressPolicies[ref]
	return p, ok
}

func lookupMatcherResolver(opts Options) sdk.MatcherResolver {
	if IsNilCapability(opts.MatcherResolver) {
		return nil
	}
	return opts.MatcherResolver
}

// Validate verifies that feature configuration and generation-bound prerequisites
// for reasoning semantic compression are satisfied.
func Validate(in GenerationInput) error {
	_, err := decodeBindings(in)
	return err
}

// Bind replaces placeholder reasoning participants with configured compression services
// on the candidate merge surface using generated typed operations.
func Bind(surface featurebundle.GeneratedMergeSurface, in GenerationInput) (featurebundle.GeneratedMergeSurface, error) {
	bindings, err := decodeBindings(in)
	if err != nil {
		return featurebundle.GeneratedMergeSurface{}, err
	}
	if len(bindings) == 0 {
		return surface, nil
	}
	staged := surface
	for _, b := range bindings {
		svc := reasoningpreservation.CompressionServices{
			Client:       in.Client,
			Poller:       in.Poller,
			EgressPolicy: b.policy,
			Sanitizer:    b.sanitizer,
		}
		_, bundle, err := reasoningpreservation.FeatureBundleWithPartsAndCompression(b.cfg, svc, standardplugins.CodexCompanionPolicy())
		if err != nil {
			return featurebundle.GeneratedMergeSurface{}, fmt.Errorf("reasoningpreservation: compression composition: %w", err)
		}
		attemptTransforms := aipfeature.Get(bundle.PlaneSet, aipfeature.PlaneAttemptTransforms)
		if len(attemptTransforms) > 0 {
			staged, err = staged.BindAttemptTransforms(reasoningpreservation.ID, attemptTransforms)
			if err != nil {
				return featurebundle.GeneratedMergeSurface{}, err
			}
		}
		streamObservers := aipfeature.Get(bundle.PlaneSet, aipfeature.PlaneStreamObserverFactories)
		if len(streamObservers) > 0 {
			staged, err = staged.BindStreamObserverFactories(reasoningpreservation.ID, streamObservers)
			if err != nil {
				return featurebundle.GeneratedMergeSurface{}, err
			}
		}
	}
	return staged, nil
}

// BindFeatureSurface is an alias for Bind following the compactioncompose naming convention.
func BindFeatureSurface(surface featurebundle.GeneratedMergeSurface, in GenerationInput) (featurebundle.GeneratedMergeSurface, error) {
	return Bind(surface, in)
}
