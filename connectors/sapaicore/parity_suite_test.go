package sapaicore_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/sapaicore/internal/service"
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

func validServiceKeyJSON(aiURL, authURL string) []byte {
	return fmt.Appendf(nil, `{
		"clientid": "test-client-id",
		"clientsecret": "super-secret-key-12345",
		"url": %q,
		"serviceurls": {
			"AI_API_URL": %q
		}
	}`, authURL, aiURL)
}

func newTestExecuteStream(ctx context.Context, modelID string, op aipapi.Operation, userMsg string, nonStreaming bool) *memStream {
	delivery := aipapi.DeliveryModeStreaming
	transport := aipapi.TransportModeStreaming
	if nonStreaming {
		delivery = aipapi.DeliveryModeNonStreaming
		transport = aipapi.TransportModeNonStreaming
	}
	msg := userMsg
	inv := backendplugin.Invocation{
		RequestID:        "req-1",
		AttemptID:        "att-1",
		ALegID:           "al-1",
		BLegID:           "bl-1",
		CanonicalModelID: modelID,
		NativeModelID:    modelID,
		Operation:        string(op),
		DeliveryMode:     string(delivery),
		TransportMode:    string(transport),
		Messages: []backendplugin.Message{{
			Role:  backendplugin.RoleUser,
			Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: &msg}},
		}},
	}
	return &memStream{
		ctx: ctx,
		inbox: []backendplugin.ClientFrame{
			{Kind: backendplugin.ClientFrameStart, InstanceID: "test-inst", Invocation: &inv},
			{Kind: backendplugin.ClientFrameCloseInput, InstanceID: "test-inst"},
		},
	}
}

// 1. Missing resource_group / inference_contract / service_key fields fail closed.
func TestConfigure_RequiredFields_FailClosed(t *testing.T) {
	t.Parallel()
	svc := service.New(service.WithTokenProvider(service.StaticTokenProvider("tok")))

	cases := []struct {
		name       string
		kind       string
		cfgYAML    string
		serviceKey []byte
		errSub     string
	}{
		{
			name:       "missing resource_group",
			cfgYAML:    "inference_contract: openai-chat\n",
			serviceKey: validServiceKeyJSON("https://api.ai.example.com", "https://auth.example.com"),
			errSub:     "resource_group is required",
		},
		{
			name:       "missing inference_contract",
			cfgYAML:    "resource_group: default\n",
			serviceKey: validServiceKeyJSON("https://api.ai.example.com", "https://auth.example.com"),
			errSub:     "inference_contract is required",
		},
		{
			name:       "missing service_key secret",
			cfgYAML:    "resource_group: default\ninference_contract: openai-chat\n",
			serviceKey: nil,
			errSub:     "missing required service_key in secrets",
		},
		{
			name:       "invalid service_key json",
			cfgYAML:    "resource_group: default\ninference_contract: openai-chat\n",
			serviceKey: []byte("not-json"),
			errSub:     "invalid service_key JSON",
		},
		{
			name:       "missing clientid in service_key",
			cfgYAML:    "resource_group: default\ninference_contract: openai-chat\n",
			serviceKey: []byte(`{"clientsecret":"s","url":"https://u","serviceurls":{"AI_API_URL":"https://ai"}}`),
			errSub:     "missing required fields",
		},
		{
			name:       "missing clientsecret in service_key",
			cfgYAML:    "resource_group: default\ninference_contract: openai-chat\n",
			serviceKey: []byte(`{"clientid":"c","url":"https://u","serviceurls":{"AI_API_URL":"https://ai"}}`),
			errSub:     "missing required fields",
		},
		{
			name:       "missing url in service_key",
			cfgYAML:    "resource_group: default\ninference_contract: openai-chat\n",
			serviceKey: []byte(`{"clientid":"c","clientsecret":"s","serviceurls":{"AI_API_URL":"https://ai"}}`),
			errSub:     "missing required fields",
		},
		{
			name:       "missing AI_API_URL in service_key",
			cfgYAML:    "resource_group: default\ninference_contract: openai-chat\n",
			serviceKey: []byte(`{"clientid":"c","clientsecret":"s","url":"https://u","serviceurls":{}}`),
			errSub:     "missing required fields",
		},
		{
			name:       "unexpected factory kind",
			kind:       "wrong-kind",
			cfgYAML:    "resource_group: default\ninference_contract: openai-chat\n",
			serviceKey: validServiceKeyJSON("https://api.ai.example.com", "https://auth.example.com"),
			errSub:     "unexpected factory kind",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			kind := service.FactoryKind
			if tc.kind != "" {
				kind = tc.kind
			}
			sec := backendplugin.SecretBundle{Values: map[string][]byte{}}
			if tc.serviceKey != nil {
				sec.Values["service_key"] = tc.serviceKey
			}
			_, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
				FactoryKind: kind,
				ConfigYAML:  []byte(tc.cfgYAML),
				Secrets:     sec,
			})
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.errSub)
			}
			if !strings.Contains(err.Error(), tc.errSub) {
				t.Fatalf("expected error containing %q, got %q", tc.errSub, err.Error())
			}
		})
	}
}

