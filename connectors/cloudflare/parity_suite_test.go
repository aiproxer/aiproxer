package cloudflare_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/connector-support/openaicompat"
	"github.com/aiproxer/aiproxer/connectors/cloudflare/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func TestDescribe_FactoryKind(t *testing.T) {
	t.Parallel()
	d, err := service.New().Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if d.PluginID != service.PluginID {
		t.Fatalf("plugin_id=%s want %s", d.PluginID, service.PluginID)
	}
	if len(d.Factories) == 0 {
		t.Fatal("no factories described")
	}
	fact := d.Factories[0]
	if fact.Kind != service.FactoryKind {
		t.Fatalf("kind=%s want %s", fact.Kind, service.FactoryKind)
	}
	if fact.DisplayName != "Cloudflare AI Gateway" {
		t.Fatalf("display_name=%s want Cloudflare AI Gateway", fact.DisplayName)
	}
	if len(fact.RoutePrefixes) == 0 || fact.RoutePrefixes[0] != service.FactoryKind {
		t.Fatalf("route_prefixes=%v want [%s]", fact.RoutePrefixes, service.FactoryKind)
	}
	if d.ProtocolMajor != 1 {
		t.Fatalf("protocol_major=%d want 1", d.ProtocolMajor)
	}
	if d.ProtocolMinor != backendplugin.ProtocolMinorCancellationHandshake {
		t.Fatalf("protocol_minor=%d want %d", d.ProtocolMinor, backendplugin.ProtocolMinorCancellationHandshake)
	}
	hasHandshake := false
	for _, feat := range d.Features {
		if feat.Name == backendplugin.FeatureCancellationHandshake {
			hasHandshake = true
			if feat.Required {
				t.Fatal("FeatureCancellationHandshake must be optional (Required false)")
			}
		}
	}
	if !hasHandshake {
		t.Fatal("missing FeatureCancellationHandshake in described features")
	}
}

func TestConfigure_RejectsMissingAccountIdOrToken(t *testing.T) {
	t.Parallel()
	svc := service.New()

	// Missing account_id
	_, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst1",
		ConfigYAML:  []byte("gateway_id: gw1\n"),
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{"api_token": []byte("tok")}},
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "account_id") {
		t.Fatalf("expected account_id error, got %v", err)
	}

	// Missing token and api_key
	_, err = svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst2",
		ConfigYAML:  []byte("account_id: acc1\n"),
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{}},
	})
	if err == nil || (!strings.Contains(strings.ToLower(err.Error()), "token") && !strings.Contains(strings.ToLower(err.Error()), "api_key")) {
		t.Fatalf("expected token/api_key error, got %v", err)
	}

	// Token via api_token succeeds
	_, err = svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst3",
		ConfigYAML:  []byte("account_id: acc1\n"),
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{"api_token": []byte("tok123")}},
	})
	if err != nil {
		t.Fatalf("unexpected error with api_token: %v", err)
	}

	// Token via api_key is rejected: the canonical api_token[_N] slots converge
	// legacy aliases onto the single shared CLOUDFLARE_API_TOKEN root.
	_, err = svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst4",
		ConfigYAML:  []byte("account_id: acc1\n"),
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("key123")}},
	})
	if err == nil || !strings.Contains(err.Error(), "api_token") || !strings.Contains(err.Error(), "CLOUDFLARE_API_TOKEN") {
		t.Fatalf("expected canonical api_token slot error naming env root, got %v", err)
	}
}

func TestConfigure_RejectsLiteralSecretsInYAML(t *testing.T) {
	t.Parallel()
	svc := service.New()

	cases := []struct {
		name string
		yaml string
	}{
		{"api_token", "account_id: acc1\napi_token: secret-value-123\n"},
		{"api_key", "account_id: acc1\napi_key: secret-value-456\n"},
		{"token", "account_id: acc1\ntoken: secret-value-789\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
				FactoryKind: service.FactoryKind,
				InstanceID:  "inst",
				ConfigYAML:  []byte(tc.yaml),
				Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{"api_token": []byte("valid")}},
			})
			if err == nil {
				t.Fatal("expected error for literal secret in YAML, got nil")
			}
			if strings.Contains(err.Error(), "secret-value") {
				t.Fatalf("error echoed sensitive secret value: %s", err.Error())
			}
		})
	}
}

