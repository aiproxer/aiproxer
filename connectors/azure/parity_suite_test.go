package azure_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/azure/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

type memStream struct {
	ctx    context.Context
	inbox  []backendplugin.ClientFrame
	outbox []backendplugin.ServerFrame
	ri     int
}

func (m *memStream) Context() context.Context { return m.ctx }
func (m *memStream) Recv() (backendplugin.ClientFrame, error) {
	if m.ri >= len(m.inbox) {
		return backendplugin.ClientFrame{}, io.EOF
	}
	f := m.inbox[m.ri]
	m.ri++
	return f, nil
}

func (m *memStream) Send(frame backendplugin.ServerFrame) error {
	m.outbox = append(m.outbox, frame)
	return nil
}

func newTestExecuteStream(ctx context.Context, modelID string, op aipapi.Operation) *memStream {
	text := "hi"
	inv := backendplugin.Invocation{
		RequestID:        "r1",
		AttemptID:        "a1",
		ALegID:           "al1",
		BLegID:           "bl1",
		CanonicalModelID: modelID,
		Operation:        string(op),
		Messages: []backendplugin.Message{{
			Role:  backendplugin.RoleUser,
			Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: &text}},
		}},
		Options: backendplugin.GenerationOptions{ResponseSchemaJSON: backendplugin.RawJSONAbsentValue()},
	}
	return &memStream{
		ctx: ctx,
		inbox: []backendplugin.ClientFrame{{
			Kind:       backendplugin.ClientFrameStart,
			InstanceID: "inst",
			Invocation: &inv,
		}},
	}
}

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
	if fact.DisplayName != service.DisplayName {
		t.Fatalf("display_name=%s want %s", fact.DisplayName, service.DisplayName)
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

func TestConfigure_RejectsMissingInputs(t *testing.T) {
	t.Parallel()
	svc := service.New()

	// 1. Missing endpoint and resource_name
	_, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst1",
		ConfigYAML:  []byte("api_version: 2024-10-21\n"),
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("key123")}},
	})
	if err == nil || (!strings.Contains(strings.ToLower(err.Error()), "endpoint") && !strings.Contains(strings.ToLower(err.Error()), "resource")) {
		t.Fatalf("expected endpoint/resource error, got %v", err)
	}

	// 2. Missing api_version
	_, err = svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst2",
		ConfigYAML:  []byte("resource_name: my-res\n"),
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("key123")}},
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "api_version") {
		t.Fatalf("expected api_version error, got %v", err)
	}

	// 3. API-key mode with missing api_key in secrets
	_, err = svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst3",
		ConfigYAML:  []byte("resource_name: my-res\napi_version: 2024-10-21\ncredential_mode: api_key\n"),
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{}},
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "api_key") {
		t.Fatalf("expected api_key error, got %v", err)
	}

	// 4. Entra mode with no token provider or production chain in bare New()
	_, err = svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst4",
		ConfigYAML:  []byte("resource_name: my-res\napi_version: 2024-10-21\ncredential_mode: entra\n"),
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{}},
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "entra") {
		t.Fatalf("expected entra error, got %v", err)
	}
}

func TestConfigure_RejectsLiteralSecretsInYAML(t *testing.T) {
	t.Parallel()
	svc := service.New()

	cases := []struct {
		name string
		yaml string
	}{
		{"api_key", "resource_name: res\napi_version: 2024-10-21\napi_key: secret-key-123\n"},
		{"api_token", "resource_name: res\napi_version: 2024-10-21\napi_token: secret-tok-456\n"},
		{"token", "resource_name: res\napi_version: 2024-10-21\ntoken: secret-raw-789\n"},
		{"client_secret", "resource_name: res\napi_version: 2024-10-21\nclient_secret: secret-cs-012\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
				FactoryKind: service.FactoryKind,
				InstanceID:  "inst",
				ConfigYAML:  []byte(tc.yaml),
				Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("valid")}},
			})
			if err == nil {
				t.Fatal("expected error for literal secret in YAML, got nil")
			}
			if strings.Contains(err.Error(), "secret-") {
				t.Fatalf("error echoed sensitive secret value: %s", err.Error())
			}
		})
	}
}

