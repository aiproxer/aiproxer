package runtimebundle_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
	"gopkg.in/yaml.v3"
)

func TestMinimal_NoPluginDirBuiltinsOnly(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallEssentialBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			BackendDiscovery: config.BackendDiscoveryConfig{Enabled: false},
		},
	}
	rep, err := runtimebundle.InspectBackendPlugins(cfg, reg)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range rep.Entries {
		if e.Source == "discovered" {
			t.Fatalf("unexpected discovered entry: %+v", e)
		}
	}
	if !reg.HasBackend(standardplugins.EssentialBackendKinds()[0]) {
		t.Fatal("essential builtin missing")
	}
}

func TestInactive_UnusedInstalledNoProcess(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeE2EManifest(t, root, "unused.backendplugin.json", "com.aiproxer.unused", "unused-kind-e2e")
	launcher := &processhost.TestLauncher{PID: 9101}
	host := processhost.NewHost(processhost.Config{Launcher: launcher, Channel: &processhost.TestChannel{}})
	t.Cleanup(func() { _ = host.Close() })

	reg := pluginreg.NewRegistry()
	_ = standardplugins.InstallEssentialBackendsOn(reg, standardplugins.UpstreamAPIKeys{})
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			BackendDiscovery: config.BackendDiscoveryConfig{
				Enabled: true, Paths: []string{root}, DevelopmentMode: true,
			},
		},
	}
	rep, err := runtimebundle.InspectBackendPlugins(cfg, reg)
	if err != nil {
		t.Fatal(err)
	}
	var sawUnused bool
	for _, e := range rep.Entries {
		if e.PluginID == "com.aiproxer.unused" || e.Kind == "unused-kind-e2e" {
			sawUnused = true
			if e.ActivationRequired {
				t.Fatalf("unused installed must not require activation: %+v", e)
			}
		}
	}
	if !sawUnused {
		t.Fatalf("expected unused discovered plugin in inspect: %+v", rep.Entries)
	}
	if launcher.Launches.Load() != 0 {
		t.Fatalf("launches=%d want 0", launcher.Launches.Load())
	}
}

func TestMixed_ConfiguredExternalActivatesExactlyOnce(t *testing.T) {
	t.Parallel()
	kind := "mixed-ext-kind"
	reg := pluginreg.NewRegistry()
	_ = standardplugins.InstallEssentialBackendsOn(reg, standardplugins.UpstreamAPIKeys{})
	launcher := &processhost.TestLauncher{PID: 9102}
	host := processhost.NewHost(processhost.Config{
		Launcher: launcher,
		Channel:  &processhost.TestChannel{},
	})
	t.Cleanup(func() { _ = host.Close() })
	fake := &bpkit.FakeService{Mode: bpkit.ModeValid}
	if err := runtimebundle.InstallDiscoveredExports(reg, host, []runtimebundle.ValidatedExport{{
		Kind: kind,
		Profile: pluginreg.BackendSecurityProfile{
			CredentialMode: pluginreg.CredentialNone, AccessScope: pluginreg.BackendAccessLocalOnly,
		},
		Artifact: &trust.VerifiedArtifact{DigestHex: "mixed-digest"},
		Model:    processhost.ProcessModelPerInstance,
	}}, runtimebundle.DiscoveredInstallOptions{DialSession: inProcessFakeDial(fake)}); err != nil {
		t.Fatal(err)
	}

	var cfgNode yaml.Node
	_ = yaml.Unmarshal([]byte("x: 1\n"), &cfgNode)
	res, err := reg.BuildBackendWithLifecycle(kind, "ext-1", cfgNode, nil, pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if res.Cleanup != nil {
			_ = res.Cleanup()
		}
	})
	if launcher.Launches.Load() != 1 {
		t.Fatalf("activates=%d want 1", launcher.Launches.Load())
	}
	stream, err := res.Backend.Open(context.Background(), aipapi.Call{
		ID: "mixed", Session: aipapi.SessionRef{ALegID: "a"},
		Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIResponses},
		Messages:   []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	}, routing.AttemptCandidate{Primary: routing.Primary{Backend: "ext-1", Model: "m"}, Key: "ext-1:m"})
	if err != nil {
		t.Fatal(err)
	}
	_ = stream.Close()
	if launcher.Launches.Load() != 1 {
		t.Fatalf("Open must not relaunch: activates=%d", launcher.Launches.Load())
	}
}

