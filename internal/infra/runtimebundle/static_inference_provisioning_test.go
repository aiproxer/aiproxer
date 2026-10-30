package runtimebundle

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	inframanifest "github.com/aiproxer/aiproxer/internal/infra/backendplugins/manifest"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// inferenceProvisioningFixture builds an authorized static-inference target:
// an openrouter export (registered quota_pre_output namespace
// OPENROUTER_API_KEY) bound without operator secret_refs. Host-approved
// namespace roots are the sole credential source; SecretRefs stay empty.
func inferenceProvisioningFixture(t *testing.T, mode string) (*config.Config, *pluginreg.Registry, []ValidatedExport, backendplugin.CredentialContract) {
	t.Helper()
	contract, ok := inframanifest.LookupCanonicalContract("openrouter")
	if !ok {
		t.Fatal("openrouter must be registered")
	}
	cfg := &config.Config{Access: config.AccessConfig{Mode: mode}, Plugins: config.PluginsConfig{
		Backends: []config.PluginConfig{{ID: "or", Kind: "openrouter", Enabled: true}},
		BackendDiscovery: config.BackendDiscoveryConfig{Enabled: true, Instances: map[string]config.BackendInstanceProvisioning{
			"or": {Kind: "openrouter", ExecutionEnv: []string{"HOME"}},
		}},
	}}
	reg := pluginreg.NewRegistry()
	exports := []ValidatedExport{{
		Kind:        "openrouter",
		Profile:     pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialStatic, AccessScope: pluginreg.BackendAccessAny},
		ExecProfile: aipsdk.BackendExecutionProfile{Class: aipsdk.BackendExecutionInference},
		Artifact:    &trust.VerifiedArtifact{DigestHex: "verified"},
		Model:       processhost.ProcessModelPerInstance,
		Contract:    &contract,
	}}
	host := processhost.NewHost(processhost.Config{})
	t.Cleanup(func() { _ = host.Close() })
	if err := InstallDiscoveredExports(reg, host, exports, DiscoveredInstallOptions{}); err != nil {
		t.Fatal(err)
	}
	return cfg, reg, exports, contract
}

// TestStaticInferenceCapture_OrderedBoundedSlotsViaConfigureSecrets proves
// requirement 2.3/4.2: the separately authorized static-inference lane
// captures sparse approved-root slots into ordered Configure Secrets
// (api_key, api_key_3 preserve numeric slot identity) while LaunchEnv carries
// only allowlisted non-secret execution context. No lookup may name anything
// outside the approved namespace or the execution allowlist.
func TestStaticInferenceCapture_OrderedBoundedSlotsViaConfigureSecrets(t *testing.T) {
	t.Parallel()
	cfg, reg, exports, _ := inferenceProvisioningFixture(t, "single_user")
	env := &provisioningEnv{values: map[string]string{
		"OPENROUTER_API_KEY":   "or-key-one",
		"OPENROUTER_API_KEY_3": "or-key-three",
		"HOME":                 "/home/op",
		"UNREQUESTED":          "not-forwarded",
	}}
	got, err := captureBackendProvisioning(cfg, reg, exports, env)
	if err != nil {
		t.Fatalf("inference capture: %v", err)
	}
	input := got["or"]
	if len(input.Secrets.Values) != 2 || string(input.Secrets.Values["api_key"]) != "or-key-one" || string(input.Secrets.Values["api_key_3"]) != "or-key-three" {
		t.Fatalf("ordered slots = %v, want api_key + api_key_3", secretNames(input.Secrets))
	}
	if len(input.LaunchEnv) != 1 || input.LaunchEnv["HOME"] != "/home/op" {
		t.Fatalf("launch env = %v, want only HOME", input.LaunchEnv)
	}
	for _, name := range env.lookups {
		if name == "UNREQUESTED" {
			t.Fatal("lookup outside the authorized namespace/allowlist")
		}
		if strings.Contains(name, "or-key") {
			t.Fatal("lookup carries secret material")
		}
	}
	for _, v := range input.LaunchEnv {
		if v == "or-key-one" || v == "or-key-three" {
			t.Fatal("secret value reached child launch env")
		}
	}
}

