package runtimebundle

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/catalog"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/diagnostics"
	inframanifest "github.com/aiproxer/aiproxer/internal/infra/backendplugins/manifest"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"github.com/aiproxer/aiproxer/internal/infra/hostcredentials"
	"github.com/aiproxer/aiproxer/internal/infra/osenv"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

type backendProvisioningInput struct {
	FactoryKind string
	Secrets     backendplugin.SecretBundle
	LaunchEnv   map[string]string
	// CredentialPolicyRevision is the nonsecret upstream-credential policy
	// revision effective at capture (numbered scan bound and quota-switch
	// threshold). It binds policy changes into the physical resource
	// identity; empty when captured without a static-inference lane.
	CredentialPolicyRevision string
}

func (in backendProvisioningInput) clone() backendProvisioningInput {
	out := backendProvisioningInput{FactoryKind: in.FactoryKind, Secrets: backendplugin.SecretBundle{Values: make(map[string][]byte, len(in.Secrets.Values))}, LaunchEnv: maps.Clone(in.LaunchEnv), CredentialPolicyRevision: in.CredentialPolicyRevision}
	for k, v := range in.Secrets.Values {
		out.Secrets.Values[k] = bytes.Clone(v)
	}
	return out
}

func backendProvisioningError(reason string) error {
	return fmt.Errorf("plugins.backend_discovery.instances: %s", reason)
}

// captureBackendProvisioning runs only at an authorized delivery composition.
// No discovery/inspection path calls it; validate ALL targets before any Lookup.
func captureBackendProvisioning(cfg *config.Config, reg *pluginreg.Registry, exports []ValidatedExport, env featurehost.HostEnvironment) (map[string]backendProvisioningInput, error) {
	if err := config.ValidateBackendInstanceBindings(cfg); err != nil {
		return nil, err
	}
	if err := validateBackendSecurityProfiles(cfg, reg); err != nil {
		return nil, err
	}
	// Complete pre-lookup policy validation (requirements 1.5, 2.5): the
	// numbered scan bound and quota-switch threshold are rejected before any
	// environment value is observed. The revision string binds the effective
	// policy into the physical resource identity.
	maxEnvIndex, err := cfg.UpstreamCredentials.EffectiveMaxEnvIndex()
	if err != nil {
		return nil, err
	}
	quotaSwitch, err := cfg.UpstreamCredentials.EffectiveQuotaSwitchRetryAfter()
	if err != nil {
		return nil, err
	}
	policyRevision := "max_env_index=" + strconv.Itoa(maxEnvIndex) + ";quota_switch_retry_after=" + quotaSwitch.String()
	bindings := cfg.Plugins.BackendDiscovery.Instances
	enabled := map[string]bool{}
	for _, b := range cfg.Plugins.Backends {
		enabled[b.InstanceID()] = b.Enabled
	}
	// inferenceContracts carries the host-authoritative credential contract
	// for every enabled binding admitted to the static-inference lane. The
	// agent lane never consults it: the original local-only agent gate below
	// is preserved verbatim.
	inferenceContracts := map[string]backendplugin.CredentialContract{}
	for id, binding := range bindings {
		if !enabled[id] {
			continue
		}
		agent := false
		for _, export := range exports {
			if export.Kind != binding.Kind {
				continue
			}
			profile, registered := reg.BackendSecurityProfile(binding.Kind)
			execution, classified := reg.BackendExecutionProfile(binding.Kind)
			if export.Artifact == nil || export.Model != processhost.ProcessModelPerInstance || !registered || !classified || profile != export.Profile || execution != export.ExecProfile {
				return nil, backendProvisioningError("target posture rejected")
			}
			// The original local-only agent gate is preserved verbatim: only
			// agent runtimes under local-only access provision here.
			if profile.AccessScope == pluginreg.BackendAccessLocalOnly && execution.Class == aipsdk.BackendExecutionAgentRuntime {
				agent = true
				continue
			}
			// The separately authorized static-inference lane admits
			// approved inference exports with a host-authoritative
			// credential contract.
			contract, inference, err := authorizeStaticInferenceBinding(binding, export, profile, execution)
			if err != nil {
				return nil, err
			}
			if !inference {
				return nil, backendProvisioningError("target posture rejected")
			}
			inferenceContracts[id] = contract
		}
		if _, isInference := inferenceContracts[id]; !agent && !isInference {
			return nil, backendProvisioningError("verified target required")
		}
	}
	if err := rejectInferenceNamespaceCollisions(bindings, enabled, inferenceContracts); err != nil {
		return nil, err
	}
	if env == nil && len(bindings) > 0 {
		return nil, backendProvisioningError("delivery environment required")
	}
	out := make(map[string]backendProvisioningInput, len(bindings))
	secretBytes, executionBytes := 0, 0
	for _, id := range slices.Sorted(maps.Keys(bindings)) {
		if !enabled[id] {
			continue
		}
		binding := bindings[id]
		if contract, isInference := inferenceContracts[id]; isInference {
			input, err := captureStaticInferenceSecrets(binding.Kind, contract, env, maxEnvIndex)
			if err != nil {
				return nil, err
			}
			input.CredentialPolicyRevision = policyRevision
			for _, name := range binding.ExecutionEnv {
				value, ok := env.Lookup(name)
				executionBytes += len(value)
				if strings.ContainsRune(value, 0) || len(value) > 16<<10 || executionBytes > 64<<10 {
					return nil, backendProvisioningError("execution capture rejected")
				}
				if ok && value != "" {
					input.LaunchEnv[name] = value
				}
			}
			out[id] = input.clone()
			continue
		}
		input := backendProvisioningInput{FactoryKind: binding.Kind, Secrets: backendplugin.SecretBundle{Values: map[string][]byte{}}, LaunchEnv: map[string]string{}, CredentialPolicyRevision: policyRevision}
		for _, name := range slices.Sorted(maps.Keys(binding.SecretRefs)) {
			value, ok := env.Lookup(binding.SecretRefs[name].Env)
			secretBytes += len(value)
			if !ok || value == "" || strings.ContainsRune(value, 0) || len(value) > 64<<10 || secretBytes > 256<<10 {
				return nil, backendProvisioningError("secret capture rejected")
			}
			input.Secrets.Values[name] = []byte(value)
		}
		for _, name := range binding.ExecutionEnv {
			value, ok := env.Lookup(name)
			executionBytes += len(value)
			if strings.ContainsRune(value, 0) || len(value) > 16<<10 || executionBytes > 64<<10 {
				return nil, backendProvisioningError("execution capture rejected")
			}
			if ok && value != "" {
				input.LaunchEnv[name] = value
			}
		}
		out[id] = input.clone()
	}
	return out, nil
}

