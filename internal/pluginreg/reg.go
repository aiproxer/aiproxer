// Package pluginreg holds the registry and factory-contract types for plugin registration
// (backends, frontends, features). The standard distribution tables now live under
// internal/standardplugins; this package provides the registry mechanism itself.
package pluginreg

import (
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"gopkg.in/yaml.v3"
)

// FrontendMount is the stable SDK-named contract (see pkg/aipsdk).
type FrontendMount = aipsdk.FrontendMount

// BackendFactoryDeps is the host dependency surface for in-process backend
// factories. It aliases GenericBackendFactoryDeps. External executable plugins
// do not receive this type.
type BackendFactoryDeps = GenericBackendFactoryDeps

// BackendFactory builds a backend from opaque per-plugin YAML, the composition-root HTTP client,
// and explicit composition-root runtime dependencies.
type BackendFactory func(n yaml.Node, upstreamHTTP *http.Client, deps BackendFactoryDeps) (execbackend.Backend, error)

// BackendCredentialMode and BackendSecurityProfile are the public plugin registration contract
// (see [aipsdk.BackendCredentialMode], [aipsdk.BackendSecurityProfile]).
type BackendCredentialMode = aipsdk.BackendCredentialMode

type BackendAccessScope = aipsdk.BackendAccessScope

type BackendSecurityProfile = aipsdk.BackendSecurityProfile

type BackendExecutionClass = aipsdk.BackendExecutionClass

type BackendExecutionProfile = aipsdk.BackendExecutionProfile

// Credential posture re-exports for call sites that register through [Registry].
const (
	CredentialStatic    = aipsdk.CredentialStatic
	CredentialWorkload  = aipsdk.CredentialWorkload
	CredentialOAuthUser = aipsdk.CredentialOAuthUser
	CredentialNone      = aipsdk.CredentialNone
	CredentialUnknown   = aipsdk.CredentialUnknown

	BackendAccessAny       = aipsdk.BackendAccessAny
	BackendAccessLocalOnly = aipsdk.BackendAccessLocalOnly

	BackendExecutionUnknown      = aipsdk.BackendExecutionUnknown
	BackendExecutionInference    = aipsdk.BackendExecutionInference
	BackendExecutionAgentRuntime = aipsdk.BackendExecutionAgentRuntime
)

// FeatureFactory builds a versioned feature bundle from opaque plugin YAML.
type FeatureFactory func(n yaml.Node) (aipfeature.FeatureBundle, error)

// BackendRegistrationSource is the provenance of a registered backend factory.
type BackendRegistrationSource string

const (
	// BackendSourceBuiltin marks essential in-process composition-root registrations.
	BackendSourceBuiltin BackendRegistrationSource = "builtin"
	// BackendSourceBuiltinCompatible marks dependency-free configurable protocol aliases.
	BackendSourceBuiltinCompatible BackendRegistrationSource = "built_in_compatible"
	// BackendSourceDiscovered marks factories installed from trusted plugin artifacts.
	BackendSourceDiscovered BackendRegistrationSource = "discovered"
)

// Registry holds bundled plugin factories for one composition root. The zero value is an
// empty registry: lookups behave like an empty bundle, and the first Register* call lazily
// allocates internal maps (same observable behavior as [NewRegistry]). Use [NewRegistry] and
// [InstallStandardBundleOn] to assemble isolated bundles for each composition root.
type Registry struct {
	mu                       sync.RWMutex
	backends                 map[string]BackendFactory
	lifecycleBackends        map[string]LifecycleBackendFactory
	backendProfiles          map[string]BackendSecurityProfile
	backendExecutionProfiles map[string]BackendExecutionProfile
	backendSources           map[string]BackendRegistrationSource
	reloadPolicies           map[string]BackendReloadPolicy
	discovered               map[string]struct{}
	discoveryFrozen          bool
	frontends                map[string]FrontendMount
	features                 map[string]FeatureFactory
	authErrorRenderers       map[string]aipsdk.AuthErrorRenderer
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		backends:                 map[string]BackendFactory{},
		lifecycleBackends:        map[string]LifecycleBackendFactory{},
		backendProfiles:          map[string]BackendSecurityProfile{},
		backendExecutionProfiles: map[string]BackendExecutionProfile{},
		backendSources:           map[string]BackendRegistrationSource{},
		reloadPolicies:           map[string]BackendReloadPolicy{},
		discovered:               map[string]struct{}{},
		frontends:                map[string]FrontendMount{},
		features:                 map[string]FeatureFactory{},
		authErrorRenderers:       map[string]aipsdk.AuthErrorRenderer{},
	}
}

