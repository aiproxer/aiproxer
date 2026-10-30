package cohere_test

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

	"github.com/aiproxer/aiproxer/connectors/cohere/internal/service"
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

// 1. YAML api_key rejected.
func TestConfigure_YAMLSecrets_Rejected(t *testing.T) {
	t.Parallel()
	svc := service.New()
	sec := backendplugin.SecretBundle{
		Values: map[string][]byte{"api_key": []byte("valid-key")},
	}

	badYAMLs := []struct {
		name string
		yaml string
	}{
		{"api_key in yaml", "api_key: secret-key\n"},
		{"apikey in yaml", "apikey: secret-key\n"},
		{"token in yaml", "token: secret-token\n"},
		{"secret in yaml", "secret: secret-value\n"},
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
			if !strings.Contains(err.Error(), "is forbidden") || !strings.Contains(err.Error(), "COHERE_API_KEY") {
				t.Fatalf("expected forbidden-field error naming env root, got %q", err.Error())
			}
		})
	}
}

// 2. Constructed POST {origin}/v2/chat (not /v1/chat/completions).
// 3. Bearer from injected token / NewProduction api_key.
func TestConstructedPOST_AndBearer(t *testing.T) {
	t.Parallel()
	var capturedPath string
	var capturedAuth string
	var capturedContentType string
	var capturedAccept string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedAuth = r.Header.Get("Authorization")
		capturedContentType = r.Header.Get("Content-Type")
		capturedAccept = r.Header.Get("Accept")

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"c-1","finish_reason":"COMPLETE","message":{"role":"assistant","content":"ok"}}`))
	}))
	defer srv.Close()

	svc := service.New()
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		ConfigYAML:  fmt.Appendf(nil, "api_origin: %s\nmodel: command-r-plus\n", srv.URL),
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("test-token-123")}},
	})
	if err != nil {
		t.Fatalf("configure failed: %v", err)
	}

	stream := newTestExecuteStream(context.Background(), "cohere/command-r-plus", aipapi.OperationOpenAIChatCompletions, "hi", true)
	if err := inst.Execute(stream); err != nil {
		t.Fatalf("execute failed: %v", err)
	}

	if capturedPath != "/v2/chat" {
		t.Fatalf("expected path /v2/chat, got %q", capturedPath)
	}
	if capturedAuth != "Bearer test-token-123" {
		t.Fatalf("expected Authorization Bearer test-token-123, got %q", capturedAuth)
	}
	if !strings.Contains(capturedContentType, "application/json") {
		t.Fatalf("expected application/json Content-Type, got %q", capturedContentType)
	}
	if !strings.Contains(capturedAccept, "application/json") {
		t.Fatalf("expected application/json Accept header for unary, got %q", capturedAccept)
	}
}

// 4. Configure→Execute maps native v2 body (model, messages) and response text.
func TestConfigure_Execute_NativeV2Body_Unary(t *testing.T) {
	t.Parallel()
	var capturedBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedBody)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Return array content-block format
		_, _ = w.Write([]byte(`{
			"id": "c-resp-1",
			"finish_reason": "COMPLETE",
			"message": {
				"role": "assistant",
				"content": [
					{"type": "text", "text": "Hello from Cohere"}
				]
			}
		}`))
	}))
	defer srv.Close()

	svc := service.New()
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		ConfigYAML:  fmt.Appendf(nil, "api_origin: %s\n", srv.URL),
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("tok")}},
	})
	if err != nil {
		t.Fatalf("configure failed: %v", err)
	}

	stream := newTestExecuteStream(context.Background(), "cohere/command-r", aipapi.OperationOpenAIChatCompletions, "Hello world", true)
	if err := inst.Execute(stream); err != nil {
		t.Fatalf("execute failed: %v", err)
	}

	if capturedBody["model"] != "command-r" {
		t.Fatalf("expected model 'command-r', got %v", capturedBody["model"])
	}
	messages, ok := capturedBody["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("expected 1 message in body, got %+v", capturedBody["messages"])
	}
	msg0, ok := messages[0].(map[string]any)
	if !ok {
		t.Fatalf("expected message 0 to be map[string]any, got %T", messages[0])
	}
	if msg0["role"] != "user" || msg0["content"] != "Hello world" {
		t.Fatalf("unexpected message payload: %+v", msg0)
	}

	var foundText strings.Builder
	for _, frame := range stream.outbox {
		if frame.Kind == backendplugin.ServerFrameEvent && frame.Event != nil && frame.Event.Delta != nil {
			foundText.WriteString(*frame.Event.Delta)
		}
	}
	if foundText.String() != "Hello from Cohere" {
		t.Fatalf("expected 'Hello from Cohere', got %q", foundText.String())
	}
}

// 5. Streaming uses /v2/chat with stream: true and SSE deltas.
func TestConfigure_Execute_NativeV2Body_Streaming(t *testing.T) {
	t.Parallel()
	var capturedPath string
	var capturedAccept string
	var capturedBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedAccept = r.Header.Get("Accept")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedBody)

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if ok {
			flusher.Flush()
		}

		chunks := []string{
			`data: {"type":"message-start","id":"msg-1","delta":{"message":{"role":"assistant"}}}` + "\n\n",
			`data: {"type":"content-start","index":0,"delta":{"message":{"content":{"type":"text","text":""}}}}` + "\n\n",
			`data: {"type":"content-delta","index":0,"delta":{"message":{"content":{"text":"Streaming "}}}}` + "\n\n",
			`data: {"type":"content-delta","index":0,"delta":{"message":{"content":{"text":"from Cohere"}}}}` + "\n\n",
			`data: {"type":"content-end","index":0}` + "\n\n",
			`data: {"type":"message-end","id":"msg-1","delta":{"finish_reason":"COMPLETE"}}` + "\n\n",
		}
		for _, c := range chunks {
			_, _ = w.Write([]byte(c))
			if ok {
				flusher.Flush()
			}
		}
	}))
	defer srv.Close()

	svc := service.New()
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		ConfigYAML:  fmt.Appendf(nil, "api_origin: %s\n", srv.URL),
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("tok")}},
	})
	if err != nil {
		t.Fatalf("configure failed: %v", err)
	}

	stream := newTestExecuteStream(context.Background(), "cohere/command-r", aipapi.OperationOpenAIChatCompletions, "Hello stream", false)
	if err := inst.Execute(stream); err != nil {
		t.Fatalf("execute failed: %v", err)
	}

	if capturedPath != "/v2/chat" {
		t.Fatalf("expected /v2/chat, got %q", capturedPath)
	}
	if !strings.Contains(capturedAccept, "text/event-stream") {
		t.Fatalf("expected text/event-stream Accept header, got %q", capturedAccept)
	}
	if capturedBody["stream"] != true {
		t.Fatalf("expected stream=true in body, got %v", capturedBody["stream"])
	}

	var foundText strings.Builder
	for _, frame := range stream.outbox {
		if frame.Kind == backendplugin.ServerFrameEvent && frame.Event != nil && frame.Event.Delta != nil {
			foundText.WriteString(*frame.Event.Delta)
		}
	}
	if foundText.String() != "Streaming from Cohere" {
		t.Fatalf("expected 'Streaming from Cohere', got %q", foundText.String())
	}
}

// 6. Vision/non-text parts fail closed. Responses operation fail closed.
// Tools are natively supported (see tools_test.go) and must NOT fail closed.
func TestExecute_VisionAndResponses_FailClosed(t *testing.T) {
	t.Parallel()
	svc := service.New()
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		ConfigYAML:  []byte("model: command-r\n"),
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("tok")}},
	})
	if err != nil {
		t.Fatalf("configure failed: %v", err)
	}

	// Tools are natively supported and must be accepted at admission; the
	// native mapping is covered in tools_test.go.
	msg := "hello"
	// Vision/image fail closed
	imgRef := "http://image.png"
	invVision := backendplugin.Invocation{
		RequestID:        "req-vision",
		AttemptID:        "att-vision",
		CanonicalModelID: "cohere/command-r",
		Operation:        string(aipapi.OperationOpenAIChatCompletions),
		Messages: []backendplugin.Message{{
			Role: backendplugin.RoleUser,
			Parts: []backendplugin.Part{
				{Kind: backendplugin.PartKindText, Text: &msg},
				{Kind: backendplugin.PartKindImageRef, ImageRef: &imgRef},
			},
		}},
	}
	streamVision := &memStream{
		ctx: context.Background(),
		inbox: []backendplugin.ClientFrame{
			{Kind: backendplugin.ClientFrameStart, InstanceID: "inst-2", Invocation: &invVision},
			{Kind: backendplugin.ClientFrameCloseInput, InstanceID: "inst-2"},
		},
	}
	if err := inst.Execute(streamVision); err == nil {
		t.Fatalf("expected vision to fail closed, got nil")
	}

	// Responses operation fail closed
	streamResponses := newTestExecuteStream(context.Background(), "cohere/command-r", aipapi.OperationOpenAIResponses, "hi", false)
	if err := inst.Execute(streamResponses); err == nil {
		t.Fatalf("expected responses operation to fail closed, got nil")
	}
}

// 7. ListModels GET /v1/models with endpoint=chat; 404 fails closed.
func TestListModels_EndpointChat_AndFailClosed(t *testing.T) {
	t.Parallel()
	var capturedPath string
	var capturedQuery string
	var capturedAuth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedQuery = r.URL.RawQuery
		capturedAuth = r.Header.Get("Authorization")

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"models": [
				{
					"name": "command-r-plus-08-2024",
					"endpoints": ["chat"],
					"context_length": 128000
				},
				{
					"name": "command-light",
					"endpoints": ["chat", "summarize"]
				},
				{
					"name": "embed-english-v3.0",
					"endpoints": ["embed"]
				},
				{
					"name": "rerank-v3.5",
					"endpoints": ["rerank"]
				}
			]
		}`))
	}))
	defer srv.Close()

	svc := service.New()
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		ConfigYAML:  fmt.Appendf(nil, "api_origin: %s\n", srv.URL),
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("my-bearer-tok")}},
	})
	if err != nil {
		t.Fatalf("configure failed: %v", err)
	}

	resp, err := inst.ListModels(context.Background(), 0)
	if err != nil {
		t.Fatalf("list models failed: %v", err)
	}

	if capturedPath != "/v1/models" {
		t.Fatalf("expected /v1/models, got %q", capturedPath)
	}
	if !strings.Contains(capturedQuery, "endpoint=chat") {
		t.Fatalf("expected query endpoint=chat, got %q", capturedQuery)
	}
	if capturedAuth != "Bearer my-bearer-tok" {
		t.Fatalf("expected Authorization Bearer my-bearer-tok, got %q", capturedAuth)
	}
	if len(resp.Models) != 2 {
		t.Fatalf("expected 2 chat models (filtered out embed and rerank), got %d: %+v", len(resp.Models), resp.Models)
	}
	if resp.Models[0].CanonicalModelID != "cohere/command-r-plus-08-2024" || resp.Models[0].NativeModelID != "command-r-plus-08-2024" {
		t.Fatalf("unexpected model 0: %+v", resp.Models[0])
	}
	if resp.Models[1].CanonicalModelID != "cohere/command-light" || resp.Models[1].NativeModelID != "command-light" {
		t.Fatalf("unexpected model 1: %+v", resp.Models[1])
	}
	if resp.FetchedUnixMS <= 0 {
		t.Fatalf("expected FetchedUnixMS > 0, got %d", resp.FetchedUnixMS)
	}

	// 404 fails closed
	srv404 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv404.Close()

	inst404, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		ConfigYAML:  fmt.Appendf(nil, "api_origin: %s\nmodel: fallback-model\n", srv404.URL),
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("my-bearer-tok")}},
	})
	if err != nil {
		t.Fatalf("configure failed: %v", err)
	}
	_, listErr := inst404.ListModels(context.Background(), 0)
	if listErr == nil {
		t.Fatalf("expected 404 to fail closed, got nil")
	}
}

