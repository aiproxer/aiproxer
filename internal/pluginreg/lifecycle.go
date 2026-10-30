package pluginreg

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"gopkg.in/yaml.v3"
)

// BackendBuildResult is a composition-owned backend construction outcome.
// Cleanup is optional and owned by runtimebundle assembly, not by core Backend.
type BackendBuildResult struct {
	Backend execbackend.Backend
	Cleanup func() error
}

// LifecycleBackendFactory builds a backend with optional composition-owned cleanup.
// instanceID is the configured backend row id (routing/executor key); discovered
// host-backed factories use it for Activate/Configure. Static factories may ignore it.
type LifecycleBackendFactory func(instanceID string, n yaml.Node, upstreamHTTP *http.Client, deps BackendFactoryDeps) (BackendBuildResult, error)

// RegisterLifecycleBackend records a lifecycle-aware builtin backend factory.
func (r *Registry) RegisterLifecycleBackend(id string, fn LifecycleBackendFactory) error {
	return r.RegisterLifecycleBackendWithProfiles(id, fn, BackendSecurityProfile{CredentialMode: CredentialUnknown}, BackendExecutionProfile{})
}

// RegisterLifecycleBackendWithProfile records a lifecycle-aware builtin factory with security profile.
func (r *Registry) RegisterLifecycleBackendWithProfile(id string, fn LifecycleBackendFactory, profile BackendSecurityProfile) error {
	return r.registerLifecycleBackendWithSource(id, fn, profile, BackendExecutionProfile{}, BackendSourceBuiltin)
}

// RegisterLifecycleBackendWithProfiles records a lifecycle-aware builtin factory with security and execution profiles.
func (r *Registry) RegisterLifecycleBackendWithProfiles(id string, fn LifecycleBackendFactory, profile BackendSecurityProfile, execProfile BackendExecutionProfile) error {
	return r.registerLifecycleBackendWithSource(id, fn, profile, execProfile, BackendSourceBuiltin)
}

// RegisterLifecycleBackendWithSource records a lifecycle-aware builtin factory with provenance.
func (r *Registry) RegisterLifecycleBackendWithSource(id string, fn LifecycleBackendFactory, profile BackendSecurityProfile, source BackendRegistrationSource) error {
	return r.registerLifecycleBackendWithSource(id, fn, profile, BackendExecutionProfile{}, source)
}

// RegisterLifecycleBackendWithProfilesAndSource records a lifecycle-aware factory with security, execution profile, and provenance.
func (r *Registry) RegisterLifecycleBackendWithProfilesAndSource(id string, fn LifecycleBackendFactory, profile BackendSecurityProfile, execProfile BackendExecutionProfile, source BackendRegistrationSource) error {
	return r.registerLifecycleBackendWithSource(id, fn, profile, execProfile, source)
}

// RegisterDiscoveredLifecycleBackendWithProfile records a lifecycle-aware factory
// installed from a trusted discovered artifact. Inspect treats these as non-builtins.
func (r *Registry) RegisterDiscoveredLifecycleBackendWithProfile(id string, fn LifecycleBackendFactory, profile BackendSecurityProfile) error {
	return r.registerLifecycleBackendWithSource(id, fn, profile, BackendExecutionProfile{}, BackendSourceDiscovered)
}

// RegisterDiscoveredLifecycleBackendWithProfiles records a lifecycle-aware factory with security and execution profiles
// installed from a trusted discovered artifact.
func (r *Registry) RegisterDiscoveredLifecycleBackendWithProfiles(id string, fn LifecycleBackendFactory, profile BackendSecurityProfile, execProfile BackendExecutionProfile) error {
	return r.registerLifecycleBackendWithSource(id, fn, profile, execProfile, BackendSourceDiscovered)
}

