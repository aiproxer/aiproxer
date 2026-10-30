package runtimebundle

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/adapter"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/catalog"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/discovery"
	inframanifest "github.com/aiproxer/aiproxer/internal/infra/backendplugins/manifest"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	sdkmanifest "github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/manifest"
	"gopkg.in/yaml.v3"
)

// ExecuteSession is the host-facing configured plugin session (adapter seam).
type ExecuteSession = adapter.ExecuteSession

// ValidatedExport is one catalog-approved factory export ready for generic registration.
type ValidatedExport struct {
	Kind        string
	Profile     pluginreg.BackendSecurityProfile
	ExecProfile aipsdk.BackendExecutionProfile
	Artifact    *trust.VerifiedArtifact
	Model       processhost.ProcessModel
	Sharing     processhost.SharingOptions
	// Contract is the admitted manifest credential contract for static
	// provider-secret exports (nil for non-static exports). The
	// static-inference provisioning lane resolves host-approved namespace
	// roots from here, never from operator bindings.
	Contract *backendplugin.CredentialContract
	// Manifest is the packaged manifest this export was admitted from. The
	// activation-time descriptor comparison (CompareDescriptorToManifest)
	// checks the live runtime descriptor against it.
	Manifest sdkmanifest.Manifest
}

// DialSessionRequest is the peer-authenticated configure input for a discovered factory.
type DialSessionRequest struct {
	Conn        net.Conn
	Peer        processhost.PeerIdentity
	Generation  uint64
	Secrets     backendplugin.SecretBundle
	ConfigYAML  []byte
	InstanceID  string
	FactoryKind string
	Policy      backendplugin.RuntimePolicy
}

// DialSessionFunc establishes a configured ExecuteSession after peer auth.
// Tests inject an in-process FakeService bridge; production uses adapter.DialConfiguredSession.
type DialSessionFunc func(ctx context.Context, req DialSessionRequest) (ExecuteSession, backendplugin.ResolvedProfile, error)

// DiscoveredInstallOptions configures InstallDiscoveredExports.
type DiscoveredInstallOptions struct {
	DialSession   DialSessionFunc
	RuntimePolicy backendplugin.RuntimePolicy
	// ConnectorEvidencePolicy selects the host's reaction to connector
	// economic evidence it cannot admit. The default preserves the client
	// session with an explicit, durable degradation.
	ConnectorEvidencePolicy execbackend.ConnectorEconomicEvidencePolicy
	provisioning            map[string]backendProvisioningInput
}

// DiscoveredPluginInstall is an optional BuildOptions payload that installs
// validated catalog exports before backend construction.
type DiscoveredPluginInstall struct {
	Host    *processhost.Host
	Exports []ValidatedExport
	Options DiscoveredInstallOptions
	// Trusted holds every verified artifact bound to the shared staging root.
	// Exports reference the same pointers; the ownership holder must close all
	// of them before removing staging (Windows locks staged executables).
	Trusted []*trust.VerifiedArtifact
}

// InstallDiscoveredExports registers each validated export as a host-backed
// lifecycle factory. Duplicate kinds and collisions with already-registered
// builtins fail closed. There is no connector-specific switch.
func InstallDiscoveredExports(
	reg *pluginreg.Registry,
	host *processhost.Host,
	exports []ValidatedExport,
	opt DiscoveredInstallOptions,
) error {
	return installDiscoveredExportsWithPool(reg, host, exports, opt, nil)
}

