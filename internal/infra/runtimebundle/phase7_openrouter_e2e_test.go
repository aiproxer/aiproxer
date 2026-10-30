//go:build windows

package runtimebundle_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func TestPhase7_OpenRouterExternalViaBuildHost(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	emu := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			http.Error(w, `{"error":{"message":"auth"}}`, http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"emu-model"}]}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	t.Cleanup(emu.Close)

	pluginRoot := runtimebundle.StageOpenRouterForTest(t)
	cfgPath := writeOpenRouterDiscoveryConfig(t, pluginRoot, emu.URL+"/v1", true)
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
	if reg == nil || !reg.HasBackend("openrouter") {
		t.Fatal("discovered openrouter missing from registry")
	}
	backends := hostActiveExecutorBackends(t, host)
	be, ok := backends["or1"]
	if !ok || be.Open == nil {
		t.Fatalf("backends=%v", keysOf(backends))
	}
	stream, err := be.Open(ctx, aipapi.Call{
		ID: "p7", Session: aipapi.SessionRef{ALegID: "a"},
		Invocation: aipapi.Invocation{
			Operation: aipapi.OperationOpenAIChatCompletions, DeliveryMode: aipapi.DeliveryModeStreaming, TransportMode: aipapi.TransportModeStreaming,
		},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	}, routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "or1", Model: "openrouter/emu-model"},
		Key:     "or1:openrouter/emu-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	var saw bool
	for {
		ev, err := stream.Recv(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if ev.Kind == aipapi.EventTextDelta {
			saw = true
		}
	}
	_ = stream.Close()
	if !saw {
		t.Fatal("expected text from external openrouter plugin")
	}
}

func TestPhase7_OpenRouterConfiguredMissingFails(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	emptyPlugins := t.TempDir()
	cfgPath := writeOpenRouterDiscoveryConfig(t, emptyPlugins, "http://127.0.0.1:9/v1", true)
	_, err := runtimebundle.BuildHost(ctx, runtimebundle.BuildHostInput{
		ConfigPath:      cfgPath,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err == nil {
		t.Fatal("expected configured-missing openrouter to fail BuildHost")
	}
}

func writeOpenRouterDiscoveryConfig(t *testing.T, pluginRoot, baseURL string, enableOR bool) string {
	t.Helper()
	orBlock := `
    - kind: openrouter
      id: or1
      enabled: false
      config: {}`
	if enableOR {
		orBlock = fmt.Sprintf(`
    - kind: openrouter
      id: or1
      enabled: true
      config:
        base_url: %q
        api_key: test-key`, baseURL)
	}
	cfg := fmt.Sprintf(`server:
  address: "127.0.0.1:0"
access:
  mode: single_user
routing:
  max_attempts: 3
  default_route: "or1:openrouter/emu-model"
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
    - id: opencode-go
      enabled: false
      config: {}
    - id: opencode-zen
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
`, filepath.ToSlash(pluginRoot), orBlock)
	path := filepath.Join(t.TempDir(), "openrouter-discovery.yaml")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