// TestStaticInferenceCapture_MissingKeysFailClosedAfterAuthorizedLookup proves
// requirement 2.5 for the inference lane: with no approved-root values, the
// candidate fails closed with a secret-free error only after the authorized
// scan ran (lookups prove validation preceded failure, not a posture dodge).
func TestStaticInferenceCapture_MissingKeysFailClosedAfterAuthorizedLookup(t *testing.T) {
	t.Parallel()
	cfg, reg, exports, _ := inferenceProvisioningFixture(t, "single_user")
	env := &provisioningEnv{values: map[string]string{"HOME": "/home/op"}}
	_, err := captureBackendProvisioning(cfg, reg, exports, env)
	if err == nil {
		t.Fatal("missing inference keys must fail candidate composition")
	}
	if len(env.lookups) == 0 {
		t.Fatal("rejection must follow the authorized namespace scan, not precede validation")
	}
	if strings.Contains(err.Error(), "or-key") {
		t.Fatalf("secret-bearing capture error: %v", err)
	}
}

// TestStaticInferenceCapture_MultiUserApproved proves requirement 4.2: a
// multi-user-safe service API-key export (openrouter is host-approved for
// shared use) provisions in multi_user mode through the same lane.
func TestStaticInferenceCapture_MultiUserApproved(t *testing.T) {
	t.Parallel()
	cfg, reg, exports, _ := inferenceProvisioningFixture(t, "multi_user")
	env := &provisioningEnv{values: map[string]string{"OPENROUTER_API_KEY": "or-shared-key"}}
	got, err := captureBackendProvisioning(cfg, reg, exports, env)
	if err != nil {
		t.Fatalf("approved multi-user inference capture: %v", err)
	}
	if string(got["or"].Secrets.Values["api_key"]) != "or-shared-key" {
		t.Fatalf("slots = %v", secretNames(got["or"].Secrets))
	}
}

func secretNames(bundle backendplugin.SecretBundle) []string {
	out := make([]string, 0, len(bundle.Values))
	for name := range bundle.Values {
		out = append(out, name)
	}
	return out
}

