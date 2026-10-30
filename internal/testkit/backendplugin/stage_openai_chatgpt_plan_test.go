package backendplugin_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/adapter"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/catalog"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/discovery"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	bpkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkplugin "github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"gopkg.in/yaml.v3"
)

// Coexistence constants.
//
// The legacy private-Codex connector and the new official ChatGPT-plan
// connector are two independent connectors with two independent plugin ids.
// While the new connector is under validation both stay usable, and neither may
// answer for the other (requirement 1.6).
const (
	legacyCodexPluginID = "com.aiproxer.backend.codex"
	planPluginID        = "com.aiproxer.backend.openai-chatgpt-plan"

	legacyCodexKind          = "openai-codex"
	legacyCodexAppServerKind = "openai-codex-app-server"
	planKind                 = "openai-chatgpt-plan"
)

// TestCoexistence_OnePluginRootDiscoversLegacyAndNewConnectors proves that a
// single plugin root can carry the legacy private-Codex connector and the new
// official ChatGPT-plan connector side by side: the host discovers two
// manifests with distinct safe ids and distinct plugin ids, and the catalog
// resolves the three export kinds to the connector that actually declares
// them.
func TestCoexistence_OnePluginRootDiscoversLegacyAndNewConnectors(t *testing.T) {
	t.Parallel()
	root := bpkit.StageCodexAndOpenAIChatGPTPlan(t)

	res, _ := resolveCoexistenceCatalog(t, root)
	if res.CatalogErr != nil {
		t.Fatalf("catalog error: %v", res.CatalogErr)
	}

	// Two manifests, two safe ids, two plugin ids: the shared root must not
	// collapse the two connectors into one plugin.
	if len(res.Discovered) != 2 {
		t.Fatalf("shared root must expose 2 discovered manifests, got %d: %+v", len(res.Discovered), res.Discovered)
	}
	safeIDs := map[string]struct{}{}
	pluginIDs := map[string]struct{}{}
	for _, d := range res.Discovered {
		if d.Status != discovery.StatusDiscovered {
			t.Fatalf("manifest %q status = %q (%s), want discovered", d.ManifestPath, d.Status, d.Reason)
		}
		if _, dup := safeIDs[d.SafeID]; dup {
			t.Fatalf("duplicate discovery safe id %q in the shared root", d.SafeID)
		}
		safeIDs[d.SafeID] = struct{}{}
		if _, dup := pluginIDs[d.Manifest.PluginID]; dup {
			t.Fatalf("duplicate plugin id %q in the shared root", d.Manifest.PluginID)
		}
		pluginIDs[d.Manifest.PluginID] = struct{}{}
	}
	for _, want := range []string{legacyCodexPluginID, planPluginID} {
		if _, ok := pluginIDs[want]; !ok {
			t.Fatalf("shared root must publish plugin id %q, got %v", want, sortedKeys(pluginIDs))
		}
	}

	// Every export kind resolves to the connector that declares it, and no
	// kind is in conflict: the two plugin ids are distinct, so the shared
	// root is not a duplicate-plugin-id conflict.
	wantOwners := map[string]string{
		legacyCodexKind:          legacyCodexPluginID,
		legacyCodexAppServerKind: legacyCodexPluginID,
		planKind:                 planPluginID,
	}
	entries := map[string]catalog.Entry{}
	for _, e := range res.Snapshot.Entries {
		if e.ExportKind == "" {
			continue
		}
		if e.State == catalog.StateConflict {
			t.Fatalf("shared root must not produce a catalog conflict: %+v", e)
		}
		entries[e.ExportKind] = e
	}
	for kind, owner := range wantOwners {
		e, ok := entries[kind]
		if !ok {
			t.Fatalf("kind %q not discovered in the shared root, got %v", kind, sortedEntries(entries))
		}
		if e.State != catalog.StateDiscovered {
			t.Fatalf("kind %q state = %q (%s), want discovered", kind, e.State, e.Reason)
		}
		if e.PluginID != owner {
			t.Fatalf("kind %q resolved to plugin id %q, want %q", kind, e.PluginID, owner)
		}
	}
	if len(entries) != len(wantOwners) {
		t.Fatalf("shared root must expose exactly %d export kinds, got %v", len(wantOwners), sortedEntries(entries))
	}
}

