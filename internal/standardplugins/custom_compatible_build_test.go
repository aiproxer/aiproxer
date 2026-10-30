package standardplugins

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
	"gopkg.in/yaml.v3"
)

func customCompatibleRegistry(t *testing.T) *pluginreg.Registry {
	t.Helper()
	reg := pluginreg.NewRegistry()
	if err := InstallStandardBackendsOn(reg, UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	return reg
}

func buildCompatibleBackend(t *testing.T, reg *pluginreg.Registry, kind, instanceID string, node yaml.Node, client *http.Client) execbackend.Backend {
	t.Helper()
	res, err := reg.BuildBackendWithLifecycle(kind, instanceID, node, client, pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	return res.Backend
}

func customCompatibleTestCall(op aipapi.Operation) aipapi.Call {
	return aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "hi"}},
		}},
		Invocation: aipapi.Invocation{
			Operation:     op,
			DeliveryMode:  aipapi.DeliveryModeNonStreaming,
			TransportMode: aipapi.TransportModeNonStreaming,
		},
	}
}

func TestBuildBackend_customOpenAILegacyCompatible_usesBackendPrefixAndChatTransport(t *testing.T) {
	root := "COMPAT_BUILD_LEGACY_KEY"
	clearCustomEnvRoot(t, root)
	t.Setenv(root, "yaml-key")

	modelsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %q, want /v1/models", r.URL.Path)
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer yaml-key" {
			t.Errorf("Authorization = %q", got)
			http.Error(w, "unexpected authorization", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"openai/gpt-4o-mini"}]}`))
	}))
	t.Cleanup(modelsSrv.Close)

	reg := customCompatibleRegistry(t)
	raw := fmt.Sprintf(`backend_prefix: my-legacy
base_url: %s/v1
api_key_env_var_root: %s
`, modelsSrv.URL, root)
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	be := buildCompatibleBackend(t, reg, CustomOpenAILegacyCompatibleID, "my-legacy", node, modelsSrv.Client())
	if be.Open == nil {
		t.Fatal("expected backend Open")
	}
	if len(be.BackendPrefixes) != 1 || be.BackendPrefixes[0] != "my-legacy" {
		t.Fatalf("BackendPrefixes = %#v, want [my-legacy]", be.BackendPrefixes)
	}

	caps := execbackend.EffectiveTransportCaps(context.Background(), be, customCompatibleTestCall(aipapi.OperationOpenAIChatCompletions), routing.AttemptCandidate{})
	if !caps.Supports(aipapi.OperationOpenAIChatCompletions, aipapi.TransportModeStreaming) {
		t.Fatal("expected chat streaming supported")
	}
	if !caps.Supports(aipapi.OperationOpenAIChatCompletions, aipapi.TransportModeNonStreaming) {
		t.Fatal("expected chat non-streaming supported")
	}
	if caps.Supports(aipapi.OperationOpenAIResponses, aipapi.TransportModeNonStreaming) {
		t.Fatal("custom legacy compatible must not expose responses transport")
	}

	snap, err := be.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Source != modelinventory.SourceRemote {
		t.Fatalf("Source = %q, want %q", snap.Source, modelinventory.SourceRemote)
	}
	if len(snap.Models) != 1 || snap.Models[0].CanonicalID != "my-legacy/openai/gpt-4o-mini" {
		t.Fatalf("Models = %+v", snap.Models)
	}
}

func TestBuildBackend_customOpenAIResponsesCompatible_usesBackendPrefixAndResponsesTransport(t *testing.T) {
	root := "COMPAT_BUILD_RESPONSES_KEY"
	clearCustomEnvRoot(t, root)
	t.Setenv(root, "yaml-key")

	modelsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %q, want /v1/models", r.URL.Path)
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer yaml-key" {
			t.Errorf("Authorization = %q", got)
			http.Error(w, "unexpected authorization", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4.1"}]}`))
	}))
	t.Cleanup(modelsSrv.Close)

	reg := customCompatibleRegistry(t)
	raw := fmt.Sprintf(`backend_prefix: my-responses
base_url: %s/v1
api_key_env_var_root: %s
`, modelsSrv.URL, root)
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	be := buildCompatibleBackend(t, reg, CustomOpenAIResponsesCompatibleID, "my-responses", node, modelsSrv.Client())
	if be.Open == nil {
		t.Fatal("expected backend Open")
	}
	if len(be.BackendPrefixes) != 1 || be.BackendPrefixes[0] != "my-responses" {
		t.Fatalf("BackendPrefixes = %#v, want [my-responses]", be.BackendPrefixes)
	}

	caps := execbackend.EffectiveTransportCaps(context.Background(), be, customCompatibleTestCall(aipapi.OperationOpenAIResponses), routing.AttemptCandidate{})
	if !caps.Supports(aipapi.OperationOpenAIResponses, aipapi.TransportModeStreaming) {
		t.Fatal("expected responses streaming supported")
	}
	if !caps.Supports(aipapi.OperationOpenAIResponses, aipapi.TransportModeNonStreaming) {
		t.Fatal("expected responses non-streaming supported")
	}
	if caps.Supports(aipapi.OperationOpenAIChatCompletions, aipapi.TransportModeNonStreaming) {
		t.Fatal("custom responses compatible must not expose chat-completions transport")
	}

	snap, err := be.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Source != modelinventory.SourceRemote {
		t.Fatalf("Source = %q, want %q", snap.Source, modelinventory.SourceRemote)
	}
	if len(snap.Models) != 1 || snap.Models[0].CanonicalID != "my-responses/gpt-4.1" {
		t.Fatalf("Models = %+v", snap.Models)
	}
}

