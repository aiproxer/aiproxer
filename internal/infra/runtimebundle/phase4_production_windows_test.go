//go:build windows

package runtimebundle_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	coreruntime "github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	sdkmanifest "github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/manifest"
)

// TestProduction_UnknownKindViaBuildHost proves the standard serve path
// discovers, trusts, launches via production processhost, configures over the
// secure channel, and invokes the adapter — without FakeService DialSession or
// TestLauncher injection.
func TestProduction_UnknownKindViaBuildHost(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	kind := "prod-unknown-kind-e2e"
	pluginRoot := stageProductionFakePlugin(t, kind)
	cfgPath := writeProductionDiscoveryConfig(t, pluginRoot, kind)

	host, err := runtimebundle.BuildHost(ctx, runtimebundle.BuildHostInput{
		ConfigPath:      cfgPath,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	hostServeCleanup(t, host)

	backends := hostActiveExecutorBackends(t, host)
	be, ok := backends["ext-prod-1"]
	if !ok || be.Open == nil {
		t.Fatalf("missing discovered backend ext-prod-1; backends=%v", keysOf(backends))
	}
	reg := runtimebundle.HostProcess(host).FactoryCatalog
	if reg == nil || !reg.HasBackend(kind) {
		t.Fatalf("registry missing discovered kind %q", kind)
	}

	stream, err := be.Open(ctx, aipapi.Call{
		ID: "prod-e2e", Session: aipapi.SessionRef{ALegID: "a"},
		Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIResponses},
		Messages:   []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	}, routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "ext-prod-1", Model: "fake-model"},
		Key:     "ext-prod-1:fake-model",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var sawText bool
	for {
		ev, err := stream.Recv(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		if ev.Kind == aipapi.EventTextDelta {
			sawText = true
		}
	}
	_ = stream.Close()
	if !sawText {
		t.Fatal("expected text delta from production fake plugin")
	}
}

func TestProduction_InvalidConfiguredFatalViaBuildHost(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "bad.backendplugin.json"), []byte(`{"schema":"nope"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := writeProductionDiscoveryConfig(t, root, "missing-configured-kind")
	_, err := runtimebundle.BuildHost(ctx, runtimebundle.BuildHostInput{
		ConfigPath:      cfgPath,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err == nil {
		t.Fatal("expected fatal BuildHost for invalid/missing configured kind")
	}
}

func hostActiveExecutorBackends(t *testing.T, host *runtimebundle.Host) map[string]execbackend.Backend {
	t.Helper()
	if host == nil {
		t.Fatal("nil host")
	}
	active := runtimebundle.HostManager(host).Active()
	if active == nil {
		t.Fatal("nil active generation")
	}
	provider, ok := active.RequestPlane().(runtimehost.ExecutorProvider)
	if !ok || provider == nil {
		t.Fatal("active generation missing ExecutorProvider")
	}
	ex, ok := provider.ExecutorView().(*coreruntime.Executor)
	if !ok || ex == nil {
		t.Fatal("expected *runtime.Executor from active generation")
	}
	return ex.Backends
}

func stageProductionFakePlugin(t *testing.T, kind string) string {
	t.Helper()
	bin := buildProductionFakePlugin(t)
	root := t.TempDir()
	rel := "bin/plugin.exe"
	dst := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	m := sdkmanifest.Manifest{
		Schema: sdkmanifest.SchemaV1, PluginID: "com.aiproxer.prod.fake", Version: "0.0.1", BuildID: "e2e",
		Executable: rel, SHA256: digest, ProtocolMajor: 1,
		Platforms: []sdkmanifest.Platform{{OS: "windows", Arch: runtime.GOARCH}},
		Exports: []sdkmanifest.Export{{
			Kind: kind, CredentialMode: backendplugin.CredentialModeNone,
			AccessScope: backendplugin.AccessScopeLocalOnly, ProcessSharing: backendplugin.ProcessSharingPerInstance,
		}},
	}
	body := fmt.Sprintf(`{
  "schema":"aiproxer.backendplugin.manifest/v1",
  "plugin_id":"com.aiproxer.prod.fake",
  "version":"0.0.1",
  "build_id":"e2e",
  "executable":%q,
  "sha256":%q,
  "protocol_major":1,
  "protocol_min_minor":0,
  "protocol_max_minor":0,
  "platforms":[{"os":"windows","arch":%q}],
  "exports":[{
    "kind":%q,
    "credential_mode":"none",
    "access_scope":"local_only",
    "process_sharing":"per_instance"
  }]
}`, rel, digest, runtime.GOARCH, kind)
	if err := os.WriteFile(filepath.Join(root, "plugin.backendplugin.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	tr := trust.Verify(root, m, trust.VerifyOptions{StagingDir: t.TempDir()})
	if tr.Reason != trust.ReasonOK || tr.Artifact == nil {
		t.Fatalf("preflight trust: %+v", tr)
	}
	_ = tr.Artifact.Close()
	return root
}

func buildProductionFakePlugin(t *testing.T) string {
	t.Helper()
	return runtimebundle.BackendPluginFakeBinaryForTest(t)
}

func writeProductionDiscoveryConfig(t *testing.T, pluginRoot, kind string) string {
	t.Helper()
	cfg := fmt.Sprintf(`server:
  address: "127.0.0.1:0"
access:
  mode: single_user
routing:
  max_attempts: 3
  default_route: "ext-prod-1:fake-model"
continuity:
  in_memory: true
  store: memory
logging:
  level: error
  format: text
diagnostics:
  enabled: false
hooks:
  tool_reactor_error_policy: fail_open
plugins:
  backend_discovery:
    enabled: true
    paths:
      - %q
    development_mode: true
  frontends:
    - id: openai-responses
      enabled: true
      config: {}
    - id: openai-legacy
      enabled: true
      config: {}
    - id: anthropic
      enabled: true
      config: {}
    - id: gemini
      enabled: true
      config: {}
  backends:
    - id: openai-responses
      enabled: false
      config: {}
    - id: openai-legacy
      enabled: false
      config: {}
    - id: anthropic
      enabled: false
      config: {}
    - id: gemini
      enabled: false
      config: {}
    - id: bedrock
      enabled: false
      config: {}
    - id: acp
      enabled: false
      config: {}
    - id: openrouter
      enabled: false
      config: {}
    - id: nvidia
      enabled: false
      config: {}
    - id: opencode-go
      enabled: false
      config: {}
    - id: opencode-zen
      enabled: false
      config: {}
    - id: ollama
      enabled: false
      config: {}
    - id: ollama-cloud
      enabled: false
      config: {}
    - id: llamacpp
      enabled: false
      config: {}
    - id: lmstudio
      enabled: false
      config: {}
    - id: vllm
      enabled: false
      config: {}
    - kind: %q
      id: ext-prod-1
      enabled: true
      config:
        opaque: delivered
  features:
    - id: submit-noop
      enabled: true
      config: {}
    - id: parts-noop
      enabled: true
      config: {}
    - id: tool-reactor-noop
      enabled: true
      config: {}
`, filepath.ToSlash(pluginRoot), kind)
	path := filepath.Join(t.TempDir(), "prod-discovery.yaml")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func keysOf[M ~map[string]V, V any](m M) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