// installDiscoveredExportsWithPool is the private composition seam used by
// discovered-install preparation. The pool is captured lexically by each
// factory closure; public callers retain the established isolated path.
func installDiscoveredExportsWithPool(
	reg *pluginreg.Registry,
	host *processhost.Host,
	exports []ValidatedExport,
	opt DiscoveredInstallOptions,
	resourcePool *backendResourcePool,
) error {
	if reg == nil {
		return fmt.Errorf("runtimebundle: InstallDiscoveredExports: nil registry")
	}
	if host == nil {
		return fmt.Errorf("runtimebundle: InstallDiscoveredExports: nil process host")
	}
	dial := opt.DialSession
	if dial == nil {
		dial = defaultDialSession
	}
	policy := opt.RuntimePolicy
	policy.AllowedEnvNames = append([]string(nil), policy.AllowedEnvNames...)
	policy.DisableTransportRetries = true

	// activationSeq mints unique per-instance Host Activate handles (install-owned).
	activationSeq := new(atomic.Uint64)

	seen := make(map[string]struct{}, len(exports))
	for _, exp := range exports {
		kind := strings.TrimSpace(exp.Kind)
		if kind == "" {
			return fmt.Errorf("runtimebundle: InstallDiscoveredExports: empty export kind")
		}
		if _, dup := seen[kind]; dup {
			return fmt.Errorf("runtimebundle: InstallDiscoveredExports: duplicate export kind %q", kind)
		}
		seen[kind] = struct{}{}
		if reg.HasBackend(kind) {
			return fmt.Errorf("runtimebundle: InstallDiscoveredExports: duplicate backend registration: %s", kind)
		}
		if exp.Artifact == nil {
			return fmt.Errorf("runtimebundle: InstallDiscoveredExports: kind %q: nil verified artifact", kind)
		}
		if exp.Model != processhost.ProcessModelPerInstance && exp.Model != processhost.ProcessModelSharedArtifact {
			return fmt.Errorf("runtimebundle: InstallDiscoveredExports: kind %q: invalid process model %q", kind, exp.Model)
		}

		export := exp
		factoryKind := kind
		var fn pluginreg.LifecycleBackendFactory
		if export.Model == processhost.ProcessModelPerInstance {
			// Only overlap-safe per-instance factories capture the private pool;
			// shared-artifact factories retain their existing isolated path.
			pool := resourcePool
			fn = func(instanceID string, n yaml.Node, _ *http.Client, _ pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
				return buildDiscoveredBackend(host, export, factoryKind, instanceID, n, dial, policy, opt.ConnectorEvidencePolicy, activationSeq, opt.provisioning[instanceID].clone(), pool)
			}
		} else {
			fn = func(instanceID string, n yaml.Node, _ *http.Client, _ pluginreg.BackendFactoryDeps) (pluginreg.BackendBuildResult, error) {
				return buildDiscoveredBackend(host, export, factoryKind, instanceID, n, dial, policy, opt.ConnectorEvidencePolicy, activationSeq, opt.provisioning[instanceID].clone(), nil)
			}
		}
		if err := reg.RegisterDiscoveredLifecycleBackendWithProfiles(kind, fn, export.Profile, export.ExecProfile); err != nil {
			return err
		}
		// Shared-process exclusive kinds cannot overlap candidate/active handles.
		overlap := export.Model != processhost.ProcessModelSharedArtifact
		if err := reg.SetBackendReloadPolicy(kind, pluginreg.BackendReloadPolicy{
			AllowsCandidateOverlap: overlap,
		}); err != nil {
			return err
		}
	}
	return nil
}