// TestCoexistence_OnePluginRootConfiguresBothConnectorsWithoutAliasing drives
// the real staged executables through the host's discover→trust→catalog→
// install→activate→configure path and proves that the two connectors stay
// separate: the new factory is served by the new connector, the legacy
// factories stay served by the legacy connector, and configuring the new
// factory never reaches the legacy connector.
//
// This configuration names no registration store, so the new connector's
// attempt ends in one explicit pre-I/O failure with no fabricated output. This
// test proves discovery and configuration, not inference (the execution slice
// is proven by the host integration test next to this file).
//
// Nothing legacy is removed, disabled or redirected here: the legacy staging
// and the legacy exports stay exactly as they are while the migration gate is
// still pending (requirement 12.1).
func TestCoexistence_OnePluginRootConfiguresBothConnectorsWithoutAliasing(t *testing.T) {
	t.Parallel()
	root := bpkit.StageCodexAndOpenAIChatGPTPlan(t)

	res, reg := resolveCoexistenceCatalog(t, root)
	if res.CatalogErr != nil {
		t.Fatalf("catalog error: %v", res.CatalogErr)
	}

	// The two connectors are bound to two different executables: the new
	// factory can never be served by the legacy binary.
	planExport := installableExport(t, res.Installable, planKind)
	legacyExport := installableExport(t, res.Installable, legacyCodexKind)
	if planExport.Manifest.PluginID != planPluginID {
		t.Fatalf("kind %q manifest plugin id = %q, want %q", planKind, planExport.Manifest.PluginID, planPluginID)
	}
	if legacyExport.Manifest.PluginID != legacyCodexPluginID {
		t.Fatalf("kind %q manifest plugin id = %q, want %q", legacyCodexKind, legacyExport.Manifest.PluginID, legacyCodexPluginID)
	}
	if planExport.Artifact.DigestHex == legacyExport.Artifact.DigestHex {
		t.Fatal("the new factory and the legacy factory are bound to the same executable")
	}

	host := processhost.NewHost(processhost.Config{})
	t.Cleanup(func() { _ = host.Close() })
	if err := runtimebundle.InstallDiscoveredExports(reg, host, res.Installable, runtimebundle.DiscoveredInstallOptions{}); err != nil {
		t.Fatalf("install discovered exports: %v", err)
	}
	for _, kind := range []string{planKind, legacyCodexKind, legacyCodexAppServerKind} {
		if !reg.HasBackend(kind) {
			t.Fatalf("kind %q must stay registered in the shared root", kind)
		}
	}

	// Configure the new factory through the real staged executable. The host
	// compares the live runtime descriptor with the staged manifest before it
	// publishes the instance, so a successful activation proves the new
	// connector (and not the legacy one, whose descriptor declares two
	// factories under a different plugin id) serves the new kind.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	planBackend := buildCoexistenceBackend(t, reg, planKind, "plan-1", "{}\n")
	if !slices.Contains(planBackend.BackendPrefixes, planKind) {
		t.Fatalf("configured backend prefixes = %v, want %q", planBackend.BackendPrefixes, planKind)
	}
	for _, legacy := range []string{legacyCodexKind, legacyCodexAppServerKind} {
		if slices.Contains(planBackend.BackendPrefixes, legacy) {
			t.Fatalf("the new factory must not alias the legacy kind %q", legacy)
		}
	}
	if hasCapability(planBackend, aipapi.CapabilityVision) {
		t.Fatal("the new factory must be served by the new connector, which advertises no vision capability")
	}

	// The legacy factories stay independently usable: the legacy connector
	// still configures from the same shared root, and the host accepts its
	// runtime descriptor against the legacy manifest.
	legacyBackend := buildCoexistenceBackend(t, reg, legacyCodexAppServerKind, "legacy-1", "catalog_enabled: false\n")
	if !slices.Contains(legacyBackend.BackendPrefixes, legacyCodexAppServerKind) {
		t.Fatalf("configured legacy backend prefixes = %v, want %q", legacyBackend.BackendPrefixes, legacyCodexAppServerKind)
	}
	if slices.Contains(legacyBackend.BackendPrefixes, planKind) {
		t.Fatalf("the legacy factory must not alias the new kind %q", planKind)
	}
	if !hasCapability(legacyBackend, aipapi.CapabilityVision) {
		t.Fatal("the legacy factory must be served by the legacy connector, which advertises a vision capability")
	}

	// Honest execution expectation: this instance names no registration
	// store, so the attempt ends in one explicit pre-I/O failure and
	// fabricates no output at all.
	stream, err := planBackend.Open(ctx, aipapi.Call{
		ID:         "coexistence-plan",
		Session:    aipapi.SessionRef{ALegID: "aleg"},
		Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIResponses},
		Messages:   []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	}, routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "plan-1", Model: planKind + "/gpt-5"},
		Key:     "plan-1:" + planKind + "/gpt-5",
	})
	if err != nil {
		t.Fatalf("open new factory: %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	var events int
	var terminalErr error
	for {
		_, recvErr := stream.Recv(ctx)
		if recvErr != nil {
			if errors.Is(recvErr, io.EOF) {
				t.Fatal("the new factory must fail explicitly, not end the stream without a verdict")
			}
			terminalErr = recvErr
			break
		}
		events++
	}
	if events != 0 {
		t.Fatalf("the new factory fabricated %d event(s); it must produce none", events)
	}
	// The attempt must end in the connector's own explicit, non-retryable
	// pre-output refusal. A cancellation or a deadline would also end the
	// stream with an error, so those are rejected explicitly: they would
	// satisfy "some error arrived" without proving the connector refused.
	if errors.Is(terminalErr, context.Canceled) || errors.Is(terminalErr, context.DeadlineExceeded) {
		t.Fatalf("the attempt ended with %v; the connector must fail explicitly, not by cancellation or timeout", terminalErr)
	}
	var classified *adapter.ClassifiedError
	if !errors.As(terminalErr, &classified) {
		t.Fatalf("the attempt must end in a classified plugin failure, got %T: %v", terminalErr, terminalErr)
	}
	if classified.Code != string(sdkplugin.ErrorCodeFailedPrecondition) {
		t.Errorf("the missing-registration refusal must carry failed_precondition, got %q", classified.Code)
	}
	if classified.Retryable {
		t.Errorf("the explicit connector failure must not be retryable: %+v", classified)
	}
	if classified.OutputCommitted {
		t.Errorf("the explicit connector failure must not report committed output: %+v", classified)
	}
}

// resolveCoexistenceCatalog runs the host's shared discover→trust→catalog path
// over the coexistence root and returns the resolution with a registry that
// carries the standard builtins.
func resolveCoexistenceCatalog(t *testing.T, root string) (runtimebundle.PluginCatalogResolution, *pluginreg.Registry) {
	t.Helper()
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallEssentialBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			BackendDiscovery: config.BackendDiscoveryConfig{
				Enabled:         true,
				Paths:           []string{root},
				DevelopmentMode: true,
			},
		},
	}
	res, err := runtimebundle.ResolvePluginCatalog(cfg, reg, t.TempDir())
	if err != nil {
		t.Fatalf("resolve plugin catalog: %v", err)
	}
	return res, reg
}