func (r *Registry) ensureMaps() {
	if r.backends == nil {
		r.backends = map[string]BackendFactory{}
	}
	if r.lifecycleBackends == nil {
		r.lifecycleBackends = map[string]LifecycleBackendFactory{}
	}
	if r.backendProfiles == nil {
		r.backendProfiles = map[string]BackendSecurityProfile{}
	}
	if r.backendExecutionProfiles == nil {
		r.backendExecutionProfiles = map[string]BackendExecutionProfile{}
	}
	if r.backendSources == nil {
		r.backendSources = map[string]BackendRegistrationSource{}
	}
	if r.reloadPolicies == nil {
		r.reloadPolicies = map[string]BackendReloadPolicy{}
	}
	if r.discovered == nil {
		r.discovered = map[string]struct{}{}
	}
	if r.frontends == nil {
		r.frontends = map[string]FrontendMount{}
	}
	if r.features == nil {
		r.features = map[string]FeatureFactory{}
	}
	if r.authErrorRenderers == nil {
		r.authErrorRenderers = map[string]aipsdk.AuthErrorRenderer{}
	}
}

// RegisterBackend records a backend factory on r.
// Duplicate ids return an error: the standard bundle must register each id exactly once.
func (r *Registry) RegisterBackend(id string, fn BackendFactory) error {
	return r.RegisterBackendWithProfiles(id, fn, BackendSecurityProfile{CredentialMode: CredentialUnknown}, BackendExecutionProfile{})
}

// RegisterBackendWithProfile records a builtin backend factory with credential posture metadata.
func (r *Registry) RegisterBackendWithProfile(id string, fn BackendFactory, profile BackendSecurityProfile) error {
	return r.RegisterBackendWithProfilesAndSource(id, fn, profile, BackendExecutionProfile{}, BackendSourceBuiltin)
}

// RegisterBackendWithProfiles records a builtin backend factory with credential and execution posture metadata.
func (r *Registry) RegisterBackendWithProfiles(id string, fn BackendFactory, secProfile BackendSecurityProfile, execProfile BackendExecutionProfile) error {
	return r.RegisterBackendWithProfilesAndSource(id, fn, secProfile, execProfile, BackendSourceBuiltin)
}

// RegisterBackendWithSource records a backend factory with bounded composition provenance.
func (r *Registry) RegisterBackendWithSource(id string, fn BackendFactory, profile BackendSecurityProfile, source BackendRegistrationSource) error {
	return r.RegisterBackendWithProfilesAndSource(id, fn, profile, BackendExecutionProfile{}, source)
}