// TestStaticInferenceCapture_PreLookupValidationNeverObservesEnvironment
// proves design step 1 for the inference lane: every authorization failure
// (invalid scan bound, operator secret binding, unregistered/drifted
// contract, misconfigured _1, oversized value) fails with a secret-free
// error; pure authorization failures run zero Lookups, while value-level
// failures surface only after the authorized scan ran.
func TestStaticInferenceCapture_PreLookupValidationNeverObservesEnvironment(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		setup       func(cfg *config.Config, exports []ValidatedExport)
		values      map[string]string
		wantErr     string
		wantLookups bool
	}{
		{
			name:        "invalid scan bound",
			setup:       func(cfg *config.Config, _ []ValidatedExport) { cfg.UpstreamCredentials.MaxEnvIndex = 999 },
			values:      map[string]string{"OPENROUTER_API_KEY": "or-key-one"},
			wantLookups: false,
		},
		{
			name: "operator secret binding rejected",
			setup: func(cfg *config.Config, _ []ValidatedExport) {
				binding := cfg.Plugins.BackendDiscovery.Instances["or"]
				binding.SecretRefs = map[string]config.BackendSecretRef{"api_key": {Env: "OPENROUTER_API_KEY"}}
				cfg.Plugins.BackendDiscovery.Instances["or"] = binding
			},
			values:      map[string]string{"OPENROUTER_API_KEY": "or-key-one"},
			wantErr:     "operator secret binding rejected",
			wantLookups: false,
		},
		{
			name: "unregistered static kind rejected",
			setup: func(cfg *config.Config, exports []ValidatedExport) {
				contract, _ := inframanifest.LookupCanonicalContract("openrouter")
				exports[0].Kind = "future-acme-inference"
				exports[0].Contract = &contract
				cfg.Plugins.Backends[0].Kind = "future-acme-inference"
				binding := cfg.Plugins.BackendDiscovery.Instances["or"]
				binding.Kind = "future-acme-inference"
				cfg.Plugins.BackendDiscovery.Instances["or"] = binding
			},
			values:      map[string]string{"OPENROUTER_API_KEY": "or-key-one"},
			wantLookups: false,
		},
		{
			name: "unverified artifact rejected",
			setup: func(_ *config.Config, exports []ValidatedExport) {
				exports[0].Artifact = nil
			},
			values:      map[string]string{"OPENROUTER_API_KEY": "or-key-one"},
			wantLookups: false,
		},
		{
			name:        "misconfigured _1 rejected secret-free",
			setup:       func(*config.Config, []ValidatedExport) {},
			values:      map[string]string{"OPENROUTER_API_KEY_1": "or-key-misconfigured"},
			wantLookups: true,
		},
		{
			name:        "oversized value rejected secret-free",
			setup:       func(*config.Config, []ValidatedExport) {},
			values:      map[string]string{"OPENROUTER_API_KEY": strings.Repeat("s", 65537)},
			wantLookups: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg, reg, exports, _ := inferenceProvisioningFixture(t, "single_user")
			tc.setup(cfg, exports)
			env := &provisioningEnv{values: tc.values}
			_, err := captureBackendProvisioning(cfg, reg, exports, env)
			if err == nil {
				t.Fatal("want authorization/capture failure")
			}
			if tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			if len(env.lookups) == 0 == tc.wantLookups {
				t.Fatalf("lookups=%v wantLookups=%v", env.lookups, tc.wantLookups)
			}
			for _, value := range tc.values {
				if len(value) > 64 && strings.Contains(err.Error(), value[:64]) {
					t.Fatalf("secret-bearing error: %v", err)
				}
			}
		})
	}
}

// TestStaticInferenceCollisionGateRejectsUnaliasedRoots is the direct helper
// proof for the namespace gate: production contracts always equal their fixed
// registration (unique-or-aliased by registry construction), so the gate is
// defense-in-depth against future namespace edits. Distinct kinds sharing one
// root without a declared alias fail; the declared OpenAI alias pair passes.
func TestStaticInferenceCollisionGateRejectsUnaliasedRoots(t *testing.T) {
	t.Parallel()
	orContract, ok := inframanifest.LookupCanonicalContract("openrouter")
	if !ok {
		t.Fatal("openrouter must be registered")
	}
	hfContract, ok := inframanifest.LookupCanonicalContract("huggingface")
	if !ok {
		t.Fatal("huggingface must be registered")
	}
	colliding := hfContract
	colliding.EnvRoot = orContract.EnvRoot
	bindings := map[string]config.BackendInstanceProvisioning{
		"a": {Kind: "openrouter"},
		"b": {Kind: "huggingface"},
	}
	enabled := map[string]bool{"a": true, "b": true}
	if err := rejectInferenceNamespaceCollisions(bindings, enabled,
		map[string]backendplugin.CredentialContract{"a": orContract, "b": colliding}); err == nil {
		t.Fatal("distinct kinds sharing one root without an alias must fail")
	}
	legacyContract, _ := inframanifest.LookupCanonicalContract("openai-legacy")
	responsesContract, _ := inframanifest.LookupCanonicalContract("openai-responses")
	aliased := map[string]config.BackendInstanceProvisioning{
		"a": {Kind: "openai-legacy"},
		"b": {Kind: "openai-responses"},
	}
	if err := rejectInferenceNamespaceCollisions(aliased, enabled,
		map[string]backendplugin.CredentialContract{"a": legacyContract, "b": responsesContract}); err != nil {
		t.Fatalf("declared same-provider alias must stay admitted: %v", err)
	}
}

