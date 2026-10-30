package ollama_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/connector-support/openaicompat"
	"github.com/aiproxer/aiproxer/connectors/ollama/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/conformance"
)

func TestDescribe_BothFactories(t *testing.T) {
	t.Parallel()
	d, err := service.New().Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]backendplugin.FactoryDescriptor{}
	for _, f := range d.Factories {
		kinds[f.Kind] = f
	}
	local, ok := kinds[service.FactoryKind]
	if !ok || local.AccessScope != backendplugin.AccessScopeLocalOnly || local.CredentialMode != backendplugin.CredentialModeNone {
		t.Fatalf("local=%+v", local)
	}
	cloud, ok := kinds[service.FactoryKindCloud]
	if !ok || cloud.AccessScope != backendplugin.AccessScopeAny || cloud.CredentialMode != backendplugin.CredentialModeStatic {
		t.Fatalf("cloud=%+v", cloud)
	}
}

func TestConfigure_LocalDefaultsNoCloudDummy(t *testing.T) {
	t.Parallel()
	localCfg, err := service.ParseConfigYAML(service.FactoryKind, nil)
	if err != nil {
		t.Fatal(err)
	}
	if localCfg.BaseURL != service.DefaultLocalURL || localCfg.APIKey != service.DummyLocalKey {
		t.Fatalf("local=%+v", localCfg)
	}
	cloudCfg, err := service.ParseConfigYAML(service.FactoryKindCloud, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cloudCfg.BaseURL != service.DefaultCloudURL || cloudCfg.APIKey != "" || cloudCfg.ModelsURL != service.DefaultCloudTags {
		t.Fatalf("cloud=%+v", cloudCfg)
	}
	_, err = service.New().Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKindCloud, InstanceID: "c1", ConfigYAML: []byte(""),
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	})
	if err == nil || !strings.Contains(err.Error(), "api_key") {
		t.Fatalf("cloud without key err=%v", err)
	}
	_, err = service.New().Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind, InstanceID: "l1", ConfigYAML: []byte(""),
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestParity_LocalInventoryCapsAndError(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.Handle("/v1/", openaicompat.NewEmulator(openaicompat.EmulatorConfig{
		ModelsJSON: `{"data":[{"id":"llama3"}]}`,
	}))
	mux.HandleFunc("/api/show", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": []string{"completion", "tools", "vision"}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	inst, err := service.New().Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind, InstanceID: "l1",
		ConfigYAML:    []byte("base_url: " + srv.URL + "/v1\nnative_root: " + srv.URL + "\n"),
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := inst.ListModels(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Models) != 1 || !resp.Models[0].Capabilities.Tools || resp.Models[0].Capabilities.Vision {
		t.Fatalf("%+v", resp.Models)
	}

	bad := httptest.NewServer(openaicompat.NewEmulator(openaicompat.EmulatorConfig{ForcedStatus: 500}))
	t.Cleanup(bad.Close)
	inst2, err := service.New().Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind, InstanceID: "l2",
		ConfigYAML:    []byte("base_url: " + bad.URL + "/v1\n"),
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = inst2.ListModels(context.Background(), 10)
	if err == nil {
		t.Fatal("expected local inventory error")
	}
}

func TestParity_CloudInventoryIndependent(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]string{{"name": "llama3-cloud"}}})
	}))
	t.Cleanup(srv.Close)
	inst, err := service.New().Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKindCloud, InstanceID: "c1",
		ConfigYAML:    []byte("base_url: https://ollama.com/v1\nmodels_url: " + srv.URL + "/api/tags\n"),
		Secrets:       backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("cloudkey")}},
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := inst.ListModels(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Models) != 1 || resp.Models[0].NativeModelID != "llama3" || resp.Models[0].FactoryKind != service.FactoryKindCloud {
		t.Fatalf("%+v", resp.Models)
	}
}

func TestParity_ConformanceAdvertisedLocal(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(openaicompat.NewEmulator(openaicompat.EmulatorConfig{}))
	t.Cleanup(srv.Close)
	opts := conformance.Options{
		ConfigYAML: []byte("base_url: " + srv.URL + "/v1\n"), SampleModel: "emu-model", DisableUsageRequirement: true,
	}
	rep := conformance.RunWith(context.Background(), service.New(), opts)
	if !rep.Ok() {
		t.Fatalf("failures=%v", rep.Failures())
	}
}

func TestParity_CancelContext(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	cfg, err := service.ParseConfigYAML(service.FactoryKind, []byte("base_url: "+srv.URL+"/v1\n"))
	if err != nil {
		t.Fatal(err)
	}
	cl := service.NewCompatClient(cfg, srv.Client(), openaicompat.RequestHooks{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = cl.Open(ctx, aipapi.Call{
		Messages:   []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "hi"}}}},
		Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions, DeliveryMode: aipapi.DeliveryModeStreaming, TransportMode: aipapi.TransportModeStreaming},
	}, "m", openaicompat.FlavorChat)
	if err == nil {
		t.Fatal("expected cancel")
	}
}