func TestConfigure_ConstructedBaseURL(t *testing.T) {
	t.Parallel()
	cfg, err := service.ParseConfigYAML([]byte("account_id: test-acc\n"))
	if err != nil {
		t.Fatal(err)
	}
	wantBase := "https://api.cloudflare.com/client/v4/accounts/test-acc/ai/v1"
	if cfg.BaseURL() != wantBase {
		t.Fatalf("BaseURL=%q want %q", cfg.BaseURL(), wantBase)
	}
	if strings.Contains(cfg.BaseURL(), "/compat") {
		t.Fatalf("BaseURL must not contain /compat: %s", cfg.BaseURL())
	}
}

func TestConfigure_CustomAPIOrigin(t *testing.T) {
	t.Parallel()
	cfg, err := service.ParseConfigYAML([]byte("account_id: custom-acc\napi_origin: http://127.0.0.1:9090/\n"))
	if err != nil {
		t.Fatal(err)
	}
	wantBase := "http://127.0.0.1:9090/client/v4/accounts/custom-acc/ai/v1"
	if cfg.BaseURL() != wantBase {
		t.Fatalf("BaseURL=%q want %q", cfg.BaseURL(), wantBase)
	}
}

func TestConfigure_RejectsCompatEndpoint(t *testing.T) {
	t.Parallel()
	_, err := service.ParseConfigYAML([]byte("account_id: acc1\napi_origin: https://api.cloudflare.com/client/v4/accounts/acc1/ai/compat\n"))
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "compat") {
		t.Fatalf("expected /compat rejection error, got %v", err)
	}
}

func TestParity_GatewayHeader(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var gotHeaders http.Header

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotHeaders = r.Header.Clone()
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"@cf/meta/llama-3.1-8b-instruct"}]}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp-1","output":[{"content":[{"type":"output_text","text":"hi"}]}]}`))
	}))
	t.Cleanup(srv.Close)

	// Case 1: gateway_id is set -> cf-aig-gateway-id header present
	instWithGW, err := service.New().Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-gw",
		ConfigYAML:  []byte("account_id: acc-123\ngateway_id: my-prod-gw\napi_origin: " + srv.URL + "\n"),
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{"api_token": []byte("tok-test")}},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = instWithGW.ListModels(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	gwHdr := gotHeaders.Get("cf-aig-gateway-id")
	authHdr := gotHeaders.Get("Authorization")
	mu.Unlock()

	if gwHdr != "my-prod-gw" {
		t.Fatalf("cf-aig-gateway-id header = %q, want %q", gwHdr, "my-prod-gw")
	}
	if authHdr != "Bearer tok-test" {
		t.Fatalf("Authorization header = %q, want Bearer tok-test", authHdr)
	}

	// Case 2: gateway_id omitted -> cf-aig-gateway-id header absent
	instNoGW, err := service.New().Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-nogw",
		ConfigYAML:  []byte("account_id: acc-123\napi_origin: " + srv.URL + "\n"),
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{"api_token": []byte("tok-test")}},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = instNoGW.ListModels(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	gwHdrNoGW := gotHeaders.Get("cf-aig-gateway-id")
	mu.Unlock()

	if gwHdrNoGW != "" {
		t.Fatalf("cf-aig-gateway-id header must be absent when unset, got %q", gwHdrNoGW)
	}
}