// installableExport returns the installable export of one kind.
func installableExport(t *testing.T, exports []runtimebundle.ValidatedExport, kind string) runtimebundle.ValidatedExport {
	t.Helper()
	for _, exp := range exports {
		if exp.Kind == kind {
			return exp
		}
	}
	t.Fatalf("kind %q is not installable", kind)
	return runtimebundle.ValidatedExport{}
}

// buildCoexistenceBackend configures one discovered factory for real: the
// staged connector process is launched, peer-authenticated and configured over
// the backend-plugin ABI.
func buildCoexistenceBackend(t *testing.T, reg *pluginreg.Registry, kind, instanceID, configYAML string) execbackend.Backend {
	t.Helper()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(configYAML), &node); err != nil {
		t.Fatal(err)
	}
	res, err := reg.BuildBackendWithLifecycle(kind, instanceID, node, http.DefaultClient, pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatalf("configure %q: %v", kind, err)
	}
	t.Cleanup(func() {
		if res.Cleanup != nil {
			_ = res.Cleanup()
		}
	})
	return res.Backend
}

// hasCapability reports whether a configured backend advertises a capability.
func hasCapability(backend execbackend.Backend, capability aipapi.Capability) bool {
	_, ok := backend.Caps[capability]
	return ok
}

// sortedKeys returns the sorted keys of a string set.
func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// sortedEntries returns the sorted export kinds of a catalog entry map.
func sortedEntries(entries map[string]catalog.Entry) []string {
	out := make([]string, 0, len(entries))
	for k := range entries {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