func TestBuildBackend_customOpenAIResponsesCompatible_staticModelsOverrideDiscovery(t *testing.T) {
	t.Parallel()

	reg := customCompatibleRegistry(t)
	raw := `backend_prefix: my-responses
base_url: http://127.0.0.1:9/v1
models:
  source: inline
  items:
    - canonical_id: my-responses/static-model
      native_id: static-model
`
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	be := buildCompatibleBackend(t, reg, CustomOpenAIResponsesCompatibleID, "my-responses-static", node, nil)
	snap, err := be.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Source != modelinventory.SourceStaticInline {
		t.Fatalf("Source = %q, want %q", snap.Source, modelinventory.SourceStaticInline)
	}
	if len(snap.Models) != 1 || snap.Models[0].NativeID != "static-model" {
		t.Fatalf("Models = %+v", snap.Models)
	}
}

func TestBuildBackend_customAnthropicCompatible_usesBackendPrefixAndRemoteDiscovery(t *testing.T) {
	root := "COMPAT_BUILD_ANTHROPIC_KEY"
	clearCustomEnvRoot(t, root)
	t.Setenv(root, "yaml-key")

	modelsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %q, want /v1/models", r.URL.Path)
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		if got := r.Header.Get("x-api-key"); got != "yaml-key" {
			t.Errorf("x-api-key = %q", got)
			http.Error(w, "unexpected api key", http.StatusUnauthorized)
			return
		}
		body := `{"data":[{"id":"claude-sonnet-4-20250514","display_name":"Claude Sonnet 4"}]}`
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(modelsSrv.Close)

	reg := customCompatibleRegistry(t)
	raw := fmt.Sprintf(`backend_prefix: my-anthropic
base_url: %s
api_key_env_var_root: %s
`, modelsSrv.URL, root)
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	be := buildCompatibleBackend(t, reg, CustomAnthropicCompatibleID, "my-anthropic", node, modelsSrv.Client())
	if len(be.BackendPrefixes) != 1 || be.BackendPrefixes[0] != "my-anthropic" {
		t.Fatalf("BackendPrefixes = %#v, want [my-anthropic]", be.BackendPrefixes)
	}
	snap, err := be.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Source != modelinventory.SourceRemote {
		t.Fatalf("Source = %q, want %q", snap.Source, modelinventory.SourceRemote)
	}
	if len(snap.Models) != 1 || snap.Models[0].CanonicalID != "my-anthropic/claude-sonnet-4-20250514" {
		t.Fatalf("Models = %+v", snap.Models)
	}
}

func TestBuildBackend_customAnthropicCompatible_staticModelsOverrideDiscovery(t *testing.T) {
	t.Parallel()

	reg := customCompatibleRegistry(t)
	raw := `backend_prefix: my-anthropic
base_url: http://127.0.0.1:9
models:
  source: inline
  items:
    - canonical_id: my-anthropic/static-claude
      native_id: static-claude
`
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	be := buildCompatibleBackend(t, reg, CustomAnthropicCompatibleID, "my-anthropic-static", node, nil)
	snap, err := be.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Source != modelinventory.SourceStaticInline {
		t.Fatalf("Source = %q, want %q", snap.Source, modelinventory.SourceStaticInline)
	}
	if len(snap.Models) != 1 || snap.Models[0].NativeID != "static-claude" {
		t.Fatalf("Models = %+v", snap.Models)
	}
}

func TestBuildBackend_customAnthropicCompatible_missingBaseURLRejected(t *testing.T) {
	t.Parallel()

	reg := customCompatibleRegistry(t)
	var node yaml.Node
	if err := yaml.Unmarshal([]byte("backend_prefix: my-anthropic\n"), &node); err != nil {
		t.Fatal(err)
	}
	_, err := reg.BuildBackendWithLifecycle(CustomAnthropicCompatibleID, "my-anthropic", node, nil, pluginreg.BackendFactoryDeps{})
	if err == nil {
		t.Fatal("expected missing base_url error")
	}
	if !strings.Contains(err.Error(), "base_url is required") {
		t.Fatalf("error = %v, want base_url required", err)
	}
}
