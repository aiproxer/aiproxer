package huggingface_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/connector-support/openaicompat"
	"github.com/aiproxer/aiproxer/connectors/huggingface/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/conformance"
)

func TestDescribe_FactoryKind(t *testing.T) {
	t.Parallel()
	d, err := service.New().Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if d.Factories[0].Kind != service.FactoryKind || d.Factories[0].RoutePrefixes[0] != service.FactoryKind {
		t.Fatalf("%+v", d.Factories[0])
	}
}

func TestConfigure_DefaultsAndAPIKey(t *testing.T) {
	t.Parallel()
	cfg, err := service.ParseConfigYAML(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != service.DefaultURL {
		t.Fatalf("base=%s", cfg.BaseURL)
	}
	_, err = service.New().Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind, InstanceID: "1", ConfigYAML: []byte(""),
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	})
	if err == nil || !strings.Contains(err.Error(), "api_key") {
		t.Fatalf("err=%v", err)
	}
}

func TestParity_ProviderRouteParamSuffix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, model, provider, want string
	}{
		{"append", "openai/gpt-oss-120b", "sambanova", "openai/gpt-oss-120b:sambanova"},
		{"existing_suffix", "openai/gpt-oss-120b:fastest", "sambanova", "openai/gpt-oss-120b:fastest"},
		{"blank_provider", "openai/gpt-oss-120b", "  ", "openai/gpt-oss-120b"},
		{"trim", "openai/gpt-oss-120b", "  sambanova  ", "openai/gpt-oss-120b:sambanova"},
		{"no_slash", "gpt-oss-120b", "sambanova", "gpt-oss-120b:sambanova"},
		{"empty_model", "", "sambanova", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := service.ApplyProviderSuffix(tc.model, tc.provider); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}

	var mu sync.Mutex
	var model string
	srv := httptest.NewServer(openaicompat.NewEmulator(openaicompat.EmulatorConfig{
		RequireBearer: true,
		OnRequestBody: func(b []byte) {
			var m map[string]any
			_ = json.Unmarshal(b, &m)
			mu.Lock()
			model, _ = m["model"].(string)
			mu.Unlock()
		},
	}))
	t.Cleanup(srv.Close)
	cfg, err := service.ParseConfigYAML([]byte("base_url: " + srv.URL + "/v1\n"))
	if err != nil {
		t.Fatal(err)
	}
	cl := service.NewCompatClient(cfg, srv.Client(), "sk", openaicompat.RequestHooks{})
	call := aipapi.Call{
		Messages:   []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "hi"}}}},
		Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions, DeliveryMode: aipapi.DeliveryModeStreaming, TransportMode: aipapi.TransportModeStreaming},
	}
	native := service.ApplyProviderSuffix("openai/gpt-oss-120b", "sambanova")
	es, err := cl.Open(context.Background(), call, native, openaicompat.FlavorChat)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = es.Close() }()
	for {
		_, err := es.Recv(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if model != "openai/gpt-oss-120b:sambanova" {
		t.Fatalf("model=%q", model)
	}
}

func TestParity_InventoryErrorsPropagate(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(openaicompat.NewEmulator(openaicompat.EmulatorConfig{
		ForcedStatus: 500, ForcedBody: `{"error":{"message":"down"}}`,
	}))
	t.Cleanup(srv.Close)
	inst, err := service.New().Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind, InstanceID: "1",
		ConfigYAML:    []byte("base_url: " + srv.URL + "/v1\n"),
		Secrets:       backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("sk")}},
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = inst.ListModels(context.Background(), 10)
	if err == nil {
		t.Fatal("expected inventory error")
	}
}

func TestParity_ConformanceAdvertised(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(openaicompat.NewEmulator(openaicompat.EmulatorConfig{RequireBearer: true}))
	t.Cleanup(srv.Close)
	rep := conformance.RunWith(context.Background(), openaicompat.SecretBackedService{
		Service: service.New(),
		Secrets: backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("conformance-key")}},
	}, conformance.Options{
		ConfigYAML: []byte("base_url: " + srv.URL + "/v1\n"), SampleModel: "emu-model", DisableUsageRequirement: true,
	})
	if !rep.Ok() {
		t.Fatalf("failures=%v", rep.Failures())
	}
}