func TestInactive_InvalidUnusedNonFatal(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "bad.backendplugin.json"), []byte(`{"schema":"nope"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	writeE2EManifest(t, root, "ok.backendplugin.json", "com.aiproxer.ok", "ok-kind")
	reg := pluginreg.NewRegistry()
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			BackendDiscovery: config.BackendDiscoveryConfig{
				Enabled: true, Paths: []string{root}, DevelopmentMode: true, Strict: false,
			},
		},
	}
	rep, err := runtimebundle.InspectBackendPlugins(cfg, reg)
	if err != nil {
		t.Fatal(err)
	}
	var invalid, ok bool
	for _, e := range rep.Entries {
		if e.State == catalog.StateInvalid {
			invalid = true
		}
		if e.PluginID == "com.aiproxer.ok" || e.Kind == "ok-kind" {
			ok = true
		}
	}
	if !invalid || !ok {
		t.Fatalf("want invalid+ok entries, got %+v", rep.Entries)
	}
}

func TestInactive_InvalidConfiguredFailsClosed(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "bad.backendplugin.json"), []byte(`{"schema":"nope"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	reg := pluginreg.NewRegistry()
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			BackendDiscovery: config.BackendDiscoveryConfig{
				Enabled: true, Paths: []string{root}, DevelopmentMode: true,
			},
			Backends: []config.PluginConfig{{
				Kind: "missing-configured-kind", ID: "bad-inst", Enabled: true,
			}},
		},
	}
	rep, err := runtimebundle.InspectBackendPlugins(cfg, reg)
	if err == nil {
		for _, e := range rep.Entries {
			if e.Reason == catalog.ReasonEnabledMissing || e.State == catalog.StateFailed {
				return
			}
		}
		t.Fatalf("expected missing/invalid configured signal, got %+v", rep.Entries)
	}
}

func TestHundred_InactiveManifestsNoLaunch(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for i := range 100 {
		writeE2EManifest(t, root, fmt.Sprintf("p%03d.backendplugin.json", i),
			fmt.Sprintf("io.p%03d", i), fmt.Sprintf("hk%03d", i))
	}
	launcher := &processhost.TestLauncher{PID: 1}
	host := processhost.NewHost(processhost.Config{
		Launcher: launcher,
		Channel:  &processhost.TestChannel{},
	})
	t.Cleanup(func() { _ = host.Close() })
	res, err := discovery.Discover(discovery.Config{ExplicitPaths: []string{root}, Development: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Descriptors) != 100 {
		t.Fatalf("descriptors=%d", len(res.Descriptors))
	}
	reg := pluginreg.NewRegistry()
	_ = standardplugins.InstallEssentialBackendsOn(reg, standardplugins.UpstreamAPIKeys{})
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			BackendDiscovery: config.BackendDiscoveryConfig{
				Enabled: true, Paths: []string{root}, DevelopmentMode: true,
			},
		},
	}
	inspect, err := runtimebundle.InspectBackendPlugins(cfg, reg)
	if err != nil {
		t.Fatal(err)
	}
	if len(inspect.Entries) < 100 {
		t.Fatalf("inspect entries=%d", len(inspect.Entries))
	}
	if launcher.Launches.Load() != 0 {
		t.Fatalf("launches=%d", launcher.Launches.Load())
	}
}