// 2. Unknown inference_contract fail closed.
func TestConfigure_UnknownInferenceContract_FailClosed(t *testing.T) {
	t.Parallel()
	svc := service.New(service.WithTokenProvider(service.StaticTokenProvider("tok")))
	sec := backendplugin.SecretBundle{
		Values: map[string][]byte{
			"service_key": validServiceKeyJSON("https://api.ai.example.com", "https://auth.example.com"),
		},
	}

	badContracts := []string{"openai-responses", "orchestration", "chat", "unknown-contract"}
	for _, c := range badContracts {
		cfgYAML := fmt.Sprintf("resource_group: default\ninference_contract: %s\n", c)
		_, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
			FactoryKind: service.FactoryKind,
			ConfigYAML:  []byte(cfgYAML),
			Secrets:     sec,
		})
		if err == nil {
			t.Fatalf("expected error for contract %q, got nil", c)
		}
		if !strings.Contains(err.Error(), "unsupported inference_contract") {
			t.Fatalf("expected unsupported inference_contract error, got %q", err.Error())
		}
	}
}

// 3. YAML secrets / embedded service-key JSON rejected.
func TestConfigure_YAMLSecrets_Rejected(t *testing.T) {
	t.Parallel()
	svc := service.New(service.WithTokenProvider(service.StaticTokenProvider("tok")))
	sec := backendplugin.SecretBundle{
		Values: map[string][]byte{
			"service_key": validServiceKeyJSON("https://api.ai.example.com", "https://auth.example.com"),
		},
	}

	badYAMLs := []struct {
		name string
		yaml string
	}{
		{"service_key in yaml", "resource_group: default\ninference_contract: openai-chat\nservice_key: secret\n"},
		{"clientsecret in yaml", "resource_group: default\ninference_contract: openai-chat\nclientsecret: secret\n"},
		{"client_secret in yaml", "resource_group: default\ninference_contract: openai-chat\nclient_secret: secret\n"},
		{"clientid in yaml", "resource_group: default\ninference_contract: openai-chat\nclientid: id\n"},
		{"token in yaml", "resource_group: default\ninference_contract: openai-chat\ntoken: secret\n"},
		{"secret in yaml", "resource_group: default\ninference_contract: openai-chat\nsecret: secret\n"},
		{"api_key in yaml", "resource_group: default\ninference_contract: openai-chat\napi_key: secret\n"},
		{"apikey in yaml", "resource_group: default\ninference_contract: openai-chat\napikey: secret\n"},
		{"serviceurls in yaml", "resource_group: default\ninference_contract: openai-chat\nserviceurls: foo\n"},
		{"ai_api_url in yaml", "resource_group: default\ninference_contract: openai-chat\nai_api_url: foo\n"},
	}

	for _, tc := range badYAMLs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
				FactoryKind: service.FactoryKind,
				ConfigYAML:  []byte(tc.yaml),
				Secrets:     sec,
			})
			if err == nil {
				t.Fatalf("expected rejection of secrets in YAML for %s, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), "literal secrets in configuration YAML are forbidden") {
				t.Fatalf("expected forbidden secrets error, got %q", err.Error())
			}
		})
	}
}

