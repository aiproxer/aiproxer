package runtimebundle_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/catalog"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/discovery"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	bpkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	sdkmanifest "github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/manifest"
	"gopkg.in/yaml.v3"
)

const syntheticUnknownKind = "synthetic-unknown-kind-xyz"

func TestDiscovered_InstallUnknownKindAndInvokeOpen(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	host := processhost.NewHost(processhost.Config{
		Launcher: &processhost.TestLauncher{PID: 9001},
		Channel:  &processhost.TestChannel{},
	})
	t.Cleanup(func() { _ = host.Close() })

	var gotInstance string
	var gotYAML []byte
	fake := &bpkit.FakeService{Mode: bpkit.ModeValid}
	export := runtimebundle.ValidatedExport{
		Kind: syntheticUnknownKind,
		Profile: pluginreg.BackendSecurityProfile{
			CredentialMode: pluginreg.CredentialNone,
			AccessScope:    pluginreg.BackendAccessLocalOnly,
		},
		Artifact: &trust.VerifiedArtifact{DigestHex: "digest-synthetic-unknown"},
		Model:    processhost.ProcessModelPerInstance,
	}
	err := runtimebundle.InstallDiscoveredExports(reg, host, []runtimebundle.ValidatedExport{export}, runtimebundle.DiscoveredInstallOptions{
		DialSession: func(ctx context.Context, req runtimebundle.DialSessionRequest) (runtimebundle.ExecuteSession, backendplugin.ResolvedProfile, error) {
			gotInstance = req.InstanceID
			gotYAML = append([]byte(nil), req.ConfigYAML...)
			inst, err := fake.Configure(ctx, backendplugin.ConfigureRequest{
				InstanceID:    req.InstanceID,
				FactoryKind:   req.FactoryKind,
				ConfigYAML:    req.ConfigYAML,
				Secrets:       req.Secrets,
				Negotiation:   backendplugin.Negotiation{Compatible: true},
				RuntimePolicy: req.Policy,
			})
			if err != nil {
				return nil, backendplugin.ResolvedProfile{}, err
			}
			profile, err := inst.Resolve(ctx, nil)
			if err != nil {
				return nil, backendplugin.ResolvedProfile{}, err
			}
			return inst, profile, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	prof, ok := reg.BackendSecurityProfile(syntheticUnknownKind)
	if !ok || prof.CredentialMode != pluginreg.CredentialNone || prof.AccessScope != pluginreg.BackendAccessLocalOnly {
		t.Fatalf("security profile = %+v ok=%v", prof, ok)
	}

	var cfgNode yaml.Node
	if err := yaml.Unmarshal([]byte("opaque_key: delivered\n"), &cfgNode); err != nil {
		t.Fatal(err)
	}
	res, err := reg.BuildBackendWithLifecycle(syntheticUnknownKind, "inst-syn-1", cfgNode, http.DefaultClient, pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if res.Cleanup != nil {
			_ = res.Cleanup()
		}
	})
	if gotInstance != "inst-syn-1" {
		t.Fatalf("instance id = %q, want inst-syn-1", gotInstance)
	}
	if !strings.Contains(string(gotYAML), "opaque_key: delivered") {
		t.Fatalf("config yaml = %q, want opaque payload", gotYAML)
	}

	stream, err := res.Backend.Open(context.Background(), aipapi.Call{
		ID:         "req-syn",
		Session:    aipapi.SessionRef{ALegID: "aleg"},
		Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIResponses},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}, routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "inst-syn-1", Model: "fake-model"},
		Key:     "inst-syn-1:fake-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	var sawText bool
	for {
		ev, err := stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if ev.Kind == aipapi.EventTextDelta {
			sawText = true
		}
	}
	if !sawText {
		t.Fatal("expected text delta from FakeService invoke path")
	}
}

// Clients route to a connector by its configured instance id, which may differ
// from the factory kind (for example claude-acp vs claudeagentacp). The
// instance id must therefore be a route prefix, or "<instance>:<model>" model
// selectors silently fall back to the default route.
func TestDiscovered_InstanceIDIsRoutePrefix(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	host := processhost.NewHost(processhost.Config{
		Launcher: &processhost.TestLauncher{PID: 9002},
		Channel:  &processhost.TestChannel{},
	})
	t.Cleanup(func() { _ = host.Close() })

	fake := &bpkit.FakeService{Mode: bpkit.ModeValid}
	export := runtimebundle.ValidatedExport{
		Kind: syntheticUnknownKind,
		Profile: pluginreg.BackendSecurityProfile{
			CredentialMode: pluginreg.CredentialNone,
			AccessScope:    pluginreg.BackendAccessLocalOnly,
		},
		Artifact: &trust.VerifiedArtifact{DigestHex: "digest-synthetic-prefix"},
		Model:    processhost.ProcessModelPerInstance,
	}
	err := runtimebundle.InstallDiscoveredExports(reg, host, []runtimebundle.ValidatedExport{export}, runtimebundle.DiscoveredInstallOptions{
		DialSession: func(ctx context.Context, req runtimebundle.DialSessionRequest) (runtimebundle.ExecuteSession, backendplugin.ResolvedProfile, error) {
			inst, err := fake.Configure(ctx, backendplugin.ConfigureRequest{
				InstanceID:    req.InstanceID,
				FactoryKind:   req.FactoryKind,
				ConfigYAML:    req.ConfigYAML,
				Negotiation:   backendplugin.Negotiation{Compatible: true},
				RuntimePolicy: req.Policy,
			})
			if err != nil {
				return nil, backendplugin.ResolvedProfile{}, err
			}
			profile, err := inst.Resolve(ctx, nil)
			if err != nil {
				return nil, backendplugin.ResolvedProfile{}, err
			}
			profile.RoutePrefixes = []string{req.FactoryKind}
			return inst, profile, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := reg.BuildBackendWithLifecycle(syntheticUnknownKind, "inst-prefix-1", yaml.Node{}, http.DefaultClient, pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if res.Cleanup != nil {
			_ = res.Cleanup()
		}
	})
	got := res.Backend.BackendPrefixes
	if !slices.Contains(got, "inst-prefix-1") {
		t.Fatalf("BackendPrefixes = %q, want instance id inst-prefix-1", got)
	}
	if !slices.Contains(got, syntheticUnknownKind) {
		t.Fatalf("BackendPrefixes = %q, want plugin-declared prefix kept", got)
	}
}

func TestDiscovered_DuplicateKindRejected(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackend(syntheticUnknownKind, func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	host := processhost.NewHost(processhost.Config{
		Launcher: &processhost.TestLauncher{PID: 9002},
		Channel:  &processhost.TestChannel{},
	})
	t.Cleanup(func() { _ = host.Close() })
	err := runtimebundle.InstallDiscoveredExports(reg, host, []runtimebundle.ValidatedExport{{
		Kind:     syntheticUnknownKind,
		Profile:  pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialNone},
		Artifact: &trust.VerifiedArtifact{DigestHex: "dup"},
		Model:    processhost.ProcessModelPerInstance,
	}}, runtimebundle.DiscoveredInstallOptions{})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("err = %v, want duplicate rejection", err)
	}
}

func TestDiscovered_DuplicateInstallRejected(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	host := processhost.NewHost(processhost.Config{
		Launcher: &processhost.TestLauncher{PID: 9003},
		Channel:  &processhost.TestChannel{},
	})
	t.Cleanup(func() { _ = host.Close() })
	exports := []runtimebundle.ValidatedExport{{
		Kind:     "dynamic-kind-a",
		Profile:  pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialNone},
		Artifact: &trust.VerifiedArtifact{DigestHex: "a1"},
		Model:    processhost.ProcessModelPerInstance,
	}, {
		Kind:     "dynamic-kind-a",
		Profile:  pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialNone},
		Artifact: &trust.VerifiedArtifact{DigestHex: "a2"},
		Model:    processhost.ProcessModelPerInstance,
	}}
	err := runtimebundle.InstallDiscoveredExports(reg, host, exports, runtimebundle.DiscoveredInstallOptions{
		DialSession: stubDialSession(t),
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "duplicate") {
		t.Fatalf("err = %v, want duplicate", err)
	}
}

func TestDynamic_DiscoveryCatalogInstallBuildBackendWithLifecycle(t *testing.T) {
	t.Parallel()
	kind := "dynamic-catalog-kind-" + strings.ReplaceAll(t.Name(), "/", "-")
	d := discovery.Descriptor{
		SafeID: "r/dynamic.json",
		Status: discovery.StatusDiscovered,
		Manifest: sdkmanifest.Manifest{
			PluginID: "com.aiproxer.dynamic.test", ProtocolMajor: 1, ProtocolMinMinor: 0, ProtocolMaxMinor: 1,
			Exports: []sdkmanifest.Export{{
				Kind:           kind,
				CredentialMode: backendplugin.CredentialModeNone,
				AccessScope:    backendplugin.AccessScopeLocalOnly,
				ProcessSharing: backendplugin.ProcessSharingPerInstance,
			}},
		},
	}
	art := &trust.VerifiedArtifact{DigestHex: "catalog-digest"}
	snap, err := catalog.Resolve(catalog.Input{
		Discovered:  []discovery.Descriptor{d},
		TrustBySafe: map[string]trust.VerifyResult{"r/dynamic.json": {Reason: trust.ReasonOK, Artifact: art}},
		HostMajor:   1, HostMinor: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	exports, err := runtimebundle.CollectInstallableExports(snap, map[string]trust.VerifyResult{
		"r/dynamic.json": {Reason: trust.ReasonOK, Artifact: art},
	}, []discovery.Descriptor{d})
	if err != nil {
		t.Fatal(err)
	}
	if len(exports) != 1 || exports[0].Kind != kind {
		t.Fatalf("exports=%+v", exports)
	}

	reg := pluginreg.NewRegistry()
	_ = reg.RegisterBackend("openai-responses", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{}, nil
	})
	host := processhost.NewHost(processhost.Config{
		Launcher: &processhost.TestLauncher{PID: 9004},
		Channel:  &processhost.TestChannel{},
	})
	t.Cleanup(func() { _ = host.Close() })
	fake := &bpkit.FakeService{Mode: bpkit.ModeValid}
	if err := runtimebundle.InstallDiscoveredExports(reg, host, exports, runtimebundle.DiscoveredInstallOptions{
		DialSession: inProcessFakeDial(fake),
	}); err != nil {
		t.Fatal(err)
	}
	available := append([]string{"openai-responses"}, kind)
	if err := pluginreg.ResolveEnabledFactoryIDs([]string{kind}, available); err != nil {
		t.Fatal(err)
	}
	var cfgNode yaml.Node
	_ = yaml.Unmarshal([]byte("from: catalog\n"), &cfgNode)
	res, err := reg.BuildBackendWithLifecycle(kind, "dyn-inst", cfgNode, nil, pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Cleanup != nil {
		t.Cleanup(func() { _ = res.Cleanup() })
	}
	if res.Backend.Open == nil {
		t.Fatal("expected Open after discovery→catalog→Install→BuildBackendWithLifecycle")
	}
}

func TestUnknownKind_BuildFailsBeforeConstruction(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	// The probe records whether the compile reached backend construction; the
	// enabled unknown row must fail the pre-flight resolution instead, before
	// any factory in the same config is built.
	var constructed atomic.Int32
	if err := reg.RegisterBackend("construction-probe", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		constructed.Add(1)
		return execbackend.Backend{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{Backends: []config.PluginConfig{
			{Kind: "construction-probe", ID: "probe", Enabled: true},
			{Kind: syntheticUnknownKind, ID: "missing", Enabled: true},
		}},
	}
	_, _, err := processAndCandidateErr(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if err == nil || !strings.Contains(err.Error(), syntheticUnknownKind) {
		t.Fatalf("compile err = %v, want unknown kind", err)
	}
	if got := constructed.Load(); got != 0 {
		t.Fatalf("backend construction ran %d time(s) before the unknown-kind rejection", got)
	}
}

func TestDiscovered_BuildBackendEmptyInstanceIDRejected(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	host := processhost.NewHost(processhost.Config{
		Launcher: &processhost.TestLauncher{PID: 9010},
		Channel:  &processhost.TestChannel{},
	})
	t.Cleanup(func() { _ = host.Close() })
	kind := "empty-id-kind-" + strings.ReplaceAll(t.Name(), "/", "-")
	if err := runtimebundle.InstallDiscoveredExports(reg, host, []runtimebundle.ValidatedExport{{
		Kind:     kind,
		Profile:  pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialNone},
		Artifact: &trust.VerifiedArtifact{DigestHex: "empty-id"},
		Model:    processhost.ProcessModelPerInstance,
	}}, runtimebundle.DiscoveredInstallOptions{DialSession: stubDialSession(t)}); err != nil {
		t.Fatal(err)
	}
	_, err := reg.BuildBackend(kind, yaml.Node{}, nil, pluginreg.BackendFactoryDeps{})
	if err == nil || !strings.Contains(err.Error(), "BuildBackendWithLifecycle") {
		t.Fatalf("BuildBackend err=%v, want fail-closed empty instance id", err)
	}
	_, err = reg.BuildBackendWithLifecycle(kind, "", yaml.Node{}, nil, pluginreg.BackendFactoryDeps{})
	if err == nil || !strings.Contains(err.Error(), "empty instance id") {
		t.Fatalf("BuildBackendWithLifecycle empty id err=%v", err)
	}
}

func TestDiscovered_SharedGenerationInvalidation(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	launcher := &processhost.TestLauncher{PID: 9011}
	host := processhost.NewHost(processhost.Config{
		Launcher: launcher,
		Channel:  &processhost.TestChannel{},
	})
	t.Cleanup(func() { _ = host.Close() })
	kind := "shared-inv-" + strings.ReplaceAll(t.Name(), "/", "-")
	fake := &bpkit.FakeService{Mode: bpkit.ModeProcessExit}
	if err := runtimebundle.InstallDiscoveredExports(reg, host, []runtimebundle.ValidatedExport{{
		Kind: kind,
		Profile: pluginreg.BackendSecurityProfile{
			CredentialMode: pluginreg.CredentialNone, AccessScope: pluginreg.BackendAccessLocalOnly,
		},
		Artifact: &trust.VerifiedArtifact{DigestHex: "shared-inv-digest"},
		Model:    processhost.ProcessModelSharedArtifact,
		Sharing:  processhost.SharingOptions{IsolationDeclared: true, ConcurrencyDeclared: true},
	}}, runtimebundle.DiscoveredInstallOptions{DialSession: inProcessFakeDial(fake)}); err != nil {
		t.Fatal(err)
	}
	var cfgNode yaml.Node
	_ = yaml.Unmarshal([]byte("k: v\n"), &cfgNode)
	resA, err := reg.BuildBackendWithLifecycle(kind, "inst-a", cfgNode, nil, pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if resA.Cleanup != nil {
			_ = resA.Cleanup()
		}
	})
	resB, err := reg.BuildBackendWithLifecycle(kind, "inst-b", cfgNode, nil, pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if resB.Cleanup != nil {
			_ = resB.Cleanup()
		}
	})
	if launcher.Launches.Load() != 1 {
		t.Fatalf("shared process launches=%d want 1", launcher.Launches.Load())
	}
	stream, err := resA.Backend.Open(context.Background(), aipapi.Call{
		ID: "inv", Session: aipapi.SessionRef{ALegID: "a"},
		Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIResponses},
		Messages:   []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	}, routing.AttemptCandidate{Primary: routing.Primary{Backend: "inst-a", Model: "m"}, Key: "inst-a:m"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	_, err = stream.Recv(context.Background())
	if err == nil || errors.Is(err, io.EOF) {
		t.Fatal("expected process-exit classified failure")
	}
	resC, err := reg.BuildBackendWithLifecycle(kind, "inst-c", cfgNode, nil, pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if resC.Cleanup != nil {
			_ = resC.Cleanup()
		}
	})
	if launcher.Launches.Load() != 2 {
		t.Fatalf("after host generation invalidation launches=%d want 2 (new shared generation)", launcher.Launches.Load())
	}
}

func TestDiscovered_NoConnectorSpecificSwitchInInstaller(t *testing.T) {
	t.Parallel()
	body := readDiscoveredInstaller(t)
	kinds := discoveredInstallerForbiddenKinds(t)
	if len(kinds) == 0 {
		t.Fatal("derived no forbidden kinds; census must never be silently empty")
	}
	if detectDiscoveredSwitchViolation(string(body), kinds) {
		t.Fatal("discovered installer must not contain connector switch")
	}
	// Negative control: a meaningful temp copy with one derived kind injected
	// must trip the same detector, proving the gate is not vacuous.
	injected := string(body) + "\n// negative control\nswitch kind {\n" + "case \"" + kinds[0] + "\":\n}\n"
	if !detectDiscoveredSwitchViolation(injected, kinds) {
		t.Fatalf("detector failed to reject injected kind %q", kinds[0])
	}
	tmp := filepath.Join(t.TempDir(), "discovered_factories.go")
	if err := os.WriteFile(tmp, []byte(injected), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if !detectDiscoveredSwitchViolation(string(raw), kinds) {
		t.Fatal("detector failed on temp copy with injected switch")
	}
}

func readDiscoveredInstaller(t *testing.T) []byte {
	t.Helper()
	candidates := []string{
		"discovered_factories.go",
		filepath.Join("internal", "infra", "runtimebundle", "discovered_factories.go"),
	}
	for _, path := range candidates {
		if body, err := os.ReadFile(path); err == nil {
			return body
		}
	}
	// Fall back to repo-root resolution when run from the package directory.
	root := discoveredRepoRoot(t)
	body, err := os.ReadFile(filepath.Join(root, "internal", "infra", "runtimebundle", "discovered_factories.go"))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func discoveredRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root with go.mod")
		}
		dir = parent
	}
}

// discoveredInstallerForbiddenKinds derives the union of essential bundle IDs
// and on-disk connector release.yaml factory kinds. The installer must not
// switch on any of them; deriving keeps the gate exact without hardcoding.
func discoveredInstallerForbiddenKinds(t *testing.T) []string {
	t.Helper()
	seen := map[string]struct{}{}
	var out []string
	add := func(kind string) {
		kind = strings.TrimSpace(kind)
		if kind == "" {
			return
		}
		if _, ok := seen[kind]; ok {
			return
		}
		seen[kind] = struct{}{}
		out = append(out, kind)
	}
	for _, e := range standardplugins.EssentialBackendBundle(standardplugins.UpstreamAPIKeys{}).Backends {
		add(e.ID)
	}
	root := discoveredRepoRoot(t)
	entries, err := os.ReadDir(filepath.Join(root, "connectors"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, "connectors", e.Name(), "release.yaml"))
		if err != nil {
			continue
		}
		var rel struct {
			FactoryKind string `yaml:"factory_kind"`
		}
		if err := yaml.Unmarshal(raw, &rel); err != nil {
			continue
		}
		add(rel.FactoryKind)
	}
	return out
}

