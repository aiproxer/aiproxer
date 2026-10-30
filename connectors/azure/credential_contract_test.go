package azure_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/azure/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/credpool"
)

func azSecretReq(yaml string, secrets map[string]string) backendplugin.ConfigureRequest {
	values := make(map[string][]byte, len(secrets))
	for name, value := range secrets {
		values[name] = []byte(value)
	}
	return backendplugin.ConfigureRequest{
		FactoryKind:   service.FactoryKind,
		InstanceID:    "cred-contract",
		ConfigYAML:    []byte(yaml),
		Secrets:       backendplugin.SecretBundle{Values: values},
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	}
}

type azScriptedResponse struct {
	status int
	header map[string]string
	body   string
}

// azScriptedKeys serves every path with per-api-key scripted statuses and
// records every presented api-key header in order. Azure key-mode carries
// the credential in the api-key header, never Authorization.
func azScriptedKeys(t *testing.T, script map[string]azScriptedResponse) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var presented []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("api-key")
		mu.Lock()
		presented = append(presented, key)
		mu.Unlock()
		resp, ok := script[key]
		if !ok {
			resp = script["*"]
		}
		for k, v := range resp.header {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.status)
		_, _ = w.Write([]byte(resp.body))
	}))
	t.Cleanup(srv.Close)
	return srv, &presented
}

const azChatBody = `{"id":"chatcmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`

func azKeyBaseYAML(srv *httptest.Server) string {
	return "endpoint: " + srv.URL + "\napi_version: 2024-10-21\ncredential_mode: api_key\ndeployments:\n  gpt-4o: gpt-4o\n"
}

func azExecute(inst backendplugin.ConfiguredInstance) error {
	return inst.Execute(newAZExecuteStream("azure-openai/gpt-4o"))
}

func TestCredentialContract_RejectsYAMLKey(t *testing.T) {
	t.Parallel()
	_, err := service.New().Configure(context.Background(), azSecretReq(
		"resource_name: res\napi_version: 2024-10-21\napi_key: hunter2\ndeployments:\n  gpt-4o: gpt-4o\n",
		map[string]string{"api_key": "host-key"},
	))
	if err == nil {
		t.Fatal("expected literal YAML api_key to be rejected")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("rejection leaks secret value: %v", err)
	}
	if !strings.Contains(err.Error(), "api_key") || !strings.Contains(err.Error(), "AZURE_OPENAI_API_KEY") {
		t.Fatalf("rejection must name field path and env root: %v", err)
	}
}

func TestCredentialContract_RequiresHostSecrets(t *testing.T) {
	t.Parallel()
	_, err := service.New().Configure(context.Background(), azSecretReq(
		"resource_name: res\napi_version: 2024-10-21\ndeployments:\n  gpt-4o: gpt-4o\n", nil,
	))
	if err == nil {
		t.Fatal("expected missing Configure secrets to fail")
	}
	if !strings.Contains(err.Error(), "api_key") || !strings.Contains(err.Error(), "AZURE_OPENAI_API_KEY") {
		t.Fatalf("err=%v", err)
	}
}

func TestCredentialContract_FirstKeyPreferredInOrder(t *testing.T) {
	t.Parallel()
	srv, presented := azScriptedKeys(t, map[string]azScriptedResponse{
		"*": {status: 200, body: azChatBody},
	})
	inst, err := service.New().Configure(context.Background(), azSecretReq(
		azKeyBaseYAML(srv),
		map[string]string{"api_key": "first-key", "api_key_3": "third-key"},
	))
	if err != nil {
		t.Fatal(err)
	}
	if err := azExecute(inst); err != nil {
		t.Fatal(err)
	}
	if len(*presented) != 1 || (*presented)[0] != "first-key" {
		t.Fatalf("presented=%q", *presented)
	}
}

func TestCredentialContract_AuthInvalidAdvances(t *testing.T) {
	t.Parallel()
	srv, presented := azScriptedKeys(t, map[string]azScriptedResponse{
		"bad-one": {status: 401, body: `{"error":{"message":"invalid"}}`},
		"*":       {status: 200, body: azChatBody},
	})
	inst, err := service.New().Configure(context.Background(), azSecretReq(
		azKeyBaseYAML(srv),
		map[string]string{"api_key": "bad-one", "api_key_2": "good-two"},
	))
	if err != nil {
		t.Fatal(err)
	}
	if err := azExecute(inst); err != nil {
		t.Fatal(err)
	}
	if len(*presented) != 2 || (*presented)[0] != "bad-one" || (*presented)[1] != "good-two" {
		t.Fatalf("presented=%q", *presented)
	}
}

func TestCredentialContract_LongThrottleAdvances(t *testing.T) {
	t.Parallel()
	srv, presented := azScriptedKeys(t, map[string]azScriptedResponse{
		"cooling-one": {status: 429, header: map[string]string{"Retry-After": "7200"}, body: `{"error":{"message":"quota"}}`},
		"*":           {status: 200, body: azChatBody},
	})
	inst, err := service.New().Configure(context.Background(), azSecretReq(
		azKeyBaseYAML(srv),
		map[string]string{"api_key": "cooling-one", "api_key_2": "next-two"},
	))
	if err != nil {
		t.Fatal(err)
	}
	if err := azExecute(inst); err != nil {
		t.Fatal(err)
	}
	if len(*presented) != 2 {
		t.Fatalf("presented=%q", *presented)
	}
}