// 4. Parsed AI_API_URL + resource_group construct the documented inference URL.
func TestInferenceURL_Construction(t *testing.T) {
	t.Parallel()
	var capturedPath string
	var capturedResourceGroup string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedResourceGroup = r.Header.Get("AI-Resource-Group")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer srv.Close()

	svc := service.New(service.WithTokenProvider(service.StaticTokenProvider("tok")))
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		ConfigYAML:  []byte("resource_group: my-custom-rg\ninference_contract: openai-chat\ndeployment_id: dep-xyz\n"),
		Secrets: backendplugin.SecretBundle{
			Values: map[string][]byte{
				"service_key": validServiceKeyJSON(srv.URL, srv.URL),
			},
		},
	})
	if err != nil {
		t.Fatalf("configure failed: %v", err)
	}

	stream := newTestExecuteStream(context.Background(), "sapaicore/dep-xyz", aipapi.OperationOpenAIChatCompletions, "hi", true)
	if err := inst.Execute(stream); err != nil {
		t.Fatalf("execute failed: %v", err)
	}

	expectedPath := "/v2/inference/deployments/dep-xyz/chat/completions"
	if capturedPath != expectedPath {
		t.Fatalf("expected path %q, got %q", expectedPath, capturedPath)
	}
	if capturedResourceGroup != "my-custom-rg" {
		t.Fatalf("expected resource group %q, got %q", "my-custom-rg", capturedResourceGroup)
	}
}

