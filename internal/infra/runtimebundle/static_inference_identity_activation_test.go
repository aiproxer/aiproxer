package runtimebundle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	inframanifest "github.com/aiproxer/aiproxer/internal/infra/backendplugins/manifest"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	bpkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	sdkmanifest "github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/manifest"
)

// inferencePhysicalInput returns a per-instance physical input carrying a
// static-inference credential binding: approved namespace, quota domain, slot
// shape, selection mode and policy revision alongside opaque slot secrets.
func inferencePhysicalInput() backendResourcePhysicalInput {
	base := testBackendResourcePhysicalInput()
	base.FactoryKind = "openrouter"
	base.ArtifactDigest = "sha256:openrouter-artifact"
	// Keep the transport policy valid: the stream-frame ceiling must not
	// exceed the whole-message ceiling.
	base.RuntimePolicy.MaxRequestBytes = 4096
	base.CredentialEnvRoot = "OPENROUTER_API_KEY"
	base.CredentialQuotaDomain = "openrouter"
	base.CredentialSlot = "api_key"
	base.CredentialSelection = "quota_pre_output"
	base.CredentialPolicyRevision = "max_env_index=20;quota_switch_retry_after=1h0m0s"
	base.Secrets.Values = map[string][]byte{
		"api_key":   []byte("or-key-one"),
		"api_key_2": []byte("or-key-two"),
	}
	return base
}

// TestStaticInferenceIdentity_BindsNamespaceQuotaSlotPolicy proves design step
// 5: normalized credential-slot order, opaque secret fingerprints,
// namespace/quota domain and policy revision enter the physical resource
// identity, so no accidental reuse crosses credential snapshots. Clearing the
// binding must also change identity (agent-style inputs never alias
// inference-bound ones).
func TestStaticInferenceIdentity_BindsNamespaceQuotaSlotPolicy(t *testing.T) {
	t.Parallel()
	base := inferencePhysicalInput()
	baseID, shareable, err := physicalIdentity(base)
	if err != nil {
		t.Fatalf("base physical identity: %v", err)
	}
	if !shareable {
		t.Fatal("inference-bound per_instance input must be shareable")
	}
	mutations := map[string]func(*backendResourcePhysicalInput){
		"namespace":    func(in *backendResourcePhysicalInput) { in.CredentialEnvRoot = "ACME_API_KEY" },
		"quota domain": func(in *backendResourcePhysicalInput) { in.CredentialQuotaDomain = "acme" },
		"slot":         func(in *backendResourcePhysicalInput) { in.CredentialSlot = "api_token" },
		"selection":    func(in *backendResourcePhysicalInput) { in.CredentialSelection = "no_rotation" },
		"policy revision": func(in *backendResourcePhysicalInput) {
			in.CredentialPolicyRevision = "max_env_index=21;quota_switch_retry_after=1h0m0s"
		},
	}
	for name, mutate := range mutations {
		input := cloneBackendResourcePhysicalInput(base)
		mutate(&input)
		got, shareable, err := physicalIdentity(input)
		if err != nil {
			t.Fatalf("%s physical identity: %v", name, err)
		}
		if !shareable {
			t.Fatalf("%s must stay shareable", name)
		}
		if reflect.DeepEqual(baseID, got) {
			t.Fatalf("credential %s change aliased the base physical identity", name)
		}
		assertIdentityOmitsSecret(t, got, "or-key-one")
	}
	cleared := cloneBackendResourcePhysicalInput(base)
	cleared.CredentialEnvRoot, cleared.CredentialQuotaDomain = "", ""
	cleared.CredentialSlot, cleared.CredentialSelection, cleared.CredentialPolicyRevision = "", "", ""
	clearedID, _, err := physicalIdentity(cleared)
	if err != nil {
		t.Fatalf("cleared physical identity: %v", err)
	}
	if reflect.DeepEqual(baseID, clearedID) {
		t.Fatal("cleared credential binding aliased the inference-bound identity")
	}
}

// activationDescribeSession wraps an in-process configured instance with a
// controllable runtime descriptor for the activation-time manifest comparison.
type activationDescribeSession struct {
	backendplugin.ConfiguredInstance
	descriptor  backendplugin.PluginDescriptor
	describeErr error
	closed      *atomic.Int64
}