// TestStaticInferenceCapture_BindsPolicyRevision proves the effective
// upstream-credential policy travels with the captured input so policy
// changes invalidate pooled physical resources.
func TestStaticInferenceCapture_BindsPolicyRevision(t *testing.T) {
	t.Parallel()
	cfg, reg, exports, _ := inferenceProvisioningFixture(t, "single_user")
	env := &provisioningEnv{values: map[string]string{"OPENROUTER_API_KEY": "or-key-one"}}
	got, err := captureBackendProvisioning(cfg, reg, exports, env)
	if err != nil {
		t.Fatal(err)
	}
	revision := got["or"].CredentialPolicyRevision
	if !strings.Contains(revision, "max_env_index=20") || !strings.Contains(revision, "quota_switch_retry_after=") {
		t.Fatalf("policy revision = %q", revision)
	}
}

// TestStaticInferenceCapture_AgentGateIntact proves the original
// local_only+agent_runtime condition still owns the agent lane: an agent
// export captures operator SecretRefs exactly as before, and an inference
// posture never takes that path (its SecretRefs are rejected, never read).
func TestStaticInferenceCapture_AgentGateIntact(t *testing.T) {
	t.Parallel()
	cfg, reg, exports := provisioningFixture(t)
	env := &provisioningEnv{values: map[string]string{"KEY_REF": "synthetic"}}
	got, err := captureBackendProvisioning(cfg, reg, exports, env)
	if err != nil {
		t.Fatal(err)
	}
	if string(got["agent"].Secrets.Values["api_key"]) != "synthetic" {
		t.Fatalf("agent lane must still capture SecretRefs: %v", secretNames(got["agent"].Secrets))
	}
	if got["agent"].CredentialPolicyRevision == "" {
		t.Fatal("agent capture must also bind the policy revision")
	}
}

// TestStaticInferenceAuthorization_ExecutionEnvMayNotNameCredentialSlots
// proves the inference lane's launch-environment boundary directly on the
// authorization helper: execution context naming the credential root (slot 1)
// or one of its numbered slots (root_2..root_N) is rejected fail-closed with
// inference=false, so a captured secret can never be re-exported into the
// child launch environment. The fixed config allowlist is the first gate; this
// pins the helper's independent second gate against a future allowlist edit.
// An unrelated allowlisted name stays admitted (control), so the rejection is
// slot-specific, not a blanket refusal.
func TestStaticInferenceAuthorization_ExecutionEnvMayNotNameCredentialSlots(t *testing.T) {
	t.Parallel()
	contract, ok := inframanifest.LookupCanonicalContract("openrouter")
	if !ok {
		t.Fatal("openrouter must be registered")
	}
	export := ValidatedExport{Kind: "openrouter", Contract: &contract}
	profile := pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialStatic, AccessScope: pluginreg.BackendAccessAny}
	execution := aipsdk.BackendExecutionProfile{Class: aipsdk.BackendExecutionInference}
	for _, name := range []string{"OPENROUTER_API_KEY", "OPENROUTER_API_KEY_2"} {
		binding := config.BackendInstanceProvisioning{Kind: "openrouter", ExecutionEnv: []string{name}}
		got, inference, err := authorizeStaticInferenceBinding(binding, export, profile, execution)
		if err == nil || !strings.Contains(err.Error(), "execution context overlaps credential namespace") {
			t.Fatalf("execution context naming %q must fail closed, got inference=%v err=%v", name, inference, err)
		}
		if inference || got != (backendplugin.CredentialContract{}) {
			t.Fatalf("rejected execution context must not return an admitted contract: %+v inference=%v", got, inference)
		}
	}
	binding := config.BackendInstanceProvisioning{Kind: "openrouter", ExecutionEnv: []string{"HOME"}}
	got, inference, err := authorizeStaticInferenceBinding(binding, export, profile, execution)
	if err != nil || !inference {
		t.Fatalf("unrelated execution name must stay admitted: inference=%v err=%v", inference, err)
	}
	if got != contract {
		t.Fatalf("admitted contract = %+v, want the registered canonical contract", got)
	}
}