func TestConfigure_BaseURLConstruction(t *testing.T) {
	t.Parallel()

	// 1. From resource_name
	cfg1, err := service.ParseConfigYAML([]byte("resource_name: my-openai-res\napi_version: 2024-10-21\n"))
	if err != nil {
		t.Fatal(err)
	}
	wantBase1 := "https://my-openai-res.openai.azure.com/openai/v1"
	if cfg1.BaseURL() != wantBase1 {
		t.Fatalf("BaseURL=%q want %q", cfg1.BaseURL(), wantBase1)
	}

	// 2. From endpoint (without /openai/v1)
	cfg2, err := service.ParseConfigYAML([]byte("endpoint: https://custom-domain.openai.azure.com\napi_version: 2024-10-21\n"))
	if err != nil {
		t.Fatal(err)
	}
	wantBase2 := "https://custom-domain.openai.azure.com/openai/v1"
	if cfg2.BaseURL() != wantBase2 {
		t.Fatalf("BaseURL=%q want %q", cfg2.BaseURL(), wantBase2)
	}

	// 3. From endpoint (with /openai/v1)
	cfg3, err := service.ParseConfigYAML([]byte("endpoint: https://custom-domain.openai.azure.com/openai/v1/\napi_version: 2024-10-21\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg3.BaseURL() != wantBase2 {
		t.Fatalf("BaseURL=%q want %q", cfg3.BaseURL(), wantBase2)
	}

	// 4. Rejection of /compat in endpoint
	_, err = service.ParseConfigYAML([]byte("endpoint: https://custom.openai.azure.com/openai/compat\napi_version: 2024-10-21\n"))
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "compat") {
		t.Fatalf("expected /compat error, got %v", err)
	}
}

func TestParity_APIKeyModeHeader(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var gotHeaders http.Header
	var gotQuery string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotHeaders = r.Header.Clone()
		gotQuery = r.URL.RawQuery
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp-1","output":[{"content":[{"type":"output_text","text":"hello"}]}]}`))
	}))
	t.Cleanup(srv.Close)

	svc := service.New()
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-key",
		ConfigYAML:  []byte("endpoint: " + srv.URL + "\napi_version: 2024-10-21\ncredential_mode: api_key\ndeployments:\n  gpt-4o: gpt-4o\n"),
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("az-key-12345")}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Drive the configured instance directly via Execute
	stream := newTestExecuteStream(context.Background(), "azure-openai/gpt-4o", aipapi.OperationOpenAIResponses)
	if err := inst.Execute(stream); err != nil {
		t.Fatalf("inst.Execute failed: %v", err)
	}

	mu.Lock()
	apiKeyHdr := gotHeaders.Get("api-key")
	authHdr := gotHeaders.Get("Authorization")
	q := gotQuery
	mu.Unlock()

	// 1. api-key header is set
	if apiKeyHdr != "az-key-12345" {
		t.Fatalf("api-key header=%q want az-key-12345", apiKeyHdr)
	}
	// 2. Authorization header is NOT set (no leaked Entra bearer)
	if authHdr != "" {
		t.Fatalf("Authorization header must NOT be set in api_key mode, got %q", authHdr)
	}
	// 3. api-version query param sent
	if !strings.Contains(q, "api-version=2024-10-21") {
		t.Fatalf("query string=%q must contain api-version=2024-10-21", q)
	}
}

