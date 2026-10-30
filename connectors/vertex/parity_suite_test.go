package vertex_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/vertex/internal/service"
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
		Operation:        string(op),
		DeliveryMode:     string(delivery),
		TransportMode:    string(transport),
		Messages: []backendplugin.Message{{
			Role:  backendplugin.RoleUser,
			Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: &msg}},
		}},
		Options: backendplugin.GenerationOptions{ResponseSchemaJSON: backendplugin.RawJSONAbsentValue()},
	}
	return &memStream{
		ctx: ctx,
		inbox: []backendplugin.ClientFrame{
			{Kind: backendplugin.ClientFrameStart, InstanceID: "test-inst", Invocation: &inv},
			{Kind: backendplugin.ClientFrameCloseInput, InstanceID: "test-inst"},
		},
	}
}

func TestConfigure_MissingProjectOrLocation_FailsClosed(t *testing.T) {
	t.Parallel()
	svc := service.New(service.WithTokenProvider(service.StaticTokenProvider("test-tok")))

	// Missing project
	_, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-1",
		ConfigYAML:  []byte("location: us-central1\n"),
	})
	if err == nil || !strings.Contains(err.Error(), "project is required") {
		t.Fatalf("expected project is required error, got: %v", err)
	}

	// Invalid project with scheme/URL
	_, err = svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-1",
		ConfigYAML:  []byte("project: https://aiplatform.googleapis.com/v1/projects/my-proj\nlocation: us-central1\n"),
	})
	if err == nil || !strings.Contains(err.Error(), "invalid project name") {
		t.Fatalf("expected invalid project name error, got: %v", err)
	}

	// Missing location
	_, err = svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-1",
		ConfigYAML:  []byte("project: my-proj\n"),
	})
	if err == nil || !strings.Contains(err.Error(), "location is required") {
		t.Fatalf("expected location is required error, got: %v", err)
	}
}

func TestParseConfigYAML_RejectsLiteralSecrets(t *testing.T) {
	t.Parallel()

	secretCases := []string{
		"project: p\nlocation: us-central1\napi_key: secret\n",
		"project: p\nlocation: us-central1\nbearer_token: secret\n",
		"project: p\nlocation: us-central1\ntoken: secret\n",
		"project: p\nlocation: us-central1\nservice_account_json: \"{}\"\n",
		"project: p\nlocation: us-central1\nprivate_key: secret\n",
		"project: p\nlocation: us-central1\nclient_secret: secret\n",
		"project: p\nlocation: us-central1\nsecret: secret\n",
	}

	for _, yamlStr := range secretCases {
		_, err := service.ParseConfigYAML([]byte(yamlStr))
		if err == nil || !strings.Contains(err.Error(), "literal secrets in configuration YAML are forbidden") {
			t.Fatalf("expected literal secret rejection for %q, got %v", yamlStr, err)
		}
	}
}