func buildDiscoveredBackend(
	host *processhost.Host,
	export ValidatedExport,
	factoryKind, instanceID string,
	n yaml.Node,
	dial DialSessionFunc,
	policy backendplugin.RuntimePolicy,
	evidencePolicy execbackend.ConnectorEconomicEvidencePolicy,
	activationSeq *atomic.Uint64,
	provisioning backendProvisioningInput,
	resourcePool *backendResourcePool,
) (pluginreg.BackendBuildResult, error) {
	input, err := prepareDiscoveredPhysicalInput(export, factoryKind, instanceID, n, policy, evidencePolicy)
	if err != nil {
		return pluginreg.BackendBuildResult{}, err
	}
	frozen := provisioning.clone()
	if frozen.FactoryKind != "" && frozen.FactoryKind != factoryKind {
		return pluginreg.BackendBuildResult{}, backendProvisioningError("factory binding mismatch")
	}
	input.Secrets, input.LaunchEnv = frozen.Secrets, frozen.LaunchEnv
	applyProvisionedCredentialIdentity(&input, export, frozen)
	if resourcePool == nil {
		backend, cleanup, err := buildDiscoveredPhysical(context.Background(), host, export, input, dial, activationSeq, func(generation uint64) {
			_ = host.InvalidateProcessGeneration(generation)
		})
		if err != nil {
			return pluginreg.BackendBuildResult{}, err
		}
		return pluginreg.BackendBuildResult{Backend: backend, Cleanup: cleanup}, nil
	}

	identity, shareable, identityErr := physicalIdentity(input)
	if identityErr != nil || !shareable {
		// An incomplete identity is deliberately non-shareable. Preserve the
		// existing generation-local construction path rather than turning an
		// optimization eligibility failure into a new runtime error.
		backend, cleanup, err := buildDiscoveredPhysical(context.Background(), host, export, input, dial, activationSeq, func(generation uint64) {
			_ = host.InvalidateProcessGeneration(generation)
		})
		if err != nil {
			return pluginreg.BackendBuildResult{}, err
		}
		return pluginreg.BackendBuildResult{Backend: backend, Cleanup: cleanup}, nil
	}

	return resourcePool.Acquire(context.Background(), identity, func(buildCtx context.Context, incarnation uint64) (execbackend.Backend, func() error, error) {
		return buildDiscoveredPhysical(buildCtx, host, export, input, dial, activationSeq, func(generation uint64) {
			resourcePool.Invalidate(identity, incarnation)
			_ = host.InvalidateProcessGeneration(generation)
		})
	})
}

// applyProvisionedCredentialIdentity binds the admitted credential
// namespace (manifest authority, known without any lookup) and the
// capture-time policy revision into the physical input, so distinct
// credential snapshots never share one pooled resource. Non-static exports
// carry no contract and keep empty namespace fields.
func applyProvisionedCredentialIdentity(input *backendResourcePhysicalInput, export ValidatedExport, provisioning backendProvisioningInput) {
	if export.Contract != nil {
		input.CredentialEnvRoot = export.Contract.EnvRoot
		input.CredentialQuotaDomain = export.Contract.QuotaDomain
		input.CredentialSlot = string(export.Contract.Slot)
		input.CredentialSelection = string(export.Contract.Selection)
	}
	input.CredentialPolicyRevision = provisioning.CredentialPolicyRevision
}

// prepareDiscoveredPhysicalInput freezes the effective input used for both
// identity and physical construction. Provider-specific parsing remains in the
// connector; this boundary only preserves the opaque Configure bytes and the
// host-owned policy/artifact/process facts.
func prepareDiscoveredPhysicalInput(
	export ValidatedExport,
	factoryKind, instanceID string,
	n yaml.Node,
	policy backendplugin.RuntimePolicy,
	evidencePolicy execbackend.ConnectorEconomicEvidencePolicy,
) (backendResourcePhysicalInput, error) {
	instanceID = strings.TrimSpace(instanceID)
	if instanceID == "" {
		return backendResourcePhysicalInput{}, fmt.Errorf("runtimebundle: discovered backend %q: empty instance id", factoryKind)
	}
	raw, err := encodeOpaqueYAML(n)
	if err != nil {
		return backendResourcePhysicalInput{}, fmt.Errorf("runtimebundle: discovered backend %q: encode config: %w", factoryKind, err)
	}
	artifactDigest := ""
	if export.Artifact != nil {
		artifactDigest = export.Artifact.DigestHex
	}
	policy.AllowedEnvNames = append([]string(nil), policy.AllowedEnvNames...)
	return backendResourcePhysicalInput{
		InstanceID:              instanceID,
		FactoryKind:             factoryKind,
		ArtifactDigest:          artifactDigest,
		ProcessModel:            export.Model,
		ConfigureYAML:           raw,
		RuntimePolicy:           policy,
		ConnectorEvidencePolicy: evidencePolicy,
	}, nil
}