// TestStaticInferenceCapture_ExportContractMustMatchRegistration proves the
// host-authoritative contract binding: an export whose admitted credential
// contract drifts from its fixed registration is rejected as target posture
// before any lookup, so connector-authored metadata can never retarget the
// approved credential root or widen the inference lane.
func TestStaticInferenceCapture_ExportContractMustMatchRegistration(t *testing.T) {
	t.Parallel()
	cfg, reg, exports, contract := inferenceProvisioningFixture(t, "single_user")
	drifted := contract
	drifted.EnvRoot = "ACME_API_KEY"
	exports[0].Contract = &drifted
	env := &provisioningEnv{values: map[string]string{"OPENROUTER_API_KEY": "or-key-one"}}
	_, err := captureBackendProvisioning(cfg, reg, exports, env)
	if err == nil || !strings.Contains(err.Error(), "target posture rejected") {
		t.Fatalf("drifted export contract must be rejected, got: %v", err)
	}
	if len(env.lookups) != 0 {
		t.Fatalf("rejection must precede the authorized namespace scan, got lookups %v", env.lookups)
	}
}

// TestStaticInferenceCapture_ExecutionBoundsFailClosedSecretFree proves the
// inference lane's bounded launch-environment capture: an oversized value, a
// NUL byte, or an oversized aggregate rejects the candidate after credential
// capture with a bounded secret-free error instead of handing an unbounded
// value to the child.
func TestStaticInferenceCapture_ExecutionBoundsFailClosedSecretFree(t *testing.T) {
	t.Parallel()
	// Every name is on the fixed non-secret execution allowlist, so config
	// validation admits it and the capture bounds are what reject the values.
	aggregateNames := []string{
		"HOME", "PATH", "TMPDIR", "TEMP", "TMP", "LANG", "LC_ALL",
		"XDG_CONFIG_HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "SYSTEMROOT",
		"ANTHROPIC_CONFIG_DIR", "CLAUDE_CONFIG_DIR",
	}
	aggregate := map[string]string{"OPENROUTER_API_KEY": "or-key-one"}
	for _, name := range aggregateNames {
		aggregate[name] = strings.Repeat("e", 12000)
	}
	for _, tc := range []struct {
		name   string
		names  []string
		values map[string]string
	}{
		{
			name:   "oversized value",
			names:  []string{"HOME"},
			values: map[string]string{"OPENROUTER_API_KEY": "or-key-one", "HOME": strings.Repeat("h", 16385)},
		},
		{
			name:   "NUL byte",
			names:  []string{"HOME"},
			values: map[string]string{"OPENROUTER_API_KEY": "or-key-one", "HOME": "probe\x00value"},
		},
		{
			name:   "oversized aggregate",
			names:  aggregateNames,
			values: aggregate,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg, reg, exports, _ := inferenceProvisioningFixture(t, "single_user")
			binding := cfg.Plugins.BackendDiscovery.Instances["or"]
			binding.ExecutionEnv = tc.names
			cfg.Plugins.BackendDiscovery.Instances["or"] = binding
			env := &provisioningEnv{values: tc.values}
			got, err := captureBackendProvisioning(cfg, reg, exports, env)
			if got != nil || err == nil || !strings.Contains(err.Error(), "execution capture rejected") {
				t.Fatalf("want bounded execution capture rejection, got inputPresent=%v err=%v", got != nil, err)
			}
			if strings.Contains(err.Error(), "or-key-one") || strings.Contains(err.Error(), "probe") {
				t.Fatalf("secret-bearing execution capture error: %v", err)
			}
		})
	}
}
