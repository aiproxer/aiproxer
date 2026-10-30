package commandcodeanthropic_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/commandcode-anthropic/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func mustCfg(t *testing.T, yaml string) backendplugin.ConfigureRequest {
	t.Helper()
	return backendplugin.ConfigureRequest{
		FactoryKind:   service.FactoryKind,
		InstanceID:    "t1",
		ConfigYAML:    []byte(yaml),
		Secrets:       backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("test-key")}},
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	}
}

func TestConfigure_DefaultBaseURL(t *testing.T) {
	t.Parallel()
	cfg, err := service.ParseConfigYAML([]byte(""))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != service.DefaultURL {
		t.Fatalf("base=%s", cfg.BaseURL)
	}
	_, err = service.New().Configure(context.Background(), mustCfg(t, ""))
	if err != nil {
		t.Fatal(err)
	}
}

func TestCapabilitiesAreStreamingOnlyUntilModelFactsExist(t *testing.T) {
	t.Parallel()
	d, err := service.New().Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	f := d.Factories[0]
	want := backendplugin.CapabilitySummary{Streaming: true}
	if f.StaticCapabilities != want || f.StaticCapabilities.Tools || f.StaticCapabilities.Vision || f.StaticCapabilities.Documents || f.StaticCapabilities.VideoInput {
		t.Fatalf("static capabilities=%+v", f.StaticCapabilities)
	}
	inst, err := service.New().Configure(context.Background(), mustCfg(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := inst.Resolve(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Capabilities != want {
		t.Fatalf("resolved capabilities=%+v", resolved.Capabilities)
	}
}

func TestListedModelsDoNotGuessCapabilities(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-3-5-sonnet-20241022"}]}`))
	}))
	t.Cleanup(srv.Close)
	inst, err := service.New().Configure(context.Background(), mustCfg(t, "base_url: "+srv.URL+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	listed, err := inst.ListModels(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Models) != 1 || listed.Models[0].CanonicalModelID != "commandcode-anthropic/claude-3-5-sonnet-20241022" || listed.Models[0].Capabilities != (backendplugin.CapabilitySummary{Streaming: true}) {
		t.Fatalf("listed=%+v", listed.Models)
	}
}

func TestConfigure_SharedRootEnvIgnored(t *testing.T) {
	t.Setenv("COMMANDCODE_API_KEY", "env-secret-key")
	t.Setenv("COMMANDCODE_ANTHROPIC_API_KEY", "env-secret-key")
	req := mustCfg(t, "base_url: https://api.commandcode.ai/provider/v1\n")
	req.Secrets = backendplugin.SecretBundle{}
	if _, err := service.New().Configure(context.Background(), req); err == nil {
		t.Fatal("connector-side environment lookup must not authenticate: host Configure secrets are required")
	}
}
