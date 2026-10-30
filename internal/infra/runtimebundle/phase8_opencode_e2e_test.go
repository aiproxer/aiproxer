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

func TestPhase8_OpenCodeExternalViaBuildHost(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var goAuth, zenAuth string
	goEmu := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		goAuth = r.Header.Get("Authorization")
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"go-emu-model"}]}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"go-hi\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	t.Cleanup(goEmu.Close)
	zenEmu := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		zenAuth = r.Header.Get("Authorization")
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"zen-emu-model"}]}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp","object":"response","created_at":1,"status":"completed","model":"wire","output":[{"type":"message","id":"m","status":"completed","role":"assistant","content":[{"type":"output_text","text":"zen-hi"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(zenEmu.Close)

	pluginRoot := runtimebundle.StageOpenCodeForTest(t)
	cfgPath := writeOpenCodeDiscoveryConfig(t, pluginRoot, goEmu.URL, zenEmu.URL, true)
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
	if reg == nil || !reg.HasBackend("opencode-go") || !reg.HasBackend("opencode-zen") {
		t.Fatalf("discovered kinds missing: go=%v zen=%v",
			reg != nil && reg.HasBackend("opencode-go"), reg != nil && reg.HasBackend("opencode-zen"))
	}
	backends := hostActiveExecutorBackends(t, host)
	goBE, ok := backends["ocgo"]
	if !ok || goBE.Open == nil {
		t.Fatalf("backends=%v", keysOf(backends))
	}
	zenBE, ok := backends["oczen"]
	if !ok || zenBE.Open == nil {
		t.Fatalf("backends=%v", keysOf(backends))
	}

	goStream, err := goBE.Open(ctx, aipapi.Call{
		ID: "p8-go", Session: aipapi.SessionRef{ALegID: "a"},
		Invocation: aipapi.Invocation{
			Operation: aipapi.OperationOpenAIChatCompletions, DeliveryMode: aipapi.DeliveryModeStreaming, TransportMode: aipapi.TransportModeStreaming,
		},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	}, routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "ocgo", Model: "opencode-go/go-emu-model"},
		Key:     "ocgo:opencode-go/go-emu-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !drainSawText(ctx, t, goStream, "go-hi") {
		t.Fatal("expected text from external opencode-go plugin")
	}
	if goAuth != "Bearer go-key" {
		t.Fatalf("go auth leaked/wrong: %q", goAuth)
	}

	zenStream, err := zenBE.Open(ctx, aipapi.Call{
		ID: "p8-zen", Session: aipapi.SessionRef{ALegID: "a2"},
		Invocation: aipapi.Invocation{
			Operation: aipapi.OperationOpenAIResponses, DeliveryMode: aipapi.DeliveryModeNonStreaming, TransportMode: aipapi.TransportModeNonStreaming,
		},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	}, routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "oczen", Model: "opencode-zen/zen-emu-model"},
		Key:     "oczen:opencode-zen/zen-emu-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !drainSawText(ctx, t, zenStream, "zen-hi") {
		t.Fatal("expected text from external opencode-zen plugin")
	}
	if zenAuth != "Bearer zen-key" {
		t.Fatalf("zen auth leaked/wrong: %q", zenAuth)
	}
	if goAuth == zenAuth {
		t.Fatal("go and zen instances must not share credential material")
	}
}

func TestPhase8_OpenCodeConfiguredMissingFails(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	emptyPlugins := t.TempDir()
	cfgPath := writeOpenCodeDiscoveryConfig(t, emptyPlugins, "http://127.0.0.1:9", "http://127.0.0.1:9", true)
	_, err := runtimebundle.BuildHost(ctx, runtimebundle.BuildHostInput{
		ConfigPath:      cfgPath,
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err == nil {
		t.Fatal("expected configured-missing opencode to fail BuildHost")
	}
}

func drainSawText(ctx context.Context, t *testing.T, stream aipapi.ManagedEventStream, want string) bool {
	t.Helper()
	defer func() { _ = stream.Close() }()
	var saw bool
	for {
		ev, err := stream.Recv(ctx)
		if errors.Is(err, io.EOF) {
			return saw
		}
		if err != nil {
			t.Fatal(err)
		}
		if ev.Kind == aipapi.EventTextDelta && strings.Contains(ev.Delta, want) {
			saw = true
		}
	}
}

func writeOpenCodeDiscoveryConfig(t *testing.T, pluginRoot, goURL, zenURL string, enable bool) string {
	t.Helper()
	backends := `
    - kind: opencode-go
      id: ocgo
      enabled: false
      config: {}
    - kind: opencode-zen
      id: oczen
      enabled: false
      config: {}`
	if enable {
		backends = fmt.Sprintf(`
    - kind: opencode-go
      id: ocgo
      enabled: true
      config:
        base_url: %q
        api_key: go-key
        models:
          - id: go-emu-model
            endpoint: %q
            ai_sdk_package: "@ai-sdk/openai-compatible"
    - kind: opencode-zen
      id: oczen
      enabled: true
      config:
        base_url: %q
        api_key: zen-key
        models:
          - id: zen-emu-model
            endpoint: %q
            ai_sdk_package: "@ai-sdk/openai"`, goURL, goURL+"/v1/chat/completions", zenURL, zenURL+"/v1/responses")
	}
	cfg := fmt.Sprintf(`server:
  address: "127.0.0.1:0"
access:
  mode: single_user
routing:
  max_attempts: 3
  default_route: "ocgo:opencode-go/go-emu-model"
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
	path := filepath.Join(t.TempDir(), "opencode-discovery.yaml")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