// 8. Hard-negative vs OpenAI chat/completions (and vs /v1/chat if you also stub it).
func TestHardNegative_VersusOpenAI_AndLegacyV1(t *testing.T) {
	t.Parallel()
	var openAIHits atomic.Int32
	var legacyV1Hits atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			openAIHits.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"openai": "should not be called"}`))
			return
		}
		if r.URL.Path == "/v1/chat" {
			legacyV1Hits.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"legacy_v1": "should not be called"}`))
			return
		}
		if r.URL.Path == "/v2/chat" {
			http.NotFound(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	svc := service.New()
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		ConfigYAML:  fmt.Appendf(nil, "api_origin: %s\nmodel: command-r\n", srv.URL),
		Secrets:     backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("tok")}},
	})
	if err != nil {
		t.Fatalf("configure failed: %v", err)
	}

	stream := newTestExecuteStream(context.Background(), "cohere/command-r", aipapi.OperationOpenAIChatCompletions, "hi", true)
	execErr := inst.Execute(stream)
	if execErr == nil {
		t.Fatalf("expected execute to fail when /v2/chat is 404, got nil")
	}
	if openAIHits.Load() > 0 {
		t.Fatalf("OpenAI path was hit %d times! Must never fall back to OpenAI", openAIHits.Load())
	}
	if legacyV1Hits.Load() > 0 {
		t.Fatalf("Legacy /v1/chat path was hit %d times! Must never fall back to legacy v1", legacyV1Hits.Load())
	}
}