func TestModelEndpoint_RegionalAndGlobalURL(t *testing.T) {
	t.Parallel()

	// Regional generateContent
	cfgRegional, err := service.ParseConfigYAML([]byte("project: my-project\nlocation: us-central1\n"))
	if err != nil {
		t.Fatal(err)
	}
	endpoint := cfgRegional.ModelEndpoint("gemini-2.5-flash", false)
	wantEndpoint := "https://us-central1-aiplatform.googleapis.com/v1/projects/my-project/locations/us-central1/publishers/google/models/gemini-2.5-flash:generateContent"
	if endpoint != wantEndpoint {
		t.Fatalf("regional generateContent got %q, want %q", endpoint, wantEndpoint)
	}

	// Regional streamGenerateContent
	streamEndpoint := cfgRegional.ModelEndpoint("gemini-2.5-flash", true)
	wantStreamEndpoint := "https://us-central1-aiplatform.googleapis.com/v1/projects/my-project/locations/us-central1/publishers/google/models/gemini-2.5-flash:streamGenerateContent?alt=sse"
	if streamEndpoint != wantStreamEndpoint {
		t.Fatalf("regional streamGenerateContent got %q, want %q", streamEndpoint, wantStreamEndpoint)
	}

	// Global location
	cfgGlobal, err := service.ParseConfigYAML([]byte("project: my-project\nlocation: global\n"))
	if err != nil {
		t.Fatal(err)
	}
	globalEndpoint := cfgGlobal.ModelEndpoint("gemini-2.5-flash", false)
	wantGlobalEndpoint := "https://aiplatform.googleapis.com/v1/projects/my-project/locations/global/publishers/google/models/gemini-2.5-flash:generateContent"
	if globalEndpoint != wantGlobalEndpoint {
		t.Fatalf("global generateContent got %q, want %q", globalEndpoint, wantGlobalEndpoint)
	}

	// Custom publisher
	cfgPublisher, err := service.ParseConfigYAML([]byte("project: my-project\nlocation: us-central1\npublisher: meta\n"))
	if err != nil {
		t.Fatal(err)
	}
	pubEndpoint := cfgPublisher.ModelEndpoint("llama-3.1-70b", false)
	wantPubEndpoint := "https://us-central1-aiplatform.googleapis.com/v1/projects/my-project/locations/us-central1/publishers/meta/models/llama-3.1-70b:generateContent"
	if pubEndpoint != wantPubEndpoint {
		t.Fatalf("custom publisher endpoint got %q, want %q", pubEndpoint, wantPubEndpoint)
	}

	// InventoryEndpoint: regional, global, custom publisher
	if inv := cfgRegional.InventoryEndpoint(); inv != "https://us-central1-aiplatform.googleapis.com/v1beta1/publishers/google/models" {
		t.Fatalf("regional inventory got %q, want %q", inv, "https://us-central1-aiplatform.googleapis.com/v1beta1/publishers/google/models")
	}
	if inv := cfgGlobal.InventoryEndpoint(); inv != "https://aiplatform.googleapis.com/v1beta1/publishers/google/models" {
		t.Fatalf("global inventory got %q, want %q", inv, "https://aiplatform.googleapis.com/v1beta1/publishers/google/models")
	}
	if inv := cfgPublisher.InventoryEndpoint(); inv != "https://us-central1-aiplatform.googleapis.com/v1beta1/publishers/meta/models" {
		t.Fatalf("custom publisher inventory got %q, want %q", inv, "https://us-central1-aiplatform.googleapis.com/v1beta1/publishers/meta/models")
	}
}

func TestAPIOrigin_PreservesPathSuffix(t *testing.T) {
	t.Parallel()

	cfg, err := service.ParseConfigYAML([]byte("project: my-project\nlocation: us-central1\napi_origin: http://127.0.0.1:8080/\n"))
	if err != nil {
		t.Fatal(err)
	}
	endpoint := cfg.ModelEndpoint("gemini-2.5-flash", false)
	want := "http://127.0.0.1:8080/v1/projects/my-project/locations/us-central1/publishers/google/models/gemini-2.5-flash:generateContent"
	if endpoint != want {
		t.Fatalf("api_origin endpoint got %q, want %q", endpoint, want)
	}

	invEndpoint := cfg.InventoryEndpoint()
	wantInv := "http://127.0.0.1:8080/v1beta1/publishers/google/models"
	if invEndpoint != wantInv {
		t.Fatalf("api_origin inventory endpoint got %q, want %q", invEndpoint, wantInv)
	}
}

func TestDescribe_Metadata(t *testing.T) {
	t.Parallel()

	svc := service.New()
	desc, err := svc.Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if desc.PluginID != service.PluginID {
		t.Fatalf("PluginID=%q want %q", desc.PluginID, service.PluginID)
	}
	if desc.ProtocolMajor != 1 || desc.ProtocolMinor != backendplugin.ProtocolMinorCancellationHandshake {
		t.Fatalf("Protocol=%d.%d want 1.%d", desc.ProtocolMajor, desc.ProtocolMinor, backendplugin.ProtocolMinorCancellationHandshake)
	}
	if len(desc.Factories) != 1 {
		t.Fatalf("expected 1 factory, got %d", len(desc.Factories))
	}
	fact := desc.Factories[0]
	if fact.Kind != service.FactoryKind {
		t.Fatalf("Factory kind=%q want %q", fact.Kind, service.FactoryKind)
	}
	if fact.DisplayName != service.DisplayName {
		t.Fatalf("DisplayName=%q want %q", fact.DisplayName, service.DisplayName)
	}
	if !fact.StaticCapabilities.Streaming {
		t.Fatal("expected Streaming capability advertised")
	}
	if fact.StaticCapabilities.Tools || fact.StaticCapabilities.Vision || fact.StaticCapabilities.StructuredOutputs {
		t.Fatal("tools/vision/structured-outputs must NOT be advertised")
	}
}

