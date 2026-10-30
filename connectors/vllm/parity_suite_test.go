package vllm_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aiproxer/aiproxer/connector-support/openaicompat"
	"github.com/aiproxer/aiproxer/connectors/vllm/internal/service"
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
	f := d.Factories[0]
	if f.Kind != service.FactoryKind || f.AccessScope != backendplugin.AccessScopeLocalOnly {
		t.Fatalf("%+v", f)
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
	inst, err := service.New().Configure(context.Background(), backendplugin.ConfigureRequest{FactoryKind: service.FactoryKind, ConfigYAML: []byte(""), Negotiation: backendplugin.Negotiation{Compatible: true}})
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
		_, _ = w.Write([]byte(`{"data":[{"id":"model-a"}]}`))
	}))
	t.Cleanup(srv.Close)
	inst, err := service.New().Configure(context.Background(), backendplugin.ConfigureRequest{FactoryKind: service.FactoryKind, ConfigYAML: []byte("base_url: " + srv.URL + "\n"), Negotiation: backendplugin.Negotiation{Compatible: true}})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := inst.ListModels(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Models) != 1 || listed.Models[0].Capabilities != (backendplugin.CapabilitySummary{Streaming: true}) {
		t.Fatalf("listed=%+v", listed.Models)
	}
}

func TestConfigure_Defaults(t *testing.T) {
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
		Negotiation: backendplugin.Negotiation{Compatible: true}, RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestParity_ChatOnlyRejectsResponses(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(openaicompat.NewEmulator(openaicompat.EmulatorConfig{}))
	t.Cleanup(srv.Close)
	cfg, _ := service.ParseConfigYAML([]byte("base_url: " + srv.URL + "/v1\n"))
	cl := service.NewCompatClient(cfg, srv.Client(), openaicompat.RequestHooks{})
	_, err := cl.Open(context.Background(), aipapi.Call{
		Messages:   []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "hi"}}}},
		Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIResponses, DeliveryMode: aipapi.DeliveryModeStreaming, TransportMode: aipapi.TransportModeStreaming},
	}, "m", openaicompat.FlavorResponses)
	if err == nil {
		t.Fatal("expected responses reject")
	}
}

func TestParity_InventoryError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(openaicompat.NewEmulator(openaicompat.EmulatorConfig{ForcedStatus: 500}))
	t.Cleanup(srv.Close)
	inst, err := service.New().Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind, InstanceID: "1",
		ConfigYAML:  []byte("base_url: " + srv.URL + "/v1\n"),
		Negotiation: backendplugin.Negotiation{Compatible: true}, RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = inst.ListModels(context.Background(), 4)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestParity_ConformanceAdvertised(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(openaicompat.NewEmulator(openaicompat.EmulatorConfig{}))
	t.Cleanup(srv.Close)
	rep := conformance.RunWith(context.Background(), service.New(), conformance.Options{
		ConfigYAML: []byte("base_url: " + srv.URL + "/v1\n"), SampleModel: "emu-model", DisableUsageRequirement: true,
	})
	if !rep.Ok() {
		t.Fatalf("failures=%v", rep.Failures())
	}
}