func TestParity_ResponsesPreferredRouting(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var requestedPaths []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requestedPaths = append(requestedPaths, r.URL.Path)
		mu.Unlock()

		if strings.HasSuffix(r.URL.Path, "/responses") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"resp-1","output":[{"content":[{"type":"output_text","text":"hello from responses"}]}]}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"chat-1","choices":[{"message":{"role":"assistant","content":"hello from chat"}}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	cfg, err := service.ParseConfigYAML([]byte("account_id: test-acc\napi_origin: " + srv.URL + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	cl := service.NewCompatClient(cfg, "tok", srv.Client(), service.ProviderHooks(cfg))

	// 1. OperationOpenAIResponses hits /responses
	callResponses := aipapi.Call{
		Messages:   []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "hi"}}}},
		Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIResponses, DeliveryMode: aipapi.DeliveryModeNonStreaming, TransportMode: aipapi.TransportModeNonStreaming},
	}
	flavorResp := service.ResolveFlavor(callResponses)
	if flavorResp != openaicompat.FlavorResponses {
		t.Fatalf("flavor for OperationOpenAIResponses = %v want %v", flavorResp, openaicompat.FlavorResponses)
	}
	st, err := cl.Open(context.Background(), callResponses, "test-model", flavorResp)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	// 2. Ambiguous operation defaults to responses
	callAmbiguous := aipapi.Call{
		Messages:   []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "hi"}}}},
		Invocation: aipapi.Invocation{Operation: "", DeliveryMode: aipapi.DeliveryModeNonStreaming, TransportMode: aipapi.TransportModeNonStreaming},
	}
	flavorAmbiguous := service.ResolveFlavor(callAmbiguous)
	if flavorAmbiguous != openaicompat.FlavorResponses {
		t.Fatalf("flavor for ambiguous operation = %v want %v", flavorAmbiguous, openaicompat.FlavorResponses)
	}
	st2, err := cl.Open(context.Background(), callAmbiguous, "test-model", flavorAmbiguous)
	if err != nil {
		t.Fatal(err)
	}
	_ = st2.Close()

	// 3. OperationOpenAIChatCompletions uses chat
	callChat := aipapi.Call{
		Messages:   []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "hi"}}}},
		Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions, DeliveryMode: aipapi.DeliveryModeNonStreaming, TransportMode: aipapi.TransportModeNonStreaming},
	}
	flavorChat := service.ResolveFlavor(callChat)
	if flavorChat != openaicompat.FlavorChat {
		t.Fatalf("flavor for OperationOpenAIChatCompletions = %v want %v", flavorChat, openaicompat.FlavorChat)
	}
	st3, err := cl.Open(context.Background(), callChat, "test-model", flavorChat)
	if err != nil {
		t.Fatal(err)
	}
	_ = st3.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(requestedPaths) != 3 {
		t.Fatalf("expected 3 requests, got %d: %v", len(requestedPaths), requestedPaths)
	}

	wantRespPath := "/client/v4/accounts/test-acc/ai/v1/responses"
	wantChatPath := "/client/v4/accounts/test-acc/ai/v1/chat/completions"

	if requestedPaths[0] != wantRespPath {
		t.Fatalf("request 0 path = %q, want %q", requestedPaths[0], wantRespPath)
	}
	if requestedPaths[1] != wantRespPath {
		t.Fatalf("request 1 path = %q, want %q", requestedPaths[1], wantRespPath)
	}
	if requestedPaths[2] != wantChatPath {
		t.Fatalf("request 2 path = %q, want %q", requestedPaths[2], wantChatPath)
	}

	for _, p := range requestedPaths {
		if strings.Contains(p, "/compat") {
			t.Fatalf("request path must not contain /compat: %s", p)
		}
	}
}

func TestInventory_FilteredModels(t *testing.T) {
	t.Parallel()

	mixedPayload := `{
		"data": [
			{"id": "@cf/meta/llama-3.1-8b-instruct", "owned_by": "meta"},
			{"id": "@cf/qwen/qwen2.5-coder-32b-instruct", "owned_by": "qwen"},
			{"id": "@cf/baai/bge-large-en-v1.5", "owned_by": "baai"},
			{"id": "@cf/baai/bge-base-en-v1.5", "owned_by": "baai"},
			{"id": "text-embedding-3-small", "owned_by": "openai"},
			{"id": "@cf/cohere/rerank-multilingual-v3.0", "owned_by": "cohere"},
			{"id": "@cf/black-forest-labs/flux-1-schnell", "owned_by": "black-forest-labs"},
			{"id": "stable-diffusion-xl-base-1.0", "owned_by": "stability"},
			{"id": "dall-e-3", "owned_by": "openai"},
			{"id": "@cf/openai/whisper", "owned_by": "openai"},
			{"id": "tts-1", "owned_by": "openai"},
			{"id": "", "owned_by": ""},
			{"id": "   ", "owned_by": ""}
		]
	}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(mixedPayload))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	inst, err := service.New().Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-filter",
		ConfigYAML:  []byte("account_id: test-acc\napi_origin: " + srv.URL + "\n"),
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{"api_token": []byte("tok-test")}},
	})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := inst.ListModels(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}

	wantIDs := []string{
		"@cf/meta/llama-3.1-8b-instruct",
		"@cf/qwen/qwen2.5-coder-32b-instruct",
	}

	if len(resp.Models) != len(wantIDs) {
		var got []string
		for _, m := range resp.Models {
			got = append(got, m.NativeModelID)
		}
		t.Fatalf("got %d models (%v), want %d (%v)", len(resp.Models), got, len(wantIDs), wantIDs)
	}

	for i, want := range wantIDs {
		if resp.Models[i].NativeModelID != want {
			t.Fatalf("model %d NativeModelID=%q want %q", i, resp.Models[i].NativeModelID, want)
		}
		wantCanonical := "cloudflare/" + want
		if resp.Models[i].CanonicalModelID != wantCanonical {
			t.Fatalf("model %d CanonicalModelID=%q want %q", i, resp.Models[i].CanonicalModelID, wantCanonical)
		}
	}

	// Test limit
	respLimited, err := inst.ListModels(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(respLimited.Models) != 1 {
		t.Fatalf("limit 1 returned %d models", len(respLimited.Models))
	}
}
