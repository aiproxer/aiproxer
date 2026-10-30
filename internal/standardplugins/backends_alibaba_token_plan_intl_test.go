package standardplugins

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/alibabatokenplanintl"
	"gopkg.in/yaml.v3"
)

func TestAlibabaTokenPlanIntlFactoryUsesEnvironmentKeyOnly(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"qwen3.7-plus"}]}`))
	}))
	defer srv.Close()

	keys := UpstreamAPIKeys{AlibabaTokenPlan: []string{"env-secret"}}
	bundle := EssentialBackendBundle(keys)
	var factory pluginreg.BackendFactory
	for _, reg := range bundle.Backends {
		if reg.ID == alibabatokenplanintl.ID {
			factory = reg.Factory
			break
		}
	}
	if factory == nil {
		t.Fatal("backend not registered")
	}
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(`base_url: "`+srv.URL+`"`), &node); err != nil {
		t.Fatal(err)
	}
	be, err := factory(*node.Content[0], srv.Client(), pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	if len(be.BackendPrefixes) != 1 || be.BackendPrefixes[0] != alibabatokenplanintl.ID {
		t.Fatalf("prefixes = %#v", be.BackendPrefixes)
	}
}

func TestAlibabaTokenPlanIntlFactoryRejectsConfiguredAPIKey(t *testing.T) {
	t.Parallel()
	bundle := EssentialBackendBundle(UpstreamAPIKeys{AlibabaTokenPlan: []string{"env-secret"}})
	var factory pluginreg.BackendFactory
	for _, reg := range bundle.Backends {
		if reg.ID == alibabatokenplanintl.ID {
			factory = reg.Factory
			break
		}
	}
	var node yaml.Node
	if err := yaml.Unmarshal([]byte("api_key: config-secret\n"), &node); err != nil {
		t.Fatal(err)
	}
	if _, err := factory(*node.Content[0], nil, pluginreg.BackendFactoryDeps{}); err == nil {
		t.Fatal("expected api_key YAML to be rejected")
	}
}

func TestResolveUpstreamAPIKeysIncludesAlibabaTokenPlan(t *testing.T) {
	t.Setenv("ALIBABA_TOKEN_PLAN_API_KEY", "env-secret")
	t.Setenv("ALIBABA_TOKEN_PLAN_API_KEY_2", "env-second")
	keys := ResolveUpstreamAPIKeysFromEnv()
	if len(keys.AlibabaTokenPlan) != 2 || keys.AlibabaTokenPlan[0] != "env-secret" || keys.AlibabaTokenPlan[1] != "env-second" {
		t.Fatalf("Token Plan key resolution mismatch: %#v", keys.AlibabaTokenPlan)
	}
}