// 9. Describe kind cohere. Streaming and Tools true; Vision false.
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
	if f.DisplayName != "Cohere" {
		t.Fatalf("expected DisplayName 'Cohere', got %q", f.DisplayName)
	}
	if !f.StaticCapabilities.Streaming {
		t.Fatalf("expected Streaming=true")
	}
	if !f.StaticCapabilities.Tools {
		t.Fatalf("expected Tools=true (native Cohere v2 tools)")
	}
	if f.StaticCapabilities.Vision {
		t.Fatalf("Vision must be false, got Vision=%v", f.StaticCapabilities.Vision)
	}
}

// 11. New() without host-issued slots fails closed naming the slot and env
// root; main uses NewProduction with host-delivered Configure secrets.
func TestNew_WithoutHostSecrets_FailsClosed(t *testing.T) {
	t.Parallel()
	svc := service.New()
	_, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		ConfigYAML:  []byte("model: command-r\n"),
		Secrets:     backendplugin.SecretBundle{},
	})
	if err == nil {
		t.Fatalf("expected error without host Configure secrets, got nil")
	}
	if !strings.Contains(err.Error(), "api_key") || !strings.Contains(err.Error(), "COHERE_API_KEY") {
		t.Fatalf("expected error naming slot and env root, got %q", err.Error())
	}
}