func TestCredentialContract_ShortThrottleKeepsPrimary(t *testing.T) {
	t.Parallel()
	srv, presented := azScriptedKeys(t, map[string]azScriptedResponse{
		"*": {status: 429, header: map[string]string{"Retry-After": "30"}, body: `{"error":{"message":"slow"}}`},
	})
	inst, err := service.New().Configure(context.Background(), azSecretReq(
		azKeyBaseYAML(srv),
		map[string]string{"api_key": "primary-key", "api_key_2": "secondary-key"},
	))
	if err != nil {
		t.Fatal(err)
	}
	err = azExecute(inst)
	if err == nil || !errors.Is(err, credpool.ErrTransientThrottle) {
		t.Fatalf("err=%v", err)
	}
	if len(*presented) != 1 || (*presented)[0] != "primary-key" {
		t.Fatalf("short throttle must not consume secondary: presented=%q", *presented)
	}
	if strings.Contains(err.Error(), "primary-key") || strings.Contains(err.Error(), "secondary-key") {
		t.Fatalf("error leaks secret: %v", err)
	}
}

func TestCredentialContract_UnknownThrottleKeepsPrimary(t *testing.T) {
	t.Parallel()
	srv, presented := azScriptedKeys(t, map[string]azScriptedResponse{
		"*": {status: 429, body: `{"error":{"message":"slow"}}`},
	})
	inst, err := service.New().Configure(context.Background(), azSecretReq(
		azKeyBaseYAML(srv),
		map[string]string{"api_key": "primary-key", "api_key_2": "secondary-key"},
	))
	if err != nil {
		t.Fatal(err)
	}
	err = azExecute(inst)
	if err == nil || !errors.Is(err, credpool.ErrTransientThrottle) {
		t.Fatalf("err=%v", err)
	}
	if len(*presented) != 1 || (*presented)[0] != "primary-key" {
		t.Fatalf("unknown throttle must not consume secondary: presented=%q", *presented)
	}
}

func TestCredentialContract_EntraModeIsolatedFromStaticPool(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	requests := 0
	var authHdr, apiKeyHdr string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		authHdr = r.Header.Get("Authorization")
		apiKeyHdr = r.Header.Get("api-key")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":{"message":"quota"}}`))
	}))
	t.Cleanup(srv.Close)

	// Entra mode configures with no static slots at all: the authenticating
	// identity is the injected token provider, never the static pool.
	svc := service.New(service.WithTokenProvider(service.StaticTokenProvider("entra-jwt")))
	inst, err := svc.Configure(context.Background(), azSecretReq(
		"endpoint: "+srv.URL+"\napi_version: 2024-10-21\ncredential_mode: entra\ndeployments:\n  gpt-4o: gpt-4o\n",
		nil,
	))
	if err != nil {
		t.Fatal(err)
	}
	err = azExecute(inst)
	if err == nil {
		t.Fatal("expected the 429 failure to surface")
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 1 {
		t.Fatalf("entra mode must not rotate across static credentials: requests=%d", requests)
	}
	if authHdr != "Bearer entra-jwt" {
		t.Fatalf("authHdr=%q", authHdr)
	}
	if apiKeyHdr != "" {
		t.Fatalf("entra mode must not send the api-key header: %q", apiKeyHdr)
	}
	if strings.Contains(err.Error(), "entra-jwt") {
		t.Fatalf("error leaks token: %v", err)
	}
}

// azFakeExecuteStream is a minimal in-process ExecuteStream: one START
// frame, then EOF; all server frames are collected for terminal inspection.
type azFakeExecuteStream struct {
	ctx     context.Context
	inv     backendplugin.Invocation
	sent    []backendplugin.ServerFrame
	started bool
}

func newAZExecuteStream(model string) *azFakeExecuteStream {
	text := "hi"
	return &azFakeExecuteStream{
		ctx: context.Background(),
		inv: backendplugin.Invocation{
			RequestID: "req-1", AttemptID: "att-1", ALegID: "a-1", BLegID: "b-1",
			CanonicalModelID: model,
			Messages: []backendplugin.Message{{
				Role:  backendplugin.RoleUser,
				Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: &text}},
			}},
			Operation:     string(aipapi.OperationOpenAIChatCompletions),
			DeliveryMode:  string(aipapi.DeliveryModeNonStreaming),
			TransportMode: string(aipapi.TransportModeNonStreaming),
		},
	}
}

func (f *azFakeExecuteStream) Context() context.Context { return f.ctx }

func (f *azFakeExecuteStream) Recv() (backendplugin.ClientFrame, error) {
	if !f.started {
		f.started = true
		return backendplugin.ClientFrame{Kind: backendplugin.ClientFrameStart, InstanceID: "cred-contract", Invocation: &f.inv}, nil
	}
	return backendplugin.ClientFrame{}, io.EOF
}

func (f *azFakeExecuteStream) Send(frame backendplugin.ServerFrame) error {
	f.sent = append(f.sent, frame)
	return nil
}