func TestParity_EntraModeBearer(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var gotHeaders http.Header
	var gotQuery string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotHeaders = r.Header.Clone()
		gotQuery = r.URL.RawQuery
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp-1","output":[{"content":[{"type":"output_text","text":"hello"}]}]}`))
	}))
	t.Cleanup(srv.Close)

	fakeToken := "fake-entra-jwt-token-xyz"
	svc := service.New(service.WithTokenProvider(service.StaticTokenProvider(fakeToken)))

	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-entra",
		ConfigYAML:  []byte("endpoint: " + srv.URL + "\napi_version: 2024-10-21\ncredential_mode: entra\ndeployments:\n  gpt-4o: gpt-4o\n"),
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Drive the configured instance directly via Execute
	stream := newTestExecuteStream(context.Background(), "azure-openai/gpt-4o", aipapi.OperationOpenAIResponses)
	if err := inst.Execute(stream); err != nil {
		t.Fatalf("inst.Execute failed: %v", err)
	}

	mu.Lock()
	authHdr := gotHeaders.Get("Authorization")
	apiKeyHdr := gotHeaders.Get("api-key")
	q := gotQuery
	mu.Unlock()

	// 1. Authorization: Bearer <token> is set
	if authHdr != "Bearer "+fakeToken {
		t.Fatalf("Authorization header=%q want Bearer %s", authHdr, fakeToken)
	}
	// 2. api-key header is NOT set
	if apiKeyHdr != "" {
		t.Fatalf("api-key header must NOT be set in entra mode, got %q", apiKeyHdr)
	}
	// 3. api-version query param sent
	if !strings.Contains(q, "api-version=2024-10-21") {
		t.Fatalf("query string=%q must contain api-version=2024-10-21", q)
	}
	// 4. Token never appears in Describe
	desc, _ := svc.Describe(context.Background())
	if strings.Contains(fmt.Sprintf("%+v", desc), fakeToken) {
		t.Fatal("token leaked into Describe")
	}
}

func TestParity_HardNegativeResponsesNeverFallsBackToChat(t *testing.T) {
	t.Parallel()

	// Scenario A: Chat endpoint 404s, but Responses endpoint succeeds -> Responses call SUCCEEDS.
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/responses") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"resp-a","output":[{"content":[{"type":"output_text","text":"responses ok"}]}]}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srvA.Close)

	svc := service.New()
	instA, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-a",
		ConfigYAML:  []byte("endpoint: " + srvA.URL + "\napi_version: 2024-10-21\ndeployments:\n  gpt-4o: gpt-4o\n"),
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("k")}},
	})
	if err != nil {
		t.Fatal(err)
	}

	streamA := newTestExecuteStream(context.Background(), "azure-openai/gpt-4o", aipapi.OperationOpenAIResponses)
	if err := instA.Execute(streamA); err != nil {
		t.Fatalf("Responses call failed on configured instance when responses endpoint was live: %v", err)
	}

	// Scenario B: Chat endpoint 200s, but Responses endpoint 404s -> Responses call MUST FAIL (never fall back to chat).
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"chat-b","choices":[{"message":{"role":"assistant","content":"chat ok"}}]}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/responses") {
			http.NotFound(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srvB.Close)

	instB, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-b",
		ConfigYAML:  []byte("endpoint: " + srvB.URL + "\napi_version: 2024-10-21\ndeployments:\n  gpt-4o: gpt-4o\n"),
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("k")}},
	})
	if err != nil {
		t.Fatal(err)
	}

	streamB := newTestExecuteStream(context.Background(), "azure-openai/gpt-4o", aipapi.OperationOpenAIResponses)
	if err := instB.Execute(streamB); err == nil {
		t.Fatal("hard-negative violation: Responses call succeeded on configured instance via Chat fallback when responses 404'd!")
	}
}

func TestParity_InboundChatOperationUsesChat(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var requestedPaths []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requestedPaths = append(requestedPaths, r.URL.Path)
		mu.Unlock()

		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"chat-1","choices":[{"message":{"role":"assistant","content":"chat"}}]}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/responses") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"resp-1","output":[{"content":[{"type":"output_text","text":"resp"}]}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	svc := service.New()
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-chat",
		ConfigYAML:  []byte("endpoint: " + srv.URL + "\napi_version: 2024-10-21\ndeployments:\n  gpt-4o: gpt-4o\n"),
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("k")}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Inbound chat operation uses chat flavor through the configured instance
	stream := newTestExecuteStream(context.Background(), "azure-openai/gpt-4o", aipapi.OperationOpenAIChatCompletions)
	if err := inst.Execute(stream); err != nil {
		t.Fatalf("inst.Execute failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requestedPaths) != 1 || !strings.HasSuffix(requestedPaths[0], "/chat/completions") {
		t.Fatalf("expected 1 /chat/completions request, got %v", requestedPaths)
	}
}