// RegisterBackendWithProfilesAndSource records a backend factory with security and execution metadata and provenance.
func (r *Registry) RegisterBackendWithProfilesAndSource(
	id string,
	fn BackendFactory,
	profile BackendSecurityProfile,
	execProfile BackendExecutionProfile,
	source BackendRegistrationSource,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensureMaps()
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("pluginreg: RegisterBackend: empty id")
	}
	if _, exists := r.backends[id]; exists {
		return fmt.Errorf("pluginreg: duplicate backend registration: %s", id)
	}
	if profile.CredentialMode == "" {
		profile.CredentialMode = CredentialUnknown
	}
	if profile.AccessScope == "" {
		profile.AccessScope = BackendAccessAny
	}
	if err := execProfile.Validate(); err != nil {
		return fmt.Errorf("pluginreg: RegisterBackend: %w", err)
	}
	if source == "" {
		source = BackendSourceBuiltin
	}
	if source != BackendSourceBuiltin && source != BackendSourceBuiltinCompatible && source != BackendSourceDiscovered {
		return fmt.Errorf("pluginreg: RegisterBackend: unsupported source %q", source)
	}
	r.backends[id] = fn
	r.backendProfiles[id] = profile
	r.backendExecutionProfiles[id] = execProfile
	r.backendSources[id] = source
	if source == BackendSourceDiscovered {
		r.discovered[id] = struct{}{}
	}
	return nil
}

// RegisterFrontend records a frontend mount on r.
func (r *Registry) RegisterFrontend(id string, fn FrontendMount) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensureMaps()
	if id == "" {
		return fmt.Errorf("pluginreg: RegisterFrontend: empty id")
	}
	if _, exists := r.frontends[id]; exists {
		return fmt.Errorf("pluginreg: duplicate frontend registration: %s", id)
	}
	r.frontends[id] = fn
	return nil
}

// RegisterAuthErrorRenderer records an optional transport auth error renderer keyed by the auth
// wire frontend id (same strings as stdhttp/auth [DefaultFrontendIDFromRequest], e.g. anthropic,
// openai_compatible, gemini). Nil renderer is a no-op registration attempt (returns nil).
// Keys are normalized to lowercase. Duplicate wire ids return an error.
func (r *Registry) RegisterAuthErrorRenderer(authWireFrontendID string, renderer aipsdk.AuthErrorRenderer) error {
	id := strings.ToLower(strings.TrimSpace(authWireFrontendID))
	if id == "" {
		return fmt.Errorf("pluginreg: RegisterAuthErrorRenderer: empty auth wire frontend id")
	}
	if renderer == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensureMaps()
	if _, exists := r.authErrorRenderers[id]; exists {
		return fmt.Errorf("pluginreg: duplicate auth error renderer registration: %s", id)
	}
	r.authErrorRenderers[id] = renderer
	return nil
}