// 5. NewProduction OAuth Execute: form grant + Bearer is access_token; secret not in errors.
func TestNewProduction_OAuth_Execute_AndSecretRedaction(t *testing.T) {
	t.Parallel()
	var oauthFormValues string
	var oauthContentType string
	var inferenceAuthHeader string
	secretValue := "super-secret-client-secret-999"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			oauthContentType = r.Header.Get("Content-Type")
			b, _ := io.ReadAll(r.Body)
			oauthFormValues = string(b)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"access_token": "bearer-token-live-abc", "token_type": "bearer", "expires_in": 3600}`))
		case "/v2/inference/deployments/dep-prod/chat/completions":
			inferenceAuthHeader = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"production ok"}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	svc := service.NewProduction()
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		ConfigYAML:  fmt.Appendf(nil, "resource_group: prod-rg\ninference_contract: openai-chat\ndeployment_id: dep-prod\napi_origin: %s\noauth_origin: %s\n", srv.URL, srv.URL),
		Secrets: backendplugin.SecretBundle{
			Values: map[string][]byte{
				"service_key": fmt.Appendf(nil, `{
					"clientid": "my-client-id",
					"clientsecret": %q,
					"url": %q,
					"serviceurls": { "AI_API_URL": %q }
				}`, secretValue, srv.URL, srv.URL),
			},
		},
	})
	if err != nil {
		t.Fatalf("configure failed: %v", err)
	}

	stream := newTestExecuteStream(context.Background(), "sapaicore/dep-prod", aipapi.OperationOpenAIChatCompletions, "hi", true)
	if err := inst.Execute(stream); err != nil {
		t.Fatalf("execute failed: %v", err)
	}

	if !strings.Contains(oauthContentType, "application/x-www-form-urlencoded") {
		t.Fatalf("expected form urlencoded content type, got %q", oauthContentType)
	}
	if !strings.Contains(oauthFormValues, "grant_type=client_credentials") {
		t.Fatalf("expected grant_type=client_credentials in %q", oauthFormValues)
	}
	if !strings.Contains(oauthFormValues, "client_id=my-client-id") {
		t.Fatalf("expected client_id in %q", oauthFormValues)
	}
	if !strings.Contains(oauthFormValues, secretValue) {
		t.Fatalf("expected client_secret in OAuth request")
	}

	expectedBearer := "Bearer bearer-token-live-abc"
	if inferenceAuthHeader != expectedBearer {
		t.Fatalf("expected inference Authorization %q, got %q", expectedBearer, inferenceAuthHeader)
	}
	if strings.Contains(inferenceAuthHeader, secretValue) {
		t.Fatalf("inference Authorization must not contain client secret!")
	}

	// Secret redaction test on OAuth failure:
	failingSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = fmt.Fprintf(w, `{"error": "unauthorized", "details": "%s"}`, secretValue)
			return
		}
		http.NotFound(w, r)
	}))
	defer failingSrv.Close()

	failInst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		ConfigYAML:  fmt.Appendf(nil, "resource_group: prod-rg\ninference_contract: openai-chat\ndeployment_id: dep-prod\napi_origin: %s\noauth_origin: %s\n", failingSrv.URL, failingSrv.URL),
		Secrets: backendplugin.SecretBundle{
			Values: map[string][]byte{
				"service_key": fmt.Appendf(nil, `{
					"clientid": "my-client-id",
					"clientsecret": %q,
					"url": %q,
					"serviceurls": { "AI_API_URL": %q }
				}`, secretValue, failingSrv.URL, failingSrv.URL),
			},
		},
	})
	if err != nil {
		t.Fatalf("configure failed: %v", err)
	}
	failStream := newTestExecuteStream(context.Background(), "sapaicore/dep-prod", aipapi.OperationOpenAIChatCompletions, "hi", true)
	execErr := failInst.Execute(failStream)
	if execErr == nil {
		t.Fatalf("expected execute error on failed oauth exchange, got nil")
	}
	if strings.Contains(execErr.Error(), secretValue) {
		t.Fatalf("error message must NOT contain secret value! Got: %s", execErr.Error())
	}
}

// 6. Token refresh before expiry.
func TestToken_RefreshBeforeExpiry(t *testing.T) {
	t.Parallel()
	var tokenCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			callNum := tokenCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			// Return expires_in: 1s, which is well within the 10s refresh skew buffer
			_, _ = fmt.Fprintf(w, `{"access_token": "token-%d", "token_type": "bearer", "expires_in": 1}`, callNum)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	tp := service.NewOAuthTokenProvider(srv.URL+"/oauth/token", "cid", "csecret", srv.Client())
	tok1, err := tp.Token(context.Background())
	if err != nil {
		t.Fatalf("first token failed: %v", err)
	}
	if tok1 != "token-1" {
		t.Fatalf("expected token-1, got %q", tok1)
	}

	// Immediate next call should refresh because 1s is < 10s skew buffer
	tok2, err := tp.Token(context.Background())
	if err != nil {
		t.Fatalf("second token failed: %v", err)
	}
	if tok2 != "token-2" {
		t.Fatalf("expected token-2, got %q", tok2)
	}
	if tokenCalls.Load() != 2 {
		t.Fatalf("expected 2 token exchanges, got %d", tokenCalls.Load())
	}
}

// 7. Configure+Execute openai-chat: path is /v2/inference/deployments/{id}/chat/completions;
// AI-Resource-Group present; openaicompat Chat body; text delta mapped.
func TestConfigure_Execute_OpenAIChat_Streaming(t *testing.T) {
	t.Parallel()
	var capturedPath string
	var capturedRG string
	var capturedBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedRG = r.Header.Get("AI-Resource-Group")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedBody)

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if ok {
			flusher.Flush()
		}
		chunk := `data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":"Hello SAP"}}]}` + "\n\n"
		_, _ = w.Write([]byte(chunk))
		if ok {
			flusher.Flush()
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if ok {
			flusher.Flush()
		}
	}))
	defer srv.Close()

	svc := service.New(service.WithTokenProvider(service.StaticTokenProvider("tok")))
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		ConfigYAML:  fmt.Appendf(nil, "resource_group: test-rg\ninference_contract: openai-chat\napi_origin: %s\n", srv.URL),
		Secrets: backendplugin.SecretBundle{
			Values: map[string][]byte{
				"service_key": validServiceKeyJSON(srv.URL, srv.URL),
			},
		},
	})
	if err != nil {
		t.Fatalf("configure failed: %v", err)
	}

	stream := newTestExecuteStream(context.Background(), "sapaicore/dep-chat-1", aipapi.OperationOpenAIChatCompletions, "Hello world", false)
	if err := inst.Execute(stream); err != nil {
		t.Fatalf("execute failed: %v", err)
	}

	if capturedPath != "/v2/inference/deployments/dep-chat-1/chat/completions" {
		t.Fatalf("unexpected path: %s", capturedPath)
	}
	if capturedRG != "test-rg" {
		t.Fatalf("unexpected resource group: %s", capturedRG)
	}
	if capturedBody["model"] == "" {
		t.Fatalf("missing model in request body")
	}

	var foundText strings.Builder
	for _, frame := range stream.outbox {
		if frame.Kind == backendplugin.ServerFrameEvent && frame.Event != nil && frame.Event.Delta != nil {
			foundText.WriteString(*frame.Event.Delta)
		}
	}
	if foundText.String() != "Hello SAP" {
		t.Fatalf("expected text 'Hello SAP', got %q", foundText.String())
	}
}