func TestConfiguredInstance_Execute_StreamingAndNonStreaming(t *testing.T) {
	t.Parallel()

	var gotAuthHeader atomic.Pointer[string]
	var gotPath atomic.Pointer[string]

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		gotAuthHeader.Store(&h)
		p := r.URL.Path + "?" + r.URL.RawQuery
		gotPath.Store(&p)

		if strings.HasSuffix(r.URL.Path, ":streamGenerateContent") {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher, ok := w.(http.Flusher)
			if !ok {
				t.Fatal("expected flusher")
			}
			chunk := `data: {"candidates": [{"content": {"parts": [{"text": "Hello stream!"}], "role": "model"}}], "usageMetadata": {"promptTokenCount": 10, "candidatesTokenCount": 5, "totalTokenCount": 15}}` + "\n\n"
			_, _ = w.Write([]byte(chunk))
			flusher.Flush()
			return
		}

		if strings.HasSuffix(r.URL.Path, ":generateContent") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			resp := `{"candidates": [{"content": {"parts": [{"text": "Hello non-stream!"}], "role": "model"}}], "usageMetadata": {"promptTokenCount": 8, "candidatesTokenCount": 4, "totalTokenCount": 12}}`
			_, _ = w.Write([]byte(resp))
			return
		}

		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	svc := service.New(service.WithTokenProvider(service.StaticTokenProvider("secret-token-12345")))
	cfgYAML := fmt.Sprintf("project: my-proj\nlocation: us-central1\napi_origin: %s\n", srv.URL)

	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-test",
		ConfigYAML:  []byte(cfgYAML),
	})
	if err != nil {
		t.Fatalf("Configure failed: %v", err)
	}

	// 1. Streaming test
	streamExec := newTestExecuteStream(context.Background(), "vertex/gemini-2.5-flash", aipapi.OperationGeminiGenerateContent, "hi", false)
	if err := inst.Execute(streamExec); err != nil {
		t.Fatalf("streaming Execute failed: %v", err)
	}

	if auth := gotAuthHeader.Load(); auth == nil || *auth != "Bearer secret-token-12345" {
		t.Fatalf("expected Authorization: Bearer secret-token-12345, got: %v", auth)
	}
	if path := gotPath.Load(); path == nil || !strings.Contains(*path, "/publishers/google/models/gemini-2.5-flash:streamGenerateContent?alt=sse") {
		t.Fatalf("expected streamGenerateContent path, got: %v", path)
	}

	var sawStart, sawMsg, sawText, sawUsage, sawFinished bool
	for _, frame := range streamExec.outbox {
		if frame.Kind == backendplugin.ServerFrameEvent && frame.Event != nil {
			switch frame.Event.Kind {
			case backendplugin.EventResponseStarted:
				sawStart = true
			case backendplugin.EventMessageStarted:
				sawMsg = true
			case backendplugin.EventTextDelta:
				if frame.Event.Delta != nil && *frame.Event.Delta == "Hello stream!" {
					sawText = true
				}
			case backendplugin.EventUsageDelta:
				if frame.Event.Usage != nil &&
					frame.Event.Usage.InputTokens != nil && *frame.Event.Usage.InputTokens == 10 &&
					frame.Event.Usage.OutputTokens != nil && *frame.Event.Usage.OutputTokens == 5 &&
					frame.Event.Usage.TotalTokens != nil && *frame.Event.Usage.TotalTokens == 15 {
					sawUsage = true
				}
			case backendplugin.EventResponseFinished:
				sawFinished = true
			}
		}
	}
	if !sawStart || !sawMsg || !sawText || !sawUsage || !sawFinished {
		t.Fatalf("missing expected stream events: sawStart=%v, sawMsg=%v, sawText=%v, sawUsage=%v, sawFinished=%v, frames=%+v",
			sawStart, sawMsg, sawText, sawUsage, sawFinished, streamExec.outbox)
	}

	// 2. Non-streaming test
	nonStreamExec := newTestExecuteStream(context.Background(), "vertex/gemini-2.5-flash", aipapi.OperationGeminiGenerateContent, "hi", true)
	if err := inst.Execute(nonStreamExec); err != nil {
		t.Fatalf("non-streaming Execute failed: %v", err)
	}
	if path := gotPath.Load(); path == nil || !strings.Contains(*path, "/publishers/google/models/gemini-2.5-flash:generateContent") {
		t.Fatalf("expected generateContent path, got: %v", path)
	}
}