func TestInventory_MapsDeployedModels(t *testing.T) {
	t.Parallel()

	// Any /models hit is a bug: Azure inference routes by deployment name
	// (user-configurable, need not equal the underlying model), so inventory
	// is deployment-driven and must never dial the models endpoint.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected outbound %s %s: azure inventory must be deployment-driven", r.Method, r.URL.Path)
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	svc := service.New()
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-inv",
		ConfigYAML:  []byte("endpoint: " + srv.URL + "\napi_version: 2024-10-21\ncredential_mode: api_key\ndeployments:\n  coding-prod: gpt-5.x\n  chat-eu: gpt-4o\n  embed-prod: text-embedding-3-large\n"),
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("k")}},
	})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := inst.ListModels(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}

	// Embedding-backed deployments stay out of Responses inventory; the rest
	// is advertised by deployment name in stable sorted order.
	wantCanonical := []string{"azure-openai/chat-eu", "azure-openai/coding-prod"}
	if len(resp.Models) != len(wantCanonical) {
		var got []string
		for _, m := range resp.Models {
			got = append(got, m.CanonicalModelID)
		}
		t.Fatalf("got %d models (%v), want %d (%v)", len(resp.Models), got, len(wantCanonical), wantCanonical)
	}

	for i, want := range wantCanonical {
		got := resp.Models[i]
		if got.CanonicalModelID != want {
			t.Fatalf("model %d CanonicalModelID=%q want %q", i, got.CanonicalModelID, want)
		}
		deployment := strings.TrimPrefix(want, "azure-openai/")
		if got.NativeModelID != deployment {
			t.Fatalf("model %d NativeModelID=%q want deployment name %q", i, got.NativeModelID, deployment)
		}
		if got.FactoryKind != service.FactoryKind {
			t.Fatalf("model %d FactoryKind=%q want %q", i, got.FactoryKind, service.FactoryKind)
		}
	}

	// Bare underlying model IDs must never be advertised as routable identities.
	for _, m := range resp.Models {
		if m.NativeModelID == "gpt-5.x" || m.NativeModelID == "gpt-4o" ||
			m.NativeModelID == "text-embedding-3-large" {
			t.Fatalf("bare model ID %q advertised; inventory must list deployment names only", m.NativeModelID)
		}
	}

	// DisplayName surfaces the underlying model for operators.
	if resp.Models[1].DisplayName != "coding-prod (gpt-5.x)" {
		t.Fatalf("DisplayName=%q want %q", resp.Models[1].DisplayName, "coding-prod (gpt-5.x)")
	}

	// Limit is honored.
	limited, err := inst.ListModels(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited.Models) != 1 || limited.Models[0].CanonicalModelID != "azure-openai/chat-eu" {
		t.Fatalf("limited inventory=%+v want [azure-openai/chat-eu]", limited.Models)
	}
}

func TestNewProduction_WiresEntraCredentialChain(t *testing.T) {
	t.Parallel()

	svcProd := service.NewProduction()
	svcDefault := service.New()

	// 1. Prove NewProduction is not the empty New()
	if svcProd == nil || svcDefault == nil {
		t.Fatal("expected non-nil services")
	}

	// 2. Default New() with credential_mode: entra and no token provider fails closed with descriptive error
	_, err := svcDefault.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-def",
		ConfigYAML:  []byte("resource_name: res\napi_version: 2024-10-21\ncredential_mode: entra\n"),
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{}},
	})
	if err == nil || !strings.Contains(err.Error(), "NewProduction") {
		t.Fatalf("expected NewProduction guidance in entra error, got %v", err)
	}

	// 3. NewProduction with client secret credentials configures successfully
	instProd, err := svcProd.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-prod",
		ConfigYAML:  []byte("resource_name: res\napi_version: 2024-10-21\ncredential_mode: entra\ntenant_id: ten-1\nclient_id: cli-1\ndeployments:\n  gpt-4o: gpt-4o\n"),
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{"client_secret": []byte("cs-1")}},
	})
	if err != nil {
		t.Fatalf("NewProduction with client secret failed: %v", err)
	}
	if instProd == nil {
		t.Fatal("expected non-nil configured instance")
	}
}