func (s *activationDescribeSession) Describe(context.Context) (backendplugin.PluginDescriptor, error) {
	if s.describeErr != nil {
		return backendplugin.PluginDescriptor{}, s.describeErr
	}
	return s.descriptor, nil
}

func (s *activationDescribeSession) Close(ctx context.Context) error {
	s.closed.Add(1)
	return s.ConfiguredInstance.Close(ctx)
}

func inferenceActivationManifest(t *testing.T) sdkmanifest.Manifest {
	t.Helper()
	contract, ok := inframanifest.LookupCanonicalContract("openrouter")
	if !ok {
		t.Fatal("openrouter must be registered")
	}
	return sdkmanifest.Manifest{
		Schema:           sdkmanifest.SchemaV1,
		PluginID:         "com.aiproxer.openrouter",
		Version:          "0.0.1",
		Executable:       "connector",
		SHA256:           "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		ProtocolMajor:    1,
		ProtocolMinMinor: 0,
		ProtocolMaxMinor: 1,
		Platforms:        []sdkmanifest.Platform{{OS: "linux", Arch: "amd64"}},
		Exports: []sdkmanifest.Export{{
			Kind:               "openrouter",
			CredentialMode:     backendplugin.CredentialModeStatic,
			AccessScope:        backendplugin.AccessScopeAny,
			ProcessSharing:     backendplugin.ProcessSharingPerInstance,
			ExecutionClass:     "inference",
			CredentialContract: &contract,
		}},
	}
}

func inferenceActivationDescriptor(extra ...backendplugin.FactoryDescriptor) backendplugin.PluginDescriptor {
	factories := []backendplugin.FactoryDescriptor{{
		Kind:           "openrouter",
		CredentialMode: backendplugin.CredentialModeStatic,
		AccessScope:    backendplugin.AccessScopeAny,
		ProcessSharing: backendplugin.ProcessSharingPerInstance,
	}}
	return backendplugin.PluginDescriptor{
		ProtocolMajor: 1,
		PluginID:      "com.aiproxer.openrouter",
		Version:       "0.0.1",
		Factories:     append(factories, extra...),
	}
}

func buildInferenceActivation(t *testing.T, manifest sdkmanifest.Manifest, descriptor backendplugin.PluginDescriptor, describeErr error) (*atomic.Int64, error) {
	t.Helper()
	host := processhost.NewHost(processhost.Config{
		Launcher: &processhost.TestLauncher{PID: 4242},
		Channel:  &processhost.TestChannel{},
	})
	t.Cleanup(func() { _ = host.Close() })
	var closed atomic.Int64
	dial := inferenceActivationDial(&closed, descriptor, describeErr)
	export := ValidatedExport{
		Kind:     "openrouter",
		Artifact: &trust.VerifiedArtifact{DigestHex: "sha256:openrouter-artifact"},
		Model:    processhost.ProcessModelPerInstance,
		Manifest: manifest,
	}
	input := inferencePhysicalInput()
	input.InstanceID = "or-1"
	backend, cleanup, err := buildDiscoveredPhysical(context.Background(), host, export, input, dial, new(atomic.Uint64), nil)
	if err != nil {
		return &closed, err
	}
	t.Cleanup(func() { _ = cleanup() })
	if backend.Open == nil {
		t.Fatal("expected adapter backend after activation")
	}
	return &closed, nil
}

// inferenceActivationDial configures an in-process fake instance and wraps it
// with a controllable runtime descriptor for the activation-time manifest
// comparison.
func inferenceActivationDial(closed *atomic.Int64, descriptor backendplugin.PluginDescriptor, describeErr error) DialSessionFunc {
	return func(ctx context.Context, req DialSessionRequest) (ExecuteSession, backendplugin.ResolvedProfile, error) {
		fake := &bpkit.FakeService{Mode: bpkit.ModeValid}
		inst, err := fake.Configure(ctx, backendplugin.ConfigureRequest{
			InstanceID:  req.InstanceID,
			FactoryKind: req.FactoryKind,
			ConfigYAML:  req.ConfigYAML,
			Secrets:     req.Secrets,
			Negotiation: backendplugin.Negotiation{
				Compatible:      true,
				NegotiatedMinor: backendplugin.ProtocolMinorOrderedItems,
				EnabledFeatures: []string{backendplugin.FeatureOrderedItems},
			},
			RuntimePolicy: req.Policy,
		})
		if err != nil {
			return nil, backendplugin.ResolvedProfile{}, err
		}
		profile, err := inst.Resolve(ctx, nil)
		if err != nil {
			return nil, backendplugin.ResolvedProfile{}, err
		}
		return &activationDescribeSession{ConfiguredInstance: inst, descriptor: descriptor, describeErr: describeErr, closed: closed}, profile, nil
	}
}