// authorizeStaticInferenceBinding admits one static-inference provisioning
// target to the separately authorized lane without observing the environment
// (requirements 2.3, 4.2, 5.3). It reports inference=false when the export is
// not an inference-lane candidate so the caller keeps the original posture
// rejection. Every inference authorization failure is fail-closed and
// secret-free. Operator secret_refs are rejected outright: inference
// credentials come only from host-approved namespace roots, never operator
// bindings; execution context overlapping the credential namespace is
// rejected so secrets can never reach the child launch environment.
func authorizeStaticInferenceBinding(binding config.BackendInstanceProvisioning, export ValidatedExport, profile pluginreg.BackendSecurityProfile, execution aipsdk.BackendExecutionProfile) (backendplugin.CredentialContract, bool, error) {
	if execution.Class != aipsdk.BackendExecutionInference || profile.CredentialMode != pluginreg.CredentialStatic {
		return backendplugin.CredentialContract{}, false, nil
	}
	if len(binding.SecretRefs) > 0 {
		return backendplugin.CredentialContract{}, false, backendProvisioningError("operator secret binding rejected")
	}
	registered, ok := inframanifest.LookupCanonicalContract(binding.Kind)
	if !ok || export.Contract == nil || *export.Contract != registered {
		return backendplugin.CredentialContract{}, false, nil
	}
	for _, name := range binding.ExecutionEnv {
		if isNumberedCredentialSlot(registered.EnvRoot, name) {
			return backendplugin.CredentialContract{}, false, backendProvisioningError("execution context overlaps credential namespace")
		}
	}
	return registered, true, nil
}

// rejectInferenceNamespaceCollisions fails closed when enabled inference
// instances of distinct kinds resolve one provider root without an explicit
// same-provider alias declaration (requirements 1.3, 4.4). Same-kind rows
// share one credential identity with per-instance ownership and stay
// allowed. It observes only nonsecret namespace roots, never secret values,
// and runs before any Lookup.
func rejectInferenceNamespaceCollisions(bindings map[string]config.BackendInstanceProvisioning, enabled map[string]bool, inferenceContracts map[string]backendplugin.CredentialContract) error {
	byRoot := map[string]map[string]struct{}{}
	for id, contract := range inferenceContracts {
		if !enabled[id] {
			continue
		}
		set := byRoot[contract.EnvRoot]
		if set == nil {
			set = map[string]struct{}{}
			byRoot[contract.EnvRoot] = set
		}
		set[bindings[id].Kind] = struct{}{}
	}
	for _, root := range slices.Sorted(maps.Keys(byRoot)) {
		kinds := slices.Sorted(maps.Keys(byRoot[root]))
		for i := range kinds {
			for j := i + 1; j < len(kinds); j++ {
				if !inframanifest.IsDeclaredSameProviderAlias(kinds[i], kinds[j]) {
					return backendProvisioningError("credential namespace collision rejected")
				}
			}
		}
	}
	return nil
}