func TestMixed_InspectAndServingSameConflictPolicy(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeE2EManifest(t, root, "a.backendplugin.json", "io.dup", "conflict-kind")
	writeE2EManifest(t, root, "b.backendplugin.json", "io.dup2", "conflict-kind")
	reg := pluginreg.NewRegistry()
	_ = standardplugins.InstallEssentialBackendsOn(reg, standardplugins.UpstreamAPIKeys{})
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			BackendDiscovery: config.BackendDiscoveryConfig{
				Enabled: true, Paths: []string{root}, DevelopmentMode: true,
			},
			Backends: []config.PluginConfig{{
				Kind: "conflict-kind", ID: "c1", Enabled: true,
			}},
		},
	}
	serveRes, err := runtimebundle.ResolvePluginCatalog(cfg, reg, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	inspect, inspectErr := runtimebundle.InspectBackendPlugins(cfg, reg)
	if inspectErr == nil || serveRes.CatalogErr == nil {
		t.Fatalf("inspect err=%v serve CatalogErr=%v; both must fail closed", inspectErr, serveRes.CatalogErr)
	}
	if inspectErr.Error() != serveRes.CatalogErr.Error() {
		t.Fatalf("inspect/serve catalog errors diverge: inspect=%v serve=%v", inspectErr, serveRes.CatalogErr)
	}
	var conflict bool
	for _, e := range inspect.Entries {
		if e.State == catalog.StateConflict || e.Reason == catalog.ReasonDuplicateExportKind ||
			e.Reason == catalog.ReasonEnabledInvalid || e.Reason == catalog.ReasonEnabledMissing ||
			e.Reason == catalog.ReasonUntrusted {
			conflict = true
		}
	}
	if !conflict {
		t.Fatalf("expected conflict/missing/untrusted policy in inspect: %+v", inspect.Entries)
	}
}

func TestMinimal_BuiltinsServeWithZeroArtifacts(t *testing.T) {
	t.Parallel()
	factoryID := "minimal-builtin-" + strings.ReplaceAll(t.Name(), "/", "-")
	reg := pluginreg.NewRegistry()
	err := reg.RegisterBackend(factoryID, func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{
			Caps:            aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			BackendPrefixes: []string{factoryID},
			ModelInventory: modelinventory.StaticProvider{
				Source: modelinventory.SourceStaticBuiltin,
				Models: []modelinventory.Model{{CanonicalID: "m", NativeID: "m", DisplayName: "m"}},
			},
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return nil, nil
			},
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{
			BackendDiscovery: config.BackendDiscoveryConfig{Enabled: false},
			Backends: []config.PluginConfig{{
				Kind: factoryID, ID: "be", Enabled: true,
			}},
		},
	}
	_, built := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
	})
	if built.Executor() == nil || len(built.Executor().Backends) != 1 {
		t.Fatalf("backends=%v", built.Executor())
	}
	rep, err := runtimebundle.InspectBackendPlugins(cfg, reg)
	if err != nil {
		t.Fatal(err)
	}
	var sawConfigured bool
	for _, e := range rep.Entries {
		if e.Source == "discovered" && e.State == catalog.StateActive {
			t.Fatalf("zero-artifact minimal must not activate discovered: %+v", e)
		}
		if e.InstanceID == "be" && e.Kind == factoryID {
			sawConfigured = true
		}
	}
	if !sawConfigured {
		t.Fatalf("expected configured builtin instance in inspect: %+v", rep.Entries)
	}
}

func writeE2EManifest(t *testing.T, root, name, pluginID, kind string) {
	t.Helper()
	exe := "bin/plugin"
	platforms := fmt.Sprintf(`[{"os":%q,"arch":"amd64"}]`, runtime.GOOS)
	if runtime.GOOS == "windows" {
		exe = "bin/plugin.exe"
	}
	if runtime.GOOS == "darwin" {
		platforms = `[{"os":"darwin","arch":"amd64"},{"os":"darwin","arch":"arm64"}]`
	}
	body := fmt.Sprintf(`{
  "schema":"aiproxer.backendplugin.manifest/v1",
  "plugin_id":%q,
  "version":"1.0.0",
  "build_id":"b",
  "executable":%q,
  "sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "protocol_major":1,
  "protocol_min_minor":0,
  "protocol_max_minor":0,
  "platforms":%s,
  "exports":[{
    "kind":%q,
    "credential_mode":"none",
    "access_scope":"local_only",
    "process_sharing":"per_instance"
  }]
}`, pluginID, exe, platforms, kind)
	if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