// buildDiscoveredPhysical performs one new host activation and adapter build.
// The caller supplies the context so pooled construction is owned by the pool,
// while the public isolated path retains its existing background lifetime.
func buildDiscoveredPhysical(
	ctx context.Context,
	host *processhost.Host,
	export ValidatedExport,
	input backendResourcePhysicalInput,
	dial DialSessionFunc,
	activationSeq *atomic.Uint64,
	invalidateGeneration func(uint64),
) (execbackend.Backend, func() error, error) {
	// Per-instance overlap across generations must not collide on Host's
	// InstanceID map: mint a unique activation handle while dialing with the
	// logical configured instance id.
	hostInstanceID := input.InstanceID
	if export.Model == processhost.ProcessModelPerInstance {
		hostInstanceID = fmt.Sprintf("%s#%d", input.InstanceID, activationSeq.Add(1))
	}

	var session ExecuteSession
	var profile backendplugin.ResolvedProfile
	act, err := host.Activate(ctx, processhost.ActivateRequest{
		InstanceID:  hostInstanceID,
		Artifact:    export.Artifact,
		Model:       export.Model,
		Sharing:     export.Sharing,
		FactoryKind: input.FactoryKind,
		ConfigYAML:  input.ConfigureYAML,
		Secrets:     input.Secrets,
		LaunchEnv:   input.LaunchEnv,
		Policy:      input.RuntimePolicy,
		DialAndConfigure: func(ctx context.Context, conn net.Conn, peer processhost.PeerIdentity, generation uint64, secrets backendplugin.SecretBundle, configYAML []byte) error {
			sess, prof, err := dial(ctx, DialSessionRequest{
				Conn:        conn,
				Peer:        peer,
				Generation:  generation,
				Secrets:     secrets,
				ConfigYAML:  configYAML,
				InstanceID:  input.InstanceID,
				FactoryKind: input.FactoryKind,
				Policy:      input.RuntimePolicy,
			})
			if err != nil {
				return err
			}
			// Assign only on success: a failed dial may return a typed-nil
			// session, which a nil-interface comparison cannot see, and the
			// rejection path below must observe a true nil to skip disposal
			// instead of panicking on Close.
			session = sess
			profile = prof
			return nil
		},
	})
	if err != nil {
		disposeRejectedSession(session)
		return execbackend.Backend{}, nil, fmt.Errorf("runtimebundle: discovered backend %q instance %q: activate: %w", input.FactoryKind, input.InstanceID, err)
	}
	if session == nil {
		return execbackend.Backend{}, nil, activationFailureError(
			fmt.Sprintf("runtimebundle: discovered backend %q instance %q: nil session after configure", input.FactoryKind, input.InstanceID),
			act.Cleanup())
	}
	if err := verifyActivationDescriptor(ctx, export, session); err != nil {
		_ = session.Close(context.Background())
		return execbackend.Backend{}, nil, activationFailureError(
			fmt.Sprintf("runtimebundle: discovered backend %q instance %q: activation descriptor rejected", input.FactoryKind, input.InstanceID),
			activationCleanupError(act.Cleanup(), err))
	}

	generation := act.Generation
	// Routes address the configured instance id, which may differ from the
	// factory kind a plugin advertises; without it "<instance>:<model>"
	// selectors fall back to the default route.
	prefixes := []string{input.InstanceID}
	if len(profile.RoutePrefixes) == 0 {
		prefixes = append(prefixes, input.FactoryKind)
	}
	for _, p := range profile.RoutePrefixes {
		if !slices.Contains(prefixes, p) {
			prefixes = append(prefixes, p)
		}
	}
	neg := backendplugin.Negotiation{Compatible: true}
	if ns, ok := session.(adapter.NegotiatedSession); ok {
		neg = ns.Negotiation()
	}
	br := adapter.Build(session, profile, adapter.Options{
		InstanceID:                      input.InstanceID,
		RoutePrefixes:                   prefixes,
		Negotiation:                     neg,
		ConnectorEconomicEvidencePolicy: input.ConnectorEvidencePolicy,
		InvalidateGeneration: func() {
			if invalidateGeneration != nil {
				invalidateGeneration(generation)
				return
			}
			_ = host.InvalidateProcessGeneration(generation)
		},
	})
	cleanup := func() error {
		var out error
		if br != nil {
			out = errors.Join(out, br.Cleanup())
		}
		if act.Cleanup != nil {
			out = errors.Join(out, act.Cleanup())
		}
		return out
	}
	if br == nil {
		return execbackend.Backend{}, nil, activationFailureError(
			fmt.Sprintf("runtimebundle: discovered backend %q instance %q: nil adapter build", input.FactoryKind, input.InstanceID),
			cleanup())
	}
	return br.Backend, cleanup, nil
}