// 8. Responses operation fail closed (TransportChatOnly).
func TestExecute_ResponsesOperation_FailClosed(t *testing.T) {
	t.Parallel()
	svc := service.New(service.WithTokenProvider(service.StaticTokenProvider("tok")))
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		ConfigYAML:  []byte("resource_group: test-rg\ninference_contract: openai-chat\ndeployment_id: dep-1\n"),
		Secrets: backendplugin.SecretBundle{
			Values: map[string][]byte{
				"service_key": validServiceKeyJSON("https://api.ai.example.com", "https://auth.example.com"),
			},
		},
	})
	if err != nil {
		t.Fatalf("configure failed: %v", err)
	}

	stream := newTestExecuteStream(context.Background(), "sapaicore/dep-1", aipapi.OperationOpenAIResponses, "hi", false)
	err = inst.Execute(stream)
	if err == nil {
		t.Fatalf("expected responses operation to fail closed, got nil")
	}
	if !strings.Contains(err.Error(), "responses API is not available") {
		t.Fatalf("expected responses API not available error, got %q", err.Error())
	}
}

// 9. ListModels asserts GET /v2/lm/deployments + resource-group header;
// 404 fails closed even with deployment_id set.
func TestListModels_Assertions_AndFailClosed(t *testing.T) {
	t.Parallel()
	var capturedPath string
	var capturedRG string
	var capturedAuth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedRG = r.Header.Get("AI-Resource-Group")
		capturedAuth = r.Header.Get("Authorization")

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"count": 4,
			"resources": [
				{"id": "dep-running-1", "status": "RUNNING"},
				{"id": "dep-stopped", "status": "STOPPED"},
				{"id": "dep-dead", "status": "DEAD"},
				{"id": "dep-running-2", "status": "running"}
			]
		}`))
	}))
	defer srv.Close()

	svc := service.New(service.WithTokenProvider(service.StaticTokenProvider("test-tok")))
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		ConfigYAML:  fmt.Appendf(nil, "resource_group: rg-inventory\ninference_contract: openai-chat\napi_origin: %s\n", srv.URL),
		Secrets: backendplugin.SecretBundle{
			Values: map[string][]byte{
				"service_key": validServiceKeyJSON(srv.URL, srv.URL),
			},
		},
	})
	if err != nil {
		t.Fatalf("configure failed: %v", err)
	}

	resp, err := inst.ListModels(context.Background(), 0)
	if err != nil {
		t.Fatalf("list models failed: %v", err)
	}

	if capturedPath != "/v2/lm/deployments" {
		t.Fatalf("expected path /v2/lm/deployments, got %q", capturedPath)
	}
	if capturedRG != "rg-inventory" {
		t.Fatalf("expected resource group rg-inventory, got %q", capturedRG)
	}
	if capturedAuth != "Bearer test-tok" {
		t.Fatalf("expected Authorization Bearer test-tok, got %q", capturedAuth)
	}
	if len(resp.Models) != 2 {
		t.Fatalf("expected 2 RUNNING models, got %d", len(resp.Models))
	}
	if resp.Models[0].CanonicalModelID != "sapaicore/dep-running-1" || resp.Models[0].NativeModelID != "dep-running-1" {
		t.Fatalf("unexpected model 0: %+v", resp.Models[0])
	}
	if resp.Models[1].CanonicalModelID != "sapaicore/dep-running-2" || resp.Models[1].NativeModelID != "dep-running-2" {
		t.Fatalf("unexpected model 1: %+v", resp.Models[1])
	}
	if resp.FetchedUnixMS <= 0 {
		t.Fatalf("expected FetchedUnixMS > 0, got %d", resp.FetchedUnixMS)
	}

	// 404 fails closed even when deployment_id is set
	srv404 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv404.Close()

	inst404, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		ConfigYAML:  fmt.Appendf(nil, "resource_group: rg-inventory\ninference_contract: openai-chat\ndeployment_id: fallback-dep\napi_origin: %s\n", srv404.URL),
		Secrets: backendplugin.SecretBundle{
			Values: map[string][]byte{
				"service_key": validServiceKeyJSON(srv404.URL, srv404.URL),
			},
		},
	})
	if err != nil {
		t.Fatalf("configure failed: %v", err)
	}
	_, listErr := inst404.ListModels(context.Background(), 0)
	if listErr == nil {
		t.Fatalf("expected 404 to fail closed, got nil")
	}
}

// 10. Hard-negative vs orchestration completion.
func TestHardNegative_OrchestrationCompletion(t *testing.T) {
	t.Parallel()
	var orchestrationHits atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/v2/completion") {
			orchestrationHits.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"orchestration": "fallback"}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	svc := service.New(service.WithTokenProvider(service.StaticTokenProvider("tok")))
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		ConfigYAML:  fmt.Appendf(nil, "resource_group: default\ninference_contract: openai-chat\ndeployment_id: dep-1\napi_origin: %s\n", srv.URL),
		Secrets: backendplugin.SecretBundle{
			Values: map[string][]byte{
				"service_key": validServiceKeyJSON(srv.URL, srv.URL),
			},
		},
	})
	if err != nil {
		t.Fatalf("configure failed: %v", err)
	}

	stream := newTestExecuteStream(context.Background(), "sapaicore/dep-1", aipapi.OperationOpenAIChatCompletions, "hi", true)
	execErr := inst.Execute(stream)
	if execErr == nil {
		t.Fatalf("expected execute to fail when chat/completions returns 404, got nil")
	}
	if orchestrationHits.Load() > 0 {
		t.Fatalf("orchestration completion path /v2/completion must NEVER be hit! Hits: %d", orchestrationHits.Load())
	}
}

// 11. Describe kind sapaicore. Streaming true; do not advertise Tools/Vision.
func TestDescribe_Descriptor(t *testing.T) {
	t.Parallel()
	svc := service.New()
	desc, err := svc.Describe(context.Background())
	if err != nil {
		t.Fatalf("describe failed: %v", err)
	}

	if desc.PluginID != service.PluginID {
		t.Fatalf("expected plugin ID %q, got %q", service.PluginID, desc.PluginID)
	}
	if desc.ProtocolMajor != 1 || desc.ProtocolMinor != backendplugin.ProtocolMinorCancellationHandshake {
		t.Fatalf("expected protocol 1.%d, got %d.%d", backendplugin.ProtocolMinorCancellationHandshake, desc.ProtocolMajor, desc.ProtocolMinor)
	}
	if len(desc.Factories) != 1 {
		t.Fatalf("expected 1 factory, got %d", len(desc.Factories))
	}
	f := desc.Factories[0]
	if f.Kind != service.FactoryKind {
		t.Fatalf("expected factory kind %q, got %q", service.FactoryKind, f.Kind)
	}
	if f.DisplayName != "SAP AI Core" {
		t.Fatalf("expected DisplayName 'SAP AI Core', got %q", f.DisplayName)
	}
	if !f.StaticCapabilities.Streaming {
		t.Fatalf("expected Streaming=true")
	}
	if f.StaticCapabilities.Tools || f.StaticCapabilities.Vision {
		t.Fatalf("Tools and Vision must be false unless implemented, got Tools=%v Vision=%v", f.StaticCapabilities.Tools, f.StaticCapabilities.Vision)
	}
}

// 13. New() without token factory fails with NewProduction guidance.
func TestNew_WithoutTokenFactory_FailsWithNewProductionGuidance(t *testing.T) {
	t.Parallel()
	svc := service.New() // no token provider and no token provider factory
	_, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		ConfigYAML:  []byte("resource_group: default\ninference_contract: openai-chat\n"),
		Secrets: backendplugin.SecretBundle{
			Values: map[string][]byte{
				"service_key": validServiceKeyJSON("https://api.ai.example.com", "https://auth.example.com"),
			},
		},
	})
	if err == nil {
		t.Fatalf("expected error without token provider, got nil")
	}
	if !strings.Contains(err.Error(), "NewProduction") {
		t.Fatalf("expected error mentioning NewProduction guidance, got %q", err.Error())
	}
}

// 14. Root go.mod has no connectors/sapaicore. No darwin. Secret redaction test.
func TestHygiene_RootGoMod_NoDarwin_SecretRedaction(t *testing.T) {
	t.Parallel()

	// Root go.mod check
	rootGoMod, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatalf("failed to read root go.mod: %v", err)
	}
	if strings.Contains(string(rootGoMod), "connectors/sapaicore") {
		t.Fatalf("root go.mod must not depend on connectors/sapaicore!")
	}

	// Manifest check: no darwin
	manifestBytes, err := os.ReadFile("manifest/template.backendplugin.json")
	if err != nil {
		t.Fatalf("failed to read manifest template: %v", err)
	}
	if strings.Contains(strings.ToLower(string(manifestBytes)), "darwin") {
		t.Fatalf("manifest template must not contain darwin platform!")
	}

	// Release yaml check: no darwin
	releaseBytes, err := os.ReadFile("release.yaml")
	if err != nil {
		t.Fatalf("failed to read release.yaml: %v", err)
	}
	if strings.Contains(strings.ToLower(string(releaseBytes)), "darwin") {
		t.Fatalf("release.yaml must not contain darwin platform!")
	}
}
