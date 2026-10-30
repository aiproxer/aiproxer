package standardplugins

import (
	"fmt"

	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/features/agentloopguard"
	"github.com/aiproxer/aiproxer/internal/plugins/features/codexclientcompat"
	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity"
	"github.com/aiproxer/aiproxer/internal/plugins/features/downstreamcacheaffinity"
	"github.com/aiproxer/aiproxer/internal/plugins/features/interleavedthinking"
	"github.com/aiproxer/aiproxer/internal/plugins/features/keepwarm"
	"github.com/aiproxer/aiproxer/internal/plugins/features/modelsystemprompt"
	"github.com/aiproxer/aiproxer/internal/plugins/features/partsnoop"
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
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// Identity projection remains part of each backend contribution factory:
// return backendOpenAIResponses(n, upstream, keys, deps.Identity)
// return backendOpenAILegacy(n, upstream, keys, deps.Identity)
// return backendAnthropic(n, upstream, keys, deps.Identity)
// return backendGemini(n, upstream, keys, deps.Identity)
// return backendBedrock(n, upstream, deps.Identity)

func installFrontends(reg *pluginreg.Registry) error {
	for _, e := range StandardBundle().Frontends {
		if err := reg.RegisterFrontend(e.ID, e.Mount); err != nil {
			return err
		}
	}
	return nil
}

// standardFrontendAuthErrorRenderers is the extension point for optional per-wire-frontend renderers
// (auth wire ids per stdhttp/auth DefaultFrontendIDFromRequest). Entries with nil Renderer are skipped.
func installStandardFrontendAuthErrorRenderers(reg *pluginreg.Registry) error {
	for _, e := range StandardBundle().AuthErrorRenderers {
		if e.Renderer == nil {
			continue
		}
		if err := reg.RegisterAuthErrorRenderer(e.WireID, e.Renderer); err != nil {
			return err
		}
	}
	return nil
}

func installFeatures(reg *pluginreg.Registry) error {
	for _, e := range StandardBundle().Features {
		if err := reg.RegisterFeature(e.ID, e.Factory); err != nil {
			return err
		}
	}
	return nil
}

// InstallStandardBundleOn registers all standard bundled factories on reg (tests, alternate bundles).
// keys supplies default API key material when plugin YAML omits api_key (typically from
// [ResolveUpstreamAPIKeysFromEnv] at process startup); tests may pass a zero value.
func InstallStandardBundleOn(reg *pluginreg.Registry, keys UpstreamAPIKeys) error {
	if err := InstallBundleOn(reg, StandardBackendBundle(keys)); err != nil {
		return err
	}
	if err := installFrontends(reg); err != nil {
		return err
	}
	if err := installStandardFrontendAuthErrorRenderers(reg); err != nil {
		return err
	}
	return installFeatures(reg)
}

// InstallStandardBackendsOn registers only bundled backend factories on reg (minimal partial bundles).
func InstallStandardBackendsOn(reg *pluginreg.Registry, keys UpstreamAPIKeys) error {
	return InstallBundleOn(reg, StandardBackendBundle(keys))
}

// InstallEssentialBackendsOn registers only the final essential backend table on reg.
// Frontends and features remain on InstallStandardBundleOn.
func InstallEssentialBackendsOn(reg *pluginreg.Registry, keys UpstreamAPIKeys) error {
	return InstallBundleOn(reg, EssentialBackendBundle(keys))
}

// FrontendRegistration is one explicit frontend contribution to a bundle.
type FrontendRegistration struct {
	ID    string
	Mount pluginreg.FrontendMount
}

// BackendRegistration is one explicit backend contribution to a bundle.
type BackendRegistration struct {
	ID               string
	Factory          pluginreg.BackendFactory
	LifecycleFactory pluginreg.LifecycleBackendFactory
	Profile          pluginreg.BackendSecurityProfile
	ExecProfile      pluginreg.BackendExecutionProfile
	Source           pluginreg.BackendRegistrationSource
}

// FeatureRegistration is one explicit feature contribution to a bundle.
type FeatureRegistration struct {
	ID      string
	Factory pluginreg.FeatureFactory
}

// AuthErrorRendererRegistration binds optional transport-auth error rendering to a wire frontend id.
type AuthErrorRendererRegistration struct {
	WireID   string
	Renderer aipsdk.AuthErrorRenderer
}

// Bundle is the standard distribution composition input. It is a value, not process-global registry state.
type Bundle struct {
	Frontends          []FrontendRegistration
	Backends           []BackendRegistration
	Features           []FeatureRegistration
	AuthErrorRenderers []AuthErrorRendererRegistration
}

// StandardBundle returns the concrete standard distribution table. The standard distribution may import
// bundled plugins here; core and SDK packages must continue to depend only on canonical/SDK contracts.
func StandardBundle() Bundle {
	frontends := standardFrontendContributions()
	return Bundle{
		Frontends: frontendRegistrationsFrom(frontends),
		Backends:  backendRegistrationsFrom(standardBackendContributions(UpstreamAPIKeys{})),
		Features: []FeatureRegistration{
			{ID: agentloopguard.ID, Factory: agentloopguard.BuildBundle},
			{ID: submitnoop.ID, Factory: featureSubmitNoop},
			{ID: partsnoop.ID, Factory: featurePartsNoop},
			{ID: toolreactornoop.ID, Factory: featureToolReactorNoop},
			{ID: toolcallrepair.ID, Factory: featureToolCallRepair},
			{ID: toolnamecorrection.ID, Factory: featureToolNameCorrection},
			{ID: pathvirtualizationconfig.ID, Factory: featurePathVirtualization},
			{ID: refsubmit.ID, Factory: featureRefSubmit},
			{ID: refparts.ID, Factory: featureRefParts},
			{ID: reftool.ID, Factory: featureRefTool},
			{ID: refautoappend.ID, Factory: featureRefAutoappend},
			{ID: reftoolpolicy.ID, Factory: featureRefToolPolicy},
			{ID: refworkspaceguard.ID, Factory: featureRefWorkspaceGuard},
			{ID: reftraffictranscript.ID, Factory: featureRefTrafficTranscript},
			{ID: refverifier.ID, Factory: featureRefVerifier},
			{ID: prerequestpolicy.ID, Factory: featurePreRequestPolicy},
			{ID: codexclientcompat.ID, Factory: featureCodexClientCompat},
			{ID: secretguard.ID, Factory: featureSecretGuard},
			{ID: reasoningpreservation.ID, Factory: featureReasoningOutputPreservation},
			{ID: compactioncontinuity.ID, Factory: featureCompactionContinuity},
			{ID: downstreamcacheaffinity.ID, Factory: featureDownstreamCacheAffinity},
			{ID: interleavedthinking.ID, Factory: featureInterleavedThinking},
			{ID: modelsystemprompt.ID, Factory: featureModelSystemPrompt},
			{ID: keepwarm.ID, Factory: featureKeepwarm},
			{ID: sessionclassification.ID, Factory: featureSessionClassification},
			{ID: proxymemory.ID, Factory: featureProxyMemory},
		},
	}
}

// StandardDiagnosticProjectors returns the projectors owned by standard
// contributions in declaration order.
// StandardBackendBundle returns the essential built-in backend table.
// Optional connector kinds are discovered via closed manifests only.
func StandardBackendBundle(keys UpstreamAPIKeys) Bundle {
	return EssentialBackendBundle(keys)
}

// InstallBundleOn registers b on reg. Tests and alternate composition roots can pass a custom bundle
// without mutating package-level globals.
func InstallBundleOn(reg *pluginreg.Registry, b Bundle) error {
	if reg == nil {
		return fmt.Errorf("pluginreg: InstallBundleOn: nil registry")
	}
	for _, e := range b.Backends {
		if e.LifecycleFactory != nil {
			source := e.Source
			if source == "" {
				source = pluginreg.BackendSourceBuiltin
			}
			if err := reg.RegisterLifecycleBackendWithProfilesAndSource(e.ID, e.LifecycleFactory, e.Profile, e.ExecProfile, source); err != nil {
				return fmt.Errorf("pluginreg: InstallBundleOn: register lifecycle backend %q: %w", e.ID, err)
			}
			continue
		}
		if err := reg.RegisterBackendWithProfilesAndSource(e.ID, e.Factory, e.Profile, e.ExecProfile, e.Source); err != nil {
			return fmt.Errorf("pluginreg: InstallBundleOn: register backend %q: %w", e.ID, err)
		}
	}
	for _, e := range b.Frontends {
		if err := reg.RegisterFrontend(e.ID, e.Mount); err != nil {
			return fmt.Errorf("pluginreg: InstallBundleOn: register frontend %q: %w", e.ID, err)
		}
	}
	for _, e := range b.AuthErrorRenderers {
		if err := reg.RegisterAuthErrorRenderer(e.WireID, e.Renderer); err != nil {
			return fmt.Errorf("pluginreg: InstallBundleOn: register auth error renderer %q: %w", e.WireID, err)
		}
	}
	for _, e := range b.Features {
		if err := reg.RegisterFeature(e.ID, e.Factory); err != nil {
			return fmt.Errorf("pluginreg: InstallBundleOn: register feature %q: %w", e.ID, err)
		}
	}
	return nil
}