// A rejected activation owns no reusable session. Close with an already
// canceled context takes the host session's transport-only cancellation path;
// it must not depend on a successful RPC to the invalidated child.
func disposeRejectedSession(session ExecuteSession) {
	if session == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = session.Close(ctx)
}

// activationCleanupError reports the failure of a failed activation.
//
// The activation cleanup carries the generation's terminal cause: when the
// connector outran its diagnostic budget, that budget is why the generation
// ended and the secondary failure is only its symptom. The budget cause
// therefore keeps precedence, while the secondary failure stays matchable so
// nothing about the activation is lost.
func activationCleanupError(cleanupErr, secondary error) error {
	switch {
	case cleanupErr == nil:
		return secondary
	case secondary == nil:
		return cleanupErr
	case errors.Is(cleanupErr, processhost.ReasonConnectorOutputLimitExceeded):
		return fmt.Errorf("%w: %w", cleanupErr, secondary)
	default:
		return fmt.Errorf("%w: %w", secondary, cleanupErr)
	}
}

// activationFailureError reports a failed activation. The cause is appended
// only when there is one, so a nil cause never produces a malformed message.
func activationFailureError(msg string, cause error) error {
	if cause == nil {
		return errors.New(msg)
	}
	return fmt.Errorf("%s: %w", msg, cause)
}

func defaultDialSession(ctx context.Context, req DialSessionRequest) (ExecuteSession, backendplugin.ResolvedProfile, error) {
	sess, profile, err := adapter.DialConfiguredSession(ctx, req.Conn, req.InstanceID, req.FactoryKind, req.ConfigYAML, req.Secrets, req.Policy)
	if err != nil {
		// Normalize the failure to a true nil session: the adapter's
		// typed-nil would otherwise pass a nil-interface comparison and
		// panic on Close in the rejection path.
		return nil, backendplugin.ResolvedProfile{}, err
	}
	return sess, profile, nil
}

// verifyActivationDescriptor compares the live runtime descriptor against the
// packaged manifest (CompareDescriptorToManifest): missing/extra exports and
// credential posture disagreements fail the candidate before adapter
// construction, releasing candidate resources without publication. Sessions
// that cannot report a descriptor (in-process fakes) skip the check; every
// other describe failure or mismatch fails closed with a secret-free error.
func verifyActivationDescriptor(ctx context.Context, export ValidatedExport, session ExecuteSession) error {
	reporter, ok := session.(adapter.DescriptorReporter)
	if !ok {
		return nil
	}
	descriptor, err := reporter.Describe(ctx)
	if err != nil {
		return fmt.Errorf("runtimebundle: activation describe: %w", err)
	}
	return inframanifest.CompareDescriptorToManifest(export.Manifest, descriptor)
}

func encodeOpaqueYAML(n yaml.Node) ([]byte, error) {
	if n.Kind == 0 {
		return nil, nil
	}
	return yaml.Marshal(&n)
}