// TestActivation_AgreeingDescriptorProceeds is the positive control: a runtime
// descriptor agreeing with the packaged manifest activates.
func TestActivation_AgreeingDescriptorProceeds(t *testing.T) {
	t.Parallel()
	_, err := buildInferenceActivation(t, inferenceActivationManifest(t), inferenceActivationDescriptor(), nil)
	if err != nil {
		t.Fatalf("agreeing descriptor must activate: %v", err)
	}
}

// TestActivation_DescriptorMismatchReleasesCandidate proves the
// activation-time CompareDescriptorToManifest wiring: a runtime descriptor
// exporting an undeclared kind fails closed with a secret-free error and the
// configured session is closed (candidate resources released, never
// published).
func TestActivation_DescriptorMismatchReleasesCandidate(t *testing.T) {
	t.Parallel()
	rogue := backendplugin.FactoryDescriptor{
		Kind:           "undeclared-kind",
		CredentialMode: backendplugin.CredentialModeStatic,
		AccessScope:    backendplugin.AccessScopeAny,
		ProcessSharing: backendplugin.ProcessSharingPerInstance,
	}
	closed, err := buildInferenceActivation(t, inferenceActivationManifest(t), inferenceActivationDescriptor(rogue), nil)
	if err == nil {
		t.Fatal("descriptor exporting an undeclared kind must fail activation")
	}
	if closed.Load() != 1 {
		t.Fatalf("mismatched activation closed the session %d times, want exactly once", closed.Load())
	}
	if strings.Contains(err.Error(), "or-key") {
		t.Fatalf("secret-bearing activation error: %v", err)
	}
}

// TestActivation_DescribeFailureFailsClosed proves a connector that will not
// describe itself is never admitted: the candidate fails and the session is
// closed without publication.
func TestActivation_DescribeFailureFailsClosed(t *testing.T) {
	t.Parallel()
	closed, err := buildInferenceActivation(t, inferenceActivationManifest(t), inferenceActivationDescriptor(), errors.New("activation describe probe failure"))
	if err == nil {
		t.Fatal("describe failure must fail activation")
	}
	if closed.Load() != 1 {
		t.Fatalf("failed-describe activation closed the session %d times, want exactly once", closed.Load())
	}
}

// TestStaticInferenceInstall_MissingSecretsReleaseCandidateResources proves
// requirement 2.5 for the inference lane: when the authorized namespace scan
// finds no approved-root values, the install fails closed, returns no
// install bundle, and removes its staging (candidate resources released;
// active/in-flight generations untouched — there is no partial provisioning
// to publish).
func TestStaticInferenceInstall_MissingSecretsReleaseCandidateResources(t *testing.T) {
	// TMPDIR isolation mirrors
	// TestBackendProvisioning_StockInstallAuthorizationAndCaptureRollback:
	// production staging lands in the process-global temp root, so the
	// before/after glob must observe only this test's own staging.
	isolated := t.TempDir()
	t.Setenv("TMPDIR", isolated)
	t.Setenv("TMP", isolated)
	t.Setenv("TEMP", isolated)
	root := StageOpenRouterForTest(t)
	manifestPath := filepath.Join(root, "plugin.backendplugin.json")
	body, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	// The staged openrouter fixture omits execution_class; the inference
	// lane requires the explicit inference declaration, so the failure under
	// test stays the missing-secret capture, not posture admission.
	body = []byte(strings.Replace(string(body),
		`"quota_domain":"openrouter"}`+"\n  }]",
		`"quota_domain":"openrouter"}`+"\n  , \"execution_class\":\"inference\"\n  }]", 1))
	if err := os.WriteFile(manifestPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Access: config.AccessConfig{Mode: "single_user"}, Plugins: config.PluginsConfig{
		Backends:         []config.PluginConfig{{ID: "or", Kind: "openrouter", Enabled: true}},
		BackendDiscovery: config.BackendDiscoveryConfig{Enabled: true, Paths: []string{root}, Instances: map[string]config.BackendInstanceProvisioning{"or": {Kind: "openrouter"}}},
	}}
	before, _ := filepath.Glob(filepath.Join(os.TempDir(), "aip-plugin-serve-*"))
	seen := map[string]bool{}
	for _, path := range before {
		seen[path] = true
	}
	var acquired []string
	env := &provisioningEnv{onLookup: func() {
		paths, _ := filepath.Glob(filepath.Join(os.TempDir(), "aip-plugin-serve-*"))
		for _, path := range paths {
			if !seen[path] {
				acquired = append(acquired, path)
			}
		}
	}}
	install, err := installDiscoveredBackendExports(cfg, pluginreg.NewRegistry(), env)
	if err == nil || install != nil {
		t.Fatalf("want missing-secret failure: %v", err)
	}
	if !strings.Contains(err.Error(), "secret capture rejected") {
		t.Fatalf("want the missing-secret capture failure, got: %v", err)
	}
	if len(env.lookups) == 0 {
		t.Fatal("rejection must follow the authorized namespace scan")
	}
	for _, name := range env.lookups {
		if !strings.HasPrefix(name, "OPENROUTER_API_KEY") {
			t.Fatalf("lookup outside the approved namespace: %q", name)
		}
	}
	if len(acquired) == 0 {
		t.Fatal("capture did not follow verified catalog acquisition")
	}
	for _, path := range acquired {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("capture failure leaked staging")
		}
	}
}

