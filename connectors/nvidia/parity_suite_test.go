package nvidia_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/connector-support/openaicompat"
	"github.com/aiproxer/aiproxer/connectors/nvidia/internal/service"
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
	if d.Factories[0].Kind != service.FactoryKind {
		t.Fatal("missing kind")
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
		Secrets:       backendplugin.SecretBundle{},
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	})
	if err == nil || !strings.Contains(err.Error(), "api_key") {
		t.Fatalf("err=%v", err)
	}
}

func TestParity_PayloadRemapAndBoundedExtraBody(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var body []byte
	srv := httptest.NewServer(openaicompat.NewEmulator(openaicompat.EmulatorConfig{
		RequireBearer: true,
		OnRequestBody: func(b []byte) {
			mu.Lock()
			body = append([]byte(nil), b...)
			mu.Unlock()
		},
	}))
	t.Cleanup(srv.Close)
	cfg, err := service.ParseConfigYAML([]byte("base_url: " + srv.URL + "/v1\n"))
	if err != nil {
		t.Fatal(err)
	}
	maxTok := 128
	call := aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "hi"}}}},
		Options:  aipapi.GenerationOptions{MaxOutputTokens: &maxTok},
		Extensions: map[string]json.RawMessage{
			"nvidia.extra_body.safe_field": json.RawMessage(`"ok"`),
			"nvidia.extra_body.bad.nested": json.RawMessage(`"no"`),
			"openai.extra_body.other_safe": json.RawMessage(`1`),
		},
		Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions, DeliveryMode: aipapi.DeliveryModeStreaming, TransportMode: aipapi.TransportModeStreaming},
	}
	cl := service.NewCompatClient(cfg, srv.Client(), "sk", service.ProviderHooks())
	es, err := cl.Open(context.Background(), call, "m", openaicompat.FlavorChat)
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
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}
	if _, ok := parsed["stream_options"]; ok {
		t.Fatal("stream_options must be stripped")
	}
	if parsed["max_tokens"] != float64(128) {
		t.Fatalf("max_tokens=%v", parsed["max_tokens"])
	}
	if _, ok := parsed["max_completion_tokens"]; ok {
		t.Fatal("max_completion_tokens must be removed")
	}
	if parsed["safe_field"] != "ok" || parsed["other_safe"] != float64(1) {
		t.Fatalf("extra=%v", parsed)
	}
	if _, ok := parsed["bad.nested"]; ok {
		t.Fatal("unsafe field injected")
	}
}

func TestParity_InventoryError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(openaicompat.NewEmulator(openaicompat.EmulatorConfig{ForcedStatus: 503}))
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
	_, err = inst.ListModels(context.Background(), 8)
	if err == nil {
		t.Fatal("expected error")
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