// CollectInstallableExports converts a catalog snapshot into ValidatedExport values
// for kinds in StateDiscovered with verified artifacts and declared process models.
func CollectInstallableExports(
	snap catalog.Snapshot,
	trustBySafe map[string]trust.VerifyResult,
	discovered []discovery.Descriptor,
) ([]ValidatedExport, error) {
	bySafe := make(map[string]discovery.Descriptor, len(discovered))
	for _, d := range discovered {
		bySafe[d.SafeID] = d
	}
	var out []ValidatedExport
	for _, e := range snap.Entries {
		if (e.State != catalog.StateDiscovered && e.State != catalog.StateConfigured) || strings.TrimSpace(e.ExportKind) == "" {
			continue
		}
		tr, ok := trustBySafe[e.SafeID]
		if !ok || tr.Reason != trust.ReasonOK || tr.Artifact == nil {
			return nil, fmt.Errorf("runtimebundle: CollectInstallableExports: kind %q: missing verified artifact", e.ExportKind)
		}
		d, ok := bySafe[e.SafeID]
		if !ok {
			return nil, fmt.Errorf("runtimebundle: CollectInstallableExports: kind %q: missing discovery descriptor", e.ExportKind)
		}
		exp, ok := findManifestExport(d.Manifest, e.ExportKind)
		if !ok {
			return nil, fmt.Errorf("runtimebundle: CollectInstallableExports: kind %q: missing manifest export", e.ExportKind)
		}
		sharing := processhost.SharingOptions{}
		if exp.ProcessSharing == backendplugin.ProcessSharingSharedArtifact {
			sharing = processhost.SharingOptions{IsolationDeclared: true, ConcurrencyDeclared: true}
		}
		model, err := processhost.ProcessModelFromSharing(exp.ProcessSharing, sharing)
		if err != nil {
			return nil, fmt.Errorf("runtimebundle: CollectInstallableExports: kind %q: %w", e.ExportKind, err)
		}
		profile, err := securityProfileFromExport(exp)
		if err != nil {
			return nil, fmt.Errorf("runtimebundle: CollectInstallableExports: kind %q: %w", e.ExportKind, err)
		}
		if err := inframanifest.AdmitStaticExportWithCustomRoots(exp, customCompatibleCredentialRoots()); err != nil {
			return nil, fmt.Errorf("runtimebundle: CollectInstallableExports: kind %q: %w", e.ExportKind, err)
		}
		out = append(out, ValidatedExport{
			Kind:        e.ExportKind,
			Profile:     profile,
			ExecProfile: aipsdk.BackendExecutionProfile{Class: exp.ExecutionClass},
			Artifact:    tr.Artifact,
			Model:       model,
			Sharing:     sharing,
			Contract:    cloneCredentialContract(exp.CredentialContract),
			Manifest:    d.Manifest,
		})
	}
	return out, nil
}

func findManifestExport(m sdkmanifest.Manifest, kind string) (sdkmanifest.Export, bool) {
	for _, e := range m.Exports {
		if e.Kind == kind {
			return e, true
		}
	}
	return sdkmanifest.Export{}, false
}

// cloneCredentialContract copies an admitted manifest contract so the
// validated export owns its namespace identity independent of the discovery
// snapshot lifetime. A nil contract stays nil (non-static exports).
func cloneCredentialContract(c *backendplugin.CredentialContract) *backendplugin.CredentialContract {
	if c == nil {
		return nil
	}
	out := *c
	return &out
}

// customCompatibleCredentialRoots enumerates the in-process compatible
// factory kinds whose credential namespace is bound per instance
// (requirement 1.4), not fixed in the manifest registry. The IDs come from
// the standardplugins-owned constants so no generic compatible-kind literal
// lives in host admission code.
func customCompatibleCredentialRoots() []string {
	return []string{
		standardplugins.CustomOpenAILegacyCompatibleID,
		standardplugins.CustomOpenAIResponsesCompatibleID,
		standardplugins.CustomAnthropicCompatibleID,
		standardplugins.CustomOpenResponsesCompatibleID,
		standardplugins.CustomSystemOneCompatibleID,
	}
}

func securityProfileFromExport(exp sdkmanifest.Export) (pluginreg.BackendSecurityProfile, error) {
	if err := backendplugin.ValidateCredentialMode(exp.CredentialMode); err != nil {
		return pluginreg.BackendSecurityProfile{}, err
	}
	if err := backendplugin.ValidateAccessScope(exp.AccessScope); err != nil {
		return pluginreg.BackendSecurityProfile{}, err
	}
	return pluginreg.BackendSecurityProfile{
		CredentialMode: pluginreg.BackendCredentialMode(exp.CredentialMode),
		AccessScope:    pluginreg.BackendAccessScope(exp.AccessScope),
	}, nil
}
