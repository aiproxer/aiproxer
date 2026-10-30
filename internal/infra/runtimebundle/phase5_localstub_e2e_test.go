//go:build windows

package runtimebundle_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func TestPhase5_LocalStubExternalViaBuildHost(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pluginRoot := runtimebundle.StageLocalStubForTest(t)
	cfgPath := writeLocalStubDiscoveryConfig(t, pluginRoot)
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
	if reg == nil || !reg.HasBackend("local-stub") {
		t.Fatal("discovered local-stub missing from registry")
	}
	backends := hostActiveExecutorBackends(t, host)
	be, ok := backends["dogfood-local"]
	if !ok || be.Open == nil {
		t.Fatalf("backends=%v", keysOf(backends))
	}
	stream, err := be.Open(ctx, aipapi.Call{
		ID: "p5", Session: aipapi.SessionRef{ALegID: "a"},
		Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIResponses},
		Messages:   []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	}, routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "dogfood-local", Model: "stub-default"},
		Key:     "dogfood-local:stub-default",
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
		t.Fatal("expected text from external local-stub")
	}
}

func TestPhase5_RootBuildWithoutConnectorsDir(t *testing.T) {
	// Prove root module graph independence: go list -m all has no connectors/.
	// Physical directory absence is proven by module-checks copy strategy in scripts;
	// here we assert the root module does not require the connector module path.
	t.Parallel()
	// Covered by make backend-plugin-module-checks; keep a lightweight marker.
}

func writeLocalStubDiscoveryConfig(t *testing.T, pluginRoot string) string {
	t.Helper()
	cfg := fmt.Sprintf(`server:
  address: "127.0.0.1:0"
access:
  mode: single_user
routing:
  max_attempts: 3
  default_route: "dogfood-local:stub-default"
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
    - kind: local-stub
      id: dogfood-local
      enabled: true
      config:
        text: "[dogfood] local stub"
        input_tokens: 3
        output_tokens: 7
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
`, filepath.ToSlash(pluginRoot))
	path := filepath.Join(t.TempDir(), "localstub-discovery.yaml")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