// TestStaticInferenceActivation_FailureReleasesGeneration proves generation
// cleanup: a failed inference activation invalidates its generation (the
// child process is reaped, no pending instance blocks), so a retry launches
// fresh and the host closes cleanly with no orphaned processes.
func TestStaticInferenceActivation_FailureReleasesGeneration(t *testing.T) {
	t.Parallel()
	launcher := &processhost.TestLauncher{PID: 4242}
	host := processhost.NewHost(processhost.Config{
		Launcher: launcher,
		Channel:  &processhost.TestChannel{},
	})
	t.Cleanup(func() { _ = host.Close() })
	contract, ok := inframanifest.LookupCanonicalContract("openrouter")
	if !ok {
		t.Fatal("openrouter must be registered")
	}
	export := ValidatedExport{
		Kind:     "openrouter",
		Artifact: &trust.VerifiedArtifact{DigestHex: "sha256:openrouter-artifact"},
		Model:    processhost.ProcessModelPerInstance,
		Contract: &contract,
		Manifest: inferenceActivationManifest(t),
	}
	newInput := func() backendResourcePhysicalInput {
		input := inferencePhysicalInput()
		input.InstanceID = "or-1"
		return input
	}
	seq := new(atomic.Uint64)
	failingDial := func(context.Context, DialSessionRequest) (ExecuteSession, backendplugin.ResolvedProfile, error) {
		return nil, backendplugin.ResolvedProfile{}, errors.New("inference activation probe failure")
	}
	if _, _, err := buildDiscoveredPhysical(context.Background(), host, export, newInput(), failingDial, seq, nil); err == nil {
		t.Fatal("failing dial must fail activation")
	}
	// The failed generation owns a launched child process; releasing the
	// generation must reap it before the retry, so no orphaned child survives
	// the failure and no failed instance blocks the next generation.
	failedProc, ok := launcher.LastProcess.(interface{ Killed() bool })
	if !ok {
		t.Fatal("test launcher process must expose its reaped state")
	}
	if !failedProc.Killed() {
		t.Fatal("failed activation must reap its launched generation process")
	}
	var closed atomic.Int64
	goodDial := inferenceActivationDial(&closed, inferenceActivationDescriptor(), nil)
	backend, cleanup, err := buildDiscoveredPhysical(context.Background(), host, export, newInput(), goodDial, seq, nil)
	if err != nil {
		t.Fatalf("retry after generation release must activate: %v", err)
	}
	t.Cleanup(func() { _ = cleanup() })
	if backend.Open == nil {
		t.Fatal("expected adapter backend after retry")
	}
	if got := launcher.Launches.Load(); got != 2 {
		t.Fatalf("launches = %d, want exactly 2 (failed generation reaped, retry relaunched)", got)
	}
	if err := host.Close(); err != nil {
		t.Fatalf("host close after generation release: %v", err)
	}
}