// isNumberedCredentialSlot reports whether name is the bare credential root
// (slot 1) or one of its numbered slot suffixes root_2..root_N.
func isNumberedCredentialSlot(root, name string) bool {
	if name == root {
		return true
	}
	rest, ok := strings.CutPrefix(name, root+"_")
	if !ok {
		return false
	}
	n, err := strconv.Atoi(rest)
	return err == nil && n >= 2
}

// captureStaticInferenceSecrets resolves one inference export's bounded
// ordered slots from host-approved namespace roots through the shared
// policy-driven resolver (requirements 1.1, 1.2, 2.3). Slot identity is
// preserved sparsely: the bare root fills api_key (slot 1) and present
// suffixes fill api_key_N in ascending numeric order without stopping at
// gaps. An empty resolution fails the candidate; resolver errors are already
// secret-free and name only roots and slot indexes. Secrets land only in the
// returned bundle for the authenticated Configure channel, never in launch
// environment, argv or YAML.
func captureStaticInferenceSecrets(kind string, contract backendplugin.CredentialContract, env featurehost.HostEnvironment, maxEnvIndex int) (backendProvisioningInput, error) {
	slots, err := hostcredentials.Resolve(contract.EnvRoot, env, maxEnvIndex)
	if err != nil {
		return backendProvisioningInput{}, fmt.Errorf("plugins.backend_discovery.instances: inference credential capture for kind %q: %w", kind, err)
	}
	if len(slots) == 0 {
		return backendProvisioningInput{}, backendProvisioningError("secret capture rejected")
	}
	base := string(contract.Slot)
	input := backendProvisioningInput{FactoryKind: kind, Secrets: backendplugin.SecretBundle{Values: make(map[string][]byte, len(slots))}, LaunchEnv: map[string]string{}}
	for _, slot := range slots {
		name := base
		if slot.Index > 1 {
			name = base + "_" + strconv.Itoa(slot.Index)
		}
		input.Secrets.Values[name] = []byte(slot.Value)
	}
	return input, nil
}

// doctorProvisionedBackend reuses the stock install/Configure path for one enabled
// selected row. Its lexical install owner withdraws every resource before return.
func doctorProvisionedBackend(ctx context.Context, cfg *config.Config, id string) (PluginDoctorReport, error) {
	selected := *cfg
	selected.Plugins = cfg.Plugins
	selected.Plugins.Backends = nil
	selected.Plugins.BackendDiscovery.Instances = map[string]config.BackendInstanceProvisioning{id: cfg.Plugins.BackendDiscovery.Instances[id]}
	for _, b := range cfg.Plugins.Backends {
		if b.InstanceID() == id && b.Enabled {
			selected.Plugins.Backends = []config.PluginConfig{b}
		}
	}
	if len(selected.Plugins.Backends) == 0 {
		return PluginDoctorReport{}, backendProvisioningError("enabled selected target required")
	}
	reg, _, err := installRegistryAndRegistrations(&selected, nil)
	if err != nil {
		return PluginDoctorReport{}, err
	}
	install, err := installDiscoveredBackendExports(&selected, reg, osenv.Process{})
	if err != nil {
		return PluginDoctorReport{}, err
	}
	if install == nil {
		return PluginDoctorReport{}, backendProvisioningError("verified selected target required")
	}
	defer install.release()
	b := selected.Plugins.Backends[0]
	for _, export := range install.exports {
		if export.Kind != b.FactoryID() {
			continue
		}
		input, err := prepareDiscoveredPhysicalInput(export, b.FactoryID(), id, b.Config, backendplugin.RuntimePolicy{DisableTransportRetries: true}, connectorEvidencePolicy(cfg))
		if err != nil {
			return PluginDoctorReport{}, err
		}
		frozen := install.provisioning[id].clone()
		input.Secrets, input.LaunchEnv = frozen.Secrets, frozen.LaunchEnv
		applyProvisionedCredentialIdentity(&input, export, frozen)
		_, cleanup, err := buildDiscoveredPhysical(ctx, install.Host, export, input, defaultDialSession, new(atomic.Uint64), nil)
		if err != nil {
			return PluginDoctorReport{}, err
		}
		if cleanup != nil {
			defer func() { _ = cleanup() }()
		}
		return PluginDoctorReport{Results: []diagnostics.DoctorResult{{InstanceID: id, Kind: b.FactoryID(), State: catalog.StateActive, Reason: string(processhost.ReasonOK), Guidance: "selected instance peer-authenticated and configured", Launched: true}}}, nil
	}
	return PluginDoctorReport{}, backendProvisioningError("verified selected export required")
}