// AuthErrorRenderers returns a defensive copy of optional per-wire-frontend auth error renderers.
func (r *Registry) AuthErrorRenderers() map[string]aipsdk.AuthErrorRenderer {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.authErrorRenderers) == 0 {
		return nil
	}
	out := make(map[string]aipsdk.AuthErrorRenderer, len(r.authErrorRenderers))
	for k, v := range r.authErrorRenderers {
		if v == nil {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// RegisterFeature records a feature factory on r.
func (r *Registry) RegisterFeature(id string, fn FeatureFactory) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensureMaps()
	if id == "" {
		return fmt.Errorf("pluginreg: RegisterFeature: empty id")
	}
	if _, exists := r.features[id]; exists {
		return fmt.Errorf("pluginreg: duplicate feature registration: %s", id)
	}
	r.features[id] = fn
	return nil
}

// BackendRegistrationSource returns bounded provenance for a registered factory.
func (r *Registry) BackendRegistrationSource(factoryID string) (BackendRegistrationSource, bool) {
	if r == nil {
		return "", false
	}
	factoryID = strings.TrimSpace(factoryID)
	r.mu.RLock()
	defer r.mu.RUnlock()
	source, ok := r.backendSources[factoryID]
	return source, ok
}

// BackendSecurityProfile returns credential posture metadata for a registered backend factory.
func (r *Registry) BackendSecurityProfile(factoryID string) (BackendSecurityProfile, bool) {
	factoryID = strings.TrimSpace(factoryID)
	r.mu.RLock()
	defer r.mu.RUnlock()
	profile, ok := r.backendProfiles[factoryID]
	return profile, ok
}

// BackendExecutionProfile returns execution metadata for a registered backend factory.
func (r *Registry) BackendExecutionProfile(factoryID string) (BackendExecutionProfile, bool) {
	if r == nil {
		return BackendExecutionProfile{}, false
	}
	factoryID = strings.TrimSpace(factoryID)
	r.mu.RLock()
	defer r.mu.RUnlock()
	profile, ok := r.backendExecutionProfiles[factoryID]
	return profile, ok
}

// BackendExecutionProfiles returns a defensive copy of all registered backend execution profiles.
func (r *Registry) BackendExecutionProfiles() map[string]BackendExecutionProfile {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return maps.Clone(r.backendExecutionProfiles)
}

// HasBackend reports whether factoryID is registered on r without exposing the factory.
func (r *Registry) HasBackend(factoryID string) bool {
	if r == nil {
		return false
	}
	factoryID = strings.TrimSpace(factoryID)
	if factoryID == "" {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.backends[factoryID]
	return ok
}

// BackendFactoryIDs returns sorted registered backend factory ids.
func (r *Registry) BackendFactoryIDs() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.backends))
	for id := range r.backends {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

// BuiltinBackendFactoryIDs returns sorted factory ids registered as builtins.
// Discovered artifact exports are omitted so inspect/catalog cannot self-collide.
func (r *Registry) BuiltinBackendFactoryIDs() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.backends))
	for id := range r.backends {
		if r.backendSources[id] == BackendSourceDiscovered {
			continue
		}
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

// BuildBackend constructs a backend from r using the factory id (plugin kind).
// upstreamHTTP is the shared outbound client from the composition root; nil is passed through
// to factories, which apply defaults where HTTP is required (e.g. Bedrock, ACP).
func (r *Registry) BuildBackend(
	factoryID string,
	n yaml.Node,
	upstreamHTTP *http.Client,
	deps BackendFactoryDeps,
) (execbackend.Backend, error) {
	factoryID = strings.TrimSpace(factoryID)

	r.mu.RLock()
	fn, ok := r.backends[factoryID]
	r.mu.RUnlock()
	if !ok {
		return execbackend.Backend{}, fmt.Errorf("pluginreg: unknown backend plugin %q", factoryID)
	}
	return fn(n, upstreamHTTP, deps)
}

// MountFrontend registers routes for one enabled frontend plugin on r.
func (r *Registry) MountFrontend(id string, mux *http.ServeMux, opts aipsdk.FrontendMountOptions) error {
	r.mu.RLock()
	fn, ok := r.frontends[id]
	r.mu.RUnlock()
	if !ok {
		return fmt.Errorf("pluginreg: unknown frontend plugin %q", id)
	}
	return fn(
		mux,
		opts,
	)
}

// BuildFeatureBundle constructs one feature bundle from YAML by dispatching to the registered
// feature factory. The featurebundle package provides MergeFeatureSurfaceGenerated and
// MergeFeatureSurfacesWithHost for merging multiple bundles into generated merge surfaces.
func (r *Registry) BuildFeatureBundle(factoryKey string, n yaml.Node) (aipfeature.FeatureBundle, error) {
	factoryKey = strings.TrimSpace(factoryKey)
	r.mu.RLock()
	fn, ok := r.features[factoryKey]
	r.mu.RUnlock()
	if !ok {
		return aipfeature.FeatureBundle{}, fmt.Errorf("pluginreg: unknown feature plugin %q", factoryKey)
	}
	b, err := fn(n)
	if err != nil {
		return aipfeature.FeatureBundle{}, fmt.Errorf("pluginreg: feature %q: %w", factoryKey, err)
	}
	if err := b.Validate(); err != nil {
		return aipfeature.FeatureBundle{}, fmt.Errorf("pluginreg: feature %q: validate: %w", factoryKey, err)
	}
	return b, nil
}