func TestConfiguredInstance_Execute_DefaultModelVsExplicitModel(t *testing.T) {
	t.Parallel()

	var gotModel atomic.Pointer[string]

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		parts := strings.Split(p, "/models/")
		if len(parts) > 1 {
			m, _, _ := strings.Cut(parts[1], ":")
			gotModel.Store(&m)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates": [{"content": {"parts": [{"text": "ok"}], "role": "model"}}]}`))
	}))
	t.Cleanup(srv.Close)

	svc := service.New(service.WithTokenProvider(service.StaticTokenProvider("tok")))
	cfgYAML := fmt.Sprintf("project: my-proj\nlocation: us-central1\nmodel: gemini-2.5-flash\napi_origin: %s\n", srv.URL)
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-model",
		ConfigYAML:  []byte(cfgYAML),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Invocation with kind-only model "vertex" -> default "gemini-2.5-flash"
	stream1 := newTestExecuteStream(context.Background(), "vertex", aipapi.OperationGeminiGenerateContent, "hi", true)
	if err := inst.Execute(stream1); err != nil {
		t.Fatalf("Execute default model failed: %v", err)
	}
	if m := gotModel.Load(); m == nil || *m != "gemini-2.5-flash" {
		t.Fatalf("expected default model gemini-2.5-flash, got %v", m)
	}

	// Invocation with explicit model "vertex/gemini-1.5-pro" -> explicit "gemini-1.5-pro"
	stream2 := newTestExecuteStream(context.Background(), "vertex/gemini-1.5-pro", aipapi.OperationGeminiGenerateContent, "hi", true)
	if err := inst.Execute(stream2); err != nil {
		t.Fatalf("Execute explicit model failed: %v", err)
	}
	if m := gotModel.Load(); m == nil || *m != "gemini-1.5-pro" {
		t.Fatalf("expected explicit model gemini-1.5-pro, got %v", m)
	}
}

func TestConfiguredInstance_Execute_HardNegative404(t *testing.T) {
	t.Parallel()

	var openAICalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "chat/completions") {
			openAICalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices": [{"message": {"role": "assistant", "content": "openai"}}]}`))
			return
		}
		// Vertex endpoint 404
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	svc := service.New(service.WithTokenProvider(service.StaticTokenProvider("tok")))
	cfgYAML := fmt.Sprintf("project: my-proj\nlocation: us-central1\napi_origin: %s\n", srv.URL)
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-404",
		ConfigYAML:  []byte(cfgYAML),
	})
	if err != nil {
		t.Fatal(err)
	}

	stream := newTestExecuteStream(context.Background(), "vertex/gemini-2.5-flash", aipapi.OperationGeminiGenerateContent, "hi", false)
	err = inst.Execute(stream)
	if err == nil {
		t.Fatal("expected error on 404, got nil")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("expected 404 error, got: %v", err)
	}
	if openAICalls.Load() > 0 {
		t.Fatalf("connector retried OpenAI-compatible endpoint! calls=%d", openAICalls.Load())
	}
}