func detectDiscoveredSwitchViolation(src string, kinds []string) bool {
	lower := strings.ToLower(src)
	for _, kind := range kinds {
		needle := "case \"" + strings.ToLower(strings.TrimSpace(kind)) + "\""
		if kind == "" {
			continue
		}
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

func stubDialSession(t *testing.T) runtimebundle.DialSessionFunc {
	t.Helper()
	return inProcessFakeDial(&bpkit.FakeService{Mode: bpkit.ModeValid})
}

func inProcessFakeDial(fake *bpkit.FakeService) runtimebundle.DialSessionFunc {
	return func(ctx context.Context, req runtimebundle.DialSessionRequest) (runtimebundle.ExecuteSession, backendplugin.ResolvedProfile, error) {
		neg := backendplugin.Negotiation{
			Compatible:      true,
			NegotiatedMinor: backendplugin.ProtocolMinorOrderedItems,
			EnabledFeatures: []string{backendplugin.FeatureOrderedItems},
		}
		inst, err := fake.Configure(ctx, backendplugin.ConfigureRequest{
			InstanceID:    req.InstanceID,
			FactoryKind:   req.FactoryKind,
			ConfigYAML:    req.ConfigYAML,
			Secrets:       req.Secrets,
			Negotiation:   neg,
			RuntimePolicy: req.Policy,
		})
		if err != nil {
			return nil, backendplugin.ResolvedProfile{}, err
		}
		profile, err := inst.Resolve(ctx, nil)
		if err != nil {
			return nil, backendplugin.ResolvedProfile{}, err
		}
		return inst, profile, nil
	}
}

func TestOrderedItemAuthorityCapabilityNoNetwork_discoveredFactoryExecute(t *testing.T) {
	t.Parallel()

	reg := pluginreg.NewRegistry()
	host := processhost.NewHost(processhost.Config{
		Launcher: &processhost.TestLauncher{PID: 9010},
		Channel:  &processhost.TestChannel{},
	})
	t.Cleanup(func() { _ = host.Close() })

	const kind = "fake-ordered-runtimebundle"
	fake := &bpkit.FakeService{Mode: bpkit.ModeValid}
	export := runtimebundle.ValidatedExport{
		Kind: kind,
		Profile: pluginreg.BackendSecurityProfile{
			CredentialMode: pluginreg.CredentialNone,
			AccessScope:    pluginreg.BackendAccessLocalOnly,
		},
		Artifact: &trust.VerifiedArtifact{DigestHex: "digest-fake-ordered"},
		Model:    processhost.ProcessModelPerInstance,
	}
	if err := runtimebundle.InstallDiscoveredExports(reg, host, []runtimebundle.ValidatedExport{export}, runtimebundle.DiscoveredInstallOptions{
		DialSession: inProcessFakeDial(fake),
	}); err != nil {
		t.Fatal(err)
	}

	var cfgNode yaml.Node
	if err := yaml.Unmarshal([]byte("instance: ordered\n"), &cfgNode); err != nil {
		t.Fatal(err)
	}
	res, err := reg.BuildBackendWithLifecycle(kind, "inst-ord-1", cfgNode, http.DefaultClient, pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if res.Cleanup != nil {
			_ = res.Cleanup()
		}
	})

	call := aipapi.Call{
		ID:         "req-ordered-rb",
		Session:    aipapi.SessionRef{ALegID: "aleg-rb"},
		Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenResponsesCreate},
		Items: []aipapi.Item{{
			Kind: aipapi.ItemKindMessage, ID: "msg-1", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleUser,
			Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "hi"}},
		}},
	}
	stream, err := res.Backend.Open(context.Background(), call, routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "inst-ord-1", Model: "fake-model"},
		Key:     "inst-ord-1:fake-model",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	for {
		_, err := stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if fake.ExecuteCount.Load() != 1 {
		t.Fatalf("ExecuteCount=%d want 1", fake.ExecuteCount.Load())
	}
}
