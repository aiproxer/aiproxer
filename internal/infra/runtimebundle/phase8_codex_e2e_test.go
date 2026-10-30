//go:build windows

package runtimebundle_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	refbackend "github.com/aiproxer/aiproxer/internal/refbackend/openaicodex"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func TestPhase8_CodexHTTPExternalViaBuildHost(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	emu := refbackend.New(refbackend.Config{Token: "codex-key", OutputText: "codex-hi"})
	srv := httptest.NewServer(emu.Handler())
	t.Cleanup(srv.Close)

	pluginRoot := runtimebundle.StageCodexForTest(t)
	fakeCLI := stageFakeCodexCLI(t)
	pidFile := fakeCLI + ".pid"
	childPIDFile := fakeCLI + ".child.pid"
	cfgPath := writeCodexDiscoveryConfig(t, pluginRoot, srv.URL, true, fakeCLI)
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
	reg := runtimebundle.HostProcess(host).FactoryCatalog
	if reg == nil || !reg.HasBackend("openai-codex") || !reg.HasBackend("openai-codex-app-server") {
		t.Fatalf("discovered kinds missing: http=%v appserver=%v",
			reg != nil && reg.HasBackend("openai-codex"), reg != nil && reg.HasBackend("openai-codex-app-server"))
	}
	backends := hostActiveExecutorBackends(t, host)
	httpBE, ok := backends["codex-http"]
	if !ok || httpBE.Open == nil {
		t.Fatalf("backends=%v", keysOf(backends))
	}
	appBE, ok := backends["codex-app"]
	if !ok || appBE.Open == nil {
		t.Fatalf("backends=%v", keysOf(backends))
	}

	stream, err := httpBE.Open(ctx, aipapi.Call{
		ID: "p8-codex", Session: aipapi.SessionRef{ALegID: "a"},
		Invocation: aipapi.Invocation{
			Operation: aipapi.OperationOpenAIResponses, DeliveryMode: aipapi.DeliveryModeStreaming, TransportMode: aipapi.TransportModeStreaming,
		},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	}, routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "codex-http", Model: "openai-codex/gpt-5.3-codex-spark"},
		Key:     "codex-http:openai-codex/gpt-5.3-codex-spark",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !drainSawText(ctx, t, stream, "codex-hi") {
		t.Fatal("expected text from external openai-codex plugin")
	}
	sawAuth := emu.LatestRequest().Authorization
	if sawAuth != "Bearer codex-key" {
		t.Fatalf("auth leaked/wrong: %q", sawAuth)
	}

	appStream, err := appBE.Open(ctx, aipapi.Call{
		ID: "p8-codex-app", Session: aipapi.SessionRef{ALegID: "a2"},
		Invocation: aipapi.Invocation{
			Operation: aipapi.OperationOpenAIResponses, DeliveryMode: aipapi.DeliveryModeStreaming, TransportMode: aipapi.TransportModeStreaming,
		},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
		Extensions: map[string]json.RawMessage{
			"project_dir": json.RawMessage(strconv.Quote(filepath.Dir(pidFile))),
		},
	}, routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "codex-app", Model: "openai-codex-app-server/auto"},
		Key:     "codex-app:openai-codex-app-server/auto",
	})
	if err != nil {
		t.Fatalf("appserver Open: %v", err)
	}
	if !drainSawText(ctx, t, appStream, "appserver-hi") {
		t.Fatal("expected text from external openai-codex-app-server via fake CLI")
	}
	waitFile(t, pidFile, 8*time.Second)
	waitFile(t, childPIDFile, 8*time.Second)
	cliPID := readPIDFile(t, pidFile)
	childPID := readPIDFile(t, childPIDFile)
	_ = appStream.Cancel(ctx, aipapi.CancelCause{Kind: aipapi.CancelExplicit, Detail: "e2e"})
	_ = appStream.Close()
	waitGone(t, cliPID, 10*time.Second)
	waitGone(t, childPID, 10*time.Second)
}

func TestPhase8_CodexConfiguredMissingFails(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	emptyPlugins := t.TempDir()
	cfgPath := writeCodexDiscoveryConfig(t, emptyPlugins, "http://127.0.0.1:9", true, "")
	_, err := runtimebundle.BuildHost(ctx, runtimebundle.BuildHostInput{
		ConfigPath:      cfgPath,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err == nil {
		t.Fatal("expected configured-missing codex to fail BuildHost")
	}
}

func stageFakeCodexCLI(t *testing.T) (exe string) {
	t.Helper()
	return runtimebundle.StageFakeCodexCLIForTest(t)
}

func writeCodexDiscoveryConfig(t *testing.T, pluginRoot, codexURL string, enable bool, fakeCLI string) string {
	t.Helper()
	ws := filepath.ToSlash(t.TempDir())
	backends := `
    - kind: openai-codex
      id: codex-http
      enabled: false
      config: {}
    - kind: openai-codex-app-server
      id: codex-app
      enabled: false
      config: {}`
	if enable {
		appCfg := fmt.Sprintf(`
        default_workspace: %q
        catalog_enabled: false
        stale_kill_delay_seconds: 0.05`, ws)
		if fakeCLI != "" {
			appCfg = fmt.Sprintf(`
        executable: %q
        default_workspace: %q
        catalog_enabled: false
        stale_kill_delay_seconds: 0.05`, filepath.ToSlash(fakeCLI), ws)
		}
		backends = fmt.Sprintf(`
    - kind: openai-codex
      id: codex-http
      enabled: true
      config:
        base_url: %q
        access_token: codex-key
        catalog_enabled: false
    - kind: openai-codex-app-server
      id: codex-app
      enabled: true
      config:%s`, codexURL, appCfg)
	}
	cfg := fmt.Sprintf(`server:
  address: "127.0.0.1:0"
access:
  mode: single_user
routing:
  max_attempts: 3
  default_route: "codex-http:openai-codex/gpt-5.3-codex-spark"
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
      config: {}%s
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
`, filepath.ToSlash(pluginRoot), backends)
	path := filepath.Join(t.TempDir(), "codex-discovery.yaml")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func waitFile(t *testing.T, path string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", path)
}

func readPIDFile(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func waitGone(t *testing.T, pid int, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pid %d still alive", pid)
}