func TestConfiguredInstance_ListModels_FiltersNonCoding(t *testing.T) {
	t.Parallel()

	var gotPath atomic.Pointer[string]
	var gotAuth atomic.Pointer[string]

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		gotPath.Store(&p)
		a := r.Header.Get("Authorization")
		gotAuth.Store(&a)

		w.Header().Set("Content-Type", "application/json")
		resp := `{
			"publisherModels": [
				{"name": "publishers/google/models/gemini-2.5-flash", "displayName": "Gemini 2.5 Flash"},
				{"name": "publishers/google/models/gemini-1.5-pro", "displayName": "Gemini 1.5 Pro"},
				{"name": "publishers/google/models/text-embedding-004", "displayName": "Text Embedding 004"},
				{"name": "publishers/google/models/imagen-3.0-generate-001", "displayName": "Imagen 3"},
				{"name": "publishers/google/models/chirp-2", "displayName": "Chirp 2"}
			]
		}`
		_, _ = w.Write([]byte(resp))
	}))
	t.Cleanup(srv.Close)

	svc := service.New(service.WithTokenProvider(service.StaticTokenProvider("tok")))
	cfgYAML := fmt.Sprintf("project: my-proj\nlocation: us-central1\napi_origin: %s\n", srv.URL)
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-list",
		ConfigYAML:  []byte(cfgYAML),
	})
	if err != nil {
		t.Fatal(err)
	}

	listResp, err := inst.ListModels(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListModels failed: %v", err)
	}

	// 1. Prove request path matches documented Model Garden /v1beta1/publishers/{publisher}/models
	reqPath := gotPath.Load()
	if reqPath == nil || *reqPath != "/v1beta1/publishers/google/models" {
		t.Fatalf("expected path /v1beta1/publishers/google/models, got: %v", reqPath)
	}
	if strings.Contains(*reqPath, "/projects/") {
		t.Fatalf("inventory request path must NOT contain /projects/, got: %s", *reqPath)
	}
	if strings.Contains(*reqPath, "/locations/") {
		t.Fatalf("inventory request path must NOT contain /locations/, got: %s", *reqPath)
	}

	// 2. Prove Authorization Bearer header is sent
	authHeader := gotAuth.Load()
	if authHeader == nil || *authHeader != "Bearer tok" {
		t.Fatalf("expected Authorization: Bearer tok, got: %v", authHeader)
	}

	// 3. Prove coding models mapped to vertex/{id} and non-coding dropped
	wantModels := []string{"gemini-2.5-flash", "gemini-1.5-pro"}
	if len(listResp.Models) != len(wantModels) {
		var got []string
		for _, m := range listResp.Models {
			got = append(got, m.NativeModelID)
		}
		t.Fatalf("got %d models (%v), want %d (%v)", len(listResp.Models), got, len(wantModels), wantModels)
	}

	for i, want := range wantModels {
		m := listResp.Models[i]
		if m.NativeModelID != want {
			t.Fatalf("model %d NativeModelID=%q want %q", i, m.NativeModelID, want)
		}
		if m.CanonicalModelID != "vertex/"+want {
			t.Fatalf("model %d CanonicalModelID=%q want %q", i, m.CanonicalModelID, "vertex/"+want)
		}
		if m.FactoryKind != service.FactoryKind {
			t.Fatalf("model %d FactoryKind=%q want %q", i, m.FactoryKind, service.FactoryKind)
		}
	}

	// 4. Prove custom publisher uses /v1beta1/publishers/{publisher}/models
	cfgCustomYAML := fmt.Sprintf("project: my-proj\nlocation: us-central1\npublisher: meta\napi_origin: %s\n", srv.URL)
	instCustom, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-list-custom",
		ConfigYAML:  []byte(cfgCustomYAML),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = instCustom.ListModels(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListModels custom publisher failed: %v", err)
	}
	customPath := gotPath.Load()
	if customPath == nil || *customPath != "/v1beta1/publishers/meta/models" {
		t.Fatalf("expected custom publisher path /v1beta1/publishers/meta/models, got: %v", customPath)
	}
	if strings.Contains(*customPath, "/projects/") || strings.Contains(*customPath, "/locations/") {
		t.Fatalf("custom publisher inventory request path must NOT contain /projects/ or /locations/, got: %s", *customPath)
	}
}

func TestNewProduction_WiresGoogleCredentialChain(t *testing.T) {
	t.Parallel()

	svcProd := service.NewProduction()
	svcDefault := service.New()

	if svcProd == nil || svcDefault == nil {
		t.Fatal("expected non-nil services")
	}

	// 1. New() with no token provider fails Configure closed
	_, err := svcDefault.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-def",
		ConfigYAML:  []byte("project: my-proj\nlocation: us-central1\n"),
	})
	if err == nil || !strings.Contains(err.Error(), "NewProduction") {
		t.Fatalf("expected NewProduction guidance in configure error, got: %v", err)
	}

	// 2. Generate a valid dummy service account JSON with real RSA key
	pk, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pkBytes := x509.MarshalPKCS1PrivateKey(pk)
	pemBlock := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: pkBytes,
	}
	pemStr := string(pem.EncodeToMemory(pemBlock))

	saMap := map[string]string{
		"type":                        "service_account",
		"project_id":                  "my-proj",
		"private_key_id":              "key-id-123",
		"private_key":                 pemStr,
		"client_email":                "sa@my-proj.iam.gserviceaccount.com",
		"client_id":                   "1234567890",
		"auth_uri":                    "https://accounts.google.com/o/oauth2/auth",
		"token_uri":                   "https://oauth2.googleapis.com/token",
		"auth_provider_x509_cert_url": "https://www.googleapis.com/oauth2/v1/certs",
		"client_x509_cert_url":        "https://www.googleapis.com/robot/v1/metadata/x509/sa%40my-proj.iam.gserviceaccount.com",
	}
	saJSON, err := json.Marshal(saMap)
	if err != nil {
		t.Fatal(err)
	}

	// 3. NewProduction with service_account_json configures successfully
	instProd, err := svcProd.Configure(context.Background(), backendplugin.ConfigureRequest{
		FactoryKind: service.FactoryKind,
		InstanceID:  "inst-prod",
		ConfigYAML:  []byte("project: my-proj\nlocation: us-central1\n"),
		Secrets: backendplugin.SecretBundle{
			Values: map[string][]byte{"service_account_json": saJSON},
		},
	})
	if err != nil {
		t.Fatalf("NewProduction configure with service_account_json failed: %v", err)
	}
	if instProd == nil {
		t.Fatal("expected non-nil configured instance")
	}
}
