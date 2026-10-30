package standardplugins

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/compatmode"
	"gopkg.in/yaml.v3"
)

func TestBuildBackend_compatibleOpenAI_rejectsInvalidBaseURL(t *testing.T) {
	t.Parallel()
	reg := customCompatibleRegistry(t)
	raw := `backend_prefix: bad-url
base_url: "https://user:pass@api.example.com/v1"
`
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	_, err := reg.BuildBackendWithLifecycle(CustomOpenAILegacyCompatibleID, "bad-url-inst", node, nil, pluginreg.BackendFactoryDeps{})
	if err == nil {
		t.Fatal("expected endpoint validation error")
	}
	if !strings.Contains(err.Error(), "userinfo") {
		t.Fatalf("error = %v, want userinfo rejection", err)
	}
	if strings.Contains(err.Error(), "<unknown>") {
		t.Fatalf("error must carry instance id: %v", err)
	}
}

func TestBuildBackend_compatibleAnthropic_normalizesTrailingSlashBaseURL(t *testing.T) {
	t.Parallel()
	reg := customCompatibleRegistry(t)
	raw := `backend_prefix: norm
base_url: "https://api.example.com/"
models:
  source: inline
  items:
    - canonical_id: norm/m1
      native_id: m1
`
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	res, err := reg.BuildBackendWithLifecycle(CustomAnthropicCompatibleID, "norm-inst", node, nil, pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Backend.Open == nil {
		t.Fatal("expected backend Open")
	}
}

func TestParseCompatibleEndpoint_openAIJoinsModels(t *testing.T) {
	t.Parallel()
	cfg := config.CompatibleModeConfig{
		BackendPrefix: "p",
		BaseURL:       "https://gateway.example.com/provider/v1/",
	}
	d, err := compatmode.ParseEndpoint(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := d.BaseURL(); got != "https://gateway.example.com/provider/v1" {
		t.Fatalf("BaseURL = %q", got)
	}
	models, err := compatmode.OpenAIModelsEndpoint(d)
	if err != nil {
		t.Fatal(err)
	}
	if models != "https://gateway.example.com/provider/v1/models" {
		t.Fatalf("models endpoint = %q", models)
	}
}

func TestParseCompatibleEndpoint_anthropicJoinsModels(t *testing.T) {
	t.Parallel()
	cfg := config.CompatibleModeConfig{
		BackendPrefix: "p",
		BaseURL:       "https://api.example.com",
	}
	d, err := compatmode.ParseEndpoint(cfg)
	if err != nil {
		t.Fatal(err)
	}
	models, err := compatmode.AnthropicModelsEndpoint(d)
	if err != nil {
		t.Fatal(err)
	}
	if models != "https://api.example.com/v1/models" {
		t.Fatalf("models endpoint = %q", models)
	}
}