func TestConfigure_RejectsMissingDeployments(t *testing.T) {
	t.Parallel()
	svc := service.New()
	secrets := backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("k")}}

	// 1. No deployments key at all.
	_, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-nodeploy",
		ConfigYAML:  []byte("endpoint: https://example.openai.azure.com\napi_version: 2024-10-21\ncredential_mode: api_key\n"),
		Secrets:     secrets,
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "deployments") {
		t.Fatalf("expected deployments error, got %v", err)
	}

	// 2. Empty deployments map.
	_, err = svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-emptydeploy",
		ConfigYAML:  []byte("endpoint: https://example.openai.azure.com\napi_version: 2024-10-21\ncredential_mode: api_key\ndeployments: {}\n"),
		Secrets:     secrets,
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "deployments") {
		t.Fatalf("expected deployments error for empty map, got %v", err)
	}
}

func TestParseConfig_RejectsEmptyDeploymentEntries(t *testing.T) {
	t.Parallel()
	_, err := service.ParseConfigYAML([]byte("endpoint: https://example.openai.azure.com\napi_version: 2024-10-21\ndeployments:\n  coding-prod: ''\n"))
	if err == nil || !strings.Contains(err.Error(), "coding-prod") {
		t.Fatalf("expected coding-prod deployment error, got %v", err)
	}
}

func TestExecute_RoutesDeploymentNameOnWire(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err == nil {
			mu.Lock()
			if m, ok := payload["model"].(string); ok {
				gotModel = m
			}
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp-1","output":[{"content":[{"type":"output_text","text":"hello"}]}]}`))
	}))
	t.Cleanup(srv.Close)

	svc := service.New()
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-deploywire",
		ConfigYAML:  []byte("endpoint: " + srv.URL + "\napi_version: 2024-10-21\ncredential_mode: api_key\ndeployments:\n  coding-prod: gpt-5.x\n"),
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("k")}},
	})
	if err != nil {
		t.Fatal(err)
	}

	stream := newTestExecuteStream(context.Background(), "azure-openai/coding-prod", aipapi.OperationOpenAIResponses)
	if err := inst.Execute(stream); err != nil {
		t.Fatalf("inst.Execute failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotModel != "coding-prod" {
		t.Fatalf("wire model=%q want %q (deployment name, not underlying model ID)", gotModel, "coding-prod")
	}
}

func TestExecute_RejectsUnmappedModelID(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected outbound %s %s: unmapped models must fail before any HTTP", r.Method, r.URL.Path)
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	svc := service.New()
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-unmapped",
		ConfigYAML:  []byte("endpoint: " + srv.URL + "\napi_version: 2024-10-21\ncredential_mode: api_key\ndeployments:\n  coding-prod: gpt-5.x\n"),
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("k")}},
	})
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name  string
		model string
	}{
		{"underlying model ID is not routable", "azure-openai/gpt-5.x"},
		{"unknown deployment", "azure-openai/gpt-4o"},
		{"empty deployment", "azure-openai/"},
		{"bare kind", "azure-openai"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stream := newTestExecuteStream(context.Background(), tc.model, aipapi.OperationOpenAIResponses)
			err := inst.Execute(stream)
			if err == nil {
				t.Fatalf("expected unknown-deployment error for %q, got nil", tc.model)
			}
			if !strings.Contains(strings.ToLower(err.Error()), "deployment") {
				t.Fatalf("error for %q must name the deployment problem, got %v", tc.model, err)
			}
		})
	}
}