func (r *Registry) registerLifecycleBackendWithSource(id string, fn LifecycleBackendFactory, profile BackendSecurityProfile, execProfile BackendExecutionProfile, source BackendRegistrationSource) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensureMaps()
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("pluginreg: RegisterLifecycleBackend: empty id")
	}
	if fn == nil {
		return fmt.Errorf("pluginreg: RegisterLifecycleBackend: nil factory")
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
		return fmt.Errorf("pluginreg: RegisterLifecycleBackend: %w", err)
	}
	if source == "" {
		source = BackendSourceBuiltin
	}
	r.lifecycleBackends[id] = fn
	// Plain BuildBackend must not invent an empty instance id for lifecycle
	// factories (discovered host-backed kinds fail closed without a row id).
	r.backends[id] = func(yaml.Node, *http.Client, BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{}, fmt.Errorf("pluginreg: backend %q requires BuildBackendWithLifecycle (instance id required)", id)
	}
	r.backendProfiles[id] = profile
	r.backendExecutionProfiles[id] = execProfile
	r.backendSources[id] = source
	if source == BackendSourceDiscovered {
		r.discovered[id] = struct{}{}
		// Default to overlap-safe; InstallDiscoveredExports may tighten for
		// shared-process exclusive models via SetBackendReloadPolicy.
		if _, ok := r.reloadPolicies[id]; !ok {
			r.reloadPolicies[id] = BackendReloadPolicy{AllowsCandidateOverlap: true}
		}
	}
	return nil
}

// SetBackendReloadPolicy updates reload/overlap policy for an already-registered factory.
func (r *Registry) SetBackendReloadPolicy(factoryID string, policy BackendReloadPolicy) error {
	if r == nil {
		return fmt.Errorf("pluginreg: nil registry")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensureMaps()
	factoryID = strings.TrimSpace(factoryID)
	if factoryID == "" {
		return fmt.Errorf("pluginreg: SetBackendReloadPolicy: empty id")
	}
	if _, ok := r.backends[factoryID]; !ok {
		return fmt.Errorf("pluginreg: SetBackendReloadPolicy: unknown backend %q", factoryID)
	}
	r.reloadPolicies[factoryID] = policy
	return nil
}

// WrapLifecycleBackend decorates an already-registered lifecycle backend factory before construction.
func (r *Registry) WrapLifecycleBackend(factoryID string, wrap func(orig LifecycleBackendFactory) LifecycleBackendFactory) error {
	if r == nil {
		return fmt.Errorf("pluginreg: nil registry")
	}
	if wrap == nil {
		return fmt.Errorf("pluginreg: nil wrap function")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensureMaps()
	factoryID = strings.TrimSpace(factoryID)
	if factoryID == "" {
		return fmt.Errorf("pluginreg: WrapLifecycleBackend: empty id")
	}
	orig, ok := r.lifecycleBackends[factoryID]
	if !ok {
		return fmt.Errorf("pluginreg: WrapLifecycleBackend: unknown backend %q", factoryID)
	}
	wrapped := wrap(orig)
	if wrapped == nil {
		return fmt.Errorf("pluginreg: WrapLifecycleBackend: wrap returned nil factory for %q", factoryID)
	}
	r.lifecycleBackends[factoryID] = wrapped
	return nil
}

// BuildBackendWithLifecycle constructs a backend and optional cleanup for assembly.
// instanceID is the configured backend instance id for the enabled row.
func (r *Registry) BuildBackendWithLifecycle(
	factoryID string,
	instanceID string,
	n yaml.Node,
	upstreamHTTP *http.Client,
	deps BackendFactoryDeps,
) (BackendBuildResult, error) {
	factoryID = strings.TrimSpace(factoryID)
	instanceID = strings.TrimSpace(instanceID)
	r.mu.RLock()
	lf, hasLife := r.lifecycleBackends[factoryID]
	fn, ok := r.backends[factoryID]
	r.mu.RUnlock()
	if !ok {
		return BackendBuildResult{}, fmt.Errorf("pluginreg: unknown backend plugin %q", factoryID)
	}
	if hasLife {
		return lf(instanceID, n, upstreamHTTP, deps)
	}
	be, err := fn(n, upstreamHTTP, deps)
	if err != nil {
		return BackendBuildResult{}, err
	}
	return BackendBuildResult{Backend: be}, nil
}