// Secret redaction test for NewProduction with API key from secrets.
func TestNewProduction_Bearer_AndSecretRedaction(t *testing.T) {
	t.Parallel()
	secretKey := "super-secret-cohere-api-key-999"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprintf(w, `{"message": "unauthorized", "key": "%s"}`, secretKey)
	}))
	defer srv.Close()

	svc := service.NewProduction()
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		ConfigYAML:  fmt.Appendf(nil, "api_origin: %s\nmodel: command-r\n", srv.URL),
		Secrets: backendplugin.SecretBundle{
			Values: map[string][]byte{
				"api_key": []byte(secretKey),
			},
		},
	})
	if err != nil {
		t.Fatalf("configure failed: %v", err)
	}

	stream := newTestExecuteStream(context.Background(), "cohere/command-r", aipapi.OperationOpenAIChatCompletions, "hi", true)
	execErr := inst.Execute(stream)
	if execErr == nil {
		t.Fatalf("expected execute error on 401, got nil")
	}
	if strings.Contains(execErr.Error(), secretKey) {
		t.Fatalf("secret key must NOT appear in error message! Got: %s", execErr.Error())
	}
}

// 12. Root go.mod has no connectors/cohere. No darwin.
func TestHygiene_RootGoMod_NoDarwin(t *testing.T) {
	t.Parallel()

	// Root go.mod check
	rootGoMod, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatalf("failed to read root go.mod: %v", err)
	}
	if strings.Contains(string(rootGoMod), "connectors/cohere") {
		t.Fatalf("root go.mod must not depend on connectors/cohere!")
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
